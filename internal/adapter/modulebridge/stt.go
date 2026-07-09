package modulebridge

import (
	"context"

	internalstt "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

type STTProviderAdapter struct {
	provider internalstt.Provider
}

func NewSTTProviderAdapter(provider internalstt.Provider) *STTProviderAdapter {
	return &STTProviderAdapter{provider: provider}
}

func NewRuntimeSTTProviderAdapter(provider internalstt.Provider) modulestt.Provider {
	return NewSTTProviderAdapter(provider)
}

func (a *STTProviderAdapter) Name() string {
	if a == nil || a.provider == nil {
		return ""
	}
	return a.provider.Name()
}

func (a *STTProviderAdapter) Health(ctx context.Context) core.HealthReport {
	if a == nil || a.provider == nil {
		return modulestt.BuildUnavailableProviderHealth()
	}
	health := a.provider.Health(ctx)
	return modulestt.BuildProviderHealth(modulestt.ProviderHealthSnapshot{
		Status:   health.Status,
		Provider: health.Provider,
		Model:    health.Model,
		Device:   health.Device,
		Ready:    health.Ready,
	})
}

func (a *STTProviderAdapter) Transcribe(ctx context.Context, req modulestt.TranscriptionRequest) (modulestt.TranscriptionResult, error) {
	req = modulestt.CloneTranscriptionRequest(req)
	result, err := a.provider.Transcribe(ctx, req.Audio)
	if err != nil {
		return modulestt.TranscriptionResult{}, err
	}
	return modulestt.BuildTranscriptionResult(req, modulestt.TranscriptionOutput{
		Text:         result.Text,
		Language:     result.Language,
		DurationSec:  result.Duration,
		Segments:     toModuleSTTSegments(result.Segments),
		Provider:     result.Provider,
		Model:        result.Model,
		ProcessingMS: result.ProcessingMS,
	}), nil
}

func toModuleSTTSegments(in []internalstt.Segment) []modulestt.SegmentOutput {
	out := make([]modulestt.SegmentOutput, 0, len(in))
	for _, segment := range in {
		out = append(out, modulestt.SegmentOutput{
			StartSeconds: segment.Start,
			EndSeconds:   segment.End,
			Text:         segment.Text,
		})
	}
	return out
}
