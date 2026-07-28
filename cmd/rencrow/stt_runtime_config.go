package main

import (
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

func sttStreamURLFromConfig(cfg *config.Config) string {
	return ""
}

func inferSTTStreamURLFromProviderURL(providerURL string) string {
	return modulestt.InferStreamURLFromProviderURL(providerURL)
}

func inferSTTBaseURL(ttsBaseURL, sttProviderURL string) string {
	return modulestt.InferBaseURL(modulestt.RuntimeURLConfig{
		TTSBaseURL:  ttsBaseURL,
		ProviderURL: sttProviderURL,
	})
}

func extractBaseFromProviderURL(raw string) string {
	return modulestt.ExtractBaseFromProviderURL(raw)
}

func inferSTTProviderURL(ttsBaseURL, sttProviderURL string) string {
	return modulestt.InferLegacyInferenceProviderURL(ttsBaseURL, sttProviderURL)
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

func inferSTTProviderURLFromConfig(cfg *config.Config) string {
	return modulestt.GatewayTranscriptionURL(inferSTTBaseURLFromConfig(cfg))
}

func buildSTTProvider(cfg *config.Config) sttinfra.Provider {
	plan, ok := modulestt.BuildRuntimeProviderPlan(sttRuntimeConfigFromAppConfig(cfg))
	if !ok {
		return nil
	}
	providerCfg := sttinfra.Config{
		Enabled:         plan.Enabled,
		Provider:        plan.Provider,
		Language:        plan.Language,
		Model:           plan.Model,
		Timeout:         plan.Timeout,
		SaveAudio:       plan.SaveAudio,
		BusyPolicy:      plan.BusyPolicy,
		ExternalHTTPURL: plan.ExternalHTTPURL,
	}
	return sttinfra.NewProvider(providerCfg)
}

func inferSTTGatewayURL(sttGatewayURL, rencrowSTTURL string) string {
	return modulestt.InferGatewayURL(sttGatewayURL, rencrowSTTURL)
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
		ProviderURL:    inferSTTProviderURLFromConfig(cfg),
		SaveAudio:      cfg.STT.Debug.SaveAudio,
		SaveTranscript: cfg.STT.Debug.SaveTranscript,
	}
}

func sttRuntimeURLConfigFromAppConfig(cfg *config.Config, ttsBaseURL string) modulestt.RuntimeURLConfig {
	if cfg == nil {
		return modulestt.RuntimeURLConfig{TTSBaseURL: ttsBaseURL}
	}
	return modulestt.RuntimeURLConfig{
		Provider:    modulestt.ProviderRenCrowSTT,
		ProviderURL: inferSTTProviderURLFromConfig(cfg),
		TTSBaseURL:  ttsBaseURL,
		ServerHost:  cfg.Server.Host,
		ServerPort:  cfg.Server.Port,
		TLSEnabled:  cfg.Server.TLS.Enabled,
	}
}
