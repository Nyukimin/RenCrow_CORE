package eventstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const externalTurnCommitOperationKind = "commit_external_turn_bundle"

var (
	ErrExternalTurnScopeMismatch  = errors.New("external turn lookup does not match an accepted source")
	errExternalTurnOutcomeCorrupt = errors.New("external turn outcome receipt is inconsistent with canonical storage")
)

var _ conversationruntime.ExternalTurnJournal = (*SQLiteStore)(nil)

type externalCommitEventReference struct {
	EventID  modulecore.EventID  `json:"event_id"`
	EventSeq modulecore.EventSeq `json:"event_seq"`
}

type externalCommitMessageReference struct {
	MessageID modulecore.MessageID `json:"message_id"`
	EventID   modulecore.EventID   `json:"event_id"`
}

// The immutable operation receipt keeps canonical references and invocation
// metadata only. Event bodies and indexes remain authoritative in their rows;
// the publication target is authoritative in live_board_publication.
type externalCommitReceipt struct {
	Lookup            conversationruntime.ExternalTurnLookup   `json:"lookup"`
	ResultState       string                                   `json:"result_state"`
	SafeError         bool                                     `json:"safe_error"`
	FailureStage      conversationruntime.ExternalFailureStage `json:"failure_stage,omitempty"`
	Events            []externalCommitEventReference           `json:"events"`
	Messages          []externalCommitMessageReference         `json:"messages"`
	InvocationReceipt agent.InvocationReceipt                  `json:"invocation_receipt"`
	WorkID            string                                   `json:"work_id"`
}

// CommitExternalTurnBundle atomically writes the canonical external response,
// its message index, publication work and private target, turn CAS, and receipt.
func (s *SQLiteStore) CommitExternalTurnBundle(ctx context.Context, bundle conversationruntime.ExternalCommitBundle) (conversationruntime.ExternalCommittedBundle, error) {
	if err := s.ensureOpen(); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	if ctx == nil {
		return conversationruntime.ExternalCommittedBundle{}, errors.New("external turn commit context is required")
	}
	if err := validateExternalCommitInput(bundle); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	if s.shouldQueueWrite(ctx) {
		queuedBundle := cloneExternalCommitBundle(bundle)
		sizeBytes, err := queuedWriteSize(queuedBundle)
		if err != nil {
			return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("size queued external turn bundle: %w", err)
		}
		var committed conversationruntime.ExternalCommittedBundle
		err = s.enqueueOrderedWrite(ctx, len(queuedBundle.CanonicalEvents), sizeBytes, func(commandCtx context.Context) error {
			var commitErr error
			committed, commitErr = s.commitExternalTurnBundleInline(commandCtx, queuedBundle)
			return commitErr
		})
		if err != nil {
			return conversationruntime.ExternalCommittedBundle{}, err
		}
		return committed, nil
	}
	return s.commitExternalTurnBundleInline(ctx, bundle)
}

// LoadExternalCommittedOutcome returns false only for this exact accepted
// source when it is still uncommitted. Scope mismatches always return an error.
func (s *SQLiteStore) LoadExternalCommittedOutcome(ctx context.Context, lookup conversationruntime.ExternalTurnLookup) (conversationruntime.ExternalCommittedBundle, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, false, err
	}
	if ctx == nil {
		return conversationruntime.ExternalCommittedBundle{}, false, errors.New("external committed outcome context is required")
	}
	if err := validateExternalTurnLookup(lookup); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, false, err
	}
	defer tx.Rollback()
	return loadExternalCommittedOutcomeTx(ctx, tx, lookup)
}

func (s *SQLiteStore) commitExternalTurnBundleInline(ctx context.Context, bundle conversationruntime.ExternalCommitBundle) (conversationruntime.ExternalCommittedBundle, error) {
	if err := s.ensureOpen(); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	if ctx == nil {
		return conversationruntime.ExternalCommittedBundle{}, errors.New("external turn commit context is required")
	}
	if err := validateExternalCommitInput(bundle); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	inputHash, err := hashJSON(bundle)
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("hash external turn bundle: %w", err)
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if prior, found, err := readOperationReceipt(ctx, tx, bundle.ExpectedLookup.OperationID); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	} else if found {
		if prior.operationKind != externalTurnCommitOperationKind || prior.inputSHA256 != inputHash {
			return conversationruntime.ExternalCommittedBundle{}, ErrOperationConflict
		}
		outcome, found, err := loadExternalCommittedOutcomeTx(ctx, tx, bundle.ExpectedLookup)
		if err != nil {
			return conversationruntime.ExternalCommittedBundle{}, err
		}
		if !found {
			return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("%w: typed operation receipt has no committed external outcome", errExternalTurnOutcomeCorrupt)
		}
		if err := tx.Commit(); err != nil {
			return conversationruntime.ExternalCommittedBundle{}, err
		}
		committed = true
		return outcome, nil
	}

	accepted, turnState, resultReceiptID, err := loadExternalAcceptedTurnTx(ctx, tx, bundle.ExpectedLookup)
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	if turnState != "accepted" || resultReceiptID != "" {
		return conversationruntime.ExternalCommittedBundle{}, ErrConversationTurnConflict
	}

	canonicalEvents := make([]modulecore.EventEnvelope, len(bundle.CanonicalEvents))
	for index, event := range bundle.CanonicalEvents {
		event.DependencyEventIDs = append([]modulecore.EventID(nil), event.DependencyEventIDs...)
		event.Payload = cloneMutablePayload(event.Payload)
		event.EventSeq, err = s.nextSequence(ctx)
		if err != nil {
			return conversationruntime.ExternalCommittedBundle{}, err
		}
		if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
			return conversationruntime.ExternalCommittedBundle{}, err
		}
		canonicalEvents[index] = event
	}
	publicationIntent := bundle.PublicationIntent
	canonical := conversationruntime.ExternalCommittedBundle{
		Lookup: bundle.ExpectedLookup, ResultState: bundle.ResultState,
		SafeError: bundle.SafeError, FailureStage: bundle.FailureStage,
		CanonicalEvents: canonicalEvents, MessageIndexes: append([]conversationruntime.MessageIndex(nil), bundle.MessageIndexes...),
		InvocationReceipt: bundle.InvocationReceipt, PublicationIntent: publicationIntent,
	}
	if err := conversationruntime.ValidateExternalCommittedBundle(canonical, bundle.ExpectedLookup, accepted); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("validate external turn bundle: %w", err)
	}

	indexes := make([]MessageIndex, len(bundle.MessageIndexes))
	eventsByID := make(map[modulecore.EventID]modulecore.EventEnvelope, len(canonicalEvents))
	for _, event := range canonicalEvents {
		if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
			return conversationruntime.ExternalCommittedBundle{}, err
		}
		if err := s.validateReferences(ctx, tx, event); err != nil {
			return conversationruntime.ExternalCommittedBundle{}, err
		}
		persisted, err := insertLiveEvent(ctx, tx, event)
		if err != nil {
			return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("insert external canonical event: %w", err)
		}
		eventsByID[persisted.EventID] = persisted
	}
	for index, appIndex := range bundle.MessageIndexes {
		indexes[index] = eventStoreMessageIndex(appIndex)
		event, exists := eventsByID[indexes[index].EventID]
		if !exists {
			return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("external message index event %q is not in the commit bundle", indexes[index].EventID)
		}
		if err := validateMessageIndex(indexes[index], event, bundle.ExpectedLookup.SessionID, bundle.ExpectedLookup.TurnID); err != nil {
			return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("validate external message index: %w", err)
		}
	}
	if err := validateReplyTargets(ctx, tx, CommitInput{
		SessionID: bundle.ExpectedLookup.SessionID, TurnID: bundle.ExpectedLookup.TurnID,
		Events: canonicalEvents, MessageIndexes: indexes,
	}); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("validate external response reply target: %w", err)
	}
	for _, index := range indexes {
		if err := insertMessageIndex(ctx, tx, index); err != nil {
			return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("insert external message index: %w", err)
		}
	}

	responseEvent := externalResponseEvent(canonicalEvents, publicationIntent.CanonicalResponseMessageID)
	if responseEvent == nil {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("%w: external response event is missing", errExternalTurnOutcomeCorrupt)
	}
	if err := validateOutboxSource(ctx, tx, responseEvent.EventID, string(accepted.InputEvent.TraceID), accepted.SessionID); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("validate BOARD publication source: %w", err)
	}
	workID := externalBoardPublicationWorkID(bundle.ExpectedLookup.OperationID)
	stableLogicalKey := publicationIntent.StableLogicalKey
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_work_outbox
			(work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
		VALUES (?,?,'board_publication',?,?,5,'pending',0,'',1)`,
		workID, stableLogicalKey, string(responseEvent.EventID), string(accepted.SessionID)); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("insert BOARD publication work: %w", err)
	}
	target := publicationIntent.Target
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_board_publication
			(work_id,response_message_id,board_instance_id,core_principal_id,peer_principal_id,channel_id,thread_id,reply_to)
		VALUES (?,?,?,?,?,?,?,?)`,
		workID, string(publicationIntent.CanonicalResponseMessageID), target.BoardInstanceID,
		target.CorePrincipalID, target.PeerPrincipalID, target.ChannelID, target.ThreadID, target.ReplyTo); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("insert private BOARD publication target: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE live_conversation_turn
		SET state='completed', result_receipt_id=?
		WHERE turn_id=? AND session_id=? AND owner_id=? AND trigger_kind='external_peer_message'
		  AND generation_epoch=? AND state='accepted' AND result_receipt_id IS NULL`,
		bundle.ExpectedLookup.OperationID, string(accepted.TurnID), string(accepted.SessionID),
		bundle.ExpectedLookup.OwnerID, bundle.ExpectedLookup.GenerationEpoch)
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("check external turn commit CAS: %w", err)
	}
	if updated != 1 {
		return conversationruntime.ExternalCommittedBundle{}, ErrConversationTurnConflict
	}

	receipt := externalCommitReceipt{
		Lookup: bundle.ExpectedLookup, ResultState: bundle.ResultState, SafeError: bundle.SafeError,
		FailureStage: bundle.FailureStage, InvocationReceipt: bundle.InvocationReceipt, WorkID: workID,
		Events:   make([]externalCommitEventReference, len(canonicalEvents)),
		Messages: make([]externalCommitMessageReference, len(indexes)),
	}
	for index, event := range canonicalEvents {
		receipt.Events[index] = externalCommitEventReference{EventID: event.EventID, EventSeq: event.EventSeq}
	}
	for index, message := range indexes {
		receipt.Messages[index] = externalCommitMessageReference{MessageID: message.MessageID, EventID: message.EventID}
	}
	if err := insertOperationReceipt(ctx, tx, bundle.ExpectedLookup.OperationID, externalTurnCommitOperationKind, inputHash, receipt); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}

	committedOutcome, found, err := loadExternalCommittedOutcomeTx(ctx, tx, bundle.ExpectedLookup)
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	if !found || !externalCommitMatchesOutcome(bundle, committedOutcome) {
		return conversationruntime.ExternalCommittedBundle{}, fmt.Errorf("%w: committed external outcome differs from its request", errExternalTurnOutcomeCorrupt)
	}
	if err := tx.Commit(); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, err
	}
	committed = true
	return committedOutcome, nil
}

func loadExternalCommittedOutcomeTx(ctx context.Context, tx *sql.Tx, lookup conversationruntime.ExternalTurnLookup) (conversationruntime.ExternalCommittedBundle, bool, error) {
	accepted, turnState, resultReceiptID, err := loadExternalAcceptedTurnTx(ctx, tx, lookup)
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, false, err
	}
	if turnState == "accepted" && resultReceiptID == "" {
		if _, occupied, err := readOperationReceipt(ctx, tx, lookup.OperationID); err != nil {
			return conversationruntime.ExternalCommittedBundle{}, false, err
		} else if occupied {
			return conversationruntime.ExternalCommittedBundle{}, false, ErrOperationConflict
		}
		return conversationruntime.ExternalCommittedBundle{}, false, nil
	}
	if turnState != "completed" || resultReceiptID != lookup.OperationID {
		return conversationruntime.ExternalCommittedBundle{}, false, ErrExternalTurnScopeMismatch
	}
	receiptRow, found, err := readOperationReceipt(ctx, tx, lookup.OperationID)
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, false, err
	}
	if !found || receiptRow.operationKind != externalTurnCommitOperationKind {
		return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("%w: turn receipt is not an external commit receipt", errExternalTurnOutcomeCorrupt)
	}
	var receipt externalCommitReceipt
	if err := json.Unmarshal([]byte(receiptRow.resultJSON), &receipt); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("decode external operation receipt: %w", err)
	}
	if receipt.Lookup != lookup || receipt.ResultState != "completed" || receipt.WorkID != externalBoardPublicationWorkID(lookup.OperationID) ||
		len(receipt.Events) < 1 || len(receipt.Events) > 2 || len(receipt.Messages) != 1 {
		return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("%w: external operation receipt identity or references are invalid", errExternalTurnOutcomeCorrupt)
	}

	work, intent, err := loadExternalPublicationIntentTx(ctx, tx, receipt.WorkID, lookup, receipt.Messages[0])
	if err != nil {
		return conversationruntime.ExternalCommittedBundle{}, false, err
	}
	events := make([]modulecore.EventEnvelope, len(receipt.Events))
	for index, reference := range receipt.Events {
		event, err := loadEventEnvelopeTx(ctx, tx, reference.EventID)
		if err != nil {
			return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("load external canonical event %q: %w", reference.EventID, err)
		}
		if event.EventSeq != reference.EventSeq {
			return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("%w: external event sequence differs from receipt", errExternalTurnOutcomeCorrupt)
		}
		events[index] = event
	}
	indexes := make([]conversationruntime.MessageIndex, len(receipt.Messages))
	for index, reference := range receipt.Messages {
		message, err := loadExternalMessageIndexTx(ctx, tx, reference.MessageID)
		if err != nil {
			return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("load external response index %q: %w", reference.MessageID, err)
		}
		if message.EventID != reference.EventID {
			return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("%w: external message index differs from receipt", errExternalTurnOutcomeCorrupt)
		}
		indexes[index] = applicationMessageIndex(message)
	}
	if work.sourceEventID != string(receipt.Messages[0].EventID) || work.sessionID != string(lookup.SessionID) || work.dedupeKey != intent.StableLogicalKey {
		return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("%w: publication work binding differs from private target", errExternalTurnOutcomeCorrupt)
	}
	outcome := conversationruntime.ExternalCommittedBundle{
		Lookup: lookup, ResultState: receipt.ResultState, SafeError: receipt.SafeError, FailureStage: receipt.FailureStage,
		CanonicalEvents: events, MessageIndexes: indexes, InvocationReceipt: receipt.InvocationReceipt,
		PublicationIntent: intent,
	}
	if err := conversationruntime.ValidateExternalCommittedBundle(outcome, lookup, accepted); err != nil {
		return conversationruntime.ExternalCommittedBundle{}, false, fmt.Errorf("validate stored external outcome: %w", err)
	}
	return outcome, true, nil
}

func loadExternalAcceptedTurnTx(ctx context.Context, tx *sql.Tx, lookup conversationruntime.ExternalTurnLookup) (conversationruntime.AcceptedTurn, string, string, error) {
	if err := validateExternalTurnLookup(lookup); err != nil {
		return conversationruntime.AcceptedTurn{}, "", "", err
	}
	var accepted conversationruntime.AcceptedTurn
	var turnID, sessionID, ownerID, sessionOwnerID, actorID, triggerKind, clientSubmissionID, inputMessageID string
	var state, eventID string
	var generationEpoch, admissionSeq int64
	var resultReceiptID sql.NullString
	var replyTo sql.NullString
	var message MessageIndex
	err := tx.QueryRowContext(ctx, `
		SELECT t.turn_id,t.session_id,t.owner_id,s.owner_id,t.actor_id,t.trigger_kind,t.client_submission_id,
		       t.input_message_id,t.admission_seq,t.generation_epoch,t.state,t.result_receipt_id,
		       m.event_id,m.from_actor,m.to_kind,m.to_actor,m.kind,m.reply_to_message_id,m.visibility
		FROM live_conversation_turn t
	JOIN live_conversation_session s ON s.session_id=t.session_id
	JOIN live_message_index m ON m.message_id=t.input_message_id AND m.session_id=t.session_id AND m.turn_id=t.turn_id
		WHERE t.turn_id=?`, string(lookup.TurnID)).Scan(
		&turnID, &sessionID, &ownerID, &sessionOwnerID, &actorID, &triggerKind, &clientSubmissionID,
		&inputMessageID, &admissionSeq, &generationEpoch, &state, &resultReceiptID,
		&eventID, &message.FromActor, &message.ToKind, &message.ToActor, &message.Kind, &replyTo, &message.Visibility,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return conversationruntime.AcceptedTurn{}, "", "", fmt.Errorf("%w: accepted external turn is missing", ErrExternalTurnScopeMismatch)
	}
	if err != nil {
		return conversationruntime.AcceptedTurn{}, "", "", err
	}
	message.MessageID = modulecore.MessageID(inputMessageID)
	message.EventID = modulecore.EventID(eventID)
	message.SessionID = modulecore.SessionID(sessionID)
	message.TurnID = modulecore.TurnID(turnID)
	if replyTo.Valid {
		message.ReplyToMessageID = modulecore.MessageID(replyTo.String)
	}
	inputEvent, err := loadEventEnvelopeTx(ctx, tx, message.EventID)
	if err != nil {
		return conversationruntime.AcceptedTurn{}, "", "", fmt.Errorf("load accepted external source event: %w", err)
	}
	accepted = conversationruntime.AcceptedTurn{
		SessionID: modulecore.SessionID(sessionID), TurnID: modulecore.TurnID(turnID), ActorID: actorID,
		TriggerKind: triggerKind, ClientSubmissionID: clientSubmissionID, InputMessageID: message.MessageID,
		AdmissionSeq: modulecore.EventSeq(admissionSeq), GenerationEpoch: generationEpoch, State: state, InputEvent: inputEvent,
	}
	if ownerID != lookup.OwnerID || sessionOwnerID != lookup.OwnerID || accepted.SessionID != lookup.SessionID ||
		accepted.TurnID != lookup.TurnID || accepted.GenerationEpoch != lookup.GenerationEpoch ||
		accepted.TriggerKind != "external_peer_message" || accepted.State != "accepted" && accepted.State != "completed" ||
		accepted.InputEvent.EventSeq != accepted.AdmissionSeq || accepted.InputEvent.SessionID != accepted.SessionID ||
		accepted.InputEvent.TurnID != accepted.TurnID || accepted.InputEvent.MessageID != accepted.InputMessageID {
		return conversationruntime.AcceptedTurn{}, "", "", ErrExternalTurnScopeMismatch
	}
	binding, err := domainconversation.ParseExternalPeerBinding(accepted.InputEvent.Payload)
	if err != nil || !binding.Equal(lookup.SourceBinding) {
		return conversationruntime.AcceptedTurn{}, "", "", ErrExternalTurnScopeMismatch
	}
	sourceActorID, err := binding.SourceActorID()
	if err != nil || sourceActorID != accepted.ActorID {
		return conversationruntime.AcceptedTurn{}, "", "", ErrExternalTurnScopeMismatch
	}
	if err := validateExternalPeerEnvelopeAndIndex(binding, accepted.ActorID, accepted.InputEvent, message, accepted.SessionID, accepted.TurnID); err != nil {
		return conversationruntime.AcceptedTurn{}, "", "", fmt.Errorf("%w: accepted external source proof is invalid: %v", ErrExternalTurnScopeMismatch, err)
	}
	derivedLookup, err := conversationruntime.ExternalTurnLookupForRequest(conversationruntime.ReadViewRequest{
		Session: conversationruntime.SessionState{OwnerID: lookup.OwnerID}, Accepted: accepted,
	})
	if err != nil || derivedLookup.SourceCausationID != lookup.SourceCausationID || derivedLookup.SourceHopCount != lookup.SourceHopCount {
		return conversationruntime.AcceptedTurn{}, "", "", ErrExternalTurnScopeMismatch
	}
	fingerprint, err := externalSourceFingerprint(accepted.InputEvent.Payload)
	if err != nil || fingerprint != lookup.SourceFingerprint {
		return conversationruntime.AcceptedTurn{}, "", "", ErrExternalTurnScopeMismatch
	}
	return accepted, state, resultReceiptID.String, nil
}

func loadExternalPublicationIntentTx(ctx context.Context, tx *sql.Tx, workID string, lookup conversationruntime.ExternalTurnLookup, response externalCommitMessageReference) (externalPublicationWork, conversationruntime.ExternalPublicationIntent, error) {
	var work externalPublicationWork
	var intent conversationruntime.ExternalPublicationIntent
	err := tx.QueryRowContext(ctx, `
		SELECT p.work_id,p.response_message_id,p.board_instance_id,p.core_principal_id,p.peer_principal_id,
		       p.channel_id,p.thread_id,p.reply_to,w.kind,w.dedupe_key,w.source_event_id,w.session_id
		FROM live_board_publication p
	JOIN live_work_outbox w ON w.work_id=p.work_id
		WHERE p.work_id=?`, workID).Scan(
		&work.workID, &work.responseMessageID, &intent.Target.BoardInstanceID, &intent.Target.CorePrincipalID,
		&intent.Target.PeerPrincipalID, &intent.Target.ChannelID, &intent.Target.ThreadID, &intent.Target.ReplyTo,
		&work.kind, &work.dedupeKey, &work.sourceEventID, &work.sessionID,
	)
	if err != nil {
		return externalPublicationWork{}, conversationruntime.ExternalPublicationIntent{}, fmt.Errorf("load private BOARD publication target: %w", err)
	}
	intent.CanonicalResponseMessageID = modulecore.MessageID(work.responseMessageID)
	intent.StableLogicalKey = work.dedupeKey
	if work.workID != externalBoardPublicationWorkID(lookup.OperationID) || work.kind != "board_publication" ||
		work.responseMessageID != string(response.MessageID) || work.sessionID != string(lookup.SessionID) ||
		work.sourceEventID != string(response.EventID) || work.dedupeKey != "external-publication:"+lookup.OperationID || response.EventID == "" {
		return externalPublicationWork{}, conversationruntime.ExternalPublicationIntent{}, fmt.Errorf("%w: private BOARD publication target scope is invalid", errExternalTurnOutcomeCorrupt)
	}
	expectedTarget := externalReplyTarget(lookup)
	persistedTarget := intent.Target
	persistedTarget.CausationID = ""
	persistedTarget.HopCount = 0
	expectedPersistedTarget := expectedTarget
	expectedPersistedTarget.CausationID = ""
	expectedPersistedTarget.HopCount = 0
	if persistedTarget != expectedPersistedTarget {
		return externalPublicationWork{}, conversationruntime.ExternalPublicationIntent{}, fmt.Errorf("%w: private BOARD publication target differs from the accepted source", errExternalTurnOutcomeCorrupt)
	}
	intent.Target.CausationID = expectedTarget.CausationID
	intent.Target.HopCount = expectedTarget.HopCount
	return work, intent, nil
}

type externalPublicationWork struct {
	workID            string
	responseMessageID string
	kind              string
	dedupeKey         string
	sourceEventID     string
	sessionID         string
}

func loadExternalMessageIndexTx(ctx context.Context, tx *sql.Tx, messageID modulecore.MessageID) (MessageIndex, error) {
	var message MessageIndex
	var eventID, sessionID, turnID string
	var replyTo sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT message_id,event_id,session_id,turn_id,from_actor,to_kind,to_actor,kind,reply_to_message_id,visibility
		FROM live_message_index WHERE message_id=?`, string(messageID)).Scan(
		&message.MessageID, &eventID, &sessionID, &turnID, &message.FromActor, &message.ToKind,
		&message.ToActor, &message.Kind, &replyTo, &message.Visibility,
	)
	if err != nil {
		return MessageIndex{}, err
	}
	message.EventID, message.SessionID, message.TurnID = modulecore.EventID(eventID), modulecore.SessionID(sessionID), modulecore.TurnID(turnID)
	if replyTo.Valid {
		message.ReplyToMessageID = modulecore.MessageID(replyTo.String)
	}
	return message, nil
}

func validateExternalCommitInput(bundle conversationruntime.ExternalCommitBundle) error {
	if err := validateExternalTurnLookup(bundle.ExpectedLookup); err != nil {
		return err
	}
	if bundle.ResultState != "completed" {
		return errors.New("external result_state must be completed")
	}
	if bundle.SafeError && bundle.FailureStage == "" || !bundle.SafeError && bundle.FailureStage != "" {
		return errors.New("external safe-error stage is inconsistent")
	}
	if bundle.SafeError {
		switch bundle.FailureStage {
		case conversationruntime.ExternalFailureStageReadView, conversationruntime.ExternalFailureStagePromptComposition, conversationruntime.ExternalFailureStageGeneration:
		default:
			return errors.New("external safe-error stage is unsupported")
		}
	}
	if len(bundle.CanonicalEvents) < 1 || len(bundle.CanonicalEvents) > 2 || len(bundle.MessageIndexes) != 1 {
		return errors.New("external result requires one or two canonical events and one response index")
	}
	if bundle.PublicationIntent.CanonicalResponseMessageID == "" ||
		bundle.PublicationIntent.StableLogicalKey != "external-publication:"+bundle.ExpectedLookup.OperationID ||
		bundle.PublicationIntent.Target != externalReplyTarget(bundle.ExpectedLookup) {
		return errors.New("external publication intent is not bound to its accepted source")
	}
	seenEvents := make(map[modulecore.EventID]struct{}, len(bundle.CanonicalEvents))
	for _, event := range bundle.CanonicalEvents {
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return fmt.Errorf("validate external event: %w", err)
		}
		if event.EventSeq != 0 || event.SessionID != bundle.ExpectedLookup.SessionID || event.TurnID != bundle.ExpectedLookup.TurnID {
			return errors.New("external event must be unsequenced and match the accepted turn")
		}
		if _, exists := seenEvents[event.EventID]; exists {
			return fmt.Errorf("duplicate external event_id %q", event.EventID)
		}
		seenEvents[event.EventID] = struct{}{}
	}
	return nil
}

func validateExternalTurnLookup(lookup conversationruntime.ExternalTurnLookup) error {
	if strings.TrimSpace(lookup.OwnerID) == "" || lookup.OwnerID != strings.TrimSpace(lookup.OwnerID) || lookup.GenerationEpoch <= 0 {
		return fmt.Errorf("%w: owner and generation epoch are required", ErrExternalTurnScopeMismatch)
	}
	if err := lookup.SessionID.Validate(); err != nil {
		return fmt.Errorf("%w: invalid session id: %v", ErrExternalTurnScopeMismatch, err)
	}
	if err := lookup.TurnID.Validate(); err != nil {
		return fmt.Errorf("%w: invalid turn id: %v", ErrExternalTurnScopeMismatch, err)
	}
	if lookup.OperationID != externalTurnOperationID(lookup.TurnID) {
		return fmt.Errorf("%w: operation id is not derived from the accepted turn", ErrExternalTurnScopeMismatch)
	}
	switch lookup.SourceBinding.Classification {
	case domainconversation.ExternalPeerClassificationRequest:
		if err := lookup.SourceBinding.ValidateNewRequest(); err != nil {
			return fmt.Errorf("%w: invalid source binding: %v", ErrExternalTurnScopeMismatch, err)
		}
	case domainconversation.ExternalPeerClassificationBrowserReplyFollowup:
		if err := lookup.SourceBinding.ValidateBrowserReplyFollowup(); err != nil {
			return fmt.Errorf("%w: invalid source binding: %v", ErrExternalTurnScopeMismatch, err)
		}
	default:
		return fmt.Errorf("%w: unsupported external source classification", ErrExternalTurnScopeMismatch)
	}
	if lookup.SourceCausationID == "" {
		if lookup.SourceHopCount != 0 || lookup.SourceBinding.Classification == domainconversation.ExternalPeerClassificationBrowserReplyFollowup {
			return fmt.Errorf("%w: browser followup requires its durable BOARD causal chain", ErrExternalTurnScopeMismatch)
		}
	} else if !validBoardPublicationID(lookup.SourceCausationID) || lookup.SourceHopCount < 0 ||
		lookup.SourceHopCount >= domainconversation.BoardCommunicationMaxHopCount ||
		(lookup.SourceBinding.Classification == domainconversation.ExternalPeerClassificationRequest &&
			(lookup.SourceCausationID != lookup.SourceBinding.MessageID || lookup.SourceHopCount != 0)) ||
		(lookup.SourceBinding.Classification == domainconversation.ExternalPeerClassificationBrowserReplyFollowup && lookup.SourceHopCount < 1) {
		return fmt.Errorf("%w: external source causal chain is invalid", ErrExternalTurnScopeMismatch)
	}
	fingerprint, err := hex.DecodeString(lookup.SourceFingerprint)
	if err != nil || len(fingerprint) != sha256.Size {
		return fmt.Errorf("%w: source fingerprint must be a SHA-256 digest", ErrExternalTurnScopeMismatch)
	}
	return nil
}

func externalSourceFingerprint(payload map[string]any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	fingerprint := sha256.Sum256(encoded)
	return hex.EncodeToString(fingerprint[:]), nil
}

func cloneExternalCommitBundle(bundle conversationruntime.ExternalCommitBundle) conversationruntime.ExternalCommitBundle {
	bundle.CanonicalEvents = append([]modulecore.EventEnvelope(nil), bundle.CanonicalEvents...)
	for index := range bundle.CanonicalEvents {
		bundle.CanonicalEvents[index].DependencyEventIDs = append([]modulecore.EventID(nil), bundle.CanonicalEvents[index].DependencyEventIDs...)
		bundle.CanonicalEvents[index].Payload = cloneMutablePayload(bundle.CanonicalEvents[index].Payload)
	}
	bundle.MessageIndexes = append([]conversationruntime.MessageIndex(nil), bundle.MessageIndexes...)
	return bundle
}

func externalReplyTarget(lookup conversationruntime.ExternalTurnLookup) conversationruntime.ExternalReplyTarget {
	binding := lookup.SourceBinding
	target := conversationruntime.ExternalReplyTarget{
		BoardInstanceID: binding.BoardInstanceID, CorePrincipalID: binding.RecipientPrincipalID,
		PeerPrincipalID: binding.SenderPrincipalID, ChannelID: binding.ChannelID,
		ThreadID: binding.ThreadID, ReplyTo: binding.MessageID,
	}
	if lookup.SourceCausationID != "" {
		target.CausationID = lookup.SourceCausationID
		target.HopCount = lookup.SourceHopCount + 1
	}
	return target
}

func externalResponseEvent(events []modulecore.EventEnvelope, messageID modulecore.MessageID) *modulecore.EventEnvelope {
	for index := range events {
		if events[index].MessageID == messageID {
			return &events[index]
		}
	}
	return nil
}

func externalTurnOperationID(turnID modulecore.TurnID) string {
	return "conversation.output:" + string(turnID)
}

func externalBoardPublicationWorkID(operationID string) string {
	return "board-publication:" + operationID
}

func eventStoreMessageIndex(index conversationruntime.MessageIndex) MessageIndex {
	return MessageIndex{
		MessageID: index.MessageID, EventID: index.EventID, SessionID: index.SessionID, TurnID: index.TurnID,
		FromActor: index.FromActor, ToKind: index.ToKind, ToActor: index.ToActor, Kind: index.Kind,
		ReplyToMessageID: index.ReplyToMessageID, Visibility: index.Visibility,
	}
}

func applicationMessageIndex(index MessageIndex) conversationruntime.MessageIndex {
	return conversationruntime.MessageIndex{
		MessageID: index.MessageID, EventID: index.EventID, SessionID: index.SessionID, TurnID: index.TurnID,
		FromActor: index.FromActor, ToKind: index.ToKind, ToActor: index.ToActor, Kind: index.Kind,
		ReplyToMessageID: index.ReplyToMessageID, Visibility: index.Visibility,
	}
}

func externalCommitMatchesOutcome(bundle conversationruntime.ExternalCommitBundle, outcome conversationruntime.ExternalCommittedBundle) bool {
	if outcome.Lookup != bundle.ExpectedLookup || outcome.ResultState != bundle.ResultState ||
		outcome.SafeError != bundle.SafeError || outcome.FailureStage != bundle.FailureStage ||
		outcome.InvocationReceipt != bundle.InvocationReceipt || outcome.PublicationIntent != bundle.PublicationIntent ||
		len(outcome.CanonicalEvents) != len(bundle.CanonicalEvents) || len(outcome.MessageIndexes) != len(bundle.MessageIndexes) {
		return false
	}
	for index, event := range bundle.CanonicalEvents {
		expected := event
		expected.EventSeq = outcome.CanonicalEvents[index].EventSeq
		if !reflect.DeepEqual(expected, outcome.CanonicalEvents[index]) {
			return false
		}
	}
	return reflect.DeepEqual(bundle.MessageIndexes, outcome.MessageIndexes)
}
