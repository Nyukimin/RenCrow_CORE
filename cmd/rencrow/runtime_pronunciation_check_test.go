package main

import (
	"context"
	"testing"

	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	schedulerpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/scheduler"
)

func TestEnsurePronunciationCheckJobRegistersSingleCORETask(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	service := schedulerapp.NewService(store, nil)
	for range 2 {
		if err := ensurePronunciationCheckJob(context.Background(), store, service, "every 24h"); err != nil {
			t.Fatalf("ensurePronunciationCheckJob() error = %v", err)
		}
	}
	jobs, err := store.ListJobs(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 1 || jobs[0].JobID != pronunciationCheckJobID || jobs[0].Target != pronunciationapp.ScheduledTarget || !jobs[0].Enabled {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestPronunciationSchedulerExecutorPreservesUnrelatedJobs(t *testing.T) {
	summary, err := (pronunciationSchedulerExecutor{}).ExecuteScheduledJob(context.Background(), domainscheduler.Job{Target: "unrelated"})
	if err != nil || summary != "scheduler run recorded without an executor" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}
