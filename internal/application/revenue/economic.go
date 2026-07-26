package revenue

import (
	"context"
	"errors"
	"fmt"
	"time"

	revenuedomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	workstreamdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	SaveDelivery(ctx context.Context, item revenuedomain.Delivery) error
	ListDeliveries(ctx context.Context, limit int) ([]revenuedomain.Delivery, error)
}

type WorkstreamGoalStore interface {
	SaveGoal(ctx context.Context, item workstreamdomain.Goal) error
}

type EconomicApprovalStore interface {
	SaveHumanDecisionGateRecord(ctx context.Context, item revenuedomain.HumanDecisionGateRecord) error
}

type WorkstreamEconomicStore interface {
	WorkstreamGoalStore
	SaveArtifact(ctx context.Context, item workstreamdomain.Artifact) error
}

var (
	ErrOpportunityNotFound  = errors.New("opportunity not found")
	ErrRevenueEventNotFound = errors.New("revenue event not found")
)

type EconomicService struct {
	store           OpportunityStore
	workstreamGoals WorkstreamGoalStore
	now             func() time.Time
	newTraceID      func() string
}

type OpportunityWorkstreamChain struct {
	Goal     workstreamdomain.Goal
	Artifact workstreamdomain.Artifact
	Approval revenuedomain.HumanDecisionGateRecord
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
	return &EconomicService{
		store: store,
		now:   now,
		newTraceID: func() string {
			return string(modulecore.NewTraceID())
		},
	}
}

func (s *EconomicService) WithTraceIDGenerator(generate func() string) *EconomicService {
	if s != nil && generate != nil {
		s.newTraceID = generate
	}
	return s
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
	opportunity, err := findOpportunity(ctx, store, item.OpportunityID)
	if err != nil {
		return revenuedomain.EconomicTask{}, err
	}
	if item.TraceID == "" {
		item.TraceID = opportunity.TraceID
	} else if opportunity.TraceID != "" && item.TraceID != opportunity.TraceID {
		return revenuedomain.EconomicTask{}, fmt.Errorf("trace_id must match opportunity")
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
	opportunity, err := findOpportunity(ctx, store, item.OpportunityID)
	if err != nil {
		return revenuedomain.EconomicReflection{}, err
	}
	if item.TraceID == "" {
		item.TraceID = opportunity.TraceID
	} else if opportunity.TraceID != "" && item.TraceID != opportunity.TraceID {
		return revenuedomain.EconomicReflection{}, fmt.Errorf("trace_id must match opportunity")
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
	chain, err := s.CreateOpportunityWorkstreamChain(ctx, opportunityID, workstreamID)
	if err != nil {
		return workstreamdomain.Goal{}, err
	}
	return chain.Goal, nil
}

func (s *EconomicService) CreateOpportunityWorkstreamChain(ctx context.Context, opportunityID, workstreamID string) (OpportunityWorkstreamChain, error) {
	if s == nil {
		return OpportunityWorkstreamChain{}, fmt.Errorf("economic store is required")
	}
	store, ok := s.store.(EconomicStore)
	if !ok {
		return OpportunityWorkstreamChain{}, fmt.Errorf("economic store is required")
	}
	if s.workstreamGoals == nil {
		return OpportunityWorkstreamChain{}, fmt.Errorf("workstream goal store is required")
	}
	workstreamStore, ok := s.workstreamGoals.(WorkstreamEconomicStore)
	if !ok {
		return OpportunityWorkstreamChain{}, fmt.Errorf("workstream artifact store is required")
	}
	approvalStore, ok := s.store.(EconomicApprovalStore)
	if !ok {
		return OpportunityWorkstreamChain{}, fmt.Errorf("economic approval store is required")
	}
	opportunity, err := findOpportunity(ctx, store, opportunityID)
	if err != nil {
		return OpportunityWorkstreamChain{}, err
	}
	if opportunity.TraceID == "" {
		opportunity.TraceID = s.newTraceID()
		if err := store.SaveOpportunity(ctx, opportunity); err != nil {
			return OpportunityWorkstreamChain{}, err
		}
	}
	now := s.now().UTC()
	goal, err := GoalFromOpportunity(opportunity, workstreamID, now)
	if err != nil {
		return OpportunityWorkstreamChain{}, err
	}
	artifact := ArtifactFromOpportunity(opportunity, workstreamID, now)
	if err := workstreamdomain.ValidateArtifact(artifact); err != nil {
		return OpportunityWorkstreamChain{}, err
	}
	approval := ApprovalFromOpportunityArtifact(opportunity, artifact, now)
	if err := revenuedomain.ValidateHumanDecisionGateRecord(approval); err != nil {
		return OpportunityWorkstreamChain{}, err
	}
	if err := s.workstreamGoals.SaveGoal(ctx, goal); err != nil {
		return OpportunityWorkstreamChain{}, err
	}
	if err := workstreamStore.SaveArtifact(ctx, artifact); err != nil {
		return OpportunityWorkstreamChain{}, err
	}
	if err := approvalStore.SaveHumanDecisionGateRecord(ctx, approval); err != nil {
		return OpportunityWorkstreamChain{}, err
	}
	return OpportunityWorkstreamChain{Goal: goal, Artifact: artifact, Approval: approval}, nil
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
		ReflectionID: req.ReflectionID, TraceID: firstNonEmptyTraceID(event.TraceID, opportunity.TraceID), OpportunityID: opportunity.OpportunityID, RevenueEventID: event.EventID,
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
	if item.TraceID == "" {
		item.TraceID = s.newTraceID()
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

func (s *EconomicService) RecordDelivery(ctx context.Context, item revenuedomain.Delivery) (revenuedomain.Delivery, error) {
	if s == nil {
		return revenuedomain.Delivery{}, fmt.Errorf("economic store is required")
	}
	store, ok := s.store.(EconomicStore)
	if !ok {
		return revenuedomain.Delivery{}, fmt.Errorf("economic store is required")
	}
	if item.OpportunityID != "" {
		opportunity, err := findOpportunity(ctx, store, item.OpportunityID)
		if err != nil {
			return revenuedomain.Delivery{}, err
		}
		if item.TraceID == "" {
			item.TraceID = opportunity.TraceID
		} else if opportunity.TraceID != "" && item.TraceID != opportunity.TraceID {
			return revenuedomain.Delivery{}, fmt.Errorf("trace_id must match opportunity")
		}
	}
	if item.TraceID == "" {
		item.TraceID = s.newTraceID()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = s.now().UTC()
	}
	if err := revenuedomain.ValidateDelivery(item); err != nil {
		return revenuedomain.Delivery{}, err
	}
	if err := store.SaveDelivery(ctx, item); err != nil {
		return revenuedomain.Delivery{}, err
	}
	return item, nil
}

func firstNonEmptyTraceID(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
		TraceID:      item.TraceID,
		WorkstreamID: workstreamID,
		Title:        item.Title,
		Description:  item.Summary,
		SuccessCriteria: []string{
			"execution policy decision is recorded before publish/send/billing",
			fmt.Sprintf("expected_profit >= %d", item.ExpectedProfit),
		},
		Verification: []string{
			"artifact exists",
			"execution policy checked",
			"revenue/reflection event recorded after delivery",
		},
		Status:    workstreamdomain.StatusDraft,
		CreatedAt: now.UTC(),
	}, nil
}

func ArtifactFromOpportunity(item revenuedomain.Opportunity, workstreamID string, now time.Time) workstreamdomain.Artifact {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return workstreamdomain.Artifact{
		ArtifactID:   "artifact_" + item.OpportunityID,
		TraceID:      item.TraceID,
		WorkstreamID: workstreamID,
		Type:         "economic_opportunity_brief",
		Title:        item.Title,
		Status:       "pending_review",
		CreatedAt:    now.UTC(),
	}
}

func ApprovalFromOpportunityArtifact(item revenuedomain.Opportunity, artifact workstreamdomain.Artifact, now time.Time) revenuedomain.HumanDecisionGateRecord {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return revenuedomain.BuildHumanDecisionGateRecord(revenuedomain.HumanDecisionGateRequest{
		DecisionID:     "approval_" + item.OpportunityID,
		TraceID:        item.TraceID,
		DecisionType:   "economic_opportunity_execution",
		SubjectID:      artifact.ArtifactID,
		Description:    "Evaluate the economic opportunity artifact against the execution policy",
		ApprovalStatus: "not_required",
		CreatedAt:      now.UTC(),
	})
}
