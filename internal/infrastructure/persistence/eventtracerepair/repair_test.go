package eventtracerepair

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDryRunAndBuildRepairFragmentedJobWithoutChangingEventIdentity(t *testing.T) {
	ctx := context.Background()
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	rootTrace := modulecore.NewTraceID()
	jobID := "20260830-094130-f66c4c10"
	events := []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "metrics.latency", map[string]any{"job_id": jobID}),
		eventFixture(rootTrace, "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "superagent", "lead_agent.started", map[string]any{"run_reference": "run_lead_" + jobID}),
		eventFixture(modulecore.NewTraceID(), "ai_workflow", "heavy_worker.started", map[string]any{"task_reference": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "agent.response", map[string]any{"job_id": jobID}),
	}
	writeStore(t, source, events)

	dryManifest := filepath.Join(snapshot, "dry-run.json")
	dry, err := Run(ctx, Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: dryManifest, Mode: ModeDryRun,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Status != StatusReady || dry.InputCount != len(events) || dry.RepairJobCount != 1 || dry.RepairEventCount != len(events) {
		t.Fatalf("unexpected dry-run receipt: %+v", dry)
	}
	if dry.SourceSHA256 == "" || dry.InputEventSetSHA256 == "" || dry.OutputEventSetSHA256 == "" || dry.InputEventSetSHA256 == dry.OutputEventSetSHA256 {
		t.Fatalf("missing or unchanged hashes: %+v", dry)
	}

	output := filepath.Join(snapshot, "repaired.db")
	built, err := Run(ctx, Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: output,
		Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryManifest, Mode: ModeBuild,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != StatusBuilt || built.OutputEventSetSHA256 != dry.OutputEventSetSHA256 {
		t.Fatalf("unexpected build receipt: %+v", built)
	}

	got := readAll(t, output)
	if len(got) != len(events) {
		t.Fatalf("output count=%d want=%d", len(got), len(events))
	}
	byID := make(map[modulecore.EventID]modulecore.EventEnvelope, len(got))
	for _, event := range got {
		byID[event.EventID] = event
		if event.TraceID != rootTrace {
			t.Fatalf("event %s trace=%s want root %s", event.EventID, event.TraceID, rootTrace)
		}
	}
	for _, before := range events {
		after := byID[before.EventID]
		before.TraceID = rootTrace
		beforeJSON, _ := json.Marshal(before)
		afterJSON, _ := json.Marshal(after)
		if string(beforeJSON) != string(afterJSON) {
			t.Fatalf("event %s changed outside trace: before=%s after=%s", before.EventID, beforeJSON, afterJSON)
		}
	}
}

func TestDryRunBlocksAmbiguousMessageReceivedRoot(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	jobID := "job-ambiguous"
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": jobID}),
	})

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "ambiguous_root" {
		t.Fatalf("ambiguous root must fail closed: receipt=%+v err=%v", receipt, err)
	}
}

func TestBuildRejectsSourceChangedAfterDryRun(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	jobID := "job-source-change"
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "agent.response", map[string]any{"job_id": jobID}),
	})
	dryPath := filepath.Join(snapshot, "dry-run.json")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: dryPath, Mode: ModeDryRun,
	}); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.NewSQLiteStore(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), eventFixture(modulecore.NewTraceID(), "orchestrator", "heartbeat.skip", nil)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "manifest_mismatch" {
		t.Fatalf("changed source must fail closed: receipt=%+v err=%v", receipt, err)
	}
}

func TestDryRunBlocksConflictingJobReferences(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": "job-a"}),
		eventFixture(modulecore.NewTraceID(), "ai_workflow", "heavy_worker.started", map[string]any{"job_id": "job-a", "task_reference": "job-b"}),
	})

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "conflicting_job_identity" {
		t.Fatalf("conflicting job references must fail closed: receipt=%+v err=%v", receipt, err)
	}
}

func eventFixture(traceID modulecore.TraceID, componentID, eventType string, payload map[string]any) modulecore.EventEnvelope {
	return modulecore.NewEventEnvelope(traceID, "", nil, componentID, eventType, time.Date(2026, 8, 30, 9, 41, 30, 0, time.UTC), payload)
}

func writeStore(t *testing.T, path string, events []modulecore.EventEnvelope) {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendBatch(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func readAll(t *testing.T, path string) []modulecore.EventEnvelope {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil || len(content) == 0 {
		t.Fatalf("read output: size=%d err=%v", len(content), err)
	}
	events, _, err := readSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return events
}
