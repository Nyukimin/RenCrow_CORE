package orchestrator

import (
	"context"
	"errors"
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
	if len(events) != 1 || events[0] != "mailbox.error:receive transport not registered" {
		t.Fatalf("expected mailbox.error event, got %#v", events)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := target.ReceiveDelivery(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request dispatched without response owner: %v", err)
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

func TestPhase20ValidateDistributedResponseChecksAddressAndSessionCorrelation(t *testing.T) {
	request := domaintransport.NewMessage("mio", "shiro", "sess-1", modulecore.NewTaskID(), "hello")
	cases := []struct {
		name  string
		label string
		edit  func(*domaintransport.Message)
	}{
		{name: "sender", label: "sender mismatch", edit: func(response *domaintransport.Message) { response.From = "mio" }},
		{name: "recipient", label: "recipient mismatch", edit: func(response *domaintransport.Message) { response.To = "shiro" }},
		{name: "session", label: "session mismatch", edit: func(response *domaintransport.Message) { response.SessionID = "sess-2" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := domaintransport.NewMessage("shiro", "mio", "sess-1", request.TaskID, "response")
			test.edit(&response)
			err := validateDistributedResponse(request, response)
			if err == nil || !strings.Contains(err.Error(), test.label) {
				t.Fatalf("validation error = %v, want %s", err, test.label)
			}
		})
	}
}

func TestPhase20DistributedTransportExecutorRejectsAddressMismatchWithoutRecording(t *testing.T) {
	cases := []struct {
		name  string
		label string
		edit  func(*domaintransport.Message)
	}{
		{name: "sender", label: "sender mismatch", edit: func(response *domaintransport.Message) { response.From = "mio" }},
		{name: "recipient", label: "recipient mismatch", edit: func(response *domaintransport.Message) { response.To = "shiro" }},
		{name: "session", label: "session mismatch", edit: func(response *domaintransport.Message) { response.SessionID = "sess-2" }},
	}
	paths := []string{"ssh", "mailbox_ssh", "local"}
	for _, test := range cases {
		for _, path := range paths {
			t.Run(test.name+"/"+path, func(t *testing.T) {
				request := domaintransport.NewMessage("mio", "shiro", "sess-1", modulecore.NewTaskID(), "hello")
				response := domaintransport.NewMessage("shiro", "mio", "sess-1", request.TaskID, "wrong address")
				test.edit(&response)
				memory := session.NewCentralMemory()
				var eventTypes []string
				executor := newDistributedTransportExecutor(
					transport.NewMessageRouter(),
					map[string]domaintransport.Transport{},
					memory,
					func(eventType, _, _, _ string, _ domaintransport.Message) {
						eventTypes = append(eventTypes, eventType)
					},
					func(string, domaintransport.Message) time.Duration { return time.Second },
				)
				defer executor.router.Stop()

				var err error
				switch path {
				case "ssh":
					_, err = executor.ExecuteViaSSH(context.Background(), &distMockTransport{response: response}, "shiro", request)
				case "mailbox_ssh":
					executor.sshTransports["shiro"] = &distMockTransport{response: response}
					_, err = executor.ExecuteToAgentViaMailbox(context.Background(), "shiro", request, "mio")
				case "local":
					targetTransport := transport.NewLocalTransport()
					receiveTransport := transport.NewLocalTransport()
					defer targetTransport.Close()
					defer receiveTransport.Close()
					executor.router.RegisterAgent("shiro", targetTransport)
					executor.router.RegisterAgent("mio", receiveTransport)
					if seedErr := receiveTransport.PutInboundMessage(response); seedErr != nil {
						t.Fatalf("seed local response: %v", seedErr)
					}
					_, err = executor.ExecuteViaLocal(context.Background(), "shiro", request, "mio")
				}
				if err == nil || !strings.Contains(err.Error(), test.label) {
					t.Fatalf("%s error = %v, want %s", path, err, test.label)
				}
				if len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != "mailbox.error" {
					t.Fatalf("events = %#v, want terminal mailbox.error", eventTypes)
				}
				for _, eventType := range eventTypes {
					if eventType == "mailbox.received" {
						t.Fatalf("mismatched response emitted mailbox.received: %#v", eventTypes)
					}
				}
				if messages := memory.GetUnifiedView(0); len(messages) != 0 {
					t.Fatalf("mismatched response was recorded in memory: %#v", messages)
				}
			})
		}
	}
}

func TestPhase20DistributedTransportExecutorRecordsValidResponsesAcrossReceivePaths(t *testing.T) {
	paths := []string{"ssh", "mailbox_ssh", "local"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			request := domaintransport.NewMessage("mio", "shiro", "sess-1", modulecore.NewTaskID(), "hello")
			response := domaintransport.NewMessage("shiro", "mio", "sess-1", request.TaskID, "valid response")
			memory := session.NewCentralMemory()
			executor := newDistributedTransportExecutor(
				transport.NewMessageRouter(),
				map[string]domaintransport.Transport{},
				memory,
				func(string, string, string, string, domaintransport.Message) {},
				func(string, domaintransport.Message) time.Duration { return time.Second },
			)
			defer executor.router.Stop()

			var got domaintransport.Message
			var err error
			switch path {
			case "ssh":
				var content string
				content, err = executor.ExecuteViaSSH(context.Background(), &distMockTransport{response: response}, "shiro", request)
				if content != response.Content {
					t.Fatalf("SSH content = %q, want %q", content, response.Content)
				}
			case "mailbox_ssh":
				executor.sshTransports["shiro"] = &distMockTransport{response: response}
				got, err = executor.ExecuteToAgentViaMailbox(context.Background(), "shiro", request, "mio")
			case "local":
				targetTransport := transport.NewLocalTransport()
				receiveTransport := transport.NewLocalTransport()
				defer targetTransport.Close()
				defer receiveTransport.Close()
				executor.router.RegisterAgent("shiro", targetTransport)
				executor.router.RegisterAgent("mio", receiveTransport)
				if seedErr := receiveTransport.PutInboundMessage(response); seedErr != nil {
					t.Fatalf("seed local response: %v", seedErr)
				}
				got, err = executor.ExecuteViaLocal(context.Background(), "shiro", request, "mio")
			}
			if err != nil {
				t.Fatalf("%s valid response error = %v", path, err)
			}
			if path != "ssh" && got.Content != response.Content {
				t.Fatalf("%s content = %q, want %q", path, got.Content, response.Content)
			}
			if messages := memory.GetUnifiedView(0); len(messages) != 1 || messages[0].Message.Content != response.Content {
				t.Fatalf("%s recorded messages = %#v, want one valid response", path, messages)
			}
		})
	}
}

func TestLocalWorkerContextSenderPreservesAndCancelsDelivery(t *testing.T) {
	router := transport.NewMessageRouter()
	defer router.Stop()
	worker := transport.NewLocalTransport()
	defer worker.Close()
	router.RegisterAgent("shiro", worker)
	receiver := transport.NewLocalTransport()
	defer receiver.Close()
	router.RegisterAgent("mio", receiver)
	executor := newDistributedTransportExecutor(router, nil, session.NewCentralMemory(), func(string, string, string, string, domaintransport.Message) {}, func(string, domaintransport.Message) time.Duration { return time.Second })
	type marker struct{}
	parent := context.WithValue(context.Background(), marker{}, "original")
	msg := domaintransport.NewMessage("mio", "shiro", "session", modulecore.NewTaskID(), "work")
	delivered := make(chan transport.LocalDelivery, 1)
	receiverDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		d, err := worker.ReceiveDelivery(ctx)
		if err != nil {
			receiverDone <- err
			return
		}
		delivered <- d
		reply := domaintransport.NewMessage("shiro", "mio", d.Message.SessionID, d.Message.TaskID, "done")
		reply.Type = domaintransport.MessageTypeResult
		receiverDone <- receiver.PutInboundMessage(reply)
	}()
	if _, err := executor.ExecuteViaLocal(parent, "shiro", msg, "mio"); err != nil {
		t.Fatal(err)
	}
	if err := <-receiverDone; err != nil {
		t.Fatal(err)
	}
	d := <-delivered
	if d.Context == nil || d.Context.Value(marker{}) != "original" {
		t.Fatal("original context value lost")
	}
	if _, ok := d.Context.Deadline(); !ok {
		t.Fatal("delivery has no request deadline")
	}
	if d.Context.Err() != context.Canceled {
		t.Fatalf("finished sender did not cancel delivery: %v", d.Context.Err())
	}
	if d.Message.TaskID != msg.TaskID {
		t.Fatal("task identity changed")
	}
}

func TestLocalWorkerContextSenderDeadlineExpiresQueuedRequest(t *testing.T) {
	router := transport.NewMessageRouter()
	defer router.Stop()
	worker := transport.NewLocalTransport()
	defer worker.Close()
	router.RegisterAgent("shiro", worker)
	receiver := transport.NewLocalTransport()
	defer receiver.Close()
	router.RegisterAgent("mio", receiver)
	executor := newDistributedTransportExecutor(router, nil, session.NewCentralMemory(), func(string, string, string, string, domaintransport.Message) {}, func(string, domaintransport.Message) time.Duration { return 20 * time.Millisecond })
	msg := domaintransport.NewMessage("mio", "shiro", "session", modulecore.NewTaskID(), "work")
	_, err := executor.ExecuteViaLocal(context.Background(), "shiro", msg, "mio")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected response deadline, got %v", err)
	}
	receiveCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, err := worker.ReceiveDelivery(receiveCtx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Context == nil || !errors.Is(d.Context.Err(), context.DeadlineExceeded) {
		t.Fatal("expired queued request lost deadline cancellation")
	}
}
