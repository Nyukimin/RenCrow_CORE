package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	SchemaVersion string                   `json:"schema_version"`
	RunID         string                   `json:"run_id"`
	Status        string                   `json:"status"`
	Missing       []string                 `json:"missing"`
	Artifacts     []WebResearchArtifactRef `json:"artifacts"`
	RawResult     json.RawMessage          `json:"-"`
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
