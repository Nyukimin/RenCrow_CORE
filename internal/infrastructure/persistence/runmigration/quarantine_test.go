package runmigration

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestQuarantineRetainsSourceAndExcludesOrphanEventDeterministically(t *testing.T) {
	fixture := newFullCohortFixture(t)
	mutateFixtureEvent(t, &fixture.options, fixture.eventID, core.NewTaskID(), false)
	sourcePath := filepath.Join(fixture.options.Snapshot, "events.sqlite")
	sourceBefore, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Run(context.Background(), fixture.options); err == nil {
		t.Fatal("normal dry-run accepted orphan event")
	}

	quarantine := fixture.options
	quarantine.Mode = "quarantine"
	first, err := Run(context.Background(), quarantine)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "quarantined" || first.QuarantineMarker != "retain_and_quarantine/v1" {
		t.Fatalf("quarantine receipt = %#v", first)
	}
	if first.Counts["quarantined_events"] != 1 || first.Counts["quarantined_task_ids"] != 1 {
		t.Fatalf("quarantine counts = %#v", first.Counts)
	}
	if _, err := os.Stat(quarantine.Target); !os.IsNotExist(err) {
		t.Fatalf("quarantine plan published output: %v", err)
	}
	sourceAfter, err := os.ReadFile(sourcePath)
	if err != nil || !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatalf("source snapshot changed: %v", err)
	}

	apply := quarantine
	apply.Mode = "quarantine-apply"
	apply.Expected = &first
	applied, err := Run(context.Background(), apply)
	if err != nil || applied.Status != "applied" || applied.QuarantineMarker != "retain_and_quarantine/v1" {
		t.Fatalf("quarantine apply receipt = %#v err=%v", applied, err)
	}
	store, err := eventstore.NewSQLiteStore(filepath.Join(apply.Target, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, found, err := store.GetByID(context.Background(), fixture.eventID); err != nil || found {
		t.Fatalf("quarantined event in canonical output: found=%v err=%v", found, err)
	}

	second, err := Run(context.Background(), quarantine)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("quarantine receipts differ: first=%#v second=%#v", first, second)
	}
}

func TestQuarantineApplyRequiresExactQuarantineReceipt(t *testing.T) {
	fixture := newFullCohortFixture(t)
	mutateFixtureEvent(t, &fixture.options, fixture.eventID, core.NewTaskID(), false)
	quarantine := fixture.options
	quarantine.Mode = "quarantine"
	planned, err := Run(context.Background(), quarantine)
	if err != nil {
		t.Fatal(err)
	}

	apply := quarantine
	apply.Mode = "quarantine-apply"
	if _, err := Run(context.Background(), apply); err == nil {
		t.Fatal("quarantine apply accepted a missing receipt")
	}
	ready := planned
	ready.Status = "ready"
	apply.Expected = &ready
	if _, err := Run(context.Background(), apply); err == nil {
		t.Fatal("quarantine apply accepted a normal ready receipt")
	}
	tampered := planned
	tampered.Counts = cloneCounts(planned.Counts)
	tampered.Counts["quarantined_events"]++
	apply.Expected = &tampered
	if _, err := Run(context.Background(), apply); err == nil {
		t.Fatal("quarantine apply accepted a mismatched receipt")
	}
}

func cloneCounts(source map[string]int) map[string]int {
	cloned := make(map[string]int, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func TestQuarantineFailsClosedWhenKeptEventDependsOnQuarantinedEvent(t *testing.T) {
	fixture := newFullCohortFixture(t)
	path := filepath.Join(fixture.options.Snapshot, "events.sqlite")
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	parent, found, err := store.GetByID(context.Background(), fixture.eventID)
	if err != nil || !found {
		store.Close()
		t.Fatalf("fixture parent: found=%v err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	mutateFixtureEvent(t, &fixture.options, fixture.eventID, core.NewTaskID(), false)
	child := core.NewEventEnvelope(parent.TraceID, parent.EventID, nil, "fixture", "task.followup", parent.OccurredAt.Add(time.Second), map[string]any{"text": "depends on quarantined event"})
	store, err = eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), child); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &fixture.options, "events.sqlite")

	quarantine := fixture.options
	quarantine.Mode = "quarantine"
	if _, err := Run(context.Background(), quarantine); err == nil {
		t.Fatal("quarantine accepted a broken dependency graph")
	}
}
