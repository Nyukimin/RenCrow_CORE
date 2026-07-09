package modulebridge

import (
	"context"

	internaltts "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type TTSProviderAdapter struct {
	provider   internaltts.Provider
	outputDir  string
	filePrefix string
}

const runtimeTTSModuleFilePrefix = "module-health"

func NewTTSProviderAdapter(provider internaltts.Provider, outputDir string, filePrefix string) *TTSProviderAdapter {
	return &TTSProviderAdapter{
		provider:   provider,
		outputDir:  outputDir,
		filePrefix: filePrefix,
	}
}

func NewRuntimeTTSProviderAdapter(provider internaltts.Provider, outputDir string) moduletts.Provider {
	return NewTTSProviderAdapter(provider, outputDir, runtimeTTSModuleFilePrefix)
}

func (a *TTSProviderAdapter) Name() string {
	if a == nil || a.provider == nil {
		return ""
	}
	return a.provider.Name()
}

func (a *TTSProviderAdapter) Health(context.Context) core.HealthReport {
	if a == nil || a.provider == nil {
		return moduletts.BuildProviderHealth(moduletts.ProviderHealthSnapshot{})
	}
	return moduletts.BuildProviderHealth(moduletts.ProviderHealthSnapshot{Provider: a.provider.Name(), Ready: true})
}

func (a *TTSProviderAdapter) Synthesize(ctx context.Context, req moduletts.SynthesisRequest) (moduletts.SynthesisResult, error) {
	out, err := a.provider.Synthesize(ctx, internaltts.SynthesisInput{
		Text:    req.SpeechText,
		Emotion: toInternalTTSEmotion(req.Emotion),
		VoiceProfile: internaltts.VoiceProfile{
			VoiceID: req.VoiceID,
		},
		OutputDir:  a.outputDir,
		FilePrefix: a.filePrefix,
	})
	if err != nil {
		return moduletts.SynthesisResult{}, err
	}
	return moduletts.BuildSynthesisResult(req, moduletts.SynthesisOutput{
		AudioPath:  out.AudioFilePath,
		DurationMS: int64(out.DurationMS),
	}), nil
}

func toInternalTTSEmotion(in *moduletts.EmotionState) internaltts.EmotionState {
	if in == nil {
		return internaltts.EmotionState{}
	}
	return internaltts.EmotionState{
		Emotion: in.PrimaryEmotion,
		Reason:  moduletts.BuildEmotionProviderReason(in),
	}
}
