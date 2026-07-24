package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const (
	ProposalFailureEmpty             = "proposal_empty"
	ProposalFailureMissingPlan       = "proposal_missing_plan"
	ProposalFailureMissingPatch      = "proposal_missing_patch"
	ProposalFailureInvalidPatch      = "proposal_invalid_patch"
	ProposalFailureDisallowedCommand = "proposal_disallowed_command"
)

// ProposalError represents a classified coder proposal failure.
type ProposalError struct {
	Kind      string
	Reason    string
	Retryable bool
}

func (e *ProposalError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "proposal generation failed"
	}
	if strings.TrimSpace(e.Kind) == "" {
		return reason
	}
	return e.Kind + ": " + reason
}

func newProposalError(kind, reason string, retryable bool) error {
	return &ProposalError{Kind: kind, Reason: reason, Retryable: retryable}
}

// ProposalFailureInfo extracts classified proposal failure metadata from err.
func ProposalFailureInfo(err error) (kind, reason string, retryable bool, ok bool) {
	var proposalErr *ProposalError
	if errors.As(err, &proposalErr) {
		return proposalErr.Kind, proposalErr.Reason, proposalErr.Retryable, true
	}
	return "", "", false, false
}

// CoderAgent は Coder（設計・実装）を担当するエンティティ
type CoderAgent struct {
	llmProvider    llm.LLMProvider
	toolRunner     ToolRunner
	mcpClient      MCPClient
	proposalPrompt string
	persona        *AgentPersona // Optional: v4.1 Agent Persona
	lightMemory    *LightMemory  // Optional: v4.1 Short-term memory
}

// NewCoderAgent は新しいCoderAgentを作成
func NewCoderAgent(
	llmProvider llm.LLMProvider,
	toolRunner ToolRunner,
	mcpClient MCPClient,
	proposalPrompt string,
) *CoderAgent {
	return &CoderAgent{
		llmProvider:    llmProvider,
		toolRunner:     toolRunner,
		mcpClient:      mcpClient,
		proposalPrompt: proposalPrompt,
	}
}

// WithPersona は AgentPersona を設定する（Builder パターン）
func (c *CoderAgent) WithPersona(persona AgentPersona) *CoderAgent {
	c.persona = &persona
	return c
}

// WithLightMemory は LightMemory を設定する（Builder パターン）
func (c *CoderAgent) WithLightMemory(memory *LightMemory) *CoderAgent {
	c.lightMemory = memory
	return c
}

// GenerateProposal はplan/patchを生成
func (c *CoderAgent) GenerateProposal(ctx context.Context, t task.Task) (*proposal.Proposal, error) {
	log.Printf("[CoderAgent] proposal generate start provider=%s job=%s prompt_len=%d", c.llmProvider.Name(), t.JobID().String(), len(t.UserMessage()))

	// システムプロンプトの構築（v4.1: Agent Persona 対応）
	systemPrompt := c.proposalPrompt
	if c.persona != nil {
		systemPrompt = c.persona.BuildSystemPrompt(c.proposalPrompt)
	}

	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	if c.lightMemory != nil {
		messages = append(messages, c.lightMemory.RecentMessages(t.ChatID())...)
	}
	messages = append(messages, userMessageWithAttachments(t.UserMessage(), t.Attachments()))
	req := llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   8192,
		Temperature: 0.5,
	})

	resp, err := c.llmProvider.Generate(ctx, req)
	if err != nil {
		log.Printf("[CoderAgent] proposal generate error provider=%s job=%s err=%v", c.llmProvider.Name(), t.JobID().String(), err)
		return nil, err
	}
	log.Printf("[CoderAgent] proposal generate response provider=%s job=%s content_len=%d finish=%s", c.llmProvider.Name(), t.JobID().String(), len(resp.Content), resp.FinishReason)

	// レスポンスからProposalを抽出
	p, err := c.extractProposal(resp.Content)
	if err != nil {
		log.Printf("[CoderAgent] proposal extract failed provider=%s job=%s err=%v", c.llmProvider.Name(), t.JobID().String(), err)
		return nil, err
	}
	if err := c.selfCheckProposal(p); err != nil {
		log.Printf("[CoderAgent] proposal self-check failed provider=%s job=%s err=%v", c.llmProvider.Name(), t.JobID().String(), err)
		return nil, err
	}
	if c.lightMemory != nil {
		c.lightMemory.Record(t.ChatID(), t.UserMessage(), resp.Content)
	}
	log.Printf("[CoderAgent] proposal extract complete provider=%s job=%s plan_len=%d patch_len=%d", c.llmProvider.Name(), t.JobID().String(), len(p.Plan()), len(p.Patch()))
	return p, nil
}

// GenerateWithContext は会話履歴を渡して LLM 応答を生成する（CoderLoop 多ターン用）
func (c *CoderAgent) GenerateWithContext(ctx context.Context, messages []llm.Message) (string, error) {
	req := llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   8192,
		Temperature: 0.5,
	})
	resp, err := c.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// GenerateWithPrompt は指定されたシステムプロンプトでLLM応答を生成
func (c *CoderAgent) GenerateWithPrompt(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
	// システムプロンプトの構築（v4.1: Agent Persona 対応）
	finalSystemPrompt := systemPrompt
	if c.persona != nil {
		finalSystemPrompt = c.persona.BuildSystemPrompt(systemPrompt)
	}

	messages := []llm.Message{{Role: "system", Content: finalSystemPrompt}}
	if c.lightMemory != nil {
		messages = append(messages, c.lightMemory.RecentMessages(t.ChatID())...)
	}
	messages = append(messages, userMessageWithAttachments(t.UserMessage(), t.Attachments()))
	req := llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   8192,
		Temperature: 0.5,
	})

	resp, err := c.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	if c.lightMemory != nil {
		c.lightMemory.Record(t.ChatID(), t.UserMessage(), resp.Content)
	}

	return resp.Content, nil
}

// extractProposal はLLM応答からProposalを抽出
func (c *CoderAgent) extractProposal(content string) (*proposal.Proposal, error) {
	if strings.TrimSpace(content) == "" {
		return nil, newProposalError(ProposalFailureEmpty, "empty LLM response", true)
	}
	plan := c.extractNamedSection(content, "plan", "implementation plan")
	patch := normalizeProposalPatch(c.extractNamedSection(content, "patch", "changes", "commands"))
	risk := c.extractNamedSection(content, "risk", "risks")
	costHint := c.extractNamedSection(content, "costhint", "cost hint", "cost", "effort")

	if strings.TrimSpace(patch) == "" {
		if fallbackPatch := normalizeProposalPatch(c.extractWholeContentPatch(content)); strings.TrimSpace(fallbackPatch) != "" {
			patch = fallbackPatch
		}
	}
	if strings.TrimSpace(plan) == "" && strings.TrimSpace(patch) != "" {
		plan = synthesizePlanFromPatch(patch)
	}

	switch {
	case strings.TrimSpace(plan) == "" && strings.TrimSpace(patch) == "":
		return nil, newProposalError(ProposalFailureEmpty, "missing Plan and Patch sections", true)
	case strings.TrimSpace(plan) == "":
		return nil, newProposalError(ProposalFailureMissingPlan, "proposal missing Plan section", true)
	case strings.TrimSpace(patch) == "":
		return nil, newProposalError(ProposalFailureMissingPatch, "proposal missing Patch section", true)
	}

	return proposal.NewProposal(plan, patch, risk, costHint), nil
}

func normalizeProposalPatch(patch string) string {
	trimmed := strings.TrimSpace(patch)
	if trimmed == "" {
		return ""
	}

	if unwrapped, ok := unwrapSingleFence(trimmed); ok {
		trimmed = strings.TrimSpace(unwrapped)
	}

	return trimmed
}

func unwrapSingleFence(content string) (string, bool) {
	if !strings.HasPrefix(content, "```") || !strings.HasSuffix(content, "```") {
		return "", false
	}

	firstNL := strings.IndexByte(content, '\n')
	if firstNL == -1 {
		return "", false
	}

	header := strings.TrimSpace(content[3:firstNL])
	body := strings.TrimSpace(content[firstNL+1 : len(content)-3])
	if body == "" {
		return "", false
	}

	switch header {
	case "json", "markdown", "md", "":
		return body, true
	default:
		return "", false
	}
}

// extractSection はコンテンツからセクションを抽出
func (c *CoderAgent) extractSection(content, startMarker, endMarker string) string {
	startIdx := strings.Index(content, startMarker)
	if startIdx == -1 {
		return ""
	}

	// セクション開始位置（マーカーの次の行）
	startIdx += len(startMarker)
	if startIdx >= len(content) {
		return ""
	}

	// 次のセクションマーカーを探す
	remaining := content[startIdx:]
	endIdx := strings.Index(remaining, endMarker)
	if endIdx == -1 {
		// 次のセクションがない場合は末尾まで
		return strings.TrimSpace(remaining)
	}

	return strings.TrimSpace(remaining[:endIdx])
}

func (c *CoderAgent) extractNamedSection(content string, names ...string) string {
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if !isHeadingLine(line) {
			continue
		}
		name := normalizeHeadingName(line)
		for _, candidate := range names {
			if name == normalizeLookupName(candidate) {
				start = i + 1
				break
			}
		}
		if start != -1 {
			break
		}
	}
	if start == -1 {
		return ""
	}

	end := len(lines)
	for i := start; i < len(lines); i++ {
		if isHeadingLine(lines[i]) {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

func isHeadingLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "#")
}

var headingPrefixRE = regexp.MustCompile(`^#+\s*`)

func normalizeHeadingName(line string) string {
	trimmed := strings.TrimSpace(line)
	trimmed = headingPrefixRE.ReplaceAllString(trimmed, "")
	return normalizeLookupName(trimmed)
}

func normalizeLookupName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer(" ", "", "-", "", "_", "", ":", "", "：", "")
	return replacer.Replace(s)
}

func (c *CoderAgent) extractWholeContentPatch(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	candidate := trimmed
	if unwrapped, ok := unwrapSingleFence(candidate); ok {
		candidate = strings.TrimSpace(unwrapped)
	}
	if _, err := patch.ParsePatch(candidate); err == nil {
		return candidate
	}
	return ""
}

func synthesizePlanFromPatch(patchText string) string {
	commands, err := patch.ParsePatch(patchText)
	if err != nil || len(commands) == 0 {
		return "- Inspect the runnable patch contents.\n- Apply only the commands that pass Worker validation."
	}
	lines := make([]string, 0, len(commands))
	for _, cmd := range commands {
		target := strings.TrimSpace(cmd.Target)
		if target == "" {
			target = "(no target)"
		}
		switch cmd.Type {
		case patch.TypeFileEdit:
			lines = append(lines, fmt.Sprintf("- %s file: %s", cmd.Action, target))
		case patch.TypeShellCommand:
			lines = append(lines, fmt.Sprintf("- run verification command: %s", target))
		case patch.TypeGitOperation:
			lines = append(lines, fmt.Sprintf("- run git operation: %s", target))
		default:
			lines = append(lines, fmt.Sprintf("- %s %s: %s", cmd.Action, cmd.Type, target))
		}
	}
	return strings.Join(lines, "\n")
}

func (c *CoderAgent) selfCheckProposal(p *proposal.Proposal) error {
	if p == nil {
		return fmt.Errorf("proposal is nil")
	}
	patchText := strings.TrimSpace(p.Patch())
	if patchText == "" {
		return newProposalError(ProposalFailureMissingPatch, "proposal patch is empty", true)
	}
	commands, err := patch.ParsePatch(patchText)
	if err != nil {
		return newProposalError(ProposalFailureInvalidPatch, fmt.Sprintf("proposal patch is not runnable: %v", err), true)
	}
	if hasBarePipCommand(patchText, commands) {
		return newProposalError(ProposalFailureDisallowedCommand, "bare pip command is not allowed", false)
	}
	return nil
}

func hasBarePipCommand(patchText string, commands []patch.PatchCommand) bool {
	for _, line := range strings.Split(patchText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "pip" || strings.HasPrefix(trimmed, "pip ") {
			return true
		}
	}
	for _, cmd := range commands {
		target := strings.TrimSpace(cmd.Target)
		if target == "pip" || strings.HasPrefix(target, "pip ") {
			return true
		}
	}
	return false
}
