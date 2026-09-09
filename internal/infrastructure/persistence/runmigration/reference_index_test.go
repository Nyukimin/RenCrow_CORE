package runmigration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	super "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMigrationRebuildsMissingSuperagentReferenceIndex(t *testing.T) {
	for _, table := range []string{"context_pack", "subagent_task"} {
		t.Run(table, func(t *testing.T) {
			f := newFullCohortFixture(t)
			addReferenceContextFixture(t, &f)
			db := openMigrationFixtureDB(t, filepath.Join(f.options.Snapshot, "superagent.sqlite"))
			query := "UPDATE context_pack SET run_id = ''"
			if table == "subagent_task" {
				query = "UPDATE subagent_task SET parent_run_id = NULL"
			}
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			closeMigrationFixtureDB(t, db)
			refreshFixtureHash(t, &f.options, "superagent.sqlite")
			r, err := Run(context.Background(), f.options)
			if err != nil {
				t.Fatal(err)
			}
			f.options.Mode, f.options.Expected = "apply", &r
			if _, err = Run(context.Background(), f.options); err != nil {
				t.Fatal(err)
			}
			assertCanonicalOwnerReload(t, f, context.Background())
		})
	}
}

func TestMigrationRejectsConflictingSuperagentReferenceIndex(t *testing.T) {
	f := newFullCohortFixture(t)
	addReferenceContextFixture(t, &f)
	db := openMigrationFixtureDB(t, filepath.Join(f.options.Snapshot, "superagent.sqlite"))
	if _, err := db.Exec("UPDATE context_pack SET run_id = 'different-run'"); err != nil {
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "superagent.sqlite")
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("conflicting populated index accepted")
	}
}

func addReferenceContextFixture(t *testing.T, f *fullCohortFixture) {
	t.Helper()
	db := openMigrationFixtureDB(t, filepath.Join(f.options.Snapshot, "superagent.sqlite"))
	item := super.ContextPack{ArtifactID: core.NewArtifactID(), Kind: core.ArtifactKindContextPack, TaskID: f.taskID, RunID: f.runID, Summary: "context fixture", CreatedAt: f.options.Inventory.SnapshotAt.Add(-time.Minute)}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO context_pack(artifact_id, run_id, created_at, payload) VALUES (?, ?, ?, ?)", string(item.ArtifactID), string(item.RunID), item.CreatedAt.Format(time.RFC3339Nano), string(b)); err != nil {
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "superagent.sqlite")
}
