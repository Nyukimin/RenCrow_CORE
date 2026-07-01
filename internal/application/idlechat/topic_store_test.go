package idlechat

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTopicStoreLoadsLongSummaryLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idlechat_topics.jsonl")
	store, err := NewTopicStore(path)
	if err != nil {
		t.Fatalf("NewTopicStore() error = %v", err)
	}
	longSummary := strings.Repeat("長い要約", 30000)
	want := SessionSummary{
		SessionID: "idle-long-line",
		Title:     "長い要約の話題まとめ",
		Topic:     "長い要約",
		Summary:   longSummary,
		StartedAt: time.Now().Format(time.RFC3339),
		EndedAt:   time.Now().Format(time.RFC3339),
		Turns:     2,
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
	if got[0].SessionID != want.SessionID || got[0].Summary != longSummary {
		t.Fatalf("loaded summary mismatch: id=%q summary_len=%d", got[0].SessionID, len(got[0].Summary))
	}
}
