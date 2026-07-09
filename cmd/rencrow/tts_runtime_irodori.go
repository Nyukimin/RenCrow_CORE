package main

import (
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func buildIrodoriTTSProviderFromPlan(plan moduletts.IrodoriProviderPlan, speed float64) (ttsProviderSelection, bool) {
	provider := ttsinfra.NewIrodoriProvider(irodoriConfigFromPlan(plan, speed))
	return ttsProviderSelection{
		Provider: provider,
		Name:     moduletts.RuntimeProviderIrodori,
		BaseURL:  plan.BaseURL,
		Endpoint: plan.EndpointPath,
	}, true
}

func irodoriConfigFromPlan(plan moduletts.IrodoriProviderPlan, speed float64) ttsinfra.IrodoriConfig {
	return ttsinfra.IrodoriConfig{
		BaseURL:               plan.BaseURL,
		EndpointPath:          plan.EndpointPath,
		VoiceID:               plan.VoiceID,
		VoiceName:             plan.VoiceName,
		Speed:                 speed,
		ReferenceAudio:        plan.ReferenceAudio,
		ReferenceAudioURL:     plan.ReferenceAudioURL,
		Timeout:               plan.Timeout,
		Checkpoint:            plan.Checkpoint,
		ModelDevice:           plan.ModelDevice,
		ModelPrecision:        plan.ModelPrecision,
		CodecDevice:           plan.CodecDevice,
		CodecPrecision:        plan.CodecPrecision,
		EnableWatermark:       plan.EnableWatermark,
		NumSteps:              plan.NumSteps,
		NumCandidates:         plan.NumCandidates,
		SeedRaw:               plan.SeedRaw,
		CFGGuidanceMode:       plan.CFGGuidanceMode,
		CFGScaleText:          plan.CFGScaleText,
		CFGScaleSpeaker:       plan.CFGScaleSpeaker,
		CFGScaleRaw:           plan.CFGScaleRaw,
		CFGMinT:               plan.CFGMinT,
		CFGMaxT:               plan.CFGMaxT,
		ContextKVCache:        plan.ContextKVCache,
		TruncationFactorRaw:   plan.TruncationFactorRaw,
		RescaleKRaw:           plan.RescaleKRaw,
		RescaleSigmaRaw:       plan.RescaleSigmaRaw,
		SpeakerKVScaleRaw:     plan.SpeakerKVScaleRaw,
		SpeakerKVMinTRaw:      plan.SpeakerKVMinTRaw,
		SpeakerKVMaxLayersRaw: plan.SpeakerKVMaxLayersRaw,
	}
}
