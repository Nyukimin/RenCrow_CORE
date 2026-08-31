// Package eventtracerepair rebuilds a canonical Event Store snapshot after a
// known runtime defect split one trigger across multiple TraceIDs. It never
// opens the active production store for writing and never updates in place.
package eventtracerepair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

const (
	ManifestSchemaVersion     = "rencrow.identity.event-trace-repair/v3"
	ModeDryRun                = "dry-run"
	ModeBuild                 = "build"
	StatusReady               = "ready"
	StatusReadyWithUnresolved = "ready_with_unresolved"
	StatusBuilt               = "built"
	StatusBlocked             = "blocked"
)

type Options struct {
	SnapshotDir    string
	SourceStore    string
	OutputStore    string
	Manifest       string
	DryRunManifest string
	Mode           string
}

type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`

	SourceSHA256               string         `json:"source_sha256,omitempty"`
	InputCount                 int            `json:"input_count"`
	RepairJobCount             int            `json:"repair_job_count"`
	RepairSegmentCount         int            `json:"repair_segment_count"`
	RepairEventCount           int            `json:"repair_event_count"`
	VerifiedJobCount           int            `json:"verified_job_count"`
	RepairableJobCount         int            `json:"repairable_job_count"`
	UnresolvedJobCount         int            `json:"unresolved_job_count"`
	RepairIdleChatRunCount     int            `json:"repair_idlechat_run_count"`
	VerifiedIdleChatRunCount   int            `json:"verified_idlechat_run_count"`
	RepairableIdleChatRunCount int            `json:"repairable_idlechat_run_count"`
	UnresolvedIdleChatRunCount int            `json:"unresolved_idlechat_run_count"`
	RepairEvidenceCounts       map[string]int `json:"repair_evidence_counts"`
	UnresolvedReasonCounts     map[string]int `json:"unresolved_reason_counts"`
	InputEventSetSHA256        string         `json:"input_event_set_sha256,omitempty"`
	OutputEventSetSHA256       string         `json:"output_event_set_sha256,omitempty"`
	NonTraceContentSHA256      string         `json:"non_trace_content_sha256,omitempty"`
	ErrorCode                  string         `json:"error_code,omitempty"`
}

type codedError struct {
	code string
	err  error
}

var repairManifestWriter = writeManifest

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func fail(code, format string, args ...any) error {
	return &codedError{code: code, err: fmt.Errorf(format, args...)}
}

func Run(ctx context.Context, options Options) (Manifest, error) {
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, Mode: options.Mode, Status: StatusBlocked}
	if err := validateOptions(options); err != nil {
		manifest.ErrorCode = "invalid_options"
		return manifest, err
	}
	sourcePath, outputPath, manifestPath, dryRunManifestPath, err := resolvePaths(options)
	if err != nil {
		manifest.ErrorCode = "invalid_path"
		return manifest, err
	}
	options.Manifest = manifestPath
	options.DryRunManifest = dryRunManifestPath
	manifest.SourceSHA256, err = fileSHA256(sourcePath)
	if err != nil {
		return finishFailure(options, manifest, "source_read", err)
	}
	events, columnsHash, err := readSnapshot(ctx, sourcePath)
	if err != nil {
		return finishFailure(options, manifest, errorCode(err, "source_read"), err)
	}
	manifest.InputCount = len(events)
	manifest.InputEventSetSHA256, err = eventSetHash(events)
	if err != nil {
		return finishFailure(options, manifest, "input_hash", err)
	}
	manifest.NonTraceContentSHA256 = columnsHash

	result, err := classifyAndRepair(events)
	if err != nil {
		return finishFailure(options, manifest, errorCode(err, "repair_blocked"), err)
	}
	manifest.RepairJobCount = result.repairJobCount
	manifest.RepairSegmentCount = result.repairSegmentCount
	manifest.RepairEventCount = result.repairEventCount
	manifest.VerifiedJobCount = result.verifiedJobCount
	manifest.RepairableJobCount = result.repairableJobCount
	manifest.UnresolvedJobCount = result.unresolvedJobCount
	manifest.RepairIdleChatRunCount = result.repairIdleChatRunCount
	manifest.VerifiedIdleChatRunCount = result.verifiedIdleChatRunCount
	manifest.RepairableIdleChatRunCount = result.repairableIdleChatRunCount
	manifest.UnresolvedIdleChatRunCount = result.unresolvedIdleChatRunCount
	manifest.RepairEvidenceCounts = result.repairEvidenceCounts
	manifest.UnresolvedReasonCounts = result.unresolvedReasonCounts
	manifest.Status = statusForUnresolved(result.unresolvedJobCount, result.unresolvedIdleChatRunCount)
	repaired := result.events
	manifest.OutputEventSetSHA256, err = eventSetHash(repaired)
	if err != nil {
		return finishFailure(options, manifest, "output_hash", err)
	}
	if err := verifyOnlyTraceChanged(events, repaired); err != nil {
		return finishFailure(options, manifest, "content_drift", err)
	}

	if options.Mode == ModeDryRun {
		if err := repairManifestWriter(options.Manifest, manifest); err != nil {
			return blockedManifestWrite(manifest, err)
		}
		return manifest, nil
	}
	prior, err := readManifest(options.DryRunManifest)
	if err != nil {
		return finishFailure(options, manifest, "dry_run_manifest_invalid", err)
	}
	if err := compareManifest(prior, manifest); err != nil {
		return finishFailure(options, manifest, "manifest_mismatch", err)
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return finishFailure(options, manifest, "output_exists", fmt.Errorf("output store already exists"))
	} else if !os.IsNotExist(err) {
		return finishFailure(options, manifest, "output_stat", err)
	}
	if err := buildOutput(ctx, outputPath, repaired); err != nil {
		return finishFailure(options, manifest, "output_build", err)
	}
	outputEvents, outputNonTraceHash, err := readSnapshot(ctx, outputPath)
	if err != nil {
		return finishFailure(options, manifest, "output_verify", err)
	}
	outputHash, err := eventSetHash(outputEvents)
	if err != nil || len(outputEvents) != manifest.InputCount || outputHash != manifest.OutputEventSetSHA256 || outputNonTraceHash != manifest.NonTraceContentSHA256 {
		if err == nil {
			err = fmt.Errorf("rebuilt output does not match dry-run receipt")
		}
		return finishFailure(options, manifest, "output_mismatch", err)
	}
	manifest.Status = StatusBuilt
	if err := repairManifestWriter(options.Manifest, manifest); err != nil {
		return blockedManifestWrite(manifest, err)
	}
	return manifest, nil
}

func validateOptions(options Options) error {
	if strings.TrimSpace(options.SnapshotDir) == "" || strings.TrimSpace(options.SourceStore) == "" || strings.TrimSpace(options.OutputStore) == "" || strings.TrimSpace(options.Manifest) == "" {
		return fmt.Errorf("snapshot-dir, source-store, output-store, and manifest are required")
	}
	if options.Mode != ModeDryRun && options.Mode != ModeBuild {
		return fmt.Errorf("mode must be %q or %q", ModeDryRun, ModeBuild)
	}
	if options.Mode == ModeBuild && strings.TrimSpace(options.DryRunManifest) == "" {
		return fmt.Errorf("dry-run-manifest is required in build mode")
	}
	if options.Mode == ModeDryRun && strings.TrimSpace(options.DryRunManifest) != "" {
		return fmt.Errorf("dry-run-manifest is only valid in build mode")
	}
	return nil
}

func resolvePaths(options Options) (string, string, string, string, error) {
	root, err := filepath.Abs(options.SnapshotDir)
	if err != nil {
		return "", "", "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", "", fmt.Errorf("resolve snapshot directory: %w", err)
	}
	source, err := containedPath(root, options.SourceStore, true)
	if err != nil {
		return "", "", "", "", fmt.Errorf("source store: %w", err)
	}
	output, err := containedPath(root, options.OutputStore, false)
	if err != nil {
		return "", "", "", "", fmt.Errorf("output store: %w", err)
	}
	manifest, err := containedPath(root, options.Manifest, false)
	if err != nil {
		return "", "", "", "", fmt.Errorf("manifest: %w", err)
	}
	dryRunManifest := ""
	if options.DryRunManifest != "" {
		dryRunManifest, err = containedPath(root, options.DryRunManifest, true)
		if err != nil {
			return "", "", "", "", fmt.Errorf("dry-run manifest: %w", err)
		}
	}
	if source == output || source == manifest || output == manifest {
		return "", "", "", "", fmt.Errorf("source, output, and manifest paths must be distinct")
	}
	return source, output, manifest, dryRunManifest, nil
}

func containedPath(root, raw string, mustExist bool) (string, error) {
	path, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return "", fmt.Errorf("parent directory must exist: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return "", fmt.Errorf("parent must be a real directory")
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve parent directory: %w", err)
	}
	if filepath.Clean(resolvedParent) != parent {
		return "", fmt.Errorf("parent path must not contain symlinks")
	}
	rel, err := filepath.Rel(root, resolvedParent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path must stay inside snapshot directory")
	}
	path = filepath.Join(resolvedParent, filepath.Base(path))
	info, statErr := os.Lstat(path)
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("must be a regular non-symlink file")
		}
	} else if !os.IsNotExist(statErr) || mustExist {
		return "", statErr
	}
	return path, nil
}

func readSnapshot(ctx context.Context, path string) ([]modulecore.EventEnvelope, string, error) {
	db, err := sql.Open("sqlite", readOnlySQLiteDSN(path))
	if err != nil {
		return nil, "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `SELECT event_id,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope ORDER BY event_id`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	events := make([]modulecore.EventEnvelope, 0)
	for rows.Next() {
		var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON string
		if err := rows.Scan(&eventID, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &envelopeJSON); err != nil {
			return nil, "", err
		}
		var event modulecore.EventEnvelope
		if err := json.Unmarshal([]byte(envelopeJSON), &event); err != nil {
			return nil, "", fail("invalid_envelope", "decode event %q: %v", eventID, err)
		}
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return nil, "", fail("invalid_envelope", "event %q: %v", eventID, err)
		}
		if string(event.EventID) != eventID || string(event.TraceID) != traceID || event.SchemaVersion != schemaVersion || event.EventType != eventType || event.ComponentID != componentID || event.OccurredAt.Format(time.RFC3339Nano) != occurredAt {
			return nil, "", fail("column_mismatch", "event %q column and envelope mismatch", eventID)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		return nil, "", fail("invalid_graph", "%v", err)
	}
	hash, err := nonTraceHash(events)
	return events, hash, err
}

func readOnlySQLiteDSN(path string) string {
	query := url.Values{}
	query.Set("mode", "ro")
	query.Set("_pragma", "query_only=1")
	return (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(path),
		RawQuery: query.Encode(),
	}).String()
}

func eventJobID(event modulecore.EventEnvelope) (string, error) {
	var candidates []string
	if raw, ok, err := canonicalIdentityValue(event, "job_id"); err != nil {
		return "", err
	} else if ok {
		candidates = append(candidates, raw)
	}
	if event.ComponentID == "ai_workflow" && strings.HasPrefix(event.EventType, "heavy_worker.") {
		if raw, ok, err := canonicalIdentityValue(event, "task_reference"); err != nil {
			return "", err
		} else if ok {
			candidates = append(candidates, raw)
		}
	}
	if event.ComponentID == "superagent" && isSuperAgentRunEvent(event.EventType) {
		raw, ok, err := canonicalIdentityValue(event, "run_reference")
		if err != nil {
			return "", err
		}
		if ok && strings.HasPrefix(raw, "run_lead_") {
			candidates = append(candidates, strings.TrimPrefix(raw, "run_lead_"))
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	for _, candidate := range candidates[1:] {
		if candidate != candidates[0] {
			return "", fail("conflicting_job_identity", "event %q has conflicting job references", event.EventID)
		}
	}
	return candidates[0], nil
}

func canonicalIdentityValue(event modulecore.EventEnvelope, key string) (string, bool, error) {
	if event.Payload == nil {
		return "", false, nil
	}
	raw, present := event.Payload[key]
	if !present || raw == nil {
		return "", false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", false, fail("invalid_job_identity", "event %q field %q must be a string", event.EventID, key)
	}
	value = strings.TrimSpace(value)
	return value, value != "", nil
}

func isSuperAgentRunEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "lead_agent.") ||
		strings.HasPrefix(eventType, "subagent.") ||
		strings.HasPrefix(eventType, "run_queue.")
}

func references(event modulecore.EventEnvelope) []modulecore.EventID {
	refs := make([]modulecore.EventID, 0, 1+len(event.DependencyEventIDs))
	if event.CausationEventID != "" {
		refs = append(refs, event.CausationEventID)
	}
	return append(refs, event.DependencyEventIDs...)
}

func verifyOnlyTraceChanged(before, after []modulecore.EventEnvelope) error {
	if len(before) != len(after) {
		return fmt.Errorf("event count changed")
	}
	beforeHash, err := nonTraceHash(before)
	if err != nil {
		return err
	}
	afterHash, err := nonTraceHash(after)
	if err != nil {
		return err
	}
	if beforeHash != afterHash {
		return fmt.Errorf("content outside trace_id changed")
	}
	return nil
}

func eventSetHash(events []modulecore.EventEnvelope) (string, error) {
	return hashEvents(events, false)
}

func nonTraceHash(events []modulecore.EventEnvelope) (string, error) {
	return hashEvents(events, true)
}

func hashEvents(events []modulecore.EventEnvelope, clearTrace bool) (string, error) {
	ordered := append([]modulecore.EventEnvelope(nil), events...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].EventID < ordered[j].EventID })
	h := sha256.New()
	for _, event := range ordered {
		if clearTrace {
			event.TraceID = ""
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return "", err
		}
		h.Write(encoded)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func buildOutput(ctx context.Context, path string, events []modulecore.EventEnvelope) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		return err
	}
	succeeded := false
	defer func() {
		_ = store.Close()
		if !succeeded {
			_ = os.Remove(path)
			_ = os.Remove(path + "-wal")
			_ = os.Remove(path + "-shm")
		}
	}()
	if err := store.AppendBatch(ctx, events); err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	succeeded = true
	return nil
}

func fileSHA256(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func compareManifest(prior, current Manifest) error {
	if prior.SchemaVersion != ManifestSchemaVersion || prior.Mode != ModeDryRun || (prior.Status != StatusReady && prior.Status != StatusReadyWithUnresolved) {
		return fmt.Errorf("prior receipt is not a ready dry-run")
	}
	if prior.Status != current.Status || prior.SourceSHA256 != current.SourceSHA256 || prior.InputCount != current.InputCount || prior.RepairJobCount != current.RepairJobCount || prior.RepairSegmentCount != current.RepairSegmentCount || prior.RepairEventCount != current.RepairEventCount || prior.VerifiedJobCount != current.VerifiedJobCount || prior.RepairableJobCount != current.RepairableJobCount || prior.UnresolvedJobCount != current.UnresolvedJobCount || prior.RepairIdleChatRunCount != current.RepairIdleChatRunCount || prior.VerifiedIdleChatRunCount != current.VerifiedIdleChatRunCount || prior.RepairableIdleChatRunCount != current.RepairableIdleChatRunCount || prior.UnresolvedIdleChatRunCount != current.UnresolvedIdleChatRunCount || prior.InputEventSetSHA256 != current.InputEventSetSHA256 || prior.OutputEventSetSHA256 != current.OutputEventSetSHA256 || prior.NonTraceContentSHA256 != current.NonTraceContentSHA256 || !sameCountMap(prior.RepairEvidenceCounts, current.RepairEvidenceCounts) || !sameCountMap(prior.UnresolvedReasonCounts, current.UnresolvedReasonCounts) {
		return fmt.Errorf("source or repair result changed after dry-run")
	}
	return nil
}

func sameCountMap(left, right map[string]int) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func readManifest(path string) (Manifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("trailing JSON is not allowed")
		}
		return Manifest{}, err
	}
	return manifest, nil
}

func writeManifest(path string, manifest Manifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("manifest parent must be a real directory")
		}
		return err
	}
	temp, err := os.CreateTemp(parent, ".rencrow-event-trace-repair-")
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
	if err := syncDirectory(parent); err != nil {
		return err
	}
	remove = false
	return nil
}

func finishFailure(options Options, manifest Manifest, fallbackCode string, err error) (Manifest, error) {
	manifest.Status = StatusBlocked
	manifest.ErrorCode = errorCode(err, fallbackCode)
	if strings.TrimSpace(options.Manifest) != "" {
		_ = writeManifest(options.Manifest, manifest)
	}
	return manifest, err
}

func blockedManifestWrite(manifest Manifest, err error) (Manifest, error) {
	manifest.Status = StatusBlocked
	manifest.ErrorCode = "manifest_write"
	return manifest, err
}

func errorCode(err error, fallback string) string {
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return fallback
}
