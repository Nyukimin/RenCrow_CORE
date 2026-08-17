package l1sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestImportChatGPTRawBatchDryRunIsReadOnlyAndApplyReplays(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := commonRawTestContext(t, "chatgpt-raw-dry-run")
	batch := chatGPTRawTestBatch("export-dry", 1, 0, 1, 1)
	var before struct{ raw, events, jobs, receipts, audit int }
	for query, target := range map[string]*int{
		"SELECT count(*) FROM l1_raw_source_manifest":    &before.raw,
		"SELECT count(*) FROM l1_memory_event":           &before.events,
		"SELECT count(*) FROM l1_profile_promotion_job":  &before.jobs,
		"SELECT count(*) FROM l1_raw_projection_receipt": &before.receipts,
		"SELECT count(*) FROM l1_event_log":              &before.audit,
	} {
		if err := store.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	dry, err := store.ImportChatGPTRawBatch(ctx, "chatgpt-raw-dry-run", "ren", "ren", batch, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Validated != 1 || dry.RawReceipt.ManifestID != "" || dry.Projected != 0 {
		t.Fatalf("unexpected dry result: %+v", dry)
	}
	var after struct{ raw, events, jobs, receipts, audit int }
	for query, target := range map[string]*int{
		"SELECT count(*) FROM l1_raw_source_manifest":    &after.raw,
		"SELECT count(*) FROM l1_memory_event":           &after.events,
		"SELECT count(*) FROM l1_profile_promotion_job":  &after.jobs,
		"SELECT count(*) FROM l1_raw_projection_receipt": &after.receipts,
		"SELECT count(*) FROM l1_event_log":              &after.audit,
	} {
		if err := store.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if before != after {
		t.Fatalf("dry run wrote rows: before=%+v after=%+v", before, after)
	}

	apply, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-raw-apply"), "chatgpt-raw-apply", "ren", "ren", batch, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if apply.RawImported != 1 || apply.Projected != 1 || apply.Existing != 0 || apply.Queued != 1 || apply.RawReceipt.ManifestID == "" {
		t.Fatalf("unexpected apply result: %+v", apply)
	}
	replay, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-raw-replay"), "chatgpt-raw-replay", "ren", "ren", batch, true)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.RawReplayed != 1 || replay.Projected != 0 || replay.Existing != 1 || replay.Queued != 0 {
		t.Fatalf("unexpected replay result: %+v", replay)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 1 {
		t.Fatalf("manifest count=%d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_projection_receipt"); got != 2 {
		t.Fatalf("projection receipt count=%d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.chatgpt_raw_l3_projected'"); got != 1 {
		t.Fatalf("projection audit count=%d", got)
	}
}

func TestImportChatGPTRawBatchBackfillsExistingLegacyRow(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := chatGPTL3RawTestRecord("export-backfill", "conv-backfill", "user-backfill")
	if _, err := store.ImportChatGPTL3Records(context.Background(), []ChatGPTL3ImportRecord{record}, true); err != nil {
		t.Fatal(err)
	}
	batch := ChatGPTRawImportBatch{ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ArtifactSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SourceCount: 1, SchemaVersion: ChatGPTL3ArtifactFormat, ConverterVersion: "chatgpt-export-memory-go/v2", BatchIndex: 0, BatchCount: 1, StartLine: 1, Records: []ChatGPTL3ImportRecord{record}}
	result, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-backfill"), "chatgpt-backfill", "ren", "ren", batch, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Projected != 0 || result.Existing != 1 || result.Queued != 0 {
		t.Fatalf("legacy row was rewritten: %+v", result)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_memory_event WHERE id = '"+record.EvidenceID+"'"); got != 1 {
		t.Fatalf("legacy event count=%d", got)
	}
}

func TestImportChatGPTRawBatchAcceptsStrippedLegacyMetadata(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := chatGPTL3RawTestRecord("export-stripped-meta", "conv-stripped-meta", "user-stripped-meta")
	if _, err := store.ImportChatGPTL3Records(context.Background(), []ChatGPTL3ImportRecord{record}, true); err != nil {
		t.Fatal(err)
	}
	rewriteChatGPTLegacyMeta(t, store, record.EvidenceID, func(meta map[string]interface{}) {
		for _, key := range chatGPTLegacyOptionalMetaKeys {
			delete(meta, key)
		}
		meta["active"] = true
	})
	batch := ChatGPTRawImportBatch{
		ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64),
		SourceCount: 1, SchemaVersion: ChatGPTL3ArtifactFormat, ConverterVersion: "chatgpt-export-memory-go/v2",
		BatchIndex: 0, BatchCount: 1, StartLine: 1, Records: []ChatGPTL3ImportRecord{record},
	}
	dry, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-stripped-dry"), "chatgpt-stripped-dry", "ren", "ren", batch, false)
	if err != nil {
		t.Fatalf("stripped metadata dry-run: %v", err)
	}
	if dry.Validated != 1 || dry.Projected != 0 {
		t.Fatalf("unexpected stripped dry result: %+v", dry)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 0 {
		t.Fatalf("dry-run wrote raw: %d", got)
	}
	result, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-stripped-apply"), "chatgpt-stripped-apply", "ren", "ren", batch, true)
	if err != nil {
		t.Fatalf("stripped metadata apply: %v", err)
	}
	if result.Projected != 0 || result.Existing != 1 {
		t.Fatalf("stripped legacy row was rewritten: %+v", result)
	}
}

func TestImportChatGPTRawBatchRejectsLegacyHashOrIdentityChange(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := chatGPTL3RawTestRecord("export-meta-guard", "conv-meta-guard", "user-meta-guard")
	if _, err := store.ImportChatGPTL3Records(context.Background(), []ChatGPTL3ImportRecord{record}, true); err != nil {
		t.Fatal(err)
	}
	batch := ChatGPTRawImportBatch{
		ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64),
		SourceCount: 1, SchemaVersion: ChatGPTL3ArtifactFormat, ConverterVersion: "chatgpt-export-memory-go/v2",
		BatchIndex: 0, BatchCount: 1, StartLine: 1, Records: []ChatGPTL3ImportRecord{record},
	}

	rewriteChatGPTLegacyMeta(t, store, record.EvidenceID, func(meta map[string]interface{}) {
		meta["content_sha256"] = strings.Repeat("c", 64)
	})
	_, err = store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-hash-change"), "chatgpt-hash-change", "ren", "ren", batch, true)
	if domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorSourceChanged {
		t.Fatalf("hash change code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}

	rewriteChatGPTLegacyMeta(t, store, record.EvidenceID, func(meta map[string]interface{}) {
		meta["content_sha256"] = chatGPTRecordContentHash(record)
		meta["conversation_id"] = "other-conversation"
	})
	_, err = store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-identity-change"), "chatgpt-identity-change", "ren", "ren", batch, true)
	if domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorUnavailable {
		t.Fatalf("identity change code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 0 {
		t.Fatalf("rejected metadata still wrote raw: %d", got)
	}
}

func rewriteChatGPTLegacyMeta(t *testing.T, store *L1SQLiteStore, evidenceID string, mutate func(map[string]interface{})) {
	t.Helper()
	var metaJSON string
	if err := store.db.QueryRow(`SELECT meta_json FROM l1_memory_event WHERE id = ?`, evidenceID).Scan(&metaJSON); err != nil {
		t.Fatalf("read legacy meta: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		t.Fatalf("decode legacy meta: %v", err)
	}
	mutate(meta)
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("encode legacy meta: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_memory_event SET meta_json = ? WHERE id = ?`, string(encoded), evidenceID); err != nil {
		t.Fatalf("rewrite legacy meta: %v", err)
	}
}

func TestImportChatGPTRawBatchPreservesLegacyUserMemoryLifecycleRows(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const owner = "ren"
	record := chatGPTL3RawTestRecord("export-lifecycle", "conv-lifecycle", "user-lifecycle")
	if _, err := store.ImportChatGPTL3Records(context.Background(), []ChatGPTL3ImportRecord{record}, true); err != nil {
		t.Fatal(err)
	}
	create := func(memoryType, statement, state string) *domainmemory.UserMemory {
		t.Helper()
		item, createErr := store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
			UserID: owner, Type: memoryType, Statement: statement, State: state,
			EvidenceEventIDs: []string{record.EvidenceID}, Confidence: 0.9,
			Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor",
		})
		if createErr != nil {
			t.Fatalf("create %s: %v", statement, createErr)
		}
		return item
	}
	candidate := create(domainmemory.UserMemoryTypePreference, "legacy candidate", MemoryStateCandidate)
	confirmed := create(domainmemory.UserMemoryTypeProject, "legacy confirmed", MemoryStateConfirmed)
	forgotten := create(domainmemory.UserMemoryTypeEpisode, "legacy forgotten", MemoryStateConfirmed)
	if _, err := store.ForgetUserMemory(context.Background(), forgotten.ID, "migration fixture"); err != nil {
		t.Fatal(err)
	}
	superseded := create(domainmemory.UserMemoryTypeConstraint, "legacy superseded", MemoryStateConfirmed)
	replacement := create(domainmemory.UserMemoryTypeConstraint, "legacy replacement", MemoryStatePinned)
	if _, err := store.SupersedeUserMemory(context.Background(), superseded.ID, replacement.ID, "migration fixture"); err != nil {
		t.Fatal(err)
	}
	ids := []string{candidate.ID, confirmed.ID, forgotten.ID, superseded.ID, replacement.ID}
	before := snapshotL1MemoryRows(t, store, ids)

	batch := ChatGPTRawImportBatch{
		ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64),
		SourceCount: 1, SchemaVersion: ChatGPTL3ArtifactFormat, ConverterVersion: "chatgpt-export-memory-go/v2",
		BatchIndex: 0, BatchCount: 1, StartLine: 1, Records: []ChatGPTL3ImportRecord{record},
	}
	result, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-lifecycle-backfill"), "chatgpt-lifecycle-backfill", owner, owner, batch, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Projected != 0 || result.Existing != 1 || result.Queued != 0 {
		t.Fatalf("legacy lifecycle backfill result=%+v", result)
	}
	if after := snapshotL1MemoryRows(t, store, ids); !equalStringMap(before, after) {
		t.Fatalf("legacy user-memory lifecycle rows changed:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestImportChatGPTRawBatchReadsContainedObjectPayloadBeforeProjection(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "raw-source")
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatal(err)
	}
	record := chatGPTL3RawTestRecord("export-object", "conv-object", "user-object")
	record.Text = string(bytesOfSize(70*1024, 'x'))
	record.Content = json.RawMessage(`{"parts":["object"]}`)
	batch := ChatGPTRawImportBatch{ManifestSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", ArtifactSHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", SourceCount: 1, SchemaVersion: ChatGPTL3ArtifactFormat, ConverterVersion: "chatgpt-export-memory-go/v2", BatchIndex: 0, BatchCount: 1, StartLine: 1, Records: []ChatGPTL3ImportRecord{record}}
	result, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-object"), "chatgpt-object", "ren", "ren", batch, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Projected != 1 || len(result.RawReceipt.Records) != 1 || result.RawReceipt.Records[0].StorageKind != domainmemory.CommonRawStorageObject {
		t.Fatalf("unexpected object result: %+v", result)
	}
	var payloadBytes []byte
	if err := store.db.QueryRow(`SELECT inline_payload FROM l1_raw_record WHERE raw_record_id = ?`, result.RawReceipt.Records[0].RawRecordID).Scan(&payloadBytes); err != nil || payloadBytes != nil {
		t.Fatalf("object payload unexpectedly inline: len=%d err=%v", len(payloadBytes), err)
	}
	events, err := store.RecentByNamespace(context.Background(), chatGPTConversationNamespace(record.ConversationID), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != record.Text {
		t.Fatalf("projection did not reconstruct object payload: %+v", events)
	}
}

func TestImportChatGPTRawBatchResumesRawOnlyAndOrdersPendingBeforeCompleted(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	batch := chatGPTRawTestBatch("export-resume", 1, 0, 1, 1)
	plan, err := prepareChatGPTRawPlan("ren", batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.IntakeCommonRaw(commonRawTestContext(t, "chatgpt-raw-only"), "chatgpt-raw-only", "ren", "ren", plan.Input); err != nil {
		t.Fatal(err)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_memory_event"); got != 0 {
		t.Fatalf("Raw-only intake projected %d events", got)
	}
	result, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-resume"), "chatgpt-resume", "ren", "ren", batch, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.RawReplayed != 1 || result.Projected != 1 {
		t.Fatalf("unexpected resume result: %+v", result)
	}
	rows, err := store.db.Query(`SELECT status FROM l1_raw_projection_receipt ORDER BY rowid ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var statuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0] != "pending" || statuses[1] != "completed" {
		t.Fatalf("receipt order=%v", statuses)
	}
}

func TestImportChatGPTRawBatchProjectionFailureLeavesRawAndPendingForRetry(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	batch := chatGPTRawTestBatch("export-failure", 1, 0, 1, 1)
	if _, err := store.db.Exec(`CREATE TRIGGER abort_chatgpt_projection BEFORE INSERT ON l1_memory_event WHEN NEW.source = 'chatgpt_export' BEGIN SELECT RAISE(ABORT, 'injected ChatGPT projection failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-failure"), "chatgpt-failure", "ren", "ren", batch, true); err == nil {
		t.Fatal("injected projection failure was swallowed")
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 1 {
		t.Fatalf("Raw manifest count=%d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_projection_receipt WHERE status = 'pending'"); got != 1 {
		t.Fatalf("pending receipt count=%d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_projection_receipt WHERE status = 'completed'"); got != 0 {
		t.Fatalf("completed receipt survived failed projection: %d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_memory_event"); got != 0 {
		t.Fatalf("L3 event survived failed projection: %d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_profile_promotion_job"); got != 0 {
		t.Fatalf("job survived failed projection: %d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.chatgpt_raw_l3_projected'"); got != 0 {
		t.Fatalf("audit survived failed projection: %d", got)
	}
	if _, err := store.db.Exec(`DROP TRIGGER abort_chatgpt_projection`); err != nil {
		t.Fatal(err)
	}
	retry, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-failure-retry"), "chatgpt-failure-retry", "ren", "ren", batch, true)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Projected != 1 || retry.Existing != 0 || retry.Queued != 1 {
		t.Fatalf("retry result=%+v", retry)
	}
}

func TestImportChatGPTRawBatchDryRunRejectsMismatchingExistingL3(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := chatGPTL3RawTestRecord("export-dry-conflict", "conv-dry-conflict", "user-dry-conflict")
	if _, err := store.ImportChatGPTL3Records(context.Background(), []ChatGPTL3ImportRecord{record}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE l1_memory_event SET message = ? WHERE id = ?`, "tampered legacy output", record.EvidenceID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-dry-conflict"), "chatgpt-dry-conflict", "ren", "ren", ChatGPTRawImportBatch{
		ManifestSHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArtifactSHA256:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SourceCount:      1,
		SchemaVersion:    ChatGPTL3ArtifactFormat,
		ConverterVersion: "chatgpt-export-memory-go/v2",
		BatchIndex:       0,
		BatchCount:       1,
		StartLine:        1,
		Records:          []ChatGPTL3ImportRecord{record},
	}, false)
	if err == nil {
		t.Fatal("dry-run accepted a mismatching existing L3 row")
	}
	if code := domainmemory.CommonRawErrorCodeOf(err); code == "" {
		t.Fatalf("dry-run conflict is not typed: %v", err)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"); got != 0 {
		t.Fatalf("dry-run wrote Raw manifests: %d", got)
	}
}

func TestImportChatGPTRawBatchRejectsProjectionOwnedByAnotherOwnerWithoutWrites(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	batch := chatGPTRawTestBatch("export-owner-binding", 1, 0, 1, 1)
	if _, err := store.ImportChatGPTRawBatch(commonRawTestContextFor(t, "chatgpt-owner-a", "owner-a"), "chatgpt-owner-a", "owner-a", "owner-a", batch, true); err != nil {
		t.Fatalf("owner A apply: %v", err)
	}

	counts := func() [6]int {
		return [6]int{
			queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"),
			queryInt(t, store, "SELECT count(*) FROM l1_raw_record"),
			queryInt(t, store, "SELECT count(*) FROM l1_raw_projection_receipt"),
			queryInt(t, store, "SELECT count(*) FROM l1_memory_event"),
			queryInt(t, store, "SELECT count(*) FROM l1_profile_promotion_job"),
			queryInt(t, store, "SELECT count(*) FROM l1_event_log"),
		}
	}
	wantCounts := counts()
	for _, apply := range []bool{false, true} {
		requestID := "chatgpt-owner-b-dry"
		if apply {
			requestID = "chatgpt-owner-b-apply"
		}
		_, err := store.ImportChatGPTRawBatch(commonRawTestContextFor(t, requestID, "owner-b"), requestID, "owner-b", "owner-b", batch, apply)
		if err == nil {
			t.Fatalf("owner B apply=%t reused owner A projection", apply)
		}
		if code := domainmemory.CommonRawErrorCodeOf(err); code != domainmemory.CommonRawErrorUnavailable {
			t.Fatalf("owner B apply=%t error code=%q err=%v", apply, code, err)
		}
		if got := counts(); got != wantCounts {
			t.Fatalf("owner B apply=%t changed rows: got=%v want=%v", apply, got, wantCounts)
		}
		if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest WHERE owner_id = 'owner-b'"); got != 0 {
			t.Fatalf("owner B apply=%t wrote Raw manifest rows: %d", apply, got)
		}
		if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_record WHERE owner_id = 'owner-b'"); got != 0 {
			t.Fatalf("owner B apply=%t wrote Raw record rows: %d", apply, got)
		}
	}
}

func TestCommitChatGPTPendingReceiptsRejectsUnknownHistoryBeforeInsert(t *testing.T) {
	for _, unknownCount := range []int{1, 2} {
		t.Run(fmt.Sprintf("unknown_%d", unknownCount), func(t *testing.T) {
			store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			batch := chatGPTRawTestBatch("export-unknown-history", 1, 0, 1, 1)
			plan, err := prepareChatGPTRawPlan("ren", batch)
			if err != nil {
				t.Fatal(err)
			}
			prepared := plan.Prepared.records[0]
			rawRecordID := domainmemory.DeterministicCommonRawRecordID("ren", plan.Prepared.manifest.Scope, chatGPTRawSourceType, plan.ExportID, prepared.input.SourceRecordID, prepared.hash)
			record := chatGPTRawProjectionRecord{RawRecordID: rawRecordID, ContentHash: prepared.hash, Item: batch.Records[0]}
			for index := 0; index < unknownCount; index++ {
				if _, err := store.db.Exec(`
INSERT INTO l1_raw_projection_receipt (
 projection_receipt_id, projection_type, output_store, output_record_id,
 raw_record_ids_json, revision, input_sha256, output_sha256, status,
 created_at, updated_at, failure_reason
) VALUES (?, ?, ?, ?, ?, ?, ?, '', 'pending', ?, ?, '')`,
					fmt.Sprintf("unknown-receipt-%d", index), ChatGPTRawProjectionType, "conversation_l1", record.Item.EvidenceID,
					chatGPTProjectionRawIDsJSON("unknown-raw-record"), ChatGPTRawProjectionRevision,
					strings.Repeat("a", 64), time.Now().UTC(), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}

			if err := store.commitChatGPTPendingReceipts(context.Background(), []chatGPTRawProjectionRecord{record}); err == nil {
				t.Fatal("unknown projection receipt history was accepted")
			}
			if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_projection_receipt"); got != unknownCount {
				t.Fatalf("receipt count=%d want=%d", got, unknownCount)
			}
			var currentPending int
			if err := store.db.QueryRow(`SELECT count(*) FROM l1_raw_projection_receipt WHERE projection_receipt_id = ?`, chatGPTRawProjectionReceiptID("pending", rawRecordID)).Scan(&currentPending); err != nil {
				t.Fatal(err)
			}
			if currentPending != 0 {
				t.Fatalf("current pending receipt was inserted: %d", currentPending)
			}
		})
	}
}

func TestProjectChatGPTRawRecordsRequiresExactPendingReceipt(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	batch := chatGPTRawTestBatch("export-pending-required", 1, 0, 1, 1)
	plan, err := prepareChatGPTRawPlan("ren", batch)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.IntakeCommonRaw(commonRawTestContext(t, "chatgpt-pending-required-raw"), "chatgpt-pending-required-raw", "ren", "ren", plan.Input)
	if err != nil {
		t.Fatal(err)
	}
	projectionRecords, err := store.readChatGPTRawProjectionRecords(context.Background(), "ren", plan, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.projectChatGPTRawRecords(context.Background(), "ren", "ren", plan, projectionRecords); err == nil {
		t.Fatal("projection accepted a batch without a pending receipt")
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_memory_event"); got != 0 {
		t.Fatalf("projection wrote L3 before pending verification: %d", got)
	}
	if got := queryInt(t, store, "SELECT count(*) FROM l1_raw_projection_receipt"); got != 0 {
		t.Fatalf("projection wrote receipts before pending verification: %d", got)
	}
}

func TestImportChatGPTRawBatchRejectsChangedBindingWithoutWrites(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := chatGPTRawTestBatch("export-binding", 2, 0, 2, 1)
	if _, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-binding-base"), "chatgpt-binding-base", "ren", "ren", base, true); err != nil {
		t.Fatal(err)
	}
	counts := func() [5]int {
		return [5]int{
			queryInt(t, store, "SELECT count(*) FROM l1_raw_source_manifest"),
			queryInt(t, store, "SELECT count(*) FROM l1_raw_record"),
			queryInt(t, store, "SELECT count(*) FROM l1_memory_event"),
			queryInt(t, store, "SELECT count(*) FROM l1_raw_projection_receipt"),
			queryInt(t, store, "SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.chatgpt_raw_l3_projected'"),
		}
	}
	wantCounts := counts()
	mutations := map[string]func(*ChatGPTRawImportBatch){
		"manifest": func(batch *ChatGPTRawImportBatch) {
			batch.ManifestSHA256 = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		},
		"artifact": func(batch *ChatGPTRawImportBatch) {
			batch.ArtifactSHA256 = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		},
		"source_count": func(batch *ChatGPTRawImportBatch) { batch.SourceCount = 3 },
		"schema":       func(batch *ChatGPTRawImportBatch) { batch.SchemaVersion = "rencrow.chatgpt_l3.v2" },
		"converter":    func(batch *ChatGPTRawImportBatch) { batch.ConverterVersion = "chatgpt-export-memory-go/v3" },
		"batch_count":  func(batch *ChatGPTRawImportBatch) { batch.BatchCount = 1 },
	}
	for name, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		_, err := store.ImportChatGPTRawBatch(commonRawTestContext(t, "chatgpt-binding-"+name), "chatgpt-binding-"+name, "ren", "ren", candidate, true)
		if err == nil {
			t.Fatalf("binding mutation %s was accepted", name)
		}
		if name != "schema" && domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorSourceChanged {
			t.Fatalf("binding mutation %s returned code %q: %v", name, domainmemory.CommonRawErrorCodeOf(err), err)
		}
		if got := counts(); got != wantCounts {
			t.Fatalf("binding mutation %s changed rows: got=%v want=%v", name, got, wantCounts)
		}
	}
}

func chatGPTRawTestBatch(exportID string, sourceCount, batchIndex, batchCount, startLine int) ChatGPTRawImportBatch {
	return ChatGPTRawImportBatch{
		ManifestSHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArtifactSHA256:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SourceCount:      sourceCount,
		SchemaVersion:    ChatGPTL3ArtifactFormat,
		ConverterVersion: "chatgpt-export-memory-go/v2",
		BatchIndex:       batchIndex,
		BatchCount:       batchCount,
		StartLine:        startLine,
		Records:          []ChatGPTL3ImportRecord{chatGPTL3RawTestRecord(exportID, "conv-raw", "user-raw")},
	}
}

func chatGPTL3RawTestRecord(exportID, conversationID, messageID string) ChatGPTL3ImportRecord {
	at := time.Date(2026, 8, 16, 12, 34, 56, 0, time.UTC)
	return ChatGPTL3ImportRecord{
		Format: ChatGPTL3ArtifactFormat, ExportID: exportID,
		EvidenceID:     "chatgpt_export:" + conversationID + ":" + messageID,
		ConversationID: conversationID, ConversationTitle: "raw test",
		ConversationCreatedAt: at.Add(-time.Hour), ConversationUpdatedAt: at.Add(time.Hour),
		NodeID: "node-1", ParentNodeID: "parent-1", ChildNodeIDs: []string{"child-1"},
		OnCurrentBranch: true, MessageID: messageID, MessageCreatedAt: at,
		Role: "user", ContentType: "text", Text: "RenのRawテスト",
		Content: json.RawMessage(`{"parts":["RenのRawテスト"]}`), Metadata: json.RawMessage(`{"source":"test"}`),
	}
}

func snapshotL1MemoryRows(t *testing.T, store *L1SQLiteStore, ids []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(ids))
	for _, id := range ids {
		var state, metaJSON, message, source string
		var createdAt, updatedAt time.Time
		if err := store.db.QueryRow(`
SELECT memory_state, meta_json, message, source, created_at, updated_at
FROM l1_memory_event WHERE id = ?`, id).Scan(&state, &metaJSON, &message, &source, &createdAt, &updatedAt); err != nil {
			t.Fatalf("snapshot memory %s: %v", id, err)
		}
		encoded, err := json.Marshal(struct {
			State, MetaJSON, Message, Source string
			CreatedAt, UpdatedAt             time.Time
		}{state, metaJSON, message, source, createdAt, updatedAt})
		if err != nil {
			t.Fatal(err)
		}
		result[id] = string(encoded)
	}
	return result
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func queryInt(t *testing.T, store *L1SQLiteStore, query string) int {
	t.Helper()
	var value int
	if err := store.db.QueryRow(query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
