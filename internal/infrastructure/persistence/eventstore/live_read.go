package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrLegacySessionBoundaryConflict = errors.New("legacy conversation session is not at the verified cutover boundary")
	ErrProjectionWatermarkConflict   = errors.New("conversation projection watermark changed or cannot advance")
)

// SessionState is the owner-checked, version-pinned EventStore state for one
// live conversation session.
type SessionState struct {
	SessionID           modulecore.SessionID `json:"session_id"`
	OwnerID             string               `json:"owner_id"`
	RuntimeVersion      string               `json:"runtime_version"`
	CutoverSeq          modulecore.EventSeq  `json:"cutover_seq"`
	AdmissionSeq        modulecore.EventSeq  `json:"admission_seq"`
	ProjectionWatermark modulecore.EventSeq  `json:"projection_watermark"`
	GenerationEpoch     int64                `json:"generation_epoch"`
}

// LegacySessionBoundary is a read-verified legacy session snapshot. Its fields
// are private so callers cannot manufacture a boundary from request data.
type LegacySessionBoundary struct {
	sessionID modulecore.SessionID
	ownerID   string
	through   modulecore.EventSeq
	verified  bool
}

func (b LegacySessionBoundary) SessionID() modulecore.SessionID { return b.sessionID }
func (b LegacySessionBoundary) OwnerID() string                 { return b.ownerID }
func (b LegacySessionBoundary) ThroughSeq() modulecore.EventSeq { return b.through }

// VerifiedIdleBoundaryToken is an in-process assertion issued by trusted
// runtime code only after it has checked legacy Task, Run, and pending-turn
// state. It is intentionally not constructible from an HTTP boolean or JSON.
type VerifiedIdleBoundaryToken struct {
	sessionID modulecore.SessionID
	ownerID   string
	through   modulecore.EventSeq
	verified  bool
}

// NewVerifiedIdleBoundaryToken binds a storage-verified legacy session
// boundary to the trusted runtime's idle check. Callers must invoke it only
// after confirming no legacy Task, Run, or pending turn remains for the
// session.
func NewVerifiedIdleBoundaryToken(boundary LegacySessionBoundary) (VerifiedIdleBoundaryToken, error) {
	if !boundary.verified || boundary.sessionID == "" || strings.TrimSpace(boundary.ownerID) == "" || boundary.through <= 0 {
		return VerifiedIdleBoundaryToken{}, ErrLegacySessionBoundaryConflict
	}
	return VerifiedIdleBoundaryToken{
		sessionID: boundary.sessionID,
		ownerID:   boundary.ownerID,
		through:   boundary.through,
		verified:  true,
	}, nil
}

// CutoverSessionInput pins an existing v1 session to a runtime version after
// the caller has obtained and verified an idle-boundary token.
type CutoverSessionInput struct {
	Boundary        VerifiedIdleBoundaryToken
	RuntimeVersion  string
	GenerationEpoch int64
}

// CanonicalMessage pairs a canonical event body with its storage-owned message
// index. The index carries the actor and visibility data required by callers
// to apply authorization without duplicating the message body.
type CanonicalMessage struct {
	Event modulecore.EventEnvelope `json:"event"`
	Index MessageIndex             `json:"index"`
}

// CanonicalTail is complete through one snapshot watermark. Complete is true
// only after every internally paged row has been decoded and the read tx has
// committed successfully.
type CanonicalTail struct {
	ThroughSeq modulecore.EventSeq `json:"through_seq"`
	Complete   bool                `json:"complete"`
	Messages   []CanonicalMessage  `json:"messages"`
}

// LegacyProjection is a read-only reconstruction from canonical v1
// orchestrator envelopes. AppliedThrough is the requested canonical sequence
// boundary inspected by this snapshot, not an assumed legacy L1 watermark.
type LegacyProjection struct {
	AppliedThrough modulecore.EventSeq   `json:"applied_through"`
	Complete       bool                  `json:"complete"`
	Messages       []LegacyMessage       `json:"messages"`
	Gaps           []LegacyProjectionGap `json:"gaps"`
}

type LegacyMessage struct {
	Index        MessageIndex        `json:"index"`
	EventSeq     modulecore.EventSeq `json:"event_seq"`
	Text         string              `json:"text"`
	TurnOrder    int64               `json:"turn_order"`
	MessageOrder int64               `json:"message_order"`
}

// LegacyProjectionGap identifies a legacy message event that could not be
// safely reconstructed without inventing canonical message or turn IDs.
type LegacyProjectionGap struct {
	EventSeq modulecore.EventSeq `json:"event_seq"`
	EventID  modulecore.EventID  `json:"event_id"`
	Reason   string              `json:"reason"`
}

const liveMessageReadPageSize = 256

// ReadLegacySessionBoundary verifies legacy session ownership and snapshots
// its latest canonical event sequence. Existing v2 journal sessions and
// sessions with unfinished v2 turns cannot be cut over through this path.
func (s *SQLiteStore) ReadLegacySessionBoundary(ctx context.Context, sessionID modulecore.SessionID, ownerID string) (LegacySessionBoundary, error) {
	if err := s.ensureOpen(); err != nil {
		return LegacySessionBoundary{}, err
	}
	if ctx == nil {
		return LegacySessionBoundary{}, errors.New("legacy session boundary context is required")
	}
	if err := sessionID.Validate(); err != nil {
		return LegacySessionBoundary{}, fmt.Errorf("session_id: %w", err)
	}
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
		return LegacySessionBoundary{}, errors.New("owner_id is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LegacySessionBoundary{}, err
	}
	defer tx.Rollback()
	if _, found, err := readSessionStateTx(ctx, tx, sessionID, ""); err != nil {
		return LegacySessionBoundary{}, err
	} else if found {
		return LegacySessionBoundary{}, ErrConversationSessionConflict
	}
	if err := ensureNoUnfinishedLiveTurns(ctx, tx, sessionID); err != nil {
		return LegacySessionBoundary{}, err
	}
	through, err := verifyLegacySessionOwner(ctx, tx, sessionID, ownerID)
	if err != nil {
		return LegacySessionBoundary{}, err
	}
	if err := tx.Commit(); err != nil {
		return LegacySessionBoundary{}, err
	}
	return LegacySessionBoundary{sessionID: sessionID, ownerID: ownerID, through: through, verified: true}, nil
}

// CutoverSession appends the canonical cutover marker and creates the pinned
// v2 session row in one transaction. The old v1 envelopes are left untouched.
func (s *SQLiteStore) CutoverSession(ctx context.Context, input CutoverSessionInput) (SessionState, error) {
	if err := s.ensureOpen(); err != nil {
		return SessionState{}, err
	}
	if ctx == nil {
		return SessionState{}, errors.New("session cutover context is required")
	}
	if !input.Boundary.verified || input.Boundary.sessionID == "" || strings.TrimSpace(input.Boundary.ownerID) == "" || input.Boundary.through <= 0 {
		return SessionState{}, ErrLegacySessionBoundaryConflict
	}
	if strings.TrimSpace(input.RuntimeVersion) == "" || input.RuntimeVersion != strings.TrimSpace(input.RuntimeVersion) || input.GenerationEpoch <= 0 {
		return SessionState{}, errors.New("runtime_version and positive generation_epoch are required")
	}
	if s.shouldQueueWrite(ctx) {
		queuedInput := cloneQueuedValue(input)
		return enqueueOrderedWriteResult(s, ctx, 1, queuedInput, func(commandCtx context.Context) (SessionState, error) {
			return s.CutoverSession(commandCtx, queuedInput)
		})
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return SessionState{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	state, found, err := readSessionStateTx(ctx, tx, input.Boundary.sessionID, "")
	if err != nil {
		return SessionState{}, err
	}
	if found {
		if state.OwnerID != input.Boundary.ownerID || state.RuntimeVersion != input.RuntimeVersion || state.CutoverSeq <= 0 || state.GenerationEpoch != input.GenerationEpoch {
			return SessionState{}, ErrConversationSessionConflict
		}
		if err := validateExistingCutoverMarker(ctx, tx, state, input.Boundary.through); err != nil {
			return SessionState{}, err
		}
		if err := tx.Commit(); err != nil {
			return SessionState{}, err
		}
		committed = true
		return state, nil
	}
	if err := ensureNoUnfinishedLiveTurns(ctx, tx, input.Boundary.sessionID); err != nil {
		return SessionState{}, err
	}
	through, err := verifyLegacySessionOwner(ctx, tx, input.Boundary.sessionID, input.Boundary.ownerID)
	if err != nil {
		return SessionState{}, err
	}
	if through != input.Boundary.through {
		return SessionState{}, ErrLegacySessionBoundaryConflict
	}

	// The insert obtains SQLite's write lock before the marker sequence is
	// assigned. A concurrent append either precedes this snapshot or makes the
	// write transaction fail; neither case can cut over past an unseen event.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_conversation_session
			(session_id, owner_id, runtime_version, cutover_seq, admission_seq, projection_watermark, epoch)
		VALUES (?, ?, ?, 0, 0, 0, ?)`,
		string(input.Boundary.sessionID), input.Boundary.ownerID, input.RuntimeVersion, input.GenerationEpoch,
	); err != nil {
		return SessionState{}, errors.Join(ErrConversationSessionConflict, err)
	}

	marker := modulecore.NewRootEventEnvelope("conversation_journal", "conversation.session.cutover", time.Now().UTC(), map[string]any{
		"generation_epoch":       input.GenerationEpoch,
		"legacy_through_seq":     int64(through),
		"owner_id":               input.Boundary.ownerID,
		"runtime_version":        input.RuntimeVersion,
		"source_runtime_version": "v1",
	})
	marker.SessionID = input.Boundary.sessionID
	marker.EventSeq, err = s.nextSequence(ctx)
	if err != nil {
		return SessionState{}, err
	}
	if err := ensureEventSeqAvailable(ctx, tx, marker.EventSeq); err != nil {
		return SessionState{}, err
	}
	if err := s.validateReferences(ctx, tx, marker); err != nil {
		return SessionState{}, err
	}
	if _, err := insertLiveEvent(ctx, tx, marker); err != nil {
		return SessionState{}, err
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE live_conversation_session
		SET cutover_seq = ?, projection_watermark = ?
		WHERE session_id = ? AND owner_id = ? AND runtime_version = ? AND cutover_seq = 0`,
		int64(marker.EventSeq), int64(marker.EventSeq), string(marker.SessionID), input.Boundary.ownerID, input.RuntimeVersion,
	)
	if err != nil {
		return SessionState{}, err
	}
	rowsAffected, err := updated.RowsAffected()
	if err != nil {
		return SessionState{}, err
	}
	if rowsAffected != 1 {
		return SessionState{}, ErrConversationSessionConflict
	}
	state = SessionState{
		SessionID:           marker.SessionID,
		OwnerID:             input.Boundary.ownerID,
		RuntimeVersion:      input.RuntimeVersion,
		CutoverSeq:          marker.EventSeq,
		ProjectionWatermark: marker.EventSeq,
		GenerationEpoch:     input.GenerationEpoch,
	}
	if err := tx.Commit(); err != nil {
		return SessionState{}, err
	}
	committed = true
	return state, nil
}

// ReadSessionState returns a session only when ownerID matches its persisted
// owner. The bool is false only when the session has no live journal row.
func (s *SQLiteStore) ReadSessionState(ctx context.Context, sessionID modulecore.SessionID, ownerID string) (SessionState, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return SessionState{}, false, err
	}
	if ctx == nil {
		return SessionState{}, false, errors.New("session read context is required")
	}
	if err := sessionID.Validate(); err != nil {
		return SessionState{}, false, fmt.Errorf("session_id: %w", err)
	}
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
		return SessionState{}, false, errors.New("owner_id is required")
	}
	db := s.sessionStateDB
	if db == nil {
		// Private in-memory stores cannot be shared with a second SQLite
		// connection, so retain their existing single-connection behavior.
		db = s.db
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionState{}, false, err
	}
	defer tx.Rollback()
	state, found, err := readSessionStateTx(ctx, tx, sessionID, ownerID)
	if err != nil {
		return SessionState{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return SessionState{}, false, err
	}
	return state, found, nil
}

// ReadCompleteSessionTail reads all indexed v2 messages after afterSeq in one
// snapshot. Rows are fetched in bounded SQL pages, but no page or message cap
// is exposed to callers and Complete is set only after the full tail succeeds.
func (s *SQLiteStore) ReadCompleteSessionTail(ctx context.Context, sessionID modulecore.SessionID, afterSeq modulecore.EventSeq) (CanonicalTail, error) {
	if err := s.ensureOpen(); err != nil {
		return CanonicalTail{}, err
	}
	if ctx == nil {
		return CanonicalTail{}, errors.New("canonical session tail context is required")
	}
	if err := sessionID.Validate(); err != nil {
		return CanonicalTail{}, fmt.Errorf("session_id: %w", err)
	}
	if afterSeq < 0 {
		return CanonicalTail{}, errors.New("after_seq must not be negative")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CanonicalTail{}, err
	}
	defer tx.Rollback()
	state, found, err := readSessionStateTx(ctx, tx, sessionID, "")
	if err != nil {
		return CanonicalTail{}, err
	}
	if !found {
		return CanonicalTail{}, ErrConversationSessionConflict
	}
	var through modulecore.EventSeq
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM event_envelope`).Scan(&through); err != nil {
		return CanonicalTail{}, err
	}
	if afterSeq > through {
		return CanonicalTail{}, errors.New("canonical session tail cursor is beyond the snapshot watermark")
	}
	if state.CutoverSeq > through {
		return CanonicalTail{}, errors.New("session cutover sequence is beyond the canonical snapshot")
	}
	startAfter := afterSeq
	if startAfter < state.CutoverSeq {
		startAfter = state.CutoverSeq
	}
	var unindexed int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM event_envelope e
		WHERE (e.component_id = 'orchestrator' OR json_extract(e.envelope_json, '$.message_id') IS NOT NULL)
		  AND json_extract(e.envelope_json, '$.session_id') = ?
		  AND e.event_seq > ? AND e.event_seq <= ?
		  AND NOT EXISTS (SELECT 1 FROM live_message_index m WHERE m.event_id = e.event_id)`,
		string(sessionID), int64(startAfter), int64(through),
	).Scan(&unindexed); err != nil {
		return CanonicalTail{}, err
	}
	if unindexed != 0 {
		return CanonicalTail{}, errors.New("canonical session tail contains unindexed orchestrator events")
	}
	tail := CanonicalTail{ThroughSeq: through, Messages: make([]CanonicalMessage, 0)}
	for cursor := startAfter; ; {
		rows, err := tx.QueryContext(ctx, `
			SELECT e.event_id, e.event_seq, e.trace_id, e.schema_version, e.event_type, e.component_id,
			       e.occurred_at, e.envelope_json, m.message_id, m.session_id, m.turn_id, m.from_actor,
			       m.to_kind, m.to_actor, m.kind, m.reply_to_message_id, m.visibility
			FROM live_message_index m
		JOIN event_envelope e ON e.event_id = m.event_id
		WHERE m.session_id = ? AND e.event_seq > ? AND e.event_seq <= ?
		ORDER BY e.event_seq ASC LIMIT ?`,
			string(sessionID), int64(cursor), int64(through), liveMessageReadPageSize,
		)
		if err != nil {
			return CanonicalTail{}, err
		}
		pageCount := 0
		for rows.Next() {
			var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, payload string
			var eventSeq int64
			var message MessageIndex
			var messageID, storedSessionID, turnID, fromActor, toKind, toActor, kind, visibility string
			var replyTo sql.NullString
			if err := rows.Scan(
				&eventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload,
				&messageID, &storedSessionID, &turnID, &fromActor, &toKind, &toActor, &kind, &replyTo, &visibility,
			); err != nil {
				_ = rows.Close()
				return CanonicalTail{}, err
			}
			event, err := decodeStoredEnvelope(eventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, payload)
			if err != nil {
				_ = rows.Close()
				return CanonicalTail{}, err
			}
			message = MessageIndex{
				MessageID:        modulecore.MessageID(messageID),
				EventID:          modulecore.EventID(eventID),
				SessionID:        modulecore.SessionID(storedSessionID),
				TurnID:           modulecore.TurnID(turnID),
				FromActor:        fromActor,
				ToKind:           toKind,
				ToActor:          toActor,
				Kind:             kind,
				ReplyToMessageID: modulecore.MessageID(replyTo.String),
				Visibility:       visibility,
			}
			if event.EventSeq <= cursor || event.EventSeq > through || event.SessionID != sessionID {
				_ = rows.Close()
				return CanonicalTail{}, errors.New("canonical session tail row violates its sequence or session filter")
			}
			if err := validateMessageIndex(message, event, sessionID, event.TurnID); err != nil {
				_ = rows.Close()
				return CanonicalTail{}, fmt.Errorf("validate canonical session message %q: %w", message.MessageID, err)
			}
			tail.Messages = append(tail.Messages, CanonicalMessage{Event: event, Index: message})
			cursor = event.EventSeq
			pageCount++
		}
		rowsErr := rows.Err()
		closeErr := rows.Close()
		if rowsErr != nil {
			return CanonicalTail{}, rowsErr
		}
		if closeErr != nil {
			return CanonicalTail{}, closeErr
		}
		if pageCount < liveMessageReadPageSize {
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return CanonicalTail{}, err
	}
	tail.Complete = true
	return tail, nil
}

// ReadLegacySessionProjection reconstructs read-only legacy messages from the
// canonical orchestrator event log through throughSeq. A zero throughSeq means
// the last legacy sequence for this session (the pinned cutover sequence for a
// journaled session). Events without canonical message/turn identity are
// returned as explicit gaps and make the projection incomplete.
func (s *SQLiteStore) ReadLegacySessionProjection(ctx context.Context, sessionID modulecore.SessionID, ownerID string, throughSeq modulecore.EventSeq) (LegacyProjection, error) {
	if err := s.ensureOpen(); err != nil {
		return LegacyProjection{}, err
	}
	if ctx == nil {
		return LegacyProjection{}, errors.New("legacy projection context is required")
	}
	if err := sessionID.Validate(); err != nil {
		return LegacyProjection{}, fmt.Errorf("session_id: %w", err)
	}
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) || throughSeq < 0 {
		return LegacyProjection{}, errors.New("owner_id and non-negative through_seq are required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LegacyProjection{}, err
	}
	defer tx.Rollback()
	state, journaled, err := readSessionStateTx(ctx, tx, sessionID, ownerID)
	if err != nil {
		return LegacyProjection{}, err
	}
	if journaled && state.CutoverSeq == 0 {
		if throughSeq != 0 {
			return LegacyProjection{}, errors.New("session has no legacy cutover boundary")
		}
		if err := tx.Commit(); err != nil {
			return LegacyProjection{}, err
		}
		return LegacyProjection{Complete: true, Messages: []LegacyMessage{}, Gaps: []LegacyProjectionGap{}}, nil
	}
	var latest modulecore.EventSeq
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM event_envelope`).Scan(&latest); err != nil {
		return LegacyProjection{}, err
	}
	if throughSeq == 0 {
		if journaled {
			throughSeq = state.CutoverSeq
		} else {
			if err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(MAX(event_seq), 0) FROM event_envelope
				WHERE json_extract(envelope_json, '$.session_id') = ?`, string(sessionID)).Scan(&throughSeq); err != nil {
				return LegacyProjection{}, err
			}
		}
	}
	if throughSeq > latest || (journaled && throughSeq > state.CutoverSeq) {
		return LegacyProjection{}, errors.New("legacy projection boundary exceeds the pinned legacy snapshot")
	}
	if journaled {
		if _, err := verifyLegacySessionOwnerThrough(ctx, tx, sessionID, ownerID, state.CutoverSeq); err != nil {
			return LegacyProjection{}, err
		}
	} else if _, err := verifyLegacySessionOwnerThrough(ctx, tx, sessionID, ownerID, throughSeq); err != nil {
		return LegacyProjection{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT event_id, event_seq, trace_id, schema_version, event_type, component_id,
		       occurred_at, envelope_json
		FROM event_envelope
		WHERE component_id = 'orchestrator'
		  AND json_extract(envelope_json, '$.session_id') = ? AND event_seq <= ?
		ORDER BY event_seq ASC`, string(sessionID), int64(throughSeq))
	if err != nil {
		return LegacyProjection{}, err
	}
	projection := LegacyProjection{
		AppliedThrough: throughSeq,
		Complete:       true,
		Messages:       make([]LegacyMessage, 0),
		Gaps:           make([]LegacyProjectionGap, 0),
	}
	messageOrder := make(map[modulecore.TurnID]int64)
	for rows.Next() {
		var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, payload string
		var eventSeq int64
		if err := rows.Scan(&eventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload); err != nil {
			_ = rows.Close()
			return LegacyProjection{}, err
		}
		event, err := decodeStoredEnvelope(eventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, payload)
		if err != nil {
			_ = rows.Close()
			return LegacyProjection{}, err
		}
		message, gap, isMessage := projectLegacyMessage(event, ownerID)
		if !isMessage {
			continue
		}
		if gap != "" {
			projection.Complete = false
			projection.Gaps = append(projection.Gaps, LegacyProjectionGap{EventSeq: event.EventSeq, EventID: event.EventID, Reason: gap})
			continue
		}
		messageOrder[message.Index.TurnID]++
		message.MessageOrder = messageOrder[message.Index.TurnID]
		projection.Messages = append(projection.Messages, message)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return LegacyProjection{}, rowsErr
	}
	if closeErr != nil {
		return LegacyProjection{}, closeErr
	}
	if err := tx.Commit(); err != nil {
		return LegacyProjection{}, err
	}
	return projection, nil
}

// ReadPendingAcceptedTurns returns accepted and awaiting-readback turns for the
// authorized owner in admission order. It is a complete read, with no hidden
// row limit, so restart recovery cannot silently miss work.
func (s *SQLiteStore) ReadPendingAcceptedTurns(ctx context.Context, sessionID modulecore.SessionID, ownerID string) ([]AcceptedTurn, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, errors.New("recoverable turn context is required")
	}
	if err := sessionID.Validate(); err != nil {
		return nil, fmt.Errorf("session_id: %w", err)
	}
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
		return nil, errors.New("owner_id is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, found, err := readSessionStateTx(ctx, tx, sessionID, ownerID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrConversationSessionConflict
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT t.turn_id, t.session_id, t.owner_id, t.actor_id, t.trigger_kind, t.client_submission_id, t.accept_receipt_id,
		       t.input_message_id, t.admission_seq, t.state, t.generation_epoch,
		       e.event_id, e.event_seq, e.trace_id, e.schema_version, e.event_type, e.component_id,
		       e.occurred_at, e.envelope_json, m.message_id, m.session_id, m.turn_id, m.from_actor,
		       m.to_kind, m.to_actor, m.kind, m.reply_to_message_id, m.visibility
		FROM live_conversation_turn t
		JOIN live_message_index m ON m.message_id = t.input_message_id
		JOIN event_envelope e ON e.event_id = m.event_id
		WHERE t.session_id = ? AND t.owner_id = ? AND t.state IN ('accepted', 'awaiting_readback')
		ORDER BY t.admission_seq ASC`, string(sessionID), ownerID,
	)
	if err != nil {
		return nil, err
	}
	turns := make([]AcceptedTurn, 0)
	for rows.Next() {
		var turn AcceptedTurn
		var storedOwner, clientSubmissionID, receiptID, inputMessageID, storedState string
		var admissionSeq, generationEpoch, eventSeq int64
		var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, payload string
		var message MessageIndex
		var messageID, messageSessionID, messageTurnID, fromActor, toKind, toActor, kind, visibility string
		var replyTo sql.NullString
		if err := rows.Scan(
			&turn.TurnID, &turn.SessionID, &storedOwner, &turn.ActorID, &turn.TriggerKind, &clientSubmissionID, &receiptID,
			&inputMessageID, &admissionSeq, &storedState, &generationEpoch,
			&eventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload,
			&messageID, &messageSessionID, &messageTurnID, &fromActor, &toKind, &toActor, &kind, &replyTo, &visibility,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		event, err := decodeStoredEnvelope(eventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, payload)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		message = MessageIndex{
			MessageID:        modulecore.MessageID(messageID),
			EventID:          modulecore.EventID(eventID),
			SessionID:        modulecore.SessionID(messageSessionID),
			TurnID:           modulecore.TurnID(messageTurnID),
			FromActor:        fromActor,
			ToKind:           toKind,
			ToActor:          toActor,
			Kind:             kind,
			ReplyToMessageID: modulecore.MessageID(replyTo.String),
			Visibility:       visibility,
		}
		if storedOwner != ownerID || clientSubmissionID == "" || event.EventSeq != modulecore.EventSeq(admissionSeq) || event.SessionID != sessionID || event.TurnID != turn.TurnID || event.MessageID != modulecore.MessageID(inputMessageID) || message.FromActor != turn.ActorID {
			_ = rows.Close()
			return nil, errors.New("recoverable turn row does not match its accepted input event")
		}
		if err := validateRecoverableTurnActor(turn, receiptID, clientSubmissionID, event, message); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("validate recoverable input actor for turn %q: %w", turn.TurnID, err)
		}
		if err := validateMessageIndex(message, event, sessionID, turn.TurnID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("validate recoverable input message %q: %w", message.MessageID, err)
		}
		turn.OperationID = receiptID
		turn.ClientSubmissionID = clientSubmissionID
		turn.InputMessageID = modulecore.MessageID(inputMessageID)
		turn.AdmissionSeq = modulecore.EventSeq(admissionSeq)
		turn.GenerationEpoch = generationEpoch
		turn.State = storedState
		turn.InputEvent = event
		turns = append(turns, turn)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return nil, rowsErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return turns, nil
}

// validateRecoverableTurnActor preserves the ordinary user-input boundary
// while allowing only the dedicated, trusted-agent report event through the
// same durable turn recovery path.
func validateRecoverableTurnActor(turn AcceptedTurn, operationID, submissionID string, event modulecore.EventEnvelope, message MessageIndex) error {
	if turn.TriggerKind != "report_reply" {
		if event.ActorKind != "" || event.ActorID != "" {
			return errors.New("non-report accepted input cannot carry execution actor identity")
		}
		return nil
	}

	reportID := payloadString(event.Payload, "report_id")
	report, reportOK := event.Payload["report"].(map[string]any)
	if !reportOK || reportID == "" || reportID != string(event.ResponseID) ||
		operationID != reportAcceptanceOperationID(modulecore.ResponseID(reportID)) ||
		submissionID != reportSubmissionID(modulecore.ResponseID(reportID)) ||
		turn.ActorID == "" || event.ActorKind != "agent" || event.ActorID != turn.ActorID ||
		event.EventType != "conversation.agent_report" || event.ComponentID != "conversation_runtime" ||
		event.RequestID == "" || event.TaskID == "" || event.RunID == "" ||
		event.CausationEventID != "" || len(event.DependencyEventIDs) != 0 ||
		payloadString(event.Payload, "kind") != "agent_report" ||
		payloadString(event.Payload, "visibility") != "private" ||
		payloadString(event.Payload, "from") != turn.ActorID ||
		payloadString(event.Payload, "source_actor_id") != turn.ActorID ||
		payloadString(event.Payload, "trusted_sender") != turn.ActorID ||
		payloadString(event.Payload, "to") != "mio" ||
		payloadString(event.Payload, "origin_trace_id") == "" ||
		payloadString(event.Payload, "origin_trace_id") == string(event.TraceID) ||
		payloadString(event.Payload, "origin_turn_id") == "" ||
		payloadString(event.Payload, "origin_message_id") == "" ||
		payloadString(event.Payload, "delegation_id") != string(event.RequestID) ||
		payloadString(event.Payload, "work_task_id") != string(event.TaskID) ||
		payloadString(event.Payload, "work_run_id") != string(event.RunID) ||
		payloadString(event.Payload, "text") == "" ||
		hasReportBodySender(report) ||
		payloadString(report, "report_id") != reportID ||
		message.FromActor != turn.ActorID || message.ToKind != "agent" || message.ToActor != "mio" ||
		message.Kind != "agent_report" || message.Visibility != "private" || message.ReplyToMessageID != "" {
		return errors.New("report reply does not match the dedicated private agent report contract")
	}
	if err := event.ResponseID.Validate(); err != nil {
		return fmt.Errorf("report response_id: %w", err)
	}
	if err := event.RequestID.Validate(); err != nil {
		return fmt.Errorf("report delegation_id: %w", err)
	}
	if err := event.TaskID.Validate(); err != nil {
		return fmt.Errorf("report task_id: %w", err)
	}
	if err := event.RunID.Validate(); err != nil {
		return fmt.Errorf("report run_id: %w", err)
	}
	return nil
}

func hasReportBodySender(report map[string]any) bool {
	_, present := report["sender"]
	return present
}

// AdvanceProjectionWatermark performs an owner-checked compare-and-swap after
// the caller has successfully applied every session projection through
// appliedThrough. It never skips a stale concurrent watermark or advances
// beyond the canonical event snapshot.
func (s *SQLiteStore) AdvanceProjectionWatermark(ctx context.Context, sessionID modulecore.SessionID, ownerID string, expected, appliedThrough modulecore.EventSeq) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("projection watermark context is required")
	}
	if err := sessionID.Validate(); err != nil {
		return fmt.Errorf("session_id: %w", err)
	}
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) || expected < 0 || appliedThrough < 0 {
		return errors.New("owner_id and non-negative projection watermarks are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	state, found, err := readSessionStateTx(ctx, tx, sessionID, ownerID)
	if err != nil {
		return err
	}
	if !found {
		return ErrConversationSessionConflict
	}
	if state.ProjectionWatermark != expected || appliedThrough < expected || appliedThrough < state.CutoverSeq {
		return ErrProjectionWatermarkConflict
	}
	var latest modulecore.EventSeq
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM event_envelope`).Scan(&latest); err != nil {
		return err
	}
	if appliedThrough > latest {
		return ErrProjectionWatermarkConflict
	}
	if appliedThrough > expected {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM event_envelope WHERE event_seq = ?`, int64(appliedThrough)).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrProjectionWatermarkConflict
			}
			return err
		}
	}
	if appliedThrough == expected {
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_conversation_session SET projection_watermark = ?
		WHERE session_id = ? AND owner_id = ? AND projection_watermark = ?`,
		int64(appliedThrough), string(sessionID), ownerID, int64(expected),
	)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrProjectionWatermarkConflict
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func readSessionStateTx(ctx context.Context, tx *sql.Tx, sessionID modulecore.SessionID, expectedOwner string) (SessionState, bool, error) {
	var state SessionState
	var cutoverSeq, admissionSeq, projectionWatermark, epoch int64
	err := tx.QueryRowContext(ctx, `
		SELECT session_id, owner_id, runtime_version, cutover_seq, admission_seq, projection_watermark, epoch
		FROM live_conversation_session WHERE session_id = ?`, string(sessionID),
	).Scan(&state.SessionID, &state.OwnerID, &state.RuntimeVersion, &cutoverSeq, &admissionSeq, &projectionWatermark, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionState{}, false, nil
	}
	if err != nil {
		return SessionState{}, false, err
	}
	if expectedOwner != "" && state.OwnerID != expectedOwner {
		return SessionState{}, false, ErrConversationSessionConflict
	}
	if cutoverSeq < 0 || admissionSeq < 0 || projectionWatermark < 0 || epoch <= 0 || strings.TrimSpace(state.RuntimeVersion) == "" {
		return SessionState{}, false, errors.New("stored conversation session state is invalid")
	}
	state.CutoverSeq = modulecore.EventSeq(cutoverSeq)
	state.AdmissionSeq = modulecore.EventSeq(admissionSeq)
	state.ProjectionWatermark = modulecore.EventSeq(projectionWatermark)
	state.GenerationEpoch = epoch
	return state, true, nil
}

func ensureNoUnfinishedLiveTurns(ctx context.Context, tx *sql.Tx, sessionID modulecore.SessionID) error {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM live_conversation_turn
		WHERE session_id = ? AND state IN ('accepted', 'awaiting_readback')`, string(sessionID),
	).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrConversationSessionConflict
	}
	return nil
}

func verifyLegacySessionOwner(ctx context.Context, tx *sql.Tx, sessionID modulecore.SessionID, ownerID string) (modulecore.EventSeq, error) {
	var through sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT MAX(event_seq) FROM event_envelope
		WHERE json_extract(envelope_json, '$.session_id') = ?`, string(sessionID),
	).Scan(&through); err != nil {
		return 0, err
	}
	if !through.Valid || through.Int64 <= 0 {
		return 0, ErrLegacySessionBoundaryConflict
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT json_extract(envelope_json, '$.actor_kind'), json_extract(envelope_json, '$.actor_id')
		FROM event_envelope
		WHERE json_extract(envelope_json, '$.session_id') = ?`, string(sessionID),
	)
	if err != nil {
		return 0, err
	}
	ownerSeen := false
	for rows.Next() {
		var actorKind, actorID sql.NullString
		if err := rows.Scan(&actorKind, &actorID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if actorKind.Valid && actorKind.String == "user" {
			if !actorID.Valid || actorID.String != ownerID {
				_ = rows.Close()
				return 0, ErrConversationSessionConflict
			}
			ownerSeen = true
		}
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return 0, rowsErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if !ownerSeen {
		return 0, ErrConversationSessionConflict
	}
	return modulecore.EventSeq(through.Int64), nil
}

func verifyLegacySessionOwnerThrough(ctx context.Context, tx *sql.Tx, sessionID modulecore.SessionID, ownerID string, through modulecore.EventSeq) (modulecore.EventSeq, error) {
	if through <= 0 {
		return 0, ErrLegacySessionBoundaryConflict
	}
	var sessionThrough sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT MAX(event_seq) FROM event_envelope
		WHERE json_extract(envelope_json, '$.session_id') = ? AND event_seq <= ?`, string(sessionID), int64(through),
	).Scan(&sessionThrough); err != nil {
		return 0, err
	}
	if !sessionThrough.Valid || sessionThrough.Int64 <= 0 {
		return 0, ErrLegacySessionBoundaryConflict
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT json_extract(envelope_json, '$.actor_kind'), json_extract(envelope_json, '$.actor_id')
		FROM event_envelope
		WHERE json_extract(envelope_json, '$.session_id') = ? AND event_seq <= ?`, string(sessionID), int64(through))
	if err != nil {
		return 0, err
	}
	ownerSeen := false
	for rows.Next() {
		var actorKind, actorID sql.NullString
		if err := rows.Scan(&actorKind, &actorID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if actorKind.Valid && actorKind.String == "user" {
			if !actorID.Valid || actorID.String != ownerID {
				_ = rows.Close()
				return 0, ErrConversationSessionConflict
			}
			ownerSeen = true
		}
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return 0, rowsErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if !ownerSeen {
		return 0, ErrConversationSessionConflict
	}
	return modulecore.EventSeq(sessionThrough.Int64), nil
}

func projectLegacyMessage(event modulecore.EventEnvelope, ownerID string) (LegacyMessage, string, bool) {
	kind := ""
	switch event.EventType {
	case "conversation.user_message", "message.received":
		kind = "user_message"
	case "agent.response":
		kind = "assistant_message"
	case "conversation.delegation_notice":
		kind = "delegation_notice"
	default:
		if event.MessageID != "" || payloadText(event.Payload, "content") != "" || payloadText(event.Payload, "text") != "" {
			return LegacyMessage{}, fmt.Sprintf("unsupported legacy message event type %q", event.EventType), true
		}
		return LegacyMessage{}, "", false
	}
	reason := ""
	if event.MessageID == "" {
		reason = "canonical message_id is missing"
	}
	if event.TurnID == "" {
		if reason != "" {
			reason += "; "
		}
		reason += "canonical turn_id is missing"
	}
	from := payloadString(event.Payload, "source_actor_id")
	if from == "" && event.ActorKind == "user" {
		from = strings.TrimSpace(event.ActorID)
	}
	if from == "" {
		from = payloadString(event.Payload, "from")
	}
	displayFrom := payloadString(event.Payload, "from")
	if event.EventType == "conversation.user_message" || event.EventType == "message.received" {
		if displayFrom == "" {
			displayFrom = "user"
		}
		if displayFrom != "user" {
			if reason != "" {
				reason += "; "
			}
			reason += "legacy user message has an invalid display source"
		}
	} else if displayFrom == "" {
		displayFrom = from
	}
	to := payloadString(event.Payload, "to")
	text := payloadText(event.Payload, "content")
	if text == "" {
		text = payloadText(event.Payload, "text")
	}
	if from == "" || displayFrom == "" || to == "" || strings.TrimSpace(text) == "" {
		if reason != "" {
			reason += "; "
		}
		reason += "legacy message source, route, or content is missing"
	}
	visibility := payloadString(event.Payload, "visibility")
	if visibility == "" {
		visibility = "public"
	}
	if visibility != "public" && visibility != "private" {
		if reason != "" {
			reason += "; "
		}
		reason += "legacy message visibility is invalid"
	}
	toKind, toActor := "agent", to
	if strings.EqualFold(to, "user") {
		toKind, toActor = "user", ownerID
	}
	message := LegacyMessage{
		Index: MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: event.SessionID, TurnID: event.TurnID,
			FromActor: from, ToKind: toKind, ToActor: toActor, Kind: kind, Visibility: visibility,
		},
		EventSeq: event.EventSeq, Text: text, TurnOrder: legacyInteger(event.Payload["turn_index"]),
	}
	return message, reason, true
}

func legacyInteger(value any) int64 {
	switch number := value.(type) {
	case int:
		return int64(number)
	case int64:
		return number
	case float64:
		if number == float64(int64(number)) {
			return int64(number)
		}
	case json.Number:
		parsed, err := number.Int64()
		if err == nil {
			return parsed
		}
	}
	return 0
}

func payloadText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func validateExistingCutoverMarker(ctx context.Context, tx *sql.Tx, state SessionState, legacyThrough modulecore.EventSeq) error {
	var storedEventID, traceID, schemaVersion, eventType, componentID, occurredAt, payload string
	var eventSeq int64
	err := tx.QueryRowContext(ctx, `
		SELECT event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json
		FROM event_envelope WHERE event_seq = ?`, int64(state.CutoverSeq),
	).Scan(&storedEventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &payload)
	if err != nil {
		return ErrConversationSessionConflict
	}
	marker, err := decodeStoredEnvelope(storedEventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, payload)
	if err != nil || marker.EventType != "conversation.session.cutover" || marker.ComponentID != "conversation_journal" || marker.SessionID != state.SessionID {
		return ErrConversationSessionConflict
	}
	if state.CutoverSeq <= legacyThrough ||
		marker.Payload["owner_id"] != state.OwnerID ||
		marker.Payload["runtime_version"] != state.RuntimeVersion ||
		marker.Payload["source_runtime_version"] != "v1" ||
		legacyInteger(marker.Payload["generation_epoch"]) != state.GenerationEpoch ||
		legacyInteger(marker.Payload["legacy_through_seq"]) != int64(legacyThrough) {
		return ErrLegacySessionBoundaryConflict
	}
	return nil
}
