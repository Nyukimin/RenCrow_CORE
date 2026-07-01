package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
	result, err := s.db.ExecContext(ctx, `
DELETE FROM l1_memory_event
WHERE id IN (
	SELECT id FROM l1_memory_event
	WHERE namespace LIKE 'conv:%'
	  AND memory_state = ?
	  AND created_at < ?
	ORDER BY created_at ASC
	LIMIT ?
)`, MemoryStateObserved, cutoff.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("failed to compact old conversation raw memory: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected > 0 {
		if _, err := s.AppendEvent(ctx, "memory.l1_raw_compacted", "conv:lifecycle", "", 0, map[string]interface{}{
			"cutoff": cutoff.UTC().Format(time.RFC3339),
			"count":  affected,
		}, "memory_lifecycle"); err != nil {
			return 0, err
		}
	}
	return int(affected), nil
}

func (s *L1SQLiteStore) queueUserMemoryCandidateReview(ctx context.Context, now time.Time, cutoff time.Time, limit int) (int, error) {
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE namespace LIKE 'user:%'
  AND memory_state = ?
  AND created_at < ?
ORDER BY created_at ASC
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
		if err := s.updateMemoryMeta(ctx, ev.ID, meta); err != nil {
			return count, err
		}
		if _, err := s.AppendEvent(ctx, "memory.candidate_review_queued", ev.Namespace, ev.SessionID, ev.ThreadID, map[string]interface{}{
			"memory_id": ev.ID,
		}, "memory_lifecycle"); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *L1SQLiteStore) buildMonthlyHighlights(ctx context.Context, now time.Time, cutoff time.Time, limit int) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT substr(digest_date, 1, 7) AS month, category,
       group_concat(id, char(10)) AS digest_ids,
       group_concat(digest_text, char(10) || char(10)) AS digest_text
FROM l1_daily_digest d
WHERE date(d.digest_date) <= date(?)
  AND NOT EXISTS (
    SELECT 1 FROM l1_monthly_highlight h
    WHERE h.month = substr(d.digest_date, 1, 7)
      AND h.category = d.category
  )
GROUP BY substr(digest_date, 1, 7), category
ORDER BY month ASC, category ASC
LIMIT ?`, cutoff.UTC().Format("2006-01-02"), limit)
	if err != nil {
		return 0, fmt.Errorf("failed to query monthly highlight candidates: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var month, category, digestIDsText, digestText string
		if err := rows.Scan(&month, &category, &digestIDsText, &digestText); err != nil {
			return count, fmt.Errorf("failed to scan monthly highlight candidate: %w", err)
		}
		sourceIDs := nonEmptyLines(digestIDsText)
		highlight := buildMonthlyHighlightText(month, category, digestText)
		sourceJSON, err := json.Marshal(sourceIDs)
		if err != nil {
			return count, fmt.Errorf("failed to marshal monthly highlight sources: %w", err)
		}
		id := fmt.Sprintf("monthly:%s:%s", month, category)
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO l1_monthly_highlight (
	id, month, category, source_ids_json, highlight_text, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, month, category, string(sourceJSON), highlight, now.UTC(), now.UTC()); err != nil {
			return count, fmt.Errorf("failed to save monthly highlight: %w", err)
		}
		if _, err := s.AppendEvent(ctx, "memory.monthly_highlight_built", "conv:lifecycle", "", 0, map[string]interface{}{
			"highlight_id": id,
			"month":        month,
			"category":     category,
			"source_ids":   sourceIDs,
		}, "memory_lifecycle"); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("monthly highlight rows error: %w", err)
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
ORDER BY updated_at ASC
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
		if err := s.updateMemoryMeta(ctx, ev.ID, meta); err != nil {
			return count, err
		}
		if _, err := s.AppendEvent(ctx, "memory.thread_summary_monthly_seed_queued", ev.Namespace, ev.SessionID, ev.ThreadID, map[string]interface{}{
			"memory_id": ev.ID,
		}, "memory_lifecycle"); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *L1SQLiteStore) markDecayedUserMemories(ctx context.Context, now time.Time, cutoff time.Time, limit int) (int, error) {
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE namespace LIKE 'user:%'
  AND memory_state = ?
ORDER BY updated_at ASC
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
		meta := cloneMeta(ev.Meta)
		meta["lifecycle_status"] = "decayed"
		meta["decay_policy"] = lifecycleDecayPolicy(ev.Meta)
		meta["decay_score"] = lifecycleDecayScore(now, ev.UpdatedAt)
		meta["decayed_at"] = now.UTC().Format(time.RFC3339)
		if err := s.updateMemoryMeta(ctx, ev.ID, meta); err != nil {
			return count, err
		}
		if _, err := s.AppendEvent(ctx, "memory.decayed", ev.Namespace, ev.SessionID, ev.ThreadID, map[string]interface{}{
			"memory_id":   ev.ID,
			"decay_score": 0.5,
		}, "memory_lifecycle"); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *L1SQLiteStore) queueVectorCleanup(ctx context.Context, now time.Time, limit int) (int, error) {
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE namespace LIKE 'user:%'
ORDER BY updated_at ASC
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
		meta := cloneMeta(ev.Meta)
		meta["vector_cleanup_status"] = "queued"
		meta["vector_cleanup_queued_at"] = now.UTC().Format(time.RFC3339)
		if err := s.updateMemoryMeta(ctx, ev.ID, meta); err != nil {
			return count, err
		}
		if _, err := s.AppendEvent(ctx, "memory.vector_cleanup_queued", ev.Namespace, ev.SessionID, ev.ThreadID, map[string]interface{}{
			"memory_id": ev.ID,
		}, "memory_lifecycle"); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *L1SQLiteStore) executeQueuedVectorCleanup(ctx context.Context, now time.Time, limit int) (int, error) {
	if s.vectorCleanupSink == nil {
		return 0, nil
	}
	events, err := s.userMemoryEventsForLifecycle(ctx, `
WHERE namespace LIKE 'user:%'
ORDER BY updated_at ASC
LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	items := make([]L1VectorCleanupItem, 0, len(events))
	eventsByID := map[string]L1MemoryEvent{}
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
		items = append(items, L1VectorCleanupItem{
			MemoryID:     ev.ID,
			Namespace:    ev.Namespace,
			SupersededBy: metaStringValue(ev.Meta, "superseded_by"),
			Reason:       reason,
		})
		eventsByID[ev.ID] = ev
	}
	if len(items) == 0 {
		return 0, nil
	}
	result, err := s.vectorCleanupSink.CleanupMemoryVectors(ctx, items)
	if err != nil {
		for _, item := range items {
			ev := eventsByID[item.MemoryID]
			meta := cloneMeta(ev.Meta)
			meta["vector_cleanup_status"] = "error"
			meta["vector_cleanup_error"] = err.Error()
			meta["vector_cleanup_error_at"] = now.UTC().Format(time.RFC3339)
			_ = s.updateMemoryMeta(ctx, ev.ID, meta)
		}
		return 0, fmt.Errorf("failed to execute vector cleanup: %w", err)
	}
	deleted := 0
	if result != nil {
		deleted = result.Deleted
	}
	for _, item := range items {
		ev := eventsByID[item.MemoryID]
		meta := cloneMeta(ev.Meta)
		meta["vector_cleanup_status"] = "done"
		meta["vector_cleanup_completed_at"] = now.UTC().Format(time.RFC3339)
		meta["vector_cleanup_deleted"] = deleted
		delete(meta, "vector_cleanup_error")
		if err := s.updateMemoryMeta(ctx, ev.ID, meta); err != nil {
			return 0, err
		}
		if _, err := s.AppendEvent(ctx, "memory.vector_cleanup_completed", ev.Namespace, ev.SessionID, ev.ThreadID, map[string]interface{}{
			"memory_id": ev.ID,
			"deleted":   deleted,
		}, "memory_lifecycle"); err != nil {
			return 0, err
		}
	}
	return len(items), nil
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
