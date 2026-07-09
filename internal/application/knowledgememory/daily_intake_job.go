package knowledgememory

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

const SourceKindSearchFallback = "search_fallback"

type DailyIntakeRuleStore interface {
	ListDailyIntakeRules(ctx context.Context, limit int) ([]domainkm.DailyIntakeRule, error)
}

type DailyIntakeRegistryStore interface {
	SaveSourceRegistryEntry(ctx context.Context, entry SourceRegistryEntry) (*SourceRegistryEntry, error)
	SweepDueSources(ctx context.Context, now time.Time, opts SourceRegistrySweepOptions) (SourceRegistrySweepResult, error)
}

type SourceRegistryEntry struct {
	SourceID      string
	URL           string
	Kind          string
	TrustScore    float64
	FetchInterval time.Duration
	LicenseNote   string
	Enabled       bool
	Meta          map[string]interface{}
}

type DailyIntakeSweepOptions struct {
	RuleLimit         int
	SourceLimit       int
	MinimumTrustScore float64
	Now               time.Time
}

type SourceRegistrySweepOptions struct {
	LimitPerSource    int
	MinimumTrustScore float64
}

type SourceRegistrySweepResult struct {
	Sources           int
	Staged            int
	Warnings          int
	Validated         int
	PromotedNews      int
	PromotedKnowledge int
	Failed            int
}

type DailyIntakeSweepResult struct {
	RulesScanned       int
	SourcesEnabled     int
	SourcesSkipped     int
	RegistrySweep      SourceRegistrySweepResult
	RegistrySweepError string
}

func RunDailyIntakeSweep(ctx context.Context, rules DailyIntakeRuleStore, registry DailyIntakeRegistryStore, opts DailyIntakeSweepOptions) (DailyIntakeSweepResult, error) {
	if rules == nil {
		return DailyIntakeSweepResult{}, fmt.Errorf("daily intake rule store is nil")
	}
	if registry == nil {
		return DailyIntakeSweepResult{}, fmt.Errorf("daily intake registry store is nil")
	}
	if opts.RuleLimit <= 0 {
		opts.RuleLimit = 100
	}
	if opts.SourceLimit <= 0 {
		opts.SourceLimit = 10
	}
	if opts.MinimumTrustScore <= 0 {
		opts.MinimumTrustScore = 0.5
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	items, err := rules.ListDailyIntakeRules(ctx, opts.RuleLimit)
	if err != nil {
		return DailyIntakeSweepResult{}, err
	}
	result := DailyIntakeSweepResult{RulesScanned: len(items)}
	for _, rule := range items {
		if !dailyIntakeRuleReady(rule) || !isDailyIntakeHTTPURL(rule.SourceHint) {
			result.SourcesSkipped++
			continue
		}
		if _, err := registry.SaveSourceRegistryEntry(ctx, dailyIntakeSourceRegistryEntry(rule, now)); err != nil {
			return result, err
		}
		result.SourcesEnabled++
	}
	sweep, err := registry.SweepDueSources(ctx, now, SourceRegistrySweepOptions{
		LimitPerSource:    opts.SourceLimit,
		MinimumTrustScore: opts.MinimumTrustScore,
	})
	result.RegistrySweep = sweep
	if err != nil {
		result.RegistrySweepError = err.Error()
	}
	return result, err
}

func dailyIntakeRuleReady(rule domainkm.DailyIntakeRule) bool {
	switch strings.ToLower(strings.TrimSpace(rule.Status)) {
	case "active", "reviewed", "enabled":
		return true
	default:
		return false
	}
}

func dailyIntakeSourceRegistryEntry(rule domainkm.DailyIntakeRule, now time.Time) SourceRegistryEntry {
	interval := dailyIntakeFetchInterval(rule.Cadence)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return SourceRegistryEntry{
		SourceID:      "knowledge_memory:daily_intake_rule:" + strings.TrimSpace(rule.RuleID),
		URL:           strings.TrimSpace(rule.SourceHint),
		Kind:          SourceKindSearchFallback,
		TrustScore:    0.55,
		FetchInterval: interval,
		LicenseNote:   "daily intake rule reviewed source; fetch to staging before promote",
		Enabled:       true,
		Meta: map[string]interface{}{
			"source_kind":           "knowledge_memory",
			"source_type":           "daily_intake_rule",
			"source_name":           rule.Topic,
			"user_id":               rule.UserID,
			"cadence":               rule.Cadence,
			"review_required":       true,
			"auto_fetch":            true,
			"created_from_l1_stage": true,
			"daily_intake_enabled":  true,
			"updated_by_job_at":     now.UTC().Format(time.RFC3339Nano),
		},
	}
}

func dailyIntakeFetchInterval(cadence string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(cadence)) {
	case "hourly":
		return time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	case "monthly":
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func isDailyIntakeHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}
