package idlechat

import (
	"context"
	"errors"
	"fmt"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// reconcileGenerationResume handles a crash between owner Run issuance and saving
// the successor ID. Only a persisted resume intent may adopt an exact
// same-Task, same-assignee checkpoint-resume successor from the canonical owner.
func reconcileGenerationResume(ctx context.Context, issuer idlechatRunIssuer, checkpoint *GenerationCheckpoint, store *GenerationCheckpointStore) error {
	return reconcileGenerationSuccessor(ctx, issuer, checkpoint, store, "resume_pending", domaintask.RunStartReasonCheckpointResume)
}

// reconcileGenerationRerun binds a revision intent to the owner's exact explicit
// rerun. The terminal predecessor remains immutable; no Task is recreated.
func reconcileGenerationRerun(ctx context.Context, issuer idlechatRunIssuer, checkpoint *GenerationCheckpoint, store *GenerationCheckpointStore) error {
	return reconcileGenerationSuccessor(ctx, issuer, checkpoint, store, "rerun_pending", domaintask.RunStartReasonExplicitRerun)
}

func reconcileGenerationSuccessor(ctx context.Context, issuer idlechatRunIssuer, checkpoint *GenerationCheckpoint, store *GenerationCheckpointStore, stage string, reason domaintask.RunStartReason) error {
	persisted, digest, err := generationCheckpointIntent(ctx, checkpoint, store, stage)
	if err != nil {
		return err
	}
	if checkpointSuccessorStage(reason) != stage {
		return errors.New("generation successor does not match persisted intent")
	}
	owner, err := generationRunOwnerFromIssuer(issuer)
	if err != nil {
		return err
	}
	runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: persisted.TaskID})
	if err != nil {
		return err
	}
	task, err := owner.Get(ctx, persisted.TaskID)
	if err != nil {
		return err
	}
	if task.TaskID != persisted.TaskID {
		return errors.New("generation successor does not match persisted intent: task identity")
	}
	if err := task.Validate(); err != nil {
		return fmt.Errorf("generation successor owner task is invalid: %w", err)
	}
	var previous, latest domaintask.Run
	latestCount := 0
	seen := make(map[modulecore.RunID]struct{}, len(runs))
	for _, run := range runs {
		if err := run.Validate(); err != nil {
			return fmt.Errorf("generation successor owner Run is invalid: %w", err)
		}
		if run.TaskID != persisted.TaskID {
			return errors.New("generation successor does not match persisted intent: Run task identity")
		}
		if _, exists := seen[run.RunID]; exists {
			return fmt.Errorf("generation successor owner history has duplicate Run %s", run.RunID)
		}
		seen[run.RunID] = struct{}{}
		if run.RunID == persisted.RunID {
			previous = run
		}
		if latest.RunID == "" || run.StartedAt.After(latest.StartedAt) {
			latest = run
			latestCount = 1
		} else if run.StartedAt.Equal(latest.StartedAt) {
			latestCount++
		}
	}
	if previous.RunID == "" || latest.RunID == "" || latestCount != 1 {
		if latestCount > 1 {
			return errors.New("generation resume successor is ambiguous")
		}
		return errors.New("generation resume owner history is missing")
	}
	for _, run := range runs {
		if run.RunID != previous.RunID && run.StartedAt.Equal(previous.StartedAt) {
			return errors.New("generation successor predecessor timestamp is ambiguous")
		}
	}
	if latest.RunID == previous.RunID {
		// A process may have persisted the successor-bound checkpoint before
		// advancing its phase. The successor is now the checkpoint's selected
		// Run, so this retry is an idempotent inspection rather than a second
		// issuance. A still-unbound predecessor must remain resumable for the
		// caller to issue through startGenerationSuccessor.
		if previous.StartCheckpointSHA256 == "" && !domaintask.CanStartRunFromCheckpoint(previous.Status, reason) {
			return errors.New("generation successor does not match persisted intent: predecessor status")
		}
		_, err := inspectGenerationRun(ctx, issuer, persisted.TaskID, previous.RunID)
		return err
	}
	var successor domaintask.Run
	successors := 0
	for _, run := range runs {
		if run.StartedAt.After(previous.StartedAt) {
			successors++
			successor = run
		}
	}
	if successors != 1 {
		return errors.New("generation resume successor is ambiguous")
	}
	if successor.RunID != latest.RunID {
		return errors.New("generation resume successor is ambiguous")
	}
	if !domaintask.CanStartRunFromCheckpoint(previous.Status, reason) {
		return fmt.Errorf("generation successor does not match persisted intent: predecessor status %s", previous.Status)
	}
	if task.Assignee == "" || previous.Assignee == "" || latest.Assignee == "" || task.Assignee != previous.Assignee || previous.Assignee != latest.Assignee {
		return errors.New("generation successor does not match persisted intent: assignee")
	}
	if latest.StartReason != reason {
		return errors.New("generation successor does not match persisted intent: start reason")
	}
	if latest.StartCheckpointSHA256 != digest {
		return errors.New("generation successor does not match persisted intent: checkpoint digest")
	}
	if latest.Status != domaintask.RunStatusRunning && latest.Status != domaintask.RunStatusWaiting && latest.Status != domaintask.RunStatusInterrupted {
		return errors.New("generation successor does not match persisted intent: successor status")
	}
	latest, err = inspectGenerationRun(ctx, issuer, persisted.TaskID, latest.RunID)
	if err != nil {
		return err
	}
	// Keep the verified successor in the caller's copy even if Put fails. The
	// caller needs this exact identity to close that issued Run as Waiting; the
	// store itself retains the old durable intent for the next recovery attempt.
	checkpoint.RunID = latest.RunID
	if checkpoint.StoryArtifact != nil {
		checkpoint.StoryArtifact.Revision++
		checkpoint.StoryArtifact.RunID = latest.RunID
	}
	if checkpoint.DialogueArtifact != nil {
		checkpoint.DialogueArtifact.Revision++
		checkpoint.DialogueArtifact.RunID = latest.RunID
	}
	return store.Put(*checkpoint)
}

func finishGenerationRun(ctx context.Context, issuer idlechatRunIssuer, checkpoint GenerationCheckpoint, status domaintask.Status, summary, reason string) error {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return completeGenerationRun(finalCtx, issuer, checkpoint.TaskID, checkpoint.RunID, status, summary, reason)
}
