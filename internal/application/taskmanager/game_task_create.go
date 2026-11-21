package taskmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const gameTaskCreateOperationVersion = "game-task-create.v1"

var ErrGameTaskCreateReceiptConflict = errors.New("GAME Task create receipt conflicts with its stored Task")

type gameTaskCreateOperationStore interface {
	ExecuteIdempotentGlobalTaskOperation(context.Context, string, string, func(Store) (json.RawMessage, error)) (json.RawMessage, error)
}

type gameTaskCreateOperationReader interface {
	LookupGlobalTaskOperation(context.Context, string, string) (json.RawMessage, bool, error)
}

type gameTaskCreateOperationRequest struct {
	Version string                       `json:"version"`
	OwnerID string                       `json:"owner_id"`
	Task    domaintask.Task              `json:"task"`
	Shared  domaintask.SharedRoleContext `json:"shared_context"`
}

type gameTaskCreateOperationReceipt struct {
	TaskID modulecore.TaskID `json:"task_id"`
}

// EnsureGameTaskCreated creates a normalized GAME Task and its typed context
// once for the caller's stable request ID. The Task, context, and replay
// receipt commit in the Task store's existing global operation transaction.
func (m *Manager) EnsureGameTaskCreated(
	ctx context.Context,
	requestID modulecore.RequestID,
	ownerID string,
	draft domaintask.Task,
	shared domaintask.SharedRoleContext,
) (domaintask.Task, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, errors.New("game Task creation context is nil")
	}
	request, operationID, requestHash, err := normalizeGameTaskCreateOperation(requestID, ownerID, draft, shared)
	if err != nil {
		return domaintask.Task{}, err
	}
	store, ok := m.store.(gameTaskCreateOperationStore)
	if !ok {
		return domaintask.Task{}, ErrGlobalTaskOperationUnsupported
	}
	encoded, err := store.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, func(tx Store) (json.RawMessage, error) {
		if tx == nil {
			return nil, errors.New("global Task transaction store is unavailable")
		}
		txManager := &Manager{
			store:                    tx,
			limits:                   m.limits,
			expectedCriteriaRevision: m.expectedCriteriaRevision,
			idIndexed:                m.idIndexed,
			now:                      m.now,
		}
		created, err := txManager.createInTransaction(ctx, request.Task, request.Shared)
		if err != nil {
			return nil, err
		}
		participants, err := gameActivityActors(*request.Shared.GameSpec)
		if err != nil {
			return nil, err
		}
		activityParticipants := make([]domaintask.ActivityParticipant, 0, len(participants))
		for _, actorID := range participants {
			participant, _ := typedGameParticipant(*request.Shared.GameSpec, actorID)
			participant.Revision = 1
			activityParticipants = append(activityParticipants, participant)
		}
		created.Activity = &domaintask.ActivityMetadata{
			Kind: domaintask.ActivityKindGame, Label: "Playing " + request.Shared.GameSpec.GameID,
			Phase: "preparing", Revision: 1,
			Game:         &domaintask.ActivityGameReference{GameID: request.Shared.GameSpec.GameID},
			Participants: activityParticipants,
		}
		created.UpdatedAt = txManager.now()
		writer, ok := tx.(domaintask.ActivityTaskWriter)
		if !ok {
			return nil, domaintask.ErrActivityTaskWriterUnsupported
		}
		if err := writer.SaveTaskActivity(ctx, created); err != nil {
			return nil, err
		}
		return json.Marshal(gameTaskCreateOperationReceipt{TaskID: created.TaskID})
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	receipt, err := decodeGameTaskCreateOperationReceipt(encoded)
	if err != nil {
		return domaintask.Task{}, err
	}
	return m.readGameTaskCreateReceipt(ctx, request, receipt)
}

// LookupGameTaskCreation recovers an exact prior create without invoking the
// transaction callback. A missing operation is reported as (zero, false, nil).
func (m *Manager) LookupGameTaskCreation(
	ctx context.Context,
	requestID modulecore.RequestID,
	ownerID string,
	draft domaintask.Task,
	shared domaintask.SharedRoleContext,
) (domaintask.Task, bool, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, false, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, false, errors.New("game Task creation lookup context is nil")
	}
	request, operationID, requestHash, err := normalizeGameTaskCreateOperation(requestID, ownerID, draft, shared)
	if err != nil {
		return domaintask.Task{}, false, err
	}
	reader, ok := m.store.(gameTaskCreateOperationReader)
	if !ok {
		return domaintask.Task{}, false, ErrGlobalTaskOperationReadUnsupported
	}
	encoded, found, err := reader.LookupGlobalTaskOperation(ctx, operationID, requestHash)
	if err != nil || !found {
		return domaintask.Task{}, found, err
	}
	receipt, err := decodeGameTaskCreateOperationReceipt(encoded)
	if err != nil {
		return domaintask.Task{}, false, err
	}
	task, err := m.readGameTaskCreateReceipt(ctx, request, receipt)
	if err != nil {
		return domaintask.Task{}, false, err
	}
	return task, true, nil
}

func normalizeGameTaskCreateOperation(
	requestID modulecore.RequestID,
	ownerID string,
	draft domaintask.Task,
	shared domaintask.SharedRoleContext,
) (gameTaskCreateOperationRequest, string, string, error) {
	if err := requestID.Validate(); err != nil {
		return gameTaskCreateOperationRequest{}, "", "", fmt.Errorf("request_id is invalid: %w", err)
	}
	if ownerID == "" {
		return gameTaskCreateOperationRequest{}, "", "", errors.New("authenticated Task owner is required")
	}
	if draft.TaskID != "" || draft.Route != domaintask.RouteGAME || draft.OwnerID != ownerID {
		return gameTaskCreateOperationRequest{}, "", "", errors.New("GAME Task creation draft is outside the authenticated owner scope")
	}
	if draft.Status != "" || !draft.CreatedAt.IsZero() || !draft.UpdatedAt.IsZero() || draft.StartedAt != nil || draft.FinishedAt != nil ||
		draft.RoutingEventID != "" || draft.AssignmentEventID != "" || draft.ExpectedCriteriaRevision != "" || draft.NativeResumeClaims != nil ||
		draft.CoderRoles != nil || draft.Timing != nil || draft.InterruptPolicy != "" ||
		draft.OriginPublication != nil || draft.AcceptedOPSClaim != nil || draft.Production != nil || draft.Autonomous != nil || draft.PrivateAccess != nil ||
		draft.TaskCenterExecution != nil || draft.NativePlanningInvocation != nil || draft.TaskMeetingInvocation != nil || draft.TaskMeetingResult != nil ||
		draft.Summary != "" || draft.WaitingReason != "" || draft.NextActions != nil || draft.Evidence != nil || draft.Artifacts != nil {
		return gameTaskCreateOperationRequest{}, "", "", errors.New("GAME Task creation draft contains owner-managed fields")
	}
	draft.Title = strings.TrimSpace(draft.Title)
	draft.OwnerID = ownerID
	draft.Route = domaintask.RouteGAME
	draft.ModuleID = strings.TrimSpace(draft.ModuleID)
	draft.ModuleRoot = strings.TrimSpace(draft.ModuleRoot)
	draft.Assignee = strings.TrimSpace(draft.Assignee)
	draft.Priority = domaintask.Priority(strings.ToLower(strings.TrimSpace(string(draft.Priority))))
	if draft.Assignee == "" {
		draft.Assignee = "mio"
	}
	if draft.Priority == "" {
		draft.Priority = domaintask.PriorityNormal
	}
	if shared.TaskID != "" || !shared.UpdatedAt.IsZero() || shared.UserIntent != "" && shared.UserIntent != draft.Title ||
		shared.ModuleID != "" && shared.ModuleID != draft.ModuleID || shared.ModuleRoot != "" && shared.ModuleRoot != draft.ModuleRoot ||
		shared.GameSpec == nil || shared.GameProgress != nil || shared.PrivateText != nil || shared.AutonomousDecision != nil ||
		len(shared.RelevantFiles) != 0 || len(shared.Decisions) != 0 || len(shared.Constraints) != 0 || len(shared.Artifacts) != 0 ||
		shared.LatestStatus != "" || shared.HandoffNotes != "" || len(shared.TaskMeetingConsumedMessages) != 0 {
		return gameTaskCreateOperationRequest{}, "", "", errors.New("GAME Task creation requires only a typed game_spec")
	}
	spec, err := domaintask.NormalizeGameSpec(*shared.GameSpec)
	if err != nil {
		return gameTaskCreateOperationRequest{}, "", "", fmt.Errorf("GAME Task spec is invalid: %w", err)
	}
	shared = domaintask.SharedRoleContext{
		GameSpec:    &spec,
		UserIntent:  draft.Title,
		ModuleID:    draft.ModuleID,
		ModuleRoot:  draft.ModuleRoot,
		CurrentPlan: strings.TrimSpace(shared.CurrentPlan),
	}
	request := gameTaskCreateOperationRequest{Version: gameTaskCreateOperationVersion, OwnerID: ownerID, Task: draft, Shared: shared}
	copyBytes, err := json.Marshal(request)
	if err != nil {
		return gameTaskCreateOperationRequest{}, "", "", fmt.Errorf("encode GAME Task creation identity: %w", err)
	}
	var snapshot gameTaskCreateOperationRequest
	if err := json.Unmarshal(copyBytes, &snapshot); err != nil {
		return gameTaskCreateOperationRequest{}, "", "", fmt.Errorf("snapshot GAME Task creation identity: %w", err)
	}
	hash := sha256.Sum256(copyBytes)
	return snapshot, "task-create:" + string(requestID), hex.EncodeToString(hash[:]), nil
}

func decodeGameTaskCreateOperationReceipt(encoded json.RawMessage) (gameTaskCreateOperationReceipt, error) {
	var receipt gameTaskCreateOperationReceipt
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return gameTaskCreateOperationReceipt{}, fmt.Errorf("GAME Task create receipt is corrupt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return gameTaskCreateOperationReceipt{}, errors.New("GAME Task create receipt has trailing JSON")
	}
	if err := receipt.TaskID.Validate(); err != nil {
		return gameTaskCreateOperationReceipt{}, fmt.Errorf("GAME Task create receipt task_id is invalid: %w", err)
	}
	return receipt, nil
}

func (m *Manager) readGameTaskCreateReceipt(ctx context.Context, request gameTaskCreateOperationRequest, receipt gameTaskCreateOperationReceipt) (domaintask.Task, error) {
	var task domaintask.Task
	err := m.readTransaction(ctx, func(txManager *Manager) error {
		var err error
		task, err = txManager.store.GetTask(ctx, receipt.TaskID)
		if err != nil {
			return fmt.Errorf("read GAME Task from create receipt: %w", err)
		}
		shared, err := txManager.store.GetContext(ctx, receipt.TaskID)
		if err != nil {
			return fmt.Errorf("read GAME Task context from create receipt: %w", err)
		}
		if task.TaskID != receipt.TaskID || task.OwnerID != request.OwnerID || task.Route != domaintask.RouteGAME ||
			task.Title != request.Task.Title || task.Assignee != request.Task.Assignee || shared.TaskID != receipt.TaskID ||
			shared.GameSpec == nil || request.Shared.GameSpec == nil || shared.GameSpec.Digest() != request.Shared.GameSpec.Digest() {
			return ErrGameTaskCreateReceiptConflict
		}
		return nil
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return task, nil
}
