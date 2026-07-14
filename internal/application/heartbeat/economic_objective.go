package heartbeat

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	revenueapp "github.com/Nyukimin/RenCrow_CORE/internal/application/revenue"
	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

const economicObjectiveSourceLimit = 50

type EconomicObjectiveDiscoveryStore interface {
	ListMarketResearchItems(ctx context.Context, limit int) ([]domainrevenue.MarketResearchItem, error)
	ListProducts(ctx context.Context, limit int) ([]domainrevenue.Product, error)
	ListCustomerVoices(ctx context.Context, limit int) ([]domainrevenue.CustomerVoice, error)
	ListRevenueEvents(ctx context.Context, limit int) ([]domainrevenue.RevenueEvent, error)
	ListOpportunities(ctx context.Context, limit int) ([]domainrevenue.Opportunity, error)
	SaveOpportunity(ctx context.Context, item domainrevenue.Opportunity) error
}

type EconomicObjectiveGoalStore interface {
	ListGoals(ctx context.Context, limit int) ([]domainworkstream.Goal, error)
}

type EconomicObjectiveDiscoveryOptions struct {
	Enabled                   bool
	DraftOnly                 bool
	HeartbeatDiscoveryEnabled bool
	DailyOpportunityLimit     int
}

type EconomicObjectiveDiscoveryReport struct {
	Status                 string   `json:"status"`
	Checked                int      `json:"checked"`
	Created                int      `json:"created"`
	DuplicateSkipped       int      `json:"duplicate_skipped"`
	Failed                 int      `json:"failed"`
	OpportunityIDs         []string `json:"opportunity_ids,omitempty"`
	Reason                 string   `json:"reason,omitempty"`
	DraftOnly              bool     `json:"draft_only"`
	ExternalActionsApplied bool     `json:"external_actions_applied"`
}

type EconomicObjectiveDiscoveryService struct {
	store     EconomicObjectiveDiscoveryStore
	goalStore EconomicObjectiveGoalStore
	options   EconomicObjectiveDiscoveryOptions
}

func NewEconomicObjectiveDiscoveryService(store EconomicObjectiveDiscoveryStore, goalStore EconomicObjectiveGoalStore, options EconomicObjectiveDiscoveryOptions) *EconomicObjectiveDiscoveryService {
	return &EconomicObjectiveDiscoveryService{store: store, goalStore: goalStore, options: options}
}

func (s *EconomicObjectiveDiscoveryService) Run(ctx context.Context, now time.Time) (EconomicObjectiveDiscoveryReport, error) {
	report := EconomicObjectiveDiscoveryReport{
		Status: "skipped", DraftOnly: s != nil && s.options.DraftOnly, ExternalActionsApplied: false,
	}
	if s == nil || !s.options.Enabled {
		report.Reason = "economic objective disabled"
		return report, nil
	}
	if !s.options.DraftOnly {
		report.Reason = "draft_only is required"
		return report, nil
	}
	if !s.options.HeartbeatDiscoveryEnabled {
		report.Reason = "heartbeat discovery disabled"
		return report, nil
	}
	if s.store == nil {
		report.Status = "unavailable"
		report.Reason = "economic discovery store unavailable"
		return report, nil
	}
	limit := s.options.DailyOpportunityLimit
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		return report, fmt.Errorf("daily opportunity limit must be <= 50")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	existing, err := s.store.ListOpportunities(ctx, 1000)
	if err != nil {
		return report, fmt.Errorf("list economic opportunities: %w", err)
	}
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		seen[strings.TrimSpace(item.OpportunityID)] = struct{}{}
	}
	candidates, err := s.loadCandidates(ctx, now)
	if err != nil {
		return report, err
	}
	service := revenueapp.NewEconomicService(s.store, func() time.Time { return now })
	report.Status = "ok"
	for _, candidate := range candidates {
		if report.Created >= limit {
			break
		}
		report.Checked++
		if _, ok := seen[candidate.OpportunityID]; ok {
			report.DuplicateSkipped++
			continue
		}
		created, err := service.DraftOpportunity(ctx, candidate)
		if err != nil {
			report.Failed++
			continue
		}
		seen[created.OpportunityID] = struct{}{}
		report.Created++
		report.OpportunityIDs = append(report.OpportunityIDs, created.OpportunityID)
	}
	if report.Failed > 0 {
		report.Status = "warning"
	}
	return report, nil
}

func (s *EconomicObjectiveDiscoveryService) loadCandidates(ctx context.Context, now time.Time) ([]domainrevenue.Opportunity, error) {
	market, err := s.store.ListMarketResearchItems(ctx, economicObjectiveSourceLimit)
	if err != nil {
		return nil, fmt.Errorf("list market research for economic discovery: %w", err)
	}
	products, err := s.store.ListProducts(ctx, economicObjectiveSourceLimit)
	if err != nil {
		return nil, fmt.Errorf("list products for economic discovery: %w", err)
	}
	voices, err := s.store.ListCustomerVoices(ctx, economicObjectiveSourceLimit)
	if err != nil {
		return nil, fmt.Errorf("list customer voices for economic discovery: %w", err)
	}
	events, err := s.store.ListRevenueEvents(ctx, economicObjectiveSourceLimit)
	if err != nil {
		return nil, fmt.Errorf("list revenue events for economic discovery: %w", err)
	}
	var goals []domainworkstream.Goal
	if s.goalStore != nil {
		goals, err = s.goalStore.ListGoals(ctx, economicObjectiveSourceLimit)
		if err != nil {
			return nil, fmt.Errorf("list workstream goals for economic discovery: %w", err)
		}
	}
	candidates := make([]domainrevenue.Opportunity, 0, len(market)+len(products)+len(voices)+len(events)+len(goals))
	for _, item := range market {
		if id := economicOpportunityID("market", item.ItemID); id != "" {
			title := firstNonEmpty(item.Theme, item.ProductName, "Market research opportunity")
			candidates = append(candidates, newDraftOpportunity(id, "market_research", title, item.ObservedSignal, "", item.Price, now))
		}
	}
	for _, item := range products {
		if id := economicOpportunityID("product", item.ProductID); id != "" {
			candidates = append(candidates, newDraftOpportunity(id, "product", firstNonEmpty(item.ProductName, "Product opportunity"), item.Promise, item.Target, item.Price, now))
		}
	}
	for _, item := range voices {
		if id := economicOpportunityID("voice", item.VoiceID); id != "" {
			// RawText and customer identifiers intentionally stay outside the generated draft.
			candidates = append(candidates, newDraftOpportunity(id, "customer_voice", "Customer voice opportunity", item.Summary, "", 0, now))
		}
	}
	for _, item := range events {
		if id := economicOpportunityID("event", item.EventID); id != "" {
			title := firstNonEmpty(item.EventType, "Revenue event opportunity")
			candidates = append(candidates, newDraftOpportunity(id, "revenue_event", title, item.Notes, "", item.Amount, now))
		}
	}
	for _, item := range goals {
		if id := economicOpportunityID("goal", item.GoalID); id != "" {
			candidates = append(candidates, newDraftOpportunity(id, "workstream_goal", firstNonEmpty(item.Title, "Workstream goal opportunity"), item.Description, "", 0, now))
		}
	}
	return candidates, nil
}

func newDraftOpportunity(id, sourceKind, title, summary, target string, expectedRevenue int, now time.Time) domainrevenue.Opportunity {
	if expectedRevenue < 0 {
		expectedRevenue = 0
	}
	return domainrevenue.Opportunity{
		OpportunityID: id, SourceKind: sourceKind, Title: strings.TrimSpace(title), Summary: strings.TrimSpace(summary),
		TargetCustomer: strings.TrimSpace(target), ExpectedRevenue: expectedRevenue, ReuseValue: 0.5,
		AutomationRate: 0.25, StrategicValue: 0.5, RiskScore: 0.25, ApprovalState: "draft", CreatedAt: now,
	}
}

func economicOpportunityID(prefix, sourceID string) string {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			return unicode.ToLower(r)
		default:
			return '_'
		}
	}, sourceID)
	cleaned = strings.Trim(strings.Join(strings.FieldsFunc(cleaned, func(r rune) bool { return r == '_' }), "_"), "_")
	if cleaned == "" {
		return ""
	}
	if len(cleaned) > 80 {
		cleaned = cleaned[:80]
	}
	return "opp_" + prefix + "_" + cleaned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
