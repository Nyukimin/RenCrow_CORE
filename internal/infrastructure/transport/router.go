package transport

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

// MessageRouter はAgent間メッセージルーティングを管理
type MessageRouter struct {
	agents map[string]*LocalTransport
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewMessageRouter は新しいMessageRouterを作成
func NewMessageRouter() *MessageRouter {
	ctx, cancel := context.WithCancel(context.Background())
	return &MessageRouter{
		agents: make(map[string]*LocalTransport),
		ctx:    ctx,
		cancel: cancel,
	}
}

// RegisterAgent はAgentのTransportを登録し、forwardLoopを開始
func (r *MessageRouter) RegisterAgent(name string, transport *LocalTransport) {
	r.mu.Lock()
	r.agents[name] = transport
	r.mu.Unlock()

	r.wg.Add(1)
	go r.forwardLoop(name, transport)

	log.Printf("[Router] Agent registered: %s", name)
}

// UnregisterAgent はAgentの登録を解除
func (r *MessageRouter) UnregisterAgent(name string) {
	r.mu.Lock()
	delete(r.agents, name)
	r.mu.Unlock()

	log.Printf("[Router] Agent unregistered: %s", name)
}

// Stop はRouterを停止（10秒タイムアウト付き）
func (r *MessageRouter) Stop() {
	r.cancel()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("[Router] All forward loops stopped")
	case <-time.After(10 * time.Second):
		log.Println("[Router] WARN: Stop timed out after 10 seconds")
	}
}

// GetAgent は登録されたAgentのTransportを返す
func (r *MessageRouter) GetAgent(name string) (*LocalTransport, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.agents[name]
	return t, ok
}

// AgentCount は登録されたAgent数を返す
func (r *MessageRouter) AgentCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}

// forwardLoop はAgent毎のgoroutineで、outboundメッセージをルーティング
func (r *MessageRouter) forwardLoop(agentName string, transport *LocalTransport) {
	defer r.wg.Done()

	outbound := transport.GetOutboundChannel()
	for {
		select {
		case <-r.ctx.Done():
			return
		case msg, ok := <-outbound:
			if !ok {
				return
			}
			r.deliverMessage(agentName, msg)
		}
	}
}

// deliverMessage はメッセージをターゲットAgentに配信
func (r *MessageRouter) deliverMessage(senderName string, msg domaintransport.Message) {
	r.mu.RLock()
	target, exists := r.agents[msg.To]
	r.mu.RUnlock()

	if !exists {
		log.Printf("[Router] WARN: target agent '%s' not found (from: %s)", msg.To, senderName)
		r.sendDeliveryError(senderName, msg)
		return
	}

	if err := target.PutInboundMessage(msg); err != nil {
		log.Printf("[Router] WARN: delivery failed to '%s': %v", msg.To, err)
		r.sendDeliveryError(senderName, msg)
	}
}

// sendDeliveryError は配信失敗を送信元に通知
func (r *MessageRouter) sendDeliveryError(senderName string, originalMsg domaintransport.Message) {
	r.mu.RLock()
	sender, exists := r.agents[senderName]
	r.mu.RUnlock()

	if !exists {
		return
	}

	errMsg := domaintransport.NewErrorMessage(
		"Router",
		senderName,
		originalMsg.SessionID,
		originalMsg.JobID,
		fmt.Sprintf("delivery failed: agent '%s' not found or unavailable", originalMsg.To),
	)

	// ベストエフォートで通知（失敗しても無視）
	_ = sender.PutInboundMessage(errMsg)
}
