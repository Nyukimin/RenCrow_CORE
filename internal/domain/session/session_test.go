package session

import (
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newCanonicalSessionForTest(t *testing.T) *Session {
	t.Helper()
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	value, err := NewCanonicalSession(modulecore.NewSessionID(), "2026-03-01", address, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newSessionTurnInputForTest(t *testing.T, sess *Session, message string) conversation.TurnInput {
	t.Helper()
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), message, sess.ChannelAddress())
	if err != nil {
		t.Fatal(err)
	}
	return input.WithSessionID(sess.ID())
}

func TestNewCanonicalSession(t *testing.T) {
	session := newCanonicalSessionForTest(t)
	expectedAddress, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}

	if err := modulecore.SessionID(session.ID()).Validate(); err != nil {
		t.Fatalf("SessionID: %v", err)
	}

	if session.ChannelAddress() != expectedAddress {
		t.Errorf("ChannelAddress = %#v", session.ChannelAddress())
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

func TestSessionAddTurnInput(t *testing.T) {
	session := newCanonicalSessionForTest(t)
	input := newSessionTurnInputForTest(t, session, "Hello")

	session.AddTurnInput(input)

	if session.HistoryCount() != 1 {
		t.Errorf("Expected 1 task in history, got %d", session.HistoryCount())
	}

	history := session.GetHistory()
	if len(history) != 1 {
		t.Fatalf("Expected 1 task in history slice, got %d", len(history))
	}

	if history[0].MessageText() != "Hello" {
		t.Errorf("Expected input message 'Hello', got '%s'", history[0].MessageText())
	}
}

func TestSessionGetRecentHistory(t *testing.T) {
	session := newCanonicalSessionForTest(t)

	// 5つの入力を追加
	for i := 1; i <= 5; i++ {
		session.AddTurnInput(newSessionTurnInputForTest(t, session, string(rune('A'+i-1))))
	}

	// 最近3件取得
	recent := session.GetRecentHistory(3)
	if len(recent) != 3 {
		t.Fatalf("Expected 3 recent tasks, got %d", len(recent))
	}

	// 最新の3件（C, D, E）が取得される
	if recent[0].MessageText() != "C" {
		t.Errorf("Expected first recent input 'C', got '%s'", recent[0].MessageText())
	}

	if recent[2].MessageText() != "E" {
		t.Errorf("Expected last recent input 'E', got '%s'", recent[2].MessageText())
	}

	// 全件より多い数を指定した場合は全件返る
	allRecent := session.GetRecentHistory(10)
	if len(allRecent) != 5 {
		t.Errorf("Expected 5 inputs when requesting 10, got %d", len(allRecent))
	}
}

func TestSessionMemory(t *testing.T) {
	session := newCanonicalSessionForTest(t)

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
	session := newCanonicalSessionForTest(t)

	session.SetMemory("key1", "value1")
	session.SetMemory("key2", "value2")

	session.ClearMemory()

	_, ok := session.GetMemory("key1")
	if ok {
		t.Error("Memory should be cleared")
	}
}

func TestReconstructCanonicalSession(t *testing.T) {
	createdAt := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	address, err := conversation.NewChannelAddress("line", "U999")
	if err != nil {
		t.Fatal(err)
	}
	id := modulecore.NewSessionID()
	s, err := ReconstructCanonicalSession(id, "2026-03-01", address, nil, nil, createdAt, updatedAt)
	if err != nil {
		t.Fatal(err)
	}

	if s.ID() != string(id) {
		t.Errorf("Expected ID %q, got %q", id, s.ID())
	}
	if s.ChannelAddress() != address {
		t.Errorf("Expected ChannelAddress %#v, got %#v", address, s.ChannelAddress())
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
	s := newCanonicalSessionForTest(t)

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
	session := newCanonicalSessionForTest(t)
	initialUpdatedAt := session.UpdatedAt()

	// わずかに待機
	time.Sleep(10 * time.Millisecond)

	// 入力追加で更新時刻が変わる
	session.AddTurnInput(newSessionTurnInputForTest(t, session, "Test"))

	if !session.UpdatedAt().After(initialUpdatedAt) {
		t.Error("UpdatedAt should be updated after AddTurnInput")
	}

	// メモリ設定で更新時刻が変わる
	prevUpdatedAt := session.UpdatedAt()
	time.Sleep(10 * time.Millisecond)
	session.SetMemory("key", "value")

	if !session.UpdatedAt().After(prevUpdatedAt) {
		t.Error("UpdatedAt should be updated after SetMemory")
	}
}
