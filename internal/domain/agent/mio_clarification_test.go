package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMioEmissionGeneratorCreatesBoundClarificationDecision(t *testing.T) {
	var calls int
	var gotScope conversation.ConversationScope
	var gotRequest llm.GenerateRequest
	provider := conversationGenerateFunc(func(_ context.Context, scope conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		gotScope, gotRequest = scope, request
		return llm.GenerateResponse{
			Content:    `{"schema_version":"mio.clarification.v1","kind":"ask_user","question":"Which date range should Shiro use?"}`,
			TokensUsed: 13, RequestID: core.NewRequestID(), ResponseID: core.NewResponseID(),
		}, nil
	})
	generator := NewMioEmissionGenerator(provider)
	scope, control := mioClarificationTestContext()
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}

	decision, receipt, err := generator.GenerateClarificationDecision(context.Background(), scope, snapshot, control, allowlist)
	if err != nil {
		t.Fatalf("GenerateClarificationDecision() error = %v", err)
	}
	if calls != 1 || gotScope != scope {
		t.Fatalf("provider calls=%d scope=%#v, want one call with %#v", calls, gotScope, scope)
	}
	if gotRequest.ResponseFormat != llm.ResponseFormatJSONObject || gotRequest.OnToken != nil {
		t.Fatalf("clarification generation must be non-stream JSON: format=%q callback=%t", gotRequest.ResponseFormat, gotRequest.OnToken != nil)
	}
	if decision.Kind != conversation.ClarificationDecisionAskUser || decision.Question != "Which date range should Shiro use?" {
		t.Fatalf("clarification decision = %#v", decision)
	}
	if receipt.RequestID == "" || receipt.ResponseID == "" || receipt.TokensUsed != 13 || receipt.Status != "completed" {
		t.Fatalf("clarification receipt = %#v", receipt)
	}
	if !hasPromptMessage(gotRequest.Messages, "Please investigate the wait") || !hasPromptMessage(gotRequest.Messages, control.QuestionText) {
		t.Fatalf("request omitted retained user or private assignee context: %#v", gotRequest.Messages)
	}
	if !hasPromptMessage(gotRequest.Messages, `"goal":"Summarize a behavior"`) ||
		!hasPromptMessage(gotRequest.Messages, `"key":"keep_scope"`) ||
		!hasPromptMessage(gotRequest.Messages, `"allowed_capabilities":["conversation.read"]`) {
		t.Fatalf("request omitted canonical handoff constraints or capabilities: %#v", gotRequest.Messages)
	}
	if !hasClarificationContract(gotRequest.Messages) {
		t.Fatal("request omitted the same-assignee clarification decision contract")
	}
	for _, message := range gotRequest.Messages {
		if message.Metadata["runtime_context_kind"] == "agent_clarification_question" &&
			(message.Type != llm.PromptContextVariable || message.Metadata["visibility"] != "private" || message.Metadata["source_actor_id"] != control.QuestionSender) {
			t.Fatalf("private question context metadata = %#v", message.Metadata)
		}
		if message.Metadata["runtime_context_kind"] == "canonical_clarification_handoff" &&
			(message.Type != llm.PromptContextVariable || message.Metadata["visibility"] != "private") {
			t.Fatalf("canonical handoff context metadata = %#v", message.Metadata)
		}
	}
}

func TestMioEmissionGeneratorRejectsClarificationAssigneeMismatchBeforeGeneration(t *testing.T) {
	calls := 0
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		return llm.GenerateResponse{}, nil
	})
	scope, control := mioClarificationTestContext()
	control.QuestionSender = "kuro"
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}
	if _, _, err := NewMioEmissionGenerator(provider).GenerateClarificationDecision(context.Background(), scope, snapshot, control, allowlist); err == nil {
		t.Fatal("GenerateClarificationDecision() error = nil, want authenticated assignee mismatch")
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want no generation for mismatched assignee", calls)
	}
}

func TestMioEmissionGeneratorAcceptsSameAssigneeContinuationShape(t *testing.T) {
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{Content: `{"schema_version":"mio.clarification.v1","kind":"continue_delegation","instruction":"Use the date range in the original user request and return the report."}`}, nil
	})
	scope, control := mioClarificationTestContext()
	snapshot := emissionTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]core.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}
	decision, _, err := NewMioEmissionGenerator(provider).GenerateClarificationDecision(context.Background(), scope, snapshot, control, allowlist)
	if err != nil {
		t.Fatalf("GenerateClarificationDecision(continuation) error = %v", err)
	}
	if decision.Kind != conversation.ClarificationDecisionContinueDelegation || !strings.Contains(decision.Instruction, "original user request") {
		t.Fatalf("continuation decision = %#v", decision)
	}
}

func mioClarificationTestContext() (conversation.ConversationScope, conversation.ClarificationControlContext) {
	handoff := conversation.HandoffEnvelope{
		DelegationID: core.NewRequestID(), Revision: 3,
		OriginTurnID: core.NewTurnID(), OriginUserMessageID: core.NewMessageID(),
		SessionID: core.NewSessionID(), SourceTraceID: core.NewTraceID(),
		FromActor: "mio", ToActor: "shiro", InstructionMessageID: core.NewMessageID(),
		ContextSnapshotHash: strings.Repeat("a", 64), AllowedCapabilities: []string{"conversation.read"},
		ReplyTo: core.NewMessageID(), Goal: "Summarize a behavior", Instruction: "Inspect the behavior and report evidence.",
		Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay within the requested scope."}},
		ExpectedOutput: "An evidence-backed report",
	}
	questionID := core.NewMessageID()
	scope := conversation.ConversationScope{
		TurnID: core.NewTurnID(), SessionID: handoff.SessionID, TraceID: handoff.SourceTraceID,
		ActorID: "user-1", InputID: questionID, GenerationID: "clarification-generation-1",
		Purpose: conversation.PurposeClarificationControl, PolicyVersion: "policy-v1",
	}
	control := conversation.ClarificationControlContext{
		Handoff: handoff, QuestionDelegationID: handoff.DelegationID, QuestionRevision: handoff.Revision,
		QuestionMessageID: questionID, QuestionSender: handoff.ToActor,
		QuestionText: "Which date range should I use?",
	}
	return scope, control
}

func hasPromptMessage(messages []llm.Message, content string) bool {
	for _, message := range messages {
		if message.Content == content || strings.Contains(message.Content, content) {
			return true
		}
	}
	return false
}

func hasClarificationContract(messages []llm.Message) bool {
	for _, message := range messages {
		if message.Metadata["runtime_context_kind"] == "mio_clarification_contract" &&
			strings.Contains(message.Content, "same delegation ID") &&
			strings.Contains(message.Content, "mio.clarification.v1") {
			return true
		}
	}
	return false
}
