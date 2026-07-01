package tts

func BuildEmotionProviderReason(in *EmotionState) map[string]any {
	if in == nil {
		return nil
	}
	return map[string]any{
		"voice_profile": in.VoiceProfile,
		"prosody":       in.Prosody,
		"metadata":      in.Metadata,
	}
}
