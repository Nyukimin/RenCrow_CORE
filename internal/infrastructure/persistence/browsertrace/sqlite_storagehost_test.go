package browsertrace

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSQLiteStoreCreateArtifactWithPublicationIntentForStorageHostStoresReceipt(t *testing.T) {
	store := newSupersedeStore(t)
	ctx := intentTestContext(t)
	item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	intent := intentFixtureEnvelope(t, item)
	identity := BrowserTraceStorageHostOperationIdentity{
		OpID:             "browsertrace-create-op-1",
		Operation:        BrowserTraceCreateAPIArtifactWithPublicationIntentOperation,
		PayloadSHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		WriterGeneration: 17,
	}
	effect := BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostArtifactCreationEffect, ID: string(item.ArtifactID)}

	if err := store.CreateAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, item, intent); err != nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntentForStorageHostOperation() error = %v", err)
	}
	if got := mustFindSupersedeArtifact(t, store, item.ArtifactID); got.ArtifactID != item.ArtifactID {
		t.Fatalf("created artifact = %#v, want %s", got, item.ArtifactID)
	}
	intents := mustDrainPublicationIntents(t, store, 10)
	if len(intents) != 1 || intents[0].EventID != intent.EventID {
		t.Fatalf("creation outbox = %#v, want the original event %s", intents, intent.EventID)
	}

	result, found, err := store.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect)
	if err != nil {
		t.Fatalf("LookupBrowserTraceStorageHostOperationReceipt() error = %v", err)
	}
	if !found || string(result) != "null" {
		t.Fatalf("owner receipt = %q, found=%v, want exact void result null", result, found)
	}
	if err := domaintrace.ValidateAPIArtifactPublicationIntent(item, intent); err != nil {
		t.Fatalf("fixture creation fact became invalid: %v", err)
	}
}

func TestNewSQLiteStoreRejectsCompositeStorageHostReceiptPrimaryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-trace-composite-receipt.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	_, err = db.Exec(`CREATE TABLE browser_trace_storage_host_operation_receipt (
		op_id TEXT NOT NULL,
		operation TEXT NOT NULL,
		payload_sha256 TEXT NOT NULL,
		writer_generation INTEGER NOT NULL,
		effect_kind TEXT NOT NULL,
		effect_id TEXT NOT NULL,
		result_json TEXT NOT NULL,
		result_sha256 TEXT NOT NULL,
		effect_json TEXT NOT NULL,
		effect_sha256 TEXT NOT NULL,
		PRIMARY KEY (op_id, operation)
	)`)
	if err != nil {
		t.Fatalf("create composite receipt table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close precreated database: %v", err)
	}

	store, err := NewSQLiteStore(path)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("NewSQLiteStore() accepted a composite (op_id, operation) receipt primary key")
	}
}

func TestSQLiteStoreArtifactSaveReceiptSurvivesLaterSupersession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-trace-later-supersession.sqlite")
	store := newSupersedeStoreAt(t, path)
	ctx := intentTestContext(t)
	predecessor, successor := supersessionFixtureSavedPair(t, store)
	identity := BrowserTraceStorageHostOperationIdentity{
		OpID:             "browsertrace-artifact-save-before-supersede",
		Operation:        BrowserTraceSaveAPIArtifactStorageHostOperation,
		PayloadSHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		WriterGeneration: 23,
	}
	effect := BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostArtifactEffect, ID: string(predecessor.ArtifactID)}
	if err := store.SaveAPIArtifactForStorageHostOperation(ctx, identity, predecessor); err != nil {
		t.Fatalf("SaveAPIArtifactForStorageHostOperation() error = %v", err)
	}
	fact := supersessionFactFixture(t, predecessor, successor)
	if err := store.SupersedeAPIArtifactWithPublicationIntent(ctx, predecessor.ArtifactID, successor.ArtifactID, fact); err != nil {
		t.Fatalf("SupersedeAPIArtifactWithPublicationIntent() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}
	reopened := newSupersedeStoreAt(t, path)
	result, found, err := reopened.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect)
	if err != nil {
		t.Fatalf("LookupBrowserTraceStorageHostOperationReceipt() after valid supersession = %v", err)
	}
	if !found || string(result) != "null" {
		t.Fatalf("original artifact save receipt = %q, found=%v, want exact original result after later supersession", result, found)
	}
	if err := reopened.SaveAPIArtifactForStorageHostOperation(ctx, identity, predecessor); err != nil {
		t.Fatalf("retry original SaveAPIArtifactForStorageHostOperation() after supersession = %v", err)
	}
}

func TestSQLiteStoreStorageHostCreateIntentAndReceiptAreAtomicAndRecoverable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-trace-create-receipt.sqlite")
	store := newSupersedeStoreAt(t, path)
	ctx := intentTestContext(t)
	item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	intent := intentFixtureEnvelope(t, item)
	identity := BrowserTraceStorageHostOperationIdentity{
		OpID: "browsertrace-create-atomic-1", Operation: BrowserTraceCreateAPIArtifactWithPublicationIntentOperation,
		PayloadSHA256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", WriterGeneration: 31,
	}
	effect := BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostArtifactCreationEffect, ID: string(item.ArtifactID)}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_browser_trace_receipt_insert BEFORE INSERT ON `+browserTraceStorageHostReceiptTable+` BEGIN SELECT RAISE(ABORT, 'receipt insert blocked'); END`); err != nil {
		t.Fatalf("install receipt failure trigger: %v", err)
	}
	err := store.CreateAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, item, intent)
	if err == nil || !ArtifactTransactionRollbackConfirmed(err) {
		t.Fatalf("failed receipt insert error = %v, want explicit transaction rollback proof", err)
	}
	if got := countBrowserTraceStorageHostRows(t, store, `api_artifact`); got != 0 {
		t.Fatalf("artifact rows after receipt rollback = %d, want 0", got)
	}
	if got := countBrowserTraceStorageHostRows(t, store, publicationIntentTable); got != 0 {
		t.Fatalf("creation intent rows after receipt rollback = %d, want 0", got)
	}
	if got := countBrowserTraceStorageHostRows(t, store, browserTraceStorageHostReceiptTable); got != 0 {
		t.Fatalf("receipt rows after receipt rollback = %d, want 0", got)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_browser_trace_receipt_insert`); err != nil {
		t.Fatalf("drop receipt failure trigger: %v", err)
	}
	if err := store.CreateAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, item, intent); err != nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntentForStorageHostOperation() = %v", err)
	}
	if got := countBrowserTraceStorageHostRows(t, store, `api_artifact`); got != 1 {
		t.Fatalf("artifact rows after commit = %d, want 1", got)
	}
	if got := countBrowserTraceStorageHostRows(t, store, publicationIntentTable); got != 1 {
		t.Fatalf("creation intent rows after commit = %d, want 1", got)
	}
	if got := countBrowserTraceStorageHostRows(t, store, browserTraceStorageHostReceiptTable); got != 1 {
		t.Fatalf("receipt rows after commit = %d, want 1", got)
	}
	beforeArtifactRows := snapshotAPIArtifactRows(t, store)
	beforeIntentRows := snapshotPublicationIntentRows(t, store)
	beforeReceiptRows := snapshotBrowserTraceStorageHostReceipts(t, store)
	if err := store.CreateAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, item, intent); err != nil {
		t.Fatalf("exact create operation retry = %v", err)
	}
	if !reflect.DeepEqual(beforeArtifactRows, snapshotAPIArtifactRows(t, store)) || !reflect.DeepEqual(beforeIntentRows, snapshotPublicationIntentRows(t, store)) || !reflect.DeepEqual(beforeReceiptRows, snapshotBrowserTraceStorageHostReceipts(t, store)) {
		t.Fatal("exact create retry changed the artifact, outbox intent, or receipt")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}
	reopened := newSupersedeStoreAt(t, path)
	result, found, err := reopened.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect)
	if err != nil || !found || string(result) != "null" {
		t.Fatalf("create receipt after reopen = %q, found=%v, err=%v; want original exact null result", result, found, err)
	}
	updated := item
	updated.Title += " later update"
	updated.Content += "\n# later update"
	updated.ContentHash = modulecore.ContentHashOf([]byte(updated.Content))
	if err := reopened.SaveAPIArtifact(ctx, updated); err != nil {
		t.Fatalf("later valid artifact update = %v", err)
	}
	result, found, err = reopened.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect)
	if err != nil || !found || string(result) != "null" {
		t.Fatalf("original create receipt after later update = %q, found=%v, err=%v; want preserved original result", result, found, err)
	}
}

func TestSQLiteStoreStorageHostSupersessionFactEdgeAndReceiptAreAtomicAndRecoverable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-trace-supersede-receipt.sqlite")
	store := newSupersedeStoreAt(t, path)
	ctx := intentTestContext(t)
	predecessor, successor := supersessionFixtureSavedPair(t, store)
	fact := supersessionFactFixture(t, predecessor, successor)
	identity := BrowserTraceStorageHostOperationIdentity{
		OpID: "browsertrace-supersede-atomic-1", Operation: BrowserTraceSupersedeAPIArtifactWithPublicationIntentOperation,
		PayloadSHA256: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", WriterGeneration: 37,
	}
	effect := BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostArtifactSupersessionEffect, ID: string(predecessor.ArtifactID)}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_browser_trace_receipt_insert BEFORE INSERT ON `+browserTraceStorageHostReceiptTable+` BEGIN SELECT RAISE(ABORT, 'receipt insert blocked'); END`); err != nil {
		t.Fatalf("install receipt failure trigger: %v", err)
	}
	err := store.SupersedeAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, predecessor.ArtifactID, successor.ArtifactID, fact)
	if err == nil || !ArtifactTransactionRollbackConfirmed(err) {
		t.Fatalf("failed supersession receipt insert error = %v, want explicit transaction rollback proof", err)
	}
	if got := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID); got.SupersededBy != "" {
		t.Fatalf("predecessor edge after receipt rollback = %s, want no edge", got.SupersededBy)
	}
	if facts := mustDrainSupersessionFacts(t, store, 10); len(facts) != 0 {
		t.Fatalf("supersession facts after receipt rollback = %#v, want none", facts)
	}
	if got := countBrowserTraceStorageHostRows(t, store, browserTraceStorageHostReceiptTable); got != 0 {
		t.Fatalf("receipt rows after receipt rollback = %d, want 0", got)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_browser_trace_receipt_insert`); err != nil {
		t.Fatalf("drop receipt failure trigger: %v", err)
	}
	if err := store.SupersedeAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, predecessor.ArtifactID, successor.ArtifactID, fact); err != nil {
		t.Fatalf("SupersedeAPIArtifactWithPublicationIntentForStorageHostOperation() = %v", err)
	}
	beforeArtifactRows := snapshotAPIArtifactRows(t, store)
	beforeFactRows := snapshotSupersessionIntentRows(t, store)
	beforeReceiptRows := snapshotBrowserTraceStorageHostReceipts(t, store)
	if err := store.SupersedeAPIArtifactWithPublicationIntentForStorageHostOperation(ctx, identity, predecessor.ArtifactID, successor.ArtifactID, fact); err != nil {
		t.Fatalf("exact supersession operation retry = %v", err)
	}
	if !reflect.DeepEqual(beforeArtifactRows, snapshotAPIArtifactRows(t, store)) || !reflect.DeepEqual(beforeFactRows, snapshotSupersessionIntentRows(t, store)) || !reflect.DeepEqual(beforeReceiptRows, snapshotBrowserTraceStorageHostReceipts(t, store)) {
		t.Fatal("exact supersession retry changed the artifact edge, fact, or receipt")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}
	reopened := newSupersedeStoreAt(t, path)
	result, found, err := reopened.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect)
	if err != nil || !found || string(result) != "null" {
		t.Fatalf("supersession receipt after reopen = %q, found=%v, err=%v; want exact null result", result, found, err)
	}
	if got := mustFindSupersedeArtifact(t, reopened, predecessor.ArtifactID); got.SupersededBy != successor.ArtifactID {
		t.Fatalf("reopened predecessor edge = %s, want %s", got.SupersededBy, successor.ArtifactID)
	}
	if facts := mustDrainSupersessionFacts(t, reopened, 10); len(facts) != 1 || facts[0].EventID != fact.EventID {
		t.Fatalf("reopened supersession facts = %#v, want the original single fact", facts)
	}
}

func TestSQLiteStoreStorageHostReceiptRejectsMismatchedAndCorruptProof(t *testing.T) {
	store := newSupersedeStore(t)
	ctx := intentTestContext(t)
	item := intentFixtureArtifact(t, intentFixtureWorkstreamID(t))
	identity := BrowserTraceStorageHostOperationIdentity{
		OpID: "browsertrace-artifact-receipt-proof-1", Operation: BrowserTraceSaveAPIArtifactStorageHostOperation,
		PayloadSHA256: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210", WriterGeneration: 41,
	}
	effect := BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostArtifactEffect, ID: string(item.ArtifactID)}
	if err := store.SaveAPIArtifactForStorageHostOperation(ctx, identity, item); err != nil {
		t.Fatalf("SaveAPIArtifactForStorageHostOperation() = %v", err)
	}
	for label, changed := range map[string]BrowserTraceStorageHostOperationIdentity{
		"operation": func() BrowserTraceStorageHostOperationIdentity {
			v := identity
			v.Operation = BrowserTraceSaveTraceRunStorageHostOperation
			return v
		}(),
		"payload": func() BrowserTraceStorageHostOperationIdentity {
			v := identity
			v.PayloadSHA256 = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
			return v
		}(),
		"generation": func() BrowserTraceStorageHostOperationIdentity {
			v := identity
			v.WriterGeneration++
			return v
		}(),
	} {
		changedEffect := effect
		if label == "operation" {
			changedEffect = BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostTraceRunEffect, ID: string(item.RunID)}
		}
		if _, found, err := store.LookupBrowserTraceStorageHostOperationReceipt(ctx, changed, changedEffect); err == nil || found {
			t.Errorf("lookup with mismatched %s = found %v, err %v; want fail closed", label, found, err)
		}
	}
	var originalProofJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT effect_json FROM `+browserTraceStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID).Scan(&originalProofJSON); err != nil {
		t.Fatalf("read original receipt proof: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE `+browserTraceStorageHostReceiptTable+` SET effect_json = ? WHERE op_id = ?`, `{}`, identity.OpID); err != nil {
		t.Fatalf("substitute receipt proof: %v", err)
	}
	if _, found, err := store.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect); err == nil || found {
		t.Fatalf("lookup with substituted effect proof = found %v, err %v; want fail closed", found, err)
	}
	var proof browserTraceStorageHostReceiptProof
	if err := json.Unmarshal([]byte(originalProofJSON), &proof); err != nil {
		t.Fatalf("decode receipt proof: %v", err)
	}
	var substituted domaintrace.APIArtifact
	if err := json.Unmarshal(proof.Payload, &substituted); err != nil {
		t.Fatalf("decode artifact proof payload: %v", err)
	}
	substituted.TaskID = modulecore.NewTaskID()
	substituted.RunID = modulecore.NewRunID()
	substitutedPayload, err := json.Marshal(substituted)
	if err != nil {
		t.Fatalf("encode substituted artifact proof: %v", err)
	}
	proof.Payload = substitutedPayload
	proofBytes, err := json.Marshal(proof)
	if err != nil {
		t.Fatalf("encode substituted owner proof: %v", err)
	}
	proofHash := sha256.Sum256(proofBytes)
	if _, err := store.db.ExecContext(ctx, `UPDATE `+browserTraceStorageHostReceiptTable+` SET effect_json = ?, effect_sha256 = ? WHERE op_id = ?`, string(proofBytes), hex.EncodeToString(proofHash[:]), identity.OpID); err != nil {
		t.Fatalf("write checksummed substituted proof: %v", err)
	}
	if _, found, err := store.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect); err == nil || found {
		t.Fatalf("lookup with checksummed but substituted proof = found %v, err %v; want fail closed", found, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM `+browserTraceStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID); err != nil {
		t.Fatalf("remove owner receipt: %v", err)
	}
	if _, found, err := store.LookupBrowserTraceStorageHostOperationReceipt(ctx, identity, effect); err == nil || found {
		t.Fatalf("lookup with missing receipt but existing effect = found %v, err %v; want fail closed", found, err)
	}
}

func countBrowserTraceStorageHostRows(t *testing.T, store *SQLiteStore, table string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	return count
}

func snapshotBrowserTraceStorageHostReceipts(t *testing.T, store *SQLiteStore) []string {
	t.Helper()
	rows, err := store.db.Query(`SELECT op_id, operation, payload_sha256, writer_generation, effect_kind, effect_id, result_json, result_sha256, effect_json, effect_sha256 FROM ` + browserTraceStorageHostReceiptTable + ` ORDER BY op_id`)
	if err != nil {
		t.Fatalf("snapshot storage-host receipts: %v", err)
	}
	defer rows.Close()
	var snapshot []string
	for rows.Next() {
		var opID, operation, payloadHash, effectKind, effectID, result, resultHash, proof, proofHash string
		var generation int64
		if err := rows.Scan(&opID, &operation, &payloadHash, &generation, &effectKind, &effectID, &result, &resultHash, &proof, &proofHash); err != nil {
			t.Fatalf("scan storage-host receipt: %v", err)
		}
		row, _ := json.Marshal([]any{opID, operation, payloadHash, generation, effectKind, effectID, result, resultHash, proof, proofHash})
		snapshot = append(snapshot, string(row))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate storage-host receipts: %v", err)
	}
	return snapshot
}
