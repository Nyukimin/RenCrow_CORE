package duckdb

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"path/filepath"
	"testing"
	"time"
)

func TestL1SQLiteStore_SaveStagingItemArchivesToDuckDB(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("l1sqlite.NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	archive, err := NewDuckDBStore(filepath.Join(t.TempDir(), "archive.duckdb"))
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer archive.Close()
	store.WithArchiveStore(archive)

	item, err := store.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:         l1sqlite.L1StagingKindExternalFetch,
		Namespace:    "kb:news",
		EventID:      "stage-archive-1",
		SourceID:     "rss:archive",
		SourceURL:    "https://example.com/archive",
		FetchedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		RawText:      "staging raw",
		SummaryDraft: "staging summary",
		Keywords:     []string{"archive"},
		LicenseNote:  "official rss excerpt",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	var count int
	if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_staging_item_archive WHERE id = ?`, item.ID).Scan(&count); err != nil {
		t.Fatalf("archive staging count failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected staging archive row, got %d", count)
	}
}

func TestL1SQLiteStore_PromoterArchivesPromotedItemsToDuckDB(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("l1sqlite.NewL1SQLiteStore failed: %v", err)
	}
	defer l1.Close()
	archive, err := NewDuckDBStore(filepath.Join(t.TempDir(), "archive.duckdb"))
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer archive.Close()
	l1.WithArchiveStore(archive)

	memoryItem, err := l1.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:         l1sqlite.L1StagingKindMemoryCandidate,
		Namespace:    "conv:archive",
		EventID:      "evt-archive-memory",
		SourceID:     "conversation",
		SourceURL:    "https://example.com/conversation/archive",
		FetchedAt:    time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		RawText:      "ユーザーは短い返答を好む",
		SummaryDraft: "短い返答を好む",
		Keywords:     []string{"preference"},
		LicenseNote:  "user provided",
		Meta:         map[string]interface{}{"type": "preference", "session_id": "sess-archive", "thread_id": float64(10)},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem memory failed: %v", err)
	}
	if _, err := l1.ValidateStagingItem(ctx, memoryItem.ID, l1sqlite.L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"conversation": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 12, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem memory failed: %v", err)
	}
	if _, err := l1.PromoteValidatedStagingItemToMemory(ctx, memoryItem.ID, "user:archive", "validator"); err != nil {
		t.Fatalf("PromoteValidatedStagingItemToMemory failed: %v", err)
	}

	newsItem, err := l1.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:         l1sqlite.L1StagingKindExternalFetch,
		Namespace:    "kb:news",
		EventID:      "evt-archive-news",
		SourceID:     "rss:archive",
		SourceURL:    "https://example.com/news/archive",
		FetchedAt:    time.Date(2026, 5, 5, 8, 0, 0, 0, time.UTC),
		PublishedAt:  time.Date(2026, 5, 5, 7, 0, 0, 0, time.UTC),
		RawText:      "ニュース本文",
		SummaryDraft: "ニュース要約",
		Keywords:     []string{"ai"},
		LicenseNote:  "official rss excerpt",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem news failed: %v", err)
	}
	if _, err := l1.ValidateStagingItem(ctx, newsItem.ID, l1sqlite.L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"rss:archive": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 8, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem news failed: %v", err)
	}
	if _, err := l1.PromoteValidatedStagingItemToNews(ctx, newsItem.ID, "ai"); err != nil {
		t.Fatalf("PromoteValidatedStagingItemToNews failed: %v", err)
	}

	kbItem, err := l1.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:         l1sqlite.L1StagingKindExternalFetch,
		Namespace:    "kb:movie",
		EventID:      "evt-archive-kb",
		SourceID:     "api:archive",
		SourceURL:    "https://example.com/kb/archive",
		FetchedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		RawText:      "作品本文",
		SummaryDraft: "作品要約",
		Keywords:     []string{"movie"},
		LicenseNote:  "official api",
		Meta:         map[string]interface{}{"title": "Archive Movie"},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem knowledge failed: %v", err)
	}
	if _, err := l1.ValidateStagingItem(ctx, kbItem.ID, l1sqlite.L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"api:archive": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 10, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem knowledge failed: %v", err)
	}
	if _, err := l1.PromoteValidatedStagingItemToKnowledge(ctx, kbItem.ID, "movie"); err != nil {
		t.Fatalf("PromoteValidatedStagingItemToKnowledge failed: %v", err)
	}

	for table, want := range map[string]int{
		"l1_memory_event_archive":   1,
		"l1_news_item_archive":      1,
		"l1_knowledge_item_archive": 1,
	} {
		var got int
		if err := archive.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			t.Fatalf("archive count failed for %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("archive count for %s: want %d, got %d", table, want, got)
		}
	}
}
