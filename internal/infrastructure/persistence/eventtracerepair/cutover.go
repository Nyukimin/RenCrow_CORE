package eventtracerepair

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	ModeApply = "apply"

	CutoverManifestSchemaVersion = "rencrow.identity.event-trace-cutover/v2"
	CutoverStatusApplied         = "applied"
	CutoverStatusBlocked         = "blocked"
	CutoverStatusRolledBack      = "rolled_back"
	CutoverStatusRollbackFailed  = "rollback_failed"

	rollbackDatabaseName = "active-store.db"
	rollbackRuntimeName  = "installed-runtime"
)

// ApplyOptions describes the only inputs accepted by the production cutover
// boundary. The source, rebuilt output, and staged runtime are snapshot
// artifacts; activeStore and installedRuntime are the exact live targets.
type ApplyOptions struct {
	SnapshotDir                    string
	SourceStore                    string
	OutputStore                    string
	BuildManifest                  string
	ExpectedBuildManifestSHA256    string
	ActiveStore                    string
	RollbackDir                    string
	InstalledRuntimeBinary         string
	StagedRuntimeBinary            string
	ExpectedInstalledRuntimeSHA256 string
	ExpectedStagedRuntimeSHA256    string
	Manifest                       string
}

// CutoverManifest is deliberately separate from the repair manifest. It is a
// bounded receipt for the coordinated database/runtime replacement and never
// includes payloads, event lists, or filesystem paths.
type CutoverManifest struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`

	BuildManifestSHA256   string `json:"build_manifest_sha256,omitempty"`
	BeforeDBSHA256        string `json:"before_db_sha256,omitempty"`
	AfterDBSHA256         string `json:"after_db_sha256,omitempty"`
	RollbackDBSHA256      string `json:"rollback_db_sha256,omitempty"`
	BeforeRuntimeSHA256   string `json:"before_runtime_sha256,omitempty"`
	AfterRuntimeSHA256    string `json:"after_runtime_sha256,omitempty"`
	RollbackRuntimeSHA256 string `json:"rollback_runtime_sha256,omitempty"`

	InputCount                 int    `json:"input_count"`
	RepairJobCount             int    `json:"repair_job_count"`
	RepairSegmentCount         int    `json:"repair_segment_count"`
	RepairEventCount           int    `json:"repair_event_count"`
	VerifiedJobCount           int    `json:"verified_job_count"`
	RepairableJobCount         int    `json:"repairable_job_count"`
	UnresolvedJobCount         int    `json:"unresolved_job_count"`
	RepairIdleChatRunCount     int    `json:"repair_idlechat_run_count"`
	VerifiedIdleChatRunCount   int    `json:"verified_idlechat_run_count"`
	RepairableIdleChatRunCount int    `json:"repairable_idlechat_run_count"`
	UnresolvedIdleChatRunCount int    `json:"unresolved_idlechat_run_count"`
	InputEventSetSHA256        string `json:"input_event_set_sha256,omitempty"`
	OutputEventSetSHA256       string `json:"output_event_set_sha256,omitempty"`
	NonTraceContentSHA256      string `json:"non_trace_content_sha256,omitempty"`
	ErrorCode                  string `json:"error_code,omitempty"`
}

type applyPaths struct {
	root             string
	source           string
	output           string
	buildManifest    string
	active           string
	rollback         string
	installedRuntime string
	stagedRuntime    string
	manifest         string
}

type cutoverSnapshot struct {
	path       string
	fileSHA256 string
	events     []modulecore.EventEnvelope
	eventHash  string
	nonTrace   string
}

// These hooks are package-local seams for deterministic failure tests. The
// production defaults remain the OS atomic replacement, post-swap verifier,
// and atomic receipt writer respectively.
var replaceTargetFile = atomicReplaceFile

var cutoverPostReplaceHook func() error

var cutoverReceiptWriter = writeCutoverReceipt

// Apply validates a checksum-bound build artifact, stages both replacements,
// atomically swaps the active database and runtime, and restores both exact
// backups on any post-swap failure.
func Apply(ctx context.Context, options ApplyOptions) (CutoverManifest, error) {
	receipt := CutoverManifest{
		SchemaVersion: CutoverManifestSchemaVersion,
		Mode:          ModeApply,
		Status:        CutoverStatusBlocked,
	}
	if err := validateApplyOptions(options); err != nil {
		return cutoverFailure(receipt, nil, "invalid_options", err)
	}
	paths, err := resolveApplyPaths(options)
	if err != nil {
		return cutoverFailure(receipt, nil, "invalid_path", err)
	}
	receipt.BuildManifestSHA256, err = fileSHA256(paths.buildManifest)
	if err != nil {
		return cutoverFailure(receipt, &paths, "build_manifest_read", err)
	}
	if receipt.BuildManifestSHA256 != options.ExpectedBuildManifestSHA256 {
		return cutoverFailure(receipt, &paths, "build_manifest_sha256", fmt.Errorf("build manifest SHA256=%s want=%s", receipt.BuildManifestSHA256, options.ExpectedBuildManifestSHA256))
	}
	buildManifest, err := readManifest(paths.buildManifest)
	if err != nil {
		return cutoverFailure(receipt, &paths, "build_manifest_invalid", err)
	}
	if err := validateBuildManifestForApply(buildManifest); err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "build_manifest_invalid"), err)
	}
	copyRepairCountsToCutover(&receipt, buildManifest)

	source, err := readCutoverSnapshot(ctx, paths.source)
	if err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "source_read"), err)
	}
	if err := verifySourceAgainstBuild(source, buildManifest); err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "source_mismatch"), err)
	}
	output, err := readCutoverSnapshot(ctx, paths.output)
	if err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "output_read"), err)
	}
	if err := verifyOutputAgainstBuild(source, output, buildManifest); err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "output_mismatch"), err)
	}
	if err := rejectActiveSidecars(paths.active); err != nil {
		return cutoverFailure(receipt, &paths, "active_sidecar", err)
	}
	active, err := readCutoverSnapshot(ctx, paths.active)
	if err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "active_read"), err)
	}
	if err := verifyActiveAgainstSource(active, source); err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "active_mismatch"), err)
	}
	installedSHA, err := fileSHA256(paths.installedRuntime)
	if err != nil {
		return cutoverFailure(receipt, &paths, "installed_runtime_read", err)
	}
	if installedSHA != options.ExpectedInstalledRuntimeSHA256 {
		return cutoverFailure(receipt, &paths, "installed_runtime_sha256", fmt.Errorf("installed runtime SHA256=%s want=%s", installedSHA, options.ExpectedInstalledRuntimeSHA256))
	}
	stagedRuntimeSHA, err := fileSHA256(paths.stagedRuntime)
	if err != nil {
		return cutoverFailure(receipt, &paths, "staged_runtime_read", err)
	}
	if stagedRuntimeSHA != options.ExpectedStagedRuntimeSHA256 {
		return cutoverFailure(receipt, &paths, "staged_runtime_sha256", fmt.Errorf("staged runtime SHA256=%s want=%s", stagedRuntimeSHA, options.ExpectedStagedRuntimeSHA256))
	}
	receipt.BeforeDBSHA256 = active.fileSHA256
	receipt.BeforeRuntimeSHA256 = installedSHA

	if err := verifyCutoverPreSwap(ctx, &paths, options, buildManifest); err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "pre_swap_mismatch"), err)
	}
	activeMode, err := fileMode(paths.active)
	if err != nil {
		return cutoverFailure(receipt, &paths, "active_mode", err)
	}
	installedMode, err := fileMode(paths.installedRuntime)
	if err != nil {
		return cutoverFailure(receipt, &paths, "installed_runtime_mode", err)
	}
	if err := os.Mkdir(paths.rollback, 0700); err != nil {
		return cutoverFailure(receipt, &paths, "rollback_create", err)
	}
	if err := os.Chmod(paths.rollback, 0700); err != nil {
		return cutoverFailure(receipt, &paths, "rollback_create", err)
	}
	if err := syncDirectory(filepath.Dir(paths.rollback)); err != nil {
		return cutoverFailure(receipt, &paths, "rollback_sync", err)
	}
	rollbackDB := filepath.Join(paths.rollback, rollbackDatabaseName)
	rollbackRuntime := filepath.Join(paths.rollback, rollbackRuntimeName)
	if err := copyFileSync(paths.active, rollbackDB, activeMode); err != nil {
		return cutoverFailure(receipt, &paths, "rollback_backup", err)
	}
	if err := copyFileSync(paths.installedRuntime, rollbackRuntime, installedMode); err != nil {
		return cutoverFailure(receipt, &paths, "rollback_backup", err)
	}
	receipt.RollbackDBSHA256, err = fileSHA256(rollbackDB)
	if err != nil {
		return cutoverFailure(receipt, &paths, "rollback_backup", err)
	}
	receipt.RollbackRuntimeSHA256, err = fileSHA256(rollbackRuntime)
	if err != nil {
		return cutoverFailure(receipt, &paths, "rollback_backup", err)
	}
	if receipt.RollbackDBSHA256 != receipt.BeforeDBSHA256 || receipt.RollbackRuntimeSHA256 != receipt.BeforeRuntimeSHA256 {
		return cutoverFailure(receipt, &paths, "rollback_backup_mismatch", fmt.Errorf("rollback backup does not match pre-swap targets"))
	}

	dbStage, err := stageFile(paths.output, filepath.Dir(paths.active), activeMode)
	if err != nil {
		return cutoverFailure(receipt, &paths, "staging_db", err)
	}
	runtimeStage, err := stageFile(paths.stagedRuntime, filepath.Dir(paths.installedRuntime), installedMode)
	if err != nil {
		_ = os.Remove(dbStage)
		return cutoverFailure(receipt, &paths, "staging_runtime", err)
	}
	defer func() {
		_ = os.Remove(dbStage)
		_ = os.Remove(runtimeStage)
	}()
	stagedDBSHA, err := fileSHA256(dbStage)
	if err != nil || stagedDBSHA != output.fileSHA256 {
		if err == nil {
			err = fmt.Errorf("staged database does not match rebuilt output")
		}
		return cutoverFailure(receipt, &paths, "staging_db_mismatch", err)
	}
	if stagedRuntimeSHA, err = fileSHA256(runtimeStage); err != nil || stagedRuntimeSHA != options.ExpectedStagedRuntimeSHA256 {
		if err == nil {
			err = fmt.Errorf("staged runtime does not match expected SHA256")
		}
		return cutoverFailure(receipt, &paths, "staging_runtime_mismatch", err)
	}

	if err := verifyCutoverPreSwap(ctx, &paths, options, buildManifest); err != nil {
		return cutoverFailure(receipt, &paths, errorCode(err, "pre_swap_mismatch"), err)
	}
	if latestStageSHA, stageErr := fileSHA256(dbStage); stageErr != nil || latestStageSHA != stagedDBSHA {
		if stageErr == nil {
			stageErr = fmt.Errorf("staged database changed before swap")
		}
		return cutoverFailure(receipt, &paths, "staging_db_mismatch", stageErr)
	}
	if latestStageSHA, stageErr := fileSHA256(runtimeStage); stageErr != nil || latestStageSHA != options.ExpectedStagedRuntimeSHA256 {
		if stageErr == nil {
			stageErr = fmt.Errorf("staged runtime changed before swap")
		}
		return cutoverFailure(receipt, &paths, "staging_runtime_mismatch", stageErr)
	}
	if err := replaceTargetFile(dbStage, paths.active); err != nil {
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "db_replace", err)
	}
	if err := replaceTargetFile(runtimeStage, paths.installedRuntime); err != nil {
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "runtime_replace", err)
	}

	if cutoverPostReplaceHook != nil {
		if err := cutoverPostReplaceHook(); err != nil {
			return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "post_replace_verify", err)
		}
	}
	activeAfter, err := readCutoverSnapshot(ctx, paths.active)
	if err != nil {
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "post_replace_verify", err)
	}
	if err := verifyActiveAgainstOutput(activeAfter, output, buildManifest); err != nil {
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "post_replace_verify", err)
	}
	if err := quickCheck(ctx, paths.active); err != nil {
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "post_replace_quick_check", err)
	}
	receipt.AfterDBSHA256, err = fileSHA256(paths.active)
	if err != nil {
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "post_replace_verify", err)
	}
	if receipt.AfterDBSHA256 != stagedDBSHA {
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "post_replace_verify", fmt.Errorf("active database SHA256=%s want rebuilt copy %s", receipt.AfterDBSHA256, stagedDBSHA))
	}
	receipt.AfterRuntimeSHA256, err = fileSHA256(paths.installedRuntime)
	if err != nil || receipt.AfterRuntimeSHA256 != options.ExpectedStagedRuntimeSHA256 {
		if err == nil {
			err = fmt.Errorf("installed runtime after swap does not match staged SHA256")
		}
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "post_replace_verify", err)
	}
	receipt.Status = CutoverStatusApplied
	if err := cutoverReceiptWriter(paths.manifest, receipt); err != nil {
		return rollbackCutover(ctx, receipt, paths, rollbackDB, rollbackRuntime, activeMode, installedMode, "receipt_write", err)
	}
	return receipt, nil
}

func validateApplyOptions(options ApplyOptions) error {
	values := map[string]string{
		"snapshot-dir":                      options.SnapshotDir,
		"source-store":                      options.SourceStore,
		"output-store":                      options.OutputStore,
		"build-manifest":                    options.BuildManifest,
		"expected-build-manifest-sha256":    options.ExpectedBuildManifestSHA256,
		"active-store":                      options.ActiveStore,
		"rollback-dir":                      options.RollbackDir,
		"installed-runtime-binary":          options.InstalledRuntimeBinary,
		"staged-runtime-binary":             options.StagedRuntimeBinary,
		"expected-installed-runtime-sha256": options.ExpectedInstalledRuntimeSHA256,
		"expected-staged-runtime-sha256":    options.ExpectedStagedRuntimeSHA256,
		"manifest":                          options.Manifest,
	}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if err := validateSHA256(options.ExpectedBuildManifestSHA256); err != nil {
		return fmt.Errorf("expected-build-manifest-sha256: %w", err)
	}
	if err := validateSHA256(options.ExpectedInstalledRuntimeSHA256); err != nil {
		return fmt.Errorf("expected-installed-runtime-sha256: %w", err)
	}
	if err := validateSHA256(options.ExpectedStagedRuntimeSHA256); err != nil {
		return fmt.Errorf("expected-staged-runtime-sha256: %w", err)
	}
	return nil
}

func resolveApplyPaths(options ApplyOptions) (applyPaths, error) {
	root, err := resolveDirectory(options.SnapshotDir)
	if err != nil {
		return applyPaths{}, fmt.Errorf("snapshot directory: %w", err)
	}
	source, err := boundedApplyPath(root, options.SourceStore, true)
	if err != nil {
		return applyPaths{}, fmt.Errorf("source store: %w", err)
	}
	output, err := boundedApplyPath(root, options.OutputStore, true)
	if err != nil {
		return applyPaths{}, fmt.Errorf("output store: %w", err)
	}
	buildManifest, err := boundedApplyPath(root, options.BuildManifest, true)
	if err != nil {
		return applyPaths{}, fmt.Errorf("build manifest: %w", err)
	}
	stagedRuntime, err := boundedApplyPath(root, options.StagedRuntimeBinary, true)
	if err != nil {
		return applyPaths{}, fmt.Errorf("staged runtime: %w", err)
	}
	manifest, err := boundedApplyPath(root, options.Manifest, false)
	if err != nil {
		return applyPaths{}, fmt.Errorf("manifest: %w", err)
	}
	active, err := exactApplyFile(options.ActiveStore)
	if err != nil {
		return applyPaths{}, fmt.Errorf("active store: %w", err)
	}
	installedRuntime, err := exactApplyFile(options.InstalledRuntimeBinary)
	if err != nil {
		return applyPaths{}, fmt.Errorf("installed runtime: %w", err)
	}
	rollback, err := newRollbackPath(options.RollbackDir)
	if err != nil {
		return applyPaths{}, fmt.Errorf("rollback directory: %w", err)
	}
	paths := applyPaths{root: root, source: source, output: output, buildManifest: buildManifest, active: active, rollback: rollback, installedRuntime: installedRuntime, stagedRuntime: stagedRuntime, manifest: manifest}
	allFiles := []string{source, output, buildManifest, active, installedRuntime, stagedRuntime, manifest}
	for index, left := range allFiles {
		for _, right := range allFiles[index+1:] {
			if left == right {
				return applyPaths{}, fmt.Errorf("target paths must be distinct")
			}
		}
		if rollback == left {
			return applyPaths{}, fmt.Errorf("rollback directory and target paths must be distinct")
		}
	}
	type existingFile struct {
		path string
		info os.FileInfo
	}
	existing := make([]existingFile, 0, len(allFiles))
	for _, path := range allFiles {
		info, statErr := os.Stat(path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return applyPaths{}, fmt.Errorf("stat %q: %w", path, statErr)
		}
		for _, prior := range existing {
			if os.SameFile(prior.info, info) {
				return applyPaths{}, fmt.Errorf("file inputs and targets must not be hardlink aliases: %q and %q", prior.path, path)
			}
		}
		existing = append(existing, existingFile{path: path, info: info})
	}
	return paths, nil
}

func resolveDirectory(raw string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func boundedApplyPath(root, raw string, mustExist bool) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	path := filepath.Clean(filepath.Join(parent, filepath.Base(abs)))
	if !pathWithin(root, path) {
		return "", fmt.Errorf("path must stay inside snapshot directory")
	}
	info, err := os.Lstat(abs)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("must be a regular non-symlink file")
		}
		resolved, resolveErr := filepath.EvalSymlinks(abs)
		if resolveErr != nil {
			return "", resolveErr
		}
		path = filepath.Clean(resolved)
		if !pathWithin(root, path) || path != filepath.Clean(filepath.Join(parent, filepath.Base(abs))) {
			return "", fmt.Errorf("path resolves outside snapshot directory or through a symlink")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if mustExist {
		return "", err
	}
	if !pathWithin(root, path) {
		return "", fmt.Errorf("path must stay inside snapshot directory")
	}
	return path, nil
}

func exactApplyFile(raw string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("must be a regular non-symlink file")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolved) != filepath.Clean(filepath.Join(parent, filepath.Base(abs))) {
		return "", fmt.Errorf("path resolves through a symlink")
	}
	return filepath.Clean(resolved), nil
}

func newRollbackPath(raw string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if info, statErr := os.Lstat(abs); statErr == nil {
		_ = info
		return "", fmt.Errorf("rollback directory must not already exist")
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(parent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("rollback parent must be a directory")
	}
	return filepath.Clean(filepath.Join(parent, filepath.Base(abs))), nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

func validateBuildManifestForApply(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Mode != ModeBuild || manifest.Status != StatusBuilt {
		return fmt.Errorf("build manifest must be schema v3, mode build, and status built")
	}
	if manifest.UnresolvedJobCount != 0 || manifest.UnresolvedIdleChatRunCount != 0 {
		return fail("build_manifest_unresolved", "build manifest contains unresolved jobs or idle chat runs")
	}
	for name, value := range map[string]string{
		"source_sha256":            manifest.SourceSHA256,
		"input_event_set_sha256":   manifest.InputEventSetSHA256,
		"output_event_set_sha256":  manifest.OutputEventSetSHA256,
		"non_trace_content_sha256": manifest.NonTraceContentSHA256,
	} {
		if err := validateSHA256(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	counts := []int{manifest.InputCount, manifest.RepairJobCount, manifest.RepairSegmentCount, manifest.RepairEventCount, manifest.VerifiedJobCount, manifest.RepairableJobCount, manifest.UnresolvedJobCount, manifest.RepairIdleChatRunCount, manifest.VerifiedIdleChatRunCount, manifest.RepairableIdleChatRunCount, manifest.UnresolvedIdleChatRunCount}
	for _, value := range counts {
		if value < 0 {
			return fmt.Errorf("manifest counts must be non-negative")
		}
	}
	if manifest.RepairJobCount != manifest.RepairableJobCount {
		return fmt.Errorf("repair_job_count must equal repairable_job_count")
	}
	if manifest.RepairIdleChatRunCount != manifest.RepairableIdleChatRunCount {
		return fmt.Errorf("repair_idlechat_run_count must equal repairable_idlechat_run_count")
	}
	evidenceTotal := 0
	for key, value := range manifest.RepairEvidenceCounts {
		if !knownRepairEvidence(key) {
			return fmt.Errorf("unknown repair evidence %q", key)
		}
		if value < 0 {
			return fmt.Errorf("repair evidence counts must be non-negative")
		}
		evidenceTotal += value
	}
	if evidenceTotal != manifest.RepairSegmentCount {
		return fmt.Errorf("repair segment count does not match evidence counts")
	}
	reasonTotal := 0
	for key, value := range manifest.UnresolvedReasonCounts {
		if !knownUnresolvedReason(key) {
			return fmt.Errorf("unknown unresolved reason %q", key)
		}
		if value < 0 {
			return fmt.Errorf("unresolved reason counts must be non-negative")
		}
		reasonTotal += value
	}
	if reasonTotal != manifest.UnresolvedJobCount+manifest.UnresolvedIdleChatRunCount {
		return fmt.Errorf("unresolved group counts do not match reason counts")
	}
	repairUnitCount := manifest.RepairJobCount + manifest.RepairIdleChatRunCount
	if manifest.RepairSegmentCount > manifest.RepairEventCount || manifest.RepairEventCount > manifest.InputCount || manifest.RepairSegmentCount < repairUnitCount || (repairUnitCount == 0 && (manifest.RepairSegmentCount != 0 || manifest.RepairEventCount != 0)) || (repairUnitCount > 0 && (manifest.RepairSegmentCount == 0 || manifest.RepairEventCount == 0)) {
		return fmt.Errorf("repair counts are internally inconsistent")
	}
	if manifest.ErrorCode != "" {
		return fmt.Errorf("built manifest cannot contain an error code")
	}
	return nil
}

func knownRepairEvidence(key string) bool {
	switch key {
	case repairEvidenceMessageReceivedRoot, repairEvidenceRunQueueClaimedRoot, repairEvidenceBackgroundFailure, repairEvidenceTTSSession, repairEvidenceIdleChatSessionTopicRoot, repairEvidenceIdleChatStoryTurnRoot, repairEvidenceIdleChatForecastFailureRoot:
		return true
	default:
		return false
	}
}

func knownUnresolvedReason(key string) bool {
	switch key {
	case unresolvedReasonMissingOwnerRoot, unresolvedReasonAmbiguousRoot, unresolvedReasonAmbiguousIdleChatSession, unresolvedReasonInvalidIdleChatTurnSequence:
		return true
	default:
		return false
	}
}

func validateSHA256(value string) error {
	if len(value) != 64 {
		return fmt.Errorf("must be a 64-character SHA256 hex string")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("must be a SHA256 hex string")
	}
	return nil
}

func copyRepairCountsToCutover(receipt *CutoverManifest, manifest Manifest) {
	receipt.InputCount = manifest.InputCount
	receipt.RepairJobCount = manifest.RepairJobCount
	receipt.RepairSegmentCount = manifest.RepairSegmentCount
	receipt.RepairEventCount = manifest.RepairEventCount
	receipt.VerifiedJobCount = manifest.VerifiedJobCount
	receipt.RepairableJobCount = manifest.RepairableJobCount
	receipt.UnresolvedJobCount = manifest.UnresolvedJobCount
	receipt.RepairIdleChatRunCount = manifest.RepairIdleChatRunCount
	receipt.VerifiedIdleChatRunCount = manifest.VerifiedIdleChatRunCount
	receipt.RepairableIdleChatRunCount = manifest.RepairableIdleChatRunCount
	receipt.UnresolvedIdleChatRunCount = manifest.UnresolvedIdleChatRunCount
	receipt.InputEventSetSHA256 = manifest.InputEventSetSHA256
	receipt.OutputEventSetSHA256 = manifest.OutputEventSetSHA256
	receipt.NonTraceContentSHA256 = manifest.NonTraceContentSHA256
}

func readCutoverSnapshot(ctx context.Context, path string) (cutoverSnapshot, error) {
	events, nonTrace, err := readSnapshot(ctx, path)
	if err != nil {
		return cutoverSnapshot{}, err
	}
	eventHash, err := eventSetHash(events)
	if err != nil {
		return cutoverSnapshot{}, err
	}
	fileHash, err := fileSHA256(path)
	if err != nil {
		return cutoverSnapshot{}, err
	}
	return cutoverSnapshot{path: path, fileSHA256: fileHash, events: events, eventHash: eventHash, nonTrace: nonTrace}, nil
}

func verifySourceAgainstBuild(source cutoverSnapshot, manifest Manifest) error {
	if source.fileSHA256 != manifest.SourceSHA256 || len(source.events) != manifest.InputCount || source.eventHash != manifest.InputEventSetSHA256 || source.nonTrace != manifest.NonTraceContentSHA256 {
		return fail("source_mismatch", "source snapshot does not match build manifest")
	}
	return nil
}

func verifyOutputAgainstBuild(source, output cutoverSnapshot, manifest Manifest) error {
	if len(output.events) != manifest.InputCount || output.eventHash != manifest.OutputEventSetSHA256 || output.nonTrace != manifest.NonTraceContentSHA256 {
		return fail("output_mismatch", "rebuilt output does not match build manifest")
	}
	if err := verifyOnlyTraceChanged(source.events, output.events); err != nil {
		return fail("output_mismatch", "%v", err)
	}
	return nil
}

func verifyActiveAgainstSource(active, source cutoverSnapshot) error {
	if len(active.events) != len(source.events) || active.eventHash != source.eventHash || active.nonTrace != source.nonTrace {
		return fail("active_mismatch", "active store does not match source logical identity")
	}
	return nil
}

func verifyActiveAgainstOutput(active, output cutoverSnapshot, manifest Manifest) error {
	if len(active.events) != manifest.InputCount || active.eventHash != manifest.OutputEventSetSHA256 || active.nonTrace != manifest.NonTraceContentSHA256 {
		return fmt.Errorf("active store does not match rebuilt output logical identity")
	}
	if err := verifyOnlyTraceChanged(output.events, active.events); err != nil {
		return err
	}
	return nil
}

func verifyCutoverPreSwap(ctx context.Context, paths *applyPaths, options ApplyOptions, manifest Manifest) error {
	if err := rejectActiveSidecars(paths.active); err != nil {
		return fail("active_sidecar", "%v", err)
	}
	latestSource, err := readCutoverSnapshot(ctx, paths.source)
	if err != nil {
		return err
	}
	if err := verifySourceAgainstBuild(latestSource, manifest); err != nil {
		return err
	}
	latestOutput, err := readCutoverSnapshot(ctx, paths.output)
	if err != nil {
		return err
	}
	if err := verifyOutputAgainstBuild(latestSource, latestOutput, manifest); err != nil {
		return err
	}
	latestActive, err := readCutoverSnapshot(ctx, paths.active)
	if err != nil {
		return err
	}
	if err := verifyActiveAgainstSource(latestActive, latestSource); err != nil {
		return err
	}
	buildManifestSHA, err := fileSHA256(paths.buildManifest)
	if err != nil {
		return err
	}
	if buildManifestSHA != options.ExpectedBuildManifestSHA256 {
		return fail("build_manifest_sha256", "build manifest changed before swap")
	}
	installedSHA, err := fileSHA256(paths.installedRuntime)
	if err != nil {
		return err
	}
	if installedSHA != options.ExpectedInstalledRuntimeSHA256 {
		return fail("installed_runtime_sha256", "installed runtime changed before swap")
	}
	stagedSHA, err := fileSHA256(paths.stagedRuntime)
	if err != nil {
		return err
	}
	if stagedSHA != options.ExpectedStagedRuntimeSHA256 {
		return fail("staged_runtime_sha256", "staged runtime changed before swap")
	}
	return nil
}

func rejectActiveSidecars(active string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(active + suffix); err == nil {
			return fmt.Errorf("active sidecar %q exists", suffix)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func fileMode(path string) (os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%q must be a regular non-symlink file", path)
	}
	return info.Mode().Perm(), nil
}

func stageFile(source, directory string, mode os.FileMode) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	stage, err := os.CreateTemp(directory, ".rencrow-cutover-")
	if err != nil {
		return "", err
	}
	path := stage.Name()
	remove := true
	defer func() {
		_ = stage.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := stage.Chmod(mode.Perm()); err != nil {
		return "", err
	}
	if _, err := io.Copy(stage, input); err != nil {
		return "", err
	}
	if err := stage.Sync(); err != nil {
		return "", err
	}
	if err := stage.Close(); err != nil {
		return "", err
	}
	if err := syncDirectory(directory); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func copyFileSync(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(target)
		}
	}()
	if err := output.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	remove = false
	return nil
}

func quickCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", readOnlySQLiteDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if strings.TrimSpace(strings.ToLower(result)) != "ok" {
		return fmt.Errorf("quick_check=%s", result)
	}
	return nil
}

func rollbackCutover(ctx context.Context, receipt CutoverManifest, paths applyPaths, rollbackDB, rollbackRuntime string, activeMode, installedMode os.FileMode, causeCode string, cause error) (CutoverManifest, error) {
	dbErr := restoreFile(rollbackDB, paths.active, activeMode)
	runtimeErr := restoreFile(rollbackRuntime, paths.installedRuntime, installedMode)
	if dbErr == nil {
		dbErr = verifyFileSHA(rollbackDB, paths.active)
	}
	if runtimeErr == nil {
		runtimeErr = verifyFileSHA(rollbackRuntime, paths.installedRuntime)
	}
	if dbErr == nil {
		dbErr = quickCheck(ctx, paths.active)
	}
	if dbErr != nil || runtimeErr != nil {
		receipt.Status = CutoverStatusRollbackFailed
		receipt.ErrorCode = "rollback_failed"
		if writeErr := cutoverReceiptWriter(paths.manifest, receipt); writeErr != nil {
			return receipt, fmt.Errorf("%w; rollback failed: %v; receipt: %v", cause, errors.Join(dbErr, runtimeErr), writeErr)
		}
		return receipt, fmt.Errorf("%w; rollback failed: %v", cause, errors.Join(dbErr, runtimeErr))
	}
	receipt.Status = CutoverStatusRolledBack
	receipt.ErrorCode = causeCode
	if err := cutoverReceiptWriter(paths.manifest, receipt); err != nil {
		receipt.Status = CutoverStatusRollbackFailed
		receipt.ErrorCode = "rollback_receipt_write"
		return receipt, fmt.Errorf("%w; rollback receipt: %v", cause, err)
	}
	return receipt, cause
}

func restoreFile(backup, target string, mode os.FileMode) error {
	stage, err := stageFile(backup, filepath.Dir(target), mode)
	if err != nil {
		return err
	}
	defer os.Remove(stage)
	return replaceTargetFile(stage, target)
}

func verifyFileSHA(expected, actual string) error {
	expectedSHA, err := fileSHA256(expected)
	if err != nil {
		return err
	}
	actualSHA, err := fileSHA256(actual)
	if err != nil {
		return err
	}
	if expectedSHA != actualSHA {
		return fmt.Errorf("restored file hash=%s want=%s", actualSHA, expectedSHA)
	}
	return nil
}

func writeCutoverReceipt(path string, manifest CutoverManifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".rencrow-cutover-receipt-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	remove := true
	defer func() {
		_ = temp.Close()
		if remove {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0600); err != nil {
		return err
	}
	if _, err := temp.Write(append(encoded, '\n')); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := atomicReplaceFile(tempPath, path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	remove = false
	return nil
}

func cutoverFailure(receipt CutoverManifest, paths *applyPaths, code string, err error) (CutoverManifest, error) {
	receipt.Status = CutoverStatusBlocked
	receipt.ErrorCode = code
	if paths != nil && paths.manifest != "" {
		if receiptErr := cutoverReceiptWriter(paths.manifest, receipt); receiptErr != nil {
			return receipt, errors.Join(err, fmt.Errorf("write cutover receipt: %w", receiptErr))
		}
	}
	return receipt, err
}
