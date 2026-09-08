package idlechat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type dialogueRecoveryOwner struct {
	*taskmanager.Manager
	failCompleteOnce error
	startCalls       int
	afterStart       func(domaintask.RunStartReason, domaintask.Run)
	afterComplete    func()
}

func (o *dialogueRecoveryOwner) StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error) {
	o.startCalls++
	run, err := o.Manager.StartRunWithReason(ctx, taskID, reason)
	if err == nil && o.afterStart != nil {
		o.afterStart(reason, run)
	}
	return run, err
}

func (o *dialogueRecoveryOwner) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error) {
	if o.failCompleteOnce != nil {
		err := o.failCompleteOnce
		o.failCompleteOnce = nil
		return domaintask.Task{}, err
	}
	task, err := o.Manager.CompleteRun(ctx, taskID, runID, actorID, status, summary, waitingReason)
	if err == nil && o.afterComplete != nil {
		o.afterComplete()
	}
	return task, err
}

type dialogueRecoveryGenerator struct {
	responses  []string
	requests   []string
	calls      int
	onGenerate func()
	err        error
}

func (g *dialogueRecoveryGenerator) Generate(_ context.Context, prompt string) (string, error) {
	g.calls++
	g.requests = append(g.requests, prompt)
	if g.onGenerate != nil {
		g.onGenerate()
	}
	if g.err != nil {
		return "", g.err
	}
	if len(g.responses) == 0 {
		return "", errors.New("dialogue recovery generator has no response")
	}
	response := g.responses[0]
	g.responses = g.responses[1:]
	return response, nil
}

func dialogueRecoveryResponse() string {
	return `{"turns":[{"speaker":"mio","display_text":"防災設備の赤いランプを店頭で確認する場面から、判断の難しさが見えるね。","speech_text":"防災設備の赤いランプを店頭で確認する場面から、判断の難しさが見えるね。"},{"speaker":"shiro","display_text":"その赤いランプを誰が確認したか記録すれば、店での判断の責任を分けられます。","speech_text":"その赤いランプを誰が確認したか記録すれば、店での判断の責任を分けられます。"}]}`
}

func dialogueRecoveryInput() TopicGenerationResult {
	return TopicGenerationResult{
		Topic: "防災設備を店頭に入れるとき誰が最後の判断を持つか", Category: TopicCategorySingle,
		Strategy: string(StrategySingleGenre), InterestingnessAxis: "観察", Initiator: "shiro",
		Seed: TopicSeed{Category: TopicCategorySingle, Genre1: "防災"},
	}
}

func newDialogueRecoveryOwner(t *testing.T) *dialogueRecoveryOwner {
	t.Helper()
	return &dialogueRecoveryOwner{Manager: newTestIdleChatRunIssuer(t)}
}

func seedWaitingDialogueCheckpoint(t *testing.T, owner *dialogueRecoveryOwner, path string, config DialogueInterestingnessConfig) (*GenerationCheckpointStore, domaintask.Task, domaintask.Run) {
	t.Helper()
	ctx := context.Background()
	task, err := owner.Create(ctx, domaintask.Task{Title: "IdleChat dialogue recovery", Route: domaintask.RouteGeneral, Assignee: "Shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := owner.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := owner.CompleteRun(ctx, task.TaskID, run.RunID, "Shiro", domaintask.StatusWaiting, "dialogue generation paused", "retry from saved dialogue generation checkpoint"); err != nil {
		t.Fatalf("wait run: %v", err)
	}
	result := dialogueRecoveryInput()
	plan := NewDialogueDirector(config).BuildArcPlan(result)
	plan.TurnPlans = buildDialogueTurnPlans(2, dialogueCategorySpec(plan.Category))
	result.Category = plan.Category
	artifact := DialogueEpisodeArtifact{
		SchemaVersion: DialogueEpisodeSchemaVersion, EpisodeID: "dialogue-recovery-seed", TaskID: task.TaskID, RunID: run.RunID,
		Revision: 1, SessionID: "dialogue-recovery-session", InitiatedBy: "shiro", TopicResult: result,
		ArcPlan: plan, Participants: []string{"mio", "shiro"}, Turns: []DialogueEpisodeTurn{
			{TurnIndex: 1, MessageID: "message-recovery-1", Speaker: "mio", DisplayText: "防災設備の赤いランプを店頭で確認する場面から、判断の難しさが見えるね。", SpeechText: "防災設備の赤いランプを店頭で確認する場面から、判断の難しさが見えるね。"},
			{TurnIndex: 2, MessageID: "message-recovery-2", Speaker: "shiro", DisplayText: "その赤いランプを誰が確認したか記録すれば、店での判断の責任を分けられます。", SpeechText: "その赤いランプを誰が確認したか記録すれば、店での判断の責任を分けられます。"},
		},
		ProductionStatus: DialogueProductionReady, CreatedAt: nowForDialogueRecovery(), UpdatedAt: nowForDialogueRecovery(),
	}
	artifact.Validation = ValidateDialogueEpisode(artifact, config)
	if !artifact.Validation.Valid {
		t.Fatalf("seed artifact is invalid: %+v", artifact.Validation)
	}
	checkpoints := NewGenerationCheckpointStore(path + ".checkpoints.json")
	checkpoint := GenerationCheckpoint{
		Key: dialogueCheckpointKey(artifact.SessionID, artifact.TopicResult, len(artifact.ArcPlan.TurnPlans)), Kind: "dialogue",
		TaskID: task.TaskID, RunID: run.RunID, Stage: "ready", DialogueArtifact: cloneDialogueEpisodePtr(artifact),
	}
	if err := checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("save dialogue checkpoint: %v", err)
	}
	return checkpoints, task, run
}

func nowForDialogueRecovery() (nowTime time.Time) {
	return time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
}

func dialogueRecoveryRuns(t *testing.T, owner *dialogueRecoveryOwner, taskID modulecore.TaskID) []domaintask.Run {
	t.Helper()
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	return runs
}

func TestDialogueEpisodeResumesSavedResultWithoutGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	owner := newDialogueRecoveryOwner(t)
	checkpoints, task, oldRun := seedWaitingDialogueCheckpoint(t, owner, path, config)
	generator := &dialogueRecoveryGenerator{err: errors.New("saved dialogue must not call CodexExe")}
	service := NewPersistentDialogueEpisodeService(path, generator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	service.SetGenerationCheckpointStore(checkpoints)
	service.SetRunIssuer(owner)

	artifact, err := service.Prepare(context.Background(), "dialogue-recovery-session", dialogueRecoveryInput(), 2)
	if err != nil {
		t.Fatalf("resume saved dialogue: %v", err)
	}
	if generator.calls != 0 || artifact.RunID == oldRun.RunID || artifact.Revision != 2 {
		t.Fatalf("resume result calls=%d artifact=%+v", generator.calls, artifact)
	}
	if _, ok := checkpoints.Get(dialogueCheckpointKey(artifact.SessionID, artifact.TopicResult, 2)); ok {
		t.Fatal("completed dialogue checkpoint remained")
	}
	runs := dialogueRecoveryRuns(t, owner, task.TaskID)
	if len(runs) != 2 || runs[0].Status != domaintask.RunStatusWaiting || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("run history = %+v", runs)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 1 {
		t.Fatalf("durable dialogue revisions = %d, want 1", lines)
	}
}

func TestDialogueEpisodeRetriesSeedWithFullGenerationAfterGeneratorFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	owner := newDialogueRecoveryOwner(t)
	sessionID := "dialogue-seed-retry"
	result := dialogueRecoveryInput()
	firstGenerator := &dialogueRecoveryGenerator{err: errors.New("injected first generation failure")}
	service := NewPersistentDialogueEpisodeService(path, firstGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	service.SetRunIssuer(owner)
	if artifact, err := service.Prepare(context.Background(), sessionID, result, 2); err == nil || artifact.EpisodeID != "" {
		t.Fatalf("first generation result artifact=%+v err=%v", artifact, err)
	}
	checkpointPath := path + ".checkpoints.json"
	reloaded := NewGenerationCheckpointStore(checkpointPath)
	key := dialogueCheckpointKey(sessionID, result, 2)
	checkpoint, ok := reloaded.Get(key)
	if !ok || checkpoint.Stage != "seed" || checkpoint.DialogueArtifact == nil || len(checkpoint.DialogueArtifact.Turns) != 0 {
		t.Fatalf("seed checkpoint after generator failure = %+v ok=%t", checkpoint, ok)
	}
	if checkpoint.DialogueArtifact.ProductionStatus != DialogueProductionValidating {
		t.Fatalf("seed artifact status = %q", checkpoint.DialogueArtifact.ProductionStatus)
	}
	if runs := dialogueRecoveryRuns(t, owner, checkpoint.TaskID); len(runs) != 1 || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("run after first generation failure = %+v", runs)
	}

	retryGenerator := &dialogueRecoveryGenerator{responses: []string{dialogueRecoveryResponse()}}
	retry := NewPersistentDialogueEpisodeService(path, retryGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	retry.SetGenerationCheckpointStore(reloaded)
	retry.SetRunIssuer(owner)
	artifact, err := retry.Prepare(context.Background(), sessionID, result, 2)
	if err != nil || artifact.EpisodeID == "" {
		t.Fatalf("seed retry artifact=%+v err=%v", artifact, err)
	}
	if retryGenerator.calls != 1 || len(retryGenerator.requests) != 1 || strings.Contains(retryGenerator.requests[0], "suffix修復担当") || !strings.Contains(retryGenerator.requests[0], "完成対話台本") {
		t.Fatalf("retry generation calls=%d requests=%q", retryGenerator.calls, retryGenerator.requests)
	}
	if owner.startCalls != 2 {
		t.Fatalf("run starts after seed retry = %d, want 2", owner.startCalls)
	}
	if _, ok := reloaded.Get(key); ok {
		t.Fatal("seed retry checkpoint remained")
	}
	runs := dialogueRecoveryRuns(t, owner, checkpoint.TaskID)
	if len(runs) != 2 || runs[0].Status != domaintask.RunStatusWaiting || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("run history after seed retry = %+v", runs)
	}
}

func TestDialogueEpisodeRetriesCompletionWithoutStartingAnotherRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	owner := newDialogueRecoveryOwner(t)
	owner.failCompleteOnce = errors.New("injected CompleteRun failure")
	generator := &dialogueRecoveryGenerator{responses: []string{dialogueRecoveryResponse()}}
	service := NewPersistentDialogueEpisodeService(path, generator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	service.SetRunIssuer(owner)
	if artifact, err := service.Prepare(context.Background(), "dialogue-completion-retry", dialogueRecoveryInput(), 2); err == nil || artifact.EpisodeID != "" {
		t.Fatalf("first completion result artifact=%+v err=%v", artifact, err)
	}
	checkpointStore := NewGenerationCheckpointStore(path + ".checkpoints.json")
	checkpoint, ok := checkpointStore.Get(dialogueCheckpointKey("dialogue-completion-retry", dialogueRecoveryInput(), 2))
	if !ok || checkpoint.DialogueArtifact == nil || checkpoint.Stage != "ready" {
		t.Fatalf("checkpoint after completion failure = %+v ok=%t", checkpoint, ok)
	}
	if runs := dialogueRecoveryRuns(t, owner, checkpoint.TaskID); len(runs) != 1 || runs[0].Status != domaintask.RunStatusRunning {
		t.Fatalf("run after completion failure = %+v", runs)
	}
	retryGenerator := &dialogueRecoveryGenerator{err: errors.New("retry must reuse saved dialogue")}
	retry := NewPersistentDialogueEpisodeService(path, retryGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	retry.SetGenerationCheckpointStore(checkpointStore)
	retry.SetRunIssuer(owner)
	artifact, err := retry.Prepare(context.Background(), "dialogue-completion-retry", dialogueRecoveryInput(), 2)
	if err != nil || artifact.EpisodeID == "" {
		t.Fatalf("completion retry artifact=%+v err=%v", artifact, err)
	}
	if retryGenerator.calls != 0 || owner.startCalls != 1 {
		t.Fatalf("retry generation=%d starts=%d", retryGenerator.calls, owner.startCalls)
	}
	if _, ok := checkpointStore.Get(checkpoint.Key); ok {
		t.Fatal("checkpoint remained after completion retry")
	}
	runs := dialogueRecoveryRuns(t, owner, checkpoint.TaskID)
	if len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("run history after retry = %+v", runs)
	}
}

func TestDialogueEpisodeCleanupFailureReloadsSavedResultWithoutGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	owner := newDialogueRecoveryOwner(t)
	generator := &dialogueRecoveryGenerator{responses: []string{dialogueRecoveryResponse()}}
	service := NewPersistentDialogueEpisodeService(path, generator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	service.SetRunIssuer(owner)
	checkpointStore := service.checkpoints
	badPath := filepath.Join(t.TempDir(), "checkpoint-directory")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatal(err)
	}
	owner.afterComplete = func() {
		checkpointStore.path = badPath
		owner.afterComplete = nil
	}
	sessionID := "dialogue-cleanup-retry"
	result := dialogueRecoveryInput()
	artifact, err := service.Prepare(context.Background(), sessionID, result, 2)
	if err == nil || artifact.EpisodeID != "" {
		t.Fatalf("cleanup failure artifact=%+v err=%v", artifact, err)
	}
	key := dialogueCheckpointKey(sessionID, result, 2)
	reloaded := NewGenerationCheckpointStore(path + ".checkpoints.json")
	checkpoint, ok := reloaded.Get(key)
	if !ok || checkpoint.Stage != "ready" || checkpoint.DialogueArtifact == nil {
		t.Fatalf("checkpoint after cleanup failure = %+v ok=%t", checkpoint, ok)
	}
	if runs := dialogueRecoveryRuns(t, owner, checkpoint.TaskID); len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("run after cleanup failure = %+v", runs)
	}

	retryGenerator := &dialogueRecoveryGenerator{err: errors.New("cleanup retry must not call CodexExe")}
	retry := NewPersistentDialogueEpisodeService(path, retryGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	retry.SetGenerationCheckpointStore(reloaded)
	retry.SetRunIssuer(owner)
	artifact, err = retry.Prepare(context.Background(), sessionID, result, 2)
	if err != nil || artifact.EpisodeID == "" {
		t.Fatalf("cleanup retry artifact=%+v err=%v", artifact, err)
	}
	if retryGenerator.calls != 0 || owner.startCalls != 1 {
		t.Fatalf("cleanup retry generation=%d starts=%d", retryGenerator.calls, owner.startCalls)
	}
	if _, ok := reloaded.Get(key); ok {
		t.Fatal("checkpoint remained after cleanup retry")
	}
	runs := dialogueRecoveryRuns(t, owner, checkpoint.TaskID)
	if len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("run history after cleanup retry = %+v", runs)
	}
}

func TestDialogueEpisodeReconcilesSuccessorAfterCheckpointPutFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	owner := newDialogueRecoveryOwner(t)
	checkpoints, task, oldRun := seedWaitingDialogueCheckpoint(t, owner, path, config)
	key := dialogueCheckpointKey("dialogue-recovery-session", dialogueRecoveryInput(), 2)
	checkpoint, ok := checkpoints.Get(key)
	if !ok {
		t.Fatal("seed checkpoint missing")
	}
	checkpoint.Stage = "resume_pending"
	if err := checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("save pending checkpoint: %v", err)
	}
	successor, err := owner.Manager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonCheckpointResume)
	if err != nil {
		t.Fatalf("issue successor: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "checkpoint-directory")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatal(err)
	}
	failedStore := NewGenerationCheckpointStore(path + ".checkpoints.json")
	failedStore.path = badPath
	first := NewPersistentDialogueEpisodeService(path, &dialogueRecoveryGenerator{err: errors.New("reconcile failure must not generate")}, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	first.SetGenerationCheckpointStore(failedStore)
	first.SetRunIssuer(owner)
	if artifact, err := first.Prepare(context.Background(), "dialogue-recovery-session", dialogueRecoveryInput(), 2); err == nil || artifact.EpisodeID != "" {
		t.Fatalf("reconcile Put failure artifact=%+v err=%v", artifact, err)
	}
	if runs := dialogueRecoveryRuns(t, owner, task.TaskID); len(runs) != 2 || runs[0].RunID != oldRun.RunID || runs[0].Status != domaintask.RunStatusWaiting || runs[1].RunID != successor.RunID || runs[1].Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs after reconcile Put failure = %+v", runs)
	}

	reloaded := NewGenerationCheckpointStore(path + ".checkpoints.json")
	retryGenerator := &dialogueRecoveryGenerator{err: errors.New("reconcile retry must reuse saved artifact")}
	retry := NewPersistentDialogueEpisodeService(path, retryGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	retry.SetGenerationCheckpointStore(reloaded)
	retry.SetRunIssuer(owner)
	artifact, err := retry.Prepare(context.Background(), "dialogue-recovery-session", dialogueRecoveryInput(), 2)
	if err != nil || artifact.EpisodeID == "" {
		t.Fatalf("reconcile retry artifact=%+v err=%v", artifact, err)
	}
	if retryGenerator.calls != 0 || owner.startCalls != 2 {
		t.Fatalf("reconcile retry generation=%d starts=%d", retryGenerator.calls, owner.startCalls)
	}
	if _, ok := reloaded.Get(key); ok {
		t.Fatal("checkpoint remained after reconcile retry")
	}
	runs := dialogueRecoveryRuns(t, owner, task.TaskID)
	if len(runs) != 3 || runs[0].Status != domaintask.RunStatusWaiting || runs[1].Status != domaintask.RunStatusWaiting || runs[2].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("run history after reconcile retry = %+v", runs)
	}
	for i, run := range runs[1:] {
		if run.StartReason != domaintask.RunStartReasonCheckpointResume {
			t.Fatalf("successor %d start reason = %q", i, run.StartReason)
		}
	}
}

func TestDialogueEpisodePersistenceFailureDoesNotReportSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	owner := newDialogueRecoveryOwner(t)
	var service *DialogueEpisodeService
	badPath := filepath.Join(t.TempDir(), "dialogue-store-directory")
	generator := &dialogueRecoveryGenerator{
		responses: []string{dialogueRecoveryResponse()},
		onGenerate: func() {
			if err := os.Mkdir(badPath, 0o755); err != nil {
				t.Fatalf("create bad dialogue path: %v", err)
			}
			service.path = badPath
		},
	}
	service = NewPersistentDialogueEpisodeService(path, generator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	service.SetRunIssuer(owner)
	artifact, err := service.Prepare(context.Background(), "dialogue-persistence-failure", dialogueRecoveryInput(), 2)
	if err == nil || artifact.EpisodeID != "" {
		t.Fatalf("persistence failure artifact=%+v err=%v", artifact, err)
	}
	checkpointStore := NewGenerationCheckpointStore(path + ".checkpoints.json")
	checkpoint, ok := checkpointStore.Get(dialogueCheckpointKey("dialogue-persistence-failure", dialogueRecoveryInput(), 2))
	if !ok || checkpoint.DialogueArtifact == nil || checkpoint.Stage != "ready" {
		t.Fatalf("checkpoint after persistence failure = %+v ok=%t", checkpoint, ok)
	}
	runs := dialogueRecoveryRuns(t, owner, checkpoint.TaskID)
	if len(runs) != 1 || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("run after persistence failure = %+v", runs)
	}
	if task, err := owner.Get(context.Background(), checkpoint.TaskID); err != nil || task.Status != domaintask.StatusWaiting {
		t.Fatalf("task after persistence failure = %+v err=%v", task, err)
	}
}
