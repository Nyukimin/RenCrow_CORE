package tts

import (
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

func BuildSynthesisResult(req SynthesisRequest, output SynthesisOutput) SynthesisResult {
	return SynthesisResult{
		Chunks: []AudioChunk{
			{
				Ref: core.ChunkRef{
					SessionID:   req.SessionID,
					ResponseID:  req.ResponseID,
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
