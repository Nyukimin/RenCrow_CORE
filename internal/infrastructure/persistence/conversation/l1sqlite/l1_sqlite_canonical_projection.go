package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrCanonicalConversationProjectionConflict = errors.New("canonical conversation projection conflicts with an existing L1 receipt")

func (s *L1SQLiteStore) applyCanonicalConversationProjectionSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS l1_canonical_conversation_projection_receipt (
		message_id TEXT PRIMARY KEY NOT NULL,
		event_id TEXT NOT NULL UNIQUE,
		session_id TEXT NOT NULL,
		turn_id TEXT NOT NULL,
		event_seq INTEGER NOT NULL UNIQUE,
		thread_id TEXT NOT NULL,
		thread_seq INTEGER NOT NULL,
		thread_kind TEXT NOT NULL,
		content_sha256 TEXT NOT NULL CHECK(length(content_sha256) = 64),
		applied_at TIMESTAMP NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("initialize canonical conversation projection receipt: %w", err)
	}
	return nil
}

// ApplyCanonicalConversationProjection atomically materializes public
// EventStore assignments into L1 and records their canonical idempotency keys.
// The receipt's AppliedThrough is the greatest event sequence in this unit,
// not a guarantee that all lower public messages are applied. An exact replay
// is a no-op; reusing any identity with changed content fails.
func (s *L1SQLiteStore) ApplyCanonicalConversationProjection(ctx context.Context, items []domconv.CanonicalConversationProjection) (domconv.ConversationProjectionApplyReceipt, error) {
	if s == nil || s.db == nil {
		return domconv.ConversationProjectionApplyReceipt{}, errors.New("L1 SQLite store is unavailable")
	}
	if ctx == nil || len(items) == 0 {
		return domconv.ConversationProjectionApplyReceipt{}, errors.New("canonical conversation projection context and items are required")
	}
	if err := validateCanonicalConversationProjectionItems(items); err != nil {
		return domconv.ConversationProjectionApplyReceipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domconv.ConversationProjectionApplyReceipt{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	receipt := domconv.ConversationProjectionApplyReceipt{
		SessionID:      items[0].SessionID,
		AppliedThrough: items[len(items)-1].EventSeq,
		Items:          make([]domconv.ConversationProjectionItemReceipt, 0, len(items)),
	}
	for _, item := range items {
		if err := s.applyCanonicalConversationProjectionItem(ctx, tx, item, now); err != nil {
			return domconv.ConversationProjectionApplyReceipt{}, err
		}
		receipt.Items = append(receipt.Items, domconv.ConversationProjectionItemReceipt{
			MessageID: item.MessageID, EventID: item.EventID, EventSeq: item.EventSeq, ContentSHA256: item.ContentSHA256,
		})
	}
	if err := tx.Commit(); err != nil {
		return domconv.ConversationProjectionApplyReceipt{}, err
	}
	return receipt, nil
}

func validateCanonicalConversationProjectionItems(items []domconv.CanonicalConversationProjection) error {
	if len(items) == 0 || len(items) > 2 {
		return domconv.ErrInvalidConversationProjection
	}
	sessionID, turnID := items[0].SessionID, items[0].TurnID
	threadID, threadKind := items[0].ThreadID, items[0].ThreadKind
	var previousSeq modulecore.EventSeq
	var previousThreadSeq modulecore.ThreadSeq
	seenMessages, seenEvents := make(map[modulecore.MessageID]struct{}, len(items)), make(map[modulecore.EventID]struct{}, len(items))
	for _, item := range items {
		hash, err := domconv.ConversationProjectionContentHash(item)
		if err != nil || item.ContentSHA256 != hash || item.SessionID != sessionID || item.TurnID != turnID || item.Visibility != "public" || item.EventSeq <= previousSeq ||
			item.ThreadID != threadID || item.ThreadKind != threadKind || item.ThreadSeq > domconv.MaxConversationProjectionThreadMessages ||
			(len(items) == 2 && ((previousSeq == 0 && item.Speaker != "user") || (previousSeq > 0 && item.Speaker == "user"))) ||
			(previousThreadSeq > 0 && item.ThreadSeq != previousThreadSeq+1) {
			return domconv.ErrInvalidConversationProjection
		}
		if _, found := seenMessages[item.MessageID]; found {
			return domconv.ErrInvalidConversationProjection
		}
		if _, found := seenEvents[item.EventID]; found {
			return domconv.ErrInvalidConversationProjection
		}
		seenMessages[item.MessageID], seenEvents[item.EventID] = struct{}{}, struct{}{}
		previousSeq = item.EventSeq
		previousThreadSeq = item.ThreadSeq
	}
	return nil
}

func (s *L1SQLiteStore) applyCanonicalConversationProjectionItem(ctx context.Context, tx *sql.Tx, item domconv.CanonicalConversationProjection, now time.Time) error {
	var eventID, sessionID, turnID, threadID, threadKind, hash string
	var eventSeq, threadSeq int64
	err := tx.QueryRowContext(ctx, `SELECT event_id, session_id, turn_id, event_seq, thread_id, thread_seq, thread_kind, content_sha256
		FROM l1_canonical_conversation_projection_receipt WHERE message_id=?`, string(item.MessageID)).Scan(
		&eventID, &sessionID, &turnID, &eventSeq, &threadID, &threadSeq, &threadKind, &hash,
	)
	if err == nil {
		if eventID != string(item.EventID) || sessionID != string(item.SessionID) || turnID != string(item.TurnID) || eventSeq != int64(item.EventSeq) ||
			threadID != string(item.ThreadID) || threadSeq != int64(item.ThreadSeq) || threadKind != string(item.ThreadKind) || hash != item.ContentSHA256 {
			return ErrCanonicalConversationProjectionConflict
		}
		return validateCanonicalConversationProjectionMemory(ctx, tx, item)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var conflictingMessageID string
	err = tx.QueryRowContext(ctx, `SELECT message_id FROM l1_canonical_conversation_projection_receipt WHERE event_id=? OR event_seq=? LIMIT 1`,
		string(item.EventID), int64(item.EventSeq)).Scan(&conflictingMessageID)
	if err == nil {
		return ErrCanonicalConversationProjectionConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	namespace := "conv:" + string(item.ThreadID)
	if err := ValidateL1Namespace(namespace); err != nil {
		return err
	}
	canonicalID := "canonical-conversation:" + string(item.MessageID)
	var existingMemoryID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM l1_memory_event WHERE id=?`, canonicalID).Scan(&existingMemoryID)
	if err == nil {
		return ErrCanonicalConversationProjectionConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	meta := map[string]any{
		"projection_version": "canonical_v2",
		"event_id":           string(item.EventID),
		"event_seq":          int64(item.EventSeq),
		"message_id":         string(item.MessageID),
		"turn_id":            string(item.TurnID),
		"source_actor_id":    item.ActorID,
		"content_sha256":     item.ContentSHA256,
		"visibility":         "public",
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	occurredAt := item.OccurredAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO l1_memory_event
		(id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
		 memory_state, layer, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		canonicalID, namespace, string(item.SessionID), string(item.ThreadID), int64(item.ThreadSeq), string(item.ThreadKind),
		item.Speaker, item.Text, string(metaJSON), MemoryStateObserved, MemoryLayerL1, "conversation_projection", occurredAt, occurredAt,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO l1_canonical_conversation_projection_receipt
		(message_id, event_id, session_id, turn_id, event_seq, thread_id, thread_seq, thread_kind, content_sha256, applied_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(item.MessageID), string(item.EventID), string(item.SessionID), string(item.TurnID), int64(item.EventSeq),
		string(item.ThreadID), int64(item.ThreadSeq), string(item.ThreadKind), item.ContentSHA256, now,
	); err != nil {
		return err
	}
	return nil
}

func validateCanonicalConversationProjectionMemory(ctx context.Context, tx *sql.Tx, item domconv.CanonicalConversationProjection) error {
	var namespace, sessionID, threadID, speaker, message, memoryState, layer, source, createdAt, updatedAt, metaJSON string
	var threadSeq int64
	err := tx.QueryRowContext(ctx, `SELECT namespace, session_id, thread_id, thread_seq, speaker, message, memory_state, layer, source, created_at, updated_at, meta_json
		FROM l1_memory_event WHERE id=?`, "canonical-conversation:"+string(item.MessageID)).Scan(
		&namespace, &sessionID, &threadID, &threadSeq, &speaker, &message, &memoryState, &layer, &source, &createdAt, &updatedAt, &metaJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCanonicalConversationProjectionConflict
	}
	if err != nil {
		return err
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return ErrCanonicalConversationProjectionConflict
	}
	occurredAt := item.OccurredAt.UTC().Format(time.RFC3339Nano)
	if namespace != "conv:"+string(item.ThreadID) || sessionID != string(item.SessionID) || threadID != string(item.ThreadID) || threadSeq != int64(item.ThreadSeq) ||
		speaker != item.Speaker || message != item.Text || memoryState != MemoryStateObserved || layer != MemoryLayerL1 || source != "conversation_projection" ||
		createdAt != occurredAt || updatedAt != occurredAt || meta["projection_version"] != "canonical_v2" || meta["event_seq"] != float64(item.EventSeq) ||
		meta["event_id"] != string(item.EventID) || meta["message_id"] != string(item.MessageID) || meta["turn_id"] != string(item.TurnID) ||
		meta["source_actor_id"] != item.ActorID || meta["content_sha256"] != item.ContentSHA256 || meta["visibility"] != "public" {
		return ErrCanonicalConversationProjectionConflict
	}
	return nil
}
