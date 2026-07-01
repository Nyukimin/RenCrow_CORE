package session

import (
	"fmt"
	"sync"
	"testing"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

func TestAgentMemory_AddAndGet(t *testing.T) {
	m := NewAgentMemory("mio")

	msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "hello")
	m.Add(msg)

	if m.Count() != 1 {
		t.Fatalf("Expected 1 entry, got %d", m.Count())
	}

	entries := m.GetAll()
	if entries[0].Message.Content != "hello" {
		t.Errorf("Expected 'hello', got '%s'", entries[0].Message.Content)
	}
}

func TestAgentMemory_FIFOEviction(t *testing.T) {
	m := NewAgentMemory("mio")

	// maxConversations+10 を追加
	for i := 0; i < maxConversations+10; i++ {
		msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", fmt.Sprintf("msg-%d", i))
		m.Add(msg)
	}

	if m.Count() != maxConversations {
		t.Errorf("Expected %d entries after eviction, got %d", maxConversations, m.Count())
	}

	// 最古のメッセージは msg-10 であるべき（0-9 がevict）
	entries := m.GetAll()
	if entries[0].Message.Content != "msg-10" {
		t.Errorf("Expected oldest to be 'msg-10', got '%s'", entries[0].Message.Content)
	}
}

func TestAgentMemory_GetRecent(t *testing.T) {
	m := NewAgentMemory("mio")

	for i := 0; i < 20; i++ {
		msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", fmt.Sprintf("msg-%d", i))
		m.Add(msg)
	}

	recent := m.GetRecent(5)
	if len(recent) != 5 {
		t.Fatalf("Expected 5 recent entries, got %d", len(recent))
	}

	if recent[0].Message.Content != "msg-15" {
		t.Errorf("Expected 'msg-15', got '%s'", recent[0].Message.Content)
	}
	if recent[4].Message.Content != "msg-19" {
		t.Errorf("Expected 'msg-19', got '%s'", recent[4].Message.Content)
	}
}

func TestAgentMemory_GetRecentMoreThanAvailable(t *testing.T) {
	m := NewAgentMemory("mio")

	msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "hello")
	m.Add(msg)

	recent := m.GetRecent(100)
	if len(recent) != 1 {
		t.Errorf("Expected 1 entry, got %d", len(recent))
	}
}

func TestAgentMemory_Clear(t *testing.T) {
	m := NewAgentMemory("mio")

	for i := 0; i < 5; i++ {
		m.Add(domaintransport.NewMessage("mio", "shiro", "s1", "j1", "msg"))
	}

	m.Clear()
	if m.Count() != 0 {
		t.Errorf("Expected 0 after clear, got %d", m.Count())
	}
}

func TestAgentMemory_Concurrent(t *testing.T) {
	m := NewAgentMemory("mio")
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				m.Add(domaintransport.NewMessage("mio", "shiro", "s1", "j1", fmt.Sprintf("msg-%d-%d", id, j)))
			}
		}(i)
	}

	wg.Wait()
	if m.Count() != 100 {
		t.Errorf("Expected 100 entries, got %d", m.Count())
	}
}

func TestCentralMemory_RecordAndRetrieve(t *testing.T) {
	cm := NewCentralMemory()

	msg := domaintransport.NewMessage("mio", "shiro", "s1", "j1", "hello")
	cm.RecordMessage(msg)

	if cm.AgentCount() != 2 {
		t.Errorf("Expected 2 agents (Mio, Shiro), got %d", cm.AgentCount())
	}

	mioMemory := cm.GetOrCreateAgent("mio")
	if mioMemory.Count() != 1 {
		t.Errorf("Expected 1 entry for Mio, got %d", mioMemory.Count())
	}

	shiroMemory := cm.GetOrCreateAgent("shiro")
	if shiroMemory.Count() != 1 {
		t.Errorf("Expected 1 entry for Shiro, got %d", shiroMemory.Count())
	}
}

func TestCentralMemory_GetUnifiedView(t *testing.T) {
	cm := NewCentralMemory()

	cm.RecordMessage(domaintransport.NewMessage("mio", "shiro", "s1", "j1", "msg1"))
	cm.RecordMessage(domaintransport.NewMessage("shiro", "mio", "s1", "j1", "msg2"))
	cm.RecordMessage(domaintransport.NewMessage("mio", "aka", "s1", "j1", "msg3"))

	view := cm.GetUnifiedView(0)
	if len(view) != 3 {
		t.Errorf("Expected 3 unique entries in unified view, got %d", len(view))
	}
}

func TestCentralMemory_GetUnifiedViewWithLimit(t *testing.T) {
	cm := NewCentralMemory()

	for i := 0; i < 10; i++ {
		cm.RecordMessage(domaintransport.NewMessage("mio", "shiro", "s1", "j1", fmt.Sprintf("msg-%d", i)))
	}

	view := cm.GetUnifiedView(3)
	if len(view) != 3 {
		t.Errorf("Expected 3 entries with limit, got %d", len(view))
	}
}

func TestCentralMemory_AgentNames(t *testing.T) {
	cm := NewCentralMemory()

	cm.RecordMessage(domaintransport.NewMessage("mio", "shiro", "s1", "j1", "hello"))
	cm.RecordMessage(domaintransport.NewMessage("aka", "ao", "s1", "j1", "hi"))

	names := cm.AgentNames()
	if len(names) != 4 {
		t.Errorf("Expected 4 agents, got %d", len(names))
	}

	// ソートされていることを確認
	expected := []string{"aka", "ao", "mio", "shiro"}
	for i, name := range expected {
		if names[i] != name {
			t.Errorf("Expected agent[%d]='%s', got '%s'", i, name, names[i])
		}
	}
}

func TestAgentMemory_AgentName(t *testing.T) {
	m := NewAgentMemory("shiro")

	if m.AgentName() != "shiro" {
		t.Errorf("Expected 'Shiro', got '%s'", m.AgentName())
	}
}

func TestCentralMemory_SelfMessage(t *testing.T) {
	cm := NewCentralMemory()

	// 自分宛メッセージ（From==To）
	msg := domaintransport.NewMessage("mio", "mio", "s1", "j1", "self note")
	cm.RecordMessage(msg)

	if cm.AgentCount() != 1 {
		t.Errorf("Expected 1 agent for self-message, got %d", cm.AgentCount())
	}

	mioMemory := cm.GetOrCreateAgent("mio")
	if mioMemory.Count() != 1 {
		t.Errorf("Expected 1 entry for self-message, got %d", mioMemory.Count())
	}
}
