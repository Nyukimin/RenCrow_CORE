package worldprojection

import (
	"context"
	"errors"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// D-06: CORE's source epoch switch is an explicit, separately authorized
// action. It restarts the revision at 1, marks the record reconcile, and must
// never burn a revision in the old epoch.
func TestProjectorSwitchesSourceEpochWithAuthorizedReconcile(t *testing.T) {
	taskID := modulecore.NewTaskID()
	source := &fakeTaskRunSource{tasks: []domaintask.Task{activeTask(taskID, domaintask.StatusQueued)}}
	projector, outbox, revisions := newTestProjector(t, source)
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}

	projector.SourceEpoch = "core-epoch-2"
	projector.ReconcileEnabled = true
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(switch) error = %v", err)
	}
	if result.Enqueued != 1 {
		t.Fatalf("switch result = %+v", result)
	}
	if revisions.calls != 1 {
		t.Fatalf("revision allocations = %d, want only the first pass", revisions.calls)
	}
	record := findEnqueued(t, outbox, domainprojection.KindWorkSet, 2)
	if !record.Reconcile || record.SourceEpoch != "core-epoch-2" {
		t.Fatalf("switch record = reconcile %t epoch %q", record.Reconcile, record.SourceEpoch)
	}
	workSet, err := decodeWorkSetRecord(record)
	if err != nil {
		t.Fatalf("decodeWorkSetRecord() error = %v", err)
	}
	if workSet.SourceRevision != 1 || workSet.SourceEpoch != "core-epoch-2" {
		t.Fatalf("switch work set = epoch %q revision %d, want core-epoch-2 revision 1",
			workSet.SourceEpoch, workSet.SourceRevision)
	}

	// Revision 1 exists in both epochs, so the source epoch is part of the
	// record identity. Reusing the retired id would collide in the durable
	// outbox and the epoch switch could never be persisted.
	retired := findEnqueued(t, outbox, domainprojection.KindWorkSet, 1)
	if retired.ID == record.ID {
		t.Fatalf("epoch switch reused record id %q", record.ID)
	}
	if record.ID != projectionRecordID(retired.Target, domainprojection.KindWorkSet, "core-epoch-2", 1) {
		t.Fatalf("switch record id = %q", record.ID)
	}
}

func TestProjectorRejectsSourceEpochSwitchWhenReconcileDisabled(t *testing.T) {
	taskID := modulecore.NewTaskID()
	source := &fakeTaskRunSource{tasks: []domaintask.Task{activeTask(taskID, domaintask.StatusQueued)}}
	projector, outbox, revisions := newTestProjector(t, source)
	revisions.epoch = "core-epoch-1"
	revisions.durable = true
	projector.SourceEpoch = "core-epoch-2"

	result, err := projector.Sync(context.Background())
	if !errors.Is(err, ErrSourceEpochSwitchNotAuthorized) {
		t.Fatalf("Sync() error = %v, want ErrSourceEpochSwitchNotAuthorized", err)
	}
	if result.Enqueued != 0 || len(outbox.enqueued) != 0 {
		t.Fatalf("result = %+v, enqueued = %d, want zero enqueue", result, len(outbox.enqueued))
	}
	if revisions.calls != 0 {
		t.Fatalf("revision allocations = %d, want zero revision burn", revisions.calls)
	}
}

// A pending record from the retired epoch must not suppress the switch forever,
// even when its content already matches the candidate snapshot.
func TestProjectorSourceEpochSwitchIgnoresStalePendingSameContent(t *testing.T) {
	taskID := modulecore.NewTaskID()
	source := &fakeTaskRunSource{tasks: []domaintask.Task{activeTask(taskID, domaintask.StatusQueued)}}
	projector, outbox, _ := newTestProjector(t, source)
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	outbox.latest = outbox.enqueued[0]
	outbox.hasLatest = true

	projector.SourceEpoch = "core-epoch-2"
	projector.ReconcileEnabled = true
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(switch) error = %v", err)
	}
	if result.Skipped != 0 || result.Enqueued != 1 {
		t.Fatalf("switch result = %+v, want one enqueue over a stale pending record", result)
	}
	record := findEnqueued(t, outbox, domainprojection.KindWorkSet, 2)
	if !record.Reconcile {
		t.Fatal("switch record is not marked reconcile")
	}
}

func TestProjectorSwitchesLifePolicySourceEpochWithAuthorizedReconcile(t *testing.T) {
	projector, outbox, revisions := newTestProjector(t, &fakeTaskRunSource{})
	projector.LifePolicySource = &fakeLifePolicySource{snapshot: LifePolicySnapshot{
		PersonaRevision:                "persona-sha256:one",
		ConditionProfileRef:            "agent-profile-sha256:one",
		AllowedNonCognitiveActivityIDs: []string{"idle", "rest"},
		ExpiredFallbackActivityIDs:     []string{"idle"},
	}}
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}

	projector.SourceEpoch = "core-epoch-2"
	projector.ReconcileEnabled = true
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(switch) error = %v", err)
	}
	if result.Enqueued != 1 || result.LifePoliciesEnqueued != 1 {
		t.Fatalf("switch result = %+v", result)
	}
	if revisions.calls != 2 {
		t.Fatalf("revision allocations = %d, want only the first pass", revisions.calls)
	}
	record := findEnqueued(t, outbox, domainprojection.KindLifePolicy, 2)
	policy, err := decodeLifePolicyRecord(record)
	if err != nil {
		t.Fatalf("decodeLifePolicyRecord() error = %v", err)
	}
	if !record.Reconcile || policy.SourceEpoch != "core-epoch-2" || policy.SourceRevision != 1 {
		t.Fatalf("switch life policy = reconcile %t epoch %q revision %d",
			record.Reconcile, policy.SourceEpoch, policy.SourceRevision)
	}
}

// The durable counter is the only epoch authority, so an unused destination
// stays on the normal full-projection path.
func TestProjectorKeepsNormalProjectionWhenSourceEpochMatches(t *testing.T) {
	taskID := modulecore.NewTaskID()
	source := &fakeTaskRunSource{tasks: []domaintask.Task{activeTask(taskID, domaintask.StatusQueued)}}
	projector, outbox, revisions := newTestProjector(t, source)
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.Enqueued != 1 {
		t.Fatalf("result = %+v", result)
	}
	record := findEnqueued(t, outbox, domainprojection.KindWorkSet)
	if record.Reconcile {
		t.Fatal("first projection must not claim reconciliation")
	}
	if revisions.calls != 1 {
		t.Fatalf("revision allocations = %d", revisions.calls)
	}
}
