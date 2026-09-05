// Package eventtaskmigration owns the standalone one-shot Step09 rebuild of a
// legacy canonical Event Store and execution-report evidence. It only reads
// caller-provided writer-stopped snapshots and never swaps, mutates, or deletes
// a source or live artifact.
package eventtaskmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	ManifestSchemaVersion = "rencrow.identity.event-task-migration/v2"
	ModeDryRun            = "dry-run"
	ModeApply             = "apply"
	StatusReady           = "ready"
	StatusApplied         = "applied"
	StatusBlocked         = "blocked"
)

type Options struct {
	Mode                   string
	SnapshotDir            string
	SourceEventStore       string
	SourceConversationL1   string
	SourceExecutionReports string
	SourceResilienceRoot   string
	TargetEventStore       string
	TargetExecutionReports string
	TargetResilienceRoot   string
	Manifest               string
	DryRunManifest         string
}

// Manifest is deliberately metadata-only. It binds a plan to both immutable
// input snapshots and the exact canonical output without disclosing paths or
// event/receipt data.
type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code,omitempty"`

	TotalEvents        int `json:"total_events"`
	OrchestratorEvents int `json:"orchestrator_events"`
	MappedByReceipt    int `json:"mapped_by_receipt"`
	MappedDerived      int `json:"mapped_derived"`
	NoTaskEvents       int `json:"no_task_events"`
	Dependencies       int `json:"dependencies"`

	SourceEventStoreSHA256          string `json:"source_event_store_sha256,omitempty"`
	SourceConversationL1SHA256      string `json:"source_conversation_l1_sha256,omitempty"`
	CanonicalOutputSetSHA256        string `json:"canonical_output_set_sha256,omitempty"`
	SourceExecutionReportsSHA256    string `json:"source_execution_reports_sha256,omitempty"`
	CanonicalExecutionReportsSHA256 string `json:"canonical_execution_reports_sha256,omitempty"`
	ExecutionReportRows             int    `json:"execution_report_rows"`
	MappedReportByEvent             int    `json:"mapped_report_by_event"`
	MappedReportDerived             int    `json:"mapped_report_derived"`
	SourceResilienceSHA256          string `json:"source_resilience_sha256,omitempty"`
	CanonicalResilienceSHA256       string `json:"canonical_resilience_sha256,omitempty"`
	ResilienceFiles                 int    `json:"resilience_files"`
	ResilienceIncidents             int    `json:"resilience_incidents"`
	MappedRepairByReport            int    `json:"mapped_repair_by_report"`
}

type prepared struct {
	events     []modulecore.EventEnvelope
	reports    []migratedExecutionReport
	resilience migratedResilience
	manifest   Manifest
}

type codedError struct {
	code string
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func coded(code, format string, args ...any) error {
	return &codedError{code: code, err: fmt.Errorf(format, args...)}
}

// Run deterministically plans or applies the migration. A blocked receipt is
// written when the manifest path itself is safe and writable.
func Run(ctx context.Context, options Options) (Manifest, error) {
	options = trimOptions(options)
	base := Manifest{SchemaVersion: ManifestSchemaVersion, Mode: options.Mode, Status: StatusBlocked}
	if ctx == nil {
		return blocked(base, "invalid_options", errors.New("context is required"), "")
	}
	if err := ctx.Err(); err != nil {
		return blocked(base, "canceled", err, "")
	}
	paths, err := validateAndResolveOptions(options)
	if err != nil {
		// The manifest path is not trusted until all alias/symlink checks pass.
		// The caller still receives the machine-readable blocked receipt value.
		return blocked(base, errorCode(err, "invalid_options"), err, "")
	}

	plan, err := prepare(ctx, paths)
	if err != nil {
		return blocked(base, errorCode(err, "source_invalid"), err, paths.manifest)
	}
	manifest := plan.manifest
	manifest.Mode = options.Mode

	if options.Mode == ModeDryRun {
		manifest.Status = StatusReady
	} else {
		prior, err := readManifestStrict(paths.dryRunManifest)
		if err != nil {
			return blocked(manifest, "dry_run_manifest_invalid", err, paths.manifest)
		}
		if err := comparePlan(prior, manifest); err != nil {
			return blocked(manifest, "manifest_mismatch", err, paths.manifest)
		}
		if err := requireAbsentTarget(paths.targetEventStore); err != nil {
			return blocked(manifest, errorCode(err, "target_store"), err, paths.manifest)
		}
		if err := requireAbsentTarget(paths.targetExecutionReports); err != nil {
			return blocked(manifest, "report_target_exists", err, paths.manifest)
		}
		if err := requireAbsentDirectoryTarget(paths.targetResilienceRoot); err != nil {
			return blocked(manifest, errorCode(err, "resilience_target_exists"), err, paths.manifest)
		}
		if err := applyAllFresh(ctx, paths, plan); err != nil {
			return blocked(manifest, errorCode(err, "target_apply"), err, paths.manifest)
		}
		manifest.Status = StatusApplied
	}
	if err := writeManifest(paths.manifest, manifest); err != nil {
		if options.Mode == ModeApply {
			if cleanupErr := cleanupAppliedTargets(paths, true, true, true); cleanupErr != nil {
				return manifest, fmt.Errorf("write migration manifest: %w; clean applied targets: %v", err, cleanupErr)
			}
		}
		return manifest, fmt.Errorf("write migration manifest: %w", err)
	}
	return manifest, nil
}

type resolvedPaths struct {
	snapshotDir, sourceEventStore, sourceConversationL1, sourceExecutionReports string
	sourceResilienceRoot, targetEventStore, targetExecutionReports              string
	targetResilienceRoot, manifest, dryRunManifest                              string
}

func trimOptions(o Options) Options {
	o.Mode = strings.TrimSpace(o.Mode)
	o.SnapshotDir = strings.TrimSpace(o.SnapshotDir)
	o.SourceEventStore = strings.TrimSpace(o.SourceEventStore)
	o.SourceConversationL1 = strings.TrimSpace(o.SourceConversationL1)
	o.SourceExecutionReports = strings.TrimSpace(o.SourceExecutionReports)
	o.SourceResilienceRoot = strings.TrimSpace(o.SourceResilienceRoot)
	o.TargetEventStore = strings.TrimSpace(o.TargetEventStore)
	o.TargetExecutionReports = strings.TrimSpace(o.TargetExecutionReports)
	o.TargetResilienceRoot = strings.TrimSpace(o.TargetResilienceRoot)
	o.Manifest = strings.TrimSpace(o.Manifest)
	o.DryRunManifest = strings.TrimSpace(o.DryRunManifest)
	return o
}

func validateAndResolveOptions(o Options) (resolvedPaths, error) {
	if o.Mode != ModeDryRun && o.Mode != ModeApply {
		return resolvedPaths{}, coded("invalid_options", "--mode must be %q or %q", ModeDryRun, ModeApply)
	}
	for flag, value := range map[string]string{
		"--snapshot-dir": o.SnapshotDir, "--source-event-store": o.SourceEventStore,
		"--source-conversation-l1": o.SourceConversationL1, "--target-event-store": o.TargetEventStore,
		"--source-execution-reports": o.SourceExecutionReports, "--target-execution-reports": o.TargetExecutionReports,
		"--source-resilience-root": o.SourceResilienceRoot, "--target-resilience-root": o.TargetResilienceRoot,
		"--manifest": o.Manifest,
	} {
		if value == "" {
			return resolvedPaths{}, coded("invalid_options", "%s is required", flag)
		}
	}
	if o.Mode == ModeApply && o.DryRunManifest == "" {
		return resolvedPaths{}, coded("invalid_options", "--dry-run-manifest is required in apply mode")
	}
	if o.Mode == ModeDryRun && o.DryRunManifest != "" {
		return resolvedPaths{}, coded("invalid_options", "--dry-run-manifest is apply-only")
	}

	snapshot, err := absolute(o.SnapshotDir)
	if err != nil {
		return resolvedPaths{}, coded("invalid_path", "resolve snapshot directory: %v", err)
	}
	info, err := os.Stat(snapshot)
	if err != nil || !info.IsDir() {
		return resolvedPaths{}, coded("invalid_path", "snapshot directory is missing or not a directory")
	}
	realSnapshot, err := filepath.EvalSymlinks(snapshot)
	if err != nil {
		return resolvedPaths{}, coded("invalid_path", "resolve snapshot directory: %v", err)
	}

	eventSource, eventInfo, err := resolveSnapshotFile(realSnapshot, o.SourceEventStore)
	if err != nil {
		return resolvedPaths{}, coded("invalid_source", "event store snapshot: %v", err)
	}
	l1Source, l1Info, err := resolveSnapshotFile(realSnapshot, o.SourceConversationL1)
	if err != nil {
		return resolvedPaths{}, coded("invalid_source", "conversation L1 snapshot: %v", err)
	}
	if os.SameFile(eventInfo, l1Info) {
		return resolvedPaths{}, coded("invalid_source", "source snapshots must be distinct files")
	}
	reportSource, reportInfo, err := resolveSnapshotFile(realSnapshot, o.SourceExecutionReports)
	if err != nil {
		return resolvedPaths{}, coded("invalid_source", "execution report snapshot: %v", err)
	}
	if os.SameFile(eventInfo, reportInfo) || os.SameFile(l1Info, reportInfo) {
		return resolvedPaths{}, coded("invalid_source", "all source snapshots must be distinct files")
	}
	resilienceSource, err := resolveSnapshotDirectory(realSnapshot, o.SourceResilienceRoot)
	if err != nil {
		return resolvedPaths{}, coded("invalid_source", "resilience snapshot: %v", err)
	}
	for _, sourceInfo := range []os.FileInfo{eventInfo, l1Info, reportInfo} {
		aliased, err := resilienceTreeAliasesFile(resilienceSource, sourceInfo)
		if err != nil {
			return resolvedPaths{}, coded("invalid_source", "inspect resilience snapshot aliases: %v", err)
		}
		if aliased {
			return resolvedPaths{}, coded("path_alias", "source resilience tree must not hard-link another source snapshot")
		}
	}
	target, err := absolute(o.TargetEventStore)
	if err != nil {
		return resolvedPaths{}, coded("invalid_path", "resolve target event store: %v", err)
	}
	manifest, err := absolute(o.Manifest)
	if err != nil {
		return resolvedPaths{}, coded("invalid_path", "resolve manifest: %v", err)
	}
	targetReports, err := absolute(o.TargetExecutionReports)
	if err != nil {
		return resolvedPaths{}, coded("invalid_path", "resolve target execution reports: %v", err)
	}
	targetResilience, err := absolute(o.TargetResilienceRoot)
	if err != nil {
		return resolvedPaths{}, coded("invalid_path", "resolve target resilience root: %v", err)
	}
	dry := ""
	if o.DryRunManifest != "" {
		dry, err = absolute(o.DryRunManifest)
		if err != nil {
			return resolvedPaths{}, coded("invalid_path", "resolve dry-run manifest: %v", err)
		}
	}
	for label, path := range map[string]string{"target": target, "target execution reports": targetReports, "target resilience root": targetResilience, "manifest": manifest, "dry-run manifest": dry} {
		if path == "" {
			continue
		}
		if samePath(path, eventSource) || samePath(path, l1Source) || samePath(path, reportSource) {
			return resolvedPaths{}, coded("path_alias", "%s must not alias a source snapshot", label)
		}
		if aliasesFile(path, eventInfo) || aliasesFile(path, l1Info) || aliasesFile(path, reportInfo) {
			return resolvedPaths{}, coded("path_alias", "%s must not hard-link a source snapshot", label)
		}
		if sqliteFamilyConflict(eventSource, path) || sqliteFamilyConflict(l1Source, path) {
			return resolvedPaths{}, coded("path_alias", "%s must not use a source snapshot sidecar path", label)
		}
		if pathsOverlap(resilienceSource, path) {
			return resolvedPaths{}, coded("path_alias", "%s must not overlap the source resilience root", label)
		}
		info, err := existingPathInfo(path)
		if err != nil {
			return resolvedPaths{}, coded("invalid_path", "inspect %s: %v", label, err)
		}
		if info != nil && info.Mode()&os.ModeSymlink != 0 {
			return resolvedPaths{}, coded("path_alias", "%s must not be a symlink", label)
		}
		if info != nil && info.Mode().IsRegular() {
			aliased, err := resilienceTreeAliasesFile(resilienceSource, info)
			if err != nil {
				return resolvedPaths{}, coded("invalid_source", "inspect resilience snapshot aliases: %v", err)
			}
			if aliased {
				return resolvedPaths{}, coded("path_alias", "%s must not hard-link a resilience source file", label)
			}
		}
	}
	for _, sourceFile := range []string{eventSource, l1Source, reportSource} {
		if pathsOverlap(resilienceSource, sourceFile) {
			return resolvedPaths{}, coded("path_alias", "source resilience root must not contain another source snapshot")
		}
	}
	if sqliteFamilyConflict(target, targetReports) || sqliteFamilyConflict(target, manifest) || samePath(targetReports, manifest) || pathsOverlap(targetResilience, target) || pathsOverlap(targetResilience, targetReports) || pathsOverlap(targetResilience, manifest) || dry != "" && (sqliteFamilyConflict(target, dry) || samePath(targetReports, dry) || samePath(manifest, dry) || pathsOverlap(targetResilience, dry)) {
		return resolvedPaths{}, coded("path_alias", "target, manifest, and dry-run manifest paths must be distinct")
	}
	outputs := []string{target, targetReports, targetResilience, manifest}
	if dry != "" {
		outputs = append(outputs, dry)
	}
	for i := range outputs {
		left, err := existingPathInfo(outputs[i])
		if err != nil || left == nil {
			continue
		}
		for j := i + 1; j < len(outputs); j++ {
			right, err := existingPathInfo(outputs[j])
			if err == nil && right != nil && os.SameFile(left, right) {
				return resolvedPaths{}, coded("path_alias", "output and manifest paths must not alias")
			}
		}
	}
	if err := validateManifestDestination(manifest); err != nil {
		return resolvedPaths{}, coded("unsafe_manifest", "%v", err)
	}
	return resolvedPaths{realSnapshot, eventSource, l1Source, reportSource, resilienceSource, target, targetReports, targetResilience, manifest, dry}, nil
}

func resolveSnapshotDirectory(snapshotDir, path string) (string, error) {
	abs, err := absolute(path)
	if err != nil {
		return "", err
	}
	if !pathWithin(snapshotDir, abs) {
		return "", errors.New("source directory is outside snapshot directory")
	}
	info, err := os.Lstat(abs)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("source must be a non-symlink directory")
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil || !samePath(real, abs) || !pathWithin(snapshotDir, real) {
		return "", errors.New("source directory path contains a symlink or escapes snapshot")
	}
	return real, nil
}

func absolute(path string) (string, error) {
	return filepath.Abs(filepath.Clean(path))
}

func resolveSnapshotFile(snapshotDir, path string) (string, os.FileInfo, error) {
	abs, err := absolute(path)
	if err != nil {
		return "", nil, err
	}
	rel, err := filepath.Rel(snapshotDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", nil, errors.New("source is outside snapshot directory")
	}
	linfo, err := os.Lstat(abs)
	if err != nil {
		return "", nil, errors.New("source is missing")
	}
	if linfo.Mode()&os.ModeSymlink != 0 || !linfo.Mode().IsRegular() {
		return "", nil, errors.New("source must be a regular non-symlink file")
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil || !samePath(real, abs) {
		return "", nil, errors.New("source path contains a symlink")
	}
	rel, err = filepath.Rel(snapshotDir, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", nil, errors.New("resolved source is outside snapshot directory")
	}
	return real, linfo, nil
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, _ := absolute(a)
	bb, _ := absolute(b)
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func aliasesFile(path string, source os.FileInfo) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink == 0 && os.SameFile(info, source)
}

func sqliteFamilyConflict(databasePath, other string) bool {
	if databasePath == "" || other == "" {
		return false
	}
	if samePath(databasePath, other) {
		return true
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if samePath(databasePath+suffix, other) || samePath(other+suffix, databasePath) {
			return true
		}
	}
	return false
}

func canonicalOutputSHA(events []modulecore.EventEnvelope) (string, error) {
	canonical := append([]modulecore.EventEnvelope(nil), events...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].EventID < canonical[j].EventID })
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func comparePlan(prior, current Manifest) error {
	if prior.SchemaVersion != ManifestSchemaVersion || prior.Mode != ModeDryRun || prior.Status != StatusReady || prior.ErrorCode != "" {
		return errors.New("dry-run manifest is not a ready Step09 receipt")
	}
	want := prior
	want.Mode, want.Status = current.Mode, current.Status
	return comparePlanFields(want, current)
}

func comparePlanFields(a, b Manifest) error {
	a.Mode, b.Mode = "", ""
	a.Status, b.Status = "", ""
	a.ErrorCode, b.ErrorCode = "", ""
	if a != b {
		return errors.New("dry-run receipt does not match current snapshot plan")
	}
	return nil
}

func blocked(manifest Manifest, code string, cause error, safeManifestPath string) (Manifest, error) {
	manifest.SchemaVersion = ManifestSchemaVersion
	manifest.Status = StatusBlocked
	manifest.ErrorCode = code
	if safeManifestPath != "" {
		if err := writeManifest(safeManifestPath, manifest); err != nil {
			return manifest, fmt.Errorf("%w; write blocked manifest: %v", cause, err)
		}
	}
	return manifest, cause
}

func errorCode(err error, fallback string) string {
	var target *codedError
	if errors.As(err, &target) && target.code != "" {
		return target.code
	}
	return fallback
}
