package main

import (
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func markIdleChatTTSTimeout(ev idlechat.TTSTimeoutEvent) {
	consumption := ttsPublicSessions.MarkTimeout(moduletts.PlaybackTimeoutInput{
		Kind:           ev.Kind,
		SessionID:      ev.SessionID,
		MessageID:      ev.MessageID,
		TurnIndex:      ev.TurnIndex,
		RemainingIndex: ev.RemainingIndex,
		RemainingCount: ev.RemainingCount,
	})
	if consumption.SessionID == "" {
		return
	}
	for _, internalSessionID := range consumption.MatchedInternalSessionIDs {
		clearIdleChatTTSPending(internalSessionID)
	}
	if consumption.AllForSession {
		log.Printf("[IdleChat] consumed pending TTS session audio timeout: session=%s matched=%d remaining_index=%d/%d", consumption.SessionID, consumption.MatchedCount, consumption.RemainingIndex, consumption.RemainingCount)
		return
	}
	log.Printf("[IdleChat] consumed pending TTS utterance timeout: session=%s message_id=%s turn_index=%d matched=%d", consumption.SessionID, consumption.MessageID, consumption.TurnIndex, consumption.MatchedCount)
}
