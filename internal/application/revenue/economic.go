package revenue

import (
	"context"
	"errors"
	"fmt"
	"time"

	revenuedomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	workstreamdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type OpportunityStore interface {
	SaveOpportunity(ctx context.Context, item revenuedomain.Opportunity) error
}

type EconomicStore interface {
	OpportunityStore
	ListOpportunities(ctx context.Context, limit int) ([]revenuedomain.Opportunity, error)
	SaveEconomicTask(ctx context.Context, item revenuedomain.EconomicTask) error
	ListEconomicTasks(ctx context.Context, limit int) ([]revenuedomain.EconomicTask, error)
	SaveEconomicReflection(ctx context.Context, item revenuedomain.EconomicReflection) error
	ListEconomicReflections(ctx context.Context, limit int) ([]revenuedomain.EconomicReflection, error)
	ListRevenueEvents(ctx context.Context, limit int) ([]revenuedomain.RevenueEvent, error)
}

type WorkstreamGoalStore interface {
	SaveGoal(ctx context.Context, item workstreamdomain.Goal) error
}

var (
	ErrOpportunityNotFound  = errors.New("opportunity not found")
	ErrRevenueEventNotFound = errors.New("revenue event not found")
)

type EconomicService struct {
	store           OpportunityStore
	workstreamGoals WorkstreamGoalStore
	now             func() time.Time
}

func (s *EconomicService) WithWorkstreamGoalStore(store WorkstreamGoalStore) *EconomicService {
	if s != nil {
		s.workstreamGoals = store
	}
	return s
}

func NewEconomicService(store OpportunityStore, now func() time.Time) *EconomicService {
	if now == nil {
		now = time.Now
	}
	return &EconomicService{store: store, now: now}
}

func (s *EconomicService) DraftEconomicTask(ctx context.Context, item revenuedomain.EconomicTask) (revenuedomain.EconomicTask, error) {
	if s == nil {
		return revenuedomain.EconomicTask{}, fmt.Errorf("economic store is required")
	}
	store, ok := s.store.(EconomicStore)
	if !ok {
		return revenuedomain.EconomicTask{}, fmt.Errorf("economic store is required")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = s.now().UTC()
	}
	if item.Status == "" {
		item.Status = "draft"
	}
	if item.ApprovalMode == "" && !revenuedomain.RequiresHumanApproval(item.TaskKind) {
		item.ApprovalMode = "none"
	}
	if err := revenuedomain.ValidateEconomicTask(item); err != nil {
		return revenuedomain.EconomicTask{}, err
	}
	if err := store.SaveEconomicTask(ctx, item); err != nil {
		return revenuedomain.EconomicTask{}, err
	}
	return item, nil
}

func (s *EconomicService) DraftReflection(ctx context.Context, item revenuedomain.EconomicReflection) (revenuedomain.EconomicReflection, error) {
	if s == nil {
		return revenuedomain.EconomicReflection{}, fmt.Errorf("economic store is required")
	}
	store, ok := s.store.(EconomicStore)
	if !ok {
		return revenuedomain.EconomicReflection{}, fmt.Errorf("economic store is required")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = s.now().UTC()
	}
	if err := revenuedomain.ValidateEconomicReflection(item); err != nil {
		return revenuedomain.EconomicReflection{}, err
	}
	if err := store.SaveEconomicReflection(ctx, item); err != nil {
		return revenuedomain.EconomicReflection{}, err
	}
	return item, nil
}

func (s *EconomicService) CreateWorkstreamGoal(ctx context.Context, opportunityID, workstreamID string) (workstreamdomain.Goal, error) {
	if s == nil {
		return workstreamdomain.Goal{}, fmt.Errorf("economic store is required")
	}
	store, ok := s.store.(EconomicStore)
	if !ok {
		return workstreamdomain.Goal{}, fmt.Errorf("economic store is required")
	}
	if s.workstreamGoals == nil {
		return workstreamdomain.Goal{}, fmt.Errorf("workstream goal store is required")
	}
	opportunity, err := findOpportunity(ctx, store, opportunityID)
	if err != nil {
		return workstreamdomain.Goal{}, err
	}
	goal, err := GoalFromOpportunity(opportunity, workstreamID, s.now().UTC())
	if err != nil {
		return workstreamdomain.Goal{}, err
	}
	if err := s.workstreamGoals.SaveGoal(ctx, goal); err != nil {
		return workstreamdomain.Goal{}, err
	}
	return goal, nil
}

type ReflectionFromRevenueEventRequest struct {
	ReflectionID   string
	OpportunityID  string
	RevenueEventID string
	Outcome        string
	Lessons        []string
	NextActions    []string
}

func (s *EconomicService) ReflectRevenueEvent(ctx context.Context, req ReflectionFromRevenueEventRequest) (revenuedomain.EconomicReflection, error) {
	if s == nil {
		return revenuedomain.EconomicReflection{}, fmt.Errorf("economic store is required")
	}
	store, ok := s.store.(EconomicStore)
	if !ok {
		return revenuedomain.EconomicReflection{}, fmt.Errorf("economic store is required")
	}
	opportunity, err := findOpportunity(ctx, store, req.OpportunityID)
	if err != nil {
		return revenuedomain.EconomicReflection{}, err
	}
	events, err := store.ListRevenueEvents(ctx, 1000)
	if err != nil {
		return revenuedomain.EconomicReflection{}, err
	}
	var event *revenuedomain.RevenueEvent
	for i := range events {
		if events[i].EventID == req.RevenueEventID {
			event = &events[i]
			break
		}
	}
	if event == nil {
		return revenuedomain.EconomicReflection{}, ErrRevenueEventNotFound
	}
	return s.DraftReflection(ctx, revenuedomain.EconomicReflection{
		ReflectionID: req.ReflectionID, OpportunityID: opportunity.OpportunityID, RevenueEventID: event.EventID,
		Outcome: req.Outcome, NetProfit: event.Amount - opportunity.ExpectedCost,
		Lessons: append([]string(nil), req.Lessons...), NextActions: append([]string(nil), req.NextActions...), CreatedAt: s.now().UTC(),
	})
}

func findOpportunity(ctx context.Context, store EconomicStore, opportunityID string) (revenuedomain.Opportunity, error) {
	opportunities, err := store.ListOpportunities(ctx, 1000)
	if err != nil {
		return revenuedomain.Opportunity{}, err
	}
	for _, item := range opportunities {
		if item.OpportunityID == opportunityID {
			return item, nil
		}
	}
	return revenuedomain.Opportunity{}, ErrOpportunityNotFound
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
