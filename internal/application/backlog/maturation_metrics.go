package backlog

import (
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

// MaturationMetrics is a deterministic projection over the canonical Item
// history. It does not create a second counter store or independently mutable
// source of truth.
type MaturationMetrics struct {
	WindowDays            int     `json:"window_days"`
	TotalBacklogs         int     `json:"total_backlogs"`
	CreatedInWindow       int     `json:"created_in_window"`
	CreationRatePerDay    float64 `json:"creation_rate_per_day"`
	DecisionCount         int     `json:"decision_count"`
	PromotedCount         int     `json:"promoted_count"`
	MergedCount           int     `json:"merged_count"`
	HeldCount             int     `json:"held_count"`
	DroppedCount          int     `json:"dropped_count"`
	PromotionRate         float64 `json:"promotion_rate"`
	MergeRate             float64 `json:"merge_rate"`
	HoldRate              float64 `json:"hold_rate"`
	DropRate              float64 `json:"drop_rate"`
	AverageMaturationDays float64 `json:"average_maturation_days"`
	BacklogGrowth         int     `json:"backlog_growth"`
}

func calculateMaturationMetrics(items []domainbacklog.Item, now time.Time, windowDays int) MaturationMetrics {
	if windowDays < 1 {
		windowDays = 30
	}
	metrics := MaturationMetrics{WindowDays: windowDays, TotalBacklogs: len(items)}
	windowStart := now.UTC().Add(-time.Duration(windowDays) * 24 * time.Hour)
	var finalMaturationDays int
	var finalDecisionCount int
	for _, item := range items {
		if created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.CreatedAt)); err == nil && !created.Before(windowStart) && !created.After(now) {
			metrics.CreatedInWindow++
		}
		for _, record := range item.RevalidationRecords {
			metrics.DecisionCount++
			switch strings.ToUpper(strings.TrimSpace(record.Decision)) {
			case domainbacklog.RevalidationDecisionPromote:
				metrics.PromotedCount++
			case domainbacklog.RevalidationDecisionMerge:
				metrics.MergedCount++
			case domainbacklog.RevalidationDecisionHold:
				metrics.HeldCount++
			case domainbacklog.RevalidationDecisionDrop:
				metrics.DroppedCount++
			}
		}
		// Average maturation is creation-to-final-selection, not an average of
		// every intermediate HOLD. Use the latest final decision for each Item.
		for index := len(item.RevalidationRecords) - 1; index >= 0; index-- {
			record := item.RevalidationRecords[index]
			switch strings.ToUpper(strings.TrimSpace(record.Decision)) {
			case domainbacklog.RevalidationDecisionPromote, domainbacklog.RevalidationDecisionMerge, domainbacklog.RevalidationDecisionDrop:
				finalMaturationDays += record.MaturationDays
				finalDecisionCount++
				index = -1
			}
		}
	}
	metrics.CreationRatePerDay = float64(metrics.CreatedInWindow) / float64(windowDays)
	if metrics.DecisionCount > 0 {
		denominator := float64(metrics.DecisionCount)
		metrics.PromotionRate = float64(metrics.PromotedCount) / denominator
		metrics.MergeRate = float64(metrics.MergedCount) / denominator
		metrics.HoldRate = float64(metrics.HeldCount) / denominator
		metrics.DropRate = float64(metrics.DroppedCount) / denominator
	}
	if finalDecisionCount > 0 {
		metrics.AverageMaturationDays = float64(finalMaturationDays) / float64(finalDecisionCount)
	}
	closed := 0
	for _, item := range items {
		if item.MaturationState == domainbacklog.MaturationStateMerged || item.MaturationState == domainbacklog.MaturationStateDropped || item.DeliveryState == domainbacklog.DeliveryDone {
			closed++
		}
	}
	metrics.BacklogGrowth = metrics.TotalBacklogs - closed
	return metrics
}
