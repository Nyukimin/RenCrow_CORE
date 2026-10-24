package idlechat

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type nightGateCountingProvider struct {
	calls atomic.Int64
	call  func(context.Context) error
}

func (p *nightGateCountingProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls.Add(1)
	if p.call != nil {
		if err := p.call(ctx); err != nil {
			return llm.GenerateResponse{}, err
		}
	}
	return llm.GenerateResponse{Content: "generated"}, nil
}

func (*nightGateCountingProvider) Name() string { return "night-gate-test" }

type nightGateCountingCodexGenerator struct {
	calls atomic.Int64
	call  func(context.Context) error
}

func (g *nightGateCountingCodexGenerator) Generate(ctx context.Context, _ string) (string, error) {
	g.calls.Add(1)
	if g.call != nil {
		if err := g.call(ctx); err != nil {
			return "", err
		}
	}
	return "generated", nil
}

func nightGateScheduler(t *testing.T, now time.Time, foreground func() bool) *NightPreparationScheduler {
	t.Helper()
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		func() time.Time { return now },
		foreground,
		func(context.Context, []string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scheduler.Close)
	return scheduler
}

func nightGateOrchestratorContext(t *testing.T, o *IdleChatOrchestrator, now time.Time, foreground func() bool) context.Context {
	t.Helper()
	scheduler := nightGateScheduler(t, now, foreground)
	o.SetNightPreparationScheduler(scheduler)
	return withNightPreparationScheduler(o.ctx, scheduler)
}

func nightGateAllowedContext(t *testing.T) context.Context {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduler := nightGateScheduler(t, time.Date(2026, 10, 24, 3, 0, 0, 0, loc), func() bool { return false })
	return withNightPreparationScheduler(context.Background(), scheduler)
}

func nightGateAllowOrchestrator(t *testing.T, o *IdleChatOrchestrator) {
	t.Helper()
	ctx := nightGateAllowedContext(t)
	scheduler := nightPreparationSchedulerFromContext(ctx)
	o.SetNightPreparationScheduler(scheduler)
	o.ctx = withNightPreparationScheduler(o.ctx, scheduler)
	o.runCtx = withNightPreparationScheduler(o.runCtx, scheduler)
}

func TestIdleChatInterruptibleProviderBlocksNightSendOutsideWindow(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduler := nightGateScheduler(t, time.Date(2026, 10, 24, 1, 59, 0, 0, loc), func() bool { return false })
	inner := &nightGateCountingProvider{}
	provider := interruptibleIdleChatProvider(inner)
	ctx := withNightPreparationScheduler(context.Background(), scheduler)

	_, err = provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "generate"}}})
	if !errors.Is(err, ErrNightPreparationBlocked) {
		t.Fatalf("Generate error = %v, want ErrNightPreparationBlocked", err)
	}
	if got := inner.calls.Load(); got != 0 {
		t.Fatalf("provider sends = %d, want 0 outside the schedule window", got)
	}
}

func TestIdleChatInterruptibleProviderBlocksSendWithoutScheduler(t *testing.T) {
	inner := &nightGateCountingProvider{}
	provider := interruptibleIdleChatProvider(inner)

	_, err := provider.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "generate"}}})
	if !errors.Is(err, ErrNightPreparationBlocked) {
		t.Fatalf("Generate error = %v, want ErrNightPreparationBlocked without scheduler", err)
	}
	if got := inner.calls.Load(); got != 0 {
		t.Fatalf("provider sends = %d, want 0 without scheduler", got)
	}
}

func TestIdleChatInterruptibleProviderBlocksNightSendDuringForegroundUse(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduler := nightGateScheduler(t, time.Date(2026, 10, 24, 3, 0, 0, 0, loc), func() bool { return true })
	inner := &nightGateCountingProvider{}
	provider := interruptibleIdleChatProvider(inner)
	ctx := withNightPreparationScheduler(context.Background(), scheduler)

	_, err = provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "generate"}}})
	if !errors.Is(err, ErrNightPreparationBlocked) {
		t.Fatalf("Generate error = %v, want ErrNightPreparationBlocked", err)
	}
	if got := inner.calls.Load(); got != 0 {
		t.Fatalf("provider sends = %d, want 0 during foreground use", got)
	}
}

func TestIdleChatInterruptibleProviderAllowsNightSendInsideWindowWithoutForeground(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduler := nightGateScheduler(t, time.Date(2026, 10, 24, 3, 0, 0, 0, loc), func() bool { return false })
	inner := &nightGateCountingProvider{}
	provider := interruptibleIdleChatProvider(inner)
	ctx := withNightPreparationScheduler(context.Background(), scheduler)

	response, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "generate"}}})
	if err != nil {
		t.Fatalf("Generate inside the allowed window: %v", err)
	}
	if got := response.Content; got != "generated" {
		t.Fatalf("response content = %q, want generated", got)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("provider sends = %d, want 1", got)
	}
}

func TestIdleChatInterruptibleProviderPassesSchedulerCancellationToProvider(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	var foreground atomic.Bool
	scheduler := nightGateScheduler(t, time.Date(2026, 10, 24, 3, 0, 0, 0, loc), foreground.Load)
	started := make(chan struct{})
	inner := &nightGateCountingProvider{call: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	provider := interruptibleIdleChatProvider(inner)
	ctx := withNightPreparationScheduler(context.Background(), scheduler)
	finished := make(chan error, 1)
	go func() {
		_, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "generate"}}})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start inside the allowed window")
	}

	foreground.Store(true)
	scheduler.NotifyPolicyChanged()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("provider result = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground transition did not cancel the provider context")
	}
}

func TestIdleChatCodexProviderBlocksNightSendOutsideWindow(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduler := nightGateScheduler(t, time.Date(2026, 10, 24, 1, 59, 0, 0, loc), func() bool { return false })
	generator := &nightGateCountingCodexGenerator{}
	provider := newIdleChatCodexLLMProvider(generator)
	ctx := withNightPreparationScheduler(context.Background(), scheduler)

	_, err = provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "generate"}}})
	if !errors.Is(err, ErrNightPreparationBlocked) {
		t.Fatalf("Generate error = %v, want ErrNightPreparationBlocked", err)
	}
	if got := generator.calls.Load(); got != 0 {
		t.Fatalf("Codex sends = %d, want 0 outside the schedule window", got)
	}
}

func TestIdleChatCodexProviderBlocksSendWithoutScheduler(t *testing.T) {
	generator := &nightGateCountingCodexGenerator{}
	provider := newIdleChatCodexLLMProvider(generator)

	_, err := provider.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "generate"}}})
	if !errors.Is(err, ErrNightPreparationBlocked) {
		t.Fatalf("Generate error = %v, want ErrNightPreparationBlocked without scheduler", err)
	}
	if got := generator.calls.Load(); got != 0 {
		t.Fatalf("Codex sends = %d, want 0 without scheduler", got)
	}
}

func TestIdleChatCodexRunEffectBlocksNightSendOutsideWindow(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	task, err := owner.Create(context.Background(), domaintask.Task{
		Title: "night gate test", Route: domaintask.RouteGeneral, Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := owner.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = owner.CompleteRun(context.Background(), task.TaskID, run.RunID, "Shiro", domaintask.StatusCancelled, "test completed", "")
	}()

	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduler := nightGateScheduler(t, time.Date(2026, 10, 24, 1, 59, 0, 0, loc), func() bool { return false })
	generator := &nightGateCountingCodexGenerator{}
	ctx := withNightPreparationScheduler(context.Background(), scheduler)

	_, err = generateIdleChatCodexWithRun(ctx, owner, task.TaskID, run.RunID, generator, "generate")
	if !errors.Is(err, ErrNightPreparationBlocked) {
		t.Fatalf("Codex run effect error = %v, want ErrNightPreparationBlocked", err)
	}
	if got := generator.calls.Load(); got != 0 {
		t.Fatalf("Codex run effect sends = %d, want 0 outside the schedule window", got)
	}
}

func TestIdleChatCodexRunEffectBlocksSendWithoutScheduler(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	task, err := owner.Create(context.Background(), domaintask.Task{
		Title: "night gate test", Route: domaintask.RouteGeneral, Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := owner.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = owner.CompleteRun(context.Background(), task.TaskID, run.RunID, "Shiro", domaintask.StatusCancelled, "test completed", "")
	}()
	generator := &nightGateCountingCodexGenerator{}

	_, err = generateIdleChatCodexWithRun(context.Background(), owner, task.TaskID, run.RunID, generator, "generate")
	if !errors.Is(err, ErrNightPreparationBlocked) {
		t.Fatalf("Codex run effect error = %v, want ErrNightPreparationBlocked without scheduler", err)
	}
	if got := generator.calls.Load(); got != 0 {
		t.Fatalf("Codex run effect sends = %d, want 0 without scheduler", got)
	}
}

func TestLegacyForecastDialoguePathBlocksCodexWithoutScheduler(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	generator := &nightGateCountingCodexGenerator{}
	service := NewPersistentDialogueEpisodeService(
		filepath.Join(t.TempDir(), "forecast_dialogue.jsonl"),
		generator,
		map[string]string{"mio": "Mio", "shiro": "Shiro"},
		DefaultDialogueInterestingnessConfig(),
	)
	service.SetRunIssuer(owner)
	service.SetGenerationCheckpointStore(NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "forecast_checkpoints.json")))

	_, err := service.Prepare(context.Background(), "legacy-forecast-http", TopicGenerationResult{
		Topic:     "地域の交通と高齢化の未来",
		Category:  TopicCategoryForecast,
		Strategy:  string(StrategyForecast),
		Initiator: "shiro",
		Seed:      TopicSeed{Category: TopicCategoryForecast, ForecastDomain: "社会", ForecastHorizon: "10年後"},
	}, 2)
	if !errors.Is(err, ErrNightPreparationBlocked) {
		t.Fatalf("legacy Forecast dialogue error = %v, want ErrNightPreparationBlocked", err)
	}
	if got := generator.calls.Load(); got != 0 {
		t.Fatalf("legacy Forecast dialogue Codex sends = %d, want 0 without scheduler", got)
	}
}

func TestIdleChatWordAndForecastProducersDoNotReserveOutsideNightWindow(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 24, 1, 59, 0, 0, loc)

	wordOwner := newTestIdleChatRunIssuer(t)
	wordGenerator := &nightGateCountingCodexGenerator{}
	word := NewIdleChatOrchestrator(nil, nil, []string{"shiro"}, 5, 1, 0.7, nil, "")
	word.SetRunIssuer(wordOwner)
	word.SetTopicCodexGenerator(wordGenerator)
	word.SetGenerationCheckpointStore(NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "word_checkpoints.json")))
	wordCtx := nightGateOrchestratorContext(t, word, start, func() bool { return false })
	word.InitWordTopicStockWithContext(wordCtx, filepath.Join(t.TempDir(), "word_stock.json"))
	if word.RefillWordTopicStockIfIdleWithContext(wordCtx, "stock_api") {
		t.Fatal("word refill was admitted outside the night window")
	}
	if snapshot := word.WordTopicStockSnapshot(); snapshot.Total != 0 || snapshot.Filling {
		t.Fatalf("word stock outside the night window = %#v, want no reservation", snapshot)
	}
	if got := wordGenerator.calls.Load(); got != 0 {
		t.Fatalf("word generator sends = %d, want 0 outside the night window", got)
	}
	wordTasks, err := wordOwner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("list word tasks: %v", err)
	}
	if len(wordTasks) != 0 {
		t.Fatalf("word tasks outside the night window = %d, want 0", len(wordTasks))
	}

	forecastOwner := newTestIdleChatRunIssuer(t)
	forecastGenerator := &nightGateCountingCodexGenerator{}
	forecast := NewIdleChatOrchestrator(nil, nil, []string{"shiro"}, 5, 1, 0.7, nil, "")
	forecast.SetRunIssuer(forecastOwner)
	forecast.SetTopicCodexGenerator(forecastGenerator)
	forecast.SetGenerationCheckpointStore(NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "forecast_checkpoints.json")))
	forecastCtx := nightGateOrchestratorContext(t, forecast, start, func() bool { return false })
	forecast.InitForecastTopicStockWithContext(forecastCtx, filepath.Join(t.TempDir(), "forecast_stock.json"))
	if forecast.RefillForecastTopicStockIfIdleWithContext(forecastCtx, "stock_api") {
		t.Fatal("forecast refill was admitted outside the night window")
	}
	if snapshot := forecast.ForecastTopicStockSnapshot(); snapshot.Total != 0 || snapshot.Filling {
		t.Fatalf("forecast stock outside the night window = %#v, want no reservation", snapshot)
	}
	if got := forecastGenerator.calls.Load(); got != 0 {
		t.Fatalf("forecast generator sends = %d, want 0 outside the night window", got)
	}
	forecastTasks, err := forecastOwner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("list forecast tasks: %v", err)
	}
	if len(forecastTasks) != 0 {
		t.Fatalf("forecast tasks outside the night window = %d, want 0", len(forecastTasks))
	}
}

func TestIdleChatWordAndForecastProducersDoNotReserveDuringForegroundUse(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 3, 0, 0, 0, loc)

	wordGenerator := &nightGateCountingCodexGenerator{}
	word := NewIdleChatOrchestrator(nil, nil, []string{"shiro"}, 5, 1, 0.7, nil, "")
	word.SetRunIssuer(newTestIdleChatRunIssuer(t))
	word.SetTopicCodexGenerator(wordGenerator)
	word.SetGenerationCheckpointStore(NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "word_checkpoints.json")))
	wordCtx := nightGateOrchestratorContext(t, word, now, func() bool { return true })
	word.InitWordTopicStockWithContext(wordCtx, filepath.Join(t.TempDir(), "word_stock.json"))
	if word.RefillWordTopicStockIfIdleWithContext(wordCtx, "tick") {
		t.Fatal("word refill was admitted during foreground use")
	}
	if snapshot := word.WordTopicStockSnapshot(); snapshot.Total != 0 || snapshot.Filling {
		t.Fatalf("word stock during foreground use = %#v, want no reservation", snapshot)
	}
	if got := wordGenerator.calls.Load(); got != 0 {
		t.Fatalf("word generator sends = %d, want 0 during foreground use", got)
	}

	forecastGenerator := &nightGateCountingCodexGenerator{}
	forecast := NewIdleChatOrchestrator(nil, nil, []string{"shiro"}, 5, 1, 0.7, nil, "")
	forecast.SetRunIssuer(newTestIdleChatRunIssuer(t))
	forecast.SetTopicCodexGenerator(forecastGenerator)
	forecast.SetGenerationCheckpointStore(NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "forecast_checkpoints.json")))
	forecastCtx := nightGateOrchestratorContext(t, forecast, now, func() bool { return true })
	forecast.InitForecastTopicStockWithContext(forecastCtx, filepath.Join(t.TempDir(), "forecast_stock.json"))
	if forecast.RefillForecastTopicStockIfIdleWithContext(forecastCtx, "tick") {
		t.Fatal("forecast refill was admitted during foreground use")
	}
	if snapshot := forecast.ForecastTopicStockSnapshot(); snapshot.Total != 0 || snapshot.Filling {
		t.Fatalf("forecast stock during foreground use = %#v, want no reservation", snapshot)
	}
	if got := forecastGenerator.calls.Load(); got != 0 {
		t.Fatalf("forecast generator sends = %d, want 0 during foreground use", got)
	}
}
