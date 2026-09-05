package main

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestReconstructLocalAgentInputPreservesCanonicalProjection(t *testing.T) {
	address, err := conversation.NewChannelAddress("line", "U-local-agent")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "local worker input", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	input = input.
		WithSessionID(string(modulecore.NewSessionID())).
		WithViewerRecipient("shiro").
		WithForcedRoute(routing.RouteOPS).
		WithRoute(routing.RouteOPS)
	const jobID = "job-local-independent"
	message, err := domaintransport.NewTurnInputMessage("mio", "shiro", jobID, input)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}

	got, err := reconstructLocalAgentInput(message)
	if err != nil {
		t.Fatalf("reconstructLocalAgentInput() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("reconstructed input invalid: %v", err)
	}
	if got.RootTaskID() != input.RootTaskID() || got.TurnID() != input.TurnID() || got.TraceID() != input.TraceID() || got.UserMessageID() != input.UserMessageID() || got.AgentMessageID() != input.AgentMessageID() {
		t.Fatalf("canonical identities changed: got=%#v want=%#v", got, input)
	}
	if got.MessageText() != input.MessageText() || got.SessionID() != input.SessionID() || got.ChannelAddress() != input.ChannelAddress() {
		t.Fatalf("outer input fields changed: got=%#v want=%#v", got, input)
	}
	if got.ViewerRecipient() != input.ViewerRecipient() || got.ForcedRoute() != routing.RouteOPS || got.Route() != routing.RouteOPS {
		t.Fatalf("route projection changed: recipient=%q forced=%q route=%q", got.ViewerRecipient(), got.ForcedRoute(), got.Route())
	}
	for _, identity := range []string{
		string(got.RootTaskID()), string(got.TurnID()), string(got.TraceID()), string(got.UserMessageID()), string(got.AgentMessageID()),
	} {
		if identity == jobID {
			t.Fatalf("canonical identity reused transport JobID=%q", jobID)
		}
	}
}

func TestReconstructLocalAgentInputRejectsMissingOrMalformedProjection(t *testing.T) {
	address, err := conversation.NewChannelAddress("viewer", "local-agent")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "local input", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	input = input.WithSessionID(string(modulecore.NewSessionID())).WithRoute(routing.RouteOPS)
	message, err := domaintransport.NewTurnInputMessage("mio", "shiro", "job-local", input)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}

	missing := message
	missing.TurnInput = nil
	if _, err := reconstructLocalAgentInput(missing); err == nil {
		t.Fatal("missing turn_input projection must fail closed")
	}

	cases := []struct {
		name   string
		mutate func(*domaintransport.TurnInputContext)
	}{
		{name: "trace id", mutate: func(projection *domaintransport.TurnInputContext) {
			projection.TraceID = modulecore.TraceID("not-a-trace-id")
		}},
		{name: "address", mutate: func(projection *domaintransport.TurnInputContext) {
			projection.ChannelType = "VIEWER"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			malformed := message
			projection := *message.TurnInput
			malformed.TurnInput = &projection
			tc.mutate(malformed.TurnInput)
			if _, err := reconstructLocalAgentInput(malformed); err == nil {
				t.Fatalf("%s corruption must fail closed", tc.name)
			}
		})
	}
}
