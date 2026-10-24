package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	conversationTurnActiveThreadTable = "conversation_active_thread"
	conversationTurnReceiptTable      = "conversation_turn_receipt"
	conversationTurnOutboxTable       = "conversation_turn_outbox"
	conversationTurnOperationTable    = "conversation_turn_operation_receipt"
	conversationTurnDefaultLease      = time.Minute
	conversationTurnMaxLease          = 24 * time.Hour
	conversationTurnMaxResultBytes    = 64 * 1024
)

// ConversationTurnOperationIdentity binds a storage-host mutation receipt to
// the caller's exact operation ID and canonical request payload hash.
type ConversationTurnOperationIdentity struct {
	OpID          string
	Operation     string
	PayloadSHA256 string
}

// PendingConversationTurnThreadFollower identifies a durable closed-thread
// follower whose work has not completed. The response marker is evidence of
// the exact canonical public final event, never evidence inferred from L1.
type PendingConversationTurnThreadFollower struct {
	TurnID            modulecore.TurnID
	TraceID           modulecore.TraceID
	AgentMessageID    modulecore.MessageID
	SessionID         string
	ClosedThreadID    modulecore.ThreadID
	ResponsePublished bool
}

func validConversationTurnOutboxTarget(target string) bool {
	return target == string(domconv.ConversationTurnTargetRedisProjection) || target == string(domconv.ConversationTurnTargetThreadFollowers)
}

type conversationTurnClaimOperationResult struct {
	Outbox     *domconv.ConversationTurnOutbox `json:"outbox"`
	LeaseToken string                          `json:"lease_token,omitempty"`
}

type conversationTurnOperationReceipt struct {
	Operation     string
	PayloadSHA256 string
	TurnID        string
	Target        string
	LeaseToken    string
	ResultJSON    string
}

type conversationTurnOperationReceiptQuery interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func (identity ConversationTurnOperationIdentity) validate() error {
	if len(identity.OpID) == 0 || len(identity.OpID) > 128 || identity.Operation == "" || len(identity.Operation) > 64 ||
		len(identity.PayloadSHA256) != 64 || strings.ToLower(identity.PayloadSHA256) != identity.PayloadSHA256 {
		return domconv.ErrConversationTurnInvalid
	}
	for _, r := range identity.OpID {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return domconv.ErrConversationTurnInvalid
		}
	}
	decoded, err := hex.DecodeString(identity.PayloadSHA256)
	if err != nil || len(decoded) != 32 {
		return domconv.ErrConversationTurnInvalid
	}
	switch identity.Operation {
	case "outbox_claim", "outbox_claim_next", "complete", "fail":
		return nil
	default:
		return domconv.ErrConversationTurnInvalid
	}
}

func readConversationTurnOperationReceipt(ctx context.Context, query conversationTurnOperationReceiptQuery, identity ConversationTurnOperationIdentity) (conversationTurnOperationReceipt, bool, error) {
	if err := identity.validate(); err != nil {
		return conversationTurnOperationReceipt{}, false, err
	}
	var receipt conversationTurnOperationReceipt
	err := query.QueryRowContext(ctx, `
SELECT operation, payload_sha256, turn_id, target, lease_token, result_json
FROM conversation_turn_operation_receipt
WHERE op_id = ?`, identity.OpID).Scan(&receipt.Operation, &receipt.PayloadSHA256, &receipt.TurnID, &receipt.Target, &receipt.LeaseToken, &receipt.ResultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return conversationTurnOperationReceipt{}, false, nil
	}
	if err != nil {
		return conversationTurnOperationReceipt{}, false, domconv.ErrConversationTurnInternal
	}
	if receipt.Operation != identity.Operation || receipt.PayloadSHA256 != identity.PayloadSHA256 || len(receipt.ResultJSON) > conversationTurnMaxResultBytes {
		return conversationTurnOperationReceipt{}, false, domconv.ErrConversationTurnConflict
	}
	return receipt, true, nil
}

func insertConversationTurnOperationReceipt(ctx context.Context, tx *sql.Tx, identity ConversationTurnOperationIdentity, turnID, target, leaseToken string, result any) error {
	if err := identity.validate(); err != nil {
		return err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil || len(resultJSON) > conversationTurnMaxResultBytes {
		return domconv.ErrConversationTurnInternal
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_turn_operation_receipt (op_id, operation, payload_sha256, turn_id, target, lease_token, result_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, identity.Operation, identity.PayloadSHA256, turnID, target, leaseToken, string(resultJSON), time.Now().UTC()); err != nil {
		return domconv.ErrConversationTurnInternal
	}
	return nil
}

func lookupConversationTurnClaimOperationReceipt(ctx context.Context, query conversationTurnOperationReceiptQuery, identity ConversationTurnOperationIdentity) (*domconv.ConversationTurnOutbox, bool, error) {
	if identity.Operation != "outbox_claim" && identity.Operation != "outbox_claim_next" {
		return nil, false, domconv.ErrConversationTurnInvalid
	}
	receipt, found, err := readConversationTurnOperationReceipt(ctx, query, identity)
	if err != nil || !found {
		return nil, found, err
	}
	var result conversationTurnClaimOperationResult
	if err := json.Unmarshal([]byte(receipt.ResultJSON), &result); err != nil {
		return nil, false, domconv.ErrConversationTurnInternal
	}
	if result.Outbox == nil {
		if receipt.TurnID != "" || receipt.Target != "" || receipt.LeaseToken != "" || result.LeaseToken != "" {
			return nil, false, domconv.ErrConversationTurnInternal
		}
		return nil, true, nil
	}
	if receipt.TurnID == "" || receipt.Target == "" || receipt.LeaseToken == "" || result.LeaseToken != receipt.LeaseToken ||
		string(result.Outbox.TurnID) != receipt.TurnID || result.Outbox.Target != receipt.Target || result.Outbox.Status != domconv.ConversationTurnOutboxRunning ||
		result.Outbox.LeaseToken != "" {
		return nil, false, domconv.ErrConversationTurnInternal
	}
	result.Outbox.LeaseToken = receipt.LeaseToken
	return result.Outbox, true, nil
}

func lookupConversationTurnFinishOperationReceipt(ctx context.Context, query conversationTurnOperationReceiptQuery, identity ConversationTurnOperationIdentity, turnID, target, leaseToken string) (domconv.ConversationTurnResult, bool, error) {
	if (identity.Operation != "complete" && identity.Operation != "fail") || turnID == "" || target == "" || leaseToken == "" {
		return domconv.ConversationTurnResult{}, false, domconv.ErrConversationTurnInvalid
	}
	receipt, found, err := readConversationTurnOperationReceipt(ctx, query, identity)
	if err != nil || !found {
		return domconv.ConversationTurnResult{}, found, err
	}
	if receipt.TurnID != turnID || receipt.Target != target || receipt.LeaseToken != leaseToken {
		return domconv.ConversationTurnResult{}, false, domconv.ErrConversationTurnConflict
	}
	var result domconv.ConversationTurnResult
	if err := json.Unmarshal([]byte(receipt.ResultJSON), &result); err != nil || string(result.TurnID) != turnID {
		return domconv.ConversationTurnResult{}, false, domconv.ErrConversationTurnInternal
	}
	return result, true, nil
}

type conversationThreadIdentity struct {
	ID   modulecore.ThreadID
	Seq  modulecore.ThreadSeq
	Kind modulecore.ThreadKind
}

type conversationThreadProjectionReader interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

func (identity conversationThreadIdentity) validate(bound bool) error {
	if bound {
		return validateL1BoundThreadTuple(identity.ID, identity.Seq, identity.Kind)
	}
	return validateL1ThreadTuple(identity.ID, identity.Seq, identity.Kind)
}

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
			thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
			thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
			thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
			domain TEXT NOT NULL CHECK(length(domain) > 0 AND length(domain) <= 1024),
			message_count INTEGER NOT NULL CHECK(message_count >= 0 AND message_count <= 12),
			updated_at TIMESTAMP NOT NULL,
			UNIQUE(thread_id),
			UNIQUE(session_id, thread_seq)
		)`,
		`CREATE TABLE IF NOT EXISTS conversation_turn_receipt (
			turn_id TEXT PRIMARY KEY CHECK(length(turn_id) > 0),
			payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
			session_id TEXT NOT NULL CHECK(length(session_id) > 0),
			trace_id TEXT NOT NULL CHECK(length(trace_id) > 0),
			root_task_id TEXT NOT NULL CHECK(length(root_task_id) > 0),
			thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
			thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
			thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
			closed_thread_id TEXT NOT NULL DEFAULT '',
			closed_thread_seq INTEGER NOT NULL DEFAULT 0,
			closed_thread_kind TEXT NOT NULL DEFAULT '',
			user_message_id TEXT NOT NULL CHECK(length(user_message_id) > 0),
			agent_message_id TEXT NOT NULL CHECK(length(agent_message_id) > 0),
			status TEXT NOT NULL CHECK(status IN ('completed', 'partial', 'failed')),
			result_json TEXT NOT NULL CHECK(length(result_json) <= 65536),
			response_published_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			CHECK (
				(closed_thread_id = '' AND closed_thread_seq = 0 AND closed_thread_kind = '') OR
				(closed_thread_id <> '' AND closed_thread_seq > 0 AND closed_thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system'))
			)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_turn_receipt_session_created ON conversation_turn_receipt(session_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS conversation_turn_outbox (
			turn_id TEXT NOT NULL CHECK(length(turn_id) > 0),
			trace_id TEXT NOT NULL CHECK(length(trace_id) > 0),
			root_task_id TEXT NOT NULL CHECK(length(root_task_id) > 0),
			target TEXT NOT NULL CHECK(target IN ('redis_projection', 'thread_followers')),
			session_id TEXT NOT NULL CHECK(length(session_id) > 0),
			thread_id TEXT NOT NULL CHECK(length(thread_id) > 0),
			thread_seq INTEGER NOT NULL CHECK(thread_seq > 0),
			thread_kind TEXT NOT NULL CHECK(thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
			closed_thread_id TEXT NOT NULL DEFAULT '',
			closed_thread_seq INTEGER NOT NULL DEFAULT 0,
			closed_thread_kind TEXT NOT NULL DEFAULT '',
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
			FOREIGN KEY(turn_id) REFERENCES conversation_turn_receipt(turn_id),
			CHECK (
				(closed_thread_id = '' AND closed_thread_seq = 0 AND closed_thread_kind = '') OR
				(closed_thread_id <> '' AND closed_thread_seq > 0 AND closed_thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system'))
			)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_turn_outbox_claim ON conversation_turn_outbox(status, lease_expires_at, created_at, turn_id, target)`,
		`CREATE TABLE IF NOT EXISTS conversation_head_reset_receipt (
			operation_id TEXT NOT NULL PRIMARY KEY CHECK(length(operation_id) BETWEEN 1 AND 128),
			payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
			session_id TEXT NOT NULL CHECK(length(session_id) > 0),
			owner_id TEXT NOT NULL CHECK(length(owner_id) BETWEEN 1 AND 1024),
			previous_thread_id TEXT NOT NULL CHECK(length(previous_thread_id) > 0),
			previous_thread_seq INTEGER NOT NULL CHECK(previous_thread_seq > 0),
			previous_thread_kind TEXT NOT NULL CHECK(previous_thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
			active_thread_id TEXT NOT NULL CHECK(length(active_thread_id) > 0),
			active_thread_seq INTEGER NOT NULL CHECK(active_thread_seq > 0),
			active_thread_kind TEXT NOT NULL CHECK(active_thread_kind IN ('user_conversation', 'agent_discussion', 'idlechat', 'document', 'system')),
			domain TEXT NOT NULL CHECK(length(domain) > 0 AND length(domain) <= 1024),
			reset_at TIMESTAMP NOT NULL,
			CHECK(previous_thread_id <> active_thread_id),
			CHECK(active_thread_seq = previous_thread_seq + 1),
			CHECK(previous_thread_kind = active_thread_kind)
		)`,
		`CREATE TABLE IF NOT EXISTS conversation_turn_operation_receipt (
			op_id TEXT NOT NULL PRIMARY KEY CHECK(length(op_id) BETWEEN 1 AND 128),
			operation TEXT NOT NULL CHECK(operation IN ('outbox_claim', 'outbox_claim_next', 'complete', 'fail')),
			payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
			turn_id TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL DEFAULT '' CHECK(target IN ('', 'redis_projection', 'thread_followers')),
			lease_token TEXT NOT NULL DEFAULT '' CHECK(length(lease_token) <= 256),
			result_json TEXT NOT NULL CHECK(length(result_json) <= 65536),
			created_at TIMESTAMP NOT NULL,
			CHECK (
				(turn_id = '' AND target = '' AND lease_token = '') OR
				(turn_id <> '' AND target <> '' AND lease_token <> '')
			)
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return domconv.ErrConversationTurnUnavailable
		}
	}
	if err := ensureConversationTurnPublicationColumn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return domconv.ErrConversationTurnUnavailable
	}
	if err := tx.Commit(); err != nil {
		return domconv.ErrConversationTurnUnavailable
	}
	return nil
}

func ensureConversationTurnPublicationColumn(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(conversation_turn_receipt)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "response_published_at" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE conversation_turn_receipt ADD COLUMN response_published_at TIMESTAMP`)
	return err
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
	userMessageID := string(normalized.UserMessageID)
	agentMessageID := string(normalized.AgentMessageID)
	base := domconv.ConversationTurnResult{
		TurnID: normalized.TurnID, TraceID: normalized.TraceID, RootTaskID: normalized.RootTaskID, SessionID: normalized.SessionID,
		UserMessageID: normalized.UserMessageID, AgentMessageID: normalized.AgentMessageID,
		MessageIDs: []string{string(normalized.UserMessageID), string(normalized.AgentMessageID)}, PayloadSHA256: payloadHash,
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
	var existingTraceID, existingRootTaskID, existingUserMessageID, existingAgentMessageID string
	var existingThreadID, existingThreadKind string
	var existingThreadSeq int64
	var existingClosedID, existingClosedKind string
	var existingClosedSeq int64
	err = tx.QueryRowContext(ctx, `
SELECT payload_sha256, trace_id, root_task_id, user_message_id, agent_message_id,
	thread_id, thread_seq, thread_kind, closed_thread_id, closed_thread_seq, closed_thread_kind, result_json
FROM conversation_turn_receipt
WHERE turn_id = ?`, normalized.TurnID).Scan(&existingHash, &existingTraceID, &existingRootTaskID, &existingUserMessageID, &existingAgentMessageID,
		&existingThreadID, &existingThreadSeq, &existingThreadKind, &existingClosedID, &existingClosedSeq, &existingClosedKind, &existingJSON)
	if err == nil {
		if existingHash != payloadHash {
			return rollback(domconv.ConversationTurnErrorConflict)
		}
		var replay domconv.ConversationTurnResult
		if err := json.Unmarshal([]byte(existingJSON), &replay); err != nil {
			return rollback(domconv.ConversationTurnErrorInternal, err)
		}
		if err := validateConversationTurnResultIdentity(replay, normalized.TurnID, modulecore.TraceID(existingTraceID), modulecore.TaskID(existingRootTaskID), modulecore.MessageID(existingUserMessageID), modulecore.MessageID(existingAgentMessageID), modulecore.ThreadID(existingThreadID), modulecore.ThreadSeq(existingThreadSeq), modulecore.ThreadKind(existingThreadKind), modulecore.ThreadID(existingClosedID), modulecore.ThreadSeq(existingClosedSeq), modulecore.ThreadKind(existingClosedKind)); err != nil {
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
	thread, closedThread, messageCount, threadDomain, err := selectConversationTurnThread(ctx, tx, normalized, now)
	if err != nil {
		if errors.Is(err, domconv.ErrConversationTurnConflict) {
			return rollback(domconv.ConversationTurnErrorConflict, err)
		}
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}
	base.ThreadID = thread.ID
	base.ThreadSeq = thread.Seq
	base.ThreadKind = thread.Kind
	base.ClosedThreadID = closedThread.ID
	base.ClosedThreadSeq = closedThread.Seq
	base.ClosedThreadKind = closedThread.Kind
	base.Status = domconv.ConversationTurnCompleted
	base.ErrorCode = ""
	base.PendingTargets = nil
	base.CompletedTargets = nil
	if target := requestedConversationTurnOutboxTargets(normalized.Targets, closedThread.ID != ""); len(target) > 0 {
		base.Status = domconv.ConversationTurnPartial
		base.PendingTargets = append([]string(nil), target...)
	}

	if err := upsertConversationActiveThread(ctx, tx, normalized.SessionID, thread, threadDomain, messageCount, now); err != nil {
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}
	if err := insertConversationTurnMessages(ctx, tx, normalized, thread, userMessageID, agentMessageID, now); err != nil {
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
	closedIDValue := string(closedThread.ID)
	closedSeqValue := int64(closedThread.Seq)
	closedKindValue := string(closedThread.Kind)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_turn_receipt (
	turn_id, payload_sha256, session_id, trace_id, root_task_id, thread_id, thread_seq, thread_kind,
	closed_thread_id, closed_thread_seq, closed_thread_kind,
	user_message_id, agent_message_id, status, result_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, normalized.TurnID, payloadHash, normalized.SessionID,
		normalized.TraceID, normalized.RootTaskID, thread.ID, thread.Seq, thread.Kind, closedIDValue, closedSeqValue, closedKindValue,
		userMessageID, agentMessageID, base.Status, string(resultJSON), now, now); err != nil {
		return rollback(domconv.ConversationTurnErrorInternal, err)
	}
	for _, target := range requestedConversationTurnOutboxTargets(normalized.Targets, closedThread.ID != "") {
		payload, err := conversationTurnOutboxPayload(normalized, thread, closedThread, userMessageID, agentMessageID, target, payloadHash)
		if err != nil {
			return rollback(domconv.ConversationTurnErrorInternal, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_turn_outbox (
	turn_id, trace_id, root_task_id, target, session_id, thread_id, thread_seq, thread_kind,
	closed_thread_id, closed_thread_seq, closed_thread_kind, payload_sha256,
	payload_json, status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, 0, '', ?, ?)`, normalized.TurnID, normalized.TraceID, normalized.RootTaskID, target,
			normalized.SessionID, thread.ID, thread.Seq, thread.Kind, closedIDValue, closedSeqValue, closedKindValue,
			payloadHash, payload, domconv.ConversationTurnOutboxPending, now, now); err != nil {
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

// ClaimConversationTurnOutboxTarget claims only the requested follower target
// for a single turn. Foreground EndTurn uses this to keep closed-thread work
// out of its synchronous Redis projection path.
func (s *L1SQLiteStore) ClaimConversationTurnOutboxTarget(ctx context.Context, turnID, target string, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || !validConversationTurnOutboxTarget(target) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return s.claimConversationTurnOutboxWithExclusions(ctx, turnID, now, leaseDuration, nil, nil, target)
}

// ClaimConversationTurnOutboxTargetForOperation persists the exact target
// claim receipt in the same transaction as its lease.
func (s *L1SQLiteStore) ClaimConversationTurnOutboxTargetForOperation(ctx context.Context, identity ConversationTurnOperationIdentity, turnID, target string, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	turnID = strings.TrimSpace(turnID)
	if identity.Operation != "outbox_claim" || turnID == "" || !validConversationTurnOutboxTarget(target) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return s.claimConversationTurnOutboxWithExclusions(ctx, turnID, now, leaseDuration, nil, &identity, target)
}

// ClaimConversationTurnOutboxForOperation persists the caller's operation
// receipt in the same transaction as the lease claim.
func (s *L1SQLiteStore) ClaimConversationTurnOutboxForOperation(ctx context.Context, identity ConversationTurnOperationIdentity, turnID string, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	turnID = strings.TrimSpace(turnID)
	if identity.Operation != "outbox_claim" || turnID == "" {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return s.claimConversationTurnOutboxWithExclusions(ctx, turnID, now, leaseDuration, nil, &identity, "")
}

// ClaimConversationTurnOutboxExcluding is used by the bounded foreground
// drain so one turn+target is claimed at most once in that call.
func (s *L1SQLiteStore) ClaimConversationTurnOutboxExcluding(ctx context.Context, turnID string, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return s.claimConversationTurnOutboxWithExclusions(ctx, turnID, now, leaseDuration, excluded, nil, "")
}

// ClaimNextConversationTurnOutbox claims the oldest pending or stale-running
// outbox target across all turns. Background workers use target-scoped claims
// so Redis projection and thread followers have independent retry ownership.
func (s *L1SQLiteStore) ClaimNextConversationTurnOutbox(ctx context.Context, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	return s.claimConversationTurnOutbox(ctx, "", now, leaseDuration)
}

// ClaimNextConversationTurnOutboxForOperation persists the caller's
// operation receipt in the same transaction as the selected lease.
func (s *L1SQLiteStore) ClaimNextConversationTurnOutboxForOperation(ctx context.Context, identity ConversationTurnOperationIdentity, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	return s.ClaimNextConversationTurnOutboxTargetExcludingForOperation(ctx, identity, "", now, leaseDuration, nil)
}

// ClaimNextConversationTurnOutboxTargetExcludingForOperation persists the
// exact target filter and exclusions with the durable claim receipt.
func (s *L1SQLiteStore) ClaimNextConversationTurnOutboxTargetExcludingForOperation(ctx context.Context, identity ConversationTurnOperationIdentity, target string, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	if identity.Operation != "outbox_claim_next" {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return s.claimConversationTurnOutboxWithExclusions(ctx, "", now, leaseDuration, excluded, &identity, target)
}

// GetConversationTurnClaimOperationReceipt returns only a claim result bound
// to this exact caller operation and payload hash. A missing receipt is not
// proof that a claim did not commit.
func (s *L1SQLiteStore) GetConversationTurnClaimOperationReceipt(ctx context.Context, identity ConversationTurnOperationIdentity) (*domconv.ConversationTurnOutbox, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, domconv.ErrConversationTurnUnavailable
	}
	return lookupConversationTurnClaimOperationReceipt(ctx, s.db, identity)
}

// ClaimNextConversationTurnOutboxExcluding is the drain-only variant that
// prevents a failed target from being claimed again in the same bounded call.
// The normal claim primitive still exposes retryable failed rows to later
// calls.
func (s *L1SQLiteStore) ClaimNextConversationTurnOutboxExcluding(ctx context.Context, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	return s.claimConversationTurnOutboxWithExclusions(ctx, "", now, leaseDuration, excluded, nil, "")
}

// ClaimNextConversationTurnOutboxTargetExcluding is the bounded worker claim
// used to keep Redis projection work separate from thread followers.
func (s *L1SQLiteStore) ClaimNextConversationTurnOutboxTargetExcluding(ctx context.Context, target string, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	if target != "" && !validConversationTurnOutboxTarget(target) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return s.claimConversationTurnOutboxWithExclusions(ctx, "", now, leaseDuration, excluded, nil, target)
}

func (s *L1SQLiteStore) claimConversationTurnOutbox(ctx context.Context, turnID string, now time.Time, leaseDuration time.Duration) (*domconv.ConversationTurnOutbox, error) {
	return s.claimConversationTurnOutboxWithExclusions(ctx, turnID, now, leaseDuration, nil, nil, "")
}

func (s *L1SQLiteStore) claimConversationTurnOutboxWithExclusions(ctx context.Context, turnID string, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}, identity *ConversationTurnOperationIdentity, targetFilter string) (*domconv.ConversationTurnOutbox, error) {
	if s == nil || s.db == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	if targetFilter != "" && !validConversationTurnOutboxTarget(targetFilter) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	for key := range excluded {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 || modulecore.TurnID(parts[0]).Validate() != nil || !validConversationTurnOutboxTarget(parts[1]) {
			return nil, domconv.ErrConversationTurnInvalid
		}
	}
	if identity != nil {
		if err := identity.validate(); err != nil || (identity.Operation == "outbox_claim" && turnID == "") ||
			(identity.Operation == "outbox_claim" && targetFilter != "" && !validConversationTurnOutboxTarget(targetFilter)) ||
			(identity.Operation == "outbox_claim_next" && turnID != "") ||
			(identity.Operation != "outbox_claim_next" && len(excluded) > 0) {
			return nil, domconv.ErrConversationTurnInvalid
		}
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
	if identity != nil {
		existing, found, err := lookupConversationTurnClaimOperationReceipt(ctx, tx, *identity)
		if err != nil {
			code := domconv.ConversationTurnErrorCodeOf(err)
			if code == "" {
				code = domconv.ConversationTurnErrorInternal
			}
			return rollback(code)
		}
		if found {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return nil, domconv.ErrConversationTurnInternal
			}
			return existing, nil
		}
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
	if targetFilter != "" {
		where += "target = ? AND "
		args = append(args, targetFilter)
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
	where += `(target <> 'thread_followers' OR EXISTS (
		SELECT 1 FROM conversation_turn_receipt r
		WHERE r.turn_id = conversation_turn_outbox.turn_id AND r.response_published_at IS NOT NULL
	)) AND (
	status = 'pending' OR
	(status = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at <= ? AND attempts < ?) OR
	(status = 'failed' AND attempts < ?)
)`
	args = append(args, now, domconv.ConversationTurnMaxOutboxAttempts, domconv.ConversationTurnMaxOutboxAttempts)
	query := `
	SELECT turn_id, trace_id, root_task_id, target, session_id, thread_id, thread_seq, thread_kind,
		closed_thread_id, closed_thread_seq, closed_thread_kind, payload_sha256,
	       payload_json, status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at
FROM conversation_turn_outbox
` + where + `
ORDER BY created_at ASC, turn_id ASC, target ASC
LIMIT 1`
	row := tx.QueryRowContext(ctx, query, args...)
	outbox, err := scanConversationTurnOutbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		if identity != nil {
			if err := insertConversationTurnOperationReceipt(ctx, tx, *identity, "", "", "", conversationTurnClaimOperationResult{}); err != nil {
				return rollback(domconv.ConversationTurnErrorInternal)
			}
		}
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
	) AND (target <> 'thread_followers' OR EXISTS (
		SELECT 1 FROM conversation_turn_receipt r
		WHERE r.turn_id = conversation_turn_outbox.turn_id AND r.response_published_at IS NOT NULL
	))`, domconv.ConversationTurnOutboxRunning, leaseToken, expires, now, outbox.TurnID, outbox.Target, now, domconv.ConversationTurnMaxOutboxAttempts, domconv.ConversationTurnMaxOutboxAttempts)
	if err != nil {
		return rollback(domconv.ConversationTurnErrorInternal)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return rollback(domconv.ConversationTurnErrorConflict)
	}
	outbox.Status = domconv.ConversationTurnOutboxRunning
	outbox.LeaseToken = leaseToken
	outbox.LeaseExpiresAt = expires
	outbox.Attempts++
	outbox.UpdatedAt = now
	if identity != nil {
		result := conversationTurnClaimOperationResult{Outbox: outbox, LeaseToken: leaseToken}
		if err := insertConversationTurnOperationReceipt(ctx, tx, *identity, string(outbox.TurnID), outbox.Target, leaseToken, result); err != nil {
			return rollback(domconv.ConversationTurnErrorInternal)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	return outbox, nil
}

// terminalizeExhaustedConversationTurnOutbox closes stale leases that have
// already consumed the bounded retry budget. A worker crash must not make an
// attempts==Max row claimable forever. The durable turn receipt remains
// partial because its L1 transaction committed; only follower delivery ended.
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
	return s.finishConversationTurnOutbox(ctx, turnID, target, leaseToken, "", now, nil)
}

// CompleteConversationTurnOutboxForOperation stores the exact result and
// lease identity atomically with completion.
func (s *L1SQLiteStore) CompleteConversationTurnOutboxForOperation(ctx context.Context, identity ConversationTurnOperationIdentity, turnID, target, leaseToken string, now time.Time) (domconv.ConversationTurnResult, error) {
	if identity.Operation != "complete" {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInvalid
	}
	return s.finishConversationTurnOutbox(ctx, turnID, target, leaseToken, "", now, &identity)
}

func (s *L1SQLiteStore) FailConversationTurnOutbox(ctx context.Context, turnID, target, leaseToken string, code domconv.ConversationTurnErrorCode, now time.Time) (domconv.ConversationTurnResult, error) {
	if !validConversationTurnErrorCode(code) {
		return domconv.ConversationTurnResult{TurnID: modulecore.TurnID(strings.TrimSpace(turnID)), Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorInvalid}, domconv.ErrConversationTurnInvalid
	}
	return s.finishConversationTurnOutbox(ctx, turnID, target, leaseToken, code, now, nil)
}

// FailConversationTurnOutboxForOperation stores the exact result and lease
// identity atomically with failure.
func (s *L1SQLiteStore) FailConversationTurnOutboxForOperation(ctx context.Context, identity ConversationTurnOperationIdentity, turnID, target, leaseToken string, code domconv.ConversationTurnErrorCode, now time.Time) (domconv.ConversationTurnResult, error) {
	if identity.Operation != "fail" || !validConversationTurnErrorCode(code) {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInvalid
	}
	return s.finishConversationTurnOutbox(ctx, turnID, target, leaseToken, code, now, &identity)
}

// GetConversationTurnFinishOperationReceipt proves a finish result only when
// operation, payload hash, turn, target, and lease all match.
func (s *L1SQLiteStore) GetConversationTurnFinishOperationReceipt(ctx context.Context, identity ConversationTurnOperationIdentity, turnID, target, leaseToken string) (domconv.ConversationTurnResult, bool, error) {
	if s == nil || s.db == nil {
		return domconv.ConversationTurnResult{}, false, domconv.ErrConversationTurnUnavailable
	}
	return lookupConversationTurnFinishOperationReceipt(ctx, s.db, identity, turnID, target, leaseToken)
}

func (s *L1SQLiteStore) finishConversationTurnOutbox(ctx context.Context, turnID, target, leaseToken string, failureCode domconv.ConversationTurnErrorCode, now time.Time, identity *ConversationTurnOperationIdentity) (domconv.ConversationTurnResult, error) {
	turnID = strings.TrimSpace(turnID)
	target = strings.TrimSpace(target)
	leaseToken = strings.TrimSpace(leaseToken)
	if turnID == "" || (domconv.ConversationTurnTarget(target) != domconv.ConversationTurnTargetRedisProjection && domconv.ConversationTurnTarget(target) != domconv.ConversationTurnTargetThreadFollowers) || leaseToken == "" {
		return domconv.ConversationTurnResult{TurnID: modulecore.TurnID(turnID), Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorInvalid}, domconv.ErrConversationTurnInvalid
	}
	if s == nil || s.db == nil {
		return domconv.ConversationTurnResult{TurnID: modulecore.TurnID(turnID), Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorUnavailable}, domconv.ErrConversationTurnUnavailable
	}
	if identity != nil {
		if err := identity.validate(); err != nil || (identity.Operation == "complete" && failureCode != "") ||
			(identity.Operation == "fail" && failureCode == "") {
			return domconv.ConversationTurnResult{TurnID: modulecore.TurnID(turnID), Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorInvalid}, domconv.ErrConversationTurnInvalid
		}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domconv.ConversationTurnResult{TurnID: modulecore.TurnID(turnID), Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorInternal}, domconv.ErrConversationTurnInternal
	}
	rollback := func(code domconv.ConversationTurnErrorCode) (domconv.ConversationTurnResult, error) {
		_ = tx.Rollback()
		return domconv.ConversationTurnResult{TurnID: modulecore.TurnID(turnID), Status: domconv.ConversationTurnFailed, ErrorCode: code}, conversationTurnError(code)
	}
	if identity != nil {
		existing, found, err := lookupConversationTurnFinishOperationReceipt(ctx, tx, *identity, turnID, target, leaseToken)
		if err != nil {
			code := domconv.ConversationTurnErrorCodeOf(err)
			if code == "" {
				code = domconv.ConversationTurnErrorInternal
			}
			return rollback(code)
		}
		if found {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return domconv.ConversationTurnResult{TurnID: modulecore.TurnID(turnID), Status: domconv.ConversationTurnFailed, ErrorCode: domconv.ConversationTurnErrorInternal}, domconv.ErrConversationTurnInternal
			}
			return existing, nil
		}
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
		if identity != nil {
			if err := insertConversationTurnOperationReceipt(ctx, tx, *identity, turnID, target, leaseToken, result); err != nil {
				return rollback(domconv.ConversationTurnErrorInternal)
			}
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
	if identity != nil {
		if err := insertConversationTurnOperationReceipt(ctx, tx, *identity, turnID, target, leaseToken, result); err != nil {
			return rollback(domconv.ConversationTurnErrorInternal)
		}
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
	var traceID, rootTaskID, userMessageID, agentMessageID string
	var threadID, threadKind string
	var threadSeq int64
	var closedID, closedKind string
	var closedSeq int64
	if err := s.db.QueryRowContext(ctx, `
SELECT trace_id, root_task_id, user_message_id, agent_message_id,
	thread_id, thread_seq, thread_kind, closed_thread_id, closed_thread_seq, closed_thread_kind, result_json
FROM conversation_turn_receipt WHERE turn_id = ?`, turnID).Scan(&traceID, &rootTaskID, &userMessageID, &agentMessageID,
		&threadID, &threadSeq, &threadKind, &closedID, &closedSeq, &closedKind, &resultJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnUnavailable
		}
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInternal
	}
	var result domconv.ConversationTurnResult
	if json.Unmarshal([]byte(resultJSON), &result) != nil {
		return domconv.ConversationTurnResult{}, domconv.ErrConversationTurnInternal
	}
	if err := validateConversationTurnResultIdentity(result, modulecore.TurnID(turnID), modulecore.TraceID(traceID), modulecore.TaskID(rootTaskID), modulecore.MessageID(userMessageID), modulecore.MessageID(agentMessageID), modulecore.ThreadID(threadID), modulecore.ThreadSeq(threadSeq), modulecore.ThreadKind(threadKind), modulecore.ThreadID(closedID), modulecore.ThreadSeq(closedSeq), modulecore.ThreadKind(closedKind)); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	return result, nil
}

// MarkConversationTurnResponsePublished records the canonical-publication
// boundary for the exact turn/trace/session tuple. Calls that do not correspond to
// a closed-thread follower are harmless no-ops because many public responses
// have no deferred thread work.
func (s *L1SQLiteStore) MarkConversationTurnResponsePublished(ctx context.Context, turnID modulecore.TurnID, traceID modulecore.TraceID, sessionID modulecore.SessionID, at time.Time) error {
	if turnID.Validate() != nil || traceID.Validate() != nil || sessionID.Validate() != nil {
		return domconv.ErrConversationTurnInvalid
	}
	if s == nil || s.db == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	at = at.UTC()
	_, err := s.db.ExecContext(ctx, `
UPDATE conversation_turn_receipt
SET response_published_at = COALESCE(response_published_at, ?), updated_at = ?
WHERE turn_id = ? AND trace_id = ? AND session_id = ?
	AND EXISTS (
		SELECT 1 FROM conversation_turn_outbox o
		WHERE o.turn_id = conversation_turn_receipt.turn_id
			AND o.target = 'thread_followers' AND o.closed_thread_id <> ''
	)`, at, at, string(turnID), string(traceID), string(sessionID))
	if err != nil {
		return domconv.ErrConversationTurnInternal
	}
	return nil
}

// IsConversationTurnResponsePublished reads the exact durable marker used to
// reconcile an uncertain storage-host response_published call.
func (s *L1SQLiteStore) IsConversationTurnResponsePublished(ctx context.Context, turnID modulecore.TurnID, traceID modulecore.TraceID, sessionID modulecore.SessionID) (bool, error) {
	if turnID.Validate() != nil || traceID.Validate() != nil || sessionID.Validate() != nil {
		return false, domconv.ErrConversationTurnInvalid
	}
	if s == nil || s.db == nil {
		return false, domconv.ErrConversationTurnUnavailable
	}
	var published int
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM conversation_turn_receipt r
	JOIN conversation_turn_outbox o ON o.turn_id = r.turn_id
	WHERE r.turn_id = ? AND r.trace_id = ? AND r.session_id = ?
		AND r.response_published_at IS NOT NULL
		AND o.target = 'thread_followers' AND o.closed_thread_id <> ''
	)`, string(turnID), string(traceID), string(sessionID)).Scan(&published)
	if err != nil {
		return false, domconv.ErrConversationTurnInternal
	}
	return published == 1, nil
}

// ListPendingConversationTurnThreadFollowers returns a bounded deterministic
// set for Recall and crash-window reconciliation. Completed followers are
// excluded; the response marker remains visible for retries and restart.
func (s *L1SQLiteStore) ListPendingConversationTurnThreadFollowers(ctx context.Context, sessionID string, limit int) ([]PendingConversationTurnThreadFollower, error) {
	if s == nil || s.db == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" && modulecore.SessionID(sessionID).Validate() != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	if limit <= 0 || limit > 100 {
		return nil, domconv.ErrConversationTurnInvalid
	}
	query := `
SELECT r.turn_id, r.trace_id, r.agent_message_id, r.session_id, r.closed_thread_id,
	CASE WHEN r.response_published_at IS NULL THEN 0 ELSE 1 END
FROM conversation_turn_receipt r
JOIN conversation_turn_outbox o ON o.turn_id = r.turn_id
WHERE o.target = 'thread_followers' AND o.status <> 'completed' AND r.closed_thread_id <> ''`
	args := make([]interface{}, 0, 2)
	if sessionID != "" {
		query += ` AND r.session_id = ?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY r.closed_thread_seq DESC, r.turn_id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	defer rows.Close()
	items := make([]PendingConversationTurnThreadFollower, 0, limit)
	for rows.Next() {
		var item PendingConversationTurnThreadFollower
		var published int
		if err := rows.Scan(&item.TurnID, &item.TraceID, &item.AgentMessageID, &item.SessionID, &item.ClosedThreadID, &published); err != nil {
			return nil, domconv.ErrConversationTurnInternal
		}
		if item.TurnID.Validate() != nil || item.TraceID.Validate() != nil || item.AgentMessageID.Validate() != nil ||
			modulecore.SessionID(item.SessionID).Validate() != nil || item.ClosedThreadID.Validate() != nil || (published != 0 && published != 1) {
			return nil, domconv.ErrConversationTurnInvalid
		}
		item.ResponsePublished = published == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	return items, nil
}

// HasConversationTurnReceiptsForSession reports whether a session contains
// any canonical turn receipt, regardless of follower completion status.
func (s *L1SQLiteStore) HasConversationTurnReceiptsForSession(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, domconv.ErrConversationTurnUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || modulecore.SessionID(sessionID).Validate() != nil {
		return false, domconv.ErrConversationTurnInvalid
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_turn_receipt WHERE session_id = ?)`, sessionID).Scan(&exists); err != nil {
		return false, domconv.ErrConversationTurnInternal
	}
	if exists != 0 && exists != 1 {
		return false, domconv.ErrConversationTurnInvalid
	}
	return exists == 1, nil
}

// ListUnpublishedConversationTurnThreadFollowersAfter returns one bounded
// keyset page of receipts that still need exact EventStore reconciliation.
// Published receipts are excluded so they cannot occupy every recovery page.
func (s *L1SQLiteStore) ListUnpublishedConversationTurnThreadFollowersAfter(ctx context.Context, afterTurnID string, limit int) ([]PendingConversationTurnThreadFollower, error) {
	if s == nil || s.db == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	afterTurnID = strings.TrimSpace(afterTurnID)
	if afterTurnID != "" && modulecore.TurnID(afterTurnID).Validate() != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	if limit <= 0 || limit > 100 {
		return nil, domconv.ErrConversationTurnInvalid
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT r.turn_id, r.trace_id, r.agent_message_id, r.session_id, r.closed_thread_id, 0
FROM conversation_turn_receipt r
JOIN conversation_turn_outbox o ON o.turn_id = r.turn_id
WHERE o.target = 'thread_followers' AND o.status <> 'completed' AND r.closed_thread_id <> ''
	AND r.response_published_at IS NULL AND r.turn_id > ?
ORDER BY r.turn_id ASC LIMIT ?`, afterTurnID, limit)
	if err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	defer rows.Close()
	items := make([]PendingConversationTurnThreadFollower, 0, limit)
	for rows.Next() {
		var item PendingConversationTurnThreadFollower
		var published int
		if err := rows.Scan(&item.TurnID, &item.TraceID, &item.AgentMessageID, &item.SessionID, &item.ClosedThreadID, &published); err != nil {
			return nil, domconv.ErrConversationTurnInternal
		}
		if item.TurnID.Validate() != nil || item.TraceID.Validate() != nil || item.AgentMessageID.Validate() != nil ||
			modulecore.SessionID(item.SessionID).Validate() != nil || item.ClosedThreadID.Validate() != nil || published != 0 {
			return nil, domconv.ErrConversationTurnInvalid
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	return items, nil
}

func validateConversationTurnResultIdentity(result domconv.ConversationTurnResult, turnID modulecore.TurnID, traceID modulecore.TraceID, rootTaskID modulecore.TaskID, userMessageID modulecore.MessageID, agentMessageID modulecore.MessageID, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, closedID modulecore.ThreadID, closedSeq modulecore.ThreadSeq, closedKind modulecore.ThreadKind) error {
	if turnID.Validate() != nil || traceID.Validate() != nil || rootTaskID.Validate() != nil || userMessageID.Validate() != nil || agentMessageID.Validate() != nil || userMessageID == agentMessageID {
		return domconv.ErrConversationTurnInvalid
	}
	if err := validateL1ThreadTuple(threadID, threadSeq, threadKind); err != nil || threadID == "" {
		return domconv.ErrConversationTurnInvalid
	}
	if err := validateL1ThreadTuple(closedID, closedSeq, closedKind); err != nil {
		return domconv.ErrConversationTurnInvalid
	}
	if result.TurnID != turnID || result.TraceID != traceID || result.RootTaskID != rootTaskID || result.UserMessageID != userMessageID || result.AgentMessageID != agentMessageID || len(result.MessageIDs) != 2 || result.MessageIDs[0] != string(userMessageID) || result.MessageIDs[1] != string(agentMessageID) || result.ThreadID != threadID || result.ThreadSeq != threadSeq || result.ThreadKind != threadKind || result.ClosedThreadID != closedID || result.ClosedThreadSeq != closedSeq || result.ClosedThreadKind != closedKind {
		return domconv.ErrConversationTurnInvalid
	}
	return nil
}

// LoadConversationThreadProjection loads the canonical conversation messages
// for one exact L1 thread. The L1 rows, rather than Redis, are authoritative.
// The returned rows retain their bodies for the summarizer but callers must
// never copy those bodies into an outbox payload.
func (s *L1SQLiteStore) LoadConversationThreadProjection(ctx context.Context, sessionID string, threadID modulecore.ThreadID) ([]L1MemoryEvent, error) {
	if s == nil || s.db == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !utf8.ValidString(sessionID) || threadID.Validate() != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	return loadConversationThreadProjection(ctx, s.db, sessionID, threadID)
}

func loadConversationThreadProjection(ctx context.Context, query conversationThreadProjectionReader, sessionID string, threadID modulecore.ThreadID) ([]L1MemoryEvent, error) {
	return loadConversationThreadProjectionMode(ctx, query, sessionID, threadID, false)
}

func loadConversationThreadProjectionAllowEmpty(ctx context.Context, query conversationThreadProjectionReader, sessionID string, threadID modulecore.ThreadID) ([]L1MemoryEvent, error) {
	return loadConversationThreadProjectionMode(ctx, query, sessionID, threadID, true)
}

func loadConversationThreadProjectionMode(ctx context.Context, query conversationThreadProjectionReader, sessionID string, threadID modulecore.ThreadID, allowEmpty bool) ([]L1MemoryEvent, error) {
	rows, err := query.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE session_id = ? AND thread_id = ?
	ORDER BY rowid ASC
	LIMIT 13`, sessionID, string(threadID))
	if err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	defer rows.Close()
	events := make([]L1MemoryEvent, 0, 12)
	for rows.Next() {
		var event L1MemoryEvent
		var speaker, metaJSON string
		if err := rows.Scan(&event.ID, &event.Namespace, &event.SessionID, &event.ThreadID, &event.ThreadSeq, &event.ThreadKind, &speaker,
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
	if allowEmpty && len(events) == 0 {
		return events, nil
	}
	if err := validateConversationThreadProjection(sessionID, threadID, events); err != nil {
		return nil, err
	}
	return events, nil
}

// LatestConversationThreadReference returns the durable active head when one
// exists, including an empty reset head. Older sessions without active metadata
// fall back to their latest canonical message thread; unthreaded rows are ignored.
func (s *L1SQLiteStore) LatestConversationThreadReference(ctx context.Context, sessionID string) (modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, bool, error) {
	if s == nil || s.db == nil {
		return "", 0, "", false, domconv.ErrConversationTurnUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !utf8.ValidString(sessionID) {
		return "", 0, "", false, domconv.ErrConversationTurnInvalid
	}
	var rawID, rawKind string
	var rawSeq int64
	err := s.db.QueryRowContext(ctx, `
SELECT thread_id, thread_seq, thread_kind
FROM conversation_active_thread
WHERE session_id = ?`, sessionID).Scan(&rawID, &rawSeq, &rawKind)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `
SELECT thread_id, thread_seq, thread_kind
FROM l1_memory_event
WHERE session_id = ? AND thread_id <> ''
	ORDER BY thread_seq DESC, rowid DESC
LIMIT 1`, sessionID).Scan(&rawID, &rawSeq, &rawKind)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, "", false, nil
	}
	if err != nil {
		return "", 0, "", false, domconv.ErrConversationTurnInternal
	}
	threadID := modulecore.ThreadID(rawID)
	threadSeq := modulecore.ThreadSeq(rawSeq)
	threadKind := modulecore.ThreadKind(rawKind)
	if err := validateL1BoundThreadTuple(threadID, threadSeq, threadKind); err != nil {
		return "", 0, "", false, domconv.ErrConversationTurnInvalid
	}
	return threadID, threadSeq, threadKind, true, nil
}

// ResetConversationHead atomically advances the durable active thread to an
// empty generation. Existing messages and turn records remain retained; any
// unfinished outbox work must finish before the generation can change.
func (s *L1SQLiteStore) ResetConversationHead(ctx context.Context, request domconv.ConversationHeadResetRequest) (domconv.ConversationHeadResetReceipt, error) {
	normalized, err := domconv.NormalizeConversationHeadResetRequest(request)
	if err != nil {
		return domconv.ConversationHeadResetReceipt{}, err
	}
	payloadHash, err := domconv.ConversationHeadResetPayloadSHA256(normalized)
	if err != nil {
		return domconv.ConversationHeadResetReceipt{}, domconv.ErrConversationTurnInvalid
	}
	base := domconv.ConversationHeadResetReceipt{
		OperationID: normalized.OperationID, SessionID: normalized.SessionID, OwnerID: normalized.OwnerID,
		PayloadSHA256: payloadHash,
	}
	if ctx == nil {
		return base, domconv.ErrConversationTurnInvalid
	}
	if s == nil || s.db == nil {
		return base, domconv.ErrConversationTurnUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return base, domconv.ErrConversationTurnUnavailable
	}
	rollback := func(cause error) (domconv.ConversationHeadResetReceipt, error) {
		_ = tx.Rollback()
		if cause != nil {
			return base, cause
		}
		return base, domconv.ErrConversationTurnInternal
	}

	var storedHash, storedSessionID, storedOwnerID string
	var previousID, previousKind, activeID, activeKind, storedDomain string
	var previousSeq, activeSeq int64
	var resetAt time.Time
	err = tx.QueryRowContext(ctx, `
SELECT payload_sha256, session_id, owner_id, previous_thread_id, previous_thread_seq, previous_thread_kind,
       active_thread_id, active_thread_seq, active_thread_kind, domain, reset_at
FROM conversation_head_reset_receipt
WHERE operation_id = ?`, normalized.OperationID).Scan(
		&storedHash, &storedSessionID, &storedOwnerID, &previousID, &previousSeq, &previousKind,
		&activeID, &activeSeq, &activeKind, &storedDomain, &resetAt,
	)
	if err == nil {
		if storedHash != payloadHash || storedSessionID != normalized.SessionID || storedOwnerID != normalized.OwnerID {
			return rollback(domconv.ErrConversationTurnConflict)
		}
		previous := domconv.ConversationTurnThreadIdentity{ID: modulecore.ThreadID(previousID), Seq: modulecore.ThreadSeq(previousSeq), Kind: modulecore.ThreadKind(previousKind)}
		active := domconv.ConversationTurnThreadIdentity{ID: modulecore.ThreadID(activeID), Seq: modulecore.ThreadSeq(activeSeq), Kind: modulecore.ThreadKind(activeKind)}
		if validateL1BoundThreadTuple(previous.ID, previous.Seq, previous.Kind) != nil ||
			validateL1BoundThreadTuple(active.ID, active.Seq, active.Kind) != nil ||
			previous.ID != normalized.ExpectedThreadID || previous.Seq != normalized.ExpectedThreadSeq ||
			active.ID == previous.ID || active.Seq != previous.Seq+1 || active.Kind != previous.Kind ||
			storedDomain == "" || !utf8.ValidString(storedDomain) || strings.IndexByte(storedDomain, 0) >= 0 || resetAt.IsZero() {
			return rollback(domconv.ErrConversationTurnInvalid)
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return base, domconv.ErrConversationTurnInternal
		}
		replay := base
		replay.PreviousThread = previous
		replay.ActiveThread = active
		replay.Domain = storedDomain
		replay.ResetAt = resetAt
		replay.IdempotentReplay = true
		return replay, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(domconv.ErrConversationTurnInternal)
	}

	var currentID, currentKind, domain string
	var currentSeq int64
	var messageCount int64
	if err := tx.QueryRowContext(ctx, `
SELECT thread_id, thread_seq, thread_kind, domain, message_count
FROM conversation_active_thread
WHERE session_id = ?`, normalized.SessionID).Scan(&currentID, &currentSeq, &currentKind, &domain, &messageCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rollback(domconv.ErrConversationTurnConflict)
		}
		return rollback(domconv.ErrConversationTurnInternal)
	}
	current := domconv.ConversationTurnThreadIdentity{ID: modulecore.ThreadID(currentID), Seq: modulecore.ThreadSeq(currentSeq), Kind: modulecore.ThreadKind(currentKind)}
	if validateL1BoundThreadTuple(current.ID, current.Seq, current.Kind) != nil || messageCount < 0 || messageCount > 12 ||
		domain == "" || !utf8.ValidString(domain) || strings.IndexByte(domain, 0) >= 0 {
		return rollback(domconv.ErrConversationTurnInvalid)
	}
	if current.ID != normalized.ExpectedThreadID || current.Seq != normalized.ExpectedThreadSeq {
		return rollback(domconv.ErrConversationTurnConflict)
	}
	if current.Seq == modulecore.ThreadSeq(1<<63-1) {
		return rollback(domconv.ErrConversationTurnConflict)
	}
	var pendingOutbox int
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
	SELECT 1 FROM conversation_turn_outbox
	WHERE session_id = ? AND status <> 'completed'
)`, normalized.SessionID).Scan(&pendingOutbox); err != nil {
		return rollback(domconv.ErrConversationTurnInternal)
	}
	if pendingOutbox != 0 && pendingOutbox != 1 {
		return rollback(domconv.ErrConversationTurnInvalid)
	}
	if pendingOutbox == 1 {
		return rollback(domconv.ErrConversationTurnConflict)
	}

	active := domconv.ConversationTurnThreadIdentity{
		ID: modulecore.NewThreadID(), Seq: current.Seq + 1, Kind: current.Kind,
	}
	if active.ID == current.ID || validateL1BoundThreadTuple(active.ID, active.Seq, active.Kind) != nil {
		return rollback(domconv.ErrConversationTurnInternal)
	}
	now := time.Now().UTC()
	update, err := tx.ExecContext(ctx, `
UPDATE conversation_active_thread
SET thread_id = ?, thread_seq = ?, message_count = 0, updated_at = ?
WHERE session_id = ? AND thread_id = ? AND thread_seq = ?`, active.ID, active.Seq, now,
		normalized.SessionID, normalized.ExpectedThreadID, normalized.ExpectedThreadSeq)
	if err != nil {
		return rollback(domconv.ErrConversationTurnInternal)
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return rollback(domconv.ErrConversationTurnInternal)
	}
	if affected != 1 {
		return rollback(domconv.ErrConversationTurnConflict)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_head_reset_receipt (
	operation_id, payload_sha256, session_id, owner_id,
	previous_thread_id, previous_thread_seq, previous_thread_kind,
	active_thread_id, active_thread_seq, active_thread_kind, domain, reset_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, normalized.OperationID, payloadHash, normalized.SessionID, normalized.OwnerID,
		current.ID, current.Seq, current.Kind, active.ID, active.Seq, active.Kind, domain, now); err != nil {
		return rollback(domconv.ErrConversationTurnInternal)
	}
	if err := tx.Commit(); err != nil {
		return base, domconv.ErrConversationTurnInternal
	}
	base.PreviousThread = current
	base.ActiveThread = active
	base.Domain = domain
	base.ResetAt = now
	return base, nil
}

// LoadActiveConversationHead reads metadata and retained messages in one
// read-only snapshot. OwnerID is the caller's already validated session scope.
func (s *L1SQLiteStore) LoadActiveConversationHead(ctx context.Context, sessionID, ownerID string) (domconv.ConversationHeadSnapshot, error) {
	if ctx == nil {
		return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnInvalid
	}
	if s == nil || s.readDB == nil {
		return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || modulecore.SessionID(sessionID).Validate() != nil || domconv.ValidateConversationHeadOwnerID(ownerID) != nil {
		return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnInvalid
	}
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnUnavailable
	}
	defer tx.Rollback()
	var threadID, threadKind, domain string
	var threadSeq, messageCount int64
	var updatedAt time.Time
	if err := tx.QueryRowContext(ctx, `
SELECT thread_id, thread_seq, thread_kind, domain, message_count, updated_at
FROM conversation_active_thread
WHERE session_id = ?`, sessionID).Scan(&threadID, &threadSeq, &threadKind, &domain, &messageCount, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domconv.ConversationHeadSnapshot{}, domconv.ErrThreadNotFound
		}
		return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnInternal
	}
	thread := domconv.ConversationTurnThreadIdentity{ID: modulecore.ThreadID(threadID), Seq: modulecore.ThreadSeq(threadSeq), Kind: modulecore.ThreadKind(threadKind)}
	if validateL1BoundThreadTuple(thread.ID, thread.Seq, thread.Kind) != nil || messageCount < 0 || messageCount > 12 ||
		domain == "" || !utf8.ValidString(domain) || strings.IndexByte(domain, 0) >= 0 || updatedAt.IsZero() {
		return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnInvalid
	}
	events, err := loadConversationThreadProjectionAllowEmpty(ctx, tx, sessionID, thread.ID)
	if err != nil {
		return domconv.ConversationHeadSnapshot{}, err
	}
	if int64(len(events)) != messageCount {
		return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnInvalid
	}
	for _, event := range events {
		if event.ThreadSeq != thread.Seq || event.ThreadKind != thread.Kind {
			return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnInvalid
		}
		if eventDomain, ok := event.Meta["domain"].(string); !ok || eventDomain != domain {
			return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnInvalid
		}
	}
	messages := make([]domconv.ConversationHeadMessage, len(events))
	for index, event := range events {
		messages[index] = domconv.ConversationHeadMessage{
			ID: modulecore.MessageID(event.ID), Speaker: event.Speaker, Message: event.Message,
			Meta: event.Meta, CreatedAt: event.CreatedAt, UpdatedAt: event.UpdatedAt,
		}
	}
	if err := tx.Commit(); err != nil {
		return domconv.ConversationHeadSnapshot{}, domconv.ErrConversationTurnInternal
	}
	return domconv.ConversationHeadSnapshot{
		SessionID: sessionID, OwnerID: ownerID, Thread: thread, Domain: domain,
		MessageCount: messageCount, Messages: messages, UpdatedAt: updatedAt,
	}, nil
}

// LoadActiveConversationThreadProjection resolves the active thread ID from
// conversation_active_thread before loading the projection. Both reads share
// one read-pool transaction so the writer connection is never queued behind
// this cold-head read and active metadata cannot race its message snapshot.
func (s *L1SQLiteStore) LoadActiveConversationThreadProjection(ctx context.Context, sessionID string) ([]L1MemoryEvent, error) {
	if s == nil || s.readDB == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !utf8.ValidString(sessionID) {
		return nil, domconv.ErrConversationTurnInvalid
	}
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	defer tx.Rollback()
	var threadID modulecore.ThreadID
	var threadSeq modulecore.ThreadSeq
	var threadKind modulecore.ThreadKind
	var messageCount int
	var activeDomain string
	if err := tx.QueryRowContext(ctx, `
SELECT thread_id, thread_seq, thread_kind, message_count, domain
FROM conversation_active_thread
	WHERE session_id = ?`, sessionID).Scan(&threadID, &threadSeq, &threadKind, &messageCount, &activeDomain); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domconv.ErrThreadNotFound
		}
		return nil, domconv.ErrConversationTurnInternal
	}
	if err := validateL1BoundThreadTuple(threadID, threadSeq, threadKind); err != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	events, err := loadConversationThreadProjection(ctx, tx, sessionID, threadID)
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
		if event.ThreadSeq != threadSeq || event.ThreadKind != threadKind {
			return nil, domconv.ErrConversationTurnInvalid
		}
		if domain, ok := event.Meta["domain"].(string); !ok || domain != activeDomain {
			return nil, domconv.ErrConversationTurnInvalid
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, domconv.ErrConversationTurnInternal
	}
	return events, nil
}

func validateConversationThreadProjection(sessionID string, threadID modulecore.ThreadID, events []L1MemoryEvent) error {
	if len(events) < 2 || len(events) > 12 || len(events)%2 != 0 {
		return domconv.ErrConversationTurnInvalid
	}
	expectedSeq := events[0].ThreadSeq
	expectedKind := events[0].ThreadKind
	if err := validateL1BoundThreadTuple(threadID, expectedSeq, expectedKind); err != nil {
		return domconv.ErrConversationTurnInvalid
	}
	var domain string
	for index, event := range events {
		if event.ID == "" || !utf8.ValidString(event.ID) || strings.IndexByte(event.ID, 0) >= 0 ||
			event.SessionID != sessionID || event.ThreadID != threadID || event.ThreadSeq != expectedSeq || event.ThreadKind != expectedKind || event.Namespace != "conv:"+string(threadID) ||
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
		if modulecore.TurnID(metaStrings["turn_id"]).Validate() != nil || modulecore.MessageID(metaStrings["message_id"]).Validate() != nil {
			return domconv.ErrConversationTurnInvalid
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
			if modulecore.MessageID(previous.ID).Validate() != nil || modulecore.MessageID(event.ID).Validate() != nil {
				return domconv.ErrConversationTurnInvalid
			}
		}
	}
	if domain == "" {
		return domconv.ErrConversationTurnInvalid
	}
	return nil
}

func selectConversationTurnThread(ctx context.Context, tx *sql.Tx, request domconv.ConversationTurnRequest, _ time.Time) (thread, closed conversationThreadIdentity, messageCount int64, domain string, err error) {
	var currentID string
	var currentSeq int64
	var currentKind string
	var currentDomain string
	var currentCount int
	err = tx.QueryRowContext(ctx, `
SELECT thread_id, thread_seq, thread_kind, domain, message_count
FROM conversation_active_thread
WHERE session_id = ?`, request.SessionID).Scan(&currentID, &currentSeq, &currentKind, &currentDomain, &currentCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return conversationThreadIdentity{}, conversationThreadIdentity{}, 0, "", err
	}
	var active *domconv.ConversationTurnActiveThreadState
	if err == nil {
		active = &domconv.ConversationTurnActiveThreadState{
			Thread: domconv.ConversationTurnThreadIdentity{
				ID:   modulecore.ThreadID(currentID),
				Seq:  modulecore.ThreadSeq(currentSeq),
				Kind: modulecore.ThreadKind(currentKind),
			},
			Domain:       currentDomain,
			MessageCount: int64(currentCount),
		}
	}
	newThreadID := modulecore.NewThreadID()
	if request.ThreadPlan != nil {
		newThreadID = request.ThreadPlan.Plan.Thread.ID
	}
	plan, err := domconv.PlanConversationTurnThread(domconv.ConversationTurnThreadPlanInput{
		Active:      active,
		Boundary:    request.Boundary,
		Domain:      request.Domain,
		NewThreadID: newThreadID,
	})
	if err != nil {
		return conversationThreadIdentity{}, conversationThreadIdentity{}, 0, "", err
	}
	if request.ThreadPlan != nil && plan != request.ThreadPlan.Plan {
		return conversationThreadIdentity{}, conversationThreadIdentity{}, 0, "", domconv.ErrConversationTurnConflict
	}
	thread = conversationThreadIdentity{ID: plan.Thread.ID, Seq: plan.Thread.Seq, Kind: plan.Thread.Kind}
	closed = conversationThreadIdentity{ID: plan.ClosedThread.ID, Seq: plan.ClosedThread.Seq, Kind: plan.ClosedThread.Kind}
	return thread, closed, plan.MessageCount, plan.Domain, nil
}

func upsertConversationActiveThread(ctx context.Context, tx *sql.Tx, sessionID string, thread conversationThreadIdentity, domain string, count int64, now time.Time) error {
	if err := thread.validate(true); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE conversation_active_thread
SET thread_id = ?, thread_seq = ?, thread_kind = ?, domain = ?, message_count = ?, updated_at = ?
WHERE session_id = ?`, thread.ID, thread.Seq, thread.Kind, domain, count, now, sessionID)
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
INSERT INTO conversation_active_thread (session_id, thread_id, thread_seq, thread_kind, domain, message_count, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, thread.ID, thread.Seq, thread.Kind, domain, count, now)
	return err
}

func insertConversationTurnMessages(ctx context.Context, tx *sql.Tx, request domconv.ConversationTurnRequest, thread conversationThreadIdentity, userMessageID, agentMessageID string, now time.Time) error {
	if err := thread.validate(true); err != nil {
		return err
	}
	namespace, err := BuildL1Namespace(NamespaceKindConversation, string(thread.ID))
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
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.id, namespace, request.SessionID, thread.ID, thread.Seq, thread.Kind,
			string(item.speaker), item.message, string(metaJSON), MemoryStateObserved, MemoryLayerL1, "conversation", createdAt, now); err != nil {
			return err
		}
		if _, err := appendL1EventLog(ctx, tx, "memory.message_saved", namespace, request.SessionID, thread.ID, thread.Seq, thread.Kind, map[string]interface{}{
			"memory_id": item.id, "turn_id": request.TurnID, "speaker": string(item.speaker), "memory_state": MemoryStateObserved,
		}, "conversation"); err != nil {
			return err
		}
	}
	if _, err := execInsertProfilePromotionJob(ctx, tx, false, userMessageID, request.SessionID, thread.ID, thread.Seq, thread.Kind, domainmemory.ProfilePromotionPending, now, now); err != nil {
		return err
	}
	return nil
}

func insertConversationTurnRecallTrace(ctx context.Context, tx *sql.Tx, request domconv.ConversationTurnRequest, now time.Time) error {
	traceID := string(request.TraceID)
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
	trace_id, owner_id, turn_id, root_task_id, chat_id, persona, route, user_message_hash, query_text_redacted,
	created_at, model_id, prompt_version, recall_policy_version, total_candidates,
	injected_count, total_injected_tokens, status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?)`, traceID, request.OwnerID, request.TurnID, request.RootTaskID, request.SessionID,
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

func conversationTurnOutboxPayload(request domconv.ConversationTurnRequest, thread, closed conversationThreadIdentity, userMessageID, agentMessageID, target, payloadHash string) (string, error) {
	if err := thread.validate(true); err != nil {
		return "", err
	}
	if err := closed.validate(false); err != nil {
		return "", err
	}
	payload := struct {
		Version          string                `json:"version"`
		TurnID           modulecore.TurnID     `json:"turn_id"`
		TraceID          modulecore.TraceID    `json:"trace_id"`
		RootTaskID       modulecore.TaskID     `json:"root_task_id"`
		SessionID        string                `json:"session_id"`
		OwnerID          string                `json:"owner_id"`
		ThreadID         modulecore.ThreadID   `json:"thread_id"`
		ThreadSeq        modulecore.ThreadSeq  `json:"thread_seq"`
		ThreadKind       modulecore.ThreadKind `json:"thread_kind"`
		ClosedThreadID   modulecore.ThreadID   `json:"closed_thread_id,omitempty"`
		ClosedThreadSeq  modulecore.ThreadSeq  `json:"closed_thread_seq,omitempty"`
		ClosedThreadKind modulecore.ThreadKind `json:"closed_thread_kind,omitempty"`
		UserMessageID    modulecore.MessageID  `json:"user_message_id"`
		AgentMessageID   modulecore.MessageID  `json:"agent_message_id"`
		Target           string                `json:"target"`
		PayloadSHA256    string                `json:"payload_sha256"`
	}{
		Version: "rencrow.conversation_turn_outbox.v2", TurnID: request.TurnID, TraceID: request.TraceID, RootTaskID: request.RootTaskID,
		SessionID: request.SessionID, OwnerID: request.OwnerID, ThreadID: thread.ID, ThreadSeq: thread.Seq, ThreadKind: thread.Kind,
		ClosedThreadID: closed.ID, ClosedThreadSeq: closed.Seq, ClosedThreadKind: closed.Kind,
		UserMessageID: modulecore.MessageID(userMessageID), AgentMessageID: modulecore.MessageID(agentMessageID), Target: target, PayloadSHA256: payloadHash,
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
	rows, err := tx.QueryContext(ctx, `SELECT target, status, last_error FROM conversation_turn_outbox WHERE turn_id = ? ORDER BY target ASC`, turnID)
	if err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	defer rows.Close()
	result.PendingTargets = nil
	result.CompletedTargets = nil
	allCompleted := true
	for rows.Next() {
		var target, status, lastError string
		if err := rows.Scan(&target, &status, &lastError); err != nil {
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
		}
	}
	if err := rows.Err(); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	if allCompleted {
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
	var traceID, rootTaskID string
	var threadID, threadKind string
	var threadSeq int64
	var closedID, closedKind string
	var closedSeq int64
	var lease sql.NullTime
	var status, lastError string
	if err := row.Scan(&outbox.TurnID, &traceID, &rootTaskID, &outbox.Target, &outbox.SessionID, &threadID, &threadSeq, &threadKind, &closedID, &closedSeq, &closedKind,
		&outbox.PayloadSHA256, &outbox.PayloadJSON, &status, &outbox.LeaseToken, &lease, &outbox.Attempts,
		&lastError, &outbox.CreatedAt, &outbox.UpdatedAt); err != nil {
		return nil, err
	}
	outbox.Status = domconv.ConversationTurnOutboxStatus(status)
	outbox.LastError = domconv.ConversationTurnErrorCode(lastError)
	outbox.TraceID = modulecore.TraceID(traceID)
	outbox.RootTaskID = modulecore.TaskID(rootTaskID)
	if outbox.TurnID.Validate() != nil || outbox.TraceID.Validate() != nil || outbox.RootTaskID.Validate() != nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	outbox.ThreadID = modulecore.ThreadID(threadID)
	outbox.ThreadSeq = modulecore.ThreadSeq(threadSeq)
	outbox.ThreadKind = modulecore.ThreadKind(threadKind)
	identity := conversationThreadIdentity{ID: outbox.ThreadID, Seq: outbox.ThreadSeq, Kind: modulecore.ThreadKind(threadKind)}
	if err := identity.validate(true); err != nil {
		return nil, err
	}
	closedIdentity := conversationThreadIdentity{ID: modulecore.ThreadID(closedID), Seq: modulecore.ThreadSeq(closedSeq), Kind: modulecore.ThreadKind(closedKind)}
	if err := closedIdentity.validate(false); err != nil {
		return nil, err
	}
	if closedID != "" {
		outbox.ClosedThreadID = closedIdentity.ID
		outbox.ClosedThreadSeq = closedIdentity.Seq
		outbox.ClosedThreadKind = closedIdentity.Kind
	}
	if lease.Valid {
		outbox.LeaseExpiresAt = lease.Time
	}
	return &outbox, nil
}

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
	return domconv.ConversationTurnResult{
		TurnID: request.TurnID, TraceID: request.TraceID, RootTaskID: request.RootTaskID,
		UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
		MessageIDs: []string{string(request.UserMessageID), string(request.AgentMessageID)},
		SessionID:  strings.TrimSpace(request.SessionID), Status: domconv.ConversationTurnFailed, ErrorCode: code,
	}
}

func rollbackNoTxConversationTurn(turnID string, code domconv.ConversationTurnErrorCode) (domconv.ConversationTurnResult, error) {
	return domconv.ConversationTurnResult{TurnID: modulecore.TurnID(turnID), Status: domconv.ConversationTurnFailed, ErrorCode: code}, conversationTurnError(code)
}
