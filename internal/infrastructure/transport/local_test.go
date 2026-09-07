package transport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLocalTransportRejectsMissingOrMalformedTaskID(t *testing.T) {
	for _, taskID := range []modulecore.TaskID{"", "not-a-task-id"} {
		t.Run(string(taskID), func(t *testing.T) {
			lt := NewLocalTransport()
			defer lt.Close()

			msg := domaintransport.NewMessage("mio", "shiro", "s1", taskID, "hello")
			if err := lt.Send(context.Background(), msg); err == nil {
				t.Fatalf("Send() accepted invalid TaskID %q", taskID)
			}
			select {
			case got := <-lt.GetOutboundChannel():
				t.Fatalf("invalid outbound message was delivered: task_id=%q", got.TaskID)
			default:
			}

			if err := lt.PutInboundMessage(msg); err == nil {
				t.Fatalf("PutInboundMessage() accepted invalid TaskID %q", taskID)
			}
			select {
			case got := <-lt.inbound:
				t.Fatalf("invalid inbound message was delivered: task_id=%q", got.TaskID)
			default:
			}
		})
	}
}

func TestLocalTransport_SendReceive(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	ctx := context.Background()
	msg := domaintransport.NewMessage("mio", "shiro", "s1", modulecore.NewTaskID(), "hello")

	// Send → outbound channel
	if err := lt.Send(ctx, msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Read from outbound
	select {
	case received := <-lt.GetOutboundChannel():
		if received.From != "mio" || received.Content != "hello" {
			t.Errorf("Unexpected message: %+v", received)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for outbound message")
	}
}

func TestLocalTransport_PutInboundReceive(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	ctx := context.Background()
	msg := domaintransport.NewMessage("Router", "mio", "s1", modulecore.NewTaskID(), "routed msg")

	if err := lt.PutInboundMessage(msg); err != nil {
		t.Fatalf("PutInboundMessage failed: %v", err)
	}

	received, err := lt.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	if received.Content != "routed msg" {
		t.Errorf("Expected 'routed msg', got '%s'", received.Content)
	}
}

func TestLocalTransport_ContextCancellation(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Receive on empty channel should timeout
	_, err := lt.Receive(ctx)
	if err == nil {
		t.Error("Expected error on context cancellation")
	}
}

func TestLocalTransport_SendContextCancellation(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Fill the outbound channel
	for i := 0; i < defaultChannelCapacity; i++ {
		msg := domaintransport.NewMessage("A", "B", "s1", modulecore.NewTaskID(), "fill")
		lt.Send(context.Background(), msg)
	}

	cancel()

	// Send on full channel with cancelled context
	msg := domaintransport.NewMessage("A", "B", "s1", modulecore.NewTaskID(), "overflow")
	err := lt.Send(ctx, msg)
	if err == nil {
		t.Error("Expected error on cancelled context")
	}
}

func TestLocalTransport_Close(t *testing.T) {
	lt := NewLocalTransport()

	if err := lt.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if lt.IsHealthy() {
		t.Error("Should not be healthy after close")
	}

	// Send after close
	err := lt.Send(context.Background(), domaintransport.Message{})
	if err == nil {
		t.Error("Expected error on send after close")
	}

	// PutInbound after close
	err = lt.PutInboundMessage(domaintransport.Message{})
	if err == nil {
		t.Error("Expected error on put inbound after close")
	}
}

func TestLocalTransport_DoubleClose(t *testing.T) {
	lt := NewLocalTransport()

	if err := lt.Close(); err != nil {
		t.Fatalf("First close failed: %v", err)
	}

	// Second close should not panic
	if err := lt.Close(); err != nil {
		t.Fatalf("Second close failed: %v", err)
	}
}

func TestLocalTransport_IsHealthy(t *testing.T) {
	lt := NewLocalTransport()

	if !lt.IsHealthy() {
		t.Error("Should be healthy initially")
	}

	lt.Close()

	if lt.IsHealthy() {
		t.Error("Should not be healthy after close")
	}
}

func TestLocalTransport_ChannelFull(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	// Fill inbound channel
	for i := 0; i < defaultChannelCapacity; i++ {
		msg := domaintransport.NewMessage("R", "A", "s1", modulecore.NewTaskID(), "fill")
		if err := lt.PutInboundMessage(msg); err != nil {
			t.Fatalf("PutInboundMessage failed at %d: %v", i, err)
		}
	}

	// Next put should fail (non-blocking)
	msg := domaintransport.NewMessage("R", "A", "s1", modulecore.NewTaskID(), "overflow")
	err := lt.PutInboundMessage(msg)
	if err == nil {
		t.Error("Expected error when inbound channel is full")
	}
}

func TestLocalTransport_Receive_DoneClosed(t *testing.T) {
	lt := NewLocalTransport()

	// doneチャネルを閉じてからReceive → "transport is closed" エラー
	lt.Close()

	ctx := context.Background()
	_, err := lt.Receive(ctx)
	if err == nil {
		t.Error("Expected error on receive after close")
	}
}

func TestLocalTransport_Send_DoneClosed(t *testing.T) {
	lt := NewLocalTransport()

	// outboundを満杯にしてからclose → done経由のエラー
	for i := 0; i < defaultChannelCapacity; i++ {
		lt.Send(context.Background(), domaintransport.NewMessage("A", "B", "s1", modulecore.NewTaskID(), "fill"))
	}

	lt.Close()

	msg := domaintransport.NewMessage("A", "B", "s1", modulecore.NewTaskID(), "after-close")
	err := lt.Send(context.Background(), msg)
	if err == nil {
		t.Error("Expected error on send after close")
	}
}

func TestLocalTransport_Concurrent(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	var wg sync.WaitGroup
	const numSenders = 10
	const numMessages = 10

	// Concurrent senders
	for i := 0; i < numSenders; i++ {
		wg.Add(1)
		go func(sender int) {
			defer wg.Done()
			for j := 0; j < numMessages; j++ {
				msg := domaintransport.NewMessage("sender", "receiver", "s1", modulecore.NewTaskID(), "msg")
				lt.Send(context.Background(), msg)
			}
		}(i)
	}

	// Concurrent reader (drain outbound)
	received := 0
	done := make(chan struct{})
	go func() {
		for range lt.GetOutboundChannel() {
			received++
			if received >= numSenders*numMessages {
				close(done)
				return
			}
		}
	}()

	wg.Wait()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatalf("Timeout: received only %d/%d messages", received, numSenders*numMessages)
	}
}

func TestLocalDeliveryPreservesExecutionContextAndCancellation(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	executionCtx, cancel := context.WithCancel(context.Background())
	msg := domaintransport.NewMessage("mio", "shiro", "s1", modulecore.NewTaskID(), "execute")
	if err := lt.PutInboundExecution(executionCtx, msg); err != nil {
		t.Fatalf("PutInboundExecution failed: %v", err)
	}
	cancel()

	delivery, err := lt.ReceiveDelivery(context.Background())
	if err != nil {
		t.Fatalf("ReceiveDelivery failed: %v", err)
	}
	if delivery.Message.Content != msg.Content || delivery.Message.TaskID != msg.TaskID {
		t.Fatalf("delivery message=%#v, want %#v", delivery.Message, msg)
	}
	if delivery.Context != executionCtx {
		t.Fatalf("delivery context=%p, want original context=%p", delivery.Context, executionCtx)
	}
	if !errors.Is(delivery.Context.Err(), context.Canceled) {
		t.Fatalf("delivery context error=%v, want context.Canceled", delivery.Context.Err())
	}
}

func TestLocalDeliveryPreservesFIFOAcrossLegacyAndExecutionMessages(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	first := domaintransport.NewMessage("router", "shiro", "s1", modulecore.NewTaskID(), "legacy-first")
	middleCtx := context.Background()
	middle := domaintransport.NewMessage("mio", "shiro", "s1", modulecore.NewTaskID(), "execution-middle")
	last := domaintransport.NewMessage("router", "shiro", "s1", modulecore.NewTaskID(), "legacy-last")
	if err := lt.PutInboundMessage(first); err != nil {
		t.Fatalf("PutInboundMessage(first) failed: %v", err)
	}
	if err := lt.PutInboundExecution(middleCtx, middle); err != nil {
		t.Fatalf("PutInboundExecution(middle) failed: %v", err)
	}
	if err := lt.PutInboundMessage(last); err != nil {
		t.Fatalf("PutInboundMessage(last) failed: %v", err)
	}

	want := []struct {
		content string
		ctx     context.Context
	}{
		{content: first.Content},
		{content: middle.Content, ctx: middleCtx},
		{content: last.Content},
	}
	for i, expected := range want {
		delivery, err := lt.ReceiveDelivery(context.Background())
		if err != nil {
			t.Fatalf("ReceiveDelivery(%d) failed: %v", i, err)
		}
		if delivery.Message.Content != expected.content {
			t.Fatalf("delivery(%d) content=%q, want %q", i, delivery.Message.Content, expected.content)
		}
		if delivery.Context != expected.ctx {
			t.Fatalf("delivery(%d) context=%p, want %p", i, delivery.Context, expected.ctx)
		}
	}
}

func TestLocalDeliveryRejectsNilOrCanceledExecutionContext(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	msg := domaintransport.NewMessage("mio", "shiro", "s1", modulecore.NewTaskID(), "execute")
	if err := lt.PutInboundExecution(nil, msg); err == nil {
		t.Fatal("PutInboundExecution accepted nil context")
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lt.PutInboundExecution(canceledCtx, msg); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutInboundExecution canceled error=%v, want context.Canceled", err)
	}
	if got := len(lt.inbound); got != 0 {
		t.Fatalf("rejected executions left %d queued deliveries", got)
	}
}

func TestLocalDeliveryClosedTransportRefusesQueuedDelivery(t *testing.T) {
	lt := NewLocalTransport()
	msg := domaintransport.NewMessage("router", "shiro", "s1", modulecore.NewTaskID(), "queued")
	if err := lt.PutInboundMessage(msg); err != nil {
		t.Fatalf("PutInboundMessage failed: %v", err)
	}
	if err := lt.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if _, err := lt.ReceiveDelivery(context.Background()); err == nil {
		t.Fatal("ReceiveDelivery accepted queued delivery after close")
	}
	if got := len(lt.inbound); got != 1 {
		t.Fatalf("closed receive consumed queued delivery: len=%d, want 1", got)
	}
}

func TestLocalDeliveryCanceledReceiverRefusesQueuedDelivery(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	msg := domaintransport.NewMessage("router", "shiro", "s1", modulecore.NewTaskID(), "queued")
	if err := lt.PutInboundMessage(msg); err != nil {
		t.Fatalf("PutInboundMessage failed: %v", err)
	}
	receiveCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := lt.ReceiveDelivery(receiveCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReceiveDelivery canceled error=%v, want context.Canceled", err)
	}
	if got := len(lt.inbound); got != 1 {
		t.Fatalf("canceled receive consumed queued delivery: len=%d, want 1", got)
	}
	delivery, err := lt.ReceiveDelivery(context.Background())
	if err != nil {
		t.Fatalf("ReceiveDelivery after canceled receiver failed: %v", err)
	}
	if delivery.Message.Content != msg.Content {
		t.Fatalf("recovered delivery content=%q, want %q", delivery.Message.Content, msg.Content)
	}
}
