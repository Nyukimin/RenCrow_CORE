package main

import (
	"context"
	"errors"
	"testing"

	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	schedulerpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/scheduler"
)

func TestEnsurePronunciationCheckScheduleRegistersSingleCORETask(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	service := schedulerapp.NewService(store, nil)
	var firstID string
	for range 2 {
		scheduleID, err := ensurePronunciationCheckSchedule(context.Background(), store, service, "every 24h")
		if err != nil {
			t.Fatalf("ensurePronunciationCheckSchedule() error = %v", err)
		}
		if firstID == "" {
			firstID = string(scheduleID)
		} else if string(scheduleID) != firstID {
			t.Fatalf("expected stable schedule_id, got %s then %s", firstID, scheduleID)
		}
	}
	schedules, err := store.ListSchedules(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListSchedules() error = %v", err)
	}
	if len(schedules) != 1 || schedules[0].Target != pronunciationapp.ScheduledTarget || !schedules[0].Enabled {
		t.Fatalf("schedules = %+v", schedules)
	}
	if err := schedules[0].ScheduleID.Validate(); err != nil {
		t.Fatalf("ScheduleID.Validate() error = %v", err)
	}
}

func TestPronunciationSchedulerExecutorRejectsUnsupportedTarget(t *testing.T) {
	inner := &recordingPronunciationExecutor{}
	summary, err := (pronunciationSchedulerExecutor{inner: inner}).ExecuteSchedule(context.Background(), domainscheduler.Schedule{Target: "unrelated"})
	if summary != "" || !errors.Is(err, schedulerapp.ErrExecutorUnavailable) || inner.calls != 0 {
		t.Fatalf("summary=%q err=%v calls=%d", summary, err, inner.calls)
	}
}

func TestPronunciationSchedulerExecutorRejectsUnavailableSupportedTarget(t *testing.T) {
	summary, err := (pronunciationSchedulerExecutor{}).ExecuteSchedule(context.Background(), domainscheduler.Schedule{Target: pronunciationapp.ScheduledTarget})
	if summary != "" || !errors.Is(err, schedulerapp.ErrExecutorUnavailable) {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}

type recordingPronunciationExecutor struct {
	calls       int
	gotContext  context.Context
	gotSchedule domainscheduler.Schedule
	summary     string
	err         error
}

func (e *recordingPronunciationExecutor) ExecuteSchedule(ctx context.Context, schedule domainscheduler.Schedule) (string, error) {
	e.calls++
	e.gotContext = ctx
	e.gotSchedule = schedule
	return e.summary, e.err
}

type pronunciationContextKey struct{}

func TestPronunciationSchedulerExecutorDelegatesSupportedTargetOnce(t *testing.T) {
	marker := "context marker"
	ctx := context.WithValue(context.Background(), pronunciationContextKey{}, marker)
	typedErr := errors.New("typed executor failure")
	inner := &recordingPronunciationExecutor{summary: "executor summary", err: typedErr}
	schedule := domainscheduler.Schedule{
		Name:   "TTS pronunciation daily",
		Target: pronunciationapp.ScheduledTarget,
	}

	summary, err := (pronunciationSchedulerExecutor{inner: inner}).ExecuteSchedule(ctx, schedule)
	if !errors.Is(err, typedErr) || summary != "executor summary" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	if inner.calls != 1 || inner.gotSchedule != schedule {
		t.Fatalf("inner calls=%d schedule=%+v want=%+v", inner.calls, inner.gotSchedule, schedule)
	}
	if got := inner.gotContext.Value(pronunciationContextKey{}); got != marker {
		t.Fatalf("context value=%v want=%v", got, marker)
	}
}
