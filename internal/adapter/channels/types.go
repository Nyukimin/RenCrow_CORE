package channels

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ChannelEvent はチャネル依存の入力を正規化した共通イベント
// Raw はデバッグ用に元イベントを保持する。
type ChannelEvent struct {
	Channel   string    `json:"channel"`
	ChatID    string    `json:"chat_id"`
	UserID    string    `json:"user_id"`
	MessageID string    `json:"message_id,omitempty"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	Raw       []byte    `json:"raw,omitempty"`
}

// Adapter はチャネル送信・疎通確認の最小契約
type Adapter interface {
	Name() string
	Send(ctx context.Context, chatID, text string) error
	Probe(ctx context.Context) error
}

// Registry は有効チャネルを一元管理する
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

func (r *Registry) Register(adapter Adapter) error {
	if adapter == nil {
		return fmt.Errorf("adapter is nil")
	}
	name := adapter.Name()
	if name == "" {
		return fmt.Errorf("adapter name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[name] = adapter
	return nil
}

func (r *Registry) Get(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) ProbeAll(ctx context.Context) map[string]error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]error, len(r.adapters))
	for name, adapter := range r.adapters {
		out[name] = adapter.Probe(ctx)
	}
	return out
}
