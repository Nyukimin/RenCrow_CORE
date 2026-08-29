// Package eventmigration owns the one-shot Step 02 migration from the legacy
// AI Workflow and SuperAgent event snapshots into the canonical Event Store.
//
// The package deliberately has no live-runtime integration. It consumes a
// caller-provided, read-only snapshot, produces a checksum-bound receipt, and
// only opens the canonical store during an explicitly requested apply.
package eventmigration

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

	identitymigration "github.com/Nyukimin/RenCrow_CORE/internal/application/identitymigration"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	ManifestSchemaVersion = "rencrow.identity.event-migration/v1"

	ModeDryRun = "dry-run"
	ModeApply  = "apply"

	StatusReady   = "ready"
	StatusApplied = "applied"
	StatusNoop    = "noop"
	StatusBlocked = "blocked"
)

// SourceOptions identifies at most one representation of each legacy source.
// Paths are validated against Options.SnapshotDir by Run.
type SourceOptions struct {
	AISQLite         string
	AIJSONL          string
	SuperagentSQLite string
	SuperagentJSONL  string
}

// Options is the complete bounded command contract for one migration run.
type Options struct {
	SnapshotDir string

	AISQLite         string
	AIJSONL          string
	SuperagentSQLite string
	SuperagentJSONL  string

	EventStore     string
	Manifest       string
	DryRunManifest string
	Mode           string
}

// SourceManifest contains only non-sensitive source identity and integrity
// metadata. It intentionally does not contain a source path or source data.
type SourceManifest struct {
	Format     string `json:"format"`
	InputCount int    `json:"input_count"`
	SHA256     string `json:"sha256"`
}

// Manifest is the machine-readable migration receipt. Canonical event data is
// represented by its deterministic set hash; event payloads are never written
// to the receipt.
type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`

	InputCount               int    `json:"input_count"`
	ConvertedCount           int    `json:"converted_count"`
	DroppedRunAsParentCount  int    `json:"dropped_run_as_parent_count"`
	DroppedRunAsParentReason string `json:"dropped_run_as_parent_reason,omitempty"`

	Sources                 map[string]SourceManifest `json:"sources"`
	CanonicalEventSetSHA256 string                    `json:"canonical_event_set_sha256"`
	ErrorCode               string                    `json:"error_code,omitempty"`
}

type sourceDescriptor struct {
	name        string
	format      string
	table       string
	componentID string
	path        string
}

type preparedMigration struct {
	events   []modulecore.EventEnvelope
	manifest Manifest
}

type codedError struct {
	code string
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }

func (e *codedError) Unwrap() error { return e.err }

func newCodedError(code, format string, args ...any) error {
	return &codedError{code: code, err: fmt.Errorf(format, args...)}
}

// Run executes one snapshot migration. Dry-run validates and hashes only the
// snapshot and writes a ready receipt; it never opens or creates EventStore.
// Apply requires a checksum-matching dry-run receipt before it opens the
// target and uses one atomic AppendBatch call for a missing event set.
func Run(ctx context.Context, options Options) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if err := validateOptions(options); err != nil {
		return Manifest{}, err
	}

	snapshotDir, sources, err := resolveSources(options)
	if err != nil {
		return failureReceipt(options, "invalid_source", err)
	}
	_ = snapshotDir // resolveSources also validates containment for every source.

	prepared, err := prepare(ctx, sources)
	if err != nil {
		return failureReceipt(options, errorCode(err, "source_read"), err)
	}
	manifest := prepared.manifest

	if options.Mode == ModeApply {
		prior, readErr := readManifestStrict(options.DryRunManifest)
		if readErr != nil {
			return failureReceiptWithManifest(options, manifest, "dry_run_manifest_invalid", readErr)
		}
		if compareErr := compareDryRunManifest(prior, manifest); compareErr != nil {
			return failureReceiptWithManifest(options, manifest, "manifest_mismatch", compareErr)
		}
		action, applyErr := apply(ctx, options.EventStore, prepared.events)
		if applyErr != nil {
			return failureReceiptWithManifest(options, manifest, errorCode(applyErr, "target_store"), applyErr)
		}
		manifest.Mode = options.Mode
		manifest.Status = action
	} else {
		manifest.Status = StatusReady
	}

	if err := writeManifest(options.Manifest, manifest); err != nil {
		return manifest, fmt.Errorf("write migration manifest: %w", err)
	}
	return manifest, nil
}

func validateOptions(options Options) error {
	if strings.TrimSpace(options.SnapshotDir) == "" {
		return fmt.Errorf("--snapshot-dir is required")
	}
	if strings.TrimSpace(options.EventStore) == "" {
		return fmt.Errorf("--event-store is required")
	}
	if strings.TrimSpace(options.Manifest) == "" {
		return fmt.Errorf("--manifest is required")
	}
	if options.Mode != ModeDryRun && options.Mode != ModeApply {
		return fmt.Errorf("--mode must be %q or %q", ModeDryRun, ModeApply)
	}
	if options.Mode == ModeApply && strings.TrimSpace(options.DryRunManifest) == "" {
		return fmt.Errorf("--dry-run-manifest is required in apply mode")
	}
	if options.Mode == ModeDryRun && strings.TrimSpace(options.DryRunManifest) != "" {
		return fmt.Errorf("--dry-run-manifest is only valid in apply mode")
	}
	if nonEmptyCount(options.AISQLite, options.AIJSONL) > 1 {
		return fmt.Errorf("exactly zero or one of --ai-sqlite and --ai-jsonl may be set")
	}
	if nonEmptyCount(options.SuperagentSQLite, options.SuperagentJSONL) > 1 {
		return fmt.Errorf("exactly zero or one of --superagent-sqlite and --superagent-jsonl may be set")
	}
	if nonEmptyCount(options.AISQLite, options.AIJSONL, options.SuperagentSQLite, options.SuperagentJSONL) == 0 {
		return fmt.Errorf("at least one legacy event source is required")
	}
	return nil
}

func nonEmptyCount(values ...string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func resolveSources(options Options) (string, []sourceDescriptor, error) {
	snapshotDir, err := resolveDirectory(options.SnapshotDir)
	if err != nil {
		return "", nil, err
	}
	manifestPath, err := absolutePath(options.Manifest)
	if err != nil {
		return "", nil, fmt.Errorf("resolve manifest path: %w", err)
	}

	var sources []sourceDescriptor
	if strings.TrimSpace(options.AISQLite) != "" {
		sources = append(sources, sourceDescriptor{
			name: "ai_workflow", format: "sqlite", table: "ai_workflow_event", componentID: "ai_workflow",
			path: options.AISQLite,
		})
	} else if strings.TrimSpace(options.AIJSONL) != "" {
		sources = append(sources, sourceDescriptor{
			name: "ai_workflow", format: "jsonl", table: "ai_workflow_event", componentID: "ai_workflow",
			path: options.AIJSONL,
		})
	}
	if strings.TrimSpace(options.SuperagentSQLite) != "" {
		sources = append(sources, sourceDescriptor{
			name: "superagent", format: "sqlite", table: "trace_event", componentID: "superagent",
			path: options.SuperagentSQLite,
		})
	} else if strings.TrimSpace(options.SuperagentJSONL) != "" {
		sources = append(sources, sourceDescriptor{
			name: "superagent", format: "jsonl", table: "trace_event", componentID: "superagent",
			path: options.SuperagentJSONL,
		})
	}

	for index := range sources {
		resolved, resolveErr := resolveSnapshotSource(snapshotDir, sources[index].path)
		if resolveErr != nil {
			return "", nil, resolveErr
		}
		if samePath(resolved, manifestPath) {
			return "", nil, fmt.Errorf("manifest path must not replace a source snapshot")
		}
		sources[index].path = resolved
	}
	return snapshotDir, sources, nil
}

func resolveDirectory(path string) (string, error) {
	abs, err := absolutePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("snapshot directory is missing or not a directory")
	}
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot directory: %w", err)
	}
	return filepath.Clean(realPath), nil
}

func resolveSnapshotSource(snapshotDir, path string) (string, error) {
	abs, err := absolutePath(path)
	if err != nil {
		return "", fmt.Errorf("resolve source path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("source snapshot is missing or not a regular file")
	}
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve source snapshot: %w", err)
	}
	realPath = filepath.Clean(realPath)
	if !pathWithin(snapshotDir, realPath) {
		return "", fmt.Errorf("source snapshot must be inside --snapshot-dir")
	}
	return realPath, nil
}

func absolutePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("path is invalid")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return false
	}
	parent := ".." + string(filepath.Separator)
	return relative != ".." && !strings.HasPrefix(relative, parent)
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	return strings.EqualFold(left, right) && filepath.VolumeName(left) != ""
}

func prepare(ctx context.Context, sources []sourceDescriptor) (preparedMigration, error) {
	result := preparedMigration{manifest: Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Mode:          ModeDryRun,
		Sources:       make(map[string]SourceManifest, len(sources)),
	}}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return preparedMigration{}, err
		}
		legacy, digest, err := loadSource(ctx, source)
		if err != nil {
			return preparedMigration{}, fmt.Errorf("load %s source: %w", source.name, err)
		}
		converted, err := identitymigration.ConvertLegacyEvents(source.componentID, legacy)
		if err != nil {
			return preparedMigration{}, fmt.Errorf("convert %s events: %w", source.name, err)
		}
		result.events = append(result.events, converted.Events...)
		result.manifest.InputCount += converted.Manifest.InputCount
		result.manifest.ConvertedCount += converted.Manifest.ConvertedCount
		result.manifest.DroppedRunAsParentCount += converted.Manifest.DroppedRunAsParentCount
		if converted.Manifest.DroppedRunAsParentReason != "" {
			if result.manifest.DroppedRunAsParentReason != "" && result.manifest.DroppedRunAsParentReason != converted.Manifest.DroppedRunAsParentReason {
				return preparedMigration{}, fmt.Errorf("conflicting dropped parent reference reasons")
			}
			result.manifest.DroppedRunAsParentReason = converted.Manifest.DroppedRunAsParentReason
		}
		result.manifest.Sources[source.name] = SourceManifest{
			Format: source.format, InputCount: converted.Manifest.InputCount, SHA256: digest,
		}
	}
	if err := modulecore.ValidateEventEnvelopeGraph(result.events); err != nil {
		return preparedMigration{}, fmt.Errorf("combined converted event graph: %w", err)
	}
	digest, err := canonicalEventSetSHA256(result.events)
	if err != nil {
		return preparedMigration{}, fmt.Errorf("hash canonical event set: %w", err)
	}
	result.manifest.CanonicalEventSetSHA256 = digest
	return result, nil
}

func failureReceipt(options Options, code string, cause error) (Manifest, error) {
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, Mode: options.Mode, Status: StatusBlocked, ErrorCode: code}
	return failureReceiptWithManifest(options, manifest, code, cause)
}

func failureReceiptWithManifest(options Options, manifest Manifest, code string, cause error) (Manifest, error) {
	manifest.SchemaVersion = ManifestSchemaVersion
	manifest.Mode = options.Mode
	manifest.Status = StatusBlocked
	manifest.ErrorCode = code
	if strings.TrimSpace(options.Manifest) != "" {
		if writeErr := writeManifest(options.Manifest, manifest); writeErr != nil {
			return manifest, fmt.Errorf("%w; write migration manifest: %v", cause, writeErr)
		}
	}
	return manifest, cause
}

func errorCode(err error, fallback string) string {
	var coded *codedError
	if errors.As(err, &coded) && coded.code != "" {
		return coded.code
	}
	return fallback
}

func canonicalEventSetSHA256(events []modulecore.EventEnvelope) (string, error) {
	sortedEvents := append([]modulecore.EventEnvelope(nil), events...)
	sort.Slice(sortedEvents, func(left, right int) bool {
		return sortedEvents[left].EventID < sortedEvents[right].EventID
	})
	encoded, err := json.Marshal(sortedEvents)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func compareDryRunManifest(prior, current Manifest) error {
	if prior.SchemaVersion != ManifestSchemaVersion {
		return newCodedError("manifest_mismatch", "dry-run manifest schema version is unsupported")
	}
	if prior.Mode != ModeDryRun || prior.Status != StatusReady {
		return newCodedError("manifest_mismatch", "manifest is not a ready dry-run receipt")
	}
	if prior.InputCount != current.InputCount || prior.ConvertedCount != current.ConvertedCount || prior.DroppedRunAsParentCount != current.DroppedRunAsParentCount || prior.DroppedRunAsParentReason != current.DroppedRunAsParentReason {
		return newCodedError("manifest_mismatch", "dry-run counts do not match the current snapshot")
	}
	if prior.CanonicalEventSetSHA256 != current.CanonicalEventSetSHA256 {
		return newCodedError("manifest_mismatch", "canonical event set checksum does not match the dry-run receipt")
	}
	if !sameSourceManifests(prior.Sources, current.Sources) {
		return newCodedError("manifest_mismatch", "source checksums do not match the dry-run receipt")
	}
	return nil
}

func sameSourceManifests(left, right map[string]SourceManifest) bool {
	if len(left) != len(right) {
		return false
	}
	for name, expected := range left {
		actual, ok := right[name]
		if !ok || expected != actual {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Mode == "" || manifest.Status == "" {
		return fmt.Errorf("manifest header is invalid")
	}
	if manifest.InputCount < 0 || manifest.ConvertedCount < 0 || manifest.DroppedRunAsParentCount < 0 {
		return fmt.Errorf("manifest counts are invalid")
	}
	if (manifest.DroppedRunAsParentCount == 0) != (manifest.DroppedRunAsParentReason == "") {
		return fmt.Errorf("manifest dropped parent reference reason is inconsistent")
	}
	if len(manifest.Sources) == 0 {
		return fmt.Errorf("manifest sources are missing")
	}
	if !validSHA256(manifest.CanonicalEventSetSHA256) {
		return fmt.Errorf("manifest canonical event set checksum is invalid")
	}
	for name, source := range manifest.Sources {
		if strings.TrimSpace(name) == "" || (source.Format != "sqlite" && source.Format != "jsonl") || source.InputCount < 0 || !validSHA256(source.SHA256) {
			return fmt.Errorf("manifest source metadata is invalid")
		}
	}
	return nil
}
