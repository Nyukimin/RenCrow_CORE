package eventstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrSessionProtocolProjectionUnavailable = errors.New("session protocol projection is unavailable")

type sessionProtocolPin struct {
	ownerID        string
	runtimeVersion string
}

// hydrateSessionProtocolProjection rebuilds the RAM index from the canonical
// EventStore table before the store is made available to runtime admission.
// A ready projection records both pins and the verified absence of a pin for
// every other session ID.
func (s *SQLiteStore) hydrateSessionProtocolProjection(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errStoreClosed
	}
	if ctx == nil {
		return errors.New("session protocol projection context is required")
	}
	db := s.sessionStateDB
	if db == nil {
		db = s.db
	}
	rows, err := db.QueryContext(ctx, `SELECT session_id, owner_id, runtime_version FROM live_conversation_session`)
	if err != nil {
		return err
	}
	pins := make(map[modulecore.SessionID]sessionProtocolPin)
	for rows.Next() {
		var sessionValue, ownerID, runtimeVersion string
		if err := rows.Scan(&sessionValue, &ownerID, &runtimeVersion); err != nil {
			_ = rows.Close()
			return err
		}
		sessionID := modulecore.SessionID(sessionValue)
		if err := sessionID.Validate(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("stored session protocol pin has invalid session_id: %w", err)
		}
		if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
			_ = rows.Close()
			return errors.New("stored session protocol pin has invalid owner_id")
		}
		if _, duplicate := pins[sessionID]; duplicate {
			_ = rows.Close()
			return errors.New("stored session protocol projection contains a duplicate session_id")
		}
		pins[sessionID] = sessionProtocolPin{ownerID: ownerID, runtimeVersion: runtimeVersion}
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	s.sessionProtocolProjectionMu.Lock()
	s.sessionProtocolPins = pins
	s.sessionProtocolProjectionReady = true
	s.sessionProtocolProjectionMu.Unlock()
	return nil
}

// SessionRuntimeVersion reads the owner-checked runtime pin from the warmed
// RAM projection. It never falls back to SQLite: a non-ready projection fails
// closed so a ready request cannot acquire a database read.
func (s *SQLiteStore) SessionRuntimeVersion(ctx context.Context, sessionID modulecore.SessionID, ownerID string) (string, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return "", false, err
	}
	if ctx == nil {
		return "", false, errors.New("session protocol lookup context is required")
	}
	if err := sessionID.Validate(); err != nil {
		return "", false, fmt.Errorf("session_id: %w", err)
	}
	if strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) {
		return "", false, errors.New("owner_id is required")
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	s.sessionProtocolProjectionMu.RLock()
	defer s.sessionProtocolProjectionMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if !s.sessionProtocolProjectionReady {
		return "", false, ErrSessionProtocolProjectionUnavailable
	}
	pin, found := s.sessionProtocolPins[sessionID]
	if !found {
		return "", false, nil
	}
	if pin.ownerID != ownerID {
		return "", false, ErrConversationSessionConflict
	}
	return pin.runtimeVersion, true, nil
}

func (s *SQLiteStore) recordSessionProtocolPin(sessionID modulecore.SessionID, ownerID, runtimeVersion string) {
	if s == nil {
		return
	}
	s.sessionProtocolProjectionMu.Lock()
	if s.sessionProtocolProjectionReady {
		if s.sessionProtocolPins == nil {
			s.sessionProtocolPins = make(map[modulecore.SessionID]sessionProtocolPin)
		}
		s.sessionProtocolPins[sessionID] = sessionProtocolPin{ownerID: ownerID, runtimeVersion: runtimeVersion}
	}
	s.sessionProtocolProjectionMu.Unlock()
}
