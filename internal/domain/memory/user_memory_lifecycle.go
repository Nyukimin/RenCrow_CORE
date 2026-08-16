package memory

import "time"

const UserMemoryLifecyclePolicyRevision = "memory-lifecycle/v1"

const (
	UserMemoryLifecycleOperationPlan            = "lifecycle_plan"
	UserMemoryLifecycleOperationRun             = "lifecycle_run"
	UserMemoryLifecycleActionCandidateReview    = "candidate_review"
	UserMemoryLifecycleActionDecay              = "decay"
	UserMemoryLifecycleActionVectorCleanupQueue = "vector_cleanup_queue"
)

// UserMemoryLifecycleAction is the bounded operation projection persisted in
// a lifecycle plan and receipt. It deliberately carries no statement or raw
// metadata.
type UserMemoryLifecycleAction struct {
	Operation  string  `json:"operation"`
	MemoryID   string  `json:"memory_id"`
	DecayScore float64 `json:"decay_score,omitempty"`
}

// UserMemoryLifecyclePlanResponse is the public projection of one owner plan.
type UserMemoryLifecyclePlanResponse struct {
	PlanRequestID  string                      `json:"plan_request_id"`
	Status         string                      `json:"status"`
	PolicyRevision string                      `json:"policy_revision"`
	CohortHash     string                      `json:"cohort_hash"`
	EvaluationAt   time.Time                   `json:"evaluation_at"`
	ExpiresAt      time.Time                   `json:"expires_at"`
	CohortCount    int                         `json:"cohort_count"`
	ActionCount    int                         `json:"action_count"`
	Actions        []UserMemoryLifecycleAction `json:"actions"`
	Receipt        UserMemoryOwnerReceipt      `json:"receipt"`
}

// UserMemoryLifecycleRunResponse is the bounded result of applying a plan.
type UserMemoryLifecycleRunResponse struct {
	PlanRequestID string                      `json:"plan_request_id"`
	Status        string                      `json:"status"`
	ActionCount   int                         `json:"action_count"`
	Actions       []UserMemoryLifecycleAction `json:"actions"`
	Receipt       UserMemoryOwnerReceipt      `json:"receipt"`
}
