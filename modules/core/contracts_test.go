package core

import (
	"context"
	"testing"
	"time"
)

type fakeCoreHealthProvider struct {
	report HealthReport
}

func (p fakeCoreHealthProvider) Health(context.Context) HealthReport {
	return p.report
}

func TestProviderHealthReportsNilProviderDown(t *testing.T) {
	checkedAt := time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
	got := ProviderHealth(context.Background(), "tts", nil, checkedAt)
	if got.Module != "tts" || got.Status != HealthDown || got.CheckedAt != checkedAt {
		t.Fatalf("unexpected nil provider health: %+v", got)
	}
}

func TestProviderHealthSetsModuleAndCheckedAt(t *testing.T) {
	checkedAt := time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
	got := ProviderHealth(context.Background(), "chat", fakeCoreHealthProvider{
		report: HealthReport{Module: "legacy-chat", Status: HealthReady, Ready: true},
	}, checkedAt)
	if got.Module != "chat" || got.CheckedAt != checkedAt || got.Status != HealthReady || !got.Ready {
		t.Fatalf("unexpected provider health: %+v", got)
	}
}

func TestAggregateHealthReportsReadyWhenAllReady(t *testing.T) {
	got := AggregateHealthReports([]HealthReport{
		{Module: "chat", Status: HealthReady, Ready: true},
		{Module: "tts", Status: HealthReady, Ready: true},
	})
	if got.Status != HealthReady || !got.Ready {
		t.Fatalf("unexpected aggregate: %+v", got)
	}
}

func TestAggregateHealthReportsLiveWhenAnyReadyLive(t *testing.T) {
	got := AggregateHealthReports([]HealthReport{
		{Module: "chat", Status: HealthReady, Ready: true},
		{Module: "tts", Status: HealthLive, Ready: true},
	})
	if got.Status != HealthLive || !got.Ready {
		t.Fatalf("unexpected aggregate: %+v", got)
	}
}

func TestAggregateHealthReportsBlockedOverridesLive(t *testing.T) {
	got := AggregateHealthReports([]HealthReport{
		{Module: "chat", Status: HealthLive, Ready: true},
		{Module: "stt", Status: HealthBlocked, Ready: false},
	})
	if got.Status != HealthBlocked || got.Ready {
		t.Fatalf("unexpected aggregate: %+v", got)
	}
}

func TestAggregateHealthReportsDownOverridesBlocked(t *testing.T) {
	got := AggregateHealthReports([]HealthReport{
		{Module: "stt", Status: HealthBlocked, Ready: false},
		{Module: "llm:worker", Status: HealthDown, Ready: false},
	})
	if got.Status != HealthDown || got.Ready {
		t.Fatalf("unexpected aggregate: %+v", got)
	}
}

func TestBuildHealthSnapshotUsesAggregateStatus(t *testing.T) {
	updatedAt := time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
	got := BuildHealthSnapshot([]HealthReport{
		{Module: "chat", Status: HealthReady, Ready: true},
		{Module: "llm:worker", Status: HealthLive, Ready: true},
	}, updatedAt)
	if got.Status != HealthLive || !got.Ready || got.UpdatedAt != "2026-05-30T01:02:03Z" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if len(got.Modules) != 2 {
		t.Fatalf("snapshot modules missing: %+v", got)
	}
}
