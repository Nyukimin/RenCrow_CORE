package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// cutoverExecutionResult is intentionally private.  The applied state keeps
// the already-bound rollback sources available to the later rollback owner;
// no filesystem path crosses the public receipt boundary.
type cutoverExecutionResult struct {
	receipt CutoverReceipt
	applied *cutoverAppliedState
}

type cutoverAppliedState struct {
	preflight          preparedCutoverPreflight
	restore            preparedCutoverRestoreStages
	replacements       []cutoverExecutionReplacement
	activeBeforeSHA256 string
	receipt            CutoverReceipt
	mutationStarted    bool
}

type cutoverExecutionReplacement struct {
	role       string
	stage      cutoverStageBinding
	activePath string
	sqlite     bool
	runtime    bool
}

type cutoverExecutionVerification struct {
	preflight    preparedCutoverPreflight
	restore      preparedCutoverRestoreStages
	replacements []cutoverExecutionReplacement
}

// cutoverRollbackTargetMode distinguishes the strict in-operation rollback
// from the later D2d hand-off.  A restarted owner may update a known SQLite
// replacement inode, but it must never authorize a different inode.
type cutoverRollbackTargetMode uint8

const (
	cutoverRollbackStrict cutoverRollbackTargetMode = iota
	cutoverRollbackKnownSQLiteInode
)

var cutoverExecuteForwardReplace = func(role, source, target string) error {
	return atomicReplaceCutoverFile(source, target)
}

var cutoverExecuteRollbackReplace = func(role, source, target string) error {
	return atomicReplaceCutoverFile(source, target)
}

var cutoverExecuteAfterForwardReplace = func(string) error { return nil }
var cutoverExecuteAfterRollbackReplace = func(string) error { return nil }
var cutoverExecuteRemoveFile = func(_ string, binding cutoverBoundFile) error {
	return removeCutoverExecutionBoundFile(binding)
}

// cutoverExecuteSyncDirectory is separate from the stage-copy seam because a
// failed JSONL retirement must enter rollback even when the rename succeeded.
var cutoverExecuteSyncDirectory = func(_ string, path string) error { return syncDirectory(path) }

var cutoverExecuteFinalVerify = verifyCutoverAppliedState
var cutoverExecuteRestoredVerify = verifyCutoverRestoredState

// executeCutover performs the forward mutation only after all D2c-1/D2c-2
// bindings have been revalidated.  Every replacement call is considered a
// mutation before invocation because the platform primitive may rename and
// then report a durability error.
func executeCutover(ctx context.Context, preflight preparedCutoverPreflight, restore preparedCutoverRestoreStages) (cutoverExecutionResult, error) {
	if ctx == nil {
		return cutoverExecutionResult{}, cutoverApplyError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return cutoverExecutionResult{}, err
	}
	fresh, err := validateCutoverExecutionInputs(ctx, preflight, restore)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = cleanupCutoverExecutionRestoreStages(restore)
			return cutoverExecutionResult{}, err
		}
		_ = cleanupCutoverExecutionRestoreStages(restore)
		return cutoverExecutionResult{receipt: cutoverBlockedReceiptValue(preflight.seed, errorCode(err, "preflight"))}, cutoverApplyError(errorCode(err, "preflight"))
	}
	restore.preflight = fresh
	replacements, err := cutoverExecutionReplacements(fresh)
	if err != nil {
		_ = cleanupCutoverExecutionRestoreStages(restore)
		return cutoverExecutionResult{receipt: cutoverBlockedReceiptValue(fresh.seed, "stage_verify")}, cutoverApplyError("stage_verify")
	}
	activeBefore, err := cutoverExecutionArtifactSet(cutoverExecutionActiveBindings(fresh.active), "active_before")
	if err != nil {
		_ = cleanupCutoverExecutionRestoreStages(restore)
		return cutoverExecutionResult{receipt: cutoverBlockedReceiptValue(fresh.seed, "source_changed")}, cutoverApplyError("source_changed")
	}
	state := &cutoverAppliedState{
		preflight:          fresh,
		restore:            restore,
		replacements:       replacements,
		activeBeforeSHA256: activeBefore,
	}
	state.receipt = cutoverExecutionReceipt(fresh, restore, replacements, activeBefore, "")

	for _, replacement := range replacements {
		if err := ctx.Err(); err != nil {
			if state.mutationStarted {
				return cutoverExecutionFailure(ctx, state, "context")
			}
			_ = cleanupCutoverExecutionRestoreStages(restore)
			return cutoverExecutionResult{}, err
		}
		if err := verifyCutoverExecutionReplacementBefore(fresh, replacement); err != nil {
			if state.mutationStarted {
				return cutoverExecutionFailure(ctx, state, "stage_verify")
			}
			_ = cleanupCutoverExecutionRestoreStages(restore)
			return cutoverExecutionResult{receipt: cutoverBlockedReceiptValue(fresh.seed, "stage_verify")}, cutoverApplyError("stage_verify")
		}
		// The call itself may have renamed the source before returning an error.
		state.mutationStarted = true
		if err := cutoverExecuteForwardReplace(replacement.role, replacement.stage.target.path, replacement.activePath); err != nil {
			return cutoverExecutionFailure(ctx, state, "replace")
		}
		if err := verifyCutoverExecutionReplacementAfter(replacement); err != nil {
			return cutoverExecutionFailure(ctx, state, "replace_verify")
		}
		if err := cutoverExecuteAfterForwardReplace(replacement.role); err != nil {
			return cutoverExecutionFailure(ctx, state, "replace")
		}
	}

	if err := ctx.Err(); err != nil {
		return cutoverExecutionFailure(ctx, state, "context")
	}
	if err := cutoverExecutionRetireJSONL(ctx, state); err != nil {
		return cutoverExecutionFailure(ctx, state, "jsonl_retire")
	}
	if err := ctx.Err(); err != nil {
		return cutoverExecutionFailure(ctx, state, "context")
	}
	verification := cutoverExecutionVerification{preflight: fresh, restore: restore, replacements: replacements}
	if err := cutoverExecuteFinalVerify(ctx, verification); err != nil {
		return cutoverExecutionFailure(ctx, state, "applied_verify")
	}
	state.receipt = cutoverExecutionReceipt(fresh, restore, replacements, activeBefore, cutoverExecutionActiveAfterArtifactSet(replacements))
	state.receipt.Status = CutoverStatusApplied
	state.receipt.ErrorCode = ""
	state.receipt.CompletedAt = time.Now().UTC()
	if err := validateCutoverReceipt(state.receipt); err != nil {
		return cutoverExecutionFailure(ctx, state, "receipt_validation")
	}
	return cutoverExecutionResult{receipt: state.receipt, applied: state}, nil
}

func validateCutoverExecutionInputs(ctx context.Context, preflight preparedCutoverPreflight, restore preparedCutoverRestoreStages) (preparedCutoverPreflight, error) {
	if err := validatePreparedCutoverStageShape(preflight.staged); err != nil {
		return preparedCutoverPreflight{}, errors.New("prepared cutover input is invalid")
	}
	if preflight.seed.Status != CutoverStatusBlocked || validateCutoverReceipt(preflight.seed) != nil {
		return preparedCutoverPreflight{}, errors.New("prepared cutover receipt is invalid")
	}
	if len(restore.files) != 6 || restore.evidence.RestoreFileCount != 6 || restore.evidence.SyncOK != 1 || restore.evidence.SidecarZero != 1 || restore.evidence.NonAlias != 1 || restore.evidence.SourceInputsStable != 1 {
		return preparedCutoverPreflight{}, errors.New("prepared restore set is invalid")
	}
	fresh, _, err := preflightStagedCutover(ctx, preflight.staged)
	if err != nil {
		return preparedCutoverPreflight{}, err
	}
	if !sameCutoverActiveCohort(preflight.active, fresh.active) || !samePath(preflight.retiredJSONL, fresh.retiredJSONL) ||
		!sameCutoverActiveCohort(restore.preflight.active, fresh.active) || !samePath(restore.preflight.retiredJSONL, fresh.retiredJSONL) {
		return preparedCutoverPreflight{}, errors.New("cutover cohort changed")
	}
	if restore.evidence.RestoreArtifactSetSHA256 != cutoverStageArtifactSetSHA256(cutoverRestoreArtifactBindings(restore.files), "restore") || !isLowerHexSHA256(restore.evidence.RestoreArtifactSetSHA256) {
		return preparedCutoverPreflight{}, errors.New("restore evidence is invalid")
	}
	if err := verifyCutoverRestoreStages(fresh, restore.files); err != nil {
		return preparedCutoverPreflight{}, err
	}
	return fresh, nil
}

func cutoverExecutionReplacements(preflight preparedCutoverPreflight) ([]cutoverExecutionReplacement, error) {
	items := []struct {
		role       string
		activePath string
		sqlite     bool
		runtime    bool
	}{
		{role: "replacement_dci", activePath: preflight.active.paths.dci, sqlite: true},
		{role: "replacement_event_store", activePath: preflight.active.paths.eventStore, sqlite: true},
		{role: "replacement_l1", activePath: preflight.active.paths.l1, sqlite: true},
		{role: "replacement_archive", activePath: preflight.active.paths.archive, sqlite: true},
		{role: "replacement_runtime", activePath: preflight.active.build.paths.installedRuntime, runtime: true},
	}
	result := make([]cutoverExecutionReplacement, 0, len(items))
	for _, item := range items {
		stage, ok := findCutoverStageRole(preflight.staged.stageFiles, item.role)
		if !ok || stage.target.info == nil || stage.source.info == nil {
			return nil, errors.New("replacement stage is missing")
		}
		result = append(result, cutoverExecutionReplacement{role: item.role, stage: stage, activePath: item.activePath, sqlite: item.sqlite, runtime: item.runtime})
	}
	return result, nil
}

func verifyCutoverExecutionReplacementBefore(preflight preparedCutoverPreflight, replacement cutoverExecutionReplacement) error {
	if err := verifyCutoverBoundFile(replacement.stage.source); err != nil {
		return err
	}
	if err := verifyCutoverBoundFile(replacement.stage.target); err != nil {
		return err
	}
	if os.SameFile(replacement.stage.source.info, replacement.stage.target.info) {
		return errors.New("replacement source aliases stage")
	}
	var old cutoverBoundFile
	var ok bool
	if replacement.runtime {
		old, ok = findCutoverBoundFile(preflight.active.build.files, replacement.activePath)
	} else {
		old, ok = findCutoverBoundFile(preflight.active.files, replacement.activePath)
	}
	if !ok || verifyCutoverBoundFile(old) != nil || os.SameFile(old.info, replacement.stage.target.info) {
		return errors.New("active replacement target is invalid")
	}
	if replacement.sqlite {
		if err := rejectSQLiteSidecars(replacement.activePath); err != nil {
			return err
		}
	}
	return nil
}

func verifyCutoverExecutionReplacementAfter(replacement cutoverExecutionReplacement) error {
	binding, err := bindCutoverFile(replacement.activePath, false, replacement.sqlite)
	if err != nil {
		return err
	}
	if binding.sha256 != replacement.stage.target.sha256 || binding.bytes != replacement.stage.target.bytes ||
		(runtime.GOOS != "windows" && binding.info.Mode().Perm() != replacement.stage.target.info.Mode().Perm()) {
		return errors.New("active replacement differs from stage")
	}
	if replacement.runtime {
		if err := validateCutoverRuntimeBinding(binding); err != nil {
			return err
		}
	}
	return nil
}

func cutoverExecutionRetireJSONL(ctx context.Context, state *cutoverAppliedState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	old, ok := findCutoverBoundFile(state.preflight.active.files, state.preflight.active.paths.dciJSONL)
	if !ok || verifyCutoverBoundFile(old) != nil {
		return errors.New("active JSONL changed")
	}
	if _, err := os.Lstat(state.preflight.retiredJSONL); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("retired JSONL stage is not fresh")
	}
	state.mutationStarted = true
	if err := cutoverExecuteForwardReplace("retire_jsonl", old.path, state.preflight.retiredJSONL); err != nil {
		return err
	}
	if err := cutoverExecuteAfterForwardReplace("retire_jsonl"); err != nil {
		return err
	}
	retired, err := bindCutoverFile(state.preflight.retiredJSONL, false, false)
	if err != nil || retired.sha256 != old.sha256 || retired.bytes != old.bytes || (runtime.GOOS != "windows" && retired.info.Mode().Perm() != old.info.Mode().Perm()) {
		return errors.New("retired JSONL differs from source")
	}
	if err := ensureCutoverAbsent(old.path); err != nil {
		return err
	}
	if err := cutoverExecuteSyncDirectory("jsonl_retire", filepath.Dir(state.preflight.retiredJSONL)); err != nil {
		return err
	}
	if err := cutoverExecuteRemoveFile("retire_jsonl", retired); err != nil {
		return err
	}
	if err := ensureCutoverAbsent(state.preflight.retiredJSONL); err != nil {
		return err
	}
	return cutoverExecuteSyncDirectory("jsonl_remove", filepath.Dir(state.preflight.retiredJSONL))
}

func cutoverExecutionFailure(ctx context.Context, state *cutoverAppliedState, cause string) (cutoverExecutionResult, error) {
	receipt, rollbackErr := rollbackCutoverState(ctx, state, cause, cutoverRollbackStrict)
	if rollbackErr == nil {
		return cutoverExecutionResult{receipt: receipt}, cutoverApplyError("rolled_back")
	}
	return cutoverExecutionResult{receipt: receipt}, cutoverApplyError("rollback_failed")
}

// rollbackAppliedCutover is the private D2d hand-off.  It does not rebuild a
// plan or derive paths from a receipt; only the retained applied state can
// authorize this operation.
func rollbackAppliedCutover(ctx context.Context, state *cutoverAppliedState) (CutoverReceipt, error) {
	if ctx == nil {
		return CutoverReceipt{}, cutoverApplyError("invalid_context")
	}
	rollbackContext := context.WithoutCancel(ctx)
	if state == nil || state.receipt.Status != CutoverStatusApplied {
		return CutoverReceipt{}, cutoverApplyError("invalid_input")
	}
	// D2d must stop the writer and provide that operational evidence before
	// calling this private hand-off.  This function only proves that every
	// target is still the known replacement inode and that rollback sources are
	// intact; it cannot prove a service lifecycle fact itself.
	if err := verifyCutoverAppliedRollbackState(rollbackContext, state); err != nil {
		return CutoverReceipt{}, cutoverApplyError("source_changed")
	}
	if err := verifyCutoverRestoreSourcesForRollback(state); err != nil {
		return CutoverReceipt{}, cutoverApplyError("restore_verify")
	}
	receipt, err := rollbackCutoverState(rollbackContext, state, "rollback_requested", cutoverRollbackKnownSQLiteInode)
	if err != nil {
		return receipt, cutoverApplyError("rollback_failed")
	}
	return receipt, nil
}

func rollbackCutoverState(ctx context.Context, state *cutoverAppliedState, cause string, targetMode cutoverRollbackTargetMode) (CutoverReceipt, error) {
	if ctx == nil || state == nil {
		return cutoverRollbackFailedReceipt(state), errors.New("rollback input is invalid")
	}
	rollbackContext := context.WithoutCancel(ctx)
	var firstErr error
	for _, role := range []string{"restore_dci", "restore_event_store", "restore_l1", "restore_archive", "restore_runtime"} {
		if err := rollbackCutoverFile(rollbackContext, state, role, targetMode); err != nil {
			firstErr = errors.Join(firstErr, err)
		}
	}
	if err := rollbackCutoverJSONL(rollbackContext, state); err != nil {
		firstErr = errors.Join(firstErr, err)
	}
	if err := cleanupCutoverExecutionArtifacts(state); err != nil {
		firstErr = errors.Join(firstErr, err)
	}
	if err := cutoverExecuteRestoredVerify(rollbackContext, state); err != nil {
		firstErr = errors.Join(firstErr, err)
	}
	if firstErr != nil {
		return cutoverRollbackFailedReceipt(state), firstErr
	}
	receipt := state.receipt
	receipt.Status = CutoverStatusRolledBack
	receipt.ErrorCode = "rollback_complete"
	receipt.ActiveAfterArtifactSetSHA256 = ""
	receipt.RestoredArtifactSetSHA256 = state.activeBeforeSHA256
	receipt.JSONLRetired = 0
	receipt.JSONLRestored = 1
	receipt.CompletedAt = time.Now().UTC()
	if err := validateCutoverReceipt(receipt); err != nil {
		return cutoverRollbackFailedReceipt(state), err
	}
	return receipt, nil
}

func rollbackCutoverFile(ctx context.Context, state *cutoverAppliedState, role string, targetMode cutoverRollbackTargetMode) error {
	binding, ok := findCutoverRestoreStageRole(state.restore.files, role)
	if !ok {
		return errors.New("restore role is missing")
	}
	if err := verifyCutoverRestoreSourceForRollback(binding); err != nil {
		return err
	}
	oldTarget, newTarget := cutoverExecutionRollbackTargetBindings(state, binding)
	if targetMode == cutoverRollbackKnownSQLiteInode && binding.source.sqlite {
		if err := validateCutoverKnownSQLiteReplacement(binding.activePath, newTarget); err != nil {
			return err
		}
	} else if err := validateCutoverExecutionTarget(binding.activePath, oldTarget, newTarget); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cutoverExecuteRollbackReplace(role, binding.target.path, binding.activePath); err != nil {
		// The source may have been consumed before a durability error.
		_ = cutoverExecuteAfterRollbackReplace(role)
		return err
	}
	if err := cutoverExecuteAfterRollbackReplace(role); err != nil {
		return err
	}
	return verifyCutoverRestoredBinding(binding)
}

func rollbackCutoverJSONL(ctx context.Context, state *cutoverAppliedState) error {
	binding, ok := findCutoverRestoreStageRole(state.restore.files, "restore_dci_jsonl")
	if !ok {
		return errors.New("restore JSONL role is missing")
	}
	if err := verifyCutoverRestoreSourceForRollback(binding); err != nil {
		return err
	}
	oldTarget, _ := cutoverExecutionRollbackTargetBindings(state, binding)
	if err := validateCutoverExecutionTarget(binding.activePath, oldTarget); err != nil {
		return err
	}
	if current, err := bindCutoverFile(binding.activePath, false, false); err == nil {
		if current.sha256 != binding.source.sha256 || current.bytes != binding.source.bytes || (runtime.GOOS != "windows" && current.info.Mode().Perm() != binding.source.info.Mode().Perm()) {
			return errors.New("active JSONL is not the old binding")
		}
		if err := cutoverExecuteRemoveFile("restore_dci_jsonl", binding.target); err != nil {
			return err
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		// bindCutoverFile intentionally returns a bounded error; inspect the
		// path separately to distinguish a missing target from unsafe content.
		if _, statErr := os.Lstat(binding.activePath); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cutoverExecuteRollbackReplace("restore_dci_jsonl", binding.target.path, binding.activePath); err != nil {
		_ = cutoverExecuteAfterRollbackReplace("restore_dci_jsonl")
		return err
	}
	if err := cutoverExecuteAfterRollbackReplace("restore_dci_jsonl"); err != nil {
		return err
	}
	return verifyCutoverRestoredBinding(binding)
}

func verifyCutoverRestoreSourceForRollback(binding cutoverRestoreStageBinding) error {
	if binding.source.info == nil || binding.target.info == nil || stringsTrim(binding.activePath) == "" {
		return errors.New("restore binding is incomplete")
	}
	if err := verifyCutoverBoundFile(binding.source); err != nil {
		return err
	}
	if err := verifyCutoverBoundFile(binding.target); err != nil {
		return err
	}
	if os.SameFile(binding.source.info, binding.target.info) {
		return errors.New("restore stage aliases rollback source")
	}
	if runtime.GOOS != "windows" && binding.source.info.Mode().Perm() != binding.target.info.Mode().Perm() {
		return errors.New("restore stage mode changed")
	}
	return nil
}

func verifyCutoverRestoredBinding(binding cutoverRestoreStageBinding) error {
	current, err := bindCutoverFile(binding.activePath, false, binding.source.sqlite)
	if err != nil {
		return err
	}
	if current.sha256 != binding.source.sha256 || current.bytes != binding.source.bytes || (runtime.GOOS != "windows" && current.info.Mode().Perm() != binding.source.info.Mode().Perm()) {
		return errors.New("restored file differs from rollback source")
	}
	if binding.runtime() {
		return validateCutoverRuntimeBinding(current)
	}
	return nil
}

func (binding cutoverRestoreStageBinding) runtime() bool {
	return binding.role == "restore_runtime"
}

func cutoverExecutionRollbackTargetBindings(state *cutoverAppliedState, restore cutoverRestoreStageBinding) (cutoverBoundFile, cutoverBoundFile) {
	if state == nil {
		return cutoverBoundFile{}, cutoverBoundFile{}
	}
	var old cutoverBoundFile
	if restore.role == "restore_runtime" {
		old, _ = findCutoverBoundFile(state.preflight.active.build.files, restore.activePath)
	} else {
		old, _ = findCutoverBoundFile(state.preflight.active.files, restore.activePath)
	}
	replacementRole := map[string]string{
		"restore_dci": "replacement_dci", "restore_event_store": "replacement_event_store",
		"restore_l1": "replacement_l1", "restore_archive": "replacement_archive", "restore_runtime": "replacement_runtime",
	}[restore.role]
	var replacement cutoverBoundFile
	if replacementRole != "" {
		if stage, ok := findCutoverStageRole(state.preflight.staged.stageFiles, replacementRole); ok {
			replacement = stage.target
		}
		for _, item := range state.replacements {
			if item.role == replacementRole {
				replacement = item.stage.target
				break
			}
		}
	}
	return old, replacement
}

func validateCutoverExecutionTarget(path string, expected ...cutoverBoundFile) error {
	if err := validateCutoverCanonicalParent(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("cutover target is unsafe")
	}
	if len(expected) == 0 {
		return errors.New("cutover target binding is missing")
	}
	current, err := bindCutoverFile(path, false, expected[0].sqlite)
	if err != nil {
		return err
	}
	for _, want := range expected {
		if want.info != nil && current.sha256 == want.sha256 && current.bytes == want.bytes &&
			(runtime.GOOS == "windows" || current.info.Mode().Perm() == want.info.Mode().Perm()) && os.SameFile(current.info, want.info) && cutoverKnownFileIsUnaliased(path, current.info) {
			// A live target may match the old binding at its original path,
			// or the replacement binding after that stage has been consumed by
			// rename.  If the distinct replacement path is still present, a
			// hardlink substitution could otherwise masquerade as either
			// legitimate inode and rollback could overwrite an unknown file.
			if !samePath(want.path, path) {
				if _, err := os.Lstat(want.path); err == nil {
					return errors.New("cutover target binding is aliased")
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			return nil
		}
	}
	return errors.New("cutover target binding is unknown")
}

func validateCutoverKnownSQLiteReplacement(path string, replacement cutoverBoundFile) error {
	if replacement.info == nil || !replacement.sqlite {
		return errors.New("known SQLite replacement binding is incomplete")
	}
	if err := validateCutoverCanonicalParent(path); err != nil {
		return err
	}
	if err := rejectSQLiteSidecars(path); err != nil {
		return err
	}
	current, err := bindCutoverFile(path, false, true)
	if err != nil {
		return err
	}
	if !os.SameFile(current.info, replacement.info) ||
		(runtime.GOOS != "windows" && current.info.Mode().Perm() != replacement.info.Mode().Perm()) ||
		!cutoverKnownFileIsUnaliased(path, current.info) {
		return errors.New("known SQLite replacement binding is invalid")
	}
	if !samePath(path, replacement.path) {
		if _, err := os.Lstat(replacement.path); err == nil {
			return errors.New("known SQLite replacement is aliased")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func cleanupCutoverExecutionArtifacts(state *cutoverAppliedState) error {
	var cleanupErr error
	for _, replacement := range state.replacements {
		cleanupErr = errors.Join(cleanupErr, cutoverExecuteRemoveFile("replacement_cleanup", replacement.stage.target))
		if replacement.sqlite {
			for _, suffix := range sqliteSidecarSuffixes {
				if _, err := os.Lstat(replacement.stage.target.path + suffix); err == nil {
					cleanupErr = errors.Join(cleanupErr, errors.New("unexpected SQLite sidecar remains"))
				} else if !errors.Is(err, os.ErrNotExist) {
					cleanupErr = errors.Join(cleanupErr, err)
				}
			}
		}
	}
	if old, ok := findCutoverBoundFile(state.preflight.active.files, state.preflight.active.paths.dciJSONL); ok {
		expected := old
		expected.path = state.preflight.retiredJSONL
		cleanupErr = errors.Join(cleanupErr, cutoverExecuteRemoveFile("retired_cleanup", expected))
	}
	return cleanupErr
}

func removeCutoverExecutionBoundFile(expected cutoverBoundFile) error {
	if expected.path == "" || expected.info == nil {
		return errors.New("cleanup binding is incomplete")
	}
	if _, err := os.Lstat(expected.path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if expected.sqlite {
		if err := rejectSQLiteSidecars(expected.path); err != nil {
			return err
		}
	}
	current, err := bindCutoverFile(expected.path, expected.require0600, expected.sqlite)
	if err != nil {
		return err
	}
	if !cutoverKnownFileIsUnaliased(expected.path, current.info) {
		return errors.New("cleanup target is aliased")
	}
	if !os.SameFile(current.info, expected.info) || current.sha256 != expected.sha256 || current.bytes != expected.bytes ||
		(runtime.GOOS != "windows" && current.info.Mode().Perm() != expected.info.Mode().Perm()) {
		return errors.New("cleanup target binding is unknown")
	}
	return removeCutoverKnownFile(expected.path)
}

func cleanupCutoverExecutionRestoreStages(restore preparedCutoverRestoreStages) error {
	var cleanupErr error
	for _, file := range restore.files {
		if file.target.info == nil || !cutoverExecutionRestoreStagePath(file) {
			return errors.New("restore cleanup path is unsafe")
		}
		cleanupErr = errors.Join(cleanupErr, removeCutoverExecutionBoundFile(file.target))
		if file.source.sqlite {
			for _, suffix := range sqliteSidecarSuffixes {
				if _, err := os.Lstat(file.target.path + suffix); err == nil {
					cleanupErr = errors.Join(cleanupErr, errors.New("unexpected SQLite sidecar remains"))
				} else if !errors.Is(err, os.ErrNotExist) {
					cleanupErr = errors.Join(cleanupErr, err)
				}
			}
		}
	}
	return cleanupErr
}

func cutoverExecutionRestoreStagePath(file cutoverRestoreStageBinding) bool {
	if file.target.path == "" || filepath.Base(file.target.path) != cutoverRestoreStageName(file.role) || !samePath(filepath.Dir(file.target.path), filepath.Dir(file.activePath)) || validateCutoverCanonicalParent(file.target.path) != nil || validateCutoverCanonicalParent(file.activePath) != nil {
		return false
	}
	return true
}

func cutoverRestoreStageName(role string) string {
	switch role {
	case "restore_dci":
		return cutoverRestoreDCIName
	case "restore_event_store":
		return cutoverRestoreEventStoreName
	case "restore_l1":
		return cutoverRestoreL1Name
	case "restore_archive":
		return cutoverRestoreArchiveName
	case "restore_runtime":
		return cutoverRestoreRuntimeName
	case "restore_dci_jsonl":
		return cutoverRestoreDCIJSONLName
	default:
		return ""
	}
}

func cutoverExecutionActiveBindings(active preparedCutoverActiveCohort) []cutoverStageBinding {
	items := []struct {
		role string
		path string
		file cutoverBoundFile
	}{
		{role: "active_dci", path: active.paths.dci},
		{role: "active_dci_jsonl", path: active.paths.dciJSONL},
		{role: "active_event_store", path: active.paths.eventStore},
		{role: "active_l1", path: active.paths.l1},
		{role: "active_archive", path: active.paths.archive},
		{role: "installed_runtime", path: active.build.paths.installedRuntime},
	}
	result := make([]cutoverStageBinding, 0, len(items))
	for _, item := range items {
		var ok bool
		if item.role == "installed_runtime" {
			item.file, ok = findCutoverBoundFile(active.build.files, item.path)
		} else {
			item.file, ok = findCutoverBoundFile(active.files, item.path)
		}
		if ok {
			result = append(result, cutoverStageBinding{role: item.role, source: item.file, target: item.file})
		}
	}
	return result
}

func cutoverExecutionArtifactSet(files []cutoverStageBinding, method string) (string, error) {
	if err := validateCutoverExecutionArtifactRoles(files, method); err != nil {
		return "", err
	}
	for _, file := range files {
		if err := verifyCutoverBoundFile(file.target); err != nil {
			return "", err
		}
	}
	hash := cutoverStageArtifactSetSHA256(files, method)
	if !isLowerHexSHA256(hash) {
		return "", errors.New("artifact set hash is invalid")
	}
	return hash, nil
}

func validateCutoverExecutionArtifactRoles(files []cutoverStageBinding, method string) error {
	roles := map[string]struct{}{}
	switch method {
	case "active_before", "restored":
		roles = map[string]struct{}{
			"active_dci": {}, "active_dci_jsonl": {}, "active_event_store": {},
			"active_l1": {}, "active_archive": {}, "installed_runtime": {},
		}
	case "active_after":
		roles = map[string]struct{}{
			"active_dci": {}, "active_event_store": {}, "active_l1": {},
			"active_archive": {}, "installed_runtime": {},
		}
	default:
		return errors.New("artifact set method is invalid")
	}
	if len(files) != len(roles) {
		return errors.New("artifact set role set is invalid")
	}
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if _, ok := roles[file.role]; !ok {
			return errors.New("artifact set role set is invalid")
		}
		if _, ok := seen[file.role]; ok {
			return errors.New("artifact set role set is duplicated")
		}
		seen[file.role] = struct{}{}
	}
	if len(seen) != len(roles) {
		return errors.New("artifact set role set is incomplete")
	}
	return nil
}

func cutoverExecutionActiveAfterArtifactSet(replacements []cutoverExecutionReplacement) string {
	roles := map[string]string{
		"replacement_dci": "active_dci", "replacement_event_store": "active_event_store",
		"replacement_l1": "active_l1", "replacement_archive": "active_archive",
		"replacement_runtime": "installed_runtime",
	}
	bindings := make([]cutoverStageBinding, 0, len(replacements))
	for _, replacement := range replacements {
		role := roles[replacement.role]
		if role == "" {
			continue
		}
		bindings = append(bindings, cutoverStageBinding{role: role, source: replacement.stage.source, target: replacement.stage.target})
	}
	if validateCutoverExecutionArtifactRoles(bindings, "active_after") != nil {
		return ""
	}
	return cutoverStageArtifactSetSHA256(bindings, "active_after")
}

func cutoverExecutionReceipt(preflight preparedCutoverPreflight, restore preparedCutoverRestoreStages, replacements []cutoverExecutionReplacement, activeBefore, activeAfter string) CutoverReceipt {
	receipt := newCutoverReceiptSeed(preflight.active)
	receipt.OutputArtifacts = cloneCutoverBuildOutputArtifacts(preflight.active.build.buildReceipt.OutputArtifacts)
	receipt.OutputArtifactSetSHA256 = preflight.active.build.buildReceipt.OutputArtifactSetSHA256
	receipt.RollbackArtifactSetSHA256 = preflight.staged.evidence.RollbackArtifactSetSHA256
	receipt.ReplacementArtifactSetSHA256 = preflight.staged.evidence.ReplacementArtifactSetSHA256
	receipt.ActiveBeforeArtifactSetSHA256 = activeBefore
	receipt.ActiveAfterArtifactSetSHA256 = activeAfter
	receipt.OldRuntimeSHA256 = cutoverExecutionRuntimeSHA(preflight.active, false)
	receipt.NewRuntimeSHA256 = cutoverExecutionRuntimeSHA(preflight.active, true)
	receipt.RollbackFileCount = 7
	receipt.ReplacementFileCount = 5
	receipt.ActiveFileCount = 5
	receipt.QuickCheckOK = 1
	receipt.ForeignKeyViolations = 0
	receipt.SidecarZero = 1
	receipt.LegacyKeyMarkers = 0
	receipt.OrphanActionRefs = 0
	receipt.SourceInputsStable = 1
	receipt.DCI = preflight.active.build.buildReceipt.DCI
	receipt.EventStore = preflight.active.build.buildReceipt.EventStore
	receipt.L1 = preflight.active.build.buildReceipt.L1
	receipt.Archive = preflight.active.build.buildReceipt.Archive
	if activeAfter != "" {
		receipt.JSONLRetired = 1
	}
	return receipt
}

func cutoverExecutionRuntimeSHA(active preparedCutoverActiveCohort, staged bool) string {
	path := active.build.paths.installedRuntime
	if staged {
		path = active.build.paths.stagedRuntime
	}
	binding, ok := findCutoverBoundFile(active.build.files, path)
	if !ok {
		return ""
	}
	return binding.sha256
}

func cloneCutoverBuildOutputArtifacts(input map[string]BuildOutputArtifact) map[string]BuildOutputArtifact {
	if input == nil {
		return nil
	}
	output := make(map[string]BuildOutputArtifact, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cutoverBlockedReceiptValue(seed CutoverReceipt, code string) CutoverReceipt {
	seed.Status = CutoverStatusBlocked
	if !validErrorCode(code) {
		code = "cutover_apply"
	}
	seed.ErrorCode = code
	clearCutoverPostMutationClaims(&seed)
	seed.CompletedAt = time.Now().UTC()
	if err := validateCutoverReceipt(seed); err != nil {
		return CutoverReceipt{}
	}
	return seed
}

func cutoverBlockedReceipt(seed CutoverReceipt, code string) (CutoverReceipt, error) {
	receipt := cutoverBlockedReceiptValue(seed, code)
	if receipt.SchemaVersion == "" {
		return CutoverReceipt{}, cutoverApplyError("receipt_validation")
	}
	return receipt, cutoverApplyError(receipt.ErrorCode)
}

func cutoverRollbackFailedReceipt(state *cutoverAppliedState) CutoverReceipt {
	if state == nil {
		return CutoverReceipt{}
	}
	receipt := state.receipt
	receipt.Status = CutoverStatusRollbackFailed
	receipt.ErrorCode = CutoverStatusRollbackFailed
	clearCutoverPostMutationClaims(&receipt)
	receipt.CompletedAt = time.Now().UTC()
	if validateCutoverReceipt(receipt) != nil {
		return CutoverReceipt{}
	}
	return receipt
}

func verifyCutoverAppliedState(ctx context.Context, verification cutoverExecutionVerification) error {
	if ctx == nil {
		return errors.New("invalid context")
	}
	if len(verification.replacements) != 5 || len(verification.restore.files) != 6 {
		return errors.New("applied cohort is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, replacement := range verification.replacements {
		if err := verifyCutoverExecutionReplacementAfter(replacement); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(verification.preflight.active.paths.dciJSONL); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("active JSONL remains")
	}
	if _, err := os.Lstat(verification.preflight.retiredJSONL); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("retired JSONL remains")
	}
	for _, replacement := range verification.replacements {
		if !replacement.sqlite {
			continue
		}
		db, err := openSQLiteReadOnly(ctx, replacement.activePath)
		if err != nil {
			return err
		}
		quickErr := captureSQLiteQuickCheck(ctx, db)
		fk, fkErr := countForeignKeyViolations(ctx, db)
		closeErr := db.Close()
		if quickErr != nil || fkErr != nil || closeErr != nil || fk != 0 {
			return errors.New("active SQLite health is invalid")
		}
		if err := rejectSQLiteSidecars(replacement.activePath); err != nil {
			return err
		}
		if err := verifyCutoverExecutionReplacementAfter(replacement); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func verifyCutoverAppliedRollbackState(ctx context.Context, state *cutoverAppliedState) error {
	if ctx == nil || state == nil || len(state.replacements) != 5 || len(state.restore.files) != 6 {
		return errors.New("applied rollback cohort is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, replacement := range state.replacements {
		_, newTarget := cutoverExecutionRollbackTargetBindings(state, cutoverRestoreStageBinding{role: cutoverExecutionRestoreRole(replacement.role), activePath: replacement.activePath})
		if replacement.sqlite {
			if err := validateCutoverKnownSQLiteReplacement(replacement.activePath, newTarget); err != nil {
				return err
			}
		} else if err := validateCutoverExecutionTarget(replacement.activePath, newTarget); err != nil {
			return err
		}
		if err := ensureCutoverAbsent(replacement.stage.target.path); err != nil {
			return err
		}
	}
	if err := ensureCutoverAbsent(state.preflight.active.paths.dciJSONL); err != nil {
		return err
	}
	if err := ensureCutoverAbsent(state.preflight.retiredJSONL); err != nil {
		return err
	}
	return ctx.Err()
}

func cutoverExecutionRestoreRole(replacementRole string) string {
	return map[string]string{
		"replacement_dci":         "restore_dci",
		"replacement_event_store": "restore_event_store",
		"replacement_l1":          "restore_l1",
		"replacement_archive":     "restore_archive",
		"replacement_runtime":     "restore_runtime",
	}[replacementRole]
}

func verifyCutoverRestoreSourcesForRollback(state *cutoverAppliedState) error {
	if state == nil || len(state.restore.files) != 6 {
		return errors.New("restore source set is incomplete")
	}
	for _, file := range state.restore.files {
		if err := verifyCutoverRestoreSourceForRollback(file); err != nil {
			return err
		}
	}
	return nil
}

func verifyCutoverRestoredState(ctx context.Context, state *cutoverAppliedState) error {
	if ctx == nil {
		return errors.New("invalid context")
	}
	if state == nil || len(state.restore.files) != 6 || len(state.replacements) != 5 {
		return errors.New("restored cohort is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, file := range state.restore.files {
		if err := verifyCutoverRestoredBinding(file); err != nil {
			return err
		}
		if file.source.sqlite {
			if err := rejectSQLiteSidecars(file.activePath); err != nil {
				return err
			}
			db, err := openSQLiteReadOnly(ctx, file.activePath)
			if err != nil {
				return err
			}
			quickErr := captureSQLiteQuickCheck(ctx, db)
			fk, fkErr := countForeignKeyViolations(ctx, db)
			closeErr := db.Close()
			if quickErr != nil || fkErr != nil || closeErr != nil || fk != 0 {
				return errors.New("restored SQLite health is invalid")
			}
		}
		if err := ensureCutoverAbsent(file.target.path); err != nil {
			return err
		}
	}
	for _, replacement := range state.replacements {
		if _, err := os.Lstat(replacement.stage.target.path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("replacement stage remains")
		}
	}
	if _, err := os.Lstat(state.preflight.retiredJSONL); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("retired JSONL remains")
	}
	restoredHash, err := cutoverExecutionArtifactSet(cutoverExecutionRestoredArtifactBindings(state.restore.files), "active_before")
	if err != nil || restoredHash != state.activeBeforeSHA256 {
		return errors.New("restored artifact set differs from active before")
	}
	return nil
}

func cutoverExecutionRestoredArtifactBindings(files []cutoverRestoreStageBinding) []cutoverStageBinding {
	roles := map[string]string{
		"restore_dci": "active_dci", "restore_dci_jsonl": "active_dci_jsonl",
		"restore_event_store": "active_event_store", "restore_l1": "active_l1",
		"restore_archive": "active_archive", "restore_runtime": "installed_runtime",
	}
	result := make([]cutoverStageBinding, 0, len(files))
	for _, file := range files {
		role, ok := roles[file.role]
		if !ok {
			continue
		}
		result = append(result, cutoverStageBinding{role: role, source: file.source, target: file.source})
	}
	return result
}

// stringsTrim keeps this file independent of payload-bearing formatting
// helpers; it only checks whether a private path field is present.
func stringsTrim(value string) string {
	for _, r := range value {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return value
		}
	}
	return ""
}
