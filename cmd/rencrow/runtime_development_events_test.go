package main

// Step18 s18_atlas_event_refs (production boundary): an Atlas lifecycle receipt
// stores a TransitionEventID, and the only thing that makes that reference real
// is the canonical event written under the very same EventID.  If this sink
// minted its own EventID for every development event, every receipt written by
// the backlog owner would point at nothing, which is exactly the dangling
// reference the Step18 plan left open.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func claimedEventSink(t *testing.T) (*developmentCapturingStore, developmentEventLogSink) {
	t.Helper()
	store := &developmentCapturingStore{}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("canonical event log: %v", err)
	}
	return store, developmentEventLogSink{store: archive}
}

func TestDevelopmentEventSinkWritesTransitionEventUnderClaimedEventID(t *testing.T) {
	store, sink := claimedEventSink(t)
	claimed := modulecore.NewEventID()
	event := backlogapp.DevelopmentEvent{
		EventID:    claimed,
		Type:       backlogapp.DevelopmentEventStageRunTransition,
		UnitID:     "unit-s18-claim",
		ArtifactID: "transition:unit-s18-claim:SPEC:unit-s18-claim:1:SPEC",
		CreatedAt:  time.Now().UTC(),
		Fields:     map[string]any{"target_stage": "SPEC"},
	}
	if err := sink.AppendDevelopmentEvent(context.Background(), event); err != nil {
		t.Fatalf("append transition companion: %v", err)
	}
	if len(store.captured) != 1 {
		t.Fatalf("captured=%d, want 1", len(store.captured))
	}
	persisted := store.captured[0]
	if persisted.EventID != claimed {
		t.Fatalf("canonical event stored under EventID %q, but the receipt references %q", persisted.EventID, claimed)
	}
	if err := persisted.EventID.Validate(); err != nil {
		t.Fatalf("persisted EventID invalid: %v", err)
	}
	if persisted.EventType != backlogapp.DevelopmentEventStageRunTransition {
		t.Fatalf("event type=%q, want %q", persisted.EventType, backlogapp.DevelopmentEventStageRunTransition)
	}
}

func TestDevelopmentEventSinkKeepsTransitionEventIDsDistinct(t *testing.T) {
	store, sink := claimedEventSink(t)
	stageEventID := modulecore.NewEventID()
	closureEventID := modulecore.NewEventID()
	if stageEventID == closureEventID {
		t.Fatalf("fixture reused one EventID for two receipts: %q", stageEventID)
	}
	for _, event := range []backlogapp.DevelopmentEvent{
		{EventID: stageEventID, Type: backlogapp.DevelopmentEventStageRunTransition, UnitID: "unit-s18-two", ArtifactID: "transition:unit-s18-two:SPEC:k1", CreatedAt: time.Now().UTC()},
		{EventID: closureEventID, Type: backlogapp.DevelopmentEventClosureTransition, UnitID: "unit-s18-two", ArtifactID: "transition:unit-s18-two:DONE:k2", CreatedAt: time.Now().UTC()},
	} {
		if err := sink.AppendDevelopmentEvent(context.Background(), event); err != nil {
			t.Fatalf("append %s: %v", event.Type, err)
		}
	}
	if len(store.captured) != 2 {
		t.Fatalf("captured=%d, want 2", len(store.captured))
	}
	if store.captured[0].EventID != stageEventID || store.captured[1].EventID != closureEventID {
		t.Fatalf("receipt EventIDs were not kept per event: %q %q", store.captured[0].EventID, store.captured[1].EventID)
	}
}

func TestDevelopmentEventSinkStampsProjectionEventsWithoutClaim(t *testing.T) {
	store, sink := claimedEventSink(t)
	event := backlogapp.DevelopmentEvent{
		Type:      "state_transition_requested",
		UnitID:    "unit-s18-projection",
		CreatedAt: time.Now().UTC(),
	}
	if err := sink.AppendDevelopmentEvent(context.Background(), event); err != nil {
		t.Fatalf("append projection event: %v", err)
	}
	if len(store.captured) != 1 {
		t.Fatalf("captured=%d, want 1", len(store.captured))
	}
	if err := store.captured[0].EventID.Validate(); err != nil {
		t.Fatalf("projection event has no canonical EventID: %v", err)
	}
}

func TestDevelopmentEventSinkRejectsMalformedClaimedEventID(t *testing.T) {
	store, sink := claimedEventSink(t)
	malformed := modulecore.EventID("not-a-canonical-event-id")
	err := sink.AppendDevelopmentEvent(context.Background(), backlogapp.DevelopmentEvent{
		EventID:   malformed,
		Type:      backlogapp.DevelopmentEventQueueFreezeTransition,
		UnitID:    "unit-s18-bad",
		CreatedAt: time.Now().UTC(),
	})
	if err == nil || !strings.Contains(err.Error(), "EventID") {
		t.Fatalf("malformed claim err=%v, want rejection naming EventID", err)
	}
	if len(store.captured) != 0 {
		t.Fatalf("malformed claim persisted anyway: %+v", store.captured)
	}
}
