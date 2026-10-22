package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMioEmissionGeneratorCreatesOnePrivateInstructionFromExplicitClarificationReply(t *testing.T) {
	scope, snapshot, input := clarificationReplyGeneratorFixture()
	var calls int
	var gotScope conversation.ConversationScope
	var gotRequest llm.GenerateRequest
	provider := conversationGenerateFunc(func(_ context.Context, receivedScope conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		gotScope, gotRequest = receivedScope, request
		return llm.GenerateResponse{
			Content:   `{"schema_version":"mio.clarification_reply.v1","instruction":"Use the explicitly selected staging region and preserve the required scope."}`,
			RequestID: core.NewRequestID(), ResponseID: core.NewResponseID(), TokensUsed: 23,
		}, nil
	})

	instruction, receipt, err := NewMioEmissionGenerator(provider).GenerateClarificationReplyInstruction(context.Background(), scope, snapshot, input)
	if err != nil {
		t.Fatalf("GenerateClarificationReplyInstruction() error = %v", err)
	}
	if calls != 1 || gotScope != scope || gotRequest.ResponseFormat != llm.ResponseFormatJSONObject || gotRequest.OnToken != nil {
		t.Fatalf("generation calls=%d scope=%#v format=%q stream=%t", calls, gotScope, gotRequest.ResponseFormat, gotRequest.OnToken != nil)
	}
	if !strings.Contains(instruction, "staging region") || receipt.Status != "completed" || receipt.TokensUsed != 23 {
		t.Fatalf("instruction/receipt = %q / %#v", instruction, receipt)
	}
	if !hasPromptMessage(gotRequest.Messages, "original public task") ||
		!hasPromptMessage(gotRequest.Messages, "Which region should I use?") ||
		!hasPromptMessage(gotRequest.Messages, "Explicit public answer from the authenticated user") ||
		!hasPromptMessage(gotRequest.Messages, "Use the staging region.") ||
		!hasPromptMessage(gotRequest.Messages, `"goal":"Investigate the task"`) ||
		!hasPromptMessage(gotRequest.Messages, `"key":"keep_scope"`) ||
		!hasPromptMessage(gotRequest.Messages, `"allowed_capabilities":["conversation.read"]`) {
		t.Fatalf("prompt omitted labeled reply or canonical handoff fields: %#v", gotRequest.Messages)
	}
	var privateQuestion, publicAnswer, contract bool
	for _, message := range gotRequest.Messages {
		switch message.Metadata["runtime_context_kind"] {
		case "agent_clarification_question":
			privateQuestion = message.Type == llm.PromptContextVariable && message.Metadata["visibility"] == "private" && message.Metadata["source_actor_id"] == input.QuestionSender
		case "public_clarification_answer":
			publicAnswer = message.Type == llm.PromptContextVariable && message.Metadata["visibility"] == "public" && message.Metadata["source_actor_id"] == scope.ActorID
		case "mio_clarification_reply_contract":
			contract = strings.Contains(message.Content, `assignee "shiro"`) && strings.Contains(message.Content, "Preserve the existing goal")
		}
	}
	if !privateQuestion || !publicAnswer || !contract {
		t.Fatalf("question/answer/contract labels = %t/%t/%t; messages=%#v", privateQuestion, publicAnswer, contract, gotRequest.Messages)
	}
}

func TestMioEmissionGeneratorRejectsPublicReplyOutputForClarificationContinuation(t *testing.T) {
	scope, snapshot, input := clarificationReplyGeneratorFixture()
	calls := 0
	provider := conversationGenerateFunc(func(context.Context, conversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error) {
		calls++
		return llm.GenerateResponse{Content: `{"schema_version":"mio.clarification_reply.v1","instruction":"Continue","user_message":{"to":"user","text":"private"}}`}, nil
	})
	_, receipt, err := NewMioEmissionGenerator(provider).GenerateClarificationReplyInstruction(context.Background(), scope, snapshot, input)
	if err == nil || calls != 1 || receipt.Status != "invalid_output" {
		t.Fatalf("GenerateClarificationReplyInstruction() error=%v calls=%d receipt=%#v; want strict rejection after one call", err, calls, receipt)
	}
}

func clarificationReplyGeneratorFixture() (conversation.ConversationScope, PromptSnapshot, conversation.ClarificationReplyGenerationContext) {
	sessionID := core.NewSessionID()
	answerID, questionID := core.NewMessageID(), core.NewMessageID()
	handoff := conversation.HandoffEnvelope{
		DelegationID: core.NewRequestID(), Revision: conversation.MaxClarificationRevision,
		OriginTurnID: core.NewTurnID(), OriginUserMessageID: core.NewMessageID(), SessionID: sessionID,
		SourceTraceID: core.NewTraceID(), FromActor: "mio", ToActor: "shiro",
		InstructionMessageID: core.NewMessageID(), ContextSnapshotHash: strings.Repeat("a", 64),
		AllowedCapabilities: []string{"conversation.read"}, ReplyTo: core.NewMessageID(),
		Goal: "Investigate the task", Instruction: "Inspect the deployment behavior.",
		Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay within the original request."}},
		ExpectedOutput: "A concise report with evidence",
	}
	scope := conversation.ConversationScope{
		TurnID: core.NewTurnID(), SessionID: sessionID, TraceID: core.NewTraceID(), ActorID: "viewer-user",
		InputID: answerID, GenerationID: "clarification-reply-generation", Purpose: conversation.PurposeClarificationReply,
		PolicyVersion: "policy-v1",
	}
	snapshot := PromptSnapshot{
		Messages: []llm.Message{
			{Role: "user", Content: "original public task"},
			{Role: "assistant", Content: "Mio delegated the task"},
			{Role: "user", Content: "Use the staging region."},
		},
		AllowedSourceMessageIDs: []core.MessageID{handoff.OriginUserMessageID, core.NewMessageID(), answerID},
		SourceRevision:          "handoff:" + handoff.ContextSnapshotHash, CutoverRevision: "canonical",
	}
	input := conversation.ClarificationReplyGenerationContext{
		Handoff: handoff, QuestionMessageID: questionID, QuestionSender: handoff.ToActor,
		QuestionText: "Which region should I use?",
	}
	return scope, snapshot, input
}
