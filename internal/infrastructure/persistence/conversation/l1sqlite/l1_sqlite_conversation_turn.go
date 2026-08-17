package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	conversationTurnActiveThreadTable = "conversation_active_thread"
	conversationTurnReceiptTable      = "conversation_turn_receipt"
	conversationTurnOutboxTable       = "conversation_turn_outbox"
	conversationTurnDefaultLease      = time.Minute
	conversationTurnMaxLease          = 24 * time.Hour
	conversationTurnMaxResultBytes    = 64 * 1024
)

// applyConversationTurnSchema is additive and is called on every L1 open.
// The tables deliberately keep the semantic receipt separate from follower
// delivery state so the SQLite commit remains the durable source of truth.
func (s *L1SQLiteStore) applyConversationTurnSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domconv.ErrConversationTurnUnavailable
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS conversation_active_thread (
			session_id TEXT PRIMARY KEY CHECK(length(session_id) > 0),
			thread_id INTEGER NOT NULL CHECK(thread_id > 0),
			domain TEXT NOT NULL CHECK(length(domain) > 0 AND length(domain) <= 1024),
			message_count INTEGER NOT NULL CHECK(message_count >= 0 AND message_count <= 12),
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS conversation_turn_receipt (
			turn_id TEXT PRIMARY KEY CHECK(length(turn_id) > 0),
			payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
			session_id TEXT NOT NULL CHECK(length(session_id) > 0),
			trace_id TEXT NOT NULL CHECK(length(trace_id) > 0),
			thread_id INTEGER NOT NULL CHECK(thread_id > 0),
			closed_thread_id INTEGER,
			user_message_id TEXT NOT NULL CHECK(length(user_message_id) > 0),
			agent_message_id TEXT NOT NULL CHECK(length(agent_message_id) > 0),
			status TEXT NOT NULL CHECK(status IN ('completed', 'partial', 'failed')),
			result_json TEXT NOT NULL CHECK(length(result_json) <= 65536),
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_turn_receipt_session_created ON conversation_turn_receipt(session_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS conversation_turn_outbox (
			turn_id TEXT NOT NULL CHECK(length(turn_id) > 0),
			target TEXT NOT NULL CHECK(target IN ('redis_projection', 'thread_followers')),
			session_id TEXT NOT NULL CHECK(length(session_id) > 0),
			thread_id INTEGER NOT NULL CHECK(thread_id > 0),
			closed_thread_id INTEGER,
			payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
			payload_json TEXT NOT NULL CHECK(length(payload_json) <= 8192),
			status TEXT NOT NULL CHECK(status IN ('pending', 'running', 'completed', 'failed')),
			lease_token TEXT NOT NULL DEFAULT '',
			lease_expires_at TIMESTAMP,
			attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
			last_error TEXT NOT NULL DEFAULT '' CHECK(last_error IN ('', 'invalid', 'conflict', 'unavailable', 'internal')),
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY(turn_id, target),
			FOREIGN KEY(turn_id) REFERENCES conversation_turn_receipt(turn_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_turn_outbox_claim ON conversation_turn_outbox(status, lease_expires_at, created_at, turn_id, target)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return domconv.ErrConversationTurnUnavailable
		}
	}
	if err := tx.Commit(); err != nil {
		return domconv.ErrConversationTurnUnavailable
	}
	return nil
}

func (s *L1SQLiteStore) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	normalized, err := domconv.NormalizeConversationTurnRequest(request)
	if err != nil {
		return failedConversationTurnResult(request, domconv.ConversationTurnErrorInvalid), err
	}
	payloadHash, err := domconv.ConversationTurnPayloadSHA256(normalized)
	if err != nil {
		return failedConversationTurnResult(normalized, domconv.ConversationTurnErrorInvalid), err
	}
	userMessageID, agentMessageID, err := domconv.ConversationTurnMessageIDs(normalized.TurnID)
	if err != nil {
		return failedConversationTurnResult(normalized, domconv.ConversationTurnErrorInvalid), err
	}
	base := domconv.ConversationTurnResult{
		TurnID: normalized.TurnID, TraceID: normalized.TurnID, SessionID: normalized.SessionID,
		UserMessageID: userMessageID, AgentMessageID: agentMessageID,
		MessageIDs: []string{userMessageID, agentMessageID}, PayloadSHA256: payloadHash,
		RequestedTargets: conversationTurnTargetStrings(normalized.Targets), Status: domconv.ConversationTurnFailed,
		ErrorCode: domconv.ConversationTurnErrorInternal,
	}
	if s == nil || s.db == nil {
		base.ErrorCode = domconv.ConversationTurnErrorUnavailable
		return base, domconv.ErrConversationTurnUnavailable
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return base, domconv.ErrConversationTurnInternal
	}
	rollback := func(code domconv.ConversationTurnErrorCode, cause ...error) (domconv.ConversationTurnResult, error) {
		_ = tx.Rollback()
		base.ErrorCode = code
		if len(cause) > 0 && cause[0] != nil {
			log.Printf("[ConversationTurn] commit rollback code=%s turn=%s cause=%v", code, normalized.TurnID, cause[0])
		}
		return base, conversationTurnError(code)
	}

	var existingHash, existingJSON string
	err = tx.QueryRowContext(ctx, `
SELECT payload_sha256, result_json
FROM conversation_turn_receipt
WHERE turn_id = ?`, normalized.TurnID).Scan(&existingHash, &existingJSON)
	if err == nil {
		if existingHash != payloadHash {
			return rollback(domconv.ConversationTurnErrorConflict)
		}
		var replay domconv.ConversationTurnResult
		if err := json.Unmarshal([]byte(existingJSON), &replay); err != nil {
			return rollback(domconv.ConversationTurnErrorInternal, err)
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return base, domconv.ErrConversationTurnInternal
		}
		replay.IdempotentReplay = true
		return replay, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}

	now := time.Now().UTC()
	threadID, closedThreadID, messageCount, threadDomain, err := selectConversationTurnThread(ctx, tx, normalized, now)
	if err != nil {
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}
	base.ThreadID = threadID
	base.ClosedThreadID = closedThreadID
	base.Status = domconv.ConversationTurnCompleted
	base.ErrorCode = ""
	base.PendingTargets = nil
	base.CompletedTargets = nil
	if target := requestedConversationTurnOutboxTargets(normalized.Targets, closedThreadID != 0); len(target) > 0 {
		base.Status = domconv.ConversationTurnPartial
		base.PendingTargets = append([]string(nil), target...)
	}

	if err := upsertConversationActiveThread(ctx, tx, normalized.SessionID, threadID, threadDomain, messageCount, now); err != nil {
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}
	if err := insertConversationTurnMessages(ctx, tx, normalized, threadID, userMessageID, agentMessageID, now); err != nil {
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}
	if err := insertConversationTurnRecallTrace(ctx, tx, normalized, now); err != nil {
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}

	resultJSON, err := json.Marshal(base)
	if err != nil || len(resultJSON) > conversationTurnMaxResultBytes {
		if err == nil {
			err = errors.New("turn result json exceeds bound")
		}
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}
	var closedValue interface{}
	if closedThreadID != 0 {
		closedValue = closedThreadID
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_turn_receipt (
	turn_id, payload_sha256, session_id, trace_id, thread_id, closed_thread_id,
	user_message_id, agent_message_id, status, result_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, normalized.TurnID, payloadHash, normalized.SessionID,
		normalized.TurnID, threadID, closedValue, userMessageID, agentMessageID, base.Status, string(resultJSON), now, now); err != nil {
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}
	for _, target := range requestedConversationTurnOutboxTargets(normalized.Targets, closedThreadID != 0) {
		payload, err := conversationTurnOutboxPayload(normalized, threadID, closedThreadID, userMessageID, agentMessageID, target, payloadHash)
		if err != nil {
			return rollback(domconv.ConversationTurnErrorInternal, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_turn_outbox (
	turn_id, target, session_id, thread_id, closed_thread_id, payload_sha256,
	payload_json, status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', NULL, 0, '', ?, ?)`, normalized.TurnID, target,
			normalized.SessionID, threadID, closedValue, payloadHash, payload, domconv.ConversationTurnOutboxPending, now, now); err != nil {
			return rollback(domconv.ConversationTurnErrorInternal, err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[ConversationTurn] commit failed turn=%s cause=%v", normalized.TurnID, err)
		base.ErrorCode = domconv.ConversationTurnErrorInternal
		return base, domconv.ErrConversationTurnInternal
	}
	return base, nil
}

func (s *L1SQLiteStore) ClaimConversationTurnOutbox(ctx context.Context, turnID string, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return s.claimConversationTurnOutbox(ctx, turnID, now, leaseDuration)
}

// ClaimConversationTurnOutboxExcluding is used by the bounded foreground
// drain so one turn+target is claimed at most once in that call.
func (s *L1SQLiteStore) ClaimConversationTurnOutboxExcluding(ctx context.Context, turnID string, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return s.claimConversationTurnOutboxWithExclusions(ctx, turnID, now, leaseDuration, excluded)
}

// ClaimNextConversationTurnOutbox claims the oldest pending or stale-running
// follower across all turns. It is the bounded startup replay primitive.
func (s *L1SQLiteStore) ClaimNextConversationTurnOutbox(ctx context.Context, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	return s.claimConversationTurnOutbox(ctx, "", now, leaseDuration)
}

// ClaimNextConversationTurnOutboxExcluding is the drain-only variant that
// prevents a failed target from being claimed again in the same bounded call.
// The normal claim primitive still exposes retryable failed rows to later
// calls.
func (s *L1SQLiteStore) ClaimNextConversationTurnOutboxExcluding(ctx context.Context, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	return s.claimConversationTurnOutboxWithExclusions(ctx, "", now, leaseDuration, excluded)
}

func (s *L1SQLiteStore) claimConversationTurnOutbox(ctx context.Context, turnID string, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	return s.claimConversationTurnOutboxWithExclusions(ctx, turnID, now, leaseDuration, nil)
}

func (s *L1SQLiteStore) claimConversationTurnOutboxWithExclusions(ctx context.Context, turnID string, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	if s == nil || s.db == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	leaseDuration = boundedConversationTurnLease(leaseDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	rollback := func(code domconv.ConversationTurnErrorCode) (*domconv.ConversationTurnOutbox, error) {
		_ = tx.Rollback()
		return nil, conversationTurnError(code)
	}
	if err := terminalizeExhaustedConversationTurnOutbox(ctx, tx, now, turnID); err != nil {
		return rollback(domconv.ConversationTurnErrorInternal)
	}
	where := "WHERE "
	args := make([]interface{}, 0, 2+len(excluded)*2)
	if turnID != "" {
		where += "turn_id = ? AND "
		args = append(args, turnID)
	}
	if len(excluded) > 0 {
		keys := make([]string, 0, len(excluded))
		for key := range excluded {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		where += "NOT ("
		for index, key := range keys {
			if index > 0 {
				where += " OR "
			}
			parts := strings.SplitN(key, "\x00", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return nil, domconv.ErrConversationTurnInvalid
			}
			where += "(turn_id = ? AND target = ?)"
			args = append(args, parts[0], parts[1])
		}
		where += ") AND "
	}
	where += `(
	status = 'pending' OR
	(status = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at <= ? AND attempts < ?) OR
	(status = 'failed' AND attempts < ?)
)`
	args = append(args, now, domconv.ConversationTurnMaxOutboxAttempts, domconv.ConversationTurnMaxOutboxAttempts)
	query := `
SELECT turn_id, target, session_id, thread_id, closed_thread_id, payload_sha256,
       payload_json, status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at
FROM conversation_turn_outbox
` + where + `
ORDER BY created_at ASC, turn_id ASC, target ASC
LIMIT 1`
	row := tx.QueryRowContext(ctx, query, args...)
	outbox, err := scanConversationTurnOutbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, domconv.ErrConversationTurnInternal
		}
		return nil, nil
	}
	if err != nil {
		return rollback(domconv.ConversationTurnErrorInternal)
	}
	leaseToken := fmt.Sprintf("conversation-turn-lease:%d:%d", now.UnixNano(), l1IDSequence.Add(1))
	expires := now.Add(leaseDuration)
	result, err := tx.ExecContext(ctx, `
UPDATE conversation_turn_outbox
SET status = ?, lease_token = ?, lease_expires_at = ?, attempts = attempts + 1, last_error = '', updated_at = ?
WHERE turn_id = ? AND target = ? AND (
	status = 'pending' OR
	(status = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at <= ? AND attempts < ?) OR
	(status = 'failed' AND attempts < ?)
	)`, domconv.ConversationTurnOutboxRunning, leaseToken, expires, now, outbox.TurnID, outbox.Target, now, domconv.ConversationTurnMaxOutboxAttempts, domconv.ConversationTurnMaxOutboxAttempts)
	if err != nil {
		return rollback(domconv.ConversationTurnErrorInternal)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return rollback(domconv.ConversationTurnErrorConflict)
	}
	if err := tx.Commit(); err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	outbox.Status = domconv.ConversationTurnOutboxRunning
	outbox.LeaseToken = leaseToken
	outbox.LeaseExpiresAt = expires
	outbox.Attempts++
	outbox.UpdatedAt = now
	return outbox, nil
}

// terminalizeExhaustedConversationTurnOutbox closes stale leases that have
// already consumed the bounded retry budget. A worker crash must not make an
// attempts==Max row claimable forever, and the turn receipt must reach the
// same terminal failed state as an explicit follower failure.
func terminalizeExhaustedConversationTurnOutbox(ctx context.Context, tx *sql.Tx, now time.Time, turnID string) error {
	query := `
SELECT turn_id, target, last_error
FROM conversation_turn_outbox
WHERE status = 'running' AND attempts >= ?
	AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?`
	args := []interface{}{domconv.ConversationTurnMaxOutboxAttempts, now}
	if turnID != "" {
		query += ` AND turn_id = ?`
		args = append(args, turnID)
	}
	query += ` ORDER BY created_at ASC, turn_id ASC, target ASC LIMIT 1`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	type exhausted struct {
		turnID    string
		target    string
		lastError string
	}
	items := make([]exhausted, 0)
	for rows.Next() {
		var item exhausted
		if err := rows.Scan(&item.turnID, &item.target, &item.lastError); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	turnIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		code := domconv.ConversationTurnErrorCode(item.lastError)
		if !validConversationTurnErrorCode(code) {
			code = domconv.ConversationTurnErrorUnavailable
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE conversation_turn_outbox
SET status = 'failed', lease_token = '', lease_expires_at = NULL, last_error = ?, updated_at = ?
WHERE turn_id = ? AND target = ? AND status = 'running' AND attempts >= ?`, string(code), now, item.turnID, item.target, domconv.ConversationTurnMaxOutboxAttempts); err != nil {
			return err
		}
		turnIDs[item.turnID] = struct{}{}
	}
	for turnID := range turnIDs {
		if _, err := recomputeConversationTurnReceipt(ctx, tx, turnID, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *L1SQLiteStore) CompleteConversationTurnOutbox(ctx context.Context, turnID, target, leaseToken string, now time.Time) (domconv.ConversationTurnResult, error) {
	return s.finishConversationTurnOutbox(ctx, turnID, target, leaseToken, "", now)
}

func (s *L1SQLiteStore) FailConversationTurnOutbox(ctx context.Context, turnID, target, leaseToken string, code domconv.ConversationTurnErrorCode, now time.Time) (domconv.ConversationTurnResult, error) {
	if !validConversationTurnErrorCode(code) {
		return domconv.ConversationTurnResult{TurnID: strings.TrimSpace(turnID), Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorInvalid}, domconv.ErrConversationTurnInvalid
	}
	return s.finishConversationTurnOutbox(ctx, turnID, target, leaseToken, code, now)
}

func (s *L1SQLiteStore) finishConversationTurnOutbox(ctx context.Context, turnID, target, leaseToken string, failureCode domconv.ConversationTurnErrorCode, now time.Time) (domconv.ConversationTurnResult, error) {
	turnID = strings.TrimSpace(turnID)
	target = strings.TrimSpace(target)
	leaseToken = strings.TrimSpace(leaseToken)
	if turnID == "" || (domconv.ConversationTurnTarget(target) != domconv.ConversationTurnTargetRedisProjection && domconv.ConversationTurnTarget(target) != domconv.ConversationTurnTargetThreadFollowers) || leaseToken == "" {
		return domconv.ConversationTurnResult{TurnID: turnID, Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorInvalid}, domconv.ErrConversationTurnInvalid
	}
	if s == nil || s.db == nil {
		return domconv.ConversationTurnResult{TurnID: turnID, Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorUnavailable}, domconv.ErrConversationTurnUnavailable
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domconv.ConversationTurnResult{TurnID: turnID, Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorInternal}, domconv.ErrConversationTurnInternal
	}
	rollback := func(code domconv.ConversationTurnErrorCode) (domconv.ConversationTurnResult, error) {
		_ = tx.Rollback()
		return domconv.ConversationTurnResult{TurnID: turnID, Status: domconv.ConversationTurnFailed, ErrorCode: code}, conversationTurnError(code)
	}
	var status string
	var expires sql.NullTime
	err = tx.QueryRowContext(ctx, `
SELECT status, lease_expires_at
FROM conversation_turn_outbox
WHERE turn_id = ? AND target = ? AND lease_token = ?`, turnID, target, leaseToken).Scan(&status, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(domconv.ConversationTurnErrorConflict)
	}
	if err != nil {
		return rollback(domconv.ConversationTurnErrorInternal)
	}
	if status == string(domconv.ConversationTurnOutboxCompleted) {
		result, resultErr := recomputeConversationTurnReceipt(ctx, tx, turnID, now)
		if resultErr != nil {
			return rollback(domconv.ConversationTurnErrorInternal)
		}
		if err := tx.Commit(); err != nil {
			return rollbackNoTxConversationTurn(turnID, domconv.ConversationTurnErrorInternal)
		}
		return result, nil
	}
	if status != string(domconv.ConversationTurnOutboxRunning) || !expires.Valid || !expires.Time.After(now) {
		return rollback(domconv.ConversationTurnErrorConflict)
	}
	newStatus := domconv.ConversationTurnOutboxCompleted
	lastError := ""
	if failureCode != "" {
		newStatus = domconv.ConversationTurnOutboxFailed
		lastError = string(failureCode)
	}
	updateResult, err := tx.ExecContext(ctx, `
UPDATE conversation_turn_outbox
SET status = ?, lease_token = '', lease_expires_at = NULL, last_error = ?, updated_at = ?
WHERE turn_id = ? AND target = ? AND status = 'running' AND lease_token = ? AND lease_expires_at > ?`, newStatus, lastError, now, turnID, target, leaseToken, now)
	if err != nil {
		return rollback(domconv.ConversationTurnErrorInternal)
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return rollback(domconv.ConversationTurnErrorInternal)
	}
	if affected != 1 {
		return rollback(domconv.ConversationTurnErrorConflict)
	}
	result, err := recomputeConversationTurnReceipt(ctx, tx, turnID, now)
	if err != nil {
		return rollback(domconv.ConversationTurnErrorInternal)
	}
	if err := tx.Commit(); err != nil {
		return rollbackNoTxConversationTurn(turnID, domconv.ConversationTurnErrorInternal)
	}
	return result, nil
}

func (s *L1SQLiteStore) GetConversationTurnReceipt(ctx context.Context, turnID string) (domconv.ConversationTurnResult, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInvalid
	}
	if s == nil || s.db == nil {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnUnavailable
	}
	var resultJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT result_json FROM conversation_turn_receipt WHERE turn_id = ?`, turnID).Scan(&resultJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnUnavailable
		}
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInternal
	}
	var result domconv.ConversationTurnResult
	if json.Unmarshal([]byte(resultJSON), &result) != nil {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInternal
	}
	return result, nil
}

// LoadConversationThreadProjection loads the canonical conversation messages
// for one exact L1 thread. The L1 rows, rather than Redis, are authoritative.
// The returned rows retain their bodies for the summarizer but callers must
// never copy those bodies into an outbox payload.
func (s *L1SQLiteStore) LoadConversationThreadProjection(ctx context.Context, sessionID string, threadID int64) ([]L1MemoryEvent, error) {
	if s == nil || s.db == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !utf8.ValidString(sessionID) || threadID <= 0 {
		return nil, domconv.ErrConversationTurnInvalid
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE session_id = ? AND thread_id = ?
ORDER BY rowid ASC
LIMIT 13`, sessionID, threadID)
	if err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	defer rows.Close()
	events := make([]L1MemoryEvent, 0, 12)
	for rows.Next() {
		var event L1MemoryEvent
		var speaker, metaJSON string
		if err := rows.Scan(&event.ID, &event.Namespace, &event.SessionID, &event.ThreadID, &speaker,
			&event.Message, &metaJSON, &event.MemoryState, &event.Layer, &event.Source,
			&event.CreatedAt, &event.UpdatedAt); err != nil {
			return nil, domconv.ErrConversationTurnInternal
		}
		event.Speaker = domconv.Speaker(speaker)
		if err := json.Unmarshal([]byte(metaJSON), &event.Meta); err != nil || event.Meta == nil {
			return nil, domconv.ErrConversationTurnInvalid
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	if err := validateConversationThreadProjection(sessionID, threadID, events); err != nil {
		return nil, err
	}
	return events, nil
}

// LoadActiveConversationThreadProjection resolves the active thread ID from
// conversation_active_thread before loading the projection. It never guesses
// an active thread from the newest message row.
func (s *L1SQLiteStore) LoadActiveConversationThreadProjection(ctx context.Context, sessionID string) ([]L1MemoryEvent, error) {
	if s == nil || s.db == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !utf8.ValidString(sessionID) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	var threadID int64
	var messageCount int
	var activeDomain string
	if err := s.db.QueryRowContext(ctx, `
SELECT thread_id, message_count, domain
FROM conversation_active_thread
	WHERE session_id = ?`, sessionID).Scan(&threadID, &messageCount, &activeDomain); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domconv.ErrThreadNotFound
		}
		return nil, domconv.ErrConversationTurnInternal
	}
	events, err := s.LoadConversationThreadProjection(ctx, sessionID, threadID)
	if err != nil {
		return nil, err
	}
	if len(events) != messageCount {
		return nil, domconv.ErrConversationTurnInvalid
	}
	if activeDomain == "" || !utf8.ValidString(activeDomain) || strings.IndexByte(activeDomain, 0) >= 0 {
		return nil, domconv.ErrConversationTurnInvalid
	}
	for _, event := range events {
		if domain, ok := event.Meta["domain"].(string); !ok || domain != activeDomain {
			return nil, domconv.ErrConversationTurnInvalid
		}
	}
	return events, nil
}

func validateConversationThreadProjection(sessionID string, threadID int64, events []L1MemoryEvent) error {
	if len(events) < 2 || len(events) > 12 || len(events)%2 != 0 {
		return domconv.ErrConversationTurnInvalid
	}
	var domain string
	for index, event := range events {
		if event.ID == "" || !utf8.ValidString(event.ID) || strings.IndexByte(event.ID, 0) >= 0 ||
			event.SessionID != sessionID || event.ThreadID != threadID || event.Namespace != fmt.Sprintf("conv:%d", threadID) ||
			event.Source != "conversation" || event.Layer != MemoryLayerL1 || event.MemoryState != MemoryStateObserved ||
			!utf8.ValidString(event.Message) || strings.IndexByte(event.Message, 0) >= 0 {
			return domconv.ErrConversationTurnInvalid
		}
		if event.Meta == nil || len(event.Meta) != 6 {
			return domconv.ErrConversationTurnInvalid
		}
		for key := range event.Meta {
			switch key {
			case "domain", "message_id", "turn_id", "speaker", "from", "to":
			default:
				return domconv.ErrConversationTurnInvalid
			}
		}
		metaStrings := make(map[string]string, 6)
		for _, key := range []string{"domain", "message_id", "turn_id", "speaker", "from", "to"} {
			value, ok := event.Meta[key].(string)
			if !ok || value != strings.TrimSpace(value) || strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
				return domconv.ErrConversationTurnInvalid
			}
			metaStrings[key] = value
		}
		if metaStrings["message_id"] != event.ID || metaStrings["speaker"] != string(event.Speaker) {
			return domconv.ErrConversationTurnInvalid
		}
		if domain == "" {
			domain = metaStrings["domain"]
		} else if domain != metaStrings["domain"] {
			return domconv.ErrConversationTurnInvalid
		}
		if index%2 == 0 {
			if event.Speaker != domconv.SpeakerUser || metaStrings["from"] != string(domconv.SpeakerUser) {
				return domconv.ErrConversationTurnInvalid
			}
		} else {
			canonical, ok := domconv.CanonicalChatAgentSpeaker(event.Speaker)
			if !ok || canonical != event.Speaker || metaStrings["from"] != string(event.Speaker) || metaStrings["to"] != string(domconv.SpeakerUser) {
				return domconv.ErrConversationTurnInvalid
			}
		}
		if index%2 == 0 {
			next := events[index+1]
			nextSpeaker, nextOK := next.Meta["speaker"].(string)
			canonical, canonicalOK := domconv.CanonicalChatAgentSpeaker(next.Speaker)
			if metaStrings["to"] == "" || !nextOK || nextSpeaker != metaStrings["to"] || !canonicalOK || canonical != next.Speaker {
				return domconv.ErrConversationTurnInvalid
			}
		} else {
			previous := events[index-1]
			previousTurn, previousOK := previous.Meta["turn_id"].(string)
			if !previousOK || previousTurn != metaStrings["turn_id"] {
				return domconv.ErrConversationTurnInvalid
			}
			userID, agentID, err := domconv.ConversationTurnMessageIDs(metaStrings["turn_id"])
			if err != nil || previous.ID != userID || event.ID != agentID {
				return domconv.ErrConversationTurnInvalid
			}
		}
	}
	if domain == "" {
		return domconv.ErrConversationTurnInvalid
	}
	return nil
}

func selectConversationTurnThread(ctx context.Context, tx *sql.Tx, request domconv.ConversationTurnRequest, _ time.Time) (threadID, closedThreadID, messageCount int64, domain string, err error) {
	var currentID int64
	var currentDomain string
	var currentCount int
	err = tx.QueryRowContext(ctx, `
SELECT thread_id, domain, message_count
FROM conversation_active_thread
WHERE session_id = ?`, request.SessionID).Scan(&currentID, &currentDomain, &currentCount)
	hasCurrent := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, "", err
	}
	if hasCurrent && (currentID <= 0 || currentCount < 0 || currentCount > 12) {
		return 0, 0, 0, "", errors.New("invalid active thread")
	}
	domain = request.Domain
	if domain == "" {
		domain = currentDomain
	}
	if !hasCurrent {
		return deterministicConversationThreadID(request.SessionID, request.TurnID, ""), 0, 2, domain, nil
	}
	if request.Boundary || currentCount+2 > 12 {
		return deterministicConversationThreadID(request.SessionID, request.TurnID, fmt.Sprintf("%d", currentID)), currentID, 2, domain, nil
	}
	return currentID, 0, int64(currentCount + 2), domain, nil
}

func upsertConversationActiveThread(ctx context.Context, tx *sql.Tx, sessionID string, threadID int64, domain string, count int64, now time.Time) error {
	result, err := tx.ExecContext(ctx, `
UPDATE conversation_active_thread
SET thread_id = ?, domain = ?, message_count = ?, updated_at = ?
WHERE session_id = ?`, threadID, domain, count, now, sessionID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO conversation_active_thread (session_id, thread_id, domain, message_count, updated_at)
VALUES (?, ?, ?, ?, ?)`, sessionID, threadID, domain, count, now)
	return err
}

func insertConversationTurnMessages(ctx context.Context, tx *sql.Tx, request domconv.ConversationTurnRequest, threadID int64, userMessageID, agentMessageID string, now time.Time) error {
	namespace, err := BuildL1Namespace(NamespaceKindConversation, fmt.Sprintf("%d", threadID))
	if err != nil {
		return err
	}
	items := []struct {
		id      string
		speaker domconv.Speaker
		message string
	}{
		{id: userMessageID, speaker: domconv.SpeakerUser, message: request.UserMessage},
		{id: agentMessageID, speaker: request.AgentSpeaker, message: request.AgentMessage},
	}
	for index, item := range items {
		from := string(item.speaker)
		createdAt := now.Add(time.Duration(index) * time.Nanosecond)
		to := string(request.AgentSpeaker)
		if item.speaker == request.AgentSpeaker {
			to = string(domconv.SpeakerUser)
		}
		metaJSON, err := json.Marshal(map[string]interface{}{
			"domain":     request.Domain,
			"message_id": item.id,
			"turn_id":    request.TurnID,
			"speaker":    string(item.speaker),
			"from":       from,
			"to":         to,
		})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.id, namespace, request.SessionID, threadID,
			string(item.speaker), item.message, string(metaJSON), MemoryStateObserved, MemoryLayerL1, "conversation", createdAt, now); err != nil {
			return err
		}
		if _, err := appendL1EventLog(ctx, tx, "memory.message_saved", namespace, request.SessionID, threadID, map[string]interface{}{
			"memory_id": item.id, "turn_id": request.TurnID, "speaker": string(item.speaker), "memory_state": MemoryStateObserved,
		}, "conversation"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_profile_promotion_job (
	evidence_event_id, session_id, thread_id, state, attempt_count,
	lease_token, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, 0, '', '', ?, ?)`, userMessageID, request.SessionID, threadID, domainmemory.ProfilePromotionPending, now, now); err != nil {
		return err
	}
	return nil
}

func insertConversationTurnRecallTrace(ctx context.Context, tx *sql.Tx, request domconv.ConversationTurnRequest, now time.Time) error {
	traceID := request.TurnID
	records := TraceItemRecordsFromPack(traceID, request.RecallTraceItems)
	injectedCount := 0
	totalTokens := 0
	for _, item := range records {
		if item.Injected {
			injectedCount++
			totalTokens += item.TokenCount
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO recall_trace (
	trace_id, owner_id, turn_id, chat_id, persona, route, user_message_hash, query_text_redacted,
	created_at, model_id, prompt_version, recall_policy_version, total_candidates,
	injected_count, total_injected_tokens, status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?)`, traceID, request.OwnerID, request.TurnID, request.SessionID,
		string(request.AgentSpeaker), "conversation", HashRecallText(request.UserMessage), RedactedRecallQuery(request.UserMessage),
		now, "conversation-turn-v1", len(records), injectedCount, totalTokens, "started"); err != nil {
		return err
	}
	for _, item := range records {
		var retrievedAt interface{}
		if !item.RetrievedAt.IsZero() {
			retrievedAt = item.RetrievedAt.UTC()
		}
		var publishedAt interface{}
		if !item.PublishedAt.IsZero() {
			publishedAt = item.PublishedAt.UTC()
		}
		injected := 0
		if item.Injected {
			injected = 1
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO recall_trace_item (
	item_id, trace_id, layer, memory_id, source_id, source_url, source_type, status,
	score, relevance, recency, confidence, source_trust, reason, injected,
	prompt_section, token_count, sensitivity, memory_state, is_raw_or_summary, retrieved_at,
	published_at, event_id, summary, kind
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ItemID, item.TraceID,
			item.Layer, item.MemoryID, item.SourceID, item.SourceURL, item.SourceType, item.Status,
			item.Score, item.Relevance, item.Recency, item.Confidence, item.SourceTrust, item.Reason, injected,
			item.PromptSection, item.TokenCount, item.Sensitivity, item.MemoryState, item.IsRawOrSummary, retrievedAt,
			publishedAt, item.EventID, item.Summary, item.Kind); err != nil {
			return err
		}
	}
	for _, event := range PromptInjectionEventsFromItems(traceID, records, now) {
		itemIDs, err := json.Marshal(event.ItemIDs)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO prompt_injection_event (
	injection_id, trace_id, prompt_section, order_index, item_ids, token_count, redaction_level, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, event.InjectionID, event.TraceID, event.PromptSection, event.OrderIndex,
			string(itemIDs), event.TokenCount, event.RedactionLevel, event.CreatedAt.UTC()); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
UPDATE recall_trace
SET status = ?, injected_count = ?, total_injected_tokens = ?
WHERE trace_id = ?`, "completed", injectedCount, totalTokens, traceID)
	return err
}

func requestedConversationTurnOutboxTargets(targets []domconv.ConversationTurnTarget, closesThread bool) []string {
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		if target == domconv.ConversationTurnTargetThreadFollowers && !closesThread {
			continue
		}
		result = append(result, string(target))
	}
	return result
}

func conversationTurnTargetStrings(targets []domconv.ConversationTurnTarget) []string {
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		result = append(result, string(target))
	}
	return result
}

func conversationTurnOutboxPayload(request domconv.ConversationTurnRequest, threadID, closedThreadID int64, userMessageID, agentMessageID, target, payloadHash string) (string, error) {
	payload := struct {
		Version        string `json:"version"`
		TurnID         string `json:"turn_id"`
		TraceID        string `json:"trace_id"`
		SessionID      string `json:"session_id"`
		OwnerID        string `json:"owner_id"`
		ThreadID       int64  `json:"thread_id"`
		ClosedThreadID int64  `json:"closed_thread_id,omitempty"`
		UserMessageID  string `json:"user_message_id"`
		AgentMessageID string `json:"agent_message_id"`
		Target         string `json:"target"`
		PayloadSHA256  string `json:"payload_sha256"`
	}{
		Version: "rencrow.conversation_turn_outbox.v1", TurnID: request.TurnID, TraceID: request.TurnID,
		SessionID: request.SessionID, OwnerID: request.OwnerID, ThreadID: threadID, ClosedThreadID: closedThreadID,
		UserMessageID: userMessageID, AgentMessageID: agentMessageID, Target: target, PayloadSHA256: payloadHash,
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > 8192 {
		return "", errors.New("conversation turn outbox payload exceeds bound")
	}
	return string(encoded), nil
}

func recomputeConversationTurnReceipt(ctx context.Context, tx *sql.Tx, turnID string, now time.Time) (domconv.ConversationTurnResult, error) {
	var resultJSON string
	var payloadHash string
	if err := tx.QueryRowContext(ctx, `SELECT payload_sha256, result_json FROM conversation_turn_receipt WHERE turn_id = ?`, turnID).Scan(&payloadHash, &resultJSON); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	var result domconv.ConversationTurnResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT target, status, attempts, last_error FROM conversation_turn_outbox WHERE turn_id = ? ORDER BY target ASC`, turnID)
	if err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	defer rows.Close()
	result.PendingTargets = nil
	result.CompletedTargets = nil
	allCompleted := true
	terminalFailure := false
	for rows.Next() {
		var target, status, lastError string
		var attempts int
		if err := rows.Scan(&target, &status, &attempts, &lastError); err != nil {
			return domconv.ConversationTurnResult{}, err
		}
		if status == string(domconv.ConversationTurnOutboxCompleted) {
			result.CompletedTargets = append(result.CompletedTargets, target)
		} else {
			allCompleted = false
			result.PendingTargets = append(result.PendingTargets, target)
			if lastError != "" {
				result.ErrorCode = domconv.ConversationTurnErrorCode(lastError)
			}
			if status == string(domconv.ConversationTurnOutboxFailed) && attempts >= domconv.ConversationTurnMaxOutboxAttempts {
				terminalFailure = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	if terminalFailure {
		result.Status = domconv.ConversationTurnFailed
		if !validConversationTurnErrorCode(result.ErrorCode) {
			result.ErrorCode = domconv.ConversationTurnErrorInternal
		}
	} else if allCompleted {
		result.Status = domconv.ConversationTurnCompleted
		result.ErrorCode = ""
	} else {
		result.Status = domconv.ConversationTurnPartial
	}
	resultJSONBytes, err := json.Marshal(result)
	if err != nil || len(resultJSONBytes) > conversationTurnMaxResultBytes {
		return domconv.ConversationTurnResult{}, errors.New("conversation turn result exceeds bound")
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE conversation_turn_receipt
SET status = ?, result_json = ?, updated_at = ?
WHERE turn_id = ? AND payload_sha256 = ?`, result.Status, string(resultJSONBytes), now, turnID, payloadHash); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	return result, nil
}

func scanConversationTurnOutbox(row interface{ Scan(...interface{}) error }) (*domconv.ConversationTurnOutbox, error) {
	var outbox domconv.ConversationTurnOutbox
	var closed sql.NullInt64
	var lease sql.NullTime
	var status, lastError string
	if err := row.Scan(&outbox.TurnID, &outbox.Target, &outbox.SessionID, &outbox.ThreadID, &closed,
		&outbox.PayloadSHA256, &outbox.PayloadJSON, &status, &outbox.LeaseToken, &lease, &outbox.Attempts,
		&lastError, &outbox.CreatedAt, &outbox.UpdatedAt); err != nil {
		return nil, err
	}
	outbox.Status = domconv.ConversationTurnOutboxStatus(status)
	outbox.LastError = domconv.ConversationTurnErrorCode(lastError)
	if closed.Valid {
		outbox.ClosedThreadID = closed.Int64
	}
	if lease.Valid {
		outbox.LeaseExpiresAt = lease.Time
	}
	return &outbox, nil
}

func deterministicConversationThreadID(sessionID, turnID, previous string) int64 {
	digest := sha256Bytes("rencrow.conversation-thread.v1\x00" + sessionID + "\x00" + turnID + "\x00" + previous)
	value := int64(binary.BigEndian.Uint64(digest[:8]) & math.MaxInt64)
	if value <= 0 {
		return 1
	}
	return value
}

func sha256Bytes(value string) [32]byte { return sha256.Sum256([]byte(value)) }

func boundedConversationTurnLease(value time.Duration) time.Duration {
	if value <= 0 {
		return conversationTurnDefaultLease
	}
	if value > conversationTurnMaxLease {
		return conversationTurnMaxLease
	}
	return value
}

func validConversationTurnErrorCode(code domconv.ConversationTurnErrorCode) bool {
	switch code {
	case domconv.ConversationTurnErrorInvalid, domconv.ConversationTurnErrorConflict, domconv.ConversationTurnErrorUnavailable, domconv.ConversationTurnErrorInternal:
		return true
	default:
		return false
	}
}

func conversationTurnError(code domconv.ConversationTurnErrorCode) error {
	switch code {
	case domconv.ConversationTurnErrorInvalid:
		return domconv.ErrConversationTurnInvalid
	case domconv.ConversationTurnErrorConflict:
		return domconv.ErrConversationTurnConflict
	case domconv.ConversationTurnErrorUnavailable:
		return domconv.ErrConversationTurnUnavailable
	default:
		return domconv.ErrConversationTurnInternal
	}
}

func failedConversationTurnResult(request domconv.ConversationTurnRequest, code domconv.ConversationTurnErrorCode) domconv.ConversationTurnResult {
	turnID := strings.TrimSpace(request.TurnID)
	return domconv.ConversationTurnResult{TurnID: turnID, TraceID: turnID, SessionID: strings.TrimSpace(request.SessionID), Status: domconv.ConversationTurnFailed, ErrorCode: code}
}

func rollbackNoTxConversationTurn(turnID string, code domconv.ConversationTurnErrorCode) (domconv.ConversationTurnResult, error) {
	return domconv.ConversationTurnResult{TurnID: turnID, TraceID: turnID, Status: domconv.ConversationTurnFailed, ErrorCode: code}, conversationTurnError(code)
}
