package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	researchcontract "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

// WebResearcher gathers evidence through the caller's own provider and the
// policy-owned query runner. A nil allowlist means the public query surface;
// a non-nil list is the exact capability grant of a private Task.
type WebResearcher interface {
	Gather(context.Context, llm.LLMProvider, llm.GenerateRequest, []string) ([]llm.Message, error)
}

type webResearchProvider struct {
	inner    llm.LLMProvider
	research WebResearcher
	allowed  []string
}

// WebResearchRunResult keeps the result fields required to explain an
// incomplete Research run while RawResult retains the exact typed payload.
type WebResearchRunResult struct {
	SchemaVersion  string                       `json:"schema_version"`
	RunID          string                       `json:"run_id"`
	TaskID         string                       `json:"task_id"`
	ExecutionRunID string                       `json:"execution_run_id"`
	Status         string                       `json:"status"`
	Missing        []string                     `json:"missing"`
	Counts         researchcontract.Counts      `json:"counts"`
	Artifacts      []WebResearchArtifactRef     `json:"artifacts"`
	Candidates     []researchcontract.Candidate `json:"candidates"`
	Report         string                       `json:"report"`
	RawResult      json.RawMessage              `json:"-"`
}

// WebResearchArtifactRef identifies an artifact persisted by the Research Engine.
type WebResearchArtifactRef struct {
	Kind string `json:"kind"`
	Hash string `json:"hash"`
	Ref  string `json:"ref"`
}

// WebResearchIncompleteError prevents a non-success Research run from being
// presented as a completed Agent answer while preserving its typed receipt.
type WebResearchIncompleteError struct {
	Result WebResearchRunResult
}

func (e *WebResearchIncompleteError) Error() string {
	artifactRefs := make([]string, 0, len(e.Result.Artifacts))
	for _, artifact := range e.Result.Artifacts {
		artifactRefs = append(artifactRefs, artifact.Ref)
	}
	return fmt.Sprintf("research run did not succeed: status=%q run_id=%q missing=%q artifacts=%q", e.Result.Status, e.Result.RunID, e.Result.Missing, artifactRefs)
}

func withWebResearch(provider llm.LLMProvider, research WebResearcher) llm.LLMProvider {
	if provider == nil || research == nil {
		return provider
	}
	if current, ok := provider.(*webResearchProvider); ok {
		provider = current.inner
	}
	return &webResearchProvider{inner: provider, research: research}
}

func (p *webResearchProvider) Name() string { return p.inner.Name() }

func (p *webResearchProvider) Generate(ctx context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	// Mio's logical outcome continuation already has a fenced retrieval owner.
	// Do not create a second acquisition or consume its one-shot admission.
	if request.LogicalOutcomeStream != "" || !webResearchRequested(request.Messages) {
		return p.inner.Generate(ctx, request)
	}
	evidence, err := p.research.Gather(ctx, p.inner, request, p.allowed)
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("web research: %w", err)
	}
	if len(evidence) == 0 {
		return llm.GenerateResponse{}, fmt.Errorf("web research returned no tool evidence")
	}
	if err := webResearchOutcomeError(evidence); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("web research: %w", err)
	}
	if typedRequest := webResearchTypedRequest(request.Messages); typedRequest != "" && !webResearchHasStructuredOutputContract(request) {
		response, err := webResearchCompletionResponse(request, evidence, typedRequest)
		if err != nil {
			return llm.GenerateResponse{}, fmt.Errorf("web research: %w", err)
		}
		return response, nil
	}
	// Preserve the original request, callbacks, output schema and typed prefix.
	// Retrieved material is variable evidence, never a system instruction.
	messages := make([]llm.Message, 0, len(request.Messages)+len(evidence))
	inserted := false
	for _, message := range request.Messages {
		if !inserted && message.Role == "user" && message.Type != llm.PromptContextRecall {
			messages = append(messages, evidence...)
			inserted = true
		}
		messages = append(messages, message)
	}
	if !inserted {
		messages = append(messages, evidence...)
	}
	request.Messages = messages
	return p.inner.Generate(ctx, request)
}

func webResearchTypedRequest(messages []llm.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") || message.Type == llm.PromptContextRecall || message.Type == llm.PromptContextVariable {
			continue
		}
		raw := strings.TrimSpace(message.Content)
		if strings.HasPrefix(raw, "```json\n") && strings.HasSuffix(raw, "\n```") {
			raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```json\n"), "\n```")
		}
		var request struct {
			SchemaVersion string `json:"schema_version"`
			Mode          string `json:"mode"`
		}
		if json.Unmarshal([]byte(raw), &request) == nil && request.SchemaVersion == researchcontract.RequestSchema {
			return strings.ToLower(strings.TrimSpace(request.Mode))
		}
		return ""
	}
	return ""
}

func webResearchHasStructuredOutputContract(request llm.GenerateRequest) bool {
	if strings.TrimSpace(request.SystemPrompt) != "" || request.ResponseFormat != llm.ResponseFormatText || request.LogicalOutcomeStream != "" || request.OnLogicalOutcomeEvent != nil {
		return true
	}
	for _, message := range request.Messages {
		if message.Metadata[coderPromptOwnerMetadataKey] == coderPromptOwnerContract {
			return true
		}
	}
	return false
}

func webResearchCompletionResponse(request llm.GenerateRequest, evidence []llm.Message, requestMode string) (llm.GenerateResponse, error) {
	for _, message := range evidence {
		if message.Metadata["runtime_context_kind"] != "web_research_evidence" {
			continue
		}
		if message.Metadata["research_request_schema"] != researchcontract.RequestSchema ||
			message.Metadata["research_mode"] != requestMode || message.Metadata["research_task_summary"] == "" {
			return llm.GenerateResponse{}, fmt.Errorf("recorded Research Task completion facts are unavailable")
		}
		var result WebResearchRunResult
		if err := json.Unmarshal([]byte(message.Content), &result); err != nil || result.SchemaVersion != researchcontract.ResultSchema || result.RunID == "" || result.Status != "succeeded" {
			return llm.GenerateResponse{}, fmt.Errorf("Research completion result is invalid")
		}
		result.RawResult = append(json.RawMessage(nil), []byte(message.Content)...)
		content, err := formatWebResearchCompletion(message.Metadata["research_task_summary"], requestMode, result)
		if err != nil {
			return llm.GenerateResponse{}, err
		}
		if request.OnToken != nil {
			request.OnToken(content)
		}
		return llm.GenerateResponse{Content: content, FinishReason: "stop"}, nil
	}
	return llm.GenerateResponse{}, fmt.Errorf("recorded Research Task completion facts are unavailable")
}

func formatWebResearchCompletion(taskSummary, mode string, result WebResearchRunResult) (string, error) {
	const maxReportedResearchRefs = 100
	const maxResearchRefLength = 4096
	switch mode {
	case "search_only", "collect", "investigate", "refresh":
	default:
		return "", fmt.Errorf("Research completion mode is invalid")
	}
	var builder strings.Builder
	if mode == "investigate" && result.Report != "" {
		builder.WriteString(result.Report)
		builder.WriteString("\n\n")
	}
	if mode == "search_only" {
		if len(result.Candidates) > maxReportedResearchRefs || len(result.Candidates) != result.Counts.Candidates {
			return "", fmt.Errorf("Research search result exceeds the completion display bound")
		}
		if len(result.Candidates) > 0 {
			builder.WriteString("Search candidates:\n")
			for _, candidate := range result.Candidates {
				if candidate.URL != "" && (len(candidate.URL) > maxResearchRefLength || strings.TrimSpace(candidate.URL) != candidate.URL) {
					return "", fmt.Errorf("Research candidate reference is invalid")
				}
				if candidate.StableID == "" && candidate.URL == "" {
					return "", fmt.Errorf("Research candidate has no stable reference")
				}
				builder.WriteString("- ")
				if candidate.URL != "" {
					builder.WriteString(strconv.Quote(candidate.URL))
				}
				if candidate.StableID != "" {
					if len(candidate.StableID) > maxResearchRefLength || strings.TrimSpace(candidate.StableID) != candidate.StableID {
						return "", fmt.Errorf("Research candidate stable reference is invalid")
					}
					builder.WriteString(" stable_item_id=")
					builder.WriteString(strconv.Quote(candidate.StableID))
				}
				builder.WriteByte('\n')
			}
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("Completion facts:\n")
	builder.WriteString(taskSummary)
	reportRefs := 0
	if mode == "investigate" {
		for _, artifact := range result.Artifacts {
			if artifact.Kind != "report" {
				continue
			}
			if len(artifact.Hash) != 64 || len(artifact.Ref) == 0 || len(artifact.Ref) > maxResearchRefLength ||
				strings.TrimSpace(artifact.Ref) != artifact.Ref || !researchSHA256Hex(artifact.Hash) ||
				(strings.HasPrefix(artifact.Ref, "sha256:") && artifact.Ref != "sha256:"+artifact.Hash) {
				return "", fmt.Errorf("Research report artifact reference is invalid")
			}
			reportRefs++
			if reportRefs > maxReportedResearchRefs {
				return "", fmt.Errorf("Research report artifact references exceed the completion display bound")
			}
			if reportRefs == 1 {
				builder.WriteString("\nReport artifacts:\n")
			}
			builder.WriteString("- ref=")
			builder.WriteString(strconv.Quote(artifact.Ref))
			builder.WriteString(" sha256=")
			builder.WriteString(artifact.Hash)
			builder.WriteByte('\n')
		}
	}
	return strings.TrimSpace(builder.String()), nil
}

func researchSHA256Hex(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func webResearchOutcomeError(evidence []llm.Message) error {
	for _, message := range evidence {
		if message.Metadata["runtime_context_kind"] != "web_research_evidence" {
			continue
		}
		status, directEngineResult := message.Metadata["research_status"]
		var result WebResearchRunResult
		if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
			if directEngineResult {
				return fmt.Errorf("research.run returned an invalid typed result")
			}
			continue
		}
		if result.SchemaVersion == "" && !directEngineResult {
			continue
		}
		if result.SchemaVersion != researchcontract.ResultSchema || result.RunID == "" || result.Status == "" || directEngineResult && result.Status != status || result.Status == "succeeded" && len(result.Missing) > 0 {
			return fmt.Errorf("research.run returned an invalid typed result")
		}
		result.RawResult = append(json.RawMessage(nil), []byte(message.Content)...)
		if result.Status != "succeeded" {
			return &WebResearchIncompleteError{Result: result}
		}
	}
	return nil
}

func webResearchRequested(messages []llm.Message) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "user" || message.Type == llm.PromptContextRecall {
			continue
		}
		text := strings.TrimSpace(message.Content)
		if looksLikeUserMemoryRecallQuestion(text) {
			return false
		}
		if needsWebSearch(text) {
			return true
		}
		lower := strings.ToLower(text)
		for _, marker := range []string{"https://", "http://", "リサーチ", "調査", "取得", "search", "fetch", "look up"} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
		return false
	}
	return false
}

func hasWebResearch(provider llm.LLMProvider) bool {
	_, ok := provider.(*webResearchProvider)
	return ok
}

func privateWebResearch(provider llm.LLMProvider, capabilities map[string]struct{}) llm.LLMProvider {
	current, ok := provider.(*webResearchProvider)
	if !ok {
		return provider
	}
	private := *current
	private.allowed = make([]string, 0, len(capabilities))
	for capability := range capabilities {
		private.allowed = append(private.allowed, capability)
	}
	return &private
}

func (m *MioAgent) WithWebResearch(research WebResearcher) *MioAgent {
	m.llmProvider = withWebResearch(m.llmProvider, research)
	return m
}

func (s *ShiroAgent) WithWebResearch(research WebResearcher) *ShiroAgent {
	s.llmProvider = withWebResearch(s.llmProvider, research)
	return s
}

func (h *HeavyAgent) WithWebResearch(research WebResearcher) *HeavyAgent {
	h.llmProvider = withWebResearch(h.llmProvider, research)
	return h
}

func (w *WildAgent) WithWebResearch(research WebResearcher) *WildAgent {
	w.llmProvider = withWebResearch(w.llmProvider, research)
	return w
}

func (c *CoderAgent) WithWebResearch(research WebResearcher) *CoderAgent {
	c.llmProvider = withWebResearch(c.llmProvider, research)
	return c
}
