package idlechat

import (
	"context"
	"errors"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
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
	if checkpoint == nil || checkpoint.Stage != stage {
		return errors.New("generation successor requires matching persisted intent")
	}
	if store == nil || strings.TrimSpace(store.path) == "" {
		return errors.New("generation successor requires persistent checkpoint store")
	}
	if err := store.LoadError(); err != nil {
		return err
	}
	persisted, found := store.Get(checkpoint.Key)
	if !found || persisted.TaskID != checkpoint.TaskID || persisted.RunID != checkpoint.RunID || persisted.Stage != stage || persisted.Kind != checkpoint.Kind {
		return errors.New("generation successor intent does not match checkpoint store")
	}
	if err := validateGenerationRunInputs(ctx, issuer, checkpoint.TaskID, checkpoint.RunID); err != nil {
		return err
	}
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
	for _, run := range runs {
		if run.RunID != previous.RunID && run.StartedAt.Equal(previous.StartedAt) {
			return errors.New("generation successor predecessor timestamp is ambiguous")
		}
	}
	if latest.RunID == previous.RunID {
		_, err := inspectGenerationRun(ctx, issuer, checkpoint.TaskID, previous.RunID)
		return err
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
	predecessorAllowed := previous.Status == domaintask.RunStatusWaiting || previous.Status == domaintask.RunStatusInterrupted
	if reason == domaintask.RunStartReasonExplicitRerun {
		predecessorAllowed = previous.Status == domaintask.RunStatusSucceeded || previous.Status == domaintask.RunStatusFailed || previous.Status == domaintask.RunStatusCancelled
	}
	if previous.TaskID != checkpoint.TaskID || previous.Assignee != latest.Assignee ||
		latest.StartReason != reason || !predecessorAllowed ||
		(latest.Status != domaintask.RunStatusRunning && latest.Status != domaintask.RunStatusWaiting && latest.Status != domaintask.RunStatusInterrupted) {
		return errors.New("generation resume successor does not match persisted intent")
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
