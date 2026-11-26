package taskcenter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskCenterListDescriptionMaxRunes = 240

type Service struct {
	store                            TaskPlanStore
	policy                           taskexecution.Policy
	actors                           []string
	kinds                            []Kind
	actorSet                         map[string]struct{}
	kindSet                          map[string]Kind
	policyRevision                   string
	now                              func() time.Time
	receiptVerifier                  HandoffReceiptVerifier
	nativeExecution                  *NativeExecution
	userTaskCancellationOwner        UserTaskCancellationOwner
	userTaskCancellationPublications UserTaskCancellationPublicationReader
}

func NewService(store TaskPlanStore, policy taskexecution.Policy, actors []string, kinds []Kind, clock func() time.Time) (*Service, error) {
	return NewServiceWithHandoffReceiptVerifier(store, policy, actors, kinds, clock, nil)
}

func NewServiceWithHandoffReceiptVerifier(store TaskPlanStore, policy taskexecution.Policy, actors []string, kinds []Kind, clock func() time.Time, verifier HandoffReceiptVerifier) (*Service, error) {
	if store == nil {
		return nil, errors.New("task-center Goal owner is required")
	}
	policy = clonePolicy(policy)
	actorIDs := append([]string(nil), actors...)
	kindIDs := make([]string, 0, len(kinds))
	kindSet := make(map[string]Kind, len(kinds))
	for _, kind := range kinds {
		if kind.ID == "" || kind.ID != strings.TrimSpace(kind.ID) || kind.ID != strings.ToLower(kind.ID) {
			return nil, fmt.Errorf("invalid registered task kind %q", kind.ID)
		}
		if !kind.ExecutorAvailable && kind.UnavailableReason == "" {
			return nil, fmt.Errorf("task kind %q must explain why its executor is unavailable", kind.ID)
		}
		if _, exists := kindSet[kind.ID]; exists {
			return nil, fmt.Errorf("duplicate registered task kind %q", kind.ID)
		}
		seenIntrinsic := map[string]struct{}{}
		if len(kind.InputSchema) > 0 && !json.Valid(kind.InputSchema) {
			return nil, fmt.Errorf("task kind %q has invalid input schema JSON", kind.ID)
		}
		for _, actorID := range kind.IntrinsicAllowedAgents {
			if !containsString(actorIDs, actorID) {
				return nil, fmt.Errorf("task kind %q has an unregistered intrinsic actor %q", kind.ID, actorID)
			}
			if _, exists := seenIntrinsic[actorID]; exists {
				return nil, fmt.Errorf("task kind %q repeats intrinsic actor %q", kind.ID, actorID)
			}
			seenIntrinsic[actorID] = struct{}{}
		}
		kindIDs = append(kindIDs, kind.ID)
		kind.InputSchema = append(json.RawMessage(nil), kind.InputSchema...)
		kind.IntrinsicAllowedAgents = append([]string(nil), kind.IntrinsicAllowedAgents...)
		kindSet[kind.ID] = kind
	}
	if err := policy.Validate(kindIDs, actorIDs); err != nil {
		return nil, fmt.Errorf("validate task execution policy: %w", err)
	}
	policyRevision, err := policy.CanonicalHash()
	if err != nil {
		return nil, fmt.Errorf("hash task execution policy: %w", err)
	}
	if clock == nil {
		clock = time.Now
	}
	actorSet := make(map[string]struct{}, len(actorIDs))
	for _, actorID := range actorIDs {
		actorSet[actorID] = struct{}{}
	}
	return &Service{
		store: store, policy: policy, actors: actorIDs, kinds: append([]Kind(nil), kinds...),
		actorSet: actorSet, kindSet: kindSet, policyRevision: policyRevision, now: clock, receiptVerifier: verifier,
	}, nil
}

func (s *Service) Submit(ctx context.Context, input SubmitInput) (MutationResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	dataScope, userID, err := requestedScope(scope, input.DataScope)
	if err != nil {
		return MutationResult{}, err
	}
	if input.ExpectedPolicyRevision == "" {
		return MutationResult{}, fmt.Errorf("%w: expected_policy_revision is required", ErrInvalid)
	}
	if input.KindID == "" || input.KindID != strings.ToLower(input.KindID) || input.KindID != strings.TrimSpace(input.KindID) {
		return MutationResult{}, fmt.Errorf("%w: invalid kind_id", ErrInvalid)
	}
	if len(input.KindInput) > 8<<10 || (len(input.KindInput) > 0 && input.KindID != domainworkstream.TaskPlanKindMovieCatalogLookup) {
		return MutationResult{}, fmt.Errorf("%w: typed kind input is unsupported or too large", ErrUnsupported)
	}
	if len(input.KindInput) > 0 {
		if input.KindID == domainworkstream.TaskPlanKindMovieCatalogLookup {
			lookup, lookupErr := normalizeTaskPlanMovieLookup(input.KindInput)
			if lookupErr != nil {
				return MutationResult{}, fmt.Errorf("%w: typed movie lookup input: %v", ErrInvalid, lookupErr)
			}
			input.KindInput, err = json.Marshal(lookup)
			if err != nil {
				return MutationResult{}, fmt.Errorf("%w: typed movie lookup input cannot be frozen", ErrInvalid)
			}
		} else if !json.Valid(input.KindInput) {
			return MutationResult{}, fmt.Errorf("%w: kind_input must be valid JSON", ErrInvalid)
		}
	}
	if len(input.SuccessCriteria) == 0 || len(input.Verification) == 0 || !allTextPresent(input.SuccessCriteria) || !allTextPresent(input.Verification) {
		return MutationResult{}, fmt.Errorf("%w: success_criteria and verification are required", ErrInvalid)
	}
	if strings.TrimSpace(input.Description) != input.Description {
		return MutationResult{}, fmt.Errorf("%w: description must not have surrounding whitespace", ErrInvalid)
	}
	if err := input.Timing.Validate(); err != nil {
		return MutationResult{}, fmt.Errorf("%w: timing: %v", ErrInvalid, err)
	}
	if input.Timing.Recurrence != nil && input.Timing.Recurrence.Frequency != modulecore.TaskRecurrenceNone {
		return MutationResult{}, fmt.Errorf("%w: recurrence scheduling is not supported for managed Goals", ErrUnsupported)
	}
	if !validIdempotencyKey(input.IdempotencyKey) {
		return MutationResult{}, fmt.Errorf("%w: idempotency_key is required", ErrInvalid)
	}

	assignee := input.Assignee
	if assignee == "" {
		if scope.ActorKind == tool.ActorKindAgent {
			assignee = scope.ActorID
		} else {
			assignee = domainworkstream.TaskPlanAssigneeUnassigned
		}
	}
	if assignee != domainworkstream.TaskPlanAssigneeUnassigned {
		if scope.ActorKind == tool.ActorKindAgent && assignee != scope.ActorID {
			return MutationResult{}, fmt.Errorf("%w: Agents may assign only themselves or leave a task unassigned", ErrForbidden)
		}
	}
	var subset *[]string
	if input.AllowedAgents != nil {
		if len(input.AllowedAgents) == 0 {
			return MutationResult{}, fmt.Errorf("%w: allowed_agents must not be empty when supplied", ErrInvalid)
		}
		copySubset := append([]string(nil), input.AllowedAgents...)
		subset = &copySubset
	}

	now := s.now().UTC()
	title := taskPlanTitle(input.Title, input.Description, input.SuccessCriteria, input.Verification)
	plan := &domainworkstream.TaskPlan{
		DataScope: dataScope, UserID: userID, CreatorKind: string(scope.ActorKind), CreatedBy: scope.ActorID, Assignee: assignee, KindID: input.KindID,
		KindInput: append(json.RawMessage(nil), input.KindInput...), KindInputVersion: boolInt(len(input.KindInput) > 0),
		SubsetAllowedAgents: subset, Revision: 1, PolicyRevision: input.ExpectedPolicyRevision, Timing: input.Timing,
		HandoffState: domainworkstream.TaskPlanHandoffNone, UpdatedAt: now,
	}
	goal := domainworkstream.Goal{
		WorkstreamID: input.WorkstreamID, Title: title, Description: input.Description,
		SuccessCriteria: cloneStrings(input.SuccessCriteria), Verification: cloneStrings(input.Verification),
		Status: domainworkstream.StatusWaiting, CreatedAt: now, TaskPlan: plan,
	}
	request := domainworkstream.TaskPlanSubmission{WorkstreamID: input.WorkstreamID, Goal: goal, IdempotencyKey: input.IdempotencyKey}
	result, err := s.store.SubmitTaskPlan(ctx, scope, request, func(candidate domainworkstream.Goal) error {
		if err := s.checkPolicyRevision(input.ExpectedPolicyRevision); err != nil {
			return err
		}
		return s.authorizeSubmittedGoal(scope, dataScope, userID, candidate)
	})
	if err != nil {
		return MutationResult{}, mapOwnerError(err)
	}
	return MutationResult{Item: s.item(result.Goal), Replayed: result.Replayed, ReceiptRef: result.ReceiptRef}, nil
}

func (s *Service) SubmitGameProjectPlan(ctx context.Context, input domainworkstream.GameProjectSubmission) (domainworkstream.ProjectPlanMutationResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	if scope.ActorKind != tool.ActorKindUser || scope.ActorID != scope.AuthenticatedUserID || scope.AuthenticatedUserID == "" || !scope.Allows(tool.DataScopePublic) {
		return domainworkstream.ProjectPlanMutationResult{}, ErrForbidden
	}
	if err := input.Validate(); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	store, ok := s.store.(GameProjectPlanStore)
	if !ok {
		return domainworkstream.ProjectPlanMutationResult{}, ErrUnsupported
	}
	result, err := store.SubmitGameProjectPlan(ctx, scope, input)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, mapOwnerError(err)
	}
	return result, nil
}

func (s *Service) GameProjectPlanReceipt(ctx context.Context, idempotencyKey string) (domainworkstream.ProjectPlanMutationResult, bool, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, false, err
	}
	if scope.ActorKind != tool.ActorKindUser || scope.ActorID != scope.AuthenticatedUserID || scope.AuthenticatedUserID == "" || !scope.Allows(tool.DataScopePublic) {
		return domainworkstream.ProjectPlanMutationResult{}, false, ErrForbidden
	}
	if idempotencyKey == "" || len(idempotencyKey) > 128 || strings.TrimSpace(idempotencyKey) != idempotencyKey {
		return domainworkstream.ProjectPlanMutationResult{}, false, ErrInvalid
	}
	store, ok := s.store.(GameProjectPlanStore)
	if !ok {
		return domainworkstream.ProjectPlanMutationResult{}, false, ErrUnsupported
	}
	result, found, err := store.GameProjectPlanReceipt(ctx, scope, idempotencyKey)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, false, mapOwnerError(err)
	}
	return result, found, nil
}

// PrepareHandoff durably freezes the exact TaskManager draft, shared context
// and typed owner input in the Goal before any TaskManager write is attempted.
func (s *Service) PrepareHandoff(ctx context.Context, input HandoffPrepareInput) (MutationResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	if scope.ActorKind != tool.ActorKindAgent || input.ExpectedRevision == 0 || input.ExpectedRevision == ^uint64(0) || input.ExpectedPolicyRevision == "" || !validIdempotencyKey(input.IdempotencyKey) {
		return MutationResult{}, fmt.Errorf("%w: invalid or unauthorized handoff preparation", ErrForbidden)
	}
	if err := input.HandoffKey.Validate(); err != nil {
		return MutationResult{}, fmt.Errorf("%w: invalid handoff key", ErrInvalid)
	}
	if len(input.TaskDraft) == 0 || len(input.TaskDraft) > 64<<10 || len(input.SharedContext) == 0 || len(input.SharedContext) > 64<<10 || !json.Valid(input.TaskDraft) || !json.Valid(input.SharedContext) || input.ExpectedRevision == ^uint64(0) {
		return MutationResult{}, fmt.Errorf("%w: exact frozen task draft and shared context are required", ErrInvalid)
	}
	store, ok := s.store.(TaskPlanHandoffStore)
	if !ok {
		return MutationResult{}, ErrUnsupported
	}
	goal, err := s.store.GetTaskPlan(ctx, scope, input.GoalID)
	if err != nil {
		return MutationResult{}, mapOwnerError(err)
	}
	if err := s.authorizeVisibleGoal(scope, goal); err != nil {
		return MutationResult{}, err
	}
	var draft domaintask.Task
	if err := decodeSingleStrictJSON(input.TaskDraft, &draft); err != nil {
		return MutationResult{}, fmt.Errorf("%w: frozen Task draft is invalid", ErrInvalid)
	}
	var shared domaintask.SharedRoleContext
	if err := decodeSingleStrictJSON(input.SharedContext, &shared); err != nil {
		return MutationResult{}, fmt.Errorf("%w: frozen Task context is invalid", ErrInvalid)
	}
	if goal.TaskPlan.KindID != domainworkstream.TaskPlanKindMovieCatalogLookup || len(goal.TaskPlan.KindInput) == 0 {
		return MutationResult{}, ErrUnsupported
	}
	lookup, err := normalizeTaskPlanMovieLookup(goal.TaskPlan.KindInput)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: stored typed lookup input is invalid", ErrInvalid)
	}
	kindInput, err := json.Marshal(lookup)
	if err != nil || !bytes.Equal(kindInput, goal.TaskPlan.KindInput) {
		return MutationResult{}, fmt.Errorf("%w: stored typed lookup input is not canonical", ErrConflict)
	}
	kindInputDigest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: stored typed lookup input cannot be hashed", ErrInvalid)
	}
	timing := goal.TaskPlan.Timing
	timingDigest, err := domaintask.TaskTimingSHA256(timing)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: stored Task timing cannot be hashed", ErrInvalid)
	}
	execution := &domaintask.TaskCenterExecution{
		KindID: goal.TaskPlan.KindID, KindInputVersion: goal.TaskPlan.KindInputVersion,
		KindInputSHA256: kindInputDigest, SourceGoalID: modulecore.GoalID(goal.GoalID),
		PreparedRevision: input.ExpectedRevision + 1, AssignmentEpoch: input.ExpectedAssignmentEpoch,
		TimingSHA256: timingDigest,
	}
	if draft.Timing != nil && !reflect.DeepEqual(*draft.Timing, timing) {
		return MutationResult{}, fmt.Errorf("%w: Task timing differs from the prepared Goal", ErrConflict)
	}
	if draft.TaskCenterExecution != nil && *draft.TaskCenterExecution != *execution {
		return MutationResult{}, fmt.Errorf("%w: Task execution descriptor differs from the prepared Goal", ErrConflict)
	}
	draft.Timing = &timing
	draft.TaskCenterExecution = execution
	if err := execution.Validate(draft); err != nil {
		return MutationResult{}, fmt.Errorf("%w: Task execution descriptor is invalid: %v", ErrInvalid, err)
	}
	if goal.TaskPlan.DataScope == domainworkstream.TaskPlanDataScopeUser {
		access := &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: goal.TaskPlan.UserID}
		if draft.PrivateAccess != nil && *draft.PrivateAccess != *access {
			return MutationResult{}, fmt.Errorf("%w: private Task owner differs from the prepared Goal", ErrForbidden)
		}
		draft.PrivateAccess = access
	} else if draft.PrivateAccess != nil {
		return MutationResult{}, fmt.Errorf("%w: public Task cannot carry private access", ErrInvalid)
	}
	if err := validateFrozenMovieLookupDraft(goal, draft, shared); err != nil {
		return MutationResult{}, fmt.Errorf("%w: frozen typed Task request is invalid: %v", ErrInvalid, err)
	}
	if err := validateFrozenMovieLookupProvenance(goal, draft, input.ExpectedRevision+1, input.ExpectedAssignmentEpoch); err != nil {
		return MutationResult{}, fmt.Errorf("%w: frozen Task provenance is invalid: %v", ErrInvalid, err)
	}
	frozenTaskDraft, err := json.Marshal(draft)
	if err != nil || len(frozenTaskDraft) > 64<<10 {
		return MutationResult{}, fmt.Errorf("%w: canonical frozen Task draft is invalid or too large", ErrInvalid)
	}
	frozenSharedContext, err := json.Marshal(shared)
	if err != nil || len(frozenSharedContext) > 64<<10 {
		return MutationResult{}, fmt.Errorf("%w: canonical frozen Task context is invalid or too large", ErrInvalid)
	}
	frozen := domainworkstream.TaskPlanHandoffInput{
		Version: 1, Revision: input.ExpectedRevision + 1, AssignmentEpoch: input.ExpectedAssignmentEpoch,
		KindID: goal.TaskPlan.KindID, KindInputVersion: goal.TaskPlan.KindInputVersion, TaskDraft: frozenTaskDraft,
		SharedContext: frozenSharedContext, KindInput: kindInput,
	}
	frozen.InputHash = domainworkstream.ComputeTaskPlanHandoffInputHash(frozen.TaskDraft, frozen.SharedContext, frozen.KindInput)
	request := domainworkstream.TaskPlanHandoffPrepare{
		GoalID: input.GoalID, ExpectedRevision: input.ExpectedRevision,
		ExpectedAssignmentEpoch: input.ExpectedAssignmentEpoch, ExpectedPolicyRevision: input.ExpectedPolicyRevision,
		IdempotencyKey: input.IdempotencyKey, HandoffKey: input.HandoffKey, Input: frozen,
	}
	result, err := store.PrepareTaskPlanHandoff(ctx, scope, request, func(candidate domainworkstream.Goal) error {
		if err := s.checkPolicyRevision(input.ExpectedPolicyRevision); err != nil {
			return err
		}
		if err := s.authorizeHandoffActor(scope, candidate, input.ExpectedRevision, input.ExpectedAssignmentEpoch); err != nil {
			return err
		}
		if candidate.TaskPlan.KindID != frozen.KindID || !bytes.Equal(candidate.TaskPlan.KindInput, kindInput) {
			return ErrConflict
		}
		allowed, allowErr := s.allowed(candidate.TaskPlan.KindID, scope.ActorID, candidate.TaskPlan.SubsetAllowedAgents)
		if allowErr != nil || !allowed {
			return ErrForbidden
		}
		return nil
	})
	if err != nil {
		return MutationResult{}, mapOwnerError(err)
	}
	return MutationResult{Item: s.item(result.Goal), Replayed: result.Replayed, ReceiptRef: result.ReceiptRef}, nil
}

func (s *Service) MarkHandoffOutcomeUnknown(ctx context.Context, input HandoffTransitionInput) (MutationResult, error) {
	return s.transitionHandoff(ctx, input, false)
}

func (s *Service) LinkHandoff(ctx context.Context, input HandoffTransitionInput) (MutationResult, error) {
	if input.TaskID == "" || input.RunID == "" || input.TaskID.Validate() != nil || input.RunID.Validate() != nil {
		return MutationResult{}, fmt.Errorf("%w: linked handoff requires canonical Task and Run IDs", ErrInvalid)
	}
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	if err := input.HandoffKey.Validate(); err != nil {
		return MutationResult{}, fmt.Errorf("%w: invalid handoff key", ErrInvalid)
	}
	if input.IdempotencyKey == "" || !validIdempotencyKey(input.IdempotencyKey) {
		return MutationResult{}, ErrInvalid
	}
	// This read-only owner receipt lookup runs before current Goal checks. Its
	// stored result contains every Link request field, so an exact retry can be
	// distinguished from same-key changed bodies even after later Goal revisions.
	receipt, found, receiptErr := s.store.TaskPlanReceipt(ctx, scope, "handoff_link", input.IdempotencyKey)
	if receiptErr != nil {
		return MutationResult{}, mapHandoffReceiptError(receiptErr)
	}
	if found {
		if !taskPlanLinkReceiptMatches(input, receipt.Goal) {
			return MutationResult{}, ErrConflict
		}
		return MutationResult{Item: s.item(receipt.Goal), Replayed: true, ReceiptRef: receipt.ReceiptRef}, nil
	}
	return s.transitionHandoff(ctx, input, true)
}

// taskPlanLinkReceiptMatches mirrors every HandoffTransitionInput field in
// the owner receipt result: GoalID, expected revision (result revision - 1),
// assignment epoch, policy revision, HandoffKey, TaskID and RunID. The owner
// receipt lookup already binds operation, idempotency key and caller principal.
// Keep this match complete if HandoffTransitionInput gains fields.
func taskPlanLinkReceiptMatches(input HandoffTransitionInput, goal domainworkstream.Goal) bool {
	if input.ExpectedRevision == ^uint64(0) || goal.GoalID != input.GoalID || goal.TaskPlan == nil || domainworkstream.ValidateGoal(goal) != nil {
		return false
	}
	plan := goal.TaskPlan
	if plan.Revision != input.ExpectedRevision+1 || plan.AssignmentEpoch != input.ExpectedAssignmentEpoch ||
		plan.PolicyRevision != input.ExpectedPolicyRevision || plan.HandoffState != domainworkstream.TaskPlanHandoffLinked ||
		plan.HandoffKey != string(input.HandoffKey) || plan.TaskID != input.TaskID || plan.RunID != input.RunID || plan.HandoffInput == nil {
		return false
	}
	return plan.HandoffInput.Validate(*plan) == nil
}

func (s *Service) transitionHandoff(ctx context.Context, input HandoffTransitionInput, link bool) (MutationResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	if scope.ActorKind != tool.ActorKindAgent || input.ExpectedRevision == 0 || input.ExpectedRevision == ^uint64(0) || input.ExpectedPolicyRevision == "" || !validIdempotencyKey(input.IdempotencyKey) {
		return MutationResult{}, fmt.Errorf("%w: invalid or unauthorized handoff transition", ErrForbidden)
	}
	if !link && (input.TaskID != "" || input.RunID != "") {
		return MutationResult{}, fmt.Errorf("%w: uncertain handoff must not claim execution identifiers", ErrInvalid)
	}
	if err := input.HandoffKey.Validate(); err != nil {
		return MutationResult{}, fmt.Errorf("%w: invalid handoff key", ErrInvalid)
	}
	store, ok := s.store.(TaskPlanHandoffStore)
	if !ok {
		return MutationResult{}, ErrUnsupported
	}
	var verifiedInputHash string
	if link {
		if s.receiptVerifier == nil {
			return MutationResult{}, ErrUnsupported
		}
		source, readErr := s.store.GetTaskPlan(ctx, scope, input.GoalID)
		if readErr != nil {
			return MutationResult{}, mapHandoffReceiptError(readErr)
		}
		if err := s.authorizeHandoffReconciliation(scope, source, input.ExpectedRevision, input.ExpectedAssignmentEpoch); err != nil {
			return MutationResult{}, err
		}
		if source.TaskPlan.HandoffKey != string(input.HandoffKey) || (source.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffPrepared && source.TaskPlan.HandoffState != domainworkstream.TaskPlanHandoffOutcomeUnknown) {
			return MutationResult{}, ErrConflict
		}
		var frozenDraft domaintask.Task
		if err := decodeSingleStrictJSON(source.TaskPlan.HandoffInput.TaskDraft, &frozenDraft); err != nil || frozenDraft.TaskID != "" || frozenDraft.WorkstreamID != modulecore.WorkstreamID(source.WorkstreamID) || frozenDraft.GoalID != modulecore.GoalID(source.GoalID) {
			return MutationResult{}, ErrConflict
		}
		if err := validateFrozenMovieLookupProvenance(source, frozenDraft, source.TaskPlan.HandoffInput.Revision, source.TaskPlan.HandoffInput.AssignmentEpoch); err != nil {
			return MutationResult{}, ErrConflict
		}
		verifiedInputHash = source.TaskPlan.HandoffInput.InputHash
		authorizeReceiptRead := func(checkCtx context.Context) error {
			if checkCtx == nil {
				return ErrForbidden
			}
			current, err := s.store.GetTaskPlan(checkCtx, scope, input.GoalID)
			if err != nil {
				return mapHandoffReceiptError(err)
			}
			if err := s.authorizeHandoffReconciliation(scope, current, input.ExpectedRevision, input.ExpectedAssignmentEpoch); err != nil {
				return err
			}
			if current.TaskPlan.HandoffKey != string(input.HandoffKey) || current.TaskPlan.HandoffInput == nil || current.TaskPlan.HandoffInput.InputHash != verifiedInputHash || current.TaskPlan.HandoffState != source.TaskPlan.HandoffState {
				return ErrConflict
			}
			return nil
		}
		if err := authorizeReceiptRead(ctx); err != nil {
			return MutationResult{}, err
		}
		if err := s.receiptVerifier.VerifyTaskCenterHandoffReceipt(ctx, source, input.TaskID, input.RunID, authorizeReceiptRead); err != nil {
			return MutationResult{}, mapHandoffReceiptError(err)
		}
		if err := authorizeReceiptRead(ctx); err != nil {
			return MutationResult{}, err
		}
	}
	request := domainworkstream.TaskPlanHandoffTransition{
		GoalID: input.GoalID, ExpectedRevision: input.ExpectedRevision,
		ExpectedAssignmentEpoch: input.ExpectedAssignmentEpoch, ExpectedPolicyRevision: input.ExpectedPolicyRevision,
		IdempotencyKey: input.IdempotencyKey, HandoffKey: input.HandoffKey, TaskID: input.TaskID, RunID: input.RunID,
	}
	authorize := func(goal domainworkstream.Goal) error {
		if err := s.authorizeHandoffReconciliation(scope, goal, input.ExpectedRevision, input.ExpectedAssignmentEpoch); err != nil {
			return err
		}
		if goal.TaskPlan.HandoffKey != string(input.HandoffKey) || goal.TaskPlan.HandoffInput == nil {
			return ErrConflict
		}
		if link {
			if goal.TaskPlan.HandoffInput.InputHash != verifiedInputHash {
				return ErrConflict
			}
			var frozenDraft domaintask.Task
			if decodeErr := decodeSingleStrictJSON(goal.TaskPlan.HandoffInput.TaskDraft, &frozenDraft); decodeErr != nil || frozenDraft.TaskID != "" || frozenDraft.WorkstreamID != modulecore.WorkstreamID(goal.WorkstreamID) || frozenDraft.GoalID != modulecore.GoalID(goal.GoalID) {
				return ErrConflict
			}
		}
		return nil
	}
	var result domainworkstream.TaskPlanMutationResult
	if link {
		result, err = store.LinkTaskPlanHandoff(ctx, scope, request, authorize)
	} else {
		result, err = store.MarkTaskPlanHandoffUnknown(ctx, scope, request, authorize)
	}
	if err != nil {
		if link {
			return MutationResult{}, mapHandoffReceiptError(err)
		}
		return MutationResult{}, mapOwnerError(err)
	}
	return MutationResult{Item: s.item(result.Goal), Replayed: result.Replayed, ReceiptRef: result.ReceiptRef}, nil
}

func mapHandoffReceiptError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, ErrConflict):
		return ErrConflict
	case errors.Is(err, domainworkstream.ErrTaskPlanConflict), errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptConflict):
		return ErrConflict
	case errors.Is(err, ErrForbidden), errors.Is(err, domainworkstream.ErrTaskPlanForbidden), errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptForbidden), errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired):
		return ErrForbidden
	case errors.Is(err, ErrInvalid), errors.Is(err, domainworkstream.ErrTaskPlanInvalid):
		return ErrInvalid
	case errors.Is(err, ErrNotFound), errors.Is(err, domainworkstream.ErrTaskPlanNotFound):
		return ErrNotFound
	case errors.Is(err, ErrUnavailable), errors.Is(err, domainworkstream.ErrTaskPlanUnavailable), errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptCorrupt), errors.Is(err, domaintask.ErrNotFound):
		return ErrUnavailable
	case errors.Is(err, ErrUnsupported), errors.Is(err, domainworkstream.ErrTaskPlanUnsupported), errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptUnsupported):
		return ErrUnsupported
	default:
		// An absent/corrupt receipt or unexpected owner failure leaves the
		// handoff uncertain. Do not turn it into a permission denial or expose
		// storage details through a caller-facing error.
		return ErrUnavailable
	}
}

func (s *Service) authorizeHandoffActor(scope tool.ToolExecutionScope, goal domainworkstream.Goal, revision, epoch uint64) error {
	if err := s.authorizeVisibleGoal(scope, goal); err != nil {
		return err
	}
	if goal.TaskPlan == nil || goal.TaskPlan.Revision != revision || goal.TaskPlan.AssignmentEpoch != epoch || goal.TaskPlan.Assignee != scope.ActorID || !domainworkstream.GoalStatusOpen(goal.Status) {
		return ErrConflict
	}
	if !goal.TaskPlan.Timing.Ready(s.now()) {
		return fmt.Errorf("%w: task timing does not permit handoff", ErrConflict)
	}
	return nil
}

func (s *Service) authorizeHandoffReconciliation(scope tool.ToolExecutionScope, goal domainworkstream.Goal, revision, epoch uint64) error {
	if err := s.authorizeVisibleGoal(scope, goal); err != nil {
		return err
	}
	if goal.TaskPlan == nil || goal.TaskPlan.Revision != revision || goal.TaskPlan.AssignmentEpoch != epoch || goal.TaskPlan.Assignee != scope.ActorID || goal.TaskPlan.HandoffInput == nil {
		return ErrConflict
	}
	return nil
}

func normalizeTaskPlanMovieLookup(payload json.RawMessage) (moviecatalog.LookupRequest, error) {
	var request moviecatalog.LookupRequest
	if err := decodeSingleStrictJSON(payload, &request); err != nil {
		return moviecatalog.LookupRequest{}, err
	}
	return moviecatalog.NormalizeLookupRequest(request)
}

func decodeSingleStrictJSON(raw []byte, target any) error {
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateFrozenMovieLookupDraft(goal domainworkstream.Goal, draft domaintask.Task, shared domaintask.SharedRoleContext) error {
	plan := goal.TaskPlan
	if plan == nil || draft.TaskID != "" || shared.TaskID != "" || draft.Title != goal.Title ||
		string(draft.WorkstreamID) != goal.WorkstreamID || string(draft.GoalID) != goal.GoalID ||
		draft.Assignee != plan.Assignee || draft.OwnerID != plan.Assignee || !draft.ReadOnly ||
		(draft.Status != "" && draft.Status != domaintask.StatusQueued) || draft.Summary != "" || draft.WaitingReason != "" ||
		draft.ModuleID != "" || draft.ModuleRoot != "" || draft.Route != "" || draft.RoutingEventID != "" || draft.AssignmentEventID != "" ||
		len(draft.CoderRoles) != 0 || draft.Priority != "" || draft.ParentTaskID != "" || len(draft.DependencyTaskIDs) != 0 ||
		draft.OriginSessionID != "" || draft.OriginThreadID != "" || draft.OriginTurnID != "" || draft.OriginMessageID != "" || draft.OriginPublication != nil ||
		draft.AcceptedOPSClaim != nil || draft.ExpectedCriteriaRevision != "" || len(draft.NativeResumeClaims) != 0 || draft.SupersedesTaskID != "" ||
		draft.InterruptPolicy != "" || !draft.CreatedAt.IsZero() || !draft.UpdatedAt.IsZero() || draft.StartedAt != nil || draft.FinishedAt != nil ||
		len(draft.NextActions) != 0 || len(draft.Evidence) != 0 || len(draft.Artifacts) != 0 {
		return errors.New("Task identity, routing and typed read-only shape must match the Goal")
	}
	if plan.DataScope == domainworkstream.TaskPlanDataScopeUser {
		if draft.PrivateAccess == nil || draft.PrivateAccess.Validate() != nil || draft.PrivateAccess.Scope != domaintask.TaskAccessScopeUser || draft.PrivateAccess.AuthenticatedUserID != plan.UserID {
			return domaintask.ErrPrivateTaskAccessInvalid
		}
	} else if draft.PrivateAccess != nil {
		return errors.New("public lookup Task must not carry private access metadata")
	}
	if shared.UserIntent != "" || shared.ModuleID != "" || shared.ModuleRoot != "" || len(shared.RelevantFiles) != 0 ||
		len(shared.Decisions) != 0 || len(shared.Constraints) != 0 || shared.CurrentPlan != "" || shared.LatestStatus != "" ||
		len(shared.Artifacts) != 0 || shared.HandoffNotes != "" || !shared.UpdatedAt.IsZero() {
		return errors.New("movie lookup must use the typed payload without freeform Worker instructions")
	}
	return nil
}

func validateFrozenMovieLookupProvenance(goal domainworkstream.Goal, draft domaintask.Task, preparedRevision, assignmentEpoch uint64) error {
	plan := goal.TaskPlan
	if plan == nil || plan.KindID != domainworkstream.TaskPlanKindMovieCatalogLookup || len(plan.KindInput) == 0 || preparedRevision == 0 {
		return errors.New("typed lookup source is incomplete")
	}
	lookup, err := normalizeTaskPlanMovieLookup(plan.KindInput)
	if err != nil {
		return fmt.Errorf("stored kind input is invalid: %w", err)
	}
	kindInput, err := json.Marshal(lookup)
	if err != nil || !bytes.Equal(kindInput, plan.KindInput) {
		return errors.New("stored kind input is not canonical")
	}
	kindInputDigest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		return err
	}
	if draft.Timing == nil || !reflect.DeepEqual(*draft.Timing, plan.Timing) {
		return errors.New("Task timing differs from Goal timing")
	}
	timingDigest, err := domaintask.TaskTimingSHA256(plan.Timing)
	if err != nil {
		return err
	}
	expected := domaintask.TaskCenterExecution{
		KindID: plan.KindID, KindInputVersion: plan.KindInputVersion, KindInputSHA256: kindInputDigest,
		SourceGoalID: modulecore.GoalID(goal.GoalID), PreparedRevision: preparedRevision,
		AssignmentEpoch: assignmentEpoch, TimingSHA256: timingDigest,
	}
	if draft.TaskCenterExecution == nil || *draft.TaskCenterExecution != expected {
		return errors.New("Task descriptor differs from the frozen Goal snapshot")
	}
	return draft.TaskCenterExecution.Validate(draft)
}

func (s *Service) Claim(ctx context.Context, input ClaimInput) (MutationResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	if scope.ActorKind != tool.ActorKindAgent {
		return MutationResult{}, fmt.Errorf("%w: only a registered Agent can claim a task", ErrForbidden)
	}
	if input.ExpectedRevision == 0 || !validIdempotencyKey(input.IdempotencyKey) {
		return MutationResult{}, fmt.Errorf("%w: expected_revision and idempotency_key are required", ErrInvalid)
	}
	if input.ExpectedPolicyRevision == "" {
		return MutationResult{}, fmt.Errorf("%w: expected_policy_revision is required", ErrInvalid)
	}
	request := domainworkstream.TaskPlanClaim{
		GoalID: input.GoalID, ExpectedRevision: input.ExpectedRevision,
		ExpectedPolicyRevision: input.ExpectedPolicyRevision, IdempotencyKey: input.IdempotencyKey,
	}
	result, err := s.store.ClaimTaskPlan(ctx, scope, request, func(goal domainworkstream.Goal) error {
		if err := s.checkPolicyRevision(input.ExpectedPolicyRevision); err != nil {
			return err
		}
		if err := s.authorizeVisibleGoal(scope, goal); err != nil {
			return err
		}
		plan := goal.TaskPlan
		if plan == nil || plan.Assignee != domainworkstream.TaskPlanAssigneeUnassigned || !domainworkstream.GoalStatusClaimReady(goal.Status) {
			return ErrConflict
		}
		if !s.executorAvailable(plan.KindID) {
			return fmt.Errorf("%w: %s", ErrUnsupported, s.kindSet[plan.KindID].UnavailableReason)
		}
		if kind := s.kindSet[plan.KindID]; kind.RequiresTypedInput && (len(plan.KindInput) == 0 || plan.KindInputVersion == 0) {
			return fmt.Errorf("%w: typed_input_unavailable", ErrUnsupported)
		}
		if !plan.Timing.Ready(s.now()) {
			return fmt.Errorf("%w: task timing does not permit claim yet", ErrConflict)
		}
		allowed, allowErr := s.allowed(plan.KindID, scope.ActorID, plan.SubsetAllowedAgents)
		if allowErr != nil || !allowed {
			return ErrForbidden
		}
		return nil
	})
	if err != nil {
		return MutationResult{}, mapOwnerError(err)
	}
	return MutationResult{Item: s.item(result.Goal), Replayed: result.Replayed, ReceiptRef: result.ReceiptRef}, nil
}

func (s *Service) Release(ctx context.Context, input ReleaseInput) (MutationResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	if input.ExpectedRevision == 0 || !validIdempotencyKey(input.IdempotencyKey) {
		return MutationResult{}, fmt.Errorf("%w: expected_revision and idempotency_key are required", ErrInvalid)
	}
	if input.ExpectedPolicyRevision == "" {
		return MutationResult{}, fmt.Errorf("%w: expected_policy_revision is required", ErrInvalid)
	}
	request := domainworkstream.TaskPlanRelease{
		GoalID: input.GoalID, ExpectedRevision: input.ExpectedRevision,
		ExpectedPolicyRevision: input.ExpectedPolicyRevision, IdempotencyKey: input.IdempotencyKey,
	}
	result, err := s.store.ReleaseTaskPlan(ctx, scope, request, func(goal domainworkstream.Goal) error {
		if err := s.checkPolicyRevision(input.ExpectedPolicyRevision); err != nil {
			return err
		}
		return s.authorizeRelease(scope, goal)
	})
	if err != nil {
		return MutationResult{}, mapOwnerError(err)
	}
	return MutationResult{Item: s.item(result.Goal), Replayed: result.Replayed, ReceiptRef: result.ReceiptRef}, nil
}

func (s *Service) List(ctx context.Context, query ListQuery) (ListResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return ListResult{}, err
	}
	if query.View == ViewDaily {
		return ListResult{}, ErrUnsupported
	}
	switch query.View {
	case "", ViewRemaining, ViewClaimable, ViewHistory:
	default:
		return ListResult{}, fmt.Errorf("%w: unknown task-center view", ErrInvalid)
	}
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 200 {
		return ListResult{}, fmt.Errorf("%w: limit exceeds 200", ErrInvalid)
	}
	if query.View == ViewClaimable {
		if scope.ActorKind != tool.ActorKindAgent || !registeredAgent(s.actorSet, scope.ActorID) {
			return ListResult{}, fmt.Errorf("%w: claimable view requires a registered Agent", ErrForbidden)
		}
		if query.Assignee != "" && query.Assignee != domainworkstream.TaskPlanAssigneeUnassigned {
			return ListResult{}, fmt.Errorf("%w: claimable view is fixed to unassigned tasks", ErrInvalid)
		}
		query.Assignee = domainworkstream.TaskPlanAssigneeUnassigned
	}
	if scope.ActorKind == tool.ActorKindAgent {
		if query.Assignee != "" && query.Assignee != scope.ActorID && !(query.View == ViewClaimable && query.Assignee == domainworkstream.TaskPlanAssigneeUnassigned) {
			return ListResult{}, ErrForbidden
		}
		if query.View == "" || query.View == ViewRemaining {
			query.Assignee = scope.ActorID
		}
	}
	domainQuery := domainworkstream.TaskPlanQuery{View: query.View, Assignee: query.Assignee, Cursor: query.Cursor, Limit: query.Limit}
	goals, nextCursor, partial, err := s.store.ListTaskPlans(ctx, scope, domainQuery)
	if err != nil {
		return ListResult{}, mapOwnerError(err)
	}
	items := make([]Item, 0, len(goals))
	for _, goal := range goals {
		if goal.TaskPlan == nil {
			continue
		}
		if query.View == ViewClaimable && !s.claimable(scope, goal) {
			continue
		}
		if query.View == ViewHistory && !domainworkstream.GoalStatusTerminal(goal.Status) {
			continue
		}
		if (query.View == "" || query.View == ViewRemaining) && !taskPlanOpen(goal) {
			continue
		}
		item := s.listItem(goal)
		item.CanAcceptResult = canAcceptTaskPlanResult(scope, goal)
		items = append(items, item)
	}
	return ListResult{Items: items, NextCursor: nextCursor, Partial: partial, PolicyRevision: s.policyRevision}, nil
}

func (s *Service) Capabilities(ctx context.Context) (Capabilities, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return Capabilities{}, err
	}
	capabilities := Capabilities{
		Kinds: make([]KindCapability, 0, len(s.kinds)), Agents: append([]string(nil), s.actors...),
		PolicyRevision: s.policyRevision, OwnerRef: OwnerRefWorkstream, Revision: 1, ActorKind: string(scope.ActorKind),
		ManagedAvailable: true, DailyAvailable: false, CanSubmit: scope.ActorKind == tool.ActorKindUser,
		DailyReasonCode: "daily_reader_unavailable",
	}
	for _, kind := range s.kinds {
		allowedAgents := make([]string, 0, len(s.actors))
		for _, actorID := range s.actors {
			allowed, allowErr := s.allowed(kind.ID, actorID, nil)
			if allowErr == nil && allowed {
				allowedAgents = append(allowedAgents, actorID)
			}
		}
		capabilities.Kinds = append(capabilities.Kinds, KindCapability{
			KindID: kind.ID, Label: kind.Label, InputSchema: append(json.RawMessage(nil), kind.InputSchema...), AllowedAgents: allowedAgents,
			RequiresTypedInput: kind.RequiresTypedInput, ExecutorAvailable: s.executorAvailable(kind.ID), UnavailableReason: kind.UnavailableReason,
		})
		if scope.ActorKind == tool.ActorKindAgent {
			if allowed, allowErr := s.allowed(kind.ID, scope.ActorID, nil); allowErr == nil && allowed && scopeCanClaim(scope) && s.executorAvailable(kind.ID) {
				capabilities.CanClaim = true
			}
			if allowed, allowErr := s.allowed(kind.ID, scope.ActorID, nil); allowErr == nil && allowed {
				capabilities.CanSubmit = true
			}
		}
	}
	return capabilities, nil
}

func (s *Service) Get(ctx context.Context, goalID string) (Item, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return Item{}, err
	}
	if strings.TrimSpace(goalID) == "" {
		return Item{}, fmt.Errorf("%w: goal_id is required", ErrInvalid)
	}
	goal, err := s.store.GetTaskPlan(ctx, scope, goalID)
	if err != nil {
		return Item{}, mapOwnerError(err)
	}
	if err := s.authorizeVisibleGoal(scope, goal); err != nil {
		return Item{}, ErrNotFound
	}
	item := s.item(goal)
	item.CanAcceptResult = canAcceptTaskPlanResult(scope, goal)
	return item, nil
}

// AcceptResult records an explicit decision by the exact user who created a
// Goal. Task/Run and Artifact proofs are read before the Goal owner's CAS;
// the CAS callback rechecks only local immutable source and creator fields.
func (s *Service) AcceptResult(ctx context.Context, input AcceptResultInput) (MutationResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	if scope.ActorKind != tool.ActorKindUser || input.GoalID == "" || input.ExpectedRevision == 0 || input.ExpectedRevision == ^uint64(0) || !validIdempotencyKey(input.IdempotencyKey) {
		return MutationResult{}, ErrForbidden
	}
	store, ok := s.store.(TaskPlanExecutionResultStore)
	if !ok {
		return MutationResult{}, ErrUnsupported
	}
	key := taskCenterAcceptIdempotencyKey(input.IdempotencyKey)
	if receipt, found, err := s.store.TaskPlanReceipt(ctx, scope, "execution_result_accept", key); err != nil {
		return MutationResult{}, mapOwnerError(err)
	} else if found {
		plan := receipt.Goal.TaskPlan
		if receipt.Goal.GoalID != input.GoalID || plan == nil || plan.Revision != input.ExpectedRevision+1 || plan.AssignmentEpoch != input.ExpectedAssignmentEpoch || plan.ExecutionResult == nil || plan.ExecutionResult.Verification != domainworkstream.TaskPlanResultVerificationAccepted || plan.CreatorKind != string(tool.ActorKindUser) || plan.CreatedBy != scope.AuthenticatedUserID {
			return MutationResult{}, ErrConflict
		}
		return MutationResult{Item: s.item(receipt.Goal), Replayed: true, ReceiptRef: receipt.ReceiptRef}, nil
	}
	goal, err := s.store.GetTaskPlan(ctx, scope, input.GoalID)
	if err != nil {
		return MutationResult{}, mapOwnerError(err)
	}
	if err := s.authorizeVisibleGoal(scope, goal); err != nil {
		return MutationResult{}, ErrNotFound
	}
	plan := goal.TaskPlan
	if plan == nil || plan.Revision != input.ExpectedRevision || plan.AssignmentEpoch != input.ExpectedAssignmentEpoch || plan.CreatorKind != string(tool.ActorKindUser) || plan.CreatedBy != scope.AuthenticatedUserID ||
		(plan.DataScope == domainworkstream.TaskPlanDataScopeUser && plan.UserID != scope.AuthenticatedUserID) || plan.ExecutionResult == nil || plan.ExecutionResult.Verification != domainworkstream.TaskPlanResultVerificationPending || plan.TaskID == "" || plan.RunID == "" || plan.HandoffInput == nil {
		return MutationResult{}, ErrForbidden
	}
	if s.nativeExecution == nil {
		return MutationResult{}, ErrUnsupported
	}
	reference, err := resultReferenceFromGoal(goal)
	if err != nil {
		return MutationResult{}, err
	}
	task, run, err := s.nativeExecution.verifySuccessfulResult(ctx, scope, goal, referenceTaskFromFrozen(goal), domaintask.Run{TaskID: plan.TaskID, RunID: plan.RunID}, reference)
	if err != nil {
		return MutationResult{}, err
	}
	storedReference, result, err := s.nativeExecution.Results.GetTaskCenterExecutionResult(ctx, reference, func(checkCtx context.Context, stored domaintask.TaskCenterResultReference) error {
		return s.nativeExecution.authorizeResultScope(checkCtx, scope, goal.GoalID, stored, task.TaskID, run.RunID)
	})
	if err != nil {
		return MutationResult{}, mapHandoffReceiptError(err)
	}
	if storedReference != reference {
		return MutationResult{}, ErrConflict
	}
	digest := sha256.Sum256(result)
	if hex.EncodeToString(digest[:]) != reference.ResultSHA256 {
		return MutationResult{}, ErrUnavailable
	}
	criteria, err := domainworkstream.TaskPlanCriteriaSHA256(goal.SuccessCriteria, goal.Verification)
	if err != nil || plan.ExecutionResult.CriteriaSHA256 != criteria {
		return MutationResult{}, ErrConflict
	}
	request := domainworkstream.TaskPlanExecutionResultAcceptance{
		GoalID: goal.GoalID, ExpectedRevision: input.ExpectedRevision, ExpectedAssignmentEpoch: input.ExpectedAssignmentEpoch,
		IdempotencyKey: key, ArtifactID: reference.ArtifactID, TaskID: reference.TaskID, RunID: reference.RunID,
		KindInputSHA256: reference.KindInputSHA256, ResultSHA256: reference.ResultSHA256, CriteriaSHA256: criteria,
	}
	resultMutation, err := store.AcceptTaskPlanExecutionResult(ctx, scope, request, func(current domainworkstream.Goal) error {
		if !acceptanceGoalMatches(scope, current, reference, criteria) {
			return ErrForbidden
		}
		return nil
	})
	if err != nil {
		return MutationResult{}, mapOwnerError(err)
	}
	return MutationResult{Item: s.item(resultMutation.Goal), Replayed: resultMutation.Replayed, ReceiptRef: resultMutation.ReceiptRef}, nil
}

func acceptanceGoalMatches(scope tool.ToolExecutionScope, goal domainworkstream.Goal, reference domaintask.TaskCenterResultReference, criteria string) bool {
	if scope.ActorKind != tool.ActorKindUser || scope.AuthenticatedUserID == "" || goal.TaskPlan == nil {
		return false
	}
	plan := goal.TaskPlan
	if plan.CreatorKind != string(tool.ActorKindUser) || plan.CreatedBy != scope.AuthenticatedUserID ||
		(plan.DataScope == domainworkstream.TaskPlanDataScopeUser && plan.UserID != scope.AuthenticatedUserID) || plan.HandoffState != domainworkstream.TaskPlanHandoffLinked ||
		plan.TaskID != reference.TaskID || plan.RunID != reference.RunID || plan.ExecutionResult == nil || plan.ExecutionResult.Verification != domainworkstream.TaskPlanResultVerificationPending ||
		plan.ExecutionResult.ArtifactID != reference.ArtifactID || plan.ExecutionResult.KindInputSHA256 != reference.KindInputSHA256 || plan.ExecutionResult.ResultSHA256 != reference.ResultSHA256 || plan.ExecutionResult.CriteriaSHA256 != criteria {
		return false
	}
	currentCriteria, err := domainworkstream.TaskPlanCriteriaSHA256(goal.SuccessCriteria, goal.Verification)
	return err == nil && currentCriteria == criteria
}

func resultReferenceFromGoal(goal domainworkstream.Goal) (domaintask.TaskCenterResultReference, error) {
	if goal.TaskPlan == nil || goal.TaskPlan.ExecutionResult == nil || goal.TaskPlan.HandoffInput == nil {
		return domaintask.TaskCenterResultReference{}, ErrConflict
	}
	plan := goal.TaskPlan
	inputDigest, err := domaintask.TaskCenterKindInputSHA256(plan.HandoffInput.KindInput)
	if err != nil {
		return domaintask.TaskCenterResultReference{}, ErrConflict
	}
	ref := domaintask.TaskCenterResultReference{
		ArtifactID: plan.ExecutionResult.ArtifactID, SourceGoalID: modulecore.GoalID(goal.GoalID), WorkstreamID: modulecore.WorkstreamID(goal.WorkstreamID),
		TaskID: plan.TaskID, RunID: plan.RunID, KindID: plan.KindID, KindInputSHA256: inputDigest,
		ResultSHA256: plan.ExecutionResult.ResultSHA256, DataScope: plan.DataScope, UserID: plan.UserID,
	}
	task := referenceTaskFromFrozen(goal)
	task.TaskID = plan.TaskID
	if err := ref.Validate(task, domaintask.Run{TaskID: plan.TaskID, RunID: plan.RunID}); err != nil {
		return domaintask.TaskCenterResultReference{}, ErrConflict
	}
	return ref, nil
}

func referenceTaskFromFrozen(goal domainworkstream.Goal) domaintask.Task {
	var task domaintask.Task
	if goal.TaskPlan != nil && goal.TaskPlan.HandoffInput != nil {
		_ = decodeSingleStrictJSON(goal.TaskPlan.HandoffInput.TaskDraft, &task)
		if goal.TaskPlan.TaskID != "" {
			task.TaskID = goal.TaskPlan.TaskID
		}
	}
	return task
}

func taskCenterAcceptIdempotencyKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "taskcenter-accept:" + hex.EncodeToString(digest[:16])
}

func (s *Service) Receipt(ctx context.Context, operation, key string) (MutationResult, bool, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return MutationResult{}, false, err
	}
	if (operation != "submit" && operation != "claim" && operation != "release" && operation != "execution_result_accept") || !validIdempotencyKey(key) {
		return MutationResult{}, false, fmt.Errorf("%w: invalid receipt query", ErrInvalid)
	}
	ownerKey := key
	if operation == "execution_result_accept" {
		if scope.ActorKind != tool.ActorKindUser {
			return MutationResult{}, false, ErrForbidden
		}
		ownerKey = taskCenterAcceptIdempotencyKey(key)
	}
	receipt, found, err := s.store.TaskPlanReceipt(ctx, scope, operation, ownerKey)
	if err != nil {
		return MutationResult{}, false, mapOwnerError(err)
	}
	if !found {
		return MutationResult{}, false, ErrNotFound
	}
	return MutationResult{Item: s.item(receipt.Goal), Replayed: true, ReceiptRef: receipt.ReceiptRef}, true, nil
}

func canAcceptTaskPlanResult(scope tool.ToolExecutionScope, goal domainworkstream.Goal) bool {
	plan := goal.TaskPlan
	return scope.ActorKind == tool.ActorKindUser && scope.AuthenticatedUserID != "" && plan != nil && plan.CreatorKind == string(tool.ActorKindUser) && plan.CreatedBy == scope.AuthenticatedUserID &&
		(plan.DataScope != domainworkstream.TaskPlanDataScopeUser || plan.UserID == scope.AuthenticatedUserID) && plan.ExecutionResult != nil && plan.ExecutionResult.Verification == domainworkstream.TaskPlanResultVerificationPending
}

func (s *Service) scopeFromContext(ctx context.Context) (tool.ToolExecutionScope, error) {
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil {
		return tool.ToolExecutionScope{}, ErrForbidden
	}
	switch scope.ActorKind {
	case tool.ActorKindUser:
		if scope.ActorID != scope.AuthenticatedUserID || !scope.Allows(tool.DataScopeUser) {
			return tool.ToolExecutionScope{}, ErrForbidden
		}
	case tool.ActorKindAgent:
		if !registeredAgent(s.actorSet, scope.ActorID) {
			return tool.ToolExecutionScope{}, ErrForbidden
		}
	default:
		return tool.ToolExecutionScope{}, ErrForbidden
	}
	if !scope.Allows(tool.DataScopePublic) && !scope.Allows(tool.DataScopeUser) {
		return tool.ToolExecutionScope{}, ErrUnsupported
	}
	return scope, nil
}

func requestedScope(scope tool.ToolExecutionScope, requested string) (string, string, error) {
	if requested == "" {
		if scope.ActorKind == tool.ActorKindUser {
			requested = domainworkstream.TaskPlanDataScopeUser
		} else if scope.AuthenticatedUserID != "" && scope.Allows(tool.DataScopeUser) {
			requested = domainworkstream.TaskPlanDataScopeUser
		} else {
			requested = domainworkstream.TaskPlanDataScopePublic
		}
	}
	switch requested {
	case domainworkstream.TaskPlanDataScopePublic:
		if !scope.Allows(tool.DataScopePublic) {
			return "", "", ErrForbidden
		}
		return requested, "", nil
	case domainworkstream.TaskPlanDataScopeUser:
		if scope.AuthenticatedUserID == "" || !scope.Allows(tool.DataScopeUser) {
			return "", "", ErrForbidden
		}
		return requested, scope.AuthenticatedUserID, nil
	case domainworkstream.TaskPlanDataScopeInternal:
		return "", "", ErrUnsupported
	default:
		return "", "", fmt.Errorf("%w: invalid data_scope", ErrInvalid)
	}
}

func (s *Service) checkPolicyRevision(revision string) error {
	if revision == "" || revision != s.policyRevision {
		return fmt.Errorf("%w: task execution policy changed", ErrConflict)
	}
	return nil
}

func (s *Service) allowed(kindID, actorID string, subset *[]string) (bool, error) {
	kind, ok := s.kindSet[kindID]
	if !ok {
		return false, fmt.Errorf("unknown kind %q", kindID)
	}
	if len(kind.IntrinsicAllowedAgents) > 0 && !containsString(kind.IntrinsicAllowedAgents, actorID) {
		return false, nil
	}
	return s.policy.Allowed(kindID, actorID, s.actors, subset)
}

func (s *Service) executorAvailable(kindID string) bool {
	kind, ok := s.kindSet[kindID]
	return ok && kind.ExecutorAvailable
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (s *Service) authorizeSubmittedGoal(scope tool.ToolExecutionScope, dataScope, userID string, goal domainworkstream.Goal) error {
	if goal.TaskPlan == nil || goal.TaskPlan.DataScope != dataScope || goal.TaskPlan.UserID != userID {
		return ErrForbidden
	}
	if err := domainworkstream.ValidateGoal(goal); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, exists := s.kindSet[goal.TaskPlan.KindID]; !exists {
		return ErrInvalid
	}
	if goal.TaskPlan.SubsetAllowedAgents != nil {
		for _, actorID := range *goal.TaskPlan.SubsetAllowedAgents {
			allowed, err := s.allowed(goal.TaskPlan.KindID, actorID, nil)
			if err != nil || !allowed {
				return ErrForbidden
			}
		}
	}
	if goal.TaskPlan.Assignee == domainworkstream.TaskPlanAssigneeUnassigned {
		eligible := false
		for _, actorID := range s.actors {
			allowed, err := s.allowed(goal.TaskPlan.KindID, actorID, goal.TaskPlan.SubsetAllowedAgents)
			if err != nil {
				return fmt.Errorf("%w: cannot determine eligible agents: %v", ErrInvalid, err)
			}
			eligible = eligible || allowed
		}
		if !eligible {
			return fmt.Errorf("%w: shared task has no eligible agent", ErrInvalid)
		}
	}
	if goal.TaskPlan.Assignee != domainworkstream.TaskPlanAssigneeUnassigned {
		if !registeredAgent(s.actorSet, goal.TaskPlan.Assignee) {
			return ErrForbidden
		}
		if scope.ActorKind == tool.ActorKindAgent && goal.TaskPlan.Assignee != scope.ActorID {
			return ErrForbidden
		}
		allowed, err := s.allowed(goal.TaskPlan.KindID, goal.TaskPlan.Assignee, goal.TaskPlan.SubsetAllowedAgents)
		if err != nil || !allowed {
			return ErrForbidden
		}
	}
	return nil
}

func (s *Service) authorizeVisibleGoal(scope tool.ToolExecutionScope, goal domainworkstream.Goal) error {
	plan := goal.TaskPlan
	if plan == nil {
		return ErrNotFound
	}
	if plan.DataScope == domainworkstream.TaskPlanDataScopePublic && scope.Allows(tool.DataScopePublic) {
		return nil
	}
	if plan.DataScope == domainworkstream.TaskPlanDataScopeUser && scope.Allows(tool.DataScopeUser) && scope.AuthenticatedUserID != "" && scope.AuthenticatedUserID == plan.UserID {
		return nil
	}
	return ErrNotFound
}

func (s *Service) claimable(scope tool.ToolExecutionScope, goal domainworkstream.Goal) bool {
	if scope.ActorKind != tool.ActorKindAgent || s.authorizeVisibleGoal(scope, goal) != nil || !domainworkstream.GoalStatusClaimReady(goal.Status) ||
		goal.TaskPlan.Assignee != domainworkstream.TaskPlanAssigneeUnassigned || !goal.TaskPlan.Timing.Ready(s.now()) || !s.executorAvailable(goal.TaskPlan.KindID) {
		return false
	}
	if kind := s.kindSet[goal.TaskPlan.KindID]; kind.RequiresTypedInput && (len(goal.TaskPlan.KindInput) == 0 || goal.TaskPlan.KindInputVersion == 0) {
		return false
	}
	allowed, err := s.allowed(goal.TaskPlan.KindID, scope.ActorID, goal.TaskPlan.SubsetAllowedAgents)
	return err == nil && allowed
}

func (s *Service) item(goal domainworkstream.Goal) Item {
	plan := goal.TaskPlan
	if plan == nil {
		return Item{}
	}
	allowedAgents := []string{}
	if _, exists := s.kindSet[plan.KindID]; exists {
		for _, actorID := range s.actors {
			if allowed, err := s.allowed(plan.KindID, actorID, plan.SubsetAllowedAgents); err == nil && allowed {
				allowedAgents = append(allowedAgents, actorID)
			}
		}
	}
	sort.Strings(allowedAgents)
	reason := ""
	if taskPlanOpen(goal) && plan.Timing.DeadlineAt != nil && !s.now().Before(*plan.Timing.DeadlineAt) {
		reason = "deadline_passed"
	} else if taskPlanOpen(goal) && !s.executorAvailable(plan.KindID) {
		reason = s.kindSet[plan.KindID].UnavailableReason
	} else if taskPlanOpen(goal) && s.kindSet[plan.KindID].RequiresTypedInput && (len(plan.KindInput) == 0 || plan.KindInputVersion == 0) {
		reason = "typed_input_unavailable"
	} else if !plan.Timing.Ready(s.now()) {
		reason = "not_ready"
	}
	return Item{
		GoalID: goal.GoalID, Title: goal.Title, Description: goal.Description,
		KindInput: append(json.RawMessage(nil), plan.KindInput...), KindInputVersion: plan.KindInputVersion, SuccessCriteria: cloneStrings(goal.SuccessCriteria), Verification: cloneStrings(goal.Verification), KindID: plan.KindID, CreatorKind: plan.CreatorKind,
		Assignee: plan.Assignee, Status: goal.Status, ReasonCode: reason, WorkstreamID: goal.WorkstreamID,
		PolicyRevision: plan.PolicyRevision, TaskID: string(plan.TaskID), RunID: string(plan.RunID),
		ResultArtifactID: taskPlanResultArtifactID(plan), ResultSHA256: taskPlanResultDigest(plan), ExecutionVerification: taskPlanVerification(plan),
		Revision: plan.Revision, AssignmentEpoch: plan.AssignmentEpoch, Timing: plan.Timing,
		AllowedAgents: allowedAgents, DataScope: plan.DataScope, CreatedAt: goal.CreatedAt, UpdatedAt: plan.UpdatedAt,
		TimingStatus: "known", TimingSource: "workstream.goal.task_plan",
	}
}

func taskPlanResultArtifactID(plan *domainworkstream.TaskPlan) string {
	if plan == nil || plan.ExecutionResult == nil {
		return ""
	}
	return plan.ExecutionResult.ArtifactID
}

func taskPlanResultDigest(plan *domainworkstream.TaskPlan) string {
	if plan == nil || plan.ExecutionResult == nil {
		return ""
	}
	return plan.ExecutionResult.ResultSHA256
}

func taskPlanVerification(plan *domainworkstream.TaskPlan) string {
	if plan == nil || plan.ExecutionResult == nil {
		return ""
	}
	return plan.ExecutionResult.Verification
}

func (s *Service) listItem(goal domainworkstream.Goal) Item {
	item := s.item(goal)
	item.Description = taskCenterListDescriptionSummary(goal.Description)
	item.SuccessCriteria = nil
	item.Verification = nil
	item.KindInput = nil
	return item
}

func taskCenterListDescriptionSummary(description string) string {
	normalized := strings.Join(strings.Fields(description), " ")
	runes := []rune(normalized)
	if len(runes) <= taskCenterListDescriptionMaxRunes {
		return normalized
	}
	return strings.TrimSpace(string(runes[:taskCenterListDescriptionMaxRunes-1])) + "…"
}

func (s *Service) authorizeRelease(scope tool.ToolExecutionScope, goal domainworkstream.Goal) error {
	if err := s.authorizeVisibleGoal(scope, goal); err != nil {
		return err
	}
	plan := goal.TaskPlan
	if !domainworkstream.GoalStatusOpen(goal.Status) {
		return ErrConflict
	}
	if plan.HandoffState != domainworkstream.TaskPlanHandoffNone || plan.TaskID != "" || plan.RunID != "" {
		return fmt.Errorf("%w: a handed-off task cannot be released before execution-owner reconciliation", ErrUnavailable)
	}
	if scope.ActorKind == tool.ActorKindAgent && plan.Assignee != scope.ActorID {
		return ErrForbidden
	}
	if scope.ActorKind == tool.ActorKindUser && plan.DataScope == domainworkstream.TaskPlanDataScopeUser && plan.UserID != scope.AuthenticatedUserID {
		return ErrForbidden
	}
	if scope.ActorKind == tool.ActorKindUser && plan.DataScope != domainworkstream.TaskPlanDataScopeUser && (plan.CreatorKind != string(scope.ActorKind) || plan.CreatedBy != scope.ActorID) {
		return ErrForbidden
	}
	return nil
}

func scopeCanClaim(scope tool.ToolExecutionScope) bool {
	return scope.ActorKind == tool.ActorKindAgent &&
		(scope.Allows(tool.DataScopePublic) || (scope.AuthenticatedUserID != "" && scope.Allows(tool.DataScopeUser)))
}

func clonePolicy(policy taskexecution.Policy) taskexecution.Policy {
	cloned := taskexecution.Policy{SchemaVersion: policy.SchemaVersion, TaskKinds: make(map[string]taskexecution.KindPolicy, len(policy.TaskKinds))}
	for kindID, kindPolicy := range policy.TaskKinds {
		kindPolicy.AllowedAgents.ActorIDs = append([]string(nil), kindPolicy.AllowedAgents.ActorIDs...)
		cloned.TaskKinds[kindID] = kindPolicy
	}
	return cloned
}

func allTextPresent(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func taskPlanTitle(title, description string, criteria, verification []string) string {
	if strings.TrimSpace(title) != "" {
		return shortenTitle(title)
	}
	content := strings.TrimSpace(description)
	if content == "" {
		for _, candidate := range criteria {
			if strings.TrimSpace(candidate) != "" {
				content = candidate
				break
			}
		}
	}
	if content == "" {
		for _, candidate := range verification {
			if strings.TrimSpace(candidate) != "" {
				content = candidate
				break
			}
		}
	}
	return shortenTitle(content)
}

func shortenTitle(value string) string {
	fields := strings.Fields(value)
	value = strings.Join(fields, " ")
	runes := []rune(value)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return value
}

func taskPlanOpen(goal domainworkstream.Goal) bool {
	return domainworkstream.GoalStatusOpen(goal.Status)
}

func registeredAgent(actors map[string]struct{}, actorID string) bool {
	_, exists := actors[actorID]
	return exists
}

func validIdempotencyKey(key string) bool {
	return key != "" && len(key) <= 128 && key == strings.TrimSpace(key)
}

func cloneStrings(values []string) []string { return append([]string(nil), values...) }

func mapOwnerError(err error) error {
	switch {
	case errors.Is(err, domainworkstream.ErrTaskPlanConflict):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case errors.Is(err, domainworkstream.ErrTaskPlanForbidden):
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	case errors.Is(err, domainworkstream.ErrTaskPlanInvalid):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	case errors.Is(err, domainworkstream.ErrTaskPlanUnavailable):
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	case errors.Is(err, domainworkstream.ErrTaskPlanNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case errors.Is(err, domainworkstream.ErrTaskPlanUnsupported):
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	default:
		return err
	}
}
