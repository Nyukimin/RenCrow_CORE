package idlechat

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type delayedGenerationWorkGenerator struct {
	started     chan struct{}
	canceled    chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	cancelOnce  sync.Once
	releaseOnce sync.Once
}

func newDelayedGenerationWorkGenerator() *delayedGenerationWorkGenerator {
	return &delayedGenerationWorkGenerator{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (g *delayedGenerationWorkGenerator) Generate(ctx context.Context, _ string) (string, error) {
	g.startOnce.Do(func() { close(g.started) })
	select {
	case <-ctx.Done():
		g.cancelOnce.Do(func() { close(g.canceled) })
		<-g.release
		return "", ctx.Err()
	case <-g.release:
		return "", errors.New("delayed test generation released")
	}
}

func (g *delayedGenerationWorkGenerator) releaseWork() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func newGenerationWorkWordFixture(t *testing.T, generator IdleChatCodexGenerator) (*IdleChatOrchestrator, interface {
	List(context.Context, domaintask.Filter) ([]domaintask.Task, error)
	ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error)
}) {
	t.Helper()
	owner := newTestIdleChatRunIssuer(t)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 60, 1, 0.7, nil, "")
	o.SetRunIssuer(owner)
	o.SetTopicCodexGenerator(generator)
	o.SetGenerationCheckpointStore(NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "generation_checkpoints.json")))
	o.mu.Lock()
	o.wordTopicStock = newWordTopicStock(filepath.Join(t.TempDir(), "word_topic_stock.json"))
	o.mu.Unlock()
	return o, owner
}

func waitForGenerationWorkSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestStopJoinsAdmittedWordTopicGenerationBeforeTaskOwnerClose(t *testing.T) {
	generator := newDelayedGenerationWorkGenerator()
	o, owner := newGenerationWorkWordFixture(t, generator)
	t.Cleanup(generator.releaseWork)

	if !o.RefillWordTopicStockIfIdle("generation-work-test") {
		t.Fatal("word topic refill was not admitted")
	}
	waitForGenerationWorkSignal(t, generator.started, "word generation start")

	stopDone := make(chan struct{})
	go func() {
		o.Stop()
		close(stopDone)
	}()
	waitForGenerationWorkSignal(t, generator.canceled, "word generation cancellation")
	select {
	case <-stopDone:
		t.Fatal("Stop returned while admitted word generation was still blocked")
	default:
	}
	generator.releaseWork()
	waitForGenerationWorkSignal(t, stopDone, "Stop completion")

	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want one real word task", len(tasks))
	}
	if tasks[0].Status == domaintask.StatusRunning {
		t.Fatalf("word task remained running after Stop: %#v", tasks[0])
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: tasks[0].TaskID})
	if err != nil {
		t.Fatalf("list word task runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status == domaintask.RunStatusRunning {
		t.Fatalf("word task runs after Stop = %#v, want one non-running run", runs)
	}
	if o.WordTopicStockSnapshot().Filling || o.topicProductionBusy() {
		t.Fatal("word topic production remained reserved after Stop")
	}
}

func TestStopJoinsAdmittedDialogueGenerationBeforeTaskOwnerClose(t *testing.T) {
	generator := newDelayedGenerationWorkGenerator()
	owner := newTestIdleChatRunIssuer(t)
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 1
	dialogueService := NewPersistentDialogueEpisodeService(
		filepath.Join(t.TempDir(), "dialogue_episodes.jsonl"), generator,
		map[string]string{"mio": "Mio canonical", "shiro": "Shiro canonical"}, config,
	)
	dialogueService.SetRunIssuer(owner)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 60, 1, 0.7, nil, "")
	o.SetRunIssuer(owner)
	o.SetDialogueInterestingnessConfig(config)
	o.SetDialogueEpisodeService(dialogueService)
	o.mu.Lock()
	o.chatActive = true
	o.sessionMode = "idle"
	o.mu.Unlock()
	t.Cleanup(generator.releaseWork)

	prepared := TopicGenerationResult{
		Topic:               "防災設備を店頭に入れるとき誰が最後の判断を持つか",
		Category:            TopicCategorySingle,
		Strategy:            string(StrategySingleGenre),
		InterestingnessAxis: "観察",
		Seed:                TopicSeed{Category: TopicCategorySingle, Genre1: "防災"},
		Initiator:           "shiro",
	}
	if !o.startGenerationWork(func() {
		o.runChatSession(StrategySingleGenre, prepared)
	}) {
		t.Fatal("dialogue generation was not admitted")
	}
	waitForGenerationWorkSignal(t, generator.started, "dialogue generation start")

	stopDone := make(chan struct{})
	go func() {
		o.Stop()
		close(stopDone)
	}()
	waitForGenerationWorkSignal(t, generator.canceled, "dialogue generation cancellation")
	select {
	case <-stopDone:
		t.Fatal("Stop returned while admitted dialogue generation was still blocked")
	default:
	}
	generator.releaseWork()
	waitForGenerationWorkSignal(t, stopDone, "Stop completion")

	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("list dialogue tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want conversation and dialogue tasks", len(tasks))
	}
	for _, task := range tasks {
		if task.Status == domaintask.StatusRunning {
			t.Fatalf("task remained running after Stop: %#v", task)
		}
		runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
		if err != nil {
			t.Fatalf("list runs for %s: %v", task.TaskID, err)
		}
		if len(runs) != 1 {
			t.Fatalf("runs for %s = %d, want one", task.TaskID, len(runs))
		}
		if runs[0].Status == domaintask.RunStatusRunning {
			t.Fatalf("run remained running after Stop: %#v", runs[0])
		}
	}
}

func TestStopClosesGenerationAdmissionAndJoinsBeforeReturning(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 60, 1, 0.7, nil, "")
	started := make(chan struct{})
	release := make(chan struct{})
	if !o.startGenerationWork(func() {
		close(started)
		<-release
	}) {
		t.Fatal("generation work was not admitted")
	}
	waitForGenerationWorkSignal(t, started, "admitted generation work")

	stopDone := make(chan struct{})
	go func() {
		o.Stop()
		close(stopDone)
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		o.generationWorkMu.Lock()
		closed := o.generationWorkClosed
		o.generationWorkMu.Unlock()
		if closed {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("Stop did not close generation admission")
		case <-time.After(time.Millisecond):
		}
	}

	var postStopRuns atomic.Int32
	var attempts sync.WaitGroup
	for i := 0; i < 16; i++ {
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			if o.startGenerationWork(func() { postStopRuns.Add(1) }) {
				t.Errorf("generation work admitted after Stop closed the gate")
			}
		}()
	}
	attempts.Wait()
	if got := postStopRuns.Load(); got != 0 {
		t.Fatalf("post-stop generation callbacks = %d, want 0", got)
	}
	select {
	case <-stopDone:
		t.Fatal("Stop returned before admitted generation work released")
	default:
	}
	close(release)
	waitForGenerationWorkSignal(t, stopDone, "Stop completion")
}

func TestStoppedWordRefillReleasesReservationWithoutCreatingTask(t *testing.T) {
	o, owner := newGenerationWorkWordFixture(t, nil)
	o.Stop()

	if o.RefillWordTopicStockIfIdle("after-stop") {
		t.Fatal("stopped word refill was admitted")
	}
	if snapshot := o.WordTopicStockSnapshot(); snapshot.Filling {
		t.Fatalf("stopped word refill left stock filling: %#v", snapshot)
	}
	if o.topicProductionBusy() {
		t.Fatal("stopped word refill left topic production busy")
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("list post-stop tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("post-stop refill created tasks: %#v", tasks)
	}
}

func TestStoppedTopicPlaybackPreservesSelectedStockAndHistory(t *testing.T) {
	stock := newWordTopicStock("")
	taskID, runID := testIdleChatRunIdentityPair()
	added, err := stock.push(WordPreparedTopic{
		Category: TopicCategorySingle,
		Topic:    "駅前の店で防災設備を選ぶ最後の判断者は誰か",
		Seed:     TopicSeed{Category: TopicCategorySingle, Genre1: "防災"},
		Axis:     "観察",
		TaskID:   taskID,
		RunID:    runID,
	})
	if err != nil {
		t.Fatalf("push playback topic: %v", err)
	}
	if !added {
		t.Fatal("playback topic was not added")
	}
	owner := newTestIdleChatRunIssuer(t)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 60, 1, 0.7, nil, "")
	o.SetRunIssuer(owner)
	o.mu.Lock()
	o.wordTopicStock = stock
	o.mu.Unlock()
	before := o.TopicStockPlaybackSnapshot()
	o.Stop()

	after, err := o.StartTopicStockPlayback(TopicStockPlaybackPlay, wordPlaybackID(runID))
	if !errors.Is(err, errIdleChatGenerationWorkClosed) {
		t.Fatalf("stopped playback error = %v, want admission closed", err)
	}
	if got := stock.count(TopicCategorySingle); got != 1 {
		t.Fatalf("stopped playback consumed stock: count=%d", got)
	}
	if after.HistorySize != before.HistorySize || after.Current != nil {
		t.Fatalf("stopped playback changed history: before=%+v after=%+v", before, after)
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("list stopped playback tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("stopped playback created tasks: %#v", tasks)
	}
}
