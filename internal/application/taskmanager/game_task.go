package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type gameTaskOwnerTransitionContextKey struct{}

func withGameTaskOwnerTransition(ctx context.Context) context.Context {
	return context.WithValue(ctx, gameTaskOwnerTransitionContextKey{}, true)
}

func isGameTaskOwnerTransition(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	owned, _ := ctx.Value(gameTaskOwnerTransitionContextKey{}).(bool)
	return owned
}

// GameTaskState is the immutable view used by the short-lived game controller.
// It keeps the Task spec and its owner-managed launch checkpoint together.
type GameTaskState struct {
	Task     domaintask.Task
	Spec     domaintask.GameSpec
	Progress domaintask.GameProgress
}

// StartGameTaskRun is the only path that creates a Run for a GAME Task. A
// resumed controller step must name the exact waiting predecessor and spec
// digest carried by its durable queue item.
func (m *Manager) StartGameTaskRun(
	ctx context.Context,
	taskID modulecore.TaskID,
	queueItemID modulecore.QueueItemID,
	actorID string,
	predecessorRunID modulecore.RunID,
	specDigest string,
) (domaintask.Run, error) {
	if ctx == nil {
		return domaintask.Run{}, errors.New("GAME Task Run context is nil")
	}
	if err := queueItemID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("GAME queue_item_id is invalid: %w", err)
	}
	if strings.TrimSpace(actorID) == "" {
		return domaintask.Run{}, errors.New("GAME Task actor is required")
	}
	firstRun := predecessorRunID == "" && specDigest == ""
	if (predecessorRunID == "") != (specDigest == "") {
		return domaintask.Run{}, errors.New("GAME Task predecessor and spec digest must be supplied together")
	}
	var issued domaintask.Run
	err := m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task.Route != domaintask.RouteGAME || task.Assignee != actorID {
			return fmt.Errorf("%w: GAME Task and controller ownership do not match", ErrRunConflict)
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if shared.GameSpec == nil || shared.GameProgress == nil || shared.GameProgress.Validate(taskID, *shared.GameSpec) != nil {
			return fmt.Errorf("%w: GAME Task checkpoint is absent or invalid", ErrRunConflict)
		}
		progress := *shared.GameProgress
		if progress.ControllerQueueItemID == queueItemID {
			if progress.ControllerRunID == "" {
				return fmt.Errorf("%w: GAME queue item has an incomplete Run binding", ErrRunConflict)
			}
			existingRun, getErr := txManager.store.GetRun(ctx, progress.ControllerRunID)
			if getErr != nil || existingRun.Validate() != nil || existingRun.TaskID != taskID || existingRun.Assignee != actorID ||
				existingRun.Status != domaintask.RunStatusRunning || task.Status != domaintask.StatusRunning {
				return fmt.Errorf("%w: GAME queue Run binding cannot be resumed safely", ErrRunConflict)
			}
			issued = existingRun
			return nil
		}
		if progress.Status == domaintask.GameExecutionCompleted || progress.Status == domaintask.GameExecutionFailed {
			return fmt.Errorf("%w: terminal GAME Task cannot start another controller Run", ErrRunConflict)
		}
		if firstRun {
			if task.Status != domaintask.StatusQueued || progress.Status != domaintask.GameExecutionPrepared {
				return fmt.Errorf("%w: first GAME Run requires a fresh queued Task", ErrRunConflict)
			}
			_, issued, err = txManager.startWithReason(withGameTaskOwnerTransition(ctx), taskID, domaintask.RunStartReasonFirst)
			if err != nil {
				return err
			}
		} else {
			if specDigest != shared.GameSpec.Digest() || predecessorRunID != progress.ControllerRunID {
				return fmt.Errorf("%w: GAME Task resume checkpoint does not match", ErrRunConflict)
			}
			reason := domaintask.RunStartReasonCheckpointResume
			if task.Status == domaintask.StatusWaiting && task.WaitingReason == domaintask.GameTaskWaitingReason {
				predecessor, err := validateCheckpointPredecessor(ctx, txManager, taskID, predecessorRunID, actorID, reason)
				if err != nil {
					return err
				}
				if predecessor.Status != domaintask.RunStatusWaiting {
					return fmt.Errorf("%w: GAME Task predecessor is not waiting", ErrRunConflict)
				}
			} else if task.Status == domaintask.StatusRunning {
				reason = domaintask.RunStartReasonProcessRestartResume
				predecessor, err := txManager.store.GetRun(ctx, predecessorRunID)
				if err != nil || predecessor.Validate() != nil || predecessor.TaskID != taskID || predecessor.Assignee != actorID || predecessor.Status != domaintask.RunStatusRunning {
					return fmt.Errorf("%w: GAME process-restart predecessor is not the current active Run", ErrRunConflict)
				}
			} else {
				return fmt.Errorf("%w: GAME Task is neither waiting nor recoverable after process restart", ErrRunConflict)
			}
			_, issued, err = txManager.startWithReason(withGameTaskOwnerTransition(ctx), taskID, reason)
			if err != nil {
				return err
			}
		}
		progress.ControllerRunID = issued.RunID
		progress.ControllerQueueItemID = queueItemID
		progress.UpdatedAt = txManager.now()
		shared.GameProgress = &progress
		shared.UpdatedAt = txManager.now()
		return txManager.store.SaveContext(ctx, shared)
	})
	if err != nil {
		return domaintask.Run{}, err
	}
	return issued, nil
}

// PauseGameTaskRun closes one short controller Run while retaining the
// Task-owned launch intent for the exact later receipt reconciliation step.
func (m *Manager) PauseGameTaskRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string) error {
	if err := taskID.Validate(); err != nil {
		return err
	}
	if err := runID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(actorID) == "" {
		return errors.New("GAME Task actor is required")
	}
	return m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
			return err
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if shared.GameSpec == nil || shared.GameProgress == nil || shared.GameProgress.Validate(taskID, *shared.GameSpec) != nil {
			return fmt.Errorf("%w: GAME Task checkpoint is absent or invalid", ErrRunConflict)
		}
		if shared.GameProgress.ControllerRunID != runID {
			return fmt.Errorf("%w: GAME pause Run is not the current controller Run", ErrRunConflict)
		}
		switch shared.GameProgress.Status {
		case domaintask.GameExecutionPrepared, domaintask.GameExecutionLaunching, domaintask.GameExecutionRunning, domaintask.GameExecutionUnknown:
		default:
			return fmt.Errorf("%w: GAME Task has no resumable controller checkpoint", ErrRunConflict)
		}
		_, err = txManager.updateStatusInTransaction(withGameTaskOwnerTransition(ctx), taskID, domaintask.StatusWaiting, "", domaintask.GameTaskWaitingReason, nil)
		return err
	})
}

func (m *Manager) GetGameTaskState(ctx context.Context, taskID modulecore.TaskID) (GameTaskState, error) {
	if m == nil || m.store == nil {
		return GameTaskState{}, errors.New("Task Manager is unavailable")
	}
	if err := taskID.Validate(); err != nil {
		return GameTaskState{}, fmt.Errorf("game task_id is invalid: %w", err)
	}
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return GameTaskState{}, err
	}
	if task.Route != domaintask.RouteGAME {
		return GameTaskState{}, errors.New("Task is not a GAME Task")
	}
	shared, err := loadPrivateTextSharedContext(ctx, m.store, taskID)
	if err != nil {
		return GameTaskState{}, err
	}
	if shared.GameSpec == nil || shared.GameProgress == nil {
		return GameTaskState{}, fmt.Errorf("%w: GAME Task checkpoint is absent", ErrRunConflict)
	}
	if err := shared.GameProgress.Validate(taskID, *shared.GameSpec); err != nil {
		return GameTaskState{}, fmt.Errorf("%w: GAME Task checkpoint is invalid: %v", ErrRunConflict, err)
	}
	return GameTaskState{Task: task, Spec: *shared.GameSpec, Progress: *shared.GameProgress}, nil
}

func (m *Manager) GetGameTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, domaintask.GameSpec, domaintask.GameProgress, error) {
	state, err := m.GetGameTaskState(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, domaintask.GameSpec{}, domaintask.GameProgress{}, err
	}
	return state.Task, state.Spec, state.Progress, nil
}

func (m *Manager) ListGameTaskStates(ctx context.Context) ([]GameTaskState, error) {
	tasks, err := m.List(ctx, domaintask.Filter{Route: domaintask.RouteGAME, Limit: domaintask.MaxTaskListPageSize})
	if err != nil {
		return nil, err
	}
	if len(tasks) == domaintask.MaxTaskListPageSize {
		return nil, errors.New("GAME Task reservation scan reached its safety bound")
	}
	states := make([]GameTaskState, 0, len(tasks))
	for _, task := range tasks {
		if domaintask.IsTerminal(task.Status) {
			continue
		}
		state, err := m.GetGameTaskState(ctx, task.TaskID)
		if err != nil {
			return nil, fmt.Errorf("read GAME Task reservation %s: %w", task.TaskID, err)
		}
		states = append(states, state)
	}
	return states, nil
}

// HasTerminalGameTaskSession checks whether an exact actor/game/session tuple
// belongs to a terminal GAME Task. It does not reserve the actor; it only
// prevents a late callback for that finished session from falling through to
// the legacy game-decision path. A different session for the same actor stays
// eligible for legacy play.
func (m *Manager) HasTerminalGameTaskSession(ctx context.Context, gameID, sessionID, actorID string) (bool, error) {
	if m == nil || m.store == nil {
		return false, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return false, errors.New("terminal GAME Task lookup context is nil")
	}
	if strings.TrimSpace(gameID) == "" || strings.TrimSpace(gameID) != gameID ||
		strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID ||
		strings.TrimSpace(actorID) == "" || strings.TrimSpace(actorID) != actorID {
		return false, errors.New("terminal GAME Task lookup identity is invalid")
	}
	for _, status := range []domaintask.Status{
		domaintask.StatusSucceeded, domaintask.StatusFailed, domaintask.StatusCancelled, domaintask.StatusSuperseded,
	} {
		var after modulecore.TaskID
		for {
			filter := domaintask.Filter{
				Route: domaintask.RouteGAME, Status: status, Limit: domaintask.MaxTaskListPageSize,
				TaskIDAfter: &after,
			}
			tasks, err := m.List(ctx, filter)
			if err != nil {
				return false, fmt.Errorf("list terminal GAME Tasks: %w", err)
			}
			for _, item := range tasks {
				shared, err := loadPrivateTextSharedContext(ctx, m.store, item.TaskID)
				if err != nil {
					return false, fmt.Errorf("read terminal GAME Task context %s: %w", item.TaskID, err)
				}
				if shared.GameSpec == nil && shared.GameProgress == nil {
					continue
				}
				if shared.GameSpec == nil || shared.GameProgress == nil {
					return false, fmt.Errorf("terminal GAME Task %s has an incomplete typed checkpoint", item.TaskID)
				}
				if err := shared.GameProgress.Validate(item.TaskID, *shared.GameSpec); err != nil {
					return false, fmt.Errorf("terminal GAME Task %s has an invalid typed checkpoint: %w", item.TaskID, err)
				}
				actorOwnsSession := false
				for _, persona := range shared.GameSpec.Personas {
					if persona == actorID {
						actorOwnsSession = true
						break
					}
				}
				if item.Status == status && shared.GameProgress.SessionID == sessionID &&
					shared.GameSpec.GameID == gameID && actorOwnsSession {
					return true, nil
				}
			}
			if len(tasks) < domaintask.MaxTaskListPageSize {
				break
			}
			after = tasks[len(tasks)-1].TaskID
		}
	}
	return false, nil
}

// BeginGameLaunch durably records the stable TaskID launch key before the
// controller performs the first network request. Retries never create a new
// key or reset an existing launch checkpoint.
func (m *Manager) BeginGameLaunch(ctx context.Context, taskID modulecore.TaskID) (GameTaskState, bool, error) {
	var state GameTaskState
	newIntent := false
	err := m.withTypedGameActivityTransaction(ctx, taskID, func(txManager *Manager, actors []string, current map[string]domaintask.Task) error {
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task.Route != domaintask.RouteGAME || task.Status != domaintask.StatusRunning {
			return fmt.Errorf("%w: GAME launch requires its running Task Run", ErrRunConflict)
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if shared.GameSpec == nil || shared.GameProgress == nil {
			return fmt.Errorf("%w: GAME Task checkpoint is absent", ErrRunConflict)
		}
		progress := *shared.GameProgress
		if err := progress.Validate(taskID, *shared.GameSpec); err != nil {
			return fmt.Errorf("%w: GAME Task checkpoint is invalid: %v", ErrRunConflict, err)
		}
		switch progress.Status {
		case domaintask.GameExecutionPrepared:
			progress.Status = domaintask.GameExecutionLaunching
			newIntent = true
			progress.UpdatedAt = txManager.now()
			shared.GameProgress = &progress
			shared.UpdatedAt = txManager.now()
			if err := txManager.store.SaveContext(ctx, shared); err != nil {
				return err
			}
		case domaintask.GameExecutionLaunching, domaintask.GameExecutionRunning, domaintask.GameExecutionUnknown:
			// Existing intent is a reconciliation checkpoint. It must not issue a
			// second launch request or replace its key/spec.
		default:
			return fmt.Errorf("%w: GAME Task is already terminal", ErrRunConflict)
		}
		state = GameTaskState{Task: task, Spec: *shared.GameSpec, Progress: progress}
		state.Task, err = txManager.ensureTypedGameTaskActivityInTransaction(ctx, taskID, state.Spec.GameID, state.Progress.SessionID, gameActivityPhase(progress.Status), state, actors, current)
		return err
	})
	if err != nil {
		return GameTaskState{}, false, err
	}
	return state, newIntent, nil
}

func (m *Manager) MarkGameLaunchUnknown(ctx context.Context, taskID modulecore.TaskID, reason string) (GameTaskState, error) {
	var state GameTaskState
	err := m.withTypedGameActivityTransaction(ctx, taskID, func(txManager *Manager, actors []string, current map[string]domaintask.Task) error {
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task.Route != domaintask.RouteGAME {
			return fmt.Errorf("%w: unknown launch is not owned by a GAME Task", ErrRunConflict)
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if shared.GameSpec == nil || shared.GameProgress == nil || shared.GameProgress.Validate(taskID, *shared.GameSpec) != nil {
			return fmt.Errorf("%w: GAME Task checkpoint is absent or invalid", ErrRunConflict)
		}
		progress := *shared.GameProgress
		if progress.Status != domaintask.GameExecutionLaunching && progress.Status != domaintask.GameExecutionRunning && progress.Status != domaintask.GameExecutionUnknown {
			return fmt.Errorf("%w: GAME Task has no ambiguous launch intent", ErrRunConflict)
		}
		progress.Status = domaintask.GameExecutionUnknown
		progress.Error = boundedGameTaskText(reason, 2048)
		progress.UpdatedAt = txManager.now()
		if err := progress.Validate(taskID, *shared.GameSpec); err != nil {
			return err
		}
		shared.GameProgress = &progress
		shared.UpdatedAt = txManager.now()
		if err := txManager.store.SaveContext(ctx, shared); err != nil {
			return err
		}
		state = GameTaskState{Task: task, Spec: *shared.GameSpec, Progress: progress}
		state.Task, err = txManager.ensureTypedGameTaskActivityInTransaction(ctx, taskID, state.Spec.GameID, state.Progress.SessionID, gameActivityPhase(progress.Status), state, actors, current)
		return err
	})
	if err != nil {
		return GameTaskState{}, err
	}
	return state, nil
}

func boundedGameTaskText(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}

// RecordGameLaunchReceipt binds the first GAMES session ID and monotonically
// advances the Task-owned checkpoint from the exact persisted launch intent.
func (m *Manager) RecordGameLaunchReceipt(ctx context.Context, taskID modulecore.TaskID, receipt domaintask.GameLaunchReceipt) (GameTaskState, error) {
	if receipt.Status == domaintask.GameExecutionCompleted || receipt.Status == domaintask.GameExecutionFailed {
		return GameTaskState{}, fmt.Errorf("%w: terminal GAMES receipt must be committed with FinalizeGameTaskRun", ErrRunConflict)
	}
	var state GameTaskState
	err := m.withTypedGameActivityTransaction(ctx, taskID, func(txManager *Manager, actors []string, current map[string]domaintask.Task) error {
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task.Route != domaintask.RouteGAME {
			return fmt.Errorf("%w: receipt owner is not a GAME Task", ErrRunConflict)
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if shared.GameSpec == nil || shared.GameProgress == nil {
			return fmt.Errorf("%w: GAME Task checkpoint is absent", ErrRunConflict)
		}
		progress := *shared.GameProgress
		if err := progress.Validate(taskID, *shared.GameSpec); err != nil {
			return fmt.Errorf("%w: GAME Task checkpoint is invalid: %v", ErrRunConflict, err)
		}
		if progress.Status != domaintask.GameExecutionLaunching && progress.Status != domaintask.GameExecutionRunning && progress.Status != domaintask.GameExecutionUnknown {
			return fmt.Errorf("%w: receipt has no outstanding launch intent", ErrRunConflict)
		}
		if progress.Status == domaintask.GameExecutionRunning && receipt.Status == domaintask.GameExecutionLaunching {
			return fmt.Errorf("%w: GAMES receipt would regress a running session to launching", ErrRunConflict)
		}
		progress, err = applyGameLaunchReceipt(taskID, progress, *shared.GameSpec, receipt, txManager.now())
		if err != nil {
			return err
		}
		shared.GameProgress = &progress
		shared.UpdatedAt = txManager.now()
		if err := txManager.store.SaveContext(ctx, shared); err != nil {
			return err
		}
		state = GameTaskState{Task: task, Spec: *shared.GameSpec, Progress: progress}
		state.Task, err = txManager.ensureTypedGameTaskActivityInTransaction(ctx, taskID, state.Spec.GameID, state.Progress.SessionID, gameActivityPhase(progress.Status), state, actors, current)
		return err
	})
	if err != nil {
		return GameTaskState{}, err
	}
	return state, nil
}

func applyGameLaunchReceipt(taskID modulecore.TaskID, progress domaintask.GameProgress, spec domaintask.GameSpec, receipt domaintask.GameLaunchReceipt, now time.Time) (domaintask.GameProgress, error) {
	if err := receipt.Validate(spec, progress.LaunchKey); err != nil {
		return progress, fmt.Errorf("%w: GAMES receipt does not match its launch intent: %v", ErrRunConflict, err)
	}
	if progress.SessionID != "" && receipt.SessionID != "" && progress.SessionID != receipt.SessionID {
		return progress, fmt.Errorf("%w: GAMES session binding changed", ErrRunConflict)
	}
	if progress.SessionID == "" && receipt.SessionID != "" {
		progress.SessionID = receipt.SessionID
	}
	progress.Status = receipt.Status
	if receipt.Result != nil {
		resultOK := receipt.Result.OK
		progress.ResultOK = &resultOK
		progress.ResultGameID = receipt.Result.GameID
		progress.ResultSessionID = receipt.Result.SessionID
		progress.ResultStatus = receipt.Result.Status
	}
	progress.Summary = strings.TrimSpace(receipt.SummaryJSON)
	progress.GameResultJSON = strings.TrimSpace(receipt.GameResultJSON)
	progress.Error = boundedGameTaskText(receipt.Error, 2048)
	progress.UpdatedAt = now
	if err := progress.Validate(taskID, spec); err != nil {
		return progress, fmt.Errorf("%w: resulting GAME Task checkpoint is invalid: %v", ErrRunConflict, err)
	}
	return progress, nil
}

// FinalizeGameTaskRun atomically stores the terminal GAMES receipt and closes
// the active controller Run together with the Task's terminal status.
func (m *Manager) FinalizeGameTaskRun(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	receipt domaintask.GameLaunchReceipt,
	summary string,
) (domaintask.Task, error) {
	if receipt.Status != domaintask.GameExecutionCompleted && receipt.Status != domaintask.GameExecutionFailed {
		return domaintask.Task{}, fmt.Errorf("%w: nonterminal GAMES receipt cannot finalize a Task", ErrRunConflict)
	}
	if strings.TrimSpace(summary) == "" {
		return domaintask.Task{}, errors.New("GAME Task terminal summary is required")
	}
	status := domaintask.StatusSucceeded
	if receipt.Status == domaintask.GameExecutionFailed {
		status = domaintask.StatusFailed
	}
	var finalized domaintask.Task
	err := m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if task.Route != domaintask.RouteGAME || shared.GameSpec == nil || shared.GameProgress == nil {
			return fmt.Errorf("%w: terminal GAMES receipt has no GAME Task owner", ErrRunConflict)
		}
		if err := receipt.Validate(*shared.GameSpec, shared.GameProgress.LaunchKey); err != nil {
			return fmt.Errorf("%w: terminal GAMES receipt is invalid: %v", ErrRunConflict, err)
		}
		if domaintask.IsTerminal(task.Status) {
			if task.Status != status || !sameGameReceiptProgress(*shared.GameProgress, receipt) || task.Summary != boundedGameTaskText(summary, 64<<10) {
				return fmt.Errorf("%w: terminal GAME Task differs from the repeated receipt", ErrRunConflict)
			}
			expectedRunStatus, _ := runStatusForTaskStatus(status)
			if err := txManager.verifyRunCompletionInTransaction(ctx, taskID, runID, actorID, status, expectedRunStatus); err != nil {
				return err
			}
			finalized = task
			return nil
		}
		if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
			return err
		}
		progress, err := applyGameLaunchReceipt(taskID, *shared.GameProgress, *shared.GameSpec, receipt, txManager.now())
		if err != nil {
			return err
		}
		shared.GameProgress = &progress
		shared.UpdatedAt = txManager.now()
		if err := txManager.store.SaveContext(ctx, shared); err != nil {
			return err
		}
		finalized, err = txManager.updateStatusInTransaction(withGameTaskOwnerTransition(ctx), taskID, status, boundedGameTaskText(summary, 64<<10), "", nil)
		return err
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return finalized, nil
}

func sameGameReceiptProgress(progress domaintask.GameProgress, receipt domaintask.GameLaunchReceipt) bool {
	if progress.LaunchKey != receipt.LaunchKey || progress.SessionID != receipt.SessionID || progress.Status != receipt.Status ||
		progress.GameResultJSON != strings.TrimSpace(receipt.GameResultJSON) || progress.Error != boundedGameTaskText(receipt.Error, 2048) ||
		progress.Summary != strings.TrimSpace(receipt.SummaryJSON) {
		return false
	}
	if receipt.Result == nil {
		return progress.ResultOK == nil && progress.ResultGameID == "" && progress.ResultSessionID == "" && progress.ResultStatus == ""
	}
	return progress.ResultOK != nil && *progress.ResultOK == receipt.Result.OK &&
		progress.ResultGameID == receipt.Result.GameID && progress.ResultSessionID == receipt.Result.SessionID &&
		progress.ResultStatus == receipt.Result.Status
}
