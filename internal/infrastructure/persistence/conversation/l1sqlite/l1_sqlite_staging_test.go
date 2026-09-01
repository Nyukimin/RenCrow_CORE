package l1sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFindStagingItemByNamespaceEventIDUsesExactBoundedLookup(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)
	item := L1StagingItem{
		ID: "dci-stage-1", Kind: L1StagingKindSearchResult, Namespace: "kb:dci", EventID: "evt_00000000-0000-7000-8000-000000000001",
		SourceID: "source-1", SourceURL: "https://example.invalid/source", FetchedAt: now, RawText: "evidence", RawHash: "",
		SummaryDraft: "summary", Keywords: []string{"term"}, LicenseNote: "license", ValidationStatus: L1StagingStatusPending,
		Meta: map[string]interface{}{"source_kind": "dci"}, CreatedAt: now, UpdatedAt: now,
	}
	item.RawHash = rawTextHash(item.RawText)
	keywords, err := json.Marshal(item.Keywords)
	if err != nil {
		t.Fatalf("marshal keywords: %v", err)
	}
	meta, err := json.Marshal(item.Meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_staging_item (
 id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
 raw_text, raw_hash, summary_draft, keywords_json, license_note, validation_status,
 meta_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Kind, item.Namespace, item.EventID, item.SourceID, item.SourceURL, item.FetchedAt,
		item.RawText, item.RawHash, item.SummaryDraft, string(keywords), item.LicenseNote, item.ValidationStatus,
		string(meta), item.CreatedAt, item.UpdatedAt); err != nil {
		t.Fatalf("insert staging item: %v", err)
	}
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
	if got, found, err := store.FindStagingItemByNamespaceEventID(ctx, item.Namespace, "evt_00000000-0000-7000-8000-000000000099"); err != nil || found || !reflect.DeepEqual(got, L1StagingItem{}) {
		t.Fatalf("missing lookup = %#v, found %v, err %v", got, found, err)
	}
	for _, invalid := range [][2]string{{"", item.EventID}, {"kb:", item.EventID}, {item.Namespace, ""}, {item.Namespace, " "}, {item.Namespace, " " + item.EventID}} {
		if _, _, err := store.FindStagingItemByNamespaceEventID(ctx, invalid[0], invalid[1]); err == nil {
			t.Fatalf("lookup(%q,%q) returned nil error", invalid[0], invalid[1])
		}
	}
	var indexExists int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('l1_staging_item') WHERE name = 'idx_l1_staging_namespace_event'`).Scan(&indexExists); err != nil {
		t.Fatalf("staging index lookup: %v", err)
	}
	if indexExists != 1 {
		t.Fatal("idx_l1_staging_namespace_event is missing")
	}
}

func TestFindStagingItemByNamespaceEventIDRejectsDuplicatesAndCanceledContext(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore(): %v", err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`DROP INDEX idx_l1_staging_namespace_event`); err != nil {
		t.Fatalf("drop unique index: %v", err)
	}
	for _, id := range []string{"stage-a", "stage-b"} {
		if _, err := store.db.Exec(`INSERT INTO l1_staging_item (id,kind,namespace,event_id,source_id,source_url,fetched_at,raw_text,raw_hash,summary_draft,keywords_json,license_note,validation_status,meta_json,created_at,updated_at) VALUES (?, 'search_result', 'kb:dci', 'evt_00000000-0000-7000-8000-000000000002', 'source', '', CURRENT_TIMESTAMP, 'text', ?, '', '[]', '', 'pending', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, id, rawTextHash("text")); err != nil {
			t.Fatalf("insert duplicate %s: %v", id, err)
		}
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
