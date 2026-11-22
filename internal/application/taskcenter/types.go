package taskcenter

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	OwnerRefWorkstream = "workstream"
	ViewRemaining      = "remaining"
	ViewClaimable      = "claimable"
	ViewDaily          = "daily"
	ViewHistory        = "history"
)

var (
	ErrConflict    = domainworkstream.ErrTaskPlanConflict
	ErrForbidden   = domainworkstream.ErrTaskPlanForbidden
	ErrInvalid     = domainworkstream.ErrTaskPlanInvalid
	ErrUnavailable = domainworkstream.ErrTaskPlanUnavailable
	ErrNotFound    = domainworkstream.ErrTaskPlanNotFound
	ErrUnsupported = domainworkstream.ErrTaskPlanUnsupported
)

type Kind struct {
	ID                     string
	Label                  string
	InputSchema            json.RawMessage
	IntrinsicAllowedAgents []string
	RequiresTypedInput     bool
	ExecutorAvailable      bool
	UnavailableReason      string
}

type KindCapability struct {
	KindID             string          `json:"kind_id"`
	Label              string          `json:"label"`
	InputSchema        json.RawMessage `json:"input_schema,omitempty"`
	AllowedAgents      []string        `json:"allowed_agents"`
	RequiresTypedInput bool            `json:"requires_typed_input,omitempty"`
	ExecutorAvailable  bool            `json:"executor_available"`
	UnavailableReason  string          `json:"unavailable_reason,omitempty"`
}

type Item struct {
	GoalID                string                `json:"goal_id"`
	Title                 string                `json:"title"`
	Description           string                `json:"description,omitempty"`
	KindInput             json.RawMessage       `json:"kind_input,omitempty"`
	KindInputVersion      int                   `json:"kind_input_version,omitempty"`
	SuccessCriteria       []string              `json:"success_criteria,omitempty"`
	Verification          []string              `json:"verification,omitempty"`
	KindID                string                `json:"kind_id"`
	CreatorKind           string                `json:"creator_kind,omitempty"`
	Assignee              string                `json:"assignee"`
	Status                string                `json:"status"`
	ReasonCode            string                `json:"reason_code,omitempty"`
	WorkstreamID          string                `json:"workstream_id"`
	PolicyRevision        string                `json:"policy_revision"`
	TaskID                string                `json:"task_id,omitempty"`
	RunID                 string                `json:"run_id,omitempty"`
	ResultArtifactID      string                `json:"result_artifact_id,omitempty"`
	ResultSHA256          string                `json:"result_sha256,omitempty"`
	ExecutionVerification string                `json:"execution_verification,omitempty"`
	CanAcceptResult       bool                  `json:"can_accept_result,omitempty"`
	Revision              uint64                `json:"revision"`
	AssignmentEpoch       uint64                `json:"assignment_epoch"`
	Timing                modulecore.TaskTiming `json:"timing"`
	TimingStatus          string                `json:"timing_status"`
	TimingSource          string                `json:"timing_source"`
	AllowedAgents         []string              `json:"allowed_agents"`
	DataScope             string                `json:"data_scope"`
	CreatedAt             time.Time             `json:"created_at"`
	UpdatedAt             time.Time             `json:"updated_at"`
}

type ListQuery struct {
	View     string `json:"view"`
	Assignee string `json:"assignee,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit"`
}

type ListResult struct {
	Items          []Item `json:"items"`
	NextCursor     string `json:"next_cursor,omitempty"`
	Partial        bool   `json:"partial"`
	PolicyRevision string `json:"policy_revision"`
}

type Capabilities struct {
	Kinds             []KindCapability `json:"kinds"`
	Agents            []string         `json:"agents"`
	PolicyRevision    string           `json:"policy_revision"`
	OwnerRef          string           `json:"owner_ref"`
	Revision          uint64           `json:"revision"`
	CanClaim          bool             `json:"can_claim"`
	ActorKind         string           `json:"actor_kind"`
	ManagedAvailable  bool             `json:"managed_available"`
	DailyAvailable    bool             `json:"daily_available"`
	CanSubmit         bool             `json:"can_submit"`
	ManagedReasonCode string           `json:"managed_reason_code,omitempty"`
	DailyReasonCode   string           `json:"daily_reason_code,omitempty"`
}

type SubmitInput struct {
	WorkstreamID           string                `json:"workstream_id,omitempty"`
	DataScope              string                `json:"data_scope,omitempty"`
	KindID                 string                `json:"kind_id"`
	KindInput              json.RawMessage       `json:"kind_input,omitempty"`
	Title                  string                `json:"title"`
	Description            string                `json:"description,omitempty"`
	SuccessCriteria        []string              `json:"success_criteria"`
	Verification           []string              `json:"verification"`
	Assignee               string                `json:"assignee,omitempty"`
	Timing                 modulecore.TaskTiming `json:"timing"`
	AllowedAgents          []string              `json:"allowed_agents,omitempty"`
	IdempotencyKey         string                `json:"idempotency_key"`
	ExpectedPolicyRevision string                `json:"expected_policy_revision"`
}

type GameProjectPlanStore interface {
	SubmitGameProjectPlan(context.Context, tool.ToolExecutionScope, domainworkstream.GameProjectSubmission) (domainworkstream.ProjectPlanMutationResult, error)
	GameProjectPlanReceipt(context.Context, tool.ToolExecutionScope, string) (domainworkstream.ProjectPlanMutationResult, bool, error)
}

type ClaimInput struct {
	GoalID                 string `json:"goal_id"`
	ExpectedRevision       uint64 `json:"expected_revision"`
	ExpectedPolicyRevision string `json:"expected_policy_revision"`
	IdempotencyKey         string `json:"idempotency_key"`
}

type ReleaseInput ClaimInput

type HandoffPrepareInput struct {
	GoalID                  string                   `json:"goal_id"`
	ExpectedRevision        uint64                   `json:"expected_revision"`
	ExpectedAssignmentEpoch uint64                   `json:"expected_assignment_epoch"`
	ExpectedPolicyRevision  string                   `json:"expected_policy_revision"`
	IdempotencyKey          string                   `json:"idempotency_key"`
	HandoffKey              taskexecution.HandoffKey `json:"handoff_key"`
	TaskDraft               json.RawMessage          `json:"task_draft"`
	SharedContext           json.RawMessage          `json:"shared_context"`
}

type HandoffTransitionInput struct {
	GoalID                  string                   `json:"goal_id"`
	ExpectedRevision        uint64                   `json:"expected_revision"`
	ExpectedAssignmentEpoch uint64                   `json:"expected_assignment_epoch"`
	ExpectedPolicyRevision  string                   `json:"expected_policy_revision"`
	IdempotencyKey          string                   `json:"idempotency_key"`
	HandoffKey              taskexecution.HandoffKey `json:"handoff_key"`
	TaskID                  modulecore.TaskID        `json:"task_id,omitempty"`
	RunID                   modulecore.RunID         `json:"run_id,omitempty"`
}

type AcceptResultInput struct {
	GoalID                  string `json:"goal_id"`
	ExpectedRevision        uint64 `json:"expected_revision"`
	ExpectedAssignmentEpoch uint64 `json:"expected_assignment_epoch"`
	IdempotencyKey          string `json:"idempotency_key"`
}

type ResultView struct {
	GoalID                string          `json:"goal_id"`
	Revision              uint64          `json:"revision"`
	AssignmentEpoch       uint64          `json:"assignment_epoch"`
	ResultArtifactID      string          `json:"result_artifact_id"`
	ResultSHA256          string          `json:"result_sha256"`
	ExecutionVerification string          `json:"execution_verification"`
	Result                json.RawMessage `json:"result"`
}

type HandoffReceiptAuthorizationCheck func(context.Context) error

// HandoffReceiptVerifier is a trusted composition port backed by the native
// TaskManager receipt reader. Implementations must invoke authorize around
// their read-only lookup and return only after matching the exact receipt.
type HandoffReceiptVerifier interface {
	VerifyTaskCenterHandoffReceipt(context.Context, domainworkstream.Goal, modulecore.TaskID, modulecore.RunID, HandoffReceiptAuthorizationCheck) error
}

type MutationResult struct {
	Item       Item   `json:"item"`
	Replayed   bool   `json:"replayed"`
	ReceiptRef string `json:"receipt_ref"`
}

type TaskPlanAuthorizer = func(domainworkstream.Goal) error

// TaskPlanStore is the native Goal owner port. Implementations must commit a
// managed Goal transition and its operation receipt atomically.
type TaskPlanStore interface {
	SubmitTaskPlan(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanSubmission, TaskPlanAuthorizer) (domainworkstream.TaskPlanMutationResult, error)
	ClaimTaskPlan(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanClaim, TaskPlanAuthorizer) (domainworkstream.TaskPlanMutationResult, error)
	ReleaseTaskPlan(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanRelease, TaskPlanAuthorizer) (domainworkstream.TaskPlanMutationResult, error)
	ListTaskPlans(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanQuery) ([]domainworkstream.Goal, string, bool, error)
	GetTaskPlan(context.Context, tool.ToolExecutionScope, string) (domainworkstream.Goal, error)
	TaskPlanReceipt(context.Context, tool.ToolExecutionScope, string, string) (domainworkstream.TaskPlanMutationResult, bool, error)
}

type TaskPlanHandoffAuthorizer = func(domainworkstream.Goal) error

// TaskPlanHandoffStore extends the native Goal owner with durable handoff CAS
// transitions. It remains optional so JSONL and legacy stores can report the
// operation as unsupported without pretending to provide transactional CAS.
type TaskPlanHandoffStore interface {
	PrepareTaskPlanHandoff(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanHandoffPrepare, TaskPlanHandoffAuthorizer) (domainworkstream.TaskPlanMutationResult, error)
	MarkTaskPlanHandoffUnknown(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanHandoffTransition, TaskPlanHandoffAuthorizer) (domainworkstream.TaskPlanMutationResult, error)
	LinkTaskPlanHandoff(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanHandoffTransition, TaskPlanHandoffAuthorizer) (domainworkstream.TaskPlanMutationResult, error)
}

type TaskPlanExecutionResultStore interface {
	RecordTaskPlanExecutionResult(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanExecutionResultUpdate, TaskPlanHandoffAuthorizer) (domainworkstream.TaskPlanMutationResult, error)
	AcceptTaskPlanExecutionResult(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanExecutionResultAcceptance, TaskPlanHandoffAuthorizer) (domainworkstream.TaskPlanMutationResult, error)
}

type TaskPlanExecutionResultReader interface {
	TaskPlanReceipt(context.Context, tool.ToolExecutionScope, string, string) (domainworkstream.TaskPlanMutationResult, bool, error)
}
