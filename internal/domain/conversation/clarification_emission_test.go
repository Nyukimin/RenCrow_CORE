package conversation

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestConversationScopeAcceptsClarificationControlPurpose(t *testing.T) {
	scope := ConversationScope{
		TurnID: core.NewTurnID(), SessionID: core.NewSessionID(), TraceID: core.NewTraceID(),
		ActorID: "mio", InputID: core.NewMessageID(), GenerationID: "clarification-control-1",
		Purpose: ConversationPurpose("clarification_control"), PolicyVersion: "policy-v1",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("ConversationScope.Validate() error = %v, want typed clarification control scope", err)
	}
}

func TestParseClarificationDecisionAcceptsOnlyAskOrSameAssigneeContinuation(t *testing.T) {
	scope := clarificationTestScope()
	for _, test := range []struct {
		name     string
		input    string
		wantKind ClarificationDecisionKind
	}{
		{
			name:     "ask user",
			input:    `{"schema_version":"mio.clarification.v1","kind":"ask_user","question":"Which date range should Shiro use?"}`,
			wantKind: ClarificationDecisionAskUser,
		},
		{
			name:     "continue same delegation",
			input:    `{"schema_version":"mio.clarification.v1","kind":"continue_delegation","instruction":"Use the date range from the original request and report the result."}`,
			wantKind: ClarificationDecisionContinueDelegation,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := ParseClarificationDecision([]byte(test.input), scope)
			if err != nil {
				t.Fatalf("ParseClarificationDecision() error = %v", err)
			}
			if decision.Kind != test.wantKind {
				t.Fatalf("decision kind = %q, want %q", decision.Kind, test.wantKind)
			}
		})
	}
}

func TestParseClarificationDecisionRejectsIdentityAndAuthorityFields(t *testing.T) {
	scope := clarificationTestScope()
	for _, input := range []string{
		`{"schema_version":"mio.clarification.v1","kind":"ask_user","question":"Which date?","delegation_id":"forged","revision":99}`,
		`{"schema_version":"mio.clarification.v1","kind":"continue_delegation","instruction":"Continue","assignee":"kuro"}`,
		`{"schema_version":"mio.clarification.v1","kind":"continue_delegation","instruction":"Continue","constraints":[],"allowed_capabilities":["*"],"user_message":{"to":"user","text":"hello"}}`,
	} {
		if _, err := ParseClarificationDecision([]byte(input), scope); err == nil {
			t.Errorf("ParseClarificationDecision(%s) error = nil, want unauthorized field rejection", input)
		}
	}
}

func TestClarificationControlContextRequiresCurrentPrivateQuestionBinding(t *testing.T) {
	handoff := contractTestHandoff()
	questionID := core.NewMessageID()
	scope := ConversationScope{
		TurnID: core.NewTurnID(), SessionID: handoff.SessionID, TraceID: handoff.SourceTraceID,
		ActorID: "mio", InputID: questionID, GenerationID: "clarification-control-1",
		Purpose: PurposeClarificationControl, PolicyVersion: "policy-v1",
	}
	input := ClarificationControlContext{
		Handoff: handoff, QuestionDelegationID: handoff.DelegationID, QuestionRevision: handoff.Revision,
		QuestionMessageID: questionID, QuestionSender: handoff.ToActor,
		QuestionText: "Which date range should I use?",
	}
	if err := input.Validate(scope); err != nil {
		t.Fatalf("ClarificationControlContext.Validate() error = %v", err)
	}
	input.QuestionSender = "kuro"
	if err := input.Validate(scope); err == nil {
		t.Fatal("ClarificationControlContext.Validate() error = nil, want assignee binding rejection")
	}
	input.QuestionSender = handoff.ToActor
	input.QuestionRevision--
	if err := input.Validate(scope); err == nil {
		t.Fatal("ClarificationControlContext.Validate() error = nil, want stale revision rejection")
	}
}

func clarificationTestScope() ConversationScope {
	return ConversationScope{
		TurnID: core.NewTurnID(), SessionID: core.NewSessionID(), TraceID: core.NewTraceID(),
		ActorID: "mio", InputID: core.NewMessageID(), GenerationID: "clarification-control-1",
		Purpose: PurposeClarificationControl, PolicyVersion: "policy-v1",
	}
}

func TestClarificationDecisionBoundsQuestionAndContinuationText(t *testing.T) {
	scope := clarificationTestScope()
	question := strings.Repeat("q", MaxUserMessageBytes+1)
	if _, err := ParseClarificationDecision([]byte(`{"schema_version":"mio.clarification.v1","kind":"ask_user","question":"`+question+`"}`), scope); err == nil {
		t.Fatal("oversized user question was accepted")
	}
	instruction := strings.Repeat("i", MaxInstructionBytes+1)
	if _, err := ParseClarificationDecision([]byte(`{"schema_version":"mio.clarification.v1","kind":"continue_delegation","instruction":"`+instruction+`"}`), scope); err == nil {
		t.Fatal("oversized continuation instruction was accepted")
	}
}
