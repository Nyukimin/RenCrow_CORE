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
	store, err := eventstore.NewSQLiteStore(filepath.Join(f.options.Snapshot, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	at := f.options.Inventory.SnapshotAt.Add(-time.Minute)
	first := core.NewRootEventEnvelope("superagent", "lead_agent.started", at, map[string]any{"run_reference": "unrelated-first"})
	next := core.NewRootEventEnvelope("superagent", "lead_agent.started", at, map[string]any{"run_reference": "unrelated-successor"})
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
