package idlechat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/google/uuid"
)

// DialogueEpisodeService prepares a complete dialogue through CodexExe, then
// validates it turn-by-turn in CORE. Only the suffix beginning at the first
// invalid turn may be regenerated.
type DialogueEpisodeService struct {
	path                   string
	generator              IdleChatCodexGenerator
	personas               map[string]string
	config                 DialogueInterestingnessConfig
	maxSuffixRegenerations int
	runIssuer              idlechatRunIssuer
	checkpoints            *GenerationCheckpointStore
	mu                     sync.Mutex
}

func (o *IdleChatOrchestrator) SetDialogueEpisodeService(service *DialogueEpisodeService) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.dialogueEpisodeService = service
	o.mu.Unlock()
}

func (o *IdleChatOrchestrator) prepareDialogueEpisode(sessionID string, result TopicGenerationResult, turnCount int) (DialogueEpisodeArtifact, error) {
	if o == nil {
		return DialogueEpisodeArtifact{}, errors.New("idlechat orchestrator is nil")
	}
	o.mu.Lock()
	service := o.dialogueEpisodeService
	o.mu.Unlock()
	if service == nil {
		return DialogueEpisodeArtifact{}, errors.New("dialogue CodexExe producer is not configured")
	}
	return service.Prepare(o.idleRunContext(), sessionID, result, turnCount)
}

func NewPersistentDialogueEpisodeService(path string, generator IdleChatCodexGenerator, personas map[string]string, config DialogueInterestingnessConfig) *DialogueEpisodeService {
	path = strings.TrimSpace(path)
	cloned := make(map[string]string, len(personas))
	for name, prompt := range personas {
		cloned[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(prompt)
	}
	var checkpoints *GenerationCheckpointStore
	if path != "" {
		checkpoints = NewGenerationCheckpointStore(path + ".checkpoints.json")
	}
	return &DialogueEpisodeService{
		path:                   path,
		generator:              generator,
		personas:               cloned,
		config:                 normalizeDialogueInterestingnessConfig(config),
		maxSuffixRegenerations: 3,
		checkpoints:            checkpoints,
	}
}

func (s *DialogueEpisodeService) SetMaxSuffixRegenerations(limit int) {
	if s != nil && limit > 0 {
		s.maxSuffixRegenerations = limit
	}
}

func (s *DialogueEpisodeService) SetRunIssuer(issuer idlechatRunIssuer) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.runIssuer = issuer
	s.mu.Unlock()
}

func (s *DialogueEpisodeService) SetGenerationCheckpointStore(store *GenerationCheckpointStore) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.checkpoints = store
	s.mu.Unlock()
}

func (s *DialogueEpisodeService) Prepare(ctx context.Context, sessionID string, result TopicGenerationResult, turnCount int) (DialogueEpisodeArtifact, error) {
	if s == nil || s.generator == nil {
		return DialogueEpisodeArtifact{}, errors.New("dialogue CodexExe producer is not configured")
	}
	if ctx == nil {
		return DialogueEpisodeArtifact{}, errors.New("dialogue generation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return DialogueEpisodeArtifact{}, errors.New("dialogue episode persistence is not configured")
	}
	if _, err := generationRunOwnerFromIssuer(s.runIssuer); err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	checkpoints := s.checkpoints
	if checkpoints == nil || strings.TrimSpace(checkpoints.path) == "" {
		return DialogueEpisodeArtifact{}, errors.New("dialogue checkpoint persistence is not configured")
	}
	if err := checkpoints.LoadError(); err != nil {
		return DialogueEpisodeArtifact{}, fmt.Errorf("dialogue checkpoint store unavailable: %w", err)
	}
	if turnCount <= 0 {
		turnCount = normalizeDialogueInterestingnessConfig(s.config).MaxTurnsPerTopic
	}
	if turnCount > maxTurnsPerTopic && result.Category != TopicCategoryForecast {
		turnCount = maxTurnsPerTopic
	}
	sessionID = strings.TrimSpace(sessionID)
	director := NewDialogueDirector(s.config)
	plan := director.BuildArcPlan(result)
	result.Category = plan.Category
	plan.TurnPlans = buildDialogueTurnPlans(turnCount, dialogueCategorySpec(result.Category))
	checkpointKey := dialogueCheckpointKey(sessionID, result, turnCount)
	persisted, err := s.readPersistedDialogueArtifacts()
	if err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	checkpoint, found := checkpoints.Get(checkpointKey)
	if found {
		return s.resumeDialogueCheckpoint(ctx, checkpointKey, checkpoint, sessionID, result, turnCount, persisted)
	}
	if existing, ok := latestDialogueReadyArtifact(persisted, checkpointKey); ok {
		run, err := inspectGenerationRun(ctx, s.runIssuer, existing.TaskID, existing.RunID)
		if err != nil {
			return DialogueEpisodeArtifact{}, err
		}
		if run.Status != domaintask.RunStatusSucceeded {
			return DialogueEpisodeArtifact{}, fmt.Errorf("durable dialogue episode %s has nonterminal owner Run %s", existing.EpisodeID, run.Status)
		}
		checkpoint := GenerationCheckpoint{
			Key: checkpointKey, Kind: "dialogue", TaskID: existing.TaskID, RunID: existing.RunID,
			Stage: "ready", DialogueArtifact: cloneDialogueEpisodePtr(existing),
		}
		if err := finishGenerationRun(ctx, s.runIssuer, checkpoint, domaintask.StatusSucceeded, "dialogue episode already saved", ""); err != nil {
			return DialogueEpisodeArtifact{}, err
		}
		return existing, nil
	}

	initiatedBy := strings.TrimSpace(result.Initiator)
	if initiatedBy == "" {
		initiatedBy = "shiro"
	}
	// A generation Run must exist before CodexExe is called. The seed checkpoint
	// makes that owner-issued identity recoverable if the first generation fails.
	taskID, runID, err := issueIdleChatRun(ctx, s.runIssuer, "IdleChat dialogue episode", initiatedBy, domaintask.RunStartReasonFirst, "")
	if err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	now := time.Now().UTC()
	artifact := DialogueEpisodeArtifact{
		SchemaVersion:    DialogueEpisodeSchemaVersion,
		EpisodeID:        "dialogue-" + uuid.NewString(),
		TaskID:           taskID,
		RunID:            runID,
		Revision:         1,
		SessionID:        sessionID,
		InitiatedBy:      initiatedBy,
		TopicResult:      result,
		ArcPlan:          plan,
		Participants:     []string{"mio", "shiro"},
		ProductionStatus: DialogueProductionValidating,
		Validation:       DialogueEpisodeValidation{Valid: false, FirstInvalidTurn: 1},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	checkpoint = GenerationCheckpoint{
		Key: checkpointKey, Kind: "dialogue", TaskID: taskID, RunID: runID, Stage: "seed",
		DialogueArtifact: cloneDialogueEpisodePtr(artifact),
	}
	if err := checkpoints.Put(checkpoint); err != nil {
		finishErr := finishGenerationRun(ctx, s.runIssuer, checkpoint, domaintask.StatusFailed, "dialogue seed checkpoint save failed", "")
		return DialogueEpisodeArtifact{}, errors.Join(err, finishErr)
	}
	return s.continueDialogueCheckpoint(ctx, checkpointKey, checkpoint, turnCount, persisted)
}

type dialogueCheckpointIdentity struct {
	SessionID string                `json:"session_id"`
	Result    TopicGenerationResult `json:"result"`
	TurnCount int                   `json:"turn_count"`
}

func dialogueCheckpointKey(sessionID string, result TopicGenerationResult, turnCount int) string {
	identity := dialogueCheckpointIdentity{
		SessionID: strings.TrimSpace(sessionID), Result: result, TurnCount: turnCount,
	}
	payload, _ := json.Marshal(identity)
	sum := sha256.Sum256(payload)
	return "dialogue:" + fmt.Sprintf("%x", sum[:])
}

func cloneDialogueEpisodePtr(artifact DialogueEpisodeArtifact) *DialogueEpisodeArtifact {
	clone := cloneDialogueEpisode(artifact)
	return &clone
}

func dialogueCheckpointStage(artifact DialogueEpisodeArtifact) string {
	if artifact.ProductionStatus == DialogueProductionValidating && len(artifact.Turns) == 0 {
		return "seed"
	}
	switch artifact.ProductionStatus {
	case DialogueProductionReady:
		return "ready"
	case DialogueProductionNeedsRepair:
		return "needs_repair"
	case DialogueProductionFailed:
		return "failed"
	default:
		return "artifact"
	}
}

func validateDialogueCheckpointStage(stage string, artifact DialogueEpisodeArtifact) error {
	if stage == "resume_pending" {
		stage = dialogueCheckpointStage(artifact)
	}
	switch stage {
	case "seed":
		if artifact.ProductionStatus != DialogueProductionValidating || len(artifact.Turns) != 0 || artifact.Validation.Valid {
			return errors.New("dialogue seed checkpoint is inconsistent with its artifact")
		}
	case "artifact":
		if artifact.ProductionStatus != DialogueProductionValidating || len(artifact.Turns) == 0 || artifact.Validation.Valid {
			return errors.New("dialogue artifact checkpoint is inconsistent with its artifact")
		}
	case "needs_repair":
		if artifact.ProductionStatus != DialogueProductionNeedsRepair || artifact.Validation.Valid {
			return errors.New("dialogue repair checkpoint is inconsistent with its artifact")
		}
	case "ready":
		if artifact.ProductionStatus != DialogueProductionReady || !artifact.Validation.Valid {
			return errors.New("dialogue ready checkpoint is inconsistent with its artifact")
		}
	case "failed":
		if artifact.ProductionStatus != DialogueProductionFailed || artifact.Validation.Valid {
			return errors.New("dialogue failed checkpoint is inconsistent with its artifact")
		}
	default:
		return fmt.Errorf("invalid dialogue checkpoint stage %q", stage)
	}
	return nil
}

func (s *DialogueEpisodeService) saveDialogueArtifactCheckpoint(checkpoint *GenerationCheckpoint, artifact DialogueEpisodeArtifact) error {
	if checkpoint == nil || s == nil || s.checkpoints == nil {
		return errors.New("dialogue checkpoint persistence is not configured")
	}
	artifact = cloneDialogueEpisode(artifact)
	checkpoint.TaskID = artifact.TaskID
	checkpoint.RunID = artifact.RunID
	checkpoint.Stage = dialogueCheckpointStage(artifact)
	checkpoint.DialogueArtifact = &artifact
	return s.checkpoints.Put(*checkpoint)
}

func validateDialogueCheckpoint(checkpoint GenerationCheckpoint, key, sessionID string, result TopicGenerationResult, turnCount int) error {
	if checkpoint.Key != key {
		return fmt.Errorf("dialogue checkpoint key mismatch: got %q, want %q", checkpoint.Key, key)
	}
	if checkpoint.Kind != "dialogue" {
		return fmt.Errorf("dialogue checkpoint kind mismatch: %q", checkpoint.Kind)
	}
	if err := validateIdleChatRunIdentity(checkpoint.TaskID, checkpoint.RunID); err != nil {
		return fmt.Errorf("dialogue checkpoint identity: %w", err)
	}
	artifact := checkpoint.DialogueArtifact
	if artifact == nil {
		return errors.New("dialogue checkpoint artifact is missing")
	}
	if artifact.SchemaVersion != DialogueEpisodeSchemaVersion {
		return fmt.Errorf("unsupported dialogue artifact schema version %d", artifact.SchemaVersion)
	}
	if artifact.EpisodeID == "" || artifact.Revision < 1 {
		return errors.New("dialogue checkpoint artifact identity is invalid")
	}
	if err := validateIdleChatRunIdentity(artifact.TaskID, artifact.RunID); err != nil {
		return fmt.Errorf("dialogue checkpoint artifact identity: %w", err)
	}
	if artifact.TaskID != checkpoint.TaskID || artifact.RunID != checkpoint.RunID {
		return errors.New("dialogue checkpoint and artifact Run identity do not match")
	}
	if strings.TrimSpace(artifact.SessionID) != strings.TrimSpace(sessionID) {
		return errors.New("dialogue checkpoint session_id mismatch")
	}
	if dialogueCheckpointKey(artifact.SessionID, artifact.TopicResult, len(artifact.ArcPlan.TurnPlans)) != key ||
		dialogueCheckpointKey(sessionID, result, turnCount) != key {
		return errors.New("dialogue checkpoint input mismatch")
	}
	if err := validateDialogueCheckpointStage(checkpoint.Stage, *artifact); err != nil {
		return err
	}
	return nil
}

func (s *DialogueEpisodeService) resumeDialogueCheckpoint(ctx context.Context, key string, checkpoint GenerationCheckpoint, sessionID string, result TopicGenerationResult, turnCount int, persisted []DialogueEpisodeArtifact) (DialogueEpisodeArtifact, error) {
	if err := validateDialogueCheckpoint(checkpoint, key, sessionID, result, turnCount); err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	if checkpoint.Stage == "resume_pending" {
		previousRunID := checkpoint.RunID
		if err := reconcileGenerationResume(ctx, s.runIssuer, &checkpoint, s.checkpoints); err != nil {
			// The shared helper changes the ID only after exact successor
			// verification. A changed ID on error means its checkpoint Put failed.
			if checkpoint.RunID != previousRunID {
				finishErr := finishGenerationRun(ctx, s.runIssuer, checkpoint, domaintask.StatusWaiting, "dialogue resume checkpoint reconciliation failed", "retry from saved dialogue generation checkpoint")
				return DialogueEpisodeArtifact{}, errors.Join(err, finishErr)
			}
			return DialogueEpisodeArtifact{}, err
		}
	}
	artifact := cloneDialogueEpisode(*checkpoint.DialogueArtifact)
	run, err := inspectGenerationRun(ctx, s.runIssuer, checkpoint.TaskID, checkpoint.RunID)
	if err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	if run.Status == domaintask.RunStatusWaiting || run.Status == domaintask.RunStatusInterrupted {
		checkpoint.Stage = "resume_pending"
		if err := s.checkpoints.Put(checkpoint); err != nil {
			return DialogueEpisodeArtifact{}, err
		}
		runID, err := resumeIdleChatRun(ctx, s.runIssuer, checkpoint.TaskID)
		if err != nil {
			return DialogueEpisodeArtifact{}, err
		}
		artifact.RunID = runID
		artifact.TaskID = checkpoint.TaskID
		artifact.Revision++
		artifact.UpdatedAt = time.Now().UTC()
		checkpoint.RunID = runID
		if err := s.saveDialogueArtifactCheckpoint(&checkpoint, artifact); err != nil {
			successor := checkpoint
			successor.RunID = runID
			successor.DialogueArtifact = cloneDialogueEpisodePtr(artifact)
			finishErr := finishGenerationRun(ctx, s.runIssuer, successor, domaintask.StatusWaiting, "dialogue resume checkpoint save failed", "retry from saved dialogue generation checkpoint")
			return DialogueEpisodeArtifact{}, errors.Join(err, finishErr)
		}
		run, err = inspectGenerationRun(ctx, s.runIssuer, checkpoint.TaskID, checkpoint.RunID)
		if err != nil {
			return DialogueEpisodeArtifact{}, err
		}
	}
	if run.Status != domaintask.RunStatusRunning {
		if !dialogueArtifactIsDurable(persisted, artifact) {
			return DialogueEpisodeArtifact{}, fmt.Errorf("terminal dialogue Run %s has no durable artifact revision %d", run.RunID, artifact.Revision)
		}
		switch {
		case run.Status == domaintask.RunStatusSucceeded && artifact.ProductionStatus == DialogueProductionReady && artifact.Validation.Valid:
		case run.Status == domaintask.RunStatusFailed && artifact.ProductionStatus == DialogueProductionFailed:
			// The terminal failure path below verifies the exact failed Run and
			// removes its checkpoint after the durable failed artifact is present.
		default:
			return DialogueEpisodeArtifact{}, fmt.Errorf("dialogue generation run %s is not resumable: %s", run.RunID, run.Status)
		}
	}
	return s.continueDialogueCheckpoint(ctx, key, checkpoint, turnCount, persisted)
}

func (s *DialogueEpisodeService) continueDialogueCheckpoint(ctx context.Context, key string, checkpoint GenerationCheckpoint, turnCount int, persisted []DialogueEpisodeArtifact) (DialogueEpisodeArtifact, error) {
	if checkpoint.DialogueArtifact == nil {
		return DialogueEpisodeArtifact{}, errors.New("dialogue checkpoint artifact is missing")
	}
	artifact := cloneDialogueEpisode(*checkpoint.DialogueArtifact)
	if len(artifact.Turns) == 0 && artifact.ProductionStatus == DialogueProductionValidating &&
		(checkpoint.Stage == "seed" || checkpoint.Stage == "artifact") {
		raw, err := s.generator.Generate(ctx, s.generationPrompt(artifact.SessionID, artifact.TopicResult, artifact.ArcPlan, turnCount))
		if err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, fmt.Errorf("CodexExe dialogue generation: %w", err))
		}
		if err := ctx.Err(); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		var generated struct {
			Turns []DialogueEpisodeTurn `json:"turns"`
		}
		if err := decodeStoryJSON(raw, &generated); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, fmt.Errorf("decode CodexExe dialogue: %w", err))
		}
		if err := ctx.Err(); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		artifact.Turns = normalizeDialogueTurns(generated.Turns, 0)
		artifact.Validation = ValidateDialogueEpisode(artifact, s.config)
		setDialogueProductionStatus(&artifact, s.maxSuffixRegenerations)
		if err := s.saveDialogueArtifactCheckpoint(&checkpoint, artifact); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		if err := s.append(artifact); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		persisted = append(persisted, cloneDialogueEpisode(artifact))
	}
	for !artifact.Validation.Valid && artifact.SuffixRegenerations < s.maxSuffixRegenerations {
		if err := ctx.Err(); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		var err error
		artifact, err = s.repairSuffix(ctx, artifact)
		if err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		if err := ctx.Err(); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		if err := s.saveDialogueArtifactCheckpoint(&checkpoint, artifact); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		if err := s.append(artifact); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		persisted = append(persisted, cloneDialogueEpisode(artifact))
	}
	if !artifact.Validation.Valid {
		artifact.ProductionStatus = DialogueProductionFailed
		if err := s.saveDialogueArtifactCheckpoint(&checkpoint, artifact); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		if err := s.append(artifact); err != nil {
			return s.waitDialogueCheckpoint(ctx, checkpoint, err)
		}
		failure := fmt.Errorf("dialogue episode %s failed validation at turn %d", artifact.EpisodeID, artifact.Validation.FirstInvalidTurn)
		if err := finishGenerationRun(ctx, s.runIssuer, checkpoint, domaintask.StatusFailed, "dialogue episode failed validation", failure.Error()); err != nil {
			return DialogueEpisodeArtifact{}, errors.Join(failure, err)
		}
		if err := s.checkpoints.Delete(key); err != nil {
			return DialogueEpisodeArtifact{}, errors.Join(failure, err)
		}
		return DialogueEpisodeArtifact{}, failure
	}
	artifact.ProductionStatus = DialogueProductionReady
	if err := ctx.Err(); err != nil {
		return s.waitDialogueCheckpoint(ctx, checkpoint, err)
	}
	if err := s.saveDialogueArtifactCheckpoint(&checkpoint, artifact); err != nil {
		return s.waitDialogueCheckpoint(ctx, checkpoint, err)
	}
	if err := s.append(artifact); err != nil {
		return s.waitDialogueCheckpoint(ctx, checkpoint, err)
	}
	if err := finishGenerationRun(ctx, s.runIssuer, checkpoint, domaintask.StatusSucceeded, "dialogue episode saved", ""); err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	if err := s.checkpoints.Delete(key); err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	return artifact, nil
}

func (s *DialogueEpisodeService) waitDialogueCheckpoint(ctx context.Context, checkpoint GenerationCheckpoint, cause error) (DialogueEpisodeArtifact, error) {
	if cause == nil {
		cause = errors.New("dialogue generation interrupted")
	}
	finishErr := finishGenerationRun(ctx, s.runIssuer, checkpoint, domaintask.StatusWaiting, "dialogue generation interrupted", "retry from saved dialogue generation checkpoint")
	return DialogueEpisodeArtifact{}, errors.Join(cause, finishErr)
}

func dialogueArtifactIsDurable(persisted []DialogueEpisodeArtifact, candidate DialogueEpisodeArtifact) bool {
	want, err := json.Marshal(candidate)
	if err != nil {
		return false
	}
	for _, existing := range persisted {
		if existing.EpisodeID != candidate.EpisodeID || existing.Revision != candidate.Revision {
			continue
		}
		got, err := json.Marshal(existing)
		if err == nil && bytes.Equal(got, want) {
			return true
		}
	}
	return false
}

func latestDialogueReadyArtifact(persisted []DialogueEpisodeArtifact, key string) (DialogueEpisodeArtifact, bool) {
	var latest DialogueEpisodeArtifact
	found := false
	for _, artifact := range persisted {
		if dialogueCheckpointKey(artifact.SessionID, artifact.TopicResult, len(artifact.ArcPlan.TurnPlans)) != key {
			continue
		}
		latest = artifact
		found = true
	}
	if !found || latest.ProductionStatus != DialogueProductionReady || !latest.Validation.Valid {
		return DialogueEpisodeArtifact{}, false
	}
	return cloneDialogueEpisode(latest), true
}

func (s *DialogueEpisodeService) readPersistedDialogueArtifacts() ([]DialogueEpisodeArtifact, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, errors.New("dialogue episode persistence is not configured")
	}
	payload, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dialogue episode store: %w", err)
	}
	if len(payload) == 0 {
		return nil, nil
	}
	if payload[len(payload)-1] != '\n' {
		return nil, errors.New("dialogue episode store has an unterminated record")
	}
	lines := bytes.Split(payload, []byte{'\n'})
	artifacts := make([]DialogueEpisodeArtifact, 0, len(lines)-1)
	canonical := make(map[string][]byte)
	for lineNo, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if len(line) > 16<<20 {
			return nil, fmt.Errorf("dialogue episode record %d exceeds size limit", lineNo+1)
		}
		var artifact DialogueEpisodeArtifact
		if err := json.Unmarshal(line, &artifact); err != nil {
			return nil, fmt.Errorf("decode dialogue episode record %d: %w", lineNo+1, err)
		}
		if err := validatePersistedDialogueArtifact(artifact); err != nil {
			return nil, fmt.Errorf("dialogue episode record %d: %w", lineNo+1, err)
		}
		encoded, err := json.Marshal(artifact)
		if err != nil {
			return nil, fmt.Errorf("encode dialogue episode record %d: %w", lineNo+1, err)
		}
		identity := artifact.EpisodeID + "#" + fmt.Sprint(artifact.Revision)
		if previous, ok := canonical[identity]; ok && !bytes.Equal(previous, encoded) {
			return nil, fmt.Errorf("conflicting dialogue episode revision %s", identity)
		}
		canonical[identity] = encoded
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func validatePersistedDialogueArtifact(artifact DialogueEpisodeArtifact) error {
	if artifact.SchemaVersion != DialogueEpisodeSchemaVersion {
		return fmt.Errorf("unsupported dialogue artifact schema version %d", artifact.SchemaVersion)
	}
	if strings.TrimSpace(artifact.EpisodeID) == "" || artifact.Revision < 1 {
		return errors.New("dialogue artifact identity is invalid")
	}
	if err := validateIdleChatRunIdentity(artifact.TaskID, artifact.RunID); err != nil {
		return err
	}
	switch artifact.ProductionStatus {
	case DialogueProductionValidating, DialogueProductionReady, DialogueProductionNeedsRepair, DialogueProductionFailed:
	default:
		return fmt.Errorf("invalid dialogue production status %q", artifact.ProductionStatus)
	}
	return nil
}

func (s *DialogueEpisodeService) repairSuffix(ctx context.Context, artifact DialogueEpisodeArtifact) (DialogueEpisodeArtifact, error) {
	from := artifact.Validation.FirstInvalidTurn
	if from < 1 {
		from = 1
	}
	prefixLength := min(from-1, len(artifact.Turns))
	prefix := append([]DialogueEpisodeTurn(nil), artifact.Turns[:prefixLength]...)
	payload, err := json.Marshal(artifact)
	if err != nil {
		return artifact, err
	}
	prompt := fmt.Sprintf(`あなたはRenCrow IdleChatの対話suffix修復担当です。
turn %dより前はCORE検査合格済みで、本文・speaker・message_idを変更禁止です。
turn %d以降だけを最終turnまで再生成し、validation.errorsをすべて解消してください。
MioとShiroのSystemPrompt、content_mode、arc_plan、発話順を維持してください。
JSON以外を付けず、{"turns":[{"speaker":"mio","display_text":"本文","speech_text":"読み上げ本文"}]}だけを返してください。message_idは省略してください。
対象artifact:
%s`, from, from, string(payload))
	raw, err := s.generator.Generate(ctx, prompt)
	if err != nil {
		return artifact, fmt.Errorf("CodexExe dialogue suffix repair: %w", err)
	}
	var generated struct {
		Turns []DialogueEpisodeTurn `json:"turns"`
	}
	if err := decodeStoryJSON(raw, &generated); err != nil {
		return artifact, fmt.Errorf("decode CodexExe dialogue suffix: %w", err)
	}
	if len(generated.Turns) == 0 {
		return artifact, errors.New("CodexExe dialogue suffix repair returned no turns")
	}
	artifact.Turns = append(prefix, normalizeDialogueTurns(generated.Turns, prefixLength)...)
	artifact.Revision++
	artifact.FixedPrefixLength = prefixLength
	artifact.RepairFromTurn = from
	artifact.SuffixRegenerations++
	artifact.UpdatedAt = time.Now().UTC()
	artifact.Validation = ValidateDialogueEpisode(artifact, s.config)
	setDialogueProductionStatus(&artifact, s.maxSuffixRegenerations)
	return artifact, nil
}

func normalizeDialogueTurns(turns []DialogueEpisodeTurn, prefixLength int) []DialogueEpisodeTurn {
	out := make([]DialogueEpisodeTurn, len(turns))
	for i, turn := range turns {
		turn.TurnIndex = prefixLength + i + 1
		turn.MessageID = newIdleChatMessageID()
		turn.Speaker = strings.ToLower(strings.TrimSpace(turn.Speaker))
		turn.DisplayText = ensureTrailingPeriod(strings.TrimSpace(turn.DisplayText))
		turn.SpeechText = ensureTrailingPeriod(strings.TrimSpace(turn.SpeechText))
		if turn.SpeechText == "" {
			turn.SpeechText = turn.DisplayText
		}
		out[i] = turn
	}
	return out
}

func setDialogueProductionStatus(artifact *DialogueEpisodeArtifact, maxRepairs int) {
	if artifact.Validation.Valid {
		artifact.ProductionStatus = DialogueProductionReady
	} else if artifact.SuffixRegenerations >= maxRepairs {
		artifact.ProductionStatus = DialogueProductionFailed
	} else {
		artifact.ProductionStatus = DialogueProductionNeedsRepair
	}
}

func ValidateDialogueEpisode(artifact DialogueEpisodeArtifact, config DialogueInterestingnessConfig) DialogueEpisodeValidation {
	validation := DialogueEpisodeValidation{Valid: true}
	wantTurns := len(artifact.ArcPlan.TurnPlans)
	if len(artifact.Turns) != wantTurns {
		turn := min(len(artifact.Turns)+1, wantTurns)
		if len(artifact.Turns) > wantTurns {
			turn = max(wantTurns, 1)
		}
		validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "turn_count_violation", TurnIndex: turn, Evidence: fmt.Sprintf("turns=%d want=%d", len(artifact.Turns), wantTurns)})
	}
	checker := NewDialogueQualityChecker(config)
	director := NewDialogueDirector(config)
	state := director.NewArcState(artifact.SessionID, artifact.TopicResult, artifact.ArcPlan)
	lastBySpeaker := map[string]string{}
	transcript := make([]string, 0, len(artifact.Turns))
	if len(artifact.Participants) == 0 {
		validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "participant_contract_violation", TurnIndex: 1, Evidence: "participants are required"})
		validation.Valid = false
		validation.FirstInvalidTurn = 1
		return validation
	}
	for i, turn := range artifact.Turns {
		turnIndex := i + 1
		if turnIndex > wantTurns {
			break
		}
		expectedSpeaker := artifact.Participants[i%len(artifact.Participants)]
		if turn.TurnIndex != turnIndex || strings.ToLower(strings.TrimSpace(turn.Speaker)) != expectedSpeaker {
			validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "speaker_sequence_violation", TurnIndex: turnIndex, Evidence: fmt.Sprintf("speaker=%q want=%q", turn.Speaker, expectedSpeaker)})
			continue
		}
		text := strings.TrimSpace(turn.DisplayText)
		if text == "" || strings.TrimSpace(turn.SpeechText) == "" {
			validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "empty_utterance", TurnIndex: turnIndex, Evidence: "display_text and speech_text are required"})
			continue
		}
		latestOther := ""
		if i > 0 {
			latestOther = artifact.Turns[i-1].DisplayText
		}
		quality := checker.Check(DialogueQualityInput{
			Category: artifact.TopicResult.Category, ContentMode: artifact.ArcPlan.ContentMode,
			Utterance: text, LatestOther: latestOther, LatestSelf: lastBySpeaker[expectedSpeaker],
			State: state, TurnPlan: artifact.ArcPlan.TurnPlans[i], Config: config,
		})
		if !quality.OK {
			validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "turn_quality_violation", TurnIndex: turnIndex, Evidence: fmt.Sprintf("score=%d", quality.Score), Reasons: quality.Reasons})
		}
		if isResponseTooSimilar(text, transcript) {
			validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "repetition_violation", TurnIndex: turnIndex, Evidence: "utterance is too similar to an earlier turn"})
		}
		state = director.UpdateArcState(state, text, artifact.ArcPlan.TurnPlans[i], quality)
		lastBySpeaker[expectedSpeaker] = text
		transcript = append(transcript, text)
	}
	for _, item := range validation.Errors {
		if item.TurnIndex > 0 && (validation.FirstInvalidTurn == 0 || item.TurnIndex < validation.FirstInvalidTurn) {
			validation.FirstInvalidTurn = item.TurnIndex
		}
	}
	validation.Valid = len(validation.Errors) == 0
	return validation
}

func (s *DialogueEpisodeService) generationPrompt(sessionID string, result TopicGenerationResult, plan DialogueArcPlan, turnCount int) string {
	resultJSON, _ := json.Marshal(result)
	planJSON, _ := json.Marshal(plan)
	return fmt.Sprintf(`あなたはRenCrow IdleChatの完成対話台本を生成します。
MioとShiroはCORE Agentであり、CodexExeやモデル名は発話者ではありません。全%d turnを一度に生成してください。

Mio SystemPrompt:
%s

Shiro SystemPrompt:
%s

session_id=%s
topic_result=%s
arc_plan=%s
content_mode指示=%s

条件:
- speakerはmioから始め、mio/shiroを交互にする。
- 各turnは直前の相手発話を受け、新しい具体的貢献を一つ加える。
- 話者名、プロンプト、JSON、候補、生成過程、ユーザーへの質問を本文へ出さない。
- display_textはViewer用、speech_textはTTS用。通常は同文でよい。
- Forecastの時間範囲はtopic_result.seed.forecast_horizonを厳守する。Forecast以外へ未来範囲を持ち込まない。
- 出力前に全turnを順番に自己点検する。

JSON以外を付けず、{"turns":[{"speaker":"mio","display_text":"本文","speech_text":"読み上げ本文"}]}だけを返してください。turn_indexとmessage_idはCOREが確定します。`,
		turnCount, s.personas["mio"], s.personas["shiro"], strings.TrimSpace(sessionID), string(resultJSON), string(planJSON), dialogueContentPolicyInstruction(DialogueContentPolicy{Mode: plan.ContentMode, Reasons: plan.ContentModeReasons}))
}

func (s *DialogueEpisodeService) append(artifact DialogueEpisodeArtifact) error {
	if s.path == "" {
		return errors.New("dialogue episode persistence is not configured")
	}
	if err := validatePersistedDialogueArtifact(artifact); err != nil {
		return fmt.Errorf("dialogue episode is not appendable: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create dialogue episode directory: %w", err)
	}
	persisted, err := s.readPersistedDialogueArtifacts()
	if err != nil {
		return err
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	for _, existing := range persisted {
		if existing.EpisodeID != artifact.EpisodeID || existing.Revision != artifact.Revision {
			continue
		}
		existingData, marshalErr := json.Marshal(existing)
		if marshalErr != nil {
			return marshalErr
		}
		if bytes.Equal(existingData, data) {
			return nil
		}
		return fmt.Errorf("conflicting dialogue episode revision %s#%d", artifact.EpisodeID, artifact.Revision)
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open dialogue episode store: %w", err)
	}
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("append dialogue episode: %w", err)
	}
	return nil
}
