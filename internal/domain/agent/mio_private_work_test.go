package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMioAgentExecutePrivateWorkUsesSelectedCharacterAndOneTextCall(t *testing.T) {
	for _, actor := range []string{"mio", "shiro"} {
		t.Run(actor, func(t *testing.T) {
			input := privateWorkTestInput(t, "task text").WithViewerRecipient(actor)
			ctx := privateWorkTestContext(t, input, actor)
			providerCalls, beginCalls, commitCalls, recentContextCalls := 0, 0, 0, 0
			var gotRequest llm.GenerateRequest
			provider := &mockLLMProvider{generateFunc: func(gotCtx context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
				providerCalls++
				gotRequest = request
				identity, err := execution.IdentityFromContext(gotCtx)
				if err != nil || identity.TaskID != input.RootTaskID() || identity.TraceID != input.TraceID() {
					t.Errorf("provider execution identity = %#v, %v", identity, err)
				}
				observation, ok := llm.ExecutionObservationFromContext(gotCtx)
				if !ok || observation.TaskID != input.RootTaskID() || observation.TraceID != string(input.TraceID()) || observation.Initiator != actor || observation.Caller != "agent."+actor {
					t.Errorf("provider execution observation = %#v, present=%v", observation, ok)
				}
				return llm.GenerateResponse{Content: "private result"}, nil
			}}
			mio := NewMioAgent(provider, nil, nil, nil, nil, nil).
				WithSystemPrompt("Mio character").
				WithViewerRecipientPrompts(map[string]string{"shiro": "Shiro character"}).
				WithGenerationOptions(MioGenerationOptions{MaxTokens: 77, Temperature: 0.3})
			mio.conversationEngine = &mockConversationEngine{
				beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
					beginCalls++
					return nil, nil
				},
				commitTurnFunc: func(context.Context, conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error) {
					commitCalls++
					return conversation.ConversationTurnResult{}, nil
				},
			}
			mio.WithRecentContextProvider(func(context.Context, int) (string, error) {
				recentContextCalls++
				return "unexpected recalled context", nil
			})

			got, err := mio.ExecutePrivateWork(ctx, input, nil)
			if err != nil || got != "private result" {
				t.Fatalf("ExecutePrivateWork() = %q, %v", got, err)
			}
			if providerCalls != 1 || beginCalls != 0 || commitCalls != 0 || recentContextCalls != 0 {
				t.Fatalf("provider=%d conversation begin=%d commit=%d recent-context=%d; want one provider call and no recall/commit", providerCalls, beginCalls, commitCalls, recentContextCalls)
			}
			if gotRequest.ResponseFormat != llm.ResponseFormatText || gotRequest.MaxTokens != 77 || gotRequest.Temperature != 0.3 {
				t.Fatalf("generation request format=%q max_tokens=%d temperature=%v", gotRequest.ResponseFormat, gotRequest.MaxTokens, gotRequest.Temperature)
			}
			if len(gotRequest.Messages) != 3 || gotRequest.Messages[0].Type != llm.PromptContextCharacter || gotRequest.Messages[0].Content != map[string]string{"mio": "Mio character", "shiro": "Shiro character"}[actor] {
				t.Fatalf("character prompt messages = %#v", gotRequest.Messages)
			}
			if gotRequest.Messages[1].Metadata["runtime_context_kind"] != "time" {
				t.Fatalf("expected only the generationRequest time context, got %#v", gotRequest.Messages[1])
			}
			if gotRequest.Messages[2].Role != "user" || gotRequest.Messages[2].Content != input.MessageText() || gotRequest.Messages[2].Type != llm.PromptContextUser {
				t.Fatalf("task input message = %#v", gotRequest.Messages[2])
			}
		})
	}
}

func TestMioAgentExecutePrivateWorkRejectsUnsupportedRecipientOrCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name         string
		recipient    string
		capabilities []string
	}{
		{name: "empty recipient"},
		{name: "unsupported recipient", recipient: "kuro"},
		{name: "noncanonical recipient", recipient: "Mio"},
		{name: "capability granted", recipient: "mio", capabilities: []string{"codex.run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := privateWorkTestInput(t, "task text").WithViewerRecipient(tc.recipient)
			ctx := context.Background()
			if tc.recipient == "mio" || tc.recipient == "shiro" {
				ctx = privateWorkTestContext(t, input, tc.recipient)
			}
			providerCalls := 0
			mio := NewMioAgent(&mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
				providerCalls++
				return llm.GenerateResponse{Content: "unexpected"}, nil
			}}, nil, nil, nil, nil, nil)
			_, err := mio.ExecutePrivateWork(ctx, input, tc.capabilities)
			if err == nil {
				t.Fatal("expected private work to be rejected")
			}
			if tc.capabilities != nil && !errors.Is(err, ErrPrivateWorkCapabilityDenied) {
				t.Fatalf("capability error = %v, want ErrPrivateWorkCapabilityDenied", err)
			}
			if providerCalls != 0 {
				t.Fatalf("provider calls=%d, want 0", providerCalls)
			}
		})
	}
}

func TestMioAgentExecutePrivateWorkRejectsActorTaskAndTraceMismatch(t *testing.T) {
	input := privateWorkTestInput(t, "task text").WithViewerRecipient("mio")
	providerCalls := 0
	mio := NewMioAgent(&mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		providerCalls++
		return llm.GenerateResponse{Content: "unexpected"}, nil
	}}, nil, nil, nil, nil, nil)

	tests := []struct {
		name string
		ctx  func() context.Context
	}{
		{name: "actor", ctx: func() context.Context { return privateWorkTestContext(t, input, "shiro") }},
		{name: "task", ctx: func() context.Context {
			ctx, err := execution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), input.TraceID())
			if err != nil {
				t.Fatal(err)
			}
			return withPrivateWorkActorScope(t, ctx, "mio")
		}},
		{name: "trace", ctx: func() context.Context {
			ctx, err := execution.WithIdentity(context.Background(), input.RootTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
			if err != nil {
				t.Fatal(err)
			}
			return withPrivateWorkActorScope(t, ctx, "mio")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mio.ExecutePrivateWork(tc.ctx(), input, nil); err == nil {
				t.Fatal("expected mismatched private work identity to be rejected")
			}
		})
	}
	if providerCalls != 0 {
		t.Fatalf("provider calls=%d, want 0", providerCalls)
	}
}

func TestMioAgentExecutePrivateWorkRequiresInitializedProvider(t *testing.T) {
	input := privateWorkTestInput(t, "task text").WithViewerRecipient("mio")
	mio := NewMioAgent(nil, nil, nil, nil, nil, nil)
	if _, err := mio.ExecutePrivateWork(privateWorkTestContext(t, input, "mio"), input, nil); err == nil {
		t.Fatal("expected unavailable provider to fail")
	}
}
