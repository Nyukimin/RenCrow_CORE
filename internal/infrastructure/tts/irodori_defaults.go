package tts

import moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"

func withIrodoriDefaults(cfg IrodoriConfig) IrodoriConfig {
	cfg.EndpointPath = moduletts.IrodoriEndpointPath(cfg.EndpointPath)
	if cfg.Speed <= 0 {
		cfg.Speed = 1.2
	}
	applyIrodoriRunGenerationConfig(&cfg, moduletts.ApplyIrodoriRunGenerationDefaults(irodoriRunGenerationConfigFromConfig(cfg)))
	return cfg
}

func resolveIrodoriVoiceID(raw string) string {
	return moduletts.ResolveIrodoriVoiceID(raw)
}

func resolveIrodoriVoiceName(raw string) string {
	return moduletts.ResolveIrodoriVoiceName(raw)
}

func resolveIrodoriStyle(emotion EmotionState) string {
	return moduletts.ResolveIrodoriStyle(moduletts.IrodoriStyleEmotion{
		Emotion:        emotion.Emotion,
		Intensity:      emotion.Intensity,
		Speed:          emotion.Speed,
		Pitch:          emotion.Pitch,
		Pause:          emotion.Pause,
		Expressiveness: emotion.Expressiveness,
	})
}

func irodoriSynthesisPayload(voice, style, text string, speed float64) moduletts.IrodoriSynthesisPayload {
	return moduletts.BuildIrodoriSynthesisPayload(moduletts.IrodoriSynthesisPayloadInput{
		Voice: voice,
		Style: style,
		Text:  text,
		Speed: speed,
	})
}

func applyIrodoriRunGenerationConfig(cfg *IrodoriConfig, runCfg moduletts.IrodoriRunGenerationConfig) {
	cfg.Checkpoint = runCfg.Checkpoint
	cfg.ModelDevice = runCfg.ModelDevice
	cfg.ModelPrecision = runCfg.ModelPrecision
	cfg.CodecDevice = runCfg.CodecDevice
	cfg.CodecPrecision = runCfg.CodecPrecision
	cfg.EnableWatermark = runCfg.EnableWatermark
	cfg.NumSteps = runCfg.NumSteps
	cfg.NumCandidates = runCfg.NumCandidates
	cfg.SeedRaw = runCfg.SeedRaw
	cfg.CFGGuidanceMode = runCfg.CFGGuidanceMode
	cfg.CFGScaleText = runCfg.CFGScaleText
	cfg.CFGScaleSpeaker = runCfg.CFGScaleSpeaker
	cfg.CFGScaleRaw = runCfg.CFGScaleRaw
	cfg.CFGMinT = runCfg.CFGMinT
	cfg.CFGMaxT = runCfg.CFGMaxT
	cfg.ContextKVCache = runCfg.ContextKVCache
	cfg.TruncationFactorRaw = runCfg.TruncationFactorRaw
	cfg.RescaleKRaw = runCfg.RescaleKRaw
	cfg.RescaleSigmaRaw = runCfg.RescaleSigmaRaw
	cfg.SpeakerKVScaleRaw = runCfg.SpeakerKVScaleRaw
	cfg.SpeakerKVMinTRaw = runCfg.SpeakerKVMinTRaw
	cfg.SpeakerKVMaxLayersRaw = runCfg.SpeakerKVMaxLayersRaw
}
