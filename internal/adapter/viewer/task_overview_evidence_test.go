package viewer

import (
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
)

func TestTaskOverviewProjectsOnlyKnownBoundedExecutionEvidence(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	result := taskcenter.DailyOutcomePartial
	definition := taskcenter.DailyDefinition{LatestEvidence: taskcenter.DailyEvidence{
		LastAttemptAt: taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueKnown, Value: &at},
		LastSuccessAt: taskcenter.DailyValue[time.Time]{State: taskcenter.DailyValueUnknown, Value: &at},
		LastResult:    taskcenter.DailyValue[taskcenter.DailyOutcome]{State: taskcenter.DailyValueKnown, Value: &result},
	}}
	item := projectDailyTaskOverviewItem(definition)
	if item.LastAttemptAt == nil || !item.LastAttemptAt.Equal(at) || item.LastSuccessAt != nil || item.LastResult == nil || *item.LastResult != "partial" {
		t.Fatalf("evidence projection = %+v", item)
	}
	result = "private error text"
	if item := projectDailyTaskOverviewItem(definition); item.LastResult != nil {
		t.Fatalf("unrecognized outcome must not expose private values: %+v", item)
	}
}
