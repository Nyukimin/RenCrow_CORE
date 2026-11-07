package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrBoardOwnerRequestStatusCorrupt = errors.New("BOARD owner request status is corrupt")

const (
	BoardOwnerRequestStageAccepted           = "accepted"
	BoardOwnerRequestStagePublicationPending = "publication_pending"
	BoardOwnerRequestStageCompleted          = "completed"
)

// BoardOwnerRequestReadback is a redacted owner-scoped status derived from
// canonical CORE acceptance and verified BOARD publication records.
type BoardOwnerRequestReadback struct {
	Scope          BoardPublicationScope `json:"scope"`
	OwnerID        string                `json:"owner_id"`
	SessionID      modulecore.SessionID  `json:"session_id"`
	TurnID         modulecore.TurnID     `json:"turn_id"`
	TraceID        modulecore.TraceID    `json:"trace_id"`
	InputMessageID modulecore.MessageID  `json:"input_message_id"`
	Stage          string                `json:"stage"`
	AcceptedAt     time.Time             `json:"accepted_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

// ReadBoardOwnerRequestStatus returns one exact owner and pin scoped status.
// A false found result is intentionally shared by unknown and out-of-scope
// requests so callers cannot use the status route to enumerate other owners.
func (s *SQLiteStore) ReadBoardOwnerRequestStatus(ctx context.Context, scope BoardPublicationScope, turnID modulecore.TurnID) (BoardOwnerRequestReadback, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardOwnerRequestReadback{}, false, err
	}
	if ctx == nil {
		return BoardOwnerRequestReadback{}, false, errors.New("BOARD owner request status context is required")
	}
	if validateBoardPublicationScope(scope) != nil || turnID.Validate() != nil {
		return BoardOwnerRequestReadback{}, false, ErrBoardCommunicationReadbackScopeMismatch
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BoardOwnerRequestReadback{}, false, err
	}
	defer tx.Rollback()

	pin, found, err := readBoardConnectionPinTx(ctx, tx)
	if err != nil {
		return BoardOwnerRequestReadback{}, false, err
	}
	if !found || pin.OwnerID != scope.OwnerID || pin.JournalInstanceID != scope.JournalInstanceID || pin.PrincipalID != scope.PrincipalID {
		return BoardOwnerRequestReadback{}, false, nil
	}
	var turnOwner, triggerKind string
	err = tx.QueryRowContext(ctx, `SELECT owner_id,trigger_kind FROM live_conversation_turn WHERE turn_id=?`, string(turnID)).
		Scan(&turnOwner, &triggerKind)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (turnOwner != scope.OwnerID || triggerKind != "board_owner_request") {
		return BoardOwnerRequestReadback{}, false, nil
	}
	if err != nil {
		return BoardOwnerRequestReadback{}, false, err
	}

	accepted, message, ownerID, sessionOwnerID, resultReceiptID, err := boardOwnerRequestAcceptedTurnTx(ctx, tx, turnID)
	if err != nil {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: load canonical accepted request: %v", ErrBoardOwnerRequestStatusCorrupt, err)
	}
	if ownerID != scope.OwnerID || sessionOwnerID != scope.OwnerID || accepted.TurnID != turnID || accepted.TriggerKind != "board_owner_request" {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: accepted turn scope differs", ErrBoardOwnerRequestStatusCorrupt)
	}
	source, err := boardOwnerRequestSourceFromAcceptedEvent(accepted.InputEvent, ownerID)
	if err != nil {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: derive source from canonical input", ErrBoardOwnerRequestStatusCorrupt)
	}
	if source.OwnerID != scope.OwnerID || source.BoardInstanceID != scope.JournalInstanceID || source.CorePrincipalID != scope.PrincipalID {
		return BoardOwnerRequestReadback{}, false, nil
	}
	reservation, err := loadBoardOwnerRequestReservationTx(ctx, tx, source)
	if err != nil {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: load immutable reservation: %v", ErrBoardOwnerRequestStatusCorrupt, err)
	}
	if err := validateBoardOwnerRequestAcceptedRecord(ctx, tx, accepted, message, ownerID, sessionOwnerID, reservation); err != nil {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: validate canonical acceptance: %v", ErrBoardOwnerRequestStatusCorrupt, err)
	}
	instruction := boardOwnerRequestPayloadText(accepted.InputEvent.Payload)
	fingerprint, err := boardOwnerRequestFingerprint(source, instruction)
	if err != nil {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: fingerprint canonical input", ErrBoardOwnerRequestStatusCorrupt)
	}
	lookup := conversationruntime.BoardOwnerRequestLookup{
		Source: source, OwnerID: ownerID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		GenerationEpoch: accepted.GenerationEpoch, OperationID: boardOwnerRequestOutputOperationID(accepted.TurnID),
		SourceFingerprint: fingerprint,
	}
	if err := validateBoardOwnerRequestLookup(lookup); err != nil {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: constructed lookup is invalid", ErrBoardOwnerRequestStatusCorrupt)
	}
	if accepted.State != "accepted" && accepted.State != "completed" {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: unsupported turn state", ErrBoardOwnerRequestStatusCorrupt)
	}
	if accepted.State == "accepted" && resultReceiptID != "" ||
		accepted.State == "completed" && resultReceiptID != lookup.OperationID {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: turn result receipt does not match its state", ErrBoardOwnerRequestStatusCorrupt)
	}
	ownerOutcome, ownerOutcomeFound, err := loadBoardOwnerRequestOutcomeTx(ctx, tx, lookup)
	if err != nil {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: verify Mio outcome: %v", ErrBoardOwnerRequestStatusCorrupt, err)
	}
	workID := boardOwnerRequestWorkID(lookup)
	boardOutcome, boardOutcomeFound, err := loadBoardPublicationOutcomeByWorkTx(ctx, tx, workID)
	if err != nil {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: verify BOARD stored receipt: %v", ErrBoardOwnerRequestStatusCorrupt, err)
	}
	var workState, workKind, workSourceEventID, workSessionID string
	workErr := tx.QueryRowContext(ctx, `SELECT state,kind,source_event_id,session_id FROM live_work_outbox WHERE work_id=?`, workID).
		Scan(&workState, &workKind, &workSourceEventID, &workSessionID)
	workFound := workErr == nil
	if workErr != nil && !errors.Is(workErr, sql.ErrNoRows) {
		return BoardOwnerRequestReadback{}, false, workErr
	}
	if accepted.State == "accepted" {
		if ownerOutcomeFound || boardOutcomeFound || workFound {
			return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: publication exists before Mio commit", ErrBoardOwnerRequestStatusCorrupt)
		}
	} else {
		if !ownerOutcomeFound || !workFound || workID != boardOwnerRequestWorkID(ownerOutcome.Lookup) ||
			workKind != "board_publication" || workSourceEventID != string(ownerOutcome.Event.EventID) || workSessionID != string(accepted.SessionID) {
			return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: Mio outcome lacks exact publication work", ErrBoardOwnerRequestStatusCorrupt)
		}
		if boardOutcomeFound && (boardOutcome.Scope != scope || workState != "completed") || !boardOutcomeFound && workState == "completed" {
			return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: BOARD work state and receipt differ", ErrBoardOwnerRequestStatusCorrupt)
		}
		if !boardOutcomeFound && workState != "pending" && workState != "leased" {
			return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: unsupported BOARD publication state", ErrBoardOwnerRequestStatusCorrupt)
		}
	}
	acceptedAt := accepted.InputEvent.OccurredAt
	readback := BoardOwnerRequestReadback{
		Scope: scope, OwnerID: ownerID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		TraceID: accepted.InputEvent.TraceID, InputMessageID: accepted.InputMessageID,
		AcceptedAt: acceptedAt, UpdatedAt: acceptedAt, Stage: BoardOwnerRequestStageAccepted,
	}
	if ownerOutcomeFound {
		readback.Stage = BoardOwnerRequestStagePublicationPending
		readback.UpdatedAt = ownerOutcome.Event.OccurredAt
	}
	if boardOutcomeFound {
		readback.Stage = BoardOwnerRequestStageCompleted
		readback.UpdatedAt = boardOutcome.SendReceipt.StoredAt
	}
	if !validBoardOwnerRequestStatusTime(readback.AcceptedAt) || !validBoardOwnerRequestStatusTime(readback.UpdatedAt) ||
		readback.UpdatedAt.Before(readback.AcceptedAt) {
		return BoardOwnerRequestReadback{}, false, fmt.Errorf("%w: authoritative stage time is invalid", ErrBoardOwnerRequestStatusCorrupt)
	}
	if err := tx.Commit(); err != nil {
		return BoardOwnerRequestReadback{}, false, err
	}
	return readback, true, nil
}

func validBoardOwnerRequestStatusTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}
