package knowledgememory

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
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

func TestSQLiteStoreEnsureOwnerRouteSchemaUpgradesExistingWritableDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge_memory.db")
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE creative_knowledge (item_id TEXT PRIMARY KEY, created_at TEXT, payload TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatalf("create existing schema error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close existing schema db: %v", err)
	}

	store, err := OpenSQLiteStoreWritable(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStoreWritable() error = %v", err)
	}
	defer store.Close()
	if err := store.EnsureOwnerRouteSchema(context.Background()); err != nil {
		t.Fatalf("EnsureOwnerRouteSchema() error = %v", err)
	}
	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "knowledge_memory_request_receipts"},
		{kind: "index", name: "idx_knowledge_memory_request_receipts_user_created"},
	} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			t.Fatalf("query %s %q: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Fatalf("schema object %s %q count = %d", object.kind, object.name, count)
		}
	}
	var unrelated int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'personal_archive'`).Scan(&unrelated); err != nil {
		t.Fatalf("query unrelated schema: %v", err)
	}
	if unrelated != 0 {
		t.Fatal("EnsureOwnerRouteSchema created unrelated schema")
	}
}
