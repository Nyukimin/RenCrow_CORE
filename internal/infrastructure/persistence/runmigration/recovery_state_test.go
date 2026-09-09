package runmigration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationLegacyRecoveryBlockIsClosedAtSnapshot(t *testing.T) {
	f := newFullCohortFixture(t)
	setLegacyRecoveryFixture(t, &f, "restart resume blocked: durable checkpoint is unavailable")
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
	if err = db.QueryRow("SELECT payload FROM agent_run").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if textField(m, "status") != "interrupted" || !timeField(m, "completed_at").Equal(f.options.Inventory.SnapshotAt) {
		t.Fatal("legacy recovery state was not closed at the explicit migration boundary")
	}
}

func TestMigrationLegacyRecoveryRejectsUnknownMissingEnd(t *testing.T) {
	f := newFullCohortFixture(t)
	setLegacyRecoveryFixture(t, &f, "unknown blocked reason")
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("unknown completion time fabricated")
	}
}

func setLegacyRecoveryFixture(t *testing.T, f *fullCohortFixture, summary string) {
	t.Helper()
	db := openMigrationFixtureDB(t, filepath.Join(f.options.Snapshot, "superagent.sqlite"))
	var raw string
	if err := db.QueryRow("SELECT payload FROM agent_run").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "task_id")
	setField(m, "run_id", "run_lead_recovery_fixture")
	setField(m, "actor_id", "lumina")
	setField(m, "agent_type", "LeadAgent")
	setField(m, "status", "blocked")
	setField(m, "summary", summary)
	setField(m, "completed_at", time.Time{})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE agent_run SET run_id = ?, payload = ?", "run_lead_recovery_fixture", string(b)); err != nil {
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "superagent.sqlite")
}
