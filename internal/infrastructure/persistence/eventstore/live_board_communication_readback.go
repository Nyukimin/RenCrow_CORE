package eventstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrBoardCommunicationReadbackUnavailable   = errors.New("BOARD communication readback store is unavailable")
	ErrBoardCommunicationReadbackScopeMismatch = errors.New("BOARD communication readback scope does not match the pinned source")
	ErrBoardCommunicationReadbackConflict      = errors.New("BOARD communication readback conflicts with an immutable source")
	ErrBoardCommunicationReadbackCorrupt       = errors.New("BOARD communication readback receipt or canonical source is corrupt")
)

const (
	maxBoardOwnerRequestPendingPageSize = 32
	boardTerminalReplyOperationPrefix   = "board-terminal-reply:"
	boardTerminalReplyOperationKind     = "board_terminal_reply_v1"
	boardTerminalReplyEventType         = "conversation.external_peer_terminal_reply"
	boardTerminalReplyEventComponent    = "boardcommunication"
)

// BoardTerminalReplySource is an authenticated BOARD terminal reply and its
// canonical inbox/thread metadata. It does not authorize another CORE turn.
type BoardTerminalReplySource struct {
	Scope           BoardPublicationScope                  `json:"scope"`
	Binding         domainconversation.ExternalPeerBinding `json:"binding"`
	InboxItemID     string                                 `json:"inbox_item_id"`
	InboxCreatedAt  time.Time                              `json:"inbox_created_at"`
	MessageAt       time.Time                              `json:"message_at"`
	ThreadCreatedAt time.Time                              `json:"thread_created_at"`
	Body            string                                 `json:"body"`
	Recipients      []string                               `json:"recipients"`
	Mentions        []string                               `json:"mentions"`
	CausationID     string                                 `json:"causation_id"`
	HopCount        int                                    `json:"hop_count"`
}

// BoardTerminalReplyReceipt contains references and fingerprints only. The
// canonical terminal event is the sole durable authority for the reply body.
type BoardTerminalReplyReceipt struct {
	OperationID            string                `json:"operation_id"`
	Scope                  BoardPublicationScope `json:"scope"`
	BoardMessageID         string                `json:"board_message_id"`
	ParentBoardMessageID   string                `json:"parent_board_message_id"`
	BoardPublicationWorkID string                `json:"board_publication_work_id"`
	SessionID              modulecore.SessionID  `json:"session_id"`
	TurnID                 modulecore.TurnID     `json:"turn_id"`
	ParentCoreEventID      modulecore.EventID    `json:"parent_core_event_id"`
	ParentCoreMessageID    modulecore.MessageID  `json:"parent_core_message_id"`
	EventID                modulecore.EventID    `json:"event_id"`
	EventSeq               modulecore.EventSeq   `json:"event_seq"`
	SourceSHA256           string                `json:"source_sha256"`
	BodySHA256             string                `json:"body_sha256"`
}

type BoardPublicationTurnLookupInput struct {
	Scope            BoardPublicationScope
	SessionID        modulecore.SessionID
	TurnID           modulecore.TurnID
	ResponderActorID string `json:"responder_actor_id,omitempty"`
}

type BoardOwnerRequestPending struct {
	Accepted    AcceptedTurn
	Reservation BoardOwnerRequestReservation
	Instruction string
	Lookup      conversationruntime.BoardOwnerRequestLookup
}

type BoardOwnerRequestPendingPage struct {
	Items            []BoardOwnerRequestPending
	NextAdmissionSeq modulecore.EventSeq
}

type boardTerminalReplyWriteResult struct {
	Receipt  BoardTerminalReplyReceipt
	Recorded bool
}

type boardTerminalReplyParent struct {
	Outcome BoardPublicationOutcome
	Lease   BoardPublicationLease
	Event   modulecore.EventEnvelope
}

type boardTerminalReplyReceiptInputHash struct {
	Source               BoardTerminalReplySource
	ParentWorkID         string               `json:"parent_work_id"`
	ParentBoardMessageID string               `json:"parent_board_message_id"`
	ParentCoreEventID    modulecore.EventID   `json:"parent_core_event_id"`
	ParentCoreMessageID  modulecore.MessageID `json:"parent_core_message_id"`
	SessionID            modulecore.SessionID `json:"session_id"`
	TurnID               modulecore.TurnID    `json:"turn_id"`
}

// RecordBoardTerminalReply correlates a current authenticated BOARD reply to
// the exact completed new-root publication before appending its private event.
func (s *SQLiteStore) RecordBoardTerminalReply(ctx context.Context, source BoardTerminalReplySource) (BoardTerminalReplyReceipt, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardTerminalReplyReceipt{}, false, err
	}
	if ctx == nil {
		return BoardTerminalReplyReceipt{}, false, errors.New("BOARD terminal reply context is required")
	}
	if err := validateBoardTerminalReplySource(source); err != nil {
		return BoardTerminalReplyReceipt{}, false, err
	}
	if s.shouldQueueWrite(ctx) {
		queuedSource := cloneQueuedValue(source)
		result, err := enqueueOrderedWriteResult(s, ctx, 1, queuedSource, func(commandCtx context.Context) (boardTerminalReplyWriteResult, error) {
			return s.recordBoardTerminalReplyInline(commandCtx, queuedSource)
		})
		if err != nil {
			return BoardTerminalReplyReceipt{}, false, err
		}
		return result.Receipt, result.Recorded, nil
	}
	result, err := s.recordBoardTerminalReplyInline(ctx, source)
	return result.Receipt, result.Recorded, err
}

func (s *SQLiteStore) recordBoardTerminalReplyInline(ctx context.Context, source BoardTerminalReplySource) (boardTerminalReplyWriteResult, error) {
	operationID, err := boardTerminalReplyOperationID(source)
	if err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := requireBoardTerminalReplyPinTx(ctx, tx, source); err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	if err := validateBoardTerminalReplySourceAtPin(ctx, tx, source); err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	prior, priorFound, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	if priorFound && prior.operationKind != boardTerminalReplyOperationKind {
		return boardTerminalReplyWriteResult{}, ErrBoardCommunicationReadbackCorrupt
	}
	parent, parentFound, err := loadBoardTerminalReplyParentTx(ctx, tx, source)
	if err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	if !parentFound {
		if priorFound {
			return boardTerminalReplyWriteResult{}, ErrBoardCommunicationReadbackCorrupt
		}
		return boardTerminalReplyWriteResult{}, nil
	}
	if err := validateBoardTerminalReplyAgainstParent(source, parent); err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	inputHash, err := boardTerminalReplyInputHash(source, parent)
	if err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	if priorFound {
		if prior.inputSHA256 != inputHash {
			return boardTerminalReplyWriteResult{}, ErrBoardCommunicationReadbackConflict
		}
		receipt, err := decodeBoardTerminalReplyReceipt(prior)
		if err != nil {
			return boardTerminalReplyWriteResult{}, err
		}
		if err := validateBoardTerminalReplyReceipt(ctx, tx, receipt, source, parent); err != nil {
			return boardTerminalReplyWriteResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return boardTerminalReplyWriteResult{}, err
		}
		committed = true
		return boardTerminalReplyWriteResult{Receipt: receipt, Recorded: true}, nil
	}

	event, err := newBoardTerminalReplyEvent(source, parent)
	if err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	event.EventSeq, err = s.nextSequence(ctx)
	if err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	if err := s.validateReferences(ctx, tx, event); err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	storedEvent, err := insertLiveEvent(ctx, tx, event)
	if err != nil {
		return boardTerminalReplyWriteResult{}, fmt.Errorf("insert canonical BOARD terminal reply event: %w", err)
	}
	receipt := newBoardTerminalReplyReceipt(operationID, source, parent, storedEvent)
	if err := insertOperationReceipt(ctx, tx, operationID, boardTerminalReplyOperationKind, inputHash, receipt); err != nil {
		return boardTerminalReplyWriteResult{}, fmt.Errorf("insert immutable BOARD terminal reply receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return boardTerminalReplyWriteResult{}, err
	}
	committed = true
	return boardTerminalReplyWriteResult{Receipt: receipt, Recorded: true}, nil
}

func validateBoardTerminalReplySource(source BoardTerminalReplySource) error {
	if err := validateBoardPublicationScope(source.Scope); err != nil {
		return ErrBoardCommunicationReadbackScopeMismatch
	}
	if err := source.Binding.Validate(); err != nil {
		return fmt.Errorf("validate BOARD terminal reply binding: %w", err)
	}
	if source.Binding.Classification != domainconversation.ExternalPeerClassificationCorrelatedReplyTerminal ||
		source.Binding.WireKind != "reply" || source.Binding.ReplyTo == "" || source.Binding.MessageID == source.Binding.ReplyTo ||
		source.Binding.BoardInstanceID != source.Scope.JournalInstanceID || source.Binding.RecipientPrincipalID != source.Scope.PrincipalID {
		return ErrBoardCommunicationReadbackScopeMismatch
	}
	if source.InboxItemID == "" || strings.TrimSpace(source.InboxItemID) != source.InboxItemID || strings.TrimSpace(source.Body) == "" ||
		!validBoardPeerReference(source.CausationID) || source.HopCount <= 0 {
		return ErrBoardCommunicationReadbackConflict
	}
	for name, value := range map[string]time.Time{
		"inbox_created_at":  source.InboxCreatedAt,
		"message_at":        source.MessageAt,
		"thread_created_at": source.ThreadCreatedAt,
	} {
		if value.IsZero() || value.Location() != time.UTC {
			return fmt.Errorf("BOARD terminal reply %s must be a fixed UTC timestamp", name)
		}
	}
	if len(source.Recipients) == 0 || !containsExactString(source.Recipients, source.Scope.PrincipalID) {
		return ErrBoardCommunicationReadbackScopeMismatch
	}
	if err := validateBoardPeerPrincipalList(source.Recipients, "terminal recipients", false); err != nil {
		return err
	}
	if err := validateBoardPeerPrincipalList(source.Mentions, "terminal mentions", true); err != nil {
		return err
	}
	return nil
}

func validateBoardTerminalReplySourceAtPin(ctx context.Context, tx *sql.Tx, source BoardTerminalReplySource) error {
	pin, err := requireBoardTerminalReplyPinTx(ctx, tx, source)
	if err != nil {
		return err
	}
	if !source.InboxCreatedAt.After(pin.CutoverAt) {
		return ErrBoardCommunicationReadbackScopeMismatch
	}
	return nil
}

func requireBoardTerminalReplyPinTx(ctx context.Context, tx *sql.Tx, source BoardTerminalReplySource) (BoardConnectionPin, error) {
	pin, err := requireBoardPublicationPeerPinTx(ctx, tx, source.Scope, source.Binding)
	if err != nil {
		return BoardConnectionPin{}, fmt.Errorf("%w: %w", ErrBoardCommunicationReadbackScopeMismatch, err)
	}
	return pin, nil
}

func requireBoardCommunicationPinTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope) (BoardConnectionPin, error) {
	pin, err := requireBoardPublicationPinTx(ctx, tx, scope)
	if err != nil {
		return BoardConnectionPin{}, fmt.Errorf("%w: %w", ErrBoardCommunicationReadbackScopeMismatch, err)
	}
	return pin, nil
}

func requireBoardOwnerRequestPinForResponderTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope, responderActorID string) error {
	if err := validateBoardPublicationScope(scope); err != nil {
		return ErrBoardCommunicationReadbackScopeMismatch
	}
	if responderActorID != "" {
		if conversationruntime.ValidateBoardOwnerRequestTargetActorID(responderActorID) != nil {
			return ErrBoardCommunicationReadbackScopeMismatch
		}
		if responderActorID != "mio" && responderActorID != scope.PrincipalID {
			return ErrBoardCommunicationReadbackScopeMismatch
		}
	}
	if responderActorID == "" || responderActorID == "mio" {
		if _, err := requireBoardCommunicationPinTx(ctx, tx, scope); err != nil {
			return err
		}
		return nil
	}
	pin, found, err := readBoardConnectionPinForPrincipalTx(ctx, tx, scope.PrincipalID)
	if err != nil {
		return err
	}
	if !found || !boardConnectionPinMatchesPublicationScope(pin, scope) {
		return ErrBoardCommunicationReadbackScopeMismatch
	}
	return nil
}

func requireBoardCommunicationPinForResponderTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope, responderActorID string) (BoardConnectionPin, error) {
	if responderActorID == "" {
		return requireBoardCommunicationPinTx(ctx, tx, scope)
	}
	pin, err := requireBoardPeerPinTx(ctx, tx, scope, responderActorID)
	if err != nil {
		return BoardConnectionPin{}, fmt.Errorf("%w: %w", ErrBoardCommunicationReadbackScopeMismatch, err)
	}
	return pin, nil
}

func loadBoardTerminalReplyParentTx(ctx context.Context, tx *sql.Tx, source BoardTerminalReplySource) (boardTerminalReplyParent, bool, error) {
	var workID string
	err := tx.QueryRowContext(ctx, `SELECT work_id FROM live_board_publication_outcome WHERE board_message_id=?`, source.Binding.ReplyTo).Scan(&workID)
	if errors.Is(err, sql.ErrNoRows) {
		return boardTerminalReplyParent{}, false, nil
	}
	if err != nil {
		return boardTerminalReplyParent{}, false, err
	}
	outcome, found, err := loadBoardPublicationOutcomeByWorkTx(ctx, tx, workID)
	if err != nil {
		return boardTerminalReplyParent{}, false, err
	}
	if !found {
		return boardTerminalReplyParent{}, false, ErrBoardCommunicationReadbackCorrupt
	}
	if outcome.Scope != source.Scope {
		return boardTerminalReplyParent{}, false, ErrBoardCommunicationReadbackScopeMismatch
	}
	lease, _, work, state, err := loadBoardPublicationSourceTx(ctx, tx, workID, source.Scope)
	if err != nil {
		return boardTerminalReplyParent{}, false, err
	}
	if work.WorkID != workID || state != "completed" {
		return boardTerminalReplyParent{}, false, ErrBoardCommunicationReadbackCorrupt
	}
	return boardTerminalReplyParent{Outcome: outcome, Lease: lease, Event: lease.CanonicalResponse}, true, nil
}

func validateBoardTerminalReplyAgainstParent(source BoardTerminalReplySource, parent boardTerminalReplyParent) error {
	outcome, lease, message := parent.Outcome, parent.Lease, parent.Outcome.CanonicalMessage
	target := lease.PublicationIntent.Target
	validParentRoute := false
	switch lease.TargetKind {
	case boardPublicationTargetNewRoot:
		validParentRoute = target.ThreadID == "" && target.ReplyTo == "" && strings.TrimSpace(lease.Title) != "" &&
			message.ReplyTo == "" && message.CausationID == message.MessageID && message.HopCount == 0
	case boardPublicationTargetReply:
		validParentRoute = target.ThreadID != "" && target.ReplyTo != "" && lease.Title == "" &&
			message.ThreadID == target.ThreadID && message.ReplyTo == target.ReplyTo &&
			message.CausationID == target.ReplyTo && message.HopCount == 1
	}
	if !validParentRoute || lease.Scope != source.Scope ||
		target.BoardInstanceID != source.Scope.JournalInstanceID || target.CorePrincipalID != source.Scope.PrincipalID ||
		outcome.ExpectedPeerPrincipalID != target.PeerPrincipalID || message.MessageID != source.Binding.ReplyTo ||
		message.ThreadID == "" || message.ChannelID != target.ChannelID || message.SenderPrincipalID != target.CorePrincipalID ||
		len(message.Recipients) != 1 || message.Recipients[0] != target.PeerPrincipalID ||
		source.CausationID != message.CausationID || source.HopCount != message.HopCount+1 ||
		outcome.SendReceipt.MessageID != message.MessageID || outcome.SendReceipt.ThreadID != message.ThreadID ||
		source.Binding.BoardInstanceID != target.BoardInstanceID || source.Binding.RecipientPrincipalID != target.CorePrincipalID ||
		source.Binding.ChannelID != target.ChannelID || source.Binding.ThreadID != message.ThreadID ||
		source.Binding.SenderPrincipalID != outcome.ExpectedPeerPrincipalID || source.Binding.ReplyTo != message.MessageID ||
		source.Binding.MessageID == message.MessageID ||
		!containsExactString(source.Recipients, target.CorePrincipalID) {
		return ErrBoardCommunicationReadbackConflict
	}
	response, index := parent.Event, lease.ResponseIndex
	if response.EventID == "" || response.MessageID == "" || response.SessionID == "" || response.TurnID == "" || response.TraceID == "" ||
		index.MessageID != response.MessageID || index.EventID != response.EventID || index.SessionID != response.SessionID || index.TurnID != response.TurnID ||
		response.EventID != modulecore.EventID(lease.CanonicalResponse.EventID) {
		return ErrBoardCommunicationReadbackCorrupt
	}
	return nil
}

func boardTerminalReplyOperationID(source BoardTerminalReplySource) (string, error) {
	identity := struct {
		Scope           BoardPublicationScope `json:"scope"`
		BoardInstanceID string                `json:"board_instance_id"`
		MessageID       string                `json:"message_id"`
	}{Scope: source.Scope, BoardInstanceID: source.Binding.BoardInstanceID, MessageID: source.Binding.MessageID}
	hash, err := hashJSON(identity)
	if err != nil {
		return "", fmt.Errorf("hash BOARD terminal reply identity: %w", err)
	}
	return boardTerminalReplyOperationPrefix + hash, nil
}

func boardTerminalReplyInputHash(source BoardTerminalReplySource, parent boardTerminalReplyParent) (string, error) {
	return hashJSON(boardTerminalReplyReceiptInputHash{
		Source: source, ParentWorkID: parent.Outcome.WorkID, ParentBoardMessageID: parent.Outcome.CanonicalMessage.MessageID,
		ParentCoreEventID: parent.Event.EventID, ParentCoreMessageID: parent.Event.MessageID,
		SessionID: parent.Event.SessionID, TurnID: parent.Event.TurnID,
	})
}

func newBoardTerminalReplyReceipt(operationID string, source BoardTerminalReplySource, parent boardTerminalReplyParent, event modulecore.EventEnvelope) BoardTerminalReplyReceipt {
	sourceHash, _ := hashJSON(source)
	return BoardTerminalReplyReceipt{
		OperationID: operationID, Scope: source.Scope, BoardMessageID: source.Binding.MessageID,
		ParentBoardMessageID: parent.Outcome.CanonicalMessage.MessageID, BoardPublicationWorkID: parent.Outcome.WorkID,
		SessionID: parent.Event.SessionID, TurnID: parent.Event.TurnID,
		ParentCoreEventID: parent.Event.EventID, ParentCoreMessageID: parent.Event.MessageID,
		EventID: event.EventID, EventSeq: event.EventSeq, SourceSHA256: sourceHash,
		BodySHA256: boardPublicationDigest([]byte(source.Body)),
	}
}

func newBoardTerminalReplyEvent(source BoardTerminalReplySource, parent boardTerminalReplyParent) (modulecore.EventEnvelope, error) {
	actorID, err := source.Binding.SourceActorID()
	if err != nil {
		return modulecore.EventEnvelope{}, err
	}
	event := modulecore.NewEventEnvelope(
		parent.Event.TraceID, parent.Event.EventID, nil, boardTerminalReplyEventComponent,
		boardTerminalReplyEventType, source.MessageAt, boardTerminalReplyEventPayload(source, parent, actorID),
	)
	event.SessionID = parent.Event.SessionID
	event.TurnID = parent.Event.TurnID
	return event, nil
}

func boardTerminalReplyEventPayload(source BoardTerminalReplySource, parent boardTerminalReplyParent, actorID string) map[string]any {
	return map[string]any{
		"from": "external_peer", "source_actor_id": actorID, "visibility": "private",
		"text": source.Body, "content": source.Body, "body": map[string]any{"text": source.Body},
		"board_terminal_reply_binding": source.Binding.ToPayload(), "board_source": boardTerminalReplySourcePayload(source),
		"parent_board_message_id": parent.Outcome.CanonicalMessage.MessageID,
		"parent_core_event_id":    string(parent.Event.EventID), "parent_core_message_id": string(parent.Event.MessageID),
		"board_publication_work_id": parent.Outcome.WorkID,
	}
}

func boardTerminalReplySourcePayload(source BoardTerminalReplySource) map[string]any {
	return map[string]any{
		"inbox_item_id":     source.InboxItemID,
		"inbox_created_at":  source.InboxCreatedAt.UTC().Format(time.RFC3339Nano),
		"message_at":        source.MessageAt.UTC().Format(time.RFC3339Nano),
		"thread_created_at": source.ThreadCreatedAt.UTC().Format(time.RFC3339Nano),
		"recipients":        append([]string(nil), source.Recipients...), "mentions": append([]string(nil), source.Mentions...),
		"causation_id": source.CausationID, "hop_count": source.HopCount,
	}
}

func decodeBoardTerminalReplyReceipt(receipt operationReceipt) (BoardTerminalReplyReceipt, error) {
	if receipt.operationKind != boardTerminalReplyOperationKind {
		return BoardTerminalReplyReceipt{}, ErrBoardCommunicationReadbackCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(receipt.resultJSON)))
	decoder.DisallowUnknownFields()
	var stored BoardTerminalReplyReceipt
	if err := decoder.Decode(&stored); err != nil {
		return BoardTerminalReplyReceipt{}, ErrBoardCommunicationReadbackCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BoardTerminalReplyReceipt{}, ErrBoardCommunicationReadbackCorrupt
	}
	encoded, err := json.Marshal(stored)
	if err != nil || !bytes.Equal(encoded, []byte(receipt.resultJSON)) ||
		stored.OperationID == "" || validateBoardPublicationScope(stored.Scope) != nil ||
		stored.BoardMessageID == "" || stored.ParentBoardMessageID == "" || stored.BoardPublicationWorkID == "" ||
		stored.SessionID.Validate() != nil || stored.TurnID.Validate() != nil || stored.ParentCoreEventID.Validate() != nil ||
		stored.ParentCoreMessageID.Validate() != nil || stored.EventID.Validate() != nil || stored.EventSeq <= 0 ||
		!validBoardPublicationDigest(stored.SourceSHA256) || !validBoardPublicationDigest(stored.BodySHA256) {
		return BoardTerminalReplyReceipt{}, ErrBoardCommunicationReadbackCorrupt
	}
	return stored, nil
}

func validateBoardTerminalReplyReceipt(ctx context.Context, tx *sql.Tx, receipt BoardTerminalReplyReceipt, source BoardTerminalReplySource, parent boardTerminalReplyParent) error {
	expected := newBoardTerminalReplyReceipt(receipt.OperationID, source, parent, modulecore.EventEnvelope{
		EventID: receipt.EventID, EventSeq: receipt.EventSeq,
	})
	if receipt.OperationID == "" || receipt.Scope != expected.Scope || receipt.BoardMessageID != expected.BoardMessageID ||
		receipt.ParentBoardMessageID != expected.ParentBoardMessageID || receipt.BoardPublicationWorkID != expected.BoardPublicationWorkID ||
		receipt.SessionID != expected.SessionID || receipt.TurnID != expected.TurnID ||
		receipt.ParentCoreEventID != expected.ParentCoreEventID || receipt.ParentCoreMessageID != expected.ParentCoreMessageID ||
		receipt.SourceSHA256 != expected.SourceSHA256 || receipt.BodySHA256 != expected.BodySHA256 ||
		receipt.EventID == "" || receipt.EventSeq <= 0 {
		return ErrBoardCommunicationReadbackCorrupt
	}
	event, err := loadEventEnvelopeTx(ctx, tx, receipt.EventID)
	if err != nil {
		return ErrBoardCommunicationReadbackCorrupt
	}
	actorID, err := source.Binding.SourceActorID()
	if err != nil {
		return ErrBoardCommunicationReadbackCorrupt
	}
	if modulecore.ValidateEventEnvelope(event) != nil || event.EventID != receipt.EventID || event.EventSeq != receipt.EventSeq ||
		event.EventType != boardTerminalReplyEventType || event.ComponentID != boardTerminalReplyEventComponent ||
		event.TraceID != parent.Event.TraceID || event.CausationEventID != parent.Event.EventID ||
		event.SessionID != parent.Event.SessionID || event.TurnID != parent.Event.TurnID || !event.OccurredAt.Equal(source.MessageAt) ||
		event.MessageID != "" || !canonicalBoardPeerJSONEqual(event.Payload, boardTerminalReplyEventPayload(source, parent, actorID)) {
		return ErrBoardCommunicationReadbackCorrupt
	}
	var messageIndexCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_message_index WHERE event_id=?`, string(receipt.EventID)).Scan(&messageIndexCount); err != nil {
		return err
	}
	if messageIndexCount != 0 {
		return ErrBoardCommunicationReadbackCorrupt
	}
	return nil
}

// LoadBoardPublicationByTurn returns only a verified, completed BOARD outcome
// for this owner-scoped canonical CORE turn.
func (s *SQLiteStore) LoadBoardPublicationByTurn(ctx context.Context, input BoardPublicationTurnLookupInput) (BoardPublicationOutcome, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	if ctx == nil {
		return BoardPublicationOutcome{}, false, errors.New("BOARD publication turn lookup context is required")
	}
	if err := validateBoardPublicationScope(input.Scope); err != nil || input.SessionID.Validate() != nil || input.TurnID.Validate() != nil {
		return BoardPublicationOutcome{}, false, ErrBoardCommunicationReadbackScopeMismatch
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	defer tx.Rollback()
	if _, err := requireBoardCommunicationPinForResponderTx(ctx, tx, input.Scope, input.ResponderActorID); err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	var sessionOwner string
	err = tx.QueryRowContext(ctx, `SELECT owner_id FROM live_conversation_session WHERE session_id=?`, string(input.SessionID)).Scan(&sessionOwner)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sessionOwner != input.Scope.OwnerID {
		return BoardPublicationOutcome{}, false, ErrBoardCommunicationReadbackScopeMismatch
	}
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	var turnOwner, turnSession string
	err = tx.QueryRowContext(ctx, `SELECT owner_id,session_id FROM live_conversation_turn WHERE turn_id=?`, string(input.TurnID)).Scan(&turnOwner, &turnSession)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (turnOwner != input.Scope.OwnerID || turnSession != string(input.SessionID)) {
		return BoardPublicationOutcome{}, false, ErrBoardCommunicationReadbackScopeMismatch
	}
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT p.work_id
		FROM live_board_publication p
		JOIN live_work_outbox w ON w.work_id=p.work_id AND w.kind='board_publication'
		JOIN live_message_index m ON m.message_id=p.response_message_id AND m.event_id=w.source_event_id AND m.session_id=w.session_id
		JOIN live_conversation_turn t ON t.turn_id=m.turn_id AND t.session_id=m.session_id AND t.owner_id=?
		WHERE t.turn_id=? AND t.session_id=?
		ORDER BY p.work_id LIMIT 2`, input.Scope.OwnerID, string(input.TurnID), string(input.SessionID))
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	workIDs := make([]string, 0, 2)
	for rows.Next() {
		var workID string
		if err := rows.Scan(&workID); err != nil {
			_ = rows.Close()
			return BoardPublicationOutcome{}, false, err
		}
		workIDs = append(workIDs, workID)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return BoardPublicationOutcome{}, false, rowsErr
	}
	if closeErr != nil {
		return BoardPublicationOutcome{}, false, closeErr
	}
	if len(workIDs) > 1 {
		return BoardPublicationOutcome{}, false, ErrBoardCommunicationReadbackCorrupt
	}
	if len(workIDs) == 0 {
		if err := tx.Commit(); err != nil {
			return BoardPublicationOutcome{}, false, err
		}
		return BoardPublicationOutcome{}, false, nil
	}
	_, _, work, state, err := loadBoardPublicationSourceTx(ctx, tx, workIDs[0], input.Scope)
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	if work.WorkID != workIDs[0] || work.SessionID != input.SessionID || work.Kind != "board_publication" {
		return BoardPublicationOutcome{}, false, ErrBoardCommunicationReadbackCorrupt
	}
	if state != "completed" {
		exists, err := boardPublicationOutcomeExistsTx(ctx, tx, work.WorkID)
		if err != nil {
			return BoardPublicationOutcome{}, false, err
		}
		if exists {
			return BoardPublicationOutcome{}, false, ErrBoardCommunicationReadbackCorrupt
		}
		if err := tx.Commit(); err != nil {
			return BoardPublicationOutcome{}, false, err
		}
		return BoardPublicationOutcome{}, false, nil
	}
	outcome, found, err := loadBoardPublicationOutcomeByWorkTx(ctx, tx, work.WorkID)
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	if !found || outcome.Scope != input.Scope {
		return BoardPublicationOutcome{}, false, ErrBoardCommunicationReadbackCorrupt
	}
	if err := tx.Commit(); err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	return outcome, true, nil
}

// ReadPendingBoardOwnerRequests reads a bounded page of accepted owner
// requests that have no committed generated response yet.
func (s *SQLiteStore) ReadPendingBoardOwnerRequests(ctx context.Context, scope BoardPublicationScope, afterAdmissionSeq modulecore.EventSeq, limit int) (BoardOwnerRequestPendingPage, error) {
	return s.ReadPendingBoardOwnerRequestsForResponder(ctx, scope, "", afterAdmissionSeq, limit)
}

// ReadPendingBoardOwnerRequestsForResponder reads a bounded page for one fixed
// owner request actor. The target actor predicate is applied before paging.
func (s *SQLiteStore) ReadPendingBoardOwnerRequestsForResponder(ctx context.Context, scope BoardPublicationScope, responderActorID string, afterAdmissionSeq modulecore.EventSeq, limit int) (BoardOwnerRequestPendingPage, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardOwnerRequestPendingPage{}, err
	}
	if ctx == nil {
		return BoardOwnerRequestPendingPage{}, errors.New("pending BOARD owner request context is required")
	}
	if afterAdmissionSeq < 0 || limit < 1 || limit > maxBoardOwnerRequestPendingPageSize {
		return BoardOwnerRequestPendingPage{}, errors.New("pending BOARD owner request cursor or page limit is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BoardOwnerRequestPendingPage{}, err
	}
	defer tx.Rollback()
	if err := requireBoardOwnerRequestPinForResponderTx(ctx, tx, scope, responderActorID); err != nil {
		return BoardOwnerRequestPendingPage{}, err
	}
	targetActorPredicate := "COALESCE(json_extract(e.envelope_json,'$.payload.board_owner_request_binding.target_actor_id'),'')=''"
	queryArgs := []any{scope.OwnerID, scope.OwnerID, int64(afterAdmissionSeq), scope.JournalInstanceID, scope.PrincipalID}
	switch responderActorID {
	case "mio":
		targetActorPredicate = "COALESCE(json_extract(e.envelope_json,'$.payload.board_owner_request_binding.target_actor_id'),'') IN ('','mio')"
	case "":
	default:
		targetActorPredicate = "json_extract(e.envelope_json,'$.payload.board_owner_request_binding.target_actor_id')=?"
		queryArgs = append(queryArgs, responderActorID)
	}
	queryArgs = append(queryArgs, limit+1)
	query := "SELECT t.turn_id,t.admission_seq\n" +
		"FROM live_conversation_turn t\n" +
		"JOIN live_conversation_session s ON s.session_id=t.session_id\n" +
		"JOIN live_message_index m ON m.message_id=t.input_message_id AND m.session_id=t.session_id AND m.turn_id=t.turn_id\n" +
		"JOIN event_envelope e ON e.event_id=m.event_id\n" +
		"WHERE t.owner_id=? AND s.owner_id=? AND t.trigger_kind='board_owner_request'\n" +
		"  AND t.state='accepted' AND t.result_receipt_id IS NULL AND t.admission_seq>?\n" +
		"  AND json_extract(e.envelope_json,'$.payload.board_owner_request_binding.board_instance_id')=?\n" +
		"  AND json_extract(e.envelope_json,'$.payload.board_owner_request_binding.core_principal_id')=?\n" +
		"  AND " + targetActorPredicate + "\n" +
		"ORDER BY t.admission_seq,t.turn_id LIMIT ?"
	rows, err := tx.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return BoardOwnerRequestPendingPage{}, err
	}
	type pendingCandidate struct {
		turnID       modulecore.TurnID
		admissionSeq modulecore.EventSeq
	}
	candidates := make([]pendingCandidate, 0, limit+1)
	for rows.Next() {
		var candidate pendingCandidate
		if err := rows.Scan(&candidate.turnID, &candidate.admissionSeq); err != nil {
			_ = rows.Close()
			return BoardOwnerRequestPendingPage{}, err
		}
		candidates = append(candidates, candidate)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return BoardOwnerRequestPendingPage{}, rowsErr
	}
	if closeErr != nil {
		return BoardOwnerRequestPendingPage{}, closeErr
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	page := BoardOwnerRequestPendingPage{Items: make([]BoardOwnerRequestPending, 0, len(candidates))}
	for _, candidate := range candidates {
		accepted, message, ownerID, sessionOwnerID, resultReceiptID, err := boardOwnerRequestAcceptedTurnTx(ctx, tx, candidate.turnID)
		if err != nil {
			return BoardOwnerRequestPendingPage{}, err
		}
		if accepted.AdmissionSeq != candidate.admissionSeq || accepted.State != "accepted" || resultReceiptID != "" ||
			accepted.TriggerKind != "board_owner_request" || ownerID != scope.OwnerID || sessionOwnerID != scope.OwnerID {
			return BoardOwnerRequestPendingPage{}, ErrBoardOwnerRequestCorrupt
		}
		source, err := boardOwnerRequestSourceFromAcceptedEvent(accepted.InputEvent, ownerID)
		if err != nil {
			return BoardOwnerRequestPendingPage{}, err
		}
		if !boardOwnerRequestTargetMatchesResponder(source.TargetActorID, responderActorID) ||
			source.BoardInstanceID != scope.JournalInstanceID || source.CorePrincipalID != scope.PrincipalID {
			return BoardOwnerRequestPendingPage{}, ErrBoardOwnerRequestCorrupt
		}
		reservation, err := loadBoardOwnerRequestReservationTx(ctx, tx, source)
		if err != nil {
			return BoardOwnerRequestPendingPage{}, err
		}
		if err := validateBoardOwnerRequestAcceptedRecord(ctx, tx, accepted, message, ownerID, sessionOwnerID, reservation); err != nil {
			return BoardOwnerRequestPendingPage{}, err
		}
		instruction := boardOwnerRequestPayloadText(accepted.InputEvent.Payload)
		lookup := conversationruntime.BoardOwnerRequestLookup{
			Source: source, OwnerID: ownerID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
			GenerationEpoch: accepted.GenerationEpoch, OperationID: boardOwnerRequestOutputOperationID(accepted.TurnID),
		}
		lookup.SourceFingerprint, err = boardOwnerRequestFingerprint(source, instruction)
		if err != nil {
			return BoardOwnerRequestPendingPage{}, err
		}
		if err := validateBoardOwnerRequestLookup(lookup); err != nil {
			return BoardOwnerRequestPendingPage{}, ErrBoardOwnerRequestCorrupt
		}
		if _, found, err := loadBoardOwnerRequestOutcomeTx(ctx, tx, lookup); err != nil {
			return BoardOwnerRequestPendingPage{}, err
		} else if found {
			return BoardOwnerRequestPendingPage{}, ErrBoardOwnerRequestCorrupt
		}
		page.Items = append(page.Items, BoardOwnerRequestPending{
			Accepted: accepted, Reservation: reservation, Instruction: instruction, Lookup: lookup,
		})
		page.NextAdmissionSeq = accepted.AdmissionSeq
	}
	if err := tx.Commit(); err != nil {
		return BoardOwnerRequestPendingPage{}, err
	}
	return page, nil
}

func boardOwnerRequestSourceFromAcceptedEvent(event modulecore.EventEnvelope, ownerID string) (conversationruntime.BoardOwnerRequestSource, error) {
	binding, ok := event.Payload["board_owner_request_binding"].(map[string]any)
	legacyBindingKeys := []string{"board_instance_id", "core_principal_id", "channel_id", "peer_principal_id", "title", "idempotency_key"}
	explicitBindingKeys := []string{"board_instance_id", "core_principal_id", "channel_id", "peer_principal_id", "title", "idempotency_key", "target_actor_id"}
	if !ok || !boardOwnerRequestExactStringKeys(binding, legacyBindingKeys...) &&
		!boardOwnerRequestExactStringKeys(binding, explicitBindingKeys...) {
		return conversationruntime.BoardOwnerRequestSource{}, ErrBoardOwnerRequestCorrupt
	}
	stringValue := func(name string) (string, bool) {
		value, ok := binding[name].(string)
		return value, ok
	}
	boardInstanceID, okBoard := stringValue("board_instance_id")
	corePrincipalID, okCore := stringValue("core_principal_id")
	channelID, okChannel := stringValue("channel_id")
	peerPrincipalID, okPeer := stringValue("peer_principal_id")
	title, okTitle := stringValue("title")
	idempotencyKey, okKey := stringValue("idempotency_key")
	if !okBoard || !okCore || !okChannel || !okPeer || !okTitle || !okKey {
		return conversationruntime.BoardOwnerRequestSource{}, ErrBoardOwnerRequestCorrupt
	}
	targetActorID := ""
	if _, explicit := binding["target_actor_id"]; explicit {
		var ok bool
		targetActorID, ok = stringValue("target_actor_id")
		if !ok || conversationruntime.ValidateBoardOwnerRequestTargetActorID(targetActorID) != nil {
			return conversationruntime.BoardOwnerRequestSource{}, ErrBoardOwnerRequestCorrupt
		}
	}
	source := conversationruntime.BoardOwnerRequestSource{
		OwnerID: ownerID, BoardInstanceID: boardInstanceID, CorePrincipalID: corePrincipalID,
		ChannelID: channelID, PeerPrincipalID: peerPrincipalID, Title: title, IdempotencyKey: idempotencyKey,
		TargetActorID: targetActorID,
	}
	if !validBoardOwnerRequestSource(source, true) {
		return conversationruntime.BoardOwnerRequestSource{}, ErrBoardOwnerRequestCorrupt
	}
	return source, nil
}

func boardOwnerRequestTargetMatchesResponder(targetActorID, responderActorID string) bool {
	switch responderActorID {
	case "":
		return targetActorID == ""
	case "mio":
		return targetActorID == "" || targetActorID == "mio"
	default:
		return targetActorID == responderActorID
	}
}

func loadBoardOwnerRequestReplyMessageIDTx(
	ctx context.Context,
	tx *sql.Tx,
	scope BoardPublicationScope,
	accepted AcceptedTurn,
	ownerOutcome conversationruntime.BoardOwnerRequestOutcome,
	publicationOutcome BoardPublicationOutcome,
) (string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT operation_id,operation_kind,input_sha256,result_json
		FROM live_operation_receipt
		WHERE operation_kind=? AND json_extract(result_json,'$.parent_board_message_id')=?
		ORDER BY operation_id LIMIT 2`, boardTerminalReplyOperationKind, publicationOutcome.CanonicalMessage.MessageID)
	if err != nil {
		return "", err
	}
	type receiptRow struct {
		operationID string
		receipt     operationReceipt
	}
	receipts := make([]receiptRow, 0, 2)
	for rows.Next() {
		var row receiptRow
		if err := rows.Scan(&row.operationID, &row.receipt.operationKind, &row.receipt.inputSHA256, &row.receipt.resultJSON); err != nil {
			_ = rows.Close()
			return "", err
		}
		receipts = append(receipts, row)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return "", rowsErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(receipts) > 1 {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	if len(receipts) == 0 {
		return "", nil
	}
	row := receipts[0]
	receipt, err := decodeBoardTerminalReplyReceipt(row.receipt)
	if err != nil || receipt.OperationID != row.operationID || receipt.Scope != scope ||
		receipt.ParentBoardMessageID != publicationOutcome.CanonicalMessage.MessageID ||
		receipt.BoardPublicationWorkID != publicationOutcome.WorkID || receipt.SessionID != accepted.SessionID ||
		receipt.TurnID != accepted.TurnID || receipt.ParentCoreEventID != ownerOutcome.Event.EventID ||
		receipt.ParentCoreMessageID != ownerOutcome.Event.MessageID {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	event, err := loadEventEnvelopeTx(ctx, tx, receipt.EventID)
	if err != nil {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	source, err := boardTerminalReplySourceFromEvent(event, scope)
	if err != nil || source.Binding.MessageID != receipt.BoardMessageID || source.Binding.ReplyTo != receipt.ParentBoardMessageID {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	if err := validateBoardTerminalReplySource(source); err != nil {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	parent, found, err := loadBoardTerminalReplyParentTx(ctx, tx, source)
	if err != nil || !found || parent.Outcome.WorkID != publicationOutcome.WorkID ||
		parent.Event.EventID != ownerOutcome.Event.EventID || parent.Event.MessageID != ownerOutcome.Event.MessageID {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	if err := validateBoardTerminalReplyAgainstParent(source, parent); err != nil {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	if err := validateBoardTerminalReplySourceAtPin(ctx, tx, source); err != nil {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	if err := validateBoardTerminalReplyReceipt(ctx, tx, receipt, source, parent); err != nil {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	inputHash, err := boardTerminalReplyInputHash(source, parent)
	if err != nil || row.receipt.inputSHA256 != inputHash {
		return "", ErrBoardCommunicationReadbackCorrupt
	}
	return receipt.BoardMessageID, nil
}

func boardTerminalReplySourceFromEvent(event modulecore.EventEnvelope, scope BoardPublicationScope) (BoardTerminalReplySource, error) {
	bindingPayload, ok := event.Payload["board_terminal_reply_binding"].(map[string]any)
	if !ok {
		return BoardTerminalReplySource{}, ErrBoardCommunicationReadbackCorrupt
	}
	binding, err := domainconversation.ParseExternalPeerBinding(map[string]any{"external_peer_binding": bindingPayload})
	if err != nil {
		return BoardTerminalReplySource{}, err
	}
	sourcePayload, ok := event.Payload["board_source"].(map[string]any)
	if !ok {
		return BoardTerminalReplySource{}, ErrBoardCommunicationReadbackCorrupt
	}
	stringValue := func(payload map[string]any, name string) (string, bool) {
		value, ok := payload[name].(string)
		return value, ok
	}
	parseTime := func(name string) (time.Time, bool) {
		value, ok := stringValue(sourcePayload, name)
		if !ok {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		return parsed, err == nil
	}
	text, okText := event.Payload["text"].(string)
	content, okContent := event.Payload["content"].(string)
	causationID, okCausation := stringValue(sourcePayload, "causation_id")
	hopCount, okHop := boardTerminalReplyInt(sourcePayload["hop_count"])
	recipients, okRecipients := boardTerminalReplyStrings(sourcePayload["recipients"])
	mentions, okMentions := boardTerminalReplyStrings(sourcePayload["mentions"])
	inboxItemID, okInboxItem := stringValue(sourcePayload, "inbox_item_id")
	inboxCreatedAt, okInboxTime := parseTime("inbox_created_at")
	messageAt, okMessageTime := parseTime("message_at")
	threadCreatedAt, okThreadTime := parseTime("thread_created_at")
	if !okText || !okContent || text == "" || content != text || !okCausation || !okHop ||
		!okRecipients || !okMentions || !okInboxItem || !okInboxTime || !okMessageTime || !okThreadTime {
		return BoardTerminalReplySource{}, ErrBoardCommunicationReadbackCorrupt
	}
	return BoardTerminalReplySource{
		Scope: scope, Binding: binding, InboxItemID: inboxItemID,
		InboxCreatedAt: inboxCreatedAt, MessageAt: messageAt, ThreadCreatedAt: threadCreatedAt,
		Body: text, Recipients: recipients, Mentions: mentions, CausationID: causationID, HopCount: hopCount,
	}, nil
}

func boardTerminalReplyInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func boardTerminalReplyStrings(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			result = append(result, text)
		}
		return result, true
	default:
		return nil, false
	}
}

// NextBoardPublicationDue returns the earliest due or future wakeup for a
// scoped publication; zero time means there is no eligible publication work.
func (s *SQLiteStore) NextBoardPublicationDue(ctx context.Context, scope BoardPublicationScope, now time.Time) (time.Time, error) {
	return s.NextBoardPublicationDueForResponder(ctx, scope, "", now)
}

// NextBoardPublicationDueForResponder finds due work for one CORE responder.
// An empty responder preserves the legacy Mio pin behavior.
func (s *SQLiteStore) NextBoardPublicationDueForResponder(ctx context.Context, scope BoardPublicationScope, responderActorID string, now time.Time) (time.Time, error) {
	if err := s.ensureOpen(); err != nil {
		return time.Time{}, err
	}
	if ctx == nil {
		return time.Time{}, errors.New("BOARD publication due lookup context is required")
	}
	now = normalizedNow(now)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := requireBoardCommunicationPinForResponderTx(ctx, tx, scope, responderActorID); err != nil {
		return time.Time{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT w.work_id
		FROM live_work_outbox w
		JOIN live_board_publication p ON p.work_id=w.work_id
		JOIN live_conversation_session s ON s.session_id=w.session_id
		WHERE w.kind='board_publication' AND s.owner_id=? AND p.board_instance_id=? AND p.core_principal_id=?
		  AND w.state IN ('pending','leased')
		ORDER BY w.work_id`, scope.OwnerID, scope.JournalInstanceID, scope.PrincipalID)
	if err != nil {
		return time.Time{}, err
	}
	workIDs := make([]string, 0)
	for rows.Next() {
		var workID string
		if err := rows.Scan(&workID); err != nil {
			_ = rows.Close()
			return time.Time{}, err
		}
		workIDs = append(workIDs, workID)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return time.Time{}, rowsErr
	}
	if closeErr != nil {
		return time.Time{}, closeErr
	}
	var earliest time.Time
	for _, workID := range workIDs {
		lease, _, work, state, err := loadBoardPublicationSourceTx(ctx, tx, workID, scope)
		if err != nil {
			return time.Time{}, err
		}
		if lease.WorkID != workID || work.Kind != "board_publication" || state != "pending" && state != "leased" {
			return time.Time{}, ErrBoardCommunicationReadbackCorrupt
		}
		if exists, err := boardPublicationOutcomeExistsTx(ctx, tx, workID); err != nil {
			return time.Time{}, err
		} else if exists {
			return time.Time{}, ErrBoardCommunicationReadbackCorrupt
		}
		var candidate time.Time
		switch state {
		case "pending":
			candidate = work.NotBefore
			if candidate.IsZero() || !candidate.After(now) {
				candidate = now
			}
		case "leased":
			candidate = work.LeaseExpiresAt
			if candidate.IsZero() {
				return time.Time{}, ErrBoardCommunicationReadbackCorrupt
			}
			if !candidate.After(now) {
				candidate = now
			}
		}
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return earliest, nil
}
