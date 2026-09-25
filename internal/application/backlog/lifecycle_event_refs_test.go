package backlog

// Step18 s18_atlas_event_refs: maturation promotion, queue freeze, stage run and
// closure receipts must carry an EventID reference that is a real canonical
// EventID and never a re-labelled ReceiptID, ActionID or BacklogItemID.
// Replaying the same idempotency key must keep the very same EventID so the
// receipt keeps pointing at one transition instead of minting a new one.

import (
	"context"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func assertEventIDReference(t *testing.T, label string, eventID modulecore.EventID, nonIDEvents ...modulecore.EventID) {
	t.Helper()
	if eventID == "" {
		t.Fatalf("%s: receipt carries no EventID reference", label)
	}
	if err := eventID.Validate(); err != nil {
		t.Fatalf("%s: EventID reference %q is not a canonical EventID: %v", label, eventID, err)
	}
	for _, other := range nonIDEvents {
		if other != "" && other == eventID {
			t.Fatalf("%s: EventID reference %q duplicates another identity of the same receipt", label, eventID)
		}
	}
}

func TestStageRunReceiptReferencesItsOwnEventID(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		BacklogItemID:      modulecore.BacklogItemID("eventref-stage"),
		ImplementationUnit: "unit-eventref-stage",
		Title:              "eventref stage",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliveryQueued,
	}}}
	workstream := &memoryWorkstreamStore{}
	service := NewService(store, workstream).WithEvidenceVerifier(revision2Verifier{})
	request := ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs: []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliverySpec, Kind: "spec", Ref: "receipt/spec/eventref", Passed: true,
		}},
	}

	if _, err := service.Revise(backlogActionContext(t), "eventref-stage", request); err != nil {
		t.Fatalf("first stage execution: %v", err)
	}
	receipt, found, err := workstream.FindStageRunReceipt(context.Background(), "unit-eventref-stage:1:SPEC")
	if err != nil || !found {
		t.Fatalf("stage receipt=%+v found=%v err=%v", receipt, found, err)
	}
	assertEventIDReference(t, "stage_run_receipt", receipt.TransitionEventID,
		modulecore.EventID(receipt.ReceiptID), modulecore.EventID(receipt.ActionID),
		modulecore.EventID(receipt.BacklogItemID), modulecore.EventID(receipt.UnitID))

	if _, err := service.Revise(backlogActionContext(t), "eventref-stage", request); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	replayed, found, err := workstream.FindStageRunReceipt(context.Background(), "unit-eventref-stage:1:SPEC")
	if err != nil || !found {
		t.Fatalf("replayed stage receipt=%+v found=%v err=%v", replayed, found, err)
	}
	if replayed.TransitionEventID != receipt.TransitionEventID {
		t.Fatalf("replay must reuse the transition EventID: first=%q replay=%q",
			receipt.TransitionEventID, replayed.TransitionEventID)
	}
}

func TestClosureReceiptReferencesItsOwnEventID(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("eventref-closure"),
		ImplementationUnit: "unit-eventref-closure", WorkstreamID: "ws-eventref-closure",
		Title: "eventref closure", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryLiveVerified, ImplementationRevision: 1,
	}}}
	workstream := &memoryWorkstreamStore{}
	if _, err := workstream.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "unit-eventref-closure",
		HolderWorkstreamID: "ws-eventref-closure", Stage: domainbacklog.DeliveryLiveVerified,
		AcquiredAt: now, HeartbeatAt: now,
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	service := NewService(store, workstream).WithClock(func() time.Time { return now })
	if err := service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("recover done: %v", err)
	}

	closure, found, err := workstream.FindClosureReceipt(context.Background(), "unit-eventref-closure:1:DONE")
	if err != nil || !found {
		t.Fatalf("closure receipt=%+v found=%v err=%v", closure, found, err)
	}
	assertEventIDReference(t, "closure_receipt", closure.TransitionEventID,
		modulecore.EventID(closure.ReceiptID), modulecore.EventID(closure.ActionID),
		modulecore.EventID(closure.BacklogItemID), modulecore.EventID(closure.WorkstreamID),
		modulecore.EventID(closure.GoalID), modulecore.EventID(closure.ArtifactID))

	stage, stageFound, err := workstream.FindStageRunReceipt(context.Background(), "unit-eventref-closure:1:DONE")
	if err != nil {
		t.Fatalf("done stage receipt: %v", err)
	}
	if stageFound && stage.TransitionEventID == closure.TransitionEventID {
		t.Fatalf("DONE stage and closure receipts must not share one EventID: stage=%q closure=%q",
			stage.TransitionEventID, closure.TransitionEventID)
	}
}

func TestQueueFreezeReferencesItsOwnEventID(t *testing.T) {
	now := time.Date(2026, 9, 3, 4, 0, 0, 0, time.UTC)
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("eventref-freeze"),
		ImplementationUnit: "unit-eventref-freeze", WorkstreamID: "ws-eventref-freeze",
		Title: "eventref freeze", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliverySpec, ImplementationRevision: 1,
		CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
	}}}
	workstream := &memoryWorkstreamStore{}
	service := NewService(store, workstream).WithClock(func() time.Time { return now })

	if _, err := service.Revise(backlogActionContext(t), "eventref-freeze", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliveryBlocked,
		EvidenceRefs:        []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "worker-failure-eventref"}},
		Reason:              "worker_failure",
	}); err != nil {
		t.Fatalf("BLOCKED transition: %v", err)
	}
	freeze, found, err := workstream.GetQueueFreeze(context.Background(), "atlas-freeze:unit-eventref-freeze:1")
	if err != nil || !found {
		t.Fatalf("queue freeze=%+v found=%v err=%v", freeze, found, err)
	}
	assertEventIDReference(t, "queue_freeze", freeze.TransitionEventID,
		modulecore.EventID(freeze.FreezeID), modulecore.EventID(freeze.BlockedUnitID),
		modulecore.EventID(freeze.ActionID), modulecore.EventID(freeze.ReasonCode))
}

func TestRevalidationPromotionRecordReferencesItsOwnEventID(t *testing.T) {
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	now := start.Add(7 * 24 * time.Hour)
	item := maturationCandidate("eventref-promote", start)
	store := &memoryItemStore{items: []domainbacklog.Item{item}}
	service := NewService(store, nil).WithClock(func() time.Time { return now })

	promoted, err := service.Revalidate(context.Background(), string(item.BacklogItemID), RevalidateRequest{
		Decision: domainbacklog.RevalidationDecisionPromote, Reason: "promotion eventref",
		ReviewAgents: []string{"Mio"},
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(promoted.RevalidationRecords) != 1 {
		t.Fatalf("records=%+v", promoted.RevalidationRecords)
	}
	record := promoted.RevalidationRecords[0]
	assertEventIDReference(t, "revalidation_record", record.TransitionEventID,
		modulecore.EventID(record.BacklogID), modulecore.EventID(record.RevalidationDate),
		modulecore.EventID(record.MergedInto))

	// PROMOTE is terminal for the item, so the second promotion is a different
	// item: two promotions must never collapse onto one EventID.
	other := maturationCandidate("eventref-promote-2", start)
	store.items = append(store.items, other)
	second, err := service.Revalidate(context.Background(), string(other.BacklogItemID), RevalidateRequest{
		Decision: domainbacklog.RevalidationDecisionPromote, Reason: "promotion eventref 2",
		ReviewAgents: []string{"Mio"},
	})
	if err != nil {
		t.Fatalf("second promotion: %v", err)
	}
	if len(second.RevalidationRecords) != 1 {
		t.Fatalf("second records=%+v", second.RevalidationRecords)
	}
	if second.RevalidationRecords[0].TransitionEventID == record.TransitionEventID {
		t.Fatalf("distinct promotions must not share one EventID: %q", record.TransitionEventID)
	}
}
