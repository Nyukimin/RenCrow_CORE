package viewer

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEventHubPrivateMioDeltaIsOwnerScopedTransientAndNonblocking(t *testing.T) {
	hub := NewEventHub(8)
	ren := hub.SubscribePrivateMioDeltas("ren")
	other := hub.SubscribePrivateMioDeltas("another-user")
	t.Cleanup(func() {
		hub.UnsubscribePrivateMioDeltas("ren", ren)
		hub.UnsubscribePrivateMioDeltas("another-user", other)
	})

	event := privateMioDeltaTestEvent("first partial")
	qualification := newViewerTestPublicationQualification()
	publishPrivateDeltaForTest(t, hub, "ren", event, qualification)
	select {
	case data := <-ren:
		var got orchestrator.OrchestratorEvent
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode private delta: %v", err)
		}
		if got.Type != "agent.response_delta" || got.Content != event.Content || got.TraceID != event.TraceID || got.SessionID != event.SessionID {
			t.Fatalf("private delta = %#v, want the admitted Mio response prefix", got)
		}
		if qualification.useCount() != 0 {
			t.Fatal("qualification ran before the subscriber claimed the frame")
		}
		if !hub.ClaimOwnerScopedLiveEvent("ren", ren, data) {
			t.Fatal("valid private delta failed writer claim")
		}
	default:
		t.Fatal("authenticated owner did not receive the live Mio delta")
	}
	select {
	case data := <-other:
		t.Fatalf("another owner received private delta: %s", data)
	default:
	}
	if history := hub.History(); len(history) != 0 {
		t.Fatalf("transient delta entered canonical in-memory history: %#v", history)
	}
	_, snapshot := hub.SubscribeForReplay()
	if len(snapshot.Events) != 0 {
		t.Fatalf("transient delta entered replay snapshot: %#v", snapshot.Events)
	}

	// A slow listener is disconnected after its bounded shared live queue fills.
	slow := hub.SubscribePrivateMioDeltas("ren")
	for index := 0; index < 128; index++ {
		publishPrivateDeltaForTest(t, hub, "ren", privateMioDeltaTestEvent("bounded"), newViewerTestPublicationQualification())
	}
	for index := 0; index < privateMioDeltaClientCapacity; index++ {
		if _, ok := <-slow; !ok {
			t.Fatalf("slow private delta listener closed before draining its bounded queue at item %d", index)
		}
	}
	if _, ok := <-slow; ok {
		t.Fatal("slow private delta listener remained connected after exceeding its bounded queue")
	}
	hub.UnsubscribePrivateMioDeltas("ren", slow)
}

func TestEventHubOwnerStreamUsesOneFIFOForDeltaAndCanonicalFinal(t *testing.T) {
	hub := NewEventHub(8)
	owner, _, _, _ := hub.SubscribeForReplayWithControlAndPrivateMioDeltas("ren")
	other, _, _, _ := hub.SubscribeForReplayWithControlAndPrivateMioDeltas("other")
	if owner == nil || other == nil {
		t.Fatal("owner subscriptions were not admitted")
	}
	defer hub.Unsubscribe(owner)
	defer hub.Unsubscribe(other)

	delta := privateMioDeltaTestEvent("before final")
	publishPrivateDeltaForTest(t, hub, "ren", delta, newViewerTestPublicationQualification())
	final := privateMioFinalTestEvent("canonical final")
	final.EventSeq = 1
	accepted, cleanup, err := hub.PublishAcceptedWithClaim(final, "ren", newViewerTestPublicationQualification())
	cleanup()
	if err != nil || !accepted {
		t.Fatalf("PublishAcceptedWithClaim accepted=%t err=%v", accepted, err)
	}

	for index, wantType := range []string{"agent.response_delta", "agent.response"} {
		data := <-owner
		var got orchestrator.OrchestratorEvent
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode queued frame %d: %v", index, err)
		}
		if got.Type != wantType {
			t.Fatalf("frame %d type=%q, want %q", index, got.Type, wantType)
		}
		if !hub.ClaimOwnerScopedLiveEvent("ren", owner, data) {
			t.Fatalf("owner frame %d failed writer claim", index)
		}
	}
	select {
	case data := <-other:
		t.Fatalf("another owner received a private canonical final: %s", data)
	default:
	}
	history := hub.History()
	if len(history) != 1 || history[0].EventID != final.EventID || history[0].EventSeq != final.EventSeq {
		t.Fatalf("canonical history=%#v, want only the admitted final event", history)
	}
	_, snapshot := hub.SubscribeForReplay()
	if len(snapshot.Events) != 1 || snapshot.Events[0].EventID != final.EventID {
		t.Fatalf("canonical replay snapshot=%#v, want final only", snapshot.Events)
	}
}

func TestEventHubRetainsCanonicalFinalWhenLiveClaimIsRevoked(t *testing.T) {
	hub := NewEventHub(8)
	owner, _, _, _ := hub.SubscribeForReplayWithControlAndPrivateMioDeltas("ren")
	if owner == nil {
		t.Fatal("owner subscription was not admitted")
	}
	defer hub.Unsubscribe(owner)
	qualification := newViewerTestPublicationQualification()
	final := privateMioFinalTestEvent("admitted before reset")
	final.EventSeq = 1
	accepted, cleanup, err := hub.PublishAcceptedWithClaim(final, "ren", qualification)
	cleanup()
	if err != nil || !accepted {
		t.Fatalf("PublishAcceptedWithClaim accepted=%t err=%v", accepted, err)
	}
	data := <-owner
	qualification.revoke()
	if hub.ClaimOwnerScopedLiveEvent("ren", owner, data) {
		t.Fatal("revoked canonical final passed its live writer claim")
	}
	if got := hub.History(); len(got) != 1 || got[0].EventID != final.EventID {
		t.Fatalf("canonical history after stale live claim=%#v, want admitted event retained", got)
	}
	if qualification.releaseCount() != 2 {
		t.Fatalf("canonical qualification releases=%d, want producer and queued subscriber refs", qualification.releaseCount())
	}
}

func TestEventHubAcceptedPrivateMioFinalRequiresPrivateIncarnation(t *testing.T) {
	hub := NewEventHub(8)
	for _, mutate := range []func(*orchestrator.OrchestratorEvent){
		func(event *orchestrator.OrchestratorEvent) { event.Visibility = "public" },
		func(event *orchestrator.OrchestratorEvent) { event.PublicationIncarnation = "" },
		func(event *orchestrator.OrchestratorEvent) {
			event.PublicationIncarnation = " session-incarnation-ren-1"
		},
	} {
		final := privateMioFinalTestEvent("qualified final")
		final.EventSeq = 1
		mutate(&final)
		qualification := newViewerTestPublicationQualification()
		accepted, cleanup, err := hub.PublishAcceptedWithClaim(final, "ren", qualification)
		cleanup()
		if err == nil || accepted {
			t.Fatalf("PublishAcceptedWithClaim accepted=%t err=%v for an invalid private/incarnation envelope", accepted, err)
		}
		if qualification.releaseCount() != 1 {
			t.Fatalf("rejected qualification releases=%d, want producer cleanup", qualification.releaseCount())
		}
	}
}

func TestEventHubAcceptedFinalDoesNotReleaseTicketsInsideOuterGuard(t *testing.T) {
	hub := NewEventHub(8)
	owner, _, _, _ := hub.SubscribeForReplayWithControlAndPrivateMioDeltas("ren")
	if owner == nil {
		t.Fatal("owner subscription was not admitted")
	}
	defer hub.Unsubscribe(owner)
	var outer sync.RWMutex
	oldQualification := &releaseTakesWriteLockQualification{lifecycle: &outer}
	oldFinal := privateMioFinalTestEvent("old queued ticket")
	oldFinal.EventSeq = 1
	accepted, oldCleanup, err := hub.PublishAcceptedWithClaim(oldFinal, "ren", oldQualification)
	oldCleanup()
	if err != nil || !accepted {
		t.Fatalf("PublishAcceptedWithClaim old final accepted=%t err=%v", accepted, err)
	}
	for index := 1; index < cap(owner); index++ {
		owner <- []byte("queued")
	}

	newFinal := privateMioFinalTestEvent("new guarded final")
	newFinal.EventSeq = 2
	qualification := newViewerTestPublicationQualification()

	// Model Guard.Use's read lock. Hub publication must return while this lock
	// is held; the caller can then unlock and run deferred ticket cleanup.
	outer.RLock()
	type publishResult struct {
		accepted bool
		cleanup  PublicationCleanup
		err      error
	}
	done := make(chan publishResult, 1)
	go func() {
		accepted, cleanup, err := hub.PublishAcceptedWithClaim(newFinal, "ren", qualification)
		done <- publishResult{accepted: accepted, cleanup: cleanup, err: err}
	}()
	select {
	case result := <-done:
		outer.RUnlock()
		defer result.cleanup()
		if result.err != nil || !result.accepted {
			t.Fatalf("PublishAcceptedWithClaim accepted=%t err=%v", result.accepted, result.err)
		}
	case <-time.After(100 * time.Millisecond):
		outer.RUnlock()
		<-done
		t.Fatal("Hub released a retained publication ticket while the outer turn guard was held")
	}
}

func TestEventHubRevokesPendingPrivateDeltaAtWriterClaimAndReleasesZeroSubscriberTicket(t *testing.T) {
	hub := NewEventHub(8)
	owner := hub.SubscribePrivateMioDeltas("ren")
	defer hub.UnsubscribePrivateMioDeltas("ren", owner)
	qualification := newViewerTestPublicationQualification()
	publishPrivateDeltaForTest(t, hub, "ren", privateMioDeltaTestEvent("pending"), qualification)
	data := <-owner
	qualification.revoke()
	if hub.ClaimOwnerScopedLiveEvent("ren", owner, data) {
		t.Fatal("revoked pending delta passed its writer claim")
	}
	if qualification.releaseCount() != 2 { // producer and queued subscriber handles
		t.Fatalf("revoked qualification release count=%d, want two", qualification.releaseCount())
	}

	noSubscribers := newViewerTestPublicationQualification()
	prepared, err := hub.PreparePrivateMioDelta("ren", privateMioDeltaTestEvent("nobody"))
	if err != nil {
		t.Fatalf("PreparePrivateMioDelta: %v", err)
	}
	if err := hub.PublishPreparedPrivateMioDelta("ren", prepared, noSubscribers); err != nil {
		t.Fatalf("zero-subscriber publish: %v", err)
	}
	if noSubscribers.releaseCount() != 1 {
		t.Fatalf("zero-subscriber qualification release count=%d, want one", noSubscribers.releaseCount())
	}
}

func TestEventHubRejectsInvalidPrivateMioDeltaAdmission(t *testing.T) {
	hub := NewEventHub(8)
	general, _ := hub.Subscribe()
	defer hub.Unsubscribe(general)
	legacyDelta := privateMioDeltaTestEvent("must not use the unscoped path")
	hub.OnEvent(legacyDelta)
	if history := hub.History(); len(history) != 0 {
		t.Fatalf("unscoped OnEvent added a transient delta to history: %#v", history)
	}
	select {
	case data := <-general:
		t.Fatalf("unscoped OnEvent fanned out a private delta: %s", data)
	default:
	}
	if _, err := hub.PreparePrivateMioDelta("ren", orchestrator.OrchestratorEvent{Type: "agent.response", Content: "final"}); err == nil {
		t.Fatal("PreparePrivateMioDelta accepted a non-delta event")
	}
	if _, err := hub.PreparePrivateMioDelta("", privateMioDeltaTestEvent("private")); err == nil {
		t.Fatal("PreparePrivateMioDelta accepted an empty authenticated owner")
	}
}

func publishPrivateDeltaForTest(t *testing.T, hub *EventHub, ownerID string, event orchestrator.OrchestratorEvent, qualification domainconversation.PreparedChatTurnPublicationGuard) {
	t.Helper()
	prepared, err := hub.PreparePrivateMioDelta(ownerID, event)
	if err != nil {
		t.Fatalf("PreparePrivateMioDelta: %v", err)
	}
	if err := hub.PublishPreparedPrivateMioDelta(ownerID, prepared, qualification); err != nil {
		t.Fatalf("PublishPreparedPrivateMioDelta: %v", err)
	}
}

func privateMioDeltaTestEvent(content string) orchestrator.OrchestratorEvent {
	event := orchestrator.NewEventWithTraceID(
		modulecore.NewTraceID(), "agent.response_delta", "mio", "user", content,
		"CHAT", string(modulecore.NewTaskID()), string(modulecore.NewSessionID()), "viewer", "cmd:ren",
	)
	event.TurnID = modulecore.NewTurnID()
	event.MessageID = modulecore.NewMessageID()
	event.RunID = modulecore.NewRunID()
	event.ActorKind = "agent"
	event.ActorID = "mio"
	return event
}

func privateMioFinalTestEvent(content string) orchestrator.OrchestratorEvent {
	event := orchestrator.NewEventWithTraceID(
		modulecore.NewTraceID(), "agent.response", "mio", "user", content,
		"CHAT", string(modulecore.NewTaskID()), string(modulecore.NewSessionID()), "viewer", "cmd:ren",
	)
	event.TurnID = modulecore.NewTurnID()
	event.MessageID = modulecore.NewMessageID()
	event.RunID = modulecore.NewRunID()
	event.ActorKind = "agent"
	event.ActorID = "mio"
	event.Visibility = "private"
	event.PublicationIncarnation = "session-incarnation-ren-1"
	return event
}

type viewerTestPublicationQualification struct {
	state    *viewerTestPublicationState
	mu       sync.Mutex
	released bool
}

type viewerTestPublicationState struct {
	mu       sync.Mutex
	valid    bool
	uses     int
	retains  int
	releases int
}

type releaseTakesWriteLockQualification struct {
	lifecycle *sync.RWMutex
	released  sync.Once
}

func (q *releaseTakesWriteLockQualification) Use(claim func() error) error {
	return claim()
}

func (q *releaseTakesWriteLockQualification) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	return &releaseTakesWriteLockQualification{lifecycle: q.lifecycle}
}

func (q *releaseTakesWriteLockQualification) Release() {
	q.released.Do(func() {
		q.lifecycle.Lock()
		q.lifecycle.Unlock()
	})
}

func newViewerTestPublicationQualification() *viewerTestPublicationQualification {
	return &viewerTestPublicationQualification{state: &viewerTestPublicationState{valid: true}}
}

func (q *viewerTestPublicationQualification) Use(claim func() error) error {
	q.state.mu.Lock()
	if !q.state.valid {
		q.state.mu.Unlock()
		return errors.New("publication qualification revoked")
	}
	q.state.uses++
	q.state.mu.Unlock()
	return claim()
}

func (q *viewerTestPublicationQualification) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if !q.state.valid {
		return nil
	}
	q.state.retains++
	return &viewerTestPublicationQualification{state: q.state}
}

func (q *viewerTestPublicationQualification) Release() {
	q.mu.Lock()
	if q.released {
		q.mu.Unlock()
		return
	}
	q.released = true
	q.mu.Unlock()
	q.state.mu.Lock()
	q.state.releases++
	q.state.mu.Unlock()
}

func (q *viewerTestPublicationQualification) revoke() {
	q.state.mu.Lock()
	q.state.valid = false
	q.state.mu.Unlock()
}

func (q *viewerTestPublicationQualification) useCount() int {
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	return q.state.uses
}

func (q *viewerTestPublicationQualification) releaseCount() int {
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	return q.state.releases
}
