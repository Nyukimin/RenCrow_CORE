package agent

import (
	"testing"
)

func TestLightMemory_RecordAndRetrieve(t *testing.T) {
	memory := NewLightMemory(3)
	sessionID := "test-session"

	// 2 ターン記録
	memory.Record(sessionID, "user1", "response1")
	memory.Record(sessionID, "user2", "response2")

	// 取得
	messages := memory.RecentMessages(sessionID)

	// 検証
	if len(messages) != 4 {
		t.Errorf("Expected 4 messages, got %d", len(messages))
	}

	expected := []struct {
		role    string
		content string
	}{
		{"user", "user1"},
		{"assistant", "response1"},
		{"user", "user2"},
		{"assistant", "response2"},
	}

	for i, exp := range expected {
		if messages[i].Role != exp.role {
			t.Errorf("Message[%d].Role = %s, want %s", i, messages[i].Role, exp.role)
		}
		if messages[i].Content != exp.content {
			t.Errorf("Message[%d].Content = %s, want %s", i, messages[i].Content, exp.content)
		}
	}
}

func TestLightMemory_MaxTurns(t *testing.T) {
	memory := NewLightMemory(2)
	sessionID := "test-session"

	// 3 ターン記録（maxTurns=2 を超える）
	memory.Record(sessionID, "user1", "response1")
	memory.Record(sessionID, "user2", "response2")
	memory.Record(sessionID, "user3", "response3")

	messages := memory.RecentMessages(sessionID)

	// 最新 2 ターンのみ保持されている
	if len(messages) != 4 { // 2 ターン × 2 メッセージ
		t.Errorf("Expected 4 messages (2 turns), got %d", len(messages))
	}

	// 古い user1/response1 は削除されている
	if messages[0].Content == "user1" {
		t.Error("Old turn should be deleted")
	}
	if messages[0].Content != "user2" {
		t.Errorf("First message should be user2, got %s", messages[0].Content)
	}
}

func TestLightMemory_EmptySession(t *testing.T) {
	memory := NewLightMemory(3)
	sessionID := "non-existent"

	messages := memory.RecentMessages(sessionID)

	if messages != nil {
		t.Errorf("Expected nil for non-existent session, got %v", messages)
	}
}

func TestLightMemory_Clear(t *testing.T) {
	memory := NewLightMemory(3)
	sessionID := "test-session"

	// 記録
	memory.Record(sessionID, "user1", "response1")

	// 削除前確認
	if messages := memory.RecentMessages(sessionID); len(messages) == 0 {
		t.Error("Expected messages before clear")
	}

	// 削除
	memory.Clear(sessionID)

	// 削除後確認
	if messages := memory.RecentMessages(sessionID); messages != nil {
		t.Errorf("Expected nil after clear, got %v", messages)
	}
}

func TestLightMemory_ClearAll(t *testing.T) {
	memory := NewLightMemory(3)

	// 複数セッション記録
	memory.Record("session1", "user1", "response1")
	memory.Record("session2", "user2", "response2")
	memory.Record("session3", "user3", "response3")

	// 削除前確認
	if messages := memory.RecentMessages("session1"); len(messages) == 0 {
		t.Error("Expected messages in session1")
	}
	if messages := memory.RecentMessages("session2"); len(messages) == 0 {
		t.Error("Expected messages in session2")
	}

	// 全削除
	memory.ClearAll()

	// 削除後確認
	if messages := memory.RecentMessages("session1"); messages != nil {
		t.Error("Expected nil for session1 after ClearAll")
	}
	if messages := memory.RecentMessages("session2"); messages != nil {
		t.Error("Expected nil for session2 after ClearAll")
	}
	if messages := memory.RecentMessages("session3"); messages != nil {
		t.Error("Expected nil for session3 after ClearAll")
	}
}

func TestLightMemory_MultipleSessions(t *testing.T) {
	memory := NewLightMemory(3)

	// 異なるセッションに記録
	memory.Record("session1", "user1-msg1", "response1-msg1")
	memory.Record("session2", "user2-msg1", "response2-msg1")
	memory.Record("session1", "user1-msg2", "response1-msg2")

	// session1 は 2 ターン
	messages1 := memory.RecentMessages("session1")
	if len(messages1) != 4 {
		t.Errorf("session1: expected 4 messages, got %d", len(messages1))
	}

	// session2 は 1 ターン
	messages2 := memory.RecentMessages("session2")
	if len(messages2) != 2 {
		t.Errorf("session2: expected 2 messages, got %d", len(messages2))
	}

	// session1 の内容確認
	if messages1[0].Content != "user1-msg1" {
		t.Errorf("session1[0]: expected 'user1-msg1', got '%s'", messages1[0].Content)
	}
	if messages1[2].Content != "user1-msg2" {
		t.Errorf("session1[2]: expected 'user1-msg2', got '%s'", messages1[2].Content)
	}

	// session2 の内容確認
	if messages2[0].Content != "user2-msg1" {
		t.Errorf("session2[0]: expected 'user2-msg1', got '%s'", messages2[0].Content)
	}
}
