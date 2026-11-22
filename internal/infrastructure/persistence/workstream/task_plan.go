package workstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskPlanOperationArtifactType = "task_operation"
const taskPlanReservationArtifactType = "task_operation_reservation"
const taskExecutionResultArtifactType = TaskCenterExecutionResultArtifactType
const taskMeetingOccurrenceArtifactType = "task_meeting_occurrence"
const taskMeetingOccurrenceReservationArtifactType = "task_meeting_occurrence_reservation"
const taskMeetingMinutesArtifactType = "task_meeting_minutes"
const taskMeetingMinutesReservationArtifactType = "task_meeting_minutes_reservation"
const gameProjectPlanReceiptArtifactType = "game_project_plan_registration"
const gameProjectPlanReservationArtifactType = "game_project_plan_registration_reservation"

func isTaskCenterInternalArtifactType(value string) bool {
	for _, reserved := range taskCenterInternalArtifactTypes() {
		if value == reserved {
			return true
		}
	}
	return false
}

func taskCenterInternalArtifactTypes() []string {
	return []string{
		taskPlanOperationArtifactType, taskPlanReservationArtifactType, taskExecutionResultArtifactType,
		taskMeetingOccurrenceArtifactType, taskMeetingOccurrenceReservationArtifactType,
		taskMeetingMinutesArtifactType, taskMeetingMinutesReservationArtifactType,
		gameProjectPlanReceiptArtifactType, gameProjectPlanReservationArtifactType,
	}
}

func taskCenterInternalArtifactTypePlaceholders() string {
	return strings.TrimSuffix(strings.Repeat("?,", len(taskCenterInternalArtifactTypes())), ",")
}

func taskCenterInternalArtifactTypeArgs() []any {
	values := taskCenterInternalArtifactTypes()
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}

func isTaskCenterInternalArtifactID(ctx context.Context, db *sql.DB, artifactID string) (bool, error) {
	if db == nil || artifactID == "" {
		return false, nil
	}
	var payload string
	err := db.QueryRowContext(ctx, `SELECT payload FROM artifact WHERE artifact_id = ?`, artifactID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var item domainworkstream.Artifact
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return true, err
	}
	return isTaskCenterInternalArtifactType(item.Type), nil
}

func (s *SQLiteStore) SubmitTaskPlan(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanSubmission, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if authorize == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanForbidden
	}
	if err := validateTaskPlanSubmission(scope, request); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	inputHash, err := hashTaskPlanSubmission(request)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, fmt.Errorf("hash task plan submission: %w", err)
	}
	if s == nil || s.db == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	receiptID, err := reserveTaskPlanOperationReceipt(ctx, tx, scope, "submit", request.IdempotencyKey)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	if prior, priorID, found, err := findTaskPlanOperationReceipt(ctx, tx, scope, "submit", request.IdempotencyKey); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	} else if found {
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifact WHERE artifact_id = ?`, receiptID); err != nil {
			return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
		}
		return replayTaskPlanReceipt(ctx, tx, scope, prior, priorID, "submit", request.IdempotencyKey, inputHash)
	}

	goal := request.Goal
	plan := *goal.TaskPlan
	workstreamID := request.WorkstreamID
	if workstreamID == "" {
		workstreamID, err = findOrCreateTaskCenterWorkstream(ctx, tx, plan.DataScope, plan.UserID, goal.CreatedAt)
		if err != nil {
			return domainworkstream.TaskPlanMutationResult{}, err
		}
	} else if err := requireManagedWorkstream(ctx, tx, workstreamID, plan.DataScope, plan.UserID); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	goal.GoalID = string(modulecore.NewGoalID())
	goal.WorkstreamID = workstreamID
	goal.TaskPlan = &plan
	if err := domainworkstream.ValidateGoal(goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanInvalid, err)
	}
	if err := authorize(goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if err := insertTaskPlanGoal(ctx, tx, goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	result, err := storeTaskPlanOperationReceipt(ctx, tx, receiptID, scope, "submit", request.IdempotencyKey, inputHash, goal)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	return result, nil
}

func (s *SQLiteStore) SubmitGameProjectPlan(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.GameProjectSubmission) (domainworkstream.ProjectPlanMutationResult, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	if scope.ActorKind != tool.ActorKindUser || scope.ActorID != scope.AuthenticatedUserID || scope.AuthenticatedUserID == "" || !scope.Allows(tool.DataScopePublic) {
		return domainworkstream.ProjectPlanMutationResult{}, domainworkstream.ErrTaskPlanForbidden
	}
	if err := request.Validate(); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanInvalid, err)
	}
	inputHash, err := hashTaskPlanValue(struct {
		Name        string                              `json:"name"`
		Description string                              `json:"description,omitempty"`
		Stages      []domainworkstream.GameProjectStage `json:"stages"`
	}{Name: request.Name, Description: request.Description, Stages: request.Stages})
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	if s == nil || s.db == nil {
		return domainworkstream.ProjectPlanMutationResult{}, domainworkstream.ErrTaskPlanUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	reservationID := string(modulecore.NewArtifactID())
	reservationPayload, err := json.Marshal(domainworkstream.ProjectPlanOperationReceipt{
		Version: 0, ActorKind: string(scope.ActorKind), ActorID: scope.ActorID,
		AuthenticatedUserID: scope.AuthenticatedUserID, Operation: "game_project_register",
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	reservation := domainworkstream.Artifact{
		ArtifactID: reservationID, Type: gameProjectPlanReservationArtifactType,
		Title: "Game project registration reservation", Status: "pending", Payload: reservationPayload, CreatedAt: time.Now().UTC(),
	}
	reservationJSON, err := json.Marshal(reservation)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, '', ?, ?)`, reservation.ArtifactID, reservation.CreatedAt.Format(timeFormatRFC3339Nano), string(reservationJSON)); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	prior, priorID, found, err := findGameProjectPlanReceipt(ctx, tx, scope, request.IdempotencyKey)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	if found {
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifact WHERE artifact_id = ? AND json_extract(payload, '$.artifact_type') = ?`, reservationID, gameProjectPlanReservationArtifactType); err != nil {
			return domainworkstream.ProjectPlanMutationResult{}, classifyTaskPlanStorageError(err)
		}
		if prior.InputHash != inputHash {
			return domainworkstream.ProjectPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
		}
		if err := tx.Commit(); err != nil {
			return domainworkstream.ProjectPlanMutationResult{}, classifyTaskPlanStorageError(err)
		}
		return domainworkstream.ProjectPlanMutationResult{Registration: prior.Registration, Replayed: true, ReceiptRef: priorID}, nil
	}

	now := time.Now().UTC()
	workstreamID := string(modulecore.NewWorkstreamID())
	domain := domainworkstream.ProjectDomainGame
	workstream := domainworkstream.Workstream{
		WorkstreamID: workstreamID, Name: request.Name, ProjectDomain: &domain,
		Description: request.Description, Status: domainworkstream.StatusActive, CreatedAt: now,
	}
	if err := domainworkstream.ValidateWorkstream(workstream); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanInvalid, err)
	}
	workstreamJSON, err := json.Marshal(workstream)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workstream (workstream_id, created_at, payload) VALUES (?, ?, ?)`, workstream.WorkstreamID, now.Format(timeFormatRFC3339Nano), string(workstreamJSON)); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	goalIDs := make([]string, len(request.Stages))
	stageKeys := make([]string, len(request.Stages))
	// Receipt arrays preserve stage order; Goal timestamps remain the real shared registration time.
	for index, stage := range request.Stages {
		goal := domainworkstream.Goal{
			GoalID: string(modulecore.NewGoalID()), WorkstreamID: workstreamID,
			Title: stage.Title, Description: stage.Description,
			SuccessCriteria: append([]string(nil), stage.SuccessCriteria...), Verification: append([]string(nil), stage.Verification...),
			Status: domainworkstream.StatusDraft, CreatedAt: now,
		}
		if err := domainworkstream.ValidateGoal(goal); err != nil {
			return domainworkstream.ProjectPlanMutationResult{}, fmt.Errorf("%w: stage %q: %v", domainworkstream.ErrTaskPlanInvalid, stage.Key, err)
		}
		goalJSON, err := json.Marshal(goal)
		if err != nil {
			return domainworkstream.ProjectPlanMutationResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workstream_goal (goal_id, workstream_id, created_at, payload) VALUES (?, ?, ?, ?)`, goal.GoalID, workstreamID, goal.CreatedAt.Format(timeFormatRFC3339Nano), string(goalJSON)); err != nil {
			return domainworkstream.ProjectPlanMutationResult{}, classifyTaskPlanStorageError(err)
		}
		goalIDs[index], stageKeys[index] = goal.GoalID, stage.Key
	}
	registration := domainworkstream.ProjectPlanRegistration{
		WorkstreamID: workstreamID, GoalIDs: goalIDs, StageKeys: stageKeys, InputSHA256: inputHash, RegisteredAt: now,
	}
	receipt := domainworkstream.ProjectPlanOperationReceipt{
		Version: 1, ActorKind: string(scope.ActorKind), ActorID: scope.ActorID, AuthenticatedUserID: scope.AuthenticatedUserID,
		Operation: "game_project_register", IdempotencyKey: request.IdempotencyKey, InputHash: inputHash, Registration: registration,
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	artifact := domainworkstream.Artifact{
		ArtifactID: reservationID, WorkstreamID: workstreamID, Type: gameProjectPlanReceiptArtifactType,
		Title: "Game project registration receipt", Status: "stored", Payload: receiptPayload, CreatedAt: now,
	}
	if err := domainworkstream.ValidateArtifact(artifact); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE artifact SET workstream_id = ?, created_at = ?, payload = ? WHERE artifact_id = ? AND json_extract(payload, '$.artifact_type') = ?`, workstreamID, now.Format(timeFormatRFC3339Nano), string(artifactJSON), reservationID, gameProjectPlanReservationArtifactType)
	if err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return domainworkstream.ProjectPlanMutationResult{}, err
		}
		return domainworkstream.ProjectPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
	}
	if err := tx.Commit(); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	return domainworkstream.ProjectPlanMutationResult{Registration: registration, ReceiptRef: reservationID}, nil
}

func (s *SQLiteStore) ClaimTaskPlan(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanClaim, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if authorize == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanForbidden
	}
	if scope.ActorKind != tool.ActorKindAgent || request.GoalID == "" || request.ExpectedRevision == 0 || !validTaskPlanKey(request.IdempotencyKey) || request.ExpectedPolicyRevision == "" {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanInvalid
	}
	inputHash, err := hashTaskPlanValue(request)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	return s.mutateTaskPlan(ctx, scope, "claim", request.IdempotencyKey, inputHash, request.GoalID, request.ExpectedRevision, request.ExpectedPolicyRevision, authorize)
}

func (s *SQLiteStore) ReleaseTaskPlan(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanRelease, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if authorize == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanForbidden
	}
	if request.GoalID == "" || request.ExpectedRevision == 0 || !validTaskPlanKey(request.IdempotencyKey) || request.ExpectedPolicyRevision == "" {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanInvalid
	}
	inputHash, err := hashTaskPlanValue(request)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	return s.mutateTaskPlan(ctx, scope, "release", request.IdempotencyKey, inputHash, request.GoalID, request.ExpectedRevision, request.ExpectedPolicyRevision, authorize)
}

func (s *SQLiteStore) PrepareTaskPlanHandoff(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanHandoffPrepare, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	if request.ExpectedRevision == ^uint64(0) || request.HandoffKey.Validate() != nil || request.Input.Revision != request.ExpectedRevision+1 || request.Input.AssignmentEpoch != request.ExpectedAssignmentEpoch {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanInvalid
	}
	return s.mutateTaskPlanHandoff(ctx, scope, "handoff_prepare", request.IdempotencyKey, request, request.GoalID, request.ExpectedRevision, request.ExpectedAssignmentEpoch, request.ExpectedPolicyRevision, authorize, func(goal *domainworkstream.Goal, plan *domainworkstream.TaskPlan) error {
		if !domainworkstream.GoalStatusOpen(goal.Status) || plan.Assignee != scope.ActorID || plan.HandoffState != domainworkstream.TaskPlanHandoffNone || plan.HandoffKey != "" || plan.HandoffInput != nil || plan.TaskID != "" || plan.RunID != "" {
			return domainworkstream.ErrTaskPlanConflict
		}
		input := request.Input
		input.TaskDraft = append([]byte(nil), request.Input.TaskDraft...)
		input.SharedContext = append([]byte(nil), request.Input.SharedContext...)
		input.KindInput = append([]byte(nil), request.Input.KindInput...)
		plan.Revision++
		if input.Revision != plan.Revision || input.KindID != plan.KindID {
			return domainworkstream.ErrTaskPlanConflict
		}
		plan.HandoffState = domainworkstream.TaskPlanHandoffPrepared
		plan.HandoffKey = string(request.HandoffKey)
		plan.HandoffInput = &input
		if err := validateTaskPlanHandoffSource(*goal, *plan, input); err != nil {
			return fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanInvalid, err)
		}
		return nil
	})
}

func (s *SQLiteStore) MarkTaskPlanHandoffUnknown(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanHandoffTransition, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	if request.HandoffKey.Validate() != nil || request.TaskID != "" || request.RunID != "" {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanInvalid
	}
	return s.mutateTaskPlanHandoff(ctx, scope, "handoff_unknown", request.IdempotencyKey, request, request.GoalID, request.ExpectedRevision, request.ExpectedAssignmentEpoch, request.ExpectedPolicyRevision, authorize, func(_ *domainworkstream.Goal, plan *domainworkstream.TaskPlan) error {
		if plan.HandoffKey != string(request.HandoffKey) || plan.HandoffInput == nil || plan.HandoffState != domainworkstream.TaskPlanHandoffPrepared || plan.TaskID != "" || plan.RunID != "" {
			return domainworkstream.ErrTaskPlanConflict
		}
		plan.HandoffState = domainworkstream.TaskPlanHandoffOutcomeUnknown
		plan.Revision++
		return nil
	})
}

func (s *SQLiteStore) LinkTaskPlanHandoff(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanHandoffTransition, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	return s.mutateTaskPlanHandoff(ctx, scope, "handoff_link", request.IdempotencyKey, request, request.GoalID, request.ExpectedRevision, request.ExpectedAssignmentEpoch, request.ExpectedPolicyRevision, authorize, func(goal *domainworkstream.Goal, plan *domainworkstream.TaskPlan) error {
		if request.HandoffKey.Validate() != nil || request.TaskID.Validate() != nil || request.RunID.Validate() != nil || plan.HandoffKey != string(request.HandoffKey) || plan.HandoffInput == nil || (plan.HandoffState != domainworkstream.TaskPlanHandoffPrepared && plan.HandoffState != domainworkstream.TaskPlanHandoffOutcomeUnknown) || plan.TaskID != "" || plan.RunID != "" {
			return domainworkstream.ErrTaskPlanConflict
		}
		var frozenDraft domaintask.Task
		if err := decodeTaskPlanStrictJSON(plan.HandoffInput.TaskDraft, &frozenDraft); err != nil || frozenDraft.TaskID != "" || frozenDraft.WorkstreamID != modulecore.WorkstreamID(goal.WorkstreamID) || frozenDraft.GoalID != modulecore.GoalID(goal.GoalID) {
			return domainworkstream.ErrTaskPlanConflict
		}
		plan.HandoffState = domainworkstream.TaskPlanHandoffLinked
		plan.TaskID = request.TaskID
		plan.RunID = request.RunID
		plan.Revision++
		return nil
	})
}

func (s *SQLiteStore) RecordTaskPlanExecutionResult(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanExecutionResultUpdate, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	if scope.ActorKind != tool.ActorKindAgent || request.ExpectedRevision == ^uint64(0) || request.TaskID.Validate() != nil || request.RunID.Validate() != nil ||
		!validTaskCenterSHA256(request.KindInputSHA256) || !validTaskCenterSHA256(request.ResultSHA256) || !validTaskCenterSHA256(request.CriteriaSHA256) {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanInvalid
	}
	return s.mutateTaskPlanHandoff(ctx, scope, "execution_result", request.IdempotencyKey, request, request.GoalID, request.ExpectedRevision, request.ExpectedAssignmentEpoch, request.ExpectedPolicyRevision, authorize, func(goal *domainworkstream.Goal, plan *domainworkstream.TaskPlan) error {
		artifactID, err := domaintask.TaskCenterResultArtifactID(request.TaskID, request.RunID)
		if err != nil || artifactID != request.ArtifactID || plan.HandoffState != domainworkstream.TaskPlanHandoffLinked || plan.TaskID != request.TaskID || plan.RunID != request.RunID || plan.HandoffInput == nil || plan.ExecutionResult != nil {
			return domainworkstream.ErrTaskPlanConflict
		}
		inputDigest, err := domaintask.TaskCenterKindInputSHA256(plan.HandoffInput.KindInput)
		criteriaDigest, criteriaErr := domainworkstream.TaskPlanCriteriaSHA256(goal.SuccessCriteria, goal.Verification)
		if err != nil || criteriaErr != nil || inputDigest != request.KindInputSHA256 || criteriaDigest != request.CriteriaSHA256 {
			return domainworkstream.ErrTaskPlanConflict
		}
		plan.ExecutionResult = &domainworkstream.TaskPlanExecutionResult{
			ArtifactID: request.ArtifactID, KindInputSHA256: request.KindInputSHA256, ResultSHA256: request.ResultSHA256,
			CriteriaSHA256: request.CriteriaSHA256, Verification: domainworkstream.TaskPlanResultVerificationPending,
		}
		plan.Revision++
		return nil
	})
}

func (s *SQLiteStore) AcceptTaskPlanExecutionResult(ctx context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanExecutionResultAcceptance, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	if scope.ActorKind != tool.ActorKindUser || request.ExpectedRevision == ^uint64(0) || request.TaskID.Validate() != nil || request.RunID.Validate() != nil ||
		!validTaskCenterSHA256(request.KindInputSHA256) || !validTaskCenterSHA256(request.ResultSHA256) || !validTaskCenterSHA256(request.CriteriaSHA256) {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanInvalid
	}
	return s.mutateTaskPlanHandoff(ctx, scope, "execution_result_accept", request.IdempotencyKey, request, request.GoalID, request.ExpectedRevision, request.ExpectedAssignmentEpoch, "", authorize, func(goal *domainworkstream.Goal, plan *domainworkstream.TaskPlan) error {
		result := plan.ExecutionResult
		artifactID, err := domaintask.TaskCenterResultArtifactID(request.TaskID, request.RunID)
		if err != nil || artifactID != request.ArtifactID || plan.CreatorKind != string(tool.ActorKindUser) || plan.CreatedBy != scope.AuthenticatedUserID || result == nil ||
			plan.HandoffState != domainworkstream.TaskPlanHandoffLinked || plan.TaskID != request.TaskID || plan.RunID != request.RunID ||
			result.ArtifactID != request.ArtifactID || result.KindInputSHA256 != request.KindInputSHA256 || result.ResultSHA256 != request.ResultSHA256 || result.CriteriaSHA256 != request.CriteriaSHA256 || result.Verification != domainworkstream.TaskPlanResultVerificationPending {
			return domainworkstream.ErrTaskPlanConflict
		}
		result.Verification = domainworkstream.TaskPlanResultVerificationAccepted
		result.AcceptedBy = scope.AuthenticatedUserID
		result.AcceptedAt = time.Now().UTC()
		goal.Status = domainworkstream.StatusCompleted
		goal.CompletedAt = result.AcceptedAt
		plan.Revision++
		return nil
	})
}

func validateTaskPlanHandoffSource(goal domainworkstream.Goal, plan domainworkstream.TaskPlan, input domainworkstream.TaskPlanHandoffInput) error {
	var draft domaintask.Task
	if err := decodeTaskPlanStrictJSON(input.TaskDraft, &draft); err != nil || draft.TaskID != "" ||
		draft.WorkstreamID != modulecore.WorkstreamID(goal.WorkstreamID) || draft.GoalID != modulecore.GoalID(goal.GoalID) ||
		draft.Title != goal.Title || draft.Assignee != plan.Assignee || draft.OwnerID != plan.Assignee || !draft.ReadOnly ||
		(draft.Status != "" && draft.Status != domaintask.StatusQueued) || draft.ModuleID != "" || draft.ModuleRoot != "" || draft.Route != "" || draft.RoutingEventID != "" || draft.AssignmentEventID != "" ||
		len(draft.CoderRoles) != 0 || draft.Priority != "" || draft.ParentTaskID != "" || len(draft.DependencyTaskIDs) != 0 ||
		draft.OriginSessionID != "" || draft.OriginThreadID != "" || draft.OriginTurnID != "" || draft.OriginMessageID != "" || draft.OriginPublication != nil ||
		draft.AcceptedOPSClaim != nil || draft.ExpectedCriteriaRevision != "" || len(draft.NativeResumeClaims) != 0 || draft.SupersedesTaskID != "" ||
		draft.InterruptPolicy != "" || !draft.CreatedAt.IsZero() || !draft.UpdatedAt.IsZero() || draft.StartedAt != nil || draft.FinishedAt != nil ||
		draft.Summary != "" || draft.WaitingReason != "" || len(draft.NextActions) != 0 || len(draft.Evidence) != 0 || len(draft.Artifacts) != 0 {
		return errors.New("frozen Task must bind the source Goal, Workstream, owner and read-only kind")
	}
	if input.Revision != plan.Revision || input.AssignmentEpoch != plan.AssignmentEpoch || input.KindID != plan.KindID || input.KindInputVersion != plan.KindInputVersion || !bytes.Equal(input.KindInput, plan.KindInput) {
		return errors.New("frozen Task input revision, epoch, or typed input differs from its Goal")
	}
	inputDigest, err := domaintask.TaskCenterKindInputSHA256(input.KindInput)
	if err != nil || draft.TaskCenterExecution == nil || draft.TaskCenterExecution.KindID != plan.KindID || draft.TaskCenterExecution.KindInputVersion != plan.KindInputVersion || draft.TaskCenterExecution.KindInputSHA256 != inputDigest || draft.TaskCenterExecution.SourceGoalID != modulecore.GoalID(goal.GoalID) || draft.TaskCenterExecution.PreparedRevision != input.Revision || draft.TaskCenterExecution.AssignmentEpoch != input.AssignmentEpoch {
		return errors.New("frozen Task execution descriptor does not bind the exact Goal snapshot")
	}
	if draft.Timing == nil || !reflect.DeepEqual(*draft.Timing, plan.Timing) {
		return errors.New("frozen Task timing differs from its Goal")
	}
	timingDigest, err := domaintask.TaskTimingSHA256(plan.Timing)
	if err != nil || draft.TaskCenterExecution.TimingSHA256 != timingDigest {
		return errors.New("frozen Task timing digest differs from its Goal")
	}
	if err := draft.TaskCenterExecution.Validate(draft); err != nil {
		return fmt.Errorf("invalid TaskCenter execution descriptor: %w", err)
	}
	if plan.DataScope == domainworkstream.TaskPlanDataScopeUser {
		if draft.PrivateAccess == nil || draft.PrivateAccess.Validate() != nil || draft.PrivateAccess.Scope != domaintask.TaskAccessScopeUser || draft.PrivateAccess.AuthenticatedUserID != plan.UserID {
			return domaintask.ErrPrivateTaskAccessInvalid
		}
	} else if draft.PrivateAccess != nil {
		return errors.New("public Task must not carry private access metadata")
	}
	var shared domaintask.SharedRoleContext
	if err := decodeTaskPlanStrictJSON(input.SharedContext, &shared); err != nil || shared.TaskID != "" || shared.UserIntent != "" || shared.ModuleID != "" || shared.ModuleRoot != "" || len(shared.RelevantFiles) != 0 || len(shared.Decisions) != 0 || len(shared.Constraints) != 0 || shared.CurrentPlan != "" || shared.LatestStatus != "" || len(shared.Artifacts) != 0 || shared.HandoffNotes != "" || !shared.UpdatedAt.IsZero() {
		return errors.New("frozen typed operation must not include freeform Worker context")
	}
	return nil
}

func decodeTaskPlanStrictJSON(raw []byte, target any) error {
	if err := checkTaskPlanJSONDuplicates(raw); err != nil {
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

func checkTaskPlanJSONDuplicates(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeTaskPlanJSONValue(decoder); err != nil {
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

func consumeTaskPlanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
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
			if err := consumeTaskPlanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeTaskPlanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func (s *SQLiteStore) mutateTaskPlanHandoff(ctx context.Context, scope tool.ToolExecutionScope, operation, key string, request any, goalID string, expectedRevision, expectedEpoch uint64, expectedPolicyRevision string, authorize func(domainworkstream.Goal) error, mutate func(*domainworkstream.Goal, *domainworkstream.TaskPlan) error) (domainworkstream.TaskPlanMutationResult, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	acceptance := operation == "execution_result_accept"
	if (scope.ActorKind != tool.ActorKindAgent && !(acceptance && scope.ActorKind == tool.ActorKindUser)) || authorize == nil || goalID == "" || expectedRevision == 0 || expectedRevision == ^uint64(0) || !validTaskPlanKey(key) || (!acceptance && expectedPolicyRevision == "") || mutate == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanInvalid
	}
	inputHash, err := hashTaskPlanValue(request)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if s == nil || s.db == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	receiptID, err := reserveTaskPlanOperationReceipt(ctx, tx, scope, operation, key)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	if prior, priorID, found, lookupErr := findTaskPlanOperationReceipt(ctx, tx, scope, operation, key); lookupErr != nil {
		return domainworkstream.TaskPlanMutationResult{}, lookupErr
	} else if found {
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifact WHERE artifact_id = ?`, receiptID); err != nil {
			return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
		}
		return replayTaskPlanReceipt(ctx, tx, scope, prior, priorID, operation, key, inputHash)
	}
	locked, err := tx.ExecContext(ctx, `UPDATE workstream_goal SET payload = payload WHERE goal_id = ?`, goalID)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	if count, _ := locked.RowsAffected(); count == 0 {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanNotFound
	}
	goal, oldPayload, err := readTaskPlanGoalTx(ctx, tx, goalID)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if goal.TaskPlan == nil || !taskPlanVisibleToScope(scope, *goal.TaskPlan) {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanNotFound
	}
	if goal.TaskPlan.Revision != expectedRevision || goal.TaskPlan.AssignmentEpoch != expectedEpoch || (expectedPolicyRevision != "" && goal.TaskPlan.PolicyRevision != expectedPolicyRevision) {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
	}
	if err := authorize(goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if !acceptance && goal.TaskPlan.Assignee != scope.ActorID {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanForbidden
	}
	plan := *goal.TaskPlan
	if plan.SubsetAllowedAgents != nil {
		copyAgents := append([]string(nil), (*plan.SubsetAllowedAgents)...)
		plan.SubsetAllowedAgents = &copyAgents
	}
	if plan.HandoffInput != nil {
		copyInput := *plan.HandoffInput
		copyInput.TaskDraft = append([]byte(nil), copyInput.TaskDraft...)
		copyInput.SharedContext = append([]byte(nil), copyInput.SharedContext...)
		copyInput.KindInput = append([]byte(nil), copyInput.KindInput...)
		plan.HandoffInput = &copyInput
	}
	if plan.ExecutionResult != nil {
		copyResult := *plan.ExecutionResult
		plan.ExecutionResult = &copyResult
	}
	if err := mutate(&goal, &plan); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	plan.UpdatedAt = time.Now().UTC()
	goal.TaskPlan = &plan
	if err := domainworkstream.ValidateGoal(goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanInvalid, err)
	}
	if err := updateTaskPlanGoal(ctx, tx, goal, oldPayload); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	result, err := storeTaskPlanOperationReceipt(ctx, tx, receiptID, scope, operation, key, inputHash, goal)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	return result, nil
}

func (s *SQLiteStore) mutateTaskPlan(ctx context.Context, scope tool.ToolExecutionScope, operation, key, inputHash, goalID string, expectedRevision uint64, expectedPolicyRevision string, authorize func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	if s == nil || s.db == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	receiptID, err := reserveTaskPlanOperationReceipt(ctx, tx, scope, operation, key)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	if prior, priorID, found, err := findTaskPlanOperationReceipt(ctx, tx, scope, operation, key); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	} else if found {
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifact WHERE artifact_id = ?`, receiptID); err != nil {
			return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
		}
		return replayTaskPlanReceipt(ctx, tx, scope, prior, priorID, operation, key, inputHash)
	}
	// The reservation insert above acquires SQLite's writer lock before reading
	// either the receipt namespace or the native Goal revision.
	locked, err := tx.ExecContext(ctx, `UPDATE workstream_goal SET payload = payload WHERE goal_id = ?`, goalID)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	if count, _ := locked.RowsAffected(); count == 0 {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanNotFound
	}
	goal, oldPayload, err := readTaskPlanGoalTx(ctx, tx, goalID)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if goal.TaskPlan == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanNotFound
	}
	if !taskPlanVisibleToScope(scope, *goal.TaskPlan) {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanNotFound
	}
	if goal.TaskPlan.Revision != expectedRevision {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
	}
	if operation == "claim" && !domainworkstream.GoalStatusClaimReady(goal.Status) {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
	}
	if operation == "release" && !domainworkstream.GoalStatusOpen(goal.Status) {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
	}
	if err := authorize(goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	plan := *goal.TaskPlan
	if operation == "claim" {
		if plan.Assignee != domainworkstream.TaskPlanAssigneeUnassigned {
			return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
		}
		plan.Assignee = scope.ActorID
	} else {
		if scope.ActorKind == tool.ActorKindAgent && plan.Assignee != scope.ActorID {
			return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanForbidden
		}
		if plan.HandoffState != domainworkstream.TaskPlanHandoffNone || plan.TaskID != "" || plan.RunID != "" {
			return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanUnavailable
		}
		plan.Assignee = domainworkstream.TaskPlanAssigneeUnassigned
	}
	plan.AssignmentEpoch++
	plan.Revision++
	plan.PolicyRevision = expectedPolicyRevision
	plan.UpdatedAt = time.Now().UTC()
	goal.TaskPlan = &plan
	if err := domainworkstream.ValidateGoal(goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanInvalid, err)
	}
	if err := updateTaskPlanGoal(ctx, tx, goal, oldPayload); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	result, err := storeTaskPlanOperationReceipt(ctx, tx, receiptID, scope, operation, key, inputHash, goal)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
	}
	return result, nil
}

// FindTaskCenterWorkstreamID resolves the same first user-scoped Workstream
// that Task Plan creation reuses, without creating one during a read.
func (s *SQLiteStore) FindTaskCenterWorkstreamID(ctx context.Context, scope tool.ToolExecutionScope) (modulecore.WorkstreamID, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil || scope.AuthenticatedUserID == "" || !scope.Allows(tool.DataScopeUser) {
		return "", domainworkstream.ErrTaskPlanForbidden
	}
	if s == nil || s.db == nil {
		return "", domainworkstream.ErrTaskPlanUnavailable
	}
	found, exists, err := findTaskCenterWorkstream(ctx, s.db, domainworkstream.TaskPlanDataScopeUser, scope.AuthenticatedUserID)
	if err != nil {
		return "", classifyTaskPlanStorageError(err)
	}
	if !exists {
		return "", domainworkstream.ErrTaskPlanNotFound
	}
	return modulecore.WorkstreamID(found), nil
}

func (s *SQLiteStore) ListTaskPlans(ctx context.Context, scope tool.ToolExecutionScope, query domainworkstream.TaskPlanQuery) ([]domainworkstream.Goal, string, bool, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return nil, "", false, err
	}
	if s == nil || s.db == nil {
		return nil, "", false, domainworkstream.ErrTaskPlanUnavailable
	}
	if query.View == "daily" {
		return nil, "", false, domainworkstream.ErrTaskPlanUnsupported
	}
	if query.View != "" && query.View != "remaining" && query.View != "claimable" && query.View != "history" {
		return nil, "", false, domainworkstream.ErrTaskPlanInvalid
	}
	if query.View == "claimable" && scope.ActorKind != tool.ActorKindAgent {
		return nil, "", false, domainworkstream.ErrTaskPlanForbidden
	}
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 200 {
		return nil, "", false, domainworkstream.ErrTaskPlanInvalid
	}
	where := []string{`json_type(payload, '$.task_plan') = 'object'`}
	args := make([]any, 0, 8)
	visibility := make([]string, 0, 2)
	if scope.Allows(tool.DataScopePublic) {
		visibility = append(visibility, `json_extract(payload, '$.task_plan.data_scope') = 'public'`)
	}
	if scope.AuthenticatedUserID != "" && scope.Allows(tool.DataScopeUser) {
		visibility = append(visibility, `(json_extract(payload, '$.task_plan.data_scope') = 'user' AND json_extract(payload, '$.task_plan.user_id') = ?)`)
		args = append(args, scope.AuthenticatedUserID)
	}
	if len(visibility) == 0 {
		return []domainworkstream.Goal{}, "", false, nil
	}
	where = append(where, "("+strings.Join(visibility, " OR ")+")")
	if query.Assignee != "" {
		where = append(where, `json_extract(payload, '$.task_plan.assignee') = ?`)
		args = append(args, query.Assignee)
	}
	if query.View == "history" {
		where = append(where, `json_extract(payload, '$.status') IN (?, ?)`)
		args = append(args, domainworkstream.StatusCompleted, domainworkstream.StatusArchived)
	} else if query.View == "claimable" {
		where = append(where, `json_extract(payload, '$.status') = ?`)
		args = append(args, domainworkstream.StatusWaiting)
		where = append(where, `json_extract(payload, '$.task_plan.assignee') = ?`)
		args = append(args, domainworkstream.TaskPlanAssigneeUnassigned)
	} else {
		where = append(where, `json_extract(payload, '$.status') IN (?, ?, ?, ?)`)
		args = append(args, domainworkstream.StatusDraft, domainworkstream.StatusActive, domainworkstream.StatusPaused, domainworkstream.StatusWaiting)
	}
	if query.Cursor != "" {
		createdAt, goalID, err := decodeTaskPlanCursor(query.Cursor)
		if err != nil {
			return nil, "", false, domainworkstream.ErrTaskPlanInvalid
		}
		where = append(where, `(created_at < ? OR (created_at = ? AND goal_id < ?))`)
		args = append(args, createdAt, createdAt, goalID)
	}
	args = append(args, query.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT goal_id, workstream_id, created_at, payload FROM workstream_goal WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC, goal_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", false, classifyTaskPlanStorageError(err)
	}
	defer rows.Close()
	goals := make([]domainworkstream.Goal, 0, query.Limit+1)
	for rows.Next() {
		var id, workstreamID, createdAt, payload string
		if err := rows.Scan(&id, &workstreamID, &createdAt, &payload); err != nil {
			return nil, "", false, err
		}
		var goal domainworkstream.Goal
		if err := json.Unmarshal([]byte(payload), &goal); err != nil {
			return nil, "", false, err
		}
		if goal.GoalID != id || goal.WorkstreamID != workstreamID || goal.TaskPlan == nil || !taskPlanVisibleToScope(scope, *goal.TaskPlan) {
			return nil, "", false, errors.New("task plan owner row identity is inconsistent")
		}
		goals = append(goals, goal)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, err
	}
	partial := len(goals) > query.Limit
	if partial {
		goals = goals[:query.Limit]
	}
	nextCursor := ""
	if partial && len(goals) > 0 {
		last := goals[len(goals)-1]
		nextCursor = encodeTaskPlanCursor(last.CreatedAt.Format(timeFormatRFC3339Nano), last.GoalID)
	}
	return goals, nextCursor, partial, nil
}

func (s *SQLiteStore) GetTaskPlan(ctx context.Context, scope tool.ToolExecutionScope, goalID string) (domainworkstream.Goal, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return domainworkstream.Goal{}, err
	}
	if s == nil || s.db == nil || goalID == "" {
		return domainworkstream.Goal{}, domainworkstream.ErrTaskPlanNotFound
	}
	goal, _, err := readTaskPlanGoal(ctx, s.db, goalID)
	if err != nil {
		return domainworkstream.Goal{}, err
	}
	if goal.TaskPlan == nil || !taskPlanVisibleToScope(scope, *goal.TaskPlan) {
		return domainworkstream.Goal{}, domainworkstream.ErrTaskPlanNotFound
	}
	return goal, nil
}

func (s *SQLiteStore) TaskPlanReceipt(ctx context.Context, scope tool.ToolExecutionScope, operation, key string) (domainworkstream.TaskPlanMutationResult, bool, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, false, err
	}
	if s == nil || s.db == nil || !validTaskPlanOperation(operation) || !validTaskPlanKey(key) {
		return domainworkstream.TaskPlanMutationResult{}, false, domainworkstream.ErrTaskPlanInvalid
	}
	receipt, receiptID, found, err := findTaskPlanOperationReceipt(ctx, s.db, scope, operation, key)
	if err != nil || !found {
		return domainworkstream.TaskPlanMutationResult{}, found, err
	}
	if !taskPlanReceiptMatchesScope(scope, receipt) || receipt.Operation != operation || receipt.IdempotencyKey != key {
		return domainworkstream.TaskPlanMutationResult{}, false, domainworkstream.ErrTaskPlanNotFound
	}
	if receipt.Result.TaskPlan == nil || !taskPlanVisibleToScope(scope, *receipt.Result.TaskPlan) {
		return domainworkstream.TaskPlanMutationResult{}, false, domainworkstream.ErrTaskPlanNotFound
	}
	if operation == "handoff_link" {
		current, _, err := readTaskPlanGoal(ctx, s.db, receipt.Result.GoalID)
		if err != nil {
			return domainworkstream.TaskPlanMutationResult{}, false, err
		}
		storedPlan := receipt.Result.TaskPlan
		currentPlan := current.TaskPlan
		if currentPlan == nil || domainworkstream.ValidateGoal(current) != nil || !taskPlanVisibleToScope(scope, *currentPlan) ||
			current.WorkstreamID != receipt.Result.WorkstreamID || currentPlan.DataScope != storedPlan.DataScope || currentPlan.UserID != storedPlan.UserID ||
			currentPlan.CreatedBy != storedPlan.CreatedBy || currentPlan.CreatorKind != storedPlan.CreatorKind ||
			currentPlan.HandoffState != domainworkstream.TaskPlanHandoffLinked || currentPlan.HandoffKey != storedPlan.HandoffKey ||
			currentPlan.AssignmentEpoch != storedPlan.AssignmentEpoch || currentPlan.TaskID != storedPlan.TaskID || currentPlan.RunID != storedPlan.RunID ||
			currentPlan.HandoffInput == nil || storedPlan.HandoffInput == nil || currentPlan.HandoffInput.InputHash != storedPlan.HandoffInput.InputHash || currentPlan.HandoffInput.Validate(*currentPlan) != nil {
			return domainworkstream.TaskPlanMutationResult{}, false, domainworkstream.ErrTaskPlanNotFound
		}
	}
	return domainworkstream.TaskPlanMutationResult{Goal: receipt.Result, Replayed: true, ReceiptRef: receiptID}, true, nil
}

func validateTaskPlanSubmission(scope tool.ToolExecutionScope, request domainworkstream.TaskPlanSubmission) error {
	goal := request.Goal
	if request.IdempotencyKey == "" || !validTaskPlanKey(request.IdempotencyKey) || goal.GoalID != "" || goal.TaskPlan == nil || goal.CompletedAt.IsZero() == false || goal.Status != domainworkstream.StatusWaiting {
		return domainworkstream.ErrTaskPlanInvalid
	}
	if request.WorkstreamID != goal.WorkstreamID {
		return domainworkstream.ErrTaskPlanInvalid
	}
	plan := goal.TaskPlan
	if plan.CreatorKind == "" {
		plan.CreatorKind = string(scope.ActorKind)
	} else if plan.CreatorKind != string(scope.ActorKind) {
		return domainworkstream.ErrTaskPlanForbidden
	}
	if plan.Revision != 1 || plan.AssignmentEpoch != 0 || plan.HandoffState != domainworkstream.TaskPlanHandoffNone || plan.TaskID != "" || plan.RunID != "" {
		return domainworkstream.ErrTaskPlanInvalid
	}
	if plan.CreatedBy != scope.ActorID || plan.CreatorKind != string(scope.ActorKind) || !taskPlanWritableByScope(scope, *plan) {
		return domainworkstream.ErrTaskPlanForbidden
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanInvalid, err)
	}
	if plan.Timing.Recurrence != nil && plan.Timing.Recurrence.Frequency != modulecore.TaskRecurrenceNone {
		return domainworkstream.ErrTaskPlanUnsupported
	}
	return nil
}

func validateTaskPlanOwnerCall(ctx context.Context, scope tool.ToolExecutionScope) error {
	if ctx == nil {
		return domainworkstream.ErrTaskPlanForbidden
	}
	trusted, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || trusted.Validate() != nil || !reflect.DeepEqual(scope, trusted) {
		return domainworkstream.ErrTaskPlanForbidden
	}
	if scope.ActorKind == tool.ActorKindUser && scope.ActorID != scope.AuthenticatedUserID {
		return domainworkstream.ErrTaskPlanForbidden
	}
	if !scope.Allows(tool.DataScopePublic) && !(scope.AuthenticatedUserID != "" && scope.Allows(tool.DataScopeUser)) {
		return domainworkstream.ErrTaskPlanUnsupported
	}
	return nil
}

func taskPlanWritableByScope(scope tool.ToolExecutionScope, plan domainworkstream.TaskPlan) bool {
	switch plan.DataScope {
	case domainworkstream.TaskPlanDataScopePublic:
		return plan.UserID == "" && scope.Allows(tool.DataScopePublic)
	case domainworkstream.TaskPlanDataScopeUser:
		return scope.AuthenticatedUserID != "" && scope.AuthenticatedUserID == plan.UserID && scope.Allows(tool.DataScopeUser)
	default:
		return false
	}
}

func taskPlanVisibleToScope(scope tool.ToolExecutionScope, plan domainworkstream.TaskPlan) bool {
	return taskPlanWritableByScope(scope, plan)
}

func taskPlanDefaultScope(plan domainworkstream.TaskPlan) domainworkstream.TaskCenterWorkstreamScope {
	return domainworkstream.TaskCenterWorkstreamScope{DataScope: plan.DataScope, UserID: plan.UserID}
}

func findOrCreateTaskCenterWorkstream(ctx context.Context, tx *sql.Tx, dataScope, userID string, now time.Time) (string, error) {
	if found, exists, err := findTaskCenterWorkstream(ctx, tx, dataScope, userID); err != nil {
		return "", err
	} else if exists {
		return found, nil
	}
	id := string(modulecore.NewWorkstreamID())
	if err := modulecore.WorkstreamID(id).Validate(); err != nil {
		return "", err
	}
	if err := ensureTaskCenterWorkstream(ctx, tx, id, dataScope, userID, now); err != nil {
		return "", err
	}
	return id, nil
}

type taskCenterWorkstreamQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func findTaskCenterWorkstream(ctx context.Context, queryer taskCenterWorkstreamQueryer, dataScope, userID string) (string, bool, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT workstream_id,payload FROM workstream
		WHERE CASE WHEN json_valid(payload) THEN json_type(payload,'$.task_center_scope') END='object'
		AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.task_center_scope.data_scope') END=?
		AND CASE WHEN json_valid(payload) THEN COALESCE(json_extract(payload,'$.task_center_scope.user_id'),'') END=?
		ORDER BY rowid LIMIT 1`, dataScope, userID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	var id, payload string
	if err := rows.Scan(&id, &payload); err != nil {
		return "", false, err
	}
	var workstream domainworkstream.Workstream
	if json.Unmarshal([]byte(payload), &workstream) != nil || workstream.WorkstreamID != id || workstream.TaskCenterScope == nil ||
		*workstream.TaskCenterScope != (domainworkstream.TaskCenterWorkstreamScope{DataScope: dataScope, UserID: userID}) || modulecore.WorkstreamID(id).Validate() != nil {
		return "", false, domainworkstream.ErrTaskPlanUnavailable
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	return id, true, nil
}

func hashTaskPlanSubmission(request domainworkstream.TaskPlanSubmission) (string, error) {
	goal := request.Goal
	goal.GoalID = ""
	goal.WorkstreamID = request.WorkstreamID
	goal.CreatedAt = time.Time{}
	goal.CompletedAt = time.Time{}
	if goal.TaskPlan != nil {
		plan := *goal.TaskPlan
		plan.UpdatedAt = time.Time{}
		// CreatorKind is derived from the receipt namespace's authenticated
		// principal and was absent from historical submission hashes.
		plan.CreatorKind = ""
		goal.TaskPlan = &plan
	}
	canonical := struct {
		WorkstreamID string                `json:"workstream_id"`
		Goal         domainworkstream.Goal `json:"goal"`
	}{WorkstreamID: request.WorkstreamID, Goal: goal}
	return hashTaskPlanValue(canonical)
}

func hashTaskPlanValue(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

func ensureTaskCenterWorkstream(ctx context.Context, tx *sql.Tx, id, dataScope, userID string, now time.Time) error {
	var payload string
	err := tx.QueryRowContext(ctx, `SELECT payload FROM workstream WHERE workstream_id = ?`, id).Scan(&payload)
	if err == nil {
		var existing domainworkstream.Workstream
		if err := json.Unmarshal([]byte(payload), &existing); err != nil {
			return err
		}
		if existing.TaskCenterScope == nil || *existing.TaskCenterScope != (domainworkstream.TaskCenterWorkstreamScope{DataScope: dataScope, UserID: userID}) {
			return domainworkstream.ErrTaskPlanForbidden
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	item := domainworkstream.Workstream{
		WorkstreamID: id, Name: "タスク", Status: domainworkstream.StatusActive,
		CreatedAt: now, TaskCenterScope: &domainworkstream.TaskCenterWorkstreamScope{DataScope: dataScope, UserID: userID},
	}
	if err := domainworkstream.ValidateWorkstream(item); err != nil {
		return fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanInvalid, err)
	}
	payloadBytes, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workstream (workstream_id, created_at, payload) VALUES (?, ?, ?)`, id, now.Format(timeFormatRFC3339Nano), string(payloadBytes))
	return err
}

func requireManagedWorkstream(ctx context.Context, tx *sql.Tx, id, dataScope, userID string) error {
	var payload string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM workstream WHERE workstream_id = ?`, id).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainworkstream.ErrTaskPlanNotFound
		}
		return err
	}
	var item domainworkstream.Workstream
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return err
	}
	if item.TaskCenterScope == nil || *item.TaskCenterScope != (domainworkstream.TaskCenterWorkstreamScope{DataScope: dataScope, UserID: userID}) {
		return domainworkstream.ErrTaskPlanForbidden
	}
	return nil
}

func insertTaskPlanGoal(ctx context.Context, tx *sql.Tx, goal domainworkstream.Goal) error {
	payload, err := json.Marshal(goal)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workstream_goal (goal_id, workstream_id, created_at, payload) VALUES (?, ?, ?, ?)`, goal.GoalID, goal.WorkstreamID, goal.CreatedAt.Format(timeFormatRFC3339Nano), string(payload))
	return err
}

func updateTaskPlanGoal(ctx context.Context, tx *sql.Tx, goal domainworkstream.Goal, expectedPayload string) error {
	payload, err := json.Marshal(goal)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workstream_goal SET workstream_id = ?, created_at = ?, payload = ? WHERE goal_id = ? AND payload = ?`, goal.WorkstreamID, goal.CreatedAt.Format(timeFormatRFC3339Nano), string(payload), goal.GoalID, expectedPayload)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domainworkstream.ErrTaskPlanConflict
	}
	return nil
}

func readTaskPlanGoalTx(ctx context.Context, tx *sql.Tx, goalID string) (domainworkstream.Goal, string, error) {
	var payload string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM workstream_goal WHERE goal_id = ?`, goalID).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainworkstream.Goal{}, "", domainworkstream.ErrTaskPlanNotFound
		}
		return domainworkstream.Goal{}, "", err
	}
	var goal domainworkstream.Goal
	if err := json.Unmarshal([]byte(payload), &goal); err != nil {
		return domainworkstream.Goal{}, "", err
	}
	if goal.GoalID != goalID {
		return domainworkstream.Goal{}, "", errors.New("task plan Goal row identity is inconsistent")
	}
	return goal, payload, nil
}

func readTaskPlanGoal(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, goalID string) (domainworkstream.Goal, string, error) {
	var payload string
	if err := queryer.QueryRowContext(ctx, `SELECT payload FROM workstream_goal WHERE goal_id = ?`, goalID).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainworkstream.Goal{}, "", domainworkstream.ErrTaskPlanNotFound
		}
		return domainworkstream.Goal{}, "", err
	}
	var goal domainworkstream.Goal
	if err := json.Unmarshal([]byte(payload), &goal); err != nil {
		return domainworkstream.Goal{}, "", err
	}
	if goal.GoalID != goalID {
		return domainworkstream.Goal{}, "", errors.New("task plan Goal row identity is inconsistent")
	}
	return goal, payload, nil
}

func storeTaskPlanOperationReceipt(ctx context.Context, tx *sql.Tx, receiptID string, scope tool.ToolExecutionScope, operation, key, inputHash string, goal domainworkstream.Goal) (domainworkstream.TaskPlanMutationResult, error) {
	plan := goal.TaskPlan
	if plan == nil {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanInvalid
	}
	receipt := domainworkstream.TaskPlanOperationReceipt{
		Version: 1, ActorKind: string(scope.ActorKind), ActorID: scope.ActorID,
		AuthenticatedUserID: scope.AuthenticatedUserID, DataScope: plan.DataScope, UserID: plan.UserID,
		Operation: operation, IdempotencyKey: key, InputHash: inputHash, Result: goal,
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	artifact := domainworkstream.Artifact{
		ArtifactID: receiptID, WorkstreamID: goal.WorkstreamID, Type: taskPlanOperationArtifactType,
		Title: "Task operation receipt", Status: "stored", Payload: payload, CreatedAt: plan.UpdatedAt,
	}
	if err := domainworkstream.ValidateArtifact(artifact); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, fmt.Errorf("%w: task operation receipt: %v", domainworkstream.ErrTaskPlanInvalid, err)
	}
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE artifact SET workstream_id = ?, created_at = ?, payload = ? WHERE artifact_id = ? AND json_extract(payload, '$.artifact_type') = ?`, artifact.WorkstreamID, artifact.CreatedAt.Format(timeFormatRFC3339Nano), string(artifactJSON), receiptID, taskPlanReservationArtifactType)
	if err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return domainworkstream.TaskPlanMutationResult{}, err
		}
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
	}
	return domainworkstream.TaskPlanMutationResult{Goal: goal, ReceiptRef: receiptID}, nil
}

func reserveTaskPlanOperationReceipt(ctx context.Context, tx *sql.Tx, scope tool.ToolExecutionScope, operation, key string) (string, error) {
	if !validTaskPlanOperation(operation) || !validTaskPlanKey(key) {
		return "", domainworkstream.ErrTaskPlanInvalid
	}
	id := string(modulecore.NewArtifactID())
	if err := modulecore.ArtifactID(id).Validate(); err != nil {
		return "", err
	}
	reservation := domainworkstream.Artifact{
		ArtifactID: id, Type: taskPlanReservationArtifactType, Title: "Task operation reservation",
		Status: "pending", CreatedAt: time.Now().UTC(),
	}
	receiptPayload, err := json.Marshal(domainworkstream.TaskPlanOperationReceipt{
		Version: 0, ActorKind: string(scope.ActorKind), ActorID: scope.ActorID,
		AuthenticatedUserID: scope.AuthenticatedUserID, Operation: operation, IdempotencyKey: key,
	})
	if err != nil {
		return "", err
	}
	reservation.Payload = receiptPayload
	payload, err := json.Marshal(reservation)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, '', ?, ?)`, id, reservation.CreatedAt.Format(timeFormatRFC3339Nano), string(payload))
	return id, err
}

func findTaskPlanOperationReceipt(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scope tool.ToolExecutionScope, operation, key string) (domainworkstream.TaskPlanOperationReceipt, string, bool, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT artifact_id, payload FROM artifact ORDER BY rowid`)
	if err != nil {
		return domainworkstream.TaskPlanOperationReceipt{}, "", false, err
	}
	defer rows.Close()
	var matched domainworkstream.TaskPlanOperationReceipt
	var matchedID string
	found := false
	for rows.Next() {
		var indexedID, payload string
		if err := rows.Scan(&indexedID, &payload); err != nil {
			return domainworkstream.TaskPlanOperationReceipt{}, "", false, err
		}
		var artifact domainworkstream.Artifact
		if err := json.Unmarshal([]byte(payload), &artifact); err != nil {
			return domainworkstream.TaskPlanOperationReceipt{}, "", false, err
		}
		if artifact.ArtifactID != indexedID {
			return domainworkstream.TaskPlanOperationReceipt{}, "", false, errors.New("task operation artifact identity is inconsistent")
		}
		if artifact.Type != taskPlanOperationArtifactType {
			continue
		}
		var receipt domainworkstream.TaskPlanOperationReceipt
		if err := json.Unmarshal(artifact.Payload, &receipt); err != nil {
			return domainworkstream.TaskPlanOperationReceipt{}, "", false, err
		}
		if receipt.Version != 1 || receipt.Result.TaskPlan == nil || receipt.Operation == "" || receipt.IdempotencyKey == "" || len(receipt.InputHash) != 64 {
			return domainworkstream.TaskPlanOperationReceipt{}, "", false, errors.New("task operation receipt is malformed")
		}
		if taskPlanReceiptMatchesScope(scope, receipt) && receipt.Operation == operation && receipt.IdempotencyKey == key {
			if found {
				return domainworkstream.TaskPlanOperationReceipt{}, "", false, errors.New("duplicate task operation receipt identity")
			}
			matched, matchedID, found = receipt, indexedID, true
		}
	}
	if err := rows.Err(); err != nil {
		return domainworkstream.TaskPlanOperationReceipt{}, "", false, err
	}
	return matched, matchedID, found, nil
}

func (s *SQLiteStore) GameProjectPlanReceipt(ctx context.Context, scope tool.ToolExecutionScope, key string) (domainworkstream.ProjectPlanMutationResult, bool, error) {
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return domainworkstream.ProjectPlanMutationResult{}, false, err
	}
	if scope.ActorKind != tool.ActorKindUser || scope.ActorID != scope.AuthenticatedUserID || scope.AuthenticatedUserID == "" || !scope.Allows(tool.DataScopePublic) || !validTaskPlanKey(key) {
		return domainworkstream.ProjectPlanMutationResult{}, false, domainworkstream.ErrTaskPlanForbidden
	}
	if s == nil || s.db == nil {
		return domainworkstream.ProjectPlanMutationResult{}, false, domainworkstream.ErrTaskPlanUnavailable
	}
	receipt, receiptID, found, err := findGameProjectPlanReceipt(ctx, s.db, scope, key)
	if err != nil || !found {
		return domainworkstream.ProjectPlanMutationResult{}, found, err
	}
	return domainworkstream.ProjectPlanMutationResult{Registration: receipt.Registration, Replayed: true, ReceiptRef: receiptID}, true, nil
}

func findGameProjectPlanReceipt(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scope tool.ToolExecutionScope, key string) (domainworkstream.ProjectPlanOperationReceipt, string, bool, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT artifact_id, workstream_id, payload FROM artifact ORDER BY rowid`)
	if err != nil {
		return domainworkstream.ProjectPlanOperationReceipt{}, "", false, err
	}
	defer rows.Close()
	var matched domainworkstream.ProjectPlanOperationReceipt
	var matchedID string
	found := false
	for rows.Next() {
		var indexedID, indexedWorkstreamID, payload string
		if err := rows.Scan(&indexedID, &indexedWorkstreamID, &payload); err != nil {
			return domainworkstream.ProjectPlanOperationReceipt{}, "", false, err
		}
		var artifact domainworkstream.Artifact
		if err := json.Unmarshal([]byte(payload), &artifact); err != nil {
			return domainworkstream.ProjectPlanOperationReceipt{}, "", false, err
		}
		if artifact.ArtifactID != indexedID {
			return domainworkstream.ProjectPlanOperationReceipt{}, "", false, errors.New("game project registration artifact identity is inconsistent")
		}
		if artifact.Type != gameProjectPlanReceiptArtifactType {
			continue
		}
		var receipt domainworkstream.ProjectPlanOperationReceipt
		if err := decodeTaskPlanStrictJSON(artifact.Payload, &receipt); err != nil {
			return domainworkstream.ProjectPlanOperationReceipt{}, "", false, err
		}
		if err := validateGameProjectPlanReceipt(artifact, indexedWorkstreamID, receipt); err != nil {
			return domainworkstream.ProjectPlanOperationReceipt{}, "", false, err
		}
		if receipt.ActorKind == string(scope.ActorKind) && receipt.ActorID == scope.ActorID && receipt.AuthenticatedUserID == scope.AuthenticatedUserID && receipt.IdempotencyKey == key {
			if found {
				return domainworkstream.ProjectPlanOperationReceipt{}, "", false, errors.New("duplicate game project registration receipt")
			}
			matched, matchedID, found = receipt, indexedID, true
		}
	}
	if err := rows.Err(); err != nil {
		return domainworkstream.ProjectPlanOperationReceipt{}, "", false, err
	}
	return matched, matchedID, found, nil
}

func validateGameProjectPlanReceipt(artifact domainworkstream.Artifact, indexedWorkstreamID string, receipt domainworkstream.ProjectPlanOperationReceipt) error {
	stageKeys := domainworkstream.GameProjectStageKeys()
	registration := receipt.Registration
	if receipt.Version != 1 || receipt.ActorKind != string(tool.ActorKindUser) || receipt.ActorID == "" || receipt.ActorID != receipt.AuthenticatedUserID ||
		receipt.Operation != "game_project_register" || !validTaskPlanKey(receipt.IdempotencyKey) || !validTaskCenterSHA256(receipt.InputHash) || registration.WorkstreamID == "" ||
		artifact.WorkstreamID != registration.WorkstreamID || indexedWorkstreamID != registration.WorkstreamID || modulecore.WorkstreamID(registration.WorkstreamID).Validate() != nil || registration.InputSHA256 != receipt.InputHash ||
		len(registration.GoalIDs) != len(stageKeys) || len(registration.StageKeys) != len(stageKeys) || registration.RegisteredAt.IsZero() {
		return errors.New("game project registration receipt is malformed")
	}
	seenGoalIDs := make(map[string]struct{}, len(registration.GoalIDs))
	for i, stageKey := range stageKeys {
		goalID := registration.GoalIDs[i]
		if registration.StageKeys[i] != stageKey || modulecore.GoalID(goalID).Validate() != nil {
			return errors.New("game project registration receipt stage identity is malformed")
		}
		if _, exists := seenGoalIDs[goalID]; exists {
			return errors.New("game project registration receipt repeats a Goal ID")
		}
		seenGoalIDs[goalID] = struct{}{}
	}
	return nil
}

func replayTaskPlanReceipt(ctx context.Context, tx *sql.Tx, scope tool.ToolExecutionScope, receipt domainworkstream.TaskPlanOperationReceipt, receiptID, operation, key, inputHash string) (domainworkstream.TaskPlanMutationResult, error) {
	if receipt.Result.TaskPlan == nil || !taskPlanReceiptMatchesScope(scope, receipt) || !taskPlanVisibleToScope(scope, *receipt.Result.TaskPlan) {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanNotFound
	}
	if receipt.Operation != operation || receipt.IdempotencyKey != key || receipt.InputHash != inputHash {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanConflict
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return domainworkstream.TaskPlanMutationResult{}, classifyTaskPlanStorageError(err)
		}
	}
	return domainworkstream.TaskPlanMutationResult{Goal: receipt.Result, Replayed: true, ReceiptRef: receiptID}, nil
}

func taskPlanReceiptMatchesScope(scope tool.ToolExecutionScope, receipt domainworkstream.TaskPlanOperationReceipt) bool {
	return receipt.ActorKind == string(scope.ActorKind) && receipt.ActorID == scope.ActorID && receipt.AuthenticatedUserID == scope.AuthenticatedUserID
}

func validTaskPlanKey(key string) bool {
	return key != "" && len(key) <= 128 && key == strings.TrimSpace(key)
}

func validTaskPlanOperation(operation string) bool {
	return operation == "submit" || operation == "claim" || operation == "release" || operation == "handoff_prepare" || operation == "handoff_unknown" || operation == "handoff_link" || operation == "execution_result" || operation == "execution_result_accept"
}

func encodeTaskPlanCursor(createdAt, goalID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + goalID))
}

func decodeTaskPlanCursor(cursor string) (string, string, error) {
	value, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(string(value), "\x00", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("malformed task plan cursor")
	}
	if _, err := time.Parse(timeFormatRFC3339Nano, parts[0]); err != nil {
		return "", "", err
	}
	return parts[0], parts[1], nil
}

func classifyTaskPlanStorageError(err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "database is locked") || strings.Contains(text, "database is busy") || strings.Contains(text, "sqlite_busy") {
		return fmt.Errorf("%w: %v", domainworkstream.ErrTaskPlanConflict, err)
	}
	return err
}

func (s *SQLiteStore) saveLegacyTaskCenterRecord(ctx context.Context, table, idColumn, id, secondaryColumn, secondaryValue, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return errors.New("workstream sqlite store is closed")
	}
	if err := rejectIncomingManagedRecord(table, item); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	lockQuery := fmt.Sprintf(`UPDATE %s SET payload = payload WHERE %s = ?`, table, idColumn)
	if _, err := tx.ExecContext(ctx, lockQuery, id); err != nil {
		return classifyTaskPlanStorageError(err)
	}
	var current string
	readQuery := fmt.Sprintf(`SELECT payload FROM %s WHERE %s = ?`, table, idColumn)
	var currentWorkstream domainworkstream.Workstream
	if err := tx.QueryRowContext(ctx, readQuery, id).Scan(&current); err == nil {
		if err := rejectExistingManagedRecord(table, current); err != nil {
			return err
		}
		if table == "workstream" {
			if err := json.Unmarshal([]byte(current), &currentWorkstream); err != nil {
				return err
			}
			if currentWorkstream.WorkstreamID != id {
				return errors.New("stored workstream id does not match its row key")
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if table == "workstream" {
		workstream, ok := item.(domainworkstream.Workstream)
		if !ok {
			return errors.New("workstream row has an invalid item type")
		}
		workstream = domainworkstream.PreserveProjectDomain(workstream, currentWorkstream)
		if err := domainworkstream.ValidateWorkstream(workstream); err != nil {
			return err
		}
		item = workstream
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	var writeQuery string
	var writeArgs []any
	if secondaryColumn == "" {
		writeQuery = fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
		writeArgs = []any{id, createdAt, string(payload)}
	} else {
		writeQuery = fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, created_at, payload) VALUES (?, ?, ?, ?)`, table, idColumn, secondaryColumn)
		writeArgs = []any{id, secondaryValue, createdAt, string(payload)}
	}
	if _, err := tx.ExecContext(ctx, writeQuery, writeArgs...); err != nil {
		return err
	}
	return tx.Commit()
}

func rejectIncomingManagedRecord(table string, item any) error {
	switch table {
	case "workstream":
		value, ok := item.(domainworkstream.Workstream)
		if ok && value.TaskCenterScope != nil {
			return domainworkstream.ErrTaskPlanForbidden
		}
	case "workstream_goal":
		value, ok := item.(domainworkstream.Goal)
		if ok && value.TaskPlan != nil {
			return domainworkstream.ErrTaskPlanForbidden
		}
	case "artifact":
		value, ok := item.(domainworkstream.Artifact)
		if ok && isTaskCenterInternalArtifactType(value.Type) {
			return domainworkstream.ErrTaskPlanForbidden
		}
	}
	return nil
}

func rejectExistingManagedRecord(table, payload string) error {
	switch table {
	case "workstream":
		var value domainworkstream.Workstream
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			return err
		}
		if value.TaskCenterScope != nil {
			return domainworkstream.ErrTaskPlanForbidden
		}
	case "workstream_goal":
		var value domainworkstream.Goal
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			return err
		}
		if value.TaskPlan != nil {
			return domainworkstream.ErrTaskPlanForbidden
		}
	case "artifact":
		var value domainworkstream.Artifact
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			return err
		}
		if isTaskCenterInternalArtifactType(value.Type) {
			return domainworkstream.ErrTaskPlanForbidden
		}
	}
	return nil
}

func lockTaskCenterOwnerRecord(ctx context.Context, tx *sql.Tx, table, idColumn, id string) error {
	if table != "workstream" && table != "workstream_goal" && table != "artifact" {
		return nil
	}
	query := fmt.Sprintf(`UPDATE %s SET payload = payload WHERE %s = ?`, table, idColumn)
	_, err := tx.ExecContext(ctx, query, id)
	return err
}

func validateTaskCenterOwnerReplacement(ctx context.Context, tx *sql.Tx, table, idColumn, id string, item any) error {
	if err := rejectIncomingManagedRecord(table, item); err != nil {
		return err
	}
	if table != "workstream" && table != "workstream_goal" && table != "artifact" {
		return nil
	}
	var current string
	query := fmt.Sprintf(`SELECT payload FROM %s WHERE %s = ?`, table, idColumn)
	if err := tx.QueryRowContext(ctx, query, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	return rejectExistingManagedRecord(table, current)
}

func listSQLiteUnmanaged[T any](ctx context.Context, store *SQLiteStore, table, managedJSONPath, whereTail string, args []any, limit int) ([]T, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("workstream sqlite store is closed")
	}
	if limit <= 0 {
		limit = 50
	}
	where := fmt.Sprintf("json_type(payload, '%s') IS NULL", managedJSONPath)
	if whereTail != "" {
		where += " AND " + whereTail
	}
	query := fmt.Sprintf(`SELECT payload FROM %s WHERE %s ORDER BY created_at DESC LIMIT ?`, table, where)
	queryArgs := append(append([]any(nil), args...), limit)
	rows, err := store.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]T, 0, limit)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *JSONLStore) ensureLegacyGoalWritable(goal domainworkstream.Goal) error {
	if goal.TaskPlan != nil {
		return domainworkstream.ErrTaskPlanForbidden
	}
	var existing *domainworkstream.Goal
	if err := readJSONL(s.goalPath, func(line []byte) error {
		var item domainworkstream.Goal
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.GoalID == goal.GoalID {
			copyItem := item
			existing = &copyItem
		}
		return nil
	}); err != nil {
		return err
	}
	if existing != nil && existing.TaskPlan != nil {
		return domainworkstream.ErrTaskPlanForbidden
	}
	return nil
}

func (s *JSONLStore) ensureLegacyWorkstreamWritable(item domainworkstream.Workstream) (*domainworkstream.Workstream, error) {
	if item.TaskCenterScope != nil {
		return nil, domainworkstream.ErrTaskPlanForbidden
	}
	var existing *domainworkstream.Workstream
	if err := readJSONL(s.workstreamPath, func(line []byte) error {
		var current domainworkstream.Workstream
		if err := json.Unmarshal(line, &current); err != nil {
			return err
		}
		if current.WorkstreamID == item.WorkstreamID {
			copyItem := current
			existing = &copyItem
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if existing != nil && existing.TaskCenterScope != nil {
		return nil, domainworkstream.ErrTaskPlanForbidden
	}
	return existing, nil
}

func (s *JSONLStore) ensureLegacyArtifactWritable(item domainworkstream.Artifact) error {
	if isTaskCenterInternalArtifactType(item.Type) {
		return domainworkstream.ErrTaskPlanForbidden
	}
	var existing *domainworkstream.Artifact
	if err := readJSONL(s.artifactPath, func(line []byte) error {
		var current domainworkstream.Artifact
		if err := json.Unmarshal(line, &current); err != nil {
			return err
		}
		if current.ArtifactID == item.ArtifactID {
			copyItem := current
			existing = &copyItem
		}
		return nil
	}); err != nil {
		return err
	}
	if existing != nil && isTaskCenterInternalArtifactType(existing.Type) {
		return domainworkstream.ErrTaskPlanForbidden
	}
	return nil
}

func latestUnmanagedTaskCenterItems[T any](items []T, id func(T) string, managed func(T) bool, limit int) []T {
	managedIDs := make(map[string]struct{})
	for _, item := range items {
		if managed(item) {
			managedIDs[id(item)] = struct{}{}
		}
	}
	visible := make([]T, 0, len(items))
	for _, item := range items {
		if _, protected := managedIDs[id(item)]; !protected {
			visible = append(visible, item)
		}
	}
	return reverseLimit(visible, limit)
}

// JSONL has no transactional compare-and-swap primitive. Managed TaskCenter
// operations therefore fail explicitly until the backend has an atomic owner.
func (s *JSONLStore) SubmitTaskPlan(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanSubmission, func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanUnsupported
}

func (s *JSONLStore) ClaimTaskPlan(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanClaim, func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanUnsupported
}

func (s *JSONLStore) ReleaseTaskPlan(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanRelease, func(domainworkstream.Goal) error) (domainworkstream.TaskPlanMutationResult, error) {
	return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanUnsupported
}

func (s *JSONLStore) ListTaskPlans(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanQuery) ([]domainworkstream.Goal, string, bool, error) {
	return nil, "", false, domainworkstream.ErrTaskPlanUnsupported
}

func (s *JSONLStore) GetTaskPlan(context.Context, tool.ToolExecutionScope, string) (domainworkstream.Goal, error) {
	return domainworkstream.Goal{}, domainworkstream.ErrTaskPlanUnsupported
}

func (s *JSONLStore) TaskPlanReceipt(context.Context, tool.ToolExecutionScope, string, string) (domainworkstream.TaskPlanMutationResult, bool, error) {
	return domainworkstream.TaskPlanMutationResult{}, false, domainworkstream.ErrTaskPlanUnsupported
}
