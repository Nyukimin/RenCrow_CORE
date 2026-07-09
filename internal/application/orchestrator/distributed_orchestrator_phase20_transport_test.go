package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
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

	msg := domaintransport.NewMessage("mio", "shiro", "sess-1", "job-1", "hello")
	_, err := executor.ExecuteToAgent(context.Background(), "shiro", msg)
	if err == nil {
		t.Fatal("expected local router error without registered shiro")
	}
	if timeoutTarget != "" || timeoutMsg.JobID != "" {
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

	msg := domaintransport.NewMessage("mio", "shiro", "sess-1", "job-1", "hello")
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
