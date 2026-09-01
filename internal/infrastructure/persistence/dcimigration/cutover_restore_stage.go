package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

const (
	cutoverRestoreDCIName        = ".rencrow-identity-step03-restore-dci.stage"
	cutoverRestoreEventStoreName = ".rencrow-identity-step03-restore-event-store.stage"
	cutoverRestoreL1Name         = ".rencrow-identity-step03-restore-l1.stage"
	cutoverRestoreArchiveName    = ".rencrow-identity-step03-restore-archive.stage"
	cutoverRestoreRuntimeName    = ".rencrow-identity-step03-restore-runtime.stage"
	cutoverRestoreDCIJSONLName   = ".rencrow-identity-step03-restore-dci-jsonl.stage"
)

type cutoverRestoreStageEvidence struct {
	RestoreArtifactSetSHA256 string
	RestoreFileCount         int
	SyncOK                   int
	SidecarZero              int
	NonAlias                 int
	SourceInputsStable       int
}

type preparedCutoverRestoreStages struct {
	preflight preparedCutoverPreflight
	files     []cutoverRestoreStageBinding
	evidence  cutoverRestoreStageEvidence
}

// cutoverRestoreStageBinding keeps the rollback source, fresh stage, and
// untouched active target explicit for the later restore owner.
type cutoverRestoreStageBinding struct {
	role       string
	source     cutoverBoundFile
	target     cutoverBoundFile
	activePath string
}

type cutoverRestoreStageSpec struct {
	role         string
	name         string
	path         string
	activePath   string
	rollbackRole string
	source       cutoverBoundFile
	sqlite       bool
	runtime      bool
}

type cutoverRestoreCreatedFile struct {
	path   string
	sqlite bool
}

var cutoverRestoreCopyFile = func(ctx context.Context, source cutoverBoundFile, target string, targetMode os.FileMode, require0600, sqlite, executable bool) (cutoverBoundFile, bool, error) {
	return copyCutoverStageFile(ctx, source, target, targetMode, require0600, sqlite, executable)
}

var cutoverRestoreAfterCopy = func(string) error { return nil }
var cutoverRestoreFinalRevalidate = verifyCutoverRestoreFinalState

// prepareCutoverRestoreStages creates the six same-parent restore stages that
// a later mutating owner can use to restore the active cohort.  It validates
// the already-prepared cutover bundle before creation, each source while it is
// copied, and the complete copy set afterward. It never replaces, removes, or
// renames an active file.
func prepareCutoverRestoreStages(ctx context.Context, input preparedCutoverPreflight) (result preparedCutoverRestoreStages, err error) {
	if ctx == nil {
		return preparedCutoverRestoreStages{}, cutoverStageError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverRestoreStages{}, err
	}
	preflight, _, err := preflightStagedCutover(ctx, input.staged)
	if err != nil {
		return preparedCutoverRestoreStages{}, cutoverStageCauseError(err, "restore_preflight")
	}
	if !sameCutoverActiveCohort(input.active, preflight.active) || !samePath(input.retiredJSONL, preflight.retiredJSONL) || input.seed.Status != CutoverStatusBlocked || validateCutoverReceipt(input.seed) != nil {
		return preparedCutoverRestoreStages{}, cutoverStageError("restore_preflight")
	}
	specs, err := cutoverRestoreStageSpecs(preflight)
	if err != nil {
		return preparedCutoverRestoreStages{}, cutoverStageError("restore_source")
	}
	if err := validateCutoverRestoreStagePaths(preflight, specs); err != nil {
		return preparedCutoverRestoreStages{}, cutoverStageError("unsafe_path")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverRestoreStages{}, err
	}

	created := make([]cutoverRestoreCreatedFile, 0, len(specs))
	defer func() {
		if err == nil {
			return
		}
		if cleanupErr := cleanupCutoverRestoreStageFiles(created); cleanupErr != nil {
			err = cutoverStageError("cleanup")
		}
	}()

	restoreFiles := make([]cutoverRestoreStageBinding, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return preparedCutoverRestoreStages{}, err
		}
		binding, wasCreated, copyErr := cutoverRestoreCopyFile(ctx, spec.source, spec.path, spec.source.info.Mode().Perm(), false, spec.sqlite, spec.runtime)
		if wasCreated {
			created = append(created, cutoverRestoreCreatedFile{path: spec.path, sqlite: spec.sqlite})
		}
		if copyErr != nil {
			return preparedCutoverRestoreStages{}, cutoverStageCauseError(copyErr, "restore_copy")
		}
		if err := cutoverRestoreAfterCopy(spec.role); err != nil {
			return preparedCutoverRestoreStages{}, cutoverStageCauseError(err, "restore_copy")
		}
		restoreFiles = append(restoreFiles, cutoverRestoreStageBinding{role: spec.role, source: spec.source, target: binding, activePath: spec.activePath})
	}
	if err := verifyCutoverRestoreStages(preflight, restoreFiles); err != nil {
		return preparedCutoverRestoreStages{}, cutoverStageCauseError(err, "restore_verify")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverRestoreStages{}, err
	}
	if err := cutoverRestoreFinalRevalidate(ctx, preflight, restoreFiles); err != nil {
		return preparedCutoverRestoreStages{}, cutoverStageCauseError(err, "source_changed")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverRestoreStages{}, err
	}
	evidence := cutoverRestoreStageEvidence{
		RestoreArtifactSetSHA256: cutoverStageArtifactSetSHA256(cutoverRestoreArtifactBindings(restoreFiles), "restore"),
		RestoreFileCount:         len(restoreFiles),
		SyncOK:                   1,
		SidecarZero:              1,
		NonAlias:                 1,
		SourceInputsStable:       1,
	}
	if evidence.RestoreFileCount != len(specs) || !isLowerHexSHA256(evidence.RestoreArtifactSetSHA256) {
		return preparedCutoverRestoreStages{}, cutoverStageError("restore_hash")
	}
	return preparedCutoverRestoreStages{preflight: preflight, files: restoreFiles, evidence: evidence}, nil
}

func cutoverRestoreStageSpecs(preflight preparedCutoverPreflight) ([]cutoverRestoreStageSpec, error) {
	active := preflight.active
	items := []struct {
		role         string
		name         string
		path         string
		activePath   string
		rollbackRole string
		sqlite       bool
		runtime      bool
	}{
		{role: "restore_dci", name: cutoverRestoreDCIName, path: filepath.Join(filepath.Dir(active.paths.dci), cutoverRestoreDCIName), activePath: active.paths.dci, rollbackRole: "active_dci", sqlite: true},
		{role: "restore_event_store", name: cutoverRestoreEventStoreName, path: filepath.Join(filepath.Dir(active.paths.eventStore), cutoverRestoreEventStoreName), activePath: active.paths.eventStore, rollbackRole: "active_event_store", sqlite: true},
		{role: "restore_l1", name: cutoverRestoreL1Name, path: filepath.Join(filepath.Dir(active.paths.l1), cutoverRestoreL1Name), activePath: active.paths.l1, rollbackRole: "active_l1", sqlite: true},
		{role: "restore_archive", name: cutoverRestoreArchiveName, path: filepath.Join(filepath.Dir(active.paths.archive), cutoverRestoreArchiveName), activePath: active.paths.archive, rollbackRole: "active_archive", sqlite: true},
		{role: "restore_runtime", name: cutoverRestoreRuntimeName, path: filepath.Join(filepath.Dir(active.build.paths.installedRuntime), cutoverRestoreRuntimeName), activePath: active.build.paths.installedRuntime, rollbackRole: "installed_runtime", runtime: true},
		{role: "restore_dci_jsonl", name: cutoverRestoreDCIJSONLName, path: filepath.Join(filepath.Dir(active.paths.dciJSONL), cutoverRestoreDCIJSONLName), activePath: active.paths.dciJSONL, rollbackRole: "active_dci_jsonl"},
	}
	specs := make([]cutoverRestoreStageSpec, 0, len(items))
	for _, item := range items {
		rollback, ok := findCutoverStageRole(preflight.staged.rollbackFiles, item.rollbackRole)
		rollbackName := cutoverRestoreRollbackName(item.rollbackRole)
		if !ok || rollbackName == "" || rollback.source.sqlite != item.sqlite || rollback.target.sqlite != item.sqlite || rollback.target.require0600 || filepath.Base(rollback.target.path) != rollbackName || !samePath(filepath.Dir(rollback.target.path), preflight.staged.rollback) {
			return nil, errors.New("restore source binding is missing")
		}
		if err := verifyCutoverBoundFile(rollback.target); err != nil {
			return nil, err
		}
		if item.runtime {
			if err := validateCutoverRuntimeBinding(rollback.target); err != nil {
				return nil, err
			}
		}
		var activeSource cutoverBoundFile
		var activeOK bool
		if item.runtime {
			activeSource, activeOK = findCutoverBoundFile(active.build.files, item.activePath)
		} else {
			activeSource, activeOK = findCutoverBoundFile(active.files, item.activePath)
		}
		if !activeOK || !sameCutoverBoundFile(rollback.source, activeSource) {
			return nil, errors.New("restore source relationship is invalid")
		}
		specs = append(specs, cutoverRestoreStageSpec{role: item.role, name: item.name, path: item.path, activePath: item.activePath, rollbackRole: item.rollbackRole, source: rollback.target, sqlite: item.sqlite, runtime: item.runtime})
	}
	return specs, nil
}

func validateCutoverRestoreStagePaths(preflight preparedCutoverPreflight, specs []cutoverRestoreStageSpec) error {
	reserved := []string{
		preflight.active.build.paths.buildRoot, preflight.active.build.paths.buildReceipt,
		preflight.active.build.paths.installedRuntime, preflight.active.build.paths.stagedRuntime,
		preflight.active.build.paths.rollbackDir, preflight.active.build.paths.cutoverReceipt,
		preflight.active.paths.dci, preflight.active.paths.dciJSONL, preflight.active.paths.eventStore,
		preflight.active.paths.l1, preflight.active.paths.archive, preflight.retiredJSONL,
	}
	for _, file := range preflight.active.build.files {
		reserved = append(reserved, file.path)
	}
	for _, file := range preflight.active.files {
		reserved = append(reserved, file.path)
	}
	for _, file := range preflight.staged.rollbackFiles {
		reserved = append(reserved, file.target.path)
	}
	for _, file := range preflight.staged.stageFiles {
		reserved = append(reserved, file.target.path)
	}
	targets := make([]string, 0, len(specs))
	for _, spec := range specs {
		resolved, err := resolveCutoverFreshPath(spec.path)
		if err != nil || !samePath(resolved, spec.path) || !samePath(filepath.Dir(resolved), filepath.Dir(spec.activePath)) {
			return errors.New("restore stage path is unsafe")
		}
		if spec.sqlite {
			if err := rejectSQLiteSidecars(resolved); err != nil {
				return err
			}
		}
		targets = append(targets, resolved)
	}
	for _, target := range targets {
		for _, existing := range reserved {
			if samePath(target, existing) || pathWithinOrRoot(target, existing) || pathWithinOrRoot(existing, target) {
				return errors.New("restore stage path aliases a cohort path")
			}
		}
	}
	for left := 0; left < len(targets); left++ {
		for right := left + 1; right < len(targets); right++ {
			if samePath(targets[left], targets[right]) || pathWithinOrRoot(targets[left], targets[right]) || pathWithinOrRoot(targets[right], targets[left]) {
				return errors.New("restore stage paths overlap")
			}
		}
	}
	return nil
}

func verifyCutoverRestoreStages(preflight preparedCutoverPreflight, files []cutoverRestoreStageBinding) error {
	if len(files) != 6 {
		return errors.New("restore stage set is incomplete")
	}
	specs, err := cutoverRestoreStageSpecs(preflight)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(files))
	for _, spec := range specs {
		binding, ok := findCutoverRestoreStageRole(files, spec.role)
		if !ok || binding.role != spec.role || filepath.Base(binding.target.path) != spec.name || !samePath(binding.target.path, spec.path) || !samePath(filepath.Dir(binding.target.path), filepath.Dir(spec.activePath)) {
			return errors.New("restore stage path or role is invalid")
		}
		if _, exists := seen[binding.role]; exists {
			return errors.New("restore stage roles are duplicated")
		}
		seen[binding.role] = struct{}{}
		if !samePath(binding.activePath, spec.activePath) || !sameCutoverBoundFile(binding.source, spec.source) || binding.target.require0600 || binding.target.sqlite != spec.sqlite {
			return errors.New("restore stage source binding is invalid")
		}
		resolved, err := resolveCutoverExistingPath(spec.path)
		if err != nil || !samePath(resolved, binding.target.path) {
			return errors.New("restore stage target is unsafe")
		}
		if err := verifyCutoverBoundFile(binding.target); err != nil {
			return err
		}
		if binding.target.bytes != spec.source.bytes || binding.target.sha256 != spec.source.sha256 || binding.target.info.Mode().Perm() != spec.source.info.Mode().Perm() || os.SameFile(binding.target.info, spec.source.info) {
			return errors.New("restore stage differs from source")
		}
		if spec.runtime {
			if err := validateCutoverRuntimeBinding(binding.target); err != nil {
				return err
			}
		}
	}
	if len(seen) != len(specs) {
		return errors.New("restore stage roles are incomplete")
	}
	allStages := append([]cutoverStageBinding(nil), preflight.staged.stageFiles...)
	allStages = append(allStages, cutoverRestoreArtifactBindings(files)...)
	if err := validateCutoverStageTargetAliases(preflight.active, preflight.staged.rollbackFiles, allStages); err != nil {
		return err
	}
	return nil
}

func verifyCutoverRestoreFinalState(ctx context.Context, preflight preparedCutoverPreflight, files []cutoverRestoreStageBinding) error {
	if ctx == nil {
		return errors.New("invalid context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fresh, _, err := preflightStagedCutover(ctx, preflight.staged)
	if err != nil {
		return err
	}
	if !sameCutoverActiveCohort(preflight.active, fresh.active) || !samePath(preflight.retiredJSONL, fresh.retiredJSONL) {
		return errors.New("cutover cohort changed")
	}
	if err := verifyCutoverRestoreStages(fresh, files); err != nil {
		return err
	}
	return ctx.Err()
}

func cutoverRestoreRollbackName(role string) string {
	switch role {
	case "active_dci":
		return cutoverRollbackDCIFilename
	case "active_event_store":
		return cutoverRollbackEventStoreFilename
	case "active_l1":
		return cutoverRollbackL1Filename
	case "active_archive":
		return cutoverRollbackArchiveFilename
	case "installed_runtime":
		return cutoverRollbackInstalledFilename
	case "active_dci_jsonl":
		return cutoverRollbackDCIJSONLFilename
	default:
		return ""
	}
}

func cutoverRestoreArtifactBindings(files []cutoverRestoreStageBinding) []cutoverStageBinding {
	bindings := make([]cutoverStageBinding, 0, len(files))
	for _, file := range files {
		bindings = append(bindings, cutoverStageBinding{role: file.role, source: file.source, target: file.target})
	}
	return bindings
}

func findCutoverRestoreStageRole(files []cutoverRestoreStageBinding, role string) (cutoverRestoreStageBinding, bool) {
	for _, file := range files {
		if file.role == role {
			return file, true
		}
	}
	return cutoverRestoreStageBinding{}, false
}

func cleanupCutoverRestoreStageFiles(files []cutoverRestoreCreatedFile) error {
	var cleanupErr error
	for _, file := range files {
		cleanupErr = errors.Join(cleanupErr, removeCutoverKnownFile(file.path))
		if file.sqlite {
			for _, suffix := range sqliteSidecarSuffixes {
				cleanupErr = errors.Join(cleanupErr, removeCutoverKnownFile(file.path+suffix))
			}
		}
	}
	return cleanupErr
}
