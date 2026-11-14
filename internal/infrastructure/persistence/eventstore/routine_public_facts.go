package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RoutinePublicResponseFact contains only canonical public response metadata.
// It deliberately has no field capable of carrying a message body.
type RoutinePublicResponseFact struct {
	EventID    string
	MessageID  string
	TurnID     string
	ActorID    string
	OccurredAt time.Time
}

// RoutineBoardPublicationFact contains metadata for a reply that was stored
// successfully in BOARD. It deliberately has no field capable of carrying a
// message body.
type RoutineBoardPublicationFact struct {
	EventID        string
	MessageID      string
	TurnID         string
	ActorID        string
	BoardMessageID string
	OccurredAt     time.Time
	StoredAt       time.Time
}

// ListRoutinePublicResponseFacts reads a bounded set of owner-scoped v2 public
// responses. Timestamps are parsed and date-filtered in Go because persisted
// RFC3339 offsets and fractional precision are not safe lexical boundaries.
func (s *SQLiteStore) ListRoutinePublicResponseFacts(
	ctx context.Context,
	actorID, ownerID string,
	from, through time.Time,
) ([]RoutinePublicResponseFact, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if ctx == nil || strings.TrimSpace(actorID) == "" || actorID != strings.TrimSpace(actorID) ||
		strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) ||
		from.IsZero() || through.IsZero() || !through.After(from) || through.Sub(from) > 25*time.Hour {
		return nil, errors.New("invalid routine public response fact scope")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// RFC3339 timestamps can use offsets that move their textual calendar date
	// by a day from UTC. This broad date filter narrows the SQL candidate set
	// before LIMIT; the exact instant range is checked after RFC3339Nano parsing.
	coarseFrom := from.UTC().Add(-24 * time.Hour).Format("2006-01-02")
	coarseThrough := through.UTC().Add(24 * time.Hour).Format("2006-01-02")
	rows, err := tx.QueryContext(ctx, `
		SELECT e.event_id,e.occurred_at,m.message_id,m.turn_id,m.from_actor
		FROM live_conversation_session s
		JOIN live_conversation_turn t ON t.session_id=s.session_id AND t.owner_id=s.owner_id
		JOIN live_message_index m ON m.session_id=s.session_id AND m.turn_id=t.turn_id
		JOIN event_envelope e ON e.event_id=m.event_id
		JOIN live_operation_receipt op ON op.operation_id=t.result_receipt_id
		WHERE s.owner_id=? AND s.runtime_version='v2' AND s.cutover_seq>=0
		  AND e.event_seq>s.cutover_seq
		  AND t.accept_receipt_id<>'' AND t.result_receipt_id<>''
		  AND op.operation_kind='commit_turn_bundle'
		  AND json_extract(op.result_json,'$.session_id')=s.session_id
		  AND json_extract(op.result_json,'$.turn_id')=t.turn_id
		  AND EXISTS (
			SELECT 1 FROM json_each(op.result_json,'$.events') committed
			WHERE json_extract(committed.value,'$.event_id')=e.event_id
		  )
		  AND m.from_actor=? AND m.to_kind='user' AND m.to_actor=? AND m.visibility='public'
		  AND e.component_id='orchestrator' AND e.event_type='agent.response'
		  AND json_extract(e.envelope_json,'$.session_id')=s.session_id
		  AND json_extract(e.envelope_json,'$.turn_id')=t.turn_id
		  AND json_extract(e.envelope_json,'$.message_id')=m.message_id
		  AND (json_extract(e.envelope_json,'$.payload.visibility') IS NULL OR json_extract(e.envelope_json,'$.payload.visibility')='public')
		  AND json_extract(e.envelope_json,'$.payload.source_actor_id')=m.from_actor
		  AND json_extract(e.envelope_json,'$.payload.from')=m.from_actor
		  AND json_extract(e.envelope_json,'$.payload.to')='user'
		  AND substr(e.occurred_at,1,10)>=? AND substr(e.occurred_at,1,10)<=?
		ORDER BY e.event_seq DESC LIMIT ?`, ownerID, actorID, ownerID, coarseFrom, coarseThrough, maxListLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]RoutinePublicResponseFact, 0)
	rowsRead := 0
	for rows.Next() {
		rowsRead++
		if rowsRead > maxListLimit {
			return nil, errors.New("routine public response fact query exceeds bounded history")
		}
		var row RoutinePublicResponseFact
		var occurredAt string
		if err := rows.Scan(&row.EventID, &occurredAt, &row.MessageID, &row.TurnID, &row.ActorID); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, fmt.Errorf("routine public response timestamp is invalid")
		}
		row.OccurredAt = parsed.UTC()
		if row.ActorID != actorID || row.EventID == "" || row.MessageID == "" || row.TurnID == "" {
			return nil, errors.New("routine public response metadata violates its scope")
		}
		if !row.OccurredAt.Before(from) && row.OccurredAt.Before(through) {
			result = append(result, row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].EventID < result[j].EventID
		}
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	return result, nil
}

// ListRoutineBoardPublicationFacts reads owner-scoped public external replies
// whose BOARD publication outcome is durably marked stored. StoredAt is the
// timestamp used for the requested range; the canonical response event time
// is returned separately as OccurredAt.
func (s *SQLiteStore) ListRoutineBoardPublicationFacts(
	ctx context.Context,
	actorID, ownerID string,
	from, through time.Time,
) ([]RoutineBoardPublicationFact, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if ctx == nil || strings.TrimSpace(actorID) == "" || actorID != strings.TrimSpace(actorID) ||
		strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) ||
		from.IsZero() || through.IsZero() || !through.After(from) || through.Sub(from) > 25*time.Hour {
		return nil, errors.New("invalid routine BOARD publication fact scope")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Stored timestamps are RFC3339 and may use offsets, so the SQL date range
	// is only a coarse candidate bound. Exact instant filtering follows parsing.
	coarseFrom := from.UTC().Add(-24 * time.Hour).Format("2006-01-02")
	coarseThrough := through.UTC().Add(24 * time.Hour).Format("2006-01-02")
	rows, err := tx.QueryContext(ctx, `
		SELECT e.event_id,e.occurred_at,m.message_id,m.turn_id,m.from_actor,o.board_message_id,o.stored_at
		FROM live_board_publication p
		JOIN live_work_outbox w ON w.work_id=p.work_id AND w.kind='board_publication' AND w.state='completed'
		JOIN live_message_index m ON m.message_id=p.response_message_id AND m.event_id=w.source_event_id AND m.session_id=w.session_id
		JOIN event_envelope e ON e.event_id=m.event_id
		JOIN live_conversation_session s ON s.session_id=m.session_id
		JOIN live_conversation_turn t ON t.turn_id=m.turn_id AND t.session_id=m.session_id AND t.owner_id=s.owner_id
		JOIN live_operation_receipt op ON op.operation_id=t.result_receipt_id AND op.operation_kind='commit_external_turn_bundle'
		JOIN live_board_publication_outcome o ON o.work_id=p.work_id AND o.owner_id=s.owner_id AND o.status='stored'
		JOIN live_board_publication_attempt a ON a.work_id=o.work_id AND a.attempt_ordinal=o.attempt_ordinal AND a.payload_sha256=o.payload_sha256
		WHERE s.owner_id=? AND o.owner_id=s.owner_id AND s.runtime_version='v2' AND s.cutover_seq>=0
		  AND e.event_seq>s.cutover_seq
		  AND t.trigger_kind='external_peer_message' AND t.accept_receipt_id<>'' AND t.result_receipt_id<>''
		  AND json_extract(op.result_json,'$.work_id')=w.work_id
		  AND EXISTS (
			SELECT 1 FROM json_each(op.result_json,'$.events') committed
			WHERE json_extract(committed.value,'$.event_id')=e.event_id
		  )
		  AND p.target_kind='reply' AND p.reply_to<>''
		  AND p.board_instance_id=o.journal_instance_id AND p.core_principal_id=o.principal_id
		  AND p.peer_principal_id=o.expected_peer_principal_id AND p.peer_principal_id=m.to_actor
		  AND p.channel_id=o.channel_id AND p.thread_id=o.board_thread_id AND p.reply_to=o.reply_to
		  AND o.sender_principal_id=p.core_principal_id
		  AND json_array_length(o.recipients_json)=1 AND json_extract(o.recipients_json,'$[0]')=p.peer_principal_id
		  AND m.from_actor=? AND m.to_kind='external_peer' AND m.kind='assistant_message' AND m.visibility='public'
		  AND e.component_id='conversation_runtime' AND e.event_type='agent.response'
		  AND json_extract(e.envelope_json,'$.session_id')=s.session_id
		  AND json_extract(e.envelope_json,'$.turn_id')=t.turn_id
		  AND json_extract(e.envelope_json,'$.message_id')=m.message_id
		  AND json_extract(e.envelope_json,'$.payload.kind')='reply'
		  AND json_extract(e.envelope_json,'$.payload.visibility')='public'
		  AND json_extract(e.envelope_json,'$.payload.source_actor_id')=m.from_actor
		  AND json_extract(e.envelope_json,'$.payload.from')=m.from_actor
		  AND json_extract(e.envelope_json,'$.payload.to')='external_peer'
		  AND substr(o.stored_at,1,10)>=? AND substr(o.stored_at,1,10)<=?
		ORDER BY e.event_seq DESC LIMIT ?`, ownerID, actorID, coarseFrom, coarseThrough, maxListLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]RoutineBoardPublicationFact, 0)
	rowsRead := 0
	for rows.Next() {
		rowsRead++
		if rowsRead > maxListLimit {
			return nil, errors.New("routine BOARD publication fact query exceeds bounded history")
		}
		var fact RoutineBoardPublicationFact
		var occurredAt, storedAt string
		if err := rows.Scan(&fact.EventID, &occurredAt, &fact.MessageID, &fact.TurnID, &fact.ActorID, &fact.BoardMessageID, &storedAt); err != nil {
			return nil, err
		}
		parsedOccurredAt, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, errors.New("routine BOARD response timestamp is invalid")
		}
		parsedStoredAt, err := time.Parse(time.RFC3339Nano, storedAt)
		if err != nil {
			return nil, errors.New("routine BOARD stored timestamp is invalid")
		}
		fact.OccurredAt = parsedOccurredAt.UTC()
		fact.StoredAt = parsedStoredAt.UTC()
		if fact.ActorID != actorID || fact.EventID == "" || fact.MessageID == "" || fact.TurnID == "" || fact.BoardMessageID == "" {
			return nil, errors.New("routine BOARD publication metadata violates its scope")
		}
		if !fact.StoredAt.Before(from) && fact.StoredAt.Before(through) {
			result = append(result, fact)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StoredAt.Equal(result[j].StoredAt) {
			return result[i].BoardMessageID < result[j].BoardMessageID
		}
		return result[i].StoredAt.Before(result[j].StoredAt)
	})
	return result, nil
}
