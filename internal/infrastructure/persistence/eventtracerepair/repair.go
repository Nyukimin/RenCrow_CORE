// Package eventtracerepair rebuilds a canonical Event Store snapshot after a
// known runtime defect split one trigger across multiple TraceIDs. It never
// opens the active production store for writing and never updates in place.
package eventtracerepair

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	ManifestSchemaVersion = "rencrow.identity.event-trace-repair/v1"
	ModeDryRun            = "dry-run"
	ModeBuild             = "build"
	StatusReady           = "ready"
	StatusBuilt           = "built"
	StatusBlocked         = "blocked"
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

	SourceSHA256          string `json:"source_sha256,omitempty"`
	InputCount            int    `json:"input_count"`
	RepairJobCount        int    `json:"repair_job_count"`
	RepairEventCount      int    `json:"repair_event_count"`
	InputEventSetSHA256   string `json:"input_event_set_sha256,omitempty"`
	OutputEventSetSHA256  string `json:"output_event_set_sha256,omitempty"`
	NonTraceContentSHA256 string `json:"non_trace_content_sha256,omitempty"`
	ErrorCode             string `json:"error_code,omitempty"`
}

type codedError struct {
	code string
	err  error
}

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

	repaired, jobCount, eventCount, err := repair(events)
	if err != nil {
		return finishFailure(options, manifest, errorCode(err, "repair_blocked"), err)
	}
	manifest.RepairJobCount = jobCount
	manifest.RepairEventCount = eventCount
	manifest.OutputEventSetSHA256, err = eventSetHash(repaired)
	if err != nil {
		return finishFailure(options, manifest, "output_hash", err)
	}
	if err := verifyOnlyTraceChanged(events, repaired); err != nil {
		return finishFailure(options, manifest, "content_drift", err)
	}

	if options.Mode == ModeDryRun {
		manifest.Status = StatusReady
		if err := writeManifest(options.Manifest, manifest); err != nil {
			return manifest, err
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
	if err := writeManifest(options.Manifest, manifest); err != nil {
		return manifest, err
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
	if mustExist {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("must be a regular non-symlink file")
		}
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path must stay inside snapshot directory")
	}
	return filepath.Clean(path), nil
}

func readSnapshot(ctx context.Context, path string) ([]modulecore.EventEnvelope, string, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=query_only%3d1")
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

type repairGroup struct {
	jobID   string
	indexes []int
	roots   []int
	traces  map[modulecore.TraceID]struct{}
}

func repair(input []modulecore.EventEnvelope) ([]modulecore.EventEnvelope, int, int, error) {
	output := append([]modulecore.EventEnvelope(nil), input...)
	groups := make(map[string]*repairGroup)
	eventJob := make(map[modulecore.EventID]string)
	for index, event := range input {
		jobID, err := eventJobID(event)
		if err != nil {
			return nil, 0, 0, err
		}
		if jobID == "" {
			continue
		}
		group := groups[jobID]
		if group == nil {
			group = &repairGroup{jobID: jobID, traces: make(map[modulecore.TraceID]struct{})}
			groups[jobID] = group
		}
		group.indexes = append(group.indexes, index)
		group.traces[event.TraceID] = struct{}{}
		if event.ComponentID == "orchestrator" && event.EventType == "message.received" {
			group.roots = append(group.roots, index)
		}
		eventJob[event.EventID] = jobID
	}
	for _, event := range input {
		ownerJob := eventJob[event.EventID]
		for _, ref := range references(event) {
			refJob := eventJob[ref]
			if ownerJob != refJob && (ownerJob != "" || refJob != "") {
				return nil, 0, 0, fail("cross_group_reference", "event %q and reference %q cross repair groups", event.EventID, ref)
			}
		}
	}
	jobIDs := make([]string, 0, len(groups))
	for jobID := range groups {
		jobIDs = append(jobIDs, jobID)
	}
	sort.Strings(jobIDs)
	repairJobs, repairEvents := 0, 0
	for _, jobID := range jobIDs {
		group := groups[jobID]
		if len(group.traces) <= 1 {
			continue
		}
		if len(group.roots) != 1 {
			return nil, 0, 0, fail("ambiguous_root", "job %q has %d message.received roots", jobID, len(group.roots))
		}
		target := input[group.roots[0]].TraceID
		for _, index := range group.indexes {
			output[index].TraceID = target
		}
		repairJobs++
		repairEvents += len(group.indexes)
	}
	if err := modulecore.ValidateEventEnvelopeGraph(output); err != nil {
		return nil, 0, 0, fail("invalid_repaired_graph", "%v", err)
	}
	return output, repairJobs, repairEvents, nil
}

func eventJobID(event modulecore.EventEnvelope) (string, error) {
	var candidates []string
	for _, key := range []string{"job_id", "task_reference"} {
		if raw, ok := event.Payload[key].(string); ok && strings.TrimSpace(raw) != "" {
			candidates = append(candidates, strings.TrimSpace(raw))
		}
	}
	if raw, ok := event.Payload["run_reference"].(string); ok && strings.HasPrefix(strings.TrimSpace(raw), "run_lead_") {
		candidates = append(candidates, strings.TrimPrefix(strings.TrimSpace(raw), "run_lead_"))
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
	if prior.SchemaVersion != ManifestSchemaVersion || prior.Mode != ModeDryRun || prior.Status != StatusReady {
		return fmt.Errorf("prior receipt is not a ready dry-run")
	}
	if prior.SourceSHA256 != current.SourceSHA256 || prior.InputCount != current.InputCount || prior.RepairJobCount != current.RepairJobCount || prior.RepairEventCount != current.RepairEventCount || prior.InputEventSetSHA256 != current.InputEventSetSHA256 || prior.OutputEventSetSHA256 != current.OutputEventSetSHA256 || prior.NonTraceContentSHA256 != current.NonTraceContentSHA256 {
		return fmt.Errorf("source or repair result changed after dry-run")
	}
	return nil
}

func readManifest(path string) (Manifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func writeManifest(path string, manifest Manifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(encoded, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func finishFailure(options Options, manifest Manifest, fallbackCode string, err error) (Manifest, error) {
	manifest.Status = StatusBlocked
	manifest.ErrorCode = errorCode(err, fallbackCode)
	if strings.TrimSpace(options.Manifest) != "" {
		_ = writeManifest(options.Manifest, manifest)
	}
	return manifest, err
}

func errorCode(err error, fallback string) string {
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return fallback
}
