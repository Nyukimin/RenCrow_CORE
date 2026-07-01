package session

import (
	"sort"
	"sync"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

const maxConversations = 100

// ConversationEntry はAgent間会話の1エントリ
type ConversationEntry struct {
	Message   domaintransport.Message
	Timestamp time.Time
}

// AgentMemory はAgent毎の会話メモリ
// MaxConversations=100、FIFO eviction
type AgentMemory struct {
	agentName     string
	conversations []ConversationEntry
	mu            sync.RWMutex
}

// NewAgentMemory は新しいAgentMemoryを作成
func NewAgentMemory(agentName string) *AgentMemory {
	return &AgentMemory{
		agentName:     agentName,
		conversations: make([]ConversationEntry, 0),
	}
}

// AgentName はAgent名を返す
func (m *AgentMemory) AgentName() string {
	return m.agentName
}

// Add は会話エントリを追加（FIFO eviction）
func (m *AgentMemory) Add(msg domaintransport.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := ConversationEntry{
		Message:   msg,
		Timestamp: time.Now(),
	}

	m.conversations = append(m.conversations, entry)

	// FIFO eviction
	if len(m.conversations) > maxConversations {
		m.conversations = m.conversations[len(m.conversations)-maxConversations:]
	}
}

// GetRecent は最近N件のエントリを返す
func (m *AgentMemory) GetRecent(n int) []ConversationEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.conversations) <= n {
		result := make([]ConversationEntry, len(m.conversations))
		copy(result, m.conversations)
		return result
	}

	result := make([]ConversationEntry, n)
	copy(result, m.conversations[len(m.conversations)-n:])
	return result
}

// GetAll は全エントリを返す
func (m *AgentMemory) GetAll() []ConversationEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]ConversationEntry, len(m.conversations))
	copy(result, m.conversations)
	return result
}

// Count はエントリ数を返す
func (m *AgentMemory) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.conversations)
}

// Clear はメモリをクリア
func (m *AgentMemory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conversations = make([]ConversationEntry, 0)
}

// CentralMemory はMio統合ビュー（全Agentの会話を統合）
type CentralMemory struct {
	agents map[string]*AgentMemory
	mu     sync.RWMutex
}

// NewCentralMemory は新しいCentralMemoryを作成
func NewCentralMemory() *CentralMemory {
	return &CentralMemory{
		agents: make(map[string]*AgentMemory),
	}
}

// GetOrCreateAgent はAgent名に対応するAgentMemoryを取得/作成
func (cm *CentralMemory) GetOrCreateAgent(agentName string) *AgentMemory {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if m, ok := cm.agents[agentName]; ok {
		return m
	}

	m := NewAgentMemory(agentName)
	cm.agents[agentName] = m
	return m
}

// RecordMessage はメッセージをFrom/ToのAgentMemoryに記録
func (cm *CentralMemory) RecordMessage(msg domaintransport.Message) {
	// 送信元のメモリに記録
	fromMemory := cm.GetOrCreateAgent(msg.From)
	fromMemory.Add(msg)

	// 送信先のメモリにも記録
	if msg.To != msg.From {
		toMemory := cm.GetOrCreateAgent(msg.To)
		toMemory.Add(msg)
	}
}

// GetUnifiedView は全Agentの会話をタイムスタンプ順にソートして返す
func (cm *CentralMemory) GetUnifiedView(limit int) []ConversationEntry {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// 重複排除用のmap（同じメッセージがFrom/To両方に記録されるため）
	seen := make(map[string]bool)
	var all []ConversationEntry

	for _, m := range cm.agents {
		entries := m.GetAll()
		for _, e := range entries {
			// From+To+Timestamp+Contentで一意キー生成
			key := e.Message.From + "|" + e.Message.To + "|" + e.Message.Timestamp + "|" + e.Message.Content
			if !seen[key] {
				seen[key] = true
				all = append(all, e)
			}
		}
	}

	// タイムスタンプ順ソート
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})

	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}

	return all
}

// AgentCount は登録されたAgent数を返す
func (cm *CentralMemory) AgentCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.agents)
}

// AgentNames は登録されたAgent名のリストを返す
func (cm *CentralMemory) AgentNames() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	names := make([]string, 0, len(cm.agents))
	for name := range cm.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
