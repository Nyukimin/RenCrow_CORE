package main

import (
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/modulebridge"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

// This file is the integration boundary for RenCrow_TTS.
// CORE constructs one Gateway provider and never selects a physical TTS target.

type ttsProviderSelection struct {
	Provider ttsinfra.Provider
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
	baseURL := cfg.TTS.GatewayURL()
	if baseURL == "" {
		return ttsProviderSelection{}, false
	}
	provider := ttsinfra.NewGatewayProvider(ttsinfra.GatewayProviderConfig{
		BaseURL:       baseURL,
		VoiceID:       cfg.TTS.VoiceID,
		Speed:         cfg.TTS.Speed,
		TLSSkipVerify: cfg.TTS.TLSSkipVerify,
		Timeout:       time.Duration(cfg.TTS.TimeoutMS) * time.Millisecond,
	})
	return ttsProviderSelection{
		Provider: provider,
	}.withModule(cfg.TTS.OutputDir), true
}

func buildFallbackTTSSynthesizer(cfg *config.Config) *ttsinfra.FallbackSynthesizer {
	selection, ok := buildPrimaryTTSProvider(cfg)
	if !ok {
		return nil
	}
	return ttsinfra.NewFallbackSynthesizer(selection.Provider)
}
