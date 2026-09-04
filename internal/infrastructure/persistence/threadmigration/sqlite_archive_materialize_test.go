package threadmigration

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMaterializeArchiveSQLiteRebuildsCanonicalIdentityOnDisposableClone(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_archive_materialize_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	archiveBefore := snapshotSQLiteTables(t, fixture.archive, canonicalArchiveMaterializationTables)
	memoryBefore := snapshotSQLiteQuery(t, fixture.archive, `SELECT speaker, message, meta_json, memory_state, layer, source, created_at, updated_at FROM l1_memory_event_archive WHERE id = 'archive-generic'`)
	sessionBefore := snapshotSQLiteQuery(t, fixture.archive, `SELECT ts_start, ts_end, domain, summary, keywords, embedding, is_novel, created_at FROM session_thread WHERE thread_id = 7`)
	if _, err := destination.Exec(`CREATE TABLE archive_untargeted_sentinel (value TEXT NOT NULL); INSERT INTO archive_untargeted_sentinel(value) VALUES ('keep');`); err != nil {
		t.Fatal(err)
	}
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result})
	if err != nil {
		var typed *ArchiveSQLiteMaterializationError
		t.Fatalf("MaterializeArchiveSQLite() error = %v (typed=%v cause=%v)", err, errors.As(err, &typed), errors.Unwrap(err))
	}
	if receipt.Status != SQLiteArchiveMaterializationStatus || !receipt.OwnerSchemaReconciliationRequired || receipt.IdentityAudit.LegacyNumericRows != 0 {
		t.Fatalf("unexpected receipt = %+v", receipt)
	}
	if receipt.IdentityAudit.CanonicalThreadRows != 4 || receipt.IdentityAudit.OptionalZeroRows != 1 {
		t.Fatalf("unexpected identity audit = %+v", receipt.IdentityAudit)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() error = %v", err)
	}
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	encodedAgain, err := receipt.CanonicalJSON()
	if err != nil || !reflect.DeepEqual(encoded, encodedAgain) {
		t.Fatalf("receipt canonical JSON is not deterministic: %q / %q (err=%v)", encoded, encodedAgain, err)
	}
	if sourceAfter := snapshotSQLiteTables(t, fixture.archive, canonicalArchiveMaterializationTables); !reflect.DeepEqual(archiveBefore, sourceAfter) {
		t.Fatalf("archive source tables changed: before=%v after=%v", archiveBefore, sourceAfter)
	}
	if memoryAfter := snapshotSQLiteQuery(t, destination, `SELECT speaker, message, meta_json, memory_state, layer, source, created_at, updated_at FROM l1_memory_event_archive WHERE id = 'archive-generic'`); !reflect.DeepEqual(memoryBefore, memoryAfter) {
		t.Fatalf("archive memory nonidentity columns changed: before=%v after=%v", memoryBefore, memoryAfter)
	}
	if sessionAfter := snapshotSQLiteQuery(t, destination, `SELECT ts_start, ts_end, domain, summary, keywords, embedding, is_novel, created_at FROM session_thread WHERE thread_id = ?`, stringMappingThreadID(t, result, fixture.genericID, 7)); !reflect.DeepEqual(sessionBefore, sessionAfter) {
		t.Fatalf("session_thread nonidentity/nullable columns changed: before=%v after=%v", sessionBefore, sessionAfter)
	}
	var sentinel string
	if err := destination.QueryRow(`SELECT value FROM archive_untargeted_sentinel`).Scan(&sentinel); err != nil || sentinel != "keep" {
		t.Fatalf("untargeted archive sentinel = %q, err=%v", sentinel, err)
	}
	assertArchiveCanonicalTablesAndNoStages(t, destination)

	var threadID, threadType, sessionID, sessionType, threadKind, kindType, seqType string
	var threadSeq int64
	if err := destination.QueryRow(`SELECT thread_id, typeof(thread_id), session_id, typeof(session_id), thread_seq, typeof(thread_seq), thread_kind, typeof(thread_kind) FROM session_thread`).Scan(&threadID, &threadType, &sessionID, &sessionType, &threadSeq, &seqType, &threadKind, &kindType); err != nil {
		t.Fatal(err)
	}
	if threadType != "text" || sessionType != "text" || seqType != "integer" || kindType != "text" || threadID == "" || sessionID == "" || threadSeq != 7 || threadKind == "" {
		t.Fatalf("canonical session_thread identity = id=%q (%s), session=%q (%s), seq=%d (%s), kind=%q (%s)", threadID, threadType, sessionID, sessionType, threadSeq, seqType, threadKind, kindType)
	}
	var summaryID, summaryType string
	if err := destination.QueryRow(`SELECT thread_id, typeof(thread_id) FROM conversation_thread_summary_receipt`).Scan(&summaryID, &summaryType); err != nil {
		t.Fatal(err)
	}
	if summaryID != threadID || summaryType != "text" {
		t.Fatalf("canonical summary thread_id = %q (%s), session thread = %q", summaryID, summaryType, threadID)
	}
	var zeroSession, zeroID, zeroKind string
	var zeroSeq int64
	if err := destination.QueryRow(`SELECT session_id, thread_id, thread_seq, thread_kind FROM l1_memory_event_archive WHERE id = 'archive-unbound'`).Scan(&zeroSession, &zeroID, &zeroSeq, &zeroKind); err != nil {
		t.Fatal(err)
	}
	if zeroSession == "" || zeroID != "" || zeroSeq != 0 || zeroKind != "" {
		t.Fatalf("optional-zero archive tuple = session=%q thread=%q seq=%d kind=%q", zeroSession, zeroID, zeroSeq, zeroKind)
	}
}

func TestMaterializeArchiveSQLiteRewritesExactNamespacesAndPreservesCustomNamespaces(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	for _, update := range []struct {
		id, namespace string
	}{
		{"archive-generic", "conv:7"},
		{"archive-chatgpt", "conv:conv-identity"},
		{"archive-unbound", "conv:custom"},
	} {
		if _, err := fixture.archive.Exec(`UPDATE l1_memory_event_archive SET namespace = ? WHERE id = ?`, update.namespace, update.id); err != nil {
			t.Fatalf("update archive namespace %s: %v", update.id, err)
		}
	}
	destination := openInventoryTestDB(t, "file:threadmigration_archive_namespace_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result}); err != nil {
		t.Fatal(err)
	}
	canonicalSession, err := canonicalGenericSessionID(fixture.genericID)
	if err != nil {
		t.Fatal(err)
	}
	generic, ok := result.Plan.LookupGeneric(canonicalSession, 7)
	if !ok {
		t.Fatal("generic mapping missing")
	}
	chat, ok := result.Plan.LookupChatGPT(fixture.chatGPTID)
	if !ok {
		t.Fatal("ChatGPT mapping missing")
	}
	assertArchiveNamespace := func(id, want string) {
		t.Helper()
		var got string
		if err := destination.QueryRow(`SELECT namespace FROM l1_memory_event_archive WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("namespace %s: %v", id, err)
		}
		if got != want {
			t.Fatalf("namespace %s = %q, want %q", id, got, want)
		}
	}
	assertArchiveNamespace("archive-generic", "conv:"+string(generic.ThreadID))
	assertArchiveNamespace("archive-chatgpt", "conv:"+string(chat.ThreadID))
	assertArchiveNamespace("archive-unbound", "conv:custom")
}

func TestMaterializeArchiveSQLitePreservesRawTimestampValues(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	if _, err := fixture.archive.Exec(`UPDATE session_thread SET ts_start = 1700000000.5, created_at = 1700000000 WHERE thread_id = 7`); err != nil {
		t.Fatal(err)
	}
	destination := openInventoryTestDB(t, "file:threadmigration_archive_timestamp_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	before := snapshotSQLiteQuery(t, fixture.archive, `SELECT typeof(ts_start), ts_start, typeof(created_at), created_at FROM session_thread WHERE thread_id = 7`)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result}); err != nil {
		t.Fatal(err)
	}
	after := snapshotSQLiteQuery(t, destination, `SELECT typeof(ts_start), ts_start, typeof(created_at), created_at FROM session_thread WHERE thread_id = ?`, stringMappingThreadID(t, result, fixture.genericID, 7))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("raw archive timestamp values changed: before=%v after=%v", before, after)
	}
}

func TestArchiveSQLiteMaterializationReceiptRejectsRehashedCountTamper(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_archive_tamper_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result})
	if err != nil {
		t.Fatal(err)
	}
	tampered := receipt
	tampered.IdentityAudit.CanonicalThreadRows++
	tampered.ReceiptSHA256 = mustArchiveReceiptHash(t, tampered)
	if err := tampered.Validate(); err == nil {
		t.Fatal("rehashed archive identity-count tamper unexpectedly accepted")
	}
}

func TestMaterializeArchiveSQLiteRejectsIdentityDriftWithoutTargetSwap(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_archive_identity_drift_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.archive.Exec(`UPDATE l1_memory_event_archive SET thread_id = 999 WHERE id = 'archive-generic'`); err != nil {
		t.Fatal(err)
	}
	_, err = MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("archive identity drift unexpectedly accepted")
	}
	assertBoundedArchiveMaterializationError(t, err, false)
	assertLegacyArchiveTargetAndNoStages(t, destination)
}

func TestMaterializeArchiveSQLiteRejectsRehashedCountDriftWithoutTargetSwap(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_archive_count_drift_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	for index := range result.Receipt.SurfaceCounts {
		if result.Receipt.SurfaceCounts[index].Surface == sessionThreadSurface {
			result.Receipt.SurfaceCounts[index].Rows++
			result.Receipt.SurfaceCounts[index].References++
		}
	}
	result.Receipt.ReceiptSHA256 = mustSQLiteInventoryReceiptHash(t, result.Receipt)
	if err := result.Validate(); err != nil {
		t.Fatalf("rehashed archive count drift should remain a valid receipt: %v", err)
	}
	_, err = MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("archive count drift unexpectedly accepted")
	}
	assertBoundedArchiveMaterializationError(t, err, false)
	assertLegacyArchiveTargetAndNoStages(t, destination)
}

func TestMaterializeArchiveSQLiteRejectsOrphanSummaryWithoutTargetSwap(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_archive_orphan_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range []*sql.DB{fixture.archive, destination} {
		if _, err := db.Exec(`UPDATE conversation_thread_summary_receipt SET thread_id = 999 WHERE thread_id = 7`); err != nil {
			t.Fatal(err)
		}
	}
	_, err = MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("orphan summary unexpectedly accepted")
	}
	assertBoundedArchiveMaterializationError(t, err, false)
	assertLegacyArchiveTargetAndNoStages(t, destination)
}

func TestMaterializeArchiveSQLiteRejectsReservedStageWithoutMutation(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_archive_stage_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	if _, err := destination.Exec(`CREATE TABLE session_thread_s5_archive_new (sentinel TEXT)`); err != nil {
		t.Fatal(err)
	}
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	_, err = MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("archive reserved stage collision unexpectedly accepted")
	}
	assertBoundedArchiveMaterializationError(t, err, false)
	assertLegacyArchiveTargetAndReservedStage(t, destination)
}

func TestMaterializeArchiveSQLiteRollsBackTransformFailureWithoutTargetSwap(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_archive_copy_failure_destination?mode=memory&cache=shared")
	createLegacyArchiveSchema(t, destination)
	cloneLegacyArchiveRows(t, fixture.archive, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.archive.Exec(`UPDATE l1_memory_event_archive SET thread_id = -1 WHERE id = 'archive-generic'`); err != nil {
		t.Fatal(err)
	}
	_, err = MaterializeArchiveSQLite(context.Background(), ArchiveSQLiteMaterializationInput{Source: fixture.archive, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("archive transform failure unexpectedly accepted")
	}
	assertBoundedArchiveMaterializationError(t, err, false)
	assertLegacyArchiveTargetAndNoStages(t, destination)
}

func cloneLegacyArchiveRows(t *testing.T, source, destination *sql.DB) {
	t.Helper()
	for _, table := range []string{sessionThreadSurface, threadSummaryReceiptSurface, l1MemoryEventArchiveSurface} {
		rows, err := source.Query(`SELECT * FROM ` + quoteSQLiteIdentifier(table))
		if err != nil {
			t.Fatalf("clone archive %s query: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatalf("clone archive %s columns: %v", table, err)
		}
		placeholders := make([]string, len(columns))
		for index := range placeholders {
			placeholders[index] = "?"
		}
		insert := `INSERT INTO ` + quoteSQLiteIdentifier(table) + ` (` + strings.Join(columns, ",") + `) VALUES (` + strings.Join(placeholders, ",") + `)`
		for rows.Next() {
			values := make([]interface{}, len(columns))
			destinations := make([]interface{}, len(columns))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				t.Fatalf("clone archive %s row: %v", table, err)
			}
			if _, err := destination.Exec(insert, values...); err != nil {
				_ = rows.Close()
				t.Fatalf("clone archive %s insert: %v", table, err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("clone archive %s rows: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("clone archive %s close: %v", table, err)
		}
	}
}

func stringMappingThreadID(t *testing.T, result SQLiteInventoryResult, sourceSession string, legacyThreadID int64) string {
	t.Helper()
	canonicalSession, err := canonicalGenericSessionID(sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	mapping, ok := result.Plan.LookupGeneric(canonicalSession, legacyThreadID)
	if !ok {
		t.Fatalf("generic mapping %s/%d missing", sourceSession, legacyThreadID)
	}
	return string(mapping.ThreadID)
}

func mustArchiveReceiptHash(t *testing.T, receipt SQLiteArchiveMaterializationReceipt) string {
	t.Helper()
	hash, err := receipt.ComputeSHA256()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func assertBoundedArchiveMaterializationError(t *testing.T, err error, postCommit bool) {
	t.Helper()
	if err == nil {
		t.Fatal("expected archive materialization error")
	}
	var typed *ArchiveSQLiteMaterializationError
	if !errors.As(err, &typed) {
		t.Fatalf("archive error type = %T, want *ArchiveSQLiteMaterializationError", err)
	}
	if typed.PostCommit != postCommit {
		t.Fatalf("archive post-commit error flag = %v, want %v", typed.PostCommit, postCommit)
	}
	if len(err.Error()) > 256 {
		t.Fatalf("archive bounded error length = %d", len(err.Error()))
	}
}

func assertLegacyArchiveTargetAndNoStages(t *testing.T, db *sql.DB) {
	t.Helper()
	var threadType string
	if err := db.QueryRow(`SELECT typeof(thread_id) FROM session_thread ORDER BY rowid ASC LIMIT 1`).Scan(&threadType); err != nil {
		t.Fatalf("legacy archive target check: %v", err)
	}
	if threadType != "integer" {
		t.Fatalf("legacy archive target thread_id typeof = %q", threadType)
	}
	var stageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_s5_archive_new'`).Scan(&stageCount); err != nil {
		t.Fatal(err)
	}
	if stageCount != 0 {
		t.Fatalf("archive stage objects after rollback = %d", stageCount)
	}
}

func assertLegacyArchiveTargetAndReservedStage(t *testing.T, db *sql.DB) {
	t.Helper()
	var threadType string
	if err := db.QueryRow(`SELECT typeof(thread_id) FROM session_thread ORDER BY rowid ASC LIMIT 1`).Scan(&threadType); err != nil {
		t.Fatalf("legacy archive target check: %v", err)
	}
	if threadType != "integer" {
		t.Fatalf("legacy archive target thread_id typeof = %q", threadType)
	}
	var stageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_s5_archive_new'`).Scan(&stageCount); err != nil {
		t.Fatal(err)
	}
	if stageCount != 1 {
		t.Fatalf("archive reserved stage object count = %d, want 1", stageCount)
	}
}

func assertArchiveCanonicalTablesAndNoStages(t *testing.T, db *sql.DB) {
	t.Helper()
	var stageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_s5_archive_new'`).Scan(&stageCount); err != nil {
		t.Fatal(err)
	}
	if stageCount != 0 {
		t.Fatalf("archive stage objects remain after commit = %d", stageCount)
	}
	for _, table := range canonicalArchiveMaterializationTables {
		var objectType string
		if err := db.QueryRow(`SELECT type FROM sqlite_master WHERE name = ?`, table).Scan(&objectType); err != nil {
			t.Fatalf("canonical archive table %s: %v", table, err)
		}
		if objectType != "table" {
			t.Fatalf("canonical archive object %s = %q", table, objectType)
		}
	}
}
