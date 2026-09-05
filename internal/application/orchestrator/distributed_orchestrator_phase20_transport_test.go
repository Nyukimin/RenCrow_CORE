package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPhase20DistributedTransportExecutorExecuteToAgentUsesMessageFromAsReceiveAgent(t *testing.T) {
	var events []string
	var timeoutTarget string
	var timeoutMsg domaintransport.Message
	executor := newDistributedTransportExecutor(
		transport.NewMessageRouter(),
		map[string]domaintransport.Transport{},
		session.NewCentralMemory(),
		func(eventType, from, to, content string, msg domaintransport.Message) {
			events = append(events, eventType+":"+from+":"+to+":"+content)
		},
		func(targetAgent string, msg domaintransport.Message) time.Duration {
			timeoutTarget = targetAgent
			timeoutMsg = msg
			return time.Nanosecond
		},
	)

	msg := domaintransport.NewMessage("mio", "shiro", "sess-1", modulecore.NewTaskID(), "hello")
	_, err := executor.ExecuteToAgent(context.Background(), "shiro", msg)
	if err == nil {
		t.Fatal("expected local router error without registered shiro")
	}
	if timeoutTarget != "" || !timeoutMsg.TaskID.IsZero() {
		t.Fatalf("timeout resolver should not run before target transport exists: target=%s msg=%#v", timeoutTarget, timeoutMsg)
	}
	if len(events) != 1 || events[0] != "mailbox.sent:mio:shiro:via=local receive_on=mio type=task" {
		t.Fatalf("expected mailbox.sent with receive_on from msg.From, got %#v", events)
	}
}

func TestPhase20DistributedTransportExecutorLocalReceiveMissingReturnsExistingError(t *testing.T) {
	var events []string
	router := transport.NewMessageRouter()
	target := transport.NewLocalTransport()
	defer target.Close()
	router.RegisterAgent("shiro", target)
	defer router.Stop()

	executor := newDistributedTransportExecutor(
		router,
		map[string]domaintransport.Transport{},
		session.NewCentralMemory(),
		func(eventType, from, to, content string, msg domaintransport.Message) {
			events = append(events, eventType+":"+content)
		},
		func(targetAgent string, msg domaintransport.Message) time.Duration {
			return time.Nanosecond
		},
	)

	msg := domaintransport.NewMessage("mio", "shiro", "sess-1", modulecore.NewTaskID(), "hello")
	_, err := executor.ExecuteViaLocal(context.Background(), "shiro", msg, "missing")
	if err == nil {
		t.Fatal("expected missing receive transport error")
	}
	if got := err.Error(); got != "receive transport not registered (agent=missing)" {
		t.Fatalf("unexpected error: %s", got)
	}
	if len(events) < 2 || events[len(events)-1] != "mailbox.error:receive transport not registered" {
		t.Fatalf("expected mailbox.error event, got %#v", events)
	}
}

func TestPhase20DistributedTransportExecutorSSHReceiveRejectsTaskIDMismatch(t *testing.T) {
	var eventTypes []string
	var eventMessages []domaintransport.Message
	router := transport.NewMessageRouter()
	defer router.Stop()
	executor := newDistributedTransportExecutor(
		router,
		map[string]domaintransport.Transport{},
		session.NewCentralMemory(),
		func(eventType, from, to, content string, msg domaintransport.Message) {
			eventTypes = append(eventTypes, eventType)
			eventMessages = append(eventMessages, msg)
		},
		func(targetAgent string, msg domaintransport.Message) time.Duration {
			return time.Second
		},
	)

	requestTaskID := modulecore.NewTaskID()
	request := domaintransport.NewMessage("mio", "shiro", "sess-1", requestTaskID, "hello")
	response := domaintransport.NewMessage("shiro", "mio", "sess-1", modulecore.NewTaskID(), "wrong task")
	sshTransport := &distMockTransport{response: response}

	if _, err := executor.ExecuteViaSSH(context.Background(), sshTransport, "shiro", request); err == nil || !strings.Contains(err.Error(), "task_id mismatch") {
		t.Fatalf("expected bounded task correlation error, got %v", err)
	}
	if len(eventTypes) != 1 || eventTypes[0] != "mailbox.error" {
		t.Fatalf("expected only correlated mailbox.error, got %#v", eventTypes)
	}
	if len(eventMessages) != 1 || eventMessages[0].TaskID != requestTaskID {
		t.Fatalf("mailbox.error must carry request task ID: %#v", eventMessages)
	}
}

func TestPhase20DistributedTransportExecutorMailboxSSHReceiveRejectsTaskIDMismatch(t *testing.T) {
	var eventTypes []string
	var eventMessages []domaintransport.Message
	router := transport.NewMessageRouter()
	defer router.Stop()
	sshTransport := &distMockTransport{
		response: domaintransport.NewMessage("shiro", "mio", "sess-1", modulecore.NewTaskID(), "wrong task"),
	}
	executor := newDistributedTransportExecutor(
		router,
		map[string]domaintransport.Transport{"shiro": sshTransport},
		session.NewCentralMemory(),
		func(eventType, from, to, content string, msg domaintransport.Message) {
			eventTypes = append(eventTypes, eventType)
			eventMessages = append(eventMessages, msg)
		},
		func(targetAgent string, msg domaintransport.Message) time.Duration {
			return time.Second
		},
	)

	requestTaskID := modulecore.NewTaskID()
	request := domaintransport.NewMessage("mio", "shiro", "sess-1", requestTaskID, "hello")
	if _, err := executor.ExecuteToAgentViaMailbox(context.Background(), "shiro", request, "mio"); err == nil || !strings.Contains(err.Error(), "task_id mismatch") {
		t.Fatalf("expected bounded task correlation error, got %v", err)
	}
	if len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != "mailbox.error" {
		t.Fatalf("expected terminal mailbox.error, got %#v", eventTypes)
	}
	if len(eventMessages) == 0 || eventMessages[len(eventMessages)-1].TaskID != requestTaskID {
		t.Fatalf("mailbox.error must carry request task ID: %#v", eventMessages)
	}
	for _, eventType := range eventTypes {
		if eventType == "mailbox.received" {
			t.Fatalf("mismatched response must not emit mailbox.received: %#v", eventTypes)
		}
	}
}

func TestPhase20DistributedTransportExecutorLocalReceiveRejectsTaskIDMismatch(t *testing.T) {
	var eventTypes []string
	var eventMessages []domaintransport.Message
	router := transport.NewMessageRouter()
	defer router.Stop()
	targetTransport := transport.NewLocalTransport()
	defer targetTransport.Close()
	receiveTransport := transport.NewLocalTransport()
	defer receiveTransport.Close()
	router.RegisterAgent("shiro", targetTransport)
	router.RegisterAgent("mio", receiveTransport)
	executor := newDistributedTransportExecutor(
		router,
		map[string]domaintransport.Transport{},
		session.NewCentralMemory(),
		func(eventType, from, to, content string, msg domaintransport.Message) {
			eventTypes = append(eventTypes, eventType)
			eventMessages = append(eventMessages, msg)
		},
		func(targetAgent string, msg domaintransport.Message) time.Duration {
			return time.Second
		},
	)

	requestTaskID := modulecore.NewTaskID()
	request := domaintransport.NewMessage("mio", "shiro", "sess-1", requestTaskID, "hello")
	if err := receiveTransport.PutInboundMessage(domaintransport.NewMessage("shiro", "mio", "sess-1", modulecore.NewTaskID(), "wrong task")); err != nil {
		t.Fatalf("seed mismatched local response: %v", err)
	}
	if _, err := executor.ExecuteViaLocal(context.Background(), "shiro", request, "mio"); err == nil || !strings.Contains(err.Error(), "task_id mismatch") {
		t.Fatalf("expected bounded task correlation error, got %v", err)
	}
	if len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != "mailbox.error" {
		t.Fatalf("expected terminal mailbox.error, got %#v", eventTypes)
	}
	if len(eventMessages) == 0 || eventMessages[len(eventMessages)-1].TaskID != requestTaskID {
		t.Fatalf("mailbox.error must carry request task ID: %#v", eventMessages)
	}
	for _, eventType := range eventTypes {
		if eventType == "mailbox.received" {
			t.Fatalf("mismatched response must not emit mailbox.received: %#v", eventTypes)
		}
	}
}
