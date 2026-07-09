package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestHeavyAgentGenerateUsesHeavyPromptAndStripsCommand(t *testing.T) {
	var gotReq llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			gotReq = req
			return llm.GenerateResponse{Content: "heavy response"}, nil
		},
	}

	heavy := NewHeavyAgent(provider, "kuro system")
	resp, err := heavy.Generate(context.Background(), task.NewTask(task.NewJobID(), "/analyze 原因を調べて", "line", "U123"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp != "heavy response" {
		t.Fatalf("response: want heavy response, got %q", resp)
	}
	if gotReq.SystemPrompt != "kuro system" {
		t.Fatalf("system prompt: want kuro system, got %q", gotReq.SystemPrompt)
	}
	if len(gotReq.Messages) == 0 || gotReq.Messages[len(gotReq.Messages)-1].Content != "原因を調べて" {
		t.Fatalf("expected stripped user message, got %#v", gotReq.Messages)
	}
}

func TestHeavyAgentDefaultPrompt(t *testing.T) {
	var gotReq llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			gotReq = req
			return llm.GenerateResponse{Content: "ok"}, nil
		},
	}

	heavy := NewHeavyAgent(provider, "")
	_, err := heavy.Generate(context.Background(), task.NewTask(task.NewJobID(), "診断して", "line", "U123"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if !strings.Contains(gotReq.SystemPrompt, "Heavy") {
		t.Fatalf("expected default Heavy prompt, got %q", gotReq.SystemPrompt)
	}
}

func TestHeavyAgentGenerateWithConversationEngine(t *testing.T) {
	beginCalled := false
	endCalled := false
	var gotReq llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			gotReq = req
			return llm.GenerateResponse{Content: "  heavy response  "}, nil
		},
	}
	engine := &mockConversationEngine{
		beginTurnFunc: func(ctx context.Context, sessionID, userMessage string) (*conversation.RecallPack, error) {
			beginCalled = true
			if userMessage != "調べて" {
				t.Fatalf("userMessage=%q", userMessage)
			}
			return &conversation.RecallPack{
				Persona:      conversation.PersonaState{Name: "Heavy"},
				ShortContext: []conversation.Message{{Speaker: conversation.SpeakerUser, Msg: "before"}},
			}, nil
		},
		endTurnFunc: func(ctx context.Context, sessionID, userMessage, response string) error {
			endCalled = true
			if response != "heavy response" {
				t.Fatalf("response=%q", response)
			}
			return nil
		},
	}

	heavy := NewHeavyAgent(provider, "heavy system").WithConversationEngine(engine)
	resp, err := heavy.Generate(context.Background(), task.NewTask(task.NewJobID(), "/heavy 調べて", "viewer", "chat-1"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp != "heavy response" || !beginCalled || !endCalled {
		t.Fatalf("resp=%q begin=%v end=%v", resp, beginCalled, endCalled)
	}
	if len(gotReq.Messages) < 2 {
		t.Fatalf("expected recall and user messages: %#v", gotReq.Messages)
	}
}
