package dcimigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
)

// buildOptions is the offline build input contract.  It contains only paths
// and the canonical Agent-ID set; expected counts are taken from the bound
// ready dry-run manifest.
type buildOptions struct {
	SnapshotDir    string
	BuildDir       string
	CaptureReceipt string
	DryRunManifest string
	AgentIDs       []string
}

type buildPaths struct {
	snapshotDir    string
	buildDir       string
	captureReceipt string
	dryRunManifest string
	sources        sourcePaths
}

// preparedBuild is the private, immutable input bundle passed to a later
// build/apply unit.  It deliberately retains the classified source snapshot
// and its one migration plan instead of allowing a later unit to classify or
// allocate identities again.
type preparedBuild struct {
	paths    buildPaths
	snapshot sourceSnapshot
	plan     migrationPlan

	captureReceipt CaptureReceipt
	dryRunManifest Manifest

	captureReceiptSHA256 string
	dryRunManifestSHA256 string
	artifactHashes       map[string]string
	artifactBytes        map[string]int64
	artifactSetSHA256    string
}

// buildPrepareClassifySnapshot is a package-local seam used to prove that
// preparation invokes the production classifier exactly once.  Its default is
// the direct classifier; no second planning or classification path exists.
var buildPrepareClassifySnapshot = classifySnapshot

// prepareBuild binds one captured snapshot and one ready dry-run manifest to a
// fresh prospective build root.  It is strictly offline: no output directory
// is created and no source is opened for writing.
func prepareBuild(ctx context.Context, options buildOptions) (preparedBuild, error) {
	if ctx == nil {
		return preparedBuild{}, buildPrepareError("invalid_options")
	}
	if err := ctx.Err(); err != nil {
		return preparedBuild{}, err
	}
	if err := validateBuildOptions(options); err != nil {
		return preparedBuild{}, buildPrepareError(errorCode(err, "invalid_options"))
	}

	paths, err := resolveBuildPaths(options)
	if err != nil {
		return preparedBuild{}, buildPrepareError(errorCode(err, "unsafe_path"))
	}

	captureReceipt, captureReceiptSHA256, err := readBuildCaptureReceipt(paths.captureReceipt)
	if err != nil {
		return preparedBuild{}, buildPrepareError("capture_receipt")
	}
	if err := ctx.Err(); err != nil {
		return preparedBuild{}, err
	}
	dryRunManifest, dryRunManifestSHA256, err := readBuildManifest(paths.dryRunManifest)
	if err != nil {
		return preparedBuild{}, buildPrepareError("dryrun_manifest")
	}
	if err := ctx.Err(); err != nil {
		return preparedBuild{}, err
	}

	artifacts, artifactHashes, artifactBytes, artifactSetSHA256, err :=
		bindBuildArtifacts(paths.sources, captureReceipt)
	if err != nil {
		return preparedBuild{}, buildPrepareError(errorCode(err, "artifact_drift"))
	}
	if err := ctx.Err(); err != nil {
		return preparedBuild{}, err
	}

	classificationOptions := Options{
		SnapshotDir:      paths.sources.root,
		SourceDCI:        paths.sources.dci,
		SourceDCIJSONL:   paths.sources.dciJSONL,
		SourceEventStore: paths.sources.eventStore,
		SourceL1:         paths.sources.l1,
		SourceArchive:    paths.sources.archive,
		Manifest:         paths.dryRunManifest,
		Expected:         dryRunManifest.ExpectedCounts,
		AgentIDs:         append([]string(nil), options.AgentIDs...),
	}
	report, err := buildPrepareClassifySnapshot(ctx, paths.sources, classificationOptions)
	if err != nil {
		return preparedBuild{}, buildPrepareCauseError(err, "build_classification")
	}
	if err := ctx.Err(); err != nil {
		return preparedBuild{}, err
	}
	if report.Manifest.Status != StatusReady || !reflect.DeepEqual(report.Manifest, dryRunManifest) {
		return preparedBuild{}, buildPrepareError("manifest_mismatch")
	}

	if err := verifyBuildInputsUnchanged(paths, captureReceipt, artifacts, captureReceiptSHA256, dryRunManifestSHA256); err != nil {
		return preparedBuild{}, buildPrepareCauseError(err, "source_changed")
	}
	if err := ctx.Err(); err != nil {
		return preparedBuild{}, err
	}

	return preparedBuild{
		paths:                paths,
		snapshot:             report.Snapshot,
		plan:                 report.Plan,
		captureReceipt:       captureReceipt,
		dryRunManifest:       dryRunManifest,
		captureReceiptSHA256: captureReceiptSHA256,
		dryRunManifestSHA256: dryRunManifestSHA256,
		artifactHashes:       artifactHashes,
		artifactBytes:        artifactBytes,
		artifactSetSHA256:    artifactSetSHA256,
	}, nil
}

func validateBuildOptions(options buildOptions) error {
	if strings.TrimSpace(options.SnapshotDir) == "" || strings.TrimSpace(options.BuildDir) == "" ||
		strings.TrimSpace(options.CaptureReceipt) == "" || strings.TrimSpace(options.DryRunManifest) == "" {
		return newCodedError("invalid_options", "offline build paths are required")
	}
	if len(options.AgentIDs) == 0 {
		return newCodedError("invalid_options", "canonical agent ID set is required")
	}
	seen := make(map[string]struct{}, len(options.AgentIDs))
	for _, id := range options.AgentIDs {
		if id == "" || strings.TrimSpace(id) != id {
			return newCodedError("invalid_options", "canonical agent IDs are invalid")
		}
		if _, exists := seen[id]; exists {
			return newCodedError("invalid_options", "canonical agent IDs contain a duplicate")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func resolveBuildPaths(options buildOptions) (buildPaths, error) {
	// resolvePaths owns the fixed five-artifact source layout and its regular,
	// direct-child, non-symlink checks.  It requires a not-yet-existing output
	// manifest, so reserve only a lexical placeholder for this read-only unit.
	snapshotRoot, err := absolutePath(options.SnapshotDir)
	if err != nil {
		return buildPaths{}, newCodedError("unsafe_path", "resolve snapshot root")
	}
	if err := validateCanonicalBuildSnapshotRoot(snapshotRoot); err != nil {
		return buildPaths{}, err
	}
	placeholder, err := unusedBuildManifestPath(snapshotRoot)
	if err != nil {
		return buildPaths{}, newCodedError("unsafe_path", "reserve build input placeholder")
	}
	sourceOptions := Options{
		SnapshotDir:      snapshotRoot,
		SourceDCI:        "source-dci",
		SourceDCIJSONL:   "source-dci-jsonl",
		SourceEventStore: "source-event-store",
		SourceL1:         "source-l1",
		SourceArchive:    "source-archive",
		Manifest:         placeholder,
	}
	sources, err := resolvePaths(sourceOptions)
	if err != nil {
		return buildPaths{}, err
	}

	buildRoot, err := resolveFreshBuildRoot(options.BuildDir, sources.root)
	if err != nil {
		return buildPaths{}, err
	}
	captureReceipt, err := resolveExistingBuildInput(sources.root, options.CaptureReceipt)
	if err != nil {
		return buildPaths{}, err
	}
	dryRunManifest, err := resolveExistingBuildInput(sources.root, options.DryRunManifest)
	if err != nil {
		return buildPaths{}, err
	}

	paths := buildPaths{
		snapshotDir:    sources.root,
		buildDir:       buildRoot,
		captureReceipt: captureReceipt,
		dryRunManifest: dryRunManifest,
		sources:        sources,
	}
	paths.sources.manifest = dryRunManifest
	if err := validateBuildInputAliases(paths); err != nil {
		return buildPaths{}, err
	}
	return paths, nil
}

func validateCanonicalBuildSnapshotRoot(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return newCodedError("unsafe_path", "snapshot root is missing or unsafe")
	}
	realRoot, err := filepath.EvalSymlinks(path)
	if err != nil || !samePath(path, filepath.Clean(realRoot)) {
		return newCodedError("unsafe_path", "snapshot root is not canonical")
	}
	return nil
}

func unusedBuildManifestPath(root string) (string, error) {
	for index := 0; index < 1024; index++ {
		name := ".rencrow-build-input-placeholder"
		if index != 0 {
			name += "-" + strconv.Itoa(index)
		}
		candidate := filepath.Join(root, name)
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no unused build placeholder is available")
}

func resolveFreshBuildRoot(raw, snapshotRoot string) (string, error) {
	path, err := absolutePath(raw)
	if err != nil {
		return "", newCodedError("unsafe_path", "resolve build root")
	}
	if samePath(path, snapshotRoot) {
		return "", newCodedError("unsafe_path", "build root aliases snapshot root")
	}
	if _, err := os.Lstat(path); err == nil {
		return "", newCodedError("unsafe_path", "build root must not exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", newCodedError("unsafe_path", "inspect build root")
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return "", newCodedError("unsafe_path", "build root parent is missing or unsafe")
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !samePath(parent, filepath.Clean(realParent)) {
		return "", newCodedError("unsafe_path", "build root parent is not canonical")
	}
	return filepath.Join(filepath.Clean(realParent), filepath.Base(path)), nil
}

func resolveExistingBuildInput(root, raw string) (string, error) {
	path, err := resolveRelativeOrAbsolute(root, raw)
	if err != nil || !pathWithin(root, path) {
		return "", newCodedError("unsafe_path", "build input is outside snapshot root")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", newCodedError("unsafe_path", "build input is missing or unsafe")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || !samePath(path, filepath.Clean(realPath)) {
		return "", newCodedError("unsafe_path", "build input is not canonical")
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return "", newCodedError("unsafe_path", "build input parent is unsafe")
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !samePath(parent, filepath.Clean(realParent)) || !pathWithinOrRoot(root, filepath.Clean(realParent)) {
		return "", newCodedError("unsafe_path", "build input parent is outside snapshot root")
	}
	return path, nil
}

func validateBuildInputAliases(paths buildPaths) error {
	type namedFile struct {
		name string
		path string
		info os.FileInfo
	}
	inputs := make([]namedFile, 0, len(captureArtifactSpecs)+2)
	for _, spec := range captureArtifactSpecs {
		path := filepath.Join(paths.sources.root, spec.filename)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return newCodedError("unsafe_path", "captured artifact is missing or unsafe")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			return newCodedError("unsafe_path", "captured artifact permissions are invalid")
		}
		if spec.sqlite {
			if err := rejectSQLiteSidecars(path); err != nil {
				return newCodedError("unsafe_path", "captured SQLite artifact has an unsafe sidecar")
			}
		}
		inputs = append(inputs, namedFile{name: spec.role, path: path, info: info})
	}
	for _, input := range []struct {
		name string
		path string
	}{
		{name: "capture receipt", path: paths.captureReceipt},
		{name: "dry-run manifest", path: paths.dryRunManifest},
	} {
		info, err := os.Lstat(input.path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return newCodedError("unsafe_path", "build receipt input is missing or unsafe")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			return newCodedError("unsafe_path", "build receipt permissions are invalid")
		}
		inputs = append(inputs, namedFile{name: input.name, path: input.path, info: info})
	}
	for left := 0; left < len(inputs); left++ {
		for right := left + 1; right < len(inputs); right++ {
			if samePath(inputs[left].path, inputs[right].path) || os.SameFile(inputs[left].info, inputs[right].info) {
				return newCodedError("unsafe_path", "build inputs must not alias")
			}
		}
	}
	if runtime.GOOS != "windows" {
		rootInfo, err := os.Lstat(paths.sources.root)
		if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 {
			return newCodedError("unsafe_path", "snapshot root permissions are invalid")
		}
	}
	if samePath(paths.buildDir, paths.sources.root) {
		return newCodedError("unsafe_path", "build root aliases snapshot root")
	}
	for _, input := range inputs {
		if samePath(paths.buildDir, input.path) {
			return newCodedError("unsafe_path", "build root aliases a build input")
		}
	}
	return nil
}

func bindBuildArtifacts(paths sourcePaths, receipt CaptureReceipt) (map[string]CaptureArtifact, map[string]string, map[string]int64, string, error) {
	artifacts := make(map[string]CaptureArtifact, len(captureArtifactSpecs))
	hashes := make(map[string]string, len(captureArtifactSpecs))
	bytesByRole := make(map[string]int64, len(captureArtifactSpecs))
	for _, spec := range captureArtifactSpecs {
		path := filepath.Join(paths.root, spec.filename)
		hash, size, err := hashBuildFile(path)
		if err != nil {
			return nil, nil, nil, "", newCodedError("artifact_drift", "hash captured artifact")
		}
		recorded, ok := receipt.Artifacts[spec.role]
		if !ok || recorded.FileSHA256 != hash || recorded.Bytes != size {
			return nil, nil, nil, "", newCodedError("artifact_drift", "captured artifact does not match receipt")
		}
		actual := recorded
		actual.FileSHA256 = hash
		actual.Bytes = size
		artifacts[spec.role] = actual
		hashes[spec.role] = hash
		bytesByRole[spec.role] = size
	}
	setHash := captureArtifactSetSHA256(artifacts)
	if setHash == "" || setHash != receipt.ArtifactSetSHA256 {
		return nil, nil, nil, "", newCodedError("artifact_set_mismatch", "captured artifact set does not match receipt")
	}
	return artifacts, hashes, bytesByRole, setHash, nil
}

func hashBuildFile(path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 {
		return "", 0, errors.New("build input file is invalid")
	}
	hash, err := fileSHA256(path)
	if err != nil {
		return "", 0, errors.New("build input file cannot be hashed")
	}
	latest, err := os.Lstat(path)
	if err != nil || latest.Mode()&os.ModeSymlink != 0 || !latest.Mode().IsRegular() || latest.Size() != info.Size() || !os.SameFile(info, latest) {
		return "", 0, errors.New("build input file changed")
	}
	return hash, latest.Size(), nil
}

func verifyBuildInputsUnchanged(paths buildPaths, receipt CaptureReceipt, initial map[string]CaptureArtifact, receiptSHA256, manifestSHA256 string) error {
	current, _, _, setHash, err := bindBuildArtifacts(paths.sources, receipt)
	if err != nil {
		return err
	}
	for role, artifact := range initial {
		if current[role].FileSHA256 != artifact.FileSHA256 || current[role].Bytes != artifact.Bytes {
			return newCodedError("source_changed", "captured artifact changed during preparation")
		}
	}
	if setHash != receipt.ArtifactSetSHA256 {
		return newCodedError("source_changed", "captured artifact set changed during preparation")
	}
	if err := verifyBuildBoundedFile(paths.captureReceipt, maxCaptureManifestBytes, receiptSHA256); err != nil {
		return newCodedError("source_changed", "capture receipt changed during preparation")
	}
	if err := verifyBuildBoundedFile(paths.dryRunManifest, maxManifestBytes, manifestSHA256); err != nil {
		return newCodedError("source_changed", "dry-run manifest changed during preparation")
	}
	if _, err := os.Lstat(paths.buildDir); !errors.Is(err, os.ErrNotExist) {
		return newCodedError("unsafe_path", "build root is no longer fresh")
	}
	return nil
}

func verifyBuildBoundedFile(path string, limit int64, expectedSHA256 string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return errors.New("bounded build input changed")
	}
	hash, size, err := hashBuildFile(path)
	if err != nil || size != info.Size() || hash != expectedSHA256 {
		return errors.New("bounded build input changed")
	}
	return nil
}

func buildPrepareError(code string) error {
	if code == "" {
		code = "build_prepare"
	}
	return newCodedError(code, "offline DCI build preparation failed")
}

func buildPrepareCauseError(cause error, fallback string) error {
	if cause == nil {
		return buildPrepareError(fallback)
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return buildPrepareError(errorCode(cause, fallback))
}
