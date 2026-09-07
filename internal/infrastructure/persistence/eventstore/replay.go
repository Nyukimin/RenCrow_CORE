package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ReadComponentPage reads an ascending, bounded page from a fixed canonical
// sequence window. A zero through selects the current component watermark;
// callers reuse the returned watermark so concurrent appends do not extend a replay.
func (s *SQLiteStore) ReadComponentPage(ctx context.Context, component string, after, through modulecore.EventSeq, limit int) ([]modulecore.EventEnvelope, modulecore.EventSeq, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, 0, err
	}
	if ctx == nil || strings.TrimSpace(component) == "" || component != strings.TrimSpace(component) || after < 0 || through < 0 || limit <= 0 || limit > maxListLimit {
		return nil, 0, errors.New("invalid event replay query")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var latest modulecore.EventSeq
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM event_envelope WHERE component_id = ?`, component).Scan(&latest); err != nil {
		return nil, 0, err
	}
	if through == 0 {
		through = latest
	}
	if after > through || through > latest {
		return nil, 0, errors.New("event replay cursor is outside the canonical window")
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope WHERE component_id = ? AND event_seq > ? AND event_seq <= ? ORDER BY event_seq ASC LIMIT ?`, component, int64(after), int64(through), limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events := make([]modulecore.EventEnvelope, 0, limit)
	for rows.Next() {
		var eventID, traceID, schema, eventType, storedComponent, occurredAt, payload string
		var sequence int64
		if err := rows.Scan(&eventID, &sequence, &traceID, &schema, &eventType, &storedComponent, &occurredAt, &payload); err != nil {
			return nil, 0, err
		}
		event, err := decodeStoredEnvelope(eventID, sequence, traceID, schema, eventType, storedComponent, occurredAt, payload)
		if err != nil {
			return nil, 0, err
		}
		if event.ComponentID != component || event.EventSeq <= after || event.EventSeq > through {
			return nil, 0, errors.New("event replay row violates the canonical window")
		}
		events = append(events, event)
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
	return events, through, nil
}
