package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// SessionPublicMessagesPage reads only owner-authorized public messages from
// one version-pinned v2 session. The message index is the visibility and
// routing authority; the event envelope remains the body source.
func (s *SQLiteStore) ReadSessionPublicMessagesPage(
	ctx context.Context,
	sessionID modulecore.SessionID,
	ownerID, actorID string,
	after, through modulecore.EventSeq,
	limit int,
) ([]modulecore.EventEnvelope, modulecore.EventSeq, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, 0, err
	}
	if ctx == nil || sessionID.Validate() != nil || strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) ||
		strings.TrimSpace(actorID) == "" || actorID != strings.TrimSpace(actorID) || after < 0 || through < 0 || limit <= 0 || limit > maxListLimit {
		return nil, 0, errors.New("invalid session public replay query")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	var runtimeVersion string
	var cutoverSeq modulecore.EventSeq
	err = tx.QueryRowContext(ctx, `
		SELECT runtime_version, cutover_seq
		FROM live_conversation_session
		WHERE session_id = ? AND owner_id = ?`, string(sessionID), ownerID,
	).Scan(&runtimeVersion, &cutoverSeq)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (runtimeVersion != "v2" || cutoverSeq < 0)) {
		return nil, 0, errors.New("owner-checked v2 session replay is unavailable")
	}
	if err != nil {
		return nil, 0, err
	}
	if after < cutoverSeq {
		return nil, 0, errors.New("session public replay cursor precedes v2 cutover")
	}

	var latest modulecore.EventSeq
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(e.event_seq), ?)
		FROM live_conversation_session s
		JOIN live_message_index m ON m.session_id = s.session_id
		JOIN event_envelope e ON e.event_id = m.event_id
		WHERE s.session_id = ? AND s.owner_id = ? AND s.runtime_version = 'v2'
		  AND s.cutover_seq = ?
		  AND m.visibility = 'public'
		  AND ((m.from_actor = ? AND m.to_kind = 'agent') OR (m.to_kind = 'user' AND m.to_actor = ?))
		  AND e.event_seq > s.cutover_seq
		  AND (json_extract(e.envelope_json, '$.payload.visibility') IS NULL OR json_extract(e.envelope_json, '$.payload.visibility') = 'public')`,
		int64(cutoverSeq), string(sessionID), ownerID, int64(cutoverSeq), actorID, ownerID,
	).Scan(&latest)
	if err != nil {
		return nil, 0, err
	}
	if through == 0 {
		through = latest
	}
	if after > through || through > latest {
		return nil, 0, errors.New("session public replay cursor is outside the canonical window")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT e.event_id,e.event_seq,e.trace_id,e.schema_version,e.event_type,e.component_id,e.occurred_at,e.envelope_json,
		       m.message_id,m.session_id,m.turn_id,m.from_actor,m.to_kind,m.to_actor,m.kind,m.reply_to_message_id,m.visibility
		FROM live_conversation_session s
		JOIN live_message_index m ON m.session_id = s.session_id
		JOIN event_envelope e ON e.event_id = m.event_id
		WHERE s.session_id = ? AND s.owner_id = ? AND s.runtime_version = 'v2'
		  AND s.cutover_seq = ?
		  AND m.visibility = 'public'
		  AND ((m.from_actor = ? AND m.to_kind = 'agent') OR (m.to_kind = 'user' AND m.to_actor = ?))
		  AND e.event_seq > s.cutover_seq AND e.event_seq > ? AND e.event_seq <= ?
		  AND (json_extract(e.envelope_json, '$.payload.visibility') IS NULL OR json_extract(e.envelope_json, '$.payload.visibility') = 'public')
		ORDER BY e.event_seq ASC LIMIT ?`,
		string(sessionID), ownerID, int64(cutoverSeq), actorID, ownerID, int64(after), int64(through), limit,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]modulecore.EventEnvelope, 0, limit)
	for rows.Next() {
		var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON string
		var sequence int64
		var messageID, storedSessionID, turnID, fromActor, toKind, toActor, kind, visibility string
		var replyToMessageID sql.NullString
		if err := rows.Scan(&eventID, &sequence, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &envelopeJSON,
			&messageID, &storedSessionID, &turnID, &fromActor, &toKind, &toActor, &kind, &replyToMessageID, &visibility); err != nil {
			return nil, 0, err
		}
		event, err := decodeStoredEnvelope(eventID, sequence, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON)
		if err != nil {
			return nil, 0, err
		}
		if event.EventSeq <= after || event.EventSeq > through || event.EventID != modulecore.EventID(eventID) ||
			storedSessionID != string(sessionID) || event.SessionID != sessionID || event.MessageID != modulecore.MessageID(messageID) || event.TurnID != modulecore.TurnID(turnID) ||
			visibility != "public" || strings.TrimSpace(fromActor) == "" || strings.TrimSpace(kind) == "" ||
			!((fromActor == actorID && toKind == "agent") || (toKind == "user" && toActor == ownerID)) {
			return nil, 0, fmt.Errorf("session public replay row violates its owner, visibility, or sequence boundary")
		}
		if payloadVisibility, ok := event.Payload["visibility"].(string); ok && payloadVisibility != "public" {
			return nil, 0, errors.New("session public replay envelope is not public")
		}
		payloadSource, _ := event.Payload["source_actor_id"].(string)
		payloadTo, _ := event.Payload["to"].(string)
		payloadReplyTo, _ := event.Payload["reply_to_message_id"].(string)
		if payloadSource != fromActor || payloadReplyTo != replyToMessageID.String || (fromActor == actorID && payloadTo != "mio") || (fromActor != actorID && payloadTo != "user") {
			return nil, 0, errors.New("session public replay envelope does not match its indexed audience")
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return result, through, nil
}

// ReadSessionPublicMessageByEventID validates one live canonical event against
// the owner/session-scoped public message index before it reaches SSE.
func (s *SQLiteStore) ReadSessionPublicMessageByEventID(
	ctx context.Context,
	sessionID modulecore.SessionID,
	ownerID, actorID string,
	eventID modulecore.EventID,
	eventSeq modulecore.EventSeq,
) (modulecore.EventEnvelope, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	if ctx == nil || sessionID.Validate() != nil || eventID.Validate() != nil || strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) ||
		strings.TrimSpace(actorID) == "" || actorID != strings.TrimSpace(actorID) || eventSeq <= 0 {
		return modulecore.EventEnvelope{}, false, errors.New("invalid session public event query")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	defer tx.Rollback()
	var runtimeVersion string
	var cutoverSeq modulecore.EventSeq
	err = tx.QueryRowContext(ctx, `
		SELECT runtime_version, cutover_seq FROM live_conversation_session
		WHERE session_id = ? AND owner_id = ?`, string(sessionID), ownerID,
	).Scan(&runtimeVersion, &cutoverSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return modulecore.EventEnvelope{}, false, nil
	}
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	if runtimeVersion != "v2" || cutoverSeq < 0 || eventSeq <= cutoverSeq {
		return modulecore.EventEnvelope{}, false, nil
	}
	var rawEventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON string
	var sequence int64
	var messageID, storedSessionID, turnID, fromActor, toKind, toActor, kind, visibility string
	var replyToMessageID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT e.event_id,e.event_seq,e.trace_id,e.schema_version,e.event_type,e.component_id,e.occurred_at,e.envelope_json,
		       m.message_id,m.session_id,m.turn_id,m.from_actor,m.to_kind,m.to_actor,m.kind,m.reply_to_message_id,m.visibility
		FROM live_conversation_session s
		JOIN live_message_index m ON m.session_id = s.session_id
		JOIN event_envelope e ON e.event_id = m.event_id
		WHERE s.session_id = ? AND s.owner_id = ? AND s.runtime_version = 'v2'
		  AND s.cutover_seq = ? AND m.visibility = 'public'
		  AND ((m.from_actor = ? AND m.to_kind = 'agent') OR (m.to_kind = 'user' AND m.to_actor = ?))
		  AND e.event_id = ? AND e.event_seq = ?
		  AND (json_extract(e.envelope_json, '$.payload.visibility') IS NULL OR json_extract(e.envelope_json, '$.payload.visibility') = 'public')`,
		string(sessionID), ownerID, int64(cutoverSeq), actorID, ownerID, string(eventID), int64(eventSeq),
	).Scan(&rawEventID, &sequence, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &envelopeJSON,
		&messageID, &storedSessionID, &turnID, &fromActor, &toKind, &toActor, &kind, &replyToMessageID, &visibility)
	if errors.Is(err, sql.ErrNoRows) {
		return modulecore.EventEnvelope{}, false, nil
	}
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	envelope, err := decodeStoredEnvelope(rawEventID, sequence, traceID, schemaVersion, eventType, componentID, occurredAt, envelopeJSON)
	if err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	if envelope.EventID != eventID || envelope.EventSeq != eventSeq || envelope.SessionID != sessionID || envelope.ComponentID != "orchestrator" ||
		envelope.MessageID != modulecore.MessageID(messageID) || envelope.TurnID != modulecore.TurnID(turnID) || visibility != "public" || kind == "" ||
		!((fromActor == actorID && toKind == "agent") || (toKind == "user" && toActor == ownerID)) {
		return modulecore.EventEnvelope{}, false, errors.New("session public event row violates its owner, visibility, or sequence boundary")
	}
	if payloadVisibility, ok := envelope.Payload["visibility"].(string); ok && payloadVisibility != "public" {
		return modulecore.EventEnvelope{}, false, errors.New("session public event envelope is not public")
	}
	payloadSource, _ := envelope.Payload["source_actor_id"].(string)
	payloadTo, _ := envelope.Payload["to"].(string)
	payloadReplyTo, _ := envelope.Payload["reply_to_message_id"].(string)
	if payloadSource != fromActor || payloadReplyTo != replyToMessageID.String || (fromActor == actorID && payloadTo != "mio") || (fromActor != actorID && payloadTo != "user") {
		return modulecore.EventEnvelope{}, false, errors.New("session public event envelope does not match its indexed audience")
	}
	if err := tx.Commit(); err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	return envelope, true, nil
}
