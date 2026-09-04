package orchestrator

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPhase12TaskContextBuilderHonorsAudioOutputIntent(t *testing.T) {
	builder := newMessageTaskContextBuilder(func(string, string, string, string, string, string, string, string, string) {}, func() bool { return true })
	for _, tt := range []struct {
		name      string
		intent    string
		wantEmpty bool
	}{
		{name: "disabled", intent: "disabled", wantEmpty: true},
		{name: "requested", intent: "requested", wantEmpty: false},
		{name: "omitted legacy", intent: "", wantEmpty: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := ProcessMessageRequest{SessionID: "viewer", Channel: "viewer", ChatID: "viewer-user", UserMessage: "hello", AudioOutput: AudioOutputIntent(tt.intent)}
			_, _, sessionID := builder.Build(req)
			if (sessionID == "") != tt.wantEmpty {
				t.Fatalf("sessionID=%q wantEmpty=%t", sessionID, tt.wantEmpty)
			}
		})
	}
}

func TestPhase12TaskContextBuilderEmitsAttachmentEvent(t *testing.T) {
	var events []OrchestratorEvent
	builder := newMessageTaskContextBuilder(
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
			events = append(events, NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID))
		},
		func() bool { return false },
	)

	turnID := modulecore.NewTurnID()
	traceID := modulecore.NewTraceID()
	rootTaskID := modulecore.NewTaskID()
	userMessageID := modulecore.NewMessageID()
	agentMessageID := modulecore.NewMessageID()
	tk, jobID, ttsSessionID := builder.Build(ProcessMessageRequest{
		TurnID: string(turnID), TraceID: string(traceID), RootTaskID: string(rootTaskID),
		MessageID: string(userMessageID), AgentMessageID: string(agentMessageID),
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "この画像を見て",
		Attachments: []attachment.Attachment{{ID: "att-1"}},
	})

	if tk.JobID().String() != jobID.String() {
		t.Fatalf("expected task and returned job ID to match: task=%s returned=%s", tk.JobID(), jobID)
	}
	if tk.SessionID() != "sess-1" || tk.ChatID() != "U123" {
		t.Fatalf("task identity = session=%q chat=%q", tk.SessionID(), tk.ChatID())
	}
	if tk.TurnID() != turnID || tk.TraceID() != traceID || tk.RootTaskID() != rootTaskID || tk.UserMessageID() != userMessageID || tk.AgentMessageID() != agentMessageID {
		t.Fatalf("task conversation identity drifted: turn=%q trace=%q task=%q user=%q agent=%q", tk.TurnID(), tk.TraceID(), tk.RootTaskID(), tk.UserMessageID(), tk.AgentMessageID())
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

	for _, tt := range []struct {
		name      string
		intent    AudioOutputIntent
		wantEmpty bool
	}{
		{name: "omitted", wantEmpty: true},
		{name: "explicit requested", intent: AudioOutputRequested, wantEmpty: false},
		{name: "explicit disabled", intent: AudioOutputDisabled, wantEmpty: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ttsSessionID := builder.Build(ProcessMessageRequest{
				SessionID: "viewer", Channel: "viewer", ChatID: "viewer-user", UserMessage: "おはようございます",
				OperationSource: "RenCrow_CMD", AudioOutput: tt.intent,
			})
			if (ttsSessionID == "") != tt.wantEmpty {
				t.Fatalf("ttsSessionID=%q wantEmpty=%t", ttsSessionID, tt.wantEmpty)
			}
		})
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
