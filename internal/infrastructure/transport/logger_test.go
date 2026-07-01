package transport

import (
	"context"
	"fmt"
	"testing"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

// mockTransport はテスト用のモックTransport
type mockTransport struct {
	sendErr    error
	receiveMsg domaintransport.Message
	receiveErr error
	closeErr   error
	closed     bool
	healthy    bool
}

func (m *mockTransport) Send(ctx context.Context, msg domaintransport.Message) error {
	return m.sendErr
}

func (m *mockTransport) Receive(ctx context.Context) (domaintransport.Message, error) {
	return m.receiveMsg, m.receiveErr
}

func (m *mockTransport) Close() error {
	m.closed = true
	return m.closeErr
}

func (m *mockTransport) IsHealthy() bool {
	return m.healthy
}

func TestLoggingTransport_Send(t *testing.T) {
	inner := &mockTransport{healthy: true}
	lt := NewLoggingTransport(inner, "mio")

	msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "hello")
	err := lt.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
}

func TestLoggingTransport_SendError(t *testing.T) {
	inner := &mockTransport{sendErr: fmt.Errorf("send failed"), healthy: true}
	lt := NewLoggingTransport(inner, "mio")

	msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "hello")
	err := lt.Send(context.Background(), msg)
	if err == nil {
		t.Error("Expected send error")
	}
}

func TestLoggingTransport_Receive(t *testing.T) {
	expectedMsg := domaintransport.NewMessage("shiro", "mio", "s1", "j1", "response")
	inner := &mockTransport{receiveMsg: expectedMsg, healthy: true}
	lt := NewLoggingTransport(inner, "mio")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msg, err := lt.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	if msg.Content != "response" {
		t.Errorf("Expected 'response', got '%s'", msg.Content)
	}
}

func TestLoggingTransport_ReceiveError(t *testing.T) {
	inner := &mockTransport{receiveErr: fmt.Errorf("timeout"), healthy: true}
	lt := NewLoggingTransport(inner, "mio")

	_, err := lt.Receive(context.Background())
	if err == nil {
		t.Error("Expected receive error")
	}
}

func TestLoggingTransport_Close(t *testing.T) {
	inner := &mockTransport{healthy: true}
	lt := NewLoggingTransport(inner, "mio")

	if err := lt.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !inner.closed {
		t.Error("Inner transport should be closed")
	}
}

func TestLoggingTransport_IsHealthy(t *testing.T) {
	inner := &mockTransport{healthy: true}
	lt := NewLoggingTransport(inner, "mio")

	if !lt.IsHealthy() {
		t.Error("Should be healthy when inner is healthy")
	}

	inner.healthy = false
	if lt.IsHealthy() {
		t.Error("Should not be healthy when inner is not healthy")
	}
}

func TestLoggingTransport_CloseError(t *testing.T) {
	inner := &mockTransport{closeErr: fmt.Errorf("close failed"), healthy: true}
	lt := NewLoggingTransport(inner, "mio")

	err := lt.Close()
	if err == nil {
		t.Error("Expected close error")
	}
	if !inner.closed {
		t.Error("Inner should still be marked closed")
	}
}
