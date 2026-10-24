package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type storyDemandGeneratorStep struct {
	response   string
	err        error
	waitCancel bool
}

type storyDemandGenerator struct {
	mu      sync.Mutex
	steps   []storyDemandGeneratorStep
	calls   int
	started chan int
}

func (g *storyDemandGenerator) Generate(ctx context.Context, _ string) (string, error) {
	g.mu.Lock()
	call := g.calls + 1
	g.calls++
	var step storyDemandGeneratorStep
	if len(g.steps) > 0 {
		step = g.steps[0]
		g.steps = g.steps[1:]
	}
	started := g.started
	g.mu.Unlock()
	if started != nil {
		select {
		case started <- call:
		default:
		}
	}
	if step.waitCancel {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if step.err != nil {
		return "", step.err
	}
	return step.response, nil
}

func (g *storyDemandGenerator) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func successfulStoryDemandSteps(startAttempt, count int) []storyDemandGeneratorStep {
	steps := make([]storyDemandGeneratorStep, 0, count*2)
	for i := 0; i < count; i++ {
		artifact := validStoryEpisodeFixture()
		if (startAttempt+i)%2 == 1 {
			artifact.Reader, artifact.Listener = "shiro", "mio"
			for turn := range artifact.Turns {
				if artifact.Turns[turn].UtteranceRole == StoryUtteranceNarration {
					artifact.Turns[turn].Speaker = artifact.Reader
				} else {
					artifact.Turns[turn].Speaker = artifact.Listener
				}
			}
		}
		artifact.EpisodeID = ""
		artifact.TaskID = ""
		artifact.RunID = ""
		encodedArtifact, _ := json.Marshal(artifact)
		encodedReview, _ := json.Marshal(StorySemanticReview{Valid: true})
		steps = append(steps,
			storyDemandGeneratorStep{response: string(encodedArtifact)},
			storyDemandGeneratorStep{response: string(encodedReview)},
		)
	}
	return steps
}

type storyDemandTestRuntime struct {
	orchestrator *IdleChatOrchestrator
	scheduler    *NightPreparationScheduler
	service      *StoryEpisodeService
	dispatches   chan error
	foreground   *atomic.Bool
	setNow       func(time.Time)
}

func newStoryDemandTestRuntime(t *testing.T, generator StoryCodexGenerator) *storyDemandTestRuntime {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	current := &atomic.Int64{}
	setNow := func(value time.Time) { current.Store(value.UnixNano()) }
	now := func() time.Time { return time.Unix(0, current.Load()).In(loc) }
	foreground := &atomic.Bool{}
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	orchestrator.mu.Lock()
	orchestrator.disabled = true // Prevent the normal auto-chat timer from owning stock production.
	orchestrator.mu.Unlock()
	service := NewPersistentStoryEpisodeService(filepath.Join(t.TempDir(), "stories.jsonl"), 3, generator, map[string]string{
		"mio": "Mio persona", "shiro": "Shiro persona",
	})
	service.SetRunIssuer(newTestIdleChatRunIssuer(t))
	orchestrator.SetStoryEpisodeService(service)
	dispatches := make(chan error, 8)
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		now,
		foreground.Load,
		func(ctx context.Context, _ []string) {
			dispatches <- orchestrator.PrepareRequestedStoryEpisodesWithContext(ctx)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.SetNightPreparationScheduler(scheduler)
	t.Cleanup(func() {
		scheduler.Close()
		orchestrator.Stop()
	})
	return &storyDemandTestRuntime{
		orchestrator: orchestrator,
		scheduler:    scheduler,
		service:      service,
		dispatches:   dispatches,
		foreground:   foreground,
		setNow:       setNow,
	}
}

func waitStoryDemandDispatch(t *testing.T, runtime *storyDemandTestRuntime) {
	t.Helper()
	select {
	case err := <-runtime.dispatches:
		if err != nil {
			t.Fatalf("story demand dispatch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("story demand was not dispatched")
	}
}

func waitStoryDemandSchedulerIdle(t *testing.T, scheduler *NightPreparationScheduler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		busy := scheduler.dispatching
		scheduler.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("night preparation dispatch did not finish")
}

func TestStoryDemandDeferredUntilNightAndForegroundHold(t *testing.T) {
	generator := &storyDemandGenerator{steps: successfulStoryDemandSteps(0, 1)}
	runtime := newStoryDemandTestRuntime(t, generator)
	loc, _ := time.LoadLocation("Asia/Tokyo")
	runtime.setNow(time.Date(2026, time.October, 24, 1, 59, 0, 0, loc))

	state, accepted := runtime.scheduler.RequestStoryPreparationTarget(1)
	if !accepted || state != "deferred" {
		t.Fatalf("request = (%q, %t), want accepted/deferred", state, accepted)
	}
	runtime.foreground.Store(true)
	runtime.setNow(time.Date(2026, time.October, 24, 2, 0, 0, 0, loc))
	runtime.scheduler.Tick()
	select {
	case err := <-runtime.dispatches:
		t.Fatalf("foreground-held demand dispatched: %v", err)
	default:
	}
	if got := generator.callCount(); got != 0 {
		t.Fatalf("generation calls while deferred/foreground-held = %d, want 0", got)
	}
	if target, pending := runtime.scheduler.PendingStoryPreparationTarget(); !pending || target != 1 {
		t.Fatalf("held target = (%d, %t), want (1, true)", target, pending)
	}

	runtime.foreground.Store(false)
	runtime.scheduler.NotifyPolicyChanged()
	waitStoryDemandDispatch(t, runtime)
	runtime.orchestrator.waitGenerationWork()
	if got := runtime.service.Snapshot(); got.Ready != 1 || got.Missing != 2 {
		t.Fatalf("owner stock after night dispatch = %+v, want one ready episode", got)
	}
	if target, pending := runtime.scheduler.PendingStoryPreparationTarget(); pending || target != 0 {
		t.Fatalf("completed owner target remains pending: (%d, %t)", target, pending)
	}
}

func TestStoryDemandPartialSuccessRetryKeepsFixedTargetWithoutOvergeneration(t *testing.T) {
	steps := successfulStoryDemandSteps(0, 1)
	steps = append(steps, storyDemandGeneratorStep{err: errors.New("temporary generation failure")})
	steps = append(steps, successfulStoryDemandSteps(1, 1)...)
	generator := &storyDemandGenerator{steps: steps}
	runtime := newStoryDemandTestRuntime(t, generator)
	loc, _ := time.LoadLocation("Asia/Tokyo")
	runtime.setNow(time.Date(2026, time.October, 24, 3, 0, 0, 0, loc))

	if state, accepted := runtime.scheduler.RequestStoryPreparationTarget(2); !accepted || state != "queued" {
		t.Fatalf("request = (%q, %t), want accepted/queued", state, accepted)
	}
	waitStoryDemandDispatch(t, runtime)
	runtime.orchestrator.waitGenerationWork()
	waitStoryDemandSchedulerIdle(t, runtime.scheduler)
	if got := runtime.service.Snapshot(); got.Ready != 1 || got.Missing != 2 {
		t.Fatalf("partial stock = %+v, want one ready episode toward fixed target 2", got)
	}
	if target, pending := runtime.scheduler.PendingStoryPreparationTarget(); !pending || target != 2 {
		t.Fatalf("partial success lost target: (%d, %t), want (2, true)", target, pending)
	}

	runtime.scheduler.Tick()
	waitStoryDemandDispatch(t, runtime)
	runtime.orchestrator.waitGenerationWork()
	if got := runtime.service.Snapshot(); got.Ready != 2 || got.Missing != 1 || len(got.Episodes) != 2 {
		t.Fatalf("retried stock = %+v, want exactly 2 ready episodes", got)
	}
	if got := generator.callCount(); got != 5 {
		t.Fatalf("generator calls = %d, want 5 (one failed generation plus two valid episodes)", got)
	}
	if target, pending := runtime.scheduler.PendingStoryPreparationTarget(); pending || target != 0 {
		t.Fatalf("satisfied absolute target remains pending: (%d, %t)", target, pending)
	}
}

func TestStoryDemandCancellationRetainsTargetAndRetries(t *testing.T) {
	steps := append([]storyDemandGeneratorStep{{waitCancel: true}}, successfulStoryDemandSteps(0, 1)...)
	generator := &storyDemandGenerator{steps: steps, started: make(chan int, 4)}
	runtime := newStoryDemandTestRuntime(t, generator)
	loc, _ := time.LoadLocation("Asia/Tokyo")
	runtime.setNow(time.Date(2026, time.October, 24, 3, 0, 0, 0, loc))

	if _, accepted := runtime.scheduler.RequestStoryPreparationTarget(1); !accepted {
		t.Fatal("story target was rejected")
	}
	waitStoryDemandDispatch(t, runtime)
	select {
	case call := <-generator.started:
		if call != 1 {
			t.Fatalf("first generator call = %d, want 1", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("story generator did not start")
	}
	runtime.foreground.Store(true)
	runtime.scheduler.NotifyPolicyChanged()
	runtime.orchestrator.waitGenerationWork()
	waitStoryDemandSchedulerIdle(t, runtime.scheduler)
	if target, pending := runtime.scheduler.PendingStoryPreparationTarget(); !pending || target != 1 {
		t.Fatalf("cancellation lost target: (%d, %t), want (1, true)", target, pending)
	}
	if got := runtime.service.Snapshot().Ready; got != 0 {
		t.Fatalf("ready episodes after cancellation = %d, want 0", got)
	}

	runtime.foreground.Store(false)
	runtime.scheduler.NotifyPolicyChanged()
	waitStoryDemandDispatch(t, runtime)
	runtime.orchestrator.waitGenerationWork()
	if got := runtime.service.Snapshot(); got.Ready != 1 || got.Missing != 2 {
		t.Fatalf("stock after cancellation retry = %+v, want one ready episode at fixed target 1", got)
	}
	if target, pending := runtime.scheduler.PendingStoryPreparationTarget(); pending || target != 0 {
		t.Fatalf("retried target remains pending: (%d, %t)", target, pending)
	}
}

func TestStoryDemandBusyProducerLeavesTargetPending(t *testing.T) {
	generator := &storyDemandGenerator{steps: successfulStoryDemandSteps(0, 1)}
	runtime := newStoryDemandTestRuntime(t, generator)
	loc, _ := time.LoadLocation("Asia/Tokyo")
	runtime.setNow(time.Date(2026, time.October, 24, 3, 0, 0, 0, loc))
	runtime.orchestrator.topicProducerMu.Lock()
	runtime.orchestrator.topicProducerBusy = true
	runtime.orchestrator.topicProducerMu.Unlock()

	if _, accepted := runtime.scheduler.RequestStoryPreparationTarget(1); !accepted {
		t.Fatal("story target was rejected")
	}
	waitStoryDemandDispatch(t, runtime)
	waitStoryDemandSchedulerIdle(t, runtime.scheduler)
	if target, pending := runtime.scheduler.PendingStoryPreparationTarget(); !pending || target != 1 {
		t.Fatalf("busy producer lost target: (%d, %t), want (1, true)", target, pending)
	}
	runtime.orchestrator.topicProducerMu.Lock()
	runtime.orchestrator.topicProducerBusy = false
	runtime.orchestrator.topicProducerMu.Unlock()

	runtime.scheduler.Tick()
	waitStoryDemandDispatch(t, runtime)
	runtime.orchestrator.waitGenerationWork()
	if got := runtime.service.Snapshot().Ready; got != 1 {
		t.Fatalf("ready episodes after busy retry = %d, want 1", got)
	}
	if target, pending := runtime.scheduler.PendingStoryPreparationTarget(); pending || target != 0 {
		t.Fatalf("completed busy retry target remains pending: (%d, %t)", target, pending)
	}
}
