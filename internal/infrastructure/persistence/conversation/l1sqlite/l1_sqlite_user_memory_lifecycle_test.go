package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestOwnerPlanUserMemoryLifecycleExpiryUsesEvaluationTime(t *testing.T) {
	store := newLifecycleREDStore(t)
	defer store.Close()

	const requestID = "lifecycle-red-plan"
	const ownerID = "owner-red"
	scope := domaintool.ToolExecutionScope{
		RequestID:            requestID,
		ActorKind:            domaintool.ActorKindUser,
		ActorID:              ownerID,
		AuthenticatedUserID:  ownerID,
		AllowedDataScopes:    []string{domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceHTTP,
	}
	ctx := domaintool.WithToolExecutionScope(context.Background(), scope)
	evaluationAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	result, err := store.OwnerPlanUserMemoryLifecycleAt(ctx, requestID, ownerID, ownerID, evaluationAt)
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	want := evaluationAt.Add(userMemoryLifecyclePlanTTL)
	if !result.ExpiresAt.Equal(want) {
		t.Fatalf("expiry=%s, want evaluation_at+TTL=%s", result.ExpiresAt, want)
	}
}

type lifecycleMemorySnapshot struct {
	ID          string
	Namespace   string
	Message     string
	MetaJSON    string
	MemoryState string
	Layer       string
	Source      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func ownerLifecycleContext(requestID, ownerID string) context.Context {
	scope := domaintool.ToolExecutionScope{
		RequestID:            requestID,
		ActorKind:            domaintool.ActorKindUser,
		ActorID:              ownerID,
		AuthenticatedUserID:  ownerID,
		AllowedDataScopes:    []string{domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceHTTP,
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func insertOwnerLifecycleMemory(t *testing.T, store *L1SQLiteStore, id, owner, state string, active bool, supersededBy string, createdAt, updatedAt time.Time, overrides map[string]interface{}) {
	t.Helper()
	meta := map[string]interface{}{
		"type":          "preference",
		"user_id":       owner,
		"statement":     "statement-" + id,
		"active":        active,
		"scope":         "all_personas",
		"sensitivity":   "normal",
		"confidence":    0.8,
		"superseded_by": supersededBy,
	}
	for key, value := range overrides {
		meta[key] = value
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal memory %s meta: %v", id, err)
	}
	_, err = store.db.ExecContext(context.Background(), `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, '', '', 0, '', ?, ?, ?, ?, ?, ?, ?, ?)
`, id, "user:"+owner, string(domconv.SpeakerMemory), meta["statement"], string(metaJSON), state, MemoryLayerL1, "lifecycle-test", createdAt.UTC(), updatedAt.UTC())
	if err != nil {
		t.Fatalf("insert memory %s: %v", id, err)
	}
}

func snapshotOwnerLifecycleMemories(t *testing.T, store *L1SQLiteStore, owner string) []lifecycleMemorySnapshot {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `
SELECT id, namespace, message, meta_json, memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace = ? AND speaker = ? AND layer = ?
ORDER BY id ASC
`, "user:"+owner, string(domconv.SpeakerMemory), MemoryLayerL1)
	if err != nil {
		t.Fatalf("snapshot query: %v", err)
	}
	defer rows.Close()
	var snapshots []lifecycleMemorySnapshot
	for rows.Next() {
		var snapshot lifecycleMemorySnapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.Namespace, &snapshot.Message, &snapshot.MetaJSON, &snapshot.MemoryState, &snapshot.Layer, &snapshot.Source, &snapshot.CreatedAt, &snapshot.UpdatedAt); err != nil {
			t.Fatalf("snapshot scan: %v", err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot rows: %v", err)
	}
	return snapshots
}

func lifecycleActionIDs(actions []domainmemory.UserMemoryLifecycleAction) []string {
	ids := make([]string, 0, len(actions))
	for _, action := range actions {
		ids = append(ids, action.Operation+":"+action.MemoryID)
	}
	return ids
}

func TestOwnerPlanUserMemoryLifecycleDeterministicBoundedCohort(t *testing.T) {
	store := newLifecycleREDStore(t)
	defer store.Close()
	owner := "owner-plan"
	otherOwner := "owner-other"
	evaluationAt := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	oldCandidate := evaluationAt.Add(-8 * 24 * time.Hour)
	oldDecay := evaluationAt.Add(-95 * 24 * time.Hour)
	insertOwnerLifecycleMemory(t, store, "c-active", owner, MemoryStateCandidate, true, "", oldCandidate, oldCandidate, nil)
	insertOwnerLifecycleMemory(t, store, "c-current", owner, MemoryStateCandidate, true, "", evaluationAt.Add(-24*time.Hour), evaluationAt.Add(-24*time.Hour), nil)
	insertOwnerLifecycleMemory(t, store, "d-confirmed", owner, MemoryStateConfirmed, true, "", oldDecay, oldDecay, nil)
	insertOwnerLifecycleMemory(t, store, "d-pinned-policy", owner, MemoryStateConfirmed, true, "", oldDecay, oldDecay, map[string]interface{}{"ttl_policy": "pinned"})
	insertOwnerLifecycleMemory(t, store, "v-inactive", owner, MemoryStateCandidate, false, "", oldCandidate, oldCandidate, nil)
	insertOwnerLifecycleMemory(t, store, "v-superseded", owner, MemoryStateCandidate, false, "replacement", oldCandidate, oldCandidate, nil)
	insertOwnerLifecycleMemory(t, store, "v-queued", owner, MemoryStateCandidate, false, "", oldCandidate, oldCandidate, map[string]interface{}{"vector_cleanup_status": "queued"})
	insertOwnerLifecycleMemory(t, store, "v-done", owner, MemoryStateCandidate, false, "", oldCandidate, oldCandidate, map[string]interface{}{"vector_cleanup_status": "done"})
	insertOwnerLifecycleMemory(t, store, "v-completed", owner, MemoryStateCandidate, false, "", oldCandidate, oldCandidate, map[string]interface{}{"vector_cleanup_completed_at": evaluationAt.Add(-time.Hour).Format(time.RFC3339Nano)})
	insertOwnerLifecycleMemory(t, store, "other-old", otherOwner, MemoryStateCandidate, true, "", oldCandidate, oldCandidate, nil)

	before := snapshotOwnerLifecycleMemories(t, store, owner)
	requestID := "plan-deterministic"
	result, err := store.OwnerPlanUserMemoryLifecycleAt(ownerLifecycleContext(requestID, owner), requestID, owner, owner, evaluationAt)
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if result.CohortCount != 9 {
		t.Fatalf("cohort_count=%d, want 9", result.CohortCount)
	}
	if result.ActionCount != 4 {
		t.Fatalf("action_count=%d, want 4", result.ActionCount)
	}
	wantActions := []string{
		"candidate_review:c-active",
		"decay:d-confirmed",
		"vector_cleanup_queue:v-inactive",
		"vector_cleanup_queue:v-superseded",
	}
	if got := lifecycleActionIDs(result.Actions); !reflect.DeepEqual(got, wantActions) {
		t.Fatalf("actions=%v, want %v", got, wantActions)
	}
	if !result.ExpiresAt.Equal(evaluationAt.Add(userMemoryLifecyclePlanTTL)) {
		t.Fatalf("expires_at=%s, want evaluation_at+TTL", result.ExpiresAt)
	}
	if result.Receipt.Operation != domainmemory.UserMemoryLifecycleOperationPlan || result.Receipt.OwnerRoute != "conversation_l1/user_memory/lifecycle/plan" || result.Receipt.InputCount != result.CohortCount || result.Receipt.OutputCount != result.ActionCount || result.Receipt.AuditReference != requestID {
		t.Fatalf("plan receipt=%#v", result.Receipt)
	}
	if strings.Contains(string(mustJSON(t, result)), "statement-") {
		t.Fatal("plan result leaked statement/raw memory content")
	}
	after := snapshotOwnerLifecycleMemories(t, store, owner)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("plan mutated semantic owner rows:\nbefore=%#v\nafter=%#v", before, after)
	}
	var payloadHash string
	if err := store.db.QueryRowContext(context.Background(), `SELECT payload_hash FROM l1_user_memory_lifecycle_plan WHERE plan_request_id = ?`, requestID).Scan(&payloadHash); err != nil {
		t.Fatalf("read plan payload hash: %v", err)
	}
	if payloadHash != lifecyclePlanPayloadHash {
		t.Fatalf("payload_hash=%q, want sha256({})=%q", payloadHash, lifecyclePlanPayloadHash)
	}
}

func TestOwnerUserMemoryLifecycleEmptyActionsRemainJSONArray(t *testing.T) {
	store := newLifecycleREDStore(t)
	defer store.Close()
	owner := "owner-empty-actions"
	evaluationAt := time.Now().UTC()
	planRequestID := "empty-actions-plan"
	plan, err := store.OwnerPlanUserMemoryLifecycleAt(ownerLifecycleContext(planRequestID, owner), planRequestID, owner, owner, evaluationAt)
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if plan.ActionCount != 0 {
		t.Fatalf("plan action_count=%d, want 0", plan.ActionCount)
	}
	if payload := string(mustJSON(t, plan)); !strings.Contains(payload, `"actions":[]`) {
		t.Fatalf("empty plan actions must be a JSON array: %s", payload)
	}
	runRequestID := "empty-actions-run"
	run, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext(runRequestID, owner), runRequestID, owner, owner, planRequestID, "apply empty plan", true)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if run.ActionCount != 0 {
		t.Fatalf("run action_count=%d, want 0", run.ActionCount)
	}
	if payload := string(mustJSON(t, run)); !strings.Contains(payload, `"actions":[]`) {
		t.Fatalf("empty run actions must be a JSON array: %s", payload)
	}
}

func mustJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return payload
}

func TestOwnerUserMemoryLifecycleScopeNilAndInvalidArguments(t *testing.T) {
	const owner = "owner-scope"
	const requestID = "scope-request"
	validCtx := ownerLifecycleContext(requestID, owner)

	var nilStore *L1SQLiteStore
	if _, err := nilStore.OwnerPlanUserMemoryLifecycle(validCtx, requestID, owner, owner); !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("nil plan store error=%v, want ErrUserMemoryOwnerUnavailable", err)
	}
	if _, err := nilStore.OwnerRunUserMemoryLifecycle(validCtx, requestID, owner, owner, "plan", "reason", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("nil run store error=%v, want ErrUserMemoryOwnerUnavailable", err)
	}
	store := newLifecycleREDStore(t)
	defer store.Close()

	wrongRequest := ownerLifecycleContext("different-request", owner)
	wrongActor := ownerLifecycleContext(requestID, "different-owner")
	agentScope := domaintool.ToolExecutionScope{
		RequestID:            requestID,
		ActorKind:            domaintool.ActorKindAgent,
		ActorID:              "mio",
		AuthenticatedUserID:  owner,
		AllowedDataScopes:    []string{domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
	}
	noUserScope := domaintool.ToolExecutionScope{
		RequestID:            requestID,
		ActorKind:            domaintool.ActorKindUser,
		ActorID:              owner,
		AuthenticatedUserID:  owner,
		AllowedDataScopes:    []string{domaintool.DataScopePublic},
		AuthenticationSource: domaintool.AuthenticationSourceHTTP,
	}
	invalidScope := domaintool.ToolExecutionScope{
		RequestID:            requestID,
		ActorKind:            domaintool.ActorKindUser,
		ActorID:              owner,
		AuthenticatedUserID:  "different-owner",
		AllowedDataScopes:    []string{domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceHTTP,
	}
	for name, tc := range map[string]struct {
		ctx context.Context
	}{
		"missing scope":        {ctx: context.Background()},
		"request mismatch":     {ctx: wrongRequest},
		"actor kind mismatch":  {ctx: domaintool.WithToolExecutionScope(context.Background(), agentScope)},
		"actor owner mismatch": {ctx: wrongActor},
		"data scope missing":   {ctx: domaintool.WithToolExecutionScope(context.Background(), noUserScope)},
		"invalid scope":        {ctx: domaintool.WithToolExecutionScope(context.Background(), invalidScope)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.OwnerPlanUserMemoryLifecycle(tc.ctx, requestID, owner, owner); !errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) {
				t.Fatalf("scope error=%v, want ErrUserMemoryOwnerForbidden", err)
			}
		})
	}
	if _, err := store.OwnerPlanUserMemoryLifecycle(context.Background(), "", owner, owner); !errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) {
		t.Fatalf("empty plan request error=%v, want ErrUserMemoryOwnerInvalid", err)
	}
	if _, err := store.OwnerPlanUserMemoryLifecycle(validCtx, requestID, "", owner); !errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) {
		t.Fatalf("empty plan owner error=%v, want ErrUserMemoryOwnerInvalid", err)
	}
	if _, err := store.OwnerPlanUserMemoryLifecycle(validCtx, requestID, owner, ""); !errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) {
		t.Fatalf("empty plan actor error=%v, want ErrUserMemoryOwnerInvalid", err)
	}
	if _, err := store.OwnerRunUserMemoryLifecycle(validCtx, requestID, owner, owner, "", "reason", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) {
		t.Fatalf("empty run plan error=%v, want ErrUserMemoryOwnerInvalid", err)
	}
	if _, err := store.OwnerRunUserMemoryLifecycle(validCtx, requestID, owner, owner, "plan", "", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) {
		t.Fatalf("empty run reason error=%v, want ErrUserMemoryOwnerInvalid", err)
	}
	if _, err := store.OwnerRunUserMemoryLifecycle(validCtx, requestID, owner, owner, "plan", "reason", false); !errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) {
		t.Fatalf("apply=false error=%v, want ErrUserMemoryOwnerInvalid", err)
	}
}

func TestOwnerPlanUserMemoryLifecycleCohortIgnoresRawAndOtherOwnerChanges(t *testing.T) {
	store := newLifecycleREDStore(t)
	defer store.Close()
	evaluationAt := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	createdAt := evaluationAt.Add(-8 * 24 * time.Hour)
	insertOwnerLifecycleMemory(t, store, "owner-memory", "owner-hash", MemoryStateCandidate, true, "", createdAt, createdAt, nil)
	first, err := store.OwnerPlanUserMemoryLifecycleAt(ownerLifecycleContext("hash-plan-1", "owner-hash"), "hash-plan-1", "owner-hash", "owner-hash", evaluationAt)
	if err != nil {
		t.Fatalf("first plan failed: %v", err)
	}

	var metaJSON string
	if err := store.db.QueryRowContext(context.Background(), `SELECT meta_json FROM l1_memory_event WHERE id = ?`, "owner-memory").Scan(&metaJSON); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	meta["statement"] = "changed raw statement"
	meta["unrelated_raw_meta"] = map[string]interface{}{"secret": "not in cohort"}
	changedMeta, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("encode changed metadata: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `UPDATE l1_memory_event SET message = ?, meta_json = ? WHERE id = ?`, "changed raw statement", string(changedMeta), "owner-memory"); err != nil {
		t.Fatalf("mutate raw statement/meta: %v", err)
	}
	insertOwnerLifecycleMemory(t, store, "other-memory", "other-hash", MemoryStateCandidate, true, "", createdAt, createdAt, nil)

	second, err := store.OwnerPlanUserMemoryLifecycleAt(ownerLifecycleContext("hash-plan-2", "owner-hash"), "hash-plan-2", "owner-hash", "owner-hash", evaluationAt)
	if err != nil {
		t.Fatalf("second plan failed: %v", err)
	}
	if second.CohortHash != first.CohortHash {
		t.Fatalf("cohort hash changed for raw/full-meta or other-owner change: first=%s second=%s", first.CohortHash, second.CohortHash)
	}
}

type lifecycleVectorCleanupSinkSpy struct {
	calls int
}

func (s *lifecycleVectorCleanupSinkSpy) CleanupMemoryVectors(_ context.Context, _ []L1VectorCleanupItem) (*L1VectorCleanupResult, error) {
	s.calls++
	return &L1VectorCleanupResult{}, nil
}

func readLifecycleMemoryMeta(t *testing.T, store *L1SQLiteStore, id string) map[string]interface{} {
	t.Helper()
	var raw string
	if err := store.db.QueryRowContext(context.Background(), `SELECT meta_json FROM l1_memory_event WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read lifecycle metadata %s: %v", id, err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("decode lifecycle metadata %s: %v", id, err)
	}
	return meta
}

func TestOwnerRunUserMemoryLifecycleAppliesAtomicallyAndAuditsActualScore(t *testing.T) {
	store := newLifecycleREDStore(t)
	defer store.Close()
	store.WithVectorCleanupSink(&lifecycleVectorCleanupSinkSpy{})
	spy := store.vectorCleanupSink.(*lifecycleVectorCleanupSinkSpy)
	owner := "owner-run"
	evaluationAt := time.Now().UTC()
	insertOwnerLifecycleMemory(t, store, "a-candidate", owner, MemoryStateCandidate, true, "", evaluationAt.Add(-8*24*time.Hour), evaluationAt.Add(-8*24*time.Hour), nil)
	insertOwnerLifecycleMemory(t, store, "b-decay", owner, MemoryStateConfirmed, true, "", evaluationAt.Add(-200*24*time.Hour), evaluationAt.Add(-200*24*time.Hour), nil)
	insertOwnerLifecycleMemory(t, store, "c-vector", owner, MemoryStateCandidate, false, "", evaluationAt.Add(-8*24*time.Hour), evaluationAt.Add(-8*24*time.Hour), nil)
	planRequestID := "run-plan"
	plan, err := store.OwnerPlanUserMemoryLifecycleAt(ownerLifecycleContext(planRequestID, owner), planRequestID, owner, owner, evaluationAt)
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if plan.ActionCount != 3 {
		t.Fatalf("plan action_count=%d, want 3", plan.ActionCount)
	}
	var wantDecayScore float64
	for _, action := range plan.Actions {
		if action.Operation == domainmemory.UserMemoryLifecycleActionDecay {
			wantDecayScore = action.DecayScore
		}
	}
	if wantDecayScore == 0 {
		t.Fatalf("plan decay score=%v is not a positive actual score", wantDecayScore)
	}

	runRequestID := "run-request"
	run, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext(runRequestID, owner), runRequestID, owner, owner, planRequestID, "apply lifecycle v1", true)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if run.Status != "completed" || run.ActionCount != plan.ActionCount {
		t.Fatalf("run result=%#v", run)
	}
	if run.Receipt.Operation != domainmemory.UserMemoryLifecycleOperationRun || run.Receipt.OwnerRoute != "conversation_l1/user_memory/lifecycle/run" || run.Receipt.InputCount != 3 || run.Receipt.OutputCount != 3 || run.Receipt.AuditReference != planRequestID {
		t.Fatalf("run receipt=%#v", run.Receipt)
	}
	if spy.calls != 0 {
		t.Fatalf("vector cleanup sink calls=%d, want 0", spy.calls)
	}
	candidateMeta := readLifecycleMemoryMeta(t, store, "a-candidate")
	if candidateMeta["review_status"] != "queued" || strings.TrimSpace(fmt.Sprint(candidateMeta["review_queued_at"])) == "" {
		t.Fatalf("candidate metadata=%#v", candidateMeta)
	}
	decayMeta := readLifecycleMemoryMeta(t, store, "b-decay")
	if decayMeta["lifecycle_status"] != "decayed" || decayMeta["decay_policy"] == "" || decayMeta["decayed_at"] == "" {
		t.Fatalf("decay metadata=%#v", decayMeta)
	}
	if got, ok := decayMeta["decay_score"].(float64); !ok || got != wantDecayScore {
		t.Fatalf("decay metadata score=%#v, want actual %v", decayMeta["decay_score"], wantDecayScore)
	}
	vectorMeta := readLifecycleMemoryMeta(t, store, "c-vector")
	if vectorMeta["vector_cleanup_status"] != "queued" || vectorMeta["vector_cleanup_queued_at"] == "" {
		t.Fatalf("vector metadata=%#v", vectorMeta)
	}
	var planStatus string
	if err := store.db.QueryRowContext(context.Background(), `SELECT status FROM l1_user_memory_lifecycle_plan WHERE plan_request_id = ?`, planRequestID).Scan(&planStatus); err != nil {
		t.Fatalf("read applied plan: %v", err)
	}
	if planStatus != userMemoryLifecycleAppliedStatus {
		t.Fatalf("plan status=%q, want %q", planStatus, userMemoryLifecycleAppliedStatus)
	}
	var storedRunReceipt string
	if err := store.db.QueryRowContext(context.Background(), `SELECT receipt_json FROM l1_user_memory_lifecycle_run_receipt WHERE plan_request_id = ?`, planRequestID).Scan(&storedRunReceipt); err != nil {
		t.Fatalf("read run receipt: %v", err)
	}
	if !strings.Contains(storedRunReceipt, planRequestID) || strings.Contains(storedRunReceipt, "statement-") {
		t.Fatalf("unexpected bounded run receipt=%s", storedRunReceipt)
	}
	var decayPayload string
	if err := store.db.QueryRowContext(context.Background(), `
SELECT payload_json FROM l1_event_log
WHERE event_type = 'memory.user_lifecycle_decay' AND namespace = ?
ORDER BY rowid DESC LIMIT 1
`, "user:"+owner).Scan(&decayPayload); err != nil {
		t.Fatalf("read decay audit: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(decayPayload), &payload); err != nil {
		t.Fatalf("decode decay audit: %v", err)
	}
	if got, ok := payload["decay_score"].(float64); !ok || got != wantDecayScore || got == 0.5 {
		t.Fatalf("decay audit payload=%#v, want actual score %v", payload, wantDecayScore)
	}
	beforeSecondRun := snapshotOwnerLifecycleMemories(t, store, owner)
	if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("run-second", owner), "run-second", owner, owner, planRequestID, "repeat", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
		t.Fatalf("second run error=%v, want conflict", err)
	}
	if after := snapshotOwnerLifecycleMemories(t, store, owner); !reflect.DeepEqual(beforeSecondRun, after) {
		t.Fatalf("second run mutated semantic rows:\nbefore=%#v\nafter=%#v", beforeSecondRun, after)
	}
}

func TestOwnerRunUserMemoryLifecycleRollbackLeavesSemanticRowsAndPlanUnused(t *testing.T) {
	store := newLifecycleREDStore(t)
	defer store.Close()
	owner := "owner-atomic"
	evaluationAt := time.Now().UTC()
	oldCandidate := evaluationAt.Add(-8 * 24 * time.Hour)
	oldDecay := evaluationAt.Add(-95 * 24 * time.Hour)
	insertOwnerLifecycleMemory(t, store, "a-candidate", owner, MemoryStateCandidate, true, "", oldCandidate, oldCandidate, nil)
	insertOwnerLifecycleMemory(t, store, "b-decay", owner, MemoryStateConfirmed, true, "", oldDecay, oldDecay, nil)
	planRequestID := "atomic-plan"
	if _, err := store.OwnerPlanUserMemoryLifecycleAt(ownerLifecycleContext(planRequestID, owner), planRequestID, owner, owner, evaluationAt); err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	before := snapshotOwnerLifecycleMemories(t, store, owner)
	if _, err := store.db.ExecContext(context.Background(), `
CREATE TRIGGER lifecycle_atomic_failure
BEFORE UPDATE ON l1_memory_event
WHEN OLD.id = 'b-decay'
BEGIN
	SELECT RAISE(ABORT, 'intentional lifecycle atomic failure');
END;
`); err != nil {
		t.Fatalf("create atomic failure trigger: %v", err)
	}
	if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("atomic-run", owner), "atomic-run", owner, owner, planRequestID, "atomic test", true); err == nil {
		t.Fatal("run succeeded despite semantic update failure trigger")
	}
	if after := snapshotOwnerLifecycleMemories(t, store, owner); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed run committed partial semantic changes:\nbefore=%#v\nafter=%#v", before, after)
	}
	var planStatus string
	if err := store.db.QueryRowContext(context.Background(), `SELECT status FROM l1_user_memory_lifecycle_plan WHERE plan_request_id = ?`, planRequestID).Scan(&planStatus); err != nil {
		t.Fatalf("read plan after rollback: %v", err)
	}
	if planStatus != userMemoryLifecyclePlanStatus {
		t.Fatalf("plan status after rollback=%q, want %q", planStatus, userMemoryLifecyclePlanStatus)
	}
	var runCount int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM l1_user_memory_lifecycle_run_receipt WHERE plan_request_id = ?`, planRequestID).Scan(&runCount); err != nil {
		t.Fatalf("count run receipts: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("run receipt count=%d after rollback, want 0", runCount)
	}
	var actionAuditCount int
	if err := store.db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM l1_event_log
WHERE namespace = ? AND event_type LIKE 'memory.user_lifecycle_%'
  AND event_type <> 'memory.user_lifecycle_plan_created'
`, "user:"+owner).Scan(&actionAuditCount); err != nil {
		t.Fatalf("count action audit after rollback: %v", err)
	}
	if actionAuditCount != 0 {
		t.Fatalf("action audit count=%d after rollback, want 0", actionAuditCount)
	}
}

func TestOwnerRunUserMemoryLifecycleRejectsDriftExpiryAndWrongOwner(t *testing.T) {
	newStoreWithPlan := func(t *testing.T, owner, planRequestID string, evaluationAt time.Time) (*L1SQLiteStore, domainmemory.UserMemoryLifecyclePlanResponse) {
		t.Helper()
		store := newLifecycleREDStore(t)
		old := evaluationAt.Add(-8 * 24 * time.Hour)
		insertOwnerLifecycleMemory(t, store, "memory-1", owner, MemoryStateCandidate, true, "", old, old, nil)
		plan, err := store.OwnerPlanUserMemoryLifecycleAt(ownerLifecycleContext(planRequestID, owner), planRequestID, owner, owner, evaluationAt)
		if err != nil {
			t.Fatalf("plan failed: %v", err)
		}
		return store, plan
	}

	t.Run("changed owner cohort", func(t *testing.T) {
		owner := "owner-drift-changed"
		store, plan := newStoreWithPlan(t, owner, "drift-changed-plan", time.Now().UTC())
		defer store.Close()
		if _, err := store.db.ExecContext(context.Background(), `UPDATE l1_memory_event SET memory_state = ? WHERE id = ?`, MemoryStateConfirmed, "memory-1"); err != nil {
			t.Fatalf("change owner state: %v", err)
		}
		afterDrift := snapshotOwnerLifecycleMemories(t, store, owner)
		if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("drift-changed-run", owner), "drift-changed-run", owner, owner, plan.PlanRequestID, "drift", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
			t.Fatalf("changed cohort error=%v, want conflict", err)
		}
		if after := snapshotOwnerLifecycleMemories(t, store, owner); !reflect.DeepEqual(afterDrift, after) {
			t.Fatalf("changed-cohort conflict mutated rows:\nbefore=%#v\nafter=%#v", afterDrift, after)
		}
	})

	t.Run("new owner row", func(t *testing.T) {
		owner := "owner-drift-new"
		evaluationAt := time.Now().UTC()
		store, plan := newStoreWithPlan(t, owner, "drift-new-plan", evaluationAt)
		defer store.Close()
		old := evaluationAt.Add(-8 * 24 * time.Hour)
		insertOwnerLifecycleMemory(t, store, "memory-2", owner, MemoryStateCandidate, true, "", old, old, nil)
		afterDrift := snapshotOwnerLifecycleMemories(t, store, owner)
		if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("drift-new-run", owner), "drift-new-run", owner, owner, plan.PlanRequestID, "drift", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
			t.Fatalf("new-row cohort error=%v, want conflict", err)
		}
		if after := snapshotOwnerLifecycleMemories(t, store, owner); !reflect.DeepEqual(afterDrift, after) {
			t.Fatalf("new-row conflict mutated rows:\nbefore=%#v\nafter=%#v", afterDrift, after)
		}
	})

	t.Run("deleted owner row", func(t *testing.T) {
		owner := "owner-drift-deleted"
		store, plan := newStoreWithPlan(t, owner, "drift-deleted-plan", time.Now().UTC())
		defer store.Close()
		if _, err := store.db.ExecContext(context.Background(), `DELETE FROM l1_memory_event WHERE id = ?`, "memory-1"); err != nil {
			t.Fatalf("delete owner row: %v", err)
		}
		afterDrift := snapshotOwnerLifecycleMemories(t, store, owner)
		if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("drift-deleted-run", owner), "drift-deleted-run", owner, owner, plan.PlanRequestID, "drift", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
			t.Fatalf("deleted-row cohort error=%v, want conflict", err)
		}
		if after := snapshotOwnerLifecycleMemories(t, store, owner); !reflect.DeepEqual(afterDrift, after) {
			t.Fatalf("deleted-row conflict mutated rows:\nbefore=%#v\nafter=%#v", afterDrift, after)
		}
	})

	t.Run("expired plan", func(t *testing.T) {
		owner := "owner-expired"
		evaluationAt := time.Now().UTC().Add(-time.Hour)
		store, plan := newStoreWithPlan(t, owner, "expired-plan", evaluationAt)
		defer store.Close()
		before := snapshotOwnerLifecycleMemories(t, store, owner)
		if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("expired-run", owner), "expired-run", owner, owner, plan.PlanRequestID, "expired", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict) {
			t.Fatalf("expired plan error=%v, want conflict", err)
		}
		if after := snapshotOwnerLifecycleMemories(t, store, owner); !reflect.DeepEqual(before, after) {
			t.Fatalf("expired plan mutated rows:\nbefore=%#v\nafter=%#v", before, after)
		}
	})

	t.Run("different owner not found", func(t *testing.T) {
		owner := "owner-plan-owner"
		store, plan := newStoreWithPlan(t, owner, "different-owner-plan", time.Now().UTC())
		defer store.Close()
		before := snapshotOwnerLifecycleMemories(t, store, owner)
		if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("different-owner-run", "other-owner"), "different-owner-run", "other-owner", "other-owner", plan.PlanRequestID, "wrong owner", true); !errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound) {
			t.Fatalf("different owner error=%v, want not found", err)
		}
		if after := snapshotOwnerLifecycleMemories(t, store, owner); !reflect.DeepEqual(before, after) {
			t.Fatalf("different-owner rejection mutated rows:\nbefore=%#v\nafter=%#v", before, after)
		}
	})

	t.Run("different owner change does not drift plan", func(t *testing.T) {
		owner := "owner-independent"
		evaluationAt := time.Now().UTC()
		store, plan := newStoreWithPlan(t, owner, "independent-plan", evaluationAt)
		defer store.Close()
		old := evaluationAt.Add(-8 * 24 * time.Hour)
		insertOwnerLifecycleMemory(t, store, "other-memory", "other-owner", MemoryStateCandidate, true, "", old, old, nil)
		if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("independent-run", owner), "independent-run", owner, owner, plan.PlanRequestID, "owner only", true); err != nil {
			t.Fatalf("other-owner change caused owner plan drift: %v", err)
		}
	})

	t.Run("apply false is invalid and non-mutating", func(t *testing.T) {
		owner := "owner-invalid-apply"
		store, plan := newStoreWithPlan(t, owner, "invalid-apply-plan", time.Now().UTC())
		defer store.Close()
		before := snapshotOwnerLifecycleMemories(t, store, owner)
		if _, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext("invalid-apply-run", owner), "invalid-apply-run", owner, owner, plan.PlanRequestID, "do not apply", false); !errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid) {
			t.Fatalf("apply=false error=%v, want invalid", err)
		}
		if after := snapshotOwnerLifecycleMemories(t, store, owner); !reflect.DeepEqual(before, after) {
			t.Fatalf("apply=false mutated rows:\nbefore=%#v\nafter=%#v", before, after)
		}
	})
}
