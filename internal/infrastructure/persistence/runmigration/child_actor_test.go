package runmigration

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationLegacyChildActorFromRecordedIdentity(t *testing.T) {
	f := newFullCohortFixture(t)
	// The parent is Shiro: the child's recorded actor must not be inherited.
	setLegacyChildActorFixture(t, &f, "mio", 0, "Subagent")
	r, err := Run(context.Background(), f.options)
	if err != nil {
		t.Fatal(err)
	}
	f.options.Mode, f.options.Expected = "apply", &r
	if _, err = Run(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	db := openMigrationFixtureDB(t, filepath.Join(f.options.Target, "superagent.sqlite"))
	defer db.Close()
	var raw string
	if err = db.QueryRow("SELECT payload FROM subagent_task").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if textField(m, "actor_id") != "mio" || textField(m, "task_id") == "" || textField(m, "run_id") == "" {
		t.Fatalf("actor or ownership not restored: %s", raw)
	}
}

func TestMigrationLegacyChildActorRejectsUnprovenIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, actor, kind string
		offset            int64
	}{
		{"mechanism", "worker", "Subagent", 0},
		{"timestamp", "shiro", "Subagent", 1},
		{"wrong producer", "shiro", "LeadAgent", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFullCohortFixture(t)
			setLegacyChildActorFixture(t, &f, tc.actor, tc.offset, tc.kind)
			if _, err := Run(context.Background(), f.options); err == nil {
				t.Fatal("unproven child actor accepted")
			}
		})
	}
}

func setLegacyChildActorFixture(t *testing.T, f *fullCohortFixture, actor string, offset int64, kind string) {
	t.Helper()
	db := openMigrationFixtureDB(t, filepath.Join(f.options.Snapshot, "superagent.sqlite"))
	var raw string
	if err := db.QueryRow("SELECT payload FROM subagent_task").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	var created time.Time
	if err := json.Unmarshal(m["created_at"], &created); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("sub_%s_%d", actor, created.UnixNano()+offset)
	delete(m, "actor_id")
	setField(m, "agent_type", kind)
	setField(m, "subagent_id", id)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE subagent_task SET subagent_id = ?, payload = ?", id, string(b)); err != nil {
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "superagent.sqlite")
}
