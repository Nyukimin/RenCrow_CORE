package runmigration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	super "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMigrationLegacyQueueReferenceUsesExactTrace(t *testing.T) {
	f := newFullCohortFixture(t)
	id := addLegacyQueueReferenceFixture(t, &f, false)
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
	if err = db.QueryRow("SELECT payload FROM run_queue WHERE queue_item_id = ?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var item super.RunQueueItem
	if err = json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	if item.TaskID != f.taskID || item.RunID != f.runID || item.RunStartReason != "first" {
		t.Fatal("queue was not bound to its proven owner Run")
	}
}

func TestMigrationLegacyQueueReferenceRestoresExplicitEmptyRunReference(t *testing.T) {
	f := newFullCohortFixture(t)
	queueID := addLegacyQueueReferenceFixture(t, &f, false)
	path := filepath.Join(f.options.Snapshot, "events.sqlite")
	db := openMigrationFixtureDB(t, path)
	var eventID, raw string
	if err := db.QueryRow(`SELECT event_id, envelope_json FROM event_envelope WHERE event_type = 'run_queue.claimed'`).Scan(&eventID, &raw); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(envelope["payload"], &payload); err != nil {
		db.Close()
		t.Fatal(err)
	}
	payload["run_reference"] = json.RawMessage(`""`)
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	envelope["payload"] = encodedPayload
	mutated, err := json.Marshal(envelope)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE event_envelope SET envelope_json = ? WHERE event_id = ?`, string(mutated), eventID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "events.sqlite")

	r, err := Run(context.Background(), f.options)
	if err != nil || r.Status != "ready" {
		t.Fatalf("empty run_reference receipt = %#v err=%v", r, err)
	}
	f.options.Mode, f.options.Expected = "apply", &r
	if applied, err := Run(context.Background(), f.options); err != nil || applied.Status != "applied" {
		t.Fatalf("empty run_reference apply = %#v err=%v", applied, err)
	}
	out := openMigrationFixtureDB(t, filepath.Join(f.options.Target, "superagent.sqlite"))
	defer out.Close()
	var queuePayload string
	if err := out.QueryRow(`SELECT payload FROM run_queue WHERE queue_item_id = ?`, queueID).Scan(&queuePayload); err != nil {
		t.Fatal(err)
	}
	var item super.RunQueueItem
	if err := json.Unmarshal([]byte(queuePayload), &item); err != nil {
		t.Fatal(err)
	}
	if item.RunID != f.runID || item.TaskID != f.taskID {
		t.Fatalf("queue owner = %#v", item)
	}
}

func TestMigrationLegacyQueueReferenceRejectsAmbiguousTrace(t *testing.T) {
	f := newFullCohortFixture(t)
	addLegacyQueueReferenceFixture(t, &f, true)
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("ambiguous trace accepted")
	}
}

func TestMigrationLegacyQueueReferenceIgnoresUnrelatedMultiRunTrace(t *testing.T) {
	f := newFullCohortFixture(t)
	addLegacyQueueReferenceFixture(t, &f, false)
	_, successor := addCanonicalAgentRun(t, &f)
	store, err := eventstore.NewSQLiteStore(filepath.Join(f.options.Snapshot, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	at := f.options.Inventory.SnapshotAt.Add(-time.Minute)
	first := core.NewRootEventEnvelope("superagent", "lead_agent.started", at, map[string]any{"run_reference": string(f.runID)})
	next := core.NewRootEventEnvelope("superagent", "lead_agent.started", at, map[string]any{"run_reference": string(successor)})
	next.TraceID = first.TraceID
	if err = store.AppendBatch(context.Background(), []core.EventEnvelope{first, next}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &f.options, "events.sqlite")
	if _, err = Run(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
}

func addLegacyQueueReferenceFixture(t *testing.T, f *fullCohortFixture, ambiguous bool) string {
	t.Helper()
	const old = "original-source-queue"
	id, err := core.NewMigrationID(core.CanonicalQueueItemID, "run_queue", "queue_item_id", old)
	if err != nil {
		t.Fatal(err)
	}
	at := f.options.Inventory.SnapshotAt.Add(-time.Minute)
	addCheckpointQueue(t, f, super.RunQueueItem{QueueItemID: core.QueueItemID(id), Goal: "queue fixture", Action: "chat", Status: "completed", CreatedAt: at, CompletedAt: at})
	store, err := eventstore.NewSQLiteStore(filepath.Join(f.options.Snapshot, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	q := core.NewRootEventEnvelope("superagent", "run_queue.claimed", at, map[string]any{"queue_reference": old})
	r := core.NewRootEventEnvelope("superagent", "lead_agent.started", at, map[string]any{"run_reference": string(f.runID)})
	r.TraceID = q.TraceID
	events := []core.EventEnvelope{q, r}
	if ambiguous {
		x := core.NewRootEventEnvelope("superagent", "lead_agent.completed", at, map[string]any{"run_reference": "different-source-run"})
		x.TraceID = q.TraceID
		events = append(events, x)
	}
	if err = store.AppendBatch(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &f.options, "events.sqlite")
	return id
}
