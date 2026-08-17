package l1sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestBackfillKnowledgeCommonRawEmptyIsNotReady(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	itemCount, covered, ready, err := store.KnowledgeCommonRawCoverage(context.Background())
	if err != nil || itemCount != 0 || covered != 0 || ready {
		t.Fatalf("empty coverage itemCount=%d covered=%d ready=%v err=%v", itemCount, covered, ready, err)
	}
	_, err = store.BackfillKnowledgeCommonRaw(commonRawTestContext(t, "knowledge-empty"), "knowledge-empty", "ren", "ren", true)
	if domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorInvalid {
		t.Fatalf("empty backfill code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 0 {
		t.Fatalf("empty backfill wrote raw: %d", got)
	}
}

func TestBackfillKnowledgeCommonRawLinksExistingRowsWithoutRewrite(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := insertHashedKnowledgeItem(t, store, "kb:general:one")
	second := insertHashedKnowledgeItem(t, store, "kb:general:two")
	before := snapshotKnowledgeRows(t, store, []string{first, second})

	dry, err := store.BackfillKnowledgeCommonRaw(commonRawTestContext(t, "knowledge-dry"), "knowledge-dry", "ren", "ren", false)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Validated != 2 || dry.Ready || dry.RawImported != 0 {
		t.Fatalf("unexpected dry result: %+v", dry)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 0 {
		t.Fatalf("dry-run wrote raw: %d", got)
	}

	apply, err := store.BackfillKnowledgeCommonRaw(commonRawTestContext(t, "knowledge-apply"), "knowledge-apply", "ren", "ren", true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if apply.RawImported != 2 || apply.Linked != 2 || apply.Coverage != 2 || !apply.Ready || apply.Status != domainmemory.CommonRawStateCompleted {
		t.Fatalf("unexpected apply result: %+v", apply)
	}
	if after := snapshotKnowledgeRows(t, store, []string{first, second}); !equalStringMap(before, after) {
		t.Fatalf("knowledge rows were rewritten:\nbefore=%v\nafter=%v", before, after)
	}

	replay, err := store.BackfillKnowledgeCommonRaw(commonRawTestContext(t, "knowledge-replay"), "knowledge-replay", "ren", "ren", true)
	if err != nil {
		t.Fatal(err)
	}
	if replay.RawReplayed != 2 || replay.RawImported != 0 || !replay.Ready {
		t.Fatalf("unexpected replay result: %+v", replay)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 1 {
		t.Fatalf("manifest count=%d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_record"); got != 2 {
		t.Fatalf("raw record count=%d", got)
	}
}

func TestBackfillKnowledgeCommonRawRejectsHashMismatch(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := insertHashedKnowledgeItem(t, store, "kb:general:changed")
	if _, err := store.db.Exec(`UPDATE l1_knowledge_item SET raw_hash = ? WHERE id = ?`, strings.Repeat("c", 64), id); err != nil {
		t.Fatal(err)
	}
	_, err = store.BackfillKnowledgeCommonRaw(commonRawTestContext(t, "knowledge-hash"), "knowledge-hash", "ren", "ren", true)
	if domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorSourceChanged {
		t.Fatalf("hash mismatch code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 0 {
		t.Fatalf("hash mismatch wrote raw: %d", got)
	}
	_, _, ready, coverErr := store.KnowledgeCommonRawCoverage(context.Background())
	if coverErr == nil || ready {
		t.Fatalf("coverage accepted a hash mismatch: ready=%v err=%v", ready, coverErr)
	}
}

func insertHashedKnowledgeItem(t *testing.T, store *L1SQLiteStore, id string) string {
	t.Helper()
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	body := "knowledge body for " + id
	hash := domainmemory.SHA256Hex([]byte(body))
	_, err := store.db.Exec(`INSERT INTO l1_knowledge_item
(id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json, created_at, updated_at)
VALUES (?, ?, 'general', ?, 'test', '', ?, ?, ?, '[]', '', '{}', ?, ?)`,
		id, "staging-"+id, "Title "+id, body, hash, "summary "+id, now, now)
	if err != nil {
		t.Fatalf("insert knowledge item %s: %v", id, err)
	}
	return id
}

func snapshotKnowledgeRows(t *testing.T, store *L1SQLiteStore, ids []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(ids))
	for _, id := range ids {
		var rawText, rawHash, title, stagingID string
		var updatedAt time.Time
		if err := store.db.QueryRow(`
SELECT raw_text, raw_hash, title, staging_id, updated_at FROM l1_knowledge_item WHERE id = ?`, id).Scan(&rawText, &rawHash, &title, &stagingID, &updatedAt); err != nil {
			t.Fatalf("snapshot knowledge %s: %v", id, err)
		}
		result[id] = rawText + "\x00" + rawHash + "\x00" + title + "\x00" + stagingID + "\x00" + updatedAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}
