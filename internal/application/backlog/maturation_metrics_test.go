package backlog

import (
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

func TestCalculateMaturationMetricsProjectsCanonicalHistory(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	items := []domainbacklog.Item{
		{ItemID: "a", CreatedAt: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339), MaturationState: domainbacklog.MaturationStatePromoted, RevalidationRecords: []domainbacklog.RevalidationRecord{{Decision: domainbacklog.RevalidationDecisionPromote, MaturationDays: 8}}},
		{ItemID: "b", CreatedAt: now.Add(-40 * 24 * time.Hour).Format(time.RFC3339), MaturationState: domainbacklog.MaturationStateDropped, RevalidationRecords: []domainbacklog.RevalidationRecord{{Decision: domainbacklog.RevalidationDecisionDrop, MaturationDays: 12}}},
	}
	metrics := calculateMaturationMetrics(items, now, 30)
	if metrics.CreatedInWindow != 1 || metrics.DecisionCount != 2 || metrics.PromotedCount != 1 || metrics.DroppedCount != 1 {
		t.Fatalf("unexpected counters: %+v", metrics)
	}
	if metrics.PromotionRate != 0.5 || metrics.DropRate != 0.5 || metrics.AverageMaturationDays != 10 || metrics.BacklogGrowth != 1 {
		t.Fatalf("unexpected rates: %+v", metrics)
	}
}

func TestCalculateMaturationMetricsAverageUsesFinalDecisionNotIntermediateHold(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	items := []domainbacklog.Item{{
		ItemID: "held-then-promoted", CreatedAt: now.Add(-12 * 24 * time.Hour).Format(time.RFC3339),
		MaturationState: domainbacklog.MaturationStatePromoted,
		RevalidationRecords: []domainbacklog.RevalidationRecord{
			{Decision: domainbacklog.RevalidationDecisionHold, MaturationDays: 7},
			{Decision: domainbacklog.RevalidationDecisionPromote, MaturationDays: 12},
		},
	}, {
		ItemID: "still-held", CreatedAt: now.Add(-9 * 24 * time.Hour).Format(time.RFC3339),
		MaturationState:     domainbacklog.MaturationStateHold,
		RevalidationRecords: []domainbacklog.RevalidationRecord{{Decision: domainbacklog.RevalidationDecisionHold, MaturationDays: 9}},
	}}
	metrics := calculateMaturationMetrics(items, now, 30)
	if metrics.DecisionCount != 3 || metrics.HeldCount != 2 || metrics.PromotedCount != 1 {
		t.Fatalf("decision rates lost history: %+v", metrics)
	}
	if metrics.AverageMaturationDays != 12 {
		t.Fatalf("average included non-final HOLD decisions: %+v", metrics)
	}
}
