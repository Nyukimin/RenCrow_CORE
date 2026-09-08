package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// storyRevisionCheckpoint describes a durable mutation of an already stored
// story. The surrounding GenerationCheckpoint owns the Task/Run identity and
// the artifact snapshot; this value records which deterministic stage remains.
type storyRevisionCheckpoint struct {
	Operation   string           `json:"operation"`
	Phase       string           `json:"phase"`
	SourceRunID modulecore.RunID `json:"source_run_id"`
}

const (
	storyRevisionOperationBackfill  = "backfill"
	storyRevisionOperationTitle     = "title"
	storyRevisionOperationSuffix    = "suffix"
	storyRevisionOperationExhausted = "exhausted"

	storyRevisionPhaseInput  = "input"
	storyRevisionPhaseSuffix = "suffix"
	storyRevisionPhaseTitle  = "title"
	storyRevisionPhaseReview = "review"
	storyRevisionPhaseFinal  = "final"
)

func validateStoryRevisionCheckpoint(checkpoint GenerationCheckpoint) error {
	revision := checkpoint.StoryRevision
	if revision == nil {
		if checkpoint.Stage == "rerun_pending" {
			return errors.New("story rerun checkpoint is missing revision intent")
		}
		return nil
	}
	if checkpoint.StoryArtifact == nil {
		return errors.New("story revision checkpoint has no artifact")
	}
	if err := revision.SourceRunID.Validate(); err != nil {
		return fmt.Errorf("story revision source Run: %w", err)
	}
	if checkpoint.Stage != "rerun_pending" && checkpoint.RunID == revision.SourceRunID {
		return errors.New("story revision must execute under a new Run")
	}
	if checkpoint.Stage != "rerun_pending" && checkpoint.Stage != "resume_pending" && checkpoint.Stage != "artifact" && checkpoint.Stage != "review" {
		return fmt.Errorf("story revision checkpoint stage is invalid: %q", checkpoint.Stage)
	}
	validPhase := func(phase string) bool {
		switch phase {
		case storyRevisionPhaseInput, storyRevisionPhaseSuffix, storyRevisionPhaseTitle, storyRevisionPhaseReview, storyRevisionPhaseFinal:
			return true
		default:
			return false
		}
	}
	if !validPhase(revision.Phase) {
		return fmt.Errorf("story revision checkpoint phase is invalid: %q", revision.Phase)
	}
	switch revision.Operation {
	case storyRevisionOperationBackfill:
		if revision.Phase != storyRevisionPhaseInput && revision.Phase != storyRevisionPhaseTitle && revision.Phase != storyRevisionPhaseFinal {
			return fmt.Errorf("backfill revision phase is invalid: %q", revision.Phase)
		}
	case storyRevisionOperationTitle:
		if revision.Phase != storyRevisionPhaseInput && revision.Phase != storyRevisionPhaseTitle && revision.Phase != storyRevisionPhaseReview && revision.Phase != storyRevisionPhaseFinal {
			return fmt.Errorf("title revision phase is invalid: %q", revision.Phase)
		}
	case storyRevisionOperationSuffix:
		if revision.Phase != storyRevisionPhaseInput && revision.Phase != storyRevisionPhaseSuffix && revision.Phase != storyRevisionPhaseTitle && revision.Phase != storyRevisionPhaseReview && revision.Phase != storyRevisionPhaseFinal {
			return fmt.Errorf("suffix revision phase is invalid: %q", revision.Phase)
		}
	case storyRevisionOperationExhausted:
		if revision.Phase != storyRevisionPhaseInput && revision.Phase != storyRevisionPhaseFinal {
			return fmt.Errorf("exhausted revision phase is invalid: %q", revision.Phase)
		}
	default:
		return fmt.Errorf("story revision operation is invalid: %q", revision.Operation)
	}
	if checkpoint.Stage == "rerun_pending" && revision.Phase != storyRevisionPhaseInput {
		return errors.New("story rerun intent must begin at input phase")
	}
	if checkpoint.Stage != "rerun_pending" && checkpoint.Stage != "resume_pending" && checkpoint.Stage != storyRevisionStageForPhase(revision.Phase) {
		return errors.New("story revision stage does not match completed phase")
	}
	needsReview := revision.Operation == storyRevisionOperationTitle || revision.Operation == storyRevisionOperationSuffix
	reviewed := revision.Phase == storyRevisionPhaseReview || revision.Phase == storyRevisionPhaseFinal
	if needsReview && reviewed && checkpoint.StoryReview == nil {
		return errors.New("story revision review phase has no semantic review")
	}
	if checkpoint.StoryReview != nil && (!needsReview || !reviewed) {
		return errors.New("story revision semantic review does not match phase")
	}
	artifact := *checkpoint.StoryArtifact
	if revision.Operation == storyRevisionOperationBackfill {
		if artifact.ProductionStatus != StoryProductionReady || !artifact.Validation.Valid {
			return errors.New("story title backfill requires a validated ready artifact")
		}
		if revision.Phase == storyRevisionPhaseInput && strings.TrimSpace(artifact.StoryTitle) != "" {
			return errors.New("story title backfill input already has a title")
		}
	}
	if revision.Phase != storyRevisionPhaseInput && revision.Phase != storyRevisionPhaseSuffix && revision.Operation != storyRevisionOperationExhausted {
		if evidence := storyTitleValidationEvidence(artifact); evidence != "" {
			return fmt.Errorf("story revision title is invalid: %s", evidence)
		}
	}
	if revision.Operation == storyRevisionOperationExhausted && revision.Phase == storyRevisionPhaseFinal && artifact.ProductionStatus != StoryProductionFailed {
		return errors.New("exhausted story revision must remain failed")
	}
	if revision.Phase == storyRevisionPhaseFinal {
		if _, err := storyArtifactCompletionStatus(*checkpoint.StoryArtifact); err != nil {
			return err
		}
		if needsReview && !storyValidationMatches(checkpoint.StoryArtifact.Validation, ValidateStoryEpisode(*checkpoint.StoryArtifact, *checkpoint.StoryReview)) {
			return errors.New("story revision final validation does not match review")
		}
	}
	return nil
}

func storyRevisionStageForPhase(phase string) string {
	if phase == storyRevisionPhaseReview || phase == storyRevisionPhaseFinal {
		return "review"
	}
	return "artifact"
}

func storyRevisionPredecessorStatus(status domaintask.RunStatus) (domaintask.Status, bool) {
	switch status {
	case domaintask.RunStatusSucceeded:
		return domaintask.StatusSucceeded, true
	case domaintask.RunStatusFailed:
		return domaintask.StatusFailed, true
	case domaintask.RunStatusCancelled:
		return domaintask.StatusCancelled, true
	default:
		return "", false
	}
}

func (s *StoryEpisodeService) storyRevisionCheckpointStore() (*GenerationCheckpointStore, error) {
	if s == nil || s.store == nil || strings.TrimSpace(s.store.path) == "" {
		return nil, errors.New("story episode persistence is not configured")
	}
	store := s.generationCheckpointStore()
	if store == nil || strings.TrimSpace(store.path) == "" {
		return nil, errors.New("story generation checkpoint persistence is not configured")
	}
	if err := store.LoadError(); err != nil {
		return nil, fmt.Errorf("story generation checkpoint store unavailable: %w", err)
	}
	if err := s.store.loadError(); err != nil {
		return nil, fmt.Errorf("story episode store unavailable: %w", err)
	}
	return store, nil
}

func (s *StoryEpisodeService) hasNormalStoryCheckpoint(store *GenerationCheckpointStore) bool {
	if store == nil {
		return false
	}
	checkpoint, ok := store.Get(storyGenerationCheckpointKey)
	return ok && checkpoint.StoryRevision == nil
}

// recoverStoryRevisionLocked runs a persisted revision before its caller makes
// a stock or eligibility decision. The caller must hold prepareMu.
func (s *StoryEpisodeService) recoverStoryRevisionLocked(ctx context.Context) error {
	checkpointStore, err := s.storyRevisionCheckpointStore()
	if err != nil {
		return err
	}
	checkpoint, ok := checkpointStore.Get(storyGenerationCheckpointKey)
	if !ok || checkpoint.StoryRevision == nil {
		return nil
	}
	if err := validateStoryGenerationCheckpoint(checkpoint); err != nil {
		return err
	}
	if err := s.continueStoryRevisionLocked(ctx, checkpointStore, &checkpoint); err != nil {
		return err
	}
	if _, stillPending := checkpointStore.Get(storyGenerationCheckpointKey); stillPending {
		return errors.New("story revision remained pending after continuation")
	}
	return nil
}

func (s *StoryEpisodeService) putStoryRevisionProgress(checkpointStore *GenerationCheckpointStore, checkpoint *GenerationCheckpoint, phase, stage string, artifact StoryEpisodeArtifact, review *StorySemanticReview) error {
	if checkpoint == nil || checkpoint.StoryRevision == nil {
		return errors.New("story revision checkpoint is missing")
	}
	checkpoint.StoryArtifact = &artifact
	checkpoint.StoryReview = review
	checkpoint.StoryRevision.Phase = phase
	checkpoint.Stage = stage
	return checkpointStore.Put(*checkpoint)
}

func (s *StoryEpisodeService) beginStoryRevisionLocked(ctx context.Context, checkpointStore *GenerationCheckpointStore, operation string, artifact StoryEpisodeArtifact) (GenerationCheckpoint, error) {
	if err := validateIdleChatRunIdentity(artifact.TaskID, artifact.RunID); err != nil {
		return GenerationCheckpoint{}, err
	}
	if _, found := checkpointStore.Get(storyGenerationCheckpointKey); found {
		if s.hasNormalStoryCheckpoint(checkpointStore) {
			return GenerationCheckpoint{}, errors.New("normal story generation checkpoint is pending")
		}
		return GenerationCheckpoint{}, errors.New("another story revision is pending")
	}
	if _, err := generationRunOwnerFromIssuer(s.runIssuer); err != nil {
		return GenerationCheckpoint{}, err
	}
	predecessor, err := inspectGenerationRun(ctx, s.runIssuer, artifact.TaskID, artifact.RunID)
	if err != nil {
		return GenerationCheckpoint{}, err
	}
	predecessorStatus, ok := storyRevisionPredecessorStatus(predecessor.Status)
	if !ok {
		return GenerationCheckpoint{}, fmt.Errorf("story revision predecessor run %s is not terminal: %s", predecessor.RunID, predecessor.Status)
	}
	// Verify the terminal predecessor and its owning Task in the canonical
	// owner transaction before any explicit rerun can be issued.
	if err := completeGenerationRun(ctx, s.runIssuer, artifact.TaskID, artifact.RunID, predecessorStatus, "story revision predecessor verified", ""); err != nil {
		return GenerationCheckpoint{}, err
	}
	artifact = cloneStoryEpisode(artifact)
	checkpoint := GenerationCheckpoint{
		Key:           storyGenerationCheckpointKey,
		Kind:          "story",
		TaskID:        artifact.TaskID,
		RunID:         artifact.RunID,
		Stage:         "rerun_pending",
		StoryArtifact: &artifact,
		StoryRevision: &storyRevisionCheckpoint{Operation: operation, Phase: storyRevisionPhaseInput, SourceRunID: artifact.RunID},
	}
	if err := checkpointStore.Put(checkpoint); err != nil {
		return GenerationCheckpoint{}, err
	}

	return checkpoint, nil
}

func (s *StoryEpisodeService) finishStoryRevisionSuccessorOnError(ctx context.Context, checkpoint GenerationCheckpoint, successorID modulecore.RunID, cause error) error {
	if successorID == "" {
		return cause
	}
	checkpoint.RunID = successorID
	return s.storyRevisionFailure(ctx, checkpoint, cause, "story revision checkpoint recovery failed", "retry from saved story revision checkpoint")
}

func (s *StoryEpisodeService) reconcileStoryRevisionRunLocked(ctx context.Context, checkpointStore *GenerationCheckpointStore, checkpoint *GenerationCheckpoint) error {
	if checkpoint == nil || checkpoint.StoryRevision == nil {
		return errors.New("story revision checkpoint is missing")
	}
	for {
		switch checkpoint.Stage {
		case "rerun_pending":
			previousRunID := checkpoint.RunID
			if err := reconcileGenerationRerun(ctx, s.runIssuer, checkpoint, checkpointStore); err != nil {
				if checkpoint.RunID != previousRunID {
					return s.finishStoryRevisionSuccessorOnError(ctx, *checkpoint, checkpoint.RunID, err)
				}
				// The explicit issuance may have failed before a successor existed.
				return err
			}
			if checkpoint.RunID == checkpoint.StoryRevision.SourceRunID {
				predecessor, err := inspectGenerationRun(ctx, s.runIssuer, checkpoint.TaskID, checkpoint.RunID)
				if err != nil {
					return err
				}
				status, ok := storyRevisionPredecessorStatus(predecessor.Status)
				if !ok {
					return fmt.Errorf("story revision predecessor run %s is not terminal: %s", predecessor.RunID, predecessor.Status)
				}
				if err := completeGenerationRun(ctx, s.runIssuer, checkpoint.TaskID, checkpoint.RunID, status, "story revision predecessor verified", ""); err != nil {
					return err
				}
				successor, err := rerunIdleChatRun(ctx, s.runIssuer, checkpoint, checkpointStore)
				if err != nil {
					return err
				}
				successorID := successor.RunID
				if err := reconcileGenerationRerun(ctx, s.runIssuer, checkpoint, checkpointStore); err != nil {
					return s.finishStoryRevisionSuccessorOnError(ctx, *checkpoint, successorID, err)
				}
				if checkpoint.RunID == previousRunID {
					return errors.New("story revision rerun did not produce a successor")
				}
			} else {
				run, err := inspectGenerationRun(ctx, s.runIssuer, checkpoint.TaskID, checkpoint.RunID)
				if err != nil {
					return err
				}
				if run.StartReason != domaintask.RunStartReasonExplicitRerun || (run.Status != domaintask.RunStatusRunning && run.Status != domaintask.RunStatusWaiting && run.Status != domaintask.RunStatusInterrupted) {
					return errors.New("story revision bound rerun is not resumable")
				}
			}
			checkpoint.Stage = storyRevisionStageForPhase(checkpoint.StoryRevision.Phase)
			if err := checkpointStore.Put(*checkpoint); err != nil {
				return s.finishStoryRevisionSuccessorOnError(ctx, *checkpoint, checkpoint.RunID, err)
			}
			continue

		case "resume_pending":
			previousRunID := checkpoint.RunID
			if err := reconcileGenerationResume(ctx, s.runIssuer, checkpoint, checkpointStore); err != nil {
				if checkpoint.RunID != previousRunID {
					return s.finishStoryRevisionSuccessorOnError(ctx, *checkpoint, checkpoint.RunID, err)
				}
				return err
			}
			if checkpoint.RunID == previousRunID {
				previous, err := inspectGenerationRun(ctx, s.runIssuer, checkpoint.TaskID, checkpoint.RunID)
				if err != nil {
					return err
				}
				if previous.Status != domaintask.RunStatusWaiting && previous.Status != domaintask.RunStatusInterrupted {
					if previous.Status == domaintask.RunStatusRunning {
						checkpoint.Stage = storyRevisionStageForPhase(checkpoint.StoryRevision.Phase)
						if err := checkpointStore.Put(*checkpoint); err != nil {
							return s.finishStoryRevisionSuccessorOnError(ctx, *checkpoint, checkpoint.RunID, err)
						}
						continue
					}
					return fmt.Errorf("story revision resume predecessor run %s is not waiting: %s", previous.RunID, previous.Status)
				}
				successor, err := resumeIdleChatRun(ctx, s.runIssuer, checkpoint, checkpointStore)
				if err != nil {
					return err
				}
				successorID := successor.RunID
				if err := reconcileGenerationResume(ctx, s.runIssuer, checkpoint, checkpointStore); err != nil {
					return s.finishStoryRevisionSuccessorOnError(ctx, *checkpoint, successorID, err)
				}
				if checkpoint.RunID == previousRunID {
					return errors.New("story revision resume did not produce a successor")
				}
			}
			checkpoint.Stage = storyRevisionStageForPhase(checkpoint.StoryRevision.Phase)
			if err := checkpointStore.Put(*checkpoint); err != nil {
				return s.finishStoryRevisionSuccessorOnError(ctx, *checkpoint, checkpoint.RunID, err)
			}
			continue
		}

		run, err := inspectGenerationRun(ctx, s.runIssuer, checkpoint.TaskID, checkpoint.RunID)
		if err != nil {
			return err
		}
		if run.Status == domaintask.RunStatusWaiting || run.Status == domaintask.RunStatusInterrupted {
			checkpoint.Stage = "resume_pending"
			if err := checkpointStore.Put(*checkpoint); err != nil {
				return err
			}
			continue
		}
		if run.Status != domaintask.RunStatusRunning {
			return fmt.Errorf("story revision run %s is not running: %s", checkpoint.RunID, run.Status)
		}
		return nil
	}
}

func (s *StoryEpisodeService) storyRevisionFailure(ctx context.Context, checkpoint GenerationCheckpoint, cause error, summary, reason string) error {
	completionErr := finishGenerationRun(ctx, s.runIssuer, checkpoint, domaintask.StatusWaiting, summary, reason)
	return errors.Join(cause, completionErr)
}

func (s *StoryEpisodeService) validateSavedStoryRevisionArtifact(checkpoint GenerationCheckpoint, stored StoryEpisodeArtifact) error {
	if checkpoint.StoryRevision == nil || checkpoint.StoryArtifact == nil || checkpoint.StoryRevision.Phase != storyRevisionPhaseFinal {
		return errors.New("story revision final artifact checkpoint is incomplete")
	}
	if !storyArtifactSameRevisionIgnoringPlayback(stored, *checkpoint.StoryArtifact) {
		return fmt.Errorf("story revision artifact %s is mismatched", stored.EpisodeID)
	}
	return nil
}

func (s *StoryEpisodeService) beginOrRecoverStoryRevisionLocked(ctx context.Context, checkpointStore *GenerationCheckpointStore, operation string, artifact StoryEpisodeArtifact) error {
	checkpoint, err := s.beginStoryRevisionLocked(ctx, checkpointStore, operation, artifact)
	if err != nil {
		return err
	}
	return s.continueStoryRevisionLocked(ctx, checkpointStore, &checkpoint)
}

func (s *StoryEpisodeService) continueStoryRevisionLocked(ctx context.Context, checkpointStore *GenerationCheckpointStore, checkpoint *GenerationCheckpoint) error {
	if checkpoint == nil || checkpoint.StoryRevision == nil {
		return errors.New("story revision checkpoint is missing")
	}
	if err := validateStoryGenerationCheckpoint(*checkpoint); err != nil {
		return err
	}
	if err := s.verifyStoryRevisionSource(ctx, *checkpoint); err != nil {
		return err
	}
	if checkpoint.StoryRevision.Phase == storyRevisionPhaseFinal && checkpoint.Stage != "resume_pending" {
		run, err := inspectGenerationRun(ctx, s.runIssuer, checkpoint.TaskID, checkpoint.RunID)
		if err != nil {
			return err
		}
		if _, terminal := storyRevisionPredecessorStatus(run.Status); terminal {
			stored, found := s.store.artifactByRunID(checkpoint.RunID)
			if !found {
				return errors.New("terminal story revision has no persisted artifact")
			}
			if err := s.validateSavedStoryRevisionArtifact(*checkpoint, stored); err != nil {
				return err
			}
			return s.finalizeStoryRevisionLocked(ctx, checkpointStore, checkpoint)
		}
	}
	if err := s.reconcileStoryRevisionRunLocked(ctx, checkpointStore, checkpoint); err != nil {
		return err
	}
	artifact := cloneStoryEpisode(*checkpoint.StoryArtifact)
	operation := checkpoint.StoryRevision.Operation
	phase := checkpoint.StoryRevision.Phase

	save := func(next string, review *StorySemanticReview) error {
		if err := s.putStoryRevisionProgress(checkpointStore, checkpoint, next, storyRevisionStageForPhase(next), artifact, review); err != nil {
			return s.storyRevisionFailure(ctx, *checkpoint, err, "story revision checkpoint save failed", "retry from saved story revision phase")
		}
		phase = next
		return nil
	}
	if operation == storyRevisionOperationExhausted && phase == storyRevisionPhaseInput {
		artifact.ProductionStatus = StoryProductionFailed
		if err := save(storyRevisionPhaseFinal, nil); err != nil {
			return err
		}
	}
	if operation == storyRevisionOperationSuffix && phase == storyRevisionPhaseInput {
		from := storyRepairFromTurn(artifact)
		turns, err := s.generateStorySuffix(ctx, artifact, checkpoint.TaskID, checkpoint.RunID)
		if err != nil {
			return s.storyRevisionFailure(ctx, *checkpoint, err, "story suffix repair interrupted", "retry from saved story revision phase")
		}
		artifact.Turns = turns
		artifact.FixedPrefixLength = from - 1
		artifact.RepairFromTurn = from
		artifact.SuffixRegenerations++
		if err := save(storyRevisionPhaseSuffix, nil); err != nil {
			return err
		}
	}
	if phase == storyRevisionPhaseInput || phase == storyRevisionPhaseSuffix {
		if operation != storyRevisionOperationSuffix || strings.TrimSpace(artifact.StoryTitle) == "" || storyValidationHasCode(artifact.Validation, "title_violation") {
			title, err := s.generateStoryTitle(ctx, artifact, checkpoint.TaskID, checkpoint.RunID)
			if err != nil {
				return s.storyRevisionFailure(ctx, *checkpoint, err, "story revision title interrupted", "retry from saved story revision phase")
			}
			artifact.StoryTitle = title
		}
		if err := save(storyRevisionPhaseTitle, nil); err != nil {
			return err
		}
	}
	if phase == storyRevisionPhaseTitle {
		if operation == storyRevisionOperationBackfill {
			// Legacy title backfill preserves the previously accepted body and
			// validation. generateStoryTitle has checked the new title itself.
			if err := save(storyRevisionPhaseFinal, nil); err != nil {
				return err
			}
		} else {
			review, err := s.reviewArtifact(ctx, artifact, checkpoint.TaskID, checkpoint.RunID)
			if err != nil {
				return s.storyRevisionFailure(ctx, *checkpoint, err, "story revision review interrupted", "retry from saved story revision phase")
			}
			if err := save(storyRevisionPhaseReview, &review); err != nil {
				return err
			}
		}
	}
	if phase == storyRevisionPhaseReview {
		artifact.Validation = ValidateStoryEpisode(artifact, *checkpoint.StoryReview)
		if artifact.Validation.Valid {
			artifact.ProductionStatus = StoryProductionReady
		} else if operation == storyRevisionOperationSuffix && artifact.SuffixRegenerations >= s.maxSuffixRegenerations {
			artifact.ProductionStatus = StoryProductionFailed
		} else {
			artifact.ProductionStatus = StoryProductionNeedsRepair
		}
		if err := save(storyRevisionPhaseFinal, checkpoint.StoryReview); err != nil {
			return err
		}
	}

	if phase != storyRevisionPhaseFinal {
		return fmt.Errorf("story revision %s stopped at phase %s", operation, phase)
	}
	return s.finalizeStoryRevisionLocked(ctx, checkpointStore, checkpoint)
}

func (s *StoryEpisodeService) finalizeStoryRevisionLocked(ctx context.Context, checkpointStore *GenerationCheckpointStore, checkpoint *GenerationCheckpoint) error {
	artifact := cloneStoryEpisode(*checkpoint.StoryArtifact)
	if stored, ok := s.store.artifactByRunID(checkpoint.RunID); ok {
		if err := s.validateSavedStoryRevisionArtifact(*checkpoint, stored); err != nil {
			return err
		}
		artifact = stored
	} else {
		if err := s.store.append(artifact); err != nil {
			s.store.recordFailure("storage", err)
			return s.storyRevisionFailure(ctx, *checkpoint, err, "story revision save failed", "retry saving story revision")
		}
	}
	status, err := storyArtifactCompletionStatus(artifact)
	if err != nil {
		return err
	}
	if err := finishGenerationRun(ctx, s.runIssuer, *checkpoint, status, "story revision saved", ""); err != nil {
		return err
	}
	if err := checkpointStore.Delete(storyGenerationCheckpointKey); err != nil {
		return err
	}
	s.store.recordFailure("", nil)
	if status == domaintask.StatusFailed && checkpoint.StoryRevision.Operation != storyRevisionOperationExhausted {
		return fmt.Errorf("story episode %s remains needs_repair", artifact.EpisodeID)
	}
	return nil
}

func (s *StoryEpisodeService) generateStorySuffix(ctx context.Context, artifact StoryEpisodeArtifact, taskID modulecore.TaskID, runID modulecore.RunID) ([]StoryEpisodeTurn, error) {
	from := storyRepairFromTurn(artifact)
	prefixLength := from - 1
	prefix := append([]StoryEpisodeTurn(nil), artifact.Turns[:prefixLength]...)
	payload, err := json.Marshal(artifact)
	if err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf(`あなたはRenCrow IdleChat物語のsuffix修復担当です。
turn %dより前は合格済みで変更禁止です。turn %d以降だけを、末尾まで作り直してください。
reader=%s、listener=%s、story_contractとstory_ledgerを維持し、検出errorをすべて解消してください。
JSON以外を付けず、{"turns":[...]}だけを返してください。各turnのmessage_idは省略してください。
対象episode:
%s`, from, from, artifact.Reader, artifact.Listener, string(payload))
	raw, err := generateIdleChatCodexWithRun(ctx, s.runIssuer, taskID, runID, s.generator, prompt)
	if err != nil {
		return nil, fmt.Errorf("CodexExe suffix repair: %w", err)
	}
	var suffix struct {
		Turns []StoryEpisodeTurn `json:"turns"`
	}
	if err := decodeStoryJSON(raw, &suffix); err != nil {
		return nil, fmt.Errorf("decode CodexExe suffix repair: %w", err)
	}
	if len(suffix.Turns) == 0 {
		return nil, errors.New("CodexExe suffix repair returned no turns")
	}
	for i := range suffix.Turns {
		suffix.Turns[i].TurnIndex = prefixLength + i + 1
		suffix.Turns[i].MessageID = newIdleChatMessageID()
		if suffix.Turns[i].UtteranceRole == StoryUtteranceNarration {
			suffix.Turns[i].ReactsTo = 0
		}
	}
	return append(prefix, suffix.Turns...), nil
}

func storyRepairFromTurn(artifact StoryEpisodeArtifact) int {
	from := artifact.Validation.FirstInvalidTurn
	if from < 1 || from > len(artifact.Turns) {
		return 1
	}
	return from
}

// verifyStoryRevisionSource binds recovery to the stored episode before any
// generation or Run issuance. Only the operation's intended content fields may
// differ; the accepted prefix, source, contract and ledger remain immutable.
func (s *StoryEpisodeService) verifyStoryRevisionSource(ctx context.Context, checkpoint GenerationCheckpoint) error {
	artifact := *checkpoint.StoryArtifact
	revision := checkpoint.StoryRevision
	stored, err := s.storyRevisionSourceArtifact(checkpoint)
	if err != nil {
		return err
	}
	owner, err := generationRunOwnerFromIssuer(s.runIssuer)
	if err != nil {
		return err
	}
	runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: checkpoint.TaskID})
	if err != nil {
		return err
	}
	found := false
	for _, run := range runs {
		if run.RunID != revision.SourceRunID {
			continue
		}
		if run.TaskID != checkpoint.TaskID {
			return errors.New("story revision source Run Task does not match")
		}
		if _, terminal := storyRevisionPredecessorStatus(run.Status); !terminal {
			return errors.New("story revision source Run is not terminal")
		}
		found = true
	}
	if !found {
		return errors.New("story revision source Run is missing from owner history")
	}
	// The current store may already contain a final result whose completion or
	// cleanup failed. Such recovery can change execution identity, not content.
	if stored.RunID != revision.SourceRunID {
		if revision.Phase != storyRevisionPhaseFinal {
			return errors.New("story revision source was superseded before final phase")
		}
		candidate := artifact
		candidate.RunID, candidate.Revision = stored.RunID, stored.Revision
		if !storyArtifactSameRevisionIgnoringPlayback(candidate, stored) {
			return errors.New("story revision saved result is mismatched")
		}
		return nil
	}
	candidate := artifact
	candidate.RunID, candidate.Revision = stored.RunID, stored.Revision
	if revision.Phase != storyRevisionPhaseInput {
		candidate.StoryTitle = stored.StoryTitle
		if revision.Operation == storyRevisionOperationSuffix {
			prefix := storyRepairFromTurn(stored) - 1
			if artifact.FixedPrefixLength != prefix || artifact.RepairFromTurn != prefix+1 || artifact.SuffixRegenerations != stored.SuffixRegenerations+1 || len(artifact.Turns) < prefix {
				return errors.New("story revision suffix boundary does not match source")
			}
			for i := 0; i < prefix; i++ {
				if artifact.Turns[i] != stored.Turns[i] {
					return errors.New("story revision changed accepted prefix")
				}
			}
			candidate.Turns = stored.Turns
			candidate.FixedPrefixLength, candidate.RepairFromTurn, candidate.SuffixRegenerations = stored.FixedPrefixLength, stored.RepairFromTurn, stored.SuffixRegenerations
		}
		if revision.Phase == storyRevisionPhaseFinal {
			candidate.Validation, candidate.ProductionStatus = stored.Validation, stored.ProductionStatus
		}
	}
	if !storyArtifactSameRevisionIgnoringPlayback(candidate, stored) {
		return errors.New("story revision changed immutable source fields")
	}
	return nil
}

func (s *StoryEpisodeService) storyRevisionSourceArtifact(checkpoint GenerationCheckpoint) (StoryEpisodeArtifact, error) {
	artifact := *checkpoint.StoryArtifact
	revision := checkpoint.StoryRevision
	stored, ok := s.store.get(artifact.EpisodeID)
	if !ok {
		return StoryEpisodeArtifact{}, errors.New("story revision has no persisted artifact for its source episode")
	}
	if stored.TaskID != checkpoint.TaskID {
		return StoryEpisodeArtifact{}, errors.New("story revision source Task does not match")
	}
	if source, found := s.store.artifactByRunID(revision.SourceRunID); found && source.EpisodeID != artifact.EpisodeID {
		return StoryEpisodeArtifact{}, errors.New("story revision source Run belongs to another episode")
	}
	return stored, nil
}
