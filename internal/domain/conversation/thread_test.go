package conversation

import (
	"testing"
	"time"
)

func TestNewThread(t *testing.T) {
	sessionID := "test-session-001"
	domain := "programming"

	thread := NewThread(sessionID, domain)

	if thread.SessionID != sessionID {
		t.Errorf("Expected session ID %s, got %s", sessionID, thread.SessionID)
	}

	if thread.Domain != domain {
		t.Errorf("Expected domain %s, got %s", domain, thread.Domain)
	}

	if thread.ID == 0 {
		t.Error("Expected non-zero thread ID")
	}

	if thread.Status != ThreadActive {
		t.Errorf("Expected status %s, got %s", ThreadActive, thread.Status)
	}

	if len(thread.Turns) != 0 {
		t.Errorf("Expected empty turns, got %d", len(thread.Turns))
	}

	if thread.EndTime != nil {
		t.Error("Expected nil EndTime for new thread")
	}
}

func TestThreadAddMessage(t *testing.T) {
	thread := NewThread("session-001", "test")

	msg1 := NewMessage(SpeakerUser, "Hello", nil)
	thread.AddMessage(msg1)

	if len(thread.Turns) != 1 {
		t.Errorf("Expected 1 turn, got %d", len(thread.Turns))
	}

	msg2 := NewMessage(SpeakerMio, "Hi there!", nil)
	thread.AddMessage(msg2)

	if len(thread.Turns) != 2 {
		t.Errorf("Expected 2 turns, got %d", len(thread.Turns))
	}

	if thread.Turns[0].Msg != "Hello" {
		t.Errorf("Expected first message 'Hello', got '%s'", thread.Turns[0].Msg)
	}

	if thread.Turns[1].Msg != "Hi there!" {
		t.Errorf("Expected second message 'Hi there!', got '%s'", thread.Turns[1].Msg)
	}
}

func TestThreadAddMessageMaxLimit(t *testing.T) {
	thread := NewThread("session-001", "test")

	// 15件のメッセージを追加（上限12件）
	for i := 0; i < 15; i++ {
		msg := NewMessage(SpeakerUser, "Message", nil)
		thread.AddMessage(msg)
	}

	// 最新12件のみ保持
	if len(thread.Turns) != 12 {
		t.Errorf("Expected 12 turns (max limit), got %d", len(thread.Turns))
	}
}

func TestThreadClose(t *testing.T) {
	thread := NewThread("session-001", "test")

	if thread.Status != ThreadActive {
		t.Errorf("Expected status %s, got %s", ThreadActive, thread.Status)
	}

	if thread.EndTime != nil {
		t.Error("Expected nil EndTime for active thread")
	}

	before := time.Now()
	thread.Close()
	after := time.Now()

	if thread.Status != ThreadClosed {
		t.Errorf("Expected status %s, got %s", ThreadClosed, thread.Status)
	}

	if thread.EndTime == nil {
		t.Fatal("Expected non-nil EndTime after close")
	}

	if thread.EndTime.Before(before) || thread.EndTime.After(after) {
		t.Error("EndTime should be between before and after")
	}
}

func TestThreadStatusConstants(t *testing.T) {
	statuses := []ThreadStatus{
		ThreadActive,
		ThreadClosed,
		ThreadArchived,
	}

	expected := []string{
		"active",
		"closed",
		"archived",
	}

	for i, status := range statuses {
		if string(status) != expected[i] {
			t.Errorf("Expected status %s, got %s", expected[i], status)
		}
	}
}

func TestThread_LastMessageTime_Empty(t *testing.T) {
	thread := NewThread("session-001", "test")
	// No messages — should return StartTime
	if thread.LastMessageTime() != thread.StartTime {
		t.Error("LastMessageTime with no messages should return StartTime")
	}
}

func TestThread_LastMessageTime_WithMessages(t *testing.T) {
	thread := NewThread("session-001", "test")
	msg1 := NewMessage(SpeakerUser, "first", nil)
	thread.AddMessage(msg1)
	msg2 := NewMessage(SpeakerMio, "second", nil)
	thread.AddMessage(msg2)
	// Should return last message timestamp
	if thread.LastMessageTime() != msg2.Timestamp {
		t.Error("LastMessageTime should return last message's timestamp")
	}
}

func TestThread_RecentMessagesText_Empty(t *testing.T) {
	thread := NewThread("session-001", "test")
	text := thread.RecentMessagesText(5)
	if text != "" {
		t.Errorf("empty thread should return empty string, got %q", text)
	}
}

func TestThread_RecentMessagesText_LessThanN(t *testing.T) {
	thread := NewThread("session-001", "test")
	thread.AddMessage(NewMessage(SpeakerUser, "hello", nil))
	thread.AddMessage(NewMessage(SpeakerMio, "hi", nil))
	text := thread.RecentMessagesText(5)
	if text != "hello hi" {
		t.Errorf("expected 'hello hi', got %q", text)
	}
}

func TestThread_RecentMessagesText_ExactN(t *testing.T) {
	thread := NewThread("session-001", "test")
	thread.AddMessage(NewMessage(SpeakerUser, "a", nil))
	thread.AddMessage(NewMessage(SpeakerMio, "b", nil))
	text := thread.RecentMessagesText(2)
	if text != "a b" {
		t.Errorf("expected 'a b', got %q", text)
	}
}

func TestThread_RecentMessagesText_MoreThanN(t *testing.T) {
	thread := NewThread("session-001", "test")
	thread.AddMessage(NewMessage(SpeakerUser, "old", nil))
	thread.AddMessage(NewMessage(SpeakerMio, "mid", nil))
	thread.AddMessage(NewMessage(SpeakerUser, "new", nil))
	text := thread.RecentMessagesText(2)
	if text != "mid new" {
		t.Errorf("expected 'mid new', got %q", text)
	}
}

func TestGenerateThreadID(t *testing.T) {
	id1 := generateThreadID()
	time.Sleep(1 * time.Millisecond)
	id2 := generateThreadID()

	if id1 == id2 {
		t.Error("Expected unique thread IDs")
	}

	if id1 >= id2 {
		t.Error("Expected increasing thread IDs")
	}
}
