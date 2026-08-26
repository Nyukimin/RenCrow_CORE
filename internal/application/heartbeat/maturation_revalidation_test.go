package heartbeat

import (
	"context"
	"testing"
	"time"

	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

type heartbeatMaturationStub struct {
	calls int
}

func (s *heartbeatMaturationStub) AcquireRunnable(context.Context) (backlogapp.AcquireRunnableResult, error) {
	return backlogapp.AcquireRunnableResult{}, nil
}
func (s *heartbeatMaturationStub) Revise(context.Context, string, backlogapp.ReviseRequest) (domainbacklog.Item, error) {
	return domainbacklog.Item{}, nil
}
func (s *heartbeatMaturationStub) RunEligibleRevalidations(context.Context, int) (backlogapp.RevalidationSweepReport, error) {
	s.calls++
	return backlogapp.RevalidationSweepReport{Eligible: 1, Attempted: 1, Completed: 1, ItemIDs: []string{"atlas-1"}}, nil
}

func TestMaturationRevalidationRunsOncePerDay(t *testing.T) {
	service := NewHeartbeatService(nil, nil, t.TempDir(), 5)
	owner := &heartbeatMaturationStub{}
	service.WithAtlasService(owner)
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	service.runMaturationRevalidation(context.Background(), now)
	service.runMaturationRevalidation(context.Background(), now.Add(23*time.Hour))
	if owner.calls != 1 {
		t.Fatalf("sweep calls before daily boundary=%d", owner.calls)
	}
	service.runMaturationRevalidation(context.Background(), now.Add(24*time.Hour))
	if owner.calls != 2 {
		t.Fatalf("sweep calls at daily boundary=%d", owner.calls)
	}
}
