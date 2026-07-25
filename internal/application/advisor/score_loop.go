package advisor

import (
	"context"
	"fmt"
	"sort"
	"time"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
)

// DailyScoreService persists one deterministic score snapshot per Advisor and
// completed UTC day. Re-running the same window is a no-op.
type DailyScoreService struct {
	store      Store
	advisorIDs []advisorDomain.AdvisorID
	now        func() time.Time
}

func NewDailyScoreService(store Store, advisorIDs []advisorDomain.AdvisorID, now func() time.Time) *DailyScoreService {
	if now == nil {
		now = time.Now
	}
	unique := make(map[advisorDomain.AdvisorID]struct{}, len(advisorIDs))
	for _, id := range advisorIDs {
		if id != "" {
			unique[id] = struct{}{}
		}
	}
	normalized := make([]advisorDomain.AdvisorID, 0, len(unique))
	for id := range unique {
		normalized = append(normalized, id)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return &DailyScoreService{store: store, advisorIDs: normalized, now: now}
}

func (s *DailyScoreService) Run(ctx context.Context) (int, error) {
	if s == nil || s.store == nil || len(s.advisorIDs) == 0 {
		return 0, nil
	}
	now := s.now().UTC()
	windowEnd := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := windowEnd.Add(-24 * time.Hour)
	runs, err := s.store.ListAdviceRuns(ctx, 100000)
	if err != nil {
		return 0, fmt.Errorf("list Advisor runs: %w", err)
	}
	adoptions, err := s.store.ListAdvisorAdoptions(ctx, 100000)
	if err != nil {
		return 0, fmt.Errorf("list Advisor adoptions: %w", err)
	}
	existing, err := s.store.ListAdvisorScoreSnapshots(ctx, 100000)
	if err != nil {
		return 0, fmt.Errorf("list Advisor score snapshots: %w", err)
	}
	existingIDs := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		existingIDs[item.SnapshotID] = struct{}{}
	}
	saved := 0
	for _, advisorID := range s.advisorIDs {
		snapshot := buildScoreSnapshotForAdvisor(advisorID, runs, adoptions, windowStart, windowEnd)
		if _, ok := existingIDs[snapshot.SnapshotID]; ok {
			continue
		}
		if err := s.store.SaveAdvisorScoreSnapshot(ctx, snapshot); err != nil {
			return saved, fmt.Errorf("save Advisor score snapshot %s: %w", snapshot.SnapshotID, err)
		}
		existingIDs[snapshot.SnapshotID] = struct{}{}
		saved++
	}
	return saved, nil
}
