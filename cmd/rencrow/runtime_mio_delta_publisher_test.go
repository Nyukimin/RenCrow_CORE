package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMioDeltaPublicationQueueDrainsAdmittedPrefixAsTransientOwnerEvent(t *testing.T) {
	hub := viewer.NewEventHub(8)
	private := hub.SubscribePrivateMioDeltas("ren")
	defer hub.UnsubscribePrivateMioDeltas("ren", private)
	queue := newMioDeltaPublicationQueue("ren", hub)
	delta := testMioAnswerDelta()
	qualification := newMioDeltaTestQualification()
	finish, err := queue.Admit(context.Background(), delta, qualification)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if _, err := queue.Admit(context.Background(), orchestrator.MioAnswerDelta{Text: strings.Repeat("x", mioDeltaPublicationMaxBytes+1)}, newMioDeltaTestQualification()); !errors.Is(err, errMioDeltaPublicationTooLarge) {
		t.Fatalf("oversized subsequent admission error=%v, want bounded rejection", err)
	}
	finish(true)
	if err := queue.CloseAndWait(); err != nil {
		t.Fatalf("CloseAndWait: %v", err)
	}
	select {
	case data := <-private:
		if qualification.useCount() != 0 {
			t.Fatal("publication qualification ran before the SSE writer claim")
		}
		var event orchestrator.OrchestratorEvent
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("decode private event: %v", err)
		}
		if event.Type != "agent.response_delta" || event.Content != delta.Text || event.TraceID != modulecore.TraceID(delta.TraceID) ||
			event.TaskID != modulecore.TaskID(delta.TaskID) || event.RunID != modulecore.RunID(delta.RunID) ||
			event.SessionID != modulecore.SessionID(delta.SessionID) || event.MessageID != modulecore.MessageID(delta.MessageID) || event.TurnID != modulecore.TurnID(delta.TurnID) {
			t.Fatalf("private event=%#v, want canonical delta identity", event)
		}
		if !hub.ClaimOwnerScopedLiveEvent("ren", private, data) {
			t.Fatal("qualified private delta failed its writer claim")
		}
		if qualification.useCount() != 1 {
			t.Fatalf("qualification use count=%d, want one writer claim", qualification.useCount())
		}
	default:
		t.Fatal("already-admitted response prefix was lost when later admission failed")
	}
	if history := hub.History(); len(history) != 0 {
		t.Fatalf("transient publication entered canonical history: %#v", history)
	}
}

func TestMioDeltaPublicationQueueRejectsCanceledAndClosedAdmissions(t *testing.T) {
	hub := viewer.NewEventHub(8)
	queue := newMioDeltaPublicationQueue("ren", hub)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queue.Admit(ctx, testMioAnswerDelta(), newMioDeltaTestQualification()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission error=%v, want context.Canceled", err)
	}
	if err := queue.CloseAndWait(); err != nil {
		t.Fatalf("CloseAndWait: %v", err)
	}
	if _, err := queue.Admit(context.Background(), testMioAnswerDelta(), newMioDeltaTestQualification()); err == nil {
		t.Fatal("closed publication queue accepted a new delta")
	}
}

func TestMioDeltaPublicationQueueDiscardsAdmissionWhenGuardRejects(t *testing.T) {
	hub := viewer.NewEventHub(8)
	private := hub.SubscribePrivateMioDeltas("ren")
	defer hub.UnsubscribePrivateMioDeltas("ren", private)
	queue := newMioDeltaPublicationQueue("ren", hub)
	qualification := newMioDeltaTestQualification()
	finish, err := queue.Admit(context.Background(), testMioAnswerDelta(), qualification)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	finish(false)
	if err := queue.CloseAndWait(); err != nil {
		t.Fatalf("CloseAndWait: %v", err)
	}
	select {
	case data := <-private:
		t.Fatalf("guard-rejected admission was published: %s", data)
	default:
	}
	if qualification.releaseCount() != 1 {
		t.Fatalf("discarded qualification release count=%d, want exactly one", qualification.releaseCount())
	}
}

func TestMioDeltaPublicationQueueRevalidatesAtWriterClaim(t *testing.T) {
	hub := viewer.NewEventHub(8)
	private := hub.SubscribePrivateMioDeltas("ren")
	defer hub.UnsubscribePrivateMioDeltas("ren", private)
	queue := newMioDeltaPublicationQueue("ren", hub)
	qualification := newMioDeltaTestQualification()
	finish, err := queue.Admit(context.Background(), testMioAnswerDelta(), qualification)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	finish(true)
	if err := queue.CloseAndWait(); err != nil {
		t.Fatalf("CloseAndWait: revoked pending delta should be dropped without failing the completed turn: %v", err)
	}
	data := <-private
	qualification.revoke()
	if hub.ClaimOwnerScopedLiveEvent("ren", private, data) {
		t.Fatal("revoked pending delta passed its writer claim")
	}
	if qualification.releaseCount() != 2 { // producer ref plus the subscriber's retained ticket
		t.Fatalf("revoked qualification release count=%d, want producer and subscriber releases", qualification.releaseCount())
	}
}

func TestMioDeltaPublicationQueueBoundsQueuedAndInFlightAdmissions(t *testing.T) {
	hub := viewer.NewEventHub(8)
	queue := newMioDeltaPublicationQueue("ren", hub)
	qualifications := make([]*mioDeltaTestQualification, 0, mioDeltaPublicationQueueCapacity)
	tickets := make([]func(bool), 0, mioDeltaPublicationQueueCapacity)
	for index := 0; index < mioDeltaPublicationQueueCapacity; index++ {
		qualification := newMioDeltaTestQualification()
		finish, err := queue.Admit(context.Background(), testMioAnswerDelta(), qualification)
		if err != nil {
			t.Fatalf("Admit(%d): %v", index+1, err)
		}
		tickets = append(tickets, finish)
		qualifications = append(qualifications, qualification)
	}
	if _, err := queue.Admit(context.Background(), testMioAnswerDelta(), newMioDeltaTestQualification()); !errors.Is(err, errMioDeltaPublicationQueueFull) {
		t.Fatalf("admission beyond total queue capacity error=%v, want bounded queue-full status", err)
	}
	for _, finish := range tickets {
		finish(false)
	}
	if err := queue.CloseAndWait(); err != nil {
		t.Fatalf("CloseAndWait: %v", err)
	}
	for index, qualification := range qualifications {
		if qualification.releaseCount() != 1 {
			t.Fatalf("qualification %d release count=%d, want one", index, qualification.releaseCount())
		}
	}
}

type mioDeltaTestQualificationState struct {
	mu      sync.Mutex
	valid   bool
	uses    int
	release int
}

type mioDeltaTestQualification struct {
	state    *mioDeltaTestQualificationState
	mu       sync.Mutex
	released bool
}

func newMioDeltaTestQualification() *mioDeltaTestQualification {
	return &mioDeltaTestQualification{state: &mioDeltaTestQualificationState{valid: true}}
}

func (q *mioDeltaTestQualification) Use(claim func() error) error {
	q.state.mu.Lock()
	if !q.state.valid {
		q.state.mu.Unlock()
		return errors.New("qualification revoked")
	}
	q.state.uses++
	q.state.mu.Unlock()
	return claim()
}

func (q *mioDeltaTestQualification) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	q.state.mu.Lock()
	valid := q.state.valid
	q.state.mu.Unlock()
	if !valid {
		return nil
	}
	return &mioDeltaTestQualification{state: q.state}
}

func (q *mioDeltaTestQualification) Release() {
	q.mu.Lock()
	if q.released {
		q.mu.Unlock()
		return
	}
	q.released = true
	q.mu.Unlock()
	q.state.mu.Lock()
	q.state.release++
	q.state.mu.Unlock()
}

func (q *mioDeltaTestQualification) revoke() {
	q.state.mu.Lock()
	q.state.valid = false
	q.state.mu.Unlock()
}

func (q *mioDeltaTestQualification) useCount() int {
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	return q.state.uses
}

func (q *mioDeltaTestQualification) releaseCount() int {
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	return q.state.release
}

func testMioAnswerDelta() orchestrator.MioAnswerDelta {
	return orchestrator.MioAnswerDelta{
		TurnID: string(modulecore.NewTurnID()), TraceID: string(modulecore.NewTraceID()), TaskID: string(modulecore.NewTaskID()),
		RunID: string(modulecore.NewRunID()), SessionID: string(modulecore.NewSessionID()), MessageID: string(modulecore.NewMessageID()),
		Text: "partial answer",
	}
}
