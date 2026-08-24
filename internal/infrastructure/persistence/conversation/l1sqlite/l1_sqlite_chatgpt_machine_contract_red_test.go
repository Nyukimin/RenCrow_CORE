package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestChatGPTImportProgressIsExportScoped(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, exportID := range []string{"machine-export-a", "machine-export-b"} {
		appendChatGPTMachineFixture(t, store, exportID, domainmemory.ProfilePromotionCompleted)
	}
	progress, err := store.GetChatGPTImportProgress(chatGPTMachineContext(t, "progress-a", "machine-owner"), "progress-a", "machine-owner", "machine-owner", "machine-export-a")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if progress.ExportID != "machine-export-a" || progress.JobCount != 1 || progress.ExpectedJobCount != 1 {
		t.Fatalf("progress=%+v", progress)
	}
}

func TestChatGPTImportProgressUsesProjectionReceiptProgressIndex(t *testing.T) {
	store := mustMachineStore(t)
	defer store.Close()

	var indexCount int
	if err := store.db.QueryRowContext(context.Background(), `
SELECT count(*) FROM sqlite_master
WHERE type = 'index' AND name = 'idx_l1_raw_projection_progress'
`).Scan(&indexCount); err != nil {
		t.Fatalf("find progress receipt index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("progress receipt index count=%d, want 1", indexCount)
	}

	rows, err := store.db.QueryContext(context.Background(), `
EXPLAIN QUERY PLAN
SELECT COUNT(DISTINCT p.output_record_id)
FROM l1_raw_projection_receipt p
JOIN l1_raw_record r ON r.source_record_id = p.output_record_id
WHERE r.owner_id = ? AND r.scope = ? AND r.source_type = ? AND r.source_identity = ?
  AND p.projection_type = ? AND p.output_store = ? AND p.revision = ? AND p.status = 'completed'
`, "machine-owner", "user:machine-owner", chatGPTMachineRawSourceType, "machine-export", chatGPTMachineProjectionType, chatGPTMachineProjectionOutput, chatGPTMachineProjectionRevision)
	if err != nil {
		t.Fatalf("explain progress receipt query: %v", err)
	}
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan progress receipt query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate progress receipt query plan: %v", err)
	}
	if !strings.Contains(strings.Join(details, "\n"), "idx_l1_raw_projection_progress") {
		t.Fatalf("progress receipt query did not use export progress index; plan:\n%s", strings.Join(details, "\n"))
	}
}

func TestChatGPTImportRetryIsExportScopedAndRequiresEvidence(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := appendChatGPTMachineFixture(t, store, "machine-retry-a", domainmemory.ProfilePromotionFailed)
	second := appendChatGPTMachineFixture(t, store, "machine-retry-b", domainmemory.ProfilePromotionFailed)
	result, err := store.RetryFailedChatGPTImportJobsForExport(chatGPTMachineContext(t, "retry-a", "machine-owner"), "retry-a", "machine-owner", "machine-owner", first.exportID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.RequeuedCount != 1 || result.MissingEvidenceCount != 0 {
		t.Fatalf("retry result=%+v", result)
	}
	if got := queryIntArgs(t, store, `SELECT count(*) FROM l1_profile_promotion_job WHERE evidence_event_id = ? AND state = ?`, first.evidenceID, domainmemory.ProfilePromotionPending); got != 1 {
		t.Fatalf("first export pending=%d", got)
	}
	if got := queryIntArgs(t, store, `SELECT count(*) FROM l1_profile_promotion_job WHERE evidence_event_id = ? AND state = ?`, second.evidenceID, domainmemory.ProfilePromotionFailed); got != 1 {
		t.Fatalf("second export failed=%d", got)
	}
	if _, err := store.db.Exec(`DELETE FROM l1_memory_event WHERE id = ?`, first.evidenceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, domainmemory.ProfilePromotionFailed, first.evidenceID); err != nil {
		t.Fatal(err)
	}
	missing, err := store.RetryFailedChatGPTImportJobsForExport(chatGPTMachineContext(t, "retry-missing", "machine-owner"), "retry-missing", "machine-owner", "machine-owner", first.exportID)
	if err != nil {
		t.Fatalf("missing evidence retry: %v", err)
	}
	if missing.RequeuedCount != 0 || missing.MissingEvidenceCount != 1 {
		t.Fatalf("missing evidence retry=%+v", missing)
	}
}

func TestChatGPTImportFinalizeBlocksWithoutChangingCandidates(t *testing.T) {
	fixture := appendChatGPTMachineFixture(t, mustMachineStore(t), "machine-finalize-failed", domainmemory.ProfilePromotionFailed)
	store := fixture.store
	defer store.Close()
	candidateBefore := queryIntArgs(t, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = ? AND memory_state = ?`, "user:machine-owner", MemoryStateCandidate)
	_, err := store.FinalizeChatGPTImport(chatGPTMachineContext(t, "finalize-failed", "machine-owner"), domainmemory.ChatGPTImportFinalizeInput{RequestID: "finalize-failed", OwnerID: "machine-owner", ActorID: "machine-owner", ExportID: fixture.exportID, Apply: true})
	if !errors.Is(err, domainmemory.ErrChatGPTImportBlocked) {
		t.Fatalf("finalize failed error=%v, want blocked", err)
	}
	if got := queryIntArgs(t, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = ? AND memory_state = ?`, "user:machine-owner", MemoryStateCandidate); got != candidateBefore {
		t.Fatalf("candidate count changed before=%d after=%d", candidateBefore, got)
	}
	if got := queryInt(t, store, `SELECT count(*) FROM l1_chatgpt_import_finalize_receipt`); got != 0 {
		t.Fatalf("blocked finalize receipt count=%d", got)
	}
}

func TestChatGPTImportFinalizeCreatesImmutableIdempotentReceiptWithoutCandidatePromotion(t *testing.T) {
	store := mustMachineStore(t)
	fixture := appendChatGPTMachineFixtureOnStore(t, store, "machine-finalize-ready", domainmemory.ProfilePromotionCompleted)
	defer store.Close()
	candidateBefore := queryIntArgs(t, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = ? AND memory_state = ?`, "user:machine-owner", MemoryStateCandidate)
	input := domainmemory.ChatGPTImportFinalizeInput{RequestID: "finalize-ready", OwnerID: "machine-owner", ActorID: "machine-owner", ExportID: fixture.exportID, Apply: true}
	progress, progressErr := store.GetChatGPTImportProgress(chatGPTMachineContext(t, "progress-ready", input.OwnerID), "progress-ready", input.OwnerID, input.ActorID, input.ExportID)
	if progressErr != nil || !progress.TerminalSuccess {
		t.Fatalf("ready progress=%+v err=%v", progress, progressErr)
	}
	first, err := store.FinalizeChatGPTImport(chatGPTMachineContext(t, input.RequestID, input.OwnerID), input)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	replayInput := input
	replayInput.RequestID = "finalize-replay"
	second, err := store.FinalizeChatGPTImport(chatGPTMachineContext(t, replayInput.RequestID, replayInput.OwnerID), replayInput)
	if err != nil {
		t.Fatalf("finalize replay: %v", err)
	}
	if first.Status != domainmemory.ChatGPTImportFinalizeStatusCompleted || first.ReceiptID == "" || first.IdempotentReplay {
		t.Fatalf("first receipt=%+v", first)
	}
	if !second.IdempotentReplay || second.ReceiptID != first.ReceiptID {
		t.Fatalf("replay receipt=%+v first=%+v", second, first)
	}
	if got := queryInt(t, store, `SELECT count(*) FROM l1_chatgpt_import_finalize_receipt`); got != 1 {
		t.Fatalf("receipt count=%d", got)
	}
	if got := queryIntArgs(t, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = ? AND memory_state = ?`, "user:machine-owner", MemoryStateCandidate); got != candidateBefore {
		t.Fatalf("candidate count changed before=%d after=%d", candidateBefore, got)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"owner_id", "actor_id", "raw_record_id", "candidate", "statement", "content", "path"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("receipt exposes %q: %s", forbidden, encoded)
		}
	}
}

func TestChatGPTImportFinalizeDryRunDoesNotWriteReceipt(t *testing.T) {
	store := mustMachineStore(t)
	fixture := appendChatGPTMachineFixtureOnStore(t, store, "machine-finalize-dry-run", domainmemory.ProfilePromotionCompleted)
	defer store.Close()

	result, err := store.FinalizeChatGPTImport(chatGPTMachineContext(t, "finalize-dry-run", "machine-owner"), domainmemory.ChatGPTImportFinalizeInput{
		RequestID: "finalize-dry-run",
		OwnerID:   "machine-owner",
		ActorID:   "machine-owner",
		ExportID:  fixture.exportID,
		Apply:     false,
	})
	if err != nil {
		t.Fatalf("dry-run finalize: %v", err)
	}
	if result.Apply || result.ReceiptID != "" || result.IdempotentReplay {
		t.Fatalf("dry-run result=%+v", result)
	}
	if got := queryInt(t, store, `SELECT count(*) FROM l1_chatgpt_import_finalize_receipt`); got != 0 {
		t.Fatalf("dry-run receipt count=%d", got)
	}
}

type chatGPTMachineFixture struct {
	store      *L1SQLiteStore
	exportID   string
	evidenceID string
}

func appendChatGPTMachineFixture(t *testing.T, store *L1SQLiteStore, exportID, jobState string) chatGPTMachineFixture {
	t.Helper()
	return appendChatGPTMachineFixtureOnStore(t, store, exportID, jobState)
}

func appendChatGPTMachineFixtureOnStore(t *testing.T, store *L1SQLiteStore, exportID, jobState string) chatGPTMachineFixture {
	t.Helper()
	owner := "machine-owner"
	batch := chatGPTRawTestBatch(exportID, 1, 0, 1, 1)
	batch.Records[0] = chatGPTL3RawTestRecord(exportID, "conv-"+exportID, "user-"+exportID)
	if _, err := store.ImportChatGPTRawBatch(commonRawTestContextFor(t, "machine-raw-"+exportID, owner), "machine-raw-"+exportID, owner, owner, batch, true); err != nil {
		t.Fatalf("raw import: %v", err)
	}
	binding := domainmemory.ChatGPTImportBinding{ExportID: exportID, ManifestSHA256: batch.ManifestSHA256, ArtifactSHA256: batch.ArtifactSHA256, ArtifactBytes: 1, Format: domainmemory.ChatGPTImportBundleFormat, SchemaVersion: domainmemory.ChatGPTImportRecordSchema, ConverterVersion: domainmemory.ChatGPTImportConverterVersion, SourceFileCount: 1, SourceChunkCount: 1, MessageCount: 1}
	for _, state := range []domainmemory.ChatGPTImportState{domainmemory.ChatGPTImportStateValidating, domainmemory.ChatGPTImportStateCommitting, domainmemory.ChatGPTImportStateCompleted} {
		input := confirmLedgerTestInput("machine-ledger-"+exportID, owner, owner, binding, state, batch.SourceCount, batch.BatchCount)
		input.Counts.RawCount = 1
		input.Counts.ProjectionCount = 1
		input.Counts.JobCount = 1
		if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
			t.Fatalf("ledger %s: %v", state, err)
		}
	}
	evidenceID := batch.Records[0].EvidenceID
	if _, err := store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{UserID: owner, Type: domainmemory.UserMemoryTypePreference, Statement: "machine candidate", State: MemoryStateCandidate, EvidenceEventIDs: []string{evidenceID}, Confidence: 0.9, Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor"}); err != nil {
		t.Fatalf("candidate: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, jobState, evidenceID); err != nil {
		t.Fatalf("job state: %v", err)
	}
	return chatGPTMachineFixture{store: store, exportID: exportID, evidenceID: evidenceID}
}

func mustMachineStore(t *testing.T) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func chatGPTMachineContext(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	return confirmTestContext(t, requestID, ownerID, ownerID)
}
