package orchestrator

import "testing"

type phase11RecordingEventListener struct {
	events []OrchestratorEvent
}

func (l *phase11RecordingEventListener) OnEvent(ev OrchestratorEvent) {
	l.events = append(l.events, ev)
}

func TestPhase11EventPortNilListenerIsNoop(t *testing.T) {
	port := newMessageEventPort(nil)
	port.Emit("agent.start", "mio", "user", "考え中...", "CHAT", "job-1", "sess-1", "line", "U123")
	port.EmitMessageReceived(ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "こんにちは",
	}, "job-1")
}

func TestPhase11EventPortUsesUpdatedListener(t *testing.T) {
	port := newMessageEventPort(nil)
	listener := &phase11RecordingEventListener{}
	port.SetListener(listener)

	port.Emit("routing.decision", "mio", "", "confidence 90%", "CHAT", "job-1", "sess-1", "line", "U123")
	if len(listener.events) != 1 {
		t.Fatalf("expected one event, got %d", len(listener.events))
	}
	ev := listener.events[0]
	if ev.Type != "routing.decision" || ev.From != "mio" || ev.Route != "CHAT" || ev.JobID != "job-1" {
		t.Fatalf("unexpected event: %#v", ev)
	}

	port.EmitMessageReceived(ProcessMessageRequest{
		SessionID:   "sess-2",
		Channel:     "discord",
		ChatID:      "C123",
		UserMessage: "hello",
	}, "job-2")
	if len(listener.events) != 2 {
		t.Fatalf("expected two events, got %d", len(listener.events))
	}
	received := listener.events[1]
	if received.Type != "message.received" || received.From != "user" || received.To != "mio" {
		t.Fatalf("unexpected message received event: %#v", received)
	}
	if received.Route != "" || received.JobID != "job-2" {
		t.Fatalf("message.received should include job but not route before decision: %#v", received)
	}
	if received.MessageID != "sess-2:chat:msg:0001" || received.TurnIndex != 1 {
		t.Fatalf("message.received should include stable conversation identity: %#v", received)
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
	}, "job-shiro")

	if len(listener.events) != 1 {
		t.Fatalf("expected one event, got %d", len(listener.events))
	}
	got := listener.events[0]
	if got.Type != "message.received" || got.From != "user" || got.To != "shiro" {
		t.Fatalf("unexpected message received event: %#v", got)
	}
	if got.Route != "" || got.JobID != "job-shiro" {
		t.Fatalf("viewer recipient must include job without implying execution route: %#v", got)
	}
}

func TestPhase11EventPortAssignsStableConversationIdentity(t *testing.T) {
	listener := &phase11RecordingEventListener{}
	port := newMessageEventPort(listener)

	port.Emit("message.received", "user", "mio", "hello", "", "", "sess-1", "viewer", "viewer-user")
	port.Emit("agent.response", "mio", "user", "hi", "CHAT", "job-1", "sess-1", "viewer", "viewer-user")
	port.Emit("routing.decision", "mio", "", "CHAT", "CHAT", "job-1", "sess-1", "viewer", "viewer-user")

	if listener.events[0].MessageID != "sess-1:chat:msg:0001" || listener.events[0].TurnIndex != 1 {
		t.Fatalf("first conversation identity = %#v", listener.events[0])
	}
	if listener.events[1].MessageID != "sess-1:chat:msg:0002" || listener.events[1].TurnIndex != 2 {
		t.Fatalf("second conversation identity = %#v", listener.events[1])
	}
	if listener.events[2].MessageID != "" || listener.events[2].TurnIndex != 0 {
		t.Fatalf("non conversation event should not get conversation identity: %#v", listener.events[2])
	}
}
