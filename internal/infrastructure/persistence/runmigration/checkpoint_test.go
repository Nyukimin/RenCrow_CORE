package runmigration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	superdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	taskdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	superstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMigrationCheckpointConversionFullCohortDryRunApplyNoopAndOwnerReload(t *testing.T) {
	f := newFullCohortFixture(t)
	at := f.options.Inventory.SnapshotAt.Add(-30 * time.Minute)
	const revision = 7
	const summary = "checkpoint summary"
	const nextAction = "continue with the next step"
	updateCheckpointAgentRun(t, &f, func(m map[string]any) {
		m["resume_policy"] = "checkpoint"
		m["checkpoint_revision"] = revision
		m["checkpoint_summary"] = summary
		m["next_action"] = nextAction
		m["last_checkpoint_at"] = at
	})
	queueCheckpoint := core.NewCheckpointID()
	addCheckpointQueue(t, &f, superdomain.RunQueueItem{
		QueueItemID:        core.NewQueueItemID(),
		TaskID:             f.taskID,
		RunID:              f.runID,
		RunStartReason:     taskdomain.RunStartReasonCheckpointResume,
		Goal:               "preserve agent prose",
		Action:             "resume",
		Status:             "completed",
		CheckpointID:       queueCheckpoint,
		CheckpointRevision: revision,
		CheckpointSummary:  summary,
		NextAction:         nextAction,
		CreatedAt:          at,
		CompletedAt:        f.options.Inventory.SnapshotAt.Add(-time.Minute),
	})

	first, err := Run(context.Background(), f.options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ready" || first.Counts["mapped_checkpoints"] != 1 {
		t.Fatalf("dry-run receipt = %#v", first)
	}
	if _, err := os.Stat(f.options.Target); !os.IsNotExist(err) {
		t.Fatalf("dry-run published target: %v", err)
	}

	apply := f.options
	apply.Mode = "apply"
	apply.Expected = &first
	applied, err := Run(context.Background(), apply)
	if err != nil || applied.Status != "applied" {
		t.Fatalf("apply receipt = %#v err=%v", applied, err)
	}
	noop, err := Run(context.Background(), apply)
	if err != nil || noop.Status != "noop" {
		t.Fatalf("noop receipt = %#v err=%v", noop, err)
	}

	assertCanonicalOwnerReload(t, f, context.Background())
	store, err := superstore.NewSQLiteStore(filepath.Join(f.options.Target, "superagent.sqlite"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.ListAgentRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].CheckpointID != queueCheckpoint || runs[0].CheckpointRevision != revision || runs[0].CheckpointSummary != summary || runs[0].NextAction != nextAction {
		t.Fatalf("converted agent run = %#v err=%v", runs, err)
	}
}

func TestMigrationCheckpointRejectsMissingMetadata(t *testing.T) {
	f := newFullCohortFixture(t)
	updateCheckpointAgentRun(t, &f, func(m map[string]any) {
		m["resume_policy"] = "checkpoint"
		m["checkpoint_revision"] = 1
	})
	if receipt, err := Run(context.Background(), f.options); err == nil || receipt.Status != "blocked" {
		t.Fatalf("missing checkpoint metadata accepted: %#v err=%v", receipt, err)
	}
}

func TestMigrationCheckpointPreservesExistingIDWithoutResumePolicy(t *testing.T) {
	f := newFullCohortFixture(t)
	checkpointID := core.NewCheckpointID()
	at := f.options.Inventory.SnapshotAt.Add(-30 * time.Minute)
	updateCheckpointAgentRun(t, &f, func(m map[string]any) {
		delete(m, "resume_policy")
		m["checkpoint_id"] = string(checkpointID)
		m["checkpoint_revision"] = 4
		m["checkpoint_summary"] = "existing checkpoint"
		m["next_action"] = "existing next action"
		m["last_checkpoint_at"] = at
	})
	first, err := Run(context.Background(), f.options)
	if err != nil || first.Status != "ready" || first.Counts["mapped_checkpoints"] != 0 {
		t.Fatalf("existing checkpoint receipt = %#v err=%v", first, err)
	}
	apply := f.options
	apply.Mode = "apply"
	apply.Expected = &first
	if applied, err := Run(context.Background(), apply); err != nil || applied.Status != "applied" {
		t.Fatalf("existing checkpoint apply = %#v err=%v", applied, err)
	}
	payload := readAgentRunPayload(t, filepath.Join(f.options.Target, "superagent.sqlite"), string(f.runID))
	if got := string(payload["checkpoint_id"]); got != `"`+string(checkpointID)+`"` {
		t.Fatalf("checkpoint ID changed: %s", got)
	}
}

func TestMigrationCheckpointRejectsQueueConflictOrAmbiguity(t *testing.T) {
	tests := []struct {
		name string
		make func(*testing.T, *fullCohortFixture, time.Time, core.CheckpointID)
	}{
		{
			name: "metadata mismatch",
			make: func(t *testing.T, f *fullCohortFixture, at time.Time, checkpointID core.CheckpointID) {
				addCheckpointQueue(t, f, checkpointItem(f, at, checkpointID, "different summary", "next action"))
			},
		},
		{
			name: "ambiguous references",
			make: func(t *testing.T, f *fullCohortFixture, at time.Time, checkpointID core.CheckpointID) {
				addCheckpointQueue(t, f, checkpointItem(f, at, checkpointID, "checkpoint summary", "next action"))
				addCheckpointQueue(t, f, checkpointItem(f, at, core.NewCheckpointID(), "checkpoint summary", "next action"))
			},
		},
		{
			name: "explicit ID conflict",
			make: func(t *testing.T, f *fullCohortFixture, at time.Time, checkpointID core.CheckpointID) {
				updateCheckpointAgentRun(t, f, func(m map[string]any) { m["checkpoint_id"] = string(checkpointID) })
				addCheckpointQueue(t, f, checkpointItem(f, at, core.NewCheckpointID(), "checkpoint summary", "next action"))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFullCohortFixture(t)
			at := f.options.Inventory.SnapshotAt.Add(-30 * time.Minute)
			updateCheckpointAgentRun(t, &f, func(m map[string]any) {
				m["resume_policy"] = "checkpoint"
				m["checkpoint_revision"] = 3
				m["checkpoint_summary"] = "checkpoint summary"
				m["next_action"] = "next action"
				m["last_checkpoint_at"] = at
			})
			tc.make(t, &f, at, core.NewCheckpointID())
			if receipt, err := Run(context.Background(), f.options); err == nil || receipt.Status != "blocked" {
				t.Fatalf("queue conflict accepted: %#v err=%v", receipt, err)
			}
		})
	}
}

func TestMigrationCheckpointRejectsSharingAcrossTasks(t *testing.T) {
	f := newFullCohortFixture(t)
	_, secondRun := addCanonicalAgentRun(t, &f)
	checkpointID := core.NewCheckpointID()
	at := f.options.Inventory.SnapshotAt.Add(-30 * time.Minute)
	updateCheckpointAgentRun(t, &f, func(m map[string]any) {
		m["resume_policy"] = "checkpoint"
		m["checkpoint_revision"] = 2
		m["checkpoint_summary"] = "shared checkpoint"
		m["next_action"] = "continue"
		m["last_checkpoint_at"] = at
		m["checkpoint_id"] = string(checkpointID)
	})
	updateAgentRunPayloadByRun(t, &f, string(secondRun), func(m map[string]any) {
		m["resume_policy"] = "checkpoint"
		m["checkpoint_revision"] = 2
		m["checkpoint_summary"] = "shared checkpoint"
		m["next_action"] = "continue"
		m["last_checkpoint_at"] = at
		m["checkpoint_id"] = string(checkpointID)
	})
	if receipt, err := Run(context.Background(), f.options); err == nil || receipt.Status != "blocked" {
		t.Fatalf("shared checkpoint ID accepted: %#v err=%v", receipt, err)
	}
}

func TestCheckpointOwnershipAllowsSuccessorRunsOfSameTask(t *testing.T) {
	taskID, first, next := core.NewTaskID(), core.NewRunID(), core.NewRunID()
	c := &cohort{
		runs:    map[core.RunID]taskdomain.Run{first: {RunID: first, TaskID: taskID}, next: {RunID: next, TaskID: taskID}},
		rawRuns: map[string][]core.RunID{string(first): {first}, string(next): {next}},
	}
	owners := map[string]string{}
	checkpoint := string(core.NewCheckpointID())
	if err := c.registerCheckpointOwner(owners, checkpoint, string(first)); err != nil {
		t.Fatal(err)
	}
	if err := c.registerCheckpointOwner(owners, checkpoint, string(next)); err != nil {
		t.Fatal("legitimate same-Task checkpoint resume rejected:", err)
	}
}

func TestMigrationCheckpointIndependentRowsDoNotAlias(t *testing.T) {
	f := newFullCohortFixture(t)
	_, secondRun := addCanonicalAgentRun(t, &f)
	at := f.options.Inventory.SnapshotAt.Add(-30 * time.Minute)
	set := func(m map[string]any) {
		m["resume_policy"] = "checkpoint"
		m["checkpoint_revision"] = 2
		m["checkpoint_summary"] = "independent checkpoint"
		m["next_action"] = "continue"
		m["last_checkpoint_at"] = at
		delete(m, "checkpoint_id")
	}
	updateCheckpointAgentRun(t, &f, set)
	updateAgentRunPayloadByRun(t, &f, string(secondRun), set)
	first, err := Run(context.Background(), f.options)
	if err != nil || first.Status != "ready" || first.Counts["mapped_checkpoints"] != 2 {
		t.Fatalf("independent checkpoint receipt = %#v err=%v", first, err)
	}
	apply := f.options
	apply.Mode = "apply"
	apply.Expected = &first
	if applied, err := Run(context.Background(), apply); err != nil || applied.Status != "applied" {
		t.Fatalf("independent checkpoint apply = %#v err=%v", applied, err)
	}
	store, err := superstore.NewSQLiteStore(filepath.Join(f.options.Target, "superagent.sqlite"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.ListAgentRuns(context.Background(), 10)
	if err != nil || len(runs) != 2 || runs[0].CheckpointID == runs[1].CheckpointID {
		t.Fatalf("independent checkpoint IDs = %#v err=%v", runs, err)
	}
}

func checkpointItem(f *fullCohortFixture, at time.Time, id core.CheckpointID, summary, next string) superdomain.RunQueueItem {
	return superdomain.RunQueueItem{
		QueueItemID: core.NewQueueItemID(), TaskID: f.taskID, RunID: f.runID,
		RunStartReason: taskdomain.RunStartReasonCheckpointResume, Goal: "preserve agent prose", Action: "resume", Status: "completed",
		CheckpointID: id, CheckpointRevision: 3, CheckpointSummary: summary, NextAction: next,
		CreatedAt: at, CompletedAt: f.options.Inventory.SnapshotAt.Add(-time.Minute),
	}
}

func updateCheckpointAgentRun(t *testing.T, f *fullCohortFixture, mutate func(map[string]any)) {
	updateAgentRunPayloadByRun(t, f, string(f.runID), mutate)
}

func updateAgentRunPayloadByRun(t *testing.T, f *fullCohortFixture, runID string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(f.options.Snapshot, "superagent.sqlite")
	db := openMigrationFixtureDB(t, path)
	var raw []byte
	if err := db.QueryRow(`SELECT payload FROM agent_run WHERE run_id = ?`, runID).Scan(&raw); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	mutate(payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_run SET payload = ? WHERE run_id = ?`, string(encoded), runID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "superagent.sqlite")
}

func addCheckpointQueue(t *testing.T, f *fullCohortFixture, item superdomain.RunQueueItem) {
	t.Helper()
	path := filepath.Join(f.options.Snapshot, "superagent.sqlite")
	db := openMigrationFixtureDB(t, path)
	encoded, err := json.Marshal(item)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO run_queue (queue_item_id, status, created_at, payload) VALUES (?, ?, ?, ?)`, string(item.QueueItemID), item.Status, item.CreatedAt.Format(time.RFC3339Nano), string(encoded))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "superagent.sqlite")
}

func addCanonicalAgentRun(t *testing.T, f *fullCohortFixture) (core.TaskID, core.RunID) {
	t.Helper()
	taskID, runID := core.NewTaskID(), core.NewRunID()
	started := f.options.Inventory.SnapshotAt.Add(-2 * time.Hour)
	completed := started.Add(time.Minute)
	task := taskdomain.Task{TaskID: taskID, Title: "Second fixture task", Route: taskdomain.RouteGeneral, Assignee: "Shiro", Status: taskdomain.StatusSucceeded, Priority: taskdomain.PriorityNormal, InterruptPolicy: taskdomain.InterruptSilent, CreatedAt: started, UpdatedAt: completed, StartedAt: &started, FinishedAt: &completed}
	run := taskdomain.Run{RunID: runID, TaskID: taskID, StartReason: taskdomain.RunStartReasonFirst, Assignee: "Shiro", Status: taskdomain.RunStatusSucceeded, StartedAt: started, CompletedAt: &completed}
	for path, value := range map[string]string{"tasks/task_state.jsonl": string(marshalLine(task)), "tasks/task_run.jsonl": string(marshalLine(run))} {
		fullPath := filepath.Join(f.options.Snapshot, filepath.FromSlash(path))
		old, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, append(old, value...), 0o600); err != nil {
			t.Fatal(err)
		}
		refreshFixtureHash(t, &f.options, path)
	}
	db := openMigrationFixtureDB(t, filepath.Join(f.options.Snapshot, "superagent.sqlite"))
	payload, err := json.Marshal(superdomain.AgentRun{RunID: runID, TaskID: taskID, ActorID: "shiro", Goal: "second agent run", Status: "completed", StartedAt: started, CompletedAt: completed})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_run(run_id, started_at, payload) VALUES (?, ?, ?)`, string(runID), started.Format(time.RFC3339Nano), string(payload)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "superagent.sqlite")
	return taskID, runID
}

func readAgentRunPayload(t *testing.T, path, runID string) map[string]json.RawMessage {
	t.Helper()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err := db.QueryRow(`SELECT payload FROM agent_run WHERE run_id = ?`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
