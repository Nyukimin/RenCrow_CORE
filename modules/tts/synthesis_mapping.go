package tts

import (
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func BuildSynthesisResult(req SynthesisRequest, output SynthesisOutput) SynthesisResult {
	return SynthesisResult{
		RequestID:  output.RequestID,
		ResponseID: output.ResponseID,
		Chunks: []AudioChunk{
			{
				Ref: core.ChunkRef{
					SessionID:   req.SessionID,
					ResponseID:  output.ResponseID,
					UtteranceID: req.UtteranceID,
					MessageID:   "",
				},
				CharacterID: req.CharacterID,
				SpeechText:  req.SpeechText,
				DisplayText: req.DisplayText,
				AudioPath:   output.AudioPath,
				AudioURL:    output.AudioURL,
				Duration:    time.Duration(output.DurationMS) * time.Millisecond,
			},
		},
	}
}
