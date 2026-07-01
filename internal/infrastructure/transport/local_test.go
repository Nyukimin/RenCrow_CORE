package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

func TestLocalTransport_SendReceive(t *testing.T) {
	lt := NewLocalTransport()
	defer lt.Close()

	ctx := context.Background()
	msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "hello")

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
	msg := domaintransport.NewMessage("Router", "mio", "s1", "j1", "routed msg")

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
		msg := domaintransport.NewMessage("A", "B", "s1", "j1", "fill")
		lt.Send(context.Background(), msg)
	}

	cancel()

	// Send on full channel with cancelled context
	msg := domaintransport.NewMessage("A", "B", "s1", "j1", "overflow")
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
		msg := domaintransport.NewMessage("R", "A", "s1", "j1", "fill")
		if err := lt.PutInboundMessage(msg); err != nil {
			t.Fatalf("PutInboundMessage failed at %d: %v", i, err)
		}
	}

	// Next put should fail (non-blocking)
	msg := domaintransport.NewMessage("R", "A", "s1", "j1", "overflow")
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
		lt.Send(context.Background(), domaintransport.NewMessage("A", "B", "s1", "j1", "fill"))
	}

	lt.Close()

	msg := domaintransport.NewMessage("A", "B", "s1", "j1", "after-close")
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
				msg := domaintransport.NewMessage("sender", "receiver", "s1", "j1", "msg")
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
