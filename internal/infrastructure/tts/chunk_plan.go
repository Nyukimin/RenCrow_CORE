package tts

import (
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type ttsChunkPlanItem struct {
	SpeechText  string
	DisplayText string
}

func planTTSChunks(speechText, displayText string) []ttsChunkPlanItem {
	originalSpeech := strings.TrimSpace(speechText)
	rawSpeech := moduletts.FormatTTSSpeechPlainText(speechText)
	if rawSpeech == "" {
		return nil
	}
	speechChunks := orchestrator.SplitTTSChunks(rawSpeech)
	if len(speechChunks) == 0 {
		return nil
	}
	plan := make([]ttsChunkPlanItem, 0, len(speechChunks))
	for i, chunk := range speechChunks {
		speechChunk := ensureTTSPunctuation(chunk)
		if speechChunk == "" {
			continue
		}
		displayChunk := displayChunkForSpeechChunk(originalSpeech, displayText, speechChunks, i)
		if strings.TrimSpace(displayText) == "" {
			displayChunk = speechChunk
		} else if strings.TrimSpace(displayText) == originalSpeech && len(speechChunks) == 1 {
			displayChunk = strings.TrimSpace(displayText)
		}
		plan = append(plan, ttsChunkPlanItem{
			SpeechText:  speechChunk,
			DisplayText: displayChunk,
		})
	}
	return plan
}

func displayChunkForSpeechChunk(rawSpeechText, rawDisplayText string, speechChunks []string, index int) string {
	if index < 0 || index >= len(speechChunks) {
		return ""
	}
	chunkText := strings.TrimSpace(speechChunks[index])
	displayText := strings.TrimSpace(rawDisplayText)
	speechText := strings.TrimSpace(rawSpeechText)
	if displayText == "" || displayText == speechText {
		return chunkText
	}
	if len(speechChunks) == 1 {
		return displayText
	}
	// Do not independently split display text and speech text; that creates
	// false chunk-index correspondence when the two strings have different
	// boundaries. In multi-chunk pronunciation-normalized cases, keep the
	// diagnostic display chunk tied to the speech chunk.
	return chunkText
}
