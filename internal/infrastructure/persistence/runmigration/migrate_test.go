package runmigration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	super "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	tasks "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func migrationFixture(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if e := os.Mkdir(snapshot, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(snapshot, "superagent.sqlite")
	s, e := super.NewSQLiteStore(p, 0)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := openDB(p)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	m := map[string]any{"run_id": "historical-run-exact", "actor_id": "lumina", "agent_type": "LeadAgent", "status": "completed", "started_at": now, "completed_at": now.Add(time.Minute), "goal": "historical content", "summary": "preserve this"}
	raw, _ := json.Marshal(m)
	if _, e = db.Exec("INSERT INTO agent_run(run_id,started_at,payload) VALUES(?,?,?)", "historical-run-exact", now.Format(time.RFC3339Nano), string(raw)); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	inv := Inventory{SchemaVersion: Schema, SnapshotAt: now.Add(time.Hour), Files: map[string]string{"superagent.sqlite": digest(b)}, Roles: map[string]string{}}
	for _, role := range roles {
		inv.Roles[role] = ""
	}
	inv.Roles["superagent"] = "superagent.sqlite"
	return Options{Snapshot: snapshot, Target: filepath.Join(root, "cohort"), Mode: "dry-run", Inventory: inv}
}

func TestMigrationPublishFailureReturnsBlockedReceipt(t *testing.T) {
	o := migrationFixture(t)
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(o.Target, []byte("unrelated output"), 0600); err != nil {
		t.Fatal(err)
	}
	o.Mode, o.Expected = "apply", &r
	failed, err := Run(context.Background(), o)
	if err == nil || failed.Status != "blocked" || failed.ErrorCode != "migration_rejected" {
		t.Fatalf("failure receipt: %#v %v", failed, err)
	}
	b, err := os.ReadFile(o.Target)
	if err != nil || string(b) != "unrelated output" {
		t.Fatalf("existing target changed: %q %v", b, err)
	}
}

func TestMigrationInterruptsRunningOwnerAndProjectionTogether(t *testing.T) {
	o := migrationFixture(t)
	p := filepath.Join(o.Snapshot, "superagent.sqlite")
	db, err := openDB(p)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = db.QueryRow("SELECT payload FROM agent_run").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["actor_id"] = "mio"
	fields["status"] = "running"
	delete(fields, "completed_at")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE agent_run SET payload=?", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	o.Inventory.Files["superagent.sqlite"] = digest(raw)
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	o.Mode, o.Expected = "apply", &r
	if _, err = Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	reader, err := tasks.NewJSONLReader(filepath.Join(o.Target, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	runs, err := reader.ListRuns(context.Background(), domain.RunFilter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v %v", runs, err)
	}
	if runs[0].Status != domain.RunStatusInterrupted || runs[0].CompletedAt == nil || !runs[0].CompletedAt.Equal(o.Inventory.SnapshotAt) {
		t.Fatalf("owner not interrupted: %#v", runs[0])
	}
	projection, err := super.NewSQLiteStore(filepath.Join(o.Target, "superagent.sqlite"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer projection.Close()
	items, err := projection.ListAgentRuns(context.Background(), 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("projection: %v %v", items, err)
	}
	if items[0].Status != "interrupted" || !items[0].CompletedAt.Equal(o.Inventory.SnapshotAt) {
		t.Fatalf("projection not interrupted: %#v", items[0])
	}
}

func TestMigrationDryRunApplyNoopAndOwnerReload(t *testing.T) {
	o := migrationFixture(t)
	ctx := context.Background()
	r, e := Run(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	if r.Status != "ready" || r.Counts["tasks"] != 1 || r.Counts["runs"] != 1 {
		t.Fatalf("receipt %#v", r)
	}
	if _, e = os.Stat(o.Target); !os.IsNotExist(e) {
		t.Fatal("dry-run published target")
	}
	o.Mode = "apply"
	o.Expected = &r
	a, e := Run(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	if a.Status != "applied" {
		t.Fatalf("apply status %s", a.Status)
	}
	again, e := Run(ctx, o)
	if e != nil || again.Status != "noop" {
		t.Fatalf("second apply %s %v", again.Status, e)
	}
	reader, e := tasks.NewJSONLReader(filepath.Join(o.Target, "tasks"))
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	rs, e := reader.ListRuns(ctx, domain.RunFilter{})
	if e != nil || len(rs) != 1 {
		t.Fatalf("canonical owner reload: %v %v", rs, e)
	}
	if rs[0].Assignee != "lumina" || rs[0].Status != domain.RunStatusSucceeded {
		t.Fatal("historical ownership changed")
	}
	projection, e := super.NewSQLiteStore(filepath.Join(o.Target, "superagent.sqlite"), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer projection.Close()
	items, e := projection.ListAgentRuns(ctx, 0)
	if e != nil || len(items) != 1 {
		t.Fatalf("projection reload: %v", e)
	}
	if items[0].Summary != "preserve this" || items[0].TaskID != rs[0].TaskID || items[0].RunID != rs[0].RunID {
		t.Fatal("projection mismatch or content changed")
	}
}

func TestMigrationRejectsReceiptDriftAndMissingRole(t *testing.T) {
	o := migrationFixture(t)
	r, e := Run(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	r.Counts["tasks"]++
	o.Mode = "apply"
	o.Expected = &r
	if _, e = Run(context.Background(), o); e == nil {
		t.Fatal("tampered receipt accepted")
	}
	if _, e = os.Stat(o.Target); !os.IsNotExist(e) {
		t.Fatal("failed apply published target")
	}
	delete(o.Inventory.Roles, "browser")
	if _, e = Run(context.Background(), o); e == nil {
		t.Fatal("unaccounted role accepted")
	}
}

func TestMigrationRejectsUnknownPayloadAndMissingAttribution(t *testing.T) {
	for _, kind := range []string{"unknown", "unattributed"} {
		t.Run(kind, func(t *testing.T) {
			o := migrationFixture(t)
			p := filepath.Join(o.Snapshot, "superagent.sqlite")
			db, e := openDB(p)
			if e != nil {
				t.Fatal(e)
			}
			var raw string
			if e = db.QueryRow("SELECT payload FROM agent_run").Scan(&raw); e != nil {
				t.Fatal(e)
			}
			var m map[string]any
			_ = json.Unmarshal([]byte(raw), &m)
			if kind == "unknown" {
				m["surprise_identity"] = "not allowed"
			} else {
				delete(m, "actor_id")
			}
			b, _ := json.Marshal(m)
			if _, e = db.Exec("UPDATE agent_run SET payload=?", string(b)); e != nil {
				t.Fatal(e)
			}
			_ = db.Close()
			b, _ = os.ReadFile(p)
			o.Inventory.Files["superagent.sqlite"] = digest(b)
			if _, e = Run(context.Background(), o); e == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}

func TestMigrationRejectsDuplicateJSONFields(t *testing.T) {
	var v map[string]any
	if err := strictJSON([]byte(`{"actor_id":"mio","actor_id":"lumina"}`), &v); err == nil {
		t.Fatal("duplicate identity key accepted")
	}
}
