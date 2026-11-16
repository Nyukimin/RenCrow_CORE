package knowledgememory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

func luminaDiaryReceiptItem(entryID, userID, sourceRef, original string) domainkm.PersonalArchiveEntry {
	return domainkm.PersonalArchiveEntry{
		EntryID:      entryID,
		UserID:       userID,
		SourceRef:    sourceRef,
		Title:        "【ルミナとれんの会話記録】2026-11-15",
		SourceKind:   "lumina_diary_email",
		AuthorID:     "lumina",
		DiaryDate:    "2026-11-15",
		OriginalText: original,
		Protected:    true,
		CreatedAt:    time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC),
	}
}

func TestPersonalArchiveReceiptSavesOriginalAndIndexesFullPrivateDiary(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	locator := "gmail://ren%40example.com/messages/0123456789abcdef"
	body := "冒頭 " + strings.Repeat("背景文章", 10_000) + " 最後に固有語 luminaTailEvidence"
	item := luminaDiaryReceiptItem("diary-1", "ren", locator, body)
	reused, err := store.SavePersonalArchiveEntryWithReceipt(ctx, item, locator, "lumina")
	if err != nil || reused {
		t.Fatalf("first save = reused %v, err %v", reused, err)
	}
	var receiptCount, documentCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_memory_personal_archive_receipts`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_memory_search_documents WHERE record_type = ? AND record_id = ? AND scope = 'user' AND user_id = 'ren' AND visibility = 'private'`, personalArchiveRecordType, item.EntryID).Scan(&documentCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 || documentCount != 1 {
		t.Fatalf("receipt/document counts = %d/%d", receiptCount, documentCount)
	}

	results, err := store.Search(ctx, appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: "ren"},
		Query: "luminaTailEvidence", RecordType: personalArchiveRecordType, Limit: 5,
	})
	if err != nil || len(results) != 1 || results[0].RecordID != item.EntryID {
		t.Fatalf("owner search results=%#v err=%v", results, err)
	}
	if strings.Contains(results[0].Title+results[0].Summary, "luminaTailEvidence") || strings.Contains(results[0].Title+results[0].Summary, "背景文章") {
		t.Fatalf("search projection leaked original text: %#v", results[0])
	}
	otherOwner, err := store.Search(ctx, appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: "other"},
		Query: "luminaTailEvidence", RecordType: personalArchiveRecordType, Limit: 5,
	})
	if err != nil || len(otherOwner) != 0 {
		t.Fatalf("other-owner results=%#v err=%v", otherOwner, err)
	}
	stored, err := store.ListPersonalArchiveEntries(ctx, 5)
	if err != nil || len(stored) != 1 || stored[0].OriginalText != body || stored[0].SourceRef != locator || stored[0].DiaryDate != item.DiaryDate || stored[0].AuthorID != "lumina" {
		t.Fatalf("stored protected original=%#v err=%v", stored, err)
	}
	ownerDetail, found, err := store.GetPersonalArchiveEntry(ctx, item.EntryID, "ren")
	if err != nil || !found || ownerDetail.OriginalText != body {
		t.Fatalf("owner-scoped exact detail=%#v found=%v err=%v", ownerDetail, found, err)
	}
	otherDetail, found, err := store.GetPersonalArchiveEntry(ctx, item.EntryID, "other")
	if err != nil || found || otherDetail.OriginalText != "" {
		t.Fatalf("other-owner exact detail=%#v found=%v err=%v", otherDetail, found, err)
	}

	reused, err = store.SavePersonalArchiveEntryWithReceipt(ctx, item, locator, "lumina")
	if err != nil || !reused {
		t.Fatalf("exact replay = reused %v, err %v", reused, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM personal_archive WHERE entry_id = ?`, item.EntryID).Scan(&receiptCount); err != nil || receiptCount != 1 {
		t.Fatalf("archive rows=%d err=%v", receiptCount, err)
	}
}

func TestOneHundredEightyReceiptBoundDiariesRemainReadyAndPrivateSearchable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	if _, err := ImportJSONLToSQLite(ctx, t.TempDir(), path); err != nil {
		t.Fatalf("promote empty imported source: %v", err)
	}
	store, err := OpenSQLiteStoreWritable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()

	const entryCount = 180
	start := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	items := make([]domainkm.PersonalArchiveEntry, 0, entryCount)
	for i := 0; i < entryCount; i++ {
		day := start.AddDate(0, 0, i)
		date := day.Format("2006-01-02")
		locator := fmt.Sprintf("gmail://nyukimin0811%%40gmail.com/messages/%016x", i+1)
		item := luminaDiaryReceiptItem(fmt.Sprintf("diary-%03d", i), "ren", locator,
			"source details "+strings.Repeat("背景", 100)+fmt.Sprintf(" lateBodyEvidenceDay%03d", i))
		item.Title = "Lumina diary " + date
		item.DiaryDate = date
		item.CreatedAt = day
		if _, err := store.SavePersonalArchiveEntryWithReceipt(ctx, item, locator, "lumina"); err != nil {
			t.Fatalf("save receipt-bound diary %d: %v", i, err)
		}
		items = append(items, item)
	}
	readiness, err := store.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.DatabaseAvailable || !readiness.SchemaReady || !readiness.IndexReady || readiness.Coverage.State != KnowledgeMemoryCoverageReady || readiness.IntegrityState != KnowledgeMemoryIntegrityReady {
		t.Fatalf("180 receipt-bound diaries should remain compatible with the imported readiness manifest: readiness=%+v", readiness)
	}
	archiveBefore := snapshotPersonalArchiveReceiptRows(t, ctx, store)
	manifestBefore := snapshotKnowledgeMemoryManifest(t, ctx, store)
	legacyTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		tokens := legacyPersonalArchiveTokens(t, item)
		if _, err := legacyTx.ExecContext(ctx, `DELETE FROM knowledge_memory_search_terms WHERE record_type = ? AND record_id = ?`, personalArchiveRecordType, item.EntryID); err != nil {
			_ = legacyTx.Rollback()
			t.Fatal(err)
		}
		for _, token := range tokens {
			if _, err := legacyTx.ExecContext(ctx, `INSERT INTO knowledge_memory_search_terms (scope, user_id, token, record_type, record_id) VALUES (?, ?, ?, ?, ?)`, appkm.SearchScopeUser, item.UserID, token, personalArchiveRecordType, item.EntryID); err != nil {
				_ = legacyTx.Rollback()
				t.Fatal(err)
			}
		}
	}
	if err := legacyTx.Commit(); err != nil {
		t.Fatal(err)
	}
	legacyReadiness, err := store.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if legacyReadiness.Coverage.State != KnowledgeMemoryCoverageReady || legacyReadiness.IntegrityState != KnowledgeMemoryIntegrityFailed {
		t.Fatalf("V1-shaped projection must expose the stale V2 term contract before repair: %+v", legacyReadiness)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	report, err := ImportJSONLToSQLite(ctx, t.TempDir(), path)
	if err != nil {
		t.Fatalf("promote should rebuild receipt-bound derived indexes without importing a changed source: %v", err)
	}
	if report.SourceCount != 0 || report.ImportedCount != 0 || report.Coverage.State != KnowledgeMemoryCoverageReady {
		t.Fatalf("receipt projection repair should preserve the empty imported-source manifest: %+v", report)
	}
	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	readiness, err = store.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.DatabaseAvailable || !readiness.SchemaReady || !readiness.IndexReady || readiness.Coverage.State != KnowledgeMemoryCoverageReady || readiness.IntegrityState != KnowledgeMemoryIntegrityReady {
		t.Fatalf("receipt-bound V2 projections should be ready after promote: readiness=%+v", readiness)
	}
	if after := snapshotPersonalArchiveReceiptRows(t, ctx, store); !reflect.DeepEqual(after, archiveBefore) {
		t.Fatal("promotion changed protected archive payloads or receipt owner/source/hash bindings")
	}
	if after := snapshotKnowledgeMemoryManifest(t, ctx, store); after != manifestBefore {
		t.Fatal("promotion changed the imported manifest while rebuilding receipt projections")
	}
	var emailTermRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_memory_search_terms WHERE record_type = ? AND token = 'email'`, personalArchiveRecordType).Scan(&emailTermRows); err != nil {
		t.Fatal(err)
	}
	if emailTermRows != entryCount {
		t.Fatalf("V2 summary token was not restored for all receipt-bound diary entries: %d", emailTermRows)
	}
	results, err := store.Search(ctx, appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: "ren"},
		Query: "lateBodyEvidenceDay179", RecordType: personalArchiveRecordType, Limit: 5,
	})
	if err != nil || len(results) != 1 || results[0].RecordID != "diary-179" {
		t.Fatalf("owner search for late diary body token = %#v, err=%v", results, err)
	}
	otherOwner, err := store.Search(ctx, appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: "other"},
		Query: "lateBodyEvidenceDay179", RecordType: personalArchiveRecordType, Limit: 5,
	})
	if err != nil || len(otherOwner) != 0 {
		t.Fatalf("other-owner search should not reveal diary content: results=%#v err=%v", otherOwner, err)
	}
}

type personalArchiveReceiptSnapshot struct {
	PayloadHash        string
	SourceKey          string
	UserID             string
	ActorID            string
	OriginalHash       string
	CurrentPayloadHash string
}

func snapshotPersonalArchiveReceiptRows(t *testing.T, ctx context.Context, store *SQLiteStore) map[string]personalArchiveReceiptSnapshot {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `SELECT archive.entry_id, archive.payload, receipt.source_key, receipt.user_id, receipt.actor_id, receipt.original_payload_hash, receipt.current_payload_hash
		FROM personal_archive AS archive JOIN knowledge_memory_personal_archive_receipts AS receipt ON receipt.entry_id = archive.entry_id ORDER BY archive.entry_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]personalArchiveReceiptSnapshot{}
	for rows.Next() {
		var entryID, payload string
		var snapshot personalArchiveReceiptSnapshot
		if err := rows.Scan(&entryID, &payload, &snapshot.SourceKey, &snapshot.UserID, &snapshot.ActorID, &snapshot.OriginalHash, &snapshot.CurrentPayloadHash); err != nil {
			t.Fatal(err)
		}
		snapshot.PayloadHash = personalArchivePayloadHash([]byte(payload))
		result[entryID] = snapshot
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

type knowledgeMemoryManifestSnapshot struct {
	SourceCount   int
	ImportedCount int
	SourceHash    string
	ImportedHash  string
	CoverageState string
	EligibleCount int
	IndexedCount  int
}

func snapshotKnowledgeMemoryManifest(t *testing.T, ctx context.Context, store *SQLiteStore) knowledgeMemoryManifestSnapshot {
	t.Helper()
	var snapshot knowledgeMemoryManifestSnapshot
	if err := store.db.QueryRowContext(ctx, `SELECT source_count, imported_count, source_hash, imported_hash, coverage_state, eligible_count, indexed_count
		FROM knowledge_memory_import_manifest WHERE manifest_id = 'active'`).Scan(&snapshot.SourceCount, &snapshot.ImportedCount, &snapshot.SourceHash, &snapshot.ImportedHash, &snapshot.CoverageState, &snapshot.EligibleCount, &snapshot.IndexedCount); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func legacyPersonalArchiveTokens(t *testing.T, item domainkm.PersonalArchiveEntry) []string {
	t.Helper()
	bodyTokens, err := appkm.IndexSourceTokens(item.OriginalText)
	if err != nil {
		t.Fatal(err)
	}
	titleTokens, err := appkm.IndexTokens(item.Title)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	for _, token := range append(bodyTokens, titleTokens...) {
		seen[token] = struct{}{}
	}
	tokens := make([]string, 0, len(seen))
	for token := range seen {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

func TestPersonalArchiveReceiptRejectsChangedSourceItemAndOwnerBindings(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	locator := "gmail://ren%40example.com/messages/abcdef0123456789"
	item := luminaDiaryReceiptItem("diary-1", "ren", locator, "original")
	if _, err := store.SavePersonalArchiveEntryWithReceipt(ctx, item, locator, "lumina"); err != nil {
		t.Fatal(err)
	}

	changed := item
	changed.OriginalText = "changed"
	if _, err := store.SavePersonalArchiveEntryWithReceipt(ctx, changed, locator, "lumina"); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("changed source payload error = %v", err)
	}
	changed = item
	changed.EntryID = "diary-other"
	if _, err := store.SavePersonalArchiveEntryWithReceipt(ctx, changed, locator, "lumina"); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("changed item binding error = %v", err)
	}
	changed = item
	changed.UserID = "other"
	if _, err := store.SavePersonalArchiveEntryWithReceipt(ctx, changed, locator, "lumina"); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("changed owner binding error = %v", err)
	}
	changed = item
	changed.SourceRef = "gmail://ren%40example.com/messages/0123456789abcdef"
	if _, err := store.SavePersonalArchiveEntryWithReceipt(ctx, changed, changed.SourceRef, "lumina"); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("changed source key binding error = %v", err)
	}
}

func TestPersonalArchiveReceiptIntegrityDetectsPayloadOrIndexTampering(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	locator := "gmail://ren%40example.com/messages/0123456789abcdef"
	item := luminaDiaryReceiptItem("diary-integrity", "ren", locator, "original evidence")
	if _, err := store.SavePersonalArchiveEntryWithReceipt(ctx, item, locator, "lumina"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.verifyPersonalArchiveReceiptOverlays(ctx); err != nil || !ok {
		t.Fatalf("healthy overlay integrity=%v err=%v", ok, err)
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_memory_search_terms WHERE record_type = ? AND record_id = ?`, personalArchiveRecordType, item.EntryID); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.verifyPersonalArchiveReceiptOverlays(ctx); err != nil || ok {
		t.Fatalf("missing search terms integrity=%v err=%v", ok, err)
	}
}
