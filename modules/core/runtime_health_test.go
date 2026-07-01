package core

import (
	"context"
	"testing"
	"time"
)

type fakeRuntimeHealthProvider struct {
	module string
	status HealthStatus
}

func (p fakeRuntimeHealthProvider) Health(context.Context) HealthReport {
	return HealthReport{
		Module: p.module,
		Status: p.status,
		Ready:  p.status == HealthReady || p.status == HealthLive,
	}
}

func TestBuildRuntimeHealthReportsOrdersRuntimeModules(t *testing.T) {
	now := time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
	reports := BuildRuntimeHealthReports(context.Background(), RuntimeHealthProviders{
		LLMReports: []HealthReport{
			{Module: "llm:chat", Status: HealthLive, Ready: true},
			{Module: "llm:worker", Status: HealthLive, Ready: true},
		},
		Chat:           fakeRuntimeHealthProvider{module: "chat", status: HealthReady},
		Worker:         fakeRuntimeHealthProvider{module: "worker", status: HealthReady},
		TTS:            fakeRuntimeHealthProvider{module: "tts", status: HealthLive},
		TTSPlayback:    fakeRuntimeHealthProvider{module: "tts.playback", status: HealthReady},
		STT:            fakeRuntimeHealthProvider{module: "stt", status: HealthReady},
		STTViewerInput: fakeRuntimeHealthProvider{module: "stt.viewer_input", status: HealthReady},
	}, now)

	want := []string{"llm:chat", "llm:worker", "chat", "worker", "tts", "tts.playback", "stt", "stt.viewer_input"}
	if len(reports) != len(want) {
		t.Fatalf("expected %d reports, got %+v", len(want), reports)
	}
	for i, name := range want {
		if reports[i].Module != name {
			t.Fatalf("report %d module mismatch: want %s got %+v", i, name, reports)
		}
		if reports[i].CheckedAt.IsZero() && i >= 2 {
			t.Fatalf("runtime provider report missing checked_at: %+v", reports[i])
		}
	}
}

func TestBuildRuntimeHealthSnapshotAggregatesReports(t *testing.T) {
	snapshot := BuildRuntimeHealthSnapshot(context.Background(), RuntimeHealthProviders{
		LLMReports:     []HealthReport{{Module: "llm:chat", Status: HealthLive, Ready: true}},
		Chat:           fakeRuntimeHealthProvider{module: "chat", status: HealthReady},
		Worker:         fakeRuntimeHealthProvider{module: "worker", status: HealthBlocked},
		TTS:            fakeRuntimeHealthProvider{module: "tts", status: HealthReady},
		TTSPlayback:    fakeRuntimeHealthProvider{module: "tts.playback", status: HealthReady},
		STT:            fakeRuntimeHealthProvider{module: "stt", status: HealthReady},
		STTViewerInput: fakeRuntimeHealthProvider{module: "stt.viewer_input", status: HealthReady},
	}, time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))

	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" || snapshot.Status != HealthBlocked || snapshot.Ready {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}
