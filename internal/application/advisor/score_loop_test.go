package advisor

import (
	"context"
	"testing"
	"time"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
)

func TestDailyScoreServiceBuildsPreviousUTCWindowIdempotently(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	store := &recordingStore{
		runs: []advisorDomain.AdviceRunRecord{{
			RunID:         "run-1",
			AdvisorID:     advisorDomain.AdvisorCodex,
			Status:        advisorDomain.AdviceStatus(advisorDomain.StatusCompleted),
			StartedAt:     windowStart.Add(time.Hour),
			LatencyMillis: 1000,
		}},
		adoptions: []advisorDomain.AdvisorAdoptionRecord{{
			AdoptionID: "adopt-1",
			RunID:      "run-1",
			AdvisorID:  advisorDomain.AdvisorCodex,
			Adopted:    true,
			Outcome:    "success",
		}},
	}
	service := NewDailyScoreService(store, []advisorDomain.AdvisorID{advisorDomain.AdvisorCodex}, func() time.Time { return now })

	for range 2 {
		count, err := service.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if count < 0 || count > 1 {
			t.Fatalf("saved count=%d", count)
		}
	}
	if len(store.snapshots) != 1 {
		t.Fatalf("snapshots=%#v", store.snapshots)
	}
	got := store.snapshots[0]
	if got.SnapshotID != "advisor-score:codex:2026-07-24" ||
		!got.WindowStart.Equal(windowStart) ||
		!got.WindowEnd.Equal(windowStart.Add(24*time.Hour)) ||
		got.RequestCount != 1 || got.AdoptedCount != 1 || got.SuccessCount != 1 {
		t.Fatalf("snapshot=%#v", got)
	}
}

func TestDailyScoreServiceWritesObservableEmptyWindow(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := &recordingStore{}
	service := NewDailyScoreService(store, []advisorDomain.AdvisorID{advisorDomain.AdvisorCodex}, func() time.Time { return now })

	count, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(store.snapshots) != 1 || store.snapshots[0].RequestCount != 0 {
		t.Fatalf("count=%d snapshots=%#v", count, store.snapshots)
	}
}
