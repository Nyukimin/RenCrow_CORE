package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func (s *L1SQLiteStore) ClaimProfilePromotionBatch(
	ctx context.Context,
	limit int,
	maxAttempts int,
	leaseDuration time.Duration,
	now time.Time,
) (*domainmemory.ProfilePromotionBatch, error) {
	if limit <= 0 {
		limit = 24
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if leaseDuration <= 0 {
		leaseDuration = time.Minute
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_profile_promotion_job (
	evidence_event_id, session_id, thread_id, state, attempt_count,
	lease_token, last_error, created_at, updated_at
)
SELECT id, session_id, thread_id, ?, 0, '', '', created_at, ?
FROM l1_memory_event
WHERE namespace LIKE 'conv:%' AND speaker = ? AND memory_state = ?
`, domainmemory.ProfilePromotionPending, now, string(domconv.SpeakerUser), MemoryStateObserved); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("enqueue existing profile promotion jobs: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, lease_token = '', lease_expires_at = NULL, updated_at = ?
WHERE state = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?
`, domainmemory.ProfilePromotionPending, now, domainmemory.ProfilePromotionRunning, now); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("recover expired profile promotion leases: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, next_attempt_at = NULL, updated_at = ?
WHERE state = ? AND next_attempt_at IS NOT NULL AND next_attempt_at <= ?
`, domainmemory.ProfilePromotionPending, now, domainmemory.ProfilePromotionRetryWait, now); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("release profile promotion retry wait: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, updated_at = ?
WHERE state IN (?, ?) AND attempt_count >= ?
`, domainmemory.ProfilePromotionFailed, now, domainmemory.ProfilePromotionPending, domainmemory.ProfilePromotionRetryWait, maxAttempts); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("finalize exhausted profile promotion jobs: %w", err))
	}

	var sessionID string
	var threadID int64
	err = tx.QueryRowContext(ctx, `
SELECT j.session_id, j.thread_id
FROM l1_profile_promotion_job j
JOIN l1_memory_event e ON e.id = j.evidence_event_id
WHERE j.state = ? AND j.attempt_count < ?
ORDER BY CASE WHEN e.source = 'chatgpt_export' THEN 1 ELSE 0 END ASC,
	j.created_at ASC, j.evidence_event_id ASC
LIMIT 1
`, domainmemory.ProfilePromotionPending, maxAttempts).Scan(&sessionID, &threadID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, rollbackL1Tx(tx, err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("select profile promotion batch: %w", err))
	}
	rows, err := tx.QueryContext(ctx, `
SELECT j.evidence_event_id, j.session_id, j.thread_id, e.message, e.created_at
FROM l1_profile_promotion_job j
JOIN l1_memory_event e ON e.id = j.evidence_event_id
WHERE j.state = ? AND j.attempt_count < ? AND j.session_id = ? AND j.thread_id = ?
ORDER BY j.created_at ASC, j.evidence_event_id ASC
LIMIT ?
`, domainmemory.ProfilePromotionPending, maxAttempts, sessionID, threadID, limit)
	if err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("load profile promotion batch: %w", err))
	}
	var messages []domainmemory.ProfilePromotionMessage
	for rows.Next() {
		var item domainmemory.ProfilePromotionMessage
		if err := rows.Scan(&item.EventID, &item.SessionID, &item.ThreadID, &item.Text, &item.CreatedAt); err != nil {
			rows.Close()
			return nil, rollbackL1Tx(tx, fmt.Errorf("scan profile promotion batch: %w", err))
		}
		messages = append(messages, item)
	}
	if err := rows.Close(); err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	if err := rows.Err(); err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	if len(messages) == 0 {
		return nil, rollbackL1Tx(tx, errors.New("profile promotion batch lost its evidence events"))
	}
	leaseToken := fmt.Sprintf("profile-promotion:%d:%d", now.UnixNano(), l1IDSequence.Add(1))
	leaseExpiresAt := now.Add(leaseDuration)
	for _, item := range messages {
		result, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, lease_token = ?, lease_expires_at = ?, updated_at = ?
WHERE evidence_event_id = ? AND state = ?
`, domainmemory.ProfilePromotionRunning, leaseToken, leaseExpiresAt, now, item.EventID, domainmemory.ProfilePromotionPending)
		if err != nil {
			return nil, rollbackL1Tx(tx, fmt.Errorf("claim profile promotion job: %w", err))
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return nil, rollbackL1Tx(tx, errors.New("profile promotion job claim conflict"))
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	return &domainmemory.ProfilePromotionBatch{
		LeaseToken: leaseToken,
		SessionID:  sessionID,
		ThreadID:   threadID,
		Messages:   messages,
	}, nil
}

func (s *L1SQLiteStore) CompleteProfilePromotionBatch(
	ctx context.Context,
	batch domainmemory.ProfilePromotionBatch,
	candidates []domainmemory.ProfileCandidate,
	userID string,
	now time.Time,
) (int, error) {
	if strings.TrimSpace(batch.LeaseToken) == "" || len(batch.Messages) == 0 {
		return 0, errors.New("profile promotion lease is required")
	}
	if strings.TrimSpace(userID) == "" {
		userID = "ren"
	}
	namespace, err := BuildL1Namespace(NamespaceKindUser, userID)
	if err != nil {
		return 0, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	evidenceIDs := make([]string, 0, len(batch.Messages))
	for _, item := range batch.Messages {
		evidenceIDs = append(evidenceIDs, item.EventID)
	}
	sort.Strings(evidenceIDs)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	saved := 0
	for _, candidate := range candidates {
		statement := strings.TrimSpace(candidate.Statement)
		if statement == "" {
			continue
		}
		memoryType := strings.TrimSpace(candidate.Type)
		if memoryType == "" {
			memoryType = domainmemory.UserMemoryTypeProfile
		}
		if err := domainmemory.ValidateUserMemoryType(memoryType); err != nil {
			return 0, rollbackL1Tx(tx, err)
		}
		confidence := candidate.Confidence
		if confidence <= 0 {
			confidence = 0.5
		}
		sensitivity := strings.TrimSpace(candidate.Sensitivity)
		if sensitivity == "" {
			sensitivity = "normal"
		}
		scope := strings.TrimSpace(candidate.Scope)
		if scope == "" {
			scope = "all_personas"
		}
		if err := domainmemory.CanPromoteUserMemory(MemoryStateCandidate, evidenceIDs, sensitivity, "profile_extractor"); err != nil {
			return 0, rollbackL1Tx(tx, err)
		}
		id := deterministicProfileCandidateID(namespace, evidenceIDs, statement)
		meta := map[string]interface{}{
			"type": memoryType, "user_id": userID, "statement": statement,
			"evidence_event_ids": evidenceIDs, "confidence": confidence,
			"sensitivity": sensitivity, "scope": scope, "active": true,
		}
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return 0, rollbackL1Tx(tx, err)
		}
		result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, '', 0, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, namespace, string(domconv.SpeakerMemory), statement, string(metaJSON),
			MemoryStateCandidate, MemoryLayerL1, "profile_extractor", now, now)
		if err != nil {
			return 0, rollbackL1Tx(tx, fmt.Errorf("save profile candidate: %w", err))
		}
		affected, _ := result.RowsAffected()
		if affected == 1 {
			saved++
			if _, err := appendL1EventLog(ctx, tx, "memory.user_created", namespace, batch.SessionID, batch.ThreadID, map[string]interface{}{
				"memory_id": id, "user_id": userID, "type": memoryType,
				"memory_state": MemoryStateCandidate, "evidence_event_ids": evidenceIDs,
			}, "profile_extractor"); err != nil {
				return 0, rollbackL1Tx(tx, err)
			}
		}
	}
	for _, item := range batch.Messages {
		result, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, lease_token = '', lease_expires_at = NULL,
	next_attempt_at = NULL, last_error = '', updated_at = ?
WHERE evidence_event_id = ? AND state = ? AND lease_token = ?
`, domainmemory.ProfilePromotionCompleted, now, item.EventID, domainmemory.ProfilePromotionRunning, batch.LeaseToken)
		if err != nil {
			return 0, rollbackL1Tx(tx, err)
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return 0, rollbackL1Tx(tx, errors.New("profile promotion lease lost before commit"))
		}
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.profile_promotion_completed", "conv:profile-promotion", batch.SessionID, batch.ThreadID, map[string]interface{}{
		"evidence_event_ids": evidenceIDs,
		"candidate_count":    saved,
	}, "profile_extractor"); err != nil {
		return 0, rollbackL1Tx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, rollbackL1Tx(tx, err)
	}
	return saved, nil
}

func (s *L1SQLiteStore) DeferProfilePromotionBatch(ctx context.Context, batch domainmemory.ProfilePromotionBatch, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.updateProfilePromotionLease(ctx, batch, `
state = ?, lease_token = '', lease_expires_at = NULL, next_attempt_at = NULL, updated_at = ?
`, domainmemory.ProfilePromotionPending, now.UTC())
}

func (s *L1SQLiteStore) FailProfilePromotionBatch(
	ctx context.Context,
	batch domainmemory.ProfilePromotionBatch,
	maxAttempts int,
	now time.Time,
	errorText string,
) error {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, item := range batch.Messages {
		var attempts int
		if err := tx.QueryRowContext(ctx, `
SELECT attempt_count FROM l1_profile_promotion_job
WHERE evidence_event_id = ? AND state = ? AND lease_token = ?
`, item.EventID, domainmemory.ProfilePromotionRunning, batch.LeaseToken).Scan(&attempts); err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("load profile promotion attempt: %w", err))
		}
		attempts++
		state := domainmemory.ProfilePromotionRetryWait
		var nextAttempt interface{} = now.Add(profilePromotionBackoff(attempts))
		if attempts >= maxAttempts {
			state = domainmemory.ProfilePromotionFailed
			nextAttempt = nil
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, attempt_count = ?, lease_token = '', lease_expires_at = NULL,
	next_attempt_at = ?, last_error = ?, updated_at = ?
WHERE evidence_event_id = ? AND lease_token = ?
`, state, attempts, nextAttempt, compactProfilePromotionError(errorText), now, item.EventID, batch.LeaseToken); err != nil {
			return rollbackL1Tx(tx, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return rollbackL1Tx(tx, err)
	}
	return nil
}

// RetryFailedProfilePromotionJobs explicitly requeues terminal failed jobs
// whose raw evidence still exists. The transition and its audit event share a
// transaction so a retry request cannot report success without its evidence
// remaining observable.
func (s *L1SQLiteStore) RetryFailedProfilePromotionJobs(ctx context.Context, now time.Time) (domainmemory.ProfilePromotionRetryResult, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.ProfilePromotionRetryResult{}, err
	}
	var missingEvidence int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM l1_profile_promotion_job j
LEFT JOIN l1_memory_event e ON e.id = j.evidence_event_id
WHERE j.state = ? AND e.id IS NULL
`, domainmemory.ProfilePromotionFailed).Scan(&missingEvidence); err != nil {
		return domainmemory.ProfilePromotionRetryResult{}, rollbackL1Tx(tx, fmt.Errorf("count missing profile promotion evidence: %w", err))
	}
	result, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, attempt_count = 0, lease_token = '', lease_expires_at = NULL,
	next_attempt_at = NULL, updated_at = ?
WHERE state = ?
  AND EXISTS (
	SELECT 1 FROM l1_memory_event e WHERE e.id = l1_profile_promotion_job.evidence_event_id
  )
`, domainmemory.ProfilePromotionPending, now, domainmemory.ProfilePromotionFailed)
	if err != nil {
		return domainmemory.ProfilePromotionRetryResult{}, rollbackL1Tx(tx, fmt.Errorf("requeue failed profile promotion jobs: %w", err))
	}
	requeued64, err := result.RowsAffected()
	if err != nil {
		return domainmemory.ProfilePromotionRetryResult{}, rollbackL1Tx(tx, fmt.Errorf("count requeued profile promotion jobs: %w", err))
	}
	requeued := int(requeued64)
	if requeued > 0 {
		if _, err := appendL1EventLog(ctx, tx, "memory.profile_promotion_retry_requested", "conv:profile-promotion", "", 0, map[string]interface{}{
			"requeued_count":         requeued,
			"missing_evidence_count": missingEvidence,
		}, "viewer"); err != nil {
			return domainmemory.ProfilePromotionRetryResult{}, rollbackL1Tx(tx, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.ProfilePromotionRetryResult{}, rollbackL1Tx(tx, err)
	}
	return domainmemory.ProfilePromotionRetryResult{
		RequeuedCount:        requeued,
		MissingEvidenceCount: missingEvidence,
	}, nil
}

// ProfilePromotionDiagnostics returns all-row counters independently from the
// limited job detail page used by the Viewer.
func (s *L1SQLiteStore) ProfilePromotionDiagnostics(ctx context.Context) (domainmemory.ProfilePromotionDiagnostics, error) {
	stateCounts := map[string]int{
		domainmemory.ProfilePromotionPending:   0,
		domainmemory.ProfilePromotionRunning:   0,
		domainmemory.ProfilePromotionRetryWait: 0,
		domainmemory.ProfilePromotionCompleted: 0,
		domainmemory.ProfilePromotionFailed:    0,
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT state, COUNT(*)
FROM l1_profile_promotion_job
GROUP BY state
`)
	if err != nil {
		return domainmemory.ProfilePromotionDiagnostics{}, err
	}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return domainmemory.ProfilePromotionDiagnostics{}, err
		}
		stateCounts[state] = count
	}
	if err := rows.Close(); err != nil {
		return domainmemory.ProfilePromotionDiagnostics{}, err
	}
	if err := rows.Err(); err != nil {
		return domainmemory.ProfilePromotionDiagnostics{}, err
	}

	var failed, retryable, missing int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
	COALESCE(SUM(CASE WHEN e.id IS NOT NULL THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN e.id IS NULL THEN 1 ELSE 0 END), 0)
FROM l1_profile_promotion_job j
LEFT JOIN l1_memory_event e ON e.id = j.evidence_event_id
WHERE j.state = ?
`, domainmemory.ProfilePromotionFailed).Scan(&failed, &retryable, &missing); err != nil {
		return domainmemory.ProfilePromotionDiagnostics{}, err
	}
	stats := s.db.Stats()
	return domainmemory.ProfilePromotionDiagnostics{
		StateCounts:                stateCounts,
		FailedCount:                failed,
		RetryableFailedCount:       retryable,
		MissingEvidenceFailedCount: missing,
		DBPoolStats: domainmemory.L1DBPoolStats{
			Max:                stats.MaxOpenConnections,
			Open:               stats.OpenConnections,
			InUse:              stats.InUse,
			Idle:               stats.Idle,
			PoolWaitCount:      stats.WaitCount,
			PoolWaitDurationMS: stats.WaitDuration.Milliseconds(),
		},
	}, nil
}

func (s *L1SQLiteStore) updateProfilePromotionLease(
	ctx context.Context,
	batch domainmemory.ProfilePromotionBatch,
	setClause string,
	args ...interface{},
) error {
	if strings.TrimSpace(batch.LeaseToken) == "" || len(batch.Messages) == 0 {
		return errors.New("profile promotion lease is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, item := range batch.Messages {
		queryArgs := append(append([]interface{}{}, args...), item.EventID, domainmemory.ProfilePromotionRunning, batch.LeaseToken)
		result, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job SET `+setClause+`
WHERE evidence_event_id = ? AND state = ? AND lease_token = ?
`, queryArgs...)
		if err != nil {
			return rollbackL1Tx(tx, err)
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return rollbackL1Tx(tx, errors.New("profile promotion lease is no longer active"))
		}
	}
	if err := tx.Commit(); err != nil {
		return rollbackL1Tx(tx, err)
	}
	return nil
}

func (s *L1SQLiteStore) ListProfilePromotionJobs(ctx context.Context, limit int) ([]domainmemory.ProfilePromotionJob, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT evidence_event_id, session_id, thread_id, state, attempt_count,
	lease_token, lease_expires_at, next_attempt_at, last_error, created_at, updated_at
FROM l1_profile_promotion_job
ORDER BY created_at DESC, evidence_event_id DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domainmemory.ProfilePromotionJob
	for rows.Next() {
		var item domainmemory.ProfilePromotionJob
		var leaseExpiresAt, nextAttemptAt sql.NullTime
		if err := rows.Scan(
			&item.EvidenceEventID, &item.SessionID, &item.ThreadID, &item.State, &item.AttemptCount,
			&item.LeaseToken, &leaseExpiresAt, &nextAttemptAt, &item.LastError, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if leaseExpiresAt.Valid {
			item.LeaseExpiresAt = leaseExpiresAt.Time
		}
		if nextAttemptAt.Valid {
			item.NextAttemptAt = nextAttemptAt.Time
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func deterministicProfileCandidateID(namespace string, evidenceIDs []string, statement string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
	sum := sha256.Sum256([]byte(strings.Join(evidenceIDs, "\n") + "\n" + normalized))
	return namespace + ":profile_candidate:" + hex.EncodeToString(sum[:16])
}

func profilePromotionBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Minute * time.Duration(1<<(attempt-1))
}

func compactProfilePromotionError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 600 {
		return value[:600]
	}
	return value
}
