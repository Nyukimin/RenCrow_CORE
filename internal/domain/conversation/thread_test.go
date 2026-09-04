package conversation

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/google/uuid"
)

func threadTestSessionID() string {
	return string(modulecore.NewSessionID())
}

func TestNewThread(t *testing.T) {
	sessionID := threadTestSessionID()
	domain := "programming"

	thread, err := NewThread(sessionID, domain, ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}

	if thread.SessionID != sessionID {
		t.Errorf("Expected session ID %s, got %s", sessionID, thread.SessionID)
	}

	if thread.Domain != domain {
		t.Errorf("Expected domain %s, got %s", domain, thread.Domain)
	}

	if thread.ID == "" {
		t.Error("Expected non-empty thread ID")
	}
	if err := thread.ID.Validate(); err != nil {
		t.Fatalf("Expected valid canonical thread ID: %v", err)
	}
	if thread.ThreadSeq != ThreadSeq(1) {
		t.Errorf("Expected thread sequence 1, got %d", thread.ThreadSeq)
	}
	if thread.ThreadKind != ThreadKindUserConversation {
		t.Errorf("Expected thread kind %q, got %q", ThreadKindUserConversation, thread.ThreadKind)
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
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}

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
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}

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
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}

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

func TestThreadAddMessageRejectsInactiveThreadWithoutMutation(t *testing.T) {
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
	thread.AddMessage(NewMessage(SpeakerUser, "before close", nil))
	if err := thread.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	closedTurns := append([]Message(nil), thread.Turns...)
	if err := thread.AddMessage(NewMessage(SpeakerMio, "after close", nil)); err == nil {
		t.Fatal("expected closed thread append to fail")
	}
	if !reflect.DeepEqual(thread.Turns, closedTurns) {
		t.Fatalf("closed thread turns mutated: got=%v want=%v", thread.Turns, closedTurns)
	}

	thread.Status = ThreadArchived
	archivedTurns := append([]Message(nil), thread.Turns...)
	if err := thread.AddMessage(NewMessage(SpeakerUser, "after archive", nil)); err == nil {
		t.Fatal("expected archived thread append to fail")
	}
	if !reflect.DeepEqual(thread.Turns, archivedTurns) {
		t.Fatalf("archived thread turns mutated: got=%v want=%v", thread.Turns, archivedTurns)
	}

	var nilThread *Thread
	if err := nilThread.AddMessage(NewMessage(SpeakerUser, "nil thread", nil)); err == nil {
		t.Fatal("expected nil thread append to fail")
	}
}

func TestThreadCloseRejectsRepeatedAndArchivedWithoutMutation(t *testing.T) {
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
	if err := thread.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	firstEndTime := *thread.EndTime
	if err := thread.Close(); err == nil {
		t.Fatal("expected repeated close to fail")
	}
	if thread.Status != ThreadClosed {
		t.Fatalf("repeated close changed status to %q", thread.Status)
	}
	if thread.EndTime == nil || !thread.EndTime.Equal(firstEndTime) {
		t.Fatalf("repeated close changed end time: got=%v want=%v", thread.EndTime, firstEndTime)
	}

	archived, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
	sentinel := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	archived.Status = ThreadArchived
	archived.EndTime = &sentinel
	if err := archived.Close(); err == nil {
		t.Fatal("expected archived close to fail")
	}
	if archived.Status != ThreadArchived {
		t.Fatalf("archived close changed status to %q", archived.Status)
	}
	if archived.EndTime == nil || !archived.EndTime.Equal(sentinel) {
		t.Fatalf("archived close changed end time: got=%v want=%v", archived.EndTime, sentinel)
	}

	var nilThread *Thread
	if err := nilThread.Close(); err == nil {
		t.Fatal("expected nil thread close to fail")
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
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
	// No messages — should return StartTime
	if thread.LastMessageTime() != thread.StartTime {
		t.Error("LastMessageTime with no messages should return StartTime")
	}
}

func TestThread_LastMessageTime_WithMessages(t *testing.T) {
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
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
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
	text := thread.RecentMessagesText(5)
	if text != "" {
		t.Errorf("empty thread should return empty string, got %q", text)
	}
}

func TestThread_RecentMessagesText_LessThanN(t *testing.T) {
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
	thread.AddMessage(NewMessage(SpeakerUser, "hello", nil))
	thread.AddMessage(NewMessage(SpeakerMio, "hi", nil))
	text := thread.RecentMessagesText(5)
	if text != "hello hi" {
		t.Errorf("expected 'hello hi', got %q", text)
	}
}

func TestThread_RecentMessagesText_ExactN(t *testing.T) {
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
	thread.AddMessage(NewMessage(SpeakerUser, "a", nil))
	thread.AddMessage(NewMessage(SpeakerMio, "b", nil))
	text := thread.RecentMessagesText(2)
	if text != "a b" {
		t.Errorf("expected 'a b', got %q", text)
	}
}

func TestThread_RecentMessagesText_MoreThanN(t *testing.T) {
	thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(1))
	if err != nil {
		t.Fatalf("NewThread failed: %v", err)
	}
	thread.AddMessage(NewMessage(SpeakerUser, "old", nil))
	thread.AddMessage(NewMessage(SpeakerMio, "mid", nil))
	thread.AddMessage(NewMessage(SpeakerUser, "new", nil))
	text := thread.RecentMessagesText(2)
	if text != "mid new" {
		t.Errorf("expected 'mid new', got %q", text)
	}
}

func TestNewThreadRejectsInvalidKindAndSequence(t *testing.T) {
	if thread, err := NewThread(threadTestSessionID(), "test", ThreadKind("invalid"), ThreadSeq(1)); err == nil || thread != nil {
		t.Fatalf("expected invalid thread kind to fail closed, thread=%v err=%v", thread, err)
	}
	if thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(0)); err == nil || thread != nil {
		t.Fatalf("expected zero thread sequence to fail closed, thread=%v err=%v", thread, err)
	}
	if thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(-1)); err == nil || thread != nil {
		t.Fatalf("expected negative thread sequence to fail closed, thread=%v err=%v", thread, err)
	}
}

func TestNewThreadRejectsLegacySessionID(t *testing.T) {
	if thread, err := NewThread("session-001", "test", ThreadKindUserConversation, ThreadSeq(1)); err == nil || thread != nil {
		t.Fatalf("expected legacy session ID to fail closed, thread=%v err=%v", thread, err)
	}
}

func TestNewThreadConcurrentUUIDv7Uniqueness(t *testing.T) {
	const count = 128
	ids := make(chan string, count)
	errs := make(chan error, count)
	var waitGroup sync.WaitGroup
	waitGroup.Add(count)
	for index := 0; index < count; index++ {
		go func(index int) {
			defer waitGroup.Done()
			thread, err := NewThread(threadTestSessionID(), "test", ThreadKindUserConversation, ThreadSeq(index+1))
			if err != nil {
				errs <- err
				return
			}
			ids <- string(thread.ID)
		}(index)
	}
	waitGroup.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent NewThread failed: %v", err)
	}

	seen := make(map[string]struct{}, count)
	for raw := range ids {
		if !strings.HasPrefix(raw, "thr_") {
			t.Fatalf("thread ID %q lacks thr_ prefix", raw)
		}
		parsed, err := uuid.Parse(strings.TrimPrefix(raw, "thr_"))
		if err != nil {
			t.Fatalf("parse generated thread ID %q: %v", raw, err)
		}
		if parsed.Version() != 7 {
			t.Fatalf("thread ID %q uses UUIDv%d, want UUIDv7", raw, parsed.Version())
		}
		if _, exists := seen[raw]; exists {
			t.Fatalf("duplicate generated thread ID %q", raw)
		}
		seen[raw] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("expected %d generated thread IDs, got %d", count, len(seen))
	}
}
