package eventstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLiveReadAndCutoverAPIsExist(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	for _, name := range []string{
		"ReadLegacySessionBoundary",
		"CutoverSession",
		"ReadSessionState",
		"ReadCompleteSessionTail",
		"ReadLegacySessionProjection",
		"ReadPendingAcceptedTurns",
		"AdvanceProjectionWatermark",
	} {
		if !reflect.ValueOf(store).MethodByName(name).IsValid() {
			t.Errorf("eventstore API %s is missing", name)
		}
	}
}

func TestReadLegacySessionProjectionPreservesCanonicalIDsAndReportsMissingIDs(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	input := appendLegacyUserEvent(t, store, sessionID, "user-1", "legacy question")
	reply := eventFixture(modulecore.NewTraceID(), "agent.response", "orchestrator")
	reply.SessionID, reply.TurnID, reply.MessageID = sessionID, input.TurnID, modulecore.NewMessageID()
	reply.Payload = map[string]any{"from": "mio", "to": "user", "content": "legacy answer", "turn_index": 7}
	storedReply, err := store.AppendSequenced(ctx, reply)
	if err != nil {
		t.Fatalf("AppendSequenced(legacy reply): %v", err)
	}
	completeProjection, err := store.ReadLegacySessionProjection(ctx, sessionID, "user-1", storedReply.EventSeq)
	if err != nil {
		t.Fatalf("ReadLegacySessionProjection(complete): %v", err)
	}
	if !completeProjection.Complete || len(completeProjection.Gaps) != 0 || len(completeProjection.Messages) != 2 {
		t.Fatalf("complete legacy projection = %#v, want two messages and no gaps", completeProjection)
	}
	missing := eventFixture(modulecore.NewTraceID(), "conversation.user_message", "orchestrator")
	missing.SessionID = sessionID
	missing.ActorKind, missing.ActorID = "user", "user-1"
	missing.Payload = map[string]any{"from": "user", "to": "mio", "content": "uncorrelatable old message"}
	missing, err = store.AppendSequenced(ctx, missing)
	if err != nil {
		t.Fatalf("AppendSequenced(legacy message missing canonical ids): %v", err)
	}

	projection, err := store.ReadLegacySessionProjection(ctx, sessionID, "user-1", missing.EventSeq)
	if err != nil {
		t.Fatalf("ReadLegacySessionProjection(): %v", err)
	}
	if projection.AppliedThrough != missing.EventSeq || projection.Complete || len(projection.Gaps) != 1 {
		t.Fatalf("legacy projection watermark/complete/gaps = %d/%v/%#v, want through %d, incomplete, one gap", projection.AppliedThrough, projection.Complete, projection.Gaps, missing.EventSeq)
	}
	if projection.Gaps[0].EventSeq != missing.EventSeq || projection.Gaps[0].EventID != missing.EventID {
		t.Fatalf("legacy projection gap = %#v, want exact missing-ID event", projection.Gaps[0])
	}
	if len(projection.Messages) != 2 {
		t.Fatalf("legacy projection messages = %d, want two messages with canonical identities", len(projection.Messages))
	}
	if got := projection.Messages[0]; got.Index.MessageID != input.MessageID || got.Index.TurnID != input.TurnID || got.Index.FromActor != "user-1" || got.Index.ToKind != "agent" || got.Index.ToActor != "mio" || got.Index.Visibility != "public" || got.Text != "legacy question" {
		t.Fatalf("legacy input projection = %#v, want preserved identity and routing", got)
	}
	if got := projection.Messages[1]; got.Index.MessageID != storedReply.MessageID || got.Index.TurnID != storedReply.TurnID || got.Index.FromActor != "mio" || got.Index.ToKind != "user" || got.Index.ToActor != "user-1" || got.Index.Visibility != "public" || got.Text != "legacy answer" || got.TurnOrder != 7 {
		t.Fatalf("legacy reply projection = %#v, want preserved identity, routing, and turn order", got)
	}
	if _, err := store.ReadLegacySessionProjection(ctx, sessionID, "user-2", missing.EventSeq); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("ReadLegacySessionProjection(wrong owner) error = %v, want owner conflict", err)
	}
}

func TestReadLegacySessionProjectionForFreshV2SessionIsEmptyAndComplete(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	accepted := acceptLiveTurn(t, store, modulecore.NewSessionID(), "fresh-v2", true, 1)
	projection, err := store.ReadLegacySessionProjection(context.Background(), accepted.SessionID, "user-1", 0)
	if err != nil {
		t.Fatalf("ReadLegacySessionProjection(fresh v2): %v", err)
	}
	if !projection.Complete || projection.AppliedThrough != 0 || len(projection.Messages) != 0 || len(projection.Gaps) != 0 {
		t.Fatalf("fresh v2 legacy projection = %#v, want empty complete projection at zero", projection)
	}
}

func TestCutoverPinsExistingV1SessionAndLeavesOldEventsUnchanged(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	legacy := appendLegacyUserEvent(t, store, sessionID, "user-1", "legacy input")

	boundary, err := store.ReadLegacySessionBoundary(ctx, sessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadLegacySessionBoundary(): %v", err)
	}
	if boundary.ThroughSeq() != legacy.EventSeq {
		t.Fatalf("legacy boundary seq = %d, want %d", boundary.ThroughSeq(), legacy.EventSeq)
	}
	token, err := NewVerifiedIdleBoundaryToken(boundary)
	if err != nil {
		t.Fatalf("NewVerifiedIdleBoundaryToken(): %v", err)
	}
	state, err := store.CutoverSession(ctx, CutoverSessionInput{
		Boundary: token, RuntimeVersion: "v2", GenerationEpoch: 1,
	})
	if err != nil {
		t.Fatalf("CutoverSession(): %v", err)
	}
	if state.SessionID != sessionID || state.OwnerID != "user-1" || state.RuntimeVersion != "v2" || state.CutoverSeq <= legacy.EventSeq || state.ProjectionWatermark != state.CutoverSeq || state.GenerationEpoch != 1 {
		t.Fatalf("cutover state = %#v, want owner/version pinned after legacy event", state)
	}

	gotLegacy, found, err := store.GetByID(ctx, legacy.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID(legacy): found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(gotLegacy, legacy) {
		t.Fatalf("legacy event changed during cutover:\n got %#v\nwant %#v", gotLegacy, legacy)
	}
	markers, err := store.ListByComponent(ctx, "conversation_journal", 10)
	if err != nil {
		t.Fatalf("ListByComponent(conversation_journal): %v", err)
	}
	foundMarker := false
	for _, marker := range markers {
		if marker.EventType == "conversation.session.cutover" && marker.EventSeq == state.CutoverSeq {
			foundMarker = true
			if marker.ActorKind != "" || marker.ActorID != "" {
				t.Fatalf("cutover marker has execution identity: %#v", marker)
			}
		}
	}
	if !foundMarker {
		t.Fatalf("cutover marker not found in internal journal component: %#v", markers)
	}

	if _, _, err := store.ReadSessionState(ctx, sessionID, "user-2"); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("ReadSessionState(wrong owner) error = %v, want owner conflict", err)
	}
	readState, found, err := store.ReadSessionState(ctx, sessionID, "user-1")
	if err != nil || !found || readState != state {
		t.Fatalf("ReadSessionState(owner): state=%#v found=%v err=%v", readState, found, err)
	}

	accepted := acceptLiveTurn(t, store, sessionID, "after-cutover", false, 1)
	if accepted.AdmissionSeq <= state.CutoverSeq {
		t.Fatalf("accepted admission seq = %d, want after cutover %d", accepted.AdmissionSeq, state.CutoverSeq)
	}
	if repeated, err := store.CutoverSession(ctx, CutoverSessionInput{Boundary: token, RuntimeVersion: "v2", GenerationEpoch: 1}); err != nil || repeated.CutoverSeq != state.CutoverSeq {
		t.Fatalf("idempotent CutoverSession() = %#v, %v; want original cutover", repeated, err)
	}
}

func TestCutoverRejectsMissingOwnerAndStaleOrUnverifiedBoundary(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	legacy := appendLegacyUserEvent(t, store, sessionID, "user-1", "legacy input")

	if _, err := store.ReadLegacySessionBoundary(ctx, sessionID, "user-2"); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("ReadLegacySessionBoundary(wrong owner) error = %v, want owner conflict", err)
	}
	if _, err := store.CutoverSession(ctx, CutoverSessionInput{RuntimeVersion: "v2", GenerationEpoch: 1}); !errors.Is(err, ErrLegacySessionBoundaryConflict) {
		t.Fatalf("CutoverSession(zero token) error = %v, want boundary conflict", err)
	}
	boundary, err := store.ReadLegacySessionBoundary(ctx, sessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadLegacySessionBoundary(): %v", err)
	}
	token, err := NewVerifiedIdleBoundaryToken(boundary)
	if err != nil {
		t.Fatalf("NewVerifiedIdleBoundaryToken(): %v", err)
	}
	appendLegacyUserEvent(t, store, sessionID, "user-1", "late v1 event")
	if _, err := store.CutoverSession(ctx, CutoverSessionInput{Boundary: token, RuntimeVersion: "v2", GenerationEpoch: 1}); !errors.Is(err, ErrLegacySessionBoundaryConflict) {
		t.Fatalf("CutoverSession(stale boundary) error = %v, want boundary conflict", err)
	}
	if _, found, err := store.ReadSessionState(ctx, sessionID, "user-1"); err != nil || found {
		t.Fatalf("session after rejected stale cutover: found=%v err=%v, want no journal row", found, err)
	}
	if legacy.EventSeq <= 0 {
		t.Fatalf("legacy fixture seq = %d, want positive", legacy.EventSeq)
	}
}

func TestCutoverRejectsSessionAlreadyOwnedByV2Journal(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	accepted := acceptLiveTurn(t, store, sessionID, "first", true, 1)
	if _, err := store.ReadLegacySessionBoundary(ctx, sessionID, "user-1"); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("ReadLegacySessionBoundary(v2 session) error = %v, want session conflict", err)
	}
	if _, err := store.CutoverSession(ctx, CutoverSessionInput{RuntimeVersion: "v2", GenerationEpoch: 1}); !errors.Is(err, ErrLegacySessionBoundaryConflict) {
		t.Fatalf("CutoverSession(zero boundary) error = %v, want boundary conflict", err)
	}
	if accepted.State != "accepted" {
		t.Fatalf("accepted state = %q, want accepted", accepted.State)
	}
}

func TestReadCompleteSessionTailReturnsAllPagedIndexedMessagesAndVisibilityMetadata(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	appendLegacyUserEvent(t, store, sessionID, "user-1", "legacy input")
	boundary, err := store.ReadLegacySessionBoundary(ctx, sessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadLegacySessionBoundary(): %v", err)
	}
	token, err := NewVerifiedIdleBoundaryToken(boundary)
	if err != nil {
		t.Fatalf("NewVerifiedIdleBoundaryToken(): %v", err)
	}
	cutover, err := store.CutoverSession(ctx, CutoverSessionInput{Boundary: token, RuntimeVersion: "v2", GenerationEpoch: 1})
	if err != nil {
		t.Fatalf("CutoverSession(): %v", err)
	}
	const messageCount = liveMessageReadPageSize*2 + 1
	indexed := seedIndexedMessages(t, store, sessionID, "user-1", messageCount)

	tail, err := store.ReadCompleteSessionTail(ctx, sessionID, 0)
	if err != nil {
		t.Fatalf("ReadCompleteSessionTail(): %v", err)
	}
	if !tail.Complete || len(tail.Messages) != messageCount {
		t.Fatalf("tail complete=%v messages=%d, want true and all %d messages", tail.Complete, len(tail.Messages), messageCount)
	}
	if tail.ThroughSeq < indexed[len(indexed)-1].Event.EventSeq || tail.Messages[0].Event.EventSeq <= cutover.CutoverSeq {
		t.Fatalf("tail watermark/cutover range invalid: through=%d first=%d cutover=%d", tail.ThroughSeq, tail.Messages[0].Event.EventSeq, cutover.CutoverSeq)
	}
	privateCount := 0
	for index, message := range tail.Messages {
		if message.Event.EventSeq != indexed[index].Event.EventSeq || message.Index.MessageID != indexed[index].Index.MessageID {
			t.Fatalf("tail[%d] = seq %d message %s, want seq %d message %s", index, message.Event.EventSeq, message.Index.MessageID, indexed[index].Event.EventSeq, indexed[index].Index.MessageID)
		}
		if message.Index.Visibility == "private" {
			privateCount++
			if message.Index.ToKind != "agent" || message.Index.ToActor != "worker-1" {
				t.Fatalf("private tail index lost actor scope: %#v", message.Index)
			}
		}
	}
	if privateCount == 0 {
		t.Fatal("tail omitted private messages needed for caller-side visibility filtering")
	}

	after := indexed[liveMessageReadPageSize-1].Event.EventSeq
	page, err := store.ReadCompleteSessionTail(ctx, sessionID, after)
	if err != nil {
		t.Fatalf("ReadCompleteSessionTail(after): %v", err)
	}
	if !page.Complete || len(page.Messages) != messageCount-liveMessageReadPageSize {
		t.Fatalf("tail after seq %d complete=%v messages=%d, want %d complete messages", after, page.Complete, len(page.Messages), messageCount-liveMessageReadPageSize)
	}
}

func TestReadCompleteSessionTailFailsClosedOnUnindexedOrchestratorMessage(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	appendLegacyUserEvent(t, store, sessionID, "user-1", "legacy input")
	boundary, err := store.ReadLegacySessionBoundary(ctx, sessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadLegacySessionBoundary(): %v", err)
	}
	token, err := NewVerifiedIdleBoundaryToken(boundary)
	if err != nil {
		t.Fatalf("NewVerifiedIdleBoundaryToken(): %v", err)
	}
	if _, err := store.CutoverSession(ctx, CutoverSessionInput{Boundary: token, RuntimeVersion: "v2", GenerationEpoch: 1}); err != nil {
		t.Fatalf("CutoverSession(): %v", err)
	}
	unindexed := eventFixture(modulecore.NewTraceID(), "agent.response", "orchestrator")
	unindexed.SessionID, unindexed.TurnID, unindexed.MessageID = sessionID, modulecore.NewTurnID(), modulecore.NewMessageID()
	unindexed.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "missing index"}
	if _, err := store.AppendSequenced(ctx, unindexed); err != nil {
		t.Fatalf("AppendSequenced(unindexed event fixture): %v", err)
	}
	if _, err := store.ReadCompleteSessionTail(ctx, sessionID, 0); err == nil {
		t.Fatal("ReadCompleteSessionTail() error = nil, want fail-closed error for unindexed orchestrator message")
	}
}

func TestReadPendingAcceptedTurnsReturnsAcceptedAndAwaitingReadbackInAdmissionOrder(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	accepted := acceptLiveTurn(t, store, sessionID, "accepted", true, 1)
	delegated := acceptLiveTurn(t, store, sessionID, "delegated", false, 1)
	commitAwaitingReadback(t, store, delegated)
	completed := acceptLiveTurn(t, store, sessionID, "completed", false, 1)
	commitCompletedTurn(t, store, completed)

	turns, err := store.ReadPendingAcceptedTurns(ctx, sessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadPendingAcceptedTurns(): %v", err)
	}
	if len(turns) != 2 || turns[0].TurnID != accepted.TurnID || turns[0].State != "accepted" || turns[1].TurnID != delegated.TurnID || turns[1].State != "awaiting_readback" {
		t.Fatalf("recoverable turns = %#v, want accepted and awaiting_readback in admission order", turns)
	}
	for index, turn := range turns {
		if turn.InputEvent.MessageID != turn.InputMessageID || turn.InputEvent.EventSeq != turn.AdmissionSeq {
			t.Fatalf("recovery turn lost canonical input: %#v", turn)
		}
		wantSubmission := []string{"accepted", "delegated"}[index]
		if turn.ClientSubmissionID != wantSubmission {
			t.Fatalf("recovery client submission id = %q, want %q", turn.ClientSubmissionID, wantSubmission)
		}
	}
	if _, err := store.ReadPendingAcceptedTurns(ctx, sessionID, "user-2"); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("ReadPendingAcceptedTurns(wrong owner) error = %v, want owner conflict", err)
	}
}

func TestAdvanceProjectionWatermarkIsOwnerCheckedMonotonicAndSnapshotBounded(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	accepted := acceptLiveTurn(t, store, sessionID, "watermark", true, 1)
	if err := store.AdvanceProjectionWatermark(ctx, sessionID, "user-2", 0, accepted.AdmissionSeq); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("AdvanceProjectionWatermark(wrong owner) error = %v, want owner conflict", err)
	}
	if err := store.AdvanceProjectionWatermark(ctx, sessionID, "user-1", 0, accepted.AdmissionSeq); err != nil {
		t.Fatalf("AdvanceProjectionWatermark(success): %v", err)
	}
	state, found, err := store.ReadSessionState(ctx, sessionID, "user-1")
	if err != nil || !found || state.ProjectionWatermark != accepted.AdmissionSeq {
		t.Fatalf("projection watermark state=%#v found=%v err=%v", state, found, err)
	}
	if err := store.AdvanceProjectionWatermark(ctx, sessionID, "user-1", 0, accepted.AdmissionSeq+1); !errors.Is(err, ErrProjectionWatermarkConflict) {
		t.Fatalf("AdvanceProjectionWatermark(stale or beyond snapshot) error = %v, want watermark conflict", err)
	}
	if err := store.AdvanceProjectionWatermark(ctx, sessionID, "user-1", accepted.AdmissionSeq, accepted.AdmissionSeq-1); !errors.Is(err, ErrProjectionWatermarkConflict) {
		t.Fatalf("AdvanceProjectionWatermark(regression) error = %v, want watermark conflict", err)
	}
}

func appendLegacyUserEvent(t *testing.T, store *SQLiteStore, sessionID modulecore.SessionID, ownerID, text string) modulecore.EventEnvelope {
	t.Helper()
	event := eventFixture(modulecore.NewTraceID(), "conversation.user_message", "orchestrator")
	event.SessionID = sessionID
	event.TurnID = modulecore.NewTurnID()
	event.MessageID = modulecore.NewMessageID()
	event.ActorKind = "user"
	event.ActorID = ownerID
	event.Payload = map[string]any{"from": "user", "source_actor_id": ownerID, "to": "mio", "content": text, "text": text}
	stored, err := store.AppendSequenced(context.Background(), event)
	if err != nil {
		t.Fatalf("AppendSequenced(legacy): %v", err)
	}
	return stored
}

func acceptLiveTurn(t *testing.T, store *SQLiteStore, sessionID modulecore.SessionID, submissionID string, createNewSession bool, epoch int64) AcceptedTurn {
	t.Helper()
	event := liveInputEvent(sessionID, modulecore.NewTurnID(), submissionID)
	accepted, err := store.AcceptTurn(context.Background(), AcceptInput{
		OperationID: "accept-" + submissionID, OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: submissionID, ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: epoch, CreateNewSession: createNewSession, InputEvent: event,
		InputMessage: MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: sessionID, TurnID: event.TurnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(%s): %v", submissionID, err)
	}
	return accepted
}

func seedIndexedMessages(t *testing.T, store *SQLiteStore, sessionID modulecore.SessionID, ownerID string, count int) []CanonicalMessage {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin indexed message fixture: %v", err)
	}
	defer tx.Rollback()
	messages := make([]CanonicalMessage, 0, count)
	for index := 0; index < count; index++ {
		turnID := modulecore.NewTurnID()
		event := eventFixture(modulecore.NewTraceID(), "conversation.user_message", "orchestrator")
		fromActor, toKind, toActor, kind, visibility := ownerID, "agent", "mio", "user_message", "public"
		if index%19 == 0 {
			event.EventType, event.ComponentID = "conversation.delegation_instruction", "worker"
			event.ActorKind, event.ActorID = "agent", "mio"
			fromActor, toKind, toActor, kind, visibility = "mio", "agent", "worker-1", "delegation_instruction", "private"
			event.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "worker-1", "content": "indexed", "text": "indexed"}
		} else {
			event.Payload = map[string]any{"from": "user", "source_actor_id": ownerID, "to": "mio", "content": "indexed", "text": "indexed"}
		}
		event.SessionID, event.TurnID, event.MessageID = sessionID, turnID, modulecore.NewMessageID()
		event.EventSeq, err = nextEventSeq(ctx, tx)
		if err != nil {
			t.Fatalf("next indexed event sequence: %v", err)
		}
		if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
			t.Fatalf("ensure indexed event sequence: %v", err)
		}
		stored, err := insertLiveEvent(ctx, tx, event)
		if err != nil {
			t.Fatalf("insert indexed event: %v", err)
		}
		message := MessageIndex{
			MessageID: stored.MessageID, EventID: stored.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: fromActor, ToKind: toKind, ToActor: toActor, Kind: kind, Visibility: visibility,
		}
		if err := insertMessageIndex(ctx, tx, message); err != nil {
			t.Fatalf("insert indexed message: %v", err)
		}
		messages = append(messages, CanonicalMessage{Event: stored, Index: message})
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit indexed message fixture: %v", err)
	}
	return messages
}

func commitCompletedTurn(t *testing.T, store *SQLiteStore, accepted AcceptedTurn) {
	t.Helper()
	notice, message := liveOutputEvent(accepted, "mio", "user-1", "assistant_message", "public", "reply")
	if _, err := store.CommitTurnBundle(context.Background(), CommitInput{
		OperationID: "commit-completed-" + string(accepted.TurnID), SessionID: accepted.SessionID,
		TurnID: accepted.TurnID, GenerationEpoch: accepted.GenerationEpoch, ResultState: "completed",
		Events: []modulecore.EventEnvelope{notice}, MessageIndexes: []MessageIndex{message},
	}); err != nil {
		t.Fatalf("CommitTurnBundle(completed): %v", err)
	}
}

func commitAwaitingReadback(t *testing.T, store *SQLiteStore, accepted AcceptedTurn) {
	t.Helper()
	notice, noticeIndex := liveOutputEvent(accepted, "mio", "user-1", "assistant_message", "public", "handoff")
	instruction, instructionIndex := liveOutputEvent(accepted, "mio", "worker-1", "delegation_instruction", "private", "instruction")
	instruction.ComponentID = "worker"
	if _, err := store.CommitTurnBundle(context.Background(), CommitInput{
		OperationID: "commit-awaiting-" + string(accepted.TurnID), SessionID: accepted.SessionID,
		TurnID: accepted.TurnID, GenerationEpoch: accepted.GenerationEpoch, ResultState: "awaiting_readback",
		Events: []modulecore.EventEnvelope{notice, instruction}, MessageIndexes: []MessageIndex{noticeIndex, instructionIndex},
		Delegation: &DelegationRecord{
			DelegationID: "delegation-" + string(accepted.TurnID), Revision: 1, OriginTurnID: accepted.TurnID,
			Assignee: "worker-1", InstructionMessageID: instruction.MessageID,
			ContextEventID: accepted.InputEvent.EventID, State: "dispatch_accepted",
		},
		Outbox: []WorkItem{{
			WorkID: "work-" + string(accepted.TurnID), DedupeKey: "delegation-" + string(accepted.TurnID),
			Kind: "dispatch_delegation", SourceEventID: instruction.EventID, SessionID: accepted.SessionID,
		}},
	}); err != nil {
		t.Fatalf("CommitTurnBundle(awaiting readback): %v", err)
	}
}

func liveOutputEvent(accepted AcceptedTurn, actorID, toActor, kind, visibility, text string) (modulecore.EventEnvelope, MessageIndex) {
	event := eventFixture(accepted.InputEvent.TraceID, "agent.response", "orchestrator")
	if visibility == "private" {
		event.EventType, event.ComponentID = "conversation.delegation_instruction", "worker"
	}
	event.SessionID, event.TurnID, event.MessageID = accepted.SessionID, accepted.TurnID, modulecore.NewMessageID()
	event.Payload = map[string]any{"from": actorID, "source_actor_id": actorID, "to": toActor, "content": text, "text": text}
	if visibility == "private" {
		event.ActorKind, event.ActorID = "agent", actorID
	}
	toKind := "user"
	if visibility == "private" {
		toKind = "agent"
	}
	return event, MessageIndex{
		MessageID: event.MessageID, EventID: event.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: actorID, ToKind: toKind, ToActor: toActor, Kind: kind, Visibility: visibility,
	}
}
