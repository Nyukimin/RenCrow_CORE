package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrTypedTaskRequiresOwner                          = errors.New("typed Task requires its registered kind owner")
	ErrTaskCenterExecutionImmutable                    = errors.New("TaskCenter execution provenance is immutable after the first Task save")
	ErrTaskCenterExecutionReceiptAuthorizationRequired = errors.New("TaskCenter receipt read requires exact source authorization")
	ErrTaskCenterExecutionReceiptNotFound              = errors.New("TaskCenter execution receipt not found")
	ErrTaskCenterExecutionReceiptForbidden             = errors.New("TaskCenter execution receipt read is forbidden")
	ErrTaskCenterExecutionReceiptUnsupported           = errors.New("Task store does not support authorized TaskCenter receipts")
	ErrTaskCenterExecutionReceiptConflict              = errors.New("TaskCenter execution receipt does not match the frozen handoff")
	ErrTaskCenterExecutionReceiptCorrupt               = errors.New("TaskCenter execution receipt is corrupt")
)

// TaskCenterExecutionAdmissionCheck is the source-owner authorization gate
// for creating a frozen TaskCenter Task and its first Run.
type TaskCenterExecutionAdmissionCheck func(context.Context, Task, SharedRoleContext, json.RawMessage) error

// TaskCenterExecutionInputAdmissionCheck authorizes one closed typed effect
// while the Task owner holds its execution fence.
type TaskCenterExecutionInputAdmissionCheck func(context.Context, Task, Run, SharedRoleContext) (json.RawMessage, error)

// TaskCenterKindEffect is the registered kind owner's bounded typed effect.
type TaskCenterKindEffect func(context.Context, json.RawMessage) (json.RawMessage, error)

// TaskCenterExecutionResultAuthorizationCheck lets the Workstream result
// owner verify the source Goal scope before returning or writing an artifact.
type TaskCenterExecutionResultAuthorizationCheck func(context.Context, TaskCenterResultReference) error

type TaskCenterExecutionAuthorizationCheck func(context.Context, Task, Run, SharedRoleContext) error

type TaskCenterExecutionFinishAuthorizationCheck func(context.Context, Task, Run, SharedRoleContext, TaskCenterResultReference) error

const (
	TaskCenterResultScopePublic = "public"
	TaskCenterResultScopeUser   = "user"
)

// TaskCenterResultReference points to the sole canonical Workstream Artifact
// containing a typed result. The Task owner receipt stores this tuple only.
type TaskCenterResultReference struct {
	ArtifactID      string                  `json:"artifact_id"`
	SourceGoalID    modulecore.GoalID       `json:"source_goal_id"`
	WorkstreamID    modulecore.WorkstreamID `json:"workstream_id"`
	TaskID          modulecore.TaskID       `json:"task_id"`
	RunID           modulecore.RunID        `json:"run_id"`
	KindID          string                  `json:"kind_id"`
	KindInputSHA256 string                  `json:"kind_input_sha256"`
	ResultSHA256    string                  `json:"result_sha256"`
	DataScope       string                  `json:"data_scope"`
	UserID          string                  `json:"user_id,omitempty"`
}

func (reference TaskCenterResultReference) Validate(task Task, run Run) error {
	expectedArtifactID, idErr := TaskCenterResultArtifactID(reference.TaskID, reference.RunID)
	if idErr != nil || reference.ArtifactID != expectedArtifactID || reference.SourceGoalID.Validate() != nil || reference.WorkstreamID.Validate() != nil ||
		reference.TaskID.Validate() != nil || reference.RunID.Validate() != nil || !validSHA256(reference.KindInputSHA256) || !validSHA256(reference.ResultSHA256) {
		return ErrTaskCenterExecutionReceiptConflict
	}
	if task.TaskCenterExecution == nil || reference.SourceGoalID != task.GoalID || reference.WorkstreamID != task.WorkstreamID ||
		reference.TaskID != task.TaskID || reference.RunID != run.RunID || run.TaskID != task.TaskID ||
		reference.KindID != task.TaskCenterExecution.KindID || reference.KindInputSHA256 != task.TaskCenterExecution.KindInputSHA256 {
		return ErrTaskCenterExecutionReceiptConflict
	}
	if task.PrivateAccess == nil {
		if reference.DataScope != TaskCenterResultScopePublic || reference.UserID != "" {
			return ErrTaskCenterExecutionReceiptConflict
		}
		return nil
	}
	if task.PrivateAccess.Validate() != nil || reference.DataScope != TaskCenterResultScopeUser || reference.UserID != task.PrivateAccess.AuthenticatedUserID {
		return ErrTaskCenterExecutionReceiptConflict
	}
	return nil
}

// TaskCenterResultArtifactID returns the stable canonical artifact key for a
// typed Task/Run result, so retries address the same Workstream artifact.
func TaskCenterResultArtifactID(taskID modulecore.TaskID, runID modulecore.RunID) (string, error) {
	if err := taskID.Validate(); err != nil {
		return "", fmt.Errorf("task id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return "", fmt.Errorf("run id is invalid: %w", err)
	}
	return "task_execution_result:" + string(taskID) + ":" + string(runID), nil
}

type TaskCenterExecutionWriter interface {
	SaveTaskCenterExecutionState(context.Context, Task, Run, *SharedRoleContext, TaskCenterExecutionAuthorizationCheck) error
}

type TaskCenterExecutionFenceCallback func(context.Context, Task, Run, SharedRoleContext) error

type TaskCenterExecutionFencer interface {
	WithTaskCenterExecutionFence(context.Context, modulecore.TaskID, modulecore.RunID, TaskCenterExecutionFenceCallback) error
}

// TaskCenterExecutionFenceRecovery is an owner-only path for closing an old
// active effect capability after restart. The authorization callback must
// prove the exact already-persisted result and current Goal source before the
// capability can be released; it never admits or repeats an effect.
type TaskCenterExecutionFenceRecovery interface {
	RecoverTaskCenterExecutionFence(context.Context, modulecore.TaskID, modulecore.RunID, TaskCenterResultReference, TaskCenterExecutionFinishAuthorizationCheck) error
}

type TaskCenterExecutionReceiptReadInput struct {
	OperationID      string
	RequestHash      string
	HandoffKey       modulecore.RequestID
	PreparedRevision uint64
	Task             Task
	Shared           SharedRoleContext
	KindInput        json.RawMessage
}

type TaskCenterExecutionReceiptReadAuthorizationCheck func(context.Context, TaskCenterExecutionReceiptReadInput) error

type TaskCenterExecutionReceiptReader interface {
	LookupTaskCenterExecutionReceipt(context.Context, TaskCenterExecutionReceiptReadInput, TaskCenterExecutionReceiptReadAuthorizationCheck) (Task, Run, error)
}

type TaskCenterExecutionStateReader interface {
	GetTaskCenterExecutionState(context.Context, modulecore.TaskID, modulecore.RunID, TaskCenterExecutionAuthorizationCheck) (Task, Run, error)
}

type TaskCenterExecutionResultReceiptReadInput struct {
	OperationID     string
	RequestHash     string
	TaskID          modulecore.TaskID
	RunID           modulecore.RunID
	ResultReference TaskCenterResultReference
	ExpectedTask    Task
}

type TaskCenterExecutionResultReceiptReader interface {
	LookupTaskCenterExecutionResultReceipt(context.Context, TaskCenterExecutionResultReceiptReadInput, TaskCenterExecutionFinishAuthorizationCheck) (Task, Run, TaskCenterResultReference, error)
}

func NewTaskCenterExecutionReceiptReadInput(handoffKey modulecore.RequestID, preparedRevision uint64, task Task, shared SharedRoleContext, kindInput json.RawMessage) (TaskCenterExecutionReceiptReadInput, error) {
	operationID, requestHash, err := BuildTaskCenterExecutionOperationIdentity(handoffKey, preparedRevision, task, shared, kindInput)
	if err != nil {
		return TaskCenterExecutionReceiptReadInput{}, ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	return TaskCenterExecutionReceiptReadInput{
		OperationID: operationID, RequestHash: requestHash,
		HandoffKey: handoffKey, PreparedRevision: preparedRevision,
		Task: task, Shared: shared, KindInput: append(json.RawMessage(nil), kindInput...),
	}, nil
}

// TaskCenterExecution is the immutable reference from a native Task to the
// exact typed input and timing snapshot persisted by its source Goal.
type TaskCenterExecution struct {
	KindID           string            `json:"kind_id"`
	KindInputVersion int               `json:"kind_input_version"`
	KindInputSHA256  string            `json:"kind_input_sha256"`
	SourceGoalID     modulecore.GoalID `json:"source_goal_id"`
	PreparedRevision uint64            `json:"prepared_revision"`
	AssignmentEpoch  uint64            `json:"assignment_epoch"`
	TimingSHA256     string            `json:"timing_sha256"`
}

func (execution TaskCenterExecution) Validate(task Task) error {
	if strings.TrimSpace(execution.KindID) == "" || strings.TrimSpace(execution.KindID) != execution.KindID || strings.ToLower(execution.KindID) != execution.KindID {
		return errors.New("kind_id must be a canonical non-empty identifier")
	}
	if execution.KindInputVersion <= 0 || execution.PreparedRevision == 0 {
		return errors.New("kind input version and prepared revision must be positive")
	}
	if task.TaskMeetingInvocation != nil || task.NativePlanningInvocation != nil {
		return errors.New("TaskCenter execution Task cannot use another native owner")
	}
	if !validSHA256(execution.KindInputSHA256) || !validSHA256(execution.TimingSHA256) {
		return errors.New("kind input and timing digests must be lowercase SHA-256 values")
	}
	if err := execution.SourceGoalID.Validate(); err != nil {
		return fmt.Errorf("source_goal_id is invalid: %w", err)
	}
	if task.GoalID == "" || execution.SourceGoalID != task.GoalID {
		return errors.New("source_goal_id must equal Task.goal_id")
	}
	if task.WorkstreamID == "" {
		return errors.New("typed Task requires workstream_id")
	}
	if task.Timing == nil {
		return errors.New("typed Task requires an explicit timing snapshot")
	}
	timingDigest, err := TaskTimingSHA256(*task.Timing)
	if err != nil {
		return fmt.Errorf("encode timing snapshot: %w", err)
	}
	if timingDigest != execution.TimingSHA256 {
		return errors.New("timing digest does not match Task timing")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// TaskTimingSHA256 hashes the canonical JSON representation of a timing
// snapshot. It does not apply defaults or normalize the supplied value.
func TaskTimingSHA256(timing modulecore.TaskTiming) (string, error) {
	encoded, err := json.Marshal(timing)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// TaskCenterKindInputSHA256 hashes exact canonical JSON bytes supplied by the
// Workstream owner; callers must not decode and re-encode arbitrary input.
func TaskCenterKindInputSHA256(input json.RawMessage) (string, error) {
	if len(input) == 0 || !json.Valid(input) {
		return "", errors.New("kind input must be non-empty valid JSON")
	}
	digest := sha256.Sum256(input)
	return hex.EncodeToString(digest[:]), nil
}

// BuildTaskCenterExecutionOperationIdentity is the one operation identity
// derivation shared by Ensure and receipt reconciliation. draft and shared
// are marshaled as their frozen canonical JSON representations; kindInput is
// hashed as exact canonical JSON bytes.
func BuildTaskCenterExecutionOperationIdentity(
	handoffKey modulecore.RequestID,
	preparedRevision uint64,
	draft Task,
	shared SharedRoleContext,
	kindInput json.RawMessage,
) (string, string, error) {
	if err := handoffKey.Validate(); err != nil {
		return "", "", fmt.Errorf("handoff key is invalid: %w", err)
	}
	if preparedRevision == 0 || !IsTaskCenterTyped(draft) || draft.TaskCenterExecution.PreparedRevision != preparedRevision {
		return "", "", errors.New("prepared revision does not match typed Task")
	}
	if err := validateTaskCenterDraft(draft); err != nil {
		return "", "", fmt.Errorf("typed Task is invalid: %w", err)
	}
	if shared.TaskID != "" && shared.TaskID != draft.TaskID || draft.TaskID != "" && shared.TaskID != draft.TaskID {
		return "", "", errors.New("shared context TaskID does not match typed Task")
	}
	inputDigest, err := TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		return "", "", err
	}
	if inputDigest != draft.TaskCenterExecution.KindInputSHA256 {
		return "", "", errors.New("kind input does not match typed Task digest")
	}
	taskBytes, err := json.Marshal(draft)
	if err != nil {
		return "", "", err
	}
	sharedBytes, err := json.Marshal(shared)
	if err != nil {
		return "", "", err
	}
	requestHash := sha256.New()
	for _, value := range [][]byte{taskBytes, sharedBytes, kindInput} {
		var length [8]byte
		n := uint64(len(value))
		for index := len(length) - 1; index >= 0; index-- {
			length[index] = byte(n)
			n >>= 8
		}
		_, _ = requestHash.Write(length[:])
		_, _ = requestHash.Write(value)
	}
	operationID := fmt.Sprintf("taskcenter:%s:%d:start", handoffKey, preparedRevision)
	return operationID, hex.EncodeToString(requestHash.Sum(nil)), nil
}

func validateTaskCenterDraft(draft Task) error {
	if !IsTaskCenterTyped(draft) || draft.TaskCenterExecution.Validate(draft) != nil {
		return ErrTypedTaskRequiresOwner
	}
	if draft.TaskID != "" || !draft.CreatedAt.IsZero() || !draft.UpdatedAt.IsZero() || draft.StartedAt != nil || draft.FinishedAt != nil ||
		(draft.Status != "" && draft.Status != StatusQueued) {
		return errors.New("frozen TaskCenter draft must leave Task identity and lifecycle timestamps to TaskManager")
	}
	if draft.Summary != "" || draft.WaitingReason != "" || len(draft.NextActions) != 0 || len(draft.Evidence) != 0 || len(draft.Artifacts) != 0 {
		return errors.New("frozen TaskCenter draft must not contain lifecycle output")
	}
	validation := draft
	if validation.TaskID == "" {
		validation.TaskID = modulecore.NewTaskID()
	}
	validation.ApplyDefaults(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	return validation.Validate()
}

// TaskCenterDraftMatchesStored compares a frozen, pre-creation request with
// its stored Task while ignoring fields the Task owner generates or changes as
// lifecycle bookkeeping.
func TaskCenterDraftMatchesStored(expected, stored Task) bool {
	if expected.TaskID != "" && expected.TaskID != stored.TaskID {
		return false
	}
	expected.ApplyDefaults(stored.CreatedAt)
	expected.TaskID = stored.TaskID
	expected.CreatedAt, expected.UpdatedAt = stored.CreatedAt, stored.UpdatedAt
	expected.StartedAt, expected.FinishedAt = stored.StartedAt, stored.FinishedAt
	expected.Status, expected.Summary, expected.WaitingReason = stored.Status, stored.Summary, stored.WaitingReason
	expected.Activity = cloneActivityMetadata(stored.Activity)
	return reflect.DeepEqual(expected, stored)
}

func IsTaskCenterTyped(task Task) bool { return task.TaskCenterExecution != nil }

// ValidateStartTiming applies explicit schedule conditions without inventing
// defaults for legacy Tasks whose timing snapshot is nil.
func (task Task) ValidateStartTiming(now time.Time) error {
	if task.Timing == nil {
		return nil
	}
	condition := task.Timing.CompletionCondition
	if task.Route == RouteGAME && condition != nil && condition.Kind == "count" && condition.TargetCount == 1 && condition.CountUnit == "game" {
		if !task.Timing.ScheduleReady(now) {
			return errors.New("Task timing does not admit a start at the current time")
		}
		return nil
	}
	if !task.Timing.Ready(now) {
		return errors.New("Task timing does not admit a start at the current time")
	}
	return nil
}
