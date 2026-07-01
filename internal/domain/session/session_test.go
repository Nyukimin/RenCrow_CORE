package session

import (
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

func TestNewSession(t *testing.T) {
	session := NewSession("20260301-line-U123", "line", "U123")

	if session.ID() != "20260301-line-U123" {
		t.Errorf("Expected ID '20260301-line-U123', got '%s'", session.ID())
	}

	if session.Channel() != "line" {
		t.Errorf("Expected channel 'line', got '%s'", session.Channel())
	}

	if session.ChatID() != "U123" {
		t.Errorf("Expected chatID 'U123', got '%s'", session.ChatID())
	}

	if session.HistoryCount() != 0 {
		t.Errorf("Expected 0 history count, got %d", session.HistoryCount())
	}

	// 作成時刻は現在時刻に近い
	now := time.Now()
	if session.CreatedAt().After(now) || session.CreatedAt().Before(now.Add(-1*time.Second)) {
		t.Error("CreatedAt should be close to current time")
	}
}

func TestSessionAddTask(t *testing.T) {
	session := NewSession("session1", "line", "U123")
	jobID := task.NewJobID()
	newTask := task.NewTask(jobID, "Hello", "line", "U123")

	session.AddTask(newTask)

	if session.HistoryCount() != 1 {
		t.Errorf("Expected 1 task in history, got %d", session.HistoryCount())
	}

	history := session.GetHistory()
	if len(history) != 1 {
		t.Fatalf("Expected 1 task in history slice, got %d", len(history))
	}

	if history[0].UserMessage() != "Hello" {
		t.Errorf("Expected task message 'Hello', got '%s'", history[0].UserMessage())
	}
}

func TestSessionGetRecentHistory(t *testing.T) {
	session := NewSession("session1", "line", "U123")

	// 5つのタスクを追加
	for i := 1; i <= 5; i++ {
		jobID := task.NewJobID()
		newTask := task.NewTask(jobID, string(rune('A'+i-1)), "line", "U123")
		session.AddTask(newTask)
	}

	// 最近3件取得
	recent := session.GetRecentHistory(3)
	if len(recent) != 3 {
		t.Fatalf("Expected 3 recent tasks, got %d", len(recent))
	}

	// 最新の3件（C, D, E）が取得される
	if recent[0].UserMessage() != "C" {
		t.Errorf("Expected first recent task 'C', got '%s'", recent[0].UserMessage())
	}

	if recent[2].UserMessage() != "E" {
		t.Errorf("Expected last recent task 'E', got '%s'", recent[2].UserMessage())
	}

	// 全件より多い数を指定した場合は全件返る
	allRecent := session.GetRecentHistory(10)
	if len(allRecent) != 5 {
		t.Errorf("Expected 5 tasks when requesting 10, got %d", len(allRecent))
	}
}

func TestSessionMemory(t *testing.T) {
	session := NewSession("session1", "line", "U123")

	// メモリ設定
	session.SetMemory("key1", "value1")
	session.SetMemory("key2", 42)

	// メモリ取得
	val1, ok1 := session.GetMemory("key1")
	if !ok1 {
		t.Error("Expected key1 to exist")
	}
	if val1 != "value1" {
		t.Errorf("Expected value1, got %v", val1)
	}

	val2, ok2 := session.GetMemory("key2")
	if !ok2 {
		t.Error("Expected key2 to exist")
	}
	if val2 != 42 {
		t.Errorf("Expected 42, got %v", val2)
	}

	// 存在しないキー
	_, ok3 := session.GetMemory("nonexistent")
	if ok3 {
		t.Error("Expected nonexistent key to return false")
	}
}

func TestSessionClearMemory(t *testing.T) {
	session := NewSession("session1", "line", "U123")

	session.SetMemory("key1", "value1")
	session.SetMemory("key2", "value2")

	session.ClearMemory()

	_, ok := session.GetMemory("key1")
	if ok {
		t.Error("Memory should be cleared")
	}
}

func TestReconstructSession(t *testing.T) {
	createdAt := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	s := ReconstructSession("sid-1", "line", "U999", createdAt, updatedAt)

	if s.ID() != "sid-1" {
		t.Errorf("Expected ID 'sid-1', got '%s'", s.ID())
	}
	if s.Channel() != "line" {
		t.Errorf("Expected channel 'line', got '%s'", s.Channel())
	}
	if s.ChatID() != "U999" {
		t.Errorf("Expected chatID 'U999', got '%s'", s.ChatID())
	}
	if !s.CreatedAt().Equal(createdAt) {
		t.Errorf("Expected createdAt %v, got %v", createdAt, s.CreatedAt())
	}
	if !s.UpdatedAt().Equal(updatedAt) {
		t.Errorf("Expected updatedAt %v, got %v", updatedAt, s.UpdatedAt())
	}
	if s.HistoryCount() != 0 {
		t.Errorf("Expected 0 history, got %d", s.HistoryCount())
	}
}

func TestSessionGetAllMemory(t *testing.T) {
	s := NewSession("session1", "line", "U123")

	s.SetMemory("key1", "value1")
	s.SetMemory("key2", 42)

	all := s.GetAllMemory()

	if len(all) != 2 {
		t.Fatalf("Expected 2 memory entries, got %d", len(all))
	}
	if all["key1"] != "value1" {
		t.Errorf("Expected key1='value1', got %v", all["key1"])
	}
	if all["key2"] != 42 {
		t.Errorf("Expected key2=42, got %v", all["key2"])
	}

	// 返り値はコピーであることを確認（元を変えない）
	all["key3"] = "should not affect original"
	_, ok := s.GetMemory("key3")
	if ok {
		t.Error("Modifying GetAllMemory result should not affect original")
	}
}

func TestSessionUpdatedAt(t *testing.T) {
	session := NewSession("session1", "line", "U123")
	initialUpdatedAt := session.UpdatedAt()

	// わずかに待機
	time.Sleep(10 * time.Millisecond)

	// タスク追加で更新時刻が変わる
	jobID := task.NewJobID()
	newTask := task.NewTask(jobID, "Test", "line", "U123")
	session.AddTask(newTask)

	if !session.UpdatedAt().After(initialUpdatedAt) {
		t.Error("UpdatedAt should be updated after AddTask")
	}

	// メモリ設定で更新時刻が変わる
	prevUpdatedAt := session.UpdatedAt()
	time.Sleep(10 * time.Millisecond)
	session.SetMemory("key", "value")

	if !session.UpdatedAt().After(prevUpdatedAt) {
		t.Error("UpdatedAt should be updated after SetMemory")
	}
}
