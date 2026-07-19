package pronunciationcheck

import (
	"context"
	"errors"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
)

type fakeGPUProbe struct {
	snapshots []GPUSnapshot
	calls     int
}

func TestScheduledExecutorMapsGPUDeferralAndCompletesReport(t *testing.T) {
	busyProbe := &fakeGPUProbe{snapshots: []GPUSnapshot{{
		Name: "NVIDIA GeForce RTX 5060 Ti", FreeMB: 1200, UtilizationPercent: 50,
	}}}
	executor := NewScheduledExecutor(NewService(busyProbe, &fakeTool{}, Config{
		GPUMatch: "RTX 5060 Ti", MinFreeMB: 768, MaxUtilizationPercent: 10,
		RetryAfter: 5 * time.Minute,
	}))
	job := domainscheduler.Job{Target: ScheduledTarget}
	if _, err := executor.ExecuteScheduledJob(context.Background(), job); err == nil {
		t.Fatal("ExecuteScheduledJob() returned nil error for busy GPU")
	}

	idleProbe := &fakeGPUProbe{snapshots: []GPUSnapshot{{
		Name: "NVIDIA GeForce RTX 5060 Ti", FreeMB: 1200, UtilizationPercent: 0,
	}}}
	executor = NewScheduledExecutor(NewService(idleProbe, &fakeTool{}, Config{
		GPUMatch: "RTX 5060 Ti", MinFreeMB: 768, MaxUtilizationPercent: 10, IdleSamples: 1,
	}))
	summary, err := executor.ExecuteScheduledJob(context.Background(), job)
	if err != nil || summary == "" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}

func (p *fakeGPUProbe) Snapshot(context.Context, string) (GPUSnapshot, error) {
	if p.calls >= len(p.snapshots) {
		return GPUSnapshot{}, errors.New("unexpected GPU probe call")
	}
	result := p.snapshots[p.calls]
	p.calls++
	return result, nil
}

type fakeTool struct {
	calls int
}

func (t *fakeTool) Run(context.Context) (ToolReport, error) {
	t.calls++
	return ToolReport{Total: 30, Passed: 29, Failed: 1}, nil
}

func TestServiceRunsToolAfterConsecutiveIdleSamples(t *testing.T) {
	probe := &fakeGPUProbe{snapshots: []GPUSnapshot{
		{Name: "NVIDIA GeForce RTX 5060 Ti", FreeMB: 1200, UtilizationPercent: 0},
		{Name: "NVIDIA GeForce RTX 5060 Ti", FreeMB: 1180, UtilizationPercent: 2},
		{Name: "NVIDIA GeForce RTX 5060 Ti", FreeMB: 1170, UtilizationPercent: 1},
	}}
	tool := &fakeTool{}
	service := NewService(probe, tool, Config{
		GPUMatch: "RTX 5060 Ti", MinFreeMB: 768, MaxUtilizationPercent: 10,
		IdleSamples: 3, RetryAfter: 5 * time.Minute,
	}).WithSleep(func(context.Context, time.Duration) error { return nil })

	result, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if probe.calls != 3 || tool.calls != 1 || result.Total != 30 {
		t.Fatalf("probe.calls=%d tool.calls=%d result=%+v", probe.calls, tool.calls, result)
	}
}

func TestServiceDefersWhenGPUIsBusy(t *testing.T) {
	probe := &fakeGPUProbe{snapshots: []GPUSnapshot{{
		Name: "NVIDIA GeForce RTX 5060 Ti", FreeMB: 1200, UtilizationPercent: 42,
	}}}
	tool := &fakeTool{}
	service := NewService(probe, tool, Config{
		GPUMatch: "RTX 5060 Ti", MinFreeMB: 768, MaxUtilizationPercent: 10,
		IdleSamples: 3, RetryAfter: 5 * time.Minute,
	})

	_, err := service.Run(context.Background())
	var deferred *DeferredError
	if !errors.As(err, &deferred) || deferred.RetryAfter != 5*time.Minute {
		t.Fatalf("Run() error = %#v", err)
	}
	if tool.calls != 0 {
		t.Fatalf("tool calls = %d", tool.calls)
	}
}

func TestServiceDefersWhenGPUFreeMemoryIsLow(t *testing.T) {
	probe := &fakeGPUProbe{snapshots: []GPUSnapshot{{
		Name: "NVIDIA GeForce RTX 5060 Ti", FreeMB: 500, UtilizationPercent: 0,
	}}}
	tool := &fakeTool{}
	service := NewService(probe, tool, Config{
		GPUMatch: "RTX 5060 Ti", MinFreeMB: 768, MaxUtilizationPercent: 10,
		IdleSamples: 3, RetryAfter: 5 * time.Minute,
	})

	_, err := service.Run(context.Background())
	var deferred *DeferredError
	if !errors.As(err, &deferred) {
		t.Fatalf("Run() error = %#v", err)
	}
	if tool.calls != 0 {
		t.Fatalf("tool calls = %d", tool.calls)
	}
}
