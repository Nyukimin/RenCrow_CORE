package backlog

// Step18 s18_atlas_event_refs (dangling reference closure): a receipt that
// carries a TransitionEventID must have the companion canonical event written
// by the Step18 owner under that very EventID. Until now the five write points
// only stored the EventID inside the receipt payload, so every reference
// resolved to nothing in the canonical Event log.

import (
	"context"
	"strings"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// assertCompanionEvent proves the TransitionEventID of a persisted receipt is
// actually addressed by a written companion event.
func assertCompanionEvent(t *testing.T, label string, sink *developmentEventSinkStub, eventID modulecore.EventID, unitID string, nonIdentity ...modulecore.EventID) {
	t.Helper()
	if err := eventID.Validate(); err != nil {
		t.Fatalf("%s: transition EventID %q is not a canonical EventID: %v", label, eventID, err)
	}
	for _, other := range nonIdentity {
		if other != "" && other == eventID {
			t.Fatalf("%s: transition EventID %q duplicates another identity of the same receipt", label, eventID)
		}
	}
	var companions []DevelopmentEvent
	for _, event := range sink.events {
		if event.EventID == eventID {
			companions = append(companions, event)
		}
	}
	if len(companions) == 0 {
		t.Fatalf("%s: TransitionEventID %q dangles: no companion event was written for it (sink events=%+v)", label, eventID, sink.events)
	}
	if strings.TrimSpace(companions[0].Type) == "" {
		t.Fatalf("%s: companion event for %q carries no event type", label, eventID)
	}
	if unitID != "" && companions[0].UnitID != unitID {
		t.Fatalf("%s: companion event unit=%q, want %q", label, companions[0].UnitID, unitID)
	}
	for _, repeat := range companions[1:] {
		if repeat.Type != companions[0].Type {
			t.Fatalf("%s: repeated companion writes for %q must keep one event type: %q vs %q", label, eventID, companions[0].Type, repeat.Type)
		}
	}
}

// assertNoForeignCompanionEvent proves the owner never mints a second EventID
// beside the one the receipt already references.
func assertNoForeignCompanionEvent(t *testing.T, label string, sink *developmentEventSinkStub, want ...modulecore.EventID) {
	t.Helper()
	for _, event := range sink.events {
		if event.EventID == "" {
			continue
		}
		known := false
		for _, id := range want {
			if event.EventID == id {
				known = true
			}
		}
		if !known {
			t.Fatalf("%s: companion event %q carries EventID %q that no receipt references", label, event.Type, event.EventID)
		}
	}
}

func TestStageRunReceiptWritesCompanionEvent(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		BacklogItemID:      modulecore.BacklogItemID("companion-stage"),
		ImplementationUnit: "unit-companion-stage",
		Title:              "companion stage",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliveryQueued,
	}}}
	workstream := &memoryWorkstreamStore{}
	sink := &developmentEventSinkStub{}
	service := NewService(store, workstream).WithEvidenceVerifier(revision2Verifier{}).WithDevelopmentEventSink(sink)
	request := ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs: []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliverySpec, Kind: "spec", Ref: "receipt/spec/companion", Passed: true,
		}},
	}

	if _, err := service.Revise(backlogActionContext(t), "companion-stage", request); err != nil {
		t.Fatalf("first stage execution: %v", err)
	}
	receipt, found, err := workstream.FindStageRunReceipt(context.Background(), "unit-companion-stage:1:SPEC")
	if err != nil || !found {
		t.Fatalf("stage receipt=%+v found=%v err=%v", receipt, found, err)
	}
	assertCompanionEvent(t, "stage_run_receipt", sink, receipt.TransitionEventID, "unit-companion-stage",
		modulecore.EventID(receipt.ReceiptID), modulecore.EventID(receipt.ActionID),
		modulecore.EventID(receipt.BacklogItemID), modulecore.EventID(receipt.UnitID))
	assertNoForeignCompanionEvent(t, "stage_run_receipt", sink, receipt.TransitionEventID)

	if _, err := service.Revise(backlogActionContext(t), "companion-stage", request); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	replayed, found, err := workstream.FindStageRunReceipt(context.Background(), "unit-companion-stage:1:SPEC")
	if err != nil || !found {
		t.Fatalf("replayed stage receipt=%+v found=%v err=%v", replayed, found, err)
	}
	if replayed.TransitionEventID != receipt.TransitionEventID {
		t.Fatalf("replay must reuse the transition EventID: first=%q replay=%q", receipt.TransitionEventID, replayed.TransitionEventID)
	}
	assertCompanionEvent(t, "stage_run_receipt replay", sink, replayed.TransitionEventID, "unit-companion-stage")
	assertNoForeignCompanionEvent(t, "stage_run_receipt replay", sink, replayed.TransitionEventID)
}

func TestClosureReceiptWritesCompanionEvent(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("companion-closure"),
		ImplementationUnit: "unit-companion-closure", WorkstreamID: "ws-companion-closure",
		Title: "companion closure", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryLiveVerified, ImplementationRevision: 1,
	}}}
	workstream := &memoryWorkstreamStore{}
	if _, err := workstream.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "unit-companion-closure",
		HolderWorkstreamID: "ws-companion-closure", Stage: domainbacklog.DeliveryLiveVerified,
		AcquiredAt: now, HeartbeatAt: now,
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	sink := &developmentEventSinkStub{}
	service := NewService(store, workstream).WithClock(func() time.Time { return now }).WithDevelopmentEventSink(sink)
	if err := service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("recover done: %v", err)
	}

	closure, found, err := workstream.FindClosureReceipt(context.Background(), "unit-companion-closure:1:DONE")
	if err != nil || !found {
		t.Fatalf("closure receipt=%+v found=%v err=%v", closure, found, err)
	}
	assertCompanionEvent(t, "closure_receipt", sink, closure.TransitionEventID, "unit-companion-closure",
		modulecore.EventID(closure.ReceiptID), modulecore.EventID(closure.ActionID),
		modulecore.EventID(closure.BacklogItemID), modulecore.EventID(closure.WorkstreamID),
		modulecore.EventID(closure.GoalID), modulecore.EventID(closure.ArtifactID))

	stage, stageFound, err := workstream.FindStageRunReceipt(context.Background(), "unit-companion-closure:1:DONE")
	if err != nil {
		t.Fatalf("done stage receipt: %v", err)
	}
	if !stageFound {
		t.Fatalf("done stage receipt not found")
	}
	assertCompanionEvent(t, "stage_run_receipt done", sink, stage.TransitionEventID, "unit-companion-closure")
	if stage.TransitionEventID == closure.TransitionEventID {
		t.Fatalf("DONE stage and closure receipts must not share one EventID: stage=%q closure=%q", stage.TransitionEventID, closure.TransitionEventID)
	}
	assertNoForeignCompanionEvent(t, "closure_receipt", sink, closure.TransitionEventID, stage.TransitionEventID)
}

func TestQueueFreezeWritesCompanionEvent(t *testing.T) {
	now := time.Date(2026, 9, 3, 4, 0, 0, 0, time.UTC)
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("companion-freeze"),
		ImplementationUnit: "unit-companion-freeze", WorkstreamID: "ws-companion-freeze",
		Title: "companion freeze", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliverySpec, ImplementationRevision: 1,
		CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
	}}}
	workstream := &memoryWorkstreamStore{}
	sink := &developmentEventSinkStub{}
	service := NewService(store, workstream).WithClock(func() time.Time { return now }).WithDevelopmentEventSink(sink)

	if _, err := service.Revise(backlogActionContext(t), "companion-freeze", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliveryBlocked,
		EvidenceRefs:        []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "worker-failure-companion"}},
		Reason:              "worker_failure",
	}); err != nil {
		t.Fatalf("BLOCKED transition: %v", err)
	}
	freeze, found, err := workstream.GetQueueFreeze(context.Background(), "atlas-freeze:unit-companion-freeze:1")
	if err != nil || !found {
		t.Fatalf("queue freeze=%+v found=%v err=%v", freeze, found, err)
	}
	assertCompanionEvent(t, "queue_freeze", sink, freeze.TransitionEventID, "unit-companion-freeze",
		modulecore.EventID(freeze.FreezeID), modulecore.EventID(freeze.BlockedUnitID),
		modulecore.EventID(freeze.ActionID), modulecore.EventID(freeze.ReasonCode))
	// The BLOCKED transition persists both its stage run receipt and the queue
	// freeze, so both receipts must own their own companion event and no event
	// may be minted beside them.
	blockedStage, stageFound, err := workstream.FindStageRunReceipt(context.Background(), "unit-companion-freeze:1:BLOCKED")
	if err != nil || !stageFound {
		t.Fatalf("blocked stage receipt=%+v found=%v err=%v", blockedStage, stageFound, err)
	}
	assertCompanionEvent(t, "stage_run_receipt blocked", sink, blockedStage.TransitionEventID, "unit-companion-freeze",
		modulecore.EventID(blockedStage.ReceiptID), modulecore.EventID(blockedStage.ActionID),
		modulecore.EventID(blockedStage.BacklogItemID), modulecore.EventID(blockedStage.UnitID))
	if blockedStage.TransitionEventID == freeze.TransitionEventID {
		t.Fatalf("BLOCKED stage and freeze receipts must not share one EventID: stage=%q freeze=%q", blockedStage.TransitionEventID, freeze.TransitionEventID)
	}
	assertNoForeignCompanionEvent(t, "queue_freeze", sink, freeze.TransitionEventID, blockedStage.TransitionEventID)
}

func TestRevalidationPromotionWritesCompanionEvent(t *testing.T) {
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	now := start.Add(7 * 24 * time.Hour)
	item := maturationCandidate("companion-promote", start)
	store := &memoryItemStore{items: []domainbacklog.Item{item}}
	sink := &developmentEventSinkStub{}
	service := NewService(store, nil).WithClock(func() time.Time { return now }).WithDevelopmentEventSink(sink)

	promoted, err := service.Revalidate(backlogActionContext(t), string(item.BacklogItemID), RevalidateRequest{
		Decision: domainbacklog.RevalidationDecisionPromote, Reason: "promotion companion",
		ReviewAgents: []string{"Mio"},
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(promoted.RevalidationRecords) != 1 {
		t.Fatalf("records=%+v", promoted.RevalidationRecords)
	}
	record := promoted.RevalidationRecords[0]
	assertCompanionEvent(t, "revalidation_record", sink, record.TransitionEventID, promoted.ImplementationUnit,
		modulecore.EventID(record.BacklogID), modulecore.EventID(record.RevalidationDate),
		modulecore.EventID(record.MergedInto))
	assertNoForeignCompanionEvent(t, "revalidation_record", sink, record.TransitionEventID)

	stored, err := service.Get(context.Background(), string(item.BacklogItemID))
	if err != nil || len(stored.RevalidationRecords) != 1 {
		t.Fatalf("stored item=%+v err=%v", stored, err)
	}
	if stored.RevalidationRecords[0].TransitionEventID != record.TransitionEventID {
		t.Fatalf("persisted promotion record must keep its EventID: stored=%q wrote=%q", stored.RevalidationRecords[0].TransitionEventID, record.TransitionEventID)
	}
}
