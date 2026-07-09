package stt

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ViewerInputReport struct {
	UpdatedAt string              `json:"updated_at"`
	Health    core.HealthReport   `json:"health"`
	Snapshot  ViewerInputSnapshot `json:"snapshot"`
}

const (
	DefaultViewerChatInputEndpoint  = "/stt/chat-input"
	DefaultViewerClientLogPath      = "tmp/client_stt_log.txt"
	DefaultViewerLatestWAVPath      = "tmp/client_stt_input_latest.wav"
	DefaultViewerLatestRawWAVPath   = "tmp/client_stt_input_latest_raw.wav"
	DefaultViewerArchiveDir         = "tmp/stt_inputs"
	DefaultViewerAutoTestScriptPath = "scripts/stt_e2e_probe.py"
	DefaultViewerAutoTestOutputPath = "tmp/stt_e2e_from_mic_latest.json"
	DefaultViewerTranscriptSource   = "local_stt"
	DefaultViewerTranscriptType     = "voice"

	ViewerInputObserverUnavailableMessage = "stt viewer input observer unavailable"
	ViewerInputSnapshotFailedPrefix       = "stt viewer input snapshot failed: "
)

func BuildViewerInputSnapshot(config ViewerInputRuntimeConfig) ViewerInputSnapshot {
	providerURL := strings.TrimSpace(config.ProviderURL)
	gatewayURL := strings.TrimSpace(config.GatewayURL)
	streamURL := strings.TrimSpace(config.StreamURL)
	chatInputEndpoint := defaultString(config.ChatInputEndpoint, DefaultViewerChatInputEndpoint)
	return ViewerInputSnapshot{
		BaseURL:              strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"),
		StreamURL:            streamURL,
		ChatInputEndpoint:    chatInputEndpoint,
		ClientLogPath:        defaultString(config.ClientLogPath, DefaultViewerClientLogPath),
		LatestWAVPath:        defaultString(config.LatestWAVPath, DefaultViewerLatestWAVPath),
		ArchiveDir:           defaultString(config.ArchiveDir, DefaultViewerArchiveDir),
		AutoTestScriptPath:   defaultString(config.AutoTestScriptPath, DefaultViewerAutoTestScriptPath),
		AutoTestOutputPath:   defaultString(config.AutoTestOutputPath, DefaultViewerAutoTestOutputPath),
		ProviderURL:          providerURL,
		GatewayURL:           gatewayURL,
		ProviderConfigured:   providerURL != "" || config.ProviderAvailable,
		GatewayConfigured:    gatewayURL != "",
		WebSocketConfigured:  streamURL != "" || config.WebSocketAvailable,
		TranscriptSource:     defaultString(config.TranscriptSource, DefaultViewerTranscriptSource),
		TranscriptInputType:  defaultString(config.TranscriptInputType, DefaultViewerTranscriptType),
		TranscriptInjectPath: chatInputEndpoint,
	}
}

func BuildViewerInputReport(ctx context.Context, observer ViewerInputObserver, snapshot ViewerInputSnapshot, updatedAt time.Time) ViewerInputReport {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	health := core.ProviderHealth(ctx, "stt.viewer_input", observer, updatedAt)
	return ViewerInputReport{
		UpdatedAt: updatedAt.UTC().Format(time.RFC3339),
		Health:    health,
		Snapshot:  snapshot,
	}
}

func BuildViewerInputHealthReport(snapshot ViewerInputSnapshot) core.HealthReport {
	status := core.HealthReady
	detail := "viewer stt input configured"
	if !snapshot.ProviderConfigured && !snapshot.WebSocketConfigured {
		status = core.HealthBlocked
		detail = "viewer stt input has no provider or websocket"
	}
	return core.HealthReport{
		Module: "stt.viewer_input",
		Status: status,
		Ready:  status == core.HealthReady,
		Detail: detail,
		Metadata: map[string]any{
			"chat_input_endpoint": snapshot.ChatInputEndpoint,
			"stream_url":          snapshot.StreamURL,
			"provider_configured": snapshot.ProviderConfigured,
			"gateway_configured":  snapshot.GatewayConfigured,
		},
	}
}

func BuildViewerInputArchivePath(archiveDir string, capturedAt time.Time) string {
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	return filepath.Join(defaultString(archiveDir, DefaultViewerArchiveDir), fmt.Sprintf("client_stt_input_%s.wav", capturedAt.Format("20060102_150405")))
}

func BuildViewerInputRawArchivePath(archiveDir string, capturedAt time.Time) string {
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	return filepath.Join(defaultString(archiveDir, DefaultViewerArchiveDir), fmt.Sprintf("client_stt_input_%s_raw.wav", capturedAt.Format("20060102_150405")))
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
