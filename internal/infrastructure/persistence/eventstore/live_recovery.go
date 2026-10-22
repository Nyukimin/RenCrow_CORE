package eventstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ReadPendingAcceptedV2SessionPage returns a keyset page of owner-scoped v2
// sessions that still have an accepted turn. The caller can resume each
// session through ReadPendingAcceptedTurns, which remains the canonical
// accepted-turn reader.
func (s *SQLiteStore) ReadPendingAcceptedV2SessionPage(
	ctx context.Context,
	ownerID string,
	after modulecore.SessionID,
	limit int,
) ([]modulecore.SessionID, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, false, err
	}
	if ctx == nil || strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) || limit < 1 || limit > maxListLimit {
		return nil, false, errors.New("invalid pending accepted v2 session query")
	}
	if after != "" {
		if err := after.Validate(); err != nil {
			return nil, false, fmt.Errorf("after_session_id: %w", err)
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT t.session_id
		FROM live_conversation_turn t
		JOIN live_conversation_session s ON s.session_id = t.session_id
		WHERE t.owner_id = ? AND s.owner_id = ? AND s.runtime_version = 'v2'
		  AND t.state = 'accepted' AND t.session_id > ?
		ORDER BY t.session_id ASC
		LIMIT ?`, ownerID, ownerID, string(after), limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	sessions := make([]modulecore.SessionID, 0, limit)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		sessionID := modulecore.SessionID(raw)
		if err := sessionID.Validate(); err != nil || sessionID <= after {
			return nil, false, fmt.Errorf("pending accepted v2 session row is invalid: %q", raw)
		}
		if len(sessions) == limit {
			if err := rows.Err(); err != nil {
				return nil, false, err
			}
			if err := rows.Close(); err != nil {
				return nil, false, err
			}
			return sessions, true, nil
		}
		sessions = append(sessions, sessionID)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return sessions, false, nil
}
