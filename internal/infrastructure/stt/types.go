package stt

import (
	"context"
	"fmt"
	"strings"
	"time"

	modulestt "github.com/Nyukimin/picoclaw_multiLLM/modules/stt"
)

const (
	ProviderExternalHTTP = "external_http"
	ProviderOpenAIAPI    = "openai-api"
	ProviderMock         = "mock"

	ErrorNoSpeechDetected = "NO_SPEECH_DETECTED"
	ErrorInvalidAudio     = "INVALID_AUDIO"
	ErrorProviderFailure  = "PROVIDER_FAILURE"
	ErrorProviderTimeout  = "PROVIDER_TIMEOUT"
	ErrorProviderBusy     = "PROVIDER_BUSY"

	BusyPolicyQueueLatest = "queue_latest"
	BusyPolicyReject      = "reject"
	BusyPolicyDirect      = "direct"
)

type Config struct {
	Enabled    bool
	Provider   string
	Language   string
	Model      string
	Timeout    time.Duration
	SaveAudio  bool
	BusyPolicy string

	ExternalHTTPURL string
	HelperCommand   string
	HelperArgs      []string
}

func (c Config) WithDefaults() Config {
	defaults := modulestt.ApplyProviderDefaults(modulestt.ProviderDefaultsConfig{
		Provider:   c.Provider,
		Language:   c.Language,
		Timeout:    c.Timeout,
		BusyPolicy: c.BusyPolicy,
	})
	c.Provider = defaults.Provider
	c.Language = defaults.Language
	c.Timeout = defaults.Timeout
	c.BusyPolicy = defaults.BusyPolicy
	return c
}

type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type Result struct {
	Text         string    `json:"text"`
	Language     string    `json:"language"`
	Duration     float64   `json:"duration"`
	Segments     []Segment `json:"segments"`
	Provider     string    `json:"provider,omitempty"`
	Model        string    `json:"model,omitempty"`
	EventID      string    `json:"event_id,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty"`
	Message      string    `json:"message,omitempty"`
	ProcessingMS int64     `json:"processing_ms,omitempty"`
}

type Health struct {
	Status   string `json:"status"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Device   string `json:"device"`
	Ready    bool   `json:"ready"`
}

type Provider interface {
	Name() string
	Health(ctx context.Context) Health
	Transcribe(ctx context.Context, wav []byte) (Result, error)
}

type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewError(code, message string, err error) *Error {
	return &Error{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message), Err: err}
}
