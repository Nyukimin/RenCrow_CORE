package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

// cutoverArtifactOptions names the already-created artifacts that a later
// cutover owner may consume.  This unit only binds and verifies them; it does
// not create a staging area, write a receipt, or touch a production path.
type cutoverArtifactOptions struct {
	BuildRoot                      string
	BuildReceipt                   string
	ExpectedBuildReceiptSHA256     string
	InstalledRuntime               string
	StagedRuntime                  string
	ExpectedInstalledRuntimeSHA256 string
	ExpectedStagedRuntimeSHA256    string
	RollbackDir                    string
	CutoverReceipt                 string
}

type cutoverArtifactPaths struct {
	buildRoot        string
	buildReceipt     string
	installedRuntime string
	stagedRuntime    string
	rollbackDir      string
	cutoverReceipt   string
}

type cutoverBoundFile struct {
	path        string
	info        os.FileInfo
	sha256      string
	bytes       int64
	require0600 bool
	sqlite      bool
}

// preparedCutoverArtifacts is private by design.  It carries only paths and
// measured bindings to the next owner; no path or identity value crosses a
// public receipt boundary.
type preparedCutoverArtifacts struct {
	paths              cutoverArtifactPaths
	buildReceipt       BuildReceipt
	buildReceiptSHA256 string
	outputFiles        map[string]buildOutputFile
	files              []cutoverBoundFile
}

// cutoverActiveOptions names the five legacy files that remain active until
// a later owner performs the coordinated cutover.  Their expected semantic
// values come exclusively from the already-bound BuildReceipt.
type cutoverActiveOptions struct {
	SourceDCI        string
	SourceDCIJSONL   string
	SourceEventStore string
	SourceL1         string
	SourceArchive    string
}

type cutoverActivePaths struct {
	dci        string
	dciJSONL   string
	eventStore string
	l1         string
	archive    string
}

type cutoverActiveSources struct {
	dciCounts SourceCounts
	dciHashes sourceHashes

	jsonRecords int
	jsonSteps   int
	jsonHash    string

	eventCounts SourceCounts
	eventHashes sourceHashes

	currentCounts SourceCounts
	currentHashes sourceHashes
	archiveCounts SourceCounts
	archiveHashes sourceHashes
}

// preparedCutoverActiveCohort is the private, read-only binding passed to a
// future cutover owner.  It retains the D2a-1 cohort and the owner-loaded
// active source data without publishing paths, identities, or payloads.
type preparedCutoverActiveCohort struct {
	build   preparedCutoverArtifacts
	paths   cutoverActivePaths
	files   []cutoverBoundFile
	sources cutoverActiveSources
}

// cutoverPrepareAfterBind is a package-local test seam.  Its production value
// is a no-op, and no alternate preparation route is exposed.
var cutoverPrepareAfterBind = func() error { return nil }

// cutoverPrepareActiveAfterBind is a package-local seam for deterministic
// source-change tests.  Production keeps it as a no-op.
var cutoverPrepareActiveAfterBind = func() error { return nil }

// prepareCutoverBuildCohort verifies one immutable build cohort and the two
// runtime binaries needed by a later cutover owner.  It deliberately performs
// no filesystem mutation, including no creation of prospective rollback or
// receipt paths.
func prepareCutoverBuildCohort(ctx context.Context, options cutoverArtifactOptions) (preparedCutoverArtifacts, error) {
	if ctx == nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if err := validateCutoverArtifactOptions(options); err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError(errorCode(err, "invalid_options"))
	}
	paths, err := resolveCutoverArtifactPaths(options)
	if err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("unsafe_path")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverArtifacts{}, err
	}

	receipt, receiptSHA256, receiptFile, err := readCutoverBuildReceipt(paths.buildReceipt)
	if err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("build_receipt")
	}
	if receiptSHA256 != options.ExpectedBuildReceiptSHA256 {
		return preparedCutoverArtifacts{}, cutoverPrepareError("build_receipt_sha256")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverArtifacts{}, err
	}

	outputFiles, outputBindings, err := verifyCutoverBuildOutputs(ctx, paths.buildRoot, receipt)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverArtifacts{}, err
		}
		return preparedCutoverArtifacts{}, cutoverPrepareError("build_output")
	}
	installed, err := bindCutoverFile(paths.installedRuntime, false, false)
	if err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("runtime")
	}
	if err := validateCutoverRuntimeBinding(installed); err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("runtime")
	}
	if installed.sha256 != options.ExpectedInstalledRuntimeSHA256 {
		return preparedCutoverArtifacts{}, cutoverPrepareError("installed_runtime_sha256")
	}
	staged, err := bindCutoverFile(paths.stagedRuntime, false, false)
	if err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("runtime")
	}
	if err := validateCutoverRuntimeBinding(staged); err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("runtime")
	}
	if staged.sha256 != options.ExpectedStagedRuntimeSHA256 {
		return preparedCutoverArtifacts{}, cutoverPrepareError("staged_runtime_sha256")
	}
	if err := validateCutoverExistingAliases(receiptFile, outputBindings, installed, staged); err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("unsafe_path")
	}

	files := make([]cutoverBoundFile, 0, 2+len(outputBindings))
	files = append(files, receiptFile)
	files = append(files, outputBindings...)
	files = append(files, installed, staged)
	if err := ctx.Err(); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if err := cutoverPrepareAfterBind(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverArtifacts{}, err
		}
		return preparedCutoverArtifacts{}, cutoverPrepareError("source_changed")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if err := verifyCutoverRootAndFreshPaths(paths); err != nil {
		return preparedCutoverArtifacts{}, cutoverPrepareError("source_changed")
	}
	for _, file := range files {
		if err := verifyCutoverBoundFile(file); err != nil {
			return preparedCutoverArtifacts{}, cutoverPrepareError("source_changed")
		}
		if err := ctx.Err(); err != nil {
			return preparedCutoverArtifacts{}, err
		}
	}

	return preparedCutoverArtifacts{
		paths: paths, buildReceipt: receipt, buildReceiptSHA256: receiptSHA256,
		outputFiles: outputFiles, files: files,
	}, nil
}

// prepareCutoverActiveCohort binds the currently active legacy sources to the
// already-prepared build/runtime cohort.  All expected semantic values are
// read from the retained ready BuildReceipt; activeOptions contains paths only.
// The operation is read-only and does not create or write any prospective
// cutover artifact.
func prepareCutoverActiveCohort(ctx context.Context, prepared preparedCutoverArtifacts, options cutoverActiveOptions) (preparedCutoverActiveCohort, error) {
	if ctx == nil {
		return preparedCutoverActiveCohort{}, cutoverPrepareError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := validateCutoverActiveOptions(options); err != nil {
		return preparedCutoverActiveCohort{}, cutoverPrepareError(errorCode(err, "invalid_options"))
	}

	build, err := revalidatePreparedCutoverBuild(ctx, prepared)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverActiveCohort{}, err
		}
		return preparedCutoverActiveCohort{}, cutoverPrepareError("build_cohort")
	}
	activePaths, err := resolveCutoverActivePaths(options)
	if err != nil {
		return preparedCutoverActiveCohort{}, cutoverPrepareError("unsafe_path")
	}
	activeFiles, err := bindCutoverActiveFiles(activePaths)
	if err != nil {
		return preparedCutoverActiveCohort{}, cutoverPrepareError("active_source")
	}
	if err := validateCutoverActiveAliases(build.files, activeFiles); err != nil {
		return preparedCutoverActiveCohort{}, cutoverPrepareError("unsafe_path")
	}
	if err := validateCutoverActiveProspectiveDisjoint(build.paths, activePaths); err != nil {
		return preparedCutoverActiveCohort{}, cutoverPrepareError("unsafe_path")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverActiveCohort{}, err
	}

	sources, err := loadCutoverActiveSources(ctx, activePaths, build.buildReceipt)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverActiveCohort{}, err
		}
		return preparedCutoverActiveCohort{}, cutoverPrepareError(errorCode(err, "active_source"))
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := cutoverPrepareActiveAfterBind(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverActiveCohort{}, err
		}
		return preparedCutoverActiveCohort{}, cutoverPrepareError("source_changed")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverActiveCohort{}, err
	}
	if err := verifyCutoverActiveBindings(activeFiles); err != nil {
		return preparedCutoverActiveCohort{}, cutoverPrepareError("source_changed")
	}
	if _, err := revalidatePreparedCutoverBuild(ctx, build); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverActiveCohort{}, err
		}
		return preparedCutoverActiveCohort{}, cutoverPrepareError("source_changed")
	}

	return preparedCutoverActiveCohort{build: build, paths: activePaths, files: activeFiles, sources: sources}, nil
}

func validateCutoverActiveOptions(options cutoverActiveOptions) error {
	for _, value := range []string{options.SourceDCI, options.SourceDCIJSONL, options.SourceEventStore, options.SourceL1, options.SourceArchive} {
		if strings.TrimSpace(value) == "" {
			return newCodedError("invalid_options", "active source paths are required")
		}
	}
	return nil
}

func resolveCutoverActivePaths(options cutoverActiveOptions) (cutoverActivePaths, error) {
	dci, err := resolveCutoverExistingPath(options.SourceDCI)
	if err != nil {
		return cutoverActivePaths{}, err
	}
	jsonl, err := resolveCutoverExistingPath(options.SourceDCIJSONL)
	if err != nil {
		return cutoverActivePaths{}, err
	}
	eventStore, err := resolveCutoverExistingPath(options.SourceEventStore)
	if err != nil {
		return cutoverActivePaths{}, err
	}
	l1, err := resolveCutoverExistingPath(options.SourceL1)
	if err != nil {
		return cutoverActivePaths{}, err
	}
	archive, err := resolveCutoverExistingPath(options.SourceArchive)
	if err != nil {
		return cutoverActivePaths{}, err
	}
	paths := cutoverActivePaths{dci: dci, dciJSONL: jsonl, eventStore: eventStore, l1: l1, archive: archive}
	values := []string{dci, jsonl, eventStore, l1, archive}
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if samePath(values[left], values[right]) {
				return cutoverActivePaths{}, errors.New("active source paths alias")
			}
		}
	}
	return paths, nil
}

func bindCutoverActiveFiles(paths cutoverActivePaths) ([]cutoverBoundFile, error) {
	items := []struct {
		path   string
		sqlite bool
	}{
		{path: paths.dci, sqlite: true},
		{path: paths.dciJSONL, sqlite: false},
		{path: paths.eventStore, sqlite: true},
		{path: paths.l1, sqlite: true},
		{path: paths.archive, sqlite: true},
	}
	files := make([]cutoverBoundFile, 0, len(items))
	for _, item := range items {
		binding, err := bindCutoverFile(item.path, false, item.sqlite)
		if err != nil {
			return nil, err
		}
		files = append(files, binding)
	}
	return files, nil
}

func validateCutoverActiveAliases(existing, active []cutoverBoundFile) error {
	files := make([]cutoverBoundFile, 0, len(existing)+len(active))
	files = append(files, existing...)
	files = append(files, active...)
	for left := 0; left < len(files); left++ {
		for right := left + 1; right < len(files); right++ {
			if samePath(files[left].path, files[right].path) || os.SameFile(files[left].info, files[right].info) {
				return errors.New("active source aliases a bound file")
			}
		}
	}
	return nil
}

func validateCutoverActiveProspectiveDisjoint(build cutoverArtifactPaths, active cutoverActivePaths) error {
	for _, activePath := range []string{active.dci, active.dciJSONL, active.eventStore, active.l1, active.archive} {
		if samePath(activePath, build.rollbackDir) || samePath(activePath, build.cutoverReceipt) {
			return errors.New("active source aliases a prospective path")
		}
	}
	return nil
}

func loadCutoverActiveSources(ctx context.Context, paths cutoverActivePaths, receipt BuildReceipt) (cutoverActiveSources, error) {
	if err := ctx.Err(); err != nil {
		return cutoverActiveSources{}, err
	}
	_, _, dciCounts, dciHashes, err := loadLegacyDCI(ctx, paths.dci)
	if err != nil {
		return cutoverActiveSources{}, cutoverActiveCauseError(err, "active_dci")
	}
	if err := compareCutoverActiveSQLiteHashes(receipt, "source_dci", dciHashes, false); err != nil {
		return cutoverActiveSources{}, err
	}

	_, jsonRecords, jsonSteps, jsonHash, err := loadLegacyJSONL(ctx, paths.dciJSONL)
	if err != nil {
		return cutoverActiveSources{}, cutoverActiveCauseError(err, "active_jsonl")
	}
	jsonHashes := sourceHashes{Classification: jsonHash, File: jsonHash}
	if err := compareCutoverActiveJSONLHashes(receipt, jsonHashes); err != nil {
		return cutoverActiveSources{}, err
	}

	currentL1, currentHashes, err := loadL1Current(ctx, paths.l1)
	if err != nil {
		return cutoverActiveSources{}, cutoverActiveCauseError(err, "active_l1")
	}
	if err := compareCutoverActiveSQLiteHashes(receipt, "source_l1", currentHashes, true); err != nil {
		return cutoverActiveSources{}, err
	}

	archiveL1, archiveHashes, err := loadL1Archive(ctx, paths.archive)
	if err != nil {
		return cutoverActiveSources{}, cutoverActiveCauseError(err, "active_archive")
	}
	if err := compareCutoverActiveSQLiteHashes(receipt, "source_archive", archiveHashes, true); err != nil {
		return cutoverActiveSources{}, err
	}

	_, eventCounts, eventHashes, err := loadEventStore(ctx, paths.eventStore)
	if err != nil {
		return cutoverActiveSources{}, cutoverActiveCauseError(err, "active_event_store")
	}
	if err := compareCutoverActiveSQLiteHashes(receipt, "source_event_store", eventHashes, true); err != nil {
		return cutoverActiveSources{}, err
	}
	currentCounts := currentL1.Counts
	currentCounts.CurrentDCIStaging = currentL1.DCIStaging
	archiveCounts := archiveL1.Counts
	archiveCounts.ArchiveDCIStaging = archiveL1.DCIStaging
	if err := compareCutoverActiveSourceCounts(receipt.SourceCounts, dciCounts, jsonRecords, jsonSteps, eventCounts, currentCounts, archiveCounts); err != nil {
		return cutoverActiveSources{}, err
	}
	return cutoverActiveSources{
		dciCounts: dciCounts, dciHashes: dciHashes,
		jsonRecords: jsonRecords, jsonSteps: jsonSteps, jsonHash: jsonHash,
		eventCounts: eventCounts, eventHashes: eventHashes,
		currentCounts: currentCounts, currentHashes: currentHashes,
		archiveCounts: archiveCounts, archiveHashes: archiveHashes,
	}, nil
}

func compareCutoverActiveSourceCounts(expected SourceCounts, dci SourceCounts, jsonRecords, jsonSteps int, eventStore, currentL1, archiveL1 SourceCounts) error {
	if dci.DCITraces != expected.DCITraces || dci.DCISteps != expected.DCISteps || dci.DCIEvidence != expected.DCIEvidence || dci.DCIQueryTerms != expected.DCIQueryTerms ||
		jsonRecords != expected.JSONLTraces || jsonSteps != expected.JSONLSteps ||
		eventStore.EventStore != expected.EventStore ||
		currentL1.CurrentStaging != expected.CurrentStaging || currentL1.CurrentDCIStaging != expected.CurrentDCIStaging || currentL1.CurrentRegistry != expected.CurrentRegistry ||
		archiveL1.ArchiveStaging != expected.ArchiveStaging || archiveL1.ArchiveDCIStaging != expected.ArchiveDCIStaging {
		return cutoverPrepareError("active_source_counts")
	}
	return nil
}

func compareCutoverActiveSQLiteHashes(receipt BuildReceipt, key string, got sourceHashes, requireNonDCI bool) error {
	if !sameCutoverSourceHash(got.DatabaseLogical, receipt.SourceDatabaseLogicalSHA256[key], true) ||
		!sameCutoverSourceHash(got.Schema, receipt.SourceSchemaSHA256[key], true) ||
		!sameCutoverSourceHash(got.Classification, receipt.SourceDCIClassificationSHA256[key], true) ||
		!sameCutoverSourceHash(got.File, "", false) {
		return cutoverPrepareError("active_source_binding")
	}
	if requireNonDCI {
		if !sameCutoverSourceHash(got.NonDCI, receipt.SourceNonDCILogicalSHA256[key], true) {
			return cutoverPrepareError("active_source_binding")
		}
	} else if got.NonDCI != "" {
		return cutoverPrepareError("active_source_binding")
	}
	return nil
}

func compareCutoverActiveJSONLHashes(receipt BuildReceipt, got sourceHashes) error {
	if !sameCutoverSourceHash(got.Classification, receipt.SourceDCIClassificationSHA256["source_dci_jsonl"], true) ||
		!sameCutoverSourceHash(got.File, receipt.SourceFileSHA256["source_dci_jsonl"], true) ||
		got.DatabaseLogical != "" || got.Schema != "" || got.NonDCI != "" {
		return cutoverPrepareError("active_source_binding")
	}
	return nil
}

func sameCutoverSourceHash(got, want string, required bool) bool {
	if !required {
		return got == ""
	}
	return want != "" && got == want
}

func cutoverActiveCauseError(err error, fallback string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return cutoverPrepareError(errorCode(err, fallback))
}

func verifyCutoverActiveBindings(files []cutoverBoundFile) error {
	if len(files) != 5 {
		return errors.New("active source bindings are incomplete")
	}
	for _, file := range files {
		if err := verifyCutoverBoundFile(file); err != nil {
			return err
		}
	}
	return nil
}

func revalidatePreparedCutoverBuild(ctx context.Context, prepared preparedCutoverArtifacts) (preparedCutoverArtifacts, error) {
	result, err := revalidatePreparedCutoverBuildFiles(ctx, prepared)
	if err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if err := validateCutoverProspectiveAliases(result.paths); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if err := verifyCutoverFreshPaths(result.paths); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	return result, nil
}

func revalidatePreparedCutoverBuildFiles(ctx context.Context, prepared preparedCutoverArtifacts) (preparedCutoverArtifacts, error) {
	if ctx == nil {
		return preparedCutoverArtifacts{}, errors.New("invalid context")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if strings.TrimSpace(prepared.paths.buildRoot) == "" || strings.TrimSpace(prepared.paths.buildReceipt) == "" || strings.TrimSpace(prepared.paths.installedRuntime) == "" || strings.TrimSpace(prepared.paths.stagedRuntime) == "" || strings.TrimSpace(prepared.paths.rollbackDir) == "" || strings.TrimSpace(prepared.paths.cutoverReceipt) == "" {
		return preparedCutoverArtifacts{}, errors.New("prepared build paths are incomplete")
	}
	if prepared.buildReceipt.Status != StatusReady || prepared.buildReceipt.SchemaVersion != BuildSchemaVersion || prepared.buildReceipt.Mode != ModeBuild || validateBuildReceipt(prepared.buildReceipt) != nil || !isLowerHexSHA256(prepared.buildReceiptSHA256) || len(prepared.outputFiles) != 4 || len(prepared.files) != 7 {
		return preparedCutoverArtifacts{}, errors.New("prepared build cohort is invalid")
	}
	if !safeBuildRootForReceipt(prepared.paths.buildRoot) {
		return preparedCutoverArtifacts{}, errors.New("prepared build root is invalid")
	}
	if err := verifyCutoverBuildRoot(prepared.paths.buildRoot); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if _, err := resolveCutoverExistingPath(prepared.paths.buildReceipt); err != nil || !samePath(prepared.paths.buildReceipt, filepath.Join(prepared.paths.buildRoot, BuildReceiptFilename)) {
		return preparedCutoverArtifacts{}, errors.New("prepared build receipt path is invalid")
	}
	if _, err := resolveCutoverExistingPath(prepared.paths.installedRuntime); err != nil {
		return preparedCutoverArtifacts{}, errors.New("prepared installed runtime path is invalid")
	}
	if _, err := resolveCutoverExistingPath(prepared.paths.stagedRuntime); err != nil {
		return preparedCutoverArtifacts{}, errors.New("prepared staged runtime path is invalid")
	}

	receipt, receiptSHA256, receiptFile, err := readCutoverBuildReceipt(prepared.paths.buildReceipt)
	if err != nil || receiptSHA256 != prepared.buildReceiptSHA256 || !reflect.DeepEqual(receipt, prepared.buildReceipt) {
		return preparedCutoverArtifacts{}, errors.New("prepared build receipt changed")
	}
	outputFiles, outputBindings, err := verifyCutoverBuildOutputs(ctx, prepared.paths.buildRoot, receipt)
	if err != nil {
		return preparedCutoverArtifacts{}, err
	}
	installed, err := bindCutoverFile(prepared.paths.installedRuntime, false, false)
	if err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if err := validateCutoverRuntimeBinding(installed); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	staged, err := bindCutoverFile(prepared.paths.stagedRuntime, false, false)
	if err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if err := validateCutoverRuntimeBinding(staged); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	if err := validateCutoverExistingAliases(receiptFile, outputBindings, installed, staged); err != nil {
		return preparedCutoverArtifacts{}, err
	}
	actualFiles := make([]cutoverBoundFile, 0, 7)
	actualFiles = append(actualFiles, receiptFile)
	actualFiles = append(actualFiles, outputBindings...)
	actualFiles = append(actualFiles, installed, staged)
	for index, file := range prepared.files {
		if err := verifyCutoverBoundFile(file); err != nil || !sameCutoverBoundFile(file, actualFiles[index]) {
			return preparedCutoverArtifacts{}, errors.New("prepared build binding changed")
		}
	}
	if !reflect.DeepEqual(outputFiles, prepared.outputFiles) {
		return preparedCutoverArtifacts{}, errors.New("prepared build output binding changed")
	}
	return preparedCutoverArtifacts{
		paths: prepared.paths, buildReceipt: receipt, buildReceiptSHA256: receiptSHA256,
		outputFiles: outputFiles, files: actualFiles,
	}, nil
}

func sameCutoverBoundFile(left, right cutoverBoundFile) bool {
	if left.path != right.path || left.sha256 != right.sha256 || left.bytes != right.bytes || left.require0600 != right.require0600 || left.sqlite != right.sqlite || left.info == nil || right.info == nil {
		return false
	}
	if runtime.GOOS != "windows" && left.info.Mode().Perm() != right.info.Mode().Perm() {
		return false
	}
	return os.SameFile(left.info, right.info)
}

func validateCutoverArtifactOptions(options cutoverArtifactOptions) error {
	for _, value := range []string{
		options.BuildRoot, options.BuildReceipt, options.InstalledRuntime,
		options.StagedRuntime, options.RollbackDir, options.CutoverReceipt,
	} {
		if strings.TrimSpace(value) == "" {
			return newCodedError("invalid_options", "cutover artifact paths are required")
		}
	}
	for _, value := range []string{options.ExpectedBuildReceiptSHA256, options.ExpectedInstalledRuntimeSHA256, options.ExpectedStagedRuntimeSHA256} {
		if !isLowerHexSHA256(value) {
			return newCodedError("invalid_options", "cutover artifact hashes must be lowercase SHA-256")
		}
	}
	return nil
}

func resolveCutoverArtifactPaths(options cutoverArtifactOptions) (cutoverArtifactPaths, error) {
	buildRoot, err := absolutePath(options.BuildRoot)
	if err != nil || !safeBuildRootForReceipt(buildRoot) {
		return cutoverArtifactPaths{}, newCodedError("unsafe_path", "build root is unsafe")
	}
	buildReceipt, err := resolveCutoverExistingPath(options.BuildReceipt)
	if err != nil || !samePath(buildReceipt, filepath.Join(buildRoot, BuildReceiptFilename)) {
		return cutoverArtifactPaths{}, newCodedError("unsafe_path", "build receipt path is unsafe")
	}
	installed, err := resolveCutoverExistingPath(options.InstalledRuntime)
	if err != nil {
		return cutoverArtifactPaths{}, newCodedError("unsafe_path", "installed runtime path is unsafe")
	}
	staged, err := resolveCutoverExistingPath(options.StagedRuntime)
	if err != nil {
		return cutoverArtifactPaths{}, newCodedError("unsafe_path", "staged runtime path is unsafe")
	}
	rollback, err := resolveCutoverFreshPath(options.RollbackDir)
	if err != nil {
		return cutoverArtifactPaths{}, newCodedError("unsafe_path", "rollback path is unsafe")
	}
	receipt, err := resolveCutoverFreshPath(options.CutoverReceipt)
	if err != nil {
		return cutoverArtifactPaths{}, newCodedError("unsafe_path", "cutover receipt path is unsafe")
	}

	paths := cutoverArtifactPaths{
		buildRoot: buildRoot, buildReceipt: buildReceipt, installedRuntime: installed,
		stagedRuntime: staged, rollbackDir: rollback, cutoverReceipt: receipt,
	}
	if err := validateCutoverProspectiveAliases(paths); err != nil {
		return cutoverArtifactPaths{}, err
	}
	return paths, nil
}

func resolveCutoverExistingPath(raw string) (string, error) {
	path, err := absolutePath(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("cutover path is not a regular non-symlink file")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || !samePath(path, filepath.Clean(realPath)) {
		return "", errors.New("cutover path is not canonical")
	}
	if err := validateCutoverCanonicalParent(path); err != nil {
		return "", err
	}
	return path, nil
}

func resolveCutoverFreshPath(raw string) (string, error) {
	path, err := absolutePath(raw)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("cutover prospective path is not fresh")
	}
	if err := validateCutoverCanonicalParent(path); err != nil {
		return "", err
	}
	return path, nil
}

func validateCutoverCanonicalParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("cutover path parent is unsafe")
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !samePath(parent, filepath.Clean(realParent)) {
		return errors.New("cutover path parent is not canonical")
	}
	return nil
}

func readCutoverBuildReceipt(path string) (BuildReceipt, string, cutoverBoundFile, error) {
	info, err := inspectCutoverFile(path, true, false)
	if err != nil {
		return BuildReceipt{}, "", cutoverBoundFile{}, err
	}
	data, err := readBuildInputBytes(path, maxBuildReceiptBytes)
	if err != nil || rejectDuplicateJSONKeys(data) != nil {
		return BuildReceipt{}, "", cutoverBoundFile{}, errors.New("build receipt is not strict JSON")
	}
	var receipt BuildReceipt
	if err := decodeOneBuildInputObject(data, &receipt); err != nil {
		return BuildReceipt{}, "", cutoverBoundFile{}, err
	}
	if receipt.Status != StatusReady || receipt.SchemaVersion != BuildSchemaVersion || receipt.Mode != ModeBuild {
		return BuildReceipt{}, "", cutoverBoundFile{}, errors.New("build receipt is not ready")
	}
	if err := validateBuildReceipt(receipt); err != nil {
		return BuildReceipt{}, "", cutoverBoundFile{}, err
	}
	sha256 := buildInputBytesSHA256(data)
	binding, err := bindCutoverFile(path, true, false)
	if err != nil || !os.SameFile(info, binding.info) {
		return BuildReceipt{}, "", cutoverBoundFile{}, errors.New("build receipt changed")
	}
	return receipt, sha256, binding, nil
}

func verifyCutoverBuildOutputs(ctx context.Context, root string, receipt BuildReceipt) (map[string]buildOutputFile, []cutoverBoundFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 5 {
		return nil, nil, errors.New("build root entry set is invalid")
	}
	expected := map[string]struct{}{
		BuildReceiptFilename: {}, buildOutputDCIFilename: {}, buildOutputEventStoreFilename: {},
		buildOutputL1Filename: {}, buildOutputArchiveFilename: {},
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return nil, nil, errors.New("build root entry set is invalid")
		}
	}
	targets := buildOutputTargets(root)
	files := make(map[string]buildOutputFile, len(targets))
	bindings := make([]cutoverBoundFile, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		binding, err := bindCutoverFile(target.path, true, true)
		if err != nil {
			return nil, nil, err
		}
		artifact, ok := receipt.OutputArtifacts[target.role]
		if !ok || artifact.FileSHA256 != binding.sha256 || artifact.Bytes != binding.bytes {
			return nil, nil, errors.New("build output does not match receipt")
		}
		target.sha256, target.bytes = binding.sha256, binding.bytes
		target.quickCheckOK, target.sidecarZero = artifact.QuickCheckOK, artifact.SidecarZero
		files[target.role] = target
		bindings = append(bindings, binding)
	}
	if len(files) != len(targets) || buildOutputArtifactSetSHA256(files) != receipt.OutputArtifactSetSHA256 {
		return nil, nil, errors.New("build output artifact set does not match receipt")
	}
	return files, bindings, nil
}

func inspectCutoverFile(path string, require0600, sqlite bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("cutover file is unsafe")
	}
	if require0600 && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, errors.New("cutover file permissions are invalid")
	}
	if sqlite {
		if err := rejectSQLiteSidecars(path); err != nil {
			return nil, err
		}
	}
	return info, nil
}

func bindCutoverFile(path string, require0600, sqlite bool) (cutoverBoundFile, error) {
	info, err := inspectCutoverFile(path, require0600, sqlite)
	if err != nil {
		return cutoverBoundFile{}, err
	}
	hash, bytes, err := hashBuildFile(path)
	if err != nil {
		return cutoverBoundFile{}, err
	}
	latest, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, latest) || latest.Size() != bytes {
		return cutoverBoundFile{}, errors.New("cutover file changed")
	}
	return cutoverBoundFile{path: path, info: latest, sha256: hash, bytes: bytes, require0600: require0600, sqlite: sqlite}, nil
}

func validateCutoverRuntimeBinding(binding cutoverBoundFile) error {
	if binding.info == nil || !binding.info.Mode().IsRegular() {
		return errors.New("runtime is not a regular file")
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	if binding.info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("runtime special permissions are unsafe")
	}
	perm := binding.info.Mode().Perm()
	if perm&0o100 == 0 || perm&(0o020|0o002) != 0 {
		return errors.New("runtime permissions are unsafe")
	}
	return nil
}

func verifyCutoverBoundFile(binding cutoverBoundFile) error {
	if binding.info == nil {
		return errors.New("cutover file binding is incomplete")
	}
	info, err := inspectCutoverFile(binding.path, binding.require0600, binding.sqlite)
	if err != nil || !os.SameFile(binding.info, info) || info.Size() != binding.bytes || (runtime.GOOS != "windows" && info.Mode().Perm() != binding.info.Mode().Perm()) {
		return errors.New("cutover file changed")
	}
	hash, bytes, err := hashBuildFile(binding.path)
	if err != nil || hash != binding.sha256 || bytes != binding.bytes {
		return errors.New("cutover file changed")
	}
	return nil
}

func validateCutoverExistingAliases(receipt cutoverBoundFile, outputs []cutoverBoundFile, installed, staged cutoverBoundFile) error {
	files := make([]cutoverBoundFile, 0, 2+len(outputs))
	files = append(files, receipt)
	files = append(files, outputs...)
	files = append(files, installed, staged)
	for left := 0; left < len(files); left++ {
		for right := left + 1; right < len(files); right++ {
			if samePath(files[left].path, files[right].path) || os.SameFile(files[left].info, files[right].info) {
				return errors.New("cutover artifacts alias")
			}
		}
	}
	return nil
}

func validateCutoverProspectiveAliases(paths cutoverArtifactPaths) error {
	existing := []string{paths.buildRoot, paths.buildReceipt, paths.installedRuntime, paths.stagedRuntime}
	for _, target := range buildOutputTargets(paths.buildRoot) {
		existing = append(existing, target.path)
	}
	prospective := []string{paths.rollbackDir, paths.cutoverReceipt}
	for index, left := range prospective {
		if pathWithinOrRoot(paths.buildRoot, left) {
			return newCodedError("unsafe_path", "cutover prospective path is inside build root")
		}
		for _, right := range existing {
			if samePath(left, right) {
				return newCodedError("unsafe_path", "cutover paths alias")
			}
		}
		for _, right := range prospective[index+1:] {
			if samePath(left, right) {
				return newCodedError("unsafe_path", "cutover paths alias")
			}
		}
	}
	if samePath(paths.buildReceipt, filepath.Join(paths.buildRoot, BuildReceiptFilename)) == false {
		return newCodedError("unsafe_path", "build receipt path is invalid")
	}
	return nil
}

func verifyCutoverRootAndFreshPaths(paths cutoverArtifactPaths) error {
	if err := verifyCutoverBuildRoot(paths.buildRoot); err != nil {
		return err
	}
	return verifyCutoverFreshPaths(paths)
}

func verifyCutoverBuildRoot(root string) error {
	if !safeBuildRootForReceipt(root) {
		return errors.New("build root changed")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 5 {
		return errors.New("build root changed")
	}
	expected := map[string]struct{}{
		BuildReceiptFilename: {}, buildOutputDCIFilename: {}, buildOutputEventStoreFilename: {},
		buildOutputL1Filename: {}, buildOutputArchiveFilename: {},
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return errors.New("build root changed")
		}
	}
	return nil
}

func verifyCutoverFreshPaths(paths cutoverArtifactPaths) error {
	for _, path := range []string{paths.rollbackDir, paths.cutoverReceipt} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("prospective cutover path changed")
		}
		if err := validateCutoverCanonicalParent(path); err != nil {
			return err
		}
	}
	return nil
}

func cutoverPrepareError(code string) error {
	if code == "" || !validErrorCode(code) {
		code = "cutover_prepare"
	}
	return newCodedError(code, "offline DCI cutover preparation failed")
}
