package orchestrator

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMessageTurnInputBuilderPreservesCanonicalInputAndCompanionJobID(t *testing.T) {
	turnID := modulecore.NewTurnID()
	traceID := modulecore.NewTraceID()
	rootTaskID := modulecore.NewTaskID()
	userMessageID := modulecore.NewMessageID()
	agentMessageID := modulecore.NewMessageID()
	attachments := []attachment.Attachment{{ID: "att-1"}}
	req := ProcessMessageRequest{
		JobID:          "legacy-job-companion",
		TurnID:         string(turnID),
		TraceID:        string(traceID),
		RootTaskID:     string(rootTaskID),
		MessageID:      string(userMessageID),
		AgentMessageID: string(agentMessageID),
		SessionID:      "session-1",
		Channel:        "LINE",
		ChatID:         " user-1 ",
		UserMessage:    "hello",
		To:             "shiro",
		Attachments:    attachments,
	}

	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensureProcessRequestIdentity() error = %v", err)
	}
	builder := newMessageTurnInputBuilder(func(string, string, string, string, string, string, string, string, string) {}, func() bool { return false })
	input, jobID, ttsSessionID, err := builder.Build(req)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if ttsSessionID != "" {
		t.Fatalf("tts session = %q, want empty", ttsSessionID)
	}
	if jobID.String() != req.JobID {
		t.Fatalf("companion job ID = %q, want %q", jobID, req.JobID)
	}
	if jobID.String() == req.RootTaskID {
		t.Fatal("companion JobID must remain independent from RootTaskID")
	}
	if input.TurnID() != turnID || input.TraceID() != traceID || input.RootTaskID() != rootTaskID || input.UserMessageID() != userMessageID || input.AgentMessageID() != agentMessageID {
		t.Fatalf("conversation identity changed: turn=%q trace=%q root=%q user=%q agent=%q", input.TurnID(), input.TraceID(), input.RootTaskID(), input.UserMessageID(), input.AgentMessageID())
	}
	if input.MessageText() != req.UserMessage || input.SessionID() != req.SessionID {
		t.Fatalf("input text/session changed: text=%q session=%q", input.MessageText(), input.SessionID())
	}
	address := input.ChannelAddress()
	if address.ChannelType() != "line" || address.ExternalConversationID() != "user-1" {
		t.Fatalf("input address = %#v", address)
	}
	if input.ViewerRecipient() != req.To {
		t.Fatalf("viewer recipient = %q, want %q", input.ViewerRecipient(), req.To)
	}
	if got := input.Attachments(); len(got) != 1 || got[0].ID != "att-1" {
		t.Fatalf("attachments = %#v", got)
	}
}

func TestBuildTurnInputFromProcessRequestRejectsInvalidAddress(t *testing.T) {
	req := ProcessMessageRequest{
		Channel:     "   ",
		ChatID:      "user-1",
		UserMessage: "hello",
	}
	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensureProcessRequestIdentity() error = %v", err)
	}
	if _, err := buildTurnInputFromProcessRequest(req); err == nil {
		t.Fatal("expected invalid channel address to be rejected")
	}
}
