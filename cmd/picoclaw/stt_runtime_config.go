package main

import (
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	sttinfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/picoclaw_multiLLM/modules/stt"
)

func sttStreamURLFromConfig(cfg *config.Config) string {
	return modulestt.StreamURL(sttRuntimeURLConfigFromAppConfig(cfg, ""))
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
	return modulestt.InferBaseURL(sttRuntimeURLConfigFromAppConfig(cfg, ""))
}

func inferSTTProviderURLFromConfig(cfg *config.Config) string {
	return modulestt.InferProviderURL(sttRuntimeURLConfigFromAppConfig(cfg, ""))
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
		Provider:       cfg.STT.Provider,
		Language:       cfg.STT.Language,
		Model:          cfg.STT.Model,
		TimeoutMS:      cfg.STT.TimeoutMS,
		BusyPolicy:     cfg.STT.BusyPolicy,
		ProviderURL:    cfg.STT.ProviderURL,
		SaveAudio:      cfg.STT.Debug.SaveAudio,
		SaveTranscript: cfg.STT.Debug.SaveTranscript,
	}
}

func sttRuntimeURLConfigFromAppConfig(cfg *config.Config, ttsBaseURL string) modulestt.RuntimeURLConfig {
	if cfg == nil {
		return modulestt.RuntimeURLConfig{TTSBaseURL: ttsBaseURL}
	}
	return modulestt.RuntimeURLConfig{
		Provider:    cfg.STT.Provider,
		ProviderURL: cfg.STT.ProviderURL,
		StreamURL:   cfg.STT.StreamURL,
		TTSBaseURL:  ttsBaseURL,
		ServerHost:  cfg.Server.Host,
		ServerPort:  cfg.Server.Port,
		TLSEnabled:  cfg.Server.TLS.Enabled,
	}
}
