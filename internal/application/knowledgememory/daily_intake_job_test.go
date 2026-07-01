package knowledgememory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/knowledgememory"
	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

type memoryDailyIntakeRuleStore struct {
	rules []domainkm.DailyIntakeRule
}

func (s memoryDailyIntakeRuleStore) ListDailyIntakeRules(_ context.Context, _ int) ([]domainkm.DailyIntakeRule, error) {
	return s.rules, nil
}

func TestRunDailyIntakeSweepEnablesReviewedURLRuleAndSweepsSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("daily intake fetched source text"))
	}))
	defer server.Close()

	l1, err := conversationpersistence.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	rules := memoryDailyIntakeRuleStore{rules: []domainkm.DailyIntakeRule{{
		RuleID:     "rule_ai",
		UserID:     "ren",
		Topic:      "AI news",
		SourceHint: server.URL,
		Cadence:    "daily",
		Status:     "reviewed",
		CreatedAt:  now,
	}}}

	result, err := RunDailyIntakeSweep(context.Background(), rules, l1, DailyIntakeSweepOptions{Now: now})
	if err != nil {
		t.Fatalf("RunDailyIntakeSweep failed: %v", err)
	}
	if result.RulesScanned != 1 || result.SourcesEnabled != 1 || result.RegistrySweep.Staged != 1 || result.RegistrySweep.PromotedKnowledge != 1 {
		t.Fatalf("result = %#v", result)
	}
	entries, err := l1.ListSourceRegistryEntries(context.Background(), true)
	if err != nil {
		t.Fatalf("ListSourceRegistryEntries failed: %v", err)
	}
	if len(entries) != 1 || entries[0].SourceID != "knowledge_memory:daily_intake_rule:rule_ai" || !entries[0].Enabled {
		t.Fatalf("enabled entries = %#v", entries)
	}
	if entries[0].Meta["daily_intake_enabled"] != true || entries[0].Meta["auto_fetch"] != true {
		t.Fatalf("entry meta = %#v", entries[0].Meta)
	}
}

func TestRunDailyIntakeSweepSkipsPendingOrNonURLRules(t *testing.T) {
	l1, err := conversationpersistence.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	rules := memoryDailyIntakeRuleStore{rules: []domainkm.DailyIntakeRule{
		{RuleID: "pending", UserID: "ren", Topic: "pending", SourceHint: "https://example.com/feed", Cadence: "daily", Status: "pending", CreatedAt: now},
		{RuleID: "non_url", UserID: "ren", Topic: "manual", SourceHint: "manual note", Cadence: "daily", Status: "reviewed", CreatedAt: now},
	}}

	result, err := RunDailyIntakeSweep(context.Background(), rules, l1, DailyIntakeSweepOptions{Now: now})
	if err != nil {
		t.Fatalf("RunDailyIntakeSweep failed: %v", err)
	}
	if result.RulesScanned != 2 || result.SourcesEnabled != 0 || result.SourcesSkipped != 2 || result.RegistrySweep.Sources != 0 {
		t.Fatalf("result = %#v", result)
	}
	entries, err := l1.ListSourceRegistryEntries(context.Background(), false)
	if err != nil {
		t.Fatalf("ListSourceRegistryEntries failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v", entries)
	}
}
