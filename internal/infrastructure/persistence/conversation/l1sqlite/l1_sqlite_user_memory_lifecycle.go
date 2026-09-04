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

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const (
	userMemoryLifecyclePlanTTL          = 15 * time.Minute
	userMemoryLifecycleCandidateAfter   = 7 * 24 * time.Hour
	userMemoryLifecycleDecayAfter       = 90 * 24 * time.Hour
	userMemoryLifecycleActionLimit      = 200
	userMemoryLifecyclePlanStatus       = "planned"
	userMemoryLifecycleAppliedStatus    = "applied"
	userMemoryLifecycleReceiptCompleted = "completed"
)

const lifecyclePlanPayloadHash = "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" // SHA-256 of {}

type lifecyclePlanRow struct {
	PlanRequestID string
	PlanID        string
	OwnerID       string
	ActorID       string
	PayloadHash   string
	CohortHash    string
	ActionsJSON   string
	ActionCount   int
	EvaluationAt  time.Time
	CreatedAt     time.Time
	ExpiresAt     time.Time
	Status        string
	ReceiptJSON   string
}

type lifecycleCanonicalMemory struct {
	ID                       string `json:"id"`
	State                    string `json:"state"`
	Active                   bool   `json:"active"`
	SupersededBy             string `json:"superseded_by"`
	LifecycleStatus          string `json:"lifecycle_status"`
	ReviewStatus             string `json:"review_status"`
	VectorCleanupStatus      string `json:"vector_cleanup_status"`
	VectorCleanupCompletedAt string `json:"vector_cleanup_completed_at"`
	Type                     string `json:"type"`
	TTLPolicy                string `json:"ttl_policy"`
	CreatedAt                string `json:"created_at"`
	UpdatedAt                string `json:"updated_at"`
}

func (s *L1SQLiteStore) OwnerPlanUserMemoryLifecycle(ctx context.Context, requestID, ownerID, actorID string) (domainmemory.UserMemoryLifecyclePlanResponse, error) {
	return s.ownerPlanUserMemoryLifecycleAt(ctx, requestID, ownerID, actorID, time.Now().UTC())
}

// OwnerPlanUserMemoryLifecycleAt exists for deterministic owner-store tests;
// the public handler uses OwnerPlanUserMemoryLifecycle and server time.
func (s *L1SQLiteStore) OwnerPlanUserMemoryLifecycleAt(ctx context.Context, requestID, ownerID, actorID string, evaluationAt time.Time) (domainmemory.UserMemoryLifecyclePlanResponse, error) {
	return s.ownerPlanUserMemoryLifecycleAt(ctx, requestID, ownerID, actorID, evaluationAt)
}

func (s *L1SQLiteStore) ownerPlanUserMemoryLifecycleAt(ctx context.Context, requestID, ownerID, actorID string, evaluationAt time.Time) (domainmemory.UserMemoryLifecyclePlanResponse, error) {
	if s == nil || s.db == nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, domainmemory.ErrUserMemoryOwnerUnavailable
	}
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	if err := validateLifecycleOwnerRequest(ctx, requestID, ownerID, actorID); err != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, err
	}
	if evaluationAt.IsZero() {
		evaluationAt = time.Now().UTC()
	}
	evaluationAt = evaluationAt.UTC()
	createdAt := time.Now().UTC()
	expiresAt := evaluationAt.Add(userMemoryLifecyclePlanTTL)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, fmt.Errorf("%w: begin lifecycle plan transaction: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	events, err := lifecycleOwnerMemoryEvents(ctx, tx, ownerID)
	if err != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: read lifecycle cohort: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	cohortHash, err := lifecycleCohortHash(events)
	if err != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: hash lifecycle cohort: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	actions := lifecycleActions(events, evaluationAt)
	actionsJSON, err := json.Marshal(actions)
	if err != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: encode lifecycle actions: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	receipt := lifecyclePlanReceipt(requestID, ownerID, len(events), len(actions), createdAt)
	response := domainmemory.UserMemoryLifecyclePlanResponse{
		PlanRequestID:  requestID,
		Status:         userMemoryLifecyclePlanStatus,
		PolicyRevision: domainmemory.UserMemoryLifecyclePolicyRevision,
		CohortHash:     cohortHash,
		EvaluationAt:   evaluationAt,
		ExpiresAt:      expiresAt,
		CohortCount:    len(events),
		ActionCount:    len(actions),
		Actions:        cloneLifecycleActions(actions),
		Receipt:        receipt,
	}
	receiptJSON, err := json.Marshal(response)
	if err != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: encode lifecycle plan receipt: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_user_memory_lifecycle_plan (
	plan_request_id, plan_id, owner_id, actor_id, payload_hash, cohort_hash,
	actions_json, action_count, evaluation_at, created_at, expires_at, status, receipt_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, requestID, requestID, ownerID, actorID, lifecyclePlanPayloadHash, cohortHash,
		string(actionsJSON), len(actions), evaluationAt, createdAt, expiresAt, userMemoryLifecyclePlanStatus, string(receiptJSON)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return domainmemory.UserMemoryLifecyclePlanResponse{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
		}
		return domainmemory.UserMemoryLifecyclePlanResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: save lifecycle plan: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.user_lifecycle_plan_created", "user:"+ownerID, "", "", 0, "", map[string]interface{}{
		"plan_request_id": requestID,
		"owner_id":        ownerID,
		"actor_id":        actorID,
		"cohort_hash":     cohortHash,
		"evaluation_at":   evaluationAt.Format(time.RFC3339Nano),
		"action_count":    len(actions),
	}, ownerMemorySourcePrefix+actorID); err != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: save lifecycle plan audit: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.UserMemoryLifecyclePlanResponse{}, fmt.Errorf("%w: commit lifecycle plan: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	return response, nil
}

func (s *L1SQLiteStore) OwnerRunUserMemoryLifecycle(ctx context.Context, requestID, ownerID, actorID, planRequestID, reason string, apply bool) (domainmemory.UserMemoryLifecycleRunResponse, error) {
	if s == nil || s.db == nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, domainmemory.ErrUserMemoryOwnerUnavailable
	}
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	planRequestID = strings.TrimSpace(planRequestID)
	reason = strings.TrimSpace(reason)
	if err := validateLifecycleOwnerRequest(ctx, requestID, ownerID, actorID); err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, err
	}
	if planRequestID == "" || reason == "" || !apply {
		return domainmemory.UserMemoryLifecycleRunResponse{}, domainmemory.ErrUserMemoryOwnerInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, fmt.Errorf("%w: begin lifecycle run transaction: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	plan, found, err := findLifecyclePlan(ctx, tx, planRequestID)
	if err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: read lifecycle plan: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if !found || plan.OwnerID != ownerID {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerNotFound)
	}
	if plan.Status != userMemoryLifecyclePlanStatus {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
	}
	if !time.Now().UTC().Before(plan.ExpiresAt) {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
	}
	events, err := lifecycleOwnerMemoryEvents(ctx, tx, ownerID)
	if err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: read lifecycle run cohort: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	cohortHash, err := lifecycleCohortHash(events)
	if err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: hash lifecycle run cohort: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	actions := lifecycleActions(events, plan.EvaluationAt)
	actionsJSON, err := json.Marshal(actions)
	if err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: encode lifecycle run actions: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if cohortHash != plan.CohortHash || string(actionsJSON) != plan.ActionsJSON || len(actions) != plan.ActionCount {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
	}
	completedAt := time.Now().UTC()
	for _, action := range actions {
		if err := applyLifecycleAction(ctx, tx, ownerID, actorID, requestID, action, completedAt); err != nil {
			return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, err)
		}
	}
	receipt := lifecycleRunReceipt(requestID, ownerID, len(actions), planRequestID, completedAt)
	response := domainmemory.UserMemoryLifecycleRunResponse{
		PlanRequestID: planRequestID,
		Status:        userMemoryLifecycleReceiptCompleted,
		ActionCount:   len(actions),
		Actions:       cloneLifecycleActions(actions),
		Receipt:       receipt,
	}
	receiptJSON, err := json.Marshal(response)
	if err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: encode lifecycle run receipt: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_user_memory_lifecycle_run_receipt (
	server_request_id, plan_request_id, owner_id, actor_id, reason_hash,
	cohort_hash, actions_json, action_count, completed_at, status, receipt_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, requestID, planRequestID, ownerID, actorID, lifecycleReasonHash(reason), cohortHash,
		string(actionsJSON), len(actions), completedAt, userMemoryLifecycleReceiptCompleted, string(receiptJSON)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
		}
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: save lifecycle run receipt: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.user_lifecycle_run_completed", "user:"+ownerID, "", "", 0, "", map[string]interface{}{
		"plan_request_id": planRequestID,
		"request_id":      requestID,
		"owner_id":        ownerID,
		"actor_id":        actorID,
		"cohort_hash":     cohortHash,
		"action_count":    len(actions),
	}, ownerMemorySourcePrefix+actorID); err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: save lifecycle run audit: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	result, err := tx.ExecContext(ctx, `
UPDATE l1_user_memory_lifecycle_plan
SET status = ?
WHERE plan_request_id = ? AND owner_id = ? AND status = ?
`, userMemoryLifecycleAppliedStatus, planRequestID, ownerID, userMemoryLifecyclePlanStatus)
	if err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: mark lifecycle plan applied: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	}
	if affected, err := result.RowsAffected(); err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, fmt.Errorf("%w: inspect lifecycle plan update: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err))
	} else if affected != 1 {
		return domainmemory.UserMemoryLifecycleRunResponse{}, rollbackL1Tx(tx, domainmemory.ErrUserMemoryOwnerConflict)
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.UserMemoryLifecycleRunResponse{}, fmt.Errorf("%w: commit lifecycle run: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	return response, nil
}

func validateLifecycleOwnerRequest(ctx context.Context, requestID, ownerID, actorID string) error {
	if requestID == "" || ownerID == "" || actorID == "" {
		return domainmemory.ErrUserMemoryOwnerInvalid
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if actorID != ownerID || !ok || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != actorID || scope.AuthenticatedUserID != ownerID || !scope.Allows(domaintool.DataScopeUser) || scope.RequestID != requestID {
		return domainmemory.ErrUserMemoryOwnerForbidden
	}
	return nil
}

func lifecycleOwnerMemoryEvents(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}, ownerID string) ([]L1MemoryEvent, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace = ? AND speaker = ? AND layer = ?
ORDER BY id ASC
`, NamespaceKindUser+":"+ownerID, "memory", MemoryLayerL1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events, err := scanL1Events(rows)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if _, err := strictUserMemoryFromEvent(event); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func lifecycleCohortHash(events []L1MemoryEvent) (string, error) {
	canonical := make([]lifecycleCanonicalMemory, 0, len(events))
	for _, event := range events {
		item, err := strictUserMemoryFromEvent(event)
		if err != nil {
			return "", err
		}
		canonical = append(canonical, lifecycleCanonicalMemory{
			ID:                       item.ID,
			State:                    item.State,
			Active:                   item.Active,
			SupersededBy:             item.SupersededBy,
			LifecycleStatus:          item.LifecycleStatus,
			ReviewStatus:             metaStringValue(event.Meta, "review_status"),
			VectorCleanupStatus:      metaStringValue(event.Meta, "vector_cleanup_status"),
			VectorCleanupCompletedAt: metaStringValue(event.Meta, "vector_cleanup_completed_at"),
			Type:                     item.Type,
			TTLPolicy:                lifecycleDecayPolicy(event.Meta),
			CreatedAt:                item.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:                item.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func lifecycleActions(events []L1MemoryEvent, evaluationAt time.Time) []domainmemory.UserMemoryLifecycleAction {
	evaluationAt = evaluationAt.UTC()
	candidateCutoff := evaluationAt.Add(-userMemoryLifecycleCandidateAfter)
	decayCutoff := evaluationAt.Add(-userMemoryLifecycleDecayAfter)
	candidates := make([]domainmemory.UserMemoryLifecycleAction, 0, userMemoryLifecycleActionLimit)
	decays := make([]domainmemory.UserMemoryLifecycleAction, 0, userMemoryLifecycleActionLimit)
	cleanups := make([]domainmemory.UserMemoryLifecycleAction, 0, userMemoryLifecycleActionLimit)
	for _, event := range events {
		item, err := strictUserMemoryFromEvent(event)
		if err != nil {
			continue
		}
		if item.State == domainmemory.MemoryStateCandidate && item.Active && !item.CreatedAt.After(candidateCutoff) && metaStringValue(event.Meta, "review_status") != "queued" {
			if len(candidates) < userMemoryLifecycleActionLimit {
				candidates = append(candidates, domainmemory.UserMemoryLifecycleAction{Operation: domainmemory.UserMemoryLifecycleActionCandidateReview, MemoryID: item.ID})
			}
		}
		if item.State == domainmemory.MemoryStateConfirmed && item.Active && item.SupersededBy == "" && item.LifecycleStatus != "decayed" && lifecycleDecayPolicy(event.Meta) != "pinned" {
			cutoff := lifecycleDecayCutoff(evaluationAt, decayCutoff, event.Meta)
			if !item.UpdatedAt.After(cutoff) && len(decays) < userMemoryLifecycleActionLimit {
				decays = append(decays, domainmemory.UserMemoryLifecycleAction{Operation: domainmemory.UserMemoryLifecycleActionDecay, MemoryID: item.ID, DecayScore: lifecycleDecayScore(evaluationAt, item.UpdatedAt)})
			}
		}
		cleanupStatus := metaStringValue(event.Meta, "vector_cleanup_status")
		if (!item.Active || item.SupersededBy != "") && cleanupStatus != "queued" && cleanupStatus != "done" && metaStringValue(event.Meta, "vector_cleanup_completed_at") == "" && len(cleanups) < userMemoryLifecycleActionLimit {
			cleanups = append(cleanups, domainmemory.UserMemoryLifecycleAction{Operation: domainmemory.UserMemoryLifecycleActionVectorCleanupQueue, MemoryID: item.ID})
		}
	}
	actions := append(candidates, decays...)
	actions = append(actions, cleanups...)
	// The source query is ID ordered, but sort each operation explicitly so
	// action ordering remains stable if the query changes later.
	sort.SliceStable(actions, func(i, j int) bool {
		order := func(op string) int {
			switch op {
			case domainmemory.UserMemoryLifecycleActionCandidateReview:
				return 0
			case domainmemory.UserMemoryLifecycleActionDecay:
				return 1
			default:
				return 2
			}
		}
		left, right := order(actions[i].Operation), order(actions[j].Operation)
		if left != right {
			return left < right
		}
		return actions[i].MemoryID < actions[j].MemoryID
	})
	return actions
}

func cloneLifecycleActions(actions []domainmemory.UserMemoryLifecycleAction) []domainmemory.UserMemoryLifecycleAction {
	cloned := make([]domainmemory.UserMemoryLifecycleAction, len(actions))
	copy(cloned, actions)
	return cloned
}

func findLifecyclePlan(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, planRequestID string) (lifecyclePlanRow, bool, error) {
	var plan lifecyclePlanRow
	err := queryer.QueryRowContext(ctx, `
SELECT plan_request_id, plan_id, owner_id, actor_id, payload_hash, cohort_hash,
       actions_json, action_count, evaluation_at, created_at, expires_at, status, receipt_json
FROM l1_user_memory_lifecycle_plan
WHERE plan_request_id = ?
`, planRequestID).Scan(&plan.PlanRequestID, &plan.PlanID, &plan.OwnerID, &plan.ActorID, &plan.PayloadHash, &plan.CohortHash, &plan.ActionsJSON, &plan.ActionCount, &plan.EvaluationAt, &plan.CreatedAt, &plan.ExpiresAt, &plan.Status, &plan.ReceiptJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return lifecyclePlanRow{}, false, nil
	}
	if err != nil {
		return lifecyclePlanRow{}, false, err
	}
	return plan, true, nil
}

func applyLifecycleAction(ctx context.Context, tx *sql.Tx, ownerID, actorID, requestID string, action domainmemory.UserMemoryLifecycleAction, at time.Time) error {
	event, found, err := findL1MemoryEventByID(ctx, tx, action.MemoryID)
	if err != nil {
		return fmt.Errorf("%w: read lifecycle action memory: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if !found {
		return domainmemory.ErrUserMemoryOwnerConflict
	}
	item, err := strictUserMemoryFromEvent(event)
	if err != nil {
		return fmt.Errorf("%w: invalid lifecycle action memory: %v", domainmemory.ErrUserMemoryOwnerConflict, err)
	}
	if item.UserID != ownerID || event.Namespace != NamespaceKindUser+":"+ownerID {
		return domainmemory.ErrUserMemoryOwnerForbidden
	}
	meta := cloneOwnerMemoryMeta(event.Meta)
	eventType := ""
	switch action.Operation {
	case domainmemory.UserMemoryLifecycleActionCandidateReview:
		meta["review_status"] = "queued"
		meta["review_queued_at"] = at.UTC().Format(time.RFC3339Nano)
		eventType = "memory.user_lifecycle_candidate_review"
	case domainmemory.UserMemoryLifecycleActionDecay:
		meta["lifecycle_status"] = "decayed"
		meta["decay_policy"] = lifecycleDecayPolicy(event.Meta)
		meta["decay_score"] = action.DecayScore
		meta["decayed_at"] = at.UTC().Format(time.RFC3339Nano)
		eventType = "memory.user_lifecycle_decay"
	case domainmemory.UserMemoryLifecycleActionVectorCleanupQueue:
		meta["vector_cleanup_status"] = "queued"
		meta["vector_cleanup_queued_at"] = at.UTC().Format(time.RFC3339Nano)
		eventType = "memory.user_lifecycle_vector_cleanup_queue"
	default:
		return domainmemory.ErrUserMemoryOwnerInvalid
	}
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal lifecycle action metadata")
	if err != nil {
		return fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE l1_memory_event
SET meta_json = ?, updated_at = ?
WHERE id = ? AND namespace = ? AND speaker = ? AND layer = ?
`, metaJSON, at.UTC(), action.MemoryID, event.Namespace, "memory", MemoryLayerL1)
	if err != nil {
		return fmt.Errorf("%w: apply lifecycle action: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		if err != nil {
			return fmt.Errorf("%w: inspect lifecycle action update: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
		return domainmemory.ErrUserMemoryOwnerConflict
	}
	payload := map[string]interface{}{
		"memory_id":  action.MemoryID,
		"request_id": requestID,
		"owner_id":   ownerID,
		"actor_id":   actorID,
		"operation":  action.Operation,
	}
	if action.Operation == domainmemory.UserMemoryLifecycleActionDecay {
		payload["decay_score"] = action.DecayScore
	}
	if _, err := appendL1EventLog(ctx, tx, eventType, event.Namespace, event.SessionID, event.ThreadID, event.ThreadSeq, event.ThreadKind, payload, ownerMemorySourcePrefix+actorID); err != nil {
		return fmt.Errorf("%w: save lifecycle action audit: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	return nil
}

func lifecyclePlanReceipt(requestID, ownerID string, inputCount, outputCount int, completedAt time.Time) domainmemory.UserMemoryOwnerReceipt {
	return domainmemory.UserMemoryOwnerReceipt{
		RequestID:        requestID,
		Operation:        domainmemory.UserMemoryLifecycleOperationPlan,
		Status:           userMemoryLifecycleReceiptCompleted,
		OwnerRoute:       "conversation_l1/user_memory/lifecycle/plan",
		PolicyRevision:   domainmemory.UserMemoryLifecyclePolicyRevision,
		IdempotencyKey:   requestID,
		IdempotentReplay: false,
		InputCount:       inputCount,
		OutputCount:      outputCount,
		Warnings:         []string{},
		AuditReference:   requestID,
		CompletedAt:      completedAt,
	}
}

func lifecycleRunReceipt(requestID, ownerID string, actionCount int, planRequestID string, completedAt time.Time) domainmemory.UserMemoryOwnerReceipt {
	return domainmemory.UserMemoryOwnerReceipt{
		RequestID:        requestID,
		Operation:        domainmemory.UserMemoryLifecycleOperationRun,
		Status:           userMemoryLifecycleReceiptCompleted,
		OwnerRoute:       "conversation_l1/user_memory/lifecycle/run",
		PolicyRevision:   domainmemory.UserMemoryLifecyclePolicyRevision,
		IdempotencyKey:   requestID,
		IdempotentReplay: false,
		InputCount:       actionCount,
		OutputCount:      actionCount,
		Warnings:         []string{},
		AuditReference:   planRequestID,
		CompletedAt:      completedAt,
	}
}

func lifecycleReasonHash(reason string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(reason)))
	return hex.EncodeToString(digest[:])
}
