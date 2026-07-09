package tts

import (
	"encoding/json"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func (p *IrodoriProvider) synthesisURL() string {
	return moduletts.IrodoriSynthesisURL(p.baseURL, p.cfg.EndpointPath)
}

func (p *IrodoriProvider) runGenerationURL() string {
	return moduletts.IrodoriRunGenerationURL(p.baseURL)
}

func (p *IrodoriProvider) runGenerationData(text string, uploadedAudio any) []any {
	return moduletts.IrodoriRunGenerationData(irodoriRunGenerationConfigFromConfig(p.cfg), text, uploadedAudio)
}

func irodoriRunGenerationConfigFromConfig(cfg IrodoriConfig) moduletts.IrodoriRunGenerationConfig {
	return moduletts.IrodoriRunGenerationConfig{
		Checkpoint:            cfg.Checkpoint,
		ModelDevice:           cfg.ModelDevice,
		ModelPrecision:        cfg.ModelPrecision,
		CodecDevice:           cfg.CodecDevice,
		CodecPrecision:        cfg.CodecPrecision,
		EnableWatermark:       cfg.EnableWatermark,
		NumSteps:              cfg.NumSteps,
		NumCandidates:         cfg.NumCandidates,
		SeedRaw:               cfg.SeedRaw,
		CFGGuidanceMode:       cfg.CFGGuidanceMode,
		CFGScaleText:          cfg.CFGScaleText,
		CFGScaleSpeaker:       cfg.CFGScaleSpeaker,
		CFGScaleRaw:           cfg.CFGScaleRaw,
		CFGMinT:               cfg.CFGMinT,
		CFGMaxT:               cfg.CFGMaxT,
		ContextKVCache:        cfg.ContextKVCache,
		TruncationFactorRaw:   cfg.TruncationFactorRaw,
		RescaleKRaw:           cfg.RescaleKRaw,
		RescaleSigmaRaw:       cfg.RescaleSigmaRaw,
		SpeakerKVScaleRaw:     cfg.SpeakerKVScaleRaw,
		SpeakerKVMinTRaw:      cfg.SpeakerKVMinTRaw,
		SpeakerKVMaxLayersRaw: cfg.SpeakerKVMaxLayersRaw,
	}
}

func rewriteLoopbackIrodoriFileURL(baseURL, rawURL string) string {
	return moduletts.RewriteLoopbackIrodoriFileURL(baseURL, rawURL)
}

func parseIrodoriSimpleAudioURL(raw json.RawMessage) string {
	return moduletts.ParseIrodoriSimpleAudioURL(raw)
}
