package eventstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrWorkConflict           = errors.New("work lease or state conflicts with the requested operation")
	ErrWorkResultNotPersisted = errors.New("work result has not been durably persisted")
	ErrDelegationWorkConflict = errors.New("delegation revision, acceptance, or task binding conflicts")
)

const (
	maxWorkClaimBatch       = 32
	maxWorkLeaseDuration    = 24 * time.Hour
	maxWorkAttempts         = 5
	maxDelegationContextLen = 64 * 1024
)

// WorkClaimInput bounds the number and lifetime of leases created by one
// storage transaction. Now is injectable so expiry and retry behavior is
// deterministic in tests.
type WorkClaimInput struct {
	Kind          string
	Assignee      string
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

// LeasedWorkItem is a durable outbox item with its current fencing token.
type LeasedWorkItem struct {
	WorkItem
	Attempt        int       `json:"attempt"`
	Assignee       string    `json:"assignee,omitempty"`
	LeaseToken     string    `json:"lease_token"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Version        int64     `json:"version"`
}

type WorkLeaseInput struct {
	WorkID     string
	LeaseToken string
	Version    int64
	Now        time.Time
}

type WorkRetryInput struct {
	WorkLeaseInput
}

type DelegationDispatchInput struct {
	WorkID     string
	LeaseToken string
	Version    int64
	Now        time.Time
	SessionID  modulecore.SessionID
	Assignee   string
}

type DelegationContextMessage struct {
	MessageID modulecore.MessageID `json:"message_id"`
	Role      string               `json:"role"`
	Text      string               `json:"text"`
}

// DelegationDispatch contains only the private handoff and the exact
// authorized source messages bound by the stored context hash.
type DelegationDispatch struct {
	Work             LeasedWorkItem               `json:"work"`
	Handoff          conversation.HandoffEnvelope `json:"handoff"`
	Context          []DelegationContextMessage   `json:"context"`
	AcceptedAt       time.Time                    `json:"accepted_at"`
	ReadbackDeadline time.Time                    `json:"readback_deadline"`
	FailureIntent    *ReadbackFailureIntent       `json:"failure_intent,omitempty"`
}

type BindDelegationTaskInput struct {
	DelegationID    modulecore.RequestID
	Revision        uint64
	TaskID          modulecore.TaskID
	TaskOperationID string
	WorkID          string
	LeaseToken      string
	Version         int64
	Now             time.Time
}

// ClaimWork atomically claims due work, preferring lower numeric priorities.
// An expired lease is reclaimed with a fresh token and a larger fencing
// version, so two live workers cannot own the same item.
func (s *SQLiteStore) ClaimWork(ctx context.Context, input WorkClaimInput) ([]LeasedWorkItem, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, errors.New("work claim context is required")
	}
	if input.Limit < 1 || input.Limit > maxWorkClaimBatch {
		return nil, fmt.Errorf("work claim limit must be between 1 and %d", maxWorkClaimBatch)
	}
	if input.Kind != "" && (strings.TrimSpace(input.Kind) == "" || strings.TrimSpace(input.Kind) != input.Kind) {
		return nil, errors.New("work claim kind must be non-empty and trimmed")
	}
	if input.Assignee != "" && (strings.TrimSpace(input.Assignee) == "" || strings.TrimSpace(input.Assignee) != input.Assignee) {
		return nil, errors.New("work claim assignee must be non-empty and trimmed")
	}
	if input.LeaseDuration <= 0 || input.LeaseDuration > maxWorkLeaseDuration {
		return nil, fmt.Errorf("work lease duration must be positive and at most %s", maxWorkLeaseDuration)
	}
	now := normalizedNow(input.Now)
	leaseExpiry := now.Add(input.LeaseDuration)
	nowText, expiryText := formatWorkTime(now), formatWorkTime(leaseExpiry)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE live_work_outbox
		SET state='completed', lease_token=NULL, lease_expires_at=NULL
		WHERE kind='dispatch_delegation' AND state IN ('pending','leased') AND EXISTS (
			SELECT 1 FROM live_delegation d
			WHERE live_work_outbox.dedupe_key = 'delegation:' || d.delegation_id || ':' || d.revision || ':start'
			  AND d.state IN ('accepted','needs_clarification','rejected')
		)`); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		WITH eligible_work AS (
			SELECT w.work_id, w.dedupe_key, w.kind, w.source_event_id, w.session_id,
			       w.priority, w.attempt, w.not_before, w.version,
			       COALESCE(CASE
			         WHEN w.kind='start_delegated_task' THEN (SELECT d.assignee FROM live_delegation d
			           WHERE w.dedupe_key='delegation:' || d.delegation_id || ':' || d.revision || ':start_task'
			             AND d.state='accepted'
			             AND d.revision=(SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id))
			         WHEN w.kind='dispatch_delegation' THEN (SELECT d.assignee FROM live_delegation d
			           JOIN live_message_index m ON m.message_id=d.instruction_message_id
			           WHERE m.event_id=w.source_event_id AND m.session_id=w.session_id
			             AND d.state='dispatch_accepted'
			             AND d.revision=(SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id))
			         WHEN w.kind='execute_delegated_task' THEN (SELECT d.assignee FROM live_delegation d
			           WHERE w.dedupe_key='delegation:' || d.delegation_id || ':' || d.revision || ':execute'
			             AND d.state='accepted' AND d.work_task_id IS NOT NULL
			             AND d.revision=(SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id))
			         ELSE '' END, '') AS assignee
			FROM live_work_outbox w
			WHERE ((w.state='pending' AND (w.not_before='' OR julianday(w.not_before) <= julianday(?)))
			    OR (w.state='leased' AND w.lease_expires_at IS NOT NULL AND julianday(w.lease_expires_at) <= julianday(?)))
			  AND (? = '' OR w.kind = ?)
			  AND (w.kind <> 'conversation_projection' OR EXISTS (
				SELECT 1 FROM live_conversation_projection_delivery p WHERE p.work_id=w.work_id
			  ))
			  AND (w.kind <> 'start_delegated_task' OR EXISTS (
				SELECT 1 FROM live_delegation d
				WHERE w.dedupe_key = 'delegation:' || d.delegation_id || ':' || d.revision || ':start_task'
				  AND d.revision = (SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id)
				  AND d.state='accepted' AND d.work_task_id IS NULL
			  ))
			  AND (w.kind <> 'dispatch_delegation' OR EXISTS (
				SELECT 1 FROM live_delegation d
				JOIN live_message_index m ON m.message_id=d.instruction_message_id
				WHERE m.event_id=w.source_event_id AND d.revision=(SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id)
				  AND d.state='dispatch_accepted'
			  ))
			  AND (w.kind <> 'execute_delegated_task' OR EXISTS (
				SELECT 1 FROM live_delegation d
				WHERE w.dedupe_key='delegation:' || d.delegation_id || ':' || d.revision || ':execute'
				  AND d.revision=(SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id)
				  AND d.state='accepted' AND d.work_task_id IS NOT NULL
			  ))
		)
		SELECT work_id, dedupe_key, kind, source_event_id, session_id,
		       priority, attempt, not_before, version, assignee
		FROM eligible_work
		WHERE (? = '' OR assignee = ?)
		ORDER BY priority ASC, CASE WHEN not_before='' THEN 0 ELSE julianday(not_before) END ASC, work_id ASC
		LIMIT ?`, nowText, nowText, input.Kind, input.Kind, input.Assignee, input.Assignee, input.Limit)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		work     WorkItem
		attempt  int
		version  int64
		assignee string
	}
	candidates := make([]candidate, 0, input.Limit)
	for rows.Next() {
		var row candidate
		var eventID, sessionID, notBefore string
		if err := rows.Scan(&row.work.WorkID, &row.work.DedupeKey, &row.work.Kind, &eventID, &sessionID,
			&row.work.Priority, &row.attempt, &notBefore, &row.version, &row.assignee); err != nil {
			rows.Close()
			return nil, err
		}
		row.work.SourceEventID, row.work.SessionID = modulecore.EventID(eventID), modulecore.SessionID(sessionID)
		row.work.NotBefore, err = parseWorkTime(notBefore)
		if err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	claimed := make([]LeasedWorkItem, 0, len(candidates))
	for _, candidate := range candidates {
		token := string(modulecore.NewRequestID())
		result, err := tx.ExecContext(ctx, `
			UPDATE live_work_outbox SET state='leased', attempt=attempt+1, lease_token=?, lease_expires_at=?, version=version+1
			WHERE work_id=? AND version=? AND (
				(state='pending' AND (not_before='' OR julianday(not_before)<=julianday(?))) OR
				(state='leased' AND lease_expires_at IS NOT NULL AND julianday(lease_expires_at)<=julianday(?))
			)`, token, expiryText, candidate.work.WorkID, candidate.version, nowText, nowText)
		if err != nil {
			return nil, err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			continue
		}
		claimed = append(claimed, LeasedWorkItem{
			WorkItem: candidate.work, Attempt: candidate.attempt + 1, Assignee: candidate.assignee, LeaseToken: token,
			LeaseExpiresAt: leaseExpiry, Version: candidate.version + 1,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// LoadDelegationDispatch re-reads private handoff data only while the supplied
// unexpired dispatch lease is current and belongs to the authenticated target.
func (s *SQLiteStore) LoadDelegationDispatch(ctx context.Context, input DelegationDispatchInput) (DelegationDispatch, error) {
	if err := s.ensureOpen(); err != nil {
		return DelegationDispatch{}, err
	}
	if ctx == nil || strings.TrimSpace(input.Assignee) == "" || input.SessionID == "" {
		return DelegationDispatch{}, errors.New("delegation dispatch context, session, and assignee are required")
	}
	now := normalizedNow(input.Now)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DelegationDispatch{}, err
	}
	defer tx.Rollback()
	work, err := loadCurrentWorkLease(ctx, tx, WorkLeaseInput{WorkID: input.WorkID, LeaseToken: input.LeaseToken, Version: input.Version, Now: now})
	if err != nil {
		return DelegationDispatch{}, err
	}
	if work.Kind != "dispatch_delegation" || work.SessionID != input.SessionID {
		return DelegationDispatch{}, ErrWorkConflict
	}
	var delegationID string
	var revision int64
	err = tx.QueryRowContext(ctx, `
		SELECT d.delegation_id, d.revision FROM live_delegation d
		JOIN live_message_index m ON m.message_id=d.instruction_message_id
		WHERE m.event_id=? AND m.session_id=? AND d.state='dispatch_accepted'
		  AND d.revision=(SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id)`,
		string(work.SourceEventID), string(work.SessionID)).Scan(&delegationID, &revision)
	if err != nil {
		return DelegationDispatch{}, ErrDelegationWorkConflict
	}
	requestID := modulecore.RequestID(delegationID)
	delegation, err := loadReadbackDelegation(ctx, tx, requestID, uint64(revision))
	if err != nil || delegation.sessionID != string(input.SessionID) || delegation.assignee != input.Assignee ||
		delegation.delegationState != "dispatch_accepted" || delegation.turnState != "awaiting_readback" ||
		!delegation.turnDelegationID.Valid || delegation.turnDelegationID.String != delegationID {
		return DelegationDispatch{}, ErrDelegationWorkConflict
	}
	contextEvent, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(delegation.contextEventID))
	if err != nil {
		return DelegationDispatch{}, fmt.Errorf("load stored private handoff context: %w", err)
	}
	handoff, instruction, err := validateStoredHandoff(ctx, tx, delegation, contextEvent, ReadbackInput{
		DelegationID: requestID, Revision: uint64(revision), Assignee: input.Assignee,
	})
	if err != nil || instruction.EventID != work.SourceEventID || handoff.ToActor != input.Assignee {
		return DelegationDispatch{}, ErrDelegationWorkConflict
	}
	contextMessages, err := loadAndValidateDelegationContext(ctx, tx, contextEvent, handoff, delegation)
	if err != nil {
		return DelegationDispatch{}, err
	}
	acceptedAt, err := delegationReadbackAcceptedAt(ctx, tx, requestID, uint64(revision))
	if err != nil {
		return DelegationDispatch{}, err
	}
	failureIntent, err := loadReadbackFailureIntent(ctx, tx, handoff)
	if err != nil {
		return DelegationDispatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return DelegationDispatch{}, err
	}
	work.Assignee = input.Assignee
	return DelegationDispatch{
		Work: work, Handoff: handoff, Context: contextMessages, AcceptedAt: acceptedAt,
		ReadbackDeadline: readbackDeadlineAt(acceptedAt), FailureIntent: failureIntent,
	}, nil
}

// AckWork records success only after its owner result is durable. Task start
// is completed by BindDelegationTask; dispatch delivery is proven by a saved
// readback, which also reconciles a lost acknowledgment after restart.
func (s *SQLiteStore) AckWork(ctx context.Context, input WorkLeaseInput) error {
	return s.finishWork(ctx, input, false)
}

// RetryWork returns an uncompleted lease to pending with bounded backoff.
// A saved readback or task binding reconciles the work to completed instead.
func (s *SQLiteStore) RetryWork(ctx context.Context, input WorkRetryInput) error {
	return s.finishWork(ctx, input.WorkLeaseInput, true)
}

func (s *SQLiteStore) finishWork(ctx context.Context, input WorkLeaseInput, retry bool) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("work completion context is required")
	}
	now := normalizedNow(input.Now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	work, state, err := readWorkForUpdate(ctx, tx, input.WorkID)
	if err != nil {
		return err
	}
	if state == "completed" {
		if work.LeaseToken == input.LeaseToken && work.Version == input.Version && completedWorkHasOwnerResult(ctx, tx, work) {
			return tx.Commit()
		}
		return ErrWorkConflict
	}
	retryAndCommit := func() error {
		if err := retryLeasedWork(ctx, tx, work, input, now); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := validateWorkLease(work, state, input, now); err != nil {
		return err
	}
	if work.Kind == conversationProjectionWorkKind {
		if retry {
			return retryAndCommit()
		}
		return ErrWorkResultNotPersisted
	}
	if work.Kind == "start_delegated_task" {
		bound, current, err := taskStartResultState(ctx, tx, work)
		if err != nil {
			return err
		}
		if bound {
			if err := completeLeasedWork(ctx, tx, input); err != nil {
				return err
			}
			return tx.Commit()
		}
		if retry {
			if !current {
				return ErrDelegationWorkConflict
			}
			return retryAndCommit()
		}
		return ErrWorkResultNotPersisted
	}
	if work.Kind == "dispatch_delegation" {
		persisted, current, err := dispatchReadbackResult(ctx, tx, work)
		if err != nil {
			return err
		}
		if persisted {
			if err := completeLeasedWork(ctx, tx, input); err != nil {
				return err
			}
			return tx.Commit()
		}
		if !current {
			return ErrDelegationWorkConflict
		}
		if retry {
			return retryAndCommit()
		}
		return ErrWorkResultNotPersisted
	}
	if work.Kind == "execute_delegated_task" {
		persisted, current, err := delegatedTaskReportResult(ctx, tx, work)
		if err != nil {
			return err
		}
		if persisted {
			if err := completeLeasedWork(ctx, tx, input); err != nil {
				return err
			}
			return tx.Commit()
		}
		if !current {
			return ErrDelegationWorkConflict
		}
		if retry {
			return retryAndCommit()
		}
		return ErrWorkResultNotPersisted
	}
	if retry {
		return retryAndCommit()
	}
	if err := completeLeasedWork(ctx, tx, input); err != nil {
		return err
	}
	return tx.Commit()
}

// BindDelegationTask commits the Task owner receipt identity into the current
// accepted delegation and completes its task-start outbox item atomically.
func (s *SQLiteStore) BindDelegationTask(ctx context.Context, input BindDelegationTaskInput) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("delegation task bind context is required")
	}
	if err := input.DelegationID.Validate(); err != nil {
		return fmt.Errorf("delegation_id: %w", err)
	}
	if err := input.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id: %w", err)
	}
	if input.Revision == 0 || input.Revision > math.MaxInt64 || input.TaskOperationID != delegationTaskOperationID(input.DelegationID, input.Revision) ||
		input.WorkID != readbackStartWorkID(input.DelegationID, input.Revision) {
		return ErrDelegationWorkConflict
	}
	now := normalizedNow(input.Now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM live_delegation WHERE delegation_id=?`, string(input.DelegationID)).Scan(&currentRevision); err != nil {
		return err
	}
	if currentRevision != int64(input.Revision) {
		return ErrDelegationWorkConflict
	}
	var state, workTaskID, turnState, turnDelegationID string
	err = tx.QueryRowContext(ctx, `
		SELECT d.state, COALESCE(d.work_task_id,''), t.state, COALESCE(t.delegation_id,'')
		FROM live_delegation d JOIN live_conversation_turn t ON t.turn_id=d.origin_turn_id
		WHERE d.delegation_id=? AND d.revision=?`, string(input.DelegationID), int64(input.Revision)).Scan(&state, &workTaskID, &turnState, &turnDelegationID)
	if err != nil || state != "accepted" || turnState != "completed" || turnDelegationID != string(input.DelegationID) {
		return ErrDelegationWorkConflict
	}
	var work LeasedWorkItem
	var workState string
	work, workState, err = readWorkForUpdate(ctx, tx, input.WorkID)
	if err != nil || work.Kind != "start_delegated_task" {
		return ErrDelegationWorkConflict
	}
	if workTaskID != "" {
		if workTaskID != string(input.TaskID) || workState != "completed" {
			return ErrDelegationWorkConflict
		}
		if err := ensureDelegationExecuteWork(ctx, tx, work, input.DelegationID, input.Revision); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := validateWorkLease(work, workState, WorkLeaseInput{
		WorkID: input.WorkID, LeaseToken: input.LeaseToken, Version: input.Version, Now: now,
	}, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE live_delegation SET work_task_id=? WHERE delegation_id=? AND revision=? AND state='accepted' AND work_task_id IS NULL`, string(input.TaskID), string(input.DelegationID), int64(input.Revision))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrDelegationWorkConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE live_work_outbox SET state='completed' WHERE work_id=? AND state='leased' AND lease_token=? AND version=?`, input.WorkID, input.LeaseToken, input.Version)
	if err != nil {
		return err
	}
	updated, err = result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrWorkConflict
	}
	if err := ensureDelegationExecuteWork(ctx, tx, work, input.DelegationID, input.Revision); err != nil {
		return err
	}
	return tx.Commit()
}

func delegationTaskOperationID(id modulecore.RequestID, revision uint64) string {
	return "delegation:" + string(id) + ":" + fmt.Sprint(revision) + ":start"
}

func ensureDelegationExecuteWork(ctx context.Context, tx *sql.Tx, startWork LeasedWorkItem, delegationID modulecore.RequestID, revision uint64) error {
	if startWork.Kind != "start_delegated_task" || startWork.SourceEventID == "" || startWork.SessionID == "" {
		return ErrDelegationWorkConflict
	}
	source, err := loadEventEnvelopeTx(ctx, tx, startWork.SourceEventID)
	if err != nil || source.EventType != "agent.response" || source.ComponentID != "orchestrator" ||
		source.RequestID != delegationID || source.SessionID != startWork.SessionID ||
		payloadString(source.Payload, "kind") != "readback" || payloadString(source.Payload, "status") != "accepted" ||
		payloadString(source.Payload, "visibility") != "public" || payloadString(source.Payload, "delegation_id") != string(delegationID) {
		return ErrDelegationWorkConflict
	}
	sourceRevision, ok := eventPayloadUint64(source.Payload["delegation_revision"])
	if !ok || sourceRevision != revision {
		return ErrDelegationWorkConflict
	}
	if err := validateOutboxSource(ctx, tx, startWork.SourceEventID, string(source.TraceID), startWork.SessionID); err != nil {
		return ErrDelegationWorkConflict
	}
	workID := delegationExecuteWorkID(delegationID, revision)
	dedupeKey := delegationExecuteDedupeKey(delegationID, revision)
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO live_work_outbox
			(work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
		VALUES (?,?,'execute_delegated_task',?,?,5,'pending',0,'',1)`,
		workID, dedupeKey, string(startWork.SourceEventID), string(startWork.SessionID)); err != nil {
		return err
	}
	var storedWorkID, storedKind, storedEventID, storedSessionID string
	if err := tx.QueryRowContext(ctx, `SELECT work_id,kind,source_event_id,session_id FROM live_work_outbox WHERE dedupe_key=?`, dedupeKey).Scan(
		&storedWorkID, &storedKind, &storedEventID, &storedSessionID); err != nil {
		return err
	}
	if storedWorkID != workID || storedKind != "execute_delegated_task" || storedEventID != string(startWork.SourceEventID) || storedSessionID != string(startWork.SessionID) {
		return ErrDelegationWorkConflict
	}
	return nil
}

func delegationExecuteWorkID(id modulecore.RequestID, revision uint64) string {
	return "execute:" + string(id) + ":" + fmt.Sprint(revision)
}

func delegationExecuteDedupeKey(id modulecore.RequestID, revision uint64) string {
	return "delegation:" + string(id) + ":" + fmt.Sprint(revision) + ":execute"
}

func loadCurrentWorkLease(ctx context.Context, tx *sql.Tx, input WorkLeaseInput) (LeasedWorkItem, error) {
	work, state, err := readWorkForUpdate(ctx, tx, input.WorkID)
	if err != nil {
		return LeasedWorkItem{}, err
	}
	if _, err := validateWorkLease(work, state, input, normalizedNow(input.Now)); err != nil {
		return LeasedWorkItem{}, err
	}
	return work, nil
}

func readWorkForUpdate(ctx context.Context, tx *sql.Tx, workID string) (LeasedWorkItem, string, error) {
	var work LeasedWorkItem
	var eventID, sessionID, notBefore, expires, state string
	err := tx.QueryRowContext(ctx, `SELECT work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,COALESCE(lease_token,''),COALESCE(lease_expires_at,''),version FROM live_work_outbox WHERE work_id=?`, workID).Scan(
		&work.WorkID, &work.DedupeKey, &work.Kind, &eventID, &sessionID, &work.Priority, &state, &work.Attempt,
		&notBefore, &work.LeaseToken, &expires, &work.Version,
	)
	if err != nil {
		return LeasedWorkItem{}, "", err
	}
	work.SourceEventID, work.SessionID = modulecore.EventID(eventID), modulecore.SessionID(sessionID)
	work.NotBefore, err = parseWorkTime(notBefore)
	if err != nil {
		return LeasedWorkItem{}, "", err
	}
	work.LeaseExpiresAt, err = parseWorkTime(expires)
	if err != nil {
		return LeasedWorkItem{}, "", err
	}
	return work, state, nil
}

func validateWorkLease(work LeasedWorkItem, state string, input WorkLeaseInput, now time.Time) (LeasedWorkItem, error) {
	if input.WorkID == "" || input.LeaseToken == "" || input.Version < 1 || state != "leased" ||
		work.LeaseToken != input.LeaseToken || work.Version != input.Version || work.LeaseExpiresAt.IsZero() || !work.LeaseExpiresAt.After(now) {
		return LeasedWorkItem{}, ErrWorkConflict
	}
	return work, nil
}

func completeLeasedWork(ctx context.Context, tx *sql.Tx, input WorkLeaseInput) error {
	// Preserve the completed lease identity so a lost Ack response can be
	// replayed idempotently; completed rows are never readable as active leases.
	result, err := tx.ExecContext(ctx, `UPDATE live_work_outbox SET state='completed' WHERE work_id=? AND state='leased' AND lease_token=? AND version=?`, input.WorkID, input.LeaseToken, input.Version)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrWorkConflict
	}
	return nil
}

func retryLeasedWork(ctx context.Context, tx *sql.Tx, work LeasedWorkItem, input WorkLeaseInput, now time.Time) error {
	var result sql.Result
	var err error
	if work.Attempt >= maxWorkAttempts && work.Kind == "dispatch_delegation" {
		deadline, deadlineErr := delegationReadbackDeadlineForWork(ctx, tx, work)
		if deadlineErr != nil {
			return deadlineErr
		}
		result, err = tx.ExecContext(ctx, `UPDATE live_work_outbox SET state='pending', not_before=?, lease_token=NULL, lease_expires_at=NULL, version=version+1 WHERE work_id=? AND state='leased' AND lease_token=? AND version=?`, formatWorkTime(deadline), input.WorkID, input.LeaseToken, input.Version)
	} else if work.Attempt >= maxWorkAttempts {
		result, err = tx.ExecContext(ctx, `UPDATE live_work_outbox SET state='needs_attention', lease_token=NULL, lease_expires_at=NULL, version=version+1 WHERE work_id=? AND state='leased' AND lease_token=? AND version=?`, input.WorkID, input.LeaseToken, input.Version)
	} else {
		delays := [...]time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
		delay := delays[len(delays)-1]
		if work.Attempt <= len(delays) {
			delay = delays[work.Attempt-1]
		}
		result, err = tx.ExecContext(ctx, `UPDATE live_work_outbox SET state='pending', not_before=?, lease_token=NULL, lease_expires_at=NULL, version=version+1 WHERE work_id=? AND state='leased' AND lease_token=? AND version=?`, formatWorkTime(now.Add(delay)), input.WorkID, input.LeaseToken, input.Version)
	}
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrWorkConflict
	}
	return nil
}

func taskStartResultState(ctx context.Context, tx *sql.Tx, work LeasedWorkItem) (bound, current bool, err error) {
	var taskID, state, delegationID string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT d.delegation_id,d.revision,COALESCE(d.work_task_id,''),d.state FROM live_delegation d WHERE ?='delegation:' || d.delegation_id || ':' || d.revision || ':start_task'`, work.DedupeKey).Scan(&delegationID, &revision, &taskID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, ErrDelegationWorkConflict
	}
	if err != nil {
		return false, false, err
	}
	var maxRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM live_delegation WHERE delegation_id=?`, delegationID).Scan(&maxRevision); err != nil {
		return false, false, err
	}
	return taskID != "", taskID == "" && state == "accepted" && revision == maxRevision, nil
}

func dispatchReadbackResult(ctx context.Context, tx *sql.Tx, work LeasedWorkItem) (persisted, current bool, err error) {
	var delegationID string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT d.delegation_id,d.revision FROM live_delegation d JOIN live_message_index m ON m.message_id=d.instruction_message_id WHERE m.event_id=?`, string(work.SourceEventID)).Scan(&delegationID, &revision)
	if err != nil {
		return false, false, ErrDelegationWorkConflict
	}
	var state string
	var latest int64
	if err := tx.QueryRowContext(ctx, `SELECT state,(SELECT MAX(revision) FROM live_delegation WHERE delegation_id=?) FROM live_delegation WHERE delegation_id=? AND revision=?`, delegationID, delegationID, revision).Scan(&state, &latest); err != nil {
		return false, false, err
	}
	if latest != revision {
		return false, false, nil
	}
	if state == "dispatch_accepted" {
		return false, true, nil
	}
	if state == "needs_attention" {
		failure, err := readStoredReadbackFailure(ctx, tx, modulecore.RequestID(delegationID), uint64(revision))
		if err != nil {
			return false, false, err
		}
		return failure != nil, failure != nil, nil
	}
	if state != "accepted" && state != "needs_clarification" && state != "rejected" {
		return false, false, nil
	}
	return true, true, nil
}

func completedWorkHasOwnerResult(ctx context.Context, tx *sql.Tx, work LeasedWorkItem) bool {
	switch work.Kind {
	case "start_delegated_task":
		bound, _, err := taskStartResultState(ctx, tx, work)
		return err == nil && bound
	case "dispatch_delegation":
		persisted, _, err := dispatchReadbackResult(ctx, tx, work)
		return err == nil && persisted
	case "execute_delegated_task":
		persisted, _, err := delegatedTaskReportResult(ctx, tx, work)
		return err == nil && persisted
	default:
		return true
	}
}

// delegatedTaskReportResult ties execute-work completion to the canonical
// private report event and its append-only AcceptReport receipt. Task binding
// alone is only an execution input and never proves that the worker returned.
func delegatedTaskReportResult(ctx context.Context, tx *sql.Tx, work LeasedWorkItem) (persisted, current bool, err error) {
	var delegationID, state, taskID, assignee, sessionID string
	var revision, reportRevision, latestRevision int64
	err = tx.QueryRowContext(ctx, `
		SELECT d.delegation_id,d.revision,d.state,COALESCE(d.work_task_id,''),d.report_revision,d.assignee,t.session_id,
		       (SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id)
		FROM live_delegation d JOIN live_conversation_turn t ON t.turn_id=d.origin_turn_id
		WHERE ?='delegation:' || d.delegation_id || ':' || d.revision || ':execute'`, work.DedupeKey,
	).Scan(&delegationID, &revision, &state, &taskID, &reportRevision, &assignee, &sessionID, &latestRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, ErrDelegationWorkConflict
	}
	if err != nil {
		return false, false, err
	}
	validPending := state == "accepted" && reportRevision == 0
	validReportState := false
	switch conversation.ReportStatus(state) {
	case conversation.ReportStatusSucceeded, conversation.ReportStatusPartial,
		conversation.ReportStatusFailed, conversation.ReportStatusCanceled:
		validReportState = reportRevision > 0
	}
	current = revision == latestRevision && taskID != "" && sessionID == string(work.SessionID) && (validPending || validReportState)
	if !current || validPending {
		return false, current, nil
	}

	var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelope string
	var eventSeq int64
	var message MessageIndex
	var replyTo sql.NullString
	var operationKind, resultJSON string
	err = tx.QueryRowContext(ctx, `
		SELECT e.event_id,e.event_seq,e.trace_id,e.schema_version,e.event_type,e.component_id,e.occurred_at,e.envelope_json,
		       m.message_id,m.event_id,m.session_id,m.turn_id,m.from_actor,m.to_kind,m.to_actor,m.kind,m.reply_to_message_id,m.visibility,
		       r.operation_kind,r.result_json
		FROM event_envelope e
		JOIN live_message_index m ON m.event_id=e.event_id
		JOIN live_operation_receipt r ON r.operation_id='accept_report:' || json_extract(e.envelope_json,'$.response_id')
		WHERE e.event_type='conversation.agent_report' AND e.component_id='conversation_runtime'
		  AND json_extract(e.envelope_json,'$.request_id')=?
		  AND json_extract(e.envelope_json,'$.session_id')=?
		  AND json_extract(e.envelope_json,'$.task_id')=?
		  AND json_extract(e.envelope_json,'$.actor_id')=?
		  AND json_extract(e.envelope_json,'$.payload.delegation_revision')=?
		  AND json_extract(e.envelope_json,'$.payload.report_revision')=?
		  AND r.operation_kind='accept_report'
		ORDER BY e.event_seq DESC LIMIT 1`, delegationID, sessionID, taskID, assignee, revision, reportRevision,
	).Scan(&eventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &envelope,
		&message.MessageID, &message.EventID, &message.SessionID, &message.TurnID, &message.FromActor,
		&message.ToKind, &message.ToActor, &message.Kind, &replyTo, &message.Visibility,
		&operationKind, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, current, nil
	}
	if err != nil {
		return false, current, err
	}
	event, err := decodeStoredEnvelope(eventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, envelope)
	if err != nil {
		return false, current, fmt.Errorf("decode accepted delegated-task report: %w", err)
	}
	message.EventID = modulecore.EventID(message.EventID)
	message.ReplyToMessageID = modulecore.MessageID(replyTo.String)
	if err := validateMessageIndex(message, event, event.SessionID, event.TurnID); err != nil {
		return false, current, nil
	}
	if event.SessionID != work.SessionID || event.RequestID != modulecore.RequestID(delegationID) ||
		event.TaskID != modulecore.TaskID(taskID) || event.ActorID != assignee || operationKind != "accept_report" {
		return false, current, nil
	}
	reportJSON, ok := event.Payload["report"]
	if !ok {
		return false, current, nil
	}
	encodedReport, err := json.Marshal(reportJSON)
	if err != nil {
		return false, current, err
	}
	var report conversation.ReportEnvelope
	if err := json.Unmarshal(encodedReport, &report); err != nil {
		return false, current, nil
	}
	if report.ReportID != event.ResponseID || report.DelegationID != modulecore.RequestID(delegationID) ||
		report.DelegationRevision != uint64(revision) || report.ReportRevision != uint64(reportRevision) ||
		report.WorkTaskID != modulecore.TaskID(taskID) || report.WorkRunID != event.RunID ||
		report.Status != conversation.ReportStatus(state) || payloadString(event.Payload, "text") != report.Summary {
		return false, current, nil
	}
	var accepted AcceptedTurn
	if err := json.Unmarshal([]byte(resultJSON), &accepted); err != nil {
		return false, current, fmt.Errorf("decode accepted delegated-task report receipt: %w", err)
	}
	input := ReportInput{Report: report, TrustedSender: assignee}
	operationID, submissionID := reportAcceptanceOperationID(report.ReportID), reportSubmissionID(report.ReportID)
	if err := validateStoredReportAcceptance(accepted, input, operationID, submissionID); err != nil ||
		!reflect.DeepEqual(accepted.InputEvent, event) ||
		validateRecoverableTurnActor(accepted, operationID, submissionID, event, message) != nil {
		return false, current, nil
	}
	return true, current, nil
}

func loadAndValidateDelegationContext(ctx context.Context, tx *sql.Tx, event modulecore.EventEnvelope, handoff conversation.HandoffEnvelope, delegation storedReadbackDelegation) ([]DelegationContextMessage, error) {
	if event.ComponentID != "conversation_runtime" || event.EventType != "conversation.delegation_context" {
		return nil, errors.New("delegation context is not private runtime state")
	}
	sourceRevision, ok1 := event.Payload["source_revision"].(string)
	cutoverRevision, ok2 := event.Payload["cutover_revision"].(string)
	storedHash, ok3 := event.Payload["context_snapshot_hash"].(string)
	rawMessages, ok4 := event.Payload["messages"].([]any)
	if !ok1 || !ok2 || !ok3 || !ok4 || len(rawMessages) == 0 || len(rawMessages) > 256 {
		return nil, errors.New("stored delegation context has an invalid schema")
	}
	messages := make([]DelegationContextMessage, 0, len(rawMessages))
	seen := make(map[modulecore.MessageID]struct{}, len(rawMessages))
	for _, raw := range rawMessages {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("stored delegation context message is malformed")
		}
		id, _ := row["message_id"].(string)
		role, _ := row["role"].(string)
		text, _ := row["text"].(string)
		messageID := modulecore.MessageID(id)
		if err := messageID.Validate(); err != nil || (role != "user" && role != "assistant") || strings.TrimSpace(text) == "" {
			return nil, errors.New("stored delegation context message has invalid identity, role, or text")
		}
		if _, duplicate := seen[messageID]; duplicate {
			return nil, errors.New("stored delegation context contains a duplicate message")
		}
		seen[messageID] = struct{}{}
		actualRole, actualText, err := authorizedContextMessage(ctx, tx, messageID, modulecore.SessionID(delegation.sessionID), delegation.sessionOwnerID, delegation.originActorID)
		if err != nil || actualRole != role || actualText != text {
			return nil, errors.New("stored delegation context is not an authorized canonical message snapshot")
		}
		messages = append(messages, DelegationContextMessage{MessageID: messageID, Role: role, Text: text})
	}
	if _, found := seen[modulecore.MessageID(delegation.inputMessageID)]; !found {
		return nil, errors.New("delegation context omits the origin user message")
	}
	for _, constraint := range handoff.Constraints {
		for _, sourceID := range constraint.SourceMessageIDs {
			if _, found := seen[sourceID]; !found {
				return nil, errors.New("delegation context omits a handoff constraint source")
			}
		}
	}
	material := struct {
		SourceRevision  string                     `json:"source_revision"`
		CutoverRevision string                     `json:"cutover_revision"`
		Messages        []DelegationContextMessage `json:"messages"`
	}{SourceRevision: sourceRevision, CutoverRevision: cutoverRevision, Messages: messages}
	encoded, err := json.Marshal(material)
	if err != nil || len(encoded) > maxDelegationContextLen {
		return nil, errors.New("delegation context exceeds its stored size bound")
	}
	digest := sha256.Sum256(encoded)
	computed := hex.EncodeToString(digest[:])
	if computed != storedHash || computed != handoff.ContextSnapshotHash {
		return nil, errors.New("stored delegation context hash does not match its HandoffEnvelope")
	}
	return messages, nil
}

func authorizedContextMessage(ctx context.Context, tx *sql.Tx, messageID modulecore.MessageID, sessionID modulecore.SessionID, ownerID, actorID string) (string, string, error) {
	var eventID, storedSession, turnID, fromActor, toKind, toActor, kind, visibility string
	err := tx.QueryRowContext(ctx, `SELECT m.event_id,m.session_id,m.turn_id,m.from_actor,m.to_kind,m.to_actor,m.kind,m.visibility FROM live_message_index m WHERE m.message_id=?`, string(messageID)).Scan(&eventID, &storedSession, &turnID, &fromActor, &toKind, &toActor, &kind, &visibility)
	var event modulecore.EventEnvelope
	var index MessageIndex
	indexed := false
	if err == nil {
		indexed = true
		if storedSession != string(sessionID) {
			return "", "", errors.New("context source belongs to a different session")
		}
		event, err = loadEventEnvelopeTx(ctx, tx, modulecore.EventID(eventID))
		index = MessageIndex{MessageID: messageID, EventID: modulecore.EventID(eventID), SessionID: sessionID, TurnID: modulecore.TurnID(turnID), FromActor: fromActor, ToKind: toKind, ToActor: toActor, Kind: kind, Visibility: visibility}
	} else if errors.Is(err, sql.ErrNoRows) {
		var stored string
		err = tx.QueryRowContext(ctx, `SELECT envelope_json FROM event_envelope WHERE json_extract(envelope_json,'$.message_id')=? AND json_extract(envelope_json,'$.session_id')=? ORDER BY event_seq DESC LIMIT 1`, string(messageID), string(sessionID)).Scan(&stored)
		if err == nil {
			err = json.Unmarshal([]byte(stored), &event)
			if err == nil {
				legacy, reason, isMessage := projectLegacyMessage(event, ownerID)
				if !isMessage || reason != "" {
					return "", "", errors.New("context source is not a public message")
				}
				index = legacy.Index
			}
		}
	}
	if err != nil {
		return "", "", err
	}
	if event.SessionID != sessionID || index.Visibility != "public" {
		return "", "", errors.New("context source is not public in the current session")
	}
	if indexed {
		if err := validateMessageIndex(index, event, sessionID, event.TurnID); err != nil {
			return "", "", fmt.Errorf("validate indexed context source: %w", err)
		}
	}
	role := ""
	if (index.FromActor == ownerID || index.FromActor == actorID) && index.Kind == "user_message" {
		role = "user"
	} else if index.FromActor != ownerID && index.ToKind == "user" && index.ToActor == ownerID {
		role = "assistant"
	}
	if role == "" {
		return "", "", errors.New("context source is not authorized for the owner")
	}
	text, hasText := event.Payload["text"].(string)
	content, hasContent := event.Payload["content"].(string)
	if hasText && hasContent && text != content {
		return "", "", errors.New("context message text and content differ")
	}
	if !hasText {
		text, hasText = content, hasContent
	}
	if !hasText || strings.TrimSpace(text) == "" {
		return "", "", errors.New("context source has no canonical text")
	}
	return role, text, nil
}

func normalizedNow(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC()
}

func formatWorkTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseWorkTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse persisted work timestamp: %w", err)
	}
	return parsed.UTC(), nil
}
