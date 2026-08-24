package l1sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestChatGPTImportProgressUsesImmutableCountsForLargeExport(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const (
		owner           = "large-machine-owner"
		exportID        = "large-machine-export"
		messageCount    = 50000
		jobCount        = 18917
		rawCount        = 55783
		projectionCount = 54032
	)

	batch := chatGPTRawTestBatch(exportID, 1783, 0, 1, 1)
	batch.Records[0] = chatGPTL3RawTestRecord(exportID, "large-conversation", "large-user-0")
	if _, err := store.ImportChatGPTRawBatch(commonRawTestContextFor(t, "large-raw", owner), "large-raw", owner, owner, batch, true); err != nil {
		t.Fatalf("raw import: %v", err)
	}
	firstEvidenceID := batch.Records[0].EvidenceID
	if _, err := store.db.Exec(`UPDATE l1_memory_event SET meta_json = ? WHERE id = ?`, "malformed-json", firstEvidenceID); err != nil {
		t.Fatalf("malform metadata: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = 'completed' WHERE evidence_event_id = ?`, firstEvidenceID); err != nil {
		t.Fatalf("complete first promotion job: %v", err)
	}

	binding := domainmemory.ChatGPTImportBinding{
		ExportID: exportID, ManifestSHA256: batch.ManifestSHA256, ArtifactSHA256: batch.ArtifactSHA256,
		ArtifactBytes: 1, Format: domainmemory.ChatGPTImportBundleFormat, SchemaVersion: domainmemory.ChatGPTImportRecordSchema,
		ConverterVersion: domainmemory.ChatGPTImportConverterVersion, SourceFileCount: 1, SourceChunkCount: 1,
		MessageCount: messageCount,
	}
	for _, state := range []domainmemory.ChatGPTImportState{
		domainmemory.ChatGPTImportStateValidating,
		domainmemory.ChatGPTImportStateCommitting,
		domainmemory.ChatGPTImportStateCompleted,
	} {
		input := confirmLedgerTestInput("large-ledger", owner, owner, binding, state, 1783, 1)
		input.Counts.RawCount = rawCount
		input.Counts.ProjectionCount = projectionCount
		input.Counts.JobCount = jobCount
		if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
			t.Fatalf("ledger %s: %v", state, err)
		}
	}

	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	messageStmt, err := tx.PrepareContext(context.Background(), `
INSERT INTO l1_memory_event (
 id, namespace, session_id, thread_id, speaker, message, meta_json,
 memory_state, layer, source, created_at, updated_at
) VALUES (?, 'user:large-machine-owner', 'large-session', 1, 'user', 'large', 'malformed-json', 'observed', 'L3', 'chatgpt_export', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < messageCount-1; i++ {
		if _, err := messageStmt.ExecContext(context.Background(), fmt.Sprintf("large-message-%05d", i)); err != nil {
			_ = messageStmt.Close()
			_ = tx.Rollback()
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if err := messageStmt.Close(); err != nil {
		t.Fatal(err)
	}

	jobStmt, err := tx.PrepareContext(context.Background(), `
INSERT INTO l1_profile_promotion_job (
 evidence_event_id, session_id, thread_id, state, attempt_count,
 lease_token, last_error, created_at, updated_at
) VALUES (?, 'large-session', 1, 'completed', 0, '', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	bindingStmt, err := tx.PrepareContext(context.Background(), `
INSERT OR IGNORE INTO l1_chatgpt_profile_promotion_binding (owner_id, export_id, evidence_event_id, created_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)`)
	if err != nil {
		_ = jobStmt.Close()
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := bindingStmt.ExecContext(context.Background(), owner, exportID, firstEvidenceID); err != nil {
		_ = bindingStmt.Close()
		_ = jobStmt.Close()
		_ = tx.Rollback()
		t.Fatalf("first binding: %v", err)
	}
	for i := 0; i < jobCount-1; i++ {
		evidenceID := fmt.Sprintf("large-message-%05d", i)
		if _, err := jobStmt.ExecContext(context.Background(), evidenceID); err != nil {
			_ = bindingStmt.Close()
			_ = jobStmt.Close()
			_ = tx.Rollback()
			t.Fatalf("job %d: %v", i, err)
		}
		if _, err := bindingStmt.ExecContext(context.Background(), owner, exportID, evidenceID); err != nil {
			_ = bindingStmt.Close()
			_ = jobStmt.Close()
			_ = tx.Rollback()
			t.Fatalf("binding %d: %v", i, err)
		}
	}
	if err := jobStmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bindingStmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(chatGPTMachineContext(t, "large-progress", owner), 2*time.Second)
	defer cancel()
	progress, err := store.GetChatGPTImportProgress(ctx, "large-progress", owner, owner, exportID)
	if err != nil {
		t.Fatalf("large progress: %v", err)
	}
	if progress.RawCount != rawCount || progress.ProjectionCount != projectionCount || progress.CompletedProjectionReceiptCount != projectionCount || progress.JobCount != jobCount {
		t.Fatalf("large progress counts=%+v", progress)
	}
	if !progress.TerminalSuccess {
		t.Fatalf("large progress is not terminal success: %+v", progress)
	}
}

func TestChatGPTImportProgressFailsClosedWhenBindingCountDiffers(t *testing.T) {
	store := mustMachineStore(t)
	defer store.Close()
	fixture := appendChatGPTMachineFixtureOnStore(t, store, "binding-mismatch-export", domainmemory.ProfilePromotionCompleted)
	if _, err := store.db.Exec(`INSERT INTO l1_chatgpt_profile_promotion_binding (owner_id, export_id, evidence_event_id, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, "machine-owner", fixture.exportID, "unbound-evidence"); err != nil {
		t.Fatal(err)
	}
	_, err := store.GetChatGPTImportProgress(chatGPTMachineContext(t, "binding-mismatch", "machine-owner"), "binding-mismatch", "machine-owner", "machine-owner", fixture.exportID)
	if err == nil || domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorBlocked {
		t.Fatalf("binding mismatch error=%v code=%q, want blocked", err, domainmemory.ChatGPTImportErrorCodeOf(err))
	}
}

func TestChatGPTImportProgressFailsClosedWhenBoundJobIsMissing(t *testing.T) {
	store := mustMachineStore(t)
	defer store.Close()
	fixture := appendChatGPTMachineFixtureOnStore(t, store, "missing-job-summary", domainmemory.ProfilePromotionCompleted)
	if _, err := store.db.Exec(`DELETE FROM l1_profile_promotion_job WHERE evidence_event_id = ?`, fixture.evidenceID); err != nil {
		t.Fatal(err)
	}
	var missingJobs int
	if err := store.db.QueryRow(`SELECT missing_job_count FROM l1_chatgpt_profile_promotion_summary WHERE owner_id = ? AND export_id = ?`, "machine-owner", fixture.exportID).Scan(&missingJobs); err != nil {
		t.Fatal(err)
	}
	if missingJobs != 1 {
		t.Fatalf("summary missing_job_count=%d, want 1", missingJobs)
	}
	_, err := store.GetChatGPTImportProgress(chatGPTMachineContext(t, "missing-job-summary", "machine-owner"), "missing-job-summary", "machine-owner", "machine-owner", fixture.exportID)
	if err == nil || domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorBlocked {
		t.Fatalf("missing job progress error=%v code=%q, want blocked", err, domainmemory.ChatGPTImportErrorCodeOf(err))
	}
}

func TestChatGPTImportProgressSummaryTracksJobStateTransitions(t *testing.T) {
	store := mustMachineStore(t)
	defer store.Close()
	fixture := appendChatGPTMachineFixtureOnStore(t, store, "state-summary", domainmemory.ProfilePromotionCompleted)

	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, domainmemory.ProfilePromotionFailed, fixture.evidenceID); err != nil {
		t.Fatal(err)
	}
	progress, err := store.GetChatGPTImportProgress(chatGPTMachineContext(t, "state-summary-failed", "machine-owner"), "state-summary-failed", "machine-owner", "machine-owner", fixture.exportID)
	if err != nil {
		t.Fatalf("failed progress: %v", err)
	}
	if progress.PromotionStateCounts.Failed != 1 || progress.PromotionStateCounts.Completed != 0 || progress.TerminalSuccess {
		t.Fatalf("failed summary did not follow job source of truth: %+v", progress)
	}

	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, domainmemory.ProfilePromotionCompleted, fixture.evidenceID); err != nil {
		t.Fatal(err)
	}
	progress, err = store.GetChatGPTImportProgress(chatGPTMachineContext(t, "state-summary-completed", "machine-owner"), "state-summary-completed", "machine-owner", "machine-owner", fixture.exportID)
	if err != nil {
		t.Fatalf("completed progress: %v", err)
	}
	if progress.PromotionStateCounts.Completed != 1 || progress.PromotionStateCounts.Failed != 0 || !progress.TerminalSuccess {
		t.Fatalf("completed summary did not follow job source of truth: %+v", progress)
	}
}

func TestChatGPTImportProgressDoesNotWaitForWriteConnection(t *testing.T) {
	store := mustMachineStore(t)
	defer store.Close()
	fixture := appendChatGPTMachineFixtureOnStore(t, store, "dedicated-read", domainmemory.ProfilePromotionCompleted)

	writeTx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writeTx.Rollback()
	if _, err := writeTx.Exec(`UPDATE l1_profile_promotion_job SET updated_at = updated_at WHERE evidence_event_id = ?`, fixture.evidenceID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(chatGPTMachineContext(t, "dedicated-read", "machine-owner"), 500*time.Millisecond)
	defer cancel()
	progress, err := store.GetChatGPTImportProgress(ctx, "dedicated-read", "machine-owner", "machine-owner", fixture.exportID)
	if err != nil {
		t.Fatalf("progress waited for the write connection: %v", err)
	}
	if !progress.TerminalSuccess {
		t.Fatalf("dedicated read progress=%+v", progress)
	}
}

func TestChatGPTImportProgressDoesNotWaitForLifecycleReadConnection(t *testing.T) {
	store := mustMachineStore(t)
	defer store.Close()
	fixture := appendChatGPTMachineFixtureOnStore(t, store, "dedicated-progress-read", domainmemory.ProfilePromotionCompleted)

	lifecycleReads := make([]*sql.Conn, 0, l1ReadPoolSize)
	for range l1ReadPoolSize {
		conn, err := store.readDB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		lifecycleReads = append(lifecycleReads, conn)
		defer conn.Close()
	}

	ctx, cancel := context.WithTimeout(chatGPTMachineContext(t, "dedicated-progress-read", "machine-owner"), 500*time.Millisecond)
	defer cancel()
	progress, err := store.GetChatGPTImportProgress(ctx, "dedicated-progress-read", "machine-owner", "machine-owner", fixture.exportID)
	if err != nil {
		t.Fatalf("progress waited for the lifecycle read connection: %v", err)
	}
	if !progress.TerminalSuccess {
		t.Fatalf("isolated progress read=%+v", progress)
	}
}

func TestChatGPTImportProgressReportsMissingEvidenceWithoutHidingProgress(t *testing.T) {
	store := mustMachineStore(t)
	defer store.Close()
	fixture := appendChatGPTMachineFixtureOnStore(t, store, "binding-missing-evidence", domainmemory.ProfilePromotionFailed)
	if _, err := store.db.Exec(`DELETE FROM l1_memory_event WHERE id = ?`, fixture.evidenceID); err != nil {
		t.Fatal(err)
	}
	progress, err := store.GetChatGPTImportProgress(chatGPTMachineContext(t, "binding-missing-evidence", "machine-owner"), "binding-missing-evidence", "machine-owner", "machine-owner", fixture.exportID)
	if err != nil {
		t.Fatalf("progress with missing evidence: %v", err)
	}
	if progress.MissingEvidenceCount != 1 || progress.FailedWithEvidenceCount != 0 || progress.TerminalSuccess {
		t.Fatalf("progress with missing evidence=%+v", progress)
	}
}

func TestChatGPTProfilePromotionBindingBackfillRunsOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "l1.db")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, triggerName := range []string{
		"trg_l1_chatgpt_profile_promotion_summary_binding_insert",
		"trg_l1_chatgpt_profile_promotion_summary_job_insert",
		"trg_l1_chatgpt_profile_promotion_summary_job_state_update",
		"trg_l1_chatgpt_profile_promotion_summary_job_delete",
		"trg_l1_chatgpt_profile_promotion_summary_event_delete",
		"trg_l1_chatgpt_profile_promotion_summary_event_insert",
		"trg_l1_chatgpt_profile_promotion_summary_event_validity_update",
	} {
		if _, err := store.db.Exec(`DROP TRIGGER ` + triggerName); err != nil {
			_ = store.Close()
			t.Fatalf("drop summary trigger %s: %v", triggerName, err)
		}
	}
	if _, err := store.db.Exec(`DROP TABLE l1_chatgpt_profile_promotion_summary`); err != nil {
		_ = store.Close()
		t.Fatalf("drop summary table: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_schema_migrations SET version = 1 WHERE migration_name = ?`, chatGPTProfilePromotionBindingMigrationName); err != nil {
		_ = store.Close()
		t.Fatalf("downgrade binding migration marker: %v", err)
	}

	seedChatGPTProfilePromotionBindingBackfillRow(t, store, "owner-a", "export-valid", "evidence-valid", `{"external_source":"chatgpt_export","export_id":"export-valid","original_role":"user","on_current_branch":true}`, "user:owner-a", "chatgpt_export", "L3", "user", "user")
	seedChatGPTProfilePromotionBindingBackfillRow(t, store, "owner-a", "export-malformed", "evidence-malformed", "not-json", "user:owner-a", "chatgpt_export", "L3", "user", "user")
	seedChatGPTProfilePromotionBindingBackfillRow(t, store, "owner-a", "export-conversation-namespace", "evidence-conversation-namespace", `{"external_source":"chatgpt_export","export_id":"export-conversation-namespace","original_role":"user","on_current_branch":true}`, "conv:conversation", "chatgpt_export", "L3", "user", "user")
	seedChatGPTProfilePromotionBindingBackfillRow(t, store, "owner-a", "export-wrong-source", "evidence-wrong-source", `{"external_source":"chatgpt_export","export_id":"export-wrong-source","original_role":"user","on_current_branch":true}`, "user:owner-a", "other_source", "L3", "user", "user")
	for _, row := range [][2]string{{"export-valid", "evidence-valid"}, {"export-conversation-namespace", "evidence-conversation-namespace"}} {
		if _, err := store.db.Exec(`INSERT INTO l1_chatgpt_profile_promotion_binding (owner_id, export_id, evidence_event_id, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, "owner-a", row[0], row[1]); err != nil {
			_ = store.Close()
			t.Fatalf("seed legacy binding %s: %v", row[1], err)
		}
	}

	if err := store.applyChatGPTImportLedgerSchema(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("apply binding schema and backfill: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_profile_promotion_binding`).Scan(&count); err != nil {
		_ = store.Close()
		t.Fatalf("count bindings after backfill: %v", err)
	}
	if count != 2 {
		_ = store.Close()
		t.Fatalf("backfilled binding count=%d, want 2", count)
	}
	var summaryBindingCount, summaryCompletedCount, summaryMissingJobs int
	if err := store.db.QueryRow(`SELECT binding_count, completed_count, missing_job_count FROM l1_chatgpt_profile_promotion_summary WHERE owner_id = ? AND export_id = ?`, "owner-a", "export-valid").Scan(&summaryBindingCount, &summaryCompletedCount, &summaryMissingJobs); err != nil {
		_ = store.Close()
		t.Fatalf("read backfilled summary: %v", err)
	}
	if summaryBindingCount != 1 || summaryCompletedCount != 1 || summaryMissingJobs != 0 {
		_ = store.Close()
		t.Fatalf("backfilled summary=(bindings:%d completed:%d missing_jobs:%d), want (1,1,0)", summaryBindingCount, summaryCompletedCount, summaryMissingJobs)
	}
	var migrationVersion int
	if err := store.db.QueryRow(`SELECT version FROM l1_schema_migrations WHERE migration_name = ?`, chatGPTProfilePromotionBindingMigrationName).Scan(&migrationVersion); err != nil {
		_ = store.Close()
		t.Fatalf("read binding migration version: %v", err)
	}
	if migrationVersion != chatGPTProfilePromotionBindingMigrationVersion {
		_ = store.Close()
		t.Fatalf("binding migration version=%d, want %d", migrationVersion, chatGPTProfilePromotionBindingMigrationVersion)
	}
	var createdAt time.Time
	if err := store.db.QueryRow(`SELECT created_at FROM l1_chatgpt_profile_promotion_binding WHERE evidence_event_id = ?`, "evidence-valid").Scan(&createdAt); err != nil {
		_ = store.Close()
		t.Fatalf("read backfilled binding timestamp: %v", err)
	}
	var markerAt time.Time
	if err := store.db.QueryRow(`SELECT applied_at FROM l1_schema_migrations WHERE migration_name = ?`, chatGPTProfilePromotionBindingMigrationName).Scan(&markerAt); err != nil {
		_ = store.Close()
		t.Fatalf("read binding migration marker: %v", err)
	}

	seedChatGPTProfilePromotionBindingBackfillRow(t, store, "owner-a", "export-late", "evidence-late", `{"external_source":"chatgpt_export","export_id":"export-late","original_role":"user","on_current_branch":true}`, "user:owner-a", "chatgpt_export", "L3", "user", "user")
	if err := store.applyChatGPTImportLedgerSchema(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("reapply binding schema: %v", err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_profile_promotion_binding`).Scan(&count); err != nil {
		_ = store.Close()
		t.Fatalf("count bindings after reapply: %v", err)
	}
	if count != 2 {
		_ = store.Close()
		t.Fatalf("reapply reran backfill, count=%d", count)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	if err := reopened.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_profile_promotion_binding`).Scan(&count); err != nil {
		t.Fatalf("count bindings after reopen: %v", err)
	}
	if count != 2 {
		t.Fatalf("reopen reran backfill, count=%d", count)
	}
	var reopenedCreatedAt, reopenedMarkerAt time.Time
	if err := reopened.db.QueryRow(`SELECT created_at FROM l1_chatgpt_profile_promotion_binding WHERE evidence_event_id = ?`, "evidence-valid").Scan(&reopenedCreatedAt); err != nil {
		t.Fatalf("read binding after reopen: %v", err)
	}
	if err := reopened.db.QueryRow(`SELECT applied_at FROM l1_schema_migrations WHERE migration_name = ?`, chatGPTProfilePromotionBindingMigrationName).Scan(&reopenedMarkerAt); err != nil {
		t.Fatalf("read marker after reopen: %v", err)
	}
	if !reopenedCreatedAt.Equal(createdAt) || !reopenedMarkerAt.Equal(markerAt) {
		t.Fatalf("reopen mutated one-time backfill: created_at %v -> %v, marker_at %v -> %v", createdAt, reopenedCreatedAt, markerAt, reopenedMarkerAt)
	}
}

func TestChatGPTProfilePromotionBindingIsUniqueAndImmutable(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.db.Exec(`INSERT INTO l1_chatgpt_profile_promotion_binding (owner_id, export_id, evidence_event_id, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, "owner-a", "export-a", "evidence-a"); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO l1_chatgpt_profile_promotion_binding (owner_id, export_id, evidence_event_id, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, "owner-b", "export-b", "evidence-a"); err == nil {
		t.Fatal("cross-export evidence membership was accepted")
	}
	if _, err := store.db.Exec(`UPDATE l1_chatgpt_profile_promotion_binding SET export_id = ? WHERE evidence_event_id = ?`, "export-changed", "evidence-a"); err == nil {
		t.Fatal("binding update was accepted")
	}
	if _, err := store.db.Exec(`DELETE FROM l1_chatgpt_profile_promotion_binding WHERE evidence_event_id = ?`, "evidence-a"); err == nil {
		t.Fatal("binding delete was accepted")
	}
}

func seedChatGPTProfilePromotionBindingBackfillRow(t *testing.T, store *L1SQLiteStore, ownerID, exportID, evidenceID, metaJSON, namespace, source, layer, rawRole, eventSpeaker string) {
	t.Helper()
	now := time.Now().UTC()
	manifestID := "manifest-" + evidenceID
	checksum := strings.Repeat("a", 64)
	if _, err := store.db.Exec(`
INSERT INTO l1_raw_source_manifest (
 manifest_id, contract_version, source_type, source_identity, manifest_sha256,
 source_count, asset_count, schema_version, converter_version, owner_id, scope,
 sensitivity, rights, license, provenance, allow_empty, request_id, actor_id,
 intake_status, checkpoint_json, receipt_json, created_at, updated_at
) VALUES (?, 'common-raw-v1', 'chatgpt_export', ?, ?, 1, 0, 'schema-v1', 'converter-v1', ?, ?, 'private', 'owner', 'owner', 'import', 1, ?, ?, 'completed', '{}', '{}', ?, ?)
`, manifestID, exportID, checksum, ownerID, "user:"+ownerID, "request-"+evidenceID, ownerID, now, now); err != nil {
		t.Fatalf("insert manifest %s: %v", evidenceID, err)
	}
	if _, err := store.db.Exec(`
INSERT INTO l1_raw_record (
 raw_record_id, manifest_id, contract_version, source_type, source_identity, source_record_id,
 parent_id, thread_id, owner_id, scope, sensitivity, role, content_type, occurred_at, ingested_at,
 storage_kind, inline_payload, object_ref, content_sha256, content_size, asset_refs_json,
 provenance, rights, license, created_at
) VALUES (?, ?, 'common-raw-v1', 'chatgpt_export', ?, ?, '', '1', ?, ?, 'private', ?, 'text/plain', ?, ?, 'inline', ?, '', ?, 5, '[]', 'import', 'owner', 'owner', ?)
`, "raw-"+evidenceID, manifestID, exportID, evidenceID, ownerID, "user:"+ownerID, rawRole, now, now, []byte("hello"), checksum, now); err != nil {
		t.Fatalf("insert raw record %s: %v", evidenceID, err)
	}
	if _, err := store.db.Exec(`
INSERT INTO l1_memory_event (
 id, namespace, session_id, thread_id, speaker, message, meta_json,
 memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, 1, ?, 'hello', ?, 'observed', ?, ?, ?, ?)
`, evidenceID, namespace, "session-"+evidenceID, eventSpeaker, metaJSON, layer, source, now, now); err != nil {
		t.Fatalf("insert memory event %s: %v", evidenceID, err)
	}
	if _, err := store.db.Exec(`
INSERT INTO l1_profile_promotion_job (
 evidence_event_id, session_id, thread_id, state, attempt_count,
 lease_token, last_error, created_at, updated_at
) VALUES (?, ?, 1, 'completed', 0, '', '', ?, ?)
`, evidenceID, "session-"+evidenceID, now, now); err != nil {
		t.Fatalf("insert promotion job %s: %v", evidenceID, err)
	}
}
