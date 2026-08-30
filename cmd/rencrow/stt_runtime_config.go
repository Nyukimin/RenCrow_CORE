package main

import (
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

func inferSTTBaseURL(ttsBaseURL, sttGatewayHTTPURL string) string {
	return modulestt.InferBaseURL(modulestt.RuntimeURLConfig{
		TTSBaseURL:     ttsBaseURL,
		GatewayHTTPURL: sttGatewayHTTPURL,
	})
}

func extractBaseFromGatewayHTTPURL(raw string) string {
	return modulestt.ExtractBaseFromGatewayHTTPURL(raw)
}

func inferSTTBaseURLFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return "http://127.0.0.1:8766"
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.STT.GatewayBaseURL), "/")
	if base == "" {
		return "http://127.0.0.1:8766"
	}
	return base
}

func inferSTTGatewayHTTPURLFromConfig(cfg *config.Config) string {
	return modulestt.GatewayTranscriptionURL(inferSTTBaseURLFromConfig(cfg))
}

func buildSTTProvider(cfg *config.Config) sttinfra.Provider {
	plan, ok := modulestt.BuildRuntimeProviderPlan(sttRuntimeConfigFromAppConfig(cfg))
	if !ok {
		return nil
	}
	providerCfg := sttinfra.Config{
		Enabled:        plan.Enabled,
		Provider:       plan.Provider,
		Language:       plan.Language,
		Model:          plan.Model,
		Timeout:        plan.Timeout,
		SaveAudio:      plan.SaveAudio,
		BusyPolicy:     plan.BusyPolicy,
		GatewayHTTPURL: plan.GatewayHTTPURL,
	}
	return sttinfra.NewProvider(providerCfg)
}

func sttRuntimeConfigFromAppConfig(cfg *config.Config) modulestt.RuntimeConfig {
	if cfg == nil {
		return modulestt.RuntimeConfig{}
	}
	return modulestt.RuntimeConfig{
		Enabled:        cfg.STT.Enabled,
		Provider:       modulestt.ProviderRenCrowSTT,
		Language:       "ja",
		TimeoutMS:      cfg.STT.TimeoutMS,
		BusyPolicy:     cfg.STT.BusyPolicy,
		GatewayHTTPURL: inferSTTGatewayHTTPURLFromConfig(cfg),
		SaveAudio:      cfg.STT.Debug.SaveAudio,
		SaveTranscript: cfg.STT.Debug.SaveTranscript,
	}
}

func sttRuntimeURLConfigFromAppConfig(cfg *config.Config, ttsBaseURL string) modulestt.RuntimeURLConfig {
	if cfg == nil {
		return modulestt.RuntimeURLConfig{TTSBaseURL: ttsBaseURL}
	}
	return modulestt.RuntimeURLConfig{
		Provider:       modulestt.ProviderRenCrowSTT,
		GatewayHTTPURL: inferSTTGatewayHTTPURLFromConfig(cfg),
		TTSBaseURL:     ttsBaseURL,
		ServerHost:     cfg.Server.Host,
		ServerPort:     cfg.Server.Port,
		TLSEnabled:     cfg.Server.TLS.Enabled,
	}
}
