package jobmanager

import (
	"context"
	"testing"
	"time"

	domainjob "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/job"
	jobpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/job"
)

func TestManagerEmitsNotificationOnSucceeded(t *testing.T) {
	store, err := jobpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(store, DefaultParallelLimits())
	mgr.now = func() time.Time { return time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC) }
	j, err := mgr.CreateJob(context.Background(), domainjob.Job{Title: "write spec", Route: domainjob.RouteCode}, domainjob.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.StartJob(context.Background(), j.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.UpdateStatus(context.Background(), j.JobID, domainjob.StatusSucceeded, "done", nil); err != nil {
		t.Fatal(err)
	}
	items, err := mgr.ListNotifications(context.Background(), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].JobID != j.JobID || items[0].Level != domainjob.NotificationDone {
		t.Fatalf("unexpected notifications: %#v", items)
	}
}

func TestManagerBlocksSameModuleParallelStart(t *testing.T) {
	store, err := jobpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(store, ParallelLimits{Global: 3, PerModule: 1, CodingJobs: 2, LongResearchJobs: 1, DestructiveOps: 1})
	first, err := mgr.CreateJob(context.Background(), domainjob.Job{Title: "one", ModuleID: "RenCrow", Route: domainjob.RouteCode}, domainjob.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.CreateJob(context.Background(), domainjob.Job{Title: "two", ModuleID: "RenCrow", Route: domainjob.RouteCode}, domainjob.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.StartJob(context.Background(), first.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.StartJob(context.Background(), second.JobID); err == nil {
		t.Fatal("expected same module parallel limit error")
	}
}
