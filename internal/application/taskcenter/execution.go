package taskcenter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ExecutionInput struct {
	GoalID                  string            `json:"goal_id"`
	ExpectedRevision        uint64            `json:"expected_revision"`
	ExpectedAssignmentEpoch uint64            `json:"expected_assignment_epoch"`
	ExpectedPolicyRevision  string            `json:"expected_policy_revision"`
	IdempotencyKey          string            `json:"idempotency_key"`
	CallerTaskID            modulecore.TaskID `json:"-"`
	CallerRunID             modulecore.RunID  `json:"-"`
}

type ExecutionResult struct {
	GoalID          string                               `json:"goal_id"`
	TaskID          modulecore.TaskID                    `json:"task_id"`
	RunID           modulecore.RunID                     `json:"run_id"`
	ResultReference domaintask.TaskCenterResultReference `json:"result_reference"`
	Verification    string                               `json:"verification"`
}

type TaskCenterExecutionCoordinator interface {
	ExecuteTaskCenterExecution(context.Context, ExecutionInput) (ExecutionResult, error)
}

type TaskCenterNativeExecutionOwner interface {
	EnsureTaskCenterExecution(context.Context, modulecore.RequestID, uint64, domaintask.Task, domaintask.SharedRoleContext, json.RawMessage, domaintask.TaskCenterExecutionAdmissionCheck, domaintask.TaskCenterExecutionReceiptReadAuthorizationCheck) (domaintask.Task, domaintask.Run, error)
	FindTaskCenterExecutionReceipt(context.Context, modulecore.RequestID, uint64, domaintask.Task, domaintask.SharedRoleContext, json.RawMessage, domaintask.TaskCenterExecutionReceiptReadAuthorizationCheck) (domaintask.Task, domaintask.Run, error)
	ExecuteTaskCenterKind(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.TaskCenterExecutionInputAdmissionCheck, domaintask.TaskCenterKindEffect) (json.RawMessage, error)
	FinishTaskCenterExecution(context.Context, modulecore.TaskID, modulecore.RunID, domaintask.Task, string, domaintask.Status, string, domaintask.TaskCenterResultReference, domaintask.TaskCenterExecutionFinishAuthorizationCheck) (domaintask.Task, domaintask.Run, domaintask.TaskCenterResultReference, error)
	FindTaskCenterExecutionResultReceipt(context.Context, modulecore.TaskID, modulecore.RunID, domaintask.Task, string, string, domaintask.TaskCenterResultReference, domaintask.TaskCenterExecutionFinishAuthorizationCheck) (domaintask.Task, domaintask.Run, domaintask.TaskCenterResultReference, error)
}

type TaskCenterExecutionResultOwner interface {
	SaveTaskCenterExecutionResult(context.Context, domaintask.TaskCenterResultReference, json.RawMessage, domaintask.TaskCenterExecutionResultAuthorizationCheck) error
	GetTaskCenterExecutionResult(context.Context, domaintask.TaskCenterResultReference, domaintask.TaskCenterExecutionResultAuthorizationCheck) (domaintask.TaskCenterResultReference, json.RawMessage, error)
}

type TaskCenterMovieLookupOwner interface {
	Lookup(context.Context, string, string, string, int) (any, error)
}

type NativeExecution struct {
	Goals   TaskPlanHandoffStore
	Tasks   TaskCenterNativeExecutionOwner
	Results TaskCenterExecutionResultOwner
	Movies  TaskCenterMovieLookupOwner
	Managed *Service
}

var errTaskCenterResultAlreadyPersisted = errors.New("TaskCenter result artifact already persisted")

func (s *Service) WithNativeExecution(tasks TaskCenterNativeExecutionOwner, results TaskCenterExecutionResultOwner, movies TaskCenterMovieLookupOwner) *Service {
	if s == nil {
		return nil
	}
	coordinator := &NativeExecution{Goals: nil, Tasks: tasks, Results: results, Movies: movies, Managed: s}
	s.nativeExecution = coordinator
	s.receiptVerifier = coordinator
	return s
}

func (n *NativeExecution) VerifyTaskCenterHandoffReceipt(ctx context.Context, goal domainworkstream.Goal, taskID modulecore.TaskID, runID modulecore.RunID, authorize HandoffReceiptAuthorizationCheck) error {
	if n == nil || n.Tasks == nil || authorize == nil || goal.TaskPlan == nil || goal.TaskPlan.HandoffInput == nil {
		return domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	input := goal.TaskPlan.HandoffInput
	var draft domaintask.Task
	if err := decodeSingleStrictJSON(input.TaskDraft, &draft); err != nil {
		return domaintask.ErrTaskCenterExecutionReceiptCorrupt
	}
	var shared domaintask.SharedRoleContext
	if err := decodeSingleStrictJSON(input.SharedContext, &shared); err != nil {
		return domaintask.ErrTaskCenterExecutionReceiptCorrupt
	}
	key := modulecore.RequestID(goal.TaskPlan.HandoffKey)
	read := func(checkCtx context.Context, _ domaintask.TaskCenterExecutionReceiptReadInput) error {
		return authorize(checkCtx)
	}
	_, run, err := n.Tasks.FindTaskCenterExecutionReceipt(ctx, key, input.Revision, draft, shared, input.KindInput, read)
	if err != nil {
		return err
	}
	if run.RunID != runID || run.TaskID != taskID {
		return domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	return nil
}

func (s *Service) ExecuteTaskCenterExecution(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	if s == nil || s.nativeExecution == nil {
		return ExecutionResult{}, ErrUnsupported
	}
	return s.nativeExecution.ExecuteTaskCenterExecution(ctx, input)
}

// ReadResult exposes the canonical result body only within the visible Goal
// scope. It proves the successful Task/Run receipt and exact scoped Artifact
// on every read; this read authority does not grant acceptance authority.
func (s *Service) ReadResult(ctx context.Context, goalID string) (ResultView, error) {
	if s == nil || s.nativeExecution == nil || strings.TrimSpace(goalID) == "" {
		return ResultView{}, ErrUnsupported
	}
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return ResultView{}, err
	}
	goal, err := s.store.GetTaskPlan(ctx, scope, goalID)
	if err != nil {
		return ResultView{}, mapOwnerError(err)
	}
	if s.authorizeVisibleGoal(scope, goal) != nil || !canReadTaskPlanResult(goal) || goal.TaskPlan.ExecutionResult == nil {
		return ResultView{}, ErrNotFound
	}
	reference, err := resultReferenceFromGoal(goal)
	if err != nil {
		return ResultView{}, err
	}
	plan := goal.TaskPlan
	expectedTask := referenceTaskFromFrozen(goal)
	expectedRun := domaintask.Run{TaskID: plan.TaskID, RunID: plan.RunID}
	task, run, err := s.nativeExecution.verifySuccessfulResult(ctx, scope, goal, expectedTask, expectedRun, reference)
	if err != nil {
		return ResultView{}, err
	}
	storedReference, result, err := s.nativeExecution.Results.GetTaskCenterExecutionResult(ctx, reference, func(checkCtx context.Context, candidate domaintask.TaskCenterResultReference) error {
		return s.nativeExecution.authorizeResultScope(checkCtx, scope, goal.GoalID, candidate, task.TaskID, run.RunID)
	})
	if err != nil {
		return ResultView{}, mapHandoffReceiptError(err)
	}
	if storedReference != reference {
		return ResultView{}, ErrConflict
	}
	digest := sha256.Sum256(result)
	if hex.EncodeToString(digest[:]) != reference.ResultSHA256 {
		return ResultView{}, ErrUnavailable
	}
	return ResultView{GoalID: goal.GoalID, Revision: plan.Revision, AssignmentEpoch: plan.AssignmentEpoch, ResultArtifactID: reference.ArtifactID, ResultSHA256: reference.ResultSHA256, ExecutionVerification: plan.ExecutionResult.Verification, Result: append(json.RawMessage(nil), result...)}, nil
}

func canReadTaskPlanResult(goal domainworkstream.Goal) bool {
	plan := goal.TaskPlan
	return plan != nil && plan.HandoffState == domainworkstream.TaskPlanHandoffLinked
}

// ExecuteTaskCenterExecution recovers the frozen Task receipt, links it to
// the source Goal, invokes only its registered native kind owner, stores the
// canonical scoped Artifact, then closes the native Task/Run receipt. Goal
// completion remains an explicit verification decision.
func (n *NativeExecution) ExecuteTaskCenterExecution(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	if n == nil || n.Managed == nil || n.Tasks == nil || n.Results == nil || ctx == nil {
		return ExecutionResult{}, ErrUnsupported
	}
	s := n.Managed
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return ExecutionResult{}, err
	}
	identity, identityErr := domainexecution.IdentityFromContext(ctx)
	if identityErr != nil || identity.TaskID != input.CallerTaskID || identity.RunID != input.CallerRunID ||
		scope.ActorKind != tool.ActorKindAgent || input.CallerTaskID.Validate() != nil || input.CallerRunID.Validate() != nil ||
		input.ExpectedRevision == 0 || input.ExpectedRevision == ^uint64(0) || input.ExpectedPolicyRevision == "" || !validIdempotencyKey(input.IdempotencyKey) {
		return ExecutionResult{}, fmt.Errorf("%w: native self execution requires an Agent and exact Goal revision", ErrForbidden)
	}
	if replay, found, err := n.recoverExecutionResultReceipt(ctx, scope, input); err != nil {
		return ExecutionResult{}, err
	} else if found {
		return replay, nil
	}
	prepareKey := taskCenterPrepareIdempotencyKey(input.IdempotencyKey)
	prepareReceipt, preparedByThisCall, err := s.store.TaskPlanReceipt(ctx, scope, "handoff_prepare", prepareKey)
	if err != nil {
		return ExecutionResult{}, mapOwnerError(err)
	}
	goal, err := s.store.GetTaskPlan(ctx, scope, input.GoalID)
	if err != nil {
		return ExecutionResult{}, mapOwnerError(err)
	}
	if err := s.authorizeVisibleGoal(scope, goal); err != nil {
		return ExecutionResult{}, err
	}
	plan := goal.TaskPlan
	if plan == nil || plan.Assignee != scope.ActorID || plan.AssignmentEpoch != input.ExpectedAssignmentEpoch || !domainworkstream.GoalStatusOpen(goal.Status) {
		return ExecutionResult{}, ErrConflict
	}
	if preparedByThisCall {
		prepared := prepareReceipt.Goal
		if prepared.GoalID != input.GoalID || prepared.TaskPlan == nil || prepared.TaskPlan.HandoffInput == nil ||
			prepared.TaskPlan.HandoffInput.Revision != input.ExpectedRevision+1 || prepared.TaskPlan.AssignmentEpoch != input.ExpectedAssignmentEpoch ||
			prepared.TaskPlan.PolicyRevision != input.ExpectedPolicyRevision || plan.HandoffInput == nil ||
			plan.HandoffInput.InputHash != prepared.TaskPlan.HandoffInput.InputHash || plan.HandoffKey != prepared.TaskPlan.HandoffKey {
			return ExecutionResult{}, ErrConflict
		}
	} else if plan.Revision != input.ExpectedRevision {
		return ExecutionResult{}, ErrConflict
	}
	if plan.HandoffInput == nil {
		if plan.HandoffState != domainworkstream.TaskPlanHandoffNone || plan.KindID != domainworkstream.TaskPlanKindMovieCatalogLookup {
			return ExecutionResult{}, ErrUnsupported
		}
		if err := s.checkPolicyRevision(input.ExpectedPolicyRevision); err != nil || plan.PolicyRevision != input.ExpectedPolicyRevision {
			return ExecutionResult{}, ErrForbidden
		}
		allowed, allowErr := s.allowed(plan.KindID, scope.ActorID, plan.SubsetAllowedAgents)
		if allowErr != nil || !allowed {
			return ExecutionResult{}, ErrForbidden
		}
		draft := domaintask.Task{
			Title: goal.Title, OwnerID: plan.Assignee, Assignee: plan.Assignee, ReadOnly: true,
			WorkstreamID: modulecore.WorkstreamID(goal.WorkstreamID), GoalID: modulecore.GoalID(goal.GoalID),
		}
		if plan.DataScope == domainworkstream.TaskPlanDataScopeUser {
			draft.PrivateAccess = &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: plan.UserID}
		}
		draftJSON, err := json.Marshal(draft)
		if err != nil {
			return ExecutionResult{}, ErrInvalid
		}
		sharedJSON, err := json.Marshal(domaintask.SharedRoleContext{})
		if err != nil {
			return ExecutionResult{}, ErrInvalid
		}
		prepared, prepareErr := s.PrepareHandoff(ctx, HandoffPrepareInput{
			GoalID: goal.GoalID, ExpectedRevision: plan.Revision, ExpectedAssignmentEpoch: plan.AssignmentEpoch,
			ExpectedPolicyRevision: input.ExpectedPolicyRevision, IdempotencyKey: prepareKey,
			HandoffKey: taskexecution.HandoffKey(modulecore.NewRequestID()), TaskDraft: draftJSON, SharedContext: sharedJSON,
		})
		if prepareErr != nil {
			prepareReceipt, preparedByThisCall, err = s.store.TaskPlanReceipt(ctx, scope, "handoff_prepare", prepareKey)
			if err != nil || !preparedByThisCall || prepareReceipt.Goal.GoalID != goal.GoalID || prepareReceipt.Goal.TaskPlan == nil || prepareReceipt.Goal.TaskPlan.HandoffInput == nil ||
				prepareReceipt.Goal.TaskPlan.HandoffInput.Revision != input.ExpectedRevision+1 || prepareReceipt.Goal.TaskPlan.AssignmentEpoch != input.ExpectedAssignmentEpoch {
				return ExecutionResult{}, prepareErr
			}
			goal = prepareReceipt.Goal
		} else {
			goal, err = s.store.GetTaskPlan(ctx, scope, prepared.Item.GoalID)
			if err != nil {
				return ExecutionResult{}, mapOwnerError(err)
			}
		}
		plan = goal.TaskPlan
	}
	if plan == nil || plan.HandoffInput == nil || plan.HandoffKey == "" || plan.HandoffState == domainworkstream.TaskPlanHandoffNone || plan.HandoffInput.Validate(*plan) != nil {
		return ExecutionResult{}, ErrConflict
	}
	if plan.KindID != domainworkstream.TaskPlanKindMovieCatalogLookup || !s.executorAvailable(plan.KindID) || len(plan.KindInput) == 0 || plan.KindInputVersion == 0 {
		return ExecutionResult{}, fmt.Errorf("%w: %s", ErrUnsupported, s.kindSet[plan.KindID].UnavailableReason)
	}
	handoffKey := modulecore.RequestID(plan.HandoffKey)
	if err := handoffKey.Validate(); err != nil {
		return ExecutionResult{}, ErrConflict
	}
	var draft domaintask.Task
	if err := decodeSingleStrictJSON(plan.HandoffInput.TaskDraft, &draft); err != nil {
		return ExecutionResult{}, ErrUnavailable
	}
	var shared domaintask.SharedRoleContext
	if err := decodeSingleStrictJSON(plan.HandoffInput.SharedContext, &shared); err != nil {
		return ExecutionResult{}, ErrUnavailable
	}
	if err := validateFrozenMovieLookupProvenance(goal, draft, plan.HandoffInput.Revision, plan.HandoffInput.AssignmentEpoch); err != nil {
		return ExecutionResult{}, ErrConflict
	}
	kindInput := append(json.RawMessage(nil), plan.HandoffInput.KindInput...)
	readAuth := func(checkCtx context.Context, read domaintask.TaskCenterExecutionReceiptReadInput) error {
		return n.authorizeReceiptRead(checkCtx, scope, goal.GoalID, handoffKey, read)
	}
	task, run, findErr := n.Tasks.FindTaskCenterExecutionReceipt(ctx, handoffKey, plan.HandoffInput.Revision, draft, shared, kindInput, readAuth)
	if findErr != nil {
		if !errors.Is(findErr, domaintask.ErrTaskCenterExecutionReceiptNotFound) {
			return ExecutionResult{}, mapHandoffReceiptError(findErr)
		}
		if plan.HandoffState != domainworkstream.TaskPlanHandoffPrepared {
			return ExecutionResult{}, ErrUnavailable
		}
		admit := func(admitCtx context.Context, candidate domaintask.Task, candidateShared domaintask.SharedRoleContext, candidateInput json.RawMessage) error {
			current, err := s.store.GetTaskPlan(admitCtx, scope, goal.GoalID)
			if err != nil {
				return mapHandoffReceiptError(err)
			}
			if current.TaskPlan == nil || current.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffPrepared || current.TaskPlan.HandoffKey != string(handoffKey) || current.TaskPlan.HandoffInput == nil || current.TaskPlan.HandoffInput.InputHash != plan.HandoffInput.InputHash {
				return ErrConflict
			}
			if err := s.checkPolicyRevision(input.ExpectedPolicyRevision); err != nil {
				return err
			}
			if current.TaskPlan.PolicyRevision != input.ExpectedPolicyRevision {
				return ErrConflict
			}
			if err := s.authorizeHandoffActor(scope, current, current.TaskPlan.Revision, current.TaskPlan.AssignmentEpoch); err != nil {
				return err
			}
			allowed, err := s.allowed(current.TaskPlan.KindID, scope.ActorID, current.TaskPlan.SubsetAllowedAgents)
			if err != nil || !allowed {
				return ErrForbidden
			}
			frozenShared := candidateShared
			frozenShared.TaskID = shared.TaskID
			if !bytes.Equal(candidateInput, kindInput) || !reflect.DeepEqual(frozenShared, shared) || candidate.TaskCenterExecution == nil || candidate.TaskCenterExecution.KindID != current.TaskPlan.KindID {
				return ErrConflict
			}
			return nil
		}
		task, run, err = n.Tasks.EnsureTaskCenterExecution(ctx, handoffKey, plan.HandoffInput.Revision, draft, shared, kindInput, admit, readAuth)
		if err != nil {
			return ExecutionResult{}, mapHandoffReceiptError(err)
		}
	}
	if task.TaskID == "" || run.RunID == "" || task.GoalID != modulecore.GoalID(goal.GoalID) || task.WorkstreamID != modulecore.WorkstreamID(goal.WorkstreamID) {
		return ExecutionResult{}, ErrConflict
	}
	if plan.HandoffState != domainworkstream.TaskPlanHandoffLinked || plan.TaskID != task.TaskID || plan.RunID != run.RunID {
		linkRevision := plan.Revision
		linkInput := HandoffTransitionInput{
			GoalID: goal.GoalID, ExpectedRevision: linkRevision, ExpectedAssignmentEpoch: plan.AssignmentEpoch,
			ExpectedPolicyRevision: plan.PolicyRevision, IdempotencyKey: taskCenterLinkIdempotencyKey(input.IdempotencyKey),
			HandoffKey: taskexecution.HandoffKey(handoffKey), TaskID: task.TaskID, RunID: run.RunID,
		}
		linked, err := s.LinkHandoff(ctx, linkInput)
		if err != nil {
			return ExecutionResult{}, err
		}
		goal, err = s.store.GetTaskPlan(ctx, scope, goal.GoalID)
		if err != nil {
			return ExecutionResult{}, mapOwnerError(err)
		}
		plan = goal.TaskPlan
		if plan == nil || plan.HandoffState != domainworkstream.TaskPlanHandoffLinked || plan.TaskID != task.TaskID || plan.RunID != run.RunID {
			_ = linked
			return ExecutionResult{}, ErrConflict
		}
	}
	if task.TaskCenterExecution == nil || task.TaskCenterExecution.KindID != domainworkstream.TaskPlanKindMovieCatalogLookup {
		return ExecutionResult{}, ErrUnsupported
	}
	baseReference, err := resultReferenceFor(goal, task, run, "")
	if err != nil {
		return ExecutionResult{}, err
	}
	ref, result, foundErr := n.Results.GetTaskCenterExecutionResult(ctx, baseReference, func(checkCtx context.Context, candidate domaintask.TaskCenterResultReference) error {
		return n.authorizeLinkedResult(checkCtx, scope, goal.GoalID, candidate, task.TaskID, run.RunID)
	})
	if foundErr != nil && !errors.Is(foundErr, domainworkstream.ErrTaskPlanNotFound) {
		return ExecutionResult{}, mapHandoffReceiptError(foundErr)
	}
	if foundErr != nil {
		if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
			return ExecutionResult{}, ErrUnavailable
		}
		// The movie owner is needed only for a new effect. Historical receipt
		// recovery and existing-result reads remain available after that owner is
		// disconnected or disabled.
		if n.Movies == nil || !s.executorAvailable(plan.KindID) {
			return ExecutionResult{}, ErrUnsupported
		}
		admit := func(effectCtx context.Context, current domaintask.Task, currentRun domaintask.Run, currentShared domaintask.SharedRoleContext) (json.RawMessage, error) {
			currentGoal, err := s.store.GetTaskPlan(effectCtx, scope, goal.GoalID)
			if err != nil {
				return nil, mapHandoffReceiptError(err)
			}
			if err := n.authorizeLinkedGoal(scope, currentGoal, current.TaskID, currentRun.RunID); err != nil {
				return nil, err
			}
			if currentShared.TaskID != current.TaskID || current.TaskCenterExecution == nil || current.TaskCenterExecution.KindInputSHA256 != task.TaskCenterExecution.KindInputSHA256 {
				return nil, ErrConflict
			}
			currentReference, err := resultReferenceFor(currentGoal, current, currentRun, "")
			if err != nil {
				return nil, err
			}
			if _, _, readErr := n.Results.GetTaskCenterExecutionResult(effectCtx, currentReference, func(checkCtx context.Context, candidate domaintask.TaskCenterResultReference) error {
				return n.authorizeLinkedResult(checkCtx, scope, goal.GoalID, candidate, current.TaskID, currentRun.RunID)
			}); readErr == nil {
				return nil, errTaskCenterResultAlreadyPersisted
			} else if !errors.Is(readErr, domainworkstream.ErrTaskPlanNotFound) {
				return nil, mapHandoffReceiptError(readErr)
			}
			if err := s.checkPolicyRevision(input.ExpectedPolicyRevision); err != nil || currentGoal.TaskPlan.PolicyRevision != input.ExpectedPolicyRevision {
				return nil, ErrForbidden
			}
			if !currentGoal.TaskPlan.Timing.Ready(s.now()) {
				return nil, ErrConflict
			}
			allowed, allowErr := s.allowed(currentGoal.TaskPlan.KindID, scope.ActorID, currentGoal.TaskPlan.SubsetAllowedAgents)
			if allowErr != nil || !allowed {
				return nil, ErrForbidden
			}
			return append(json.RawMessage(nil), currentGoal.TaskPlan.KindInput...), nil
		}
		effect := func(effectCtx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			request, err := normalizeTaskPlanMovieLookup(raw)
			if err != nil {
				return nil, ErrConflict
			}
			value, err := n.Movies.Lookup(effectCtx, request.Kind, request.Name, request.Information, request.Limit)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			digest := sha256.Sum256(encoded)
			candidate, err := resultReferenceFor(goal, task, run, hex.EncodeToString(digest[:]))
			if err != nil {
				return nil, err
			}
			if err := candidate.Validate(task, run); err != nil {
				return nil, ErrConflict
			}
			if err := n.Results.SaveTaskCenterExecutionResult(effectCtx, candidate, encoded, func(checkCtx context.Context, stored domaintask.TaskCenterResultReference) error {
				return n.authorizeLinkedResult(checkCtx, scope, goal.GoalID, stored, task.TaskID, run.RunID)
			}); err != nil {
				return nil, err
			}
			return encoded, nil
		}
		_, effectErr := n.Tasks.ExecuteTaskCenterKind(ctx, task.TaskID, run.RunID, scope.ActorID, admit, effect)
		// Re-read after every fence result, including an ambiguous write error.
		// A concurrent or interrupted invocation may have committed the canonical
		// artifact while this caller was waiting for the Task fence.
		ref, result, foundErr = n.Results.GetTaskCenterExecutionResult(ctx, baseReference, func(checkCtx context.Context, candidate domaintask.TaskCenterResultReference) error {
			return n.authorizeLinkedResult(checkCtx, scope, goal.GoalID, candidate, task.TaskID, run.RunID)
		})
		if foundErr != nil {
			if effectErr != nil && !errors.Is(effectErr, errTaskCenterResultAlreadyPersisted) {
				return ExecutionResult{}, mapHandoffReceiptError(effectErr)
			}
			return ExecutionResult{}, mapHandoffReceiptError(foundErr)
		}
	}
	finishAuthorize := func(checkCtx context.Context, current domaintask.Task, currentRun domaintask.Run, currentShared domaintask.SharedRoleContext, reference domaintask.TaskCenterResultReference) error {
		if currentShared.TaskID != current.TaskID || reference != ref {
			return domaintask.ErrTaskCenterExecutionReceiptConflict
		}
		if err := n.authorizeLinkedResult(checkCtx, scope, goal.GoalID, reference, current.TaskID, currentRun.RunID); err != nil {
			return err
		}
		storedRef, storedResult, err := n.Results.GetTaskCenterExecutionResult(checkCtx, reference, func(context.Context, domaintask.TaskCenterResultReference) error { return nil })
		if err != nil || storedRef != reference || !bytes.Equal(storedResult, result) {
			return domaintask.ErrTaskCenterExecutionReceiptCorrupt
		}
		return nil
	}
	finishedTask, finishedRun, finishedRef, err := n.Tasks.FinishTaskCenterExecution(ctx, task.TaskID, run.RunID, task, scope.ActorID, domaintask.StatusSucceeded, "Typed result stored for Goal verification.", ref, finishAuthorize)
	if err != nil {
		return ExecutionResult{}, mapHandoffReceiptError(err)
	}
	if err := n.recordGoalResult(ctx, scope, goal.GoalID, input.IdempotencyKey, finishedTask, finishedRun, finishedRef); err != nil {
		return ExecutionResult{}, err
	}
	verification := domainworkstream.TaskPlanResultVerificationPending
	latest, err := s.store.GetTaskPlan(ctx, scope, goal.GoalID)
	if err != nil {
		return ExecutionResult{}, mapOwnerError(err)
	}
	if latest.TaskPlan == nil || latest.TaskPlan.ExecutionResult == nil || !goalHasResult(latest.TaskPlan, finishedRef) {
		return ExecutionResult{}, ErrConflict
	}
	verification = latest.TaskPlan.ExecutionResult.Verification
	return ExecutionResult{GoalID: goal.GoalID, TaskID: finishedTask.TaskID, RunID: finishedRun.RunID, ResultReference: finishedRef, Verification: verification}, nil
}

func (n *NativeExecution) recoverExecutionResultReceipt(ctx context.Context, scope tool.ToolExecutionScope, input ExecutionInput) (ExecutionResult, bool, error) {
	key := taskCenterResultIdempotencyKey(input.IdempotencyKey)
	receipt, found, err := n.Managed.store.TaskPlanReceipt(ctx, scope, "execution_result", key)
	if err != nil {
		return ExecutionResult{}, false, mapOwnerError(err)
	}
	if !found {
		return ExecutionResult{}, false, nil
	}
	prepared := receipt.Goal
	plan := prepared.TaskPlan
	if prepared.GoalID != input.GoalID || plan == nil || plan.HandoffInput == nil || plan.ExecutionResult == nil ||
		plan.HandoffInput.Revision != input.ExpectedRevision+1 || plan.AssignmentEpoch != input.ExpectedAssignmentEpoch ||
		plan.PolicyRevision != input.ExpectedPolicyRevision || plan.Assignee != scope.ActorID {
		return ExecutionResult{}, false, ErrConflict
	}
	current, err := n.Managed.store.GetTaskPlan(ctx, scope, input.GoalID)
	if err != nil {
		return ExecutionResult{}, false, mapOwnerError(err)
	}
	if n.Managed.authorizeVisibleGoal(scope, current) != nil || current.TaskPlan == nil || current.TaskPlan.HandoffInput == nil || current.TaskPlan.ExecutionResult == nil ||
		current.TaskPlan.Assignee != scope.ActorID || current.TaskPlan.HandoffInput.InputHash != plan.HandoffInput.InputHash ||
		current.TaskPlan.PolicyRevision != plan.PolicyRevision || current.TaskPlan.AssignmentEpoch != plan.AssignmentEpoch ||
		current.TaskPlan.ExecutionResult.ArtifactID != plan.ExecutionResult.ArtifactID ||
		current.TaskPlan.ExecutionResult.KindInputSHA256 != plan.ExecutionResult.KindInputSHA256 ||
		current.TaskPlan.ExecutionResult.ResultSHA256 != plan.ExecutionResult.ResultSHA256 ||
		current.TaskPlan.ExecutionResult.CriteriaSHA256 != plan.ExecutionResult.CriteriaSHA256 ||
		(current.TaskPlan.ExecutionResult.Verification != domainworkstream.TaskPlanResultVerificationPending && current.TaskPlan.ExecutionResult.Verification != domainworkstream.TaskPlanResultVerificationAccepted) {
		return ExecutionResult{}, false, ErrConflict
	}
	view, err := n.Managed.ReadResult(ctx, input.GoalID)
	if err != nil {
		return ExecutionResult{}, false, err
	}
	if view.ResultArtifactID != plan.ExecutionResult.ArtifactID || view.ResultSHA256 != plan.ExecutionResult.ResultSHA256 {
		return ExecutionResult{}, false, ErrConflict
	}
	reference, err := resultReferenceFromGoal(current)
	if err != nil {
		return ExecutionResult{}, false, err
	}
	return ExecutionResult{
		GoalID: input.GoalID, TaskID: plan.TaskID, RunID: plan.RunID,
		ResultReference: reference,
		Verification:    view.ExecutionVerification,
	}, true, nil
}

func (n *NativeExecution) recordGoalResult(ctx context.Context, scope tool.ToolExecutionScope, goalID, executionKey string, task domaintask.Task, run domaintask.Run, reference domaintask.TaskCenterResultReference) error {
	store, ok := n.Managed.store.(TaskPlanExecutionResultStore)
	if !ok {
		return ErrUnsupported
	}
	key := taskCenterResultIdempotencyKey(executionKey)
	if prior, found, err := n.Managed.store.TaskPlanReceipt(ctx, scope, "execution_result", key); err != nil {
		return mapHandoffReceiptError(err)
	} else if found {
		if prior.Goal.GoalID != goalID || prior.Goal.TaskPlan == nil || !goalHasResult(prior.Goal.TaskPlan, reference) {
			return ErrConflict
		}
		return nil
	}
	goal, err := n.Managed.store.GetTaskPlan(ctx, scope, goalID)
	if err != nil {
		return mapHandoffReceiptError(err)
	}
	if err := n.authorizeLinkedGoal(scope, goal, task.TaskID, run.RunID); err != nil {
		return err
	}
	criteria, err := domainworkstream.TaskPlanCriteriaSHA256(goal.SuccessCriteria, goal.Verification)
	if err != nil {
		return ErrInvalid
	}
	_, _, err = n.verifySuccessfulResult(ctx, scope, goal, task, run, reference)
	if err != nil {
		return err
	}
	request := domainworkstream.TaskPlanExecutionResultUpdate{
		GoalID: goalID, ExpectedRevision: goal.TaskPlan.Revision, ExpectedAssignmentEpoch: goal.TaskPlan.AssignmentEpoch,
		ExpectedPolicyRevision: goal.TaskPlan.PolicyRevision, IdempotencyKey: key, ArtifactID: reference.ArtifactID,
		TaskID: task.TaskID, RunID: run.RunID, KindInputSHA256: reference.KindInputSHA256,
		ResultSHA256: reference.ResultSHA256, CriteriaSHA256: criteria,
	}
	_, err = store.RecordTaskPlanExecutionResult(ctx, scope, request, func(current domainworkstream.Goal) error {
		if err := n.authorizeLinkedGoal(scope, current, task.TaskID, run.RunID); err != nil {
			return err
		}
		if current.TaskPlan.PolicyRevision != goal.TaskPlan.PolicyRevision || !goalMatchesResult(current, reference, criteria) {
			return ErrConflict
		}
		return nil
	})
	return mapOwnerError(err)
}

func goalHasResult(plan *domainworkstream.TaskPlan, reference domaintask.TaskCenterResultReference) bool {
	return plan != nil && plan.ExecutionResult != nil && plan.ExecutionResult.ArtifactID == reference.ArtifactID && plan.ExecutionResult.KindInputSHA256 == reference.KindInputSHA256 && plan.ExecutionResult.ResultSHA256 == reference.ResultSHA256
}

func goalMatchesResult(goal domainworkstream.Goal, reference domaintask.TaskCenterResultReference, criteriaSHA256 string) bool {
	if goal.TaskPlan == nil || goal.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffLinked || goal.TaskPlan.TaskID != reference.TaskID || goal.TaskPlan.RunID != reference.RunID || goal.TaskPlan.DataScope != reference.DataScope || goal.TaskPlan.UserID != reference.UserID || goal.WorkstreamID != string(reference.WorkstreamID) || goal.GoalID != string(reference.SourceGoalID) {
		return false
	}
	digest, err := domaintask.TaskCenterKindInputSHA256(goal.TaskPlan.KindInput)
	criteria, criteriaErr := domainworkstream.TaskPlanCriteriaSHA256(goal.SuccessCriteria, goal.Verification)
	return err == nil && criteriaErr == nil && digest == reference.KindInputSHA256 && (criteriaSHA256 == "" || criteria == criteriaSHA256)
}

func taskCenterResultIdempotencyKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "taskcenter-result:" + hex.EncodeToString(digest[:16])
}

func taskCenterLinkIdempotencyKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "taskcenter-link:" + hex.EncodeToString(digest[:16])
}

func taskCenterPrepareIdempotencyKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "taskcenter-prepare:" + hex.EncodeToString(digest[:16])
}

func (n *NativeExecution) authorizeReceiptRead(ctx context.Context, scope tool.ToolExecutionScope, goalID string, key modulecore.RequestID, input domaintask.TaskCenterExecutionReceiptReadInput) error {
	goal, err := n.Managed.store.GetTaskPlan(ctx, scope, goalID)
	if err != nil {
		return mapHandoffReceiptError(err)
	}
	if goal.TaskPlan == nil || input.Task.TaskCenterExecution == nil || input.Task.TaskCenterExecution.Validate(input.Task) != nil ||
		n.Managed.authorizeHandoffReconciliation(scope, goal, goal.TaskPlan.Revision, input.Task.TaskCenterExecution.AssignmentEpoch) != nil ||
		goal.TaskPlan.HandoffKey != string(key) || goal.TaskPlan.HandoffInput == nil ||
		goal.TaskPlan.HandoffInput.Revision != input.PreparedRevision || goal.TaskPlan.HandoffInput.InputHash == "" || goal.TaskPlan.HandoffState == domainworkstream.TaskPlanHandoffNone {
		return ErrForbidden
	}
	return nil
}

func (n *NativeExecution) authorizeLinkedGoal(scope tool.ToolExecutionScope, goal domainworkstream.Goal, taskID modulecore.TaskID, runID modulecore.RunID) error {
	if err := n.Managed.authorizeVisibleGoal(scope, goal); err != nil {
		return err
	}
	if goal.TaskPlan == nil || goal.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffLinked || goal.TaskPlan.TaskID != taskID || goal.TaskPlan.RunID != runID ||
		goal.TaskPlan.Assignee != scope.ActorID || goal.TaskPlan.HandoffInput == nil || goal.TaskPlan.HandoffInput.Validate(*goal.TaskPlan) != nil {
		return ErrConflict
	}
	return nil
}

func (n *NativeExecution) authorizeLinkedResult(ctx context.Context, scope tool.ToolExecutionScope, goalID string, reference domaintask.TaskCenterResultReference, taskID modulecore.TaskID, runID modulecore.RunID) error {
	goal, err := n.Managed.store.GetTaskPlan(ctx, scope, goalID)
	if err != nil {
		return mapHandoffReceiptError(err)
	}
	if err := n.authorizeLinkedGoal(scope, goal, taskID, runID); err != nil {
		return err
	}
	plan := goal.TaskPlan
	if plan.HandoffInput == nil || reference.SourceGoalID != modulecore.GoalID(goal.GoalID) || reference.WorkstreamID != modulecore.WorkstreamID(goal.WorkstreamID) ||
		reference.KindID != plan.KindID || reference.DataScope != plan.DataScope || reference.UserID != plan.UserID ||
		reference.KindInputSHA256 != taskKindInputDigest(plan.HandoffInput.KindInput) {
		return ErrForbidden
	}
	return nil
}

func (n *NativeExecution) verifySuccessfulResult(ctx context.Context, scope tool.ToolExecutionScope, goal domainworkstream.Goal, expected domaintask.Task, expectedRun domaintask.Run, reference domaintask.TaskCenterResultReference) (domaintask.Task, domaintask.Run, error) {
	if goal.TaskPlan == nil || goal.TaskPlan.HandoffInput == nil || expected.TaskCenterExecution == nil || reference.TaskID != expectedRun.TaskID || reference.RunID != expectedRun.RunID {
		return domaintask.Task{}, domaintask.Run{}, ErrConflict
	}
	var frozen domaintask.Task
	if err := decodeSingleStrictJSON(goal.TaskPlan.HandoffInput.TaskDraft, &frozen); err != nil {
		return domaintask.Task{}, domaintask.Run{}, ErrUnavailable
	}
	var shared domaintask.SharedRoleContext
	if err := decodeSingleStrictJSON(goal.TaskPlan.HandoffInput.SharedContext, &shared); err != nil {
		return domaintask.Task{}, domaintask.Run{}, ErrUnavailable
	}
	requestValue := struct {
		TaskID  modulecore.TaskID                    `json:"task_id"`
		RunID   modulecore.RunID                     `json:"run_id"`
		Status  domaintask.Status                    `json:"status"`
		Summary string                               `json:"summary"`
		Result  domaintask.TaskCenterResultReference `json:"result_reference"`
	}{expected.TaskID, expectedRun.RunID, domaintask.StatusSucceeded, "Typed result stored for Goal verification.", reference}
	encoded, err := json.Marshal(requestValue)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, ErrInvalid
	}
	requestDigest := sha256.Sum256(encoded)
	operationID := fmt.Sprintf("taskcenter-finish:%s:%s", expected.TaskID, expectedRun.RunID)
	authorize := func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, currentShared domaintask.SharedRoleContext, stored domaintask.TaskCenterResultReference) error {
		frozenShared := currentShared
		frozenShared.TaskID = shared.TaskID
		if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded || stored != reference || !domaintask.TaskCenterDraftMatchesStored(frozen, task) || !reflect.DeepEqual(shared, frozenShared) {
			return domaintask.ErrTaskCenterExecutionReceiptConflict
		}
		if err := n.authorizeResultScope(checkCtx, scope, goal.GoalID, reference, task.TaskID, run.RunID); err != nil {
			return err
		}
		return nil
	}
	task, run, stored, err := n.Tasks.FindTaskCenterExecutionResultReceipt(ctx, expected.TaskID, expectedRun.RunID, frozen, operationID, hex.EncodeToString(requestDigest[:]), reference, authorize)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, mapHandoffReceiptError(err)
	}
	if stored != reference {
		return domaintask.Task{}, domaintask.Run{}, ErrConflict
	}
	return task, run, nil
}

func (n *NativeExecution) authorizeResultScope(ctx context.Context, scope tool.ToolExecutionScope, goalID string, reference domaintask.TaskCenterResultReference, taskID modulecore.TaskID, runID modulecore.RunID) error {
	goal, err := n.Managed.store.GetTaskPlan(ctx, scope, goalID)
	if err != nil {
		return mapHandoffReceiptError(err)
	}
	if n.Managed.authorizeVisibleGoal(scope, goal) != nil || goal.TaskPlan == nil || goal.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffLinked || goal.TaskPlan.TaskID != taskID || goal.TaskPlan.RunID != runID || !goalMatchesResult(goal, reference, "") {
		return ErrForbidden
	}
	if scope.ActorKind == tool.ActorKindUser || scope.ActorKind == tool.ActorKindAgent {
		return nil
	}
	return ErrForbidden
}

func taskKindInputDigest(kindInput json.RawMessage) string {
	digest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		return ""
	}
	return digest
}

func resultReferenceFor(goal domainworkstream.Goal, task domaintask.Task, run domaintask.Run, resultSHA256 string) (domaintask.TaskCenterResultReference, error) {
	artifactID, err := domaintask.TaskCenterResultArtifactID(task.TaskID, run.RunID)
	if err != nil || task.TaskCenterExecution == nil || goal.TaskPlan == nil {
		return domaintask.TaskCenterResultReference{}, ErrConflict
	}
	reference := domaintask.TaskCenterResultReference{
		ArtifactID: artifactID, SourceGoalID: modulecore.GoalID(goal.GoalID), WorkstreamID: modulecore.WorkstreamID(goal.WorkstreamID),
		TaskID: task.TaskID, RunID: run.RunID, KindID: task.TaskCenterExecution.KindID,
		KindInputSHA256: task.TaskCenterExecution.KindInputSHA256, ResultSHA256: resultSHA256, DataScope: goal.TaskPlan.DataScope,
	}
	if goal.TaskPlan.DataScope == domainworkstream.TaskPlanDataScopeUser {
		reference.UserID = goal.TaskPlan.UserID
	}
	if resultSHA256 != "" && reference.Validate(task, run) != nil {
		return domaintask.TaskCenterResultReference{}, ErrConflict
	}
	return reference, nil
}
