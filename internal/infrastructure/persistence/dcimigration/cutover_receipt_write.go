package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
)

const cutoverReceiptTempPattern = ".rencrow-identity-step03-cutover-receipt-*.tmp"

// cutoverReceiptWriteError carries no filesystem details across the bounded
// error boundary.  Any private ownership binding is retained in the result.
type cutoverReceiptWriteError struct{}

func (err *cutoverReceiptWriteError) Error() string {
	return "cutover receipt write failed"
}

var cutoverReceiptWriter = writeCutoverReceipt
var cutoverReceiptSyncFile = func(file *os.File) error { return file.Sync() }
var cutoverReceiptSyncDirectory = syncDirectory
var cutoverReceiptPublish = os.Link
var cutoverReceiptRemove = os.Remove

// cutoverApplyOperationResult is private so a later owner can retain the
// applied rollback state without making it spoofable through the public
// receipt API.
type cutoverApplyOperationResult struct {
	receipt CutoverReceipt
	applied *cutoverAppliedState
}

// cutoverReceiptWriteResult keeps the exact temporary/published inode known
// by the writer.  The binding remains private and is required for every
// readback or cleanup after publication; content equality alone is not an
// ownership proof.
type cutoverReceiptWriteResult struct {
	temporary *cutoverBoundFile
	final     *cutoverBoundFile
}

func applyStagedCutoverOperation(ctx context.Context, bundle preparedCutoverStage) (cutoverApplyOperationResult, error) {
	preflight, seed, err := preflightStagedCutover(ctx, bundle)
	if err != nil {
		return cutoverApplyOperationResult{}, err
	}
	restore, err := prepareCutoverRestoreStages(ctx, preflight)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return cutoverApplyOperationResult{}, err
		}
		blocked, blockedErr := cutoverBlockedReceipt(seed, errorCode(err, "restore_prepare"))
		return cutoverApplyOperationResult{receipt: blocked}, blockedErr
	}
	executed, err := executeCutover(ctx, restore.preflight, restore)
	if err != nil || executed.applied == nil {
		return cutoverApplyOperationResult{receipt: executed.receipt, applied: executed.applied}, err
	}
	// Keep the receipt handed to the writer and the retained rollback state
	// independent.  The later owner may retain the private state while callers
	// inspect or serialize the returned receipt.
	executed.receipt = cloneCutoverReceiptValue(executed.receipt)
	executed.applied.receipt = cloneCutoverReceiptValue(executed.receipt)

	receiptPath := executed.applied.preflight.active.build.paths.cutoverReceipt
	writeResult, writeErr := cutoverReceiptWriter(receiptPath, executed.receipt)
	if writeErr == nil {
		if writeResult.final == nil {
			return cutoverApplyReceiptWriteFailure(ctx, executed.applied, errors.New("receipt publication was not proven"))
		}
		if err := verifyCutoverReceiptFile(receiptPath, executed.receipt, writeResult.final); err == nil {
			if ctx != nil && ctx.Err() != nil {
				cleanupErr := cleanupCutoverReceiptResult(receiptPath, executed.receipt, writeResult)
				return cutoverApplyReceiptWriteFailure(ctx, executed.applied, cleanupErr)
			}
			return cutoverApplyOperationResult{receipt: executed.receipt, applied: executed.applied}, nil
		}
		cleanupErr := cleanupCutoverReceiptResult(receiptPath, executed.receipt, writeResult)
		return cutoverApplyReceiptWriteFailure(ctx, executed.applied, cleanupErr)
	}
	// Keep the original writer error only as a bounded code.  A writer seam that
	// cannot return an exact binding is not authorized to remove a final file.
	var typedWriteErr *cutoverReceiptWriteError
	_ = errors.As(writeErr, &typedWriteErr)
	cleanupErr := cleanupCutoverReceiptResult(receiptPath, executed.receipt, writeResult)
	if typedWriteErr == nil && cleanupErr == nil {
		// A plain seam error carries no ownership binding.  Only an already
		// absent final can be treated as clean; an existing final is unknown.
		if _, err := os.Lstat(receiptPath); err == nil {
			cleanupErr = errors.New("receipt final binding is unknown")
		} else if !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.New("receipt final cleanup is unproven")
		}
	}
	return cutoverApplyReceiptWriteFailure(ctx, executed.applied, cleanupErr)
}

func cutoverApplyReceiptWriteFailure(ctx context.Context, state *cutoverAppliedState, cleanupErr error) (cutoverApplyOperationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rollbackReceipt, rollbackErr := rollbackAppliedCutover(context.WithoutCancel(ctx), state)
	if cleanupErr == nil && rollbackErr == nil {
		return cutoverApplyOperationResult{receipt: cloneCutoverReceiptValue(rollbackReceipt)}, cutoverApplyError("rolled_back")
	}
	return cutoverApplyOperationResult{receipt: cloneCutoverReceiptValue(cutoverRollbackFailedReceipt(state))}, cutoverApplyError("rollback_failed")
}

func writeCutoverReceipt(path string, receipt CutoverReceipt) (cutoverReceiptWriteResult, error) {
	encoded, err := marshalCutoverReceipt(receipt)
	if err != nil {
		return cutoverReceiptWriteResult{}, &cutoverReceiptWriteError{}
	}
	absolute, err := absolutePath(path)
	if err != nil || filepath.Base(absolute) == "." {
		return cutoverReceiptWriteResult{}, &cutoverReceiptWriteError{}
	}
	if _, err := resolveCutoverFreshPath(absolute); err != nil {
		return cutoverReceiptWriteResult{}, &cutoverReceiptWriteError{}
	}
	parent := filepath.Dir(absolute)
	temporary, err := os.CreateTemp(parent, cutoverReceiptTempPattern)
	if err != nil {
		return cutoverReceiptWriteResult{}, &cutoverReceiptWriteError{}
	}
	temporaryName := temporary.Name()
	initialInfo, err := temporary.Stat()
	if err != nil {
		_ = temporary.Close()
		return cutoverReceiptWriteResult{}, &cutoverReceiptWriteError{}
	}
	writeResult := cutoverReceiptWriteResult{temporary: &cutoverBoundFile{path: temporaryName, info: initialInfo, require0600: true}}
	closed := false
	fail := func() (cutoverReceiptWriteResult, error) {
		if !closed {
			_ = temporary.Close()
			closed = true
		}
		owned := cutoverBoundFile{path: temporaryName, info: initialInfo, require0600: true}
		_ = cleanupCutoverReceiptOwnedTemp(owned)
		return writeResult, &cutoverReceiptWriteError{}
	}
	if err := temporary.Chmod(0o600); err != nil {
		return fail()
	}
	if written, err := temporary.Write(encoded); err != nil || written != len(encoded) {
		return fail()
	}
	if err := cutoverReceiptSyncFile(temporary); err != nil {
		return fail()
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return fail()
	}
	closed = true
	temporaryBinding, err := bindCutoverFile(temporaryName, true, false)
	if err != nil {
		return fail()
	}
	writeResult.temporary = &temporaryBinding
	if err := cutoverReceiptPublish(temporaryName, absolute); err != nil {
		published := cutoverReceiptFinalMatches(absolute, temporaryBinding)
		if cleanupErr := cleanupCutoverReceiptTemp(temporaryName, absolute, receipt, published, published, &temporaryBinding); cleanupErr != nil {
			writeResult.final = cutoverReceiptOptionalBinding(temporaryBinding, published)
			return writeResult, &cutoverReceiptWriteError{}
		}
		writeResult.final = cutoverReceiptOptionalBinding(temporaryBinding, published)
		return writeResult, &cutoverReceiptWriteError{}
	}
	if !cutoverReceiptFinalMatches(absolute, temporaryBinding) {
		return cleanupCutoverReceiptFailure(temporaryName, absolute, receipt, true, temporaryBinding)
	}
	if err := cutoverReceiptSyncDirectory(parent); err != nil {
		return cleanupCutoverReceiptFailure(temporaryName, absolute, receipt, true, temporaryBinding)
	}
	if err := cleanupCutoverReceiptTemp(temporaryName, absolute, receipt, true, false, &temporaryBinding); err != nil {
		return cleanupCutoverReceiptFailure(temporaryName, absolute, receipt, true, temporaryBinding)
	}
	if err := cutoverReceiptSyncDirectory(parent); err != nil {
		return cleanupCutoverReceiptFailure(temporaryName, absolute, receipt, true, temporaryBinding)
	}
	if err := verifyCutoverReceiptFile(absolute, receipt, &temporaryBinding); err != nil {
		return cleanupCutoverReceiptFailure(temporaryName, absolute, receipt, true, temporaryBinding)
	}
	writeResult.final = &temporaryBinding
	return writeResult, nil
}

func cleanupCutoverReceiptFailure(temporary, final string, receipt CutoverReceipt, published bool, expected cutoverBoundFile) (cutoverReceiptWriteResult, error) {
	if err := cleanupCutoverReceiptTemp(temporary, final, receipt, published, true, &expected); err != nil {
		return cutoverReceiptWriteResult{temporary: &expected, final: cutoverReceiptOptionalBinding(expected, published)}, &cutoverReceiptWriteError{}
	}
	return cutoverReceiptWriteResult{temporary: &expected, final: cutoverReceiptOptionalBinding(expected, published)}, &cutoverReceiptWriteError{}
}

func cleanupCutoverReceiptResult(path string, receipt CutoverReceipt, result cutoverReceiptWriteResult) error {
	if result.temporary != nil {
		if _, err := os.Lstat(result.temporary.path); err == nil {
			if result.final != nil {
				if err := cleanupCutoverReceiptTemp(result.temporary.path, path, receipt, true, false, result.temporary); err != nil {
					return err
				}
			} else if err := cleanupCutoverReceiptOwnedTemp(*result.temporary); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("receipt temporary cleanup is unproven")
		}
	}
	if result.final != nil {
		return cleanupCutoverReceiptPath(path, receipt, result.final)
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("receipt final binding is unknown")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("receipt final cleanup is unproven")
	}
	return nil
}

func cleanupCutoverReceiptTemp(temporary, final string, receipt CutoverReceipt, published, removeFinal bool, expectedTemp *cutoverBoundFile) error {
	encoded, err := marshalCutoverReceipt(receipt)
	if err != nil {
		return errors.New("receipt cleanup binding is invalid")
	}
	expected := cutoverBoundFile{path: temporary, info: nil, sha256: buildInputBytesSHA256(encoded), bytes: int64(len(encoded)), require0600: true}
	if _, err := os.Lstat(temporary); errors.Is(err, os.ErrNotExist) {
		// A successful publication may have already removed the temporary
		// name; an absent temporary is safe.
	} else if err != nil {
		return errors.New("receipt temporary cleanup failed")
	} else {
		current, err := bindCutoverFile(temporary, true, false)
		if err != nil || current.sha256 != expected.sha256 || current.bytes != expected.bytes {
			return errors.New("receipt temporary binding is unknown")
		}
		if expectedTemp != nil && !os.SameFile(current.info, expectedTemp.info) {
			return errors.New("receipt temporary binding is unknown")
		}
		if published {
			finalInfo, finalErr := os.Lstat(final)
			if finalErr != nil || !os.SameFile(current.info, finalInfo) {
				return errors.New("receipt temporary alias is unknown")
			}
		} else if !cutoverKnownFileIsUnaliased(temporary, current.info) {
			return errors.New("receipt temporary is aliased")
		}
		if err := cutoverReceiptRemove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("receipt temporary cleanup failed")
		}
		if err := ensureCutoverAbsent(temporary); err != nil {
			return err
		}
		if err := cutoverReceiptSyncDirectory(filepath.Dir(temporary)); err != nil {
			return err
		}
	}
	if published && removeFinal {
		return cleanupCutoverReceiptPath(final, receipt, expectedTemp)
	}
	return nil
}

func cleanupCutoverReceiptOwnedTemp(expected cutoverBoundFile) error {
	current, err := bindCutoverFile(expected.path, true, false)
	if err != nil || expected.info == nil || !os.SameFile(current.info, expected.info) || !cutoverKnownFileIsUnaliased(expected.path, current.info) {
		return errors.New("receipt temporary binding is unknown")
	}
	if err := cutoverReceiptRemove(expected.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("receipt temporary cleanup failed")
	}
	if err := ensureCutoverAbsent(expected.path); err != nil {
		return err
	}
	return cutoverReceiptSyncDirectory(filepath.Dir(expected.path))
}

func cleanupCutoverReceiptPath(path string, receipt CutoverReceipt, expected *cutoverBoundFile) error {
	encoded, err := marshalCutoverReceipt(receipt)
	if err != nil {
		return errors.New("receipt cleanup binding is invalid")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("receipt cleanup target is unsafe")
	}
	binding, err := bindCutoverFile(path, true, false)
	if err != nil || binding.sha256 != buildInputBytesSHA256(encoded) || binding.bytes != int64(len(encoded)) || !cutoverKnownFileIsUnaliased(path, binding.info) {
		return errors.New("receipt cleanup target is unknown")
	}
	if expected != nil && !os.SameFile(binding.info, expected.info) {
		return errors.New("receipt cleanup target is unknown")
	}
	if err := cutoverReceiptRemove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("receipt cleanup failed")
	}
	if err := ensureCutoverAbsent(path); err != nil {
		return err
	}
	return cutoverReceiptSyncDirectory(filepath.Dir(path))
}

func cutoverReceiptOptionalBinding(binding cutoverBoundFile, present bool) *cutoverBoundFile {
	if !present {
		return nil
	}
	copy := binding
	return &copy
}

func cloneCutoverReceiptValue(receipt CutoverReceipt) CutoverReceipt {
	receipt.ExclusionReasonCounts = cloneBuildIntMap(receipt.ExclusionReasonCounts)
	receipt.LegacyActorLabelCounts = cloneBuildIntMap(receipt.LegacyActorLabelCounts)
	receipt.SourceDatabaseLogicalSHA256 = cloneBuildStringMap(receipt.SourceDatabaseLogicalSHA256)
	receipt.SourceSchemaSHA256 = cloneBuildStringMap(receipt.SourceSchemaSHA256)
	receipt.SourceDCIClassificationSHA256 = cloneBuildStringMap(receipt.SourceDCIClassificationSHA256)
	receipt.SourceFileSHA256 = cloneBuildStringMap(receipt.SourceFileSHA256)
	receipt.SourceNonDCILogicalSHA256 = cloneBuildStringMap(receipt.SourceNonDCILogicalSHA256)
	receipt.OutputArtifacts = cloneCutoverBuildOutputArtifacts(receipt.OutputArtifacts)
	return receipt
}

func cutoverReceiptFinalMatches(path string, temporary cutoverBoundFile) bool {
	final, err := bindCutoverFile(path, true, false)
	if err != nil || final.sha256 != temporary.sha256 || final.bytes != temporary.bytes || runtime.GOOS != "windows" && final.info.Mode().Perm() != temporary.info.Mode().Perm() {
		return false
	}
	return os.SameFile(final.info, temporary.info)
}

func verifyCutoverReceiptFile(path string, expected CutoverReceipt, expectedBinding *cutoverBoundFile) error {
	actual, err := readCutoverReceipt(path)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		return errors.New("cutover receipt readback failed")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) || !cutoverKnownFileIsUnaliased(path, info) {
		return errors.New("cutover receipt binding is invalid")
	}
	if expectedBinding != nil && !os.SameFile(info, expectedBinding.info) {
		return errors.New("cutover receipt binding is unknown")
	}
	return nil
}
