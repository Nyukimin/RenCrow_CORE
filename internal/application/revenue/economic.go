package revenue

import (
	"context"
	"fmt"
	"time"

	revenuedomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	workstreamdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type OpportunityStore interface {
	SaveOpportunity(ctx context.Context, item revenuedomain.Opportunity) error
}

type EconomicService struct {
	store OpportunityStore
	now   func() time.Time
}

func NewEconomicService(store OpportunityStore, now func() time.Time) *EconomicService {
	if now == nil {
		now = time.Now
	}
	return &EconomicService{store: store, now: now}
}

func (s *EconomicService) DraftOpportunity(ctx context.Context, item revenuedomain.Opportunity) (revenuedomain.Opportunity, error) {
	if s == nil {
		return revenuedomain.Opportunity{}, fmt.Errorf("economic service is required")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = s.now().UTC()
	}
	if item.ApprovalState == "" {
		item.ApprovalState = "draft"
	}
	item = revenuedomain.NormalizeOpportunityEconomics(item)
	if err := revenuedomain.ValidateOpportunity(item); err != nil {
		return revenuedomain.Opportunity{}, err
	}
	if s.store != nil {
		if err := s.store.SaveOpportunity(ctx, item); err != nil {
			return revenuedomain.Opportunity{}, err
		}
	}
	return item, nil
}

func GoalFromOpportunity(item revenuedomain.Opportunity, workstreamID string, now time.Time) (workstreamdomain.Goal, error) {
	item = revenuedomain.NormalizeOpportunityEconomics(item)
	if err := revenuedomain.ValidateOpportunity(item); err != nil {
		return workstreamdomain.Goal{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return workstreamdomain.Goal{
		GoalID:       "goal_" + item.OpportunityID,
		WorkstreamID: workstreamID,
		Title:        item.Title,
		Description:  item.Summary,
		SuccessCriteria: []string{
			"human approval is recorded before publish/send/billing",
			fmt.Sprintf("expected_profit >= %d", item.ExpectedProfit),
		},
		Verification: []string{
			"artifact exists",
			"approval gate checked",
			"revenue/reflection event recorded after delivery",
		},
		Status:    workstreamdomain.StatusDraft,
		CreatedAt: now.UTC(),
	}, nil
}
