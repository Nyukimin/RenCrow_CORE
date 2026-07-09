package revenue

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

const (
	AgentRevenue             = "RevenueAgent"
	ModeDraftReportOnly      = "draft_report_only"
	DefaultDailyRoutineLimit = 50
	MaxDailyRoutineLimit     = 200
)

type DailyRoutineStore interface {
	ListMarketResearchItems(ctx context.Context, limit int) ([]domainrevenue.MarketResearchItem, error)
	ListSNSPostMetrics(ctx context.Context, limit int) ([]domainrevenue.SNSPostMetric, error)
	ListProducts(ctx context.Context, limit int) ([]domainrevenue.Product, error)
	ListCustomerVoices(ctx context.Context, limit int) ([]domainrevenue.CustomerVoice, error)
	ListRevenueEvents(ctx context.Context, limit int) ([]domainrevenue.RevenueEvent, error)
	ListHumanDecisionGateRecords(ctx context.Context, limit int) ([]domainrevenue.HumanDecisionGateRecord, error)
	SaveDailyRoutineReport(ctx context.Context, item domainrevenue.DailyRoutineReport) error
}

type DailyRoutineService struct {
	store DailyRoutineStore
}

type DailyRoutineRequest struct {
	ReportID     string
	WorkstreamID string
	Date         string
	Limit        int
	Now          time.Time
}

type DailyRoutineResult struct {
	Agent                                   string                           `json:"agent"`
	Mode                                    string                           `json:"mode"`
	Report                                  domainrevenue.DailyRoutineReport `json:"daily_routine_report"`
	ExternalActionsApplied                  bool                             `json:"external_actions_applied"`
	HumanApprovalRequiredForExternalActions bool                             `json:"human_approval_required_for_external_actions"`
}

func NewDailyRoutineService(store DailyRoutineStore) *DailyRoutineService {
	return &DailyRoutineService{store: store}
}

func (s *DailyRoutineService) RunDailyRoutine(ctx context.Context, req DailyRoutineRequest) (DailyRoutineResult, error) {
	if s == nil || s.store == nil {
		return DailyRoutineResult{}, errors.New("revenue daily routine store unavailable")
	}
	limit, err := NormalizeDailyRoutineLimit(req.Limit)
	if err != nil {
		return DailyRoutineResult{}, err
	}
	market, err := s.store.ListMarketResearchItems(ctx, limit)
	if err != nil {
		return DailyRoutineResult{}, fmt.Errorf("failed to load market research items: %w", err)
	}
	posts, err := s.store.ListSNSPostMetrics(ctx, limit)
	if err != nil {
		return DailyRoutineResult{}, fmt.Errorf("failed to load sns post metrics: %w", err)
	}
	products, err := s.store.ListProducts(ctx, limit)
	if err != nil {
		return DailyRoutineResult{}, fmt.Errorf("failed to load products: %w", err)
	}
	voices, err := s.store.ListCustomerVoices(ctx, limit)
	if err != nil {
		return DailyRoutineResult{}, fmt.Errorf("failed to load customer voices: %w", err)
	}
	events, err := s.store.ListRevenueEvents(ctx, limit)
	if err != nil {
		return DailyRoutineResult{}, fmt.Errorf("failed to load revenue events: %w", err)
	}
	decisions, err := s.store.ListHumanDecisionGateRecords(ctx, limit)
	if err != nil {
		return DailyRoutineResult{}, fmt.Errorf("failed to load human decision gate records: %w", err)
	}
	report := domainrevenue.BuildDailyRoutineReport(domainrevenue.DailyRoutineInput{
		ReportID:       req.ReportID,
		WorkstreamID:   req.WorkstreamID,
		Date:           req.Date,
		Now:            req.Now,
		MarketResearch: market,
		SNSPosts:       posts,
		Products:       products,
		CustomerVoices: voices,
		RevenueEvents:  events,
		Decisions:      decisions,
	})
	if err := domainrevenue.ValidateDailyRoutineReport(report); err != nil {
		return DailyRoutineResult{}, err
	}
	if err := s.store.SaveDailyRoutineReport(ctx, report); err != nil {
		return DailyRoutineResult{}, err
	}
	return DailyRoutineResult{
		Agent:                                   AgentRevenue,
		Mode:                                    ModeDraftReportOnly,
		Report:                                  report,
		ExternalActionsApplied:                  false,
		HumanApprovalRequiredForExternalActions: true,
	}, nil
}

func NormalizeDailyRoutineLimit(limit int) (int, error) {
	if limit <= 0 {
		return DefaultDailyRoutineLimit, nil
	}
	if limit > MaxDailyRoutineLimit {
		return 0, fmt.Errorf("limit must be <= %d", MaxDailyRoutineLimit)
	}
	return limit, nil
}
