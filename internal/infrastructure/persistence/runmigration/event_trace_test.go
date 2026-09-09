package runmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	identitymigration "github.com/Nyukimin/RenCrow_CORE/internal/application/identitymigration"
	taskdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	legacyTraceFixturePath = "evidence/trace_events.jsonl"
	legacyTraceParentRun   = "legacy-trace-parent-run-20260810"
	legacyTraceChildID     = "sub_shiro_1786359610123456789"
	legacyTraceLeadStart   = "legacy-lead-start-20260810"
	legacyTraceLeadEnd     = "legacy-lead-end-20260810"
	legacyTraceChildStart  = "evt_subagent_started_sub_shiro_1786359610123456789_1786359610123456789"
	legacyTraceChildEnd    = "evt_subagent_completed_sub_shiro_1786359610123456789_1786359650123456789"
)

type legacyTraceFixture struct {
	fullCohortFixture
	rows          []legacyTraceRow
	converted     identitymigration.EventMigrationResult
	parentRunID   core.RunID
	childTaskID   core.TaskID
	childRunID    core.RunID
	parentStartID core.EventID
	childStartID  core.EventID
	childEndID    core.EventID
}

func TestMigrationLegacyTraceEvidenceRestoresChildOwnerAndParent(t *testing.T) {
	f := newLegacyTraceFixture(t)
	ctx := context.Background()

	dry, err := Run(ctx, f.options)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Status != "ready" || dry.Counts["archived_trace_events"] != len(f.rows) || dry.Counts["archived_child_runs"] != 1 {
		t.Fatalf("dry-run receipt = %#v", dry)
	}
	if _, err := os.Stat(f.options.Target); !os.IsNotExist(err) {
		t.Fatalf("dry-run published target: %v", err)
	}

	f.options.Mode, f.options.Expected = "apply", &dry
	if applied, err := Run(ctx, f.options); err != nil || applied.Status != "applied" {
		t.Fatalf("apply = %#v, err=%v", applied, err)
	}

	reader, err := taskstore.NewJSONLReader(filepath.Join(f.options.Target, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tasks, err := reader.ListTasks(ctx, taskdomain.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := reader.ListRuns(ctx, taskdomain.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var childTask taskdomain.Task
	var childRun taskdomain.Run
	for _, item := range tasks {
		if item.TaskID == f.childTaskID {
			childTask = item
		}
	}
	for _, item := range runs {
		if item.RunID == f.childRunID {
			childRun = item
		}
	}
	if childTask.TaskID != f.childTaskID || childTask.Assignee != "Shiro" || childTask.ParentTaskID == "" {
		t.Fatalf("child Task owner/parent = %#v", childTask)
	}
	if childRun.RunID != f.childRunID || childRun.TaskID != f.childTaskID || childRun.Assignee != "Shiro" {
		t.Fatalf("child Run owner = %#v", childRun)
	}

	store, err := eventstore.NewSQLiteStore(filepath.Join(f.options.Target, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	parent, found, err := store.GetByID(ctx, f.parentStartID)
	if err != nil || !found {
		t.Fatalf("parent Event = %#v, found=%v, err=%v", parent, found, err)
	}
	child, found, err := store.GetByID(ctx, f.childStartID)
	if err != nil || !found {
		t.Fatalf("child Event = %#v, found=%v, err=%v", child, found, err)
	}
	if parent.RunID != f.parentRunID || parent.ActorID != "lumina" || parent.ActorKind != "agent" || parent.TaskID == "" {
		t.Fatalf("historical parent owner = %#v", parent)
	}
	if child.TaskID != f.childTaskID || child.RunID != f.childRunID || child.ActorID != "shiro" || child.ActorKind != "agent" {
		t.Fatalf("child Event owner = %#v", child)
	}
	if childTask.ParentTaskID != parent.TaskID || childTask.ParentTaskID == childTask.TaskID {
		t.Fatalf("child Task parent = %q, parent Event Task = %q", childTask.ParentTaskID, parent.TaskID)
	}
	if child.RunID == parent.RunID || child.TaskID == parent.TaskID {
		t.Fatal("child reused parent execution identity")
	}

	if _, err := os.Stat(filepath.Join(f.options.Target, filepath.FromSlash(legacyTraceFixturePath))); !os.IsNotExist(err) {
		t.Fatalf("archived source was published into runtime output: %v", err)
	}
}

func TestMigrationLegacyTraceEvidenceRejectsEnvelopeMismatch(t *testing.T) {
	f := newLegacyTraceFixture(t)
	rows := append([]legacyTraceRow(nil), f.rows...)
	rows[1].PayloadSummary = "altered after canonical Event capture"
	rewriteLegacyTraceFixture(t, &f, rows)

	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("altered archived source was accepted")
	}
}

func TestMigrationLegacyTraceEvidenceRejectsUnboundOrAliasedInventory(t *testing.T) {
	t.Run("missing source hash", func(t *testing.T) {
		f := newLegacyTraceFixture(t)
		delete(f.options.Inventory.Files, legacyTraceFixturePath)
		if _, err := Run(context.Background(), f.options); err == nil {
			t.Fatal("trace source without a manifest hash was accepted")
		}
	})

	t.Run("aliases events role", func(t *testing.T) {
		f := newLegacyTraceFixture(t)
		f.options.Inventory.LegacyTraceSource = f.options.Inventory.Roles["events"]
		if _, err := Run(context.Background(), f.options); err == nil {
			t.Fatal("trace source aliasing a role was accepted")
		}
	})
}

func TestMigrationLegacyTraceEvidenceRejectsMissingTerminalChild(t *testing.T) {
	f := newLegacyTraceFixture(t)
	rows := append([]legacyTraceRow(nil), f.rows[:3]...)
	rewriteLegacyTraceFixture(t, &f, rows)

	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("child execution without a terminal source row was accepted")
	}
}

func newLegacyTraceFixture(t *testing.T) legacyTraceFixture {
	t.Helper()
	f := legacyTraceFixture{fullCohortFixture: newFullCohortFixture(t)}
	leadStartedAt := time.Date(2026, 8, 10, 10, 59, 0, 987654321, time.UTC)
	childStartedAt := time.Date(2026, 8, 10, 11, 0, 10, 123456789, time.UTC)
	childCompletedAt := time.Date(2026, 8, 10, 11, 0, 50, 123456789, time.UTC)
	leadCompletedAt := time.Date(2026, 8, 10, 11, 1, 0, 987654321, time.UTC)
	f.rows = []legacyTraceRow{
		{EventID: legacyTraceLeadStart, RunID: legacyTraceParentRun, EventType: "lead_agent_started", Actor: "LeadAgent", Status: "running", CreatedAt: leadStartedAt, PayloadSummary: "legacy parent started"},
		{EventID: legacyTraceLeadEnd, RunID: legacyTraceParentRun, EventType: "lead_agent_completed", Actor: "LeadAgent", Status: "completed", CreatedAt: leadCompletedAt, PayloadSummary: "legacy parent completed", ParentEventID: legacyTraceLeadStart},
		{EventID: legacyTraceChildStart, RunID: legacyTraceParentRun, EventType: "subagent_started", Actor: "Subagent", Status: "running", CreatedAt: childStartedAt, PayloadSummary: "legacy child started", ParentEventID: legacyTraceLeadStart},
		{EventID: legacyTraceChildEnd, RunID: legacyTraceParentRun, EventType: "subagent_completed", Actor: "Subagent", Status: "completed", CreatedAt: childCompletedAt, PayloadSummary: "legacy child completed", ParentEventID: legacyTraceChildStart},
	}
	legacy := make([]identitymigration.LegacyEvent, 0, len(f.rows))
	for _, row := range f.rows {
		legacy = append(legacy, identitymigration.LegacyEvent{
			SourceTable: "trace_event", EventID: row.EventID, ParentEventID: row.ParentEventID,
			RunID: row.RunID, EventType: row.EventType, OccurredAt: row.CreatedAt,
			Payload: map[string]any{"actor": row.Actor, "status": row.Status, "payload_summary": row.PayloadSummary},
		})
	}
	converted, err := identitymigration.ConvertLegacyEvents("superagent", legacy)
	if err != nil {
		t.Fatal(err)
	}
	f.converted = converted
	oldParent := converted.Events[0].RunID
	parentRun, err := core.NewMigrationID(core.CanonicalRunID, "superagent.event_history", "run_id", string(oldParent))
	if err != nil {
		t.Fatal(err)
	}
	f.parentRunID = core.RunID(parentRun)
	childRun, err := core.NewMigrationID(core.CanonicalRunID, "superagent", "subagent_id", legacyTraceChildID)
	if err != nil {
		t.Fatal(err)
	}
	childTask, err := core.NewMigrationID(core.CanonicalTaskID, "superagent", "subagent_id", legacyTraceChildID)
	if err != nil {
		t.Fatal(err)
	}
	f.childRunID, f.childTaskID = core.RunID(childRun), core.TaskID(childTask)
	f.parentStartID = converted.Events[0].EventID
	f.childStartID = converted.Events[2].EventID
	f.childEndID = converted.Events[3].EventID

	store, err := eventstore.NewSQLiteStore(filepath.Join(f.options.Snapshot, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendBatch(context.Background(), converted.Events); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &f.options, "events.sqlite")

	var source bytes.Buffer
	for _, row := range f.rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		source.Write(line)
		source.WriteByte('\n')
	}
	writeMigrationTestFile(t, f.options.Snapshot, legacyTraceFixturePath, source.Bytes())
	f.options.Inventory.Files[legacyTraceFixturePath] = digest(source.Bytes())
	f.options.Inventory.LegacyTraceSource = legacyTraceFixturePath
	// The parent Event has a canonical migrated RunID; its historical actor is
	// explicit inventory evidence, while the child Actor comes from child_actor.go.
	f.options.Inventory.LegacyRunActors = map[string]string{string(oldParent): "lumina"}
	return f
}

func rewriteLegacyTraceFixture(t *testing.T, f *legacyTraceFixture, rows []legacyTraceRow) {
	t.Helper()
	var source bytes.Buffer
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		source.Write(line)
		source.WriteByte('\n')
	}
	writeMigrationTestFile(t, f.options.Snapshot, legacyTraceFixturePath, source.Bytes())
	f.options.Inventory.Files[legacyTraceFixturePath] = digest(source.Bytes())
}
