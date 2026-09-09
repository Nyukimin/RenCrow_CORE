package runmigration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	task "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMigrationEventQueueInputKeyRequiresExactTraceProof(t *testing.T) {
	for _, reference := range []string{"original-input", "different-input"} {
		t.Run(reference, func(t *testing.T) {
			id, taskID := core.NewRunID(), core.NewTaskID()
			const source = "run_lead_original-input"
			queue, err := core.NewMigrationID(core.CanonicalQueueItemID, "run_queue", "queue_item_id", "original-queue")
			if err != nil {
				t.Fatal(err)
			}
			c := &cohort{runs: map[core.RunID]task.Run{id: {RunID: id, TaskID: taskID, Assignee: "Shiro"}}, tasks: map[core.TaskID]task.Task{taskID: {TaskID: taskID}}, rawRuns: map[string][]core.RunID{source: {id}}, queueReferences: map[string]string{queue: source}}
			e := core.NewRootEventEnvelope("superagent", "run_queue.completed", time.Now().UTC(), map[string]any{"queue_reference": "original-queue", "run_reference": reference})
			err = c.bindEventIdentity(&e)
			if reference == "different-input" {
				if err == nil {
					t.Fatal("conflicting queue input accepted")
				}
				return
			}
			if err != nil || e.RunID != id || e.TaskID != taskID || e.ActorID != "shiro" {
				t.Fatalf("missing queue owner: %#v %v", e, err)
			}
		})
	}
}

func TestMigrationEventCanonicalChildNeedsNoLegacyReference(t *testing.T) {
	f := newFullCohortFixture(t)
	e := core.NewRootEventEnvelope("superagent", "subagent.completed", f.options.Inventory.SnapshotAt, map[string]any{"status": "completed"})
	e.TaskID, e.RunID, e.ActorKind, e.ActorID = f.taskID, f.runID, "agent", "shiro"
	appendMigrationEvent(t, &f, e)
	got := applyAndReadMigrationEvent(t, &f, e.EventID)
	if got.TaskID != e.TaskID || got.RunID != e.RunID || got.ActorID != e.ActorID {
		t.Fatal("canonical child identity changed")
	}
}

func TestMigrationEventPromotesExplicitLeadReference(t *testing.T) {
	f := newFullCohortFixture(t)
	e := core.NewRootEventEnvelope("superagent", "lead_agent.started", f.options.Inventory.SnapshotAt.Add(-time.Minute), map[string]any{"run_reference": string(f.runID), "actor_label": "LeadAgent", "status": "running"})
	appendMigrationEvent(t, &f, e)
	got := applyAndReadMigrationEvent(t, &f, e.EventID)
	if got.TaskID != f.taskID || got.RunID != f.runID || got.ActorID != "shiro" || got.ActorKind != "agent" {
		t.Fatalf("owner identity missing: %#v", got)
	}
	if _, ok := got.Payload["run_reference"]; ok {
		t.Fatal("duplicate execution reference remains")
	}
	if got.TraceID != e.TraceID || got.EventID != e.EventID {
		t.Fatal("Event or Trace changed")
	}
}

func TestMigrationEventPromotesChildRatherThanParent(t *testing.T) {
	f := newFullCohortFixture(t)
	db := openMigrationFixtureDB(t, filepath.Join(f.options.Snapshot, "superagent.sqlite"))
	var raw string
	if err := db.QueryRow("SELECT payload FROM subagent_task LIMIT 1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var child map[string]any
	if err := json.Unmarshal([]byte(raw), &child); err != nil {
		t.Fatal(err)
	}
	reference, _ := child["subagent_id"].(string)
	if reference == "" {
		t.Fatal("fixture has no legacy child")
	}
	e := core.NewRootEventEnvelope("superagent", "subagent.started", f.options.Inventory.SnapshotAt.Add(-time.Minute), map[string]any{"run_reference": string(f.runID), "task_reference": reference, "actor_label": "Subagent", "status": "running"})
	appendMigrationEvent(t, &f, e)
	got := applyAndReadMigrationEvent(t, &f, e.EventID)
	if got.RunID != f.childRun || got.TaskID != f.childTask || got.ActorID != "shiro" {
		t.Fatalf("child attributed to parent: %#v", got)
	}
}

func TestMigrationEventRejectsConflictingExecutionReferences(t *testing.T) {
	f := newFullCohortFixture(t)
	e := core.NewRootEventEnvelope("superagent", "lead_agent.started", f.options.Inventory.SnapshotAt.Add(-time.Minute), map[string]any{"run_id": "unproven-other-run"})
	e.RunID = f.runID
	appendMigrationEvent(t, &f, e)
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("conflicting references accepted")
	}
}

func TestMigrationEventRestoresClosedLeadHistoryWithExplicitActor(t *testing.T) {
	f := newFullCohortFixture(t)
	old := core.NewRunID()
	at := f.options.Inventory.SnapshotAt.Add(-time.Hour)
	start := core.NewRootEventEnvelope("superagent", "lead_agent_started", at, map[string]any{"actor": "LeadAgent", "status": "running"})
	end := core.NewRootEventEnvelope("superagent", "lead_agent_completed", at.Add(time.Minute), map[string]any{"actor": "LeadAgent", "status": "completed"})
	start.RunID, end.RunID = old, old
	end.TraceID = start.TraceID
	f.options.Inventory.LegacyRunActors = map[string]string{string(old): "lumina"}
	appendMigrationEvent(t, &f, start, end)
	got := applyAndReadMigrationEvent(t, &f, end.EventID)
	if got.ActorID != "lumina" || got.TaskID == "" || got.RunID == old {
		t.Fatalf("historical owner missing: %#v", got)
	}
}

func TestMigrationEventRejectsIncompleteLegacyHistory(t *testing.T) {
	f := newFullCohortFixture(t)
	e := core.NewRootEventEnvelope("superagent", "lead_agent_started", f.options.Inventory.SnapshotAt.Add(-time.Hour), map[string]any{"actor": "LeadAgent", "status": "running"})
	e.RunID = core.NewRunID()
	f.options.Inventory.LegacyRunActors = map[string]string{string(e.RunID): "lumina"}
	appendMigrationEvent(t, &f, e)
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("incomplete execution history accepted")
	}
}

func TestMigrationEventRejectsUnusedHistoricalActor(t *testing.T) {
	f := newFullCohortFixture(t)
	f.options.Inventory.LegacyRunActors = map[string]string{string(core.NewRunID()): "lumina"}
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("unused historical attribution accepted")
	}
}

func appendMigrationEvent(t *testing.T, f *fullCohortFixture, events ...core.EventEnvelope) {
	t.Helper()
	s, err := eventstore.NewSQLiteStore(filepath.Join(f.options.Snapshot, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AppendBatch(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &f.options, "events.sqlite")
}

func applyAndReadMigrationEvent(t *testing.T, f *fullCohortFixture, id core.EventID) core.EventEnvelope {
	t.Helper()
	r, err := Run(context.Background(), f.options)
	if err != nil {
		t.Fatal(err)
	}
	f.options.Mode, f.options.Expected = "apply", &r
	if _, err = Run(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	s, err := eventstore.NewSQLiteStore(filepath.Join(f.options.Target, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e, found, err := s.GetByID(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("missing Event: %v", err)
	}
	return e
}
