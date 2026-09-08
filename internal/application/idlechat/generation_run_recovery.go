package idlechat

import (
	"context"
	"errors"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"time"
)

// reconcileGenerationResume handles a crash between owner Run issuance and saving
// the successor ID. Only a persisted resume intent may adopt an exact
// same-Task, same-assignee checkpoint-resume successor from the canonical owner.
func reconcileGenerationResume(ctx context.Context, issuer idlechatRunIssuer, checkpoint *GenerationCheckpoint, store *GenerationCheckpointStore) error {
	owner, err := generationRunOwnerFromIssuer(issuer)
	if err != nil {
		return err
	}
	runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: checkpoint.TaskID})
	if err != nil {
		return err
	}
	var previous, latest domaintask.Run
	for _, run := range runs {
		if run.RunID == checkpoint.RunID {
			previous = run
		}
		if latest.RunID == "" || run.StartedAt.After(latest.StartedAt) {
			latest = run
		}
	}
	if previous.RunID == "" || latest.RunID == "" {
		return errors.New("generation resume owner history is missing")
	}
	if latest.RunID == previous.RunID {
		return nil
	}
	successors := 0
	for _, run := range runs {
		if run.StartedAt.After(previous.StartedAt) {
			successors++
		}
	}
	if successors != 1 {
		return errors.New("generation resume successor is ambiguous")
	}
	latest, err = inspectGenerationRun(ctx, issuer, checkpoint.TaskID, latest.RunID)
	if err != nil {
		return err
	}
	if previous.TaskID != checkpoint.TaskID || previous.Assignee != latest.Assignee ||
		latest.StartReason != domaintask.RunStartReasonCheckpointResume ||
		(previous.Status != domaintask.RunStatusWaiting && previous.Status != domaintask.RunStatusInterrupted) ||
		(latest.Status != domaintask.RunStatusRunning && latest.Status != domaintask.RunStatusWaiting && latest.Status != domaintask.RunStatusInterrupted) {
		return errors.New("generation resume successor does not match persisted intent")
	}
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
