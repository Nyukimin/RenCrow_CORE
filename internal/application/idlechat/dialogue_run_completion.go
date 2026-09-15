package idlechat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func (p PendingCompletion) Validate() error {
	if _, ok := generationRunCompletionStatus(p.Status); !ok {
		return fmt.Errorf("pending dialogue completion status is not accepted: %s", p.Status)
	}
	if p.Status == domaintask.StatusWaiting && strings.TrimSpace(p.Reason) == "" {
		return errors.New("pending dialogue waiting completion requires a reason")
	}
	return nil
}

func validateDialogueCompletionCheckpoint(checkpoint GenerationCheckpoint, pending PendingCompletion) error {
	if checkpoint.Kind != "dialogue" {
		return fmt.Errorf("dialogue completion checkpoint kind mismatch: %q", checkpoint.Kind)
	}
	if err := validateIdleChatRunIdentity(checkpoint.TaskID, checkpoint.RunID); err != nil {
		return fmt.Errorf("dialogue completion checkpoint identity: %w", err)
	}
	artifact := checkpoint.DialogueArtifact
	if artifact == nil {
		return errors.New("dialogue completion checkpoint artifact is missing")
	}
	if artifact.SchemaVersion != DialogueEpisodeSchemaVersion {
		return fmt.Errorf("unsupported dialogue completion artifact schema version %d", artifact.SchemaVersion)
	}
	if artifact.EpisodeID == "" || artifact.Revision < 1 {
		return errors.New("dialogue completion artifact identity is invalid")
	}
	if err := validateIdleChatRunIdentity(artifact.TaskID, artifact.RunID); err != nil {
		return fmt.Errorf("dialogue completion artifact identity: %w", err)
	}
	if artifact.TaskID != checkpoint.TaskID || artifact.RunID != checkpoint.RunID {
		return errors.New("dialogue completion checkpoint and artifact Run identity do not match")
	}
	if dialogueCheckpointKey(artifact.SessionID, artifact.TopicResult, len(artifact.ArcPlan.TurnPlans)) != checkpoint.Key {
		return errors.New("dialogue completion checkpoint key does not match its artifact")
	}
	if err := validateDialogueCheckpointStage(checkpoint.Stage, *artifact); err != nil {
		return err
	}
	return pending.Validate()
}

func (s *DialogueEpisodeService) rememberPendingCompletion(checkpoint GenerationCheckpoint) {
	if s == nil || checkpoint.PendingCompletion == nil || strings.TrimSpace(checkpoint.Key) == "" {
		return
	}
	if s.pendingCompletions == nil {
		s.pendingCompletions = make(map[string]GenerationCheckpoint)
	}
	s.pendingCompletions[checkpoint.Key] = cloneGenerationCheckpoint(checkpoint)
}

func (s *DialogueEpisodeService) forgetPendingCompletion(key string) {
	if s == nil || s.pendingCompletions == nil {
		return
	}
	delete(s.pendingCompletions, strings.TrimSpace(key))
}

func sameDialogueCompletionPair(left, right GenerationCheckpoint) bool {
	return strings.TrimSpace(left.Key) == strings.TrimSpace(right.Key) &&
		left.TaskID == right.TaskID && left.RunID == right.RunID
}

func samePendingCompletion(left, right GenerationCheckpoint) bool {
	if !sameDialogueCompletionPair(left, right) || left.PendingCompletion == nil || right.PendingCompletion == nil {
		return false
	}
	return left.PendingCompletion.Status == right.PendingCompletion.Status &&
		left.PendingCompletion.Summary == right.PendingCompletion.Summary &&
		left.PendingCompletion.Reason == right.PendingCompletion.Reason
}

// finishDialogueGenerationRun records the exact owner completion before
// attempting it. A failed intent write still gets one canonical completion
// attempt so a newly issued Run is not leaked; the in-memory copy remains for
// the bounded admission/Stop retry.
func (s *DialogueEpisodeService) finishDialogueGenerationRun(ctx context.Context, checkpoint GenerationCheckpoint, status domaintask.Status, summary, reason string) error {
	if s == nil {
		return errors.New("dialogue episode service is nil")
	}
	if ctx == nil {
		return errors.New("dialogue completion context is nil")
	}
	pending := PendingCompletion{
		Status:  status,
		Summary: strings.TrimSpace(summary),
		Reason:  strings.TrimSpace(reason),
	}
	if err := validateDialogueCompletionCheckpoint(checkpoint, pending); err != nil {
		return err
	}
	checkpoint = cloneGenerationCheckpoint(checkpoint)
	checkpoint.PendingCompletion = &pending
	s.rememberPendingCompletion(checkpoint)

	var intentErr error
	if s.checkpoints == nil {
		intentErr = errors.New("dialogue checkpoint persistence is not configured")
	} else {
		intentErr = s.checkpoints.Put(checkpoint)
	}
	recovered, recoveryErr := recoverGenerationRunCompletionAfterProcessRestart(
		ctx,
		s.runIssuer,
		checkpoint,
		status,
		pending.Summary,
		pending.Reason,
	)
	if recoveryErr != nil {
		return errors.Join(intentErr, recoveryErr)
	}
	var completionErr error
	if !recovered {
		completionErr = finishGenerationRun(ctx, s.runIssuer, checkpoint, status, pending.Summary, pending.Reason)
	}
	if completionErr != nil {
		return errors.Join(intentErr, completionErr)
	}

	cleared := cloneGenerationCheckpoint(checkpoint)
	cleared.PendingCompletion = nil
	var clearErr error
	if s.checkpoints == nil {
		clearErr = errors.New("dialogue checkpoint persistence is not configured")
	} else {
		clearErr = s.checkpoints.Put(cleared)
	}
	if clearErr == nil {
		s.forgetPendingCompletion(checkpoint.Key)
	}
	return errors.Join(intentErr, clearErr)
}

func (s *DialogueEpisodeService) finalizePendingCompletionLocked(ctx context.Context, checkpoint GenerationCheckpoint) error {
	if checkpoint.PendingCompletion == nil {
		return errors.New("dialogue pending completion intent is missing")
	}
	pending := *checkpoint.PendingCompletion
	return s.finishDialogueGenerationRun(ctx, checkpoint, pending.Status, pending.Summary, pending.Reason)
}

func (s *DialogueEpisodeService) flushPendingCompletionsLocked(ctx context.Context) error {
	if ctx == nil {
		return errors.New("dialogue completion context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pendingByKey := make(map[string]GenerationCheckpoint)
	if s.checkpoints != nil {
		if err := s.checkpoints.LoadError(); err != nil {
			return fmt.Errorf("dialogue checkpoint store unavailable: %w", err)
		}
		for _, checkpoint := range s.checkpoints.ListPending() {
			if checkpoint.Kind == "dialogue" {
				pendingByKey[checkpoint.Key] = checkpoint
			}
		}
	}
	for key, checkpoint := range s.pendingCompletions {
		if checkpoint.Kind != "dialogue" {
			continue
		}
		persisted, persistedOK := GenerationCheckpoint{}, false
		if s.checkpoints != nil {
			persisted, persistedOK = s.checkpoints.Get(key)
		}
		if persistedOK {
			if persisted.PendingCompletion != nil && !samePendingCompletion(persisted, checkpoint) {
				return fmt.Errorf("dialogue completion intent mismatch for %s", key)
			}
			if persisted.PendingCompletion == nil && !sameDialogueCompletionPair(persisted, checkpoint) {
				return fmt.Errorf("dialogue completion Run pair mismatch for %s", key)
			}
			if _, alreadyPending := pendingByKey[key]; alreadyPending {
				continue
			}
		}
		pendingByKey[key] = cloneGenerationCheckpoint(checkpoint)
	}
	keys := make([]string, 0, len(pendingByKey))
	for key := range pendingByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		checkpoint := pendingByKey[key]
		if err := s.finalizePendingCompletionLocked(ctx, checkpoint); err != nil {
			return fmt.Errorf("finalize dialogue completion %s: %w", key, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.checkpoints != nil {
			pending := checkpoint.PendingCompletion
			if pending != nil && (pending.Status == domaintask.StatusSucceeded || pending.Status == domaintask.StatusFailed) {
				persisted, err := s.readPersistedDialogueArtifacts()
				if err != nil {
					return fmt.Errorf("verify dialogue completion artifact %s: %w", key, err)
				}
				artifact := checkpoint.DialogueArtifact
				proof := artifact != nil && dialogueArtifactIsDurable(persisted, *artifact)
				if pending.Status == domaintask.StatusSucceeded {
					proof = proof && artifact.ProductionStatus == DialogueProductionReady && artifact.Validation.Valid
				} else {
					proof = proof && artifact.ProductionStatus == DialogueProductionFailed && !artifact.Validation.Valid
				}
				if proof {
					if err := s.checkpoints.Delete(key); err != nil {
						return fmt.Errorf("delete dialogue completion checkpoint %s: %w", key, err)
					}
				}
			}
		}
	}
	return nil
}

// FinalizePendingRuns retries persisted and process-local dialogue completion
// intents in sorted order. It never calls the dialogue provider.
func (s *DialogueEpisodeService) FinalizePendingRuns(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("dialogue completion context is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushPendingCompletionsLocked(ctx)
}
