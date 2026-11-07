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

func boardOwnerRequestPromptSnapshot() PromptSnapshot {
	return PromptSnapshot{
		Messages: []llm.Message{{
			Role:    "user",
			Content: "Owner instruction: ask the deployment owner to confirm Friday's maintenance window.",
			Type:    llm.PromptContextUser,
		}},
		AllowedSourceMessageIDs: []core.MessageID{core.NewMessageID()},
		SourceRevision:          "board-owner-request:source-1",
		CutoverRevision:         "board-owner-request:cutover-1",
	}
}

func boardOwnerRequestPromptScope(snapshot PromptSnapshot) conversation.ConversationScope {
	scope := emissionTestScope()
	scope.ActorID = "board:instance:owner"
	scope.InputID = snapshot.AllowedSourceMessageIDs[0]
	scope.Purpose = conversation.PurposeBoardOwnerRequest
	return scope
}

func TestMioBoardOwnerRequestPromptExcludesPrivateContextAndKeepsAuthoredPersona(t *testing.T) {
	snapshot := boardOwnerRequestPromptSnapshot()
	original := snapshot.Clone()
	scope := boardOwnerRequestPromptScope(snapshot)
	engine := &emissionPromptTestConversationEngine{
		recall: &conversation.RecallPack{
			UserProfile:  conversation.UserProfile{Facts: []string{"PRIVATE_OWNER_USER_MEMORY_MARKER"}},
			MidSummaries: []conversation.ThreadSummary{{Summary: "PRIVATE_OWNER_THREAD_MARKER"}},
			LongFacts:    []string{"PRIVATE_OWNER_LONG_MEMORY_MARKER"},
		},
	}
	recentCalls := 0
	runtimeCalls := 0
	mio := (&MioAgent{conversationEngine: engine}).
		WithSystemPrompt("authored Mio system persona").
		WithStableRuntimeContexts(map[string]string{"mio": "PRIVATE_OWNER_STABLE_OPS_CONTEXT_MARKER"}).
		WithRuntimeContextProvider(func(context.Context, string) string {
			runtimeCalls++
			return "PRIVATE_OWNER_RUNTIME_OPS_CONTEXT_MARKER"
		}).
		WithRecentContextProvider(func(context.Context, int) (string, error) {
			recentCalls++
			return "PRIVATE_OWNER_RECENT_MARKER", nil
		}).
		WithViewerRecipientPrompts(map[string]string{
			"external_peer": "PRIVATE_OWNER_VIEWER_HANDLER_MARKER",
			"mio":           "PRIVATE_OWNER_VIEWER_HANDLER_MARKER",
		})
	ctx := WithDailyNewsBrief(context.Background(), domainnews.DailyNewsBrief{
		Date:   "2026-11-05",
		Source: domainnews.SourceScheduled,
		Status: domainnews.StatusReady,
		Items:  []domainnews.Item{{ID: "private-owner-news", Title: "PRIVATE_OWNER_DAILY_NEWS_MARKER"}},
	})

	composed, err := mio.ComposeConversationPrompt(ctx, scope, snapshot)
	if err != nil {
		t.Fatalf("ComposeConversationPrompt(board_owner_request): %v", err)
	}
	if engine.calls != 0 || recentCalls != 0 || runtimeCalls != 0 {
		t.Fatalf("board owner request prompt consulted private suppliers: BeginTurn=%d recent=%d runtime=%d", engine.calls, recentCalls, runtimeCalls)
	}
	if !reflect.DeepEqual(snapshot, original) {
		t.Fatalf("ComposeConversationPrompt mutated the owner request snapshot: %#v", snapshot)
	}
	if !reflect.DeepEqual(composed.AllowedSourceMessageIDs, original.AllowedSourceMessageIDs) ||
		composed.SourceRevision != original.SourceRevision || composed.CutoverRevision != original.CutoverRevision {
		t.Fatalf("composition changed owner request provenance: %#v", composed)
	}
	if findPromptMessage(composed.Messages, func(message llm.Message) bool {
		return message.Type == llm.PromptContextCharacter && message.Content == "authored Mio system persona"
	}) < 0 {
		t.Fatalf("authored Mio system persona is missing: %#v", composed.Messages)
	}
	if countPromptContent(composed.Messages, snapshot.Messages[0].Content) != 1 {
		t.Fatalf("owner instruction must be present exactly once: %#v", composed.Messages)
	}
	for _, message := range composed.Messages {
		if message.Role == "assistant" || (message.Role == "user" && message.Content != snapshot.Messages[0].Content) {
			t.Fatalf("prompt contains conversation beyond the owner instruction: %#v", composed.Messages)
		}
	}
	for _, marker := range []string{
		"PRIVATE_OWNER_USER_MEMORY_MARKER",
		"PRIVATE_OWNER_THREAD_MARKER",
		"PRIVATE_OWNER_LONG_MEMORY_MARKER",
		"PRIVATE_OWNER_STABLE_OPS_CONTEXT_MARKER",
		"PRIVATE_OWNER_RUNTIME_OPS_CONTEXT_MARKER",
		"PRIVATE_OWNER_RECENT_MARKER",
		"PRIVATE_OWNER_DAILY_NEWS_MARKER",
		"PRIVATE_OWNER_VIEWER_HANDLER_MARKER",
	} {
		if promptContains(composed.Messages, marker) {
			t.Fatalf("private context marker %q was injected", marker)
		}
	}
	if promptContains(composed.Messages, "Reply only in the current thread") || promptContains(composed.Messages, "Token contract:") {
		t.Fatalf("external-peer or viewer routing instructions were reused: %#v", composed.Messages)
	}
}

func TestMioBoardOwnerRequestEmissionUsesBoundSingleGeneration(t *testing.T) {
	snapshot := boardOwnerRequestPromptSnapshot()
	scope := boardOwnerRequestPromptScope(snapshot)
	mio := (&MioAgent{}).WithSystemPrompt("authored Mio system persona")
	composed, err := mio.ComposeConversationPrompt(context.Background(), scope, snapshot)
	if err != nil {
		t.Fatalf("ComposeConversationPrompt(board_owner_request): %v", err)
	}

	const emittedText = "Could you confirm Friday's maintenance window?"
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
			Content:    `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"` + emittedText + `"}}`,
			TokensUsed: 17,
			RequestID:  requestID,
			ResponseID: responseID,
		}, nil
	})
	allowlist := conversation.EmissionAllowlist{
		AllowedSourceMessageIDs: append([]core.MessageID(nil), composed.AllowedSourceMessageIDs...),
	}

	emission, receipt, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), scope, composed, allowlist)
	if err != nil {
		t.Fatalf("GenerateEmission(board_owner_request): %v", err)
	}
	if calls != 1 {
		t.Fatalf("GenerateConversation calls = %d, want exactly one", calls)
	}
	if gotScope != scope {
		t.Fatalf("provider scope = %#v, want owner-origin scope %#v", gotScope, scope)
	}
	if emission.Kind != conversation.EmissionKindReply || emission.UserMessage.To != "external_peer" || emission.UserMessage.Text != emittedText || emission.Delegation != nil {
		t.Fatalf("unexpected board owner request emission: %#v", emission)
	}
	if emission.UserMessage.Text == snapshot.Messages[0].Content {
		t.Fatal("emission directly forwarded the owner instruction instead of composing message content")
	}
	if gotRequest.ResponseFormat != llm.ResponseFormatJSONObject || gotRequest.OnToken != nil {
		t.Fatalf("provider request must be non-stream JSON: format=%q callback=%t", gotRequest.ResponseFormat, gotRequest.OnToken != nil)
	}
	if findPromptMessage(gotRequest.Messages, func(message llm.Message) bool {
		return message.Type == llm.PromptContextCharacter && message.Content == "authored Mio system persona"
	}) < 0 || !promptContains(gotRequest.Messages, snapshot.Messages[0].Content) {
		t.Fatalf("provider request lost Mio persona or owner instruction: %#v", gotRequest.Messages)
	}
	contractIndex := findPromptMessage(gotRequest.Messages, func(message llm.Message) bool {
		return message.Metadata["runtime_context_kind"] == "mio_emission_contract"
	})
	if contractIndex < 0 {
		t.Fatalf("board owner request contract is missing: %#v", gotRequest.Messages)
	}
	contract := gotRequest.Messages[contractIndex].Content
	for _, required := range []string{
		"single server-bound peer",
		"Do not choose a recipient, thread, or channel",
		"Do not call tools",
		"Do not claim",
	} {
		if !strings.Contains(contract, required) {
			t.Errorf("board owner request contract is missing %q: %s", required, contract)
		}
	}
	if !strings.Contains(contract, `"kind":"reply"`) && !strings.Contains(contract, `"kind": "reply"`) {
		t.Errorf("board owner request contract lacks the reply-only emission example: %s", contract)
	}
	if !strings.Contains(contract, `"to":"external_peer"`) && !strings.Contains(contract, `"to": "external_peer"`) {
		t.Errorf("board owner request contract does not bind user_message.to to external_peer: %s", contract)
	}
	if strings.Contains(contract, `"kind":"delegate"`) || strings.Contains(contract, `"kind": "delegate"`) ||
		strings.Contains(contract, `"delegation"`) || strings.Contains(contract, "Reply only in the current thread") {
		t.Fatalf("board owner request contract exposes delegation or current-thread reply authority: %s", contract)
	}
	for _, key := range []string{"tools", "tool_choice", "functions", "function_call"} {
		if _, ok := gotRequest.ProviderOptions[key]; ok {
			t.Fatalf("provider request exposes tool option %q", key)
		}
	}
	if receipt.RequestID != string(requestID) || receipt.ResponseID != string(responseID) ||
		receipt.ProviderLabel != "mio-local" || receipt.TokensUsed != 17 || receipt.Status != "completed" {
		t.Fatalf("invocation receipt did not retain provider response fields: %#v", receipt)
	}

	t.Run("rejects assignee authority before provider", func(t *testing.T) {
		forbiddenCalls := 0
		forbiddenProvider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
			forbiddenCalls++
			return llm.GenerateResponse{}, nil
		})
		forbiddenAllowlist := conversation.EmissionAllowlist{
			AllowedAssignees:        []string{"shiro"},
			AllowedSourceMessageIDs: append([]core.MessageID(nil), composed.AllowedSourceMessageIDs...),
		}
		_, _, forbiddenErr := NewMioEmissionGenerator(forbiddenProvider).GenerateEmission(context.Background(), scope, composed, forbiddenAllowlist)
		if forbiddenErr == nil {
			t.Fatal("GenerateEmission() error = nil, want board owner request assignee authority rejection")
		}
		if forbiddenCalls != 0 {
			t.Fatalf("provider calls = %d, want 0 when assignee authority is present", forbiddenCalls)
		}
	})
}
