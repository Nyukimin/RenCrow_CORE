package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestReadBoardOwnerRequestStatusTracksVerifiedStages(t *testing.T) {
	ctx := context.Background()
	store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "board-owner-status.sqlite"))
	pin := pinBoardOwnerRequestConnection(t, store)
	scope := BoardPublicationScope{OwnerID: pin.OwnerID, JournalInstanceID: pin.JournalInstanceID, PrincipalID: pin.PrincipalID}
	request := boardOwnerRequestReservationFixture(pin)
	reservation, err := store.ReserveBoardOwnerRequest(ctx, request)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(status fixture): %v", err)
	}
	sessionID := seedBoardOwnerRequestSession(t, store, pin.OwnerID, request.ActorID)
	accepted, err := store.AcceptBoardOwnerRequest(ctx, BoardOwnerRequestAcceptInput{
		Reservation: reservation, ActorID: request.ActorID, Instruction: request.Instruction,
		SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
	})
	if err != nil {
		t.Fatalf("AcceptBoardOwnerRequest(status fixture): %v", err)
	}

	status, found, err := store.ReadBoardOwnerRequestStatus(ctx, scope, accepted.TurnID)
	if err != nil || !found {
		t.Fatalf("ReadBoardOwnerRequestStatus(accepted): found %t, err %v", found, err)
	}
	if status.Scope != scope || status.OwnerID != pin.OwnerID || status.SessionID != accepted.SessionID ||
		status.TurnID != accepted.TurnID || status.TraceID != accepted.InputEvent.TraceID || status.InputMessageID != accepted.InputMessageID ||
		status.Stage != BoardOwnerRequestStageAccepted || !status.AcceptedAt.Equal(accepted.InputEvent.OccurredAt) ||
		!status.UpdatedAt.Equal(accepted.InputEvent.OccurredAt) {
		t.Fatalf("accepted status = %#v; want exact canonical IDs and acceptance time", status)
	}

	lookup, commit := boardOwnerRequestCommitFixture(accepted, reservation, request.Instruction, agent.InvocationReceipt{
		RequestID: "synthetic-status-request", ResponseID: "synthetic-status-response",
		ProviderLabel: "synthetic-provider", TokensUsed: 7, Status: "completed",
	})
	journal, ok := any(store).(conversationruntime.BoardOwnerRequestJournal)
	if !ok {
		t.Fatal("SQLiteStore does not implement BoardOwnerRequestJournal")
	}
	committed, err := journal.CommitBoardOwnerRequest(ctx, commit)
	if err != nil || committed.Lookup != lookup {
		t.Fatalf("CommitBoardOwnerRequest(status fixture) = %#v, err %v", committed, err)
	}
	status, found, err = store.ReadBoardOwnerRequestStatus(ctx, scope, accepted.TurnID)
	if err != nil || !found || status.Stage != BoardOwnerRequestStagePublicationPending ||
		!status.UpdatedAt.Equal(committed.Event.OccurredAt) {
		t.Fatalf("ReadBoardOwnerRequestStatus(after Mio commit) = %#v, found %t, err %v; want publication_pending at canonical Mio event time", status, found, err)
	}

	claimAt := request.Now.Add(5 * time.Minute)
	lease, available, err := store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: scope, Now: claimAt, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(status fixture) = available %t, err %v", available, err)
	}
	prepared := boardOwnerRequestRootAttemptBytes(lease, lease.Title, "", "")
	attempt, err := store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: claimAt, LogicalKey: lease.LogicalKey,
		PreparedBytes: prepared, PayloadSHA256: boardPublicationPayloadHash(prepared),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(status fixture): %v", err)
	}
	storedAt := claimAt.Add(time.Second)
	_, err = store.CompleteBoardPublication(ctx, BoardPublicationCompletionInput{
		Lease: lease, Now: storedAt, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		SendReceipt: BoardPublicationSendReceipt{
			MessageID: "board-owner-status-message", ThreadID: "board-owner-status-thread", StoredAt: storedAt, Status: "stored",
		},
		CanonicalMessage: BoardPublicationCanonicalMessage{
			MessageID: "board-owner-status-message", ThreadID: "board-owner-status-thread", ChannelID: lease.PublicationIntent.Target.ChannelID,
			SenderPrincipalID: lease.PublicationIntent.Target.CorePrincipalID,
			Recipients:        []string{lease.PublicationIntent.Target.PeerPrincipalID}, CausationID: "board-owner-status-message", HopCount: 0,
		},
	})
	if err != nil {
		t.Fatalf("CompleteBoardPublication(status fixture): %v", err)
	}
	status, found, err = store.ReadBoardOwnerRequestStatus(ctx, scope, accepted.TurnID)
	if err != nil || !found || status.Stage != BoardOwnerRequestStageCompleted || !status.UpdatedAt.Equal(storedAt) {
		t.Fatalf("ReadBoardOwnerRequestStatus(after verified BOARD receipt) = %#v, found %t, err %v; want completed at BOARD stored time", status, found, err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal redacted status: %v", err)
	}
	for _, privateValue := range []string{request.Instruction, boardOwnerRequestTestReply, lease.LeaseToken} {
		if strings.Contains(string(encoded), privateValue) {
			t.Fatalf("status readback leaked private value %q: %s", privateValue, encoded)
		}
	}
}

func TestReadBoardOwnerRequestStatusHidesUnknownScopeAndSurfacesCorruption(t *testing.T) {
	ctx := context.Background()
	store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "board-owner-status-corrupt.sqlite"))
	pin := pinBoardOwnerRequestConnection(t, store)
	scope := BoardPublicationScope{OwnerID: pin.OwnerID, JournalInstanceID: pin.JournalInstanceID, PrincipalID: pin.PrincipalID}
	request := boardOwnerRequestReservationFixture(pin)
	reservation, err := store.ReserveBoardOwnerRequest(ctx, request)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(status corruption fixture): %v", err)
	}
	sessionID := seedBoardOwnerRequestSession(t, store, pin.OwnerID, request.ActorID)
	accepted, err := store.AcceptBoardOwnerRequest(ctx, BoardOwnerRequestAcceptInput{
		Reservation: reservation, ActorID: request.ActorID, Instruction: request.Instruction,
		SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
	})
	if err != nil {
		t.Fatalf("AcceptBoardOwnerRequest(status corruption fixture): %v", err)
	}

	wrongScope := scope
	wrongScope.OwnerID = "other-owner"
	if _, found, err := store.ReadBoardOwnerRequestStatus(ctx, wrongScope, accepted.TurnID); err != nil || found {
		t.Fatalf("ReadBoardOwnerRequestStatus(wrong owner) = found %t, err %v; want indistinguishable not found", found, err)
	}
	if _, found, err := store.ReadBoardOwnerRequestStatus(ctx, scope, modulecore.NewTurnID()); err != nil || found {
		t.Fatalf("ReadBoardOwnerRequestStatus(unknown turn) = found %t, err %v; want not found", found, err)
	}

	operationID := boardOwnerRequestOutputOperationID(accepted.TurnID)
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO live_operation_receipt(operation_id,operation_kind,input_sha256,result_json,created_at)
		VALUES(?,?,?,?,?)`, operationID, "invalid_status_receipt", strings.Repeat("0", 64), `{}`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed malformed synthetic status receipt: %v", err)
	}
	if _, found, err := store.ReadBoardOwnerRequestStatus(ctx, scope, accepted.TurnID); found || !errors.Is(err, ErrBoardOwnerRequestStatusCorrupt) {
		t.Fatalf("ReadBoardOwnerRequestStatus(corrupt outcome receipt) = found %t, err %v; want corruption, not pending or absence", found, err)
	}
}
