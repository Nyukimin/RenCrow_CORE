package eventstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrBoardOwnerRequestStoreUnavailable = errors.New("BOARD owner request store is unavailable")
	ErrBoardOwnerRequestPinMismatch      = errors.New("BOARD owner request does not match the durable connection pin")
	ErrBoardOwnerRequestConflict         = errors.New("BOARD owner request key conflicts with a different request")
	ErrBoardOwnerRequestScopeMismatch    = errors.New("BOARD owner request scope does not match the accepted request")
	ErrBoardOwnerRequestCorrupt          = errors.New("BOARD owner request receipt is corrupt")
	ErrBoardOwnerRequestInvalidOutcome   = errors.New("BOARD owner request outcome is invalid")
)

const (
	boardOwnerRequestReservationOperationKind = "board_owner_request_reservation_v1"
	boardOwnerRequestAcceptanceOperationKind  = "board_owner_request_acceptance_v1"
	boardOwnerRequestCommitOperationKind      = "commit_board_owner_request_v1"
	boardOwnerRequestStableKeyPrefix          = "board-owner-request:"
	boardOwnerRequestReservationIDPrefix      = "board-owner-request-reservation:"
	boardOwnerRequestAcceptanceIDPrefix       = "board-owner-request-acceptance:"
	boardOwnerRequestWorkIDPrefix             = "board-publication:"
	boardPublicationTargetReply               = "reply"
	boardPublicationTargetNewRoot             = "new_root"
)

// BoardOwnerRequestReservationInput is the authenticated source and exact
// instruction received before the canonical owner session is resolved.
type BoardOwnerRequestReservationInput struct {
	Source      conversationruntime.BoardOwnerRequestSource `json:"source"`
	ActorID     string                                      `json:"actor_id"`
	Instruction string                                      `json:"instruction"`
	Now         time.Time                                   `json:"now"`
}

// BoardOwnerRequestReservation stores hashes and a stable admission instant.
// Source.IdempotencyKey is the server-namespaced digest used by later stages.
type BoardOwnerRequestReservation struct {
	Source            conversationruntime.BoardOwnerRequestSource `json:"source"`
	ActorID           string                                      `json:"actor_id"`
	OperationID       string                                      `json:"operation_id"`
	InputSHA256       string                                      `json:"input_sha256"`
	InstructionSHA256 string                                      `json:"instruction_sha256"`
	AdmittedAt        time.Time                                   `json:"admitted_at"`
}

// BoardOwnerRequestAcceptInput binds a reserved request to the owner session
// resolved for this operation. CreateNewSession marks the initial acceptance
// when the resolver has just allocated that session ID.
type BoardOwnerRequestAcceptInput struct {
	Reservation      BoardOwnerRequestReservation `json:"reservation"`
	ActorID          string                       `json:"actor_id"`
	Instruction      string                       `json:"instruction"`
	SessionID        modulecore.SessionID         `json:"session_id"`
	RuntimeVersion   string                       `json:"runtime_version"`
	GenerationEpoch  int64                        `json:"generation_epoch"`
	CreateNewSession bool                         `json:"create_new_session"`
}

var _ conversationruntime.BoardOwnerRequestJournal = (*SQLiteStore)(nil)

func liveBoardPublicationTableDDL() string {
	return `CREATE TABLE IF NOT EXISTS live_board_publication (
		work_id TEXT PRIMARY KEY NOT NULL,
		response_message_id TEXT NOT NULL UNIQUE,
		board_instance_id TEXT NOT NULL CHECK(length(trim(board_instance_id)) > 0),
		core_principal_id TEXT NOT NULL CHECK(length(trim(core_principal_id)) > 0),
		peer_principal_id TEXT NOT NULL CHECK(length(trim(peer_principal_id)) > 0),
		channel_id TEXT NOT NULL CHECK(length(trim(channel_id)) > 0),
		target_kind TEXT NOT NULL DEFAULT 'reply' CHECK(target_kind IN ('reply','new_root')),
		title TEXT NOT NULL DEFAULT '',
		thread_id TEXT NOT NULL DEFAULT '',
		reply_to TEXT NOT NULL DEFAULT '',
		CHECK (
			(target_kind='reply' AND length(trim(thread_id)) > 0 AND length(trim(reply_to)) > 0 AND title='') OR
			(target_kind='new_root' AND thread_id='' AND reply_to='' AND length(trim(title)) > 0)
		),
		FOREIGN KEY (work_id) REFERENCES live_work_outbox(work_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
		FOREIGN KEY (response_message_id) REFERENCES live_message_index(message_id) ON UPDATE RESTRICT ON DELETE RESTRICT
	)`
}

func migrateBoardOwnerRequestPublicationSchema(ctx context.Context, tx *sql.Tx) error {
	columns := make(map[string]bool)
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(live_board_publication)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&ordinal, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if columns["target_kind"] && columns["title"] {
		return nil
	}
	if !columns["work_id"] || !columns["response_message_id"] || !columns["board_instance_id"] || !columns["core_principal_id"] ||
		!columns["peer_principal_id"] || !columns["channel_id"] || !columns["thread_id"] || !columns["reply_to"] {
		return errors.New("legacy BOARD publication table is incomplete")
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return fmt.Errorf("defer BOARD publication target foreign keys: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE live_board_publication_b3_preserved AS
		SELECT work_id,response_message_id,board_instance_id,core_principal_id,peer_principal_id,channel_id,thread_id,reply_to
		FROM live_board_publication`); err != nil {
		return fmt.Errorf("preserve legacy BOARD publication targets: %w", err)
	}
	for _, trigger := range []string{
		"live_board_publication_insert_binding",
		"live_board_publication_append_only_update",
		"live_board_publication_append_only_delete",
	} {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+trigger); err != nil {
			return fmt.Errorf("drop legacy BOARD publication trigger %s: %w", trigger, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE live_board_publication`); err != nil {
		return fmt.Errorf("replace legacy BOARD publication target table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, liveBoardPublicationTableDDL()); err != nil {
		return fmt.Errorf("create BOARD publication target table with new-root support: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_board_publication
			(work_id,response_message_id,board_instance_id,core_principal_id,peer_principal_id,channel_id,target_kind,title,thread_id,reply_to)
		SELECT work_id,response_message_id,board_instance_id,core_principal_id,peer_principal_id,channel_id,'reply','',thread_id,reply_to
		FROM live_board_publication_b3_preserved`); err != nil {
		return fmt.Errorf("restore legacy BOARD reply targets: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE live_board_publication_b3_preserved`); err != nil {
		return fmt.Errorf("remove preserved BOARD publication copy: %w", err)
	}
	for _, statement := range liveJournalSchemaStatements() {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TRIGGER IF NOT EXISTS live_board_publication_") {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("restore BOARD publication trigger: %w", err)
			}
		}
	}
	return nil
}

// ReserveBoardOwnerRequest durably binds the exact authenticated source and
// instruction digest before any Session is selected or accepted.
func (s *SQLiteStore) ReserveBoardOwnerRequest(ctx context.Context, input BoardOwnerRequestReservationInput) (BoardOwnerRequestReservation, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	if ctx == nil {
		return BoardOwnerRequestReservation{}, errors.New("BOARD owner request reservation context is required")
	}
	if err := validateBoardOwnerRequestReservationInput(input); err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	if s.shouldQueueWrite(ctx) {
		queued := cloneQueuedValue(input)
		sizeBytes, err := queuedWriteSize(queued)
		if err != nil {
			return BoardOwnerRequestReservation{}, fmt.Errorf("size queued BOARD owner request reservation: %w", err)
		}
		var reserved BoardOwnerRequestReservation
		err = s.enqueueOrderedWrite(ctx, 0, sizeBytes, func(commandCtx context.Context) error {
			var reserveErr error
			reserved, reserveErr = s.reserveBoardOwnerRequestInline(commandCtx, queued)
			return reserveErr
		})
		return reserved, err
	}
	return s.reserveBoardOwnerRequestInline(ctx, input)
}

func (s *SQLiteStore) reserveBoardOwnerRequestInline(ctx context.Context, input BoardOwnerRequestReservationInput) (BoardOwnerRequestReservation, error) {
	inputHash, err := boardOwnerRequestReservationInputHash(input)
	if err != nil {
		return BoardOwnerRequestReservation{}, fmt.Errorf("hash BOARD owner request reservation: %w", err)
	}
	operationID, stableKey, err := boardOwnerRequestReservationIdentity(input.Source)
	if err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	transaction, unlock, err := s.beginSequenceTx(ctx)
	if err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	defer unlock()
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if _, err := requireBoardOwnerRequestPinTx(ctx, transaction, input.Source); err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	prior, found, err := readOperationReceipt(ctx, transaction, operationID)
	if err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	if found {
		if prior.operationKind != boardOwnerRequestReservationOperationKind {
			return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestConflict
		}
		if prior.inputSHA256 != inputHash {
			return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestConflict
		}
		stored, err := decodeBoardOwnerRequestReservationReceipt(prior)
		if err != nil {
			return BoardOwnerRequestReservation{}, err
		}
		expected := BoardOwnerRequestReservation{
			Source: input.Source, ActorID: input.ActorID, OperationID: operationID,
			InputSHA256: inputHash, InstructionSHA256: boardOwnerRequestInstructionHash(input.Instruction),
		}
		expected.Source.IdempotencyKey = stableKey
		if !sameBoardOwnerRequestReservation(stored, expected) {
			return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestCorrupt
		}
		if err := transaction.Commit(); err != nil {
			return BoardOwnerRequestReservation{}, err
		}
		committed = true
		return stored, nil
	}
	reserved := BoardOwnerRequestReservation{
		Source: input.Source, ActorID: input.ActorID, OperationID: operationID,
		InputSHA256: inputHash, InstructionSHA256: boardOwnerRequestInstructionHash(input.Instruction),
		AdmittedAt: input.Now.UTC(),
	}
	reserved.Source.IdempotencyKey = stableKey
	if err := insertOperationReceipt(ctx, transaction, operationID, boardOwnerRequestReservationOperationKind, inputHash, reserved); err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	if err := transaction.Commit(); err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	committed = true
	return reserved, nil
}

// AcceptBoardOwnerRequest binds a previously reserved request to the already
// resolved canonical owner Session, then reuses AcceptTurn's durable transaction.
func (s *SQLiteStore) AcceptBoardOwnerRequest(ctx context.Context, input BoardOwnerRequestAcceptInput) (AcceptedTurn, error) {
	if err := s.ensureOpen(); err != nil {
		return AcceptedTurn{}, err
	}
	if ctx == nil {
		return AcceptedTurn{}, errors.New("BOARD owner request acceptance context is required")
	}
	if err := validateBoardOwnerRequestAcceptRequest(input); err != nil {
		return AcceptedTurn{}, err
	}
	reservation := input.Reservation
	payload := map[string]any{
		"from": "board_owner", "source_actor_id": input.ActorID, "to": "mio",
		"text": input.Instruction, "content": input.Instruction,
		"body": map[string]any{"text": input.Instruction},
		"board_owner_request_binding": map[string]any{
			"board_instance_id": reservation.Source.BoardInstanceID, "core_principal_id": reservation.Source.CorePrincipalID,
			"channel_id": reservation.Source.ChannelID, "peer_principal_id": reservation.Source.PeerPrincipalID,
			"title": reservation.Source.Title, "idempotency_key": reservation.Source.IdempotencyKey,
		},
	}
	event := modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "conversation.board_owner_request", reservation.AdmittedAt, payload)
	event.SessionID, event.TurnID, event.MessageID = input.SessionID, modulecore.NewTurnID(), modulecore.NewMessageID()
	accept := AcceptInput{
		OperationID: boardOwnerRequestAcceptanceOperationID(reservation.Source.IdempotencyKey),
		OwnerID:     reservation.Source.OwnerID, RuntimeVersion: input.RuntimeVersion,
		ClientSubmissionID: reservation.Source.IdempotencyKey, ActorID: input.ActorID,
		TriggerKind: "board_owner_request", GenerationEpoch: input.GenerationEpoch,
		CreateNewSession: input.CreateNewSession, InputEvent: event,
		InputMessage: MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: input.SessionID, TurnID: event.TurnID,
			FromActor: "board_owner", ToKind: "agent", ToActor: "mio", Kind: "board_owner_request", Visibility: "public",
		},
		BoardOwnerRequestReservation: &reservation,
	}
	return s.AcceptTurn(ctx, accept)
}

func validateBoardOwnerRequestReservationInput(input BoardOwnerRequestReservationInput) error {
	if !validBoardOwnerRequestSource(input.Source, false) || !validBoardOwnerRequestValue(input.ActorID, 512) ||
		!validBoardOwnerRequestInstruction(input.Instruction) || input.Now.IsZero() {
		return errors.New("BOARD owner request reservation input is invalid")
	}
	return nil
}

func boardOwnerRequestReservationInputHash(input BoardOwnerRequestReservationInput) (string, error) {
	return hashJSON(struct {
		Source      conversationruntime.BoardOwnerRequestSource `json:"source"`
		ActorID     string                                      `json:"actor_id"`
		Instruction string                                      `json:"instruction"`
	}{Source: input.Source, ActorID: input.ActorID, Instruction: input.Instruction})
}

func boardOwnerRequestReservationIdentity(source conversationruntime.BoardOwnerRequestSource) (string, string, error) {
	material := struct {
		OwnerID              string `json:"owner_id"`
		BoardInstanceID      string `json:"board_instance_id"`
		CorePrincipalID      string `json:"core_principal_id"`
		ClientIdempotencyKey string `json:"client_idempotency_key"`
	}{
		OwnerID: source.OwnerID, BoardInstanceID: source.BoardInstanceID,
		CorePrincipalID: source.CorePrincipalID, ClientIdempotencyKey: source.IdempotencyKey,
	}
	digest, err := hashJSON(material)
	if err != nil {
		return "", "", err
	}
	return boardOwnerRequestReservationIDPrefix + digest, boardOwnerRequestStableKeyPrefix + digest, nil
}

func boardOwnerRequestReservationIdentityFromStableKey(key string) (string, error) {
	if !strings.HasPrefix(key, boardOwnerRequestStableKeyPrefix) {
		return "", ErrBoardOwnerRequestCorrupt
	}
	digest := strings.TrimPrefix(key, boardOwnerRequestStableKeyPrefix)
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
		return "", ErrBoardOwnerRequestCorrupt
	}
	return boardOwnerRequestReservationIDPrefix + digest, nil
}

func boardOwnerRequestAcceptanceOperationID(stableKey string) string {
	return boardOwnerRequestAcceptanceIDPrefix + strings.TrimPrefix(stableKey, boardOwnerRequestStableKeyPrefix)
}

func boardOwnerRequestOutputOperationID(turnID modulecore.TurnID) string {
	return "conversation.output:" + string(turnID)
}

func boardOwnerRequestInstructionHash(instruction string) string {
	digest := sha256.Sum256([]byte(instruction))
	return hex.EncodeToString(digest[:])
}

func validBoardOwnerRequestInstruction(instruction string) bool {
	return instruction != "" && strings.TrimSpace(instruction) != "" && len([]byte(instruction)) <= domainconversation.MaxUserMessageBytes &&
		utf8.ValidString(instruction)
}

func boardOwnerRequestPayloadText(payload map[string]any) string {
	value, _ := payload["text"].(string)
	return value
}

func validBoardOwnerRequestDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func validBoardOwnerRequestValue(value string, limit int) bool {
	return value != "" && len([]byte(value)) <= limit && strings.TrimSpace(value) == value &&
		utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validBoardOwnerRequestSource(source conversationruntime.BoardOwnerRequestSource, requireNamespacedKey bool) bool {
	if !validBoardOwnerRequestValue(source.OwnerID, 512) || !validBoardConnectionJournal(source.BoardInstanceID) ||
		!validBoardConnectionPrincipal(source.CorePrincipalID) || !validBoardOwnerRequestValue(source.ChannelID, 512) ||
		!validBoardOwnerRequestValue(source.PeerPrincipalID, 512) || !validBoardOwnerRequestValue(source.Title, 2048) {
		return false
	}
	if requireNamespacedKey {
		_, err := boardOwnerRequestReservationIdentityFromStableKey(source.IdempotencyKey)
		return err == nil
	}
	return validBoardOwnerRequestValue(source.IdempotencyKey, 512)
}

func requireBoardOwnerRequestPinTx(ctx context.Context, tx *sql.Tx, source conversationruntime.BoardOwnerRequestSource) (BoardConnectionPin, error) {
	pin, found, err := readBoardConnectionPinTx(ctx, tx)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	if !found || pin.OwnerID != source.OwnerID || pin.JournalInstanceID != source.BoardInstanceID || pin.PrincipalID != source.CorePrincipalID {
		return BoardConnectionPin{}, ErrBoardOwnerRequestPinMismatch
	}
	return pin, nil
}

func decodeBoardOwnerRequestReservationReceipt(receipt operationReceipt) (BoardOwnerRequestReservation, error) {
	if receipt.operationKind != boardOwnerRequestReservationOperationKind {
		return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestCorrupt
	}
	decoder := json.NewDecoder(strings.NewReader(receipt.resultJSON))
	decoder.DisallowUnknownFields()
	var reservation BoardOwnerRequestReservation
	if err := decoder.Decode(&reservation); err != nil {
		return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestCorrupt
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestCorrupt
	}
	encoded, err := json.Marshal(reservation)
	operationID, operationErr := boardOwnerRequestReservationIdentityFromStableKey(reservation.Source.IdempotencyKey)
	if err != nil || !bytes.Equal(encoded, []byte(receipt.resultJSON)) ||
		operationErr != nil || reservation.OperationID != operationID || reservation.ActorID == "" ||
		!validBoardOwnerRequestValue(reservation.ActorID, 512) || !validBoardOwnerRequestSource(reservation.Source, true) ||
		!validBoardOwnerRequestDigest(reservation.InputSHA256) || !validBoardOwnerRequestDigest(reservation.InstructionSHA256) ||
		reservation.AdmittedAt.IsZero() || reservation.AdmittedAt.Location() != time.UTC || receipt.inputSHA256 != reservation.InputSHA256 {
		return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestCorrupt
	}
	return reservation, nil
}

func sameBoardOwnerRequestReservation(left, right BoardOwnerRequestReservation) bool {
	return left.Source == right.Source && left.ActorID == right.ActorID && left.OperationID == right.OperationID &&
		left.InputSHA256 == right.InputSHA256 && left.InstructionSHA256 == right.InstructionSHA256 &&
		left.AdmittedAt.Location() == time.UTC && !left.AdmittedAt.IsZero()
}

func validateBoardOwnerRequestAcceptRequest(input BoardOwnerRequestAcceptInput) error {
	reservation := input.Reservation
	operationID, err := boardOwnerRequestReservationIdentityFromStableKey(reservation.Source.IdempotencyKey)
	if err != nil || operationID != reservation.OperationID || !validBoardOwnerRequestSource(reservation.Source, true) ||
		!validBoardOwnerRequestValue(reservation.ActorID, 512) || input.ActorID != reservation.ActorID ||
		reservation.AdmittedAt.IsZero() || reservation.AdmittedAt.Location() != time.UTC ||
		!validBoardOwnerRequestDigest(reservation.InputSHA256) || !validBoardOwnerRequestDigest(reservation.InstructionSHA256) {
		return ErrBoardOwnerRequestScopeMismatch
	}
	if !validBoardOwnerRequestInstruction(input.Instruction) || boardOwnerRequestInstructionHash(input.Instruction) != reservation.InstructionSHA256 {
		return ErrBoardOwnerRequestConflict
	}
	if input.SessionID.Validate() != nil || input.RuntimeVersion != conversationruntime.ConversationRuntimeV2 || input.GenerationEpoch <= 0 {
		return ErrConversationSessionConflict
	}
	return nil
}

func validateBoardOwnerRequestAcceptInput(input AcceptInput) error {
	proof := input.BoardOwnerRequestReservation
	if proof == nil || input.ExternalPeerBinding != nil || input.TriggerKind != "board_owner_request" ||
		input.OwnerID != proof.Source.OwnerID || input.ActorID != proof.ActorID || input.ClientSubmissionID != proof.Source.IdempotencyKey ||
		input.OperationID != boardOwnerRequestAcceptanceOperationID(proof.Source.IdempotencyKey) ||
		!validBoardOwnerRequestSource(proof.Source, true) || input.GenerationEpoch <= 0 || input.RuntimeVersion != conversationruntime.ConversationRuntimeV2 {
		return ErrBoardOwnerRequestScopeMismatch
	}
	if !input.InputEvent.OccurredAt.Equal(proof.AdmittedAt) || input.InputEvent.EventType != "conversation.board_owner_request" ||
		input.InputEvent.ComponentID != "orchestrator" || input.InputEvent.ActorKind != "" || input.InputEvent.ActorID != "" ||
		input.InputEvent.CausationEventID != "" || len(input.InputEvent.DependencyEventIDs) != 0 || input.InputEvent.ThreadID != "" ||
		input.InputEvent.RequestID != "" || input.InputEvent.ResponseID != "" || input.InputEvent.UtteranceID != "" ||
		input.InputEvent.TaskID != "" || input.InputEvent.RunID != "" || input.InputEvent.ActionID != "" || input.InputEvent.AttemptID != "" ||
		input.InputEvent.WorkstreamID != "" || input.InputEvent.GoalID != "" || input.InputEvent.ArtifactID != "" ||
		input.InputEvent.EvidenceID != "" || input.InputEvent.MemoryID != "" || input.InputEvent.RelationID != "" ||
		input.InputEvent.ScheduleID != "" || input.InputEvent.QueueItemID != "" || input.InputEvent.CheckpointID != "" || input.InputEvent.ReceiptID != "" {
		return ErrBoardOwnerRequestScopeMismatch
	}
	if err := validateBoardOwnerRequestEventAndIndex(input.InputEvent, input.InputMessage, proof.Source, proof.ActorID,
		input.InputEvent.SessionID, input.InputEvent.TurnID, boardOwnerRequestPayloadText(input.InputEvent.Payload), true); err != nil {
		return err
	}
	return nil
}

func validateBoardOwnerRequestEventAndIndex(event modulecore.EventEnvelope, message MessageIndex, source conversationruntime.BoardOwnerRequestSource, actorID string, sessionID modulecore.SessionID, turnID modulecore.TurnID, instruction string, unsequenced bool) error {
	if err := modulecore.ValidateEventEnvelope(event); err != nil || event.SessionID != sessionID || event.TurnID != turnID || event.MessageID != message.MessageID ||
		event.EventSeq < 0 || unsequenced && event.EventSeq != 0 || !unsequenced && event.EventSeq <= 0 ||
		message.EventID != event.EventID || message.SessionID != sessionID || message.TurnID != turnID ||
		message.FromActor != "board_owner" || message.ToKind != "agent" || message.ToActor != "mio" ||
		message.Kind != "board_owner_request" || message.Visibility != "public" || message.ReplyToMessageID != "" {
		return ErrBoardOwnerRequestScopeMismatch
	}
	payload := event.Payload
	if len(payload) != 7 || payload["from"] != "board_owner" || payload["source_actor_id"] != actorID || payload["to"] != "mio" {
		return ErrBoardOwnerRequestScopeMismatch
	}
	text, textOK := payload["text"].(string)
	content, contentOK := payload["content"].(string)
	body, bodyOK := payload["body"].(map[string]any)
	if !textOK || !contentOK || !bodyOK || !boardOwnerRequestExactStringKeys(body, "text") ||
		!validBoardOwnerRequestInstruction(text) || text != content || text != instruction || body["text"] != instruction {
		return ErrBoardOwnerRequestConflict
	}
	binding, ok := payload["board_owner_request_binding"].(map[string]any)
	if !ok || !boardOwnerRequestExactStringKeys(binding, "board_instance_id", "core_principal_id", "channel_id", "peer_principal_id", "title", "idempotency_key") ||
		binding["board_instance_id"] != source.BoardInstanceID || binding["core_principal_id"] != source.CorePrincipalID ||
		binding["channel_id"] != source.ChannelID || binding["peer_principal_id"] != source.PeerPrincipalID ||
		binding["title"] != source.Title || binding["idempotency_key"] != source.IdempotencyKey {
		return ErrBoardOwnerRequestScopeMismatch
	}
	for _, coreID := range []string{string(event.SessionID), string(event.TurnID), string(event.MessageID), string(event.EventID), string(event.TraceID)} {
		for _, boardID := range []string{source.BoardInstanceID, source.CorePrincipalID, source.ChannelID, source.PeerPrincipalID} {
			if coreID != "" && coreID == boardID {
				return ErrBoardOwnerRequestScopeMismatch
			}
		}
	}
	return nil
}

func boardOwnerRequestExactStringKeys(values map[string]any, keys ...string) bool {
	if len(values) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, exists := values[key]; !exists {
			return false
		}
	}
	return true
}

func validateBoardOwnerRequestReservationInTx(ctx context.Context, tx *sql.Tx, reservation BoardOwnerRequestReservation, actorID, instruction string) error {
	operationID, err := boardOwnerRequestReservationIdentityFromStableKey(reservation.Source.IdempotencyKey)
	if err != nil || operationID != reservation.OperationID || !validBoardOwnerRequestSource(reservation.Source, true) ||
		actorID != reservation.ActorID || !validBoardOwnerRequestValue(actorID, 512) ||
		!validBoardOwnerRequestDigest(reservation.InputSHA256) || reservation.AdmittedAt.IsZero() || reservation.AdmittedAt.Location() != time.UTC {
		return ErrBoardOwnerRequestScopeMismatch
	}
	if !validBoardOwnerRequestInstruction(instruction) || boardOwnerRequestInstructionHash(instruction) != reservation.InstructionSHA256 {
		return ErrBoardOwnerRequestConflict
	}
	if _, err := requireBoardOwnerRequestPinTx(ctx, tx, reservation.Source); err != nil {
		return err
	}
	receipt, found, err := readOperationReceipt(ctx, tx, reservation.OperationID)
	if err != nil {
		return err
	}
	if !found {
		return ErrBoardOwnerRequestScopeMismatch
	}
	stored, err := decodeBoardOwnerRequestReservationReceipt(receipt)
	if err != nil {
		return err
	}
	if !sameBoardOwnerRequestReservation(stored, reservation) || stored.ActorID != actorID ||
		stored.InstructionSHA256 != boardOwnerRequestInstructionHash(instruction) {
		return ErrBoardOwnerRequestScopeMismatch
	}
	return nil
}

func validateBoardOwnerRequestAcceptanceReceiptTx(ctx context.Context, tx *sql.Tx, input AcceptInput, receipt AcceptedTurn) error {
	if err := validateBoardOwnerRequestAcceptInput(input); err != nil {
		return err
	}
	reservation := *input.BoardOwnerRequestReservation
	if receipt.OperationID != input.OperationID || receipt.ClientSubmissionID != input.ClientSubmissionID ||
		receipt.SessionID != input.InputEvent.SessionID || receipt.ActorID != input.ActorID ||
		receipt.TriggerKind != "board_owner_request" || receipt.GenerationEpoch != input.GenerationEpoch ||
		receipt.State != "accepted" || receipt.InputEvent.EventSeq <= 0 || receipt.InputMessageID != receipt.InputEvent.MessageID {
		return ErrBoardOwnerRequestCorrupt
	}
	accepted, message, ownerID, sessionOwnerID, _, err := boardOwnerRequestAcceptedTurnTx(ctx, tx, receipt.TurnID)
	if err != nil {
		return err
	}
	if err := validateBoardOwnerRequestAcceptedRecord(ctx, tx, accepted, message, ownerID, sessionOwnerID, reservation); err != nil {
		return err
	}
	if accepted.OperationID != receipt.OperationID || accepted.ClientSubmissionID != receipt.ClientSubmissionID ||
		accepted.SessionID != receipt.SessionID || accepted.TurnID != receipt.TurnID || accepted.ActorID != receipt.ActorID ||
		accepted.InputMessageID != receipt.InputMessageID || accepted.AdmissionSeq != receipt.AdmissionSeq ||
		accepted.GenerationEpoch != receipt.GenerationEpoch ||
		!reflect.DeepEqual(accepted.InputEvent, receipt.InputEvent) {
		return ErrBoardOwnerRequestCorrupt
	}
	return nil
}

func validateBoardOwnerRequestAcceptTx(ctx context.Context, tx *sql.Tx, input AcceptInput) error {
	if input.BoardOwnerRequestReservation == nil {
		return ErrBoardOwnerRequestScopeMismatch
	}
	reservation := *input.BoardOwnerRequestReservation
	if err := validateBoardOwnerRequestReservationInTx(ctx, tx, reservation, input.ActorID, boardOwnerRequestPayloadText(input.InputEvent.Payload)); err != nil {
		return err
	}
	var ownerID, runtimeVersion string
	var cutoverSeq, admissionSeq, generationEpoch int64
	err := tx.QueryRowContext(ctx, `SELECT owner_id,runtime_version,cutover_seq,admission_seq,epoch FROM live_conversation_session WHERE session_id=?`, string(input.InputEvent.SessionID)).
		Scan(&ownerID, &runtimeVersion, &cutoverSeq, &admissionSeq, &generationEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		if !input.CreateNewSession {
			return ErrConversationSessionConflict
		}
		return nil
	}
	if err != nil {
		return err
	}
	if ownerID != reservation.Source.OwnerID || input.OwnerID != ownerID || runtimeVersion != input.RuntimeVersion ||
		generationEpoch != input.GenerationEpoch || admissionSeq <= cutoverSeq {
		return ErrConversationSessionConflict
	}
	return nil
}

func boardOwnerRequestAcceptedTurnTx(ctx context.Context, tx *sql.Tx, turnID modulecore.TurnID) (AcceptedTurn, MessageIndex, string, string, string, error) {
	var accepted AcceptedTurn
	var ownerID, sessionOwnerID, actorID, triggerKind, eventID, messageID, sessionID, turnIDText string
	var clientSubmissionID, operationID, state, sessionRuntimeVersion string
	var admissionSeq, generationEpoch, sessionCutoverSeq, sessionAdmissionSeq, sessionEpoch int64
	var resultReceiptID, replyTo sql.NullString
	var message MessageIndex
	err := tx.QueryRowContext(ctx, `
		SELECT t.accept_receipt_id,t.client_submission_id,t.turn_id,t.session_id,t.owner_id,s.owner_id,s.runtime_version,s.cutover_seq,s.admission_seq,s.epoch,
		       t.actor_id,t.trigger_kind,
		       t.input_message_id,t.admission_seq,t.generation_epoch,t.state,t.result_receipt_id,
		       m.event_id,m.from_actor,m.to_kind,m.to_actor,m.kind,m.reply_to_message_id,m.visibility
		FROM live_conversation_turn t
	JOIN live_conversation_session s ON s.session_id=t.session_id
	JOIN live_message_index m ON m.message_id=t.input_message_id AND m.session_id=t.session_id AND m.turn_id=t.turn_id
	WHERE t.turn_id=?`, string(turnID)).Scan(
		&operationID, &clientSubmissionID, &turnIDText, &sessionID, &ownerID, &sessionOwnerID,
		&sessionRuntimeVersion, &sessionCutoverSeq, &sessionAdmissionSeq, &sessionEpoch, &actorID, &triggerKind,
		&messageID, &admissionSeq, &generationEpoch, &state, &resultReceiptID,
		&eventID, &message.FromActor, &message.ToKind, &message.ToActor, &message.Kind, &replyTo, &message.Visibility,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AcceptedTurn{}, MessageIndex{}, "", "", "", ErrBoardOwnerRequestScopeMismatch
	}
	if err != nil {
		return AcceptedTurn{}, MessageIndex{}, "", "", "", err
	}
	message.MessageID, message.EventID = modulecore.MessageID(messageID), modulecore.EventID(eventID)
	message.SessionID, message.TurnID = modulecore.SessionID(sessionID), modulecore.TurnID(turnIDText)
	if replyTo.Valid {
		message.ReplyToMessageID = modulecore.MessageID(replyTo.String)
	}
	event, err := loadEventEnvelopeTx(ctx, tx, message.EventID)
	if err != nil {
		return AcceptedTurn{}, MessageIndex{}, "", "", "", fmt.Errorf("load accepted BOARD owner request event: %w", err)
	}
	accepted = AcceptedTurn{
		OperationID: operationID, ClientSubmissionID: clientSubmissionID,
		SessionID: message.SessionID, TurnID: message.TurnID, ActorID: actorID,
		TriggerKind: triggerKind, InputMessageID: message.MessageID, AdmissionSeq: modulecore.EventSeq(admissionSeq),
		GenerationEpoch: generationEpoch, State: state, InputEvent: event,
	}
	if sessionRuntimeVersion != conversationruntime.ConversationRuntimeV2 || sessionEpoch != generationEpoch ||
		admissionSeq <= sessionCutoverSeq || admissionSeq > sessionAdmissionSeq {
		return AcceptedTurn{}, MessageIndex{}, "", "", "", ErrBoardOwnerRequestScopeMismatch
	}
	return accepted, message, ownerID, sessionOwnerID, resultReceiptID.String, nil
}

func validateBoardOwnerRequestAcceptedRecord(ctx context.Context, tx *sql.Tx, accepted AcceptedTurn, message MessageIndex, ownerID, sessionOwnerID string, reservation BoardOwnerRequestReservation) error {
	if accepted.OperationID != boardOwnerRequestAcceptanceOperationID(reservation.Source.IdempotencyKey) ||
		accepted.ClientSubmissionID != reservation.Source.IdempotencyKey || ownerID != reservation.Source.OwnerID ||
		accepted.ActorID != reservation.ActorID || accepted.TriggerKind != "board_owner_request" ||
		accepted.SessionID == "" || accepted.TurnID == "" || accepted.GenerationEpoch <= 0 ||
		accepted.State != "accepted" && accepted.State != "completed" || sessionOwnerID != ownerID ||
		accepted.InputEvent.EventSeq != accepted.AdmissionSeq || accepted.InputEvent.EventSeq <= 0 ||
		accepted.InputEvent.SessionID != accepted.SessionID || accepted.InputEvent.TurnID != accepted.TurnID ||
		accepted.InputEvent.MessageID != accepted.InputMessageID || accepted.AdmissionSeq <= 0 {
		return ErrBoardOwnerRequestScopeMismatch
	}
	if err := validateBoardOwnerRequestEventAndIndex(accepted.InputEvent, message, reservation.Source, reservation.ActorID,
		accepted.SessionID, accepted.TurnID, boardOwnerRequestPayloadText(accepted.InputEvent.Payload), false); err != nil {
		return err
	}
	if err := validateBoardOwnerRequestReservationInTx(ctx, tx, reservation, accepted.ActorID, boardOwnerRequestPayloadText(accepted.InputEvent.Payload)); err != nil {
		return err
	}
	return nil
}

type boardOwnerRequestEventReference struct {
	EventID  modulecore.EventID  `json:"event_id"`
	EventSeq modulecore.EventSeq `json:"event_seq"`
}

type boardOwnerRequestMessageReference struct {
	MessageID modulecore.MessageID `json:"message_id"`
	EventID   modulecore.EventID   `json:"event_id"`
}

type boardOwnerRequestCommitReceipt struct {
	Lookup            conversationruntime.BoardOwnerRequestLookup `json:"lookup"`
	Event             boardOwnerRequestEventReference             `json:"event"`
	Message           boardOwnerRequestMessageReference           `json:"message"`
	InvocationReceipt agent.InvocationReceipt                     `json:"invocation_receipt"`
	WorkID            string                                      `json:"work_id"`
}

func validateBoardOwnerRequestLookup(lookup conversationruntime.BoardOwnerRequestLookup) error {
	if !validBoardOwnerRequestSource(lookup.Source, true) || lookup.OwnerID != lookup.Source.OwnerID ||
		lookup.SessionID.Validate() != nil || lookup.TurnID.Validate() != nil || lookup.GenerationEpoch <= 0 ||
		lookup.OperationID != boardOwnerRequestOutputOperationID(lookup.TurnID) || !validBoardOwnerRequestDigest(lookup.SourceFingerprint) {
		return ErrBoardOwnerRequestScopeMismatch
	}
	return nil
}

func boardOwnerRequestFingerprint(source conversationruntime.BoardOwnerRequestSource, instruction string) (string, error) {
	return hashJSON(struct {
		Source conversationruntime.BoardOwnerRequestSource `json:"source"`
		Body   string                                      `json:"body"`
	}{Source: source, Body: instruction})
}

func boardOwnerRequestWorkID(lookup conversationruntime.BoardOwnerRequestLookup) string {
	return boardOwnerRequestWorkIDPrefix + strings.TrimPrefix(lookup.OperationID, "conversation.output:")
}

func boardOwnerRequestPublicationDedupeKey(lookup conversationruntime.BoardOwnerRequestLookup) string {
	return "board-owner-request-publication:" + lookup.OperationID
}

func validBoardOwnerRequestInvocationReceipt(receipt agent.InvocationReceipt) bool {
	return receipt.Status == "completed" && validBoardOwnerRequestValue(receipt.RequestID, 512) &&
		validBoardOwnerRequestValue(receipt.ResponseID, 512)
}

// LoadBoardOwnerRequestOutcome returns only the exact accepted source and
// generation scope requested by the typed app journal.
func (s *SQLiteStore) LoadBoardOwnerRequestOutcome(ctx context.Context, lookup conversationruntime.BoardOwnerRequestLookup) (conversationruntime.BoardOwnerRequestOutcome, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	if ctx == nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, errors.New("BOARD owner request outcome context is required")
	}
	if err := validateBoardOwnerRequestLookup(lookup); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	defer tx.Rollback()
	return loadBoardOwnerRequestOutcomeTx(ctx, tx, lookup)
}

func loadBoardOwnerRequestReservationTx(ctx context.Context, tx *sql.Tx, source conversationruntime.BoardOwnerRequestSource) (BoardOwnerRequestReservation, error) {
	operationID, err := boardOwnerRequestReservationIdentityFromStableKey(source.IdempotencyKey)
	if err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	receipt, found, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	if !found {
		return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestScopeMismatch
	}
	reservation, err := decodeBoardOwnerRequestReservationReceipt(receipt)
	if err != nil {
		return BoardOwnerRequestReservation{}, err
	}
	if reservation.Source != source || reservation.OperationID != operationID {
		return BoardOwnerRequestReservation{}, ErrBoardOwnerRequestScopeMismatch
	}
	return reservation, nil
}

func loadBoardOwnerRequestOutcomeTx(ctx context.Context, tx *sql.Tx, lookup conversationruntime.BoardOwnerRequestLookup) (conversationruntime.BoardOwnerRequestOutcome, bool, error) {
	if err := validateBoardOwnerRequestLookup(lookup); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	if _, err := requireBoardOwnerRequestPinTx(ctx, tx, lookup.Source); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	reservation, err := loadBoardOwnerRequestReservationTx(ctx, tx, lookup.Source)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	accepted, message, ownerID, sessionOwnerID, resultReceiptID, err := boardOwnerRequestAcceptedTurnTx(ctx, tx, lookup.TurnID)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	if err := validateBoardOwnerRequestAcceptedRecord(ctx, tx, accepted, message, ownerID, sessionOwnerID, reservation); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	if accepted.SessionID != lookup.SessionID || accepted.TurnID != lookup.TurnID || accepted.GenerationEpoch != lookup.GenerationEpoch ||
		ownerID != lookup.OwnerID || accepted.OperationID != boardOwnerRequestAcceptanceOperationID(lookup.Source.IdempotencyKey) {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestScopeMismatch
	}
	fingerprint, err := boardOwnerRequestFingerprint(lookup.Source, boardOwnerRequestPayloadText(accepted.InputEvent.Payload))
	if err != nil || fingerprint != lookup.SourceFingerprint {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestScopeMismatch
	}
	if accepted.State == "accepted" && resultReceiptID == "" {
		if _, occupied, err := readOperationReceipt(ctx, tx, lookup.OperationID); err != nil {
			return conversationruntime.BoardOwnerRequestOutcome{}, false, err
		} else if occupied {
			return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
		}
		return conversationruntime.BoardOwnerRequestOutcome{}, false, nil
	}
	if accepted.State != "completed" || resultReceiptID != lookup.OperationID {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestScopeMismatch
	}
	return loadCommittedBoardOwnerRequestOutcomeTx(ctx, tx, lookup, accepted, reservation)
}

func loadCommittedBoardOwnerRequestOutcomeTx(ctx context.Context, tx *sql.Tx, lookup conversationruntime.BoardOwnerRequestLookup, accepted AcceptedTurn, reservation BoardOwnerRequestReservation) (conversationruntime.BoardOwnerRequestOutcome, bool, error) {
	receiptRow, found, err := readOperationReceipt(ctx, tx, lookup.OperationID)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, err
	}
	if !found {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	if receiptRow.operationKind != boardOwnerRequestCommitOperationKind || !validBoardOwnerRequestDigest(receiptRow.inputSHA256) {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	var receipt boardOwnerRequestCommitReceipt
	decoder := json.NewDecoder(strings.NewReader(receiptRow.resultJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(encoded, []byte(receiptRow.resultJSON)) || receipt.Lookup != lookup ||
		receipt.WorkID != boardOwnerRequestWorkID(lookup) || receipt.Event.EventID == "" || receipt.Event.EventSeq <= 0 ||
		receipt.Message.MessageID == "" || receipt.Message.EventID != receipt.Event.EventID ||
		!validBoardOwnerRequestInvocationReceipt(receipt.InvocationReceipt) {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	event, err := loadEventEnvelopeTx(ctx, tx, receipt.Event.EventID)
	if err != nil || event.EventSeq != receipt.Event.EventSeq {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	storedIndex, err := loadExternalMessageIndexTx(ctx, tx, receipt.Message.MessageID)
	if err != nil || storedIndex.EventID != receipt.Message.EventID {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	var target conversationruntime.NewBoardThreadTarget
	var targetKind, threadID, replyTo, workID, responseMessageID string
	var workKind, dedupeKey, sourceEventID, workSessionID string
	err = tx.QueryRowContext(ctx, `
		SELECT p.work_id,p.response_message_id,p.board_instance_id,p.core_principal_id,p.peer_principal_id,p.channel_id,
		       p.target_kind,p.title,p.thread_id,p.reply_to,w.kind,w.dedupe_key,w.source_event_id,w.session_id
		FROM live_board_publication p JOIN live_work_outbox w ON w.work_id=p.work_id
		WHERE p.work_id=?`, receipt.WorkID).Scan(
		&workID, &responseMessageID, &target.BoardInstanceID, &target.CorePrincipalID, &target.PeerPrincipalID, &target.ChannelID,
		&targetKind, &target.Title, &threadID, &replyTo, &workKind, &dedupeKey, &sourceEventID, &workSessionID,
	)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	expectedTarget := conversationruntime.NewBoardThreadTarget{
		BoardInstanceID: lookup.Source.BoardInstanceID, CorePrincipalID: lookup.Source.CorePrincipalID,
		ChannelID: lookup.Source.ChannelID, PeerPrincipalID: lookup.Source.PeerPrincipalID, Title: lookup.Source.Title,
	}
	if workID != receipt.WorkID || responseMessageID != string(receipt.Message.MessageID) || target != expectedTarget ||
		targetKind != boardPublicationTargetNewRoot || threadID != "" || replyTo != "" || workKind != "board_publication" ||
		dedupeKey != boardOwnerRequestPublicationDedupeKey(lookup) || sourceEventID != string(event.EventID) || workSessionID != string(lookup.SessionID) {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, ErrBoardOwnerRequestCorrupt
	}
	outcome := conversationruntime.BoardOwnerRequestOutcome{
		Lookup: lookup, Event: event, MessageIndex: applicationMessageIndex(storedIndex),
		InvocationReceipt: receipt.InvocationReceipt, Target: target, IdempotencyKey: lookup.Source.IdempotencyKey,
	}
	if err := validateBoardOwnerRequestOutcomeRecord(outcome, lookup, accepted, reservation); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, false, fmt.Errorf("%w: %v", ErrBoardOwnerRequestCorrupt, err)
	}
	return outcome, true, nil
}

func validateBoardOwnerRequestCommitInput(input conversationruntime.BoardOwnerRequestCommitInput, accepted AcceptedTurn, reservation BoardOwnerRequestReservation, requireUnsequenced bool) error {
	lookup := input.ExpectedLookup
	if err := validateBoardOwnerRequestLookup(lookup); err != nil {
		return err
	}
	if lookup.Source != reservation.Source || lookup.OwnerID != reservation.Source.OwnerID ||
		lookup.SessionID != accepted.SessionID || lookup.TurnID != accepted.TurnID || lookup.GenerationEpoch != accepted.GenerationEpoch ||
		lookup.OperationID != boardOwnerRequestOutputOperationID(accepted.TurnID) ||
		input.IdempotencyKey != lookup.Source.IdempotencyKey || input.Target != (conversationruntime.NewBoardThreadTarget{
		BoardInstanceID: lookup.Source.BoardInstanceID, CorePrincipalID: lookup.Source.CorePrincipalID,
		ChannelID: lookup.Source.ChannelID, PeerPrincipalID: lookup.Source.PeerPrincipalID, Title: lookup.Source.Title,
	}) || !validBoardOwnerRequestInvocationReceipt(input.InvocationReceipt) {
		return ErrBoardOwnerRequestInvalidOutcome
	}
	fingerprint, err := boardOwnerRequestFingerprint(lookup.Source, boardOwnerRequestPayloadText(accepted.InputEvent.Payload))
	if err != nil || fingerprint != lookup.SourceFingerprint {
		return ErrBoardOwnerRequestScopeMismatch
	}
	event, index := input.Event, eventStoreMessageIndex(input.MessageIndex)
	if err := modulecore.ValidateEventEnvelope(event); err != nil || requireUnsequenced && event.EventSeq != 0 ||
		!requireUnsequenced && event.EventSeq <= 0 || event.EventType != "agent.response" || event.ComponentID != "conversation_runtime" ||
		event.ActorKind != "agent" || event.ActorID != "mio" || event.TraceID != accepted.InputEvent.TraceID ||
		event.CausationEventID != accepted.InputEvent.EventID || len(event.DependencyEventIDs) != 0 ||
		event.SessionID != accepted.SessionID || event.TurnID != accepted.TurnID || event.ThreadID != "" ||
		event.RequestID != "" || event.ResponseID != "" || event.UtteranceID != "" || event.TaskID != "" || event.RunID != "" ||
		event.ActionID != "" || event.AttemptID != "" || event.WorkstreamID != "" || event.GoalID != "" || event.ArtifactID != "" ||
		event.EvidenceID != "" || event.MemoryID != "" || event.RelationID != "" || event.ScheduleID != "" || event.QueueItemID != "" ||
		event.CheckpointID != "" || event.ReceiptID != "" || event.MessageID == accepted.InputMessageID ||
		!boardOwnerRequestIDsAvoidBoardReferences(event, lookup.Source) || !boardOwnerRequestExactStringKeys(event.Payload,
		"kind", "visibility", "from", "source_actor_id", "to", "text", "content") {
		return ErrBoardOwnerRequestInvalidOutcome
	}
	text, textOK := event.Payload["text"].(string)
	content, contentOK := event.Payload["content"].(string)
	if !textOK || !contentOK || !validBoardOwnerRequestInstruction(text) ||
		strings.TrimSpace(text) == strings.TrimSpace(boardOwnerRequestPayloadText(accepted.InputEvent.Payload)) ||
		text != content || event.Payload["kind"] != "reply" || event.Payload["visibility"] != "public" ||
		event.Payload["from"] != "mio" || event.Payload["source_actor_id"] != "mio" || event.Payload["to"] != "external_peer" {
		return ErrBoardOwnerRequestInvalidOutcome
	}
	if err := validateMessageIndex(index, event, accepted.SessionID, accepted.TurnID); err != nil ||
		index.FromActor != "mio" || index.ToKind != "external_peer" || index.ToActor != lookup.Source.PeerPrincipalID ||
		index.Kind != "board_owner_request" || index.Visibility != "public" || index.ReplyToMessageID != "" {
		return ErrBoardOwnerRequestInvalidOutcome
	}
	return nil
}

func boardOwnerRequestIDsAvoidBoardReferences(event modulecore.EventEnvelope, source conversationruntime.BoardOwnerRequestSource) bool {
	for _, coreID := range []string{string(event.SessionID), string(event.TurnID), string(event.MessageID), string(event.EventID), string(event.TraceID)} {
		for _, boardID := range []string{source.BoardInstanceID, source.CorePrincipalID, source.ChannelID, source.PeerPrincipalID} {
			if coreID != "" && coreID == boardID {
				return false
			}
		}
	}
	return true
}

func validateBoardOwnerRequestOutcomeRecord(outcome conversationruntime.BoardOwnerRequestOutcome, lookup conversationruntime.BoardOwnerRequestLookup, accepted AcceptedTurn, reservation BoardOwnerRequestReservation) error {
	if outcome.Lookup != lookup || outcome.Target != (conversationruntime.NewBoardThreadTarget{
		BoardInstanceID: lookup.Source.BoardInstanceID, CorePrincipalID: lookup.Source.CorePrincipalID,
		ChannelID: lookup.Source.ChannelID, PeerPrincipalID: lookup.Source.PeerPrincipalID, Title: lookup.Source.Title,
	}) || outcome.IdempotencyKey != lookup.Source.IdempotencyKey ||
		!validBoardOwnerRequestInvocationReceipt(outcome.InvocationReceipt) {
		return ErrBoardOwnerRequestInvalidOutcome
	}
	commit := conversationruntime.BoardOwnerRequestCommitInput{
		ExpectedLookup: lookup, Event: outcome.Event, MessageIndex: outcome.MessageIndex,
		InvocationReceipt: outcome.InvocationReceipt, Target: outcome.Target, IdempotencyKey: outcome.IdempotencyKey,
	}
	return validateBoardOwnerRequestCommitInput(commit, accepted, reservation, false)
}

func sameBoardOwnerRequestOutcome(outcome conversationruntime.BoardOwnerRequestOutcome, input conversationruntime.BoardOwnerRequestCommitInput) bool {
	if outcome.Lookup != input.ExpectedLookup || outcome.MessageIndex != input.MessageIndex ||
		outcome.InvocationReceipt != input.InvocationReceipt || outcome.Target != input.Target || outcome.IdempotencyKey != input.IdempotencyKey {
		return false
	}
	event := input.Event
	event.EventSeq = outcome.Event.EventSeq
	return reflect.DeepEqual(outcome.Event, event)
}

// CommitBoardOwnerRequest atomically stores Mio's canonical response, its
// private unresolved new-root target, publication work, turn CAS, and receipt.
func (s *SQLiteStore) CommitBoardOwnerRequest(ctx context.Context, input conversationruntime.BoardOwnerRequestCommitInput) (conversationruntime.BoardOwnerRequestOutcome, error) {
	if err := s.ensureOpen(); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if ctx == nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, errors.New("BOARD owner request commit context is required")
	}
	if err := validateBoardOwnerRequestLookup(input.ExpectedLookup); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if input.Event.EventSeq != 0 || !validBoardOwnerRequestInvocationReceipt(input.InvocationReceipt) {
		return conversationruntime.BoardOwnerRequestOutcome{}, ErrBoardOwnerRequestInvalidOutcome
	}
	if s.shouldQueueWrite(ctx) {
		queued := cloneQueuedValue(input)
		sizeBytes, err := queuedWriteSize(queued)
		if err != nil {
			return conversationruntime.BoardOwnerRequestOutcome{}, fmt.Errorf("size queued BOARD owner request commit: %w", err)
		}
		var outcome conversationruntime.BoardOwnerRequestOutcome
		err = s.enqueueOrderedWrite(ctx, 1, sizeBytes, func(commandCtx context.Context) error {
			var commitErr error
			outcome, commitErr = s.commitBoardOwnerRequestInline(commandCtx, queued)
			return commitErr
		})
		return outcome, err
	}
	return s.commitBoardOwnerRequestInline(ctx, input)
}

func (s *SQLiteStore) commitBoardOwnerRequestInline(ctx context.Context, input conversationruntime.BoardOwnerRequestCommitInput) (conversationruntime.BoardOwnerRequestOutcome, error) {
	inputHash, err := hashJSON(input)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, fmt.Errorf("hash BOARD owner request output: %w", err)
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	accepted, _, ownerID, sessionOwnerID, resultReceiptID, err := boardOwnerRequestAcceptedTurnTx(ctx, tx, input.ExpectedLookup.TurnID)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	inputMessage, err := loadExternalMessageIndexTx(ctx, tx, accepted.InputMessageID)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	reservation, err := loadBoardOwnerRequestReservationTx(ctx, tx, input.ExpectedLookup.Source)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if err := validateBoardOwnerRequestAcceptedRecord(ctx, tx, accepted, inputMessage, ownerID, sessionOwnerID, reservation); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if accepted.SessionID != input.ExpectedLookup.SessionID || accepted.TurnID != input.ExpectedLookup.TurnID ||
		accepted.GenerationEpoch != input.ExpectedLookup.GenerationEpoch || ownerID != input.ExpectedLookup.OwnerID {
		return conversationruntime.BoardOwnerRequestOutcome{}, ErrBoardOwnerRequestScopeMismatch
	}
	if prior, found, err := readOperationReceipt(ctx, tx, input.ExpectedLookup.OperationID); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	} else if found {
		if prior.operationKind != boardOwnerRequestCommitOperationKind || prior.inputSHA256 != inputHash {
			return conversationruntime.BoardOwnerRequestOutcome{}, ErrBoardOwnerRequestConflict
		}
		outcome, found, err := loadCommittedBoardOwnerRequestOutcomeTx(ctx, tx, input.ExpectedLookup, accepted, reservation)
		if err != nil {
			return conversationruntime.BoardOwnerRequestOutcome{}, err
		}
		if !found || !sameBoardOwnerRequestOutcome(outcome, input) {
			return conversationruntime.BoardOwnerRequestOutcome{}, ErrBoardOwnerRequestCorrupt
		}
		if err := tx.Commit(); err != nil {
			return conversationruntime.BoardOwnerRequestOutcome{}, err
		}
		committed = true
		return outcome, nil
	}
	if accepted.State != "accepted" || resultReceiptID != "" {
		return conversationruntime.BoardOwnerRequestOutcome{}, ErrConversationTurnConflict
	}
	if err := validateBoardOwnerRequestCommitInput(input, accepted, reservation, true); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	event := input.Event
	event.DependencyEventIDs = append([]modulecore.EventID(nil), input.Event.DependencyEventIDs...)
	event.Payload = cloneMutablePayload(input.Event.Payload)
	event.EventSeq, err = s.nextSequence(ctx)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if err := s.validateReferences(ctx, tx, event); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if _, err := insertLiveEvent(ctx, tx, event); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, fmt.Errorf("insert canonical BOARD owner response: %w", err)
	}
	index := eventStoreMessageIndex(input.MessageIndex)
	if err := insertMessageIndex(ctx, tx, index); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, fmt.Errorf("insert BOARD owner response index: %w", err)
	}
	if err := validateOutboxSource(ctx, tx, event.EventID, string(accepted.InputEvent.TraceID), accepted.SessionID); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	workID := boardOwnerRequestWorkID(input.ExpectedLookup)
	dedupeKey := boardOwnerRequestPublicationDedupeKey(input.ExpectedLookup)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_work_outbox
			(work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
		VALUES (?,?,'board_publication',?,?,5,'pending',0,'',1)`,
		workID, dedupeKey, string(event.EventID), string(accepted.SessionID)); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, fmt.Errorf("insert BOARD owner publication work: %w", err)
	}
	target := input.Target
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_board_publication
			(work_id,response_message_id,board_instance_id,core_principal_id,peer_principal_id,channel_id,target_kind,title,thread_id,reply_to)
		VALUES (?,?,?,?,?,?,'new_root',?,?,?)`, workID, string(event.MessageID), target.BoardInstanceID, target.CorePrincipalID,
		target.PeerPrincipalID, target.ChannelID, target.Title, "", ""); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, fmt.Errorf("insert private BOARD new-root target: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_conversation_turn
		SET state='completed', result_receipt_id=?
		WHERE turn_id=? AND session_id=? AND owner_id=? AND trigger_kind='board_owner_request'
		  AND generation_epoch=? AND state='accepted' AND result_receipt_id IS NULL`,
		input.ExpectedLookup.OperationID, string(accepted.TurnID), string(accepted.SessionID), input.ExpectedLookup.OwnerID,
		input.ExpectedLookup.GenerationEpoch)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, fmt.Errorf("check BOARD owner turn commit CAS: %w", err)
	}
	if updated != 1 {
		return conversationruntime.BoardOwnerRequestOutcome{}, ErrConversationTurnConflict
	}
	receipt := boardOwnerRequestCommitReceipt{
		Lookup:            input.ExpectedLookup,
		Event:             boardOwnerRequestEventReference{EventID: event.EventID, EventSeq: event.EventSeq},
		Message:           boardOwnerRequestMessageReference{MessageID: event.MessageID, EventID: event.EventID},
		InvocationReceipt: input.InvocationReceipt, WorkID: workID,
	}
	if err := insertOperationReceipt(ctx, tx, input.ExpectedLookup.OperationID, boardOwnerRequestCommitOperationKind, inputHash, receipt); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	outcome, found, err := loadCommittedBoardOwnerRequestOutcomeTx(ctx, tx, input.ExpectedLookup, AcceptedTurn{
		OperationID: accepted.OperationID, ClientSubmissionID: accepted.ClientSubmissionID, SessionID: accepted.SessionID,
		TurnID: accepted.TurnID, ActorID: accepted.ActorID, TriggerKind: accepted.TriggerKind, InputMessageID: accepted.InputMessageID,
		AdmissionSeq: accepted.AdmissionSeq, GenerationEpoch: accepted.GenerationEpoch, State: "completed", InputEvent: accepted.InputEvent,
	}, reservation)
	if err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	if !found || !sameBoardOwnerRequestOutcome(outcome, func() conversationruntime.BoardOwnerRequestCommitInput {
		copy := input
		copy.Event = event
		return copy
	}()) {
		return conversationruntime.BoardOwnerRequestOutcome{}, ErrBoardOwnerRequestCorrupt
	}
	if err := tx.Commit(); err != nil {
		return conversationruntime.BoardOwnerRequestOutcome{}, err
	}
	committed = true
	return outcome, nil
}
