package eventstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

func TestCommitExternalTurnBundlePersistsCanonicalResponseAndPublicationIntent(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "board-publication.sqlite")
	store, err := NewSQLiteStore(databasePath)
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

	input := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-b1-1", "Synthetic publication request.")
	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(valid bound external peer message): %v", err)
	}
	lookup, bundle := externalPublicationCommitFixture(t, input, accepted)

	journal, ok := any(store).(conversationruntime.ExternalTurnJournal)
	if !ok {
		t.Fatal("SQLiteStore does not implement the exact ExternalTurnJournal port")
	}
	absent, found, err := journal.LoadExternalCommittedOutcome(ctx, lookup)
	if err != nil || found {
		t.Fatalf("LoadExternalCommittedOutcome(exact accepted uncommitted scope) = %#v, %t, %v; want absent without error", absent, found, err)
	}
	committed, err := journal.CommitExternalTurnBundle(ctx, bundle)
	if err != nil {
		t.Fatalf("CommitExternalTurnBundle(valid canonical response and private intent): %v", err)
	}
	if committed.Lookup != lookup || committed.ResultState != "completed" || committed.SafeError || committed.FailureStage != "" {
		t.Fatalf("committed external outcome scope/state = %#v, want exact lookup and completed result", committed)
	}
	if len(committed.CanonicalEvents) != len(bundle.CanonicalEvents) || len(committed.MessageIndexes) != len(bundle.MessageIndexes) {
		t.Fatalf("committed rows = %d events/%d indexes, want %d/%d", len(committed.CanonicalEvents), len(committed.MessageIndexes), len(bundle.CanonicalEvents), len(bundle.MessageIndexes))
	}
	wantCommitted := conversationruntime.ExternalCommittedBundle{
		Lookup: lookup, ResultState: bundle.ResultState, SafeError: bundle.SafeError, FailureStage: bundle.FailureStage,
		CanonicalEvents:   append([]modulecore.EventEnvelope(nil), bundle.CanonicalEvents...),
		MessageIndexes:    append([]conversationruntime.MessageIndex(nil), bundle.MessageIndexes...),
		InvocationReceipt: bundle.InvocationReceipt, PublicationIntent: bundle.PublicationIntent,
	}
	for index := range wantCommitted.CanonicalEvents {
		wantCommitted.CanonicalEvents[index].EventSeq = committed.CanonicalEvents[index].EventSeq
		if committed.CanonicalEvents[index].EventSeq <= 0 || (index > 0 && committed.CanonicalEvents[index].EventSeq <= committed.CanonicalEvents[index-1].EventSeq) {
			t.Fatalf("committed canonical event sequences are not positive and ordered: %#v", committed.CanonicalEvents)
		}
	}
	assertJSONEqual(t, committed, wantCommitted)
	if committed.PublicationIntent.CanonicalResponseMessageID != committed.MessageIndexes[0].MessageID {
		t.Fatalf("publication intent response id = %q, indexed response id = %q", committed.PublicationIntent.CanonicalResponseMessageID, committed.MessageIndexes[0].MessageID)
	}

	var workCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM live_work_outbox
		WHERE kind = 'board_publication' AND source_event_id = ? AND session_id = ? AND state = 'pending'`,
		string(committed.CanonicalEvents[0].EventID), string(accepted.SessionID)).Scan(&workCount); err != nil {
		t.Fatalf("read queued BOARD publication work: %v", err)
	}
	if workCount != 1 {
		t.Fatalf("queued BOARD publication work = %d, want exactly one work item for the canonical response", workCount)
	}
	var turnState, receiptID string
	if err := store.db.QueryRowContext(ctx, `SELECT state, result_receipt_id FROM live_conversation_turn WHERE turn_id = ?`, string(accepted.TurnID)).Scan(&turnState, &receiptID); err != nil {
		t.Fatalf("read accepted turn result binding: %v", err)
	}
	if turnState != "completed" || receiptID != lookup.OperationID {
		t.Fatalf("committed turn state/receipt = %q/%q, want completed/%q", turnState, receiptID, lookup.OperationID)
	}
	if _, found, err := store.LoadCommittedOutcome(ctx, accepted.TurnID); !errors.Is(err, conversationruntime.ErrExternalTurnJournalRequired) || found {
		t.Fatalf("generic LoadCommittedOutcome(external turn) = found %t, err %v; want typed-journal refusal", found, err)
	}

	replayed, err := journal.CommitExternalTurnBundle(ctx, bundle)
	if err != nil {
		t.Fatalf("CommitExternalTurnBundle(same operation and payload): %v", err)
	}
	assertJSONEqual(t, replayed, committed)
	changed := bundle
	changed.CanonicalEvents = append([]modulecore.EventEnvelope(nil), bundle.CanonicalEvents...)
	changed.CanonicalEvents[0].Payload = cloneMutablePayload(bundle.CanonicalEvents[0].Payload)
	changed.CanonicalEvents[0].Payload["text"] = "Changed synthetic response under the same operation."
	changed.CanonicalEvents[0].Payload["content"] = "Changed synthetic response under the same operation."
	if _, err := journal.CommitExternalTurnBundle(ctx, changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("CommitExternalTurnBundle(changed payload under same operation) error = %v, want ErrOperationConflict", err)
	}

	for _, kind := range []string{"", "board_publication"} {
		claimed, err := store.ClaimWork(ctx, WorkClaimInput{
			Kind: kind, Limit: 1, LeaseDuration: time.Minute, Now: accepted.InputEvent.OccurredAt.Add(time.Minute),
		})
		if err != nil || len(claimed) != 0 {
			t.Fatalf("ClaimWork(kind=%q) exposed private BOARD publication work: claims=%#v err=%v", kind, claimed, err)
		}
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close(before external outcome read): %v", err)
	}
	store = nil
	store, err = NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	journal, ok = any(store).(conversationruntime.ExternalTurnJournal)
	if !ok {
		t.Fatal("reopened SQLiteStore does not implement the exact ExternalTurnJournal port")
	}
	reloaded, found, err := journal.LoadExternalCommittedOutcome(ctx, lookup)
	if err != nil {
		t.Fatalf("LoadExternalCommittedOutcome(exact accepted scope): %v", err)
	}
	if !found {
		t.Fatal("LoadExternalCommittedOutcome(exact accepted scope) found no committed response")
	}
	assertJSONEqual(t, reloaded, committed)

	wrongLookups := []struct {
		name   string
		mutate func(*conversationruntime.ExternalTurnLookup)
	}{
		{name: "owner", mutate: func(value *conversationruntime.ExternalTurnLookup) { value.OwnerID = "other-owner" }},
		{name: "missing owner", mutate: func(value *conversationruntime.ExternalTurnLookup) { value.OwnerID = "" }},
		{name: "session", mutate: func(value *conversationruntime.ExternalTurnLookup) { value.SessionID = modulecore.NewSessionID() }},
		{name: "turn", mutate: func(value *conversationruntime.ExternalTurnLookup) { value.TurnID = modulecore.NewTurnID() }},
		{name: "generation epoch", mutate: func(value *conversationruntime.ExternalTurnLookup) { value.GenerationEpoch++ }},
		{name: "operation", mutate: func(value *conversationruntime.ExternalTurnLookup) { value.OperationID += ":other" }},
		{name: "source binding", mutate: func(value *conversationruntime.ExternalTurnLookup) {
			value.SourceBinding.MessageID = "board-message-b1-other"
		}},
		{name: "source fingerprint", mutate: func(value *conversationruntime.ExternalTurnLookup) { value.SourceFingerprint = strings.Repeat("0", 64) }},
		{name: "missing source fingerprint", mutate: func(value *conversationruntime.ExternalTurnLookup) { value.SourceFingerprint = "" }},
	}
	for _, test := range wrongLookups {
		t.Run("wrong scope/"+test.name, func(t *testing.T) {
			wrong := lookup
			test.mutate(&wrong)
			if _, found, err := journal.LoadExternalCommittedOutcome(ctx, wrong); found || !errors.Is(err, ErrExternalTurnScopeMismatch) {
				t.Fatalf("LoadExternalCommittedOutcome(wrong %s) = found %t, err %v; want scope error and no outcome", test.name, found, err)
			} else if found {
				t.Fatalf("LoadExternalCommittedOutcome(wrong %s) exposed the committed outcome", test.name)
			}
		})
	}
}

func TestLoadExternalCommittedOutcomeRefusesOccupiedOutputOperationID(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()
	input := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-b1-operation-collision", "Synthetic operation collision request.")
	input.OperationID = "conversation.output:" + string(input.InputEvent.TurnID)
	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(external peer input using its output operation ID): %v", err)
	}
	lookup, _ := externalPublicationCommitFixture(t, input, accepted)
	if lookup.OperationID != input.OperationID {
		t.Fatalf("lookup operation ID = %q, accepted operation ID = %q", lookup.OperationID, input.OperationID)
	}
	var operationKind string
	if err := store.db.QueryRowContext(ctx, `SELECT operation_kind FROM live_operation_receipt WHERE operation_id=?`, lookup.OperationID).Scan(&operationKind); err != nil {
		t.Fatalf("read occupied output operation receipt: %v", err)
	}
	if operationKind != "accept_turn" {
		t.Fatalf("operation receipt kind = %q, want accept_turn occupying the output operation ID", operationKind)
	}

	journal, ok := any(store).(conversationruntime.ExternalTurnJournal)
	if !ok {
		t.Fatal("SQLiteStore does not implement the exact ExternalTurnJournal port")
	}
	if _, found, err := journal.LoadExternalCommittedOutcome(ctx, lookup); found || err == nil {
		t.Fatalf("LoadExternalCommittedOutcome(occupied output operation ID) = found %t, err %v; want an error and no uncommitted result", found, err)
	}
}

func TestCommitExternalTurnBundleRollsBackOnPrivatePublicationFailure(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	input := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-b1-rollback", "Synthetic rollback request.")
	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(valid bound external peer message): %v", err)
	}
	lookup, bundle := externalPublicationCommitFixture(t, input, accepted)
	journal, ok := any(store).(conversationruntime.ExternalTurnJournal)
	if !ok {
		t.Fatal("SQLiteStore does not implement the exact ExternalTurnJournal port")
	}
	if _, err := store.db.Exec(`
		CREATE TRIGGER test_fail_board_publication_insert
		BEFORE INSERT ON live_board_publication
		BEGIN
			SELECT RAISE(ABORT, 'synthetic private publication insert failure');
		END`); err != nil {
		t.Fatalf("install late private publication failure trigger: %v", err)
	}
	if _, err := journal.CommitExternalTurnBundle(ctx, bundle); err == nil {
		t.Fatal("CommitExternalTurnBundle() error = nil, want injected private publication metadata failure")
	}
	var turnState string
	var resultReceiptID sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT state,result_receipt_id FROM live_conversation_turn WHERE turn_id=?`, string(accepted.TurnID)).Scan(&turnState, &resultReceiptID); err != nil {
		t.Fatalf("read turn after injected rollback: %v", err)
	}
	if turnState != "accepted" || resultReceiptID.Valid {
		t.Fatalf("turn after injected rollback = state %q receipt %#v, want accepted without receipt", turnState, resultReceiptID)
	}
	for _, check := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "canonical events", query: `SELECT COUNT(*) FROM event_envelope WHERE event_id IN (?,?)`, args: []any{string(bundle.CanonicalEvents[0].EventID), string(bundle.CanonicalEvents[1].EventID)}},
		{name: "message index", query: `SELECT COUNT(*) FROM live_message_index WHERE message_id=?`, args: []any{string(bundle.MessageIndexes[0].MessageID)}},
		{name: "work item", query: `SELECT COUNT(*) FROM live_work_outbox WHERE work_id=?`, args: []any{externalBoardPublicationWorkID(lookup.OperationID)}},
		{name: "private target", query: `SELECT COUNT(*) FROM live_board_publication WHERE work_id=?`, args: []any{externalBoardPublicationWorkID(lookup.OperationID)}},
		{name: "operation receipt", query: `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id=?`, args: []any{lookup.OperationID}},
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			t.Fatalf("read %s after injected rollback: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("%s after injected rollback = %d, want zero", check.name, count)
		}
	}
}

func TestLiveBoardPublicationSchemaMigratesWithoutChangingAcceptedTurn(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "board-publication-old-schema.sqlite")
	store, err := NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	input := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-b1-migration", "Synthetic migration request.")
	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(valid bound external peer message): %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE live_board_publication`); err != nil {
		t.Fatalf("remove additive B1 table to emulate old schema: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(before additive migration): %v", err)
	}
	store, err = NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(migrate old schema): %v", err)
	}
	t.Cleanup(func() {
		if store != nil {
			if err := store.Close(); err != nil {
				t.Errorf("Close(): %v", err)
			}
		}
	})
	var tableCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='live_board_publication'`).Scan(&tableCount); err != nil {
		t.Fatalf("check migrated private publication table: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("migrated private publication table count = %d, want one", tableCount)
	}
	if err := ValidateLiveJournalSchema(ctx, store.db); err != nil {
		t.Fatalf("ValidateLiveJournalSchema(after additive migration): %v", err)
	}
	pending, err := store.ReadPendingAcceptedTurns(ctx, accepted.SessionID, input.OwnerID)
	if err != nil {
		t.Fatalf("ReadPendingAcceptedTurns(after additive migration): %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending accepted turns after additive migration = %d, want one existing row", len(pending))
	}
	assertJSONEqual(t, pending[0], accepted)
}

func TestCommitTurnBundleRejectsExternalPeerAcceptance(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	input := externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-b1-2", "Synthetic generic-path fence request.")
	accepted, err := store.AcceptTurn(ctx, input)
	if err != nil {
		t.Fatalf("AcceptTurn(valid bound external peer message): %v", err)
	}
	legacyOutput := genericUserOutputForAcceptedTurn(accepted)
	if err := validateCommitInput(legacyOutput); err != nil {
		t.Fatalf("generic-path bypass fixture is invalid before persistence: %v", err)
	}
	legacyWithBoardWork := legacyOutput
	legacyWithBoardWork.Outbox = []WorkItem{{
		WorkID: "generic-board-work", DedupeKey: "generic-board-work", Kind: "board_publication",
		SourceEventID: legacyOutput.Events[0].EventID, SessionID: accepted.SessionID, Priority: 1,
	}}
	if err := validateCommitInput(legacyWithBoardWork); err == nil {
		t.Fatal("validateCommitInput() accepted generic injection of reserved board_publication work")
	}
	if _, err := store.CommitTurnBundle(ctx, legacyOutput); err == nil {
		t.Fatal("CommitTurnBundle accepted a generic user-facing result for an external peer turn; want the typed external journal boundary")
	}
}

func TestGenericJournalRejectsBoardOwnerRequest(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	pin := boardConnectionPinFixture()
	if _, err := store.PinBoardConnection(ctx, pin); err != nil {
		t.Fatalf("PinBoardConnection(synthetic owner pin): %v", err)
	}
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
		t.Fatalf("AcceptBoardOwnerRequest(valid pinned owner request): %v", err)
	}
	genericSessionID, genericTurnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	genericPayload := map[string]any{
		"from": "board_owner", "source_actor_id": request.ActorID, "to": "mio",
		"text": request.Instruction, "content": request.Instruction, "body": map[string]any{"text": request.Instruction},
		"board_owner_request_binding": map[string]any{
			"board_instance_id": reservation.Source.BoardInstanceID, "core_principal_id": reservation.Source.CorePrincipalID,
			"channel_id": reservation.Source.ChannelID, "peer_principal_id": reservation.Source.PeerPrincipalID,
			"title": reservation.Source.Title, "idempotency_key": reservation.Source.IdempotencyKey,
		},
	}
	genericEvent := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.board_owner_request", time.Now().UTC(), genericPayload)
	genericEvent.SessionID, genericEvent.TurnID, genericEvent.MessageID = genericSessionID, genericTurnID, modulecore.NewMessageID()
	if _, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "generic-board-owner-accept:" + string(genericTurnID), OwnerID: pin.OwnerID,
		RuntimeVersion: conversationruntime.ConversationRuntimeV2, ClientSubmissionID: "generic-board-owner-submission:" + string(genericTurnID),
		ActorID: request.ActorID, TriggerKind: "board_owner_request", GenerationEpoch: 1, CreateNewSession: true,
		InputEvent: genericEvent, InputMessage: MessageIndex{
			MessageID: genericEvent.MessageID, EventID: genericEvent.EventID, SessionID: genericSessionID, TurnID: genericTurnID,
			FromActor: "board_owner", ToKind: "agent", ToActor: "mio", Kind: "board_owner_request", Visibility: "public",
		},
	}); !errors.Is(err, ErrBoardOwnerRequestScopeMismatch) {
		t.Fatalf("AcceptTurn(generic BOARD owner request) error = %v; want typed-scope rejection", err)
	}
	if _, found, err := store.LoadCommittedOutcome(ctx, accepted.TurnID); found || !errors.Is(err, conversationruntime.ErrExternalTurnJournalRequired) {
		t.Fatalf("LoadCommittedOutcome(board owner request) = found %t, err %v; want typed-journal refusal", found, err)
	}
	if _, err := store.CommitTurnBundle(ctx, genericUserOutputForAcceptedTurn(accepted)); !errors.Is(err, conversationruntime.ErrExternalTurnJournalRequired) {
		t.Fatalf("CommitTurnBundle(board owner request) error = %v; want typed-journal refusal", err)
	}
}

func externalPublicationCommitFixture(t *testing.T, input AcceptInput, accepted AcceptedTurn) (conversationruntime.ExternalTurnLookup, conversationruntime.ExternalCommitBundle) {
	t.Helper()
	if input.ExternalPeerBinding == nil {
		t.Fatal("external publication fixture requires the typed accepted source binding")
	}
	binding := *input.ExternalPeerBinding
	encodedPayload, err := json.Marshal(accepted.InputEvent.Payload)
	if err != nil {
		t.Fatalf("json.Marshal(accepted external source payload): %v", err)
	}
	fingerprint := sha256.Sum256(encodedPayload)
	operationID := "conversation.output:" + string(accepted.TurnID)
	lookup := conversationruntime.ExternalTurnLookup{
		OwnerID: input.OwnerID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		GenerationEpoch: accepted.GenerationEpoch, OperationID: operationID,
		SourceBinding: binding, SourceFingerprint: hex.EncodeToString(fingerprint[:]),
	}

	responseMessageID := modulecore.NewMessageID()
	responderActorID := binding.EffectiveResponderActorID()
	responsePayload := map[string]any{
		"kind": "reply", "client_submission_id": accepted.ClientSubmissionID, "availability": "available", "visibility": "public",
		"from": responderActorID, "source_actor_id": responderActorID, "to": "external_peer",
		"content": "Synthetic canonical response.", "text": "Synthetic canonical response.",
		"reply_to_message_id": string(accepted.InputMessageID), "external_peer_binding": binding.ToPayload(),
	}
	responseEvent := modulecore.NewEventEnvelope(
		accepted.InputEvent.TraceID, accepted.InputEvent.EventID, nil, "conversation_runtime", "agent.response",
		accepted.InputEvent.OccurredAt.Add(time.Second), responsePayload,
	)
	responseEvent.ActorKind, responseEvent.ActorID = "agent", responderActorID
	responseEvent.SessionID, responseEvent.TurnID, responseEvent.MessageID = accepted.SessionID, accepted.TurnID, responseMessageID

	invocation := agent.InvocationReceipt{
		RequestID: "synthetic-board-request", ResponseID: "synthetic-board-response",
		ProviderLabel: "synthetic-provider", TokensUsed: 17, Status: "completed",
	}
	receiptEvent := modulecore.NewEventEnvelope(
		accepted.InputEvent.TraceID, accepted.InputEvent.EventID, nil, "conversation_runtime", "conversation.mio_emission_receipt",
		accepted.InputEvent.OccurredAt.Add(2*time.Second), map[string]any{
			"status": invocation.Status, "request_id": invocation.RequestID, "response_id": invocation.ResponseID,
		},
	)
	receiptEvent.SessionID, receiptEvent.TurnID = accepted.SessionID, accepted.TurnID

	index := conversationruntime.MessageIndex{
		MessageID: responseMessageID, EventID: responseEvent.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: responderActorID, ToKind: "external_peer", ToActor: binding.SenderPrincipalID,
		Kind: "assistant_message", ReplyToMessageID: accepted.InputMessageID, Visibility: "public",
	}
	intent := conversationruntime.ExternalPublicationIntent{
		CanonicalResponseMessageID: responseMessageID,
		Target: conversationruntime.ExternalReplyTarget{
			BoardInstanceID: binding.BoardInstanceID, CorePrincipalID: binding.RecipientPrincipalID,
			PeerPrincipalID: binding.SenderPrincipalID, ChannelID: binding.ChannelID,
			ThreadID: binding.ThreadID, ReplyTo: binding.MessageID,
		},
		StableLogicalKey: "external-publication:" + operationID,
	}
	return lookup, conversationruntime.ExternalCommitBundle{
		ExpectedLookup: lookup, ResultState: "completed",
		CanonicalEvents: []modulecore.EventEnvelope{responseEvent, receiptEvent}, MessageIndexes: []conversationruntime.MessageIndex{index},
		InvocationReceipt: invocation, PublicationIntent: intent,
	}
}

func genericUserOutputForAcceptedTurn(accepted AcceptedTurn) CommitInput {
	messageID := modulecore.NewMessageID()
	event := modulecore.NewEventEnvelope(accepted.InputEvent.TraceID, accepted.InputEvent.EventID, nil, "orchestrator", "agent.response", accepted.InputEvent.OccurredAt.Add(time.Second), map[string]any{
		"kind": "reply", "from": "mio", "source_actor_id": "mio", "to": "user", "visibility": "public",
		"content": "Synthetic generic output that must not consume an external turn.",
		"text":    "Synthetic generic output that must not consume an external turn.",
	})
	event.SessionID, event.TurnID, event.MessageID = accepted.SessionID, accepted.TurnID, messageID
	return CommitInput{
		OperationID: "conversation.output:" + string(accepted.TurnID),
		SessionID:   accepted.SessionID, TurnID: accepted.TurnID, GenerationEpoch: accepted.GenerationEpoch,
		ResultState: "completed", Events: []modulecore.EventEnvelope{event},
		MessageIndexes: []MessageIndex{{
			MessageID: messageID, EventID: event.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
			FromActor: "mio", ToKind: "user", ToActor: "owner-1", Kind: "assistant_message",
			ReplyToMessageID: accepted.InputMessageID, Visibility: "public",
		}},
	}
}
