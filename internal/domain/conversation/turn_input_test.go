package conversation

import (
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func testTurnInputAddress(t *testing.T) ChannelAddress {
	t.Helper()
	address, err := NewChannelAddress("viewer", "user-123")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	return address
}

func newTestTurnInput(t *testing.T) TurnInput {
	t.Helper()
	input, err := NewTurnInput(modulecore.NewTaskID(), "hello", testTurnInputAddress(t))
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	return input
}

func TestNewTurnInputGeneratesIndependentCanonicalIdentities(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	input, err := NewTurnInput(rootTaskID, "hello", testTurnInputAddress(t))
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("TurnInput.Validate() error = %v", err)
	}
	if got := input.RootTaskID(); got != rootTaskID {
		t.Fatalf("RootTaskID() = %q, want %q", got, rootTaskID)
	}

	identities := []struct {
		name string
		raw  string
		err  error
	}{
		{name: "turn", raw: string(input.TurnID()), err: input.TurnID().Validate()},
		{name: "trace", raw: string(input.TraceID()), err: input.TraceID().Validate()},
		{name: "root task", raw: string(input.RootTaskID()), err: input.RootTaskID().Validate()},
		{name: "user message", raw: string(input.UserMessageID()), err: input.UserMessageID().Validate()},
		{name: "agent message", raw: string(input.AgentMessageID()), err: input.AgentMessageID().Validate()},
	}
	seen := make(map[string]string, len(identities))
	for _, identity := range identities {
		if identity.err != nil {
			t.Errorf("%s identity validation error = %v", identity.name, identity.err)
		}
		if identity.raw == "" {
			t.Errorf("%s identity is empty", identity.name)
		}
		if previous, ok := seen[identity.raw]; ok {
			t.Errorf("%s identity reuses %s identity %q", identity.name, previous, identity.raw)
		}
		seen[identity.raw] = identity.name
	}
	if got, want := input.MessageText(), "hello"; got != want {
		t.Fatalf("MessageText() = %q, want %q", got, want)
	}
	if got, want := input.ChannelAddress(), testTurnInputAddress(t); got != want {
		t.Fatalf("ChannelAddress() = %#v, want %#v", got, want)
	}
}

func TestNewTurnInputRejectsInvalidRootAndAddress(t *testing.T) {
	validAddress := testTurnInputAddress(t)
	invalidAddress := ChannelAddress{channelType: "VIEWER", externalConversationID: "user-123"}
	for _, test := range []struct {
		name     string
		rootTask modulecore.TaskID
		address  ChannelAddress
	}{
		{name: "invalid root task", rootTask: modulecore.TaskID("not-a-task-id"), address: validAddress},
		{name: "invalid address", rootTask: modulecore.NewTaskID(), address: invalidAddress},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewTurnInput(test.rootTask, "hello", test.address); err == nil {
				t.Fatal("NewTurnInput() error = nil")
			}
		})
	}
}

func TestTurnInputValidateRejectsInvalidConversationIdentity(t *testing.T) {
	input := newTestTurnInput(t)
	for _, test := range []struct {
		name   string
		mutate func(*TurnInput)
	}{
		{name: "turn", mutate: func(candidate *TurnInput) { candidate.turnID = modulecore.TurnID("bad-turn") }},
		{name: "trace", mutate: func(candidate *TurnInput) { candidate.traceID = modulecore.TraceID("bad-trace") }},
		{name: "root task", mutate: func(candidate *TurnInput) { candidate.rootTaskID = modulecore.TaskID("bad-task") }},
		{name: "user message", mutate: func(candidate *TurnInput) { candidate.userMessageID = modulecore.MessageID("bad-user-message") }},
		{name: "agent message", mutate: func(candidate *TurnInput) { candidate.agentMessageID = modulecore.MessageID("bad-agent-message") }},
		{name: "same message identity", mutate: func(candidate *TurnInput) { candidate.agentMessageID = candidate.userMessageID }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := input
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("TurnInput.Validate() error = nil")
			}
		})
	}
}

func TestReconstructTurnInputPreservesAssignedIdentities(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	turnID := modulecore.NewTurnID()
	traceID := modulecore.NewTraceID()
	userMessageID := modulecore.NewMessageID()
	agentMessageID := modulecore.NewMessageID()
	input, err := ReconstructTurnInput(rootTaskID, turnID, traceID, userMessageID, agentMessageID, "restored", testTurnInputAddress(t))
	if err != nil {
		t.Fatalf("ReconstructTurnInput() error = %v", err)
	}
	if input.RootTaskID() != rootTaskID || input.TurnID() != turnID || input.TraceID() != traceID || input.UserMessageID() != userMessageID || input.AgentMessageID() != agentMessageID {
		t.Fatalf("reconstructed identity = %q/%q/%q/%q/%q", input.RootTaskID(), input.TurnID(), input.TraceID(), input.UserMessageID(), input.AgentMessageID())
	}
	if input.MessageText() != "restored" || input.ChannelAddress() != testTurnInputAddress(t) {
		t.Fatalf("reconstructed value = text=%q address=%#v", input.MessageText(), input.ChannelAddress())
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("reconstructed input Validate() error = %v", err)
	}
}

func TestTurnInputMessageRewritePreservesIdentityAndAddress(t *testing.T) {
	original := newTestTurnInput(t)
	rewritten := original.WithMessageText("rewritten")

	if got, want := original.MessageText(), "hello"; got != want {
		t.Fatalf("original MessageText() = %q, want %q", got, want)
	}
	if got, want := rewritten.MessageText(), "rewritten"; got != want {
		t.Fatalf("rewritten MessageText() = %q, want %q", got, want)
	}
	if got, want := rewritten.ChannelAddress(), original.ChannelAddress(); got != want {
		t.Fatalf("rewritten ChannelAddress() = %#v, want %#v", got, want)
	}
	if got, want := rewritten.RootTaskID(), original.RootTaskID(); got != want {
		t.Fatalf("rewritten RootTaskID() = %q, want %q", got, want)
	}
	if got, want := rewritten.TurnID(), original.TurnID(); got != want {
		t.Fatalf("rewritten TurnID() = %q, want %q", got, want)
	}
	if got, want := rewritten.TraceID(), original.TraceID(); got != want {
		t.Fatalf("rewritten TraceID() = %q, want %q", got, want)
	}
	if got, want := rewritten.UserMessageID(), original.UserMessageID(); got != want {
		t.Fatalf("rewritten UserMessageID() = %q, want %q", got, want)
	}
	if got, want := rewritten.AgentMessageID(), original.AgentMessageID(); got != want {
		t.Fatalf("rewritten AgentMessageID() = %q, want %q", got, want)
	}
}

func TestTurnInputModifiersAreImmutableAndPreserveUnrelatedFields(t *testing.T) {
	original := newTestTurnInput(t)
	attachments := []attachment.Attachment{{ID: "att-1", Filename: "memo.txt"}}
	updated := original.
		WithSessionID("session-1").
		WithAttachments(attachments).
		WithViewerRecipient("kuro").
		WithForcedRoute(routing.RouteCODE3).
		WithRoute(routing.RouteCHAT)
	attachments[0].Filename = "changed.txt"

	if original.SessionID() != "" || original.ViewerRecipient() != "" || original.HasForcedRoute() || original.Route() != "" {
		t.Fatalf("original input mutated: session=%q recipient=%q forced=%q route=%q", original.SessionID(), original.ViewerRecipient(), original.ForcedRoute(), original.Route())
	}
	if updated.SessionID() != "session-1" || updated.ViewerRecipient() != "kuro" || !updated.HasForcedRoute() || updated.ForcedRoute() != routing.RouteCODE3 || updated.Route() != routing.RouteCHAT {
		t.Fatalf("updated input modifiers missing: session=%q recipient=%q forced=%q route=%q", updated.SessionID(), updated.ViewerRecipient(), updated.ForcedRoute(), updated.Route())
	}
	if got := updated.Attachments(); len(got) != 1 || got[0].Filename != "memo.txt" {
		t.Fatalf("updated Attachments() = %#v, want copied attachment", got)
	}
	got := updated.Attachments()
	got[0].Filename = "mutated.txt"
	if gotAgain := updated.Attachments(); gotAgain[0].Filename != "memo.txt" {
		t.Fatalf("Attachments() exposed backing slice: %#v", gotAgain)
	}
	if err := updated.Validate(); err != nil {
		t.Fatalf("updated input Validate() error = %v", err)
	}
}

func TestTurnInputHasNoLegacySemanticAccessors(t *testing.T) {
	typeOfInput := reflect.TypeOf(TurnInput{})
	for _, method := range []string{"JobID", "UserMessage", "Channel", "ChatID"} {
		if _, ok := typeOfInput.MethodByName(method); ok {
			t.Fatalf("TurnInput unexpectedly exposes legacy accessor %q", method)
		}
	}
}
