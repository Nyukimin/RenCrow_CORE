package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

func TestLLMClassifier_ClassifyWithMioTurnOutcomeUsesOneStrictVersionedGeneration(t *testing.T) {
	response := `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"Mio answer."},"thread_boundary":"unknown"}`
	var captured llm.GenerateRequest
	generateCalls := 0
	classifier := NewLLMClassifier(&mockLLMProvider{
		response: response,
		onGenerate: func(req llm.GenerateRequest) {
			generateCalls++
			captured = req
		},
	}, "existing route policy")
	messages := []llm.Message{
		{Role: "system", Content: "Mio persona", Type: llm.PromptContextCharacter},
		{Role: "system", Content: "RecallPack", Type: llm.PromptContextRecall},
		{Role: "user", Content: "answer this", Type: llm.PromptContextUser},
	}

	result, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{
		Messages: messages,
		OnToken:  func(string) {},
	}, nil)
	if err != nil {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
	}
	if result.Decision.Route != domainrouting.RouteCHAT {
		t.Fatalf("route = %s, want CHAT", result.Decision.Route)
	}
	if result.Outcome.Kind != domainagent.MioTurnOutcomeAnswer || result.Outcome.Text != "Mio answer." || len(result.Outcome.Requests) != 0 {
		t.Fatalf("outcome = %#v, want answer branch", result.Outcome)
	}
	if result.Generation.Content != response {
		t.Fatalf("generation content = %q, want original provider response", result.Generation.Content)
	}
	if generateCalls != 1 {
		t.Fatalf("Generate calls = %d, want one combined generation", generateCalls)
	}
	if captured.ResponseFormat != llm.ResponseFormatJSONObject || captured.OnToken != nil || captured.SystemPrompt != "" {
		t.Fatalf("generation request = %#v, want JSON object mode, no token callback, and typed messages", captured)
	}
	if len(captured.Messages) != len(messages)+1 || !reflect.DeepEqual(captured.Messages[0], messages[0]) || !reflect.DeepEqual(captured.Messages[2], messages[1]) || !reflect.DeepEqual(captured.Messages[3], messages[2]) {
		t.Fatalf("messages = %#v, want character, v3 contract, recall, user order", captured.Messages)
	}
	contract := captured.Messages[1]
	if contract.Role != "system" || contract.Type != llm.PromptContextStable || contract.Metadata["runtime_context_kind"] != "classifier_mio_turn_outcome_contract" {
		t.Fatalf("contract message = %#v, want stable Mio turn contract", contract)
	}
	for _, value := range []string{"schema_version", "3", "thread_boundary", "keep", "new", "unknown", "answer", "retrieve", "handoff", "conversation_memory", "user_memory", "knowledge_memory", "catalog", "glossary", "web_search", "owner", "scope"} {
		if !strings.Contains(contract.Content, value) {
			t.Errorf("contract prompt is missing %q", value)
		}
	}
	kwargs, ok := captured.ProviderOptions["chat_template_kwargs"].(map[string]any)
	if captured.ProviderOptions["think"] != false || !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("ProviderOptions = %#v, want thinking disabled", captured.ProviderOptions)
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeDefaultsToSchemaV3AndReadsBoundary(t *testing.T) {
	response := `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"Mio answer."},"thread_boundary":"new"}`
	var captured llm.GenerateRequest
	classifier := NewLLMClassifier(&mockLLMProvider{
		response:   response,
		onGenerate: func(req llm.GenerateRequest) { captured = req },
	}, "existing route policy")

	result, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
	if err != nil {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
	}
	field := reflect.ValueOf(result).FieldByName("Boundary")
	if !field.IsValid() || field.String() != "new" {
		t.Fatalf("ThreadBoundary = %v, want new", field)
	}
	if len(captured.Messages) == 0 || !strings.Contains(captured.Messages[0].Content, "schema_version 3") {
		t.Fatalf("combined prompt does not request schema_version 3: %#v", captured.Messages)
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeAcceptsLegacySchemaV2Nonstream(t *testing.T) {
	response := `{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"Legacy answer."}}`
	var captured llm.GenerateRequest
	classifier := NewLLMClassifier(&mockLLMProvider{
		response:   response,
		onGenerate: func(req llm.GenerateRequest) { captured = req },
	}, "existing route policy")

	result, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
	if err != nil {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
	}
	if result.Outcome.Kind != domainagent.MioTurnOutcomeAnswer || result.Outcome.Text != "Legacy answer." {
		t.Fatalf("outcome = %#v, want valid legacy answer", result.Outcome)
	}
	if result.Boundary != domainconversation.ThreadBoundaryUnknown {
		t.Fatalf("boundary = %q, want unknown for schema v2", result.Boundary)
	}
	if len(captured.Messages) == 0 || !strings.Contains(captured.Messages[0].Content, "schema_version 3") {
		t.Fatalf("combined prompt does not request schema_version 3: %#v", captured.Messages)
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeRejectsBoundaryOnLegacySchemaV2(t *testing.T) {
	classifier := NewLLMClassifier(&mockLLMProvider{
		response: `{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"Legacy answer."},"thread_boundary":"new"}`,
	}, "existing route policy")
	_, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
	if !errors.Is(err, domainagent.ErrInvalidMioTurnOutcome) {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v, want strict v2 envelope rejection", err)
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeBoundaryOnlyMalformedFallsBackToUnknown(t *testing.T) {
	for _, boundary := range []string{`null`, `42`, `1e400`, `[]`, `"maybe"`} {
		t.Run(boundary, func(t *testing.T) {
			response := `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"Mio answer."},"thread_boundary":` + boundary + `}`
			classifier := NewLLMClassifier(&mockLLMProvider{response: response}, "route policy")
			result, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
			if err != nil {
				t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
			}
			field := reflect.ValueOf(result).FieldByName("Boundary")
			if !field.IsValid() || field.String() != "unknown" || result.Outcome.Text != "Mio answer." {
				t.Fatalf("classification boundary/outcome = %v / %#v, want unknown / valid answer", field, result.Outcome)
			}
		})
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeRejectsNestedDuplicateBoundaryKeys(t *testing.T) {
	generateCalls := 0
	classifier := NewLLMClassifier(&mockLLMProvider{
		response:   `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"valid answer"},"thread_boundary":{"x":1,"x":2}}`,
		onGenerate: func(llm.GenerateRequest) { generateCalls++ },
	}, "route policy")
	_, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
	if !errors.Is(err, domainagent.ErrInvalidMioTurnOutcome) {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v, want duplicate boundary rejection", err)
	}
	if generateCalls != 1 {
		t.Fatalf("Generate calls = %d, want exactly one and no repair generation", generateCalls)
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeMissingBoundaryFallsBackToUnknown(t *testing.T) {
	classifier := NewLLMClassifier(&mockLLMProvider{
		response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"Mio answer."}}`,
	}, "route policy")
	result, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
	if err != nil {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
	}
	if result.Boundary != domainconversation.ThreadBoundaryUnknown || result.Outcome.Text != "Mio answer." {
		t.Fatalf("classification = %#v, want unknown boundary and valid answer", result)
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeAcceptsBoundedRetrieveAndHandoff(t *testing.T) {
	t.Run("retrieve at inclusive limits", func(t *testing.T) {
		refs := make([]string, 8)
		for i := range refs {
			refs[i] = fmt.Sprintf("ref-%d", i+1)
		}
		query := strings.Repeat("あ", 512)
		informationNeeded := strings.Repeat("知", 256)
		response := marshalMioTurnOutcome(t, "CHAT", map[string]any{
			"kind": "retrieve",
			"requests": []any{
				map[string]any{"source_kind": "conversation_memory", "query": query, "references": refs, "information_needed": informationNeeded},
				map[string]any{"source_kind": "user_memory", "query": "出発時刻", "references": []string{}, "information_needed": "確定した時刻"},
			},
		})
		classifier := NewLLMClassifier(&mockLLMProvider{response: response}, "route policy")
		fixedRoute := domainrouting.RouteCHAT

		result, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, &fixedRoute)
		if err != nil {
			t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
		}
		if result.Outcome.Kind != domainagent.MioTurnOutcomeRetrieve || len(result.Outcome.Requests) != 2 {
			t.Fatalf("outcome = %#v, want two retrieval requests", result.Outcome)
		}
		first := result.Outcome.Requests[0]
		if first.SourceKind != domainagent.MioRetrievalSourceConversationMemory || first.Query != query || !reflect.DeepEqual(first.References, refs) || first.InformationNeeded != informationNeeded {
			t.Fatalf("first request = %#v, want bounded Unicode values preserved", first)
		}
		if result.Outcome.Requests[1].SourceKind != domainagent.MioRetrievalSourceUserMemory || result.Outcome.Requests[1].References == nil {
			t.Fatalf("second request = %#v, want registered source and explicit empty references", result.Outcome.Requests[1])
		}
	})

}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeAcceptsEveryRegisteredSourceKind(t *testing.T) {
	registered := []domainagent.MioRetrievalSourceKind{
		domainagent.MioRetrievalSourceConversationMemory,
		domainagent.MioRetrievalSourceUserMemory,
		domainagent.MioRetrievalSourceKnowledgeMemory,
		domainagent.MioRetrievalSourceCatalog,
		domainagent.MioRetrievalSourceGlossary,
		domainagent.MioRetrievalSourceWebSearch,
	}
	for _, sourceKind := range registered {
		t.Run(string(sourceKind), func(t *testing.T) {
			response := marshalMioTurnOutcome(t, "CHAT", map[string]any{
				"kind": "retrieve",
				"requests": []any{map[string]any{
					"source_kind":        sourceKind,
					"query":              "x",
					"references":         []string{},
					"information_needed": "y",
				}},
			})
			classifier := NewLLMClassifier(&mockLLMProvider{response: response}, "route policy")
			result, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
			if err != nil {
				t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
			}
			if len(result.Outcome.Requests) != 1 || result.Outcome.Requests[0].SourceKind != sourceKind {
				t.Fatalf("requests = %#v, want registered source kind %q", result.Outcome.Requests, sourceKind)
			}
		})
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeRejectsInvalidV3Responses(t *testing.T) {
	tooManyRequests := marshalMioTurnOutcome(t, "CHAT", map[string]any{
		"kind": "retrieve",
		"requests": []any{
			validMioRetrievalRequest(), validMioRetrievalRequest(), validMioRetrievalRequest(),
		},
	})
	tooManyReferences := make([]string, 9)
	for i := range tooManyReferences {
		tooManyReferences[i] = fmt.Sprintf("ref-%d", i+1)
	}
	largeQuery := strings.Repeat("あ", 513)
	largeInformationNeeded := strings.Repeat("知", 257)
	tests := []struct {
		name       string
		response   string
		fixedRoute *domainrouting.Route
	}{
		{name: "missing schema version", response: `{"route":"CHAT","outcome":{"kind":"answer","text":"ok"}}`},
		{name: "unsupported schema version", response: `{"schema_version":1,"route":"CHAT","outcome":{"kind":"answer","text":"ok"}}`},
		{name: "fractional schema version", response: `{"schema_version":3.0,"route":"CHAT","outcome":{"kind":"answer","text":"ok"}}`},
		{name: "unknown top-level field", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"ok"},"extra":true}`},
		{name: "duplicate top-level field", response: `{"schema_version":3,"route":"CHAT","route":"CHAT","outcome":{"kind":"answer","text":"ok"}}`},
		{name: "duplicate boundary field", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"ok"},"thread_boundary":"new","thread_boundary":"keep"}`},
		{name: "missing route", response: `{"schema_version":3,"outcome":{"kind":"answer","text":"ok"}}`},
		{name: "missing outcome", response: `{"schema_version":3,"route":"CHAT"}`},
		{name: "unknown route", response: `{"schema_version":3,"route":"UNKNOWN","outcome":{"kind":"handoff"}}`},
		{name: "route excluded by classifier policy CODE4", response: `{"schema_version":3,"route":"CODE4","outcome":{"kind":"handoff"}}`},
		{name: "route excluded by classifier policy WILD", response: `{"schema_version":3,"route":"WILD","outcome":{"kind":"handoff"}}`},
		{name: "noncanonical route", response: `{"schema_version":3,"route":"chat","outcome":{"kind":"answer","text":"ok"}}`},
		{name: "unknown outcome kind", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"search","query":"x"}}`},
		{name: "duplicate outcome kind", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","kind":"retrieve","text":"ok"}}`},
		{name: "answer text missing", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer"}}`},
		{name: "answer text empty", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":""}}`},
		{name: "answer text whitespace", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"  \n "}}`},
		{name: "answer with retrieval branch", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"ok","requests":[]}}`},
		{name: "answer on handoff route", response: `{"schema_version":3,"route":"RESEARCH","outcome":{"kind":"answer","text":"ok"}}`},
		{name: "retrieve with answer field", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"retrieve","requests":[],"text":"ok"}}`},
		{name: "retrieve route mismatch", response: `{"schema_version":3,"route":"CODE2","outcome":{"kind":"retrieve","requests":[]}}`},
		{name: "empty requests", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{}})},
		{name: "missing requests", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"retrieve"}}`},
		{name: "more than two requests", response: tooManyRequests},
		{name: "query empty", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": "", "references": []string{}, "information_needed": "reason"}}})},
		{name: "query whitespace", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": " \n ", "references": []string{}, "information_needed": "reason"}}})},
		{name: "query over rune limit", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": largeQuery, "references": []string{}, "information_needed": "reason"}}})},
		{name: "information needed empty", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": "query", "references": []string{}, "information_needed": ""}}})},
		{name: "information needed over rune limit", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": "query", "references": []string{}, "information_needed": largeInformationNeeded}}})},
		{name: "more than eight references", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": "query", "references": tooManyReferences, "information_needed": "reason"}}})},
		{name: "references wrong type", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": "query", "references": []any{"ref-1", 2}, "information_needed": "reason"}}})},
		{name: "empty reference item", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": "query", "references": []string{" "}, "information_needed": "reason"}}})},
		{name: "duplicate request field", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"conversation_memory","query":"first","query":"second","references":[],"information_needed":"reason"}]}}`},
		{name: "unregistered physical catalog id", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "movie_catalog", "query": "query", "references": []string{}, "information_needed": "reason"}}})},
		{name: "unregistered source", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "arbitrary_sql", "query": "query", "references": []string{}, "information_needed": "reason"}}})},
		{name: "owner and scope fields are forbidden", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": "query", "references": []string{}, "information_needed": "reason", "owner": "other", "scope": "all"}}})},
		{name: "request missing references", response: marshalMioTurnOutcome(t, "CHAT", map[string]any{"kind": "retrieve", "requests": []any{map[string]any{"source_kind": "conversation_memory", "query": "query", "information_needed": "reason"}}})},
		{name: "handoff cannot be CHAT", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"handoff"}}`},
		{name: "handoff with answer field", response: `{"schema_version":3,"route":"CODE2","outcome":{"kind":"handoff","text":"do it"}}`},
		{name: "handoff cannot be interpreted on fixed non-CHAT route", response: `{"schema_version":3,"route":"CODE2","outcome":{"kind":"handoff"}}`, fixedRoute: routePointer(domainrouting.RouteCODE2)},
		{name: "fixed route rejects classifier route change", response: `{"schema_version":3,"route":"CODE2","outcome":{"kind":"handoff"}}`, fixedRoute: routePointer(domainrouting.RouteCHAT)},
		{name: "fixed route rejects answer/handoff mismatch", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"ok"}}`, fixedRoute: routePointer(domainrouting.RouteCODE2)},
		{name: "invalid JSON", response: `{"schema_version":3,`},
		{name: "trailing content", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"ok"}} garbage`},
		{name: "second JSON object", response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"answer","text":"ok"}} {}`},
		{name: "oversized response", response: strings.Repeat(" ", 64*1024+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classifier := NewLLMClassifier(&mockLLMProvider{response: tt.response}, "route policy")
			_, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, tt.fixedRoute)
			if !errors.Is(err, domainagent.ErrInvalidMioTurnOutcome) {
				t.Fatalf("ClassifyWithMioTurnOutcome() error = %v, want ErrInvalidMioTurnOutcome", err)
			}
		})
	}

}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeRejectsNilProvider(t *testing.T) {
	classifier := NewLLMClassifier(nil, "route policy")
	_, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
	if err == nil || errors.Is(err, domainagent.ErrInvalidMioTurnOutcome) {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v, want provider unavailable error", err)
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeDoesNotRepairInvalidOutput(t *testing.T) {
	generateCalls := 0
	classifier := NewLLMClassifier(&mockLLMProvider{
		response: `{"schema_version":3,"route":"CHAT","outcome":{"kind":"unknown"}}`,
		onGenerate: func(llm.GenerateRequest) {
			generateCalls++
		},
	}, "route policy")

	_, err := classifyWithMioTurnOutcome(t, classifier, llm.GenerateRequest{}, nil)
	if !errors.Is(err, domainagent.ErrInvalidMioTurnOutcome) {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v, want ErrInvalidMioTurnOutcome", err)
	}
	if generateCalls != 1 {
		t.Fatalf("Generate calls = %d, want one attempt and no semantic repair call", generateCalls)
	}
}

func classifyWithMioTurnOutcome(t *testing.T, classifier *LLMClassifier, req llm.GenerateRequest, fixedRoute *domainrouting.Route) (domainagent.MioTurnClassification, error) {
	t.Helper()
	capability, ok := any(classifier).(domainagent.MioTurnClassifier)
	if !ok {
		t.Fatal("LLMClassifier does not implement the optional MioTurnClassifier contract")
	}
	return capability.ClassifyWithMioTurnOutcome(context.Background(), req, fixedRoute)
}

func marshalMioTurnOutcome(t *testing.T, route string, outcome map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"schema_version":  3,
		"route":           route,
		"outcome":         outcome,
		"thread_boundary": "unknown",
	})
	if err != nil {
		t.Fatalf("marshal MioTurnOutcome fixture: %v", err)
	}
	return string(encoded)
}

func validMioRetrievalRequest() map[string]any {
	return map[string]any{
		"source_kind":        "conversation_memory",
		"query":              "previous decision",
		"references":         []string{},
		"information_needed": "the exact decision",
	}
}

func routePointer(route domainrouting.Route) *domainrouting.Route {
	return &route
}
