package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type webResearchStub struct {
	calls    int
	allowed  []string
	err      error
	evidence []llm.Message
}

func (r *webResearchStub) Gather(_ context.Context, _ llm.LLMProvider, _ llm.GenerateRequest, allowed []string) ([]llm.Message, error) {
	r.calls++
	r.allowed = allowed
	if r.err != nil {
		return nil, r.err
	}
	if r.evidence != nil {
		return r.evidence, nil
	}
	return []llm.Message{{Role: "user", Type: llm.PromptContextVariable, Content: "actual Web evidence", Metadata: map[string]string{"runtime_context_kind": "web_research_evidence"}}}, nil
}

func TestWebResearchNonSuccessResultStopsFinalGenerationAndPreservesRunOutcome(t *testing.T) {
	for _, status := range []string{"partial", "failed", "waiting_input"} {
		t.Run(status, func(t *testing.T) {
			result := map[string]any{
				"schema_version": "rencrow.research.result.v1", "run_id": "run_123", "status": status,
				"missing":   []string{"manual_seed"},
				"artifacts": []map[string]string{{"kind": "collection", "hash": "sha256:abc", "ref": "artifact:123"}},
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			research := &webResearchStub{evidence: []llm.Message{{
				Role: "user", Type: llm.PromptContextVariable, Content: string(data),
				Metadata: map[string]string{"runtime_context_kind": "web_research_evidence", "research_status": status},
			}}}
			innerCalls := 0
			provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
				innerCalls++
				return llm.GenerateResponse{Content: "answer must not be generated"}, nil
			}}
			_, err = withWebResearch(provider, research).Generate(context.Background(), llm.GenerateRequest{
				Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: "調べて"}},
			})
			var outcome *WebResearchIncompleteError
			if !errors.As(err, &outcome) || innerCalls != 0 {
				t.Fatalf("non-success research result did not stop final generation with typed error: inner=%d err=%v", innerCalls, err)
			}
			if outcome.Result.Status != status || outcome.Result.RunID != "run_123" || len(outcome.Result.Missing) != 1 || outcome.Result.Missing[0] != "manual_seed" || len(outcome.Result.Artifacts) != 1 || outcome.Result.Artifacts[0].Ref != "artifact:123" || string(outcome.Result.RawResult) != string(data) {
				t.Fatalf("typed non-success outcome lost fields: %+v", outcome.Result)
			}
			for _, retained := range []string{"run_123", "manual_seed", "artifact:123"} {
				if !strings.Contains(err.Error(), retained) {
					t.Errorf("Task-visible error lost %q: %v", retained, err)
				}
			}
		})
	}
}

func TestWebResearchTypedResultStatusIsFailClosedWithoutMetadata(t *testing.T) {
	content := `{"schema_version":"rencrow.research.result.v1","run_id":"run_456","status":"waiting_input","missing":["manual_seed"],"artifacts":[]}`
	research := &webResearchStub{evidence: []llm.Message{{
		Role: "user", Type: llm.PromptContextVariable, Content: content,
		Metadata: map[string]string{"runtime_context_kind": "web_research_evidence"},
	}}}
	innerCalls := 0
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		innerCalls++
		return llm.GenerateResponse{Content: "must not run"}, nil
	}}
	_, err := withWebResearch(provider, research).Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: "調べて"}},
	})
	var outcome *WebResearchIncompleteError
	if !errors.As(err, &outcome) || innerCalls != 0 || outcome.Result.RunID != "run_456" || outcome.Result.Status != "waiting_input" {
		t.Fatalf("typed non-success result without metadata was not failed closed: inner=%d outcome=%+v err=%v", innerCalls, outcome, err)
	}
}

func TestWebResearchRejectsInconsistentSuccessOrUnknownSchema(t *testing.T) {
	for _, raw := range []string{`{"schema_version":"rencrow.research.result.v1","run_id":"run_test","status":"succeeded","missing":["owner_commit"]}`, `{"schema_version":"unknown.v9","run_id":"run_test","status":"succeeded","missing":[]}`} {
		if err := webResearchOutcomeError([]llm.Message{{Content: raw, Metadata: map[string]string{"runtime_context_kind": "web_research_evidence", "research_status": "succeeded"}}}); err == nil {
			t.Fatal("inconsistent success accepted")
		}
	}
}

func TestWebResearchAllAgentGenerationPathsReceiveEvidence(t *testing.T) {
	for _, actor := range []string{"mio", "shiro-chat", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"} {
		t.Run(actor, func(t *testing.T) {
			research := &webResearchStub{}
			var captured llm.GenerateRequest
			provider := &mockLLMProvider{generateFunc: func(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
				captured = request
				return llm.GenerateResponse{Content: "verified answer", FinishReason: "stop"}, nil
			}}
			input := newAgentTurnInput(t, "公式ページを検索して", "viewer", "research-user")
			var err error
			switch actor {
			case "mio", "shiro-chat":
				_, err = NewMioAgent(provider, nil, nil, nil, nil, nil).WithWebResearch(research).Chat(context.Background(), input)
			case "shiro":
				_, err = NewShiroAgent(provider, nil, nil, "shiro", nil).WithWebResearch(research).Execute(context.Background(), input)
			case "kuro":
				_, err = NewHeavyAgent(provider, "kuro").WithWebResearch(research).Generate(context.Background(), input)
			case "midori":
				_, err = NewWildAgent(provider, "midori").WithWebResearch(research).Generate(context.Background(), input)
			default:
				_, err = NewCoderAgent(provider, nil, nil, "coder").WithWebResearch(research).GenerateWithPrompt(context.Background(), input, "proposal")
			}
			if err != nil || research.calls != 1 {
				t.Fatalf("research calls=%d err=%v", research.calls, err)
			}
			found := false
			for _, message := range captured.Messages {
				if message.Content == "actual Web evidence" && message.Type == llm.PromptContextVariable {
					found = true
				}
			}
			if !found {
				t.Fatal("final model never received actual evidence")
			}
		})
	}
}

func TestWebResearchTypedCompletionIsSharedAcrossManagementActors(t *testing.T) {
	requestText := `{"schema_version":"rencrow.research.request.v1","mode":"investigate","objective":"Search the official source"}`
	result := map[string]any{
		"schema_version": "rencrow.research.result.v1", "run_id": "research-run-1", "status": "succeeded",
		"missing": []string{}, "counts": map[string]any{"candidates": 0},
		"artifacts": []map[string]string{
			{"kind": "collection", "hash": strings.Repeat("b", 64), "ref": "collection-ref"},
			{"kind": "report", "hash": strings.Repeat("a", 64), "ref": "report-ref"},
		},
		"report": "Owner-produced report body.",
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	const taskSummary = "Research status=succeeded; documents_analyzed=1; records_saved=1"
	for _, actor := range []string{"mio", "shiro", "kuro", "midori"} {
		t.Run(actor, func(t *testing.T) {
			research := &webResearchStub{evidence: []llm.Message{{
				Role: "user", Type: llm.PromptContextVariable, Content: string(data), Metadata: map[string]string{
					"runtime_context_kind": "web_research_evidence", "research_status": "succeeded",
					"research_request_schema": "rencrow.research.request.v1", "research_mode": "investigate",
					"research_task_summary": taskSummary,
				},
			}}}
			innerCalls := 0
			provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
				innerCalls++
				return llm.GenerateResponse{Content: "LLM promise"}, nil
			}}
			input := newAgentTurnInput(t, requestText, "viewer", "research-user")
			var err error
			var output string
			switch actor {
			case "mio":
				output, err = NewMioAgent(provider, nil, nil, nil, nil, nil).WithWebResearch(research).Chat(context.Background(), input)
			case "shiro":
				output, err = NewShiroAgent(provider, nil, nil, "shiro", nil).WithWebResearch(research).Execute(context.Background(), input)
			case "kuro":
				output, err = NewHeavyAgent(provider, "kuro").WithWebResearch(research).Generate(context.Background(), input)
			case "midori":
				output, err = NewWildAgent(provider, "midori").WithWebResearch(research).Generate(context.Background(), input)
			}
			if err != nil || innerCalls != 0 || !strings.Contains(output, taskSummary) || !strings.Contains(output, "Owner-produced report body.") || strings.Contains(output, "LLM promise") {
				t.Fatalf("typed Research did not return authoritative completion: output=%q final_generations=%d err=%v", output, innerCalls, err)
			}
		})
	}
}

func TestWebResearchTypedRequestPreservesStructuredCallerContract(t *testing.T) {
	requestText := `{"schema_version":"rencrow.research.request.v1","mode":"investigate","objective":"Search the official source"}`
	result := `{"schema_version":"rencrow.research.result.v1","run_id":"research-run-structured","status":"succeeded","missing":[],"counts":{"candidates":0},"artifacts":[]}`
	for _, test := range []struct {
		name    string
		prepare func() llm.GenerateRequest
	}{
		{
			name: "response format",
			prepare: func() llm.GenerateRequest {
				return llm.GenerateRequest{
					Messages:       []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: requestText}},
					ResponseFormat: llm.ResponseFormatJSONObject,
				}
			},
		},
		{
			name: "Coder prompt contract",
			prepare: func() llm.GenerateRequest {
				return llm.GenerateRequest{Messages: []llm.Message{
					coderOwnedPromptBlock(coderPromptOwnerContract, CommonCoderContract, llm.PromptContextCharacter),
					{Role: "user", Type: llm.PromptContextUser, Content: requestText},
				}}
			},
		},
		{
			name: "system prompt contract",
			prepare: func() llm.GenerateRequest {
				return llm.GenerateRequest{
					Messages:     []llm.Message{{Role: "user", Type: llm.PromptContextUser, Content: requestText}},
					SystemPrompt: "Return the caller's declared JSON object.",
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			research := &webResearchStub{evidence: []llm.Message{{
				Role: "user", Type: llm.PromptContextVariable, Content: result,
				Metadata: map[string]string{
					"runtime_context_kind": "web_research_evidence", "research_status": "succeeded",
					"research_request_schema": "rencrow.research.request.v1", "research_mode": "investigate",
					"research_task_summary": "Research status=succeeded",
				},
			}}}
			innerCalls := 0
			provider := &mockLLMProvider{generateFunc: func(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
				innerCalls++
				if test.name == "response format" && request.ResponseFormat != llm.ResponseFormatJSONObject {
					t.Fatalf("structured ResponseFormat was lost: %q", request.ResponseFormat)
				}
				if test.name == "system prompt contract" && request.SystemPrompt != "Return the caller's declared JSON object." {
					t.Fatalf("structured SystemPrompt was lost: %q", request.SystemPrompt)
				}
				if test.name == "Coder prompt contract" {
					foundContract := false
					for _, message := range request.Messages {
						if message.Metadata[coderPromptOwnerMetadataKey] == coderPromptOwnerContract && message.Content == CommonCoderContract {
							foundContract = true
						}
					}
					if !foundContract {
						t.Fatal("Coder's owned output contract was lost")
					}
				}
				return llm.GenerateResponse{Content: "contract-compliant output"}, nil
			}}
			if _, err := withWebResearch(provider, research).Generate(context.Background(), test.prepare()); err != nil || innerCalls != 1 || research.calls != 1 {
				t.Fatalf("structured caller contract was replaced: final=%d research=%d err=%v", innerCalls, research.calls, err)
			}
		})
	}
}

func TestWebResearchTypedSearchOnlyReturnsActualCandidateLinks(t *testing.T) {
	request := llm.GenerateRequest{Messages: []llm.Message{{
		Role: "user", Type: llm.PromptContextUser,
		Content: `{"schema_version":"rencrow.research.request.v1","mode":"search_only","objective":"Find official links"}`,
	}}}
	result := map[string]any{
		"schema_version": "rencrow.research.result.v1", "run_id": "research-run-2", "status": "succeeded",
		"missing": []string{}, "counts": map[string]any{"candidates": 2},
		"candidates": []map[string]string{
			{"canonical_url": "https://example.org/one", "stable_item_id": "item-one"},
			{"stable_item_id": "item-two"},
		},
		"artifacts": []map[string]string{
			{"kind": "collection", "hash": strings.Repeat("b", 64), "ref": "collection-ref"},
			{"kind": "report", "hash": strings.Repeat("a", 64), "ref": "report-ref"},
		},
		"report": "search-only should not show report body",
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	research := &webResearchStub{evidence: []llm.Message{{
		Role: "user", Type: llm.PromptContextVariable, Content: string(data), Metadata: map[string]string{
			"runtime_context_kind": "web_research_evidence", "research_status": "succeeded",
			"research_request_schema": "rencrow.research.request.v1", "research_mode": "search_only",
			"research_task_summary": "Research status=succeeded; candidates=2",
		},
	}}}
	innerCalls := 0
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		innerCalls++
		return llm.GenerateResponse{Content: "unverified promise"}, nil
	}}
	response, err := withWebResearch(provider, research).Generate(context.Background(), request)
	if err != nil || innerCalls != 0 {
		t.Fatalf("search-only Research did not return deterministic links: response=%+v final_generations=%d err=%v", response, innerCalls, err)
	}
	for _, expected := range []string{"https://example.org/one", "stable_item_id=\"item-one\"", "stable_item_id=\"item-two\"", "candidates=2"} {
		if !strings.Contains(response.Content, expected) {
			t.Errorf("completion omitted actual search-only fact %q: %s", expected, response.Content)
		}
	}
	if strings.Contains(response.Content, "collection-ref") || strings.Contains(response.Content, "Report artifacts") || strings.Contains(response.Content, "report-ref") || strings.Contains(response.Content, "search-only should not show report body") {
		t.Fatalf("search-only output included report or non-report artifacts: %s", response.Content)
	}
}

func TestWebResearchCollectCompletionShowsOnlyOwnerFacts(t *testing.T) {
	const taskSummary = "Research status=succeeded; result_ref=sha256:result; receipt_state=verified"
	content, err := formatWebResearchCompletion(taskSummary, "collect", WebResearchRunResult{
		Report:    "unexpected report body",
		Artifacts: []WebResearchArtifactRef{{Kind: "report", Hash: strings.Repeat("a", 64), Ref: "report-ref"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, taskSummary) || strings.Contains(content, "unexpected report body") || strings.Contains(content, "Report artifacts") || strings.Contains(content, "report-ref") {
		t.Fatalf("collect output did not stay with owner facts: %s", content)
	}
}

func TestWebResearchInvestigateRejectsMismatchedSHA256ReportReference(t *testing.T) {
	hash := strings.Repeat("a", 64)
	_, err := formatWebResearchCompletion("Research status=succeeded", "investigate", WebResearchRunResult{
		Artifacts: []WebResearchArtifactRef{{Kind: "report", Hash: hash, Ref: "sha256:" + strings.Repeat("b", 64)}},
	})
	if err == nil {
		t.Fatal("report artifact reference with a different SHA-256 was displayed")
	}
}

func TestWebResearchTypedCompletionRejectsUnnormalizedModeMismatch(t *testing.T) {
	request := llm.GenerateRequest{Messages: []llm.Message{{
		Role: "user", Type: llm.PromptContextUser,
		Content: `{"schema_version":"rencrow.research.request.v1","mode":"collect","objective":"Search the official source"}`,
	}}}
	research := &webResearchStub{evidence: []llm.Message{{
		Role: "user", Type: llm.PromptContextVariable,
		Content: `{"schema_version":"rencrow.research.result.v1","run_id":"research-run-mode-mismatch","status":"succeeded","missing":[],"counts":{"candidates":0},"artifacts":[]}`,
		Metadata: map[string]string{
			"runtime_context_kind": "web_research_evidence", "research_status": "succeeded",
			"research_request_schema": "rencrow.research.request.v1", "research_mode": "search_only",
			"research_task_summary": "Research status=succeeded",
		},
	}}}
	innerCalls := 0
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		innerCalls++
		return llm.GenerateResponse{Content: "unverified promise"}, nil
	}}
	if _, err := withWebResearch(provider, research).Generate(context.Background(), request); err == nil || innerCalls != 0 {
		t.Fatalf("typed completion accepted mismatched normalized mode: final=%d err=%v", innerCalls, err)
	}
}

func TestWebResearchTypedSuccessWithoutRecordedTaskSummaryFailsClosed(t *testing.T) {
	request := llm.GenerateRequest{Messages: []llm.Message{{
		Role: "user", Type: llm.PromptContextUser,
		Content: `{"schema_version":"rencrow.research.request.v1","mode":"collect","objective":"Collect official source"}`,
	}}}
	research := &webResearchStub{evidence: []llm.Message{{
		Role: "user", Type: llm.PromptContextVariable,
		Content:  `{"schema_version":"rencrow.research.result.v1","run_id":"run-missing-summary","status":"succeeded","missing":[],"artifacts":[]}`,
		Metadata: map[string]string{"runtime_context_kind": "web_research_evidence", "research_status": "succeeded"},
	}}}
	innerCalls := 0
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		innerCalls++
		return llm.GenerateResponse{Content: "unverified promise"}, nil
	}}
	if _, err := withWebResearch(provider, research).Generate(context.Background(), request); err == nil || innerCalls != 0 {
		t.Fatalf("missing authoritative Task summary reached final LLM: calls=%d err=%v", innerCalls, err)
	}
}

func TestWebResearchPreservesFinalStreamAndStructuredContract(t *testing.T) {
	research := &webResearchStub{}
	called := false
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
		if request.ResponseFormat != llm.ResponseFormatJSONObject || request.MaxTokens != 1234 || request.Temperature != 0.4 || request.RequestAdmission == nil {
			t.Fatal("final generation contract changed")
		}
		request.OnToken("final only")
		return llm.GenerateResponse{Content: "final"}, nil
	}}
	_, err := withWebResearch(provider, research).Generate(context.Background(), llm.GenerateRequest{
		Messages:       []llm.Message{{Role: "system", Content: "stable", Type: llm.PromptContextCharacter}, {Role: "user", Content: "https://example.org を読んで"}},
		ResponseFormat: llm.ResponseFormatJSONObject, MaxTokens: 1234, Temperature: 0.4,
		RequestAdmission: llm.NewGenerateRequestAdmission(func() error { return nil }),
		OnToken:          func(text string) { called = text == "final only" },
	})
	if err != nil || !called || research.calls != 1 {
		t.Fatalf("final callback lost: called=%v research=%d err=%v", called, research.calls, err)
	}
}

func TestWebResearchDoesNotReplaceMioLogicalRetrievalOrSearchOrdinaryChat(t *testing.T) {
	for _, request := range []llm.GenerateRequest{
		{Messages: []llm.Message{{Role: "user", Content: "こんにちは"}}},
		{Messages: []llm.Message{{Role: "user", Content: "私の名前を覚えてる？"}}},
		{Messages: []llm.Message{{Role: "user", Content: "検索して"}}, LogicalOutcomeStream: llm.LogicalOutcomeStreamV2},
		{Messages: []llm.Message{{Role: "user", Content: `{"schema_version":"rencrow.research.request.v1","mode":"investigate","objective":"official source"}`}}, LogicalOutcomeStream: llm.LogicalOutcomeStreamV2},
	} {
		research := &webResearchStub{}
		if _, err := withWebResearch(&mockLLMProvider{}, research).Generate(context.Background(), request); err != nil || research.calls != 0 {
			t.Fatalf("unexpected extra research: %d, %v", research.calls, err)
		}
	}
}

func TestWebResearchUnavailableCannotBecomeAnUnverifiedAnswer(t *testing.T) {
	research := &webResearchStub{err: errors.New("no registered Web tools")}
	called := false
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		called = true
		return llm.GenerateResponse{Content: "I searched"}, nil
	}}
	_, err := withWebResearch(provider, research).Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "検索して"}}})
	if err == nil || called || !strings.Contains(err.Error(), "no registered Web tools") {
		t.Fatalf("unverified final answer was allowed: called=%v err=%v", called, err)
	}
}

func TestWebResearchPrivateTaskDoesNotInheritPublicCapabilities(t *testing.T) {
	input := privateWorkTestInput(t, "検索して")
	research := &webResearchStub{}
	wild := NewWildAgent(&mockLLMProvider{}, "midori").WithWebResearch(research)
	if _, err := wild.ExecutePrivateWork(privateWorkTestContext(t, input, "midori"), input, []string{"web_gather.fetch"}); err != nil {
		t.Fatal(err)
	}
	if research.allowed == nil || len(research.allowed) != 1 || research.allowed[0] != "web_gather.fetch" {
		t.Fatalf("Task grant was broadened: %#v", research.allowed)
	}
}
