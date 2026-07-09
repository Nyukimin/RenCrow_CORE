package knowledgememory

import (
	"context"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

type memoryDailyIntakeRuleStore struct {
	rules []domainkm.DailyIntakeRule
}

func (s memoryDailyIntakeRuleStore) ListDailyIntakeRules(_ context.Context, _ int) ([]domainkm.DailyIntakeRule, error) {
	return s.rules, nil
}

type memoryDailyIntakeRegistryStore struct {
	entries     []SourceRegistryEntry
	sweepOpts   SourceRegistrySweepOptions
	sweepResult SourceRegistrySweepResult
}

func (s *memoryDailyIntakeRegistryStore) SaveSourceRegistryEntry(_ context.Context, entry SourceRegistryEntry) (*SourceRegistryEntry, error) {
	s.entries = append(s.entries, entry)
	return &entry, nil
}

func (s *memoryDailyIntakeRegistryStore) SweepDueSources(_ context.Context, _ time.Time, opts SourceRegistrySweepOptions) (SourceRegistrySweepResult, error) {
	s.sweepOpts = opts
	return s.sweepResult, nil
}

func TestRunDailyIntakeSweepEnablesReviewedURLRuleAndSweepsSource(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	rules := memoryDailyIntakeRuleStore{rules: []domainkm.DailyIntakeRule{{
		RuleID:     "rule_ai",
		UserID:     "ren",
		Topic:      "AI news",
		SourceHint: "https://example.com/daily-ai",
		Cadence:    "daily",
		Status:     "reviewed",
		CreatedAt:  now,
	}}}
	registry := &memoryDailyIntakeRegistryStore{
		sweepResult: SourceRegistrySweepResult{Sources: 1, Staged: 1, PromotedKnowledge: 1},
	}

	result, err := RunDailyIntakeSweep(context.Background(), rules, registry, DailyIntakeSweepOptions{Now: now})
	if err != nil {
		t.Fatalf("RunDailyIntakeSweep failed: %v", err)
	}
	if result.RulesScanned != 1 || result.SourcesEnabled != 1 || result.RegistrySweep.Staged != 1 || result.RegistrySweep.PromotedKnowledge != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(registry.entries) != 1 || registry.entries[0].SourceID != "knowledge_memory:daily_intake_rule:rule_ai" || !registry.entries[0].Enabled {
		t.Fatalf("enabled entries = %#v", registry.entries)
	}
	if registry.entries[0].Meta["daily_intake_enabled"] != true || registry.entries[0].Meta["auto_fetch"] != true {
		t.Fatalf("entry meta = %#v", registry.entries[0].Meta)
	}
	if registry.sweepOpts.LimitPerSource != 10 || registry.sweepOpts.MinimumTrustScore != 0.5 {
		t.Fatalf("sweep opts = %#v", registry.sweepOpts)
	}
}

func TestRunDailyIntakeSweepSkipsPendingOrNonURLRules(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	rules := memoryDailyIntakeRuleStore{rules: []domainkm.DailyIntakeRule{
		{RuleID: "pending", UserID: "ren", Topic: "pending", SourceHint: "https://example.com/feed", Cadence: "daily", Status: "pending", CreatedAt: now},
		{RuleID: "non_url", UserID: "ren", Topic: "manual", SourceHint: "manual note", Cadence: "daily", Status: "reviewed", CreatedAt: now},
	}}
	registry := &memoryDailyIntakeRegistryStore{}

	result, err := RunDailyIntakeSweep(context.Background(), rules, registry, DailyIntakeSweepOptions{Now: now})
	if err != nil {
		t.Fatalf("RunDailyIntakeSweep failed: %v", err)
	}
	if result.RulesScanned != 2 || result.SourcesEnabled != 0 || result.SourcesSkipped != 2 || result.RegistrySweep.Sources != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(registry.entries) != 0 {
		t.Fatalf("entries = %#v", registry.entries)
	}
}
