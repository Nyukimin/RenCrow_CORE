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
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func claimedEventSink(t *testing.T) (*developmentCapturingStore, developmentEventLogSink) {
	t.Helper()
	store := &developmentCapturingStore{}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("canonical event log: %v", err)
	}
	return store, developmentEventLogSink{store: archive, canonicalActorID: atlasExecutionActorID}
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

// TestDevelopmentEventSinkStampsCanonicalActorUnderOwnerAPIUserScope covers the
// production owner route (POST /v1/atlas/items/{id}/{op}).  The HTTP ingress
// binds a *user* ToolExecutionScope for the authenticated owner, while the
// Atlas Action owner binds a Task/Run whose canonical execution actor is the
// CORE agent wired at startup.  Copying the user scope into the event made
// every run-scoped companion fail the canonical actor contract, so revise
// returned HTTP 400 "execution actor_kind must be \"agent\"" and the receipt
// never reached the canonical Event log (Step18 production E2E).
func TestDevelopmentEventSinkStampsCanonicalActorUnderOwnerAPIUserScope(t *testing.T) {
	store, sink := claimedEventSink(t)
	task, run, trace := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID()
	scope, err := tool.NewToolExecutionScope(
		"req-s18-owner-api",
		tool.ActorKindUser,
		"nyukimi",
		"nyukimi",
		[]string{tool.DataScopeUser},
		tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("owner API scope: %v", err)
	}
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	ctx, err = execution.WithIdentity(ctx, task, run, trace)
	if err != nil {
		t.Fatalf("bind execution identity: %v", err)
	}
	claimed := modulecore.NewEventID()
	err = sink.AppendDevelopmentEvent(ctx, backlogapp.DevelopmentEvent{
		EventID:    claimed,
		Type:       backlogapp.DevelopmentEventStageRunTransition,
		UnitID:     "unit-s18-owner",
		ArtifactID: "transition:unit-s18-owner:SPEC:c1",
		RequestID:  "req-s18-owner-api",
		CreatedAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("owner API user scope rejected by the canonical execution actor contract: %v", err)
	}
	if len(store.captured) != 1 {
		t.Fatalf("captured=%d, want 1", len(store.captured))
	}
	got := store.captured[0]
	if got.EventID != claimed {
		t.Fatalf("companion EventID=%q, receipt references %q", got.EventID, claimed)
	}
	if got.TaskID != task || got.RunID != run {
		t.Fatalf("execution identity lost: task=%q run=%q", got.TaskID, got.RunID)
	}
	if got.ActorKind != "agent" || got.ActorID != atlasExecutionActorID {
		t.Fatalf("actor=%s/%s, want agent/%s (the CORE agent that owns the Atlas Action)", got.ActorKind, got.ActorID, atlasExecutionActorID)
	}
}

// TestDevelopmentEventSinkKeepsNonCanonicalAgentActorRejected states the
// boundary that the normalization above must not widen: an *agent*
// ToolExecutionScope still names the acting agent, and a name outside the
// canonical CORE actors stays rejected instead of being silently rewritten.
func TestDevelopmentEventSinkKeepsNonCanonicalAgentActorRejected(t *testing.T) {
	store, sink := claimedEventSink(t)
	ctx, err := withTrustedAgentPublicToolScope(context.Background(), "req-s18-guest", "tour-guide")
	if err != nil {
		t.Fatalf("agent scope: %v", err)
	}
	ctx, err = execution.WithIdentity(ctx, modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatalf("bind execution identity: %v", err)
	}
	err = sink.AppendDevelopmentEvent(ctx, backlogapp.DevelopmentEvent{
		Type:      backlogapp.DevelopmentEventClosureTransition,
		UnitID:    "unit-s18-guest",
		CreatedAt: time.Now().UTC(),
	})
	if err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("non-canonical agent actor err=%v, want rejection naming actor", err)
	}
	if len(store.captured) != 0 {
		t.Fatalf("non-canonical agent actor persisted anyway: %+v", store.captured)
	}
}
