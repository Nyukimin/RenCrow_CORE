package idlechat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func canonicalIdleChatTestSessionID(seed string) string {
	id, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "idlechat_test", "session_id", seed)
	if err != nil {
		panic(err)
	}
	return id
}

func newTopicStoreTestSummary(sessionID string, seq modulecore.ThreadSeq) SessionSummary {
	return SessionSummary{
		SessionID:  sessionID,
		ThreadID:   modulecore.NewThreadID(),
		ThreadSeq:  seq,
		ThreadKind: modulecore.ThreadKindIdleChat,
	}
}

func writeTopicStoreTestRecords(t *testing.T, path string, records ...SessionSummary) {
	t.Helper()
	values := make([]any, 0, len(records))
	for _, record := range records {
		values = append(values, record)
	}
	writeTopicStoreTestValues(t, path, values...)
}

func writeTopicStoreTestValues(t *testing.T, path string, values ...any) {
	t.Helper()
	var data []byte
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		data = append(data, raw...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}

func TestTopicStoreLoadsLongSummaryLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
	store, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() error = %v", err)
	}
	longSummary := strings.Repeat("長い要約", 30000)
	want := SessionSummary{
		SessionID:  canonicalIdleChatTestSessionID("idle-long-line"),
		ThreadID:   modulecore.NewThreadID(),
		ThreadSeq:  1,
		ThreadKind: modulecore.ThreadKindIdleChat,
		Title:      "長い要約の話題まとめ",
		Topic:      "長い要約",
		Summary:    longSummary,
		StartedAt:  time.Now().Format(time.RFC3339),
		EndedAt:    time.Now().Format(time.RFC3339),
		Turns:      2,
	}
	if err := store.Append(want); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	reloaded, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() reload error = %v", err)
	}
	got := reloaded.GetRecent(1)
	if len(got) != 1 {
		t.Fatalf("GetRecent() len = %d, want 1", len(got))
	}
	if got[0].SessionID != want.SessionID || got[0].Summary != longSummary || got[0].ThreadID != want.ThreadID || got[0].ThreadSeq != want.ThreadSeq || got[0].ThreadKind != want.ThreadKind {
		t.Fatalf("loaded summary mismatch: id=%q summary_len=%d", got[0].SessionID, len(got[0].Summary))
	}
}

func TestTopicStoreRejectsMalformedAndLegacyRecords(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "malformed json", data: `{"session_id":"idle-bad"`},
		{name: "missing tuple", data: `{"session_id":"idle-legacy"}`},
		{name: "numeric legacy thread id", data: `{"session_id":"idle-legacy","thread_id":42,"thread_seq":1,"thread_kind":"idlechat"}`},
		{name: "zero sequence", data: `{"session_id":"idle-bad","thread_id":"thr_invalid","thread_seq":0,"thread_kind":"idlechat"}`},
		{name: "wrong kind", data: `{"session_id":"idle-bad","thread_id":"thr_invalid","thread_seq":1,"thread_kind":"user_conversation"}`},
		{name: "blank session", data: `{"session_id":"   ","thread_id":"thr_invalid","thread_seq":1,"thread_kind":"idlechat"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
			if err := os.WriteFile(path, []byte(test.data+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewTopicStore(path)
			if err == nil {
				t.Fatal("NewTopicStore() unexpectedly accepted invalid record")
			}
			if !strings.Contains(err.Error(), "line 1") {
				t.Fatalf("error = %v, want line number", err)
			}
		})
	}
}

func TestTopicStoreRejectsNoncanonicalSessionID(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
		writeTopicStoreTestRecords(t, path, newTopicStoreTestSummary("idle-legacy", 1))
		if _, err := NewTopicStore(path); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Fatalf("NewTopicStore() error = %v, want line 1 noncanonical session rejection", err)
		}
	})

	t.Run("open", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
		store, err := NewTopicStore(path)
		if err != nil {
			t.Fatalf("NewTopicStore() error = %v", err)
		}
		if _, err := store.OpenThread("idle-legacy"); err == nil {
			t.Fatal("OpenThread() accepted noncanonical session ID")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("noncanonical OpenThread() changed store, stat error = %v", err)
		}
	})

	t.Run("append", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
		store, err := NewTopicStore(path)
		if err != nil {
			t.Fatalf("NewTopicStore() error = %v", err)
		}
		if err := store.Append(newTopicStoreTestSummary("idle-legacy", 1)); err == nil {
			t.Fatal("Append() accepted noncanonical session ID")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("noncanonical Append() changed store, stat error = %v", err)
		}
	})
}

func TestTopicStoreRejectsDuplicateTupleAndThreadIdentity(t *testing.T) {
	t.Run("duplicate tuple", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
		first := newTopicStoreTestSummary(canonicalIdleChatTestSessionID("idle-duplicate"), 1)
		second := newTopicStoreTestSummary(canonicalIdleChatTestSessionID("idle-duplicate"), 1)
		writeTopicStoreTestRecords(t, path, first, second)
		_, err := NewTopicStore(path)
		if err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("NewTopicStore() error = %v, want line 2 duplicate rejection", err)
		}
	})

	t.Run("duplicate thread identity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
		first := newTopicStoreTestSummary(canonicalIdleChatTestSessionID("idle-duplicate-id"), 1)
		second := first
		second.ThreadSeq = 2
		writeTopicStoreTestRecords(t, path, first, second)
		_, err := NewTopicStore(path)
		if err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("NewTopicStore() error = %v, want line 2 identity rejection", err)
		}
	})
}

func TestTopicStoreReloadPreservesTupleAndAllocatesAfterMaximum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
	first := newTopicStoreTestSummary(canonicalIdleChatTestSessionID("idle-reload"), 1)
	third := newTopicStoreTestSummary(canonicalIdleChatTestSessionID("idle-reload"), 3)
	writeTopicStoreTestRecords(t, path, first, third)

	store, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() error = %v", err)
	}
	recent := store.GetRecent(2)
	if len(recent) != 2 {
		t.Fatalf("GetRecent() len = %d, want 2", len(recent))
	}
	if recent[0].ThreadID != third.ThreadID || recent[0].ThreadSeq != third.ThreadSeq || recent[0].ThreadKind != third.ThreadKind || recent[1].ThreadID != first.ThreadID || recent[1].ThreadSeq != first.ThreadSeq || recent[1].ThreadKind != first.ThreadKind {
		t.Fatalf("reloaded tuples = %+v, want exact persisted tuples", recent)
	}
	opened, err := store.OpenThread(canonicalIdleChatTestSessionID("idle-reload"))
	if err != nil {
		t.Fatalf("OpenThread() error = %v", err)
	}
	if opened.ThreadSeq != 4 {
		t.Fatalf("OpenThread() sequence = %d, want 4", opened.ThreadSeq)
	}
	allocated := SessionSummary{
		SessionID:  opened.SessionID,
		ThreadID:   opened.ID,
		ThreadSeq:  opened.ThreadSeq,
		ThreadKind: opened.ThreadKind,
	}
	if err := store.Append(allocated); err != nil {
		t.Fatalf("Append() after allocation error = %v", err)
	}
	reloaded, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() after append error = %v", err)
	}
	recent = reloaded.GetRecent(1)
	if len(recent) != 1 || recent[0].ThreadID != allocated.ThreadID || recent[0].ThreadSeq != allocated.ThreadSeq || recent[0].ThreadKind != allocated.ThreadKind {
		t.Fatalf("reloaded allocated tuple = %+v, want %+v", recent, allocated)
	}
}

func TestTopicStoreAllocatesDistinctSequencesConcurrently(t *testing.T) {
	store, err := NewTopicStore(filepath.Join(t.TempDir(), "idlechat_topics.jsonl"))
	if err != nil {
		t.Fatalf("NewTopicStore() error = %v", err)
	}
	const workers = 64
	sequences := make(chan modulecore.ThreadSeq, workers)
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			thread, err := store.OpenThread(canonicalIdleChatTestSessionID("idle-concurrent"))
			if err != nil {
				errors <- err
				return
			}
			sequences <- thread.ThreadSeq
		}()
	}
	wg.Wait()
	close(sequences)
	close(errors)
	for err := range errors {
		t.Fatalf("OpenThread() error = %v", err)
	}
	got := make([]modulecore.ThreadSeq, 0, workers)
	for seq := range sequences {
		got = append(got, seq)
	}
	if len(got) != workers {
		t.Fatalf("allocated %d sequences, want %d", len(got), workers)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i, seq := range got {
		want := modulecore.ThreadSeq(i + 1)
		if seq != want {
			t.Fatalf("sorted sequence[%d] = %d, want %d", i, seq, want)
		}
	}
}

func TestTopicStoreRejectsThreadSequenceOverflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
	writeTopicStoreTestRecords(t, path, newTopicStoreTestSummary(canonicalIdleChatTestSessionID("idle-overflow"), maxThreadSeq))
	store, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() error = %v", err)
	}
	if _, err := store.OpenThread(canonicalIdleChatTestSessionID("idle-overflow")); err == nil {
		t.Fatal("OpenThread() accepted sequence overflow")
	}
}

func TestTopicStoreOpenSurvivesRestartBeforeSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
	store, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() error = %v", err)
	}
	first, err := store.OpenThread(canonicalIdleChatTestSessionID("idle-crash"))
	if err != nil {
		t.Fatalf("OpenThread() error = %v", err)
	}
	reopened, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() after open error = %v", err)
	}
	second, err := reopened.OpenThread(canonicalIdleChatTestSessionID("idle-crash"))
	if err != nil {
		t.Fatalf("OpenThread() after restart error = %v", err)
	}
	if first.ID.Validate() != nil || second.ID.Validate() != nil {
		t.Fatalf("thread ids are not canonical: first=%q second=%q", first.ID, second.ID)
	}
	if first.ThreadSeq != 1 || second.ThreadSeq != 2 {
		t.Fatalf("restart sequences = %d/%d, want 1/2", first.ThreadSeq, second.ThreadSeq)
	}
	if first.ID == second.ID || first.ThreadKind != modulecore.ThreadKindIdleChat || second.ThreadKind != modulecore.ThreadKindIdleChat {
		t.Fatalf("restart thread identities = first:%+v second:%+v", first, second)
	}
	if got := reopened.GetRecent(10); len(got) != 0 {
		t.Fatalf("thread_open records entered recent summaries: %d", len(got))
	}
}

func TestTopicStoreOpenAndSummaryReloadAsOneRecentSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
	store, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() error = %v", err)
	}
	thread, err := store.OpenThread(canonicalIdleChatTestSessionID("idle-finalized"))
	if err != nil {
		t.Fatalf("OpenThread() error = %v", err)
	}
	want := SessionSummary{SessionID: thread.SessionID, ThreadID: thread.ID, ThreadSeq: thread.ThreadSeq, ThreadKind: thread.ThreadKind, Summary: "finalized"}
	if err := store.Append(want); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	reloaded, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() reload error = %v", err)
	}
	got := reloaded.GetRecent(10)
	if len(got) != 1 || got[0].SessionID != want.SessionID || got[0].ThreadID != want.ThreadID || got[0].ThreadSeq != want.ThreadSeq || got[0].ThreadKind != want.ThreadKind || got[0].Summary != want.Summary {
		t.Fatalf("reloaded summaries = %+v, want one exact summary %+v", got, want)
	}
}

func TestTopicStoreRejectsDuplicateLifecycleRecords(t *testing.T) {
	threadID := modulecore.NewThreadID()
	open := topicStoreThreadOpen{
		RecordType: topicStoreThreadOpenRecordType,
		SessionID:  canonicalIdleChatTestSessionID("idle-duplicate-open"),
		ThreadID:   threadID,
		ThreadSeq:  1,
		ThreadKind: modulecore.ThreadKindIdleChat,
	}
	path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
	writeTopicStoreTestValues(t, path, open, open)
	if _, err := NewTopicStore(path); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("duplicate open load error = %v, want line 2", err)
	}

	store, err := NewTopicStore(filepath.Join(t.TempDir(), "idlechat_topics.jsonl"))
	if err != nil {
		t.Fatalf("NewTopicStore() second case error = %v", err)
	}
	thread, err := store.OpenThread(canonicalIdleChatTestSessionID("idle-duplicate-summary"))
	if err != nil {
		t.Fatalf("OpenThread() second case error = %v", err)
	}
	summary := SessionSummary{SessionID: thread.SessionID, ThreadID: thread.ID, ThreadSeq: thread.ThreadSeq, ThreadKind: thread.ThreadKind}
	if err := store.Append(summary); err != nil {
		t.Fatalf("Append() first summary error = %v", err)
	}
	if err := store.Append(summary); err == nil {
		t.Fatal("Append() accepted second summary for one lifecycle")
	}
	wrong := summary
	wrong.ThreadID = modulecore.NewThreadID()
	if err := store.Append(wrong); err == nil {
		t.Fatal("Append() accepted tuple/thread_id mismatch")
	}
}

func TestTopicStoreFailedOpenDoesNotAdvanceAllocation(t *testing.T) {
	validPath := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
	store, err := NewTopicStore(validPath)
	if err != nil {
		t.Fatalf("NewTopicStore() error = %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "missing-parent", "idlechat_topics.jsonl")
	store.path = badPath
	if _, err := store.OpenThread(canonicalIdleChatTestSessionID("idle-write-failure")); err == nil {
		t.Fatal("OpenThread() unexpectedly succeeded with missing parent")
	}
	store.path = validPath
	thread, err := store.OpenThread(canonicalIdleChatTestSessionID("idle-write-failure"))
	if err != nil {
		t.Fatalf("OpenThread() after failed write error = %v", err)
	}
	if thread.ThreadSeq != 1 {
		t.Fatalf("failed write advanced sequence to %d, want 1", thread.ThreadSeq)
	}
}
