package conversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func testScope() ConversationScope {
	return ConversationScope{
		TurnID:        core.NewTurnID(),
		SessionID:     core.NewSessionID(),
		TraceID:       core.NewTraceID(),
		ActorID:       "user-1",
		InputID:       core.NewMessageID(),
		GenerationID:  "generation-1",
		Purpose:       PurposeMioTurn,
		PolicyVersion: "policy-v1",
	}
}

func testAllowlist() EmissionAllowlist {
	return EmissionAllowlist{
		AllowedAssignees:        []string{"shiro", "kuro"},
		AllowedSourceMessageIDs: []core.MessageID{core.NewMessageID(), core.NewMessageID()},
	}
}

func validReplyJSON(text string) string {
	encoded, _ := json.Marshal(text)
	return `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":` + string(encoded) + `}}`
}

func validDelegateJSON(assignee string, sourceID string) string {
	return fmt.Sprintf(`{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"user","text":"I’ll ask %s."},"delegation":{"to":%q,"goal":"Investigate the wait","instruction":"Measure the save path","constraints":[{"key":"keep_model","text":"Do not change the model","source_message_ids":[%q]}],"expected_output":"A report with evidence","source_message_ids":[%q]}}`, assignee, assignee, sourceID, sourceID)
}

func TestParseAgentEmission_Reply(t *testing.T) {
	emission, err := ParseAgentEmission([]byte(validReplyJSON("The answer is ready.")), testScope(), testAllowlist())
	if err != nil {
		t.Fatalf("ParseAgentEmission() error = %v", err)
	}
	if emission.Kind != EmissionKindReply || emission.UserMessage.To != "user" || emission.UserMessage.Text != "The answer is ready." || emission.Delegation != nil {
		t.Fatalf("unexpected reply emission: %#v", emission)
	}
}

func TestParseAgentEmission_Delegate(t *testing.T) {
	allowlist := testAllowlist()
	sourceID := string(allowlist.AllowedSourceMessageIDs[0])
	emission, err := ParseAgentEmission([]byte(validDelegateJSON("shiro", sourceID)), testScope(), allowlist)
	if err != nil {
		t.Fatalf("ParseAgentEmission() error = %v", err)
	}
	if emission.Kind != EmissionKindDelegate || emission.Delegation == nil || emission.Delegation.To != "shiro" {
		t.Fatalf("unexpected delegation emission: %#v", emission)
	}
}

func TestParseAgentEmission_ReportReplyMustBeDirectEvenWhenAssigneesAreAllowed(t *testing.T) {
	allowlist := testAllowlist()
	scope := testScope()
	scope.Purpose = PurposeReportReply
	delegate := validDelegateJSON("shiro", string(allowlist.AllowedSourceMessageIDs[0]))
	if _, err := ParseAgentEmission([]byte(delegate), scope, allowlist); err == nil {
		t.Fatal("ParseAgentEmission(report_reply delegate) error = nil, want direct-reply-only rejection")
	}
	if _, err := ParseAgentEmission([]byte(validReplyJSON("Here is the result.")), scope, allowlist); err != nil {
		t.Fatalf("ParseAgentEmission(report_reply reply): %v", err)
	}
}

func TestParseAgentEmissionRejectsClarificationReplyPurpose(t *testing.T) {
	scope := testScope()
	scope.Purpose = PurposeClarificationReply
	if _, err := ParseAgentEmission([]byte(validReplyJSON("I will continue the task.")), scope, testAllowlist()); err == nil {
		t.Fatal("ParseAgentEmission(clarification_reply) error = nil, want public response rejection")
	}
}

func TestParseAgentEmission_RejectsMalformedOrOutOfScopeEmission(t *testing.T) {
	allowlist := testAllowlist()
	allowedSource := string(allowlist.AllowedSourceMessageIDs[0])
	unknownSource := string(core.NewMessageID())
	tooManySourceIDs := strings.Join(func() []string {
		values := make([]string, MaxSourceMessageIDs+1)
		for i := range values {
			values[i] = fmt.Sprintf(`"%s"`, allowedSource)
		}
		return values
	}(), ",")
	tooLargeText := strings.Repeat("x", MaxUserMessageBytes+1)
	tooLargeUnicodeText := strings.Repeat("界", MaxUserMessageBytes/3+1)
	tooLargeInstruction, _ := json.Marshal(strings.Repeat("x", MaxInstructionBytes+1))
	tooManyConstraints := make([]AgentConstraint, 2)
	tooManyConstraints[0] = AgentConstraint{Key: "same", Text: "one"}
	tooManyConstraints[1] = AgentConstraint{Key: "same", Text: "two"}
	duplicateConstraintKeys, _ := json.Marshal(AgentEmission{
		SchemaVersion: "mio.turn.v1",
		Kind:          EmissionKindDelegate,
		UserMessage:   AgentUserMessage{To: "user", Text: "I’ll delegate this."},
		Delegation: &AgentDelegation{
			To: "shiro", Goal: "Investigate", Instruction: "Measure", Constraints: tooManyConstraints,
			ExpectedOutput: "Evidence", SourceMessageIDs: []core.MessageID{allowlist.AllowedSourceMessageIDs[0]},
		},
	})
	valid := validReplyJSON("A valid response.")
	delegate := validDelegateJSON("shiro", allowedSource)
	delegateUnknownSource := validDelegateJSON("shiro", unknownSource)
	delegateLargeInstruction := strings.Replace(delegate, `"instruction":"Measure the save path"`, `"instruction":`+string(tooLargeInstruction), 1)
	tests := []struct {
		name  string
		raw   string
		scope ConversationScope
		list  EmissionAllowlist
	}{
		{name: "unknown top-level key", raw: strings.TrimSuffix(valid, "}") + `,"extra":true}`, scope: testScope(), list: allowlist},
		{name: "unknown nested key", raw: strings.Replace(valid, `"text":"A valid response."`, `"text":"A valid response.","debug":true`, 1), scope: testScope(), list: allowlist},
		{name: "case variant of known key", raw: strings.Replace(valid, `"text":"A valid response."`, `"text":"A valid response.","Text":"changed"`, 1), scope: testScope(), list: allowlist},
		{name: "duplicate key", raw: `{"schema_version":"mio.turn.v1","schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"ok"}}`, scope: testScope(), list: allowlist},
		{name: "duplicate nested key", raw: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"first","text":"second"}}`, scope: testScope(), list: allowlist},
		{name: "trailing value", raw: valid + ` {"kind":"reply"}`, scope: testScope(), list: allowlist},
		{name: "invalid reply and delegation combination", raw: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"ok"},"delegation":null}`, scope: testScope(), list: allowlist},
		{name: "empty user message", raw: validReplyJSON("  \n"), scope: testScope(), list: allowlist},
		{name: "oversize envelope", raw: strings.Repeat(" ", MaxEnvelopeBytes+1), scope: testScope(), list: allowlist},
		{name: "oversize user text", raw: validReplyJSON(tooLargeText), scope: testScope(), list: allowlist},
		{name: "oversize user text by UTF-8 bytes", raw: validReplyJSON(tooLargeUnicodeText), scope: testScope(), list: allowlist},
		{name: "unsupported emission kind", raw: strings.Replace(valid, `"reply"`, `"tool"`, 1), scope: testScope(), list: allowlist},
		{name: "unsupported schema", raw: strings.Replace(valid, `mio.turn.v1`, `mio.turn.v2`, 1), scope: testScope(), list: allowlist},
		{name: "assignee outside catalog", raw: validDelegateJSON("midori", allowedSource), scope: testScope(), list: allowlist},
		{name: "oversize delegation instruction", raw: delegateLargeInstruction, scope: testScope(), list: allowlist},
		{name: "source outside prompt snapshot", raw: delegateUnknownSource, scope: testScope(), list: allowlist},
		{name: "duplicate constraint key", raw: string(duplicateConstraintKeys), scope: testScope(), list: allowlist},
		{name: "source count over limit", raw: strings.Replace(delegate, `"source_message_ids":["`+allowedSource+`"]`, `"source_message_ids":[`+tooManySourceIDs+`]`, 1), scope: testScope(), list: allowlist},
		{name: "unsupported purpose", raw: valid, scope: func() ConversationScope { s := testScope(); s.Purpose = "maintenance"; return s }(), list: allowlist},
		{name: "missing canonical scope id", raw: valid, scope: func() ConversationScope { s := testScope(); s.TurnID = ""; return s }(), list: allowlist},
		{name: "missing scope session id", raw: valid, scope: func() ConversationScope { s := testScope(); s.SessionID = ""; return s }(), list: allowlist},
		{name: "missing scope trace id", raw: valid, scope: func() ConversationScope { s := testScope(); s.TraceID = ""; return s }(), list: allowlist},
		{name: "missing scope input id", raw: valid, scope: func() ConversationScope { s := testScope(); s.InputID = ""; return s }(), list: allowlist},
		{name: "missing scope actor", raw: valid, scope: func() ConversationScope { s := testScope(); s.ActorID = " "; return s }(), list: allowlist},
		{name: "missing generation id", raw: valid, scope: func() ConversationScope { s := testScope(); s.GenerationID = ""; return s }(), list: allowlist},
		{name: "missing policy version", raw: valid, scope: func() ConversationScope { s := testScope(); s.PolicyVersion = ""; return s }(), list: allowlist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseAgentEmission([]byte(tt.raw), tt.scope, tt.list); err == nil {
				t.Fatal("ParseAgentEmission() error = nil, want rejection")
			}
		})
	}
}

func TestParseAgentEmissionSchemaFixtures(t *testing.T) {
	const schemaResource = "https://rencrow.invalid/schemas/mio.turn.v1"
	schemaBytes, err := os.ReadFile(filepath.Join("testdata", "03_emission.schema.json"))
	if err != nil {
		t.Fatalf("read emission schema: %v", err)
	}
	schemaValue, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		t.Fatalf("parse emission schema JSON: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(schemaResource, schemaValue); err != nil {
		t.Fatalf("add emission schema: %v", err)
	}
	schema, err := compiler.Compile(schemaResource)
	if err != nil {
		t.Fatalf("compile emission schema: %v", err)
	}

	const fixtureSourceID = "msg_00000000-0000-7000-8000-000000000101"
	allowlist := EmissionAllowlist{
		AllowedAssignees:        []string{"shiro", "kuro"},
		AllowedSourceMessageIDs: []core.MessageID{core.MessageID(fixtureSourceID)},
	}
	fixtures := []struct {
		name         string
		schemaAccept bool
		parserAccept bool
	}{
		{name: "reply.valid.json", schemaAccept: true, parserAccept: true},
		{name: "delegate.valid.json", schemaAccept: true, parserAccept: true},
		{name: "reply.missing_user_text.invalid.json", schemaAccept: false, parserAccept: false},
		{name: "delegate.missing_delegation.invalid.json", schemaAccept: false, parserAccept: false},
		{name: "reply.additional_property.invalid.json", schemaAccept: false, parserAccept: false},
		{name: "reply.delegation_field.invalid.json", schemaAccept: false, parserAccept: false},
		{name: "delegate.missing_constraint_field.invalid.json", schemaAccept: false, parserAccept: false},
		{name: "delegate.blank_instruction.invalid.json", schemaAccept: false, parserAccept: false},
		{name: "delegate.too_many_source_ids.invalid.json", schemaAccept: false, parserAccept: false},
		{name: "reply.duplicate_key.parser-only.json", schemaAccept: true, parserAccept: false},
		{name: "delegate.duplicate_constraint_key.parser-only.json", schemaAccept: true, parserAccept: false},
		{name: "delegate.total_source_ids_over_limit.parser-only.json", schemaAccept: true, parserAccept: false},
		{name: "delegate.unknown_source.parser-only.json", schemaAccept: true, parserAccept: false},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "examples", fixture.name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("parse fixture JSON: %v", err)
			}
			schemaErr := schema.Validate(instance)
			if got := schemaErr == nil; got != fixture.schemaAccept {
				t.Fatalf("schema acceptance = %t (error %v), want %t", got, schemaErr, fixture.schemaAccept)
			}

			_, parserErr := ParseAgentEmission(raw, testScope(), allowlist)
			if got := parserErr == nil; got != fixture.parserAccept {
				t.Fatalf("parser acceptance = %t (error %v), want %t", got, parserErr, fixture.parserAccept)
			}
		})
	}
}
