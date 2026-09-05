package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const phase15TaskID = "tsk_00000000-0000-5000-8000-000000000001"

func TestPhase15DistributedEventPortNilListenerIsNoop(t *testing.T) {
	port := newDistributedEventPort(nil)

	port.Emit("message.received", "user", "mio", "hello", "CHAT", phase15TaskID, "session-1", "line", "chat-1")
	port.EmitNote("mio", "user", "note", "CHAT", phase15TaskID, "session-1", "line", "chat-1")
	port.EmitProgress("mailbox.sent", "mio", "shiro", "sent", domaintransport.Message{
		TaskID:    modulecore.TaskID(phase15TaskID),
		SessionID: "session-1",
		Context: map[string]interface{}{
			"route":   "CODE1",
			"channel": "line",
			"chat_id": "chat-1",
		},
	})
}

func TestPhase15DistributedEventPortAssignsStableConversationIdentity(t *testing.T) {
	listener := &distRecordingEventListener{}
	port := newDistributedEventPort(listener)
	traceID := modulecore.NewTraceID()
	port.BindTrace(phase15TaskID, traceID)
	defer port.ReleaseTrace(phase15TaskID)

	port.Emit("message.received", "user", "mio", "hello", "", "", "session-1", "line", "chat-1")
	port.Emit("agent.response", "mio", "user", "hi", "CHAT", phase15TaskID, "session-1", "line", "chat-1")
	port.Emit("agent.note", "mio", "user", "note", "CHAT", phase15TaskID, "session-1", "line", "chat-1")

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
	if string(listener.events[1].TraceID) == listener.events[1].TaskID.String() {
		t.Fatalf("trace_id must not reuse task_id: %#v", listener.events[1])
	}
}

func TestPhase15DistributedEventPortEmitsProgressWithMessageContext(t *testing.T) {
	listener := &distRecordingEventListener{}
	port := newDistributedEventPort(listener)

	port.EmitProgress("mailbox.sent", "mio", "shiro", "sent", domaintransport.Message{
		TaskID:    modulecore.TaskID(phase15TaskID),
		SessionID: "session-1",
		Context: map[string]interface{}{
			"route":   "CODE1",
			"channel": "line",
			"chat_id": "chat-1",
		},
	})

	if len(listener.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(listener.events))
	}
	ev := listener.events[0]
	if ev.Type != "mailbox.sent" || ev.From != "mio" || ev.To != "shiro" || ev.Content != "sent" {
		t.Fatalf("unexpected event payload: %#v", ev)
	}
	if ev.Route != "CODE1" || ev.TaskID.String() != phase15TaskID || string(ev.SessionID) != "session-1" || ev.Channel != "line" || ev.ChatID != "chat-1" {
		t.Fatalf("unexpected event context: %#v", ev)
	}
}

func TestPhase15DistributedEvidenceReporterNoopsWithoutRequiredInputs(t *testing.T) {
	reporter := &distMockReportStore{}
	evidence := newDistributedEvidenceReporter(nil)
	now := time.Now().UTC()
	taskID := modulecore.TaskID(phase15TaskID)

	evidence.Save(context.Background(), taskID, "goal", "CHAT", now, now, nil)
	if len(reporter.reports) != 0 {
		t.Fatalf("nil report store should not save reports: %#v", reporter.reports)
	}

	evidence.SetReportStore(reporter)
	evidence.Save(context.Background(), modulecore.TaskID(""), "goal", "CHAT", now, now, nil)
	evidence.Save(context.Background(), taskID, "", "CHAT", now, now, nil)
	if len(reporter.reports) != 0 {
		t.Fatalf("blank task or goal should not save reports: %#v", reporter.reports)
	}
}

func TestPhase15DistributedEvidenceReporterPreservesSuccessAndFailureContracts(t *testing.T) {
	reporter := &distMockReportStore{}
	evidence := newDistributedEvidenceReporter(reporter)
	startedAt := time.Date(2026, 5, 16, 1, 2, 3, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)

	evidence.Save(context.Background(), modulecore.NewTaskID(), "do chat", "chat", startedAt, finishedAt, nil)
	evidence.Save(context.Background(), modulecore.NewTaskID(), "do code", "code2", startedAt, finishedAt, errors.New("verify failed"))

	if len(reporter.reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(reporter.reports))
	}
	passed := reporter.reports[0]
	if passed.Status != "passed" || passed.Route != "CHAT" || passed.ErrorKind != "" || passed.Error != "" {
		t.Fatalf("unexpected passed report: %#v", passed)
	}
	if !containsString(passed.Acceptance, "Mio 応答完了") || !containsString(passed.Verification, "final:passed") || !containsString(passed.Steps, "mio.chat") || !containsString(passed.Steps, "done") {
		t.Fatalf("passed report lost expected contract fields: %#v", passed)
	}

	failed := reporter.reports[1]
	if failed.Status != "failed" || failed.Route != "CODE2" || failed.ErrorKind != "verify" || failed.Error != "verify failed" {
		t.Fatalf("unexpected failed report: %#v", failed)
	}
	if !containsString(failed.Acceptance, "Coder 実行完了") || !containsString(failed.Acceptance, "Worker 取りまとめ完了") {
		t.Fatalf("failed report lost CODE acceptance fields: %#v", failed)
	}
	if !containsString(failed.Verification, "route=CODE2") || !containsString(failed.Verification, "final:failed") || !containsString(failed.Steps, "shiro.delegate") || !containsString(failed.Steps, "error") {
		t.Fatalf("failed report lost expected contract fields: %#v", failed)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
