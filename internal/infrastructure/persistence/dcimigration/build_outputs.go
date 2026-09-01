package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	buildOutputDCIFilename        = "target-dci.db"
	buildOutputEventStoreFilename = "target-event-store.db"
	buildOutputL1Filename         = "target-l1.db"
	buildOutputArchiveFilename    = "target-archive.db"
	buildOutputDCIRole            = "target_dci"
	buildOutputEventStoreRole     = "target_event_store"
	buildOutputL1Role             = "target_l1"
	buildOutputArchiveRole        = "target_archive"
)

// buildOutputFile is kept local to the output operation.  Its path never
// crosses the evidence boundary.
type buildOutputFile struct {
	role         string
	name         string
	path         string
	sha256       string
	bytes        int64
	quickCheckOK int
	sidecarZero  int
}

// buildOutputsEvidence is the private, path-free proof for the four offline
// build artifacts.  It contains only output hashes, sizes, and health
// counters; paths, identities, and payloads remain private to the operation.
type buildOutputsEvidence struct {
	DCI        buildDCIEvidence
	EventStore buildEventStoreEvidence
	L1         l1ProjectionEvidence
	Archive    l1ProjectionEvidence

	TargetDCISHA256        string
	TargetDCIBytes         int64
	TargetEventStoreSHA256 string
	TargetEventStoreBytes  int64
	TargetL1SHA256         string
	TargetL1Bytes          int64
	TargetArchiveSHA256    string
	TargetArchiveBytes     int64
	ArtifactSetSHA256      string
	BuildRootModeOK        int
	SidecarZero            int
	SourceInputsStable     int
}

// buildOutputsAfterOutput is a package-local seam for deterministic failure
// injection after each owner helper.  Production uses the no-op default.
var buildOutputsAfterOutput = func(string) error { return nil }

// materializeBuildOutputs creates all four offline outputs from one retained
// preparedBuild.  It never re-plans identities or writes to a captured input.
// A failed operation leaves only an empty build root for a later blocked
// receipt boundary.
func materializeBuildOutputs(ctx context.Context, prepared preparedBuild) (evidence buildOutputsEvidence, err error) {
	if ctx == nil {
		return buildOutputsEvidence{}, buildOutputsError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}
	if err := validatePreparedBuildForOutputs(prepared); err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "invalid_input")
	}
	if err := verifyBuildOutputInputs(ctx, prepared); err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "source_changed")
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}

	buildRoot := prepared.paths.buildDir
	if err := os.Mkdir(buildRoot, 0o700); err != nil {
		return buildOutputsEvidence{}, buildOutputsError("build_root")
	}
	createdRoot := true
	defer func() {
		if err != nil && createdRoot {
			cleanupBuildOutputRoot(buildRoot)
		}
	}()
	if err := os.Chmod(buildRoot, 0o700); err != nil {
		return buildOutputsEvidence{}, buildOutputsError("build_root")
	}
	if err := syncBuildOutputDirectories(buildRoot); err != nil {
		return buildOutputsEvidence{}, buildOutputsError("build_root_sync")
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}

	targets := buildOutputTargets(buildRoot)
	dciEvidence, err := createBuiltDCI(ctx, targets[0].path, prepared.snapshot, prepared.plan)
	if err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "dci_output")
	}
	targets[0].quickCheckOK = dciEvidence.QuickCheckOK
	targets[0].sidecarZero = dciEvidence.SidecarZero
	if err := buildOutputsAfterOutput(targets[0].path); err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "dci_output")
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}

	eventStoreEvidence, err := createBuiltEventStore(ctx, prepared.paths.sources.eventStore, targets[1].path, prepared.snapshot, prepared.plan)
	if err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "event_store_output")
	}
	targets[1].quickCheckOK = eventStoreEvidence.QuickCheckOK
	targets[1].sidecarZero = eventStoreEvidence.SidecarZero
	if err := buildOutputsAfterOutput(targets[1].path); err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "event_store_output")
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}

	l1Evidence, err := createProjectedL1Snapshot(ctx, prepared.paths.sources.l1, targets[2].path, false, prepared.snapshot, prepared.plan)
	if err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "l1_output")
	}
	targets[2].quickCheckOK = l1Evidence.QuickCheckOK
	targets[2].sidecarZero = l1Evidence.SidecarZero
	if err := buildOutputsAfterOutput(targets[2].path); err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "l1_output")
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}

	archiveEvidence, err := createProjectedL1Snapshot(ctx, prepared.paths.sources.archive, targets[3].path, true, prepared.snapshot, prepared.plan)
	if err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "archive_output")
	}
	targets[3].quickCheckOK = archiveEvidence.QuickCheckOK
	targets[3].sidecarZero = archiveEvidence.SidecarZero
	if err := buildOutputsAfterOutput(targets[3].path); err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "archive_output")
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}
	if err := validateBuildOutputOwnerHealth(targets); err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "output_health")
	}

	files, err := verifyBuildOutputTargets(ctx, buildRoot, targets)
	if err != nil {
		return buildOutputsEvidence{}, buildOutputsCauseError(err, "output_verify")
	}
	if err := syncBuildOutputDirectories(buildRoot); err != nil {
		return buildOutputsEvidence{}, buildOutputsError("build_root_sync")
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}
	if err := verifyBuildOutputInputs(ctx, prepared); err != nil {
		return buildOutputsEvidence{}, buildOutputsSourceChangedError(err)
	}
	if err := ctx.Err(); err != nil {
		return buildOutputsEvidence{}, err
	}

	artifactSetSHA256 := buildOutputArtifactSetSHA256(files)
	if !isLowerHexSHA256(artifactSetSHA256) {
		return buildOutputsEvidence{}, buildOutputsError("output_hash")
	}
	evidence = buildOutputsEvidence{
		DCI:                    dciEvidence,
		EventStore:             eventStoreEvidence,
		L1:                     l1Evidence,
		Archive:                archiveEvidence,
		TargetDCISHA256:        files[buildOutputDCIRole].sha256,
		TargetDCIBytes:         files[buildOutputDCIRole].bytes,
		TargetEventStoreSHA256: files[buildOutputEventStoreRole].sha256,
		TargetEventStoreBytes:  files[buildOutputEventStoreRole].bytes,
		TargetL1SHA256:         files[buildOutputL1Role].sha256,
		TargetL1Bytes:          files[buildOutputL1Role].bytes,
		TargetArchiveSHA256:    files[buildOutputArchiveRole].sha256,
		TargetArchiveBytes:     files[buildOutputArchiveRole].bytes,
		ArtifactSetSHA256:      artifactSetSHA256,
		BuildRootModeOK:        1,
		SidecarZero:            1,
		SourceInputsStable:     1,
	}
	createdRoot = false
	return evidence, nil
}

func buildOutputTargets(root string) []buildOutputFile {
	return []buildOutputFile{
		{role: buildOutputDCIRole, name: buildOutputDCIFilename, path: filepath.Join(root, buildOutputDCIFilename)},
		{role: buildOutputEventStoreRole, name: buildOutputEventStoreFilename, path: filepath.Join(root, buildOutputEventStoreFilename)},
		{role: buildOutputL1Role, name: buildOutputL1Filename, path: filepath.Join(root, buildOutputL1Filename)},
		{role: buildOutputArchiveRole, name: buildOutputArchiveFilename, path: filepath.Join(root, buildOutputArchiveFilename)},
	}
}

func validatePreparedBuildForOutputs(prepared preparedBuild) error {
	paths := prepared.paths
	if strings.TrimSpace(paths.snapshotDir) == "" || strings.TrimSpace(paths.buildDir) == "" ||
		strings.TrimSpace(paths.captureReceipt) == "" || strings.TrimSpace(paths.dryRunManifest) == "" {
		return errors.New("prepared build paths are incomplete")
	}
	if err := validateCanonicalBuildSnapshotRoot(paths.snapshotDir); err != nil {
		return err
	}
	if !samePath(paths.snapshotDir, paths.sources.root) {
		return errors.New("prepared build snapshot roots differ")
	}
	for _, spec := range captureArtifactSpecs {
		var path string
		switch spec.role {
		case "source_dci":
			path = paths.sources.dci
		case "source_dci_jsonl":
			path = paths.sources.dciJSONL
		case "source_event_store":
			path = paths.sources.eventStore
		case "source_l1":
			path = paths.sources.l1
		case "source_archive":
			path = paths.sources.archive
		default:
			return errors.New("prepared build source role is invalid")
		}
		if !samePath(path, filepath.Join(paths.sources.root, spec.filename)) {
			return errors.New("prepared build source layout is invalid")
		}
	}
	if !samePath(paths.sources.manifest, paths.dryRunManifest) {
		return errors.New("prepared build manifest binding is invalid")
	}
	receiptPath, err := resolveExistingBuildInput(paths.snapshotDir, paths.captureReceipt)
	if err != nil || !samePath(receiptPath, paths.captureReceipt) {
		return errors.New("prepared capture receipt binding is invalid")
	}
	manifestPath, err := resolveExistingBuildInput(paths.snapshotDir, paths.dryRunManifest)
	if err != nil || !samePath(manifestPath, paths.dryRunManifest) {
		return errors.New("prepared dry-run manifest binding is invalid")
	}
	freshRoot, err := resolveFreshBuildRoot(paths.buildDir, paths.snapshotDir)
	if err != nil || !samePath(freshRoot, paths.buildDir) {
		return errors.New("prepared build root is invalid")
	}
	if err := validateBuildInputAliases(paths); err != nil {
		return err
	}
	if err := validateCaptureReceipt(prepared.captureReceipt); err != nil || prepared.captureReceipt.SchemaVersion != CaptureSchemaVersion || prepared.captureReceipt.Mode != ModeCapture || prepared.captureReceipt.Status != StatusReady {
		return errors.New("prepared capture receipt is not ready")
	}
	if err := validateManifest(prepared.dryRunManifest); err != nil || prepared.dryRunManifest.SchemaVersion != ManifestSchemaVersion || prepared.dryRunManifest.Mode != ModeDryRun || prepared.dryRunManifest.Status != StatusReady {
		return errors.New("prepared dry-run manifest is not ready")
	}
	if !isLowerHexSHA256(prepared.captureReceiptSHA256) || !isLowerHexSHA256(prepared.dryRunManifestSHA256) || !isLowerHexSHA256(prepared.artifactSetSHA256) {
		return errors.New("prepared input hashes are invalid")
	}
	if prepared.artifactSetSHA256 != prepared.captureReceipt.ArtifactSetSHA256 || len(prepared.artifactHashes) != len(captureArtifactSpecs) || len(prepared.artifactBytes) != len(captureArtifactSpecs) {
		return errors.New("prepared artifact bindings are incomplete")
	}
	for _, spec := range captureArtifactSpecs {
		hash, ok := prepared.artifactHashes[spec.role]
		if !ok || !isLowerHexSHA256(hash) {
			return errors.New("prepared artifact hash is invalid")
		}
		bytes, ok := prepared.artifactBytes[spec.role]
		if !ok || bytes < 0 {
			return errors.New("prepared artifact size is invalid")
		}
		recorded, ok := prepared.captureReceipt.Artifacts[spec.role]
		if !ok || recorded.FileSHA256 != hash || recorded.Bytes != bytes {
			return errors.New("prepared artifact binding differs from receipt")
		}
	}
	return nil
}

func verifyBuildOutputInputs(ctx context.Context, prepared preparedBuild) error {
	if ctx == nil {
		return errors.New("build output context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCanonicalBuildSnapshotRoot(prepared.paths.snapshotDir); err != nil {
		return err
	}
	if err := validateBuildInputAliases(prepared.paths); err != nil {
		return err
	}
	_, hashes, bytesByRole, setHash, err := bindBuildArtifacts(prepared.paths.sources, prepared.captureReceipt)
	if err != nil {
		return err
	}
	if setHash != prepared.artifactSetSHA256 || len(hashes) != len(prepared.artifactHashes) || len(bytesByRole) != len(prepared.artifactBytes) {
		return errors.New("prepared artifact set changed")
	}
	for _, spec := range captureArtifactSpecs {
		if hashes[spec.role] != prepared.artifactHashes[spec.role] || bytesByRole[spec.role] != prepared.artifactBytes[spec.role] {
			return errors.New("prepared artifact changed")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyBuildBoundedFile(prepared.paths.captureReceipt, maxCaptureManifestBytes, prepared.captureReceiptSHA256); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyBuildBoundedFile(prepared.paths.dryRunManifest, maxManifestBytes, prepared.dryRunManifestSHA256); err != nil {
		return err
	}
	return ctx.Err()
}

func verifyBuildOutputTargets(ctx context.Context, root string, targets []buildOutputFile) (map[string]buildOutputFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("build root is invalid")
	}
	if runtime.GOOS != "windows" && rootInfo.Mode().Perm() != 0o700 {
		return nil, errors.New("build root permissions are invalid")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != len(targets) {
		return nil, errors.New("build output set is invalid")
	}
	expected := make(map[string]buildOutputFile, len(targets))
	for _, target := range targets {
		expected[target.name] = target
	}
	files := make(map[string]buildOutputFile, len(targets))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		target, ok := expected[entry.Name()]
		if !ok {
			return nil, errors.New("build output set contains an unexpected entry")
		}
		info, err := os.Lstat(target.path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("build output is not a regular file")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			return nil, errors.New("build output permissions are invalid")
		}
		if err := rejectCapturedSQLiteSidecars(target.path); err != nil {
			return nil, err
		}
		hash, bytes, err := hashBuildFile(target.path)
		if err != nil {
			return nil, err
		}
		target.sha256 = hash
		target.bytes = bytes
		files[target.role] = target
	}
	if len(files) != len(targets) {
		return nil, errors.New("build output set is incomplete")
	}
	return files, nil
}

func validateBuildOutputOwnerHealth(targets []buildOutputFile) error {
	if len(targets) != 4 {
		return errors.New("build output owner evidence is incomplete")
	}
	for _, target := range targets {
		if target.quickCheckOK != 1 || target.sidecarZero != 1 {
			return errors.New("build output owner health is not ready")
		}
	}
	return nil
}

func buildOutputArtifactSetSHA256(files map[string]buildOutputFile) string {
	artifacts := make(map[string]CaptureArtifact, len(files))
	for role, file := range files {
		quickCheck := ""
		if file.quickCheckOK == 1 {
			quickCheck = "ok"
		}
		var sidecarZero *int
		if file.sidecarZero == 1 {
			zero := 0
			sidecarZero = &zero
		}
		artifacts[role] = CaptureArtifact{
			Method:      "offline_build",
			FileSHA256:  file.sha256,
			Bytes:       file.bytes,
			QuickCheck:  quickCheck,
			SidecarZero: sidecarZero,
		}
	}
	return captureArtifactSetSHA256(artifacts)
}

func syncBuildOutputDirectories(root string) error {
	if err := syncDirectory(root); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(root))
}

func cleanupBuildOutputRoot(root string) {
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		child, err := os.Lstat(path)
		if err != nil || child.Mode()&os.ModeSymlink != 0 || child.Mode().IsRegular() {
			_ = os.Remove(path)
			continue
		}
		if child.IsDir() {
			_ = os.Remove(path)
			continue
		}
		_ = os.Remove(path)
	}
	_ = syncDirectory(root)
}

func buildOutputsError(code string) error {
	if code == "" {
		code = "build_outputs"
	}
	return newCodedError(code, "offline build outputs failed")
}

func buildOutputsCauseError(cause error, fallback string) error {
	if cause == nil {
		return buildOutputsError(fallback)
	}
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return buildOutputsError(errorCode(cause, fallback))
}

func buildOutputsSourceChangedError(cause error) error {
	if cause == nil {
		return buildOutputsError("source_changed")
	}
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return buildOutputsError("source_changed")
}
