package main

import (
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/modulebridge"
	ttsinfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tts"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

// This file is the integration boundary for RenCrow_TTS.
// Keep provider selection and config translation here so callers do not know
// about provider implementation details.

type ttsProviderSelection struct {
	Provider ttsinfra.Provider
	Name     string
	BaseURL  string
	Endpoint string
	Module   moduletts.Provider
}

func (s ttsProviderSelection) withModule(outputDir string) ttsProviderSelection {
	if s.Provider != nil && s.Module == nil {
		s.Module = modulebridge.NewRuntimeTTSProviderAdapter(s.Provider, outputDir)
	}
	return s
}

func buildPrimaryTTSProvider(cfg *config.Config) (ttsProviderSelection, bool) {
	if cfg == nil || !cfg.TTS.Enabled {
		return ttsProviderSelection{}, false
	}
	plan, ok := moduletts.FirstRuntimeProviderPlan(ttsRuntimeConfigFromAppConfig(cfg), false)
	if !ok {
		return ttsProviderSelection{}, false
	}
	sel, ok := buildTTSProviderFromPlan(plan, cfg.TTS.Speed)
	if !ok {
		return ttsProviderSelection{}, false
	}
	return sel.withModule(cfg.TTS.OutputDir), true
}

func buildFallbackTTSSynthesizer(cfg *config.Config) *ttsinfra.FallbackSynthesizer {
	providers := buildTTSProviders(cfg, true)
	if len(providers) == 0 {
		return nil
	}
	return ttsinfra.NewFallbackSynthesizer(providers...)
}

func buildTTSProviders(cfg *config.Config, includeUnavailable bool) []ttsinfra.Provider {
	if cfg == nil || !cfg.TTS.Enabled {
		return nil
	}
	plans := moduletts.BuildRuntimeProviderPlans(ttsRuntimeConfigFromAppConfig(cfg), includeUnavailable)
	providers := make([]ttsinfra.Provider, 0, len(plans))
	for _, plan := range plans {
		sel, ok := buildTTSProviderFromPlan(plan, cfg.TTS.Speed)
		if ok {
			providers = append(providers, sel.Provider)
		}
	}
	return providers
}

func buildTTSProviderByName(cfg *config.Config, name string, includeUnavailable bool) (ttsProviderSelection, bool) {
	plan, ok := moduletts.BuildRuntimeProviderPlan(ttsRuntimeConfigFromAppConfig(cfg), name, includeUnavailable)
	if !ok {
		return ttsProviderSelection{}, false
	}
	return buildTTSProviderFromPlan(plan, cfg.TTS.Speed)
}

func buildTTSProviderFromPlan(plan moduletts.RuntimeProviderPlan, speed float64) (ttsProviderSelection, bool) {
	if !plan.Available {
		return ttsProviderSelection{
			Provider: ttsinfra.NewUnavailableProvider(plan.Name, plan.Unavailable),
			Name:     plan.Name,
		}, true
	}
	switch plan.Name {
	case moduletts.RuntimeProviderIrodori:
		return buildIrodoriTTSProviderFromPlan(plan.Irodori, speed)
	}
	return ttsProviderSelection{}, false
}

func ttsRuntimeConfigFromAppConfig(cfg *config.Config) moduletts.RuntimeConfig {
	if cfg == nil {
		return moduletts.RuntimeConfig{}
	}
	commands := make([]moduletts.CommandSpec, 0, len(cfg.TTS.PlaybackCommands))
	for _, command := range cfg.TTS.PlaybackCommands {
		commands = append(commands, moduletts.CommandSpec{Name: command.Name, Args: append([]string(nil), command.Args...)})
	}
	return moduletts.RuntimeConfig{
		Enabled:          cfg.TTS.Enabled,
		VoiceID:          cfg.TTS.VoiceID,
		ProviderPriority: append([]string(nil), cfg.TTS.ProviderPriority...),
		PlaybackCommands: commands,
		Irodori: moduletts.IrodoriRuntimeConfig{
			Enabled:               cfg.TTS.Irodori.Enabled,
			BaseURL:               cfg.TTS.Irodori.BaseURL,
			EndpointPath:          cfg.TTS.Irodori.EndpointPath,
			VoiceID:               cfg.TTS.Irodori.VoiceID,
			VoiceName:             cfg.TTS.Irodori.VoiceName,
			ReferenceAudio:        cfg.TTS.Irodori.ReferenceAudio,
			ReferenceAudioURL:     cfg.TTS.Irodori.ReferenceAudioURL,
			TimeoutSec:            cfg.TTS.Irodori.TimeoutSec,
			Checkpoint:            cfg.TTS.Irodori.Checkpoint,
			ModelDevice:           cfg.TTS.Irodori.ModelDevice,
			ModelPrecision:        cfg.TTS.Irodori.ModelPrecision,
			CodecDevice:           cfg.TTS.Irodori.CodecDevice,
			CodecPrecision:        cfg.TTS.Irodori.CodecPrecision,
			EnableWatermark:       cfg.TTS.Irodori.EnableWatermark,
			NumSteps:              cfg.TTS.Irodori.NumSteps,
			NumCandidates:         cfg.TTS.Irodori.NumCandidates,
			SeedRaw:               cfg.TTS.Irodori.SeedRaw,
			CFGGuidanceMode:       cfg.TTS.Irodori.CFGGuidanceMode,
			CFGScaleText:          cfg.TTS.Irodori.CFGScaleText,
			CFGScaleSpeaker:       cfg.TTS.Irodori.CFGScaleSpeaker,
			CFGScaleRaw:           cfg.TTS.Irodori.CFGScaleRaw,
			CFGMinT:               cfg.TTS.Irodori.CFGMinT,
			CFGMaxT:               cfg.TTS.Irodori.CFGMaxT,
			ContextKVCache:        cfg.TTS.Irodori.ContextKVCache,
			TruncationFactorRaw:   cfg.TTS.Irodori.TruncationFactorRaw,
			RescaleKRaw:           cfg.TTS.Irodori.RescaleKRaw,
			RescaleSigmaRaw:       cfg.TTS.Irodori.RescaleSigmaRaw,
			SpeakerKVScaleRaw:     cfg.TTS.Irodori.SpeakerKVScaleRaw,
			SpeakerKVMinTRaw:      cfg.TTS.Irodori.SpeakerKVMinTRaw,
			SpeakerKVMaxLayersRaw: cfg.TTS.Irodori.SpeakerKVMaxLayersRaw,
		},
	}
}
