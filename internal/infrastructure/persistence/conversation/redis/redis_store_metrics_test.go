package redisstore

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newValidRedisThread() *conversation.Thread {
	return &conversation.Thread{
		ID:         modulecore.NewThreadID(),
		ThreadSeq:  1,
		ThreadKind: modulecore.ThreadKindUserConversation,
		SessionID:  string(modulecore.NewSessionID()),
		Domain:     "conversation",
	}
}

func TestThreadKeyUsesCanonicalThreadID(t *testing.T) {
	threadID := modulecore.ThreadID("thr_0190c4a5-7b2d-7e31-a8e6-1f3e5c9d0247")

	if got, want := threadKey(threadID), "thread:"+string(threadID); got != want {
		t.Fatalf("thread key = %q, want %q", got, want)
	}
}

func TestSaveThreadRejectsInvalidValuesBeforeRedisAccess(t *testing.T) {
	tests := []struct {
		name   string
		thread *conversation.Thread
	}{
		{name: "nil", thread: nil},
		{name: "invalid id", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.ID = ""
			return thread
		}()},
		{name: "nonpositive sequence", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.ThreadSeq = 0
			return thread
		}()},
		{name: "invalid kind", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.ThreadKind = "unknown"
			return thread
		}()},
		{name: "empty session", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.SessionID = ""
			return thread
		}()},
		{name: "legacy session", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.SessionID = "legacy-session"
			return thread
		}()},
		{name: "empty domain", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.Domain = ""
			return thread
		}()},
	}

	store := &RedisStore{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.SaveThread(context.Background(), test.thread); err == nil {
				t.Fatal("SaveThread accepted invalid thread")
			}
		})
	}
}

func TestThreadOperationsRejectInvalidIDBeforeRedisAccess(t *testing.T) {
	store := &RedisStore{}
	invalidID := modulecore.ThreadID("legacy-thread")

	if _, err := store.GetThread(context.Background(), invalidID); err == nil {
		t.Fatal("GetThread accepted an invalid thread ID")
	}
	if err := store.DeleteThread(context.Background(), invalidID); err == nil {
		t.Fatal("DeleteThread accepted an invalid thread ID")
	}
}

func TestValidateStoredThreadRejectsMismatchedAndInvalidData(t *testing.T) {
	requestedID := modulecore.NewThreadID()
	valid := newValidRedisThread()
	valid.ID = requestedID
	if err := validateStoredThread(requestedID, valid); err != nil {
		t.Fatalf("validateStoredThread(valid) error = %v", err)
	}

	tests := []struct {
		name   string
		thread *conversation.Thread
	}{
		{name: "mismatched id", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			return thread
		}()},
		{name: "invalid id", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.ID = "thr_invalid"
			return thread
		}()},
		{name: "nonpositive sequence", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.ID = requestedID
			thread.ThreadSeq = 0
			return thread
		}()},
		{name: "invalid kind", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.ID = requestedID
			thread.ThreadKind = "unknown"
			return thread
		}()},
		{name: "empty session", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.ID = requestedID
			thread.SessionID = ""
			return thread
		}()},
		{name: "empty domain", thread: func() *conversation.Thread {
			thread := newValidRedisThread()
			thread.ID = requestedID
			thread.Domain = ""
			return thread
		}()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateStoredThread(requestedID, test.thread); err == nil {
				t.Fatal("validateStoredThread accepted invalid data")
			}
		})
	}
}

func TestSessionLastThreadIDValidation(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	valid := modulecore.NewThreadID()
	for _, test := range []struct {
		name string
		id   modulecore.ThreadID
		seq  modulecore.ThreadSeq
		kind modulecore.ThreadKind
		want bool
	}{
		{name: "empty is allowed", want: true},
		{name: "canonical is allowed", id: valid, seq: 1, kind: modulecore.ThreadKindUserConversation, want: true},
		{name: "partial id is rejected", id: valid, want: false},
		{name: "partial sequence is rejected", id: valid, seq: 1, want: false},
		{name: "partial kind is rejected", id: valid, kind: modulecore.ThreadKindUserConversation, want: false},
		{name: "invalid id is rejected", id: "legacy-thread", seq: 1, kind: modulecore.ThreadKindUserConversation, want: false},
		{name: "invalid sequence is rejected", id: valid, seq: 0, kind: modulecore.ThreadKindUserConversation, want: false},
		{name: "invalid kind is rejected", id: valid, seq: 1, kind: "legacy", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateSessionConversation(&conversation.SessionConversation{ID: sessionID, LastThreadID: test.id, LastThreadSeq: test.seq, LastThreadKind: test.kind})
			if (err == nil) != test.want {
				t.Fatalf("validateSessionConversation(%q) error = %v, want allowed=%t", test.id, err, test.want)
			}
		})
	}
}

func TestSessionConversationIdentityValidation(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	validSummary := conversation.ThreadSummary{
		ThreadID:   modulecore.NewThreadID(),
		ThreadSeq:  1,
		ThreadKind: modulecore.ThreadKindUserConversation,
		SessionID:  sessionID,
	}
	validSession := func() *conversation.SessionConversation {
		return &conversation.SessionConversation{
			ID:      sessionID,
			History: []conversation.ThreadSummary{validSummary},
		}
	}

	if err := validateSessionConversation(validSession()); err != nil {
		t.Fatalf("validateSessionConversation(valid) error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*conversation.SessionConversation)
	}{
		{name: "legacy session ID", mutate: func(sess *conversation.SessionConversation) { sess.ID = "legacy-session" }},
		{name: "legacy history thread ID", mutate: func(sess *conversation.SessionConversation) { sess.History[0].ThreadID = "10" }},
		{name: "zero history sequence", mutate: func(sess *conversation.SessionConversation) { sess.History[0].ThreadSeq = 0 }},
		{name: "invalid history kind", mutate: func(sess *conversation.SessionConversation) { sess.History[0].ThreadKind = "legacy" }},
		{name: "history parent mismatch", mutate: func(sess *conversation.SessionConversation) {
			sess.History[0].SessionID = string(modulecore.NewSessionID())
		}},
		{name: "duplicate history thread ID", mutate: func(sess *conversation.SessionConversation) {
			duplicate := validSummary
			duplicate.ThreadSeq = 2
			sess.History = append(sess.History, duplicate)
		}},
		{name: "duplicate history sequence", mutate: func(sess *conversation.SessionConversation) {
			duplicate := validSummary
			duplicate.ThreadID = modulecore.NewThreadID()
			sess.History = append(sess.History, duplicate)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sess := validSession()
			test.mutate(sess)
			if err := validateSessionConversation(sess); err == nil {
				t.Fatal("validateSessionConversation accepted invalid identity")
			}
		})
	}
}

func TestValidateStoredSessionRejectsMismatchedSessionID(t *testing.T) {
	requestedID := string(modulecore.NewSessionID())
	if err := validateStoredSession(requestedID, &conversation.SessionConversation{ID: requestedID}); err != nil {
		t.Fatalf("validateStoredSession(valid) error = %v", err)
	}
	if err := validateStoredSession(requestedID, &conversation.SessionConversation{ID: string(modulecore.NewSessionID())}); err == nil {
		t.Fatal("validateStoredSession accepted a mismatched session ID")
	}
}

func TestSaveSessionRejectsInvalidLastThreadIDBeforeRedisAccess(t *testing.T) {
	store := &RedisStore{}
	session := &conversation.SessionConversation{ID: string(modulecore.NewSessionID()), LastThreadID: "legacy-thread"}

	if err := store.SaveSession(context.Background(), session); err == nil {
		t.Fatal("SaveSession accepted an invalid last thread ID")
	}
}

func TestRedisMetrics_Initialization(t *testing.T) {
	metrics := &RedisMetrics{}

	if metrics.SessionHits != 0 {
		t.Errorf("Expected SessionHits to be 0, got %d", metrics.SessionHits)
	}
	if metrics.SessionMisses != 0 {
		t.Errorf("Expected SessionMisses to be 0, got %d", metrics.SessionMisses)
	}
	if metrics.ThreadHits != 0 {
		t.Errorf("Expected ThreadHits to be 0, got %d", metrics.ThreadHits)
	}
	if metrics.ThreadMisses != 0 {
		t.Errorf("Expected ThreadMisses to be 0, got %d", metrics.ThreadMisses)
	}
}

func TestRedisMetrics_HitRate_Zero(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{},
	}

	sessionRate, threadRate := store.GetCacheHitRate()

	if sessionRate != 0 {
		t.Errorf("Expected 0%% hit rate with no data, got %.2f%%", sessionRate)
	}
	if threadRate != 0 {
		t.Errorf("Expected 0%% hit rate with no data, got %.2f%%", threadRate)
	}
}

func TestRedisMetrics_HitRate_AllHits(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{
			SessionHits:   10,
			SessionMisses: 0,
			ThreadHits:    5,
			ThreadMisses:  0,
		},
	}

	sessionRate, threadRate := store.GetCacheHitRate()

	if sessionRate != 100.0 {
		t.Errorf("Expected 100%% session hit rate, got %.2f%%", sessionRate)
	}
	if threadRate != 100.0 {
		t.Errorf("Expected 100%% thread hit rate, got %.2f%%", threadRate)
	}
}

func TestRedisMetrics_HitRate_AllMisses(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{
			SessionHits:   0,
			SessionMisses: 10,
			ThreadHits:    0,
			ThreadMisses:  5,
		},
	}

	sessionRate, threadRate := store.GetCacheHitRate()

	if sessionRate != 0.0 {
		t.Errorf("Expected 0%% session hit rate, got %.2f%%", sessionRate)
	}
	if threadRate != 0.0 {
		t.Errorf("Expected 0%% thread hit rate, got %.2f%%", threadRate)
	}
}

func TestRedisMetrics_HitRate_Mixed(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{
			SessionHits:   8,
			SessionMisses: 2,
			ThreadHits:    6,
			ThreadMisses:  4,
		},
	}

	sessionRate, threadRate := store.GetCacheHitRate()

	expectedSessionRate := 80.0 // 8/(8+2) * 100
	if sessionRate != expectedSessionRate {
		t.Errorf("Expected %.2f%% session hit rate, got %.2f%%", expectedSessionRate, sessionRate)
	}

	expectedThreadRate := 60.0 // 6/(6+4) * 100
	if threadRate != expectedThreadRate {
		t.Errorf("Expected %.2f%% thread hit rate, got %.2f%%", expectedThreadRate, threadRate)
	}
}

func TestRedisMetrics_GetMetrics(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{
			SessionHits:   5,
			SessionMisses: 3,
			ThreadHits:    7,
			ThreadMisses:  2,
		},
	}

	metrics := store.GetMetrics()

	if metrics.SessionHits != 5 {
		t.Errorf("Expected 5 session hits, got %d", metrics.SessionHits)
	}
	if metrics.SessionMisses != 3 {
		t.Errorf("Expected 3 session misses, got %d", metrics.SessionMisses)
	}
	if metrics.ThreadHits != 7 {
		t.Errorf("Expected 7 thread hits, got %d", metrics.ThreadHits)
	}
	if metrics.ThreadMisses != 2 {
		t.Errorf("Expected 2 thread misses, got %d", metrics.ThreadMisses)
	}
}
