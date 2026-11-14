package eventstore

import (
	"context"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestListRoutinePublicResponseFactsReturnsOnlyVerifiedPublicMetadata(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	input := liveInputEvent(sessionID, turnID, "private input must not be returned")
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-routine-facts", OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "routine-facts-submission", ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: input,
		InputMessage: MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn: %v", err)
	}

	response, responseIndex := liveOutputEvent(accepted, "mio", "user-1", "assistant_message", "public", "private response body must not be returned")
	// The production agent.response payload addresses the user kind; the
	// owner-specific recipient remains in MessageIndex.ToActor.
	response.Payload["to"] = "user"
	if _, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-routine-facts", SessionID: sessionID, TurnID: turnID, GenerationEpoch: 1,
		ResultState: "completed", Events: []modulecore.EventEnvelope{response},
		MessageIndexes: []MessageIndex{responseIndex},
	}); err != nil {
		t.Fatalf("CommitTurnBundle: %v", err)
	}

	from := response.OccurredAt.Add(-time.Second)
	through := response.OccurredAt.Add(time.Second)
	facts, err := store.ListRoutinePublicResponseFacts(ctx, "mio", "user-1", from, through)
	if err != nil {
		t.Fatalf("ListRoutinePublicResponseFacts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts = %#v, want one verified public response", facts)
	}
	got := facts[0]
	if got.EventID != string(response.EventID) || got.MessageID != string(response.MessageID) || got.TurnID != string(turnID) ||
		got.ActorID != "mio" || !got.OccurredAt.Equal(response.OccurredAt) {
		t.Fatalf("fact metadata = %#v, want exact canonical response metadata", got)
	}
	for _, scope := range []struct {
		actorID string
		ownerID string
		name    string
	}{
		{actorID: "shiro", ownerID: "user-1", name: "different actor"},
		{actorID: "mio", ownerID: "user-2", name: "different owner"},
	} {
		mismatched, err := store.ListRoutinePublicResponseFacts(ctx, scope.actorID, scope.ownerID, from, through)
		if err != nil {
			t.Fatalf("ListRoutinePublicResponseFacts(%s): %v", scope.name, err)
		}
		if len(mismatched) != 0 {
			t.Fatalf("%s scope returned facts = %#v, want none", scope.name, mismatched)
		}
	}

	outside, err := store.ListRoutinePublicResponseFacts(ctx, "mio", "user-1", from.Add(2*time.Second), through.Add(2*time.Second))
	if err != nil {
		t.Fatalf("ListRoutinePublicResponseFacts(outside range): %v", err)
	}
	if len(outside) != 0 {
		t.Fatalf("outside-range facts = %#v, want none", outside)
	}
}

func TestListRoutinePublicResponseFactsRejectsInvalidScope(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	if _, err := store.ListRoutinePublicResponseFacts(context.Background(), "mio", "user-1", time.Time{}, time.Now()); err == nil {
		t.Fatal("accepted zero lower boundary")
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.ListRoutinePublicResponseFacts(context.Background(), "mio", "user-1", from, from.Add(26*time.Hour)); err == nil {
		t.Fatal("accepted unbounded date range")
	}
}

func TestListRoutineBoardPublicationFactsRequiresStoredReplyAndExactScope(t *testing.T) {
	fixture := newBoardPublicationDeliveryFixtureForResponder(t, "shiro", true)
	ctx := context.Background()
	from, through := fixture.now.Add(-time.Second), fixture.now.Add(time.Second)

	pending, err := fixture.store.ListRoutineBoardPublicationFacts(ctx, "shiro", fixture.scope.OwnerID, from, through)
	if err != nil {
		t.Fatalf("ListRoutineBoardPublicationFacts(before stored receipt): %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending BOARD facts = %#v, want none before stored receipt", pending)
	}

	completePeerReplyPublicationForReadbackTest(t, fixture)
	facts, err := fixture.store.ListRoutineBoardPublicationFacts(ctx, "shiro", fixture.scope.OwnerID, from, through)
	if err != nil {
		t.Fatalf("ListRoutineBoardPublicationFacts(after stored receipt): %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("BOARD publication facts = %#v, want one stored public reply", facts)
	}
	fact := facts[0]
	response := fixture.bundle.CanonicalEvents[0]
	if fact.EventID != string(response.EventID) || fact.MessageID != string(response.MessageID) || fact.TurnID != string(fixture.accepted.TurnID) ||
		fact.ActorID != "shiro" || fact.BoardMessageID == "" || !fact.OccurredAt.Equal(response.OccurredAt) || !fact.StoredAt.Equal(fixture.now) {
		t.Fatalf("BOARD metadata = %#v, want exact canonical response and stored receipt metadata", fact)
	}

	for _, scope := range []struct {
		actorID string
		ownerID string
		name    string
	}{
		{actorID: "mio", ownerID: fixture.scope.OwnerID, name: "different actor"},
		{actorID: "shiro", ownerID: fixture.scope.OwnerID + "-other", name: "different owner"},
	} {
		mismatched, err := fixture.store.ListRoutineBoardPublicationFacts(ctx, scope.actorID, scope.ownerID, from, through)
		if err != nil {
			t.Fatalf("ListRoutineBoardPublicationFacts(%s): %v", scope.name, err)
		}
		if len(mismatched) != 0 {
			t.Fatalf("%s scope returned BOARD facts = %#v, want none", scope.name, mismatched)
		}
	}

	responseFrom := response.OccurredAt.Add(-time.Second)
	responseThrough := response.OccurredAt.Add(time.Second)
	ordinary, err := fixture.store.ListRoutinePublicResponseFacts(ctx, "shiro", fixture.scope.OwnerID, responseFrom, responseThrough)
	if err != nil {
		t.Fatalf("ListRoutinePublicResponseFacts(external BOARD reply): %v", err)
	}
	if len(ordinary) != 0 {
		t.Fatalf("ordinary conversation facts = %#v, want no overlap with BOARD reply facts", ordinary)
	}
}
