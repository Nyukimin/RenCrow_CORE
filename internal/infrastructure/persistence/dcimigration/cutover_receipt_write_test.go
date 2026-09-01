package dcimigration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestApplyStagedCutoverPersistsReceiptAndRetainsAppliedState(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	result, err := applyStagedCutoverOperation(context.Background(), preflight.staged)
	if err != nil || result.applied == nil || result.receipt.Status != CutoverStatusApplied {
		t.Fatalf("apply operation = %#v, err=%v", result, err)
	}
	path := preflight.active.build.paths.cutoverReceipt
	disk, err := readCutoverReceipt(path)
	if err != nil || !reflect.DeepEqual(disk, result.receipt) {
		t.Fatalf("disk receipt = %#v, err=%v; want exact in-memory receipt", disk, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %o, want 0600", info.Mode().Perm())
	}
	if !cutoverKnownFileIsUnaliased(path, info) {
		t.Fatal("published receipt is not a single known file")
	}
	if got := cutoverReceiptTempEntries(t, filepath.Dir(path)); len(got) != 0 {
		t.Fatalf("receipt temporary entries remain: %v", got)
	}
	if strings.Contains(string(mustReadFile(t, path)), fixture.paths.dci) || strings.Contains(string(mustReadFile(t, path)), "legacy-search-1") {
		t.Fatal("durable receipt leaked private fixture values")
	}
	if got := result.applied.receipt.Status; got != CutoverStatusApplied {
		t.Fatalf("retained applied state status = %q", got)
	}
	result.receipt.SourceDatabaseLogicalSHA256["source_dci"] = strings.Repeat("a", 64)
	if result.applied.receipt.SourceDatabaseLogicalSHA256["source_dci"] == strings.Repeat("a", 64) {
		t.Fatal("returned receipt shares maps with retained applied state")
	}
}

func TestApplyStagedCutoverReceiptWriteFailuresRollbackAndLeaveNoReceipt(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{name: "pre-publication file sync", setup: func(t *testing.T) {
			original := cutoverReceiptSyncFile
			cutoverReceiptSyncFile = func(*os.File) error { return errors.New("injected receipt file sync") }
			t.Cleanup(func() { cutoverReceiptSyncFile = original })
		}},
		{name: "post-link parent sync", setup: func(t *testing.T) {
			original := cutoverReceiptSyncDirectory
			calls := 0
			cutoverReceiptSyncDirectory = func(path string) error {
				calls++
				if calls == 1 {
					return errors.New("injected receipt parent sync")
				}
				return original(path)
			}
			t.Cleanup(func() { cutoverReceiptSyncDirectory = original })
		}},
		{name: "readback", setup: func(t *testing.T) {
			original := cutoverReceiptWriter
			cutoverReceiptWriter = func(path string, receipt CutoverReceipt) (cutoverReceiptWriteResult, error) {
				result, err := original(path, receipt)
				if err != nil {
					return result, err
				}
				return result, errors.New("injected receipt readback failure")
			}
			t.Cleanup(func() { cutoverReceiptWriter = original })
		}},
		{name: "temporary cleanup", setup: func(t *testing.T) {
			original := cutoverReceiptRemove
			failed := false
			cutoverReceiptRemove = func(path string) error {
				if strings.Contains(path, ".tmp") && !failed {
					failed = true
					return errors.New("injected receipt temporary cleanup")
				}
				return original(path)
			}
			t.Cleanup(func() { cutoverReceiptRemove = original })
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preflight, fixture := newCutoverRestorePreflightFixture(t)
			before := cutoverActiveTestHashes(t, fixture.paths)
			tt.setup(t)
			result, err := applyStagedCutoverOperation(context.Background(), preflight.staged)
			if err == nil || result.receipt.Status != CutoverStatusRolledBack {
				t.Fatalf("receipt write failure = %#v, err=%v", result.receipt, err)
			}
			if receiptErr := validateCutoverReceipt(result.receipt); receiptErr != nil {
				t.Fatalf("rolled-back receipt validation error = %v", receiptErr)
			}
			if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
				t.Fatalf("active cohort was not restored: before=%#v after=%#v", before, got)
			}
			if _, err := os.Lstat(preflight.active.build.paths.cutoverReceipt); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("receipt remains after write failure: %v", err)
			}
			if got := cutoverReceiptTempEntries(t, filepath.Dir(preflight.active.build.paths.cutoverReceipt)); len(got) != 0 {
				t.Fatalf("receipt temporary entries remain: %v", got)
			}
		})
	}
}

func TestApplyStagedCutoverReceiptCancellationAfterPublicationRollsBack(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	before := cutoverActiveTestHashes(t, fixture.paths)
	original := cutoverReceiptWriter
	ctx, cancel := context.WithCancel(context.Background())
	cutoverReceiptWriter = func(path string, receipt CutoverReceipt) (cutoverReceiptWriteResult, error) {
		result, err := original(path, receipt)
		if err != nil {
			return result, err
		}
		cancel()
		return result, nil
	}
	t.Cleanup(func() { cutoverReceiptWriter = original })
	result, err := applyStagedCutoverOperation(ctx, preflight.staged)
	if err == nil || result.receipt.Status != CutoverStatusRolledBack {
		t.Fatalf("canceled post-publication apply = %#v, err=%v", result.receipt, err)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatal("active cohort was not restored after post-publication cancellation")
	}
	if _, err := os.Lstat(preflight.active.build.paths.cutoverReceipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt remains after post-publication cancellation: %v", err)
	}
}

func TestApplyStagedCutoverPreservesByteIdenticalUnknownFinalAndRollsBackFailed(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	before := cutoverActiveTestHashes(t, fixture.paths)
	receiptPath := preflight.active.build.paths.cutoverReceipt
	originalPublish := cutoverReceiptPublish
	originalRollback := cutoverExecuteRollbackReplace
	rollbackCalls := 0
	var published []byte
	cutoverReceiptPublish = func(temporary, final string) error {
		data, err := os.ReadFile(temporary)
		if err != nil {
			return err
		}
		published = append([]byte(nil), data...)
		if err := os.WriteFile(final, data, 0o600); err != nil {
			return err
		}
		return errors.New("injected unknown final publication")
	}
	cutoverExecuteRollbackReplace = func(role, source, target string) error {
		rollbackCalls++
		return originalRollback(role, source, target)
	}
	t.Cleanup(func() {
		cutoverReceiptPublish = originalPublish
		cutoverExecuteRollbackReplace = originalRollback
	})

	result, err := applyStagedCutoverOperation(context.Background(), preflight.staged)
	if err == nil || result.receipt.Status != CutoverStatusRollbackFailed {
		t.Fatalf("unknown final result = %#v, err=%v", result.receipt, err)
	}
	if rollbackCalls != 6 {
		t.Fatalf("rollback calls = %d, want six restore attempts", rollbackCalls)
	}
	if result.receipt.RestoredArtifactSetSHA256 != "" || result.receipt.JSONLRestored != 0 {
		t.Fatalf("rollback-failed receipt claims restoration: %#v", result.receipt)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatalf("active cohort was not restored: before=%#v after=%#v", before, got)
	}
	if got := mustReadFile(t, receiptPath); !bytes.Equal(got, published) {
		t.Fatal("unknown byte-identical final was changed")
	}
	if _, err := readCutoverReceipt(receiptPath); err != nil {
		t.Fatalf("preserved final is not the valid published receipt: %v", err)
	}
	if got := cutoverReceiptTempEntries(t, filepath.Dir(receiptPath)); len(got) != 0 {
		t.Fatalf("owned temporary receipt remains: %v", got)
	}
}

func TestApplyStagedCutoverCancellationPreservesByteIdenticalPublishedSubstitution(t *testing.T) {
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	before := cutoverActiveTestHashes(t, fixture.paths)
	receiptPath := preflight.active.build.paths.cutoverReceipt
	originalWriter := cutoverReceiptWriter
	ctx, cancel := context.WithCancel(context.Background())
	cutoverReceiptWriter = func(path string, receipt CutoverReceipt) (cutoverReceiptWriteResult, error) {
		result, err := originalWriter(path, receipt)
		if err != nil {
			return result, err
		}
		data := mustReadFile(t, path)
		substitute := path + ".substitute"
		if err := os.WriteFile(substitute, data, 0o600); err != nil {
			return result, err
		}
		if err := os.Remove(path); err != nil {
			_ = os.Remove(substitute)
			return result, err
		}
		if err := os.Rename(substitute, path); err != nil {
			return result, err
		}
		info, err := os.Lstat(path)
		if err != nil || result.final == nil || os.SameFile(info, result.final.info) {
			return result, errors.New("test substitution did not change inode")
		}
		cancel()
		return result, nil
	}
	t.Cleanup(func() { cutoverReceiptWriter = originalWriter })

	result, err := applyStagedCutoverOperation(ctx, preflight.staged)
	if err == nil || result.receipt.Status != CutoverStatusRollbackFailed {
		t.Fatalf("canceled substituted publication = %#v, err=%v", result.receipt, err)
	}
	if result.receipt.RestoredArtifactSetSHA256 != "" || result.receipt.JSONLRestored != 0 {
		t.Fatalf("rollback-failed receipt claims restoration: %#v", result.receipt)
	}
	if got := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(before, got) {
		t.Fatal("active cohort was not restored after substituted publication")
	}
	if _, err := os.Lstat(receiptPath); err != nil {
		t.Fatalf("substituted receipt was not preserved: %v", err)
	}
	if _, err := readCutoverReceipt(receiptPath); err != nil {
		t.Fatalf("preserved substituted receipt is invalid: %v", err)
	}
	if got := cutoverReceiptTempEntries(t, filepath.Dir(receiptPath)); len(got) != 0 {
		t.Fatalf("owned temporary receipt remains: %v", got)
	}
}

func TestApplyStagedCutoverReceiptCleanupSubstitutionIsPreserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink substitution test is Unix-specific")
	}
	preflight, fixture := newCutoverRestorePreflightFixture(t)
	original := cutoverReceiptWriter
	cutoverReceiptWriter = func(path string, receipt CutoverReceipt) (cutoverReceiptWriteResult, error) {
		result, err := original(path, receipt)
		if err != nil {
			return result, err
		}
		if err := os.Remove(path); err != nil {
			return result, err
		}
		if err := os.Symlink("receipt-secret", path); err != nil {
			return result, err
		}
		return result, errors.New("injected receipt substitution")
	}
	t.Cleanup(func() { cutoverReceiptWriter = original })
	result, err := applyStagedCutoverOperation(context.Background(), preflight.staged)
	if err == nil || result.receipt.Status != CutoverStatusRollbackFailed {
		t.Fatalf("receipt substitution result = %#v, err=%v", result.receipt, err)
	}
	info, err := os.Lstat(preflight.active.build.paths.cutoverReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("unknown substituted receipt was removed")
	}
	if containsCutoverActivePrivateValue(err, fixture) {
		t.Fatal("receipt write error leaked private value")
	}
}

func TestWriteCutoverReceiptRejectsInvalidInputWithoutCreatingAFile(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	valid := newValidCutoverReceiptTestValue(preflight.active, preflight.staged)
	path := filepath.Join(t.TempDir(), "cutover.json")
	invalid := valid
	invalid.Status = "partial"
	if _, err := writeCutoverReceipt(path, invalid); err == nil {
		t.Fatal("invalid receipt was written")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid receipt path exists: %v", err)
	}
}

func TestWriteCutoverReceiptPreservesExistingAndSubstitutedFinal(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	valid := newValidCutoverReceiptTestValue(preflight.active, preflight.staged)
	parent := filepath.Dir(preflight.active.build.paths.cutoverReceipt)
	path := preflight.active.build.paths.cutoverReceipt

	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustReadFile(t, path)
	if _, err := writeCutoverReceipt(path, valid); err == nil {
		t.Fatal("existing receipt was overwritten")
	}
	if got := mustReadFile(t, path); !reflect.DeepEqual(got, before) {
		t.Fatalf("existing receipt changed: got=%q want=%q", got, before)
	}
	if got := cutoverReceiptTempEntries(t, parent); len(got) != 0 {
		t.Fatalf("temporary receipt entries remain after existing final: %v", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	encoded, err := marshalCutoverReceipt(valid)
	if err != nil {
		t.Fatal(err)
	}
	originalPublish := cutoverReceiptPublish
	cutoverReceiptPublish = func(temporary, final string) error {
		data, err := os.ReadFile(temporary)
		if err != nil {
			return err
		}
		if err := os.WriteFile(final, data, 0o600); err != nil {
			return err
		}
		return errors.New("injected publication failure")
	}
	t.Cleanup(func() { cutoverReceiptPublish = originalPublish })
	if _, err := writeCutoverReceipt(path, valid); err == nil {
		t.Fatal("publication failure was accepted")
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, encoded) {
		t.Fatal("substituted receipt was changed")
	}
	if got := cutoverReceiptTempEntries(t, parent); len(got) != 0 {
		t.Fatalf("temporary receipt entries remain after publication failure: %v", got)
	}
}

func TestWriteCutoverReceiptPreservesRegularFinalSubstitutionOnCleanupFailure(t *testing.T) {
	preflight, _ := newCutoverRestorePreflightFixture(t)
	valid := newValidCutoverReceiptTestValue(preflight.active, preflight.staged)
	path := preflight.active.build.paths.cutoverReceipt
	originalSync := cutoverReceiptSyncDirectory
	calls := 0
	cutoverReceiptSyncDirectory = func(parent string) error {
		calls++
		if calls == 1 {
			entries, err := os.ReadDir(parent)
			if err != nil {
				return err
			}
			var temporary string
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".rencrow-identity-step03-cutover-receipt-") {
					temporary = filepath.Join(parent, entry.Name())
					break
				}
			}
			if temporary == "" {
				return errors.New("temporary receipt was not found")
			}
			data := mustReadFile(t, temporary)
			if err := os.Remove(path); err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return err
			}
			return errors.New("injected receipt cleanup substitution")
		}
		return originalSync(parent)
	}
	t.Cleanup(func() { cutoverReceiptSyncDirectory = originalSync })
	if _, err := writeCutoverReceipt(path, valid); err == nil {
		t.Fatal("cleanup substitution was accepted")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatal("substituted final is not preserved as a regular file")
	}
	encoded, err := marshalCutoverReceipt(valid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustReadFile(t, path), encoded) {
		t.Fatal("substituted final content changed")
	}
	if got := cutoverReceiptTempEntries(t, filepath.Dir(path)); len(got) != 1 {
		t.Fatalf("unsafe temporary entry was removed or multiplied: %v", got)
	}
}

func cutoverReceiptTempEntries(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".rencrow-identity-step03-cutover-receipt-") {
			result = append(result, entry.Name())
		}
	}
	return result
}
