package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	L1ResearchWatchDefaultLimit = 20
	L1ResearchWatchMaxLimit     = 100
	L1ResearchWatchLeaseTTL     = 10 * time.Minute
	L1ResearchWatchMinCadence   = time.Minute
	L1ResearchWatchMaxCadence   = 365 * 24 * time.Hour
	L1ResearchWatchDefaultRetry = time.Minute
	L1ResearchWatchMaxRetry     = 24 * time.Hour
	l1ResearchWatchMaxSources   = 100
	l1ResearchWatchMaxJSON      = 2 << 20
)

var ErrL1ResearchWatchConflict = errors.New("l1 research watch identity conflicts with stored payload")

// L1ResearchWatch is the owner-persisted registration and scheduler cursor.
// A cursor is opaque state; its presence is not proof that an external write
// was accepted or verified.
type L1ResearchWatch struct {
	WatchID       string
	OwnerID       string
	Scope         string
	PayloadJSON   string
	PayloadDigest string
	SourceRefs    []string
	Cadence       time.Duration
	NextDueAt     time.Time
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	CursorJSON    string
	Enabled       bool
	LeaseToken    string
	LeaseUntil    time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// L1ResearchWatchRefresh is the durable owner binding for one frozen Engine
// plan. LeaseToken is owner-persistence data and must not be returned to users.
type L1ResearchWatchRefresh struct {
	Scope                 string
	OwnerID               string
	WatchID               string
	PlanID                string
	LeaseToken            string
	PlanJSON              string
	PlanDigest            string
	OriginalExecutionJSON string
	CurrentInvocationJSON string
	SourcePolicyRevision  string
	PreviousCursor        string
	State                 string
	AttemptDigest         string
	AttemptRef            string
	RetryAt               time.Time
	CommittedCursor       string
	CheckpointDigest      string
	ExecutionDigest       string
	ReceiptJSON           string
	UpdatedAt             time.Time
}

// BindResearchWatchRefresh binds an Engine plan to the exact live owner lease.
func (s *L1SQLiteStore) BindResearchWatchRefresh(ctx context.Context, binding L1ResearchWatchRefresh, now time.Time) error {
	if err := validateResearchWatchRefreshBinding(s, ctx, binding); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin research watch plan bind: %w", err)
	}
	defer tx.Rollback()
	watch, found, err := readL1ResearchWatch(ctx, tx, binding.Scope, binding.OwnerID, binding.WatchID)
	if err != nil || !found || !watch.Enabled || watch.LeaseToken != binding.LeaseToken || !watch.LeaseUntil.After(now) || watch.CursorJSON != binding.PreviousCursor {
		return errors.New("research watch lease changed before plan binding")
	}
	current, found, err := readL1ResearchWatchRefresh(ctx, tx, binding.Scope, binding.OwnerID, binding.WatchID)
	if err != nil {
		return err
	}
	if found && current.State == "active" && current.LeaseToken == binding.LeaseToken {
		if current.PlanJSON == binding.PlanJSON && current.PlanDigest == binding.PlanDigest {
			return tx.Commit()
		}
		return errors.New("research watch lease already has a different plan")
	}
	binding.State = "active"
	binding.UpdatedAt = now
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_research_watch_refresh (
	scope, owner_id, watch_id, plan_id, lease_token, plan_json, plan_digest,
	original_execution_json, current_invocation_json, source_policy_revision, previous_cursor,
	state, attempt_digest, attempt_ref, retry_at, committed_cursor, checkpoint_digest,
	execution_digest, receipt_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', '', '', NULL, '', '', '', '', ?)
ON CONFLICT(scope, owner_id, watch_id) DO UPDATE SET
	plan_id=excluded.plan_id, lease_token=excluded.lease_token, plan_json=excluded.plan_json,
	plan_digest=excluded.plan_digest, original_execution_json=excluded.original_execution_json,
	current_invocation_json=excluded.current_invocation_json, source_policy_revision=excluded.source_policy_revision,
	previous_cursor=excluded.previous_cursor, state='active', attempt_digest='', attempt_ref='', retry_at=NULL,
	committed_cursor='', checkpoint_digest='', execution_digest='', receipt_json='', updated_at=excluded.updated_at
`, binding.Scope, binding.OwnerID, binding.WatchID, binding.PlanID, binding.LeaseToken, binding.PlanJSON,
		binding.PlanDigest, binding.OriginalExecutionJSON, binding.CurrentInvocationJSON, binding.SourcePolicyRevision,
		binding.PreviousCursor, now)
	if err != nil {
		return fmt.Errorf("persist research watch plan binding: %w", err)
	}
	return tx.Commit()
}

func (s *L1SQLiteStore) GetResearchWatchRefresh(ctx context.Context, scope, ownerID, watchID string) (L1ResearchWatchRefresh, bool, error) {
	if s == nil || s.readDB == nil || ctx == nil {
		return L1ResearchWatchRefresh{}, false, errors.New("research watch refresh reader and context are required")
	}
	if err := validateResearchWatchOwner(scope, ownerID); err != nil {
		return L1ResearchWatchRefresh{}, false, err
	}
	return readL1ResearchWatchRefresh(ctx, s.readDB, scope, ownerID, strings.TrimSpace(watchID))
}

// RevalidateResearchWatchRefresh either verifies the live exact lease or
// re-grants its same token after a durable retry becomes due.
func (s *L1SQLiteStore) RevalidateResearchWatchRefresh(ctx context.Context, expected L1ResearchWatchRefresh, currentInvocationJSON string, now time.Time) (L1ResearchWatchRefresh, bool, error) {
	if err := validateResearchWatchRefreshBinding(s, ctx, expected); err != nil {
		return L1ResearchWatchRefresh{}, false, err
	}
	if strings.TrimSpace(currentInvocationJSON) == "" {
		return L1ResearchWatchRefresh{}, false, errors.New("research watch continuation identity is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return L1ResearchWatchRefresh{}, false, fmt.Errorf("begin research watch revalidation: %w", err)
	}
	defer tx.Rollback()
	current, found, err := readL1ResearchWatchRefresh(ctx, tx, expected.Scope, expected.OwnerID, expected.WatchID)
	if err != nil || !found {
		return L1ResearchWatchRefresh{}, false, err
	}
	if !sameL1ResearchWatchRefreshPlan(current, expected) {
		return L1ResearchWatchRefresh{}, false, errors.New("research watch owner plan binding changed")
	}
	watch, found, err := readL1ResearchWatch(ctx, tx, expected.Scope, expected.OwnerID, expected.WatchID)
	if err != nil || !found || !watch.Enabled || watch.CursorJSON != expected.PreviousCursor {
		return L1ResearchWatchRefresh{}, false, errors.New("research watch cursor or registration changed")
	}
	switch current.State {
	case "active":
		if watch.LeaseToken != expected.LeaseToken || !watch.LeaseUntil.After(now) {
			return L1ResearchWatchRefresh{}, false, nil
		}
	case "retry_scheduled":
		if current.RetryAt.IsZero() || now.Before(current.RetryAt) || watch.NextDueAt.After(now) || !watch.LeaseUntil.IsZero() && watch.LeaseUntil.After(now) || watch.LeaseToken != "" {
			return L1ResearchWatchRefresh{}, false, nil
		}
		result, updateErr := tx.ExecContext(ctx, `
UPDATE l1_research_watch SET lease_token=?, lease_until=?, last_attempt_at=?, updated_at=?
WHERE watch_id=? AND scope=? AND owner_id=? AND enabled=1 AND cursor_json=?
AND (lease_token='' OR lease_until IS NULL OR lease_until<=?)
`, expected.LeaseToken, now.Add(L1ResearchWatchLeaseTTL), now, now, expected.WatchID, expected.Scope,
			expected.OwnerID, expected.PreviousCursor, now)
		if updateErr != nil {
			return L1ResearchWatchRefresh{}, false, fmt.Errorf("re-grant research watch retry lease: %w", updateErr)
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return L1ResearchWatchRefresh{}, false, rowsErr
		}
		if rows != 1 {
			return L1ResearchWatchRefresh{}, false, fmt.Errorf("re-grant research watch retry lease lost its CAS: rows=%d", rows)
		}
	default:
		return L1ResearchWatchRefresh{}, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE l1_research_watch_refresh SET state='active', current_invocation_json=?, updated_at=? WHERE scope=? AND owner_id=? AND watch_id=? AND plan_id=? AND plan_digest=?`, currentInvocationJSON, now, expected.Scope, expected.OwnerID, expected.WatchID, expected.PlanID, expected.PlanDigest); err != nil {
		return L1ResearchWatchRefresh{}, false, fmt.Errorf("update research watch invocation binding: %w", err)
	}
	current.State = "active"
	current.CurrentInvocationJSON = currentInvocationJSON
	current.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return L1ResearchWatchRefresh{}, false, err
	}
	return current, true, nil
}

func (s *L1SQLiteStore) CommitResearchWatchRefresh(ctx context.Context, binding L1ResearchWatchRefresh, cursorJSON, checkpointDigest, executionDigest, receiptJSON string, now time.Time) error {
	if err := validateResearchWatchRefreshBinding(s, ctx, binding); err != nil {
		return err
	}
	if cursorJSON == "" || checkpointDigest == "" || executionDigest == "" || receiptJSON == "" {
		return errors.New("research watch checkpoint receipt is incomplete")
	}
	canonicalCursor, err := canonicalResearchWatchJSON(cursorJSON)
	if err != nil || canonicalCursor != cursorJSON {
		return errors.New("research watch committed cursor is not canonical JSON")
	}
	canonicalReceipt, err := canonicalResearchWatchJSON(receiptJSON)
	if err != nil || canonicalReceipt != receiptJSON {
		return errors.New("research watch checkpoint receipt is not canonical JSON")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin research watch checkpoint commit: %w", err)
	}
	defer tx.Rollback()
	current, found, err := readL1ResearchWatchRefresh(ctx, tx, binding.Scope, binding.OwnerID, binding.WatchID)
	if err != nil || !found || !sameL1ResearchWatchRefreshPlan(current, binding) || current.State != "active" {
		return errors.New("research watch checkpoint plan is not active")
	}
	watch, found, err := readL1ResearchWatch(ctx, tx, binding.Scope, binding.OwnerID, binding.WatchID)
	if err != nil || !found || !watch.Enabled || watch.LeaseToken != binding.LeaseToken || !watch.LeaseUntil.After(now) || watch.CursorJSON != binding.PreviousCursor {
		return errors.New("research watch checkpoint lease is stale")
	}
	res, err := tx.ExecContext(ctx, `UPDATE l1_research_watch SET cursor_json=?,last_attempt_at=?,last_success_at=?,next_due_at=?,lease_token='',lease_until=NULL,updated_at=? WHERE watch_id=? AND scope=? AND owner_id=? AND enabled=1 AND lease_token=? AND lease_until>? AND cursor_json=?`, cursorJSON, now, now, now.Add(watch.Cadence), now, binding.WatchID, binding.Scope, binding.OwnerID, binding.LeaseToken, now, binding.PreviousCursor)
	if err != nil {
		return fmt.Errorf("commit research watch cursor: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil || rows != 1 {
		return errors.New("research watch cursor commit lost its lease CAS")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE l1_research_watch_refresh SET state='committed',committed_cursor=?,checkpoint_digest=?,execution_digest=?,receipt_json=?,attempt_digest='',attempt_ref='',retry_at=NULL,updated_at=? WHERE scope=? AND owner_id=? AND watch_id=? AND plan_id=? AND plan_digest=?`, cursorJSON, checkpointDigest, executionDigest, receiptJSON, now, binding.Scope, binding.OwnerID, binding.WatchID, binding.PlanID, binding.PlanDigest); err != nil {
		return fmt.Errorf("persist research watch commit receipt: %w", err)
	}
	return tx.Commit()
}

func (s *L1SQLiteStore) ScheduleResearchWatchRefreshRetry(ctx context.Context, binding L1ResearchWatchRefresh, attemptDigest, attemptRef string, retryAfter time.Duration, now time.Time) (time.Time, error) {
	if err := validateResearchWatchRefreshBinding(s, ctx, binding); err != nil {
		return time.Time{}, err
	}
	if attemptDigest == "" || attemptRef == "" {
		return time.Time{}, errors.New("research watch retry receipt identity is required")
	}
	if retryAfter <= 0 {
		retryAfter = L1ResearchWatchDefaultRetry
	}
	if retryAfter > L1ResearchWatchMaxRetry {
		retryAfter = L1ResearchWatchMaxRetry
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	retryAt := now.Add(retryAfter)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("begin research watch retry schedule: %w", err)
	}
	defer tx.Rollback()
	current, found, err := readL1ResearchWatchRefresh(ctx, tx, binding.Scope, binding.OwnerID, binding.WatchID)
	if err != nil || !found || !sameL1ResearchWatchRefreshPlan(current, binding) || current.State != "active" {
		return time.Time{}, errors.New("research watch retry plan is not active")
	}
	watch, found, err := readL1ResearchWatch(ctx, tx, binding.Scope, binding.OwnerID, binding.WatchID)
	if err != nil || !found || !watch.Enabled || watch.LeaseToken != binding.LeaseToken || !watch.LeaseUntil.After(now) || watch.CursorJSON != binding.PreviousCursor {
		return time.Time{}, errors.New("research watch retry lease is stale")
	}
	res, err := tx.ExecContext(ctx, `UPDATE l1_research_watch SET last_attempt_at=?,next_due_at=?,lease_token='',lease_until=NULL,updated_at=? WHERE watch_id=? AND scope=? AND owner_id=? AND enabled=1 AND lease_token=? AND lease_until>? AND cursor_json=?`, now, retryAt, now, binding.WatchID, binding.Scope, binding.OwnerID, binding.LeaseToken, now, binding.PreviousCursor)
	if err != nil {
		return time.Time{}, fmt.Errorf("release failed research watch lease: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil || rows != 1 {
		return time.Time{}, errors.New("research watch retry release lost its lease CAS")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE l1_research_watch_refresh SET state='retry_scheduled',attempt_digest=?,attempt_ref=?,retry_at=?,updated_at=? WHERE scope=? AND owner_id=? AND watch_id=? AND plan_id=? AND plan_digest=?`, attemptDigest, attemptRef, retryAt, now, binding.Scope, binding.OwnerID, binding.WatchID, binding.PlanID, binding.PlanDigest); err != nil {
		return time.Time{}, fmt.Errorf("persist research watch retry receipt: %w", err)
	}
	return retryAt, tx.Commit()
}

func validateResearchWatchRefreshBinding(s *L1SQLiteStore, ctx context.Context, binding L1ResearchWatchRefresh) error {
	if s == nil || s.db == nil || ctx == nil {
		return errors.New("research watch refresh owner and context are required")
	}
	if err := validateResearchWatchOwner(binding.Scope, binding.OwnerID); err != nil {
		return err
	}
	if strings.TrimSpace(binding.WatchID) == "" || strings.TrimSpace(binding.PlanID) == "" || strings.TrimSpace(binding.LeaseToken) == "" || strings.TrimSpace(binding.PlanDigest) == "" || binding.PlanJSON == "" || binding.OriginalExecutionJSON == "" || binding.CurrentInvocationJSON == "" || binding.SourcePolicyRevision == "" || binding.PreviousCursor == "" {
		return errors.New("research watch refresh binding is incomplete")
	}
	return nil
}

func sameL1ResearchWatchRefreshPlan(a, b L1ResearchWatchRefresh) bool {
	return a.Scope == b.Scope && a.OwnerID == b.OwnerID && a.WatchID == b.WatchID && a.PlanID == b.PlanID && a.LeaseToken == b.LeaseToken && a.PlanJSON == b.PlanJSON && a.PlanDigest == b.PlanDigest && a.OriginalExecutionJSON == b.OriginalExecutionJSON && a.SourcePolicyRevision == b.SourcePolicyRevision && a.PreviousCursor == b.PreviousCursor
}

func readL1ResearchWatchRefresh(ctx context.Context, queryer l1ResearchWatchQueryer, scope, ownerID, watchID string) (L1ResearchWatchRefresh, bool, error) {
	var item L1ResearchWatchRefresh
	var retryAt sql.NullTime
	err := queryer.QueryRowContext(ctx, `SELECT scope,owner_id,watch_id,plan_id,lease_token,plan_json,plan_digest,original_execution_json,current_invocation_json,source_policy_revision,previous_cursor,state,attempt_digest,attempt_ref,retry_at,committed_cursor,checkpoint_digest,execution_digest,receipt_json,updated_at FROM l1_research_watch_refresh WHERE scope=? AND owner_id=? AND watch_id=?`, scope, ownerID, watchID).Scan(
		&item.Scope, &item.OwnerID, &item.WatchID, &item.PlanID, &item.LeaseToken, &item.PlanJSON, &item.PlanDigest,
		&item.OriginalExecutionJSON, &item.CurrentInvocationJSON, &item.SourcePolicyRevision, &item.PreviousCursor,
		&item.State, &item.AttemptDigest, &item.AttemptRef, &retryAt, &item.CommittedCursor, &item.CheckpointDigest,
		&item.ExecutionDigest, &item.ReceiptJSON, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return L1ResearchWatchRefresh{}, false, nil
	}
	if err != nil {
		return L1ResearchWatchRefresh{}, false, fmt.Errorf("read research watch refresh binding: %w", err)
	}
	if retryAt.Valid {
		item.RetryAt = retryAt.Time.UTC()
	}
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item, true, nil
}

// RegisterResearchWatch accepts only source IDs already present and enabled
// in the canonical source registry. It never creates or updates registry rows.
func (s *L1SQLiteStore) RegisterResearchWatch(
	ctx context.Context,
	scope string,
	ownerID string,
	payloadJSON string,
	sourceRefs []string,
	cadence time.Duration,
	nextDueAt time.Time,
) (*L1ResearchWatch, error) {
	if s == nil || s.db == nil || ctx == nil {
		return nil, errors.New("l1 research watch store and context are required")
	}
	if err := validateResearchWatchOwner(scope, ownerID); err != nil {
		return nil, err
	}
	if cadence < L1ResearchWatchMinCadence || cadence > L1ResearchWatchMaxCadence {
		return nil, errors.New("l1 research watch cadence is outside the supported bounds")
	}
	canonicalPayload, err := canonicalResearchWatchJSON(payloadJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid l1 research watch payload: %w", err)
	}
	refs, err := normalizeResearchWatchSourceRefs(sourceRefs)
	if err != nil {
		return nil, err
	}
	if nextDueAt.IsZero() {
		nextDueAt = time.Now().UTC()
	}
	nextDueAt = nextDueAt.UTC()
	refsJSON, err := json.Marshal(refs)
	if err != nil {
		return nil, err
	}
	identityJSON, err := json.Marshal(struct {
		Payload    json.RawMessage `json:"payload"`
		SourceRefs []string        `json:"source_refs"`
		CadenceNS  int64           `json:"cadence_ns"`
	}{json.RawMessage(canonicalPayload), refs, int64(cadence)})
	if err != nil {
		return nil, err
	}
	payloadDigest := sha256Hex(identityJSON)
	watchID := "rw_" + sha256Hex([]byte(scope+"\x00"+ownerID+"\x00"+payloadDigest))
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin l1 research watch registration: %w", err)
	}
	if err := validateRegisteredResearchSources(ctx, tx, refs, cadence); err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	stored, found, err := readL1ResearchWatch(ctx, tx, scope, ownerID, watchID)
	if err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	if found {
		if !researchWatchPayloadMatches(stored, payloadDigest, canonicalPayload, refs, cadence) {
			return nil, rollbackL1Tx(tx, ErrL1ResearchWatchConflict)
		}
		if err := tx.Commit(); err != nil {
			return nil, rollbackL1Tx(tx, fmt.Errorf("commit l1 research watch idempotent readback: %w", err))
		}
		return &stored, nil
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_research_watch (
	watch_id, owner_id, scope, payload_json, payload_digest, source_refs_json,
	cadence_ns, next_due_at, last_attempt_at, last_success_at, cursor_json,
	enabled, lease_token, lease_until, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, '{}', 1, '', NULL, ?, ?)
ON CONFLICT(watch_id) DO NOTHING
`, watchID, ownerID, scope, canonicalPayload, payloadDigest, string(refsJSON), int64(cadence), nextDueAt, now, now)
	if err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("insert l1 research watch: %w", err))
	}
	stored, found, err = readL1ResearchWatch(ctx, tx, scope, ownerID, watchID)
	if err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	if !found {
		return nil, rollbackL1Tx(tx, errors.New("l1 research watch registration readback was missing"))
	}
	if !researchWatchPayloadMatches(stored, payloadDigest, canonicalPayload, refs, cadence) {
		return nil, rollbackL1Tx(tx, ErrL1ResearchWatchConflict)
	}
	if err := tx.Commit(); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("commit l1 research watch registration: %w", err))
	}
	return &stored, nil
}

func researchWatchPayloadMatches(stored L1ResearchWatch, digest, payload string, refs []string, cadence time.Duration) bool {
	return stored.PayloadDigest == digest && stored.PayloadJSON == payload && stored.SourceRefs != nil && sameStrings(stored.SourceRefs, refs) && stored.Cadence == cadence
}

// GetResearchWatch returns an exact owner/scope match. Unknown and foreign
// watch IDs are indistinguishable to callers.
func (s *L1SQLiteStore) GetResearchWatch(ctx context.Context, scope, ownerID, watchID string) (L1ResearchWatch, bool, error) {
	if s == nil || s.readDB == nil || ctx == nil {
		return L1ResearchWatch{}, false, errors.New("l1 research watch reader and context are required")
	}
	if err := validateResearchWatchOwner(scope, ownerID); err != nil {
		return L1ResearchWatch{}, false, err
	}
	return readL1ResearchWatch(ctx, s.readDB, scope, ownerID, strings.TrimSpace(watchID))
}

// DisableResearchWatch changes only the exact owner/scope watch and revokes
// its current lease so an in-flight scheduler cannot advance its cursor.
func (s *L1SQLiteStore) DisableResearchWatch(ctx context.Context, scope, ownerID, watchID string) (bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return false, errors.New("l1 research watch store and context are required")
	}
	if err := validateResearchWatchOwner(scope, ownerID); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE l1_research_watch
SET enabled = 0, lease_token = '', lease_until = NULL, updated_at = ?
WHERE watch_id = ? AND scope = ? AND owner_id = ?
`, time.Now().UTC(), strings.TrimSpace(watchID), scope, ownerID)
	if err != nil {
		return false, fmt.Errorf("disable l1 research watch: %w", err)
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// DueResearchWatches lists only due, unleased or expired-lease rows within one
// exact owner scope. It does not grant a lease.
func (s *L1SQLiteStore) DueResearchWatches(ctx context.Context, scope, ownerID string, now time.Time, limit int) ([]L1ResearchWatch, error) {
	if s == nil || s.readDB == nil || ctx == nil {
		return nil, errors.New("l1 research watch reader and context are required")
	}
	if err := validateResearchWatchOwner(scope, ownerID); err != nil {
		return nil, err
	}
	limit, err := boundedResearchWatchLimit(limit)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := s.readDB.QueryContext(ctx, `
SELECT `+l1ResearchWatchColumns+`
FROM l1_research_watch
WHERE scope = ? AND owner_id = ? AND enabled = 1
  AND next_due_at <= ?
  AND (lease_token = '' OR lease_until IS NULL OR lease_until <= ?)
ORDER BY next_due_at ASC, watch_id ASC
LIMIT ?
`, scope, ownerID, now.UTC(), now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("query due l1 research watches: %w", err)
	}
	defer rows.Close()
	return scanL1ResearchWatches(rows)
}

// ClaimDueResearchWatches atomically leases a bounded batch with SQLite's
// single-statement UPDATE/CAS. A competing claimant cannot receive the same
// still-live lease.
func (s *L1SQLiteStore) ClaimDueResearchWatches(ctx context.Context, scope, ownerID string, now time.Time, limit int) ([]L1ResearchWatch, error) {
	if s == nil || s.db == nil || ctx == nil {
		return nil, errors.New("l1 research watch store and context are required")
	}
	if err := validateResearchWatchOwner(scope, ownerID); err != nil {
		return nil, err
	}
	limit, err := boundedResearchWatchLimit(limit)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	leaseUntil := now.Add(L1ResearchWatchLeaseTTL)
	rows, err := s.db.QueryContext(ctx, `
UPDATE l1_research_watch
SET lease_token = lower(hex(randomblob(32))), lease_until = ?, last_attempt_at = ?, updated_at = ?
WHERE watch_id IN (
	SELECT watch_id FROM l1_research_watch
	WHERE scope = ? AND owner_id = ? AND enabled = 1
	  AND next_due_at <= ?
	  AND (lease_token = '' OR lease_until IS NULL OR lease_until <= ?)
	ORDER BY next_due_at ASC, watch_id ASC
	LIMIT ?
)
AND scope = ? AND owner_id = ? AND enabled = 1
AND next_due_at <= ?
AND (lease_token = '' OR lease_until IS NULL OR lease_until <= ?)
RETURNING `+l1ResearchWatchColumns+`
`, leaseUntil, now, now, scope, ownerID, now, now, limit, scope, ownerID, now, now)
	if err != nil {
		return nil, fmt.Errorf("claim due l1 research watches: %w", err)
	}
	defer rows.Close()
	claimed, err := scanL1ResearchWatches(rows)
	if err != nil {
		return nil, err
	}
	sort.Slice(claimed, func(i, j int) bool {
		if claimed[i].NextDueAt.Equal(claimed[j].NextDueAt) {
			return claimed[i].WatchID < claimed[j].WatchID
		}
		return claimed[i].NextDueAt.Before(claimed[j].NextDueAt)
	})
	return claimed, nil
}

// ClaimResearchWatch reserves only the exact requested due watch. It never
// leases unrelated due rows as a side effect of an explicit refresh request.
func (s *L1SQLiteStore) ClaimResearchWatch(ctx context.Context, scope, ownerID, watchID string, now time.Time) (L1ResearchWatch, bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return L1ResearchWatch{}, false, errors.New("l1 research watch store and context are required")
	}
	if err := validateResearchWatchOwner(scope, ownerID); err != nil {
		return L1ResearchWatch{}, false, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	item, err := scanL1ResearchWatch(s.db.QueryRowContext(ctx, `
UPDATE l1_research_watch
SET lease_token=lower(hex(randomblob(32))),lease_until=?,last_attempt_at=?,updated_at=?
WHERE watch_id=? AND scope=? AND owner_id=? AND enabled=1 AND next_due_at<=?
AND (lease_token='' OR lease_until IS NULL OR lease_until<=?)
RETURNING `+l1ResearchWatchColumns, now.Add(L1ResearchWatchLeaseTTL), now, now, watchID, scope, ownerID, now, now))
	if errors.Is(err, sql.ErrNoRows) {
		return L1ResearchWatch{}, false, nil
	}
	if err != nil {
		return L1ResearchWatch{}, false, fmt.Errorf("claim exact l1 research watch: %w", err)
	}
	return item, true, nil
}

// CommitResearchWatchCursor advances the cursor only after successful external
// receipt verification by the caller. Failure records an attempt and retry due
// time while preserving the prior cursor and last success.
func (s *L1SQLiteStore) CommitResearchWatchCursor(
	ctx context.Context,
	scope string,
	ownerID string,
	watchID string,
	leaseToken string,
	cursorJSON string,
	succeeded bool,
	now time.Time,
	retryAfter time.Duration,
) error {
	if s == nil || s.db == nil || ctx == nil {
		return errors.New("l1 research watch store and context are required")
	}
	if err := validateResearchWatchOwner(scope, ownerID); err != nil {
		return err
	}
	watchID = strings.TrimSpace(watchID)
	leaseToken = strings.TrimSpace(leaseToken)
	if watchID == "" || leaseToken == "" {
		return errors.New("l1 research watch id and lease token are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	var cursor any
	var nextDue time.Time
	if succeeded {
		canonicalCursor, err := canonicalResearchWatchJSON(cursorJSON)
		if err != nil {
			return fmt.Errorf("invalid l1 research watch cursor: %w", err)
		}
		cursor = canonicalCursor
		// Calculate timestamps in Go to retain sub-second cadence precision.
		var cadenceNS int64
		if err := s.db.QueryRowContext(ctx, `SELECT cadence_ns FROM l1_research_watch WHERE watch_id = ? AND scope = ? AND owner_id = ?`, watchID, scope, ownerID).Scan(&cadenceNS); err != nil {
			return fmt.Errorf("read l1 research watch cadence: %w", err)
		}
		nextDue = now.Add(time.Duration(cadenceNS))
	} else {
		if retryAfter <= 0 {
			retryAfter = L1ResearchWatchDefaultRetry
		}
		if retryAfter > L1ResearchWatchMaxRetry {
			retryAfter = L1ResearchWatchMaxRetry
		}
		nextDue = now.Add(retryAfter)
	}
	var result sql.Result
	var err error
	if succeeded {
		result, err = s.db.ExecContext(ctx, `
UPDATE l1_research_watch
SET cursor_json = ?, last_attempt_at = ?, last_success_at = ?, next_due_at = ?, lease_token = '', lease_until = NULL, updated_at = ?
WHERE watch_id = ? AND scope = ? AND owner_id = ? AND enabled = 1 AND lease_token = ? AND lease_until > ?
`, cursor, now, now, nextDue, now, watchID, scope, ownerID, leaseToken, now)
	} else {
		result, err = s.db.ExecContext(ctx, `
UPDATE l1_research_watch
SET last_attempt_at = ?, next_due_at = ?, lease_token = '', lease_until = NULL, updated_at = ?
WHERE watch_id = ? AND scope = ? AND owner_id = ? AND enabled = 1 AND lease_token = ? AND lease_until > ?
`, now, nextDue, now, watchID, scope, ownerID, leaseToken, now)
	}
	if err != nil {
		return fmt.Errorf("commit l1 research watch cursor: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect l1 research watch cursor commit: %w", err)
	}
	if rows != 1 {
		return errors.New("l1 research watch lease is stale, expired, disabled, or outside owner scope")
	}
	return nil
}

const l1ResearchWatchColumns = `watch_id, owner_id, scope, payload_json, payload_digest, source_refs_json,
cadence_ns, next_due_at, last_attempt_at, last_success_at, cursor_json, enabled,
lease_token, lease_until, created_at, updated_at`

type l1ResearchWatchQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readL1ResearchWatch(ctx context.Context, queryer l1ResearchWatchQueryer, scope, ownerID, watchID string) (L1ResearchWatch, bool, error) {
	item, err := scanL1ResearchWatch(queryer.QueryRowContext(ctx, `
SELECT `+l1ResearchWatchColumns+` FROM l1_research_watch WHERE scope = ? AND owner_id = ? AND watch_id = ?
`, scope, ownerID, watchID))
	if errors.Is(err, sql.ErrNoRows) {
		return L1ResearchWatch{}, false, nil
	}
	if err != nil {
		return L1ResearchWatch{}, false, err
	}
	return item, true, nil
}

type l1ResearchWatchScanner interface {
	Scan(...any) error
}

func scanL1ResearchWatch(scanner l1ResearchWatchScanner) (L1ResearchWatch, error) {
	var item L1ResearchWatch
	var cadenceNS int64
	var enabled int
	var lastAttemptAt, lastSuccessAt, leaseUntil sql.NullTime
	var sourceRefsJSON string
	err := scanner.Scan(
		&item.WatchID, &item.OwnerID, &item.Scope, &item.PayloadJSON, &item.PayloadDigest, &sourceRefsJSON,
		&cadenceNS, &item.NextDueAt, &lastAttemptAt, &lastSuccessAt, &item.CursorJSON, &enabled,
		&item.LeaseToken, &leaseUntil, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return L1ResearchWatch{}, err
	}
	if err := json.Unmarshal([]byte(sourceRefsJSON), &item.SourceRefs); err != nil {
		return L1ResearchWatch{}, fmt.Errorf("decode l1 research watch source refs: %w", err)
	}
	item.Cadence = time.Duration(cadenceNS)
	item.Enabled = enabled != 0
	item.LastAttemptAt = nullTimeValue(lastAttemptAt)
	item.LastSuccessAt = nullTimeValue(lastSuccessAt)
	item.LeaseUntil = nullTimeValue(leaseUntil)
	return item, nil
}

func scanL1ResearchWatches(rows *sql.Rows) ([]L1ResearchWatch, error) {
	var items []L1ResearchWatch
	for rows.Next() {
		item, err := scanL1ResearchWatch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan l1 research watch: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate l1 research watches: %w", err)
	}
	return items, nil
}

func canonicalResearchWatchJSON(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > l1ResearchWatchMaxJSON {
		return "", errors.New("json is empty or exceeds the size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if _, ok := value.(map[string]any); !ok {
		return "", errors.New("json value must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("json contains trailing values")
		}
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func validateRegisteredResearchSources(ctx context.Context, tx *sql.Tx, refs []string, cadence time.Duration) error {
	rows, err := tx.QueryContext(ctx, `
SELECT source_id, url, kind, trust_score, fetch_interval_sec, license_note,
       enabled, meta_json, last_fetched_at, last_status, last_error, created_at, updated_at
FROM l1_source_registry WHERE source_id IN (`+placeholders(len(refs))+`)
`, stringSliceAny(refs)...)
	if err != nil {
		return fmt.Errorf("read registered research sources: %w", err)
	}
	entries, scanErr := scanL1SourceRegistryEntries(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return scanErr
	}
	if closeErr != nil {
		return closeErr
	}
	byID := make(map[string]L1SourceRegistryEntry, len(entries))
	for _, entry := range entries {
		byID[entry.SourceID] = entry
	}
	for _, ref := range refs {
		entry, ok := byID[ref]
		if !ok || !entry.Enabled || strings.TrimSpace(entry.LicenseNote) == "" || !isPublicResearchSourceURL(entry.URL) {
			return fmt.Errorf("research watch source %q is unknown, disabled, unlicensed, or outside the public-source boundary", ref)
		}
		if entry.FetchInterval > cadence {
			return fmt.Errorf("research watch cadence is shorter than source %q fetch interval", ref)
		}
	}
	return nil
}

func normalizeResearchWatchSourceRefs(sourceRefs []string) ([]string, error) {
	if len(sourceRefs) == 0 || len(sourceRefs) > l1ResearchWatchMaxSources {
		return nil, errors.New("research watch requires between 1 and 100 source refs")
	}
	refs := make([]string, 0, len(sourceRefs))
	seen := make(map[string]struct{}, len(sourceRefs))
	for _, original := range sourceRefs {
		ref := strings.TrimSpace(original)
		if ref == "" || ref != original || strings.ContainsAny(ref, " \t\r\n") {
			return nil, errors.New("research watch source refs must be canonical source IDs")
		}
		if _, exists := seen[ref]; exists {
			return nil, fmt.Errorf("duplicate research watch source ref %q", ref)
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

func isPublicResearchSourceURL(raw string) bool {
	if validateOptionalSourceURL(raw) != nil {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
	}
	return strings.Contains(host, ".")
}

func validateResearchWatchOwner(scope, ownerID string) error {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(ownerID) != ownerID {
		return errors.New("research watch owner id must be non-empty and canonical")
	}
	if scope != "public" && scope != "user" {
		return errors.New("research watch scope must be public or user")
	}
	return nil
}

func boundedResearchWatchLimit(limit int) (int, error) {
	if limit == 0 {
		return L1ResearchWatchDefaultLimit, nil
	}
	if limit < 0 || limit > L1ResearchWatchMaxLimit {
		return 0, errors.New("research watch limit must be between 1 and 100")
	}
	return limit, nil
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringSliceAny(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
