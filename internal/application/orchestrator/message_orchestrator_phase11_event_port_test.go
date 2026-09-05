package orchestrator

import (
	"errors"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	phase11TaskID1     = "tsk_00000000-0000-5000-8000-000000000011"
	phase11TaskID2     = "tsk_00000000-0000-5000-8000-000000000012"
	phase11TaskIDShiro = "tsk_00000000-0000-5000-8000-000000000013"
)

type phase11RecordingEventListener struct {
	events []OrchestratorEvent
}

func (l *phase11RecordingEventListener) OnEvent(ev OrchestratorEvent) error {
	l.events = append(l.events, ev)
	return nil
}

type failingEventListener struct {
	err   error
	calls int
}

func (l *failingEventListener) OnEvent(OrchestratorEvent) error {
	l.calls++
	return l.err
}

func TestPhase11MessageReceivedReturnsPublicationFailure(t *testing.T) {
	wantErr := errors.New("canonical append unavailable")
	listener := &failingEventListener{err: wantErr}
	port := newMessageEventPort(listener)
	traceID := modulecore.NewTraceID()
	port.BindTrace(phase11TaskID1, traceID)

	err := port.EmitMessageReceived(ProcessMessageRequest{
		TraceID: string(traceID), SessionID: "session-1", Channel: "viewer", ChatID: "chat-1", UserMessage: "hello",
	}, phase11TaskID1)
	if !errors.Is(err, wantErr) || listener.calls != 1 {
		t.Fatalf("EmitMessageReceived() error=%v calls=%d", err, listener.calls)
	}
}

func TestPhase11EventPortStopsAfterFirstPublicationFailure(t *testing.T) {
	wantErr := errors.New("canonical append unavailable")
	listener := &failingEventListener{err: wantErr}
	port := newMessageEventPort(listener)
	traceID := modulecore.NewTraceID()
	port.BindTrace(phase11TaskID1, traceID)
	port.publicationFail.Begin(traceID, nil)
	defer port.publicationFail.End(traceID)

	if err := port.EmitMessageReceived(ProcessMessageRequest{SessionID: "session-1", UserMessage: "hello"}, phase11TaskID1); !errors.Is(err, wantErr) {
		t.Fatalf("first publication error=%v, want %v", err, wantErr)
	}
	port.Emit("agent.response", "mio", "user", "must not project", "CHAT", phase11TaskID1, "session-1", "viewer", "viewer-user")
	if listener.calls != 1 {
		t.Fatalf("listener calls=%d, want one call before trace failure closed publication", listener.calls)
	}
}

func TestPhase11EventPortNilListenerIsNoop(t *testing.T) {
	port := newMessageEventPort(nil)
	port.Emit("agent.start", "mio", "user", "考え中...", "CHAT", phase11TaskID1, "sess-1", "line", "U123")
	port.EmitMessageReceived(ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "こんにちは",
	}, phase11TaskID1)
}

func TestPhase11EventPortUsesUpdatedListener(t *testing.T) {
	port := newMessageEventPort(nil)
	listener := &phase11RecordingEventListener{}
	port.SetListener(listener)
	traceID := modulecore.NewTraceID()
	port.BindTrace(phase11TaskID1, traceID)
	port.BindTrace(phase11TaskID2, traceID)

	port.Emit("routing.decision", "mio", "", "confidence 90%", "CHAT", phase11TaskID1, "sess-1", "line", "U123")
	if len(listener.events) != 1 {
		t.Fatalf("expected one event, got %d", len(listener.events))
	}
	ev := listener.events[0]
	if ev.Type != "routing.decision" || ev.From != "mio" || ev.Route != "CHAT" || ev.TaskID.String() != phase11TaskID1 {
		t.Fatalf("unexpected event: %#v", ev)
	}

	port.EmitMessageReceived(ProcessMessageRequest{
		SessionID:   "sess-2",
		Channel:     "discord",
		ChatID:      "C123",
		UserMessage: "hello",
	}, phase11TaskID2)
	if len(listener.events) != 2 {
		t.Fatalf("expected two events, got %d", len(listener.events))
	}
	received := listener.events[1]
	if received.Type != "message.received" || received.From != "user" || received.To != "mio" {
		t.Fatalf("unexpected message received event: %#v", received)
	}
	if received.Route != "" || received.TaskID.String() != phase11TaskID2 {
		t.Fatalf("message.received should include task_id but not route before decision: %#v", received)
	}
	if !strings.HasPrefix(string(received.MessageID), "msg_") || received.TurnIndex != 1 {
		t.Fatalf("message.received should include stable conversation identity: %#v", received)
	}
	if received.TraceID != traceID {
		t.Fatalf("message.received trace_id = %q, want %q", received.TraceID, traceID)
	}
}

func TestPhase11EventPortUsesViewerRecipientWithoutExecutionRoute(t *testing.T) {
	listener := &phase11RecordingEventListener{}
	port := newMessageEventPort(listener)

	port.EmitMessageReceived(ProcessMessageRequest{
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "作業手順を相談したい",
		To:          "shiro",
	}, phase11TaskIDShiro)

	if len(listener.events) != 1 {
		t.Fatalf("expected one event, got %d", len(listener.events))
	}
	got := listener.events[0]
	if got.Type != "message.received" || got.From != "user" || got.To != "shiro" {
		t.Fatalf("unexpected message received event: %#v", got)
	}
	if got.Route != "" || got.TaskID.String() != phase11TaskIDShiro {
		t.Fatalf("viewer recipient must include task_id without implying execution route: %#v", got)
	}
}

func TestPhase11EventPortAssignsStableConversationIdentity(t *testing.T) {
	listener := &phase11RecordingEventListener{}
	port := newMessageEventPort(listener)
	traceID := modulecore.NewTraceID()
	port.BindTrace(phase11TaskID1, traceID)

	port.Emit("message.received", "user", "mio", "hello", "", "", "sess-1", "viewer", "viewer-user")
	port.Emit("agent.response", "mio", "user", "hi", "CHAT", phase11TaskID1, "sess-1", "viewer", "viewer-user")
	port.Emit("routing.decision", "mio", "", "CHAT", "CHAT", phase11TaskID1, "sess-1", "viewer", "viewer-user")

	if !strings.HasPrefix(string(listener.events[0].MessageID), "msg_") || listener.events[0].TurnIndex != 1 {
		t.Fatalf("first conversation identity = %#v", listener.events[0])
	}
	if !strings.HasPrefix(string(listener.events[1].MessageID), "msg_") || listener.events[1].TurnIndex != 2 {
		t.Fatalf("second conversation identity = %#v", listener.events[1])
	}
	if listener.events[0].MessageID == listener.events[1].MessageID {
		t.Fatalf("different messages must have different IDs: %#v", listener.events)
	}
	if listener.events[2].MessageID != "" || listener.events[2].TurnIndex != 0 {
		t.Fatalf("non conversation event should not get conversation identity: %#v", listener.events[2])
	}
	if listener.events[1].TraceID != traceID || listener.events[2].TraceID != traceID {
		t.Fatalf("all task events must retain trace_id: %#v", listener.events)
	}
}

func TestPhase11EventPortReleasesTraceBinding(t *testing.T) {
	listener := &phase11RecordingEventListener{}
	port := newMessageEventPort(listener)
	bound := modulecore.NewTraceID()
	port.BindTrace(phase11TaskID1, bound)
	port.ReleaseTrace(phase11TaskID1)

	port.Emit("routing.decision", "mio", "", "CHAT", "CHAT", phase11TaskID1, "sess-1", "viewer", "viewer-user")

	if got := modulecore.TraceID(listener.events[0].TraceID); got == bound || got.Validate() != nil {
		t.Fatalf("released trace binding was reused or invalid: got=%q bound=%q", got, bound)
	}
}

func TestPhase11EventPortDoesNotReuseMessageIDAfterRestart(t *testing.T) {
	firstListener := &phase11RecordingEventListener{}
	newMessageEventPort(firstListener).Emit(
		"message.received", "user", "mio", "first", "", phase11TaskID1, "sess-1", "viewer", "viewer-user",
	)
	secondListener := &phase11RecordingEventListener{}
	newMessageEventPort(secondListener).Emit(
		"message.received", "user", "mio", "second", "", phase11TaskID2, "sess-1", "viewer", "viewer-user",
	)

	if firstListener.events[0].MessageID == secondListener.events[0].MessageID {
		t.Fatalf("fresh event ports reused message ID: %q", firstListener.events[0].MessageID)
	}
}

func TestPhase11MessageReceivedPreservesIngressMessageID(t *testing.T) {
	listener := &phase11RecordingEventListener{}
	port := newMessageEventPort(listener)
	port.EmitMessageReceived(ProcessMessageRequest{
		MessageID:   "msg_client_or_adapter_generated",
		SessionID:   "sess-1",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "hello",
	}, phase11TaskID1)

	if got := listener.events[0].MessageID; got != "msg_client_or_adapter_generated" {
		t.Fatalf("message_id = %q, want ingress ID", got)
	}
}

func TestPhase11EventPortTreatsHandoffSpeechAsConversation(t *testing.T) {
	listener := &phase11RecordingEventListener{}
	port := newMessageEventPort(listener)

	port.Emit("agent.delegate", "mio", "shiro", "Shiro、作業をお願いします。", "OPS", phase11TaskID1, "sess-1", "viewer", "viewer-user")
	port.Emit("agent.acknowledge", "shiro", "mio", "Mio、復唱します。", "OPS", phase11TaskID1, "sess-1", "viewer", "viewer-user")
	port.Emit("agent.report", "shiro", "mio", "Mio、完了しました。", "OPS", phase11TaskID1, "sess-1", "viewer", "viewer-user")

	for i, ev := range listener.events {
		wantTurn := i + 1
		if ev.TurnIndex != wantTurn || ev.MessageID == "" {
			t.Fatalf("handoff event must have conversation identity: %#v", ev)
		}
	}
}
