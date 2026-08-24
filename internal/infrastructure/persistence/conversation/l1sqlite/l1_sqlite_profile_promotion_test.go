package l1sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func newProfilePromotionTestStore(t *testing.T) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestProfilePromotionPersistsUserRawJobsAndCompletesAtomically(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveMessage(ctx, "ren", 10, "conv:10", domconv.NewMessage(domconv.SpeakerUser, "私はGoが好き", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, "ren", 10, "conv:10", domconv.NewMessage(domconv.SpeakerMio, "覚えました", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}

	batch, err := store.ClaimProfilePromotionBatch(ctx, 24, 5, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Messages) != 1 || batch.Messages[0].Text != "私はGoが好き" {
		t.Fatalf("batch=%#v", batch)
	}
	saved, err := store.CompleteProfilePromotionBatch(ctx, *batch, []domainmemory.ProfileCandidate{{
		Type: domainmemory.UserMemoryTypePreference, Statement: "Goが好き", Confidence: 0.8,
		Sensitivity: "normal", Scope: "all_personas",
	}}, "ren", now)
	if err != nil {
		t.Fatal(err)
	}
	if saved != 1 {
		t.Fatalf("saved=%d", saved)
	}
	memories, err := store.ListUserMemories(ctx, "ren", MemoryStateCandidate, true, 10)
	if err != nil || len(memories) != 1 {
		t.Fatalf("memories=%#v err=%v", memories, err)
	}
	if len(memories[0].EvidenceEventIDs) != 1 || memories[0].EvidenceEventIDs[0] != batch.Messages[0].EventID {
		t.Fatalf("evidence=%v", memories[0].EvidenceEventIDs)
	}
	jobs, err := store.ListProfilePromotionJobs(ctx, 10)
	if err != nil || len(jobs) != 1 || jobs[0].State != domainmemory.ProfilePromotionCompleted {
		t.Fatalf("jobs=%#v err=%v", jobs, err)
	}

	// Deterministic candidate identity prevents duplicate candidates on replay.
	if err := store.DeferProfilePromotionBatch(ctx, *batch, now); err == nil {
		t.Fatal("completed lease must not be deferred")
	}
}

func TestClaimProfilePromotionDoesNotRepairMissingJobsByScanningRaw(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.SaveMessage(ctx, "missing-job-session", 1, "conv:missing-job", domconv.NewMessage(domconv.SpeakerUser, "missing job", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM l1_profile_promotion_job WHERE session_id = ?`, "missing-job-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_claim_time_job_repair
BEFORE INSERT ON l1_profile_promotion_job
BEGIN
	SELECT RAISE(ABORT, 'claim must not repair missing jobs');
END`); err != nil {
		t.Fatal(err)
	}

	batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("claim attempted implicit raw repair: %v", err)
	}
	if batch != nil {
		t.Fatalf("claim returned batch for raw without canonical job: %+v", batch)
	}
}

func TestCompleteProfilePromotionRejectsInvalidCandidate(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveMessage(ctx, "ren", 14, "conv:14", domconv.NewMessage(domconv.SpeakerUser, "不正な候補", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Minute, now)
	if err != nil || batch == nil {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	_, err = store.CompleteProfilePromotionBatch(ctx, *batch, []domainmemory.ProfileCandidate{{
		Type: domainmemory.UserMemoryTypeProfile, Statement: "不正な候補", Confidence: 0,
	}}, "ren", now)
	if err == nil {
		t.Fatal("expected invalid candidate confidence to fail")
	}
}

func TestCompleteProfilePromotionUsesTypeInDeterministicCandidateID(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveMessage(ctx, "ren", 16, "conv:16", domconv.NewMessage(domconv.SpeakerUser, "同じ文の候補", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Minute, now)
	if err != nil || batch == nil {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	candidates := []domainmemory.ProfileCandidate{
		{Type: domainmemory.UserMemoryTypePreference, Statement: "同じ文", Confidence: 0.8, Sensitivity: "normal", Scope: "all_personas"},
		{Type: domainmemory.UserMemoryTypeProfile, Statement: "同じ文", Confidence: 0.8, Sensitivity: "normal", Scope: "all_personas"},
	}
	saved, err := store.CompleteProfilePromotionBatch(ctx, *batch, candidates, "ren", now)
	if err != nil {
		t.Fatalf("same statement with distinct allowed types must save atomically: %v", err)
	}
	if saved != len(candidates) {
		t.Fatalf("saved=%d want=%d", saved, len(candidates))
	}
	memories, err := store.ListUserMemories(ctx, "ren", MemoryStateCandidate, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != len(candidates) {
		t.Fatalf("memories=%#v want=%d", memories, len(candidates))
	}
	if memories[0].ID == memories[1].ID || memories[0].Type == memories[1].Type {
		t.Fatalf("candidate identities/types are not distinct: %#v", memories)
	}
	jobs, err := store.ListProfilePromotionJobs(ctx, 10)
	if err != nil || len(jobs) != 1 || jobs[0].State != domainmemory.ProfilePromotionCompleted {
		t.Fatalf("jobs=%#v err=%v", jobs, err)
	}
}

func TestCompleteProfilePromotionRejectsUnboundEvidenceBatch(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveMessage(ctx, "ren", 15, "conv:15", domconv.NewMessage(domconv.SpeakerUser, "根拠", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Minute, now)
	if err != nil || batch == nil {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	batch.Messages[0].SessionID = "other-session"
	if _, err := store.CompleteProfilePromotionBatch(ctx, *batch, nil, "ren", now); err == nil {
		t.Fatal("expected evidence binding mismatch to fail")
	}
}

func TestListProfilePromotionProjectionFiltersBoundsAndOrdersStable(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	insert := func(id, owner, state, statement string, active bool, updatedAt time.Time) {
		t.Helper()
		meta, err := json.Marshal(map[string]interface{}{
			"type": domainmemory.UserMemoryTypeProfile, "user_id": owner, "statement": statement,
			"evidence_event_ids": []string{"evidence-" + id}, "confidence": 0.7,
			"sensitivity": "normal", "scope": "all_personas", "active": active,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, '', 0, ?, ?, ?, ?, ?, 'test', ?, ?)`,
			id, "user:"+owner, string(domconv.SpeakerMemory), statement, string(meta), state, MemoryLayerL1, updatedAt, updatedAt); err != nil {
			t.Fatal(err)
		}
	}
	insert("candidate-old", "ren", MemoryStateCandidate, "candidate old", true, now.Add(-3*time.Hour))
	insert("candidate-new", "ren", MemoryStateCandidate, "candidate new", true, now.Add(-time.Hour))
	insert("confirmed", "ren", MemoryStateConfirmed, "confirmed", true, now.Add(-4*time.Hour))
	insert("pinned", "ren", MemoryStatePinned, "pinned", true, now.Add(-24*time.Hour))
	insert("inactive", "ren", MemoryStateConfirmed, "inactive", false, now)
	insert("other-owner", "other", MemoryStatePinned, "other", true, now)

	items, err := store.ListProfilePromotionProjection(ctx, "ren", 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("projection=%#v", items)
	}
	want := []string{"pinned", "confirmed", "candidate new", "candidate old"}
	for i, item := range items {
		if item.Statement != want[i] {
			t.Fatalf("projection[%d]=%q want %q", i, item.Statement, want[i])
		}
	}
}

func TestListProfilePromotionProjectionFailsClosedOnCorruptSelectedMetadata(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	meta, err := json.Marshal(map[string]interface{}{
		"type": domainmemory.UserMemoryTypeProfile, "user_id": "ren", "statement": "corrupt",
		"evidence_event_ids": []string{"e-corrupt"}, "confidence": "not-a-number",
		"sensitivity": "normal", "scope": "all_personas", "active": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES ('corrupt-projection', 'user:ren', '', 0, ?, 'corrupt', ?, ?, ?, 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		string(domconv.SpeakerMemory), string(meta), MemoryStateCandidate, MemoryLayerL1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProfilePromotionProjection(ctx, "ren", 32); err == nil {
		t.Fatal("expected corrupt selected projection row to fail closed")
	}
}

func TestListProfilePromotionProjectionRejectsUnknownScopeEnum(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	meta, err := json.Marshal(map[string]interface{}{
		"type": domainmemory.UserMemoryTypeProfile, "user_id": "ren", "statement": "scope",
		"evidence_event_ids": []string{"e-scope"}, "confidence": 0.7,
		"sensitivity": "normal", "scope": "untrusted", "active": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES ('unknown-scope', 'user:ren', '', 0, ?, 'scope', ?, ?, ?, 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		string(domconv.SpeakerMemory), string(meta), MemoryStateCandidate, MemoryLayerL1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProfilePromotionProjection(ctx, "ren", 32); err == nil {
		t.Fatal("expected unknown scope enum to fail closed")
	}
}

func TestListProfilePromotionProjectionUsesTotalRunePrefixWithoutFailingValidRows(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	statement := strings.Repeat("x", 512)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("bounded-%02d", i)
		meta, err := json.Marshal(map[string]interface{}{
			"type": domainmemory.UserMemoryTypeProfile, "user_id": "ren", "statement": statement,
			"evidence_event_ids": []string{"e-" + id}, "confidence": 0.7,
			"sensitivity": "normal", "scope": "all_personas", "active": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, 'user:ren', '', 0, ?, ?, ?, ?, ?, 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			id, string(domconv.SpeakerMemory), statement, string(meta), MemoryStateCandidate, MemoryLayerL1); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListProfilePromotionProjection(ctx, "ren", 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 16 {
		t.Fatalf("bounded projection count=%d want=16", len(items))
	}
}

func TestProfilePromotionCancelDoesNotConsumeAttemptAndFailureIsFinite(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveMessage(ctx, "ren", 11, "conv:11", domconv.NewMessage(domconv.SpeakerUser, "毎朝コーヒーを飲む", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 2, time.Minute, now)
	if err != nil || batch == nil {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	if err := store.DeferProfilePromotionBatch(ctx, *batch, now); err != nil {
		t.Fatal(err)
	}
	jobs, _ := store.ListProfilePromotionJobs(ctx, 10)
	if jobs[0].State != domainmemory.ProfilePromotionPending || jobs[0].AttemptCount != 0 {
		t.Fatalf("after cancel=%#v", jobs[0])
	}

	for attempt := 1; attempt <= 2; attempt++ {
		batch, err = store.ClaimProfilePromotionBatch(ctx, 1, 2, time.Minute, now.Add(time.Duration(attempt)*time.Hour))
		if err != nil || batch == nil {
			t.Fatalf("attempt=%d batch=%#v err=%v", attempt, batch, err)
		}
		if err := store.FailProfilePromotionBatch(ctx, *batch, 2, now.Add(time.Duration(attempt)*time.Hour), "extract failed"); err != nil {
			t.Fatal(err)
		}
	}
	jobs, _ = store.ListProfilePromotionJobs(ctx, 10)
	if jobs[0].State != domainmemory.ProfilePromotionFailed || jobs[0].AttemptCount != 2 {
		t.Fatalf("after failures=%#v", jobs[0])
	}
}

func TestMemoryLifecycleKeepsRawWhileProfilePromotionIsPending(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-60 * 24 * time.Hour)
	if err := store.SaveMessage(ctx, "ren", 12, "conv:12", domconv.NewMessage(domconv.SpeakerUser, "未処理の根拠", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET created_at = ?, updated_at = ? WHERE namespace = 'conv:12'`, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now: now, RawConversationRetention: 30 * 24 * time.Hour, RawCompactLimit: 10,
		CandidateReviewAfter: 0, MonthlyHighlightAfter: 0, ThreadSummarySeedAfter: 0,
		DecayAfter: 0, VectorCleanupLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RawCompacted != 0 {
		t.Fatalf("pending evidence was compacted: %+v", result)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = ?`, "conv:12"); got != 1 {
		t.Fatalf("raw rows=%d", got)
	}
}

func TestRetryFailedProfilePromotionJobsRequeuesOnlyEvidenceBackedRowsIdempotently(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	if err := store.SaveMessage(ctx, "ren", 13, "conv:13", domconv.NewMessage(domconv.SpeakerUser, "根拠は保持する", map[string]any{"keep": true}), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	var evidenceID string
	var beforeMessage, beforeMeta, beforeState string
	if err := store.db.QueryRowContext(ctx, `
SELECT id, message, meta_json, memory_state FROM l1_memory_event WHERE namespace = ?`, "conv:13").Scan(&evidenceID, &beforeMessage, &beforeMeta, &beforeState); err != nil {
		t.Fatal(err)
	}
	batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 1, time.Minute, now)
	if err != nil || batch == nil {
		t.Fatalf("claim batch=%#v err=%v", batch, err)
	}
	if err := store.FailProfilePromotionBatch(ctx, *batch, 1, now, "numeric preference parse failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_profile_promotion_job (
	evidence_event_id, session_id, thread_id, state, attempt_count, lease_token, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, '', ?, ?, ?)`,
		"orphan-evidence", "ren", 13, domainmemory.ProfilePromotionFailed, 5, "orphan must remain", now, now); err != nil {
		t.Fatal(err)
	}

	got, err := store.RetryFailedProfilePromotionJobs(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.RequeuedCount != 1 || got.MissingEvidenceCount != 1 {
		t.Fatalf("retry result=%+v", got)
	}
	var state string
	var attempts int
	var lastError string
	if err := store.db.QueryRowContext(ctx, `
SELECT state, attempt_count, last_error FROM l1_profile_promotion_job WHERE evidence_event_id = ?`, evidenceID).
		Scan(&state, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if state != domainmemory.ProfilePromotionPending || attempts != 0 || lastError != "numeric preference parse failed" {
		t.Fatalf("requeued job state=%q attempts=%d error=%q", state, attempts, lastError)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM l1_profile_promotion_job WHERE evidence_event_id = ?`, "orphan-evidence").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != domainmemory.ProfilePromotionFailed {
		t.Fatalf("orphan state=%q", state)
	}
	var afterMessage, afterMeta, afterState string
	if err := store.db.QueryRowContext(ctx, `
SELECT message, meta_json, memory_state FROM l1_memory_event WHERE id = ?`, evidenceID).
		Scan(&afterMessage, &afterMeta, &afterState); err != nil {
		t.Fatal(err)
	}
	if afterMessage != beforeMessage || afterMeta != beforeMeta || afterState != beforeState {
		t.Fatalf("evidence changed: before=(%q,%q,%q) after=(%q,%q,%q)", beforeMessage, beforeMeta, beforeState, afterMessage, afterMeta, afterState)
	}
	var auditCount int
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM l1_event_log WHERE event_type = ?`, "memory.profile_promotion_retry_requested").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count=%d want=1", auditCount)
	}

	got, err = store.RetryFailedProfilePromotionJobs(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.RequeuedCount != 0 || got.MissingEvidenceCount != 1 {
		t.Fatalf("second retry result=%+v", got)
	}
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM l1_event_log WHERE event_type = ?`, "memory.profile_promotion_retry_requested").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("second retry changed audit count=%d", auditCount)
	}

	batch, err = store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Hour, now.Add(3*time.Minute))
	if err != nil || batch == nil || len(batch.Messages) != 1 || batch.Messages[0].EventID != evidenceID {
		t.Fatalf("requeued row was not claimable: batch=%#v err=%v", batch, err)
	}
}

func TestProfilePromotionDiagnosticsCountsAllRowsAndReportsPoolStats(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	states := []string{
		domainmemory.ProfilePromotionPending,
		domainmemory.ProfilePromotionRunning,
		domainmemory.ProfilePromotionRetryWait,
		domainmemory.ProfilePromotionCompleted,
		domainmemory.ProfilePromotionFailed,
	}
	for i, state := range states {
		namespace := "conv:diagnostics-" + string(rune('a'+i))
		if err := store.SaveMessage(ctx, "ren", int64(20+i), namespace, domconv.NewMessage(domconv.SpeakerUser, namespace, nil), MemoryStateObserved); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `
UPDATE l1_profile_promotion_job SET state = ?, attempt_count = ?, last_error = ?
WHERE evidence_event_id = (SELECT id FROM l1_memory_event WHERE namespace = ?)`,
			state, i, "diagnostic error", namespace); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_profile_promotion_job (
	evidence_event_id, session_id, thread_id, state, attempt_count, lease_token, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, '', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		"diagnostics-orphan", "ren", 99, domainmemory.ProfilePromotionFailed, 5, "orphan"); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.ListProfilePromotionJobs(ctx, 2)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("limited jobs=%d err=%v", len(jobs), err)
	}
	report, err := store.ProfilePromotionDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.StateCounts[domainmemory.ProfilePromotionPending] != 1 ||
		report.StateCounts[domainmemory.ProfilePromotionRunning] != 1 ||
		report.StateCounts[domainmemory.ProfilePromotionRetryWait] != 1 ||
		report.StateCounts[domainmemory.ProfilePromotionCompleted] != 1 ||
		report.StateCounts[domainmemory.ProfilePromotionFailed] != 2 {
		t.Fatalf("state counts=%v", report.StateCounts)
	}
	if report.FailedCount != 2 || report.RetryableFailedCount != 1 || report.MissingEvidenceFailedCount != 1 {
		t.Fatalf("failed diagnostics=%+v", report)
	}
	if report.DBPoolStats.Max <= 0 || report.DBPoolStats.Open < 0 || report.DBPoolStats.InUse < 0 || report.DBPoolStats.Idle < 0 || report.DBPoolStats.PoolWaitCount < 0 || report.DBPoolStats.PoolWaitDurationMS < 0 {
		t.Fatalf("invalid pool stats=%+v", report.DBPoolStats)
	}
}

func TestClaimProfilePromotionPrefersLiveConversationOverOlderChatGPTBackfill(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	chatGPTTime := now.Add(-24 * time.Hour)
	if _, err := store.ImportChatGPTL3Records(ctx, []ChatGPTL3ImportRecord{{
		Format:           ChatGPTL3ArtifactFormat,
		ExportID:         "priority-export",
		EvidenceID:       "chatgpt_export:old-conversation:user-1",
		ConversationID:   "old-conversation",
		MessageID:        "user-1",
		MessageCreatedAt: chatGPTTime,
		Role:             "user",
		Text:             "古いインポート根拠",
		ContentType:      "text",
		Content:          json.RawMessage(`{"parts":["古いインポート根拠"]}`),
		OnCurrentBranch:  true,
	}}, true); err != nil {
		t.Fatal(err)
	}
	liveMessage := domconv.NewMessage(domconv.SpeakerUser, "新しい通常会話", nil)
	liveMessage.Timestamp = now
	if err := store.SaveMessage(ctx, "live-session", 42, "conv:live-priority", liveMessage, MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	var liveEvidenceID string
	if err := store.db.QueryRowContext(ctx, `
SELECT id FROM l1_memory_event WHERE namespace = ? AND source = ?`, "conv:live-priority", "conversation").Scan(&liveEvidenceID); err != nil {
		t.Fatal(err)
	}

	batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Minute, now)
	if err != nil || batch == nil {
		t.Fatalf("first batch=%#v err=%v", batch, err)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].EventID != liveEvidenceID || batch.SessionID != "live-session" || batch.ThreadID != 42 {
		t.Fatalf("first batch=%#v want live conversation group", batch)
	}
	if _, err := store.CompleteProfilePromotionBatch(ctx, *batch, nil, "ren", now); err != nil {
		t.Fatal(err)
	}

	backfill, err := store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Minute, now.Add(time.Minute))
	if err != nil || backfill == nil {
		t.Fatalf("backfill batch=%#v err=%v", backfill, err)
	}
	if len(backfill.Messages) != 1 || backfill.Messages[0].EventID != "chatgpt_export:old-conversation:user-1" || backfill.Messages[0].SessionID != backfill.SessionID || backfill.Messages[0].ThreadID != backfill.ThreadID {
		t.Fatalf("backfill batch=%#v want ChatGPT group", backfill)
	}
}
