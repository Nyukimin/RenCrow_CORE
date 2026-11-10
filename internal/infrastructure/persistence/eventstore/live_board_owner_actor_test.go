package eventstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
)

func createScopedBoardOwnerRequestForTest(t *testing.T, store *SQLiteStore, actorID string) (BoardConnectionPin, BoardOwnerRequestReservation, AcceptedTurn) {
	t.Helper()
	ctx := context.Background()
	pin := boardConnectionPinFixture()
	pin.PrincipalID = actorID
	if _, err := store.PinBoardConnectionForPrincipal(ctx, pin); err != nil {
		t.Fatalf("PinBoardConnectionForPrincipal(%s): %v", actorID, err)
	}
	input := boardOwnerRequestReservationFixture(pin)
	input.Source.TargetActorID = actorID
	input.Source.IdempotencyKey = "board-owner-request:scoped:" + actorID
	reservation, err := store.ReserveBoardOwnerRequest(ctx, input)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(%s): %v", actorID, err)
	}
	sessionID := seedBoardOwnerRequestSession(t, store, pin.OwnerID, input.ActorID)
	accepted, err := store.AcceptBoardOwnerRequest(ctx, BoardOwnerRequestAcceptInput{
		Reservation: reservation, ActorID: input.ActorID, Instruction: input.Instruction,
		SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
	})
	if err != nil {
		t.Fatalf("AcceptBoardOwnerRequest(%s): %v", actorID, err)
	}
	return pin, reservation, accepted
}

func TestReadPendingBoardOwnerRequestsFiltersResponderBeforePaging(t *testing.T) {
	ctx := context.Background()
	store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "board-owner-target-paging.sqlite"))
	createScopedBoardOwnerRequestForTest(t, store, "kuro")
	shiroPin, _, shiroAccepted := createScopedBoardOwnerRequestForTest(t, store, "shiro")
	scope := BoardPublicationScope{OwnerID: shiroPin.OwnerID, JournalInstanceID: shiroPin.JournalInstanceID, PrincipalID: shiroPin.PrincipalID}
	page, err := store.ReadPendingBoardOwnerRequestsForResponder(ctx, scope, "shiro", 0, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Accepted.TurnID != shiroAccepted.TurnID ||
		page.Items[0].Lookup.Source.TargetActorID != "shiro" {
		t.Fatalf("ReadPendingBoardOwnerRequestsForResponder(Shiro, limit=1) = %#v, err %v; want Shiro despite an earlier Kuro request", page, err)
	}
	if page.NextAdmissionSeq != shiroAccepted.AdmissionSeq {
		t.Fatalf("Shiro pending cursor = %d, want admission %d", page.NextAdmissionSeq, shiroAccepted.AdmissionSeq)
	}
}

func TestExplicitBoardOwnerRequestUsesActorPinAndStrictCanonicalMention(t *testing.T) {
	ctx := context.Background()
	store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "board-owner-explicit-shiro.sqlite"))
	shiroPin, reservation, accepted := createScopedBoardOwnerRequestForTest(t, store, "shiro")
	scope := BoardPublicationScope{OwnerID: shiroPin.OwnerID, JournalInstanceID: shiroPin.JournalInstanceID, PrincipalID: shiroPin.PrincipalID}
	if accepted.ActorID != boardOwnerRequestTestActorID {
		t.Fatalf("accepted.ActorID = %q, want the authenticated owner source actor %q", accepted.ActorID, boardOwnerRequestTestActorID)
	}
	if target, err := boardOwnerRequestSourceFromAcceptedEvent(accepted.InputEvent, shiroPin.OwnerID); err != nil || target.TargetActorID != "shiro" {
		t.Fatalf("accepted target actor = %#v, err %v; want durable Shiro binding", target, err)
	}
	lookup, commit := boardOwnerRequestCommitFixture(accepted, reservation, boardOwnerRequestTestBody, agent.InvocationReceipt{
		RequestID: "scoped-shiro-provider-request", ResponseID: "scoped-shiro-provider-response",
		ProviderLabel: "scoped-shiro-provider", TokensUsed: 11, Status: "completed",
	})
	journal, ok := any(store).(conversationruntime.BoardOwnerRequestJournal)
	if !ok {
		t.Fatal("SQLiteStore does not implement BoardOwnerRequestJournal")
	}
	committed, err := journal.CommitBoardOwnerRequest(ctx, commit)
	if err != nil || committed.Lookup != lookup || committed.Event.ActorID != "shiro" ||
		!reflect.DeepEqual(committed.Event.Payload["mentions"], []any{reservation.Source.PeerPrincipalID}) {
		t.Fatalf("CommitBoardOwnerRequest(Shiro) = %#v, err %v; want Shiro event with exactly the persisted peer mention", committed, err)
	}
	lease, available, err := store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: scope, ResponderActorID: "shiro", Now: reservation.AdmittedAt.Add(5 * time.Minute), LeaseDuration: time.Minute,
	})
	if err != nil || !available || lease.CanonicalResponse.ActorID != "shiro" {
		t.Fatalf("ClaimBoardPublication(Shiro new root) = available %t lease=%#v err %v", available, lease, err)
	}
	prepared := boardOwnerRequestRootAttemptBytes(lease, lease.Title, "", "")
	wire, err := decodeBoardPublicationPreparedWire(prepared)
	if err != nil || !reflect.DeepEqual(wire.Mentions, []string{reservation.Source.PeerPrincipalID}) {
		t.Fatalf("prepared Shiro BOARD mentions = %#v err %v; want exactly the persisted peer", wire.Mentions, err)
	}
	for _, badMentions := range [][]string{nil, {}, {"other-peer"}, {reservation.Source.PeerPrincipalID, "other-peer"}} {
		bad := boardOwnerRequestRootAttemptBytes(lease, lease.Title, "", "")
		var body map[string]any
		if err := json.Unmarshal(bad, &body); err != nil {
			t.Fatalf("decode prepared root request: %v", err)
		}
		if badMentions == nil {
			delete(body, "mentions")
		} else {
			body["mentions"] = badMentions
		}
		bad, _ = json.Marshal(body)
		if _, err := store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
			Lease: lease, Now: reservation.AdmittedAt.Add(5 * time.Minute), LogicalKey: lease.LogicalKey,
			PreparedBytes: bad, PayloadSHA256: boardPublicationPayloadHash(bad),
		}); err == nil {
			t.Fatalf("RecordBoardPublicationAttempt accepted mentions %#v for explicit Shiro root", badMentions)
		}
	}
}

func TestExplicitBoardOwnerRequestReadbackIsActorScopedAndReturnsReceipts(t *testing.T) {
	ctx := context.Background()
	store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "board-owner-explicit-status.sqlite"))
	legacyPin := pinBoardOwnerRequestConnection(t, store)
	shiroPin, _, accepted := createScopedBoardOwnerRequestForTest(t, store, "shiro")
	shiroScope := BoardPublicationScope{OwnerID: shiroPin.OwnerID, JournalInstanceID: shiroPin.JournalInstanceID, PrincipalID: shiroPin.PrincipalID}
	legacyScope := BoardPublicationScope{OwnerID: legacyPin.OwnerID, JournalInstanceID: legacyPin.JournalInstanceID, PrincipalID: legacyPin.PrincipalID}
	if _, found, err := store.ReadBoardOwnerRequestStatus(ctx, legacyScope, accepted.TurnID); err != nil || found {
		t.Fatalf("legacy ReadBoardOwnerRequestStatus exposed Shiro turn: found=%t err=%v", found, err)
	}
	legacyPage, err := store.ReadPendingBoardOwnerRequests(ctx, legacyScope, 0, 1)
	if err != nil || len(legacyPage.Items) != 0 {
		t.Fatalf("legacy ReadPendingBoardOwnerRequests exposed Shiro turn: page=%#v err=%v", legacyPage, err)
	}
	status, found, err := store.ReadBoardOwnerRequestStatusForResponder(ctx, shiroScope, accepted.TurnID, "shiro")
	if err != nil || !found || status.TargetActorID != "shiro" || status.Stage != BoardOwnerRequestStageAccepted {
		t.Fatalf("ReadBoardOwnerRequestStatusForResponder(accepted Shiro) = %#v found=%t err=%v", status, found, err)
	}
}

func TestExplicitBoardOwnerRequestStatusReturnsVerifiedPublishedIDsAndTerminalReplyAfterReopen(t *testing.T) {
	ctx := context.Background()
	fixture := newBoardOwnerRequestPublicationTestFixtureForResponder(t, "shiro")
	rootMessageID := "board-shiro-root-readback"
	rootThreadID := "thread-shiro-root-readback"
	root := completeNewRootPublicationForReadbackTest(t, fixture, fixture.lease, fixture.now, rootMessageID, rootThreadID)
	source := boardTerminalReplySourceForTest(fixture, rootMessageID)
	source.Binding.ResponderActorID = "shiro"
	source.Binding.MessageID = "board-peer-terminal-reply-shiro"
	source.Binding.ThreadID = rootThreadID
	source.CausationID = rootMessageID
	receipt, recorded, err := fixture.store.RecordBoardTerminalReply(ctx, source)
	if err != nil || !recorded || receipt.BoardMessageID != source.Binding.MessageID {
		t.Fatalf("RecordBoardTerminalReply(Shiro correlated reply) = %#v recorded=%t err=%v", receipt, recorded, err)
	}
	if err := fixture.reopen(t); err != nil {
		t.Fatalf("reopen before actor-scoped completed status read: %v", err)
	}
	status, found, err := fixture.store.ReadBoardOwnerRequestStatusForResponder(ctx, fixture.scope, fixture.lease.CanonicalResponse.TurnID, "shiro")
	if err != nil || !found || status.Stage != BoardOwnerRequestStageCompleted || status.TargetActorID != "shiro" ||
		status.BoardMessageID != root.CanonicalMessage.MessageID || status.BoardThreadID != root.CanonicalMessage.ThreadID || status.BoardReplyTo != "" ||
		status.ReplyMessageID != receipt.BoardMessageID {
		t.Fatalf("ReadBoardOwnerRequestStatusForResponder(after reopen) = %#v found=%t err=%v; want verified root IDs and correlated BOARD reply ID %q",
			status, found, err, receipt.BoardMessageID)
	}
}
