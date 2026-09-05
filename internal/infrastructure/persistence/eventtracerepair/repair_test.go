package eventtracerepair

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	storedSource := readAll(t, source)

	dryManifest := filepath.Join(snapshot, "dry-run.json")
	dry, err := Run(ctx, Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: dryManifest, Mode: ModeDryRun,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Status != StatusReady || dry.InputCount != len(events) || dry.RepairJobCount != 1 || dry.RepairSegmentCount != 1 || dry.RepairEventCount != 4 {
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
	for _, before := range storedSource {
		after := byID[before.EventID]
		before.TraceID = rootTrace
		beforeJSON, _ := json.Marshal(before)
		afterJSON, _ := json.Marshal(after)
		if string(beforeJSON) != string(afterJSON) {
			t.Fatalf("event %s changed outside trace: before=%s after=%s", before.EventID, beforeJSON, afterJSON)
		}
	}

	secondDry, err := Run(ctx, Options{
		SnapshotDir: snapshot, SourceStore: output, OutputStore: filepath.Join(snapshot, "idempotent-repaired.db"),
		Manifest: filepath.Join(snapshot, "second-dry-run.json"), Mode: ModeDryRun,
	})
	if err != nil {
		t.Fatalf("second dry-run: %v", err)
	}
	if secondDry.Status != StatusReady || secondDry.InputEventSetSHA256 != secondDry.OutputEventSetSHA256 || secondDry.RepairJobCount != 0 || secondDry.RepairableJobCount != 0 || secondDry.RepairSegmentCount != 0 || secondDry.RepairEventCount != 0 || secondDry.VerifiedJobCount != 1 || secondDry.UnresolvedJobCount != 0 {
		t.Fatalf("repaired output must be idempotently verified: %+v", secondDry)
	}
	secondEvents := readAll(t, output)
	for index, event := range secondEvents {
		if event.TraceID != byID[event.EventID].TraceID {
			t.Fatalf("second dry-run changed event[%d] trace", index)
		}
	}
}

func TestDryRunSegmentsReusedJobByDirectAndQueueTriggerRoots(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	jobID := "job-reused"
	directTrace := modulecore.NewTraceID()
	queueTrace := modulecore.NewTraceID()
	events := []modulecore.EventEnvelope{
		eventFixture(directTrace, "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "agent.thinking", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "viewer.error", map[string]any{"job_id": jobID}),
		eventFixture(queueTrace, "superagent", "run_queue.claimed", map[string]any{"run_reference": "run_lead_" + jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "agent.thinking", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "superagent", "run_queue.failed", map[string]any{"run_reference": "run_lead_" + jobID}),
	}
	writeStore(t, source, events)

	dryPath := filepath.Join(snapshot, "dry-run.json")
	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: dryPath, Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReady || receipt.RepairJobCount != 1 || receipt.RepairSegmentCount != 2 || receipt.RepairEventCount != 5 {
		t.Fatalf("reused job must be segmented by owner roots: receipt=%+v err=%v", receipt, err)
	}
	if receipt.RepairEvidenceCounts["message_received_root"] != 1 || receipt.RepairEvidenceCounts["run_queue_claimed_root"] != 1 {
		t.Fatalf("unexpected evidence counts: %+v", receipt.RepairEvidenceCounts)
	}
	output := filepath.Join(snapshot, "repaired.db")
	if _, err := Run(context.Background(), Options{SnapshotDir: snapshot, SourceStore: source, OutputStore: output, Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild}); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, output)
	for index, event := range got {
		want := directTrace
		if index >= 3 {
			want = queueTrace
		}
		if event.TraceID != want {
			t.Fatalf("event[%d] trace=%s want=%s", index, event.TraceID, want)
		}
	}
	secondDry, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: output, OutputStore: filepath.Join(snapshot, "idempotent-repaired.db"),
		Manifest: filepath.Join(snapshot, "second-dry-run.json"), Mode: ModeDryRun,
	})
	if err != nil {
		t.Fatalf("second dry-run: %v", err)
	}
	if secondDry.Status != StatusReady || secondDry.InputEventSetSHA256 != secondDry.OutputEventSetSHA256 || secondDry.RepairJobCount != 0 || secondDry.RepairableJobCount != 0 || secondDry.RepairSegmentCount != 0 || secondDry.RepairEventCount != 0 || secondDry.VerifiedJobCount != 1 || secondDry.UnresolvedJobCount != 0 {
		t.Fatalf("repaired reused job must be idempotently verified: %+v", secondDry)
	}
}

func TestDryRunCountsOnlyChangedSegmentInMixedReusedJob(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	jobID := "job-mixed-reused"
	directTrace := modulecore.NewTraceID()
	queueTrace := modulecore.NewTraceID()
	events := []modulecore.EventEnvelope{
		eventFixture(directTrace, "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(directTrace, "orchestrator", "agent.thinking", map[string]any{"job_id": jobID}),
		eventFixture(directTrace, "orchestrator", "viewer.error", map[string]any{"job_id": jobID}),
		eventFixture(queueTrace, "superagent", "run_queue.claimed", map[string]any{"run_reference": "run_lead_" + jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "agent.thinking", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "superagent", "run_queue.failed", map[string]any{"run_reference": "run_lead_" + jobID}),
	}
	writeStore(t, source, events)

	dryPath := filepath.Join(snapshot, "dry-run.json")
	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: dryPath, Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReady || receipt.RepairJobCount != 1 || receipt.RepairSegmentCount != 1 || receipt.RepairEventCount != 3 {
		t.Fatalf("only broken segment must be repairable: receipt=%+v err=%v", receipt, err)
	}
	if receipt.RepairEvidenceCounts[repairEvidenceMessageReceivedRoot] != 0 || receipt.RepairEvidenceCounts[repairEvidenceRunQueueClaimedRoot] != 1 {
		t.Fatalf("only broken segment evidence must be counted: %+v", receipt.RepairEvidenceCounts)
	}

	output := filepath.Join(snapshot, "repaired.db")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: output,
		Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild,
	}); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, output)
	for index, event := range got {
		want := directTrace
		if index >= 3 {
			want = queueTrace
		}
		if event.TraceID != want {
			t.Fatalf("event[%d] trace=%s want=%s", index, event.TraceID, want)
		}
	}
}

func TestDryRunClassifiesStandaloneTTSSessionAndBackgroundFailure(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	ttsTrace := modulecore.NewTraceID()
	backgroundTrace := modulecore.NewTraceID()
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(ttsTrace, "orchestrator", "metrics.latency", map[string]any{"job_id": "idle:0001", "session_id": "idle", "response_id": "idle:0001", "content": `{"kind":"tts","point":"audio_chunk_ready"}`}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "tts.audio_chunk", map[string]any{"job_id": "idle:0001", "session_id": "idle", "response_id": "idle:0001"}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "tts.session_completed", map[string]any{"job_id": "idle:0001", "session_id": "idle", "response_id": "idle:0001"}),
		eventFixture(backgroundTrace, "orchestrator", "background_job.failed", map[string]any{"job_id": "background-job-1"}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "job.notification", map[string]any{"job_id": "background-job-1"}),
	})

	receipt, err := Run(context.Background(), Options{SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"), Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun})
	if err != nil || receipt.Status != StatusReady || receipt.RepairJobCount != 2 || receipt.RepairSegmentCount != 2 || receipt.RepairEventCount != 3 {
		t.Fatalf("owner evidence groups must be repairable: receipt=%+v err=%v", receipt, err)
	}
	if receipt.RepairEvidenceCounts["tts_session_existing_trace"] != 1 || receipt.RepairEvidenceCounts["background_failure_root"] != 1 {
		t.Fatalf("unexpected evidence counts: %+v", receipt.RepairEvidenceCounts)
	}
}

func TestDryRunRepairsIdleChatTimelineRunWithoutCountingItAsJob(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	sessionID := "idle-identity-step02-topic-00"
	rootTrace := modulecore.NewTraceID()
	events := []modulecore.EventEnvelope{
		eventFixture(rootTrace, "orchestrator", "idlechat.topic", map[string]any{"session_id": sessionID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "mio", "to": "shiro", "turn_index": 1}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "shiro", "to": "mio", "turn_index": 2}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.summary", map[string]any{"session_id": sessionID, "turn_index": 3}),
	}
	writeStore(t, source, events)

	dryPath := filepath.Join(snapshot, "dry-run.json")
	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: dryPath, Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReady || receipt.RepairJobCount != 0 || receipt.RepairIdleChatRunCount != 1 || receipt.RepairableIdleChatRunCount != 1 || receipt.RepairSegmentCount != 1 || receipt.RepairEventCount != 3 || receipt.UnresolvedIdleChatRunCount != 0 {
		t.Fatalf("idle chat run must be repaired independently from jobs: receipt=%+v err=%v", receipt, err)
	}
	if receipt.RepairEvidenceCounts[repairEvidenceIdleChatSessionTopicRoot] != 1 {
		t.Fatalf("unexpected evidence counts: %+v", receipt.RepairEvidenceCounts)
	}

	output := filepath.Join(snapshot, "repaired.db")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: output,
		Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild,
	}); err != nil {
		t.Fatal(err)
	}
	for index, event := range readAll(t, output) {
		if event.TraceID != rootTrace {
			t.Fatalf("event[%d] trace=%s want=%s", index, event.TraceID, rootTrace)
		}
	}
	second, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: output, OutputStore: filepath.Join(snapshot, "second-repaired.db"),
		Manifest: filepath.Join(snapshot, "second-dry-run.json"), Mode: ModeDryRun,
	})
	if err != nil || second.Status != StatusReady || second.RepairIdleChatRunCount != 0 || second.VerifiedIdleChatRunCount != 1 || second.UnresolvedIdleChatRunCount != 0 || second.InputEventSetSHA256 != second.OutputEventSetSHA256 {
		t.Fatalf("repaired idle chat run must be idempotently verified: receipt=%+v err=%v", second, err)
	}
}

func TestDryRunSegmentsReplayedStorySessionByTurnOneRoots(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	sessionID := "story-episode-reused"
	firstTrace := modulecore.NewTraceID()
	secondTrace := modulecore.NewTraceID()
	events := []modulecore.EventEnvelope{
		eventFixture(firstTrace, "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "mio", "to": "shiro", "turn_index": 1}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "shiro", "to": "mio", "turn_index": 2}),
		eventFixture(secondTrace, "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "mio", "to": "shiro", "turn_index": 1}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "shiro", "to": "mio", "turn_index": 2}),
	}
	writeStore(t, source, events)

	dryPath := filepath.Join(snapshot, "dry-run.json")
	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: dryPath, Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReady || receipt.RepairJobCount != 0 || receipt.RepairIdleChatRunCount != 2 || receipt.RepairSegmentCount != 2 || receipt.RepairEventCount != 2 {
		t.Fatalf("replayed story session must remain two owner runs: receipt=%+v err=%v", receipt, err)
	}
	if receipt.RepairEvidenceCounts[repairEvidenceIdleChatStoryTurnRoot] != 2 {
		t.Fatalf("unexpected evidence counts: %+v", receipt.RepairEvidenceCounts)
	}
	output := filepath.Join(snapshot, "repaired.db")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: output,
		Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild,
	}); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, output)
	for index, want := range []modulecore.TraceID{firstTrace, firstTrace, secondTrace, secondTrace} {
		if got[index].TraceID != want {
			t.Fatalf("event[%d] trace=%s want=%s", index, got[index].TraceID, want)
		}
	}
}

func TestDryRunRepairsIdleChatForecastFailureFromOwnerAnnouncement(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	sessionID := "forecast-identity-step02"
	rootTrace := modulecore.NewTraceID()
	events := []modulecore.EventEnvelope{
		eventFixture(rootTrace, "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "user", "to": "mio"}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "shiro", "to": "mio", "turn_index": 1}),
	}
	writeStore(t, source, events)

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReady || receipt.RepairIdleChatRunCount != 1 || receipt.RepairSegmentCount != 1 || receipt.RepairEventCount != 1 {
		t.Fatalf("forecast failure must bind to its owner announcement: receipt=%+v err=%v", receipt, err)
	}
	if receipt.RepairEvidenceCounts[repairEvidenceIdleChatForecastFailureRoot] != 1 {
		t.Fatalf("unexpected evidence counts: %+v", receipt.RepairEvidenceCounts)
	}
}

func TestDryRunLeavesMalformedIdleChatTurnSequenceUnresolved(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	sessionID := "story-episode-malformed"
	events := []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "mio", "to": "shiro", "turn_index": 1}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "shiro", "to": "mio", "turn_index": 3}),
	}
	writeStore(t, source, events)

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReadyWithUnresolved || receipt.RepairEventCount != 0 || receipt.UnresolvedIdleChatRunCount != 1 {
		t.Fatalf("malformed idle chat sequence must remain unchanged: receipt=%+v err=%v", receipt, err)
	}
	if receipt.UnresolvedReasonCounts[unresolvedReasonInvalidIdleChatTurnSequence] != 1 || receipt.InputEventSetSHA256 != receipt.OutputEventSetSHA256 {
		t.Fatalf("unexpected unresolved result: %+v", receipt)
	}
}

func TestDryRunLeavesIdleChatTurnBeforeTopicUnresolved(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	sessionID := "idle-topic-after-turn"
	events := []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "mio", "to": "shiro", "turn_index": 1}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.topic", map[string]any{"session_id": sessionID}),
	}
	writeStore(t, source, events)

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReadyWithUnresolved || receipt.RepairEventCount != 0 || receipt.UnresolvedIdleChatRunCount != 1 {
		t.Fatalf("turn before topic must remain unchanged: receipt=%+v err=%v", receipt, err)
	}
	if receipt.UnresolvedReasonCounts[unresolvedReasonAmbiguousIdleChatSession] != 1 || receipt.InputEventSetSHA256 != receipt.OutputEventSetSHA256 {
		t.Fatalf("unexpected unresolved result: %+v", receipt)
	}
}

func TestDryRunLeavesIdleChatTopicWithoutTurnUnresolved(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	sessionID := "idle-topic-without-turn"
	events := []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.message", map[string]any{"session_id": sessionID, "from": "user", "to": "mio"}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "idlechat.topic", map[string]any{"session_id": sessionID}),
	}
	writeStore(t, source, events)

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReadyWithUnresolved || receipt.RepairEventCount != 0 || receipt.UnresolvedIdleChatRunCount != 1 {
		t.Fatalf("topic without a numbered turn must remain unchanged: receipt=%+v err=%v", receipt, err)
	}
	if receipt.UnresolvedReasonCounts[unresolvedReasonInvalidIdleChatTurnSequence] != 1 || receipt.InputEventSetSHA256 != receipt.OutputEventSetSHA256 {
		t.Fatalf("unexpected unresolved result: %+v", receipt)
	}
}

func TestDryRunKeepsEvidenceInsufficientGroupUnchanged(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	events := []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "unknown.started", map[string]any{"job_id": "unknown-job"}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "unknown.finished", map[string]any{"job_id": "unknown-job"}),
	}
	writeStore(t, source, events)
	dryPath := filepath.Join(snapshot, "dry-run.json")
	receipt, err := Run(context.Background(), Options{SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"), Manifest: dryPath, Mode: ModeDryRun})
	if err != nil || receipt.Status != StatusReadyWithUnresolved || receipt.UnresolvedJobCount != 1 || receipt.RepairEventCount != 0 {
		t.Fatalf("evidence-insufficient group must remain explicit: receipt=%+v err=%v", receipt, err)
	}
	if receipt.UnresolvedReasonCounts["missing_owner_root"] != 1 || receipt.InputEventSetSHA256 != receipt.OutputEventSetSHA256 {
		t.Fatalf("unresolved group changed or reason missing: %+v", receipt)
	}
	output := filepath.Join(snapshot, "repaired.db")
	built, err := Run(context.Background(), Options{SnapshotDir: snapshot, SourceStore: source, OutputStore: output, Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild})
	if err != nil {
		t.Fatal(err)
	}
	if built.Status != StatusBuilt || built.UnresolvedJobCount != 1 || built.RepairEventCount != 0 || built.OutputEventSetSHA256 != receipt.OutputEventSetSHA256 {
		t.Fatalf("unresolved build must remain investigation-only and checksum-bound: %+v", built)
	}
	got := readAll(t, output)
	for index := range events {
		if got[index].TraceID != events[index].TraceID {
			t.Fatalf("unresolved event[%d] trace changed", index)
		}
	}
}

func TestDryRunLeavesTwoMessageRootsWithoutPriorTerminalUnresolved(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	jobID := "job-missing-terminal"
	firstTrace := modulecore.NewTraceID()
	secondTrace := modulecore.NewTraceID()
	events := []modulecore.EventEnvelope{
		eventFixture(firstTrace, "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "orchestrator", "agent.thinking", map[string]any{"job_id": jobID}),
		eventFixture(secondTrace, "orchestrator", "message.received", map[string]any{"job_id": jobID}),
	}
	writeStore(t, source, events)
	dryPath := filepath.Join(snapshot, "dry-run.json")
	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: dryPath, Mode: ModeDryRun,
	})
	if err != nil || receipt.Status != StatusReadyWithUnresolved || receipt.UnresolvedJobCount != 1 || receipt.RepairEventCount != 0 {
		t.Fatalf("ambiguous roots without terminal must remain unresolved: receipt=%+v err=%v", receipt, err)
	}
	if receipt.UnresolvedReasonCounts[unresolvedReasonAmbiguousRoot] != 1 {
		t.Fatalf("unexpected unresolved reasons: %+v", receipt.UnresolvedReasonCounts)
	}

	output := filepath.Join(snapshot, "repaired.db")
	if _, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: output,
		Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild,
	}); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, output)
	for index, event := range got {
		if event.TraceID != events[index].TraceID {
			t.Fatalf("unresolved event[%d] trace changed from %s to %s", index, events[index].TraceID, event.TraceID)
		}
	}
}

func TestDryRunRejectsCrossSegmentReference(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	jobID := "job-cross-segment-reference"
	firstTrace := modulecore.NewTraceID()
	secondTrace := modulecore.NewTraceID()
	root := eventFixture(firstTrace, "orchestrator", "message.received", map[string]any{"job_id": jobID})
	crossSegmentCause := eventFixture(secondTrace, "orchestrator", "agent.thinking", map[string]any{"job_id": jobID})
	terminal := eventFixture(firstTrace, "orchestrator", "viewer.error", map[string]any{"job_id": jobID})
	laterRoot := eventFixture(secondTrace, "orchestrator", "message.received", map[string]any{"job_id": jobID})
	dependent := eventFixture(secondTrace, "orchestrator", "agent.response", map[string]any{"job_id": jobID})
	dependent.CausationEventID = crossSegmentCause.EventID
	writeStore(t, source, []modulecore.EventEnvelope{root, crossSegmentCause, terminal, laterRoot, dependent})

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "cross_segment_reference" {
		t.Fatalf("cross-segment reference must fail closed: receipt=%+v err=%v", receipt, err)
	}
}

func TestDryRunLeavesMalformedTTSSessionUnresolved(t *testing.T) {
	tests := []struct {
		name   string
		events []modulecore.EventEnvelope
	}{
		{
			name: "multiple response ids",
			events: []modulecore.EventEnvelope{
				eventFixture(modulecore.NewTraceID(), "orchestrator", "metrics.latency", map[string]any{
					"job_id": "tts-malformed-response", "session_id": "tts-session", "response_id": "response-1",
					"content": `{"kind":"tts","point":"audio_chunk_ready"}`,
				}),
				eventFixture(modulecore.NewTraceID(), "orchestrator", "tts.audio_chunk", map[string]any{
					"job_id": "tts-malformed-response", "session_id": "tts-session", "response_id": "response-2",
				}),
				eventFixture(modulecore.NewTraceID(), "orchestrator", "tts.session_completed", map[string]any{
					"job_id": "tts-malformed-response", "session_id": "tts-session", "response_id": "response-2",
				}),
			},
		},
		{
			name: "missing completion",
			events: []modulecore.EventEnvelope{
				eventFixture(modulecore.NewTraceID(), "orchestrator", "metrics.latency", map[string]any{
					"job_id": "tts-missing-completion", "session_id": "tts-session", "response_id": "response-1",
					"content": `{"kind":"tts","point":"audio_chunk_ready"}`,
				}),
				eventFixture(modulecore.NewTraceID(), "orchestrator", "tts.audio_chunk", map[string]any{
					"job_id": "tts-missing-completion", "session_id": "tts-session", "response_id": "response-1",
				}),
			},
		},
		{
			name: "identity missing from one event",
			events: []modulecore.EventEnvelope{
				eventFixture(modulecore.NewTraceID(), "orchestrator", "metrics.latency", map[string]any{
					"job_id": "tts-missing-event-identity", "session_id": "tts-session", "response_id": "response-1",
					"content": `{"kind":"tts","point":"audio_chunk_ready"}`,
				}),
				eventFixture(modulecore.NewTraceID(), "orchestrator", "tts.audio_chunk", map[string]any{
					"job_id": "tts-missing-event-identity",
				}),
				eventFixture(modulecore.NewTraceID(), "orchestrator", "tts.session_completed", map[string]any{
					"job_id": "tts-missing-event-identity", "session_id": "tts-session", "response_id": "response-1",
				}),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := t.TempDir()
			source := filepath.Join(snapshot, "event_store.db")
			writeStore(t, source, test.events)
			dryPath := filepath.Join(snapshot, "dry-run.json")
			receipt, err := Run(context.Background(), Options{
				SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
				Manifest: dryPath, Mode: ModeDryRun,
			})
			if err != nil || receipt.Status != StatusReadyWithUnresolved || receipt.UnresolvedJobCount != 1 || receipt.RepairEventCount != 0 {
				t.Fatalf("malformed TTS session must remain unresolved: receipt=%+v err=%v", receipt, err)
			}
			if receipt.UnresolvedReasonCounts[unresolvedReasonMissingOwnerRoot] != 1 {
				t.Fatalf("unexpected unresolved reasons: %+v", receipt.UnresolvedReasonCounts)
			}
			output := filepath.Join(snapshot, "repaired.db")
			if _, err := Run(context.Background(), Options{
				SnapshotDir: snapshot, SourceStore: source, OutputStore: output,
				Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild,
			}); err != nil {
				t.Fatal(err)
			}
			got := readAll(t, output)
			for index, event := range got {
				if event.TraceID != test.events[index].TraceID {
					t.Fatalf("unresolved event[%d] trace changed from %s to %s", index, test.events[index].TraceID, event.TraceID)
				}
			}
		})
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

func TestBuildRejectsUnknownOrTrailingDryRunManifestJSON(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "unknown field",
			mutate: func(content []byte) []byte {
				trimmed := bytes.TrimSpace(content)
				return append(append(trimmed[:len(trimmed)-1], []byte(`,"unexpected":true}`)...), '\n')
			},
		},
		{
			name: "trailing json",
			mutate: func(content []byte) []byte {
				return append(append([]byte{}, content...), []byte(`{}`)...)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := t.TempDir()
			source := filepath.Join(snapshot, "event_store.db")
			writeStore(t, source, []modulecore.EventEnvelope{
				eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": "job-strict-manifest"}),
				eventFixture(modulecore.NewTraceID(), "orchestrator", "agent.response", map[string]any{"job_id": "job-strict-manifest"}),
			})
			dryPath := filepath.Join(snapshot, "dry-run.json")
			if _, err := Run(context.Background(), Options{
				SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
				Manifest: dryPath, Mode: ModeDryRun,
			}); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(dryPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dryPath, test.mutate(content), 0600); err != nil {
				t.Fatal(err)
			}
			receipt, err := Run(context.Background(), Options{
				SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
				Manifest: filepath.Join(snapshot, "build.json"), DryRunManifest: dryPath, Mode: ModeBuild,
			})
			if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "dry_run_manifest_invalid" {
				t.Fatalf("malformed dry-run manifest must block: receipt=%+v err=%v", receipt, err)
			}
		})
	}
}

func TestDryRunManifestWriteFailureReturnsBlockedStatus(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "heartbeat.skip", nil),
	})
	wantErr := errors.New("injected repair receipt failure")
	previous := repairManifestWriter
	repairManifestWriter = func(string, Manifest) error { return wantErr }
	defer func() { repairManifestWriter = previous }()

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if !errors.Is(err, wantErr) || receipt.Status != StatusBlocked || receipt.ErrorCode != "manifest_write" {
		t.Fatalf("manifest write failure must return blocked receipt: receipt=%+v err=%v", receipt, err)
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

func TestDryRunBlocksMalformedCanonicalJobIdentityField(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "message.received", map[string]any{"job_id": 42}),
	})

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "invalid_job_identity" {
		t.Fatalf("malformed canonical job identity must fail closed: receipt=%+v err=%v", receipt, err)
	}
}

func TestDryRunRejectsSymlinkedManifestParent(t *testing.T) {
	snapshot := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "heartbeat.skip", nil),
	})
	linkedParent := filepath.Join(snapshot, "linked-parent")
	if err := os.Symlink(outside, linkedParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(linkedParent, "dry-run.json"), Mode: ModeDryRun,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "invalid_path" {
		t.Fatalf("symlinked manifest parent must fail closed: receipt=%+v err=%v", receipt, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "dry-run.json")); !os.IsNotExist(err) {
		t.Fatalf("manifest escaped snapshot through symlinked parent: %v", err)
	}
}

func TestDryRunDoesNotFollowPredictableManifestTempSymlink(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	manifestPath := filepath.Join(snapshot, "dry-run.json")
	victim := filepath.Join(snapshot, "victim.txt")
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "orchestrator", "heartbeat.skip", nil),
	})
	if err := os.WriteFile(victim, []byte("do-not-change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, manifestPath+".tmp"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: manifestPath, Mode: ModeDryRun,
	}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(victim); err != nil || string(content) != "do-not-change" {
		t.Fatalf("predictable temp symlink changed victim: content=%q err=%v", content, err)
	}
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("manifest must be a regular file: info=%v err=%v", info, err)
	}
}

func TestDryRunUsesSuperAgentRunReferenceWithoutTreatingTaskAsJob(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	jobID := "20260829-234308-ec21a7d5"
	rootTrace := modulecore.NewTraceID()
	writeStore(t, source, []modulecore.EventEnvelope{
		eventFixture(rootTrace, "orchestrator", "message.received", map[string]any{"job_id": jobID}),
		eventFixture(modulecore.NewTraceID(), "superagent", "subagent.started", map[string]any{
			"run_reference":  "run_lead_" + jobID,
			"task_reference": "sub_shiro_1788047000859921868",
		}),
	})

	receipt, err := Run(context.Background(), Options{
		SnapshotDir: snapshot, SourceStore: source, OutputStore: filepath.Join(snapshot, "repaired.db"),
		Manifest: filepath.Join(snapshot, "dry-run.json"), Mode: ModeDryRun,
	})
	if err != nil {
		t.Fatalf("superagent task identity must not conflict with root job: receipt=%+v err=%v", receipt, err)
	}
	if receipt.Status != StatusReady || receipt.RepairJobCount != 1 || receipt.RepairEventCount != 1 {
		t.Fatalf("unexpected receipt: %+v", receipt)
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
