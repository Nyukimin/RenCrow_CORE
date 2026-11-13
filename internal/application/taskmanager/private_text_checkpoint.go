package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// StartPrivateTextRun creates the first private-text Run or atomically resumes
// the exact waiting predecessor whose Task-owned cursor matches digest.
func (m *Manager) StartPrivateTextRun(
	ctx context.Context,
	taskID modulecore.TaskID,
	queueItemID modulecore.QueueItemID,
	actorID string,
	predecessorRunID modulecore.RunID,
	digest string,
) (domaintask.Run, error) {
	if ctx == nil {
		return domaintask.Run{}, errors.New("private text run context is nil")
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Run{}, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Run{}, errors.New("private text task_id is invalid")
	}
	if err := queueItemID.Validate(); err != nil {
		return domaintask.Run{}, errors.New("private text queue_item_id is invalid")
	}
	if !validPrivateTextActor(actorID) {
		return domaintask.Run{}, errors.New("private text actor_id is invalid")
	}
	firstRun := predecessorRunID == "" && digest == ""
	if (predecessorRunID == "") != (digest == "") {
		return domaintask.Run{}, errors.New("private text predecessor and digest must be supplied together")
	}
	if !firstRun {
		if err := predecessorRunID.Validate(); err != nil {
			return domaintask.Run{}, errors.New("private text predecessor_run_id is invalid")
		}
		if err := domaintask.ValidateStartCheckpointSHA256(digest); err != nil || digest == "" {
			return domaintask.Run{}, errors.New("private text checkpoint digest is invalid")
		}
	}

	var issued domaintask.Run
	err := m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task.TaskID != taskID || task.Validate() != nil {
			return fmt.Errorf("%w: private text Task is invalid", ErrRunConflict)
		}
		if task.Assignee != actorID {
			return fmt.Errorf("%w: private text Task and actor ownership do not match", ErrRunConflict)
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}

		if firstRun {
			if task.Status != domaintask.StatusQueued || shared.PrivateText != nil {
				return fmt.Errorf("%w: private text first Run requires a fresh queued Task", ErrRunConflict)
			}
			_, issued, err = txManager.startWithReason(ctx, taskID, domaintask.RunStartReasonFirst)
			if err != nil {
				return err
			}
			checkpoint := domaintask.PrivateTextCheckpoint{
				TaskID:      taskID,
				RunID:       issued.RunID,
				QueueItemID: queueItemID,
				ActorID:     actorID,
				InputSHA256: privateTextInputSHA256(task.Title),
			}
			if err := checkpoint.Validate(); err != nil {
				return fmt.Errorf("%w: private text checkpoint is invalid", ErrRunConflict)
			}
			shared.PrivateText = &checkpoint
			return txManager.savePrivateTextSharedContext(ctx, shared)
		}

		predecessor, err := validateCheckpointPredecessor(
			ctx,
			txManager,
			taskID,
			predecessorRunID,
			actorID,
			domaintask.RunStartReasonCheckpointResume,
		)
		if err != nil {
			return err
		}
		if predecessor.Status != domaintask.RunStatusWaiting || task.Status != domaintask.StatusWaiting || task.WaitingReason != domaintask.PrivateTextWaitingReason {
			return fmt.Errorf("%w: private text predecessor is not paused at its checkpoint", ErrRunConflict)
		}
		if shared.PrivateText == nil {
			return fmt.Errorf("%w: private text checkpoint is absent", ErrRunConflict)
		}
		checkpoint := *shared.PrivateText
		if err := validatePrivateTextCheckpointOwner(task, checkpoint); err != nil || checkpoint.RunID != predecessorRunID || checkpoint.ActorID != actorID {
			return fmt.Errorf("%w: private text checkpoint ownership does not match", ErrRunConflict)
		}
		if checkpoint.Digest() != digest {
			return fmt.Errorf("%w: private text checkpoint digest does not match", ErrRunConflict)
		}

		_, issued, err = txManager.startWithReasonAndCheckpoint(ctx, taskID, domaintask.RunStartReasonCheckpointResume, digest)
		if err != nil {
			return err
		}
		checkpoint.RunID = issued.RunID
		checkpoint.QueueItemID = queueItemID
		shared.PrivateText = &checkpoint
		return txManager.savePrivateTextSharedContext(ctx, shared)
	})
	if err != nil {
		return domaintask.Run{}, err
	}
	return issued, nil
}

// GetPrivateTextCheckpoint reads the canonical private cursor only when it is
// still bound to the latest Run, current Task assignee, and immutable title.
func (m *Manager) GetPrivateTextCheckpoint(ctx context.Context, taskID modulecore.TaskID) (domaintask.PrivateTextCheckpoint, bool, error) {
	if ctx == nil {
		return domaintask.PrivateTextCheckpoint{}, false, errors.New("private text checkpoint context is nil")
	}
	if err := ctx.Err(); err != nil {
		return domaintask.PrivateTextCheckpoint{}, false, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.PrivateTextCheckpoint{}, false, errors.New("private text task_id is invalid")
	}

	var checkpoint domaintask.PrivateTextCheckpoint
	var found bool
	err := m.readTransaction(ctx, func(txManager *Manager) error {
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task.TaskID != taskID || task.Validate() != nil {
			return fmt.Errorf("%w: private text Task is invalid", ErrRunConflict)
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if shared.PrivateText == nil {
			return nil
		}
		candidate := *shared.PrivateText
		if err := validatePrivateTextCheckpointOwner(task, candidate); err != nil {
			return fmt.Errorf("%w: private text checkpoint ownership does not match", ErrRunConflict)
		}
		latest, err := txManager.validateLatestRunInTransaction(ctx, taskID, candidate.RunID)
		if err != nil {
			return err
		}
		if latest.RunID != candidate.RunID || latest.Assignee != candidate.ActorID {
			return fmt.Errorf("%w: private text checkpoint is not owned by the latest Run", ErrRunConflict)
		}
		checkpoint = candidate
		found = true
		return nil
	})
	if err != nil {
		return domaintask.PrivateTextCheckpoint{}, false, err
	}
	return checkpoint, found, nil
}

// SavePrivateTextOutput records the first validated output for the active
// private-text generation. Repeating the same output is idempotent.
func (m *Manager) SavePrivateTextOutput(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID, output string) error {
	if ctx == nil {
		return errors.New("private text output context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := taskID.Validate(); err != nil {
		return errors.New("private text task_id is invalid")
	}
	if err := runID.Validate(); err != nil {
		return errors.New("private text run_id is invalid")
	}
	if !validPrivateTextActor(actorID) {
		return errors.New("private text actor_id is invalid")
	}

	return m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
			return err
		}
		if _, err := txManager.validateLatestRunInTransaction(ctx, taskID, runID); err != nil {
			return err
		}
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if shared.PrivateText == nil {
			return fmt.Errorf("%w: private text checkpoint is absent", ErrRunConflict)
		}
		checkpoint := *shared.PrivateText
		if err := validatePrivateTextCheckpointOwner(task, checkpoint); err != nil || checkpoint.RunID != runID || checkpoint.ActorID != actorID {
			return fmt.Errorf("%w: private text checkpoint ownership does not match", ErrRunConflict)
		}
		if output == "" {
			return errors.New("private text output is required")
		}
		checkpoint.Output = output
		if err := checkpoint.Validate(); err != nil {
			return errors.New("private text output is invalid")
		}
		if shared.PrivateText.Output != "" {
			if shared.PrivateText.Output == output {
				return nil
			}
			return fmt.Errorf("%w: private text output is immutable", ErrRunConflict)
		}
		shared.PrivateText = &checkpoint
		return txManager.savePrivateTextSharedContext(ctx, shared)
	})
}

// CompletePrivateTextRun closes the active generation as either a private
// checkpoint pause or a successful Task. The cursor remains for receipt repair.
func (m *Manager) CompletePrivateTextRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, pause bool) error {
	if ctx == nil {
		return errors.New("private text completion context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := taskID.Validate(); err != nil {
		return errors.New("private text task_id is invalid")
	}
	if err := runID.Validate(); err != nil {
		return errors.New("private text run_id is invalid")
	}
	if !validPrivateTextActor(actorID) {
		return errors.New("private text actor_id is invalid")
	}

	return m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
			return err
		}
		if _, err := txManager.validateLatestRunInTransaction(ctx, taskID, runID); err != nil {
			return err
		}
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		shared, err := loadPrivateTextSharedContext(ctx, txManager.store, taskID)
		if err != nil {
			return err
		}
		if shared.PrivateText == nil {
			return fmt.Errorf("%w: private text checkpoint is absent", ErrRunConflict)
		}
		checkpoint := *shared.PrivateText
		if err := validatePrivateTextCheckpointOwner(task, checkpoint); err != nil || checkpoint.RunID != runID || checkpoint.ActorID != actorID {
			return fmt.Errorf("%w: private text checkpoint ownership does not match", ErrRunConflict)
		}
		status := domaintask.StatusSucceeded
		waitingReason := ""
		summary := checkpoint.Output
		if pause {
			status = domaintask.StatusWaiting
			waitingReason = domaintask.PrivateTextWaitingReason
			summary = ""
		} else if checkpoint.Output == "" {
			return fmt.Errorf("%w: private text output has not been saved", ErrRunConflict)
		}
		_, err = txManager.updateStatusInTransaction(ctx, taskID, status, summary, waitingReason, nil)
		return err
	})
}

func loadPrivateTextSharedContext(ctx context.Context, store Store, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	shared, err := store.GetContext(ctx, taskID)
	if errors.Is(err, domaintask.ErrNotFound) {
		return domaintask.SharedRoleContext{TaskID: taskID}, nil
	}
	if err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	if shared.TaskID != taskID {
		return domaintask.SharedRoleContext{}, fmt.Errorf("%w: Task context identity does not match", ErrRunConflict)
	}
	return shared, nil
}

func (m *Manager) savePrivateTextSharedContext(ctx context.Context, shared domaintask.SharedRoleContext) error {
	shared.UpdatedAt = m.now()
	if err := m.store.SaveContext(ctx, shared); err != nil {
		return err
	}
	return nil
}

func validatePrivateTextCheckpointOwner(task domaintask.Task, checkpoint domaintask.PrivateTextCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if checkpoint.TaskID != task.TaskID || checkpoint.ActorID != task.Assignee || checkpoint.InputSHA256 != privateTextInputSHA256(task.Title) {
		return errors.New("private text checkpoint ownership does not match")
	}
	return nil
}

func privateTextInputSHA256(title string) string {
	digest := sha256.Sum256([]byte(title))
	return hex.EncodeToString(digest[:])
}

func validPrivateTextActor(actorID string) bool {
	return actorID != "" && strings.TrimSpace(actorID) == actorID && utf8.ValidString(actorID)
}
