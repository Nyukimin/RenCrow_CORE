package orchestrator

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
)

func TestPhase12TaskContextBuilderEmitsAttachmentEvent(t *testing.T) {
	var events []OrchestratorEvent
	builder := newMessageTaskContextBuilder(
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
			events = append(events, NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID))
		},
		func() bool { return false },
	)

	tk, jobID, ttsSessionID := builder.Build(ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "この画像を見て",
		Attachments: []attachment.Attachment{{ID: "att-1"}},
	})

	if tk.JobID().String() != jobID.String() {
		t.Fatalf("expected task and returned job ID to match: task=%s returned=%s", tk.JobID(), jobID)
	}
	if len(tk.Attachments()) != 1 {
		t.Fatalf("expected attachment to be copied to task, got %d", len(tk.Attachments()))
	}
	if ttsSessionID != "" {
		t.Fatalf("expected empty TTS session without TTS, got %q", ttsSessionID)
	}
	if len(events) != 1 {
		t.Fatalf("expected one attachment event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "viewer.attachment.received" || ev.From != "viewer" || ev.To != "mio" {
		t.Fatalf("unexpected attachment event routing: %#v", ev)
	}
	if ev.Content != "1 attachment(s)" || ev.JobID != jobID.String() || ev.SessionID != "sess-1" || ev.Channel != "line" || ev.ChatID != "U123" {
		t.Fatalf("unexpected attachment event payload: %#v", ev)
	}
}

func TestPhase12TaskContextBuilderBuildsTTSSessionOnlyWhenEnabled(t *testing.T) {
	enabled := false
	builder := newMessageTaskContextBuilder(
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func() bool { return enabled },
	)
	req := ProcessMessageRequest{
		SessionID:   "sess-2",
		Channel:     "discord",
		ChatID:      "C123",
		UserMessage: "話して",
	}

	_, _, noTTS := builder.Build(req)
	if noTTS != "" {
		t.Fatalf("expected empty TTS session when disabled, got %q", noTTS)
	}

	enabled = true
	_, jobID, ttsSessionID := builder.Build(req)
	expected := "sess-2-" + jobID.String()
	if ttsSessionID != expected {
		t.Fatalf("expected TTS session %q, got %q", expected, ttsSessionID)
	}
}

func TestPhase12TaskContextBuilderSkipsTTSSessionForRenCrowCMD(t *testing.T) {
	builder := newMessageTaskContextBuilder(
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func() bool { return true },
	)

	_, _, ttsSessionID := builder.Build(ProcessMessageRequest{
		SessionID:       "viewer",
		Channel:         "viewer",
		ChatID:          "viewer-user",
		UserMessage:     "おはようございます",
		OperationSource: "RenCrow_CMD",
	})

	if ttsSessionID != "" {
		t.Fatalf("expected CMD text chat to skip TTS, got %q", ttsSessionID)
	}
}

func TestPhase12TaskContextBuilderPreservesProvidedJobID(t *testing.T) {
	builder := newMessageTaskContextBuilder(
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func() bool { return false },
	)

	_, jobID, _ := builder.Build(ProcessMessageRequest{
		JobID:       "viewer-job-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "こんにちは",
	})

	if jobID.String() != "viewer-job-1" {
		t.Fatalf("job ID = %q, want viewer-job-1", jobID.String())
	}
}
