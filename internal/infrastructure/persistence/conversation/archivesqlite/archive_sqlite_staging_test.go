package archivesqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestFindStagingItemByNamespaceEventIDUsesExactBoundedLookup(t *testing.T) {
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore(): %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	item := l1sqlite.L1StagingItem{
		ID: "archive-stage-1", Kind: l1sqlite.L1StagingKindSearchResult, Namespace: "kb:dci",
		EventID: "evt_00000000-0000-7000-8000-000000000001", SourceID: "source-1",
		SourceURL: "https://example.invalid/source", FetchedAt: time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC),
		RawText: "evidence", SummaryDraft: "summary", Keywords: []string{"term"}, LicenseNote: "license",
		ValidationStatus: l1sqlite.L1StagingStatusValidated, Meta: map[string]interface{}{"source_kind": "dci"},
	}
	item.RawHash = stagingTestHash(item.RawText)
	item.CreatedAt = item.FetchedAt
	item.UpdatedAt = item.FetchedAt
	insertArchiveStagingItem(t, store, item)

	got, found, err := store.FindStagingItemByNamespaceEventID(ctx, item.Namespace, item.EventID)
	if err != nil {
		t.Fatalf("FindStagingItemByNamespaceEventID(): %v", err)
	}
	got.FetchedAt = got.FetchedAt.UTC()
	got.CreatedAt = got.CreatedAt.UTC()
	got.UpdatedAt = got.UpdatedAt.UTC()
	if !found || !reflect.DeepEqual(got, item) {
		t.Fatalf("lookup = found %v item %#v, want %#v", found, got, item)
	}
	if got, found, err := store.FindStagingItemByNamespaceEventID(ctx, item.Namespace, "evt_00000000-0000-7000-8000-000000000099"); err != nil || found || !reflect.DeepEqual(got, l1sqlite.L1StagingItem{}) {
		t.Fatalf("missing lookup = %#v, found %v, err %v", got, found, err)
	}
	for _, invalid := range [][2]string{{"", item.EventID}, {"kb:", item.EventID}, {item.Namespace, ""}, {item.Namespace, " "}, {item.Namespace, " " + item.EventID}} {
		if _, _, err := store.FindStagingItemByNamespaceEventID(ctx, invalid[0], invalid[1]); err == nil {
			t.Fatalf("lookup(%q,%q) returned nil error", invalid[0], invalid[1])
		}
	}

	var indexExists int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('l1_staging_item_archive') WHERE name = 'idx_l1_staging_archive_namespace_event'`).Scan(&indexExists); err != nil {
		t.Fatalf("archive staging index lookup: %v", err)
	}
	if indexExists != 1 {
		t.Fatal("idx_l1_staging_archive_namespace_event is missing")
	}
	var planID, planParent, planNotUsed int
	var queryPlan string
	if err := store.db.QueryRowContext(ctx, `EXPLAIN QUERY PLAN SELECT id FROM l1_staging_item_archive WHERE namespace = ? AND event_id = ? LIMIT 2`, item.Namespace, item.EventID).Scan(&planID, &planParent, &planNotUsed, &queryPlan); err != nil {
		t.Fatalf("archive staging query plan: %v", err)
	}
	if !strings.Contains(strings.ToUpper(queryPlan), "IDX_L1_STAGING_ARCHIVE_NAMESPACE_EVENT") {
		t.Fatalf("exact lookup did not use archive namespace/event index: %q", queryPlan)
	}
}

func TestFindStagingItemByNamespaceEventIDRejectsDuplicatesAndCanceledContext(t *testing.T) {
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore(): %v", err)
	}
	defer store.Close()

	for _, id := range []string{"archive-stage-a", "archive-stage-b"} {
		item := l1sqlite.L1StagingItem{
			ID: id, Kind: l1sqlite.L1StagingKindSearchResult, Namespace: "kb:dci",
			EventID: "evt_00000000-0000-7000-8000-000000000002", SourceID: "source", RawText: "text",
			RawHash: stagingTestHash("text"), Keywords: []string{}, Meta: map[string]interface{}{},
			FetchedAt: time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC), CreatedAt: time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC),
		}
		insertArchiveStagingItem(t, store, item)
	}
	if _, _, err := store.FindStagingItemByNamespaceEventID(context.Background(), "kb:dci", "evt_00000000-0000-7000-8000-000000000002"); err == nil {
		t.Fatal("duplicate lookup returned nil error")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.FindStagingItemByNamespaceEventID(canceled, "kb:dci", "evt_00000000-0000-7000-8000-000000000002"); err == nil {
		t.Fatal("canceled lookup returned nil error")
	}
}

func insertArchiveStagingItem(t *testing.T, store *ArchiveSQLiteStore, item l1sqlite.L1StagingItem) {
	t.Helper()
	keywords, err := json.Marshal(item.Keywords)
	if err != nil {
		t.Fatalf("marshal keywords: %v", err)
	}
	meta, err := json.Marshal(item.Meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
INSERT INTO l1_staging_item_archive (
 id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
 raw_text, raw_hash, summary_draft, keywords_json, license_note, validation_status,
 meta_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Kind, item.Namespace, item.EventID, item.SourceID, item.SourceURL, item.FetchedAt,
		item.RawText, item.RawHash, item.SummaryDraft, string(keywords), item.LicenseNote, item.ValidationStatus,
		string(meta), item.CreatedAt, item.UpdatedAt); err != nil {
		t.Fatalf("insert archive staging item: %v", err)
	}
}

func stagingTestHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
