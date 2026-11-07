package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type conversationGenerateFunc func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error)

func (f conversationGenerateFunc) GenerateConversation(ctx context.Context, scope conversation.ConversationScope, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return f(ctx, scope, req)
}

func (f conversationGenerateFunc) Name() string { return "mio-local" }

func emissionTestScope() conversation.ConversationScope {
	return conversation.ConversationScope{
		TurnID:        core.NewTurnID(),
		SessionID:     core.NewSessionID(),
		TraceID:       core.NewTraceID(),
		ActorID:       "user-1",
		InputID:       core.NewMessageID(),
		GenerationID:  "generation-1",
		Purpose:       conversation.PurposeMioTurn,
		PolicyVersion: "policy-v1",
	}
}

func emissionTestSnapshot() PromptSnapshot {
	return PromptSnapshot{
		Messages: []llm.Message{
			{Role: "system", Content: "Mio persona", Type: llm.PromptContextCharacter},
			{Role: "user", Content: "Please investigate the wait", Type: llm.PromptContextUser},
		},
		AllowedSourceMessageIDs: []core.MessageID{core.NewMessageID()},
		SourceRevision:          "source-rev-7",
		CutoverRevision:         "cutover-rev-3",
	}
}

func TestMioEmissionGeneratorUsesOneScopedStructuredGeneration(t *testing.T) {
	var calls int
	var gotScope conversation.ConversationScope
	var gotRequest llm.GenerateRequest
	provider := conversationGenerateFunc(func(_ context.Context, scope conversation.ConversationScope, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		gotScope = scope
		gotRequest = req
		return llm.GenerateResponse{Content: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"I will check the wait."}}`, TokensUsed: 19, RequestID: core.NewRequestID(), ResponseID: core.NewResponseID()}, nil
	})
	generator := NewMioEmissionGenerator(provider)
	scope := emissionTestScope()
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}

	emission, receipt, err := generator.GenerateEmission(context.Background(), scope, snapshot, allowlist)
	if err != nil {
		t.Fatalf("GenerateEmission() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("GenerateConversation calls = %d, want exactly one", calls)
	}
	if gotScope != scope {
		t.Fatalf("provider scope = %#v, want %#v", gotScope, scope)
	}
	if gotRequest.ResponseFormat != llm.ResponseFormatJSONObject || gotRequest.OnToken != nil {
		t.Fatalf("provider request must be non-stream JSON: format=%q callback=%t", gotRequest.ResponseFormat, gotRequest.OnToken != nil)
	}
	if emission.Kind != conversation.EmissionKindReply || emission.UserMessage.Text != "I will check the wait." {
		t.Fatalf("unexpected emission: %#v", emission)
	}
	if receipt.RequestID == "" || receipt.TokensUsed != 19 || receipt.ProviderLabel != "mio-local" || receipt.Status != "completed" {
		t.Fatalf("incomplete RAM invocation receipt: %#v", receipt)
	}
	if !containsEmissionInstruction(gotRequest.Messages) {
		t.Fatal("request does not include the AgentEmission output contract")
	}
}

func TestMioEmissionGeneratorRejectsInvalidScopeWithoutCallingProvider(t *testing.T) {
	calls := 0
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		return llm.GenerateResponse{}, nil
	})
	scope := emissionTestScope()
	scope.TurnID = ""

	_, _, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), scope, emissionTestSnapshot(), conversation.EmissionAllowlist{})
	if err == nil {
		t.Fatal("GenerateEmission() error = nil, want invalid scope rejection")
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for invalid scope", calls)
	}
}

func TestMioEmissionGeneratorRejectsClarificationReplyPurposeWithoutCallingProvider(t *testing.T) {
	calls := 0
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		return llm.GenerateResponse{Content: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"I will continue."}}`}, nil
	})
	scope := emissionTestScope()
	scope.Purpose = conversation.PurposeClarificationReply
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}
	_, _, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), scope, snapshot, allowlist)
	if err == nil || calls != 0 {
		t.Fatalf("GenerateEmission(clarification_reply) error=%v provider calls=%d; want rejection before generation", err, calls)
	}
}

func TestMioEmissionGeneratorRejectsInvalidOutputWithoutRetry(t *testing.T) {
	calls := 0
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		return llm.GenerateResponse{Content: `{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"user","text":"I asked Midori."},"delegation":{"to":"midori","goal":"investigate","instruction":"inspect","constraints":[],"expected_output":"report","source_message_ids":[]}}`}, nil
	})
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}

	_, _, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), emissionTestScope(), snapshot, allowlist)
	if err == nil {
		t.Fatal("GenerateEmission() error = nil, want invalid assignee rejection")
	}
	if calls != 1 {
		t.Fatalf("GenerateConversation calls = %d, want one attempt and no retry", calls)
	}
}

func TestMioEmissionGeneratorRejectsInvalidReservedProductionStage(t *testing.T) {
	calls := 0
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		return llm.GenerateResponse{Content: `{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"user","text":"I will ask Midori to draft this."},"delegation":{"to":"midori","goal":"Draft an introduction","instruction":"Write a concise draft.","constraints":[{"key":"rencrow_production_stage_v1","text":"UNTRUSTED_INVALID_STAGE","source_message_ids":[]}],"expected_output":"A short draft","source_message_ids":[]}}`}, nil
	})
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{
		AllowedAssignees:        []string{"midori"},
		AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...),
	}

	_, receipt, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), emissionTestScope(), snapshot, allowlist)
	if err == nil || receipt.Status != "invalid_output" || calls != 1 {
		t.Fatalf("GenerateEmission() error=%v receipt=%+v calls=%d; want bounded invalid-output rejection after one provider call", err, receipt, calls)
	}
	if strings.Contains(err.Error(), "UNTRUSTED_INVALID_STAGE") {
		t.Fatalf("validation error echoed untrusted stage text: %v", err)
	}
}

func TestMioEmissionGeneratorRejectsMalformedOrDuplicateTextRuneConstraint(t *testing.T) {
	for _, test := range []struct {
		name        string
		constraints string
	}{
		{name: "approximate", constraints: `{"key":"rencrow_production_stage_v1","text":"author","source_message_ids":[]},{"key":"rencrow_production_text_runes_v2","text":"about 300","source_message_ids":[]}`},
		{name: "duplicate", constraints: `{"key":"rencrow_production_stage_v1","text":"author","source_message_ids":[]},{"key":"rencrow_production_text_runes_v2","text":"160..160","source_message_ids":[]},{"key":"rencrow_production_text_runes_v2","text":"160..160","source_message_ids":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
				calls++
				return llm.GenerateResponse{Content: `{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"user","text":"I will ask Midori to draft this."},"delegation":{"to":"midori","goal":"Draft an introduction","instruction":"Write a concise draft.","constraints":[` + test.constraints + `],"expected_output":"A short draft","source_message_ids":[]}}`}, nil
			})
			snapshot := emissionTestSnapshot()
			allowlist := conversation.EmissionAllowlist{
				AllowedAssignees:        []string{"midori"},
				AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...),
			}
			_, receipt, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), emissionTestScope(), snapshot, allowlist)
			if err == nil || receipt.Status != "invalid_output" || calls != 1 {
				t.Fatalf("GenerateEmission() error=%v receipt=%+v calls=%d; want malformed constraint rejected before accepted handoff", err, receipt, calls)
			}
			if strings.Contains(err.Error(), "about 300") || strings.Contains(err.Error(), "160..160") {
				t.Fatalf("validation error echoed untrusted constraint text: %v", err)
			}
		})
	}
}

func TestMioEmissionGeneratorDoesNotGenerateWhenSnapshotAndAllowlistDisagree(t *testing.T) {
	calls := 0
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		return llm.GenerateResponse{}, nil
	})
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: []core.MessageID{core.NewMessageID()}}

	_, _, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), emissionTestScope(), snapshot, allowlist)
	if err == nil {
		t.Fatal("GenerateEmission() error = nil, want source scope mismatch rejection")
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for source scope mismatch", calls)
	}
}

func TestMioEmissionGeneratorReturnsProviderFailureWithoutRetry(t *testing.T) {
	calls := 0
	wantErr := errors.New("gateway unavailable")
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		return llm.GenerateResponse{}, wantErr
	})
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}

	_, _, err := NewMioEmissionGenerator(provider).GenerateEmission(context.Background(), emissionTestScope(), snapshot, allowlist)
	if !errors.Is(err, wantErr) {
		t.Fatalf("GenerateEmission() error = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("GenerateConversation calls = %d, want 1", calls)
	}
}

func TestPromptSnapshotDeepCopyIncludesSourceAndCutoverRevisions(t *testing.T) {
	snapshot := emissionTestSnapshot()
	copy := snapshot.Clone()
	snapshot.Messages[0].Content = "mutated source"
	snapshot.Messages[0].Metadata = map[string]string{"runtime_context_kind": "mutated"}
	snapshot.Messages[0].Parts = []llm.MessagePart{{Type: llm.MessagePartImage, Data: []byte("mutated")}}
	snapshot.AllowedSourceMessageIDs[0] = core.NewMessageID()
	if copy.Messages[0].Content != "Mio persona" || copy.Messages[0].Metadata != nil || len(copy.Messages[0].Parts) != 0 {
		t.Fatalf("snapshot clone shares mutable prompt data: %#v", copy.Messages[0])
	}
	if copy.SourceRevision != "source-rev-7" || copy.CutoverRevision != "cutover-rev-3" || len(copy.AllowedSourceMessageIDs) != 1 {
		t.Fatalf("snapshot clone lost provenance: %#v", copy)
	}
}

func containsEmissionInstruction(messages []llm.Message) bool {
	for _, message := range messages {
		if message.Role == "system" && strings.Contains(message.Content, "schema_version") && strings.Contains(message.Content, "delegation") {
			return true
		}
	}
	return false
}
