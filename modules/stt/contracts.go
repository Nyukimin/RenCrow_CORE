// Package stt defines speech-to-text module contracts.
package stt

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type AudioFormat string

const (
	AudioFormatWAV  AudioFormat = "wav"
	AudioFormatWebM AudioFormat = "webm"
	AudioFormatM4A  AudioFormat = "m4a"
)

type TranscriptionRequest struct {
	SessionID core.SessionID `json:"session_id,omitempty"`
	RequestID core.RequestID `json:"request_id,omitempty"`
	Audio     []byte         `json:"-"`
	Format    AudioFormat    `json:"format,omitempty"`
	Language  string         `json:"language,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
}

type Segment struct {
	Start time.Duration `json:"start"`
	End   time.Duration `json:"end"`
	Text  string        `json:"text"`
}

type TranscriptionResult struct {
	RequestID    core.RequestID `json:"request_id,omitempty"`
	Text         string         `json:"text"`
	Language     string         `json:"language,omitempty"`
	Duration     time.Duration  `json:"duration,omitempty"`
	Segments     []Segment      `json:"segments,omitempty"`
	Provider     string         `json:"provider,omitempty"`
	Model        string         `json:"model,omitempty"`
	ProcessingMS int64          `json:"processing_ms,omitempty"`
}

type SegmentOutput struct {
	StartSeconds float64
	EndSeconds   float64
	Text         string
}

type TranscriptionOutput struct {
	Text         string
	Language     string
	DurationSec  float64
	Segments     []SegmentOutput
	Provider     string
	Model        string
	ProcessingMS int64
}

type ProviderHealthSnapshot struct {
	Status   string
	Provider string
	Model    string
	Device   string
	Ready    bool
}

type ViewerInputSnapshot struct {
	BaseURL              string `json:"base_url,omitempty"`
	StreamURL            string `json:"stream_url,omitempty"`
	ChatInputEndpoint    string `json:"chat_input_endpoint,omitempty"`
	ClientLogPath        string `json:"client_log_path,omitempty"`
	LatestWAVPath        string `json:"latest_wav_path,omitempty"`
	ArchiveDir           string `json:"archive_dir,omitempty"`
	AutoTestScriptPath   string `json:"autotest_script_path,omitempty"`
	AutoTestOutputPath   string `json:"autotest_output_path,omitempty"`
	ProviderURL          string `json:"provider_url,omitempty"`
	GatewayURL           string `json:"gateway_url,omitempty"`
	ProviderConfigured   bool   `json:"provider_configured"`
	GatewayConfigured    bool   `json:"gateway_configured"`
	WebSocketConfigured  bool   `json:"websocket_configured"`
	TranscriptSource     string `json:"transcript_source,omitempty"`
	TranscriptInputType  string `json:"transcript_input_type,omitempty"`
	TranscriptInjectPath string `json:"transcript_inject_path,omitempty"`
}

type ViewerInputRuntimeConfig struct {
	BaseURL             string
	StreamURL           string
	ProviderURL         string
	GatewayURL          string
	ProviderAvailable   bool
	WebSocketAvailable  bool
	ChatInputEndpoint   string
	ClientLogPath       string
	LatestWAVPath       string
	ArchiveDir          string
	AutoTestScriptPath  string
	AutoTestOutputPath  string
	TranscriptSource    string
	TranscriptInputType string
}

type ViewerInputObserver interface {
	Health(ctx context.Context) core.HealthReport
	Snapshot(ctx context.Context) (ViewerInputSnapshot, error)
}

type Provider interface {
	Name() string
	Health(ctx context.Context) core.HealthReport
	Transcribe(ctx context.Context, req TranscriptionRequest) (TranscriptionResult, error)
}
