package l1sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	_ "modernc.org/sqlite"
)

func (s *L1SQLiteStore) SaveMessage(ctx context.Context, sessionID string, threadID int64, namespace string, msg domconv.Message, memoryState string) error {
	if err := validateL1MessageSaveInput(sessionID, threadID, msg); err != nil {
		return err
	}
	if namespace == "" {
		var err error
		namespace, err = BuildL1Namespace(NamespaceKindConversation, fmt.Sprintf("%d", threadID))
		if err != nil {
			return err
		}
	}
	if err := ValidateL1Namespace(namespace); err != nil {
		return err
	}
	if memoryState == "" {
		memoryState = MemoryStateObserved
	}
	if err := validateMemoryState(memoryState); err != nil {
		return err
	}
	layer := MemoryLayerL1
	now := time.Now().UTC()
	createdAt := msg.Timestamp
	if createdAt.IsZero() {
		createdAt = now
	}
	createdAt = createdAt.UTC()
	meta := msg.Meta
	if meta == nil {
		meta = map[string]interface{}{}
	}
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal l1 memory meta")
	if err != nil {
		return err
	}
	id := fmt.Sprintf("%s:%d:%d:%s", sessionID, threadID, createdAt.UnixNano(), msg.Speaker)
	event := L1MemoryEvent{
		ID:          id,
		Namespace:   namespace,
		SessionID:   sessionID,
		ThreadID:    threadID,
		Speaker:     msg.Speaker,
		Message:     msg.Msg,
		Meta:        meta,
		MemoryState: memoryState,
		Layer:       layer,
		Source:      "conversation",
		CreatedAt:   createdAt,
		UpdatedAt:   now,
	}
	if err := validateL1MemoryEvent(event); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	message = excluded.message,
	meta_json = excluded.meta_json,
	memory_state = excluded.memory_state,
	updated_at = excluded.updated_at
`, event.ID, event.Namespace, event.SessionID, event.ThreadID, string(event.Speaker), event.Message, metaJSON,
		event.MemoryState, event.Layer, event.Source, event.CreatedAt, event.UpdatedAt)
	if err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to save l1 memory event: %w", err))
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.message_saved", namespace, sessionID, threadID, map[string]interface{}{
		"memory_id":    id,
		"speaker":      string(msg.Speaker),
		"memory_state": memoryState,
		"layer":        layer,
	}, "conversation"); err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to append l1 message event log: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to commit l1 message transaction: %w", err))
	}
	return nil
}

func (s *L1SQLiteStore) UpdateMemoryState(ctx context.Context, id string, memoryState string) error {
	if id == "" {
		return errors.New("l1 memory event id is required")
	}
	if err := validateMemoryState(memoryState); err != nil {
		return err
	}
	var namespace string
	var sessionID string
	var threadID int64
	var previousState string
	if err := s.db.QueryRowContext(ctx, `
SELECT namespace, session_id, thread_id, memory_state
FROM l1_memory_event
WHERE id = ?
`, id).Scan(&namespace, &sessionID, &threadID, &previousState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return fmt.Errorf("failed to load l1 memory event before state update: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE l1_memory_event
SET memory_state = ?, updated_at = ?
WHERE id = ?
`, memoryState, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update l1 memory state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to inspect l1 memory state update: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	if _, err := s.AppendEvent(ctx, "memory.state_updated", namespace, sessionID, threadID, map[string]interface{}{
		"memory_id":      id,
		"previous_state": previousState,
		"memory_state":   memoryState,
	}, "memory"); err != nil {
		return fmt.Errorf("failed to append l1 memory state event log: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) PromoteMemoryToNamespace(ctx context.Context, id string, targetNamespace string, promotedBy string) (*L1MemoryEvent, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("l1 memory event id is required")
	}
	if err := ValidateL1Namespace(targetNamespace); err != nil {
		return nil, err
	}
	source, err := s.memoryByID(ctx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	meta := map[string]interface{}{}
	for k, v := range source.Meta {
		meta[k] = v
	}
	meta["promoted_from"] = source.ID
	meta["promoted_by"] = promotedBy
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal promoted l1 memory meta")
	if err != nil {
		return nil, err
	}
	promoted := &L1MemoryEvent{
		ID:          fmt.Sprintf("%s:%s:%d", targetNamespace, source.ID, now.UnixNano()),
		Namespace:   targetNamespace,
		SessionID:   source.SessionID,
		ThreadID:    source.ThreadID,
		Speaker:     source.Speaker,
		Message:     source.Message,
		Meta:        meta,
		MemoryState: MemoryStateConfirmed,
		Layer:       source.Layer,
		Source:      "promoter",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := validateL1MemoryEvent(*promoted); err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, promoted.ID, promoted.Namespace, promoted.SessionID, promoted.ThreadID, string(promoted.Speaker), promoted.Message, metaJSON,
		promoted.MemoryState, promoted.Layer, promoted.Source, promoted.CreatedAt, promoted.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to promote l1 memory: %w", err)
	}
	if _, err := s.AppendEvent(ctx, "memory.promoted", targetNamespace, source.SessionID, source.ThreadID, map[string]interface{}{
		"source_memory_id":   source.ID,
		"promoted_memory_id": promoted.ID,
		"promoted_by":        promotedBy,
		"memory_state":       promoted.MemoryState,
	}, "promoter"); err != nil {
		return nil, fmt.Errorf("failed to append l1 memory promoted event log: %w", err)
	}
	if err := s.archivePromotedMemory(ctx, *promoted); err != nil {
		return nil, err
	}
	return promoted, nil
}

func (s *L1SQLiteStore) RecentByNamespace(ctx context.Context, namespace string, limit int) ([]L1MemoryEvent, error) {
	if err := ValidateL1Namespace(namespace); err != nil {
		return nil, err
	}
	return s.recentL1MemoryEvents(ctx, "namespace = ?", "failed to query l1 memory events", limit, namespace)
}

func (s *L1SQLiteStore) memoryByID(ctx context.Context, id string) (*L1MemoryEvent, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE id = ?
`, id)
	events, err := scanL1EventRows(row)
	if err != nil {
		return nil, err
	}
	return &events[0], nil
}

func (s *L1SQLiteStore) RecentByState(ctx context.Context, memoryState string, limit int) ([]L1MemoryEvent, error) {
	if err := validateMemoryState(memoryState); err != nil {
		return nil, err
	}
	return s.recentL1MemoryEvents(ctx, "memory_state = ?", "failed to query l1 memory events by state", limit, memoryState)
}

func (s *L1SQLiteStore) RecentBySession(ctx context.Context, sessionID string, limit int) ([]L1MemoryEvent, error) {
	return s.recentL1MemoryEvents(ctx, "session_id = ?", "failed to query l1 memory events by session", limit, sessionID)
}

func (s *L1SQLiteStore) recentL1MemoryEvents(ctx context.Context, whereClause string, queryErr string, limit int, args ...interface{}) ([]L1MemoryEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	queryArgs := append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE `+whereClause+`
ORDER BY created_at DESC
LIMIT ?
`, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", queryErr, err)
	}
	defer rows.Close()
	return scanL1Events(rows)
}
