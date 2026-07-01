package job

import (
	"context"
	"testing"
	"time"

	domainjob "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/job"
)

func TestJSONLStoreKeepsLatestJobState(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	j := domainjob.Job{JobID: "job_test", Title: "test", Route: domainjob.RouteCode, Status: domainjob.StatusQueued, Priority: domainjob.PriorityNormal, InterruptPolicy: domainjob.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	j.Status = domainjob.StatusRunning
	j.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetJob(context.Background(), "job_test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domainjob.StatusRunning {
		t.Fatalf("expected latest status running, got %s", got.Status)
	}
	items, err := store.ListJobs(context.Background(), domainjob.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one latest job, got %d", len(items))
	}
}
