package threadmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMaterializeL1SQLiteRebuildsCanonicalIdentityOnDisposableClone(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	sourceBefore := snapshotSQLiteTables(t, fixture.l1, canonicalL1MaterializationTables)
	memoryBefore := snapshotSQLiteQuery(t, fixture.l1, `SELECT speaker, message, meta_json, memory_state, layer, source, created_at, updated_at FROM l1_memory_event WHERE id = 'event-generic'`)
	profileBefore := snapshotSQLiteQuery(t, fixture.l1, `SELECT state, attempt_count, lease_token, lease_expires_at, next_attempt_at, last_error, created_at, updated_at FROM l1_profile_promotion_job WHERE evidence_event_id = 'event-generic'`)
	outboxBefore := snapshotSQLiteQuery(t, fixture.l1, `SELECT payload_sha256, status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at FROM conversation_turn_outbox WHERE turn_id = 'turn-1' AND target = 'redis_projection'`)
	if _, err := destination.Exec(`CREATE TABLE untargeted_sentinel (value TEXT NOT NULL); INSERT INTO untargeted_sentinel(value) VALUES ('keep');`); err != nil {
		t.Fatal(err)
	}
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatalf("InventorySQLite() error = %v", err)
	}

	receipt, err := MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{
		Source: fixture.l1, Destination: destination, Inventory: result,
	})
	if err != nil {
		t.Fatalf("MaterializeL1SQLite() error = %v (cause=%v)", err, errors.Unwrap(err))
	}
	if receipt.Status != SQLiteL1MaterializationStatus || !receipt.OwnerSchemaReconciliationRequired || receipt.IdentityAudit.LegacyNumericRows != 0 {
		t.Fatalf("unexpected receipt = %+v", receipt)
	}
	if receipt.IdentityAudit.OptionalZeroRows != 6 || receipt.IdentityAudit.CanonicalThreadRows != 8 || receipt.IdentityAudit.CanonicalJSONRows != 2 || receipt.IdentityAudit.CanonicalClosedThreadRows != 2 {
		t.Fatalf("unexpected identity audit = %+v", receipt.IdentityAudit)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() error = %v", err)
	}
	if sourceAfter := snapshotSQLiteTables(t, fixture.l1, canonicalL1MaterializationTables); !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatalf("source L1 tables changed: before=%v after=%v", sourceBefore, sourceAfter)
	}
	if memoryAfter := snapshotSQLiteQuery(t, destination, `SELECT speaker, message, meta_json, memory_state, layer, source, created_at, updated_at FROM l1_memory_event WHERE id = 'event-generic'`); !reflect.DeepEqual(memoryBefore, memoryAfter) {
		t.Fatalf("memory nonidentity columns changed: before=%v after=%v", memoryBefore, memoryAfter)
	}
	if profileAfter := snapshotSQLiteQuery(t, destination, `SELECT state, attempt_count, lease_token, lease_expires_at, next_attempt_at, last_error, created_at, updated_at FROM l1_profile_promotion_job WHERE evidence_event_id = 'event-generic'`); !reflect.DeepEqual(profileBefore, profileAfter) {
		t.Fatalf("profile nonidentity/nullable columns changed: before=%v after=%v", profileBefore, profileAfter)
	}
	if outboxAfter := snapshotSQLiteQuery(t, destination, `SELECT payload_sha256, status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at FROM conversation_turn_outbox WHERE turn_id = 'turn-1' AND target = 'redis_projection'`); !reflect.DeepEqual(outboxBefore, outboxAfter) {
		t.Fatalf("outbox nonidentity/nullable columns changed: before=%v after=%v", outboxBefore, outboxAfter)
	}
	var zeroThreadID, zeroKind, zeroSession string
	var zeroSeq int64
	if err := destination.QueryRow(`SELECT session_id, thread_id, thread_seq, thread_kind FROM l1_memory_event WHERE id = 'event-zero-empty'`).Scan(&zeroSession, &zeroThreadID, &zeroSeq, &zeroKind); err != nil {
		t.Fatal(err)
	}
	if zeroSession != "" || zeroThreadID != "" || zeroSeq != 0 || zeroKind != "" {
		t.Fatalf("empty optional tuple = session=%q thread=%q seq=%d kind=%q", zeroSession, zeroThreadID, zeroSeq, zeroKind)
	}
	var closedID, closedIDType, closedKind, closedKindType, closedSeqType string
	var closedSeq int64
	if err := destination.QueryRow(`SELECT closed_thread_id, typeof(closed_thread_id), closed_thread_seq, typeof(closed_thread_seq), closed_thread_kind, typeof(closed_thread_kind) FROM conversation_turn_receipt WHERE turn_id = 'turn-1'`).Scan(&closedID, &closedIDType, &closedSeq, &closedSeqType, &closedKind, &closedKindType); err != nil {
		t.Fatal(err)
	}
	if closedID == "" || closedIDType != "text" || closedSeq != 8 || closedSeqType != "integer" || closedKind == "" || closedKindType != "text" {
		t.Fatalf("closed canonical tuple = id=%q (%s), seq=%d (%s), kind=%q (%s)", closedID, closedIDType, closedSeq, closedSeqType, closedKind, closedKindType)
	}
	for _, surface := range canonicalL1MaterializationTables {
		var threadType string
		if err := destination.QueryRow(`SELECT typeof(thread_id) FROM ` + quoteSQLiteIdentifier(surface) + ` ORDER BY rowid LIMIT 1`).Scan(&threadType); err != nil {
			if surface == l1ProfilePromotionSurface || surface == activeThreadSurface || surface == turnReceiptSurface || surface == turnOutboxSurface {
				// These fixture tables are nonempty; retain an explicit failure for
				// any unexpected empty-table behavior.
				t.Fatalf("%s identity scan: %v", surface, err)
			}
		} else if threadType != "text" {
			t.Fatalf("%s thread_id typeof = %q, want text", surface, threadType)
		}
	}
	var sentinel string
	if err := destination.QueryRow(`SELECT value FROM untargeted_sentinel`).Scan(&sentinel); err != nil || sentinel != "keep" {
		t.Fatalf("untargeted sentinel = %q, err=%v", sentinel, err)
	}
	var stageCount int
	if err := destination.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_s5_new'`).Scan(&stageCount); err != nil {
		t.Fatal(err)
	}
	if stageCount != 0 {
		t.Fatalf("stage objects remain: %d", stageCount)
	}
}

func TestMaterializeL1SQLiteCanonicalizesLegacyTurnSessionInSQLAndJSON(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	const legacySession = "viewer-user"

	var resultJSON string
	if err := fixture.l1.QueryRow(`SELECT result_json FROM conversation_turn_receipt WHERE turn_id = ?`, fixture.turnID).Scan(&resultJSON); err != nil {
		t.Fatal(err)
	}
	resultJSON = strings.Replace(resultJSON, fixture.turnSession, legacySession, 1)
	if _, err := fixture.l1.Exec(`UPDATE conversation_turn_receipt SET session_id = ?, result_json = ? WHERE turn_id = ?`, legacySession, resultJSON, fixture.turnID); err != nil {
		t.Fatal(err)
	}

	var payloadJSON string
	if err := fixture.l1.QueryRow(`SELECT payload_json FROM conversation_turn_outbox WHERE turn_id = ?`, fixture.turnID).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	payloadJSON = strings.Replace(payloadJSON, fixture.turnSession, legacySession, 1)
	if _, err := fixture.l1.Exec(`UPDATE conversation_turn_outbox SET session_id = ?, payload_json = ? WHERE turn_id = ?`, legacySession, payloadJSON, fixture.turnID); err != nil {
		t.Fatal(err)
	}

	destination := openInventoryTestDB(t, "file:threadmigration_materialize_legacy_turn_session_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatalf("InventorySQLite() rejected legacy turn session: %v", err)
	}
	if _, err := MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result}); err != nil {
		t.Fatalf("MaterializeL1SQLite() error = %v", err)
	}

	wantSession, err := canonicalGenericSessionID(legacySession)
	if err != nil {
		t.Fatal(err)
	}
	var receiptSession, receiptSessionType, canonicalResultJSON string
	if err := destination.QueryRow(`SELECT session_id, typeof(session_id), result_json FROM conversation_turn_receipt WHERE turn_id = ?`, fixture.turnID).Scan(&receiptSession, &receiptSessionType, &canonicalResultJSON); err != nil {
		t.Fatal(err)
	}
	if receiptSession != wantSession || receiptSessionType != "text" {
		t.Fatalf("materialized receipt session = %q (%s), want %q (text)", receiptSession, receiptSessionType, wantSession)
	}
	var canonicalResult struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(canonicalResultJSON), &canonicalResult); err != nil {
		t.Fatal(err)
	}
	if canonicalResult.SessionID != wantSession {
		t.Fatalf("materialized receipt JSON session = %q, want %q", canonicalResult.SessionID, wantSession)
	}

	var outboxSession, outboxSessionType, canonicalPayloadJSON string
	if err := destination.QueryRow(`SELECT session_id, typeof(session_id), payload_json FROM conversation_turn_outbox WHERE turn_id = ? AND target = ?`, fixture.turnID, "redis_projection").Scan(&outboxSession, &outboxSessionType, &canonicalPayloadJSON); err != nil {
		t.Fatal(err)
	}
	if outboxSession != wantSession || outboxSessionType != "text" {
		t.Fatalf("materialized outbox session = %q (%s), want %q (text)", outboxSession, outboxSessionType, wantSession)
	}
	var canonicalPayload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(canonicalPayloadJSON), &canonicalPayload); err != nil {
		t.Fatal(err)
	}
	if canonicalPayload.SessionID != wantSession {
		t.Fatalf("materialized outbox JSON session = %q, want %q", canonicalPayload.SessionID, wantSession)
	}
}

func TestMaterializeL1SQLitePreservesTerminalProfilePromotionOrphans(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	execInventory(t, fixture.l1, `INSERT INTO l1_profile_promotion_job (evidence_event_id, session_id, thread_id, state, attempt_count, lease_token, lease_expires_at, next_attempt_at, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"orphan-materialized-generic", "orphan-materialized-session", 43, "completed", 1, "", nil, nil, "", "2026-01-07", "2026-01-07")
	execInventory(t, fixture.l1, `INSERT INTO l1_profile_promotion_job (evidence_event_id, session_id, thread_id, state, attempt_count, lease_token, lease_expires_at, next_attempt_at, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"orphan-materialized-chatgpt", fixture.chatSession, fixture.chatThread, "failed", 2, "", nil, nil, "unavailable", "2026-01-08", "2026-01-08")
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_terminal_orphans_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatalf("InventorySQLite() error = %v", err)
	}
	if count, found := result.Receipt.SurfaceCount(l1ProfilePromotionSurface); !found || count.PreservedTerminalOrphans != 2 {
		t.Fatalf("preserved terminal orphan count = %+v, found=%v", count, found)
	}
	if _, err := MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result}); err != nil {
		t.Fatalf("MaterializeL1SQLite() error = %v", err)
	}

	for _, test := range []struct {
		evidenceID string
		state      string
		mapping    ThreadMapping
	}{
		{evidenceID: "orphan-materialized-generic", state: "completed"},
		{evidenceID: "orphan-materialized-chatgpt", state: "failed"},
	} {
		mapping, found := result.Plan.LookupBySource(l1ProfilePromotionSurface, test.evidenceID)
		if !found {
			t.Fatalf("materialized orphan %q has no plan mapping", test.evidenceID)
		}
		test.mapping = mapping
		var sessionID, threadID, threadKind, state string
		var threadSeq int64
		if err := destination.QueryRow(`SELECT session_id, thread_id, thread_seq, thread_kind, state FROM l1_profile_promotion_job WHERE evidence_event_id = ?`, test.evidenceID).Scan(&sessionID, &threadID, &threadSeq, &threadKind, &state); err != nil {
			t.Fatalf("read materialized orphan %q: %v", test.evidenceID, err)
		}
		if sessionID != string(test.mapping.SessionID) || threadID != string(test.mapping.ThreadID) || threadSeq != int64(test.mapping.ThreadSeq) || threadKind != string(test.mapping.ThreadKind) || state != test.state {
			t.Fatalf("materialized orphan %q = session=%q thread=%q seq=%d kind=%q state=%q; want mapping=%+v state=%q", test.evidenceID, sessionID, threadID, threadSeq, threadKind, state, test.mapping, test.state)
		}
	}
}

func TestRewriteL1LegacyNamespaceOnlyRewritesExactIdentity(t *testing.T) {
	plan, err := BuildPlan([]LegacyThreadFact{{Surface: l1MemoryEventSurface, RecordKey: "generic", SessionID: "legacy-namespace", LegacyThreadID: 7}, {Surface: l1MemoryEventSurface, RecordKey: "chat", ChatGPTConversationID: "chat-namespace"}})
	if err != nil {
		t.Fatal(err)
	}
	index, err := newSQLiteTransformIndex(plan)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSession, err := canonicalGenericSessionID("legacy-namespace")
	if err != nil {
		t.Fatal(err)
	}
	generic, ok := plan.LookupGeneric(canonicalSession, 7)
	if !ok {
		t.Fatal("generic mapping missing")
	}
	got, err := rewriteL1LegacyNamespace(index, "legacy-namespace", 7, "conv:7")
	if err != nil || got != "conv:"+string(generic.ThreadID) {
		t.Fatalf("generic exact namespace = %q, err=%v", got, err)
	}
	for _, namespace := range []string{"conv:other", "conv:7-extra", "user:owner", "session:legacy"} {
		got, err := rewriteL1LegacyNamespace(index, "legacy-namespace", 7, namespace)
		if err != nil || got != namespace {
			t.Fatalf("custom namespace %q -> %q, err=%v", namespace, got, err)
		}
	}
	chatSession, chatThread, err := chatGPTLegacyTuple("chat-namespace")
	if err != nil {
		t.Fatal(err)
	}
	chat, ok := plan.LookupChatGPT("chat-namespace")
	if !ok {
		t.Fatal("ChatGPT mapping missing")
	}
	got, err = rewriteL1LegacyNamespace(index, chatSession, chatThread, "conv:chat-namespace")
	if err != nil || got != "conv:"+string(chat.ThreadID) {
		t.Fatalf("ChatGPT exact namespace = %q, err=%v", got, err)
	}
}

func TestMaterializeL1SQLiteRewritesExactNamespacesAndPreservesCustomNamespaces(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	for _, update := range []struct {
		table, id, namespace string
	}{
		{l1MemoryEventSurface, "event-generic", "conv:7"},
		{l1MemoryEventSurface, "event-chatgpt", "conv:conv-identity"},
		{l1EventLogSurface, "log-generic", "conv:7"},
		{l1EventLogSurface, "log-chatgpt", "conv:conv-identity"},
		{l1MemoryEventSurface, "event-zero-empty", "conv:custom"},
	} {
		if _, err := fixture.l1.Exec(`UPDATE `+quoteSQLiteIdentifier(update.table)+` SET namespace = ? WHERE id = ?`, update.namespace, update.id); err != nil {
			t.Fatalf("update %s/%s: %v", update.table, update.id, err)
		}
	}
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_namespace_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result})
	if err != nil {
		t.Fatal(err)
	}
	canonicalGenericSession, err := canonicalGenericSessionID(fixture.genericID)
	if err != nil {
		t.Fatal(err)
	}
	generic, ok := result.Plan.LookupGeneric(canonicalGenericSession, 7)
	if !ok {
		t.Fatal("generic mapping missing")
	}
	chat, ok := result.Plan.LookupChatGPT(fixture.chatGPTID)
	if !ok {
		t.Fatal("ChatGPT mapping missing")
	}
	assertNamespace := func(table, id, want string) {
		t.Helper()
		var got string
		if err := destination.QueryRow(`SELECT namespace FROM `+quoteSQLiteIdentifier(table)+` WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("namespace %s/%s: %v", table, id, err)
		}
		if got != want {
			t.Fatalf("namespace %s/%s = %q, want %q", table, id, got, want)
		}
	}
	assertNamespace(l1MemoryEventSurface, "event-generic", "conv:"+string(generic.ThreadID))
	assertNamespace(l1MemoryEventSurface, "event-chatgpt", "conv:"+string(chat.ThreadID))
	assertNamespace(l1EventLogSurface, "log-generic", "conv:"+string(generic.ThreadID))
	assertNamespace(l1EventLogSurface, "log-chatgpt", "conv:"+string(chat.ThreadID))
	assertNamespace(l1MemoryEventSurface, "event-zero-empty", "conv:custom")
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeL1SQLitePreservesRawTimestampValues(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	if _, err := fixture.l1.Exec(`UPDATE l1_memory_event SET created_at = 1700000000, updated_at = 1700000000.5 WHERE id = 'event-generic'`); err != nil {
		t.Fatal(err)
	}
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_timestamp_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	before := snapshotSQLiteQuery(t, fixture.l1, `SELECT typeof(created_at), created_at, typeof(updated_at), updated_at FROM l1_memory_event WHERE id = 'event-generic'`)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result}); err != nil {
		t.Fatal(err)
	}
	after := snapshotSQLiteQuery(t, destination, `SELECT typeof(created_at), created_at, typeof(updated_at), updated_at FROM l1_memory_event WHERE id = 'event-generic'`)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("raw timestamp values changed: before=%v after=%v", before, after)
	}
}

func TestMaterializeL1SQLiteRejectsStaleInventoryWithoutTargetSwap(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_stale_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.l1.Exec(`UPDATE l1_memory_event SET thread_id = 999 WHERE id = 'event-generic'`); err != nil {
		t.Fatal(err)
	}
	_, err = MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("stale inventory unexpectedly accepted")
	}
	assertBoundedL1MaterializationError(t, err, false)
	var tableType string
	if err := destination.QueryRow(`SELECT type FROM sqlite_master WHERE name = 'l1_memory_event'`).Scan(&tableType); err != nil || tableType != "table" {
		t.Fatalf("legacy target after rejected migration = %q, err=%v", tableType, err)
	}
	var stageCount int
	if err := destination.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_s5_new'`).Scan(&stageCount); err != nil {
		t.Fatal(err)
	}
	if stageCount != 0 {
		t.Fatalf("stage objects after rejected migration = %d", stageCount)
	}
}

func TestMaterializeL1SQLiteRejectsRehashedCountDriftBeforeSwap(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_count_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	for index := range result.Receipt.SurfaceCounts {
		if result.Receipt.SurfaceCounts[index].Surface == l1MemoryEventSurface {
			result.Receipt.SurfaceCounts[index].Rows++
			result.Receipt.SurfaceCounts[index].References++
		}
	}
	result.Receipt.ReceiptSHA256 = mustSQLiteInventoryReceiptHash(t, result.Receipt)
	if err := result.Validate(); err != nil {
		t.Fatalf("rehashed count drift should remain a valid receipt: %v", err)
	}
	_, err = MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("count drift unexpectedly accepted")
	}
	assertBoundedL1MaterializationError(t, err, false)
	assertLegacyL1TargetAndNoStages(t, destination)
}

func TestMaterializeL1SQLiteRejectsOrphanOutboxBeforeSwap(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_orphan_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.l1.Exec(`DELETE FROM conversation_turn_receipt WHERE turn_id = ?`, fixture.turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.Exec(`DELETE FROM conversation_turn_receipt WHERE turn_id = ?`, fixture.turnID); err != nil {
		t.Fatal(err)
	}
	_, err = MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("orphan outbox unexpectedly accepted")
	}
	assertBoundedL1MaterializationError(t, err, false)
	assertLegacyL1TargetAndNoStages(t, destination)
}

func TestMaterializeL1SQLiteRollsBackCopyFailureWithoutTargetSwap(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_copy_failure_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	// The legacy contract allows arbitrary last_error text; the canonical
	// outbox CHECK rejects this value after all prior stage inserts, exercising
	// the pre-commit rollback boundary without changing row counts or identity.
	for _, db := range []*sql.DB{fixture.l1, destination} {
		if _, err := db.Exec(`UPDATE conversation_turn_outbox SET last_error = 'unexpected' WHERE turn_id = ?`, fixture.turnID); err != nil {
			t.Fatal(err)
		}
	}
	_, err = MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("canonical CHECK failure unexpectedly accepted")
	}
	assertBoundedL1MaterializationError(t, err, false)
	assertLegacyL1TargetAndNoStages(t, destination)
}

func TestMaterializeL1SQLiteRejectsReservedStageWithoutMutation(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_stage_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	if _, err := destination.Exec(`CREATE TABLE l1_memory_event_s5_new (sentinel TEXT)`); err != nil {
		t.Fatal(err)
	}
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	_, err = MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result})
	if err == nil {
		t.Fatal("reserved stage collision unexpectedly accepted")
	}
	assertBoundedL1MaterializationError(t, err, false)
	assertLegacyL1TargetAndNoStagesExceptReserved(t, destination)
}

func cloneLegacyL1Rows(t *testing.T, source, destination *sql.DB) {
	t.Helper()
	for _, table := range []string{l1MemoryEventSurface, l1EventLogSurface, l1ProfilePromotionSurface, activeThreadSurface, turnReceiptSurface, turnOutboxSurface} {
		rows, err := source.Query(`SELECT * FROM ` + quoteSQLiteIdentifier(table))
		if err != nil {
			t.Fatalf("clone %s query: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatalf("clone %s columns: %v", table, err)
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
				t.Fatalf("clone %s row: %v", table, err)
			}
			if _, err := destination.Exec(insert, values...); err != nil {
				_ = rows.Close()
				t.Fatalf("clone %s insert: %v", table, err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("clone %s rows: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("clone %s close: %v", table, err)
		}
	}
}

func snapshotSQLiteTables(t *testing.T, db *sql.DB, tables []string) map[string][]string {
	t.Helper()
	snapshot := make(map[string][]string, len(tables))
	for _, table := range tables {
		snapshot[table] = snapshotSQLiteQuery(t, db, `SELECT * FROM `+quoteSQLiteIdentifier(table)+` ORDER BY rowid ASC`)
	}
	return snapshot
}

func snapshotSQLiteQuery(t *testing.T, db *sql.DB, query string, args ...interface{}) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("snapshot query: %v", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("snapshot columns: %v", err)
	}
	values := make([]string, 0)
	for rows.Next() {
		rowValues := make([]interface{}, len(columns))
		destinations := make([]interface{}, len(columns))
		for index := range rowValues {
			destinations[index] = &rowValues[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatalf("snapshot row: %v", err)
		}
		fields := make([]string, len(rowValues))
		for index, value := range rowValues {
			switch typed := value.(type) {
			case nil:
				fields[index] = "<nil>"
			case []byte:
				fields[index] = fmt.Sprintf("[]byte:%s", string(typed))
			default:
				fields[index] = fmt.Sprintf("%T:%v", value, value)
			}
		}
		values = append(values, strings.Join(fields, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot rows: %v", err)
	}
	return values
}

func mustSQLiteInventoryReceiptHash(t *testing.T, receipt SQLiteInventoryReceipt) string {
	t.Helper()
	hash, err := receipt.ComputeSHA256()
	if err != nil {
		t.Fatalf("inventory receipt hash: %v", err)
	}
	return hash
}

func assertBoundedL1MaterializationError(t *testing.T, err error, postCommit bool) {
	t.Helper()
	if err == nil {
		t.Fatal("expected materialization error")
	}
	var typed *L1SQLiteMaterializationError
	if !errors.As(err, &typed) {
		t.Fatalf("error type = %T, want *L1SQLiteMaterializationError", err)
	}
	if typed.PostCommit != postCommit {
		t.Fatalf("post-commit error flag = %v, want %v", typed.PostCommit, postCommit)
	}
	if len(err.Error()) > 256 {
		t.Fatalf("bounded error length = %d", len(err.Error()))
	}
}

func assertLegacyL1TargetAndNoStages(t *testing.T, db *sql.DB) {
	t.Helper()
	var threadType string
	if err := db.QueryRow(`SELECT typeof(thread_id) FROM l1_memory_event ORDER BY id ASC LIMIT 1`).Scan(&threadType); err != nil {
		t.Fatalf("legacy target check: %v", err)
	}
	if threadType != "integer" {
		t.Fatalf("legacy target thread_id typeof = %q", threadType)
	}
	var stageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_s5_new'`).Scan(&stageCount); err != nil {
		t.Fatal(err)
	}
	if stageCount != 0 {
		t.Fatalf("stage objects after rollback = %d", stageCount)
	}
}

func assertLegacyL1TargetAndNoStagesExceptReserved(t *testing.T, db *sql.DB) {
	t.Helper()
	var threadType string
	if err := db.QueryRow(`SELECT typeof(thread_id) FROM l1_memory_event ORDER BY id ASC LIMIT 1`).Scan(&threadType); err != nil {
		t.Fatalf("legacy target check: %v", err)
	}
	if threadType != "integer" {
		t.Fatalf("legacy target thread_id typeof = %q", threadType)
	}
	var stageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_s5_new'`).Scan(&stageCount); err != nil {
		t.Fatal(err)
	}
	if stageCount != 1 {
		t.Fatalf("reserved stage object count = %d, want 1", stageCount)
	}
}

func TestSQLiteL1MaterializationReceiptCanonicalizesTableOrder(t *testing.T) {
	receipt := SQLiteL1MaterializationReceipt{
		SchemaVersion: SQLiteL1MaterializationReceiptSchemaVersion, Status: SQLiteL1MaterializationStatus,
		OwnerSchemaReconciliationRequired: true, InventoryReceiptSHA256: strings.Repeat("a", 64), MappingSHA256: strings.Repeat("b", 64),
		TableCounts: []SQLiteL1MaterializationTableCount{{Table: turnOutboxSurface}, {Table: activeThreadSurface}, {Table: l1MemoryEventSurface}, {Table: turnReceiptSurface}, {Table: l1ProfilePromotionSurface}, {Table: l1EventLogSurface}},
	}
	hash, err := receipt.ComputeSHA256()
	if err != nil {
		t.Fatal(err)
	}
	receipt.ReceiptSHA256 = hash
	if err := receipt.Validate(); err == nil {
		t.Fatal("unsorted table counts unexpectedly accepted")
	}
}

func TestAuditCanonicalTurnJSONBindsSQLIdentityTuple(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	index, err := newSQLiteTransformIndex(result.Plan)
	if err != nil {
		t.Fatal(err)
	}
	var legacyJSON string
	if err := fixture.l1.QueryRow(`SELECT result_json FROM conversation_turn_receipt WHERE turn_id = ?`, fixture.turnID).Scan(&legacyJSON); err != nil {
		t.Fatal(err)
	}
	row := legacyReceiptRow{
		turnID: fixture.turnID, payloadHash: strings.Repeat("a", 64), sessionID: fixture.turnSession,
		traceID: fixture.turnID, threadID: 9, closedID: 8, closed: true,
		userMessage: "msg-user", agentMessage: "msg-agent", status: "completed",
	}
	canonicalJSON, err := transformLegacyTurnResult(index, row, legacyJSON)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := resolveSQLiteThreadTuple(index, row.sessionID, row.threadID)
	if err != nil {
		t.Fatal(err)
	}
	expectedClosed, err := resolveSQLiteThreadTuple(index, row.sessionID, row.closedID)
	if err != nil {
		t.Fatal(err)
	}
	if err := auditCanonicalTurnJSON(string(canonicalJSON), expected, expectedClosed); err != nil {
		t.Fatalf("canonical SQL/JSON tuple unexpectedly rejected: %v", err)
	}
	wrong := expected
	wrong.ThreadSeq++
	if err := auditCanonicalTurnJSON(string(canonicalJSON), wrong, expectedClosed); err == nil {
		t.Fatal("valid canonical JSON with a wrong SQL tuple unexpectedly accepted")
	}
}

func TestSQLiteL1MaterializationReceiptRejectsRehashedCountTamper(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	destination := openInventoryTestDB(t, "file:threadmigration_materialize_tamper_destination?mode=memory&cache=shared")
	createLegacyL1Schema(t, destination)
	cloneLegacyL1Rows(t, fixture.l1, destination)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := MaterializeL1SQLite(context.Background(), L1SQLiteMaterializationInput{Source: fixture.l1, Destination: destination, Inventory: result})
	if err != nil {
		t.Fatal(err)
	}
	tampered := receipt
	tampered.IdentityAudit.CanonicalThreadRows++
	tamperedHash, err := tampered.ComputeSHA256()
	if err != nil {
		t.Fatal(err)
	}
	tampered.ReceiptSHA256 = tamperedHash
	if err := tampered.Validate(); err == nil {
		t.Fatal("rehashed identity-count tamper unexpectedly accepted")
	}
}
