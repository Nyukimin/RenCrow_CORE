package stt

import (
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func BuildTranscriptionResult(req TranscriptionRequest, output TranscriptionOutput) TranscriptionResult {
	return TranscriptionResult{
		RequestID:    req.RequestID,
		Text:         output.Text,
		Language:     output.Language,
		Duration:     SecondsToDuration(output.DurationSec),
		Segments:     BuildSegments(output.Segments),
		Provider:     output.Provider,
		Model:        output.Model,
		ProcessingMS: output.ProcessingMS,
	}
}

func BuildSegments(in []SegmentOutput) []Segment {
	out := make([]Segment, 0, len(in))
	for _, segment := range in {
		out = append(out, Segment{
			Start: SecondsToDuration(segment.StartSeconds),
			End:   SecondsToDuration(segment.EndSeconds),
			Text:  segment.Text,
		})
	}
	return out
}

func SecondsToDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

func BuildProviderHealth(snapshot ProviderHealthSnapshot) core.HealthReport {
	status := core.HealthBlocked
	if snapshot.Ready {
		status = core.HealthReady
	}
	return core.HealthReport{
		Module:   "stt",
		Status:   status,
		Ready:    snapshot.Ready,
		Detail:   snapshot.Status,
		Metadata: map[string]any{"provider": snapshot.Provider, "model": snapshot.Model, "device": snapshot.Device},
	}
}

func BuildUnavailableProviderHealth() core.HealthReport {
	return core.HealthReport{Module: "stt", Status: core.HealthDown, Detail: "stt provider is nil"}
}
