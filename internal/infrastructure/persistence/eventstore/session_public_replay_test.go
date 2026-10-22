package eventstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestReadSessionPublicMessagesPageReplaysOnlyOwnedPublicV2MessagesAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session-public-replay.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	input := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.user_message", testReplayTime(), map[string]any{
		"from": "user", "source_actor_id": "viewer-user", "to": "mio", "text": "user-visible input", "content": "user-visible input", "visibility": "public",
	})
	input.SessionID, input.TurnID, input.MessageID = sessionID, turnID, modulecore.NewMessageID()
	_, err = store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-session-public-replay", OwnerID: "owner-1", RuntimeVersion: "v2",
		ClientSubmissionID: "submission-session-public-replay", ActorID: "viewer-user", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: input,
		InputMessage: MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "viewer-user", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}

	otherSessionID, otherTurnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	otherInput := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.user_message", testReplayTime(), map[string]any{
		"from": "user", "source_actor_id": "viewer-user", "to": "mio", "text": "other owner input", "content": "other owner input", "visibility": "public",
	})
	otherInput.SessionID, otherInput.TurnID, otherInput.MessageID = otherSessionID, otherTurnID, modulecore.NewMessageID()
	if _, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-other-session-replay", OwnerID: "owner-2", RuntimeVersion: "v2",
		ClientSubmissionID: "submission-other-session-replay", ActorID: "viewer-user", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: otherInput,
		InputMessage: MessageIndex{
			MessageID: otherInput.MessageID, EventID: otherInput.EventID, SessionID: otherSessionID, TurnID: otherTurnID,
			FromActor: "viewer-user", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	}); err != nil {
		t.Fatalf("AcceptTurn(other owner): %v", err)
	}

	reply := modulecore.NewEventEnvelope(input.TraceID, input.EventID, nil, "orchestrator", "agent.response", testReplayTime(), map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "kind": "reply", "reply_to_message_id": string(input.MessageID), "text": "public answer", "content": "public answer", "visibility": "public",
	})
	reply.SessionID, reply.TurnID, reply.MessageID = sessionID, turnID, modulecore.NewMessageID()
	clarification := modulecore.NewEventEnvelope(input.TraceID, input.EventID, nil, "orchestrator", "agent.response", testReplayTime(), map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "kind": "clarification_prompt", "reply_to_message_id": string(input.MessageID), "text": "public question", "content": "public question", "visibility": "public",
	})
	clarification.SessionID, clarification.TurnID, clarification.MessageID = sessionID, turnID, modulecore.NewMessageID()
	private := modulecore.NewEventEnvelope(input.TraceID, input.EventID, nil, "conversation_runtime", "conversation.clarification_question", testReplayTime(), map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "shiro", "kind": "clarification_question", "text": "PRIVATE_SENTINEL", "content": "PRIVATE_SENTINEL", "visibility": "private",
	})
	private.SessionID, private.TurnID, private.MessageID = sessionID, turnID, modulecore.NewMessageID()
	private.ActorKind, private.ActorID = "agent", "mio"
	batch, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-session-public-replay", SessionID: sessionID, TurnID: turnID, GenerationEpoch: 1, ResultState: "completed",
		Events: []modulecore.EventEnvelope{reply, clarification, private},
		MessageIndexes: []MessageIndex{
			{MessageID: reply.MessageID, EventID: reply.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "user", ToActor: "owner-1", Kind: "reply", ReplyToMessageID: input.MessageID, Visibility: "public"},
			{MessageID: clarification.MessageID, EventID: clarification.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "user", ToActor: "owner-1", Kind: "clarification_prompt", ReplyToMessageID: input.MessageID, Visibility: "public"},
			{MessageID: private.MessageID, EventID: private.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "agent", ToActor: "shiro", Kind: "clarification_question", Visibility: "private"},
		},
	})
	if err != nil {
		t.Fatalf("CommitTurnBundle(): %v", err)
	}
	reply, clarification, private = batch.Events[0], batch.Events[1], batch.Events[2]
	state, found, err := store.ReadSessionState(ctx, sessionID, "owner-1")
	if err != nil || !found {
		t.Fatalf("ReadSessionState() found=%t err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	defer store.Close()

	first, through, err := store.ReadSessionPublicMessagesPage(ctx, sessionID, "owner-1", "viewer-user", state.CutoverSeq, 0, 2)
	if err != nil {
		t.Fatalf("ReadSessionPublicMessagesPage(first): %v", err)
	}
	if through <= state.CutoverSeq || len(first) != 2 || first[0].MessageID != input.MessageID || first[1].MessageID != reply.MessageID {
		t.Fatalf("first page=%#v through=%d cutover=%d, want input then reply and stable watermark", first, through, state.CutoverSeq)
	}
	second, sameThrough, err := store.ReadSessionPublicMessagesPage(ctx, sessionID, "owner-1", "viewer-user", first[1].EventSeq, through, 2)
	if err != nil {
		t.Fatalf("ReadSessionPublicMessagesPage(second): %v", err)
	}
	if sameThrough != through || len(second) != 1 || second[0].MessageID != clarification.MessageID || strings.Contains(string(second[0].Payload["content"].(string)), "PRIVATE_SENTINEL") {
		t.Fatalf("second page=%#v through=%d, want only public clarification prompt", second, sameThrough)
	}
	if _, _, err := store.ReadSessionPublicMessagesPage(ctx, sessionID, "owner-2", "viewer-user", state.CutoverSeq, 0, 10); err == nil {
		t.Fatal("wrong owner read returned v2 messages")
	}
	if _, _, err := store.ReadSessionPublicMessagesPage(ctx, otherSessionID, "owner-1", "viewer-user", state.CutoverSeq, 0, 10); err == nil {
		t.Fatal("cross-session read returned v2 messages")
	}
	if _, found, err := store.ReadSessionPublicMessageByEventID(ctx, sessionID, "owner-1", "viewer-user", clarification.EventID, clarification.EventSeq); err != nil || !found {
		t.Fatalf("owner-scoped public prompt live lookup found=%t err=%v", found, err)
	}
	if _, found, err := store.ReadSessionPublicMessageByEventID(ctx, sessionID, "owner-1", "viewer-user", private.EventID, private.EventSeq); err != nil || found {
		t.Fatalf("private question live lookup found=%t err=%v, want false", found, err)
	}
	if _, found, err := store.ReadSessionPublicMessageByEventID(ctx, sessionID, "owner-2", "viewer-user", clarification.EventID, clarification.EventSeq); err != nil || found {
		t.Fatalf("wrong-owner live lookup found=%t err=%v, want false", found, err)
	}
}

func testReplayTime() time.Time {
	return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
}
