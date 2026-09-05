package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newAgentTurnInputForTest(t *testing.T) conversation.TurnInput {
	t.Helper()

	address, err := conversation.NewChannelAddress("line", "U-agent-h2")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.ReconstructTurnInput(
		modulecore.NewTaskID(),
		modulecore.NewTurnID(),
		modulecore.NewTraceID(),
		modulecore.NewMessageID(),
		modulecore.NewMessageID(),
		"canonical agent message",
		address,
	)
	if err != nil {
		t.Fatalf("ReconstructTurnInput() error = %v", err)
	}

	return input.
		WithSessionID(string(modulecore.NewSessionID())).
		WithAttachments([]attachment.Attachment{{
			ID:                  "att-h2",
			Kind:                attachment.KindDocument,
			Filename:            "context.md",
			ContentType:         "text/markdown",
			SizeBytes:           42,
			Path:                "workspace/context.md",
			SHA256:              "sha256-h2",
			ExtractedText:       "attachment metadata",
			ExtractionError:     "",
			ExtractionTruncated: true,
			SecurityWarnings:    []string{"warning-h2"},
		}}).
		WithViewerRecipient("mio").
		WithForcedRoute(routing.RouteCODE3).
		WithRoute(routing.RouteCODE2)
}

func TestTurnInputFromAgentMessagePreservesCanonicalProjectionAfterJSONRoundTrip(t *testing.T) {
	want := newAgentTurnInputForTest(t)
	message, err := domaintransport.NewTurnInputMessage("mio", "shiro", "legacy-job-is-independent", want)
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded domaintransport.Message
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	decoded.JobID = "a-different-legacy-job"

	got, err := turnInputFromAgentMessage(decoded)
	if err != nil {
		t.Fatalf("turnInputFromAgentMessage() error = %v", err)
	}
	if got.RootTaskID() != want.RootTaskID() || got.TurnID() != want.TurnID() || got.TraceID() != want.TraceID() || got.UserMessageID() != want.UserMessageID() || got.AgentMessageID() != want.AgentMessageID() {
		t.Fatalf("canonical identities changed: got root=%q turn=%q trace=%q user=%q agent=%q, want root=%q turn=%q trace=%q user=%q agent=%q", got.RootTaskID(), got.TurnID(), got.TraceID(), got.UserMessageID(), got.AgentMessageID(), want.RootTaskID(), want.TurnID(), want.TraceID(), want.UserMessageID(), want.AgentMessageID())
	}
	if got.SessionID() != want.SessionID() || got.MessageText() != want.MessageText() {
		t.Fatalf("session/message changed: got session=%q message=%q, want session=%q message=%q", got.SessionID(), got.MessageText(), want.SessionID(), want.MessageText())
	}
	if got.ChannelAddress().ChannelType() != want.ChannelAddress().ChannelType() || got.ChannelAddress().ExternalConversationID() != want.ChannelAddress().ExternalConversationID() {
		t.Fatalf("channel address changed: got=%q/%q, want=%q/%q", got.ChannelAddress().ChannelType(), got.ChannelAddress().ExternalConversationID(), want.ChannelAddress().ChannelType(), want.ChannelAddress().ExternalConversationID())
	}
	if !reflect.DeepEqual(got.Attachments(), want.Attachments()) {
		t.Fatalf("attachments changed: got=%#v, want=%#v", got.Attachments(), want.Attachments())
	}
	if got.ViewerRecipient() != want.ViewerRecipient() || got.ForcedRoute() != want.ForcedRoute() || got.Route() != want.Route() || !got.HasForcedRoute() {
		t.Fatalf("recipient/routes changed: got recipient=%q forced=%q route=%q, want recipient=%q forced=%q route=%q", got.ViewerRecipient(), got.ForcedRoute(), got.Route(), want.ViewerRecipient(), want.ForcedRoute(), want.Route())
	}
}

func TestTurnInputFromAgentMessageRejectsMissingOrMalformedProjection(t *testing.T) {
	legacy := domaintransport.NewMessage("mio", "shiro", "legacy-session", "legacy-job", "message without projection")
	if _, err := turnInputFromAgentMessage(legacy); err == nil || !strings.Contains(err.Error(), "invalid agent turn input projection") {
		t.Fatalf("missing projection error = %v, want bounded projection error", err)
	}

	message, err := domaintransport.NewTurnInputMessage("mio", "shiro", "legacy-job", newAgentTurnInputForTest(t))
	if err != nil {
		t.Fatalf("NewTurnInputMessage() error = %v", err)
	}
	projection := *message.TurnInput
	projection.TurnID = modulecore.TurnID("malformed-turn-id")
	message.TurnInput = &projection
	if _, err := turnInputFromAgentMessage(message); err == nil {
		t.Fatal("malformed projection was accepted")
	}
}
