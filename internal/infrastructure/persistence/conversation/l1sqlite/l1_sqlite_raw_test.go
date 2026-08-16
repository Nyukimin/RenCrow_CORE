package l1sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const commonRawTestOwner = "ren"

func TestL1SQLiteStoreIntakeCommonRawInlineDoesNotProjectOrMutateDomainTables(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := commonRawTestContext(t, "raw-inline-request")
	input := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("message-1", []byte("immutable inline"))}, nil)
	baseline := commonRawTableCounts(t, store)
	domainBaseline := commonRawDomainCounts(t, store)
	result, err := store.IntakeCommonRaw(ctx, "raw-inline-request", commonRawTestOwner, commonRawTestOwner, input)
	if err != nil {
		t.Fatalf("IntakeCommonRaw inline: %v", err)
	}
	if result.Status != domainmemory.CommonRawStateCompleted || result.IdempotentReplay || len(result.Records) != 1 {
		t.Fatalf("unexpected inline receipt: %+v", result)
	}
	wantID := domainmemory.DeterministicCommonRawRecordID(commonRawTestOwner, "user:"+commonRawTestOwner, input.Manifest.SourceType, input.Manifest.SourceIdentity, "message-1", input.Records[0].ContentSHA256)
	if result.Records[0].RawRecordID != wantID || result.Records[0].StorageKind != domainmemory.CommonRawStorageInline || result.Records[0].ObjectRef != "" {
		t.Fatalf("unexpected inline record receipt: %+v", result.Records[0])
	}
	if got := commonRawTableCounts(t, store); got != baseline+3 {
		t.Fatalf("raw intake changed unexpected table count delta: baseline=%d got=%d", baseline, got)
	}
	if got := commonRawDomainCounts(t, store); !reflect.DeepEqual(got, domainBaseline) {
		t.Fatalf("raw intake mutated domain/projection tables: before=%v after=%v", domainBaseline, got)
	}
	var manifestCount, recordCount, stateCount, projectionCount int
	for _, query := range []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM l1_raw_source_manifest`, &manifestCount},
		{`SELECT count(*) FROM l1_raw_record`, &recordCount},
		{`SELECT count(*) FROM l1_raw_state_event`, &stateCount},
		{`SELECT count(*) FROM l1_raw_projection_receipt`, &projectionCount},
	} {
		if err := store.db.QueryRow(query.query).Scan(query.out); err != nil {
			t.Fatalf("count %s: %v", query.query, err)
		}
	}
	if manifestCount != 1 || recordCount != 1 || stateCount != 1 || projectionCount != 0 {
		t.Fatalf("raw rows manifest=%d record=%d state=%d projection=%d", manifestCount, recordCount, stateCount, projectionCount)
	}
}

func TestL1SQLiteStoreIntakeCommonRawObjectAndAssetUsesContainedContentAddressedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatalf("SetCommonRawSourceRoot: %v", err)
	}
	content := bytesOfSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 'x')
	asset := []byte("asset-bytes")
	input := commonRawTestInput([]domainmemory.CommonRawRecord{{SourceRecordID: "message-large", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: commonRawTestTime(), Content: content, ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "export", Rights: "owner", License: "private", AssetRefs: []string{"asset-1"}}}, []domainmemory.CommonRawAsset{{SourceAssetID: "asset-1", MediaType: "image/png", Content: asset, ContentSHA256: domainmemory.SHA256Hex(asset), Provenance: "export", Rights: "owner", License: "private"}})
	result, err := store.IntakeCommonRaw(commonRawTestContext(t, "raw-object-request"), "raw-object-request", commonRawTestOwner, commonRawTestOwner, input)
	if err != nil {
		t.Fatalf("IntakeCommonRaw object: %v", err)
	}
	if len(result.Records) != 1 || result.Records[0].StorageKind != domainmemory.CommonRawStorageObject || filepath.IsAbs(filepath.FromSlash(result.Records[0].ObjectRef)) {
		t.Fatalf("unexpected object receipt: %+v", result)
	}
	if len(result.Records[0].AssetRefs) != 1 || filepath.IsAbs(filepath.FromSlash(result.Records[0].AssetRefs[0].ObjectRef)) {
		t.Fatalf("unexpected asset receipt: %+v", result.Records[0].AssetRefs)
	}
	for _, ref := range []string{result.Records[0].ObjectRef, result.Records[0].AssetRefs[0].ObjectRef} {
		path := filepath.Join(root, filepath.FromSlash(ref))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("object %q: %v", ref, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || commonRawLinkCount(info) > 1 {
			t.Fatalf("unsafe object %q mode=%v links=%d", ref, info.Mode(), commonRawLinkCount(info))
		}
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("raw root: %v", err)
	}
	if runtimeGOOSNotWindows() && rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("raw root mode=%o, want 700", rootInfo.Mode().Perm())
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(result.Records[0].ObjectRef))); err != nil {
		t.Fatalf("remove object for replay verification: %v", err)
	}
	if _, err := store.IntakeCommonRaw(commonRawTestContext(t, "raw-object-replay"), "raw-object-replay", commonRawTestOwner, commonRawTestOwner, input); err == nil {
		t.Fatal("replay with missing object must not return stored receipt")
	}
}

func TestL1SQLiteStoreIntakeCommonRawReplayOrderIndependenceAndSourceConflict(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	recordA := commonRawTestRecord("a", []byte("a"))
	recordB := commonRawTestRecord("b", []byte("b"))
	input := commonRawTestInput([]domainmemory.CommonRawRecord{recordB, recordA}, nil)
	first, err := store.IntakeCommonRaw(commonRawTestContext(t, "raw-replay-1"), "raw-replay-1", commonRawTestOwner, commonRawTestOwner, input)
	if err != nil {
		t.Fatalf("first intake: %v", err)
	}
	reordered := commonRawTestInput([]domainmemory.CommonRawRecord{recordA, recordB}, nil)
	second, err := store.IntakeCommonRaw(commonRawTestContext(t, "raw-replay-2"), "raw-replay-2", commonRawTestOwner, commonRawTestOwner, reordered)
	if err != nil || !second.IdempotentReplay || second.ManifestID != first.ManifestID {
		t.Fatalf("order-independent replay=%+v err=%v", second, err)
	}
	changed := commonRawTestRecord("a", []byte("changed"))
	conflict := commonRawTestInput([]domainmemory.CommonRawRecord{changed, recordB}, nil)
	_, err = store.IntakeCommonRaw(commonRawTestContext(t, "raw-replay-conflict"), "raw-replay-conflict", commonRawTestOwner, commonRawTestOwner, conflict)
	if domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorSourceChanged {
		t.Fatalf("changed source code=%q err=%v, want source_changed", domainmemory.CommonRawErrorCodeOf(err), err)
	}
	if got := commonRawTableCounts(t, store); got != 5 {
		t.Fatalf("conflict created rows, total raw rows=%d", got)
	}
}

func TestL1SQLiteStoreIntakeCommonRawReplayRejectsTamperedReceiptAndState(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	input := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("tamper", []byte("tamper"))}, nil)
	if _, err := store.IntakeCommonRaw(commonRawTestContext(t, "tamper-request"), "tamper-request", commonRawTestOwner, commonRawTestOwner, input); err != nil {
		t.Fatalf("initial intake: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER trg_l1_raw_state_immutable_update`); err != nil {
		t.Fatalf("drop state trigger: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_raw_state_event SET actor_id = 'tampered'`); err != nil {
		t.Fatalf("tamper state row: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER trg_l1_raw_state_immutable_update BEFORE UPDATE ON l1_raw_state_event BEGIN SELECT RAISE(ABORT, 'l1 raw state event is immutable'); END`); err != nil {
		t.Fatalf("restore state trigger: %v", err)
	}
	if _, err := store.IntakeCommonRaw(commonRawTestContext(t, "tamper-replay-state"), "tamper-replay-state", commonRawTestOwner, commonRawTestOwner, input); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorUnavailable {
		t.Fatalf("tampered state replay code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}

	if _, err := store.db.Exec(`DROP TRIGGER trg_l1_raw_state_immutable_update`); err != nil {
		t.Fatalf("drop state trigger for restore: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_raw_state_event SET actor_id = ?`, commonRawTestOwner); err != nil {
		t.Fatalf("restore state row: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER trg_l1_raw_state_immutable_update BEFORE UPDATE ON l1_raw_state_event BEGIN SELECT RAISE(ABORT, 'l1 raw state event is immutable'); END`); err != nil {
		t.Fatalf("restore state trigger after fixture: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER trg_l1_raw_manifest_immutable_update`); err != nil {
		t.Fatalf("drop manifest trigger: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_raw_source_manifest SET receipt_json = ?`, `{"request_id":"tampered"}`); err != nil {
		t.Fatalf("tamper receipt row: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER trg_l1_raw_manifest_immutable_update BEFORE UPDATE ON l1_raw_source_manifest BEGIN SELECT RAISE(ABORT, 'l1 raw source manifest is immutable'); END`); err != nil {
		t.Fatalf("restore manifest trigger: %v", err)
	}
	if _, err := store.IntakeCommonRaw(commonRawTestContext(t, "tamper-replay-receipt"), "tamper-replay-receipt", commonRawTestOwner, commonRawTestOwner, input); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorUnavailable {
		t.Fatalf("tampered receipt replay code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}
}

func TestL1SQLiteStoreIntakeCommonRawRejectsScopeHashSchemaRightsAndEmptyFailures(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	base := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("message-1", []byte("valid"))}, nil)
	tests := []struct {
		name string
		in   domainmemory.CommonRawIntakeRequest
		code domainmemory.CommonRawErrorCode
	}{
		{name: "missing scope", in: base, code: domainmemory.CommonRawErrorForbidden},
		{name: "wrong owner scope", in: base, code: domainmemory.CommonRawErrorForbidden},
		{name: "uppercase record hash", in: commonRawTestInput([]domainmemory.CommonRawRecord{{SourceRecordID: "upper", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "text/plain", OccurredAt: commonRawTestTime(), Content: []byte("upper"), ContentSHA256: strings.ToUpper(domainmemory.SHA256Hex([]byte("upper"))), Provenance: "export", Rights: "owner", License: "private"}}, nil), code: domainmemory.CommonRawErrorInvalid},
		{name: "schema", in: func() domainmemory.CommonRawIntakeRequest {
			v := base
			v.Manifest.ContractVersion = "common-raw/v0"
			return v
		}(), code: domainmemory.CommonRawErrorSchema},
		{name: "rights", in: func() domainmemory.CommonRawIntakeRequest {
			v := base
			v.Manifest.Rights = ""
			v.Manifest.ManifestSHA256, _ = domainmemory.CommonRawInputHash(v.Manifest, v.Records, v.Assets)
			return v
		}(), code: domainmemory.CommonRawErrorInvalid},
		{name: "scope claim", in: func() domainmemory.CommonRawIntakeRequest { v := base; v.Manifest.Scope = "user:other"; return v }(), code: domainmemory.CommonRawErrorForbidden},
		{name: "empty disallowed", in: commonRawTestInput(nil, nil), code: domainmemory.CommonRawErrorInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestID := test.name + "-request"
			ctx := commonRawTestContext(t, requestID)
			if test.name == "missing scope" {
				ctx = context.Background()
			} else if test.name == "wrong owner scope" {
				ctx = commonRawTestContextFor(t, requestID, "other")
			}
			_, err := store.IntakeCommonRaw(ctx, requestID, commonRawTestOwner, commonRawTestOwner, test.in)
			if domainmemory.CommonRawErrorCodeOf(err) != test.code {
				t.Fatalf("error code=%q err=%v, want %q", domainmemory.CommonRawErrorCodeOf(err), err, test.code)
			}
		})
	}
	allowed := commonRawTestInput(nil, nil)
	allowed.Manifest.AllowEmpty = true
	allowed.Manifest.ManifestSHA256, _ = domainmemory.CommonRawInputHash(allowed.Manifest, allowed.Records, allowed.Assets)
	if result, err := store.IntakeCommonRaw(commonRawTestContext(t, "empty-allowed"), "empty-allowed", commonRawTestOwner, commonRawTestOwner, allowed); err != nil || result.Status != domainmemory.CommonRawStateCompleted {
		t.Fatalf("explicit allow_empty result=%+v err=%v", result, err)
	}
}

func TestL1SQLiteStoreIntakeCommonRawRollbackCleansNewObjectsAndTriggersAreImmutable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatalf("SetCommonRawSourceRoot: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER abort_common_raw_state BEFORE INSERT ON l1_raw_state_event BEGIN SELECT RAISE(ABORT, 'test rollback'); END`); err != nil {
		t.Fatalf("install rollback trigger: %v", err)
	}
	content := bytesOfSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 'r')
	input := commonRawTestInput([]domainmemory.CommonRawRecord{{SourceRecordID: "rollback", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: commonRawTestTime(), Content: content, ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "export", Rights: "owner", License: "private"}}, nil)
	_, err = store.IntakeCommonRaw(commonRawTestContext(t, "rollback-request"), "rollback-request", commonRawTestOwner, commonRawTestOwner, input)
	if domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorUnavailable {
		t.Fatalf("rollback error code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}
	for _, table := range []string{"l1_raw_source_manifest", "l1_raw_record", "l1_raw_state_event"} {
		var count int
		if err := store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("rollback left %s rows=%d", table, count)
		}
	}
	var leftoverFiles []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			leftoverFiles = append(leftoverFiles, path)
		}
		return nil
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk rollback root: %v", err)
	}
	if len(leftoverFiles) != 0 {
		t.Fatalf("rollback left object files: %v", leftoverFiles)
	}

	// Remove the abort trigger and commit one inline record so all immutable
	// triggers can be exercised against actual terminal rows.
	if _, err := store.db.Exec(`DROP TRIGGER abort_common_raw_state`); err != nil {
		t.Fatalf("drop rollback trigger: %v", err)
	}
	inline := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("immutable", []byte("immutable"))}, nil)
	result, err := store.IntakeCommonRaw(commonRawTestContext(t, "immutable-request"), "immutable-request", commonRawTestOwner, commonRawTestOwner, inline)
	if err != nil {
		t.Fatalf("immutable fixture intake: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_raw_source_manifest SET receipt_json = receipt_json WHERE manifest_id = ?`, result.ManifestID); err == nil {
		t.Fatal("manifest update trigger did not reject mutation")
	}
	if _, err := store.db.Exec(`DELETE FROM l1_raw_record WHERE manifest_id = ?`, result.ManifestID); err == nil {
		t.Fatal("record delete trigger did not reject mutation")
	}
	if _, err := store.db.Exec(`UPDATE l1_raw_state_event SET reason_code = reason_code WHERE manifest_id = ?`, result.ManifestID); err == nil {
		t.Fatal("state update trigger did not reject mutation")
	}
	if _, err := store.db.Exec(`INSERT INTO l1_raw_projection_receipt (projection_receipt_id, projection_type, output_store, output_record_id, raw_record_ids_json, revision, input_sha256, status, created_at, updated_at) VALUES ('projection-test', 'test', 'test', 'out', '[]', 'v1', ?, 'completed', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("projection fixture insert: %v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM l1_raw_projection_receipt WHERE projection_receipt_id = 'projection-test'`); err == nil {
		t.Fatal("projection delete trigger did not reject mutation")
	}
}

func TestL1SQLiteStoreIntakeCommonRawRejectsUnsafeExistingObject(t *testing.T) {
	if runtimeGOOSNotWindows() {
		t.Run("symlink", func(t *testing.T) {
			targetRoot, err := os.MkdirTemp(t.TempDir(), "outside-")
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "raw-sources")
			hash := domainmemory.SHA256Hex(bytesOfSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 's'))
			objectDir := filepath.Join(root, "objects", "sha256", hash[:2])
			if err := os.MkdirAll(objectDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(targetRoot, filepath.Join(objectDir, hash)); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.SetCommonRawSourceRoot(root); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorObject {
				t.Fatalf("symlink root error code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
			}
		})
		t.Run("hardlink", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "raw-sources")
			hash := domainmemory.SHA256Hex(bytesOfSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 'h'))
			objectDir := filepath.Join(root, "objects", "sha256", hash[:2])
			if err := os.MkdirAll(objectDir, 0o700); err != nil {
				t.Fatal(err)
			}
			objectPath := filepath.Join(objectDir, hash)
			content := bytesOfSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 'h')
			if err := os.WriteFile(objectPath, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(objectPath, filepath.Join(objectDir, "alias")); err != nil {
				t.Skipf("hardlink unavailable: %v", err)
			}
			store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.SetCommonRawSourceRoot(root); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorObject {
				t.Fatalf("hardlink root error code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
			}
		})
	}
}

func TestL1SQLiteStoreIntakeCommonRawRejectsInvalidObjectRoots(t *testing.T) {
	if !runtimeGOOSNotWindows() {
		t.Skip("root separator portability check is Unix-specific")
	}
	for _, root := range []string{"relative-raw-root", string(filepath.Separator)} {
		t.Run(root, func(t *testing.T) {
			store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.SetCommonRawSourceRoot(root); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorRoot {
				t.Fatalf("root=%q code=%q err=%v", root, domainmemory.CommonRawErrorCodeOf(err), err)
			}
		})
	}
}

func TestL1SQLiteStoreIntakeCommonRawRejectsRemainingValidationFailures(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("valid", []byte("valid"))}, nil)
	content := bytesOfSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 'o')
	large := commonRawTestInput([]domainmemory.CommonRawRecord{{SourceRecordID: "large", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: commonRawTestTime(), Content: content, ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "export", Rights: "owner", License: "private"}}, nil)
	batchRecordSize := domainmemory.CommonRawMaxBatchPayloadSize/2 + 1
	batchOne := bytesOfSize(batchRecordSize, '1')
	batchTwo := bytesOfSize(batchRecordSize, '2')
	batch := commonRawTestInput([]domainmemory.CommonRawRecord{
		{SourceRecordID: "batch-1", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: commonRawTestTime(), Content: batchOne, ContentSHA256: domainmemory.SHA256Hex(batchOne), Provenance: "export", Rights: "owner", License: "private"},
		{SourceRecordID: "batch-2", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: commonRawTestTime(), Content: batchTwo, ContentSHA256: domainmemory.SHA256Hex(batchTwo), Provenance: "export", Rights: "owner", License: "private"},
	}, nil)
	tests := []struct {
		name  string
		input domainmemory.CommonRawIntakeRequest
		code  domainmemory.CommonRawErrorCode
	}{
		{name: "manifest count mismatch", input: func() domainmemory.CommonRawIntakeRequest {
			v := cloneCommonRawInput(base)
			v.Manifest.SourceCount++
			return v
		}(), code: domainmemory.CommonRawErrorInvalid},
		{name: "record content claim mismatch", input: func() domainmemory.CommonRawIntakeRequest {
			v := cloneCommonRawInput(base)
			v.Records[0].ContentSHA256 = domainmemory.SHA256Hex([]byte("different"))
			v.Manifest.ManifestSHA256, _ = domainmemory.CommonRawInputHash(v.Manifest, v.Records, v.Assets)
			return v
		}(), code: domainmemory.CommonRawErrorConflict},
		{name: "asset content claim mismatch", input: func() domainmemory.CommonRawIntakeRequest {
			v := commonRawTestInput([]domainmemory.CommonRawRecord{{SourceRecordID: "with-asset", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "text/plain", OccurredAt: commonRawTestTime(), Content: []byte("record"), ContentSHA256: domainmemory.SHA256Hex([]byte("record")), Provenance: "export", Rights: "owner", License: "private", AssetRefs: []string{"asset"}}}, []domainmemory.CommonRawAsset{{SourceAssetID: "asset", MediaType: "image/png", Content: []byte("asset"), ContentSHA256: domainmemory.SHA256Hex([]byte("other")), Provenance: "export", Rights: "owner", License: "private"}})
			v.Manifest.ManifestSHA256, _ = domainmemory.CommonRawInputHash(v.Manifest, v.Records, v.Assets)
			return v
		}(), code: domainmemory.CommonRawErrorConflict},
		{name: "canonical manifest hash mismatch", input: func() domainmemory.CommonRawIntakeRequest {
			v := cloneCommonRawInput(base)
			v.Manifest.ManifestSHA256 = strings.Repeat("b", 64)
			return v
		}(), code: domainmemory.CommonRawErrorConflict},
		{name: "missing asset reference", input: func() domainmemory.CommonRawIntakeRequest {
			v := cloneCommonRawInput(base)
			v.Records[0].AssetRefs = []string{"missing"}
			v.Manifest.ManifestSHA256, _ = domainmemory.CommonRawInputHash(v.Manifest, v.Records, v.Assets)
			return v
		}(), code: domainmemory.CommonRawErrorInvalid},
		{name: "unreferenced asset", input: func() domainmemory.CommonRawIntakeRequest {
			v := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("record", []byte("record"))}, []domainmemory.CommonRawAsset{{SourceAssetID: "orphan", MediaType: "text/plain", Content: []byte("orphan"), ContentSHA256: domainmemory.SHA256Hex([]byte("orphan")), Provenance: "export", Rights: "owner", License: "private"}})
			return v
		}(), code: domainmemory.CommonRawErrorInvalid},
		{name: "object root unset", input: large, code: domainmemory.CommonRawErrorRoot},
		{name: "batch payload bound", input: batch, code: domainmemory.CommonRawErrorInvalid},
		{name: "oversized payload", input: func() domainmemory.CommonRawIntakeRequest {
			v := commonRawTestInput([]domainmemory.CommonRawRecord{{SourceRecordID: "oversized", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: commonRawTestTime(), Content: bytesOfSize(domainmemory.CommonRawMaxPayloadSize+1, 'p'), ContentSHA256: domainmemory.SHA256Hex(bytesOfSize(domainmemory.CommonRawMaxPayloadSize+1, 'p')), Provenance: "export", Rights: "owner", License: "private"}}, nil)
			return v
		}(), code: domainmemory.CommonRawErrorInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestID := "validation-" + strings.ReplaceAll(test.name, " ", "-")
			_, err := store.IntakeCommonRaw(commonRawTestContext(t, requestID), requestID, commonRawTestOwner, commonRawTestOwner, test.input)
			if domainmemory.CommonRawErrorCodeOf(err) != test.code {
				t.Fatalf("code=%q err=%v, want %q", domainmemory.CommonRawErrorCodeOf(err), err, test.code)
			}
		})
	}

	var nilStore *L1SQLiteStore
	if _, err := nilStore.IntakeCommonRaw(commonRawTestContext(t, "nil-store"), "nil-store", commonRawTestOwner, commonRawTestOwner, base); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorUnavailable {
		t.Fatalf("nil store code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}
	closed, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.IntakeCommonRaw(commonRawTestContext(t, "closed-store"), "closed-store", commonRawTestOwner, commonRawTestOwner, base); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorUnavailable {
		t.Fatalf("closed store code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}
}

func commonRawTestInput(records []domainmemory.CommonRawRecord, assets []domainmemory.CommonRawAsset) domainmemory.CommonRawIntakeRequest {
	manifest := domainmemory.CommonRawManifest{ContractVersion: domainmemory.CommonRawContractVersion, SourceType: "test", SourceIdentity: "export-1", SourceCount: len(records), AssetCount: len(assets), SchemaVersion: "schema-1", ConverterVersion: "converter-1", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "test-export"}
	manifest.ManifestSHA256, _ = domainmemory.CommonRawInputHash(manifest, records, assets)
	return domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: records, Assets: assets}
}

func cloneCommonRawInput(input domainmemory.CommonRawIntakeRequest) domainmemory.CommonRawIntakeRequest {
	clone := input
	clone.Records = append([]domainmemory.CommonRawRecord(nil), input.Records...)
	for index := range clone.Records {
		clone.Records[index].Content = append([]byte(nil), input.Records[index].Content...)
		clone.Records[index].AssetRefs = append([]string(nil), input.Records[index].AssetRefs...)
	}
	clone.Assets = append([]domainmemory.CommonRawAsset(nil), input.Assets...)
	for index := range clone.Assets {
		clone.Assets[index].Content = append([]byte(nil), input.Assets[index].Content...)
	}
	return clone
}

func commonRawTestRecord(id string, content []byte) domainmemory.CommonRawRecord {
	return domainmemory.CommonRawRecord{SourceRecordID: id, Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "text/plain", OccurredAt: commonRawTestTime(), Content: content, ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "test-export", Rights: "owner", License: "private"}
}

func commonRawTestTime() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) }

func commonRawTestContext(t *testing.T, requestID string) context.Context {
	return commonRawTestContextFor(t, requestID, commonRawTestOwner)
}

func commonRawTestContextFor(t *testing.T, requestID, ownerID string) context.Context {
	scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, ownerID, ownerID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func commonRawTableCounts(t *testing.T, store *L1SQLiteStore) int {
	t.Helper()
	total := 0
	for _, table := range []string{"l1_raw_source_manifest", "l1_raw_record", "l1_raw_state_event", "l1_raw_projection_receipt"} {
		var count int
		if err := store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		total += count
	}
	return total
}

func commonRawDomainCounts(t *testing.T, store *L1SQLiteStore) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{"l1_memory_event", "l1_staging_item", "l1_news_item", "l1_knowledge_item", "recall_trace"} {
		var count int
		if err := store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count domain table %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func bytesOfSize(size int, value byte) []byte {
	content := make([]byte, size)
	for i := range content {
		content[i] = value
	}
	return content
}

func runtimeGOOSNotWindows() bool { return runtime.GOOS != "windows" }
