package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type MemoryLifecycleOptions struct {
	Now                      time.Time
	RawConversationRetention time.Duration
	CandidateReviewAfter     time.Duration
	MonthlyHighlightAfter    time.Duration
	ThreadSummarySeedAfter   time.Duration
	DecayAfter               time.Duration
	RawCompactLimit          int
	CandidateReviewLimit     int
	MonthlyHighlightLimit    int
	ThreadSummarySeedLimit   int
	DecayLimit               int
	VectorCleanupLimit       int
}

type MemoryLifecycleResult struct {
	RawCompacted             int
	CandidatesQueued         int
	MonthlyHighlightsBuilt   int
	ThreadSummarySeedsQueued int
	Decayed                  int
	VectorCleanupQueued      int
	VectorCleanupExecuted    int
}

func DefaultMemoryLifecycleOptions() MemoryLifecycleOptions {
	return MemoryLifecycleOptions{
		Now:                      time.Now().UTC(),
		RawConversationRetention: 30 * 24 * time.Hour,
		CandidateReviewAfter:     7 * 24 * time.Hour,
		MonthlyHighlightAfter:    14 * 24 * time.Hour,
		ThreadSummarySeedAfter:   14 * 24 * time.Hour,
		DecayAfter:               90 * 24 * time.Hour,
		RawCompactLimit:          1000,
		CandidateReviewLimit:     200,
		MonthlyHighlightLimit:    50,
		ThreadSummarySeedLimit:   200,
		DecayLimit:               200,
		VectorCleanupLimit:       200,
	}
}

func (s *L1SQLiteStore) RunMemoryLifecycleMaintenance(ctx context.Context, opts MemoryLifecycleOptions) (*MemoryLifecycleResult, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	opts = normalizeMemoryLifecycleOptions(opts)
	result := &MemoryLifecycleResult{}
	if opts.RawConversationRetention > 0 {
		n, err := s.compactOldConversationRaw(ctx, opts.Now.Add(-opts.RawConversationRetention), opts.RawCompactLimit)
		if err != nil {
			return nil, err
		}
		result.RawCompacted = n
	}
	if opts.CandidateReviewAfter > 0 {
		n, err := s.queueUserMemoryCandidateReview(ctx, opts.Now, opts.Now.Add(-opts.CandidateReviewAfter), opts.CandidateReviewLimit)
		if err != nil {
			return nil, err
		}
		result.CandidatesQueued = n
	}
	if opts.MonthlyHighlightAfter > 0 {
		n, err := s.buildMonthlyHighlights(ctx, opts.Now, opts.Now.Add(-opts.MonthlyHighlightAfter), opts.MonthlyHighlightLimit)
		if err != nil {
			return nil, err
		}
		result.MonthlyHighlightsBuilt = n
	}
	if opts.ThreadSummarySeedAfter > 0 {
		n, err := s.queueThreadSummaryMonthlySeeds(ctx, opts.Now, opts.Now.Add(-opts.ThreadSummarySeedAfter), opts.ThreadSummarySeedLimit)
		if err != nil {
			return nil, err
		}
		result.ThreadSummarySeedsQueued = n
	}
	if opts.DecayAfter > 0 {
		n, err := s.markDecayedUserMemories(ctx, opts.Now, opts.Now.Add(-opts.DecayAfter), opts.DecayLimit)
		if err != nil {
			return nil, err
		}
		result.Decayed = n
	}
	n, err := s.queueVectorCleanup(ctx, opts.Now, opts.VectorCleanupLimit)
	if err != nil {
		return nil, err
	}
	result.VectorCleanupQueued = n
	n, err = s.executeQueuedVectorCleanup(ctx, opts.Now, opts.VectorCleanupLimit)
	if err != nil {
		return nil, err
	}
	result.VectorCleanupExecuted = n
	if result.RawCompacted > 0 || result.CandidatesQueued > 0 || result.MonthlyHighlightsBuilt > 0 || result.ThreadSummarySeedsQueued > 0 || result.Decayed > 0 || result.VectorCleanupQueued > 0 || result.VectorCleanupExecuted > 0 {
		if _, err := s.AppendEvent(ctx, "memory.lifecycle_maintenance_completed", "conv:lifecycle", "", 0, map[string]interface{}{
			"raw_compacted":               result.RawCompacted,
			"candidates_queued":           result.CandidatesQueued,
			"monthly_highlights_built":    result.MonthlyHighlightsBuilt,
			"thread_summary_seeds_queued": result.ThreadSummarySeedsQueued,
			"decayed":                     result.Decayed,
			"vector_cleanup_queued":       result.VectorCleanupQueued,
			"vector_cleanup_executed":     result.VectorCleanupExecuted,
		}, "memory_lifecycle"); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func normalizeMemoryLifecycleOptions(opts MemoryLifecycleOptions) MemoryLifecycleOptions {
	defaults := DefaultMemoryLifecycleOptions()
	if opts.Now.IsZero() {
		opts.Now = defaults.Now
	}
	if opts.RawCompactLimit <= 0 {
		opts.RawCompactLimit = defaults.RawCompactLimit
	}
	if opts.CandidateReviewLimit <= 0 {
		opts.CandidateReviewLimit = defaults.CandidateReviewLimit
	}
	if opts.MonthlyHighlightLimit <= 0 {
		opts.MonthlyHighlightLimit = defaults.MonthlyHighlightLimit
	}
	if opts.ThreadSummarySeedLimit <= 0 {
		opts.ThreadSummarySeedLimit = defaults.ThreadSummarySeedLimit
	}
	if opts.DecayLimit <= 0 {
		opts.DecayLimit = defaults.DecayLimit
	}
	if opts.VectorCleanupLimit <= 0 {
		opts.VectorCleanupLimit = defaults.VectorCleanupLimit
	}
	return opts
}

func (s *L1SQLiteStore) compactOldConversationRaw(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if err := s.queueOldConversationRaw(ctx, cutoff, limit); err != nil {
		return 0, err
	}
	return s.drainRawLifecycleArchiveOutbox(ctx, cutoff, limit)
}

type rawLifecycleOutboxEntry struct {
	OutboxID    string
	EventID     string
	Namespace   string
	EventSHA256 string
	Status      string
	CreatedAt   time.Time
}

func (s *L1SQLiteStore) queueOldConversationRaw(ctx context.Context, cutoff time.Time, limit int) error {
	if limit <= 0 {
		return nil
	}
	events, err := s.rawLifecycleArchiveCandidates(ctx, cutoff, limit)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin raw archive outbox queue transaction: %w", err)
	}
	now := time.Now().UTC()
	var conflictErr error
	for _, event := range events {
		eligible, err := rawLifecycleArchiveCandidateEligible(ctx, tx, event.ID, cutoff)
		if err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("failed to revalidate raw archive candidate %s: %w", event.ID, err))
		}
		if !eligible {
			continue
		}
		eventSHA256, err := CanonicalL1MemoryEventSHA256(event)
		if err != nil {
			return rollbackL1Tx(tx, fmt.Errorf("failed to hash raw archive candidate %s: %w", event.ID, err))
		}
		outboxID := L1RawLifecycleOutboxID(event.ID, eventSHA256)
		var existingOutboxID, existingNamespace, existingHash, existingStatus string
		err = tx.QueryRowContext(ctx, `
SELECT outbox_id, namespace, event_sha256, status
FROM conversation_lifecycle_raw_archive_outbox
WHERE event_id = ?`, event.ID).Scan(&existingOutboxID, &existingNamespace, &existingHash, &existingStatus)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_lifecycle_raw_archive_outbox (
	outbox_id, event_id, namespace, event_sha256, status, attempt_count,
	last_error, created_at, updated_at, archived_at
) VALUES (?, ?, ?, ?, ?, 0, '', ?, ?, NULL)`, outboxID, event.ID, event.Namespace,
				eventSHA256, L1RawLifecycleArchiveOutboxStatusPending, now, now); err != nil {
				return rollbackL1Tx(tx, fmt.Errorf("failed to queue raw archive event %s: %w", event.ID, err))
			}
			if _, err := appendL1EventLog(ctx, tx, "memory.l1_raw_archive_queued", "conv:lifecycle", event.SessionID, event.ThreadID, map[string]interface{}{
				"outbox_id":    outboxID,
				"event_id":     event.ID,
				"event_sha256": eventSHA256,
				"status":       L1RawLifecycleArchiveOutboxStatusPending,
			}, "memory_lifecycle"); err != nil {
				return rollbackL1Tx(tx, err)
			}
		case err != nil:
			return rollbackL1Tx(tx, fmt.Errorf("failed to read raw archive outbox binding for %s: %w", event.ID, err))
		case existingHash != eventSHA256 || existingNamespace != event.Namespace || existingOutboxID != outboxID:
			conflictErr = fmt.Errorf("%w: raw archive outbox binding changed for event %s", ErrL1RawLifecycleArchiveConflict, event.ID)
			if _, err := tx.ExecContext(ctx, `
UPDATE conversation_lifecycle_raw_archive_outbox
SET status = ?, last_error = ?, updated_at = ?
WHERE event_id = ? AND status <> ?`, L1RawLifecycleArchiveOutboxStatusFailed, conflictErr.Error(), now, event.ID, L1RawLifecycleArchiveOutboxStatusArchived); err != nil {
				return rollbackL1Tx(tx, fmt.Errorf("failed to mark raw archive outbox conflict for %s: %w", event.ID, err))
			}
			if _, err := appendL1EventLog(ctx, tx, "memory.l1_raw_archive_conflict", "conv:lifecycle", event.SessionID, event.ThreadID, map[string]interface{}{
				"outbox_id":    existingOutboxID,
				"event_id":     event.ID,
				"event_sha256": eventSHA256,
				"last_error":   conflictErr.Error(),
			}, "memory_lifecycle"); err != nil {
				return rollbackL1Tx(tx, err)
			}
		default:
			if existingStatus == L1RawLifecycleArchiveOutboxStatusArchived {
				continue
			}
			if existingStatus == L1RawLifecycleArchiveOutboxStatusFailed {
				if _, err := tx.ExecContext(ctx, `
UPDATE conversation_lifecycle_raw_archive_outbox
SET status = ?, last_error = '', updated_at = ?
WHERE event_id = ?`, L1RawLifecycleArchiveOutboxStatusPending, now, event.ID); err != nil {
					return rollbackL1Tx(tx, fmt.Errorf("failed to requeue raw archive event %s: %w", event.ID, err))
				}
				if _, err := appendL1EventLog(ctx, tx, "memory.l1_raw_archive_queued", "conv:lifecycle", event.SessionID, event.ThreadID, map[string]interface{}{
					"outbox_id":    existingOutboxID,
					"event_id":     event.ID,
					"event_sha256": eventSHA256,
					"status":       L1RawLifecycleArchiveOutboxStatusPending,
				}, "memory_lifecycle"); err != nil {
					return rollbackL1Tx(tx, err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit raw archive outbox queue: %w", err)
	}
	return conflictErr
}

func (s *L1SQLiteStore) rawLifecycleArchiveCandidates(ctx context.Context, cutoff time.Time, limit int) ([]L1MemoryEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.readDB.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace LIKE 'conv:%'
  AND memory_state = ?
  AND layer = ?
  AND created_at < ?
  AND NOT EXISTS (
	SELECT 1
	FROM l1_profile_promotion_job p
	WHERE p.evidence_event_id = l1_memory_event.id
	  AND p.state <> ?
  )
ORDER BY created_at ASC, id ASC
LIMIT ?`, MemoryStateObserved, MemoryLayerL1, cutoff.UTC(), domainmemory.ProfilePromotionCompleted, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query raw archive candidates: %w", err)
	}
	events, scanErr := scanL1Events(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, fmt.Errorf("failed to scan raw archive candidates: %w", scanErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("failed to close raw archive candidates: %w", closeErr)
	}
	return events, nil
}

func rawLifecycleArchiveCandidateEligible(ctx context.Context, tx *sql.Tx, eventID string, cutoff time.Time) (bool, error) {
	var eligible bool
	err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1
	FROM l1_memory_event e
	WHERE e.id = ?
	  AND e.namespace LIKE 'conv:%'
	  AND e.memory_state = ?
	  AND e.layer = ?
	  AND e.created_at < ?
	  AND NOT EXISTS (
		SELECT 1
		FROM l1_profile_promotion_job p
		WHERE p.evidence_event_id = e.id
		  AND p.state <> ?
	  )
)`, eventID, MemoryStateObserved, MemoryLayerL1, cutoff.UTC(), domainmemory.ProfilePromotionCompleted).Scan(&eligible)
	return eligible, err
}

func (s *L1SQLiteStore) drainRawLifecycleArchiveOutbox(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	entries, err := s.pendingRawLifecycleOutbox(ctx, limit)
	if err != nil {
		return 0, err
	}
	compacted := 0
	for _, entry := range entries {
		claimed, err := s.claimRawLifecycleOutboxAttempt(ctx, entry.OutboxID)
		if err != nil {
			return compacted, err
		}
		if !claimed {
			continue
		}
		event, found, err := readL1MemoryEventByID(ctx, s.db, entry.EventID)
		if err != nil {
			failure := fmt.Errorf("failed to reread raw archive source %s: %w", entry.EventID, err)
			if markErr := s.failRawLifecycleOutbox(ctx, entry.OutboxID, failure); markErr != nil {
				return compacted, errors.Join(failure, markErr)
			}
			return compacted, failure
		}
		if !found {
			failure := fmt.Errorf("%w: raw archive source %s is missing", ErrL1RawLifecycleArchiveConflict, entry.EventID)
			if markErr := s.failRawLifecycleOutbox(ctx, entry.OutboxID, failure); markErr != nil {
				return compacted, errors.Join(failure, markErr)
			}
			return compacted, failure
		}
		if err := validateRawLifecycleSource(ctx, s.db, event, entry, cutoff); err != nil {
			if markErr := s.failRawLifecycleOutbox(ctx, entry.OutboxID, err); markErr != nil {
				return compacted, errors.Join(err, markErr)
			}
			return compacted, err
		}
		if s.rawLifecycleArchiveStore == nil {
			failure := fmt.Errorf("%w: %w: raw lifecycle archive store is not configured", ErrL1RawLifecycleArchiveUnavailable, domainmemory.ErrUserMemoryOwnerUnavailable)
			if markErr := s.failRawLifecycleOutbox(ctx, entry.OutboxID, failure); markErr != nil {
				return compacted, errors.Join(failure, markErr)
			}
			return compacted, failure
		}
		receipt := L1RawLifecycleArchiveReceipt{
			OutboxID:    entry.OutboxID,
			EventID:     entry.EventID,
			EventSHA256: entry.EventSHA256,
			CreatedAt:   time.Now().UTC(),
		}
		if err := s.rawLifecycleArchiveStore.ArchiveL1RawLifecycleEvent(ctx, event, receipt); err != nil {
			if markErr := s.failRawLifecycleOutbox(ctx, entry.OutboxID, err); markErr != nil {
				return compacted, errors.Join(err, markErr)
			}
			return compacted, err
		}
		deleted, err := s.finalizeRawLifecycleArchive(ctx, cutoff, entry)
		if err != nil {
			if markErr := s.failRawLifecycleOutbox(ctx, entry.OutboxID, err); markErr != nil {
				return compacted, errors.Join(err, markErr)
			}
			return compacted, err
		}
		if deleted {
			compacted++
		}
	}
	return compacted, nil
}

func (s *L1SQLiteStore) pendingRawLifecycleOutbox(ctx context.Context, limit int) ([]rawLifecycleOutboxEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT outbox_id, event_id, namespace, event_sha256, status, created_at
FROM conversation_lifecycle_raw_archive_outbox
WHERE status IN (?, ?)
ORDER BY created_at ASC, outbox_id ASC
LIMIT ?`, L1RawLifecycleArchiveOutboxStatusPending, L1RawLifecycleArchiveOutboxStatusFailed, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending raw archive outbox: %w", err)
	}
	defer rows.Close()
	entries := make([]rawLifecycleOutboxEntry, 0, limit)
	for rows.Next() {
		var entry rawLifecycleOutboxEntry
		if err := rows.Scan(&entry.OutboxID, &entry.EventID, &entry.Namespace, &entry.EventSHA256, &entry.Status, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan pending raw archive outbox: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending raw archive outbox rows error: %w", err)
	}
	return entries, nil
}

func (s *L1SQLiteStore) claimRawLifecycleOutboxAttempt(ctx context.Context, outboxID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE conversation_lifecycle_raw_archive_outbox
SET status = ?, attempt_count = attempt_count + 1, last_error = '', updated_at = ?
WHERE outbox_id = ? AND status IN (?, ?)`, L1RawLifecycleArchiveOutboxStatusPending, time.Now().UTC(), outboxID,
		L1RawLifecycleArchiveOutboxStatusPending, L1RawLifecycleArchiveOutboxStatusFailed)
	if err != nil {
		return false, fmt.Errorf("failed to claim raw archive outbox %s: %w", outboxID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to inspect raw archive outbox claim %s: %w", outboxID, err)
	}
	return affected == 1, nil
}

func (s *L1SQLiteStore) failRawLifecycleOutbox(ctx context.Context, outboxID string, cause error) error {
	if cause == nil {
		cause = errors.New("raw lifecycle archive failed")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE conversation_lifecycle_raw_archive_outbox
SET status = ?, last_error = ?, updated_at = ?
WHERE outbox_id = ? AND status <> ?`, L1RawLifecycleArchiveOutboxStatusFailed, cause.Error(), time.Now().UTC(), outboxID, L1RawLifecycleArchiveOutboxStatusArchived)
	if err != nil {
		return fmt.Errorf("failed to mark raw archive outbox %s failed: %w", outboxID, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("failed to inspect raw archive outbox failure %s: %w", outboxID, err)
	} else if affected == 0 {
		return fmt.Errorf("raw archive outbox %s was not pending", outboxID)
	}
	return nil
}

func validateRawLifecycleSource(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, event L1MemoryEvent, entry rawLifecycleOutboxEntry, cutoff time.Time) error {
	if event.ID != entry.EventID || event.Namespace != entry.Namespace {
		return fmt.Errorf("%w: raw archive source identity changed for %s", ErrL1RawLifecycleArchiveConflict, entry.EventID)
	}
	actualHash, err := CanonicalL1MemoryEventSHA256(event)
	if err != nil {
		return fmt.Errorf("%w: raw archive source hash failed for %s: %v", ErrL1RawLifecycleArchiveConflict, entry.EventID, err)
	}
	if actualHash != entry.EventSHA256 {
		return fmt.Errorf("%w: raw archive source hash changed for %s", ErrL1RawLifecycleArchiveConflict, entry.EventID)
	}
	if !strings.HasPrefix(event.Namespace, "conv:") || event.MemoryState != MemoryStateObserved || event.Layer != MemoryLayerL1 || !event.CreatedAt.Before(cutoff.UTC()) {
		return fmt.Errorf("%w: raw archive source is no longer eligible for %s", ErrL1RawLifecycleArchiveConflict, entry.EventID)
	}
	var blocked int
	if err := queryer.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM l1_profile_promotion_job
	WHERE evidence_event_id = ? AND state <> ?
)`, event.ID, domainmemory.ProfilePromotionCompleted).Scan(&blocked); err != nil {
		return fmt.Errorf("failed to recheck raw archive profile promotion for %s: %w", event.ID, err)
	}
	if blocked != 0 {
		return fmt.Errorf("%w: profile promotion for raw archive source %s is not completed", ErrL1RawLifecycleArchiveConflict, event.ID)
	}
	return nil
}

func readL1MemoryEventByID(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, eventID string) (L1MemoryEvent, bool, error) {
	row := queryer.QueryRowContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event WHERE id = ?`, eventID)
	events, err := scanL1EventRows(row)
	if errors.Is(err, sql.ErrNoRows) {
		return L1MemoryEvent{}, false, nil
	}
	if err != nil {
		return L1MemoryEvent{}, false, err
	}
	return events[0], true, nil
}

func (s *L1SQLiteStore) finalizeRawLifecycleArchive(ctx context.Context, cutoff time.Time, entry rawLifecycleOutboxEntry) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to begin raw archive finalize transaction: %w", err)
	}
	var status, boundHash string
	if err := tx.QueryRowContext(ctx, `
SELECT status, event_sha256 FROM conversation_lifecycle_raw_archive_outbox
WHERE outbox_id = ?`, entry.OutboxID).Scan(&status, &boundHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, rollbackL1Tx(tx, fmt.Errorf("%w: raw archive outbox %s is missing", ErrL1RawLifecycleArchiveConflict, entry.OutboxID))
		}
		return false, rollbackL1Tx(tx, fmt.Errorf("failed to reread raw archive outbox %s: %w", entry.OutboxID, err))
	}
	if status == L1RawLifecycleArchiveOutboxStatusArchived {
		return false, rollbackL1Tx(tx, nil)
	}
	if boundHash != entry.EventSHA256 {
		failure := fmt.Errorf("%w: raw archive outbox hash changed for %s", ErrL1RawLifecycleArchiveConflict, entry.EventID)
		if _, err := failRawLifecycleOutboxTx(ctx, tx, entry.OutboxID, failure); err != nil {
			return false, rollbackL1Tx(tx, err)
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, failure
	}
	event, found, err := readL1MemoryEventByID(ctx, tx, entry.EventID)
	if err != nil {
		return false, rollbackL1Tx(tx, fmt.Errorf("failed to reread raw archive source %s: %w", entry.EventID, err))
	}
	if !found {
		failure := fmt.Errorf("%w: raw archive source %s disappeared before finalize", ErrL1RawLifecycleArchiveConflict, entry.EventID)
		if _, err := failRawLifecycleOutboxTx(ctx, tx, entry.OutboxID, failure); err != nil {
			return false, rollbackL1Tx(tx, err)
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, failure
	}
	if err := validateRawLifecycleSource(ctx, tx, event, entry, cutoff); err != nil {
		if _, failErr := failRawLifecycleOutboxTx(ctx, tx, entry.OutboxID, err); failErr != nil {
			return false, rollbackL1Tx(tx, failErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, commitErr
		}
		return false, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM l1_memory_event WHERE id = ?`, entry.EventID)
	if err != nil {
		return false, rollbackL1Tx(tx, fmt.Errorf("failed to delete archived raw source %s: %w", entry.EventID, err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, rollbackL1Tx(tx, fmt.Errorf("failed to inspect archived raw source deletion %s: %w", entry.EventID, err))
	}
	if affected != 1 {
		failure := fmt.Errorf("%w: raw archive source %s deletion affected %d rows", ErrL1RawLifecycleArchiveConflict, entry.EventID, affected)
		if _, err := failRawLifecycleOutboxTx(ctx, tx, entry.OutboxID, failure); err != nil {
			return false, rollbackL1Tx(tx, err)
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, failure
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
UPDATE conversation_lifecycle_raw_archive_outbox
SET status = ?, last_error = '', archived_at = ?, updated_at = ?
WHERE outbox_id = ?`, L1RawLifecycleArchiveOutboxStatusArchived, now, now, entry.OutboxID); err != nil {
		return false, rollbackL1Tx(tx, fmt.Errorf("failed to mark raw archive outbox %s archived: %w", entry.OutboxID, err))
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.l1_raw_archived_compacted", "conv:lifecycle", event.SessionID, event.ThreadID, map[string]interface{}{
		"outbox_id":    entry.OutboxID,
		"event_id":     entry.EventID,
		"event_sha256": entry.EventSHA256,
		"count":        1,
	}, "memory_lifecycle"); err != nil {
		return false, rollbackL1Tx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit archived raw source finalize: %w", err)
	}
	return true, nil
}

func failRawLifecycleOutboxTx(ctx context.Context, tx *sql.Tx, outboxID string, cause error) (int64, error) {
	if cause == nil {
		cause = errors.New("raw lifecycle archive failed")
	}
	result, err := tx.ExecContext(ctx, `
UPDATE conversation_lifecycle_raw_archive_outbox
SET status = ?, last_error = ?, updated_at = ?
WHERE outbox_id = ? AND status <> ?`, L1RawLifecycleArchiveOutboxStatusFailed, cause.Error(), time.Now().UTC(), outboxID, L1RawLifecycleArchiveOutboxStatusArchived)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// applyLifecycleMemoryMeta applies one lifecycle metadata transition and its
// audit record in the same transaction. The original metadata and updated_at
// are part of the optimistic binding, so a concurrent owner transition makes
// this a no-op instead of overwriting newer state.
func (s *L1SQLiteStore) applyLifecycleMemoryMeta(ctx context.Context, ev L1MemoryEvent, expectedWhere string, expectedArgs []interface{}, meta map[string]interface{}, updatedAt time.Time, eventType string, payload map[string]interface{}) (bool, error) {
	expectedMetaJSON, err := marshalL1MetaJSON(ev.Meta, "failed to marshal lifecycle binding metadata")
	if err != nil {
		return false, err
	}
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal lifecycle metadata")
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to begin lifecycle transaction: %w", err)
	}
	query := `
UPDATE l1_memory_event
SET meta_json = ?, updated_at = ?
WHERE id = ? AND meta_json = ? AND updated_at = ?` + expectedWhere
	args := []interface{}{metaJSON, updatedAt.UTC(), ev.ID, expectedMetaJSON, ev.UpdatedAt}
	args = append(args, expectedArgs...)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, rollbackL1Tx(tx, fmt.Errorf("failed to apply lifecycle metadata for %s: %w", ev.ID, err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, rollbackL1Tx(tx, fmt.Errorf("failed to inspect lifecycle metadata for %s: %w", ev.ID, err))
	}
	if affected == 0 {
		return false, rollbackL1Tx(tx, nil)
	}
	if _, err := appendL1EventLog(ctx, tx, eventType, ev.Namespace, ev.SessionID, ev.ThreadID, payload, "memory_lifecycle"); err != nil {
		return false, rollbackL1Tx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit lifecycle metadata for %s: %w", ev.ID, err)
	}
	return true, nil
}

func (s *L1SQLiteStore) queueUserMemoryCandidateReview(ctx context.Context, now time.Time, cutoff time.Time, limit int) (int, error) {
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE namespace LIKE 'user:%'
  AND memory_state = ?
  AND created_at < ?
ORDER BY created_at ASC, rowid ASC
LIMIT ?`, MemoryStateCandidate, cutoff.UTC(), limit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, ev := range events {
		if !lifecycleMemoryActive(ev.Meta) || strings.TrimSpace(metaStringValue(ev.Meta, "review_status")) == "queued" {
			continue
		}
		meta := cloneMeta(ev.Meta)
		meta["review_status"] = "queued"
		meta["review_queued_at"] = now.UTC().Format(time.RFC3339)
		updated, err := s.applyLifecycleMemoryMeta(ctx, ev, `
 AND namespace LIKE 'user:%'
 AND memory_state = ?
 AND created_at < ?`, []interface{}{MemoryStateCandidate, cutoff.UTC()}, meta, now, "memory.candidate_review_queued", map[string]interface{}{
			"memory_id": ev.ID,
		})
		if err != nil {
			return count, err
		}
		if updated {
			count++
		}
	}
	return count, nil
}

func (s *L1SQLiteStore) buildMonthlyHighlights(ctx context.Context, now time.Time, cutoff time.Time, limit int) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT substr(d.digest_date, 1, 7) AS month, d.category,
       group_concat(d.id, char(10)) AS digest_ids,
       group_concat(d.digest_text, char(10) || char(10)) AS digest_text
FROM (
  SELECT id, digest_date, category, digest_text
  FROM l1_daily_digest
  WHERE date(digest_date) <= date(?)
  ORDER BY digest_date ASC, id ASC
) d
WHERE NOT EXISTS (
    SELECT 1 FROM l1_monthly_highlight h
    WHERE h.month = substr(d.digest_date, 1, 7)
      AND h.category = d.category
  )
GROUP BY substr(d.digest_date, 1, 7), d.category
ORDER BY month ASC, category ASC
LIMIT ?`, cutoff.UTC().Format("2006-01-02"), limit)
	if err != nil {
		return 0, fmt.Errorf("failed to query monthly highlight candidates: %w", err)
	}
	type monthlyHighlightCandidate struct {
		month         string
		category      string
		digestIDsText string
		digestText    string
	}
	candidates := make([]monthlyHighlightCandidate, 0)
	for rows.Next() {
		var candidate monthlyHighlightCandidate
		if err := rows.Scan(&candidate.month, &candidate.category, &candidate.digestIDsText, &candidate.digestText); err != nil {
			rows.Close()
			return 0, fmt.Errorf("failed to scan monthly highlight candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("monthly highlight rows close error: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("monthly highlight rows error: %w", err)
	}

	count := 0
	for _, candidate := range candidates {
		month := candidate.month
		category := candidate.category
		digestIDsText := candidate.digestIDsText
		digestText := candidate.digestText
		sourceIDs := nonEmptyLines(digestIDsText)
		highlight := buildMonthlyHighlightText(month, category, digestText)
		sourceJSON, err := json.Marshal(sourceIDs)
		if err != nil {
			return count, fmt.Errorf("failed to marshal monthly highlight sources: %w", err)
		}
		id := fmt.Sprintf("monthly:%s:%s", month, category)
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return count, fmt.Errorf("failed to begin monthly highlight transaction: %w", err)
		}
		var currentDigestIDs, currentDigestText string
		if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(group_concat(id, char(10)), ''),
       COALESCE(group_concat(digest_text, char(10) || char(10)), '')
FROM (
  SELECT id, digest_date, digest_text
  FROM l1_daily_digest
  WHERE date(digest_date) <= date(?)
    AND substr(digest_date, 1, 7) = ?
    AND category = ?
  ORDER BY digest_date ASC, id ASC
)`, cutoff.UTC().Format("2006-01-02"), month, category).Scan(&currentDigestIDs, &currentDigestText); err != nil {
			return count, rollbackL1Tx(tx, fmt.Errorf("failed to recheck monthly highlight source %s/%s: %w", month, category, err))
		}
		if currentDigestIDs != digestIDsText || currentDigestText != digestText {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return count, fmt.Errorf("failed to rollback drifted monthly highlight %s/%s: %w", month, category, err)
			}
			continue
		}
		var existing int
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM l1_monthly_highlight WHERE month = ? AND category = ?
)`, month, category).Scan(&existing); err != nil {
			return count, rollbackL1Tx(tx, fmt.Errorf("failed to recheck monthly highlight %s/%s: %w", month, category, err))
		}
		if existing != 0 {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return count, fmt.Errorf("failed to rollback existing monthly highlight %s/%s: %w", month, category, err)
			}
			continue
		}
		result, err := tx.ExecContext(ctx, `
INSERT INTO l1_monthly_highlight (
	id, month, category, source_ids_json, highlight_text, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(month, category) DO NOTHING`, id, month, category, string(sourceJSON), highlight, now.UTC(), now.UTC())
		if err != nil {
			return count, rollbackL1Tx(tx, fmt.Errorf("failed to save monthly highlight: %w", err))
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return count, rollbackL1Tx(tx, fmt.Errorf("failed to inspect monthly highlight insert: %w", err))
		}
		if affected == 0 {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return count, fmt.Errorf("failed to rollback duplicate monthly highlight %s/%s: %w", month, category, err)
			}
			continue
		}
		if _, err := appendL1EventLog(ctx, tx, "memory.monthly_highlight_built", "conv:lifecycle", "", 0, map[string]interface{}{
			"highlight_id": id,
			"month":        month,
			"category":     category,
			"source_ids":   sourceIDs,
		}, "memory_lifecycle"); err != nil {
			return count, rollbackL1Tx(tx, err)
		}
		if err := tx.Commit(); err != nil {
			return count, fmt.Errorf("failed to commit monthly highlight %s/%s: %w", month, category, err)
		}
		count++
	}
	return count, nil
}

func buildMonthlyHighlightText(month string, category string, digestText string) string {
	lines := nonEmptyLines(digestText)
	if len(lines) > 24 {
		lines = lines[:24]
	}
	var b strings.Builder
	b.WriteString("Monthly highlight ")
	b.WriteString(month)
	if strings.TrimSpace(category) != "" {
		b.WriteString(" / ")
		b.WriteString(category)
	}
	for _, line := range lines {
		b.WriteString("\n- ")
		b.WriteString(strings.TrimPrefix(strings.TrimSpace(line), "- "))
	}
	return b.String()
}

func (s *L1SQLiteStore) queueThreadSummaryMonthlySeeds(ctx context.Context, now time.Time, cutoff time.Time, limit int) (int, error) {
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE layer = ?
  AND (source = ? OR json_extract(meta_json, '$.kind') = ?)
  AND updated_at < ?
ORDER BY updated_at ASC, rowid ASC
LIMIT ?`, "L2", "thread_summary", "thread_summary", cutoff.UTC(), limit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, ev := range events {
		if strings.TrimSpace(metaStringValue(ev.Meta, "monthly_highlight_seed_status")) == "queued" {
			continue
		}
		meta := cloneMeta(ev.Meta)
		meta["monthly_highlight_seed_status"] = "queued"
		meta["monthly_highlight_seed_queued_at"] = now.UTC().Format(time.RFC3339)
		updated, err := s.applyLifecycleMemoryMeta(ctx, ev, `
 AND layer = ?
 AND (source = ? OR json_extract(meta_json, '$.kind') = ?)
 AND updated_at < ?`, []interface{}{"L2", "thread_summary", "thread_summary", cutoff.UTC()}, meta, now, "memory.thread_summary_monthly_seed_queued", map[string]interface{}{
			"memory_id": ev.ID,
		})
		if err != nil {
			return count, err
		}
		if updated {
			count++
		}
	}
	return count, nil
}

func (s *L1SQLiteStore) markDecayedUserMemories(ctx context.Context, now time.Time, cutoff time.Time, limit int) (int, error) {
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE namespace LIKE 'user:%'
  AND memory_state = ?
ORDER BY updated_at ASC, rowid ASC
LIMIT ?`, MemoryStateConfirmed, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, ev := range events {
		if !lifecycleMemoryActive(ev.Meta) || strings.TrimSpace(metaStringValue(ev.Meta, "superseded_by")) != "" {
			continue
		}
		if strings.TrimSpace(metaStringValue(ev.Meta, "lifecycle_status")) == "decayed" {
			continue
		}
		if ev.UpdatedAt.After(lifecycleDecayCutoff(now, cutoff, ev.Meta)) {
			continue
		}
		decayScore := lifecycleDecayScore(now, ev.UpdatedAt)
		meta := cloneMeta(ev.Meta)
		meta["lifecycle_status"] = "decayed"
		meta["decay_policy"] = lifecycleDecayPolicy(ev.Meta)
		meta["decay_score"] = decayScore
		meta["decayed_at"] = now.UTC().Format(time.RFC3339)
		updated, err := s.applyLifecycleMemoryMeta(ctx, ev, `
 AND namespace LIKE 'user:%'
 AND memory_state = ?
 AND updated_at <= ?`, []interface{}{MemoryStateConfirmed, lifecycleDecayCutoff(now, cutoff, ev.Meta).UTC()}, meta, now, "memory.decayed", map[string]interface{}{
			"memory_id":   ev.ID,
			"decay_score": decayScore,
		})
		if err != nil {
			return count, err
		}
		if updated {
			count++
		}
	}
	return count, nil
}

func (s *L1SQLiteStore) queueVectorCleanup(ctx context.Context, now time.Time, limit int) (int, error) {
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE namespace LIKE 'user:%'
ORDER BY updated_at ASC, rowid ASC
LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, ev := range events {
		if lifecycleMemoryActive(ev.Meta) && strings.TrimSpace(metaStringValue(ev.Meta, "superseded_by")) == "" {
			continue
		}
		cleanupStatus := strings.TrimSpace(metaStringValue(ev.Meta, "vector_cleanup_status"))
		if cleanupStatus == "queued" || cleanupStatus == "done" {
			continue
		}
		if strings.TrimSpace(metaStringValue(ev.Meta, "vector_cleanup_completed_at")) != "" {
			continue
		}
		if cleanupStatus == "running" && !vectorCleanupRunningStale(ev.Meta, now) {
			continue
		}
		meta := cloneMeta(ev.Meta)
		meta["vector_cleanup_status"] = "queued"
		meta["vector_cleanup_queued_at"] = now.UTC().Format(time.RFC3339)
		delete(meta, "vector_cleanup_error")
		delete(meta, "vector_cleanup_error_at")
		delete(meta, "vector_cleanup_failed_at")
		delete(meta, "vector_cleanup_claim_id")
		updated, err := s.applyLifecycleMemoryMeta(ctx, ev, `
 AND namespace LIKE 'user:%'
 AND (COALESCE(json_extract(meta_json, '$.active'), 1) = 0 OR COALESCE(json_extract(meta_json, '$.superseded_by'), '') <> '')`, nil, meta, now, "memory.vector_cleanup_queued", map[string]interface{}{
			"memory_id":       ev.ID,
			"previous_status": cleanupStatus,
		})
		if err != nil {
			return count, err
		}
		if updated {
			count++
		}
	}
	return count, nil
}

func (s *L1SQLiteStore) executeQueuedVectorCleanup(ctx context.Context, now time.Time, limit int) (int, error) {
	if s.vectorCleanupSink == nil {
		return 0, nil
	}
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE namespace LIKE 'user:%'
ORDER BY updated_at ASC, rowid ASC
LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type claimedVectorCleanup struct {
		item    L1VectorCleanupItem
		binding L1MemoryEvent
	}
	claimed := make([]claimedVectorCleanup, 0, len(events))
	for _, ev := range events {
		if strings.TrimSpace(metaStringValue(ev.Meta, "vector_cleanup_status")) != "queued" {
			continue
		}
		if strings.TrimSpace(metaStringValue(ev.Meta, "vector_cleanup_completed_at")) != "" {
			continue
		}
		reason := firstNonEmptyString(
			metaStringValue(ev.Meta, "forget_reason"),
			metaStringValue(ev.Meta, "supersede_reason"),
			"memory inactive or superseded",
		)
		item := L1VectorCleanupItem{
			MemoryID:     ev.ID,
			Namespace:    ev.Namespace,
			SupersededBy: metaStringValue(ev.Meta, "superseded_by"),
			Reason:       reason,
		}
		claimID := fmt.Sprintf("%s:%d", ev.ID, l1IDSequence.Add(1))
		meta := cloneMeta(ev.Meta)
		meta["vector_cleanup_status"] = "running"
		meta["vector_cleanup_started_at"] = now.UTC().Format(time.RFC3339Nano)
		meta["vector_cleanup_claim_id"] = claimID
		delete(meta, "vector_cleanup_error")
		delete(meta, "vector_cleanup_error_at")
		updated, err := s.applyLifecycleMemoryMeta(ctx, ev, `
 AND namespace LIKE 'user:%'
 AND (COALESCE(json_extract(meta_json, '$.active'), 1) = 0 OR COALESCE(json_extract(meta_json, '$.superseded_by'), '') <> '')
 AND json_extract(meta_json, '$.vector_cleanup_status') = ?
 AND COALESCE(json_extract(meta_json, '$.vector_cleanup_completed_at'), '') = ''`, []interface{}{"queued"}, meta, now, "memory.vector_cleanup_started", map[string]interface{}{
			"memory_id":  ev.ID,
			"claim_id":   claimID,
			"started_at": now.UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return 0, err
		}
		if updated {
			binding := ev
			binding.Meta = meta
			binding.UpdatedAt = now.UTC()
			claimed = append(claimed, claimedVectorCleanup{item: item, binding: binding})
		}
	}
	if len(claimed) == 0 {
		return 0, nil
	}
	items := make([]L1VectorCleanupItem, 0, len(claimed))
	for _, claim := range claimed {
		items = append(items, claim.item)
	}
	result, err := s.vectorCleanupSink.CleanupMemoryVectors(ctx, items)
	if err != nil {
		var persistErr error
		for _, claim := range claimed {
			meta := cloneMeta(claim.binding.Meta)
			meta["vector_cleanup_status"] = "error"
			meta["vector_cleanup_error"] = err.Error()
			meta["vector_cleanup_error_at"] = now.UTC().Format(time.RFC3339Nano)
			meta["vector_cleanup_failed_at"] = now.UTC().Format(time.RFC3339Nano)
			delete(meta, "vector_cleanup_claim_id")
			updated, markErr := s.applyLifecycleMemoryMeta(ctx, claim.binding, `
 AND namespace = ?
 AND json_extract(meta_json, '$.vector_cleanup_status') = ?
 AND json_extract(meta_json, '$.vector_cleanup_claim_id') = ?`, []interface{}{claim.binding.Namespace, "running", metaStringValue(claim.binding.Meta, "vector_cleanup_claim_id")}, meta, now, "memory.vector_cleanup_failed", map[string]interface{}{
				"memory_id": claim.item.MemoryID,
				"claim_id":  metaStringValue(claim.binding.Meta, "vector_cleanup_claim_id"),
				"error":     err.Error(),
			})
			if markErr != nil {
				persistErr = errors.Join(persistErr, markErr)
			} else if !updated {
				persistErr = errors.Join(persistErr, fmt.Errorf("vector cleanup claim %s drifted before failure audit", claim.item.MemoryID))
			}
		}
		return 0, errors.Join(fmt.Errorf("failed to execute vector cleanup: %w", err), persistErr)
	}
	deleted := 0
	if result != nil {
		deleted = result.Deleted
	}
	completed := 0
	for _, claim := range claimed {
		meta := cloneMeta(claim.binding.Meta)
		meta["vector_cleanup_status"] = "done"
		meta["vector_cleanup_completed_at"] = now.UTC().Format(time.RFC3339Nano)
		meta["vector_cleanup_deleted"] = deleted
		delete(meta, "vector_cleanup_error")
		delete(meta, "vector_cleanup_claim_id")
		updated, err := s.applyLifecycleMemoryMeta(ctx, claim.binding, `
 AND namespace = ?
 AND json_extract(meta_json, '$.vector_cleanup_status') = ?
 AND json_extract(meta_json, '$.vector_cleanup_claim_id') = ?`, []interface{}{claim.binding.Namespace, "running", metaStringValue(claim.binding.Meta, "vector_cleanup_claim_id")}, meta, now, "memory.vector_cleanup_completed", map[string]interface{}{
			"memory_id": claim.item.MemoryID,
			"deleted":   deleted,
		})
		if err != nil {
			return 0, err
		}
		if updated {
			completed++
		}
	}
	return completed, nil
}

func (s *L1SQLiteStore) userMemoryEventsForLifecycle(ctx context.Context, where string, args ...interface{}) ([]L1MemoryEvent, error) {
	query := `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
` + where
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query lifecycle user memories: %w", err)
	}
	defer rows.Close()
	return scanL1Events(rows)
}

func lifecycleMemoryActive(meta map[string]interface{}) bool {
	if meta == nil {
		return true
	}
	raw, ok := meta["active"]
	if !ok {
		return true
	}
	active, ok := raw.(bool)
	if !ok {
		return true
	}
	return active
}

func vectorCleanupRunningStale(meta map[string]interface{}, now time.Time) bool {
	// RunMemoryLifecycleMaintenance is process-serialized, so a running row
	// observed by a later run cannot belong to an in-flight run on this store.
	// The started_at marker therefore only needs to distinguish a future lease
	// from a completed-run/crash marker; malformed or missing markers are stale.
	startedAt := strings.TrimSpace(metaStringValue(meta, "vector_cleanup_started_at"))
	if startedAt == "" {
		return true
	}
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return true
	}
	return !started.After(now.UTC())
}

func lifecycleDecayCutoff(now time.Time, defaultCutoff time.Time, meta map[string]interface{}) time.Time {
	policy := lifecycleDecayPolicy(meta)
	switch policy {
	case "short":
		return now.Add(-30 * 24 * time.Hour)
	case "project", "constraint", "long":
		return now.Add(-180 * 24 * time.Hour)
	case "pinned":
		return time.Time{}
	default:
		return defaultCutoff
	}
}

func lifecycleDecayPolicy(meta map[string]interface{}) string {
	ttl := strings.ToLower(strings.TrimSpace(metaStringValue(meta, "ttl_policy")))
	if ttl != "" {
		return ttl
	}
	switch strings.ToLower(strings.TrimSpace(metaStringValue(meta, "type"))) {
	case "episode":
		return "short"
	case "project", "constraint":
		return strings.ToLower(strings.TrimSpace(metaStringValue(meta, "type")))
	default:
		return "normal"
	}
}

func lifecycleDecayScore(now time.Time, updatedAt time.Time) float64 {
	if updatedAt.IsZero() || now.Before(updatedAt) {
		return 0
	}
	days := now.Sub(updatedAt).Hours() / 24
	switch {
	case days >= 365:
		return 0.2
	case days >= 180:
		return 0.35
	case days >= 90:
		return 0.5
	default:
		return 0.65
	}
}

func nonEmptyLines(text string) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func cloneMeta(meta map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range meta {
		out[k] = v
	}
	return out
}
