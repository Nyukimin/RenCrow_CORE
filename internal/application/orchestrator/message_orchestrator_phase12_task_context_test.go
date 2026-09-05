package orchestrator

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func buildPhase12TurnInput(t *testing.T, builder *messageTurnInputBuilder, req ProcessMessageRequest) (conversation.TurnInput, modulecore.TaskID, string) {
	t.Helper()
	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensureProcessRequestIdentity() error = %v", err)
	}
	input, jobID, ttsSessionID, err := builder.Build(req)
	if err != nil {
		t.Fatalf("messageTurnInputBuilder.Build() error = %v", err)
	}
	return input, jobID, ttsSessionID
}

func TestPhase12TurnInputBuilderHonorsAudioOutputIntent(t *testing.T) {
	builder := newMessageTurnInputBuilder(func(string, string, string, string, string, string, string, string, string) {}, func() bool { return true })
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
			_, _, ttsSessionID := buildPhase12TurnInput(t, builder, req)
			if (ttsSessionID == "") != tt.wantEmpty {
				t.Fatalf("ttsSessionID=%q wantEmpty=%t", ttsSessionID, tt.wantEmpty)
			}
		})
	}
}

func TestPhase12TurnInputBuilderEmitsAttachmentEvent(t *testing.T) {
	var events []OrchestratorEvent
	builder := newMessageTurnInputBuilder(
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
	companionTaskID := modulecore.NewTaskID()
	tk, jobID, ttsSessionID := buildPhase12TurnInput(t, builder, ProcessMessageRequest{
		JobID:  companionTaskID.String(),
		TurnID: string(turnID), TraceID: string(traceID), RootTaskID: string(rootTaskID),
		MessageID: string(userMessageID), AgentMessageID: string(agentMessageID),
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "この画像を見て",
		To:          "mio",
		Attachments: []attachment.Attachment{{ID: "att-1"}},
	})

	if jobID != companionTaskID || jobID.String() == string(rootTaskID) {
		t.Fatalf("companion job ID = %q, root task ID = %q", jobID, rootTaskID)
	}
	if tk.MessageText() != "この画像を見て" || tk.SessionID() != "sess-1" {
		t.Fatalf("turn input text/session = text=%q session=%q", tk.MessageText(), tk.SessionID())
	}
	if address := tk.ChannelAddress(); address.ChannelType() != "line" || address.ExternalConversationID() != "U123" {
		t.Fatalf("turn input address = %#v", address)
	}
	if tk.ViewerRecipient() != "mio" {
		t.Fatalf("turn input recipient = %q", tk.ViewerRecipient())
	}
	if tk.TurnID() != turnID || tk.TraceID() != traceID || tk.RootTaskID() != rootTaskID || tk.UserMessageID() != userMessageID || tk.AgentMessageID() != agentMessageID {
		t.Fatalf("turn input conversation identity drifted: turn=%q trace=%q root=%q user=%q agent=%q", tk.TurnID(), tk.TraceID(), tk.RootTaskID(), tk.UserMessageID(), tk.AgentMessageID())
	}
	if len(tk.Attachments()) != 1 {
		t.Fatalf("expected attachment to be copied to turn input, got %d", len(tk.Attachments()))
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

func TestPhase12TurnInputBuilderBuildsTTSSessionOnlyWhenEnabled(t *testing.T) {
	enabled := false
	builder := newMessageTurnInputBuilder(
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func() bool { return enabled },
	)
	req := ProcessMessageRequest{
		SessionID:   "sess-2",
		Channel:     "discord",
		ChatID:      "C123",
		UserMessage: "話して",
	}

	_, _, noTTS := buildPhase12TurnInput(t, builder, req)
	if noTTS != "" {
		t.Fatalf("expected empty TTS session when disabled, got %q", noTTS)
	}

	enabled = true
	_, jobID, ttsSessionID := buildPhase12TurnInput(t, builder, req)
	expected := "sess-2-" + jobID.String()
	if ttsSessionID != expected {
		t.Fatalf("expected TTS session %q, got %q", expected, ttsSessionID)
	}
}

func TestPhase12TurnInputBuilderSkipsTTSSessionForRenCrowCMD(t *testing.T) {
	builder := newMessageTurnInputBuilder(
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
			_, _, ttsSessionID := buildPhase12TurnInput(t, builder, ProcessMessageRequest{
				SessionID: "viewer", Channel: "viewer", ChatID: "viewer-user", UserMessage: "おはようございます",
				OperationSource: "RenCrow_CMD", AudioOutput: tt.intent,
			})
			if (ttsSessionID == "") != tt.wantEmpty {
				t.Fatalf("ttsSessionID=%q wantEmpty=%t", ttsSessionID, tt.wantEmpty)
			}
		})
	}
}

func TestPhase12TurnInputBuilderPreservesProvidedJobID(t *testing.T) {
	builder := newMessageTurnInputBuilder(
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func() bool { return false },
	)

	companionTaskID := modulecore.NewTaskID()
	input, jobID, _ := buildPhase12TurnInput(t, builder, ProcessMessageRequest{
		JobID:       companionTaskID.String(),
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "こんにちは",
	})

	if jobID != companionTaskID {
		t.Fatalf("job ID = %q, want %q", jobID.String(), companionTaskID)
	}
	if jobID.String() == string(input.RootTaskID()) {
		t.Fatalf("companion JobID was mixed into TurnInput root task ID: job=%q root=%q", jobID, input.RootTaskID())
	}
}
