package transport

import (
	"context"
	"testing"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

func TestMessageRouter_RouteMessage(t *testing.T) {
	router := NewMessageRouter()
	defer router.Stop()

	mioTransport := NewLocalTransport()
	shiroTransport := NewLocalTransport()
	defer mioTransport.Close()
	defer shiroTransport.Close()

	router.RegisterAgent("mio", mioTransport)
	router.RegisterAgent("shiro", shiroTransport)

	// Mio → Shiro
	msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "hello Shiro")
	if err := mioTransport.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Shiro should receive
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received, err := shiroTransport.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	if received.From != "mio" || received.Content != "hello Shiro" {
		t.Errorf("Unexpected message: %+v", received)
	}
}

func TestMessageRouter_UnknownAgent(t *testing.T) {
	router := NewMessageRouter()
	defer router.Stop()

	mioTransport := NewLocalTransport()
	defer mioTransport.Close()

	router.RegisterAgent("mio", mioTransport)

	// Mio → Unknown (should get error back)
	msg := domaintransport.NewMessage("mio", "NonExistent", "s1", "j1", "hello?")
	if err := mioTransport.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Mio should receive error message
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errMsg, err := mioTransport.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive error message failed: %v", err)
	}

	if errMsg.Type != domaintransport.MessageTypeError {
		t.Errorf("Expected error message type, got '%s'", errMsg.Type)
	}

	if errMsg.From != "Router" {
		t.Errorf("Expected error from 'Router', got '%s'", errMsg.From)
	}
}

func TestMessageRouter_MultipleAgents(t *testing.T) {
	router := NewMessageRouter()
	defer router.Stop()

	agents := map[string]*LocalTransport{
		"mio":   NewLocalTransport(),
		"shiro": NewLocalTransport(),
		"aka":   NewLocalTransport(),
		"ao":    NewLocalTransport(),
		"gin":   NewLocalTransport(),
	}
	for name, transport := range agents {
		defer transport.Close()
		router.RegisterAgent(name, transport)
	}

	if router.AgentCount() != 5 {
		t.Errorf("Expected 5 agents, got %d", router.AgentCount())
	}

	// Mio → Gin
	msg := domaintransport.NewMessage("mio", "gin", "s1", "j1", "hello Gin")
	agents["mio"].Send(context.Background(), msg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received, err := agents["gin"].Receive(ctx)
	if err != nil {
		t.Fatalf("Gin receive failed: %v", err)
	}

	if received.Content != "hello Gin" {
		t.Errorf("Expected 'hello Gin', got '%s'", received.Content)
	}
}

func TestMessageRouter_Stop(t *testing.T) {
	router := NewMessageRouter()

	transport := NewLocalTransport()
	defer transport.Close()

	router.RegisterAgent("Test", transport)

	// Stop should complete within reasonable time
	done := make(chan struct{})
	go func() {
		router.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Router.Stop() timed out")
	}
}

func TestMessageRouter_UnregisterAgent(t *testing.T) {
	router := NewMessageRouter()
	defer router.Stop()

	transport := NewLocalTransport()
	defer transport.Close()

	router.RegisterAgent("Test", transport)
	if router.AgentCount() != 1 {
		t.Errorf("Expected 1 agent, got %d", router.AgentCount())
	}

	router.UnregisterAgent("Test")
	if router.AgentCount() != 0 {
		t.Errorf("Expected 0 agents, got %d", router.AgentCount())
	}
}

func TestMessageRouter_GetAgent(t *testing.T) {
	router := NewMessageRouter()
	defer router.Stop()

	transport := NewLocalTransport()
	defer transport.Close()

	router.RegisterAgent("mio", transport)

	got, ok := router.GetAgent("mio")
	if !ok {
		t.Fatal("Expected to find agent 'Mio'")
	}
	if got != transport {
		t.Error("Expected same transport reference")
	}

	_, ok = router.GetAgent("NonExistent")
	if ok {
		t.Error("Expected not to find non-existent agent")
	}
}

func TestMessageRouter_RoundTrip(t *testing.T) {
	router := NewMessageRouter()
	defer router.Stop()

	mioTransport := NewLocalTransport()
	shiroTransport := NewLocalTransport()
	defer mioTransport.Close()
	defer shiroTransport.Close()

	router.RegisterAgent("mio", mioTransport)
	router.RegisterAgent("shiro", shiroTransport)

	// Mio sends request to Shiro
	request := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "execute task")
	mioTransport.Send(context.Background(), request)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Shiro receives
	received, err := shiroTransport.Receive(ctx)
	if err != nil {
		t.Fatalf("Shiro receive failed: %v", err)
	}

	// Shiro sends response back to Mio
	response := domaintransport.NewMessage("shiro", "mio", received.SessionID, received.JobID, "task done")
	response.Type = domaintransport.MessageTypeResult
	shiroTransport.Send(context.Background(), response)

	// Mio receives response
	result, err := mioTransport.Receive(ctx)
	if err != nil {
		t.Fatalf("Mio receive response failed: %v", err)
	}

	if result.Content != "task done" {
		t.Errorf("Expected 'task done', got '%s'", result.Content)
	}
	if result.Type != domaintransport.MessageTypeResult {
		t.Errorf("Expected result type, got '%s'", result.Type)
	}
}

func TestMessageRouter_DeliverMessage_InboundFull(t *testing.T) {
	router := NewMessageRouter()
	defer router.Stop()

	mioTransport := NewLocalTransport()
	shiroTransport := NewLocalTransport()
	defer mioTransport.Close()
	defer shiroTransport.Close()

	router.RegisterAgent("mio", mioTransport)
	router.RegisterAgent("shiro", shiroTransport)

	// Shiroのinboundチャネルを満杯にする
	for i := 0; i < defaultChannelCapacity; i++ {
		shiroTransport.PutInboundMessage(domaintransport.NewMessage("X", "shiro", "s1", "j1", "fill"))
	}

	// Mio → Shiro（inbound full → deliverMessage のエラーパス → Mioにエラー返送）
	msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "should-fail")
	mioTransport.Send(context.Background(), msg)

	// Mioがエラーメッセージを受信するはず
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errMsg, err := mioTransport.Receive(ctx)
	if err != nil {
		t.Fatalf("Mio should receive error message: %v", err)
	}
	if errMsg.Type != domaintransport.MessageTypeError {
		t.Errorf("Expected error type, got '%s'", errMsg.Type)
	}
}
