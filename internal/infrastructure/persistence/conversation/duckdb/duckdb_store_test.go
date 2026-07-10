package duckdb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/parquet-go/parquet-go"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestDuckDBStore_ExportThreadSummariesParquet(t *testing.T) {
	ctx := context.Background()
	store, err := NewDuckDBStore(":memory:")
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer store.Close()

	if err := store.SaveThreadSummary(ctx, &domconv.ThreadSummary{
		ThreadID:  101,
		StartTime: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 1, 10, 10, 0, 0, time.UTC),
		Domain:    "ai",
		Summary:   "AI discussion",
		Keywords:  []string{"ai", "local-llm"},
		Embedding: []float32{0.1, 0.2},
		IsNovel:   true,
	}); err != nil {
		t.Fatalf("SaveThreadSummary failed: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "thread_summaries.parquet")
	if err := store.ExportThreadSummariesParquet(ctx, outPath); err != nil {
		t.Fatalf("ExportThreadSummariesParquet failed: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("expected parquet file: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("parquet file should not be empty")
	}

	readBack, err := parquet.ReadFile[threadSummaryParquetRow](outPath)
	if err != nil {
		t.Fatalf("failed to read back parquet file: %v", err)
	}
	if len(readBack) != 1 {
		t.Fatalf("parquet row count: want 1, got %d", len(readBack))
	}
	if readBack[0].ThreadID != 101 {
		t.Fatalf("unexpected thread_id in parquet row: %+v", readBack[0])
	}
}

func TestDuckDBStore_SaveThreadSummaryArchivesSessionID(t *testing.T) {
	ctx := context.Background()
	store, err := NewDuckDBStore(":memory:")
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer store.Close()

	if err := store.SaveThreadSummary(ctx, &domconv.ThreadSummary{
		ThreadID:  201,
		SessionID: "sess-l2",
		StartTime: time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 1, 11, 10, 0, 0, time.UTC),
		Domain:    "memory",
		Summary:   "L2 summary archived by session",
		Keywords:  []string{"l2"},
	}); err != nil {
		t.Fatalf("SaveThreadSummary failed: %v", err)
	}

	got, err := store.GetSessionHistory(ctx, "sess-l2", 10)
	if err != nil {
		t.Fatalf("GetSessionHistory failed: %v", err)
	}
	if len(got) != 1 || got[0].ThreadID != 201 || got[0].SessionID != "sess-l2" {
		t.Fatalf("unexpected session history: %+v", got)
	}
}

func TestDuckDBStore_SaveThreadSummaryRejectsMalformedSummary(t *testing.T) {
	ctx := context.Background()
	store, err := NewDuckDBStore(":memory:")
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer store.Close()

	tests := []struct {
		name    string
		summary *domconv.ThreadSummary
		want    string
	}{
		{name: "nil", summary: nil, want: "thread summary is required"},
		{name: "missing thread id", summary: &domconv.ThreadSummary{Summary: "valid summary"}, want: "thread_id must be > 0"},
		{name: "missing summary", summary: &domconv.ThreadSummary{ThreadID: 301, Summary: "   "}, want: "summary is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.SaveThreadSummary(ctx, tt.summary)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SaveThreadSummary() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDuckDBStore_ReadThreadSummaryRejectsMalformedRows(t *testing.T) {
	ctx := context.Background()
	store, err := NewDuckDBStore(":memory:")
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer store.Close()

	_, err = store.db.ExecContext(ctx, `
INSERT INTO session_thread (
	thread_id, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, int64(401), "sess-bad-l2", time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC), time.Date(2026, 5, 20, 9, 5, 0, 0, time.UTC), "memory", "", "[]", "[]", false)
	if err != nil {
		t.Fatalf("insert malformed thread summary: %v", err)
	}

	_, err = store.GetSessionHistory(ctx, "sess-bad-l2", 10)
	if err == nil || !strings.Contains(err.Error(), "summary is required") {
		t.Fatalf("GetSessionHistory() error = %v, want summary validation", err)
	}
	_, err = store.SearchByDomain(ctx, "memory", 10)
	if err == nil || !strings.Contains(err.Error(), "summary is required") {
		t.Fatalf("SearchByDomain() error = %v, want summary validation", err)
	}
}

func TestDuckDBStore_ArchiveL1DataParquet(t *testing.T) {
	ctx := context.Background()
	store, err := NewDuckDBStore(":memory:")
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	if err := store.ArchiveL1MemoryEvents(ctx, []l1sqlite.L1MemoryEvent{{
		ID:          "mem-1",
		Namespace:   "user:ren",
		SessionID:   "sess-1",
		ThreadID:    1,
		Speaker:     "mio",
		Message:     "confirmed preference",
		Meta:        map[string]any{"type": "preference"},
		MemoryState: l1sqlite.MemoryStateConfirmed,
		Layer:       l1sqlite.MemoryLayerL1,
		Source:      "test",
		CreatedAt:   now,
		UpdatedAt:   now,
	}}); err != nil {
		t.Fatalf("ArchiveL1MemoryEvents failed: %v", err)
	}
	if err := store.ArchiveL1NewsItems(ctx, []l1sqlite.L1NewsItem{{
		ID:           "news-1",
		StagingID:    "stage-news-1",
		Category:     "ai",
		SourceID:     "rss:test",
		SourceURL:    "https://example.com/news/1",
		PublishedAt:  now,
		FetchedAt:    now,
		RawText:      "raw news",
		RawHash:      "hash-news-1",
		SummaryDraft: "summary news",
		Keywords:     []string{"ai", "local"},
		LicenseNote:  "public feed",
		Meta:         map[string]any{"event_id": "evt-news-1"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}}); err != nil {
		t.Fatalf("ArchiveL1NewsItems failed: %v", err)
	}
	if err := store.ArchiveL1KnowledgeItems(ctx, []l1sqlite.L1KnowledgeItem{{
		ID:           "kb-1",
		StagingID:    "stage-kb-1",
		Domain:       "movie",
		Title:        "Interstellar",
		SourceID:     "manual",
		SourceURL:    "https://example.com/kb/1",
		RawText:      "raw kb",
		RawHash:      "hash-kb-1",
		SummaryDraft: "summary kb",
		Keywords:     []string{"space"},
		LicenseNote:  "manual",
		Meta:         map[string]any{"year": 2014},
		CreatedAt:    now,
		UpdatedAt:    now,
	}}); err != nil {
		t.Fatalf("ArchiveL1KnowledgeItems failed: %v", err)
	}
	if err := store.ArchiveL1StagingItems(ctx, []l1sqlite.L1StagingItem{{
		ID:               "stage-1",
		Kind:             l1sqlite.L1StagingKindMemoryCandidate,
		Namespace:        "user:ren",
		EventID:          "evt-stage-1",
		SourceID:         "conversation",
		SourceURL:        "https://example.com/conversation/1",
		FetchedAt:        now,
		PublishedAt:      now,
		RawText:          "raw staging",
		RawHash:          "hash-stage-1",
		SummaryDraft:     "summary staging",
		Keywords:         []string{"preference"},
		LicenseNote:      "user provided",
		ValidationStatus: l1sqlite.L1StagingStatusValidated,
		Meta:             map[string]any{"type": "preference"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}}); err != nil {
		t.Fatalf("ArchiveL1StagingItems failed: %v", err)
	}

	outDir := t.TempDir()
	paths, err := store.ExportL1ArchivesParquet(ctx, outDir)
	if err != nil {
		t.Fatalf("ExportL1ArchivesParquet failed: %v", err)
	}
	for _, name := range []string{L1ArchiveMemory, L1ArchiveNews, L1ArchiveKnowledge, L1ArchiveStaging} {
		path := paths[name]
		if path == "" {
			t.Fatalf("missing parquet path for %s", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected parquet file for %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("parquet file for %s should not be empty", name)
		}

		var count int
		switch name {
		case L1ArchiveMemory:
			rows, err := parquet.ReadFile[l1MemoryEventParquetRow](path)
			if err != nil {
				t.Fatalf("failed to read back parquet file for %s: %v", name, err)
			}
			count = len(rows)
		case L1ArchiveNews:
			rows, err := parquet.ReadFile[l1NewsItemParquetRow](path)
			if err != nil {
				t.Fatalf("failed to read back parquet file for %s: %v", name, err)
			}
			count = len(rows)
		case L1ArchiveKnowledge:
			rows, err := parquet.ReadFile[l1KnowledgeItemParquetRow](path)
			if err != nil {
				t.Fatalf("failed to read back parquet file for %s: %v", name, err)
			}
			count = len(rows)
		case L1ArchiveStaging:
			rows, err := parquet.ReadFile[l1StagingItemParquetRow](path)
			if err != nil {
				t.Fatalf("failed to read back parquet file for %s: %v", name, err)
			}
			count = len(rows)
		default:
			t.Fatalf("unexpected archive kind %s", name)
		}
		if count != 1 {
			t.Fatalf("parquet row count for %s: want 1, got %d", name, count)
		}
	}
}

func TestDuckDBStore_SearchKnowledgeArchiveFTS(t *testing.T) {
	ctx := context.Background()
	store, err := NewDuckDBStore(":memory:")
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	items := []l1sqlite.L1KnowledgeItem{
		{
			ID:           "kb-space",
			StagingID:    "stage-kb-space",
			Domain:       "movie",
			Title:        "Interstellar",
			SourceID:     "manual",
			SourceURL:    "https://example.com/kb/space",
			RawText:      "重力と時間を扱う宇宙映画",
			RawHash:      "hash-space",
			SummaryDraft: "父と娘、重力、時間の話",
			Keywords:     []string{"宇宙", "重力"},
			LicenseNote:  "manual",
			CreatedAt:    now,
			UpdatedAt:    now.Add(time.Minute),
		},
		{
			ID:           "kb-music",
			StagingID:    "stage-kb-music",
			Domain:       "music",
			Title:        "Ambient Note",
			SourceID:     "manual",
			SourceURL:    "https://example.com/kb/music",
			RawText:      "静かな音楽メモ",
			RawHash:      "hash-music",
			SummaryDraft: "音色の話",
			Keywords:     []string{"音楽"},
			LicenseNote:  "manual",
			CreatedAt:    now,
			UpdatedAt:    now.Add(2 * time.Minute),
		},
	}
	if err := store.ArchiveL1KnowledgeItems(ctx, items); err != nil {
		t.Fatalf("ArchiveL1KnowledgeItems failed: %v", err)
	}

	got, err := store.SearchKnowledgeArchiveFTS(ctx, "movie", "重力", 10)
	if err != nil {
		t.Fatalf("SearchKnowledgeArchiveFTS failed: %v", err)
	}
	if len(got) != 1 || got[0].ID != "kb-space" {
		t.Fatalf("unexpected knowledge archive search results: %+v", got)
	}
	if got[0].Keywords[0] != "宇宙" {
		t.Fatalf("keywords should be restored from archive json: %+v", got[0])
	}
}
