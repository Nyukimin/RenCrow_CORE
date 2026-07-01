package core

import (
	"context"
	"time"
)

type RuntimeHealthProviders struct {
	LLMReports     []HealthReport
	Chat           HealthProvider
	Worker         HealthProvider
	TTS            HealthProvider
	TTSPlayback    HealthProvider
	STT            HealthProvider
	STTViewerInput HealthProvider
}

func BuildRuntimeHealthReports(ctx context.Context, providers RuntimeHealthProviders, checkedAt time.Time) []HealthReport {
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	reports := make([]HealthReport, 0, len(providers.LLMReports)+6)
	reports = append(reports, providers.LLMReports...)
	reports = append(reports, ProviderHealth(ctx, "chat", providers.Chat, checkedAt))
	reports = append(reports, ProviderHealth(ctx, "worker", providers.Worker, checkedAt))
	reports = append(reports, ProviderHealth(ctx, "tts", providers.TTS, checkedAt))
	reports = append(reports, ProviderHealth(ctx, "tts.playback", providers.TTSPlayback, checkedAt))
	reports = append(reports, ProviderHealth(ctx, "stt", providers.STT, checkedAt))
	reports = append(reports, ProviderHealth(ctx, "stt.viewer_input", providers.STTViewerInput, checkedAt))
	return reports
}

func BuildRuntimeHealthSnapshot(ctx context.Context, providers RuntimeHealthProviders, updatedAt time.Time) HealthSnapshot {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	reports := BuildRuntimeHealthReports(ctx, providers, updatedAt)
	return BuildHealthSnapshot(reports, updatedAt)
}
