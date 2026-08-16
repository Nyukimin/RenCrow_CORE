package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestConfirmChatGPTImportCandidatesDryRunApplyAndReplay(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	owner := "ren"
	exportID := "confirm-export"
	batch := chatGPTRawTestBatch(exportID, 1, 0, 1, 1)
	if _, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "raw-confirm"), "raw-confirm", owner, owner, batch, true); err != nil {
		t.Fatalf("raw import: %v", err)
	}
	binding := domainmemory.ChatGPTImportBinding{
		ExportID:          exportID,
		ManifestSHA256:    batch.ManifestSHA256,
		ArtifactSHA256:    batch.ArtifactSHA256,
		ArtifactBytes:     1,
		Format:            domainmemory.ChatGPTImportBundleFormat,
		SchemaVersion:     domainmemory.ChatGPTImportRecordSchema,
		ConverterVersion:  domainmemory.ChatGPTImportConverterVersion,
		SourceFileCount:   1,
		SourceChunkCount:  1,
		SourceObjectCount: 0,
		MessageCount:      1,
	}
	for _, state := range []domainmemory.ChatGPTImportState{domainmemory.ChatGPTImportStateValidating, domainmemory.ChatGPTImportStateCommitting, domainmemory.ChatGPTImportStateCompleted} {
		input := confirmLedgerTestInput("ledger-confirm", owner, owner, binding, state, batch.SourceCount, batch.BatchCount)
		input.Apply = true
		if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
			t.Fatalf("append ledger %s: %v", state, err)
		}
	}
	evidenceID := batch.Records[0].EvidenceID
	candidate, err := store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
		UserID: owner, Type: domainmemory.UserMemoryTypePreference, Statement: "RenのRawテスト",
		State: MemoryStateCandidate, EvidenceEventIDs: []string{evidenceID}, Confidence: 0.9,
		Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor",
	})
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, domainmemory.ProfilePromotionCompleted, evidenceID); err != nil {
		t.Fatal(err)
	}

	dryInput := domainmemory.ChatGPTImportConfirmInput{RequestID: "confirm-dry", OwnerID: owner, ActorID: owner, ExportID: exportID, Reason: "confirm test", Apply: false}
	before := confirmTableCounts(t, store)
	dry, err := store.ConfirmChatGPTImportCandidates(ledgerTestContext(t, dryInput.RequestID, owner), dryInput)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Matched != 1 || dry.Confirmed != 0 || dry.ProjectionCompleted != 1 || dry.AuditReference != "" {
		t.Fatalf("unexpected dry result: %+v", dry)
	}
	if got := confirmTableCounts(t, store); got != before {
		t.Fatalf("dry run changed durable state: before=%+v after=%+v", before, got)
	}

	applyInput := dryInput
	applyInput.RequestID = "confirm-apply"
	applyInput.Apply = true
	apply, err := store.ConfirmChatGPTImportCandidates(ledgerTestContext(t, applyInput.RequestID, owner), applyInput)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if apply.Matched != 1 || apply.Confirmed != 1 || !strings.HasPrefix(apply.AuditReference, "chatgpt-confirm-audit:") {
		t.Fatalf("unexpected apply result: %+v", apply)
	}
	replay, err := store.ConfirmChatGPTImportCandidates(ledgerTestContext(t, applyInput.RequestID, owner), applyInput)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.IdempotentReplay || replay.Matched != apply.Matched || replay.Confirmed != apply.Confirmed || replay.AuditReference != apply.AuditReference {
		t.Fatalf("unexpected replay result: %+v", replay)
	}
	if got := queryInt(t, store, `SELECT count(*) FROM l1_chatgpt_import_confirm_receipt`); got != 1 {
		t.Fatalf("receipt count=%d", got)
	}
	var auditCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_event_log WHERE event_type = ?`, chatGPTConfirmAuditEventType).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count=%d", auditCount)
	}
	confirmed, err := store.ListUserMemories(context.Background(), owner, MemoryStateConfirmed, false, 10)
	if err != nil || len(confirmed) != 1 || confirmed[0].ID != candidate.ID {
		t.Fatalf("confirmed memories=%+v err=%v", confirmed, err)
	}

	changed := applyInput
	changed.Reason = "different reason"
	if _, err := store.ConfirmChatGPTImportCandidates(ledgerTestContext(t, changed.RequestID, owner), changed); !errors.Is(err, domainmemory.ErrChatGPTImportConflict) {
		t.Fatalf("changed request error=%v", err)
	}
}

func TestConfirmChatGPTImportCandidatesResultDoesNotExposeStorageFields(t *testing.T) {
	result := domainmemory.ChatGPTImportConfirmResult{RequestID: "request", ExportID: "export", AuditReference: "opaque"}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"owner_id", "actor_id", "raw_record_id", "candidate_id", "statement", "content", "path"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("result contains %q: %s", forbidden, encoded)
		}
	}
}

func TestConfirmChatGPTImportCandidatesClosedStorePersistenceIsInternal(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	input := domainmemory.ChatGPTImportConfirmInput{
		RequestID: "closed-confirm", OwnerID: "closed-owner", ActorID: "closed-owner",
		ExportID: "closed-export", Reason: "closed store test", Apply: false,
	}
	if _, err := store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, input.OwnerID, input.ActorID), input); domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorInternal {
		t.Fatalf("closed confirm code=%q err=%v, want internal", domainmemory.ChatGPTImportErrorCodeOf(err), err)
	}
}

func TestConfirmChatGPTImportCandidatesNilStoreIsUnavailable(t *testing.T) {
	var store *L1SQLiteStore
	input := domainmemory.ChatGPTImportConfirmInput{
		RequestID: "nil-confirm", OwnerID: "nil-owner", ActorID: "nil-owner",
		ExportID: "nil-export", Reason: "nil store test", Apply: false,
	}
	if _, err := store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, input.OwnerID, input.ActorID), input); domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorUnavailable {
		t.Fatalf("nil confirm code=%q err=%v, want unavailable", domainmemory.ChatGPTImportErrorCodeOf(err), err)
	}
}

func TestConfirmChatGPTImportCandidatesSeparatesSourceAndMessageCounts(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, exportID := "owner-counts", "export-counts"
	batch := chatGPTRawTestBatch(exportID, 5, 0, 3, 1)
	if _, err := store.ImportChatGPTRawBatch(commonRawTestContextFor(t, "raw-counts", owner), "raw-counts", owner, owner, batch, true); err != nil {
		t.Fatalf("raw import: %v", err)
	}
	binding := domainmemory.ChatGPTImportBinding{
		ExportID: exportID, ManifestSHA256: batch.ManifestSHA256, ArtifactSHA256: batch.ArtifactSHA256,
		ArtifactBytes: 1, Format: domainmemory.ChatGPTImportBundleFormat, SchemaVersion: domainmemory.ChatGPTImportRecordSchema,
		ConverterVersion: domainmemory.ChatGPTImportConverterVersion, SourceFileCount: 1, SourceChunkCount: 1, MessageCount: 1,
	}
	for _, state := range []domainmemory.ChatGPTImportState{domainmemory.ChatGPTImportStateValidating, domainmemory.ChatGPTImportStateCommitting, domainmemory.ChatGPTImportStateCompleted} {
		input := confirmLedgerTestInput("ledger-counts", owner, owner, binding, state, batch.SourceCount, batch.BatchCount)
		if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
			t.Fatalf("append ledger %s: %v", state, err)
		}
	}
	evidenceID := batch.Records[0].EvidenceID
	if _, err := store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
		UserID: owner, Type: domainmemory.UserMemoryTypePreference, Statement: "count binding candidate",
		State: MemoryStateCandidate, EvidenceEventIDs: []string{evidenceID}, Confidence: 0.9,
		Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor",
	}); err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, domainmemory.ProfilePromotionCompleted, evidenceID); err != nil {
		t.Fatal(err)
	}
	input := domainmemory.ChatGPTImportConfirmInput{RequestID: "confirm-counts", OwnerID: owner, ActorID: owner, ExportID: exportID, Reason: "count binding", Apply: true}
	result, err := store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, owner, owner), input)
	if err != nil {
		t.Fatalf("confirm source/message count-separated chain: %v", err)
	}
	if result.Matched != 1 || result.Confirmed != 1 {
		t.Fatalf("unexpected count-separated result: %+v", result)
	}
}

func TestConfirmChatGPTImportCandidatesPagesCandidateValidationAndApply(t *testing.T) {
	fixture := newChatGPTConfirmFixture(t, "owner-paged", "export-paged", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
	const candidateCount = chatGPTConfirmCandidatePageSize + 5
	for index := 2; index <= candidateCount; index++ {
		if _, err := fixture.store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
			UserID: fixture.owner, Type: domainmemory.UserMemoryTypePreference,
			Statement:        "paged candidate " + strconv.Itoa(index),
			State:            MemoryStateCandidate,
			EvidenceEventIDs: []string{fixture.evidenceID},
			Confidence:       0.9,
			Sensitivity:      "normal",
			Scope:            "all_personas",
			Source:           "profile_extractor",
		}); err != nil {
			t.Fatalf("create paged candidate %d: %v", index, err)
		}
	}

	tx, err := fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := loadChatGPTConfirmCandidatePage(context.Background(), tx, fixture.owner, 0)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if len(first) != chatGPTConfirmCandidatePageSize {
		_ = tx.Rollback()
		t.Fatalf("first candidate page=%d want=%d", len(first), chatGPTConfirmCandidatePageSize)
	}
	second, err := loadChatGPTConfirmCandidatePage(context.Background(), tx, fixture.owner, first[len(first)-1].RowID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if len(second) != 5 {
		_ = tx.Rollback()
		t.Fatalf("second candidate page=%d want=5", len(second))
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	input := confirmFixtureInput(fixture, "confirm-paged", true)
	result, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, fixture.owner, fixture.owner), input)
	if err != nil {
		t.Fatalf("paged confirm: %v", err)
	}
	if result.Matched != candidateCount || result.Confirmed != candidateCount {
		t.Fatalf("paged confirm result=%+v want matched=confirmed=%d", result, candidateCount)
	}
}

type chatGPTConfirmCounts struct {
	Candidates int
	Confirmed  int
	Receipts   int
	Audits     int
}

func confirmTableCounts(t *testing.T, store *L1SQLiteStore) chatGPTConfirmCounts {
	t.Helper()
	var result chatGPTConfirmCounts
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_memory_event WHERE source = 'profile_extractor' AND memory_state = ?`, MemoryStateCandidate).Scan(&result.Candidates); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_memory_event WHERE source = 'profile_extractor' AND memory_state = ?`, MemoryStateConfirmed).Scan(&result.Confirmed); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_confirm_receipt`).Scan(&result.Receipts); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_event_log WHERE event_type = ?`, chatGPTConfirmAuditEventType).Scan(&result.Audits); err != nil {
		t.Fatal(err)
	}
	return result
}

func queryIntArgs(t *testing.T, store *L1SQLiteStore, query string, args ...interface{}) int {
	t.Helper()
	var value int
	if err := store.db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

type chatGPTConfirmFixture struct {
	store       *L1SQLiteStore
	owner       string
	exportID    string
	evidenceID  string
	rawRecordID string
	candidateID string
}

// newChatGPTConfirmFixture builds the smallest owner-bound Raw, ledger, L3,
// promotion-job and candidate chain. Each test gets a fresh database so it
// can mutate one provenance edge without affecting the other cases.
func newChatGPTConfirmFixture(t *testing.T, owner, exportID string, latestState domainmemory.ChatGPTImportState, apply bool, jobState string) chatGPTConfirmFixture {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	batch := chatGPTRawTestBatch(exportID, 1, 0, 1, 1)
	rawResult, err := store.ImportChatGPTRawBatch(commonRawTestContextFor(t, "raw-fixture-"+owner+"-"+exportID, owner), "raw-fixture-"+owner+"-"+exportID, owner, owner, batch, true)
	if err != nil {
		t.Fatalf("raw import: %v", err)
	}
	binding := domainmemory.ChatGPTImportBinding{
		ExportID:          exportID,
		ManifestSHA256:    batch.ManifestSHA256,
		ArtifactSHA256:    batch.ArtifactSHA256,
		ArtifactBytes:     1,
		Format:            domainmemory.ChatGPTImportBundleFormat,
		SchemaVersion:     domainmemory.ChatGPTImportRecordSchema,
		ConverterVersion:  domainmemory.ChatGPTImportConverterVersion,
		SourceFileCount:   1,
		SourceChunkCount:  1,
		SourceObjectCount: 0,
		MessageCount:      1,
	}
	ledgerRequestID := "ledger-fixture-" + owner + "-" + exportID
	sequence := []domainmemory.ChatGPTImportState{domainmemory.ChatGPTImportStateValidating}
	if latestState == domainmemory.ChatGPTImportStateCommitting || latestState == domainmemory.ChatGPTImportStateCompleted || latestState == domainmemory.ChatGPTImportStateRejected || latestState == domainmemory.ChatGPTImportStateBlocked {
		sequence = append(sequence, domainmemory.ChatGPTImportStateCommitting)
	}
	if latestState != domainmemory.ChatGPTImportStateValidating && latestState != domainmemory.ChatGPTImportStateCommitting {
		sequence = append(sequence, latestState)
	}
	for _, state := range sequence {
		input := confirmLedgerTestInput(ledgerRequestID, owner, owner, binding, state, batch.SourceCount, batch.BatchCount)
		input.Apply = apply
		if state == domainmemory.ChatGPTImportStateRejected || state == domainmemory.ChatGPTImportStateBlocked {
			input.ErrorCode = "fixture_failure"
			input.FailureReason = "fixture terminal state"
		}
		if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, ledgerRequestID, owner), input); err != nil {
			t.Fatalf("append ledger %s: %v", state, err)
		}
	}
	evidenceID := batch.Records[0].EvidenceID
	candidate, err := store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
		UserID: owner, Type: domainmemory.UserMemoryTypePreference, Statement: "fixture candidate",
		State: MemoryStateCandidate, EvidenceEventIDs: []string{evidenceID}, Confidence: 0.9,
		Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor",
	})
	if err != nil {
		t.Fatalf("create fixture candidate: %v", err)
	}
	if jobState != "" {
		if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, jobState, evidenceID); err != nil {
			t.Fatalf("set fixture job state: %v", err)
		}
	}
	if len(rawResult.RawRecordIDs) != 1 {
		t.Fatalf("raw record count=%d", len(rawResult.RawRecordIDs))
	}
	return chatGPTConfirmFixture{store: store, owner: owner, exportID: exportID, evidenceID: evidenceID, rawRecordID: rawResult.RawRecordIDs[0], candidateID: candidate.ID}
}

func confirmFixtureInput(f chatGPTConfirmFixture, requestID string, apply bool) domainmemory.ChatGPTImportConfirmInput {
	return domainmemory.ChatGPTImportConfirmInput{RequestID: requestID, OwnerID: f.owner, ActorID: f.owner, ExportID: f.exportID, Reason: "fixture confirmation", Apply: apply}
}

func confirmLedgerTestInput(requestID, ownerID, actorID string, binding domainmemory.ChatGPTImportBinding, state domainmemory.ChatGPTImportState, sourceCount, batchCount int) domainmemory.ChatGPTImportEventInput {
	return domainmemory.ChatGPTImportEventInput{
		RequestID: requestID,
		OwnerID:   ownerID,
		ActorID:   actorID,
		Binding:   binding,
		Apply:     true,
		State:     state,
		Counts: domainmemory.ChatGPTImportCounts{
			SourceCount: sourceCount, FileCount: binding.SourceFileCount, ChunkCount: binding.SourceChunkCount,
			ObjectCount: binding.SourceObjectCount, MessageCount: binding.MessageCount, BatchCount: batchCount,
			RawCount: sourceCount + binding.MessageCount,
		},
		AuditReference: "confirm-audit-" + requestID,
	}
}

func confirmTestContext(t *testing.T, requestID, ownerID, actorID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, actorID, ownerID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatalf("new confirmation scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func assertConfirmUnchanged(t *testing.T, f chatGPTConfirmFixture, before chatGPTConfirmCounts, input domainmemory.ChatGPTImportConfirmInput, wantCode domainmemory.ChatGPTImportErrorCode) {
	t.Helper()
	_, err := f.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, input.OwnerID, input.ActorID), input)
	if got := domainmemory.ChatGPTImportErrorCodeOf(err); got != wantCode {
		t.Fatalf("error code=%q want=%q err=%v", got, wantCode, err)
	}
	if after := confirmTableCounts(t, f.store); after != before {
		t.Fatalf("failed confirmation changed state: before=%+v after=%+v", before, after)
	}
}

func TestConfirmChatGPTImportCandidatesOwnerIsolationAndLatestLedgerGate(t *testing.T) {
	t.Run("another owner is indistinguishable", func(t *testing.T) {
		fixture := newChatGPTConfirmFixture(t, "owner-a", "shared-export", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
		before := confirmTableCounts(t, fixture.store)
		unknownOwner := domainmemory.ChatGPTImportConfirmInput{RequestID: "cross-owner", OwnerID: "owner-b", ActorID: "owner-b", ExportID: fixture.exportID, Reason: "fixture confirmation", Apply: true}
		assertConfirmUnchanged(t, fixture, before, unknownOwner, domainmemory.ChatGPTImportErrorNotFound)
		if _, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, "cross-owner-forbidden", "owner-b", "owner-b"), confirmFixtureInput(fixture, "cross-owner-forbidden", true)); domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorForbidden {
			t.Fatalf("cross-owner scope mismatch error=%v", err)
		}
		if got := queryInt(t, fixture.store, `SELECT count(*) FROM l1_memory_event WHERE id = '`+fixture.candidateID+`' AND memory_state = 'candidate'`); got != 1 {
			t.Fatalf("owner-a candidate was changed: count=%d", got)
		}
	})

	cases := []struct {
		name       string
		state      domainmemory.ChatGPTImportState
		apply      bool
		wantReason string
	}{
		{name: "latest dry-run completed", state: domainmemory.ChatGPTImportStateCompleted, apply: false},
		{name: "latest active validating", state: domainmemory.ChatGPTImportStateValidating, apply: true},
		{name: "latest active committing", state: domainmemory.ChatGPTImportStateCommitting, apply: true},
		{name: "latest rejected", state: domainmemory.ChatGPTImportStateRejected, apply: false},
		{name: "latest blocked", state: domainmemory.ChatGPTImportStateBlocked, apply: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newChatGPTConfirmFixture(t, "owner-ledger", "export-"+tc.name, tc.state, tc.apply, domainmemory.ProfilePromotionCompleted)
			before := confirmTableCounts(t, fixture.store)
			input := confirmFixtureInput(fixture, "latest-gate-"+tc.name, true)
			assertConfirmUnchanged(t, fixture, before, input, domainmemory.ChatGPTImportErrorConflict)
		})
	}
}

func TestConfirmChatGPTImportCandidatesProjectionJobGateAndDryRunCounts(t *testing.T) {
	cases := []struct {
		name  string
		state string
	}{
		{name: "pending", state: domainmemory.ProfilePromotionPending},
		{name: "running", state: domainmemory.ProfilePromotionRunning},
		{name: "retry wait", state: domainmemory.ProfilePromotionRetryWait},
		{name: "failed", state: domainmemory.ProfilePromotionFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newChatGPTConfirmFixture(t, "owner-job", "export-"+tc.name, domainmemory.ChatGPTImportStateCompleted, true, tc.state)
			dryInput := confirmFixtureInput(fixture, "job-dry-"+tc.name, false)
			before := confirmTableCounts(t, fixture.store)
			dry, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, dryInput.RequestID, fixture.owner, fixture.owner), dryInput)
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}
			if dry.Matched != 1 || dry.Confirmed != 0 || dry.ProjectionCompleted != 0 {
				t.Fatalf("dry result=%+v", dry)
			}
			switch tc.state {
			case domainmemory.ProfilePromotionPending:
				if dry.ProjectionPending != 1 {
					t.Fatalf("pending count=%+v", dry)
				}
			case domainmemory.ProfilePromotionRunning:
				if dry.ProjectionRunning != 1 {
					t.Fatalf("running count=%+v", dry)
				}
			case domainmemory.ProfilePromotionRetryWait:
				if dry.ProjectionRetryWait != 1 {
					t.Fatalf("retry count=%+v", dry)
				}
			case domainmemory.ProfilePromotionFailed:
				if dry.ProjectionFailed != 1 {
					t.Fatalf("failed count=%+v", dry)
				}
			}
			if got := confirmTableCounts(t, fixture.store); got != before {
				t.Fatalf("dry run changed state: before=%+v after=%+v", before, got)
			}
			applyInput := confirmFixtureInput(fixture, "job-apply-"+tc.name, true)
			assertConfirmUnchanged(t, fixture, before, applyInput, domainmemory.ChatGPTImportErrorConflict)
		})
	}

	t.Run("missing job", func(t *testing.T) {
		fixture := newChatGPTConfirmFixture(t, "owner-job-missing", "export-missing-job", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
		if _, err := fixture.store.db.Exec(`DELETE FROM l1_profile_promotion_job WHERE evidence_event_id = ?`, fixture.evidenceID); err != nil {
			t.Fatal(err)
		}
		before := confirmTableCounts(t, fixture.store)
		assertConfirmUnchanged(t, fixture, before, confirmFixtureInput(fixture, "missing-job", true), domainmemory.ChatGPTImportErrorInternal)
	})

	t.Run("invalid job binding", func(t *testing.T) {
		fixture := newChatGPTConfirmFixture(t, "owner-job-invalid", "export-invalid-job", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
		if _, err := fixture.store.db.Exec(`UPDATE l1_profile_promotion_job SET session_id = ? WHERE evidence_event_id = ?`, "foreign-session", fixture.evidenceID); err != nil {
			t.Fatal(err)
		}
		before := confirmTableCounts(t, fixture.store)
		assertConfirmUnchanged(t, fixture, before, confirmFixtureInput(fixture, "invalid-job", true), domainmemory.ChatGPTImportErrorInternal)
	})
}

func TestConfirmChatGPTImportCandidatesStrictEligibilityAndOwnerRows(t *testing.T) {
	fixture := newChatGPTConfirmFixture(t, "owner-eligible", "export-eligible", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
	create := func(userID, source, sensitivity string, evidence []string) string {
		t.Helper()
		item, err := fixture.store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
			UserID: userID, Type: domainmemory.UserMemoryTypePreference, Statement: "ineligible fixture candidate",
			State: MemoryStateCandidate, EvidenceEventIDs: evidence, Confidence: 0.8,
			Sensitivity: sensitivity, Scope: "all_personas", Source: source,
		})
		if err != nil {
			t.Fatalf("create candidate %s: %v", source, err)
		}
		return item.ID
	}
	mixedID := create(fixture.owner, "profile_extractor", "normal", []string{fixture.evidenceID, "chatgpt_export:foreign:evidence"})
	inactiveID := create(fixture.owner, "profile_extractor", "normal", []string{fixture.evidenceID})
	sensitiveID := create(fixture.owner, "profile_extractor", "sensitive", []string{fixture.evidenceID})
	otherSourceID := create(fixture.owner, "operator:test", "normal", []string{fixture.evidenceID})
	otherOwnerID := create("owner-other", "profile_extractor", "normal", []string{fixture.evidenceID})
	if _, err := fixture.store.db.Exec(`UPDATE l1_memory_event SET meta_json = json_set(meta_json, '$.active', json('false')) WHERE id = ?`, inactiveID); err != nil {
		t.Fatal(err)
	}
	before := confirmTableCounts(t, fixture.store)
	input := confirmFixtureInput(fixture, "strict-eligibility", true)
	result, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, fixture.owner, fixture.owner), input)
	if err != nil {
		t.Fatalf("strict eligibility apply: %v", err)
	}
	if result.Matched != 1 || result.Confirmed != 1 {
		t.Fatalf("strict eligibility result=%+v", result)
	}
	for _, id := range []string{mixedID, inactiveID, sensitiveID, otherSourceID, otherOwnerID} {
		if got := queryIntArgs(t, fixture.store, `SELECT count(*) FROM l1_memory_event WHERE id = ? AND memory_state = 'candidate'`, id); got != 1 {
			t.Fatalf("ineligible candidate %s was confirmed: count=%d", id, got)
		}
	}
	if got := queryIntArgs(t, fixture.store, `SELECT count(*) FROM l1_memory_event WHERE id = ? AND memory_state = 'confirmed'`, fixture.candidateID); got != 1 {
		t.Fatalf("valid candidate was not confirmed: count=%d", got)
	}
	if got := confirmTableCounts(t, fixture.store); got.Candidates != before.Candidates-1 || got.Confirmed != before.Confirmed+1 {
		t.Fatalf("unexpected candidate transition: before=%+v after=%+v", before, got)
	}
}

func TestConfirmChatGPTImportCandidatesPreservesExistingTerminalLifecycleRows(t *testing.T) {
	fixture := newChatGPTConfirmFixture(t, "owner-lifecycle", "export-lifecycle-confirm", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
	create := func(memoryType, statement, state string) *domainmemory.UserMemory {
		t.Helper()
		item, err := fixture.store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
			UserID: fixture.owner, Type: memoryType, Statement: statement, State: state,
			EvidenceEventIDs: []string{fixture.evidenceID}, Confidence: 0.9,
			Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor",
		})
		if err != nil {
			t.Fatalf("create %s: %v", statement, err)
		}
		return item
	}
	confirmed := create(domainmemory.UserMemoryTypeProject, "existing confirmed", MemoryStateConfirmed)
	forgotten := create(domainmemory.UserMemoryTypeEpisode, "existing forgotten", MemoryStateConfirmed)
	if _, err := fixture.store.ForgetUserMemory(context.Background(), forgotten.ID, "migration fixture"); err != nil {
		t.Fatal(err)
	}
	superseded := create(domainmemory.UserMemoryTypeConstraint, "existing superseded", MemoryStateConfirmed)
	replacement := create(domainmemory.UserMemoryTypeConstraint, "existing replacement", MemoryStatePinned)
	if _, err := fixture.store.SupersedeUserMemory(context.Background(), superseded.ID, replacement.ID, "migration fixture"); err != nil {
		t.Fatal(err)
	}
	ids := []string{confirmed.ID, forgotten.ID, superseded.ID, replacement.ID}
	before := snapshotL1MemoryRows(t, fixture.store, ids)

	input := confirmFixtureInput(fixture, "confirm-preserve-lifecycle", true)
	result, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, fixture.owner, fixture.owner), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 1 || result.Confirmed != 1 {
		t.Fatalf("confirmation result=%+v", result)
	}
	if after := snapshotL1MemoryRows(t, fixture.store, ids); !equalStringMap(before, after) {
		t.Fatalf("terminal lifecycle rows changed:\nbefore=%v\nafter=%v", before, after)
	}
	if got := queryIntArgs(t, fixture.store, `SELECT count(*) FROM l1_memory_event WHERE id = ? AND memory_state = 'confirmed'`, fixture.candidateID); got != 1 {
		t.Fatalf("eligible candidate was not confirmed: count=%d", got)
	}
}

func TestConfirmChatGPTImportCandidatesRejectsOwnerlessLegacyPromotion(t *testing.T) {
	fixture := newChatGPTConfirmFixture(t, "owner-ownerless", "export-ownerless", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
	legacyID := "chatgpt_export:legacy-conversation:legacy-message"
	if _, err := fixture.store.db.Exec(`
INSERT INTO l1_memory_event (
 id, namespace, session_id, thread_id, speaker, message, meta_json,
 memory_state, layer, source, created_at, updated_at
) VALUES (?, 'conv:legacy-conversation', 'legacy-conversation', 1, 'user', 'legacy',
 ?, 'observed', 'L3', 'chatgpt_export', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, legacyID, `{"external_source":"chatgpt_export","export_id":"export-ownerless","original_role":"user","on_current_branch":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`
INSERT INTO l1_profile_promotion_job (
 evidence_event_id, session_id, thread_id, state, attempt_count, lease_token, last_error, created_at, updated_at
) VALUES (?, 'legacy-conversation', 1, 'pending', 0, '', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, legacyID); err != nil {
		t.Fatal(err)
	}
	before := confirmTableCounts(t, fixture.store)
	assertConfirmUnchanged(t, fixture, before, confirmFixtureInput(fixture, "ownerless-legacy", true), domainmemory.ChatGPTImportErrorInternal)
}

func TestConfirmChatGPTImportCandidatesRejectsChangedCompletedLedgerBinding(t *testing.T) {
	fixture := newChatGPTConfirmFixture(t, "owner-ledger-binding", "export-ledger-binding", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
	if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_chatgpt_import_event_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE l1_chatgpt_import_event SET manifest_sha256 = ? WHERE owner_id = ? AND export_id = ? AND state = 'completed'`, strings.Repeat("c", 64), fixture.owner, fixture.exportID); err != nil {
		t.Fatal(err)
	}
	before := confirmTableCounts(t, fixture.store)
	assertConfirmUnchanged(t, fixture, before, confirmFixtureInput(fixture, "changed-ledger-binding", true), domainmemory.ChatGPTImportErrorInternal)
}

func TestConfirmChatGPTImportCandidatesCorruptProvenanceRollsBack(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, fixture chatGPTConfirmFixture)
	}{
		{name: "malformed raw manifest receipt", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_raw_manifest_immutable_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`UPDATE l1_raw_source_manifest SET receipt_json = '{}' WHERE manifest_id = (SELECT manifest_id FROM l1_raw_record WHERE raw_record_id = ?)`, fixture.rawRecordID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "noncanonical raw manifest receipt", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_raw_manifest_immutable_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`UPDATE l1_raw_source_manifest SET receipt_json = json_set(receipt_json, '$.unexpected', 1) WHERE manifest_id = (SELECT manifest_id FROM l1_raw_record WHERE raw_record_id = ?)`, fixture.rawRecordID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed projection receipt", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_raw_projection_immutable_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`UPDATE l1_raw_projection_receipt SET raw_record_ids_json = 'not-json' WHERE projection_receipt_id = ?`, chatGPTRawProjectionReceiptID("completed", fixture.rawRecordID)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt inline raw payload", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_raw_record_immutable_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`UPDATE l1_raw_record SET inline_payload = ? WHERE raw_record_id = ?`, []byte("corrupt raw payload"), fixture.rawRecordID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing pending projection receipt", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_raw_projection_immutable_delete`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`DELETE FROM l1_raw_projection_receipt WHERE projection_receipt_id = ?`, chatGPTRawProjectionReceiptID("pending", fixture.rawRecordID)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing completed projection receipt", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_raw_projection_immutable_delete`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`DELETE FROM l1_raw_projection_receipt WHERE projection_receipt_id = ?`, chatGPTRawProjectionReceiptID("completed", fixture.rawRecordID)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed L3 metadata", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`UPDATE l1_memory_event SET meta_json = 'not-json' WHERE id = ?`, fixture.evidenceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "L3 evidence binding changed", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`UPDATE l1_memory_event SET session_id = ? WHERE id = ?`, "foreign-session", fixture.evidenceID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "L3 message changed with receipt recomputed", mutate: func(t *testing.T, fixture chatGPTConfirmFixture) {
			t.Helper()
			if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_raw_projection_immutable_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`UPDATE l1_memory_event SET message = ? WHERE id = ?`, "tampered L3 message", fixture.evidenceID); err != nil {
				t.Fatal(err)
			}
			event, err := loadChatGPTEventQueryer(context.Background(), fixture.store.db, fixture.evidenceID)
			if err != nil {
				t.Fatal(err)
			}
			outputHash, err := CanonicalL1MemoryEventSHA256(*event)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`UPDATE l1_raw_projection_receipt SET output_sha256 = ? WHERE projection_receipt_id = ?`, outputHash, chatGPTRawProjectionReceiptID("completed", fixture.rawRecordID)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newChatGPTConfirmFixture(t, "owner-corrupt", "export-"+strings.ReplaceAll(tc.name, " ", "-"), domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
			tc.mutate(t, fixture)
			before := confirmTableCounts(t, fixture.store)
			wantCode := domainmemory.ChatGPTImportErrorInternal
			if tc.name == "L3 evidence binding changed" {
				wantCode = domainmemory.ChatGPTImportErrorSourceChanged
			}
			assertConfirmUnchanged(t, fixture, before, confirmFixtureInput(fixture, "corrupt-"+strings.ReplaceAll(tc.name, " ", "-"), true), wantCode)
		})
	}
}

func TestConfirmChatGPTImportCandidatesRejectsCorruptObjectPayload(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "raw-source")
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatal(err)
	}
	owner, exportID := "owner-object", "export-object-confirm"
	batch := chatGPTRawTestBatch(exportID, 1, 0, 1, 1)
	batch.Records[0].Text = strings.Repeat("object payload ", 6000)
	rawResult, err := store.ImportChatGPTRawBatch(commonRawTestContextFor(t, "raw-object-confirm", owner), "raw-object-confirm", owner, owner, batch, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rawResult.RawReceipt.Records) != 1 || rawResult.RawReceipt.Records[0].StorageKind != domainmemory.CommonRawStorageObject {
		t.Fatalf("raw object fixture=%+v", rawResult.RawReceipt.Records)
	}
	binding := domainmemory.ChatGPTImportBinding{
		ExportID: exportID, ManifestSHA256: batch.ManifestSHA256, ArtifactSHA256: batch.ArtifactSHA256,
		ArtifactBytes: 1, Format: domainmemory.ChatGPTImportBundleFormat, SchemaVersion: domainmemory.ChatGPTImportRecordSchema,
		ConverterVersion: domainmemory.ChatGPTImportConverterVersion, SourceFileCount: 1, SourceChunkCount: 1, MessageCount: 1,
	}
	ledgerRequestID := "ledger-object-confirm"
	for _, state := range []domainmemory.ChatGPTImportState{domainmemory.ChatGPTImportStateValidating, domainmemory.ChatGPTImportStateCommitting, domainmemory.ChatGPTImportStateCompleted} {
		ledgerInput := confirmLedgerTestInput(ledgerRequestID, owner, owner, binding, state, batch.SourceCount, batch.BatchCount)
		ledgerInput.Apply = true
		if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, ledgerRequestID, owner), ledgerInput); err != nil {
			t.Fatalf("append object ledger %s: %v", state, err)
		}
	}
	evidenceID := batch.Records[0].EvidenceID
	candidate, err := store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
		UserID: owner, Type: domainmemory.UserMemoryTypePreference, Statement: "object candidate", State: MemoryStateCandidate,
		EvidenceEventIDs: []string{evidenceID}, Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, domainmemory.ProfilePromotionCompleted, evidenceID); err != nil {
		t.Fatal(err)
	}
	fixture := chatGPTConfirmFixture{store: store, owner: owner, exportID: exportID, evidenceID: evidenceID, rawRecordID: rawResult.RawReceipt.Records[0].RawRecordID, candidateID: candidate.ID}
	before := confirmTableCounts(t, store)
	store.rawSourceRoot = ""
	assertConfirmUnchanged(t, fixture, before, confirmFixtureInput(fixture, "object-root-unconfigured", true), domainmemory.ChatGPTImportErrorUnavailable)
	store.rawSourceRoot = root
	objectPath := filepath.Join(root, filepath.FromSlash(rawResult.RawReceipt.Records[0].ObjectRef))
	if err := os.WriteFile(objectPath, []byte("corrupt object bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertConfirmUnchanged(t, fixture, before, confirmFixtureInput(fixture, "object-corrupt", true), domainmemory.ChatGPTImportErrorInternal)
}

func TestConfirmChatGPTImportCandidatesAtomicFailureAndZeroMatchReceipt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		triggerSQL string
	}{
		{name: "audit failure", triggerSQL: `CREATE TRIGGER confirm_audit_failure BEFORE INSERT ON l1_event_log WHEN NEW.event_type = 'memory.chatgpt_import_candidates_confirmed' BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`},
		{name: "receipt failure", triggerSQL: `CREATE TRIGGER confirm_receipt_failure BEFORE INSERT ON l1_chatgpt_import_confirm_receipt BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newChatGPTConfirmFixture(t, "owner-atomic", "export-"+strings.ReplaceAll(tc.name, " ", "-"), domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
			if _, err := fixture.store.db.Exec(tc.triggerSQL); err != nil {
				t.Fatal(err)
			}
			before := confirmTableCounts(t, fixture.store)
			assertConfirmUnchanged(t, fixture, before, confirmFixtureInput(fixture, "atomic-"+strings.ReplaceAll(tc.name, " ", "-"), true), domainmemory.ChatGPTImportErrorInternal)
			if got := queryIntArgs(t, fixture.store, `SELECT count(*) FROM l1_memory_event WHERE id = ? AND memory_state = 'candidate'`, fixture.candidateID); got != 1 {
				t.Fatalf("candidate update was not rolled back: count=%d", got)
			}
		})
	}

	t.Run("zero matched still writes audit and receipt", func(t *testing.T) {
		fixture := newChatGPTConfirmFixture(t, "owner-zero", "export-zero", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
		if _, err := fixture.store.db.Exec(`UPDATE l1_memory_event SET meta_json = json_set(meta_json, '$.evidence_event_ids', json('["foreign-evidence"]')) WHERE id = ?`, fixture.candidateID); err != nil {
			t.Fatal(err)
		}
		input := confirmFixtureInput(fixture, "zero-match", true)
		result, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, fixture.owner, fixture.owner), input)
		if err != nil {
			t.Fatalf("zero-match apply: %v", err)
		}
		if result.Matched != 0 || result.Confirmed != 0 || result.AuditReference == "" {
			t.Fatalf("zero-match result=%+v", result)
		}
		if got := queryInt(t, fixture.store, `SELECT count(*) FROM l1_chatgpt_import_confirm_receipt`); got != 1 {
			t.Fatalf("zero-match receipt count=%d", got)
		}
		if got := queryInt(t, fixture.store, `SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.chatgpt_import_candidates_confirmed'`); got != 1 {
			t.Fatalf("zero-match audit count=%d", got)
		}
		if got := queryIntArgs(t, fixture.store, `SELECT count(*) FROM l1_memory_event WHERE id = ? AND memory_state = 'candidate'`, fixture.candidateID); got != 1 {
			t.Fatalf("zero-match candidate state=%d", got)
		}
	})
}

func TestConfirmChatGPTImportCandidatesReplayIntegrityAndReceiptImmutability(t *testing.T) {
	fixture := newChatGPTConfirmFixture(t, "owner-replay", "export-replay", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
	input := confirmFixtureInput(fixture, "replay-request", true)
	first, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, fixture.owner, fixture.owner), input)
	if err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	if first.Confirmed != 1 {
		t.Fatalf("initial result=%+v", first)
	}
	before := confirmTableCounts(t, fixture.store)
	changed := []struct {
		name       string
		input      domainmemory.ChatGPTImportConfirmInput
		wantCode   domainmemory.ChatGPTImportErrorCode
		contextOwn string
		contextAct string
	}{
		{name: "actor", input: func() domainmemory.ChatGPTImportConfirmInput { v := input; v.ActorID = "another-actor"; return v }(), wantCode: domainmemory.ChatGPTImportErrorForbidden, contextOwn: fixture.owner, contextAct: fixture.owner},
		{name: "export", input: func() domainmemory.ChatGPTImportConfirmInput { v := input; v.ExportID = "another-export"; return v }(), wantCode: domainmemory.ChatGPTImportErrorConflict, contextOwn: fixture.owner, contextAct: fixture.owner},
		{name: "apply", input: func() domainmemory.ChatGPTImportConfirmInput { v := input; v.Apply = false; return v }(), wantCode: domainmemory.ChatGPTImportErrorConflict, contextOwn: fixture.owner, contextAct: fixture.owner},
		{name: "reason", input: func() domainmemory.ChatGPTImportConfirmInput { v := input; v.Reason = "changed reason"; return v }(), wantCode: domainmemory.ChatGPTImportErrorConflict, contextOwn: fixture.owner, contextAct: fixture.owner},
	}
	for _, tc := range changed {
		t.Run("changed "+tc.name, func(t *testing.T) {
			_, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, tc.input.RequestID, tc.contextOwn, tc.contextAct), tc.input)
			if got := domainmemory.ChatGPTImportErrorCodeOf(err); got != tc.wantCode {
				t.Fatalf("changed %s error code=%q want=%q err=%v", tc.name, got, tc.wantCode, err)
			}
			if after := confirmTableCounts(t, fixture.store); after != before {
				t.Fatalf("changed %s modified state: before=%+v after=%+v", tc.name, before, after)
			}
		})
	}

	t.Run("stored result corruption is internal", func(t *testing.T) {
		if _, err := fixture.store.db.Exec(`DROP TRIGGER trg_l1_chatgpt_confirm_receipt_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.db.Exec(`UPDATE l1_chatgpt_import_confirm_receipt SET result_json = '{}' WHERE request_id = ?`, input.RequestID); err != nil {
			t.Fatal(err)
		}
		assertConfirmUnchanged(t, fixture, before, input, domainmemory.ChatGPTImportErrorInternal)
	})

	t.Run("noncanonical stored result is internal", func(t *testing.T) {
		other := newChatGPTConfirmFixture(t, "owner-result-canonical", "export-result-canonical", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
		otherInput := confirmFixtureInput(other, "result-canonical-request", true)
		if _, err := other.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, otherInput.RequestID, other.owner, other.owner), otherInput); err != nil {
			t.Fatal(err)
		}
		otherBefore := confirmTableCounts(t, other.store)
		if _, err := other.store.db.Exec(`DROP TRIGGER trg_l1_chatgpt_confirm_receipt_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := other.store.db.Exec(`UPDATE l1_chatgpt_import_confirm_receipt SET result_json = ' ' || result_json WHERE request_id = ?`, otherInput.RequestID); err != nil {
			t.Fatal(err)
		}
		assertConfirmUnchanged(t, other, otherBefore, otherInput, domainmemory.ChatGPTImportErrorInternal)
	})

	t.Run("immutable update and delete triggers", func(t *testing.T) {
		other := newChatGPTConfirmFixture(t, "owner-immutable", "export-immutable", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
		otherInput := confirmFixtureInput(other, "immutable-request", true)
		if _, err := other.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, otherInput.RequestID, other.owner, other.owner), otherInput); err != nil {
			t.Fatal(err)
		}
		if _, err := other.store.db.Exec(`UPDATE l1_chatgpt_import_confirm_receipt SET reason_hash = '' WHERE request_id = ?`, otherInput.RequestID); err == nil {
			t.Fatal("receipt update unexpectedly succeeded")
		}
		if _, err := other.store.db.Exec(`DELETE FROM l1_chatgpt_import_confirm_receipt WHERE request_id = ?`, otherInput.RequestID); err == nil {
			t.Fatal("receipt delete unexpectedly succeeded")
		}
	})
}
