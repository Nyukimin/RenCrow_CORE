package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMioExternalPeerPromptExcludesPrivateContextAndPreservesSnapshot(t *testing.T) {
	snapshot := emissionPromptTestSnapshot()
	original := snapshot.Clone()
	engine := &emissionPromptTestConversationEngine{
		recall: &conversation.RecallPack{
			UserProfile:  conversation.UserProfile{Facts: []string{"PRIVATE_USER_MEMORY_MARKER"}},
			MidSummaries: []conversation.ThreadSummary{{Summary: "PRIVATE_THREAD_SUMMARY_MARKER"}},
			LongFacts:    []string{"PRIVATE_LONG_MEMORY_MARKER"},
		},
	}
	recentCalls := 0
	runtimeCalls := 0
	mio := (&MioAgent{conversationEngine: engine}).
		WithSystemPrompt("authored Mio system prompt").
		WithStableRuntimeContexts(map[string]string{"mio": "PRIVATE_STABLE_CONTEXT_MARKER"}).
		WithRuntimeContextProvider(func(context.Context, string) string {
			runtimeCalls++
			return "PRIVATE_RUNTIME_CONTEXT_MARKER"
		}).
		WithRecentContextProvider(func(context.Context, int) (string, error) {
			recentCalls++
			return "PRIVATE_RECENT_CONTEXT_MARKER", nil
		}).
		WithViewerRecipientPrompts(map[string]string{"external_peer": "PRIVATE_VIEWER_HANDLER_MARKER"})

	scope := emissionPromptTestScope(conversation.ConversationPurpose("external_peer"))
	scope.ActorID = "board:instance:sender"
	ctx := WithDailyNewsBrief(context.Background(), domainnews.DailyNewsBrief{
		Date:   "2026-11-05",
		Source: domainnews.SourceScheduled,
		Status: domainnews.StatusReady,
		Items:  []domainnews.Item{{ID: "private-news", Title: "PRIVATE_DAILY_NEWS_MARKER"}},
	})

	composed, err := mio.ComposeConversationPrompt(ctx, scope, snapshot)
	if err != nil {
		t.Fatalf("ComposeConversationPrompt(external_peer): %v", err)
	}
	if engine.calls != 0 || recentCalls != 0 || runtimeCalls != 0 {
		t.Fatalf("external peer prompt consulted private suppliers: BeginTurn=%d recent=%d runtime=%d", engine.calls, recentCalls, runtimeCalls)
	}
	if !reflect.DeepEqual(snapshot, original) {
		t.Fatalf("ComposeConversationPrompt mutated the input snapshot: %#v", snapshot)
	}
	if !reflect.DeepEqual(composed.AllowedSourceMessageIDs, original.AllowedSourceMessageIDs) ||
		composed.SourceRevision != original.SourceRevision || composed.CutoverRevision != original.CutoverRevision {
		t.Fatalf("composition changed thread snapshot provenance: %#v", composed)
	}
	if findPromptMessage(composed.Messages, func(message llm.Message) bool {
		return message.Type == llm.PromptContextCharacter && message.Content == "authored Mio system prompt"
	}) < 0 {
		t.Fatalf("authored Mio system prompt is missing: %#v", composed.Messages)
	}
	for _, message := range original.Messages {
		if findPromptMessage(composed.Messages, func(candidate llm.Message) bool {
			return candidate.Role == message.Role && candidate.Content == message.Content
		}) < 0 || countPromptContent(composed.Messages, message.Content) != 1 {
			t.Fatalf("authorized thread message was not preserved exactly once: %#v", message)
		}
	}
	for _, marker := range []string{
		"PRIVATE_USER_MEMORY_MARKER",
		"PRIVATE_THREAD_SUMMARY_MARKER",
		"PRIVATE_LONG_MEMORY_MARKER",
		"PRIVATE_RECENT_CONTEXT_MARKER",
		"PRIVATE_RUNTIME_CONTEXT_MARKER",
		"PRIVATE_STABLE_CONTEXT_MARKER",
		"PRIVATE_DAILY_NEWS_MARKER",
		"PRIVATE_VIEWER_HANDLER_MARKER",
	} {
		if promptContains(composed.Messages, marker) {
			t.Fatalf("private context marker %q was injected", marker)
		}
	}
	if promptContains(composed.Messages, "Token contract:") {
		t.Fatal("viewer-specific token handler was injected into the external peer prompt")
	}
}

func TestMioExternalPeerEmissionUsesScopedReplyContractAndReceipt(t *testing.T) {
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{
		AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...),
	}
	scope := emissionTestScope()
	scope.ActorID = "board:instance:sender"
	scope.Purpose = conversation.ConversationPurpose("external_peer")
	requestID := core.NewRequestID()
	responseID := core.NewResponseID()
	calls := 0
	var gotScope conversation.ConversationScope
	var gotRequest llm.GenerateRequest
	provider := conversationGenerateFunc(func(_ context.Context, receivedScope conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		gotScope = receivedScope
		gotRequest = request
		return llm.GenerateResponse{
			Content:    `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"The requested update is ready."}}`,
			TokensUsed: 23,
			RequestID:  requestID,
			ResponseID: responseID,
		}, nil
	})

	emission, receipt, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), scope, snapshot, allowlist)
	if err != nil {
		t.Fatalf("GenerateEmission(external_peer): %v", err)
	}
	if calls != 1 {
		t.Fatalf("GenerateConversation calls = %d, want exactly one", calls)
	}
	if gotScope != scope || gotScope.ActorID != "board:instance:sender" {
		t.Fatalf("provider scope = %#v, want namespaced external source scope %#v", gotScope, scope)
	}
	if emission.Kind != conversation.EmissionKindReply || emission.UserMessage.To != "external_peer" || emission.UserMessage.Text != "The requested update is ready." || emission.Delegation != nil {
		t.Fatalf("unexpected external peer emission: %#v", emission)
	}
	if gotRequest.ResponseFormat != llm.ResponseFormatJSONObject || gotRequest.OnToken != nil {
		t.Fatalf("provider request must be non-stream JSON: format=%q callback=%t", gotRequest.ResponseFormat, gotRequest.OnToken != nil)
	}
	contractIndex := findPromptMessage(gotRequest.Messages, func(message llm.Message) bool {
		return message.Metadata["runtime_context_kind"] == "mio_emission_contract"
	})
	if contractIndex < 0 {
		t.Fatalf("Mio emission contract is missing from provider request: %#v", gotRequest.Messages)
	}
	contract := gotRequest.Messages[contractIndex].Content
	if !strings.Contains(contract, `"to":"external_peer"`) && !strings.Contains(contract, `"to": "external_peer"`) {
		t.Fatalf("external peer reply target is not explicit in the contract: %q", contract)
	}
	if !strings.Contains(contract, `"kind":"reply"`) && !strings.Contains(contract, `"kind": "reply"`) {
		t.Fatalf("reply-only contract example is missing: %q", contract)
	}
	if strings.Contains(contract, `"kind":"delegate"`) || strings.Contains(contract, `"kind": "delegate"`) ||
		strings.Contains(contract, `"delegation"`) {
		t.Fatalf("external peer contract exposes delegation capability: %q", contract)
	}
	for _, key := range []string{"tools", "tool_choice", "functions", "function_call"} {
		if _, ok := gotRequest.ProviderOptions[key]; ok {
			t.Fatalf("external peer request exposes tool option %q", key)
		}
	}
	if receipt.RequestID != string(requestID) || receipt.ResponseID != string(responseID) ||
		receipt.ProviderLabel != "mio-local" || receipt.TokensUsed != 23 || receipt.Status != "completed" {
		t.Fatalf("invocation receipt did not retain provider response fields: %#v", receipt)
	}

	t.Run("rejects assignee authority before provider", func(t *testing.T) {
		forbiddenAllowlist := conversation.EmissionAllowlist{
			AllowedAssignees:        []string{"shiro"},
			AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...),
		}
		forbiddenCalls := 0
		forbiddenProvider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
			forbiddenCalls++
			return llm.GenerateResponse{Content: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"The requested update is ready."}}`}, nil
		})
		_, _, forbiddenErr := NewMioEmissionGenerator(forbiddenProvider).GenerateEmission(context.Background(), scope, snapshot, forbiddenAllowlist)
		if forbiddenErr == nil {
			t.Fatal("GenerateEmission() error = nil, want nonempty external peer assignee authority rejection")
		}
		if forbiddenCalls != 0 {
			t.Fatalf("provider calls = %d, want 0 when assignee authority is present", forbiddenCalls)
		}
	})
}
