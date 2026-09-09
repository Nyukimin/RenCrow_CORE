package runmigration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	task "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMigrationIdleHistoryUsesFinalProductionRevision(t *testing.T) {
	f := newFullCohortFixture(t)
	finished := writeLegacyDialogueHistory(t, &f, "ready", "shiro", 2)
	path := filepath.Join(f.options.Snapshot, "dialogue.jsonl")
	history, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var repeated map[string]json.RawMessage
	if err = json.Unmarshal(jsonLines(history)[1], &repeated); err != nil {
		t.Fatal(err)
	}
	setField(repeated, "updated_at", f.options.Inventory.SnapshotAt)
	if err = os.WriteFile(path, append(history, marshalLine(repeated)...), 0600); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &f.options, "dialogue.jsonl")
	receipt, err := Run(context.Background(), f.options)
	if err != nil {
		t.Fatal(err)
	}
	f.options.Mode, f.options.Expected = "apply", &receipt
	if _, err = Run(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.options.Target, "tasks/task_run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := core.NewMigrationID(core.CanonicalRunID, "idlechat", "generation_id", "old-dialogue-history")
	found := false
	for _, line := range jsonLines(raw) {
		var run task.Run
		if err = json.Unmarshal(line, &run); err != nil {
			t.Fatal(err)
		}
		if string(run.RunID) != id {
			continue
		}
		found = true
		if run.Status != task.RunStatusSucceeded || run.CompletedAt == nil || !run.CompletedAt.Equal(finished) {
			t.Fatalf("final production state lost: %#v", run)
		}
	}
	if !found {
		t.Fatal("missing migrated Run")
	}
	raw, err = os.ReadFile(filepath.Join(f.options.Target, "dialogue.jsonl"))
	if err != nil || len(jsonLines(raw)) != 3 {
		t.Fatalf("history lost: %v", err)
	}
}

func TestMigrationIdleHistoryRejectsOwnershipOrRevisionConflict(t *testing.T) {
	for _, tc := range []struct {
		name, actor string
		revision    int
	}{
		{"actor change", "mio", 2}, {"same revision status change", "shiro", 1}, {"revision regression", "shiro", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFullCohortFixture(t)
			writeLegacyDialogueHistory(t, &f, "ready", tc.actor, tc.revision)
			if _, err := Run(context.Background(), f.options); err == nil {
				t.Fatal("conflicting history accepted")
			}
		})
	}
}

func writeLegacyDialogueHistory(t *testing.T, f *fullCohortFixture, status, actor string, revision int) time.Time {
	t.Helper()
	path := filepath.Join(f.options.Snapshot, "dialogue.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "task_id")
	delete(m, "run_id")
	setField(m, "generation_id", "old-dialogue-history")
	setField(m, "production_status", "needs_repair")
	setField(m, "revision", 1)
	first := marshalLine(m)
	finished := f.options.Inventory.SnapshotAt.Add(-time.Second)
	setField(m, "production_status", status)
	setField(m, "revision", revision)
	setField(m, "initiated_by", actor)
	setField(m, "updated_at", finished)
	if err = os.WriteFile(path, append(first, marshalLine(m)...), 0600); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &f.options, "dialogue.jsonl")
	return finished
}
