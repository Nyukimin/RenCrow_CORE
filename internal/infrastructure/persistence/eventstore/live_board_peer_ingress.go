package eventstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrBoardPeerIngressUnavailable   = errors.New("BOARD peer ingress store is unavailable")
	ErrBoardPeerIngressPinMismatch   = errors.New("BOARD peer source does not match the pinned connection")
	ErrBoardPeerIngressScopeMismatch = errors.New("BOARD peer ingress scope does not match the stored source")
	ErrBoardPeerIngressConflict      = errors.New("BOARD peer ingress key conflicts with a different source")
	ErrBoardPeerIngressCorrupt       = errors.New("BOARD peer ingress receipt is corrupt")
)

const (
	boardPeerThreadSessionOperationPrefix     = "board-peer-thread-session:"
	boardPeerThreadSessionOperationKind       = "board_peer_thread_session_v1"
	boardPeerRequestStableKeyPrefix           = "board-peer-request:"
	boardPeerRequestAcceptanceOperationPrefix = "board-peer-request-acceptance:"
)

// BoardPeerRequestSource is authenticated BOARD request metadata after the
// caller has verified the connection, ACL, inbox row, and canonical thread.
// The three timestamps are deliberately separate BOARD clock stages.
type BoardPeerRequestSource struct {
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

// BoardPeerThreadSessionKey is the exact owner/pin/channel/thread creation
// tuple used to resolve a canonical conversation session.
type BoardPeerThreadSessionKey struct {
	Scope            BoardPublicationScope `json:"scope"`
	ChannelID        string                `json:"channel_id"`
	ThreadID         string                `json:"thread_id"`
	ThreadCreatedAt  time.Time             `json:"thread_created_at"`
	ResponderActorID string                `json:"responder_actor_id,omitempty"`
}

// BoardPeerThreadSessionBinding is one immutable BOARD-thread-to-CORE-session
// mapping. CreateNewSession marks a resolver-minted session ID that has no
// persisted session row until its first accepted canonical event.
type BoardPeerThreadSessionBinding struct {
	Key              BoardPeerThreadSessionKey `json:"key"`
	SessionID        modulecore.SessionID      `json:"session_id"`
	CreateNewSession bool                      `json:"create_new_session"`
}

// BoardPeerRequestAcceptInput supplies the existing canonical session chosen
// by the owner-side resolver and its current generation fence.
type BoardPeerRequestAcceptInput struct {
	Source          BoardPeerRequestSource `json:"source"`
	SessionID       modulecore.SessionID   `json:"session_id"`
	RuntimeVersion  string                 `json:"runtime_version"`
	GenerationEpoch int64                  `json:"generation_epoch"`
}

// ReadBoardPeerThreadSession reads only the exact immutable BOARD thread key.
func (s *SQLiteStore) ReadBoardPeerThreadSession(ctx context.Context, key BoardPeerThreadSessionKey) (BoardPeerThreadSessionBinding, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardPeerThreadSessionBinding{}, false, err
	}
	if ctx == nil {
		return BoardPeerThreadSessionBinding{}, false, errors.New("BOARD peer thread session context is required")
	}
	if err := validateBoardPeerThreadSessionKey(key); err != nil {
		return BoardPeerThreadSessionBinding{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BoardPeerThreadSessionBinding{}, false, err
	}
	defer tx.Rollback()
	if _, err := requireBoardPeerPinTx(ctx, tx, key.Scope, key.ResponderActorID); err != nil {
		return BoardPeerThreadSessionBinding{}, false, err
	}
	binding, found, err := readBoardPeerThreadSessionTx(ctx, tx, key)
	if err != nil {
		return BoardPeerThreadSessionBinding{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BoardPeerThreadSessionBinding{}, false, err
	}
	return binding, found, nil
}

// BindBoardPeerThreadSession compare-and-sets one exact mapping to an existing
// owner-scoped CORE session or a freshly minted ID awaiting its first event.
func (s *SQLiteStore) BindBoardPeerThreadSession(ctx context.Context, binding BoardPeerThreadSessionBinding) (BoardPeerThreadSessionBinding, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardPeerThreadSessionBinding{}, err
	}
	if ctx == nil {
		return BoardPeerThreadSessionBinding{}, errors.New("BOARD peer thread session context is required")
	}
	if err := validateBoardPeerThreadSessionBinding(binding); err != nil {
		return BoardPeerThreadSessionBinding{}, err
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(binding)
		if err != nil {
			return BoardPeerThreadSessionBinding{}, fmt.Errorf("size BOARD peer thread session write: %w", err)
		}
		var persisted BoardPeerThreadSessionBinding
		err = s.enqueueOrderedWrite(ctx, 0, sizeBytes, func(commandCtx context.Context) error {
			var writeErr error
			persisted, writeErr = s.bindBoardPeerThreadSessionInline(commandCtx, binding)
			return writeErr
		})
		if err != nil {
			return BoardPeerThreadSessionBinding{}, err
		}
		return persisted, nil
	}
	return s.bindBoardPeerThreadSessionInline(ctx, binding)
}

func (s *SQLiteStore) bindBoardPeerThreadSessionInline(ctx context.Context, binding BoardPeerThreadSessionBinding) (BoardPeerThreadSessionBinding, error) {
	inputHash, err := hashJSON(binding)
	if err != nil {
		return BoardPeerThreadSessionBinding{}, fmt.Errorf("hash BOARD peer thread session binding: %w", err)
	}
	operationID, err := boardPeerThreadSessionOperationID(binding.Key)
	if err != nil {
		return BoardPeerThreadSessionBinding{}, err
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return BoardPeerThreadSessionBinding{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := requireBoardPeerPinTx(ctx, tx, binding.Key.Scope, binding.Key.ResponderActorID); err != nil {
		return BoardPeerThreadSessionBinding{}, err
	}
	prior, found, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil {
		return BoardPeerThreadSessionBinding{}, err
	}
	if found {
		stored, err := decodeBoardPeerThreadSessionReceipt(prior)
		if err != nil {
			return BoardPeerThreadSessionBinding{}, err
		}
		if !sameBoardPeerThreadSessionKey(stored.Key, binding.Key) {
			return BoardPeerThreadSessionBinding{}, ErrBoardPeerIngressCorrupt
		}
		if !sameBoardPeerThreadSessionBinding(stored, binding) {
			return BoardPeerThreadSessionBinding{}, ErrBoardPeerIngressConflict
		}
		if err := validateBoardPeerBoundSessionTx(ctx, tx, stored, true); err != nil {
			return BoardPeerThreadSessionBinding{}, err
		}
		if err := tx.Commit(); err != nil {
			return BoardPeerThreadSessionBinding{}, err
		}
		committed = true
		return stored, nil
	}
	if err := validateBoardPeerBoundSessionTx(ctx, tx, binding, false); err != nil {
		return BoardPeerThreadSessionBinding{}, err
	}
	if err := insertOperationReceipt(ctx, tx, operationID, boardPeerThreadSessionOperationKind, inputHash, binding); err != nil {
		return BoardPeerThreadSessionBinding{}, fmt.Errorf("insert BOARD peer thread session receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BoardPeerThreadSessionBinding{}, err
	}
	committed = true
	return binding, nil
}

// FindAcceptedBoardPeerRequest returns false only when this valid, exact
// source has not yet been accepted.
func (s *SQLiteStore) FindAcceptedBoardPeerRequest(ctx context.Context, source BoardPeerRequestSource) (AcceptedTurn, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return AcceptedTurn{}, false, err
	}
	if ctx == nil {
		return AcceptedTurn{}, false, errors.New("BOARD peer acceptance lookup context is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AcceptedTurn{}, false, err
	}
	defer tx.Rollback()
	if _, err := validateBoardPeerRequestSourceTx(ctx, tx, source); err != nil {
		return AcceptedTurn{}, false, err
	}
	threadBinding, found, err := readBoardPeerThreadSessionTx(ctx, tx, boardPeerThreadSessionKeyForSource(source))
	if err != nil {
		return AcceptedTurn{}, false, err
	}
	if !found {
		return AcceptedTurn{}, false, nil
	}
	accepted, found, err := loadAcceptedBoardPeerRequestTx(ctx, tx, source, threadBinding.SessionID)
	if err != nil {
		return AcceptedTurn{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return AcceptedTurn{}, false, err
	}
	return accepted, found, nil
}

// AcceptBoardPeerRequest durably accepts the exact authenticated BOARD root
// request against its previously bound CORE session.
func (s *SQLiteStore) AcceptBoardPeerRequest(ctx context.Context, input BoardPeerRequestAcceptInput) (AcceptedTurn, error) {
	if err := s.ensureOpen(); err != nil {
		return AcceptedTurn{}, err
	}
	if ctx == nil {
		return AcceptedTurn{}, errors.New("BOARD peer acceptance context is required")
	}
	if input.RuntimeVersion == "" || strings.TrimSpace(input.RuntimeVersion) != input.RuntimeVersion || input.GenerationEpoch <= 0 {
		return AcceptedTurn{}, errors.New("BOARD peer acceptance runtime version and positive generation epoch are required")
	}
	if err := validateBoardPeerRequestSource(input.Source); err != nil {
		return AcceptedTurn{}, err
	}
	key := boardPeerThreadSessionKeyForSource(input.Source)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AcceptedTurn{}, err
	}
	if _, err := validateBoardPeerRequestSourceTx(ctx, tx, input.Source); err != nil {
		_ = tx.Rollback()
		return AcceptedTurn{}, err
	}
	threadBinding, found, err := readBoardPeerThreadSessionTx(ctx, tx, key)
	if err != nil {
		_ = tx.Rollback()
		return AcceptedTurn{}, err
	}
	if !found || threadBinding.SessionID != input.SessionID {
		_ = tx.Rollback()
		return AcceptedTurn{}, ErrBoardPeerIngressScopeMismatch
	}
	var ownerID string
	var runtimeVersion string
	var epoch int64
	err = tx.QueryRowContext(ctx, `SELECT owner_id, runtime_version, epoch FROM live_conversation_session WHERE session_id = ?`, string(input.SessionID)).Scan(&ownerID, &runtimeVersion, &epoch)
	sessionExists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return AcceptedTurn{}, err
	}
	if !sessionExists && !threadBinding.CreateNewSession {
		_ = tx.Rollback()
		return AcceptedTurn{}, ErrBoardPeerIngressScopeMismatch
	}
	if sessionExists && (ownerID != input.Source.Scope.OwnerID || runtimeVersion != input.RuntimeVersion || epoch != input.GenerationEpoch) {
		_ = tx.Rollback()
		return AcceptedTurn{}, ErrConversationSessionConflict
	}
	if err := tx.Commit(); err != nil {
		return AcceptedTurn{}, err
	}

	operationID, clientSubmissionID, err := boardPeerRequestOperationIDs(input.Source)
	if err != nil {
		return AcceptedTurn{}, err
	}
	actorID, err := input.Source.Binding.SourceActorID()
	if err != nil {
		return AcceptedTurn{}, err
	}
	responderActorID := input.Source.Binding.EffectiveResponderActorID()
	event := modulecore.NewRootEventEnvelope(
		"orchestrator", "conversation.external_peer_message", input.Source.MessageAt, boardPeerRequestPayload(input.Source, actorID),
	)
	event.SessionID = input.SessionID
	event.TurnID = modulecore.NewTurnID()
	event.MessageID = modulecore.NewMessageID()
	acceptInput := AcceptInput{
		OperationID: operationID, OwnerID: input.Source.Scope.OwnerID, RuntimeVersion: input.RuntimeVersion,
		ClientSubmissionID: clientSubmissionID, ActorID: actorID, TriggerKind: "external_peer_message",
		GenerationEpoch: input.GenerationEpoch, InputEvent: event,
		InputMessage: MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: input.SessionID, TurnID: event.TurnID,
			FromActor: actorID, ToKind: input.Source.Binding.ResponderActorKind(), ToActor: responderActorID, Kind: "external_peer_message", Visibility: "public",
		},
		ExternalPeerBinding: &input.Source.Binding,
		CreateNewSession:    !sessionExists && threadBinding.CreateNewSession,
	}
	return s.AcceptTurn(ctx, acceptInput)
}

func validateBoardPeerRequestSource(source BoardPeerRequestSource) error {
	if err := validateBoardPublicationScope(source.Scope); err != nil {
		return ErrBoardPeerIngressScopeMismatch
	}
	if err := source.Binding.ValidateNewRequest(); err != nil {
		return fmt.Errorf("validate BOARD peer request binding: %w", err)
	}
	if source.Binding.BoardInstanceID != source.Scope.JournalInstanceID || source.Binding.RecipientPrincipalID != source.Scope.PrincipalID {
		return ErrBoardPeerIngressScopeMismatch
	}
	if source.InboxItemID == "" || strings.TrimSpace(source.InboxItemID) != source.InboxItemID || strings.TrimSpace(source.Body) == "" ||
		source.CausationID != source.Binding.MessageID || source.HopCount != 0 {
		return errors.New("BOARD peer request source identity, body, or root causation is invalid")
	}
	for name, value := range map[string]time.Time{
		"inbox_created_at":  source.InboxCreatedAt,
		"message_at":        source.MessageAt,
		"thread_created_at": source.ThreadCreatedAt,
	} {
		if value.IsZero() || value.Location() != time.UTC {
			return fmt.Errorf("BOARD peer request %s must be a fixed UTC timestamp", name)
		}
	}
	if len(source.Recipients) == 0 || !containsExactString(source.Recipients, source.Scope.PrincipalID) {
		return ErrBoardPeerIngressScopeMismatch
	}
	if err := validateBoardPeerPrincipalList(source.Recipients, "recipients", false); err != nil {
		return err
	}
	if err := validateBoardPeerPrincipalList(source.Mentions, "mentions", true); err != nil {
		return err
	}
	return nil
}

func validateBoardPeerRequestSourceTx(ctx context.Context, tx *sql.Tx, source BoardPeerRequestSource) (BoardConnectionPin, error) {
	if err := validateBoardPeerRequestSource(source); err != nil {
		return BoardConnectionPin{}, err
	}
	pin, err := requireBoardPeerPinTx(ctx, tx, source.Scope, source.Binding.ResponderActorID)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	if !source.InboxCreatedAt.After(pin.CutoverAt) {
		return BoardConnectionPin{}, ErrBoardPeerIngressPinMismatch
	}
	return pin, nil
}

func readBoardPeerConnectionPinTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope, responderActorID string) (BoardConnectionPin, bool, error) {
	if err := validateBoardPublicationScope(scope); err != nil {
		return BoardConnectionPin{}, false, ErrBoardPeerIngressScopeMismatch
	}
	if responderActorID == "" {
		return readBoardConnectionPinTx(ctx, tx)
	}
	if responderActorID != scope.PrincipalID || !domainconversation.IsExternalPeerResponderActor(responderActorID) {
		return BoardConnectionPin{}, false, ErrBoardPeerIngressScopeMismatch
	}
	return readBoardConnectionPinForPrincipalTx(ctx, tx, responderActorID)
}

func requireBoardPeerPinTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope, responderActorID string) (BoardConnectionPin, error) {
	pin, found, err := readBoardPeerConnectionPinTx(ctx, tx, scope, responderActorID)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	if !found {
		return BoardConnectionPin{}, ErrBoardPeerIngressPinMismatch
	}
	if pin.OwnerID != scope.OwnerID || pin.JournalInstanceID != scope.JournalInstanceID || pin.PrincipalID != scope.PrincipalID {
		return BoardConnectionPin{}, ErrBoardPeerIngressScopeMismatch
	}
	return pin, nil
}

func validateBoardPeerPrincipalList(values []string, name string, emptyAllowed bool) error {
	if !emptyAllowed && len(values) == 0 {
		return fmt.Errorf("BOARD peer request %s must not be empty", name)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("BOARD peer request %s contains an invalid principal", name)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("BOARD peer request %s contains a duplicate principal", name)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func containsExactString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateBoardPeerThreadSessionKey(key BoardPeerThreadSessionKey) error {
	if err := validateBoardPublicationScope(key.Scope); err != nil {
		return ErrBoardPeerIngressScopeMismatch
	}
	if key.ResponderActorID != "" && (key.ResponderActorID != key.Scope.PrincipalID ||
		!domainconversation.IsExternalPeerResponderActor(key.ResponderActorID)) {
		return ErrBoardPeerIngressScopeMismatch
	}
	if !validBoardPeerReference(key.ChannelID) || !validBoardPeerReference(key.ThreadID) ||
		key.ThreadCreatedAt.IsZero() || key.ThreadCreatedAt.Location() != time.UTC {
		return errors.New("BOARD peer thread session key requires channel, thread, and fixed UTC creation time")
	}
	return nil
}

func validBoardPeerReference(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validateBoardPeerThreadSessionBinding(binding BoardPeerThreadSessionBinding) error {
	if err := validateBoardPeerThreadSessionKey(binding.Key); err != nil {
		return err
	}
	if err := binding.SessionID.Validate(); err != nil {
		return fmt.Errorf("BOARD peer thread session id: %w", err)
	}
	return nil
}

func boardPeerThreadSessionKeyForSource(source BoardPeerRequestSource) BoardPeerThreadSessionKey {
	return BoardPeerThreadSessionKey{
		Scope: source.Scope, ChannelID: source.Binding.ChannelID, ThreadID: source.Binding.ThreadID,
		ThreadCreatedAt: source.ThreadCreatedAt, ResponderActorID: source.Binding.ResponderActorID,
	}
}

func readBoardPeerThreadSessionTx(ctx context.Context, tx *sql.Tx, key BoardPeerThreadSessionKey) (BoardPeerThreadSessionBinding, bool, error) {
	operationID, err := boardPeerThreadSessionOperationID(key)
	if err != nil {
		return BoardPeerThreadSessionBinding{}, false, err
	}
	receipt, found, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil || !found {
		return BoardPeerThreadSessionBinding{}, found, err
	}
	binding, err := decodeBoardPeerThreadSessionReceipt(receipt)
	if err != nil {
		return BoardPeerThreadSessionBinding{}, false, err
	}
	if !sameBoardPeerThreadSessionKey(binding.Key, key) {
		return BoardPeerThreadSessionBinding{}, false, ErrBoardPeerIngressCorrupt
	}
	if err := validateBoardPeerBoundSessionTx(ctx, tx, binding, true); err != nil {
		if errors.Is(err, ErrBoardPeerIngressScopeMismatch) {
			return BoardPeerThreadSessionBinding{}, false, ErrBoardPeerIngressCorrupt
		}
		return BoardPeerThreadSessionBinding{}, false, err
	}
	return binding, true, nil
}

func validateBoardPeerBoundSessionTx(ctx context.Context, tx *sql.Tx, binding BoardPeerThreadSessionBinding, allowExistingFresh bool) error {
	var ownerID string
	err := tx.QueryRowContext(ctx, `SELECT owner_id FROM live_conversation_session WHERE session_id = ?`, string(binding.SessionID)).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		if binding.CreateNewSession {
			var existingEvents int
			if countErr := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM event_envelope
				WHERE json_extract(envelope_json, '$.session_id') = ?`, string(binding.SessionID)).Scan(&existingEvents); countErr != nil {
				return countErr
			}
			if existingEvents == 0 {
				return nil
			}
		}
		return ErrBoardPeerIngressScopeMismatch
	}
	if err != nil {
		return err
	}
	if ownerID != binding.Key.Scope.OwnerID || binding.CreateNewSession && !allowExistingFresh {
		return ErrBoardPeerIngressScopeMismatch
	}
	return nil
}

func decodeBoardPeerThreadSessionReceipt(receipt operationReceipt) (BoardPeerThreadSessionBinding, error) {
	if receipt.operationKind != boardPeerThreadSessionOperationKind {
		return BoardPeerThreadSessionBinding{}, ErrBoardPeerIngressCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(receipt.resultJSON)))
	decoder.DisallowUnknownFields()
	var binding BoardPeerThreadSessionBinding
	if err := decoder.Decode(&binding); err != nil {
		return BoardPeerThreadSessionBinding{}, ErrBoardPeerIngressCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BoardPeerThreadSessionBinding{}, ErrBoardPeerIngressCorrupt
	}
	if err := validateBoardPeerThreadSessionBinding(binding); err != nil {
		return BoardPeerThreadSessionBinding{}, ErrBoardPeerIngressCorrupt
	}
	encoded, err := json.Marshal(binding)
	if err != nil || !bytes.Equal(encoded, []byte(receipt.resultJSON)) {
		return BoardPeerThreadSessionBinding{}, ErrBoardPeerIngressCorrupt
	}
	inputHash, err := hashJSON(binding)
	if err != nil || receipt.inputSHA256 != inputHash {
		return BoardPeerThreadSessionBinding{}, ErrBoardPeerIngressCorrupt
	}
	return binding, nil
}

func boardPeerThreadSessionOperationID(key BoardPeerThreadSessionKey) (string, error) {
	if err := validateBoardPeerThreadSessionKey(key); err != nil {
		return "", err
	}
	hash, err := hashJSON(key)
	if err != nil {
		return "", fmt.Errorf("hash BOARD peer thread session key: %w", err)
	}
	return boardPeerThreadSessionOperationPrefix + hash, nil
}

func sameBoardPeerThreadSessionKey(left, right BoardPeerThreadSessionKey) bool {
	return left.Scope == right.Scope && left.ChannelID == right.ChannelID && left.ThreadID == right.ThreadID &&
		left.ThreadCreatedAt.Equal(right.ThreadCreatedAt) && left.ThreadCreatedAt.Location() == right.ThreadCreatedAt.Location() &&
		left.ResponderActorID == right.ResponderActorID
}

func sameBoardPeerThreadSessionBinding(left, right BoardPeerThreadSessionBinding) bool {
	return left.SessionID == right.SessionID && sameBoardPeerThreadSessionKey(left.Key, right.Key)
}

func boardPeerRequestPayload(source BoardPeerRequestSource, actorID string) map[string]any {
	return map[string]any{
		"from": "external_peer", "source_actor_id": actorID, "to": source.Binding.EffectiveResponderActorID(), "visibility": "public",
		"text": source.Body, "content": source.Body, "body": map[string]any{"text": source.Body},
		"external_peer_binding": source.Binding.ToPayload(), "board_source": boardPeerRequestSourcePayload(source),
	}
}

func boardPeerRequestSourcePayload(source BoardPeerRequestSource) map[string]any {
	return map[string]any{
		"inbox_item_id":     source.InboxItemID,
		"inbox_created_at":  source.InboxCreatedAt.UTC().Format(time.RFC3339Nano),
		"message_at":        source.MessageAt.UTC().Format(time.RFC3339Nano),
		"thread_created_at": source.ThreadCreatedAt.UTC().Format(time.RFC3339Nano),
		"recipients":        append([]string(nil), source.Recipients...), "mentions": append([]string(nil), source.Mentions...),
		"causation_id": source.CausationID, "hop_count": source.HopCount,
	}
}

func boardPeerRequestOperationIDs(source BoardPeerRequestSource) (operationID, clientSubmissionID string, err error) {
	if err := validateBoardPeerRequestSource(source); err != nil {
		return "", "", err
	}
	hash, err := boardPeerRequestStableIDHash(source)
	if err != nil {
		return "", "", err
	}
	return boardPeerRequestAcceptanceOperationPrefix + hash, boardPeerRequestStableKeyPrefix + hash, nil
}

func loadAcceptedBoardPeerRequestTx(ctx context.Context, tx *sql.Tx, source BoardPeerRequestSource, expectedSessionID modulecore.SessionID) (AcceptedTurn, bool, error) {
	operationID, clientSubmissionID, err := boardPeerRequestOperationIDs(source)
	if err != nil {
		return AcceptedTurn{}, false, err
	}
	receipt, found, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil || !found {
		return AcceptedTurn{}, found, err
	}
	if receipt.operationKind != "accept_turn" {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	var accepted AcceptedTurn
	decoder := json.NewDecoder(bytes.NewReader([]byte(receipt.resultJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&accepted); err != nil {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	encoded, err := json.Marshal(accepted)
	if err != nil || !bytes.Equal(encoded, []byte(receipt.resultJSON)) {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	if accepted.OperationID != operationID || accepted.ClientSubmissionID != clientSubmissionID ||
		accepted.TriggerKind != "external_peer_message" || accepted.ActorID == "" || accepted.SessionID != expectedSessionID ||
		accepted.InputMessageID != accepted.InputEvent.MessageID || accepted.TurnID != accepted.InputEvent.TurnID ||
		accepted.SessionID != accepted.InputEvent.SessionID || accepted.AdmissionSeq != accepted.InputEvent.EventSeq ||
		accepted.GenerationEpoch <= 0 || accepted.State != "accepted" {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}

	var ownerID, sessionOwnerID, runtimeVersion, actorID, triggerKind, storedClientID, acceptReceiptID, state string
	var turnID, sessionID, inputMessageID string
	var admissionSeq, generationEpoch, sessionCutoverSeq, sessionAdmissionSeq, sessionEpoch int64
	var acceptedMessageEventID string
	var replyTo sql.NullString
	var message MessageIndex
	err = tx.QueryRowContext(ctx, `
		SELECT t.turn_id,t.session_id,t.owner_id,s.owner_id,s.runtime_version,s.cutover_seq,s.admission_seq,s.epoch,
		       t.actor_id,t.trigger_kind,t.client_submission_id,t.accept_receipt_id,t.input_message_id,t.admission_seq,
	       t.generation_epoch,t.state,m.event_id,m.from_actor,m.to_kind,m.to_actor,m.kind,m.reply_to_message_id,m.visibility
		FROM live_conversation_turn t
		JOIN live_conversation_session s ON s.session_id=t.session_id
		JOIN live_message_index m ON m.message_id=t.input_message_id AND m.session_id=t.session_id AND m.turn_id=t.turn_id
		WHERE t.turn_id=?`, string(accepted.TurnID)).Scan(
		&turnID, &sessionID, &ownerID, &sessionOwnerID, &runtimeVersion, &sessionCutoverSeq, &sessionAdmissionSeq, &sessionEpoch,
		&actorID, &triggerKind, &storedClientID, &acceptReceiptID, &inputMessageID, &admissionSeq, &generationEpoch, &state,
		&acceptedMessageEventID, &message.FromActor, &message.ToKind, &message.ToActor, &message.Kind, &replyTo, &message.Visibility,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
		}
		return AcceptedTurn{}, false, err
	}
	message.MessageID = modulecore.MessageID(inputMessageID)
	message.EventID = modulecore.EventID(acceptedMessageEventID)
	message.SessionID = modulecore.SessionID(sessionID)
	message.TurnID = modulecore.TurnID(turnID)
	if replyTo.Valid {
		message.ReplyToMessageID = modulecore.MessageID(replyTo.String)
	}
	if ownerID != source.Scope.OwnerID || sessionOwnerID != source.Scope.OwnerID || runtimeVersion == "" ||
		turnID != string(accepted.TurnID) || sessionID != string(expectedSessionID) || actorID != accepted.ActorID ||
		triggerKind != "external_peer_message" || storedClientID != clientSubmissionID || acceptReceiptID != operationID ||
		inputMessageID != string(accepted.InputMessageID) || admissionSeq != int64(accepted.AdmissionSeq) ||
		generationEpoch != accepted.GenerationEpoch || sessionEpoch != accepted.GenerationEpoch || state == "" ||
		admissionSeq <= sessionCutoverSeq || admissionSeq > sessionAdmissionSeq {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	event, err := loadEventEnvelopeTx(ctx, tx, message.EventID)
	if err != nil {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	if !reflect.DeepEqual(event, accepted.InputEvent) {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	if !canonicalBoardPeerJSONEqual(accepted.InputEvent.Payload, boardPeerRequestPayload(source, accepted.ActorID)) || !event.OccurredAt.Equal(source.MessageAt) {
		return AcceptedTurn{}, false, ErrBoardPeerIngressConflict
	}
	if err := validateExternalPeerEnvelopeAndIndex(source.Binding, accepted.ActorID, event, message, expectedSessionID, accepted.TurnID); err != nil {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	input := AcceptInput{
		OperationID: operationID, OwnerID: ownerID, RuntimeVersion: runtimeVersion, ClientSubmissionID: clientSubmissionID,
		ActorID: actorID, TriggerKind: triggerKind, GenerationEpoch: generationEpoch, InputEvent: event,
		InputMessage: message, ExternalPeerBinding: &source.Binding,
	}
	inputHash, err := acceptInputHash(input)
	if err != nil || inputHash != receipt.inputSHA256 {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	var storedHash string
	if err := tx.QueryRowContext(ctx, `SELECT submission_sha256 FROM live_conversation_turn WHERE turn_id=?`, string(accepted.TurnID)).Scan(&storedHash); err != nil {
		return AcceptedTurn{}, false, err
	}
	if storedHash != inputHash {
		return AcceptedTurn{}, false, ErrBoardPeerIngressCorrupt
	}
	return accepted, true, nil
}

func boardPeerRequestStableIDHash(source BoardPeerRequestSource) (string, error) {
	identity := struct {
		Scope           BoardPublicationScope `json:"scope"`
		BoardInstanceID string                `json:"board_instance_id"`
		MessageID       string                `json:"message_id"`
	}{Scope: source.Scope, BoardInstanceID: source.Binding.BoardInstanceID, MessageID: source.Binding.MessageID}
	hash, err := hashJSON(identity)
	if err != nil {
		return "", fmt.Errorf("hash BOARD peer message identity: %w", err)
	}
	return hash, nil
}

func canonicalBoardPeerJSONEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
