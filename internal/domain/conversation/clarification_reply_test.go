package conversation

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func clarificationReplyScope() ConversationScope {
	return ConversationScope{
		TurnID: core.NewTurnID(), SessionID: core.NewSessionID(), TraceID: core.NewTraceID(),
		ActorID: "viewer-user", InputID: core.NewMessageID(), GenerationID: "clarification-reply-1",
		Purpose: PurposeClarificationReply, PolicyVersion: "policy-v1",
	}
}

func TestParseClarificationReplyInstructionAcceptsOnlyInstruction(t *testing.T) {
	scope := clarificationReplyScope()
	got, err := ParseClarificationReplyInstruction([]byte(`{"schema_version":"mio.clarification_reply.v1","instruction":"Use the user's stated staging region and retain the existing constraints."}`), scope)
	if err != nil {
		t.Fatalf("ParseClarificationReplyInstruction() error = %v", err)
	}
	if got.SchemaVersion != "mio.clarification_reply.v1" || !strings.Contains(got.Instruction, "staging region") {
		t.Fatalf("parsed clarification instruction = %#v", got)
	}
}

func TestParseClarificationReplyInstructionRejectsPublicOrRoutingFields(t *testing.T) {
	scope := clarificationReplyScope()
	for _, raw := range []string{
		`{"schema_version":"mio.clarification_reply.v1","instruction":"Continue","to":"kuro"}`,
		`{"schema_version":"mio.clarification_reply.v1","instruction":"Continue","user_message":{"to":"user","text":"private question"}}`,
		`{"schema_version":"mio.clarification_reply.v1","instruction":"Continue","allowed_capabilities":["*"]}`,
		`{"schema_version":"mio.clarification_reply.v1","instruction":"Continue"} trailing`,
		`{"schema_version":"mio.clarification_reply.v1","instruction":"Continue","instruction":"Another"}`,
	} {
		if _, err := ParseClarificationReplyInstruction([]byte(raw), scope); err == nil {
			t.Errorf("ParseClarificationReplyInstruction(%s) error = nil, want strict schema rejection", raw)
		}
	}
	if _, err := ParseClarificationReplyInstruction([]byte(`{"schema_version":"mio.clarification_reply.v1","instruction":"Continue"}`), clarificationTestScope()); err == nil {
		t.Fatal("ParseClarificationReplyInstruction(control purpose) accepted a non-reply scope")
	}
}

func TestClarificationReplyGenerationContextBindsQuestionToSameHandoff(t *testing.T) {
	handoff := contractTestHandoff()
	scope := clarificationReplyScope()
	scope.SessionID = handoff.SessionID
	input := ClarificationReplyGenerationContext{
		Handoff: handoff, QuestionMessageID: core.NewMessageID(), QuestionSender: handoff.ToActor,
		QuestionText: "Which region should I use?",
	}
	if err := input.Validate(scope); err != nil {
		t.Fatalf("ClarificationReplyGenerationContext.Validate() error = %v", err)
	}
	input.QuestionSender = "kuro"
	if err := input.Validate(scope); err == nil {
		t.Fatal("ClarificationReplyGenerationContext.Validate() accepted a question from another assignee")
	}
	input.QuestionSender = handoff.ToActor
	input.QuestionMessageID = scope.InputID
	if err := input.Validate(scope); err == nil {
		t.Fatal("ClarificationReplyGenerationContext.Validate() accepted the answer ID as private question ID")
	}
}
