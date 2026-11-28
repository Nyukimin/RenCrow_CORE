package l1sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchResearchStagingDocumentsScopesAndAppliesTimeWindow(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	insertResearchRecallItem(t, store, "public-hit", "kb:research", "evt-public-hit", "public", "", "稲作制度の変化と農業", base, base.Add(-24*time.Hour))
	insertResearchRecallItem(t, store, "user-hit", "user:alice", "evt-user-hit", "user", "alice", "稲作制度の変化と農業", base, time.Time{})
	insertResearchRecallItem(t, store, "foreign-hit", "user:bob", "evt-foreign-hit", "user", "bob", "稲作制度の変化と農業", base, time.Time{})
	insertResearchRecallItem(t, store, "promoted", "kb:research", "evt-promoted", "public", "", "稲作制度の変化と農業", base, time.Time{})
	if _, err := store.db.Exec(`UPDATE l1_staging_item SET validation_status='validated' WHERE id='promoted'`); err != nil {
		t.Fatal(err)
	}
	since, until := base.Add(-48*time.Hour), base.Add(48*time.Hour)
	items, err := store.SearchResearchStagingDocuments(ctx, "public", "", "稲作制度の変化", nil, since, until, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != 1 || items.Items[0].ID != "public-hit" || items.ScanLimited {
		t.Fatalf("public documents = %#v, want only public pending research record", items)
	}
	items, err = store.SearchResearchStagingDocuments(ctx, "public", "", "%", nil, since, until, 20)
	if err != nil || len(items.Items) != 0 {
		t.Fatalf("LIKE metacharacter query = %#v, err=%v; want literal no-match", items, err)
	}
	items, err = store.SearchResearchStagingDocuments(ctx, "user", "alice", "稲作制度の変化", nil, since, until, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != 1 || items.Items[0].ID != "user-hit" {
		t.Fatalf("user documents = %#v, want only alice's pending research record", items)
	}
	items, err = store.SearchResearchStagingDocuments(ctx, "user", "alice", "稲作制度の変化", []string{"evt-foreign-hit"}, since, until, 20)
	if err != nil || len(items.Items) != 0 {
		t.Fatalf("foreign ref result = %#v, err=%v; want empty", items, err)
	}
	insertResearchRecallItem(t, store, "public-hit-2", "kb:research", "evt-public-hit-2", "public", "", "稲作制度の変化と農業", base, base)
	items, err = store.SearchResearchStagingDocuments(ctx, "public", "", "稲作制度の変化", nil, since, until, 1)
	if err != nil || len(items.Items) != 1 || !items.CandidatesTruncated {
		t.Fatalf("candidate cap result = %#v err=%v; want truncated bounded candidates", items, err)
	}
}

func TestSearchResearchStagingDocumentsRequiresBoundedCompleteQuery(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, tc := range []struct {
		name, scope, owner, query string
		limit                     int
		since, until              time.Time
	}{
		{"bad-scope", "admin", "", "資料", 2, time.Time{}, time.Time{}},
		{"missing-owner", "user", "", "資料", 2, time.Time{}, time.Time{}},
		{"empty-query", "public", "", " ", 2, time.Time{}, time.Time{}},
		{"bad-limit", "public", "", "資料", 21, time.Time{}, time.Time{}},
		{"partial-window", "public", "", "資料", 2, time.Now(), time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.SearchResearchStagingDocuments(context.Background(), tc.scope, tc.owner, tc.query, nil, tc.since, tc.until, tc.limit); err == nil {
				t.Fatal("invalid search returned nil error")
			}
		})
	}
}

func TestSearchResearchStagingDocumentsReportsBoundedRowIDWindow(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.db.Exec(`WITH RECURSIVE seq(x) AS (
SELECT 1 UNION ALL SELECT x + 1 FROM seq WHERE x < ?
)
INSERT INTO l1_staging_item
(id,kind,namespace,event_id,source_id,source_url,fetched_at,raw_text,raw_hash,summary_draft,keywords_json,license_note,validation_status,meta_json,created_at,updated_at)
SELECT 'unrelated-' || x, 'external_fetch', 'kb:research', 'event-' || x, 'source', '', CURRENT_TIMESTAMP,
'unrelated', 'invalid', '', '[]', '', 'pending', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM seq`, L1ResearchRecallMaxScanRows+1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.SearchResearchStagingDocuments(context.Background(), "public", "", "not-present", nil, time.Time{}, time.Time{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || !result.ScanLimited {
		t.Fatalf("bounded result = %#v, want empty and scan-limited", result)
	}
}

func TestSearchResearchStagingDocumentRefsUseScopedExactLookupsBeyondScanWindow(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	insertResearchRecallItem(t, store, "older-ref-id", "kb:research", "evt-older-ref-id", "public", "", "exact ref target phrase", now, time.Time{})
	insertResearchRecallItem(t, store, "older-ref-event", "kb:research", "evt-older-ref-event", "public", "", "exact ref target phrase", now, time.Time{})
	_, err = store.db.Exec(`WITH RECURSIVE seq(x) AS (
SELECT 1 UNION ALL SELECT x + 1 FROM seq WHERE x < ?
)
INSERT INTO l1_staging_item
(id,kind,namespace,event_id,source_id,source_url,fetched_at,raw_text,raw_hash,summary_draft,keywords_json,license_note,validation_status,meta_json,created_at,updated_at)
SELECT 'unrelated-' || x, 'external_fetch', 'kb:research', 'event-' || x, 'source', '', CURRENT_TIMESTAMP,
'unrelated', 'invalid', '', '[]', '', 'pending', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM seq`, L1ResearchRecallMaxScanRows+1)
	if err != nil {
		t.Fatal(err)
	}
	byID, err := store.SearchResearchStagingDocuments(ctx, "public", "", "exact ref target", []string{"older-ref-id"}, time.Time{}, time.Time{}, 20)
	if err != nil || len(byID.Items) != 1 || byID.Items[0].ID != "older-ref-id" || byID.ScanLimited {
		t.Fatalf("explicit document ID lookup=%+v err=%v, want exact old row without scan limit", byID, err)
	}
	byEvent, err := store.SearchResearchStagingDocuments(ctx, "public", "", "exact ref target", []string{"evt-older-ref-event"}, time.Time{}, time.Time{}, 20)
	if err != nil || len(byEvent.Items) != 1 || byEvent.Items[0].ID != "older-ref-event" || byEvent.ScanLimited {
		t.Fatalf("explicit event ID lookup=%+v err=%v, want exact old row without scan limit", byEvent, err)
	}
	foreign, err := store.SearchResearchStagingDocuments(ctx, "user", "alice", "exact ref target", []string{"older-ref-id"}, time.Time{}, time.Time{}, 20)
	if err != nil || len(foreign.Items) != 0 || foreign.ScanLimited {
		t.Fatalf("scope-external ref lookup=%+v err=%v, want empty and complete", foreign, err)
	}
	unknown, err := store.SearchResearchStagingDocuments(ctx, "public", "", "exact ref target", []string{"unknown-ref"}, time.Time{}, time.Time{}, 20)
	if err != nil || len(unknown.Items) != 0 || unknown.ScanLimited {
		t.Fatalf("unknown ref lookup=%+v err=%v, want empty and complete", unknown, err)
	}
	bounded, err := store.SearchResearchStagingDocuments(ctx, "public", "", "exact ref target", []string{"older-ref-id", "evt-older-ref-event"}, time.Time{}, time.Time{}, 1)
	if err != nil || len(bounded.Items) != 1 || !bounded.CandidatesTruncated || bounded.ScanLimited {
		t.Fatalf("bounded explicit refs=%+v err=%v, want limit+1 truncation without row scan limit", bounded, err)
	}
	unreferenced, err := store.SearchResearchStagingDocuments(ctx, "public", "", "exact ref target", nil, time.Time{}, time.Time{}, 20)
	if err != nil || len(unreferenced.Items) != 0 || !unreferenced.ScanLimited {
		t.Fatalf("unreferenced bounded search=%+v err=%v, want existing partial scan behavior", unreferenced, err)
	}
}

func insertResearchRecallItem(t *testing.T, store *L1SQLiteStore, id, namespace, eventID, scope, owner, text string, fetchedAt, publishedAt time.Time) {
	t.Helper()
	meta := map[string]interface{}{
		"research_scope": scope, "research_namespace": namespace,
		"research_digest": rawTextHash(text), "content_complete": true,
		"retention_allowed": true,
	}
	var pub any
	if !publishedAt.IsZero() {
		pub = publishedAt
	}
	if _, err := store.db.Exec(`INSERT INTO l1_staging_item
(id,kind,namespace,event_id,source_id,source_url,fetched_at,published_at,raw_text,raw_hash,summary_draft,keywords_json,license_note,validation_status,meta_json,created_at,updated_at)
VALUES(?, 'external_fetch', ?, ?, 'source', 'https://example.invalid/', ?, ?, ?, ?, '', '[]', 'retention explicitly authorized by research request', 'pending', ?, ?, ?)`,
		id, namespace, eventID, fetchedAt, pub, text, rawTextHash(text), mustResearchRecallMeta(t, meta), fetchedAt, fetchedAt); err != nil {
		t.Fatal(err)
	}
}

func mustResearchRecallMeta(t *testing.T, meta map[string]interface{}) string {
	t.Helper()
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
