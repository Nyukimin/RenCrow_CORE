package conversation

import (
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func projectionSource(seq int64, speaker, text string) ConversationProjectionSource {
	return ConversationProjectionSource{
		SessionID: modulecore.NewSessionID(), TurnID: modulecore.NewTurnID(), MessageID: modulecore.NewMessageID(),
		EventID: modulecore.NewEventID(), EventSeq: modulecore.EventSeq(seq), ActorID: speaker, Speaker: speaker,
		Text: text, OccurredAt: time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC), Visibility: "public",
	}
}

func TestPackConversationProjectionUnitKeepsInitialPairTogether(t *testing.T) {
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	user, answer := projectionSource(2, "user", "question"), projectionSource(3, "mio", "answer")
	user.SessionID, answer.SessionID = sessionID, sessionID
	user.TurnID, answer.TurnID = turnID, turnID
	result, err := PackConversationProjectionUnit(ConversationProjectionPackInput{
		NextSequence: 1, NewThreadID: modulecore.NewThreadID(), ThreadKind: modulecore.ThreadKindUserConversation,
		Sources: []ConversationProjectionSource{user, answer}, InitialPair: true,
	})
	if err != nil {
		t.Fatalf("PackConversationProjectionUnit(): %v", err)
	}
	if len(result.Items) != 2 || result.Active.MessageCount != 2 || result.Closed != nil {
		t.Fatalf("packed pair = items %d, active %#v, closed %#v", len(result.Items), result.Active, result.Closed)
	}
	if result.Items[0].ThreadID != result.Items[1].ThreadID || result.Items[0].ThreadSeq != 1 || result.Items[1].ThreadSeq != 2 {
		t.Fatalf("pair assignments = %#v, want same thread and consecutive sequence", result.Items)
	}
	if result.Items[0].ContentSHA256 == "" || result.Items[1].ContentSHA256 == "" || result.Items[0].ContentSHA256 == result.Items[1].ContentSHA256 {
		t.Fatalf("pair hashes = %q/%q, want distinct canonical hashes", result.Items[0].ContentSHA256, result.Items[1].ContentSHA256)
	}
}

func TestPackConversationProjectionUnitClosesOddThreadBeforePair(t *testing.T) {
	active := ConversationProjectionThread{ID: modulecore.NewThreadID(), Sequence: 1, MessageCount: 11}
	user, answer := projectionSource(20, "user", "question"), projectionSource(21, "mio", "answer")
	sessionID, turnID := user.SessionID, user.TurnID
	user.SessionID, answer.SessionID = sessionID, sessionID
	user.TurnID, answer.TurnID = turnID, turnID
	threadID := modulecore.NewThreadID()
	result, err := PackConversationProjectionUnit(ConversationProjectionPackInput{
		Active: active, NextSequence: 2, NewThreadID: threadID, ThreadKind: modulecore.ThreadKindUserConversation,
		Sources: []ConversationProjectionSource{user, answer}, InitialPair: true,
	})
	if err != nil {
		t.Fatalf("PackConversationProjectionUnit(): %v", err)
	}
	if result.Closed == nil || result.Closed.ID != active.ID || result.Closed.MessageCount != 11 || !result.Closed.Closed {
		t.Fatalf("closed odd thread = %#v, want old thread closed at 11", result.Closed)
	}
	if result.Active.ID != threadID || result.Active.Sequence != 2 || result.Active.MessageCount != 2 {
		t.Fatalf("new active thread = %#v, want two-message pair in next thread", result.Active)
	}
	if result.Items[0].ThreadID != threadID || result.Items[1].ThreadID != threadID || result.Items[1].ThreadSeq != 2 {
		t.Fatalf("pair split across boundary: %#v", result.Items)
	}
}

func TestPackConversationProjectionUnitRejectsMalformedOutputUnit(t *testing.T) {
	user, answer := projectionSource(40, "mio", "not a user input"), projectionSource(41, "mio", "answer")
	answer.SessionID, answer.TurnID = user.SessionID, user.TurnID
	if _, err := PackConversationProjectionUnit(ConversationProjectionPackInput{
		NextSequence: 1, NewThreadID: modulecore.NewThreadID(), ThreadKind: modulecore.ThreadKindUserConversation,
		Sources: []ConversationProjectionSource{user, answer}, InitialPair: true,
	}); err == nil {
		t.Fatal("initial pair without user input was accepted")
	}
}

func TestPackConversationProjectionUnitClosesAtTwelveAndRejectsPrivate(t *testing.T) {
	active := ConversationProjectionThread{ID: modulecore.NewThreadID(), Sequence: 3, MessageCount: 11}
	single := projectionSource(30, "mio", "readback")
	result, err := PackConversationProjectionUnit(ConversationProjectionPackInput{
		Active: active, ThreadKind: modulecore.ThreadKindUserConversation, Sources: []ConversationProjectionSource{single},
	})
	if err != nil {
		t.Fatalf("PackConversationProjectionUnit(single): %v", err)
	}
	if result.Closed == nil || result.Closed.MessageCount != 12 || result.Active.ID != "" || result.Items[0].ThreadSeq != 12 {
		t.Fatalf("exact boundary result = %#v, want closed at 12", result)
	}

	private := projectionSource(31, "mio", "secret report")
	private.Visibility = "private"
	if _, err := PackConversationProjectionUnit(ConversationProjectionPackInput{
		NextSequence: 1, NewThreadID: modulecore.NewThreadID(), ThreadKind: modulecore.ThreadKindUserConversation,
		Sources: []ConversationProjectionSource{private},
	}); err == nil {
		t.Fatal("private source was accepted into the public conversation projection")
	}
}

func TestPackConversationProjectionUnitAcceptsClarificationReplySingleton(t *testing.T) {
	source := projectionSource(30, "user", "Use the staging region.")
	source.ActorID = "viewer-user"
	threadID := modulecore.NewThreadID()
	result, err := PackConversationProjectionUnit(ConversationProjectionPackInput{
		Active:       ConversationProjectionThread{ID: threadID, Sequence: 2, MessageCount: 11},
		NextSequence: 3, NewThreadID: modulecore.NewThreadID(), ThreadKind: modulecore.ThreadKindUserConversation,
		Sources: []ConversationProjectionSource{source},
	})
	if err != nil {
		t.Fatalf("PackConversationProjectionUnit(clarification reply): %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Speaker != "user" || result.Items[0].ActorID != "viewer-user" ||
		result.Items[0].ThreadID != threadID || result.Items[0].ThreadSeq != 12 || result.Active.ID != "" ||
		result.Closed == nil || result.Closed.ID != threadID || result.Closed.MessageCount != 12 {
		t.Fatalf("clarification singleton assignment = %#v, want user item at message 12 closing the active thread", result)
	}
}
