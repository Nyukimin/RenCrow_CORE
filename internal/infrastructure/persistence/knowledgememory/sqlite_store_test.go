package knowledgememory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/knowledgememory"
)

func TestSQLiteStoreSavesKnowledgeMemoryRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge_memory.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SavePersonalArchiveEntry(context.Background(), domainkm.PersonalArchiveEntry{
		EntryID:      "pa_1",
		UserID:       "ren",
		OriginalText: "bio",
		Protected:    true,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SavePersonalArchiveEntry() error = %v", err)
	}
	if err := store.SaveCreativeKnowledgeItem(context.Background(), domainkm.CreativeKnowledgeItem{
		ItemID:    "ck_1",
		Title:     "title",
		Status:    "candidate",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveCreativeKnowledgeItem() error = %v", err)
	}
	if err := store.SaveNewsKnowledgeItem(context.Background(), domainkm.NewsKnowledgeItem{
		ItemID:    "news_1",
		Source:    "source",
		Topic:     "topic",
		Status:    "candidate",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveNewsKnowledgeItem() error = %v", err)
	}
	if err := store.SaveDailyIntakeRule(context.Background(), domainkm.DailyIntakeRule{
		RuleID:    "rule_1",
		UserID:    "ren",
		Topic:     "AI",
		Cadence:   "daily",
		Status:    "active",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveDailyIntakeRule() error = %v", err)
	}
	if err := store.SaveTemporalMemoryMarker(context.Background(), domainkm.TemporalMemoryMarker{
		MarkerID:    "tm_1",
		Layer:       "today",
		ReferenceID: "pa_1",
		Summary:     "bio",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveTemporalMemoryMarker() error = %v", err)
	}
	if err := store.SaveDreamConsolidationRun(context.Background(), domainkm.DreamConsolidationRun{
		RunID:        "dream_1",
		Status:       "proposal",
		ReviewStatus: "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveDreamConsolidationRun() error = %v", err)
	}
	assertOne := func(name string, err error, got int) {
		t.Helper()
		if err != nil || got != 1 {
			t.Fatalf("%s count = %d, err = %v", name, got, err)
		}
	}
	personal, err := store.ListPersonalArchiveEntries(context.Background(), 10)
	assertOne("personal", err, len(personal))
	creative, err := store.ListCreativeKnowledgeItems(context.Background(), 10)
	assertOne("creative", err, len(creative))
	news, err := store.ListNewsKnowledgeItems(context.Background(), 10)
	assertOne("news", err, len(news))
	rules, err := store.ListDailyIntakeRules(context.Background(), 10)
	assertOne("rules", err, len(rules))
	markers, err := store.ListTemporalMemoryMarkers(context.Background(), 10)
	assertOne("markers", err, len(markers))
	dreams, err := store.ListDreamConsolidationRuns(context.Background(), 10)
	assertOne("dreams", err, len(dreams))
}

func TestSQLiteStoreRejectsUnprotectedPersonalArchive(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge_memory.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	err = store.SavePersonalArchiveEntry(context.Background(), domainkm.PersonalArchiveEntry{
		EntryID:      "pa_1",
		UserID:       "ren",
		OriginalText: "bio",
		Protected:    false,
		CreatedAt:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected unprotected personal archive to fail")
	}
}
