package dcimigration

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

const (
	cutoverRollbackDCIFilename        = "active-dci.db"
	cutoverRollbackDCIJSONLFilename   = "active-dci-search-trace.jsonl"
	cutoverRollbackEventStoreFilename = "active-event-store.db"
	cutoverRollbackL1Filename         = "active-l1.db"
	cutoverRollbackArchiveFilename    = "active-archive.db"
	cutoverRollbackInstalledFilename  = "installed-runtime"
	cutoverRollbackStagedFilename     = "staged-runtime"

	cutoverStageDCIName        = ".rencrow-identity-step03-dci.stage"
	cutoverStageEventStoreName = ".rencrow-identity-step03-event-store.stage"
	cutoverStageL1Name         = ".rencrow-identity-step03-l1.stage"
	cutoverStageArchiveName    = ".rencrow-identity-step03-archive.stage"
	cutoverStageRuntimeName    = ".rencrow-identity-step03-runtime.stage"

	cutoverRollbackTempPrefix = ".rencrow-identity-step03-rollback-"
)

type cutoverStagePaths struct {
	dci        string
	eventStore string
	l1         string
	archive    string
	runtime    string
}

type cutoverStageBinding struct {
	role   string
	source cutoverBoundFile
	target cutoverBoundFile
}

// cutoverStageEvidence is private and path-free.  It records only aggregate
// artifact hashes, fixed counts, and measured safety counters for the next
// cutover owner.
type cutoverStageEvidence struct {
	RollbackArtifactSetSHA256    string
	ReplacementArtifactSetSHA256 string
	RollbackFileCount            int
	ReplacementFileCount         int
	RollbackRootModeOK           int
	ReplacementModeOK            int
	SourceInputsStable           int
	SidecarZero                  int
	NonAlias                     int
	SyncOK                       int
}

// preparedCutoverStage is private because it contains filesystem bindings.
// The active cohort remains the only source of migration meaning; this bundle
// adds only rollback/stage paths and measured file bindings.
type preparedCutoverStage struct {
	active        preparedCutoverActiveCohort
	rollback      string
	rollbackFiles []cutoverStageBinding
	stageFiles    []cutoverStageBinding
	evidence      cutoverStageEvidence
}

type cutoverStageSource struct {
	role    string
	name    string
	source  cutoverBoundFile
	sqlite  bool
	runtime bool
}

var cutoverStageCopyFile = copyCutoverStageFile
var cutoverStageSyncFile = func(file *os.File) error { return file.Sync() }
var cutoverStageSyncDirectory = syncDirectory
var cutoverStageRename = os.Rename
var cutoverStageAfterCopy = func(string) error { return nil }
var cutoverStageFinalRevalidate = verifyCutoverStageFinalState

// stageCutoverCohort creates a durable rollback snapshot and same-filesystem
// replacement stages without touching any active target.  The returned bundle
// is private and can be consumed by a later cutover owner.
func stageCutoverCohort(ctx context.Context, prepared preparedCutoverActiveCohort) (result preparedCutoverStage, err error) {
	if ctx == nil {
		return preparedCutoverStage{}, cutoverStageError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverStage{}, err
	}
	active, err := revalidateCutoverStageCohort(ctx, prepared, false)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverStage{}, err
		}
		return preparedCutoverStage{}, cutoverStageCauseError(err, "cohort")
	}
	rollbackPath := active.build.paths.rollbackDir
	stagePaths, err := resolveCutoverStagePaths(active)
	if err != nil {
		return preparedCutoverStage{}, cutoverStageCauseError(err, "unsafe_path")
	}
	rollbackSources, err := cutoverRollbackSources(active)
	if err != nil {
		return preparedCutoverStage{}, cutoverStageCauseError(err, "cohort")
	}
	stageSources, err := cutoverReplacementSources(active, stagePaths)
	if err != nil {
		return preparedCutoverStage{}, cutoverStageCauseError(err, "cohort")
	}

	rollbackTemp, err := createCutoverRollbackTemp(ctx, rollbackPath)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverStage{}, err
		}
		return preparedCutoverStage{}, cutoverStageCauseError(err, "rollback_root")
	}
	rollbackFinalized := false
	createdRollbackFiles := make([]string, 0, len(rollbackSources))
	createdStageFiles := make([]string, 0, len(stageSources))
	defer func() {
		if err == nil {
			return
		}
		cleanupErr := cleanupCutoverStageArtifacts(rollbackTemp, rollbackPath, rollbackFinalized, createdRollbackFiles, createdStageFiles, stageSources)
		if cleanupErr != nil {
			err = cutoverStageError("cleanup")
		}
	}()

	rollbackFiles := make([]cutoverStageBinding, 0, len(rollbackSources))
	for _, source := range rollbackSources {
		if err := ctx.Err(); err != nil {
			return preparedCutoverStage{}, err
		}
		target := filepath.Join(rollbackTemp, source.name)
		binding, created, copyErr := cutoverStageCopyFile(ctx, source.source, target, source.source.info.Mode().Perm(), false, source.sqlite, source.runtime)
		if created {
			createdRollbackFiles = append(createdRollbackFiles, target)
		}
		if copyErr != nil {
			return preparedCutoverStage{}, cutoverStageCauseError(copyErr, "rollback_copy")
		}
		if err := cutoverStageAfterCopy(source.role); err != nil {
			return preparedCutoverStage{}, cutoverStageCauseError(err, "rollback_copy")
		}
		rollbackFiles = append(rollbackFiles, cutoverStageBinding{role: source.role, source: source.source, target: binding})
	}

	stageFiles := make([]cutoverStageBinding, 0, len(stageSources))
	for _, source := range stageSources {
		if err := ctx.Err(); err != nil {
			return preparedCutoverStage{}, err
		}
		targetMode := os.FileMode(0o600)
		require0600 := true
		if source.runtime {
			targetMode = source.source.info.Mode().Perm()
			require0600 = false
		}
		binding, created, copyErr := cutoverStageCopyFile(ctx, source.source, source.name, targetMode, require0600, source.sqlite, source.runtime)
		if created {
			createdStageFiles = append(createdStageFiles, source.name)
		}
		if copyErr != nil {
			return preparedCutoverStage{}, cutoverStageCauseError(copyErr, "stage_copy")
		}
		if err := cutoverStageAfterCopy(source.role); err != nil {
			return preparedCutoverStage{}, cutoverStageCauseError(err, "stage_copy")
		}
		stageFiles = append(stageFiles, cutoverStageBinding{role: source.role, source: source.source, target: binding})
	}

	if err := verifyCutoverRollbackRoot(rollbackTemp, rollbackFiles); err != nil {
		return preparedCutoverStage{}, cutoverStageCauseError(err, "rollback_verify")
	}
	if err := verifyCutoverReplacementStages(stageFiles); err != nil {
		return preparedCutoverStage{}, cutoverStageCauseError(err, "stage_verify")
	}
	if err := validateCutoverStageTargetAliases(active, rollbackFiles, stageFiles); err != nil {
		return preparedCutoverStage{}, cutoverStageCauseError(err, "unsafe_path")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverStage{}, err
	}
	if _, err := revalidateCutoverStageCohort(ctx, active, false); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverStage{}, err
		}
		return preparedCutoverStage{}, cutoverStageCauseError(err, "source_changed")
	}

	if err := cutoverStageRename(rollbackTemp, rollbackPath); err != nil {
		if _, statErr := os.Lstat(rollbackPath); statErr == nil {
			rollbackFinalized = true
		}
		return preparedCutoverStage{}, cutoverStageError("rollback_finalize")
	}
	rollbackFinalized = true
	if err := cutoverStageSyncDirectory(filepath.Dir(rollbackPath)); err != nil {
		return preparedCutoverStage{}, cutoverStageError("rollback_sync")
	}
	rollbackFiles, err = rebindCutoverRollbackFiles(rollbackPath, rollbackFiles)
	if err != nil {
		return preparedCutoverStage{}, cutoverStageCauseError(err, "rollback_verify")
	}
	if err := cutoverStageFinalRevalidate(ctx, active, rollbackFiles, stageFiles); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverStage{}, err
		}
		return preparedCutoverStage{}, cutoverStageCauseError(err, "final_verify")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverStage{}, err
	}

	evidence := cutoverStageEvidence{
		RollbackArtifactSetSHA256:    cutoverStageArtifactSetSHA256(rollbackFiles, "rollback"),
		ReplacementArtifactSetSHA256: cutoverStageArtifactSetSHA256(stageFiles, "replacement"),
		RollbackFileCount:            len(rollbackFiles),
		ReplacementFileCount:         len(stageFiles),
		RollbackRootModeOK:           1,
		ReplacementModeOK:            1,
		SourceInputsStable:           1,
		SidecarZero:                  1,
		NonAlias:                     1,
		SyncOK:                       1,
	}
	if !isLowerHexSHA256(evidence.RollbackArtifactSetSHA256) || !isLowerHexSHA256(evidence.ReplacementArtifactSetSHA256) {
		return preparedCutoverStage{}, cutoverStageError("artifact_hash")
	}
	return preparedCutoverStage{active: active, rollback: rollbackPath, rollbackFiles: rollbackFiles, stageFiles: stageFiles, evidence: evidence}, nil
}

func resolveCutoverStagePaths(active preparedCutoverActiveCohort) (cutoverStagePaths, error) {
	paths := cutoverStagePaths{
		dci:        filepath.Join(filepath.Dir(active.paths.dci), cutoverStageDCIName),
		eventStore: filepath.Join(filepath.Dir(active.paths.eventStore), cutoverStageEventStoreName),
		l1:         filepath.Join(filepath.Dir(active.paths.l1), cutoverStageL1Name),
		archive:    filepath.Join(filepath.Dir(active.paths.archive), cutoverStageArchiveName),
		runtime:    filepath.Join(filepath.Dir(active.build.paths.installedRuntime), cutoverStageRuntimeName),
	}
	values := []struct {
		path   string
		sqlite bool
	}{
		{path: paths.dci, sqlite: true}, {path: paths.eventStore, sqlite: true},
		{path: paths.l1, sqlite: true}, {path: paths.archive, sqlite: true},
		{path: paths.runtime, sqlite: false},
	}
	for _, value := range values {
		if _, err := resolveCutoverFreshPath(value.path); err != nil {
			return cutoverStagePaths{}, err
		}
		if value.sqlite {
			if err := rejectSQLiteSidecars(value.path); err != nil {
				return cutoverStagePaths{}, err
			}
		}
	}
	existing := make([]string, 0, len(active.files)+len(active.build.files)+8)
	for _, file := range active.files {
		existing = append(existing, file.path)
	}
	for _, file := range active.build.files {
		existing = append(existing, file.path)
	}
	existing = append(existing, active.build.paths.buildRoot, active.build.paths.rollbackDir, active.build.paths.cutoverReceipt)
	stages := []string{paths.dci, paths.eventStore, paths.l1, paths.archive, paths.runtime}
	for _, stage := range stages {
		for _, other := range existing {
			if samePath(stage, other) || pathWithinOrRoot(stage, other) || pathWithinOrRoot(other, stage) {
				return cutoverStagePaths{}, errors.New("stage path aliases an existing cohort path")
			}
		}
		for _, other := range stages {
			if stage != other && (samePath(stage, other) || pathWithinOrRoot(stage, other) || pathWithinOrRoot(other, stage)) {
				return cutoverStagePaths{}, errors.New("stage paths overlap")
			}
		}
	}
	return paths, nil
}

func cutoverRollbackSources(active preparedCutoverActiveCohort) ([]cutoverStageSource, error) {
	activeSources := []struct {
		role   string
		name   string
		path   string
		sqlite bool
	}{
		{role: "active_dci", name: cutoverRollbackDCIFilename, path: active.paths.dci, sqlite: true},
		{role: "active_dci_jsonl", name: cutoverRollbackDCIJSONLFilename, path: active.paths.dciJSONL},
		{role: "active_event_store", name: cutoverRollbackEventStoreFilename, path: active.paths.eventStore, sqlite: true},
		{role: "active_l1", name: cutoverRollbackL1Filename, path: active.paths.l1, sqlite: true},
		{role: "active_archive", name: cutoverRollbackArchiveFilename, path: active.paths.archive, sqlite: true},
	}
	sources := make([]cutoverStageSource, 0, 7)
	for _, item := range activeSources {
		binding, ok := findCutoverBoundFile(active.files, item.path)
		if !ok {
			return nil, errors.New("active source binding is missing")
		}
		sources = append(sources, cutoverStageSource{role: item.role, name: item.name, source: binding, sqlite: item.sqlite})
	}
	for _, item := range []struct {
		role string
		name string
		path string
	}{
		{role: "installed_runtime", name: cutoverRollbackInstalledFilename, path: active.build.paths.installedRuntime},
		{role: "staged_runtime", name: cutoverRollbackStagedFilename, path: active.build.paths.stagedRuntime},
	} {
		binding, ok := findCutoverBoundFile(active.build.files, item.path)
		if !ok {
			return nil, errors.New("runtime binding is missing")
		}
		if err := validateCutoverRuntimeBinding(binding); err != nil {
			return nil, err
		}
		sources = append(sources, cutoverStageSource{role: item.role, name: item.name, source: binding, runtime: true})
	}
	return sources, nil
}

func cutoverReplacementSources(active preparedCutoverActiveCohort, paths cutoverStagePaths) ([]cutoverStageSource, error) {
	targets := []struct {
		role   string
		output string
		path   string
	}{
		{role: "replacement_dci", output: buildOutputDCIRole, path: paths.dci},
		{role: "replacement_event_store", output: buildOutputEventStoreRole, path: paths.eventStore},
		{role: "replacement_l1", output: buildOutputL1Role, path: paths.l1},
		{role: "replacement_archive", output: buildOutputArchiveRole, path: paths.archive},
	}
	sources := make([]cutoverStageSource, 0, 5)
	for _, target := range targets {
		output, ok := active.build.outputFiles[target.output]
		if !ok {
			return nil, errors.New("build output binding is missing")
		}
		binding, ok := findCutoverBoundFile(active.build.files, output.path)
		if !ok {
			return nil, errors.New("build output file binding is missing")
		}
		sources = append(sources, cutoverStageSource{role: target.role, name: target.path, source: binding, sqlite: true})
	}
	runtimeBinding, ok := findCutoverBoundFile(active.build.files, active.build.paths.stagedRuntime)
	if !ok {
		return nil, errors.New("staged runtime binding is missing")
	}
	if err := validateCutoverRuntimeBinding(runtimeBinding); err != nil {
		return nil, err
	}
	sources = append(sources, cutoverStageSource{role: "replacement_runtime", name: paths.runtime, source: runtimeBinding, runtime: true})
	return sources, nil
}

func findCutoverBoundFile(files []cutoverBoundFile, path string) (cutoverBoundFile, bool) {
	for _, file := range files {
		if samePath(file.path, path) {
			return file, true
		}
	}
	return cutoverBoundFile{}, false
}

func createCutoverRollbackTemp(ctx context.Context, rollback string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parent := filepath.Dir(rollback)
	if err := validateCutoverCanonicalParent(rollback); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(parent, cutoverRollbackTempPrefix)
	if err != nil {
		return "", err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(temporary)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		return "", errors.New("rollback temporary root is unsafe")
	}
	if err := cutoverStageSyncDirectory(parent); err != nil {
		return "", err
	}
	removeTemp = false
	return temporary, nil
}

func copyCutoverStageFile(ctx context.Context, source cutoverBoundFile, target string, targetMode os.FileMode, require0600, sqlite, executable bool) (binding cutoverBoundFile, created bool, err error) {
	if ctx == nil {
		return cutoverBoundFile{}, false, cutoverStageError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return cutoverBoundFile{}, false, err
	}
	if source.info == nil || strings.TrimSpace(source.path) == "" {
		return cutoverBoundFile{}, false, errors.New("source binding is invalid")
	}
	if err := verifyCutoverBoundFile(source); err != nil {
		return cutoverBoundFile{}, false, err
	}
	input, err := os.Open(source.path)
	if err != nil {
		return cutoverBoundFile{}, false, err
	}
	inputInfo, err := input.Stat()
	if err != nil || !os.SameFile(source.info, inputInfo) || inputInfo.Mode()&os.ModeSymlink != 0 || !inputInfo.Mode().IsRegular() {
		_ = input.Close()
		return cutoverBoundFile{}, false, errors.New("source changed")
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, targetMode.Perm())
	if err != nil {
		_ = input.Close()
		return cutoverBoundFile{}, false, err
	}
	created = true
	cleanup := func() {
		_ = output.Close()
		_ = input.Close()
		_ = removeCutoverKnownFile(target)
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	if err := output.Chmod(targetMode.Perm()); err != nil {
		return cutoverBoundFile{}, created, err
	}
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return cutoverBoundFile{}, created, err
		}
		readBytes, readErr := input.Read(buffer)
		if readBytes > 0 {
			written, writeErr := output.Write(buffer[:readBytes])
			if writeErr != nil || written != readBytes {
				if writeErr == nil {
					writeErr = io.ErrShortWrite
				}
				return cutoverBoundFile{}, created, writeErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return cutoverBoundFile{}, created, readErr
		}
	}
	if err := cutoverStageSyncFile(output); err != nil {
		return cutoverBoundFile{}, created, err
	}
	if err := output.Close(); err != nil {
		return cutoverBoundFile{}, created, err
	}
	if err := input.Close(); err != nil {
		return cutoverBoundFile{}, created, err
	}
	if err := cutoverStageSyncDirectory(filepath.Dir(target)); err != nil {
		return cutoverBoundFile{}, created, err
	}
	binding, err = bindCutoverFile(target, require0600, sqlite)
	if err != nil {
		return cutoverBoundFile{}, created, err
	}
	if binding.bytes != source.bytes || binding.sha256 != source.sha256 || binding.info.Mode().Perm() != targetMode.Perm() || os.SameFile(source.info, binding.info) {
		return cutoverBoundFile{}, created, errors.New("copied file binding differs")
	}
	if executable {
		if err := validateCutoverRuntimeBinding(binding); err != nil {
			return cutoverBoundFile{}, created, err
		}
	}
	return binding, created, nil
}

func verifyCutoverRollbackRoot(root string, files []cutoverStageBinding) error {
	if len(files) != 7 {
		return errors.New("rollback file set is incomplete")
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		return errors.New("rollback root is unsafe")
	}
	if realRoot, err := filepath.EvalSymlinks(root); err != nil || !samePath(root, filepath.Clean(realRoot)) || validateCutoverCanonicalParent(root) != nil {
		return errors.New("rollback root is not canonical")
	}
	expected := map[string]struct{}{
		cutoverRollbackDCIFilename: {}, cutoverRollbackDCIJSONLFilename: {}, cutoverRollbackEventStoreFilename: {},
		cutoverRollbackL1Filename: {}, cutoverRollbackArchiveFilename: {}, cutoverRollbackInstalledFilename: {}, cutoverRollbackStagedFilename: {},
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != len(expected) {
		return errors.New("rollback file set is invalid")
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return errors.New("rollback file set is invalid")
		}
	}
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if _, ok := expected[filepath.Base(file.target.path)]; !ok || filepath.Dir(file.target.path) != filepath.Clean(root) {
			return errors.New("rollback file path is invalid")
		}
		if _, ok := seen[filepath.Base(file.target.path)]; ok {
			return errors.New("rollback file set has duplicates")
		}
		seen[filepath.Base(file.target.path)] = struct{}{}
		binding, err := bindCutoverFile(file.target.path, false, file.source.sqlite)
		if err != nil {
			return err
		}
		if binding.sha256 != file.source.sha256 || binding.bytes != file.source.bytes || binding.info.Mode().Perm() != file.source.info.Mode().Perm() || os.SameFile(binding.info, file.source.info) {
			return errors.New("rollback file differs from source")
		}
		if file.role == "installed_runtime" || file.role == "staged_runtime" {
			if err := validateCutoverRuntimeBinding(binding); err != nil {
				return err
			}
		}
	}
	if len(seen) != len(expected) {
		return errors.New("rollback file set is incomplete")
	}
	return nil
}

func verifyCutoverReplacementStages(files []cutoverStageBinding) error {
	if len(files) != 5 {
		return errors.New("replacement stage set is incomplete")
	}
	seen := make(map[string]struct{}, len(files))
	expectedNames := map[string]string{
		"replacement_dci": cutoverStageDCIName, "replacement_event_store": cutoverStageEventStoreName,
		"replacement_l1": cutoverStageL1Name, "replacement_archive": cutoverStageArchiveName,
		"replacement_runtime": cutoverStageRuntimeName,
	}
	for _, file := range files {
		wantName, ok := expectedNames[file.role]
		if !ok || filepath.Base(file.target.path) != wantName || validateCutoverCanonicalParent(file.target.path) != nil {
			return errors.New("replacement stage path is invalid")
		}
		if _, ok := seen[file.target.path]; ok {
			return errors.New("replacement stage set has duplicates")
		}
		seen[file.target.path] = struct{}{}
		require0600 := file.role != "replacement_runtime"
		binding, err := bindCutoverFile(file.target.path, require0600, file.source.sqlite)
		if err != nil {
			return err
		}
		wantMode := os.FileMode(0o600)
		if file.role == "replacement_runtime" {
			wantMode = file.source.info.Mode().Perm()
		}
		if binding.sha256 != file.source.sha256 || binding.bytes != file.source.bytes || binding.info.Mode().Perm() != wantMode || os.SameFile(binding.info, file.source.info) {
			return errors.New("replacement stage differs from source")
		}
		if file.role == "replacement_runtime" {
			if err := validateCutoverRuntimeBinding(binding); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCutoverStageTargetAliases(active preparedCutoverActiveCohort, rollback, stages []cutoverStageBinding) error {
	all := make([]cutoverBoundFile, 0, len(active.build.files)+len(active.files)+len(rollback)+len(stages))
	all = append(all, active.build.files...)
	all = append(all, active.files...)
	for _, file := range rollback {
		all = append(all, file.target)
	}
	for _, file := range stages {
		all = append(all, file.target)
	}
	for left := 0; left < len(all); left++ {
		for right := left + 1; right < len(all); right++ {
			if samePath(all[left].path, all[right].path) || os.SameFile(all[left].info, all[right].info) {
				return errors.New("cutover stage aliases a cohort file")
			}
		}
	}
	return nil
}

func rebindCutoverRollbackFiles(root string, files []cutoverStageBinding) ([]cutoverStageBinding, error) {
	rebound := make([]cutoverStageBinding, 0, len(files))
	for _, file := range files {
		path := filepath.Join(root, filepath.Base(file.target.path))
		binding, err := bindCutoverFile(path, false, file.source.sqlite)
		if err != nil {
			return nil, err
		}
		if binding.sha256 != file.source.sha256 || binding.bytes != file.source.bytes || binding.info.Mode().Perm() != file.source.info.Mode().Perm() || os.SameFile(binding.info, file.source.info) {
			return nil, errors.New("final rollback file differs")
		}
		if file.role == "installed_runtime" || file.role == "staged_runtime" {
			if err := validateCutoverRuntimeBinding(binding); err != nil {
				return nil, err
			}
		}
		file.target = binding
		rebound = append(rebound, file)
	}
	if err := verifyCutoverRollbackRoot(root, rebound); err != nil {
		return nil, err
	}
	return rebound, nil
}

func verifyCutoverStageFinalState(ctx context.Context, active preparedCutoverActiveCohort, rollback, stages []cutoverStageBinding) error {
	if err := verifyCutoverRollbackRoot(active.build.paths.rollbackDir, rollback); err != nil {
		return err
	}
	if err := verifyCutoverReplacementStages(stages); err != nil {
		return err
	}
	if err := validateCutoverStageTargetAliases(active, rollback, stages); err != nil {
		return err
	}
	if _, err := revalidateCutoverStageCohort(ctx, active, true); err != nil {
		return err
	}
	return nil
}

func revalidateCutoverStageCohort(ctx context.Context, input preparedCutoverActiveCohort, finalized bool) (preparedCutoverActiveCohort, error) {
	if ctx == nil {
		return preparedCutoverActiveCohort{}, errors.New("invalid context")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := validatePreparedCutoverActiveShape(input); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if !finalized {
		options := cutoverActiveOptions{SourceDCI: input.paths.dci, SourceDCIJSONL: input.paths.dciJSONL, SourceEventStore: input.paths.eventStore, SourceL1: input.paths.l1, SourceArchive: input.paths.archive}
		fresh, err := prepareCutoverActiveCohort(ctx, input.build, options)
		if err != nil {
			return preparedCutoverActiveCohort{}, err
		}
		if !sameCutoverActiveCohort(input, fresh) {
			return preparedCutoverActiveCohort{}, errors.New("active cohort changed")
		}
		return fresh, nil
	}

	activeFiles, err := bindCutoverActiveFiles(input.paths)
	if err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := validateCutoverActiveAliases(input.build.files, activeFiles); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := validateCutoverActiveProspectiveDisjoint(input.build.paths, input.paths); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	sources, err := loadCutoverActiveSources(ctx, input.paths, input.build.buildReceipt)
	if err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := cutoverPrepareActiveAfterBind(); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := verifyCutoverActiveBindings(activeFiles); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	revalidatedBuild, err := revalidatePreparedCutoverBuildFiles(ctx, input.build)
	if err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	revalidatedBuild.paths.rollbackDir = input.build.paths.rollbackDir
	revalidatedBuild.paths.cutoverReceipt = input.build.paths.cutoverReceipt
	result := preparedCutoverActiveCohort{build: revalidatedBuild, paths: input.paths, files: activeFiles, sources: sources}
	if !sameCutoverActiveCohort(input, result) {
		return preparedCutoverActiveCohort{}, errors.New("active cohort changed")
	}
	return result, nil
}

func validatePreparedCutoverActiveShape(input preparedCutoverActiveCohort) error {
	if len(input.build.files) != 7 || len(input.build.outputFiles) != 4 || len(input.files) != 5 {
		return errors.New("prepared active cohort shape is invalid")
	}
	for _, path := range []string{input.paths.dci, input.paths.dciJSONL, input.paths.eventStore, input.paths.l1, input.paths.archive} {
		if strings.TrimSpace(path) == "" {
			return errors.New("prepared active paths are incomplete")
		}
	}
	return nil
}

func sameCutoverActiveCohort(left, right preparedCutoverActiveCohort) bool {
	if !sameCutoverPreparedArtifacts(left.build, right.build) || !sameCutoverActivePaths(left.paths, right.paths) || len(left.files) != len(right.files) {
		return false
	}
	for index := range left.files {
		if !sameCutoverBoundFile(left.files[index], right.files[index]) {
			return false
		}
	}
	return reflect.DeepEqual(left.sources, right.sources)
}

func sameCutoverActivePaths(left, right cutoverActivePaths) bool {
	return samePath(left.dci, right.dci) && samePath(left.dciJSONL, right.dciJSONL) && samePath(left.eventStore, right.eventStore) && samePath(left.l1, right.l1) && samePath(left.archive, right.archive)
}

func sameCutoverPreparedArtifacts(left, right preparedCutoverArtifacts) bool {
	if !sameCutoverArtifactPaths(left.paths, right.paths) || left.buildReceiptSHA256 != right.buildReceiptSHA256 || !reflect.DeepEqual(left.buildReceipt, right.buildReceipt) || !reflect.DeepEqual(left.outputFiles, right.outputFiles) || len(left.files) != len(right.files) {
		return false
	}
	for index := range left.files {
		if !sameCutoverBoundFile(left.files[index], right.files[index]) {
			return false
		}
	}
	return true
}

func sameCutoverArtifactPaths(left, right cutoverArtifactPaths) bool {
	return samePath(left.buildRoot, right.buildRoot) && samePath(left.buildReceipt, right.buildReceipt) && samePath(left.installedRuntime, right.installedRuntime) && samePath(left.stagedRuntime, right.stagedRuntime) && samePath(left.rollbackDir, right.rollbackDir) && samePath(left.cutoverReceipt, right.cutoverReceipt)
}

func cutoverStageArtifactSetSHA256(files []cutoverStageBinding, method string) string {
	artifacts := make(map[string]CaptureArtifact, len(files))
	for _, file := range files {
		var sidecarZero *int
		if file.source.sqlite {
			zero := 0
			sidecarZero = &zero
		}
		artifacts[file.role] = CaptureArtifact{Method: method, FileSHA256: file.target.sha256, Bytes: file.target.bytes, SidecarZero: sidecarZero}
	}
	return captureArtifactSetSHA256(artifacts)
}

func cleanupCutoverStageArtifacts(temporary, finalized string, finalizedRoot bool, rollbackFiles, stageFiles []string, stageSources []cutoverStageSource) error {
	var cleanupErr error
	if finalizedRoot {
		cleanupErr = errors.Join(cleanupErr, cleanupCutoverRollbackRoot(finalized))
	} else if temporary != "" {
		cleanupErr = errors.Join(cleanupErr, cleanupCutoverRollbackRoot(temporary))
	}
	for _, path := range rollbackFiles {
		if !finalizedRoot {
			cleanupErr = errors.Join(cleanupErr, removeCutoverKnownFile(path))
		}
	}
	for _, path := range stageFiles {
		cleanupErr = errors.Join(cleanupErr, removeCutoverKnownFile(path))
		for _, source := range stageSources {
			if source.name != path || !source.sqlite {
				continue
			}
			for _, suffix := range sqliteSidecarSuffixes {
				cleanupErr = errors.Join(cleanupErr, removeCutoverKnownFile(path+suffix))
			}
		}
	}
	if temporary != "" && finalizedRoot {
		cleanupErr = errors.Join(cleanupErr, ensureCutoverAbsent(temporary))
	}
	if finalizedRoot {
		cleanupErr = errors.Join(cleanupErr, ensureCutoverAbsent(finalized))
	}
	for _, path := range stageFiles {
		cleanupErr = errors.Join(cleanupErr, ensureCutoverAbsent(path))
	}
	return cleanupErr
}

func cleanupCutoverRollbackRoot(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("rollback cleanup root is unsafe")
	}
	known := map[string]struct{}{
		cutoverRollbackDCIFilename: {}, cutoverRollbackDCIJSONLFilename: {}, cutoverRollbackEventStoreFilename: {},
		cutoverRollbackL1Filename: {}, cutoverRollbackArchiveFilename: {}, cutoverRollbackInstalledFilename: {}, cutoverRollbackStagedFilename: {},
	}
	for _, name := range []string{cutoverRollbackDCIFilename, cutoverRollbackEventStoreFilename, cutoverRollbackL1Filename, cutoverRollbackArchiveFilename} {
		for _, suffix := range sqliteSidecarSuffixes {
			known[name+suffix] = struct{}{}
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, ok := known[entry.Name()]; !ok {
			return errors.New("rollback cleanup found an unknown entry")
		}
		path := filepath.Join(root, entry.Name())
		if err := removeCutoverKnownFile(path); err != nil {
			return err
		}
	}
	if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := cutoverStageSyncDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	return ensureCutoverAbsent(root)
}

func removeCutoverKnownFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("cutover cleanup target is unsafe")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return cutoverStageSyncDirectory(filepath.Dir(path))
}

func ensureCutoverAbsent(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err == nil {
		return errors.New("cutover cleanup target remains")
	} else {
		return err
	}
}

func cutoverStageError(code string) error {
	if code == "" || !validErrorCode(code) {
		code = "cutover_stage"
	}
	return newCodedError(code, "offline DCI cutover staging failed")
}

func cutoverStageCauseError(cause error, fallback string) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return cutoverStageError(errorCode(cause, fallback))
}
