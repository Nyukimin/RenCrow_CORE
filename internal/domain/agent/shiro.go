package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const shiroMaxTokens = 4096

// ShiroAgent は Worker（実行・道具係）を担当するエンティティ
type ShiroAgent struct {
	llmProvider     llm.LLMProvider
	toolRunner      ToolRunner
	mcpClient       MCPClient
	systemPrompt    string
	subagentManager SubagentManager // v1.0: ReActループ統合
	advisorService  AdvisorService
	agentPolicy     AgentPolicyService
	persona         *AgentPersona // v4.2: Optional Agent Persona
	lightMemory     *LightMemory  // Optional: short-term memory
	conversation    conversation.ConversationEngine
}

// NewShiroAgent は新しいShiroAgentを作成
func NewShiroAgent(
	llmProvider llm.LLMProvider,
	toolRunner ToolRunner,
	mcpClient MCPClient,
	systemPrompt string,
	subagentManager SubagentManager,
) *ShiroAgent {
	return &ShiroAgent{
		llmProvider:     llmProvider,
		toolRunner:      toolRunner,
		mcpClient:       mcpClient,
		systemPrompt:    systemPrompt,
		subagentManager: subagentManager,
	}
}

// WithPersona は AgentPersona を設定する（Builder パターン）
func (s *ShiroAgent) WithPersona(persona AgentPersona) *ShiroAgent {
	s.persona = &persona
	return s
}

// WithLightMemory は LightMemory を設定する（Builder パターン）。
func (s *ShiroAgent) WithLightMemory(memory *LightMemory) *ShiroAgent {
	s.lightMemory = memory
	return s
}

func (s *ShiroAgent) WithConversationEngine(engine conversation.ConversationEngine) *ShiroAgent {
	s.conversation = engine
	return s
}

func (s *ShiroAgent) WithAdvisorService(service AdvisorService) *ShiroAgent {
	s.advisorService = service
	return s
}

func (s *ShiroAgent) WithAgentPolicyService(service AgentPolicyService) *ShiroAgent {
	s.agentPolicy = service
	return s
}

const advisorApprovalRequiredMessage = "Advisorの利用には人間の承認が必要です。自動実行は行いません。"

// Execute はWorkerタスクを実行
// v1.0: SubagentManager が設定されている場合は ReActLoop を使ってツールを自律的に選択・実行する
func (s *ShiroAgent) Execute(ctx context.Context, t task.Task) (string, error) {
	systemPrompt := s.systemPrompt
	if s.persona != nil {
		systemPrompt = s.persona.BuildSystemPrompt(s.systemPrompt)
	}
	systemPrompt = ensureShiroJapaneseResponsePrompt(systemPrompt)

	if resp, ok, err := s.tryExecuteCodexWorkPath(ctx, t); ok || err != nil {
		return resp, err
	}
	systemPrompt = llm.AppendNowJST(systemPrompt)

	// SubagentManager が設定されている場合は ReActLoop を使用
	if s.subagentManager != nil {
		result, err := s.runSubagentSafely(ctx, SubagentTask{
			AgentName:    "shiro",
			Instruction:  t.UserMessage(),
			SystemPrompt: systemPrompt,
		})
		if err != nil {
			return "", err
		}
		return result.Output, nil
	}

	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	if s.conversation != nil {
		recallPack, err := s.conversation.BeginTurn(ctx, t.ChatID(), t.UserMessage())
		if err != nil {
			log.Printf("[Shiro] BeginTurn failed: %v", err)
		} else if recallPack != nil {
			filtered := recallPack.FilterForRole("worker").WithoutPersonaSystemPrompt()
			if err := recordRecallTrace(ctx, s.conversation, t.ChatID(), t.JobID().String(), "worker", filtered); err != nil {
				log.Printf("[Shiro] RecordRecallTrace failed: %v", err)
			}
			messages = append(messages, filtered.ToPromptMessages()...)
		}
	}
	if s.lightMemory != nil {
		messages = append(messages, s.lightMemory.RecentMessages(t.ChatID())...)
	}
	messages = append(messages, userMessageWithAttachments(t.UserMessage(), t.Attachments()))
	req := llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   shiroMaxTokens,
		Temperature: 0.3, // Workerは確実性重視
	}

	resp, err := s.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	if s.lightMemory != nil {
		s.lightMemory.Record(t.ChatID(), t.UserMessage(), resp.Content)
	}

	if s.conversation != nil {
		if err := endConversationTurnAs(ctx, s.conversation, t.ChatID(), t.UserMessage(), resp.Content, conversation.SpeakerShiro); err != nil {
			log.Printf("[Shiro] EndTurn failed: %v", err)
		}
	}
	return resp.Content, nil
}

func (s *ShiroAgent) tryExecuteCodexWorkPath(ctx context.Context, t task.Task) (string, bool, error) {
	path := routing.DetectCodexWorkPath(t.UserMessage())
	if !path.Found() {
		return "", false, nil
	}
	if s.agentPolicy != nil {
		decision, err := s.agentPolicy.Decide("shiro", "ask_advisor")
		if err != nil {
			log.Printf("[Shiro] ask_advisor policy failed; using normal worker path: %v", err)
			return "", false, nil
		}
		switch decision.Decision {
		case "forbidden":
			return "", false, nil
		case "approval_required":
			return advisorApprovalRequiredMessage, true, nil
		case "allowed":
		default:
			log.Printf("[Shiro] ask_advisor policy returned unsupported decision %q; using normal worker path", decision.Decision)
			return "", false, nil
		}
	}
	if s.advisorService != nil {
		return s.requestCodexAdvice(ctx, path, t)
	}
	if s.toolRunner == nil || !s.hasTool(ctx, "codex.run") {
		return "", false, nil
	}

	resp, err := s.toolRunner.ExecuteV2(ctx, "codex.run", map[string]any{
		"prompt":  buildCodexWorkPrompt(path, t.UserMessage()),
		"sandbox": "read-only",
	})
	if err != nil {
		return "", true, err
	}
	if resp == nil {
		return "", true, fmt.Errorf("codex.run returned nil response")
	}
	if resp.IsError() {
		return "", true, fmt.Errorf("%s", resp.Error.Message)
	}
	return resp.String(), true, nil
}

func (s *ShiroAgent) requestCodexAdvice(ctx context.Context, path routing.CodexWorkPath, t task.Task) (string, bool, error) {
	result, err := s.advisorService.RequestAdvice(ctx, advisor.AdviceRequest{
		ID:               t.JobID().String(),
		TaskID:           t.JobID().String(),
		RequestedByAgent: "shiro",
		AdvisorID:        advisor.AdvisorCodex,
		Purpose:          "codex_work_path:" + string(path.Domain),
		Prompt:           buildCodexWorkPrompt(path, t.UserMessage()),
		RiskClass:        "low",
		ApprovalMode:     "advice_only",
	})
	if err != nil {
		return "", true, err
	}
	if result.Status != advisor.StatusCompleted {
		return "", true, fmt.Errorf("advisor %s returned status %s", result.AdvisorID, result.Status)
	}
	output := result.OutputText()
	if output == "" {
		return "", true, fmt.Errorf("advisor %s returned empty advice", result.AdvisorID)
	}
	return output, true, nil
}

func (s *ShiroAgent) hasTool(ctx context.Context, toolID string) bool {
	if s.toolRunner == nil {
		return false
	}
	metas, err := s.toolRunner.ListTools(ctx)
	if err != nil {
		return false
	}
	for _, meta := range metas {
		if meta.ToolID == toolID {
			return true
		}
	}
	return false
}

func buildCodexWorkPrompt(path routing.CodexWorkPath, userMessage string) string {
	var role string
	switch path.Domain {
	case routing.CodexWorkDomainDrawing:
		role = "描画領域。依頼内容から、画像生成や人間の描画作業にそのまま使える具体的な描画仕様または画像プロンプトを日本語で作成する。"
	case routing.CodexWorkDomainFolktale:
		role = "昔話生成領域。依頼内容に沿って、昔話として読める完成文または生成方針を自然な日本語で作成する。"
	default:
		role = "Codexの明示業務領域。依頼内容に沿って自然な日本語で成果物を返す。"
	}
	return strings.Join([]string{
		"RenCrow Codex PATH",
		role,
		"この実行ではリポジトリの変更、コマンド実行、外部アクセスを前提にしない。",
		"成果物だけを簡潔に返し、実装済みや保存済みのような未確認の主張はしない。",
		"",
		"ユーザー依頼:",
		userMessage,
	}, "\n")
}

func (s *ShiroAgent) runSubagentSafely(ctx context.Context, t SubagentTask) (res SubagentResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("subagent runtime panic: %v", r)
		}
	}()
	res, err = s.subagentManager.RunSync(ctx, t)
	return res, err
}

func ensureShiroJapaneseResponsePrompt(systemPrompt string) string {
	const guard = "出力言語の絶対ルール: Shiroは必ず自然な日本語で応答する。英語での説明、英語の見出し、英語だけの完了報告は禁止する。ユーザーが英語を求めた場合だけ、必要最小限の英語を併記してよい。"
	if strings.Contains(systemPrompt, "必ず自然な日本語で応答") {
		return systemPrompt
	}
	if systemPrompt == "" {
		return guard
	}
	return systemPrompt + "\n\n" + guard
}

// ExecuteTool はツールを実行
func (s *ShiroAgent) ExecuteTool(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	resp, err := s.toolRunner.ExecuteV2(ctx, toolName, args)
	if err != nil {
		return "", err
	}
	if resp.IsError() {
		return "", fmt.Errorf("%s", resp.Error.Message)
	}
	return resp.String(), nil
}

// ExecuteMCPTool はMCPツールを実行
func (s *ShiroAgent) ExecuteMCPTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error) {
	return s.mcpClient.CallTool(ctx, serverName, toolName, args)
}
