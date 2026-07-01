package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	domainrouting "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	domainsuperagent "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/superagent"
	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

func TestNewSuperAgentRunQueueProcessorSendsQueueItemToOrchestrator(t *testing.T) {
	processor := &captureSuperAgentRunQueueProcessor{
		response: orchestrator.ProcessMessageResponse{
			Route: domainrouting.RouteCODE,
			JobID: "job-1",
		},
	}
	item := domainsuperagent.RunQueueItem{
		QueueID:      " q-1 ",
		RunID:        "run-1",
		WorkstreamID: "ws-1",
		Goal:         " continue the queued run ",
		Action:       " resume ",
	}

	summary, err := newSuperAgentRunQueueProcessor(processor, backgroundJobFailureReporter{}).ProcessRunQueueItem(context.Background(), item)
	if err != nil {
		t.Fatalf("ProcessRunQueueItem() error = %v", err)
	}
	if summary != "route=CODE job_id=job-1" {
		t.Fatalf("summary = %q, want route=CODE job_id=job-1", summary)
	}
	req := processor.request
	if req.SessionID != "ws-1" || req.Channel != "superagent" || req.ChatID != "q-1" || req.UserMessage != "continue the queued run" {
		t.Fatalf("request = %#v", req)
	}
}

func TestNewSuperAgentRunQueueProcessorRejectsUnsupportedAction(t *testing.T) {
	processor := &captureSuperAgentRunQueueProcessor{}
	_, err := newSuperAgentRunQueueProcessor(processor, backgroundJobFailureReporter{}).ProcessRunQueueItem(context.Background(), domainsuperagent.RunQueueItem{
		QueueID: "q-1",
		Goal:    "run",
		Action:  "external_pr",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported run queue action") {
		t.Fatalf("ProcessRunQueueItem() error = %v, want unsupported action error", err)
	}
	if processor.called {
		t.Fatal("processor was called for unsupported action")
	}
}

func TestNewSuperAgentRunQueueProcessorAllowsExplicitChatAction(t *testing.T) {
	processor := &captureSuperAgentRunQueueProcessor{
		response: orchestrator.ProcessMessageResponse{
			Route: domainrouting.RouteCHAT,
			JobID: "job-chat",
		},
	}
	summary, err := newSuperAgentRunQueueProcessor(processor, backgroundJobFailureReporter{}).ProcessRunQueueItem(context.Background(), domainsuperagent.RunQueueItem{
		QueueID: "q-1",
		Goal:    "run",
		Action:  "chat",
	})
	if err != nil {
		t.Fatalf("ProcessRunQueueItem() error = %v", err)
	}
	if summary != "route=CHAT job_id=job-chat" {
		t.Fatalf("summary = %q, want route=CHAT job_id=job-chat", summary)
	}
}

func TestNewSuperAgentRunQueueProcessorRejectsChatFallbackForResume(t *testing.T) {
	processor := &captureSuperAgentRunQueueProcessor{
		response: orchestrator.ProcessMessageResponse{
			Route: domainrouting.RouteCHAT,
			JobID: "job-chat",
		},
	}
	_, err := newSuperAgentRunQueueProcessor(processor, backgroundJobFailureReporter{}).ProcessRunQueueItem(context.Background(), domainsuperagent.RunQueueItem{
		QueueID: "q-1",
		Goal:    "run",
		Action:  "resume",
	})
	if err == nil || !strings.Contains(err.Error(), "CHAT route") {
		t.Fatalf("ProcessRunQueueItem() error = %v, want CHAT route error", err)
	}
}

func TestNewSuperAgentRunQueueProcessorRejectsMissingJobID(t *testing.T) {
	processor := &captureSuperAgentRunQueueProcessor{
		response: orchestrator.ProcessMessageResponse{
			Route: domainrouting.RouteCODE,
		},
	}
	_, err := newSuperAgentRunQueueProcessor(processor, backgroundJobFailureReporter{}).ProcessRunQueueItem(context.Background(), domainsuperagent.RunQueueItem{
		QueueID: "q-1",
		Goal:    "run",
		Action:  "resume",
	})
	if err == nil || !strings.Contains(err.Error(), "job_id") {
		t.Fatalf("ProcessRunQueueItem() error = %v, want job_id error", err)
	}
}

func TestNewSuperAgentRunQueueProcessorReportsFailure(t *testing.T) {
	processor := &captureSuperAgentRunQueueProcessor{}
	listener := &captureBackgroundJobEventListener{}
	_, err := newSuperAgentRunQueueProcessor(processor, newBackgroundJobFailureReporter(listener)).ProcessRunQueueItem(context.Background(), domainsuperagent.RunQueueItem{
		QueueID: "q-1",
		RunID:   "run-1",
		Goal:    "run",
		Action:  "external_pr",
	})
	if err == nil {
		t.Fatal("ProcessRunQueueItem() error = nil, want error")
	}
	events := listener.Events()
	if len(events) != 2 {
		t.Fatalf("events=%d, want 2", len(events))
	}
	if events[0].Type != "background_job.failed" || events[0].ChatID != "superagent_run_queue" {
		t.Fatalf("failed event = %#v", events[0])
	}
	if !strings.Contains(events[0].Content, "unsupported run queue action") {
		t.Fatalf("failed content = %q", events[0].Content)
	}
	if events[1].Type != "job.notification" || events[1].To != "mio" {
		t.Fatalf("notification event = %#v", events[1])
	}
	if processor.called {
		t.Fatal("processor was called for unsupported action")
	}
}

func TestBackgroundJobFailureReporterEmitsShiroAndMioEvents(t *testing.T) {
	listener := &captureBackgroundJobEventListener{}
	reporter := newBackgroundJobFailureReporter(listener)
	reporter.Failed("daily_intake_sweep", errors.New("boom"), "rule_limit=100")

	events := listener.Events()
	if len(events) != 2 {
		t.Fatalf("events=%d, want 2", len(events))
	}
	failed := events[0]
	if failed.Type != "background_job.failed" || failed.From != "background_job" || failed.To != "shiro" || failed.Route != "OPS" || failed.Channel != "background" || failed.ChatID != "daily_intake_sweep" {
		t.Fatalf("failed event = %#v", failed)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(failed.Content), &payload); err != nil {
		t.Fatalf("failed payload is not JSON: %v", err)
	}
	if payload["job"] != "daily_intake_sweep" || payload["status"] != "failed" || payload["error"] != "boom" || payload["llm_policy"] != "no_llm_until_failure" {
		t.Fatalf("payload = %#v", payload)
	}
	notification := events[1]
	if notification.Type != "job.notification" || notification.From != "shiro" || notification.To != "mio" || notification.JobID != failed.JobID || notification.SessionID != failed.SessionID {
		t.Fatalf("notification event = %#v", notification)
	}
	if !strings.Contains(notification.Content, "Shiro investigation requested") || !strings.Contains(notification.Content, "Mio should report") {
		t.Fatalf("notification content = %q", notification.Content)
	}
}

func TestStartMemoryLifecycleJobReportsFailure(t *testing.T) {
	runner := &failingMemoryLifecycleRunner{err: errors.New("maintenance failed")}
	listener := &captureBackgroundJobEventListener{}
	stop := make(chan struct{})
	defer close(stop)
	startMemoryLifecycleJobRunner(runner, memoryLifecycleJobConfig{
		Interval: time.Hour,
		Now:      func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) },
		Label:    "test",
	}, stop, newBackgroundJobFailureReporter(listener))
	if !listener.EventCountReachesWithin(2, 500*time.Millisecond) {
		t.Fatalf("events=%d, want failure notification events", len(listener.Events()))
	}
	events := listener.Events()
	if events[0].ChatID != "memory_lifecycle" || events[1].ChatID != "memory_lifecycle" {
		t.Fatalf("events = %#v", events)
	}
}

func TestMemoryLifecycleJobConfigAcceleratesMonthIntoOneHour(t *testing.T) {
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC", "3600")
	var wall = time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	cfg := memoryLifecycleJobConfigFromEnv(func() time.Time { return wall })
	if cfg.Interval != 30*time.Second {
		t.Fatalf("interval=%s, want 30s", cfg.Interval)
	}
	wall = wall.Add(time.Hour)
	got := cfg.Now()
	want := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("accelerated now=%s, want %s", got, want)
	}
}

func TestMemoryLifecycleJobConfigCanRunMonthAsFastAsOneSecond(t *testing.T) {
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC", "1")
	var wall = time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	cfg := memoryLifecycleJobConfigFromEnv(func() time.Time { return wall })
	if cfg.Interval != minimumAcceleratedMemoryLifecycleInterval {
		t.Fatalf("interval=%s, want %s", cfg.Interval, minimumAcceleratedMemoryLifecycleInterval)
	}
	wall = wall.Add(time.Second)
	got := cfg.Now()
	want := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("accelerated now=%s, want %s", got, want)
	}
}

func TestMemoryLifecycleJobConfigCanRunMonthAsFastAsOneHundredMilliseconds(t *testing.T) {
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_MS", "100")
	var wall = time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	cfg := memoryLifecycleJobConfigFromEnv(func() time.Time { return wall })
	if cfg.Interval != minimumAcceleratedMemoryLifecycleInterval {
		t.Fatalf("interval=%s, want %s", cfg.Interval, minimumAcceleratedMemoryLifecycleInterval)
	}
	wall = wall.Add(100 * time.Millisecond)
	got := cfg.Now()
	want := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("accelerated now=%s, want %s", got, want)
	}
}

func TestMemoryLifecycleJobConfigAllowsExplicitAcceleratedInterval(t *testing.T) {
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC", "3600")
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_INTERVAL_SEC", "5")
	cfg := memoryLifecycleJobConfigFromEnv(time.Now)
	if cfg.Interval != 5*time.Second {
		t.Fatalf("interval=%s, want 5s", cfg.Interval)
	}
}

func TestMemoryLifecycleJobConfigAllowsMillisecondIntervalOverride(t *testing.T) {
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_MS", "100")
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_INTERVAL_MS", "100")
	cfg := memoryLifecycleJobConfigFromEnv(time.Now)
	if cfg.Interval != 100*time.Millisecond {
		t.Fatalf("interval=%s, want 100ms", cfg.Interval)
	}
}

func TestMemoryLifecycleJobConfigIgnoresInvalidAcceleration(t *testing.T) {
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC", "bad")
	t.Setenv("RENCROW_MEMORY_LIFECYCLE_INTERVAL_SEC", "bad")
	cfg := memoryLifecycleJobConfigFromEnv(time.Now)
	if cfg.Interval != 24*time.Hour {
		t.Fatalf("interval=%s, want normal 24h", cfg.Interval)
	}
}

func TestMemoryLifecycleJobConfigDefaultEnvIsNormal(t *testing.T) {
	_ = os.Unsetenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC")
	_ = os.Unsetenv("RENCROW_MEMORY_LIFECYCLE_INTERVAL_SEC")
	cfg := memoryLifecycleJobConfigFromEnv(time.Now)
	if cfg.Interval != 24*time.Hour || cfg.Label != "normal" {
		t.Fatalf("unexpected default cfg: %+v", cfg)
	}
}

func TestStartMemoryLifecycleJobWithConfigUsesConfiguredInterval(t *testing.T) {
	runner := &countingMemoryLifecycleRunner{}
	stop := make(chan struct{})
	defer close(stop)
	startMemoryLifecycleJobRunner(runner, memoryLifecycleJobConfig{
		Interval: 10 * time.Millisecond,
		Now:      func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) },
		Label:    "test",
	}, stop, backgroundJobFailureReporter{})
	if !countReachesWithin(&runner.calls, 3, 500*time.Millisecond) {
		t.Fatalf("maintenance calls=%d, want at least 3 using configured interval", runner.calls.Load())
	}
}

type captureSuperAgentRunQueueProcessor struct {
	called   bool
	request  orchestrator.ProcessMessageRequest
	response orchestrator.ProcessMessageResponse
}

func (p *captureSuperAgentRunQueueProcessor) ProcessMessage(_ context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	p.called = true
	p.request = req
	return p.response, nil
}

type countingMemoryLifecycleRunner struct {
	calls atomic.Int64
}

func (r *countingMemoryLifecycleRunner) RunMemoryLifecycleMaintenance(context.Context, conversationpersistence.MemoryLifecycleOptions) (*conversationpersistence.MemoryLifecycleResult, error) {
	r.calls.Add(1)
	return &conversationpersistence.MemoryLifecycleResult{}, nil
}

type failingMemoryLifecycleRunner struct {
	err error
}

func (r *failingMemoryLifecycleRunner) RunMemoryLifecycleMaintenance(context.Context, conversationpersistence.MemoryLifecycleOptions) (*conversationpersistence.MemoryLifecycleResult, error) {
	return nil, r.err
}

type captureBackgroundJobEventListener struct {
	mu     sync.Mutex
	events []orchestrator.OrchestratorEvent
}

func (l *captureBackgroundJobEventListener) OnEvent(ev orchestrator.OrchestratorEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *captureBackgroundJobEventListener) Events() []orchestrator.OrchestratorEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := make([]orchestrator.OrchestratorEvent, len(l.events))
	copy(events, l.events)
	return events
}

func (l *captureBackgroundJobEventListener) EventCountReachesWithin(want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(l.Events()) >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func countReachesWithin(count *atomic.Int64, want int64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count.Load() >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
