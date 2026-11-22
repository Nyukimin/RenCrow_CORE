package workstream

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	TaskPlanDataScopePublic        = "public"
	TaskPlanDataScopeUser          = "user"
	TaskPlanDataScopeInternal      = "internal"
	TaskPlanAssigneeUnassigned     = "unassigned"
	TaskPlanHandoffNone            = "none"
	TaskPlanHandoffPrepared        = "prepared"
	TaskPlanHandoffOutcomeUnknown  = "outcome_unknown"
	TaskPlanHandoffLinked          = "linked"
	TaskPlanKindMovieCatalogLookup = "movie.catalog.lookup"
	ProjectDomainGame              = "game"
	ProjectDomainSoftware          = "software"
	ProjectDomainContent           = "content"
)

var gameProjectStageOrder = [...]string{"idea", "specification", "prototype", "test", "review", "implementation", "post_implementation_qa"}

func GameProjectStageKeys() []string {
	return append([]string(nil), gameProjectStageOrder[:]...)
}

// GameProjectStage is one pre-registered, non-executable Workstream Goal.
type GameProjectStage struct {
	Key             string   `json:"key"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	SuccessCriteria []string `json:"success_criteria"`
	Verification    []string `json:"verification"`
}

// GameProjectSubmission describes one public Workstream and all planned stages.
// The stage Goals are created as drafts and are not TaskCenter execution items.
type GameProjectSubmission struct {
	Name           string             `json:"name"`
	Description    string             `json:"description,omitempty"`
	Stages         []GameProjectStage `json:"stages"`
	IdempotencyKey string             `json:"idempotency_key"`
}

func (submission GameProjectSubmission) Validate() error {
	if strings.TrimSpace(submission.Name) == "" || len([]rune(submission.Name)) > 160 {
		return errors.New("game project name is required and must be at most 160 characters")
	}
	if strings.TrimSpace(submission.Description) != submission.Description || len([]rune(submission.Description)) > 4000 {
		return errors.New("game project description is invalid")
	}
	if submission.IdempotencyKey == "" || len(submission.IdempotencyKey) > 128 || strings.TrimSpace(submission.IdempotencyKey) != submission.IdempotencyKey {
		return errors.New("game project idempotency_key is required and must be at most 128 characters")
	}
	if len(submission.Stages) != len(gameProjectStageOrder) {
		return fmt.Errorf("game project requires exactly %d ordered stages", len(gameProjectStageOrder))
	}
	for i, stage := range submission.Stages {
		if stage.Key != gameProjectStageOrder[i] || strings.TrimSpace(stage.Title) == "" || len([]rune(stage.Title)) > 160 || strings.TrimSpace(stage.Description) != stage.Description || len([]rune(stage.Description)) > 4000 {
			return fmt.Errorf("game project stage %d is invalid", i)
		}
		if !validProjectPlanTextList(stage.SuccessCriteria) || !validProjectPlanTextList(stage.Verification) {
			return fmt.Errorf("game project stage %q requires success criteria and verification", stage.Key)
		}
	}
	return nil
}

func validProjectPlanTextList(values []string) bool {
	if len(values) == 0 || len(values) > 16 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len([]rune(value)) > 1000 {
			return false
		}
	}
	return true
}

// ProjectPlanRegistration is immutable evidence of creation, not a current
// Workstream or Goal snapshot. Callers read current records from the Workstream API.
type ProjectPlanRegistration struct {
	WorkstreamID string    `json:"workstream_id"`
	GoalIDs      []string  `json:"goal_ids"`
	StageKeys    []string  `json:"stage_keys"`
	InputSHA256  string    `json:"input_sha256"`
	RegisteredAt time.Time `json:"registered_at"`
}

type ProjectPlanOperationReceipt struct {
	Version             int                     `json:"version"`
	ActorKind           string                  `json:"actor_kind"`
	ActorID             string                  `json:"actor_id"`
	AuthenticatedUserID string                  `json:"authenticated_user_id,omitempty"`
	Operation           string                  `json:"operation"`
	IdempotencyKey      string                  `json:"idempotency_key"`
	InputHash           string                  `json:"input_hash"`
	Registration        ProjectPlanRegistration `json:"registration"`
}

type ProjectPlanMutationResult struct {
	Registration ProjectPlanRegistration `json:"registration"`
	Replayed     bool                    `json:"replayed"`
	ReceiptRef   string                  `json:"receipt_ref"`
}

var (
	ErrTaskPlanConflict    = errors.New("task plan conflict")
	ErrTaskPlanForbidden   = errors.New("task plan forbidden")
	ErrTaskPlanInvalid     = errors.New("task plan invalid")
	ErrTaskPlanUnavailable = errors.New("task plan owner unavailable")
	ErrTaskPlanNotFound    = errors.New("task plan not found")
	ErrTaskPlanUnsupported = errors.New("task plan operation unsupported")
)

type TaskCenterWorkstreamScope struct {
	DataScope string `json:"data_scope"`
	UserID    string `json:"user_id,omitempty"`
}

func (scope TaskCenterWorkstreamScope) Validate() error {
	switch scope.DataScope {
	case TaskPlanDataScopePublic, TaskPlanDataScopeInternal:
		if scope.UserID != "" {
			return errors.New("public or internal task-center workstream must not set user_id")
		}
	case TaskPlanDataScopeUser:
		if !validIdentity(scope.UserID) {
			return errors.New("user task-center workstream requires user_id")
		}
	default:
		return fmt.Errorf("unsupported task-center workstream data_scope %q", scope.DataScope)
	}
	return nil
}

type TaskPlan struct {
	DataScope           string                   `json:"data_scope"`
	UserID              string                   `json:"user_id,omitempty"`
	CreatorKind         string                   `json:"creator_kind,omitempty"`
	CreatedBy           string                   `json:"created_by"`
	Assignee            string                   `json:"assignee"`
	KindID              string                   `json:"kind_id"`
	KindInput           json.RawMessage          `json:"kind_input,omitempty"`
	KindInputVersion    int                      `json:"kind_input_version,omitempty"`
	SubsetAllowedAgents *[]string                `json:"subset_allowed_agents,omitempty"`
	Revision            uint64                   `json:"revision"`
	AssignmentEpoch     uint64                   `json:"assignment_epoch"`
	PolicyRevision      string                   `json:"policy_revision"`
	Timing              modulecore.TaskTiming    `json:"timing"`
	HandoffState        string                   `json:"handoff_state"`
	HandoffKey          string                   `json:"handoff_key,omitempty"`
	HandoffInput        *TaskPlanHandoffInput    `json:"handoff_input,omitempty"`
	ExecutionResult     *TaskPlanExecutionResult `json:"execution_result,omitempty"`
	TaskID              modulecore.TaskID        `json:"task_id,omitempty"`
	RunID               modulecore.RunID         `json:"run_id,omitempty"`
	UpdatedAt           time.Time                `json:"updated_at"`
}

type TaskPlanExecutionResult struct {
	ArtifactID      string    `json:"artifact_id"`
	KindInputSHA256 string    `json:"kind_input_sha256"`
	ResultSHA256    string    `json:"result_sha256"`
	CriteriaSHA256  string    `json:"criteria_sha256"`
	Verification    string    `json:"verification"`
	AcceptedBy      string    `json:"accepted_by,omitempty"`
	AcceptedAt      time.Time `json:"accepted_at,omitempty"`
}

// TaskPlanHandoffInput freezes the exact native Task request payload before
// the Task owner is called. The serialized draft and context are opaque here
// because the Workstream domain does not own TaskManager types.
type TaskPlanHandoffInput struct {
	Version          int             `json:"version"`
	Revision         uint64          `json:"revision"`
	AssignmentEpoch  uint64          `json:"assignment_epoch"`
	KindID           string          `json:"kind_id"`
	KindInputVersion int             `json:"kind_input_version"`
	TaskDraft        []byte          `json:"task_draft"`
	SharedContext    []byte          `json:"shared_context"`
	KindInput        json.RawMessage `json:"kind_input"`
	InputHash        string          `json:"input_hash"`
}

type TaskPlanHandoffPrepare struct {
	GoalID                  string
	ExpectedRevision        uint64
	ExpectedAssignmentEpoch uint64
	ExpectedPolicyRevision  string
	IdempotencyKey          string
	HandoffKey              taskexecution.HandoffKey
	Input                   TaskPlanHandoffInput
}

type TaskPlanHandoffTransition struct {
	GoalID                  string
	ExpectedRevision        uint64
	ExpectedAssignmentEpoch uint64
	ExpectedPolicyRevision  string
	IdempotencyKey          string
	HandoffKey              taskexecution.HandoffKey
	TaskID                  modulecore.TaskID
	RunID                   modulecore.RunID
}

// TaskPlanExecutionResultUpdate binds an immutable native result reference to
// its source Goal after the Task owner has closed the successful Run receipt.
type TaskPlanExecutionResultUpdate struct {
	GoalID                  string
	ExpectedRevision        uint64
	ExpectedAssignmentEpoch uint64
	ExpectedPolicyRevision  string
	IdempotencyKey          string
	ArtifactID              string
	TaskID                  modulecore.TaskID
	RunID                   modulecore.RunID
	KindInputSHA256         string
	ResultSHA256            string
	CriteriaSHA256          string
}

// TaskPlanExecutionResultAcceptance is an explicit creator decision. The
// caller supplies the exact immutable proof tuple already projected by Goal.
type TaskPlanExecutionResultAcceptance struct {
	GoalID                  string
	ExpectedRevision        uint64
	ExpectedAssignmentEpoch uint64
	IdempotencyKey          string
	ArtifactID              string
	TaskID                  modulecore.TaskID
	RunID                   modulecore.RunID
	KindInputSHA256         string
	ResultSHA256            string
	CriteriaSHA256          string
}

type TaskPlanSubmission struct {
	WorkstreamID   string `json:"workstream_id,omitempty"`
	Goal           Goal   `json:"goal"`
	IdempotencyKey string `json:"idempotency_key"`
}

type TaskPlanClaim struct {
	GoalID                 string `json:"goal_id"`
	ExpectedRevision       uint64 `json:"expected_revision"`
	ExpectedPolicyRevision string `json:"expected_policy_revision"`
	IdempotencyKey         string `json:"idempotency_key"`
}

type TaskPlanRelease struct {
	GoalID                 string `json:"goal_id"`
	ExpectedRevision       uint64 `json:"expected_revision"`
	ExpectedPolicyRevision string `json:"expected_policy_revision"`
	IdempotencyKey         string `json:"idempotency_key"`
}

type TaskPlanQuery struct {
	View     string `json:"view"`
	Assignee string `json:"assignee,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit"`
}

type TaskPlanMutationResult struct {
	Goal       Goal   `json:"goal"`
	Replayed   bool   `json:"replayed"`
	ReceiptRef string `json:"receipt_ref"`
}

type TaskPlanOperationReceipt struct {
	Version             int    `json:"version"`
	ActorKind           string `json:"actor_kind"`
	ActorID             string `json:"actor_id"`
	AuthenticatedUserID string `json:"authenticated_user_id,omitempty"`
	DataScope           string `json:"data_scope"`
	UserID              string `json:"user_id,omitempty"`
	Operation           string `json:"operation"`
	IdempotencyKey      string `json:"idempotency_key"`
	InputHash           string `json:"input_hash"`
	Result              Goal   `json:"result"`
}

func (plan TaskPlan) Validate() error {
	if err := (TaskCenterWorkstreamScope{DataScope: plan.DataScope, UserID: plan.UserID}).Validate(); err != nil {
		return err
	}
	if !validIdentity(plan.CreatedBy) {
		return errors.New("task plan created_by is required")
	}
	if plan.CreatorKind != "" && plan.CreatorKind != "user" && plan.CreatorKind != "agent" {
		return errors.New("task plan creator_kind must be user or agent")
	}
	if plan.Assignee != TaskPlanAssigneeUnassigned && !canonicalOwnerID(plan.Assignee) {
		return errors.New("task plan assignee must be unassigned or a canonical agent ID")
	}
	if !canonicalOwnerID(plan.KindID) {
		return errors.New("task plan kind_id is required and must be canonical")
	}
	if len(plan.KindInput) > 8<<10 || (len(plan.KindInput) > 0 && !json.Valid(plan.KindInput)) {
		return errors.New("task plan kind_input must be valid JSON")
	}
	if (len(plan.KindInput) == 0) != (plan.KindInputVersion == 0) || plan.KindInputVersion < 0 {
		return errors.New("task plan kind_input and schema version must be provided together")
	}
	if plan.Revision == 0 {
		return errors.New("task plan revision must be positive")
	}
	if len(plan.PolicyRevision) != 64 {
		return errors.New("task plan policy_revision must be a SHA-256 hash")
	}
	for _, character := range plan.PolicyRevision {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return errors.New("task plan policy_revision must be lowercase hexadecimal")
		}
	}
	if plan.SubsetAllowedAgents != nil {
		if len(*plan.SubsetAllowedAgents) == 0 {
			return errors.New("task plan explicit agent subset must not be empty")
		}
		seen := make(map[string]struct{}, len(*plan.SubsetAllowedAgents))
		for _, agentID := range *plan.SubsetAllowedAgents {
			if !canonicalOwnerID(agentID) {
				return errors.New("task plan explicit agent subset contains an invalid agent ID")
			}
			if _, exists := seen[agentID]; exists {
				return fmt.Errorf("task plan explicit agent subset repeats %q", agentID)
			}
			seen[agentID] = struct{}{}
		}
	}
	switch plan.HandoffState {
	case TaskPlanHandoffNone:
		if plan.HandoffKey != "" || plan.HandoffInput != nil || plan.TaskID != "" || plan.RunID != "" {
			return errors.New("task plan with no handoff must not have handoff or execution references")
		}
	case TaskPlanHandoffPrepared, TaskPlanHandoffOutcomeUnknown:
		if plan.HandoffKey == "" {
			return errors.New("pending task plan handoff requires a handoff_key")
		}
	case TaskPlanHandoffLinked:
		if plan.HandoffKey == "" || plan.TaskID == "" {
			return errors.New("linked task plan handoff requires handoff_key and task_id")
		}
	default:
		return fmt.Errorf("unsupported task plan handoff_state %q", plan.HandoffState)
	}
	if plan.HandoffState != TaskPlanHandoffNone && plan.HandoffInput != nil && plan.HandoffInput.AssignmentEpoch != plan.AssignmentEpoch {
		return errors.New("task plan handoff assignment epoch changed after preparation")
	}
	if plan.HandoffInput != nil {
		if err := plan.HandoffInput.Validate(plan); err != nil {
			return fmt.Errorf("task plan frozen handoff input: %w", err)
		}
	}
	if plan.TaskID != "" {
		if err := plan.TaskID.Validate(); err != nil {
			return fmt.Errorf("task plan task_id: %w", err)
		}
	}
	if plan.RunID != "" {
		if err := plan.RunID.Validate(); err != nil {
			return fmt.Errorf("task plan run_id: %w", err)
		}
		if plan.TaskID == "" {
			return errors.New("task plan run_id requires task_id")
		}
	}
	if plan.ExecutionResult != nil {
		result := plan.ExecutionResult
		expectedArtifactID, idErr := domaintask.TaskCenterResultArtifactID(plan.TaskID, plan.RunID)
		if idErr != nil || result.ArtifactID != expectedArtifactID || !isLowerSHA256(result.KindInputSHA256) || !isLowerSHA256(result.ResultSHA256) || !isLowerSHA256(result.CriteriaSHA256) || plan.HandoffState != TaskPlanHandoffLinked || plan.HandoffInput == nil {
			return errors.New("execution result reference is invalid or detached from its linked Task")
		}
		digest, hashErr := domaintask.TaskCenterKindInputSHA256(plan.HandoffInput.KindInput)
		if hashErr != nil || result.KindInputSHA256 != digest {
			return errors.New("execution result kind-input digest does not match the frozen Goal input")
		}
		switch result.Verification {
		case TaskPlanResultVerificationPending:
			if result.AcceptedBy != "" || !result.AcceptedAt.IsZero() {
				return errors.New("pending execution result must not carry acceptance proof")
			}
		case TaskPlanResultVerificationAccepted:
			if result.AcceptedBy == "" || result.AcceptedBy != plan.CreatedBy || plan.CreatorKind != "user" || result.AcceptedAt.IsZero() || (plan.DataScope == TaskPlanDataScopeUser && plan.UserID != result.AcceptedBy) {
				return errors.New("accepted execution result requires its Goal creator proof")
			}
		default:
			return errors.New("execution result verification state is invalid")
		}
	}
	if plan.UpdatedAt.IsZero() {
		return errors.New("task plan updated_at is required")
	}
	if err := plan.Timing.Validate(); err != nil {
		return fmt.Errorf("task plan timing: %w", err)
	}
	return nil
}

const (
	TaskPlanResultVerificationPending  = "verification_pending"
	TaskPlanResultVerificationAccepted = "accepted"
)

func TaskPlanCriteriaSHA256(successCriteria, verification []string) (string, error) {
	value := struct {
		SuccessCriteria []string `json:"success_criteria"`
		Verification    []string `json:"verification"`
	}{append([]string(nil), successCriteria...), append([]string(nil), verification...)}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func (input TaskPlanHandoffInput) Validate(plan TaskPlan) error {
	if input.Version != 1 || input.Revision == 0 || input.Revision > plan.Revision || input.AssignmentEpoch != plan.AssignmentEpoch || input.KindID != plan.KindID || input.KindInputVersion != plan.KindInputVersion || !bytes.Equal(input.KindInput, plan.KindInput) {
		return errors.New("frozen input does not match current Goal assignment or kind")
	}
	if len(input.TaskDraft) == 0 || len(input.TaskDraft) > 64<<10 || len(input.SharedContext) == 0 || len(input.SharedContext) > 64<<10 || len(input.KindInput) == 0 || input.KindInputVersion == 0 || !json.Valid(input.TaskDraft) || !json.Valid(input.SharedContext) || !json.Valid(input.KindInput) || len(input.InputHash) != 64 {
		return errors.New("frozen input requires exact task draft, shared context, typed input and SHA-256")
	}
	if ComputeTaskPlanHandoffInputHash(input.TaskDraft, input.SharedContext, input.KindInput) != input.InputHash {
		return errors.New("frozen input hash does not match its payload")
	}
	for _, character := range input.InputHash {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return errors.New("frozen input hash must be lowercase hexadecimal")
		}
	}
	return nil
}

// ComputeTaskPlanHandoffInputHash binds the exact JSON bytes that are replayed
// to TaskManager, including the kind-specific typed operation payload.
func ComputeTaskPlanHandoffInputHash(taskDraft, sharedContext []byte, kindInput json.RawMessage) string {
	hash := sha256.New()
	for _, value := range [][]byte{taskDraft, sharedContext, kindInput} {
		var length [8]byte
		n := uint64(len(value))
		for index := len(length) - 1; index >= 0; index-- {
			length[index] = byte(n)
			n >>= 8
		}
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func canonicalOwnerID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && strings.ToLower(value) == value
}

func validIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}
