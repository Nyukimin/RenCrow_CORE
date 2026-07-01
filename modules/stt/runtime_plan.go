package stt

import (
	"strings"
	"time"
)

const (
	ProviderOpenAIAPI = "openai-api"
	ProviderMock      = "mock"

	DefaultProviderLanguage = "ja"
	DefaultProviderTimeout  = 8 * time.Second

	BusyPolicyQueueLatest = "queue_latest"
	BusyPolicyReject      = "reject"
	BusyPolicyDirect      = "direct"
)

type RuntimeConfig struct {
	Enabled        bool
	Provider       string
	Language       string
	Model          string
	TimeoutMS      int
	BusyPolicy     string
	ProviderURL    string
	SaveAudio      bool
	SaveTranscript bool
}

type RuntimeProviderPlan struct {
	Enabled         bool
	Provider        string
	Language        string
	Model           string
	Timeout         time.Duration
	SaveAudio       bool
	BusyPolicy      string
	ExternalHTTPURL string
}

type ProviderDefaultsConfig struct {
	Provider   string
	Language   string
	Timeout    time.Duration
	BusyPolicy string
}

func BuildRuntimeProviderPlan(cfg RuntimeConfig) (RuntimeProviderPlan, bool) {
	if !cfg.Enabled {
		return RuntimeProviderPlan{}, false
	}
	defaults := ApplyProviderDefaults(ProviderDefaultsConfig{
		Provider:   cfg.Provider,
		Language:   cfg.Language,
		Timeout:    time.Duration(cfg.TimeoutMS) * time.Millisecond,
		BusyPolicy: cfg.BusyPolicy,
	})
	return RuntimeProviderPlan{
		Enabled:         cfg.Enabled,
		Provider:        defaults.Provider,
		Language:        defaults.Language,
		Model:           strings.TrimSpace(cfg.Model),
		Timeout:         defaults.Timeout,
		SaveAudio:       cfg.SaveAudio,
		BusyPolicy:      defaults.BusyPolicy,
		ExternalHTTPURL: strings.TrimSpace(cfg.ProviderURL),
	}, true
}

func ApplyProviderDefaults(cfg ProviderDefaultsConfig) ProviderDefaultsConfig {
	if strings.TrimSpace(cfg.Provider) == "" {
		cfg.Provider = ProviderExternalHTTP
	} else {
		cfg.Provider = strings.TrimSpace(cfg.Provider)
	}
	if strings.TrimSpace(cfg.Language) == "" {
		cfg.Language = DefaultProviderLanguage
	} else {
		cfg.Language = strings.TrimSpace(cfg.Language)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultProviderTimeout
	}
	if strings.TrimSpace(cfg.BusyPolicy) == "" {
		cfg.BusyPolicy = BusyPolicyQueueLatest
	} else {
		cfg.BusyPolicy = NormalizeBusyPolicy(cfg.BusyPolicy)
	}
	return cfg
}
