package transport

import (
	"context"
	"fmt"
	"log"
	"sync"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

const defaultChannelCapacity = 100

// LocalDelivery carries a message and its trusted in-process execution context.
// The embedded Message keeps the legacy field access used by local callers while
// preserving the context outside the serialized transport representation.
type LocalDelivery struct {
	domaintransport.Message
	Context context.Context
}

// LocalTransport はローカル（同一プロセス内）のAgent間通信
type LocalTransport struct {
	inbound  chan LocalDelivery
	outbound chan domaintransport.Message
	done     chan struct{}
	mu       sync.Mutex
	closed   bool
}

// NewLocalTransport は新しいLocalTransportを作成
func NewLocalTransport() *LocalTransport {
	return &LocalTransport{
		inbound:  make(chan LocalDelivery, defaultChannelCapacity),
		outbound: make(chan domaintransport.Message, defaultChannelCapacity),
		done:     make(chan struct{}),
	}
}

// Send はメッセージを送信（outboundチャネルに書き込み）
func (t *LocalTransport) Send(ctx context.Context, msg domaintransport.Message) error {
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("validate outbound message: %w", err)
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("transport is closed")
	}
	t.mu.Unlock()

	select {
	case t.outbound <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return fmt.Errorf("transport is closed")
	}
}

// ReceiveDelivery receives one local delivery, including its in-process context.
// A delivery's context is intentionally not inspected here; request handlers
// decide how to report a canceled or otherwise invalid execution context.
func (t *LocalTransport) ReceiveDelivery(ctx context.Context) (LocalDelivery, error) {
	if ctx == nil {
		return LocalDelivery{}, fmt.Errorf("receive context is nil")
	}
	if err := ctx.Err(); err != nil {
		log.Printf("[LocalTransport] recv canceled err=%v", err)
		return LocalDelivery{}, err
	}

	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return LocalDelivery{}, fmt.Errorf("transport is closed")
	}

	select {
	case delivery, ok := <-t.inbound:
		if !ok {
			return LocalDelivery{}, fmt.Errorf("transport is closed")
		}
		if err := ctx.Err(); err != nil {
			log.Printf("[LocalTransport] recv canceled err=%v", err)
			return LocalDelivery{}, err
		}
		t.mu.Lock()
		closed = t.closed
		t.mu.Unlock()
		if closed {
			return LocalDelivery{}, fmt.Errorf("transport is closed")
		}
		msg := delivery.Message
		log.Printf("[LocalTransport] recv from=%s to=%s type=%s task=%s", msg.From, msg.To, msg.Type, msg.TaskID)
		return delivery, nil
	case <-ctx.Done():
		log.Printf("[LocalTransport] recv canceled err=%v", ctx.Err())
		return LocalDelivery{}, ctx.Err()
	case <-t.done:
		return LocalDelivery{}, fmt.Errorf("transport is closed")
	}
}

// Receive はメッセージを受信（inboundチャネルから読み取り）
func (t *LocalTransport) Receive(ctx context.Context) (domaintransport.Message, error) {
	delivery, err := t.ReceiveDelivery(ctx)
	if err != nil {
		return domaintransport.Message{}, err
	}
	return delivery.Message, nil
}

// Close はTransportを閉じる
func (t *LocalTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil // 冪等
	}

	t.closed = true
	close(t.done)
	return nil
}

// IsHealthy はTransportの健全性を返す
func (t *LocalTransport) IsHealthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed
}

// GetOutboundChannel はoutboundチャネルを返す（MessageRouter用）
func (t *LocalTransport) GetOutboundChannel() <-chan domaintransport.Message {
	return t.outbound
}

// PutInboundExecution enqueues a locally trusted execution request together
// with the caller's already-bound execution context.
func (t *LocalTransport) PutInboundExecution(ctx context.Context, msg domaintransport.Message) error {
	if ctx == nil {
		return fmt.Errorf("execution context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return t.enqueue(LocalDelivery{Message: msg, Context: ctx})
}

// PutInboundMessage はinboundチャネルにメッセージを投入（ノンブロッキング）
func (t *LocalTransport) PutInboundMessage(msg domaintransport.Message) error {
	return t.enqueue(LocalDelivery{Message: msg})
}

func (t *LocalTransport) enqueue(delivery LocalDelivery) error {
	msg := delivery.Message
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("validate inbound message: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("transport is closed")
	}

	select {
	case t.inbound <- delivery:
		log.Printf("[LocalTransport] enqueue from=%s to=%s type=%s task=%s", msg.From, msg.To, msg.Type, msg.TaskID)
		return nil
	default:
		log.Printf("[LocalTransport] enqueue drop from=%s to=%s type=%s task=%s reason=inbound_full", msg.From, msg.To, msg.Type, msg.TaskID)
		return fmt.Errorf("inbound channel full for agent, message dropped")
	}
}
