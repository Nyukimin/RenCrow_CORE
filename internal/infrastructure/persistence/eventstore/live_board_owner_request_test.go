package eventstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	boardOwnerRequestTestActorID = "core-owner-actor-alpha"
	boardOwnerRequestTestBody    = "Ask the peer to confirm the synthetic maintenance window."
	boardOwnerRequestTestReply   = "Would you confirm the synthetic maintenance window?"
	boardOwnerRequestTestKey     = "client-request-key-42"
)

func boardOwnerRequestReservationFixture(pin BoardConnectionPin) BoardOwnerRequestReservationInput {
	return BoardOwnerRequestReservationInput{
		Source: conversationruntime.BoardOwnerRequestSource{
			OwnerID: pin.OwnerID, BoardInstanceID: pin.JournalInstanceID, CorePrincipalID: pin.PrincipalID,
			ChannelID: "channel-alpha", PeerPrincipalID: "peer-principal-alpha",
			Title: "Synthetic maintenance window", IdempotencyKey: boardOwnerRequestTestKey,
		},
		ActorID: boardOwnerRequestTestActorID, Instruction: boardOwnerRequestTestBody,
		Now: time.Date(2026, 11, 5, 3, 15, 0, 0, time.UTC),
	}
}

func newBoardOwnerRequestStore(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if store != nil {
			if err := store.Close(); err != nil {
				t.Errorf("Close(): %v", err)
			}
		}
	})
	return store
}

func pinBoardOwnerRequestConnection(t *testing.T, store *SQLiteStore) BoardConnectionPin {
	t.Helper()
	pin := boardConnectionPinFixture()
	if _, err := store.PinBoardConnection(context.Background(), pin); err != nil {
		t.Fatalf("PinBoardConnection(synthetic owner pin): %v", err)
	}
	return pin
}

func TestReserveBoardOwnerRequestPersistsFingerprintAndOriginalAdmissionTime(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "board-owner-reservation.sqlite")
	store := newBoardOwnerRequestStore(t, databasePath)
	pin := pinBoardOwnerRequestConnection(t, store)
	input := boardOwnerRequestReservationFixture(pin)

	first, err := store.ReserveBoardOwnerRequest(ctx, input)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(first): %v", err)
	}
	if first.Source.OwnerID != pin.OwnerID || first.Source.BoardInstanceID != pin.JournalInstanceID ||
		first.Source.CorePrincipalID != pin.PrincipalID || first.ActorID != input.ActorID {
		t.Fatalf("reservation scope = %#v, want exact authenticated owner and durable BOARD pin", first)
	}
	if first.Source.IdempotencyKey == "" || first.Source.IdempotencyKey == input.Source.IdempotencyKey ||
		first.OperationID == "" || first.OperationID == input.Source.IdempotencyKey {
		t.Fatalf("reservation key/operation id are not server-namespaced: %#v", first)
	}
	for name, value := range map[string]string{
		"input hash": first.InputSHA256, "instruction hash": first.InstructionSHA256,
	} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size {
			t.Fatalf("reservation %s = %q, want a SHA-256 digest", name, value)
		}
	}
	if !first.AdmittedAt.Equal(input.Now) || first.AdmittedAt.Location() != time.UTC {
		t.Fatalf("reservation admission time = %s (%s), want original UTC %s", first.AdmittedAt, first.AdmittedAt.Location(), input.Now)
	}

	var operationKind, resultJSON string
	var receiptInputHash string
	if err := store.db.QueryRowContext(ctx, `SELECT operation_kind,input_sha256,result_json FROM live_operation_receipt WHERE operation_id=?`, first.OperationID).
		Scan(&operationKind, &receiptInputHash, &resultJSON); err != nil {
		t.Fatalf("read immutable reservation receipt: %v", err)
	}
	if operationKind != boardOwnerRequestReservationOperationKind || receiptInputHash == "" || len(receiptInputHash) != sha256.Size*2 {
		t.Fatalf("reservation operation receipt metadata = %q/%q, want typed receipt and input digest", operationKind, receiptInputHash)
	}
	if strings.Contains(resultJSON, input.Instruction) || strings.Contains(resultJSON, input.Source.IdempotencyKey) {
		t.Fatal("reservation receipt duplicated the instruction or raw client key")
	}
	var acceptedReceiptCount, turnCount, eventCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_kind='accept_turn'`).Scan(&acceptedReceiptCount); err != nil {
		t.Fatalf("count accepted receipts after reservation: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_conversation_turn`).Scan(&turnCount); err != nil {
		t.Fatalf("count accepted turns after reservation: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_envelope`).Scan(&eventCount); err != nil {
		t.Fatalf("count events after reservation: %v", err)
	}
	if acceptedReceiptCount != 0 || turnCount != 0 || eventCount != 0 {
		t.Fatalf("reservation created accepted state: receipts=%d turns=%d events=%d", acceptedReceiptCount, turnCount, eventCount)
	}

	retry := input
	retry.Now = input.Now.Add(36 * time.Hour)
	replayed, err := store.ReserveBoardOwnerRequest(ctx, retry)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(same request after day rollover): %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("reservation retry = %#v, want exact original reservation %#v", replayed, first)
	}

	conflicts := []struct {
		name   string
		mutate func(*BoardOwnerRequestReservationInput)
	}{
		{name: "title", mutate: func(value *BoardOwnerRequestReservationInput) { value.Source.Title += " changed" }},
		{name: "channel", mutate: func(value *BoardOwnerRequestReservationInput) { value.Source.ChannelID = "channel-other" }},
		{name: "peer", mutate: func(value *BoardOwnerRequestReservationInput) { value.Source.PeerPrincipalID = "peer-principal-other" }},
		{name: "instruction", mutate: func(value *BoardOwnerRequestReservationInput) { value.Instruction += " changed" }},
		{name: "authenticated actor", mutate: func(value *BoardOwnerRequestReservationInput) { value.ActorID = "core-owner-actor-other" }},
	}
	for _, test := range conflicts {
		t.Run("changed request/"+test.name, func(t *testing.T) {
			changed := input
			test.mutate(&changed)
			if _, err := store.ReserveBoardOwnerRequest(ctx, changed); !errors.Is(err, ErrBoardOwnerRequestConflict) {
				t.Fatalf("ReserveBoardOwnerRequest(changed %s) error = %v, want ErrBoardOwnerRequestConflict", test.name, err)
			}
		})
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close(before restart replay): %v", err)
	}
	store = nil
	reopened := newBoardOwnerRequestStore(t, databasePath)
	retry.Now = input.Now.Add(72 * time.Hour)
	reloaded, err := reopened.ReserveBoardOwnerRequest(ctx, retry)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(after restart and day rollover): %v", err)
	}
	if !reflect.DeepEqual(reloaded, first) {
		t.Fatalf("reservation after restart = %#v, want exact original %#v", reloaded, first)
	}
}

func TestAcceptBoardOwnerRequestAndJournalPersistOneCanonicalNewRootOutcome(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "board-owner-request.sqlite")
	store := newBoardOwnerRequestStore(t, databasePath)
	pin := pinBoardOwnerRequestConnection(t, store)
	reservationInput := boardOwnerRequestReservationFixture(pin)
	reservation, err := store.ReserveBoardOwnerRequest(ctx, reservationInput)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(): %v", err)
	}
	sessionID := seedBoardOwnerRequestSession(t, store, pin.OwnerID, reservationInput.ActorID)
	acceptInput := BoardOwnerRequestAcceptInput{
		Reservation: reservation, ActorID: reservationInput.ActorID, Instruction: reservationInput.Instruction,
		SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
	}
	accepted, err := store.AcceptBoardOwnerRequest(ctx, acceptInput)
	if err != nil {
		t.Fatalf("AcceptBoardOwnerRequest(exact owner session): %v", err)
	}
	assertAcceptedBoardOwnerRequest(t, store, ctx, accepted, reservation, reservationInput)

	journal, ok := any(store).(conversationruntime.BoardOwnerRequestJournal)
	if !ok {
		t.Fatal("SQLiteStore does not implement the frozen BoardOwnerRequestJournal port")
	}
	lookup, commit := boardOwnerRequestCommitFixture(accepted, reservation, reservationInput.Instruction, agent.InvocationReceipt{
		RequestID: "synthetic-provider-request", ResponseID: "synthetic-provider-response",
		ProviderLabel: "synthetic-provider", TokensUsed: 11, Status: "completed",
	})
	if _, found, err := journal.LoadBoardOwnerRequestOutcome(ctx, lookup); err != nil || found {
		t.Fatalf("LoadBoardOwnerRequestOutcome(exact pending scope) = found %t, err %v; want absence without error", found, err)
	}
	wrongLookup := lookup
	wrongLookup.Source.Title += " other"
	if _, found, err := journal.LoadBoardOwnerRequestOutcome(ctx, wrongLookup); found || !errors.Is(err, ErrBoardOwnerRequestScopeMismatch) {
		t.Fatalf("LoadBoardOwnerRequestOutcome(wrong source scope) = found %t, err %v; want typed scope rejection", found, err)
	}
	if _, _, err := store.LoadCommittedOutcome(ctx, accepted.TurnID); !errors.Is(err, conversationruntime.ErrExternalTurnJournalRequired) {
		t.Fatalf("generic LoadCommittedOutcome(board owner turn) error = %v, want typed journal refusal", err)
	}
	if _, err := store.CommitTurnBundle(ctx, genericUserOutputForAcceptedTurn(accepted)); !errors.Is(err, conversationruntime.ErrExternalTurnJournalRequired) {
		t.Fatalf("generic CommitTurnBundle(board owner turn) error = %v, want typed journal refusal", err)
	}

	committed, err := journal.CommitBoardOwnerRequest(ctx, commit)
	if err != nil {
		t.Fatalf("CommitBoardOwnerRequest(valid generated Mio response): %v", err)
	}
	if committed.Lookup != lookup || committed.InvocationReceipt != commit.InvocationReceipt ||
		committed.Target != commit.Target || committed.IdempotencyKey != reservation.Source.IdempotencyKey ||
		committed.Event.EventSeq <= 0 || committed.Event.Payload["text"] == reservationInput.Instruction {
		t.Fatalf("committed BOARD owner request outcome = %#v, want exact generated response, receipt, and unresolved target", committed)
	}
	if committed.Target != (conversationruntime.NewBoardThreadTarget{
		BoardInstanceID: reservation.Source.BoardInstanceID, CorePrincipalID: reservation.Source.CorePrincipalID,
		ChannelID: reservation.Source.ChannelID, PeerPrincipalID: reservation.Source.PeerPrincipalID, Title: reservation.Source.Title,
	}) {
		t.Fatalf("committed new-root target = %#v, want accepted owner target", committed.Target)
	}

	var targetKind, title, threadID, replyTo, workKind, workDedupe string
	var publicationCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT p.target_kind,p.title,p.thread_id,p.reply_to,w.kind,w.dedupe_key,COUNT(*) OVER()
		FROM live_board_publication p JOIN live_work_outbox w ON w.work_id=p.work_id
		WHERE p.response_message_id=?`, string(committed.MessageIndex.MessageID)).
		Scan(&targetKind, &title, &threadID, &replyTo, &workKind, &workDedupe, &publicationCount); err != nil {
		t.Fatalf("read new-root BOARD publication target/work: %v", err)
	}
	if targetKind != "new_root" || title != reservation.Source.Title || threadID != "" || replyTo != "" ||
		workKind != "board_publication" || workDedupe == "" || publicationCount != 1 {
		t.Fatalf("new-root publication target/work = %q %q thread=%q reply=%q kind=%q dedupe=%q count=%d",
			targetKind, title, threadID, replyTo, workKind, workDedupe, publicationCount)
	}
	var turnState, resultReceiptID string
	if err := store.db.QueryRowContext(ctx, `SELECT state,result_receipt_id FROM live_conversation_turn WHERE turn_id=?`, string(accepted.TurnID)).
		Scan(&turnState, &resultReceiptID); err != nil {
		t.Fatalf("read committed owner turn: %v", err)
	}
	if turnState != "completed" || resultReceiptID != lookup.OperationID {
		t.Fatalf("committed owner turn state/receipt = %q/%q, want completed/%q", turnState, resultReceiptID, lookup.OperationID)
	}
	var receiptJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT result_json FROM live_operation_receipt WHERE operation_id=?`, lookup.OperationID).Scan(&receiptJSON); err != nil {
		t.Fatalf("read immutable output operation receipt: %v", err)
	}
	if strings.Contains(receiptJSON, boardOwnerRequestTestReply) || strings.Contains(receiptJSON, reservationInput.Instruction) {
		t.Fatal("typed output receipt duplicated a canonical event body")
	}

	replayed, err := journal.CommitBoardOwnerRequest(ctx, commit)
	if err != nil || !reflect.DeepEqual(replayed, committed) {
		t.Fatalf("CommitBoardOwnerRequest(exact retry) = %#v, %v; want exact original outcome", replayed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(before typed journal reopen): %v", err)
	}
	store = nil
	reopened := newBoardOwnerRequestStore(t, databasePath)
	reopenedJournal, ok := any(reopened).(conversationruntime.BoardOwnerRequestJournal)
	if !ok {
		t.Fatal("reopened SQLiteStore does not implement BoardOwnerRequestJournal")
	}
	reloaded, found, err := reopenedJournal.LoadBoardOwnerRequestOutcome(ctx, lookup)
	if err != nil || !found || !reflect.DeepEqual(reloaded, committed) {
		t.Fatalf("LoadBoardOwnerRequestOutcome(after restart) = %#v, found %t, err %v; want exact durable outcome", reloaded, found, err)
	}
	changedCommit := commit
	changedCommit.Event = cloneMutableEventForBoardOwnerRequest(commit.Event)
	changedCommit.Event.Payload["text"] = "Changed generated response under the same owner key."
	changedCommit.Event.Payload["content"] = "Changed generated response under the same owner key."
	if _, err := reopenedJournal.CommitBoardOwnerRequest(ctx, changedCommit); !errors.Is(err, ErrBoardOwnerRequestConflict) {
		t.Fatalf("CommitBoardOwnerRequest(changed result under same operation) error = %v, want conflict", err)
	}
	if _, _, err := reopened.LoadCommittedOutcome(ctx, accepted.TurnID); !errors.Is(err, conversationruntime.ErrExternalTurnJournalRequired) {
		t.Fatalf("generic LoadCommittedOutcome(after owner commit) error = %v, want typed journal refusal", err)
	}
}

func TestAcceptBoardOwnerRequestCreatesFreshSessionFromFirstAcceptedEvent(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "board-owner-fresh-session.sqlite")
	store := newBoardOwnerRequestStore(t, databasePath)
	pin := pinBoardOwnerRequestConnection(t, store)
	request := boardOwnerRequestReservationFixture(pin)
	reservation, err := store.ReserveBoardOwnerRequest(ctx, request)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(fresh-session owner request): %v", err)
	}
	sessionID := modulecore.NewSessionID()
	assertFreshBoardSessionAbsent(t, store, sessionID)
	input := BoardOwnerRequestAcceptInput{
		Reservation: reservation, ActorID: request.ActorID, Instruction: request.Instruction,
		SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
		CreateNewSession: true,
	}
	withoutFreshMarker := input
	withoutFreshMarker.CreateNewSession = false
	if _, err := store.AcceptBoardOwnerRequest(ctx, withoutFreshMarker); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("AcceptBoardOwnerRequest(missing fresh-session marker) error = %v, want session conflict", err)
	}
	assertFreshBoardSessionAbsent(t, store, sessionID)

	accepted, err := store.AcceptBoardOwnerRequest(ctx, input)
	if err != nil {
		t.Fatalf("AcceptBoardOwnerRequest(first canonical owner event creates fresh session): %v", err)
	}
	assertAcceptedBoardOwnerRequest(t, store, ctx, accepted, reservation, request)
	var ownerID, runtimeVersion string
	var admissionSeq, generationEpoch, sessionEvents int64
	if err := store.db.QueryRowContext(ctx, `
		SELECT owner_id,runtime_version,admission_seq,epoch,
		       (SELECT COUNT(*) FROM event_envelope WHERE json_extract(envelope_json,'$.session_id')=?)
		FROM live_conversation_session WHERE session_id=?`, string(sessionID), string(sessionID)).Scan(
		&ownerID, &runtimeVersion, &admissionSeq, &generationEpoch, &sessionEvents,
	); err != nil {
		t.Fatalf("read atomically created owner session and first event: %v", err)
	}
	if ownerID != pin.OwnerID || runtimeVersion != conversationruntime.ConversationRuntimeV2 ||
		admissionSeq != int64(accepted.AdmissionSeq) || generationEpoch != input.GenerationEpoch || sessionEvents != 1 {
		t.Fatalf("fresh owner session = owner %q runtime %q admission %d epoch %d events %d; want exact first accepted event", ownerID, runtimeVersion, admissionSeq, generationEpoch, sessionEvents)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close(before fresh owner acceptance replay): %v", err)
	}
	store = nil
	reopened := newBoardOwnerRequestStore(t, databasePath)
	resolverReplay := input
	resolverReplay.CreateNewSession = false
	replayed, err := reopened.AcceptBoardOwnerRequest(ctx, resolverReplay)
	if err != nil || !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("AcceptBoardOwnerRequest(exact retry after resolver observes existing session) = %#v, err %v; want original acceptance", replayed, err)
	}
	assertFreshBoardSessionCount(t, reopened, sessionID, 1)

	secondRequest := request
	secondRequest.Source.IdempotencyKey = boardOwnerRequestTestKey + "-second-session-use"
	secondReservation, err := reopened.ReserveBoardOwnerRequest(ctx, secondRequest)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(second distinct operation): %v", err)
	}
	reusedSession := resolverReplay
	reusedSession.Reservation = secondReservation
	reusedSession.CreateNewSession = true
	if _, err := reopened.AcceptBoardOwnerRequest(ctx, reusedSession); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("AcceptBoardOwnerRequest(reuse of accepted fresh session as new) error = %v, want session conflict", err)
	}
	wrongEpoch := resolverReplay
	wrongEpoch.Reservation = secondReservation
	wrongEpoch.GenerationEpoch++
	if _, err := reopened.AcceptBoardOwnerRequest(ctx, wrongEpoch); !errors.Is(err, ErrConversationSessionConflict) {
		t.Fatalf("AcceptBoardOwnerRequest(existing session with wrong epoch) error = %v, want session conflict", err)
	}
	wrongOwner := resolverReplay
	wrongOwner.Reservation.Source.OwnerID = "owner-other"
	if _, err := reopened.AcceptBoardOwnerRequest(ctx, wrongOwner); !errors.Is(err, ErrBoardOwnerRequestPinMismatch) {
		t.Fatalf("AcceptBoardOwnerRequest(fresh binding with wrong owner) error = %v, want durable pin mismatch", err)
	}
	assertFreshBoardSessionCount(t, reopened, sessionID, 1)
}

func assertFreshBoardSessionAbsent(t *testing.T, store *SQLiteStore, sessionID modulecore.SessionID) {
	t.Helper()
	assertFreshBoardSessionCount(t, store, sessionID, 0)
}

func assertFreshBoardSessionCount(t *testing.T, store *SQLiteStore, sessionID modulecore.SessionID, expected int) {
	t.Helper()
	var sessionCount, eventCount int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM live_conversation_session WHERE session_id=?`, string(sessionID)).Scan(&sessionCount); err != nil {
		t.Fatalf("count fresh CORE session row: %v", err)
	}
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM event_envelope WHERE json_extract(envelope_json,'$.session_id')=?`, string(sessionID)).Scan(&eventCount); err != nil {
		t.Fatalf("count fresh CORE session events: %v", err)
	}
	if sessionCount != expected || eventCount != expected {
		t.Fatalf("fresh CORE session/event counts = %d/%d; want %d/%d", sessionCount, eventCount, expected, expected)
	}
}

func TestBoardOwnerRequestPublicationPersistsAndCompletesNewRootWithoutClaimProof(t *testing.T) {
	t.Run("new root rejects mismatched title", func(t *testing.T) {
		fixture := newBoardOwnerRequestPublicationTestFixture(t)
		prepared := boardOwnerRequestRootAttemptBytes(fixture.lease, fixture.lease.Title+" changed", "claim-owner-root-title", "synthetic-private-claim-receipt-title")
		_, err := fixture.store.RecordBoardPublicationAttempt(context.Background(), BoardPublicationAttemptInput{
			Lease: fixture.lease, Now: fixture.now, LogicalKey: fixture.lease.LogicalKey,
			PreparedBytes: prepared, PayloadSHA256: boardPublicationPayloadHash(prepared),
			ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
		})
		if !errors.Is(err, ErrBoardPublicationAttemptConflict) {
			t.Errorf("RecordBoardPublicationAttempt(new_root with changed title) error = %v, want attempt conflict", err)
		}
		assertNoBoardPublicationAttempts(t, fixture.store, fixture.lease.WorkID)
	})

	t.Run("new root rejects processing claim proof", func(t *testing.T) {
		fixture := newBoardOwnerRequestPublicationTestFixture(t)
		prepared := boardOwnerRequestRootAttemptBytes(fixture.lease, fixture.lease.Title, "claim-owner-root-forbidden", "synthetic-private-claim-receipt-forbidden")
		_, err := fixture.store.RecordBoardPublicationAttempt(context.Background(), BoardPublicationAttemptInput{
			Lease: fixture.lease, Now: fixture.now, LogicalKey: fixture.lease.LogicalKey,
			PreparedBytes: prepared, PayloadSHA256: boardPublicationPayloadHash(prepared),
			ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
		})
		if !errors.Is(err, ErrBoardPublicationAttemptConflict) {
			t.Errorf("RecordBoardPublicationAttempt(new_root with peer claim proof) error = %v, want attempt conflict", err)
		}
		assertNoBoardPublicationAttempts(t, fixture.store, fixture.lease.WorkID)
	})

	t.Run("exact proofless root attempt survives restart and completes", func(t *testing.T) {
		ctx := context.Background()
		fixture := newBoardOwnerRequestPublicationTestFixture(t)
		prepared := boardOwnerRequestRootAttemptBytes(fixture.lease, fixture.lease.Title, "", "")
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(prepared, &fields); err != nil {
			t.Fatalf("decode exact proofless new-root attempt: %v", err)
		}
		for _, forbidden := range []string{"thread_id", "reply_to", "processing_claim_id", "processing_claim_receipt"} {
			if _, exists := fields[forbidden]; exists {
				t.Fatalf("proofless new-root request unexpectedly includes %q", forbidden)
			}
		}
		attempt, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
			Lease: fixture.lease, Now: fixture.now, LogicalKey: fixture.lease.LogicalKey,
			PreparedBytes: prepared, PayloadSHA256: boardPublicationPayloadHash(prepared),
		})
		if err != nil {
			t.Fatalf("RecordBoardPublicationAttempt(exact new-root request without claim): %v", err)
		}
		if attempt.WorkID != fixture.lease.WorkID || attempt.Ordinal != 1 || attempt.LogicalKey != fixture.lease.LogicalKey ||
			attempt.PayloadSHA256 != boardPublicationPayloadHash(prepared) || !bytes.Equal(attempt.PreparedBytes, prepared) ||
			attempt.ProcessingClaimID != "" || attempt.ProcessingClaimReceiptSHA256 != boardPublicationPayloadHash(nil) ||
			!attempt.ProcessingClaimLeaseUntil.IsZero() {
			t.Fatalf("stored proofless new-root attempt = %#v; want exact bytes and no claim metadata", attempt)
		}

		if err := fixture.reopen(t); err != nil {
			t.Fatalf("reopen EventStore with unresolved new-root attempt: %v", err)
		}
		reclaimAt := fixture.lease.LeaseExpiresAt.Add(time.Second)
		reclaimed, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
			Scope: fixture.scope, Now: reclaimAt, LeaseDuration: time.Minute,
		})
		if err != nil || !available || len(reclaimed.Attempts) != 1 || !reflect.DeepEqual(reclaimed.Attempts[0], attempt) {
			t.Fatalf("ClaimBoardPublication(reclaim exact root attempt) = %#v, available %t, err %v; want same unresolved bytes", reclaimed, available, err)
		}
		replayed, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
			Lease: reclaimed, Now: reclaimAt, LogicalKey: attempt.LogicalKey,
			PreparedBytes: append([]byte(nil), attempt.PreparedBytes...), PayloadSHA256: attempt.PayloadSHA256,
		})
		if err != nil || !reflect.DeepEqual(replayed, attempt) {
			t.Fatalf("RecordBoardPublicationAttempt(exact proofless replay) = %#v, err %v; want immutable original attempt", replayed, err)
		}

		messageID, threadID := "board-owner-request-message-alpha", "board-owner-request-thread-alpha"
		sendReceipt := BoardPublicationSendReceipt{
			MessageID: messageID, ThreadID: threadID, StoredAt: reclaimAt.Add(time.Second), Status: "stored",
		}
		canonical := BoardPublicationCanonicalMessage{
			MessageID: messageID, ThreadID: threadID, ChannelID: fixture.lease.PublicationIntent.Target.ChannelID,
			SenderPrincipalID: fixture.lease.PublicationIntent.Target.CorePrincipalID,
			Recipients:        []string{fixture.lease.PublicationIntent.Target.PeerPrincipalID},
			CausationID:       messageID, HopCount: 0,
		}
		outcome, err := fixture.store.CompleteBoardPublication(ctx, BoardPublicationCompletionInput{
			Lease: reclaimed, Now: reclaimAt, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
			SendReceipt: sendReceipt, CanonicalMessage: canonical,
		})
		if err != nil {
			t.Fatalf("CompleteBoardPublication(canonical BOARD root self-cause and hop zero): %v", err)
		}
		if outcome.SendReceipt != sendReceipt || !reflect.DeepEqual(outcome.CanonicalMessage, canonical) ||
			outcome.ExpectedPeerPrincipalID != fixture.lease.PublicationIntent.Target.PeerPrincipalID {
			t.Fatalf("completed new-root outcome = %#v; want exact BOARD receipt, root cause, and peer", outcome)
		}
		loaded, found, err := fixture.store.LoadBoardPublicationByParentMessage(ctx, BoardPublicationLookupInput{
			Scope: fixture.scope, ParentMessageID: messageID,
		})
		if err != nil || !found || !reflect.DeepEqual(loaded, outcome) {
			t.Fatalf("LoadBoardPublicationByParentMessage(completed new root) = %#v, found %t, err %v; want durable root outcome", loaded, found, err)
		}
	})
}

type boardOwnerRequestPublicationTestFixture struct {
	store        *SQLiteStore
	databasePath string
	scope        BoardPublicationScope
	lease        BoardPublicationLease
	now          time.Time
}

func newBoardOwnerRequestPublicationTestFixture(t *testing.T) *boardOwnerRequestPublicationTestFixture {
	return newBoardOwnerRequestPublicationTestFixtureForResponder(t, "")
}

func newBoardOwnerRequestPublicationTestFixtureForResponder(t *testing.T, responderActorID string) *boardOwnerRequestPublicationTestFixture {
	t.Helper()
	ctx := context.Background()
	fixture := &boardOwnerRequestPublicationTestFixture{databasePath: filepath.Join(t.TempDir(), "board-owner-publication.sqlite")}
	var err error
	fixture.store, err = NewSQLiteStore(fixture.databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(owner publication fixture): %v", err)
	}
	t.Cleanup(func() {
		if fixture.store != nil {
			if err := fixture.store.Close(); err != nil {
				t.Errorf("Close(owner publication fixture): %v", err)
			}
		}
	})
	pin := boardConnectionPinFixture()
	if responderActorID != "" && responderActorID != "mio" {
		pin.PrincipalID = responderActorID
		if _, err := fixture.store.PinBoardConnectionForPrincipal(ctx, pin); err != nil {
			t.Fatalf("PinBoardConnectionForPrincipal(%s owner publication fixture): %v", responderActorID, err)
		}
	} else if _, err := fixture.store.PinBoardConnection(ctx, pin); err != nil {
		t.Fatalf("PinBoardConnection(owner publication fixture): %v", err)
	}
	reservationInput := boardOwnerRequestReservationFixture(pin)
	if responderActorID != "" {
		reservationInput.Source.TargetActorID = responderActorID
		reservationInput.Source.IdempotencyKey += ":" + responderActorID
	}
	reservation, err := fixture.store.ReserveBoardOwnerRequest(ctx, reservationInput)
	if err != nil {
		t.Fatalf("ReserveBoardOwnerRequest(owner publication fixture): %v", err)
	}
	sessionID := seedBoardOwnerRequestSession(t, fixture.store, pin.OwnerID, reservationInput.ActorID)
	accepted, err := fixture.store.AcceptBoardOwnerRequest(ctx, BoardOwnerRequestAcceptInput{
		Reservation: reservation, ActorID: reservationInput.ActorID, Instruction: reservationInput.Instruction,
		SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
	})
	if err != nil {
		t.Fatalf("AcceptBoardOwnerRequest(owner publication fixture): %v", err)
	}
	assertAcceptedBoardOwnerRequest(t, fixture.store, ctx, accepted, reservation, reservationInput)
	journal, ok := any(fixture.store).(conversationruntime.BoardOwnerRequestJournal)
	if !ok {
		t.Fatal("SQLiteStore does not implement BoardOwnerRequestJournal in owner publication fixture")
	}
	lookup, commit := boardOwnerRequestCommitFixture(accepted, reservation, reservationInput.Instruction, agent.InvocationReceipt{
		RequestID: "synthetic-provider-request-root", ResponseID: "synthetic-provider-response-root",
		ProviderLabel: "synthetic-provider", TokensUsed: 11, Status: "completed",
	})
	committed, err := journal.CommitBoardOwnerRequest(ctx, commit)
	if err != nil {
		t.Fatalf("CommitBoardOwnerRequest(owner publication fixture): %v", err)
	}
	if committed.Lookup != lookup || committed.Event.Payload["text"] == reservationInput.Instruction {
		t.Fatalf("owner publication fixture outcome = %#v; want exact committed generated result", committed)
	}
	fixture.scope = BoardPublicationScope{OwnerID: pin.OwnerID, JournalInstanceID: pin.JournalInstanceID, PrincipalID: pin.PrincipalID}
	fixture.now = reservationInput.Now.Add(5 * time.Minute)
	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, ResponderActorID: responderActorID, Now: fixture.now, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(owner request fixture) = available %t, err %v", available, err)
	}
	fixture.lease = lease
	if fixture.lease.TargetKind != "new_root" || fixture.lease.Title != reservation.Source.Title ||
		fixture.lease.PublicationIntent.Target.ThreadID != "" || fixture.lease.PublicationIntent.Target.ReplyTo != "" ||
		len(fixture.lease.Attempts) != 0 {
		t.Fatalf("claimed owner publication lease = %#v; want pinned unresolved new-root target", fixture.lease)
	}
	return fixture
}

func (fixture *boardOwnerRequestPublicationTestFixture) reopen(t *testing.T) error {
	t.Helper()
	if err := fixture.store.Close(); err != nil {
		return err
	}
	fixture.store = nil
	store, err := NewSQLiteStore(fixture.databasePath)
	if err != nil {
		return err
	}
	fixture.store = store
	return nil
}

func boardOwnerRequestRootAttemptBytes(lease BoardPublicationLease, title, claimID, claimReceipt string) []byte {
	wire := map[string]any{
		"channel_id": lease.PublicationIntent.Target.ChannelID, "title": title,
		"body":       payloadString(lease.CanonicalResponse.Payload, "text"),
		"recipients": []string{lease.PublicationIntent.Target.PeerPrincipalID}, "kind": "request",
		"idempotency_key": lease.LogicalKey,
	}
	if mentions := lease.CanonicalResponse.Payload["mentions"]; mentions != nil {
		wire["mentions"] = []string{lease.PublicationIntent.Target.PeerPrincipalID}
	}
	if claimID != "" {
		wire["processing_claim_id"] = claimID
	}
	if claimReceipt != "" {
		wire["processing_claim_receipt"] = claimReceipt
	}
	prepared, _ := json.Marshal(wire)
	return prepared
}

func assertNoBoardPublicationAttempts(t *testing.T, store *SQLiteStore, workID string) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM live_board_publication_attempt WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatalf("count attempts after rejected new-root variant: %v", err)
	}
	if count != 0 {
		t.Errorf("rejected new-root variant persisted %d attempt(s), want none", count)
	}
}

func TestAcceptBoardOwnerRequestRejectsWrongPinSessionActorAndCorruptReservation(t *testing.T) {
	t.Run("reservation requires the exact current pin", func(t *testing.T) {
		store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "missing-pin.sqlite"))
		input := boardOwnerRequestReservationFixture(boardConnectionPinFixture())
		if _, err := store.ReserveBoardOwnerRequest(context.Background(), input); !errors.Is(err, ErrBoardOwnerRequestPinMismatch) {
			t.Fatalf("ReserveBoardOwnerRequest(without pin) error = %v, want pin mismatch", err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*conversationruntime.BoardOwnerRequestSource)
	}{
		{name: "owner", mutate: func(source *conversationruntime.BoardOwnerRequestSource) { source.OwnerID = "owner-other" }},
		{name: "BOARD instance", mutate: func(source *conversationruntime.BoardOwnerRequestSource) {
			source.BoardInstanceID = "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "CORE principal", mutate: func(source *conversationruntime.BoardOwnerRequestSource) { source.CorePrincipalID = "principal-other" }},
	} {
		t.Run("reservation pin mismatch/"+test.name, func(t *testing.T) {
			store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "wrong-pin.sqlite"))
			pin := pinBoardOwnerRequestConnection(t, store)
			input := boardOwnerRequestReservationFixture(pin)
			test.mutate(&input.Source)
			if _, err := store.ReserveBoardOwnerRequest(context.Background(), input); !errors.Is(err, ErrBoardOwnerRequestPinMismatch) {
				t.Fatalf("ReserveBoardOwnerRequest(wrong %s) error = %v, want pin mismatch", test.name, err)
			}
		})
	}

	t.Run("accept checks reservation actor and existing session owner", func(t *testing.T) {
		store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "wrong-session.sqlite"))
		pin := pinBoardOwnerRequestConnection(t, store)
		request := boardOwnerRequestReservationFixture(pin)
		reservation, err := store.ReserveBoardOwnerRequest(context.Background(), request)
		if err != nil {
			t.Fatalf("ReserveBoardOwnerRequest(): %v", err)
		}
		sessionID := seedBoardOwnerRequestSession(t, store, pin.OwnerID, request.ActorID)
		wrongActor := BoardOwnerRequestAcceptInput{
			Reservation: reservation, ActorID: "core-owner-actor-other", Instruction: request.Instruction,
			SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
		}
		if _, err := store.AcceptBoardOwnerRequest(context.Background(), wrongActor); !errors.Is(err, ErrBoardOwnerRequestScopeMismatch) {
			t.Fatalf("AcceptBoardOwnerRequest(wrong authenticated actor) error = %v, want scope mismatch", err)
		}
		wrongSessionID := seedBoardOwnerRequestSession(t, store, "owner-other", request.ActorID)
		wrongOwnerSession := wrongActor
		wrongOwnerSession.ActorID, wrongOwnerSession.SessionID = request.ActorID, wrongSessionID
		if _, err := store.AcceptBoardOwnerRequest(context.Background(), wrongOwnerSession); !errors.Is(err, ErrConversationSessionConflict) {
			t.Fatalf("AcceptBoardOwnerRequest(session owned by another CORE owner) error = %v, want session conflict", err)
		}
		wrongBody := wrongActor
		wrongBody.ActorID, wrongBody.Instruction = request.ActorID, request.Instruction+" changed"
		if _, err := store.AcceptBoardOwnerRequest(context.Background(), wrongBody); !errors.Is(err, ErrBoardOwnerRequestConflict) {
			t.Fatalf("AcceptBoardOwnerRequest(changed body after reservation) error = %v, want request conflict", err)
		}
	})

	t.Run("malformed reservation receipt is corruption, not absence", func(t *testing.T) {
		store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "corrupt-reservation.sqlite"))
		pin := pinBoardOwnerRequestConnection(t, store)
		request := boardOwnerRequestReservationFixture(pin)
		operationID, _, err := boardOwnerRequestReservationIdentity(request.Source)
		if err != nil {
			t.Fatalf("derive synthetic reservation operation ID: %v", err)
		}
		inputHash, err := boardOwnerRequestReservationInputHash(request)
		if err != nil {
			t.Fatalf("hash synthetic reservation input: %v", err)
		}
		if _, err := store.db.ExecContext(context.Background(), `
			INSERT INTO live_operation_receipt (operation_id,operation_kind,input_sha256,result_json,created_at)
			VALUES (?,?,?,?,?)`, operationID, boardOwnerRequestReservationOperationKind, inputHash, "{malformed",
			time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("seed malformed synthetic reservation receipt: %v", err)
		}
		if _, err := store.ReserveBoardOwnerRequest(context.Background(), request); !errors.Is(err, ErrBoardOwnerRequestCorrupt) {
			t.Fatalf("ReserveBoardOwnerRequest(corrupted receipt) error = %v, want corruption error", err)
		}
	})
}

func TestCommitBoardOwnerRequestRejectsIncompleteInvocationReceipt(t *testing.T) {
	for _, test := range []struct {
		name    string
		receipt agent.InvocationReceipt
	}{
		{name: "request id", receipt: agent.InvocationReceipt{ResponseID: "response", ProviderLabel: "synthetic", Status: "completed"}},
		{name: "response id", receipt: agent.InvocationReceipt{RequestID: "request", ProviderLabel: "synthetic", Status: "completed"}},
		{name: "status", receipt: agent.InvocationReceipt{RequestID: "request", ResponseID: "response", ProviderLabel: "synthetic", Status: "pending"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newBoardOwnerRequestStore(t, filepath.Join(t.TempDir(), "invalid-receipt.sqlite"))
			pin := pinBoardOwnerRequestConnection(t, store)
			request := boardOwnerRequestReservationFixture(pin)
			reservation, err := store.ReserveBoardOwnerRequest(ctx, request)
			if err != nil {
				t.Fatalf("ReserveBoardOwnerRequest(): %v", err)
			}
			sessionID := seedBoardOwnerRequestSession(t, store, pin.OwnerID, request.ActorID)
			accepted, err := store.AcceptBoardOwnerRequest(ctx, BoardOwnerRequestAcceptInput{
				Reservation: reservation, ActorID: request.ActorID, Instruction: request.Instruction,
				SessionID: sessionID, RuntimeVersion: conversationruntime.ConversationRuntimeV2, GenerationEpoch: 1,
			})
			if err != nil {
				t.Fatalf("AcceptBoardOwnerRequest(): %v", err)
			}
			lookup, commit := boardOwnerRequestCommitFixture(accepted, reservation, request.Instruction, test.receipt)
			journal, ok := any(store).(conversationruntime.BoardOwnerRequestJournal)
			if !ok {
				t.Fatal("SQLiteStore does not implement BoardOwnerRequestJournal")
			}
			if _, err := journal.CommitBoardOwnerRequest(ctx, commit); !errors.Is(err, ErrBoardOwnerRequestInvalidOutcome) {
				t.Fatalf("CommitBoardOwnerRequest(invalid %s) error = %v, want invalid outcome", test.name, err)
			}
			if _, found, err := journal.LoadBoardOwnerRequestOutcome(ctx, lookup); err != nil || found {
				t.Fatalf("LoadBoardOwnerRequestOutcome(after rejected receipt) = found %t, err %v; want absent", found, err)
			}
			var workCount int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_work_outbox WHERE kind='board_publication'`).Scan(&workCount); err != nil {
				t.Fatalf("count publication work after rejected invocation receipt: %v", err)
			}
			if workCount != 0 {
				t.Fatalf("invalid invocation receipt created %d publication work items", workCount)
			}
			var state string
			if err := store.db.QueryRowContext(ctx, `SELECT state FROM live_conversation_turn WHERE turn_id=?`, string(accepted.TurnID)).Scan(&state); err != nil {
				t.Fatalf("read turn after rejected invocation receipt: %v", err)
			}
			if state != "accepted" {
				t.Fatalf("turn state after rejected invocation receipt = %q, want accepted", state)
			}
		})
	}
}

func seedBoardOwnerRequestSession(t *testing.T, store *SQLiteStore, ownerID, actorID string) modulecore.SessionID {
	t.Helper()
	ctx := context.Background()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	inputEvent := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.user_message",
		time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC), map[string]any{
			"from": "user", "source_actor_id": actorID, "to": "mio", "text": "Synthetic canonical session seed.", "content": "Synthetic canonical session seed.",
		})
	inputEvent.SessionID, inputEvent.TurnID, inputEvent.MessageID = sessionID, turnID, modulecore.NewMessageID()
	_, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "seed-board-owner-session:" + string(sessionID), OwnerID: ownerID, RuntimeVersion: conversationruntime.ConversationRuntimeV2,
		ClientSubmissionID: "seed-board-owner-submission:" + string(turnID), ActorID: actorID,
		TriggerKind: "user_message", GenerationEpoch: 1, CreateNewSession: true, InputEvent: inputEvent,
		InputMessage: MessageIndex{
			MessageID: inputEvent.MessageID, EventID: inputEvent.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: actorID, ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(seed existing canonical owner session): %v", err)
	}
	return sessionID
}

func assertAcceptedBoardOwnerRequest(t *testing.T, store *SQLiteStore, ctx context.Context, accepted AcceptedTurn, reservation BoardOwnerRequestReservation, input BoardOwnerRequestReservationInput) {
	t.Helper()
	event := accepted.InputEvent
	if accepted.OperationID == "" || accepted.ClientSubmissionID == "" || accepted.ActorID != input.ActorID || accepted.SessionID != event.SessionID ||
		accepted.TriggerKind != "board_owner_request" || accepted.State != "accepted" || accepted.GenerationEpoch != 1 ||
		accepted.InputMessageID != event.MessageID || accepted.AdmissionSeq != event.EventSeq || event.EventSeq <= 0 ||
		event.EventType != "conversation.board_owner_request" || event.ComponentID != "orchestrator" || event.ActorKind != "" || event.ActorID != "" ||
		!event.OccurredAt.Equal(reservation.AdmittedAt) {
		t.Fatalf("accepted owner request identity/provenance = %#v, want exact reserved public owner input", accepted)
	}
	if len(event.Payload) != 7 || event.Payload["from"] != "board_owner" || event.Payload["source_actor_id"] != input.ActorID || event.Payload["to"] != "mio" ||
		event.Payload["text"] != input.Instruction || event.Payload["content"] != input.Instruction {
		t.Fatalf("accepted owner payload = %#v, want exact owner instruction and source", event.Payload)
	}
	body, ok := event.Payload["body"].(map[string]any)
	if !ok || body["text"] != input.Instruction {
		t.Fatalf("accepted owner body = %#v, want exact instruction", event.Payload["body"])
	}
	binding, ok := event.Payload["board_owner_request_binding"].(map[string]any)
	wantBindingLength := 6
	if reservation.Source.TargetActorID != "" {
		wantBindingLength = 7
	}
	if !ok || len(binding) != wantBindingLength || binding["board_instance_id"] != reservation.Source.BoardInstanceID ||
		binding["core_principal_id"] != reservation.Source.CorePrincipalID || binding["channel_id"] != reservation.Source.ChannelID ||
		binding["peer_principal_id"] != reservation.Source.PeerPrincipalID || binding["title"] != reservation.Source.Title ||
		binding["idempotency_key"] != reservation.Source.IdempotencyKey ||
		reservation.Source.TargetActorID != "" && binding["target_actor_id"] != reservation.Source.TargetActorID {
		t.Fatalf("accepted typed BOARD target binding = %#v, want exact pinned source and namespaced key", event.Payload["board_owner_request_binding"])
	}
	for _, coreID := range []string{string(event.SessionID), string(event.TurnID), string(event.MessageID), string(event.EventID), string(event.TraceID)} {
		for _, boardRef := range []string{reservation.Source.BoardInstanceID, reservation.Source.CorePrincipalID, reservation.Source.ChannelID, reservation.Source.PeerPrincipalID} {
			if coreID == boardRef {
				t.Fatalf("CORE-generated identity %q reused BOARD reference %q", coreID, boardRef)
			}
		}
	}
	var indexEventID, indexSessionID, indexTurnID, fromActor, toKind, toActor, kind, visibility string
	var replyTo sql.NullString
	if err := store.db.QueryRowContext(ctx, `
		SELECT event_id,session_id,turn_id,from_actor,to_kind,to_actor,kind,reply_to_message_id,visibility
		FROM live_message_index WHERE message_id=?`, string(accepted.InputMessageID)).Scan(
		&indexEventID, &indexSessionID, &indexTurnID, &fromActor, &toKind, &toActor, &kind, &replyTo, &visibility,
	); err != nil {
		t.Fatalf("read accepted owner input index: %v", err)
	}
	if indexEventID != string(event.EventID) || indexSessionID != string(event.SessionID) || indexTurnID != string(event.TurnID) ||
		fromActor != "board_owner" || toKind != "agent" || toActor != "mio" || kind != "board_owner_request" ||
		replyTo.Valid || visibility != "public" {
		t.Fatalf("accepted owner request index = %q/%q/%q %q->%s:%s kind=%q reply=%#v visibility=%q",
			indexEventID, indexSessionID, indexTurnID, fromActor, toKind, toActor, kind, replyTo, visibility)
	}
}

func boardOwnerRequestCommitFixture(accepted AcceptedTurn, reservation BoardOwnerRequestReservation, instruction string, invocation agent.InvocationReceipt) (conversationruntime.BoardOwnerRequestLookup, conversationruntime.BoardOwnerRequestCommitInput) {
	material, _ := json.Marshal(struct {
		Source conversationruntime.BoardOwnerRequestSource `json:"source"`
		Body   string                                      `json:"body"`
	}{Source: reservation.Source, Body: instruction})
	fingerprint := sha256.Sum256(material)
	lookup := conversationruntime.BoardOwnerRequestLookup{
		Source: reservation.Source, OwnerID: reservation.Source.OwnerID, SessionID: accepted.SessionID,
		TurnID: accepted.TurnID, GenerationEpoch: accepted.GenerationEpoch,
		OperationID: "conversation.output:" + string(accepted.TurnID), SourceFingerprint: hex.EncodeToString(fingerprint[:]),
	}
	actorID := boardOwnerRequestCanonicalActorID(reservation.Source)
	messageID := modulecore.NewMessageID()
	payload := map[string]any{
		"kind": "reply", "visibility": "public", "from": actorID, "source_actor_id": actorID, "to": "external_peer",
		"text": boardOwnerRequestTestReply, "content": boardOwnerRequestTestReply,
	}
	if reservation.Source.TargetActorID != "" {
		payload["mentions"] = []any{reservation.Source.PeerPrincipalID}
	}
	responseEvent := modulecore.NewEventEnvelope(accepted.InputEvent.TraceID, accepted.InputEvent.EventID, nil,
		"conversation_runtime", "agent.response", reservation.AdmittedAt.Add(time.Second), payload)
	responseEvent.SessionID, responseEvent.TurnID, responseEvent.MessageID = accepted.SessionID, accepted.TurnID, messageID
	responseEvent.ActorKind, responseEvent.ActorID = "agent", actorID
	index := conversationruntime.MessageIndex{
		MessageID: messageID, EventID: responseEvent.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: actorID, ToKind: "external_peer", ToActor: reservation.Source.PeerPrincipalID,
		Kind: "board_owner_request", Visibility: "public",
	}
	target := conversationruntime.NewBoardThreadTarget{
		BoardInstanceID: reservation.Source.BoardInstanceID, CorePrincipalID: reservation.Source.CorePrincipalID,
		ChannelID: reservation.Source.ChannelID, PeerPrincipalID: reservation.Source.PeerPrincipalID, Title: reservation.Source.Title,
	}
	return lookup, conversationruntime.BoardOwnerRequestCommitInput{
		ExpectedLookup: lookup, Event: responseEvent, MessageIndex: index,
		InvocationReceipt: invocation, Target: target, IdempotencyKey: reservation.Source.IdempotencyKey,
	}
}

func cloneMutableEventForBoardOwnerRequest(event modulecore.EventEnvelope) modulecore.EventEnvelope {
	clone := event
	clone.Payload = cloneMutablePayload(event.Payload)
	return clone
}
