package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func buildTTSClientBridge(
	cfg *config.Config,
	onChunk func(ev orchestrator.OrchestratorEvent),
	onChunkReady func(sessionID, characterID, text string),
	onSessionCompleted func(sessionID, characterID string),
) orchestrator.TTSBridge {
	if cfg == nil || !cfg.TTS.Enabled {
		return nil
	}
	cmds := buildTTSCommandSpecs(cfg)
	var sessionResponseIDs sync.Map

	sink := ttsinfra.AudioSink(ttsinfra.NewNoopAudioSink())
	if len(cmds) == 0 {
		log.Printf("TTS browser-only mode enabled (local playback disabled)")
	} else {
		player := ttsinfra.NewCommandPlayer(cmds)
		sink = ttsinfra.NewAsyncAudioSink(ttsinfra.NewPlaybackAudioSink(player, cfg.TTS.AudioPathRoot))
	}
	onChunkFn := func(sessionID, responseID string, chunkIndex int, characterID, text, displayText, audioPath, audioURL string) {
		if isStaleTTSPublicSession(sessionID) {
			log.Printf("[TTS] dropping stale idlechat chunk session=%s response=%s chunk=%d", sessionID, responseID, chunkIndex)
			return
		}
		publicSessionID, publicChunkIndex := resolveTTSPublicChunk(sessionID, chunkIndex)
		if normalizedResponseID := strings.TrimSpace(responseID); normalizedResponseID != "" {
			sessionResponseIDs.Store(sessionID, normalizedResponseID)
		}
		messageID, turnIndex, utteranceID := resolveTTSPublicMessage(sessionID)
		if utteranceID == "" {
			utteranceID = fmt.Sprintf("%s:%04d", publicSessionID, publicChunkIndex)
		}
		payload := moduletts.BuildAudioChunkEventPayload(moduletts.AudioChunkEventPayloadInput{
			SessionID:   publicSessionID,
			ResponseID:  responseID,
			MessageID:   messageID,
			TurnIndex:   turnIndex,
			UtteranceID: utteranceID,
			ChunkIndex:  publicChunkIndex,
			CharacterID: characterID,
			SpeechText:  text,
			DisplayText: displayText,
			AudioPath:   audioPath,
			AudioURL:    audioURL,
		})
		if onChunkReady != nil {
			onChunkReady(payload.SessionID, payload.CharacterID, payload.DisplayText)
		}
		if onChunk == nil {
			return
		}
		metricJSON, metricErr := json.Marshal(map[string]any{
			"kind":        "tts",
			"point":       "audio_chunk_ready",
			"at_unix_ms":  time.Now().UnixMilli(),
			"detail":      fmt.Sprintf("chunk=%d text_len=%d", payload.ChunkIndex, len(payload.DisplayText)),
			"chunk_index": payload.ChunkIndex,
		})
		if metricErr == nil {
			route := moduletts.PlaybackEventRouteForSession(payload.SessionID)
			onChunk(orchestrator.NewEvent("metrics.latency", "metrics", "viewer", string(metricJSON), "TTS", payload.ResponseID, payload.SessionID, route.Channel, route.ChatID))
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			log.Printf("WARN: tts chunk payload marshal failed: %v", err)
			return
		}
		route := moduletts.PlaybackEventRouteForSession(payload.SessionID)
		onChunk(orchestrator.NewEvent("tts.audio_chunk", "tts", "user", string(payloadJSON), "TTS", "", payload.SessionID, route.Channel, route.ChatID))
	}
	onSessionDoneFn := func(sessionID, characterID string) {
		if isStaleTTSPublicSession(sessionID) {
			log.Printf("[TTS] dropping stale idlechat completion session=%s", sessionID)
			clearTTSPublicSession(sessionID)
			return
		}
		publicSessionID := resolveTTSPublicSession(sessionID)
		responseID := resolveTTSPublicResponse(sessionID)
		if responseID == "" {
			if remembered, ok := sessionResponseIDs.Load(sessionID); ok {
				responseID = strings.TrimSpace(remembered.(string))
			}
		}
		sessionResponseIDs.Delete(sessionID)
		messageID, turnIndex, utteranceID := resolveTTSPublicMessage(sessionID)
		payload := moduletts.BuildSessionCompletedEventPayload(moduletts.SessionCompletedEventPayloadInput{
			SessionID:   publicSessionID,
			ResponseID:  responseID,
			MessageID:   messageID,
			TurnIndex:   turnIndex,
			UtteranceID: utteranceID,
			CharacterID: characterID,
		})
		if onChunk != nil {
			payloadJSON, err := json.Marshal(payload)
			if err != nil {
				log.Printf("WARN: tts session completed payload marshal failed: %v", err)
			} else {
				route := moduletts.PlaybackEventRouteForSession(payload.SessionID)
				onChunk(orchestrator.NewEvent("tts.session_completed", "tts", "user", string(payloadJSON), "TTS", "", payload.SessionID, route.Channel, route.ChatID))
			}
		}
		if onSessionCompleted != nil {
			onSessionCompleted(payload.SessionID, payload.CharacterID)
		}
	}
	if sel, ok := buildPrimaryTTSProvider(cfg); ok {
		logTTSProviderSelection(sel)
		return ttsinfra.NewProviderTTSBridge(ttsinfra.ProviderTTSBridgeConfig{
			Provider:           sel.Provider,
			Sink:               sink,
			OutputDir:          cfg.TTS.OutputDir,
			HTTPBaseURL:        cfg.TTS.HTTPBaseURL,
			OnChunkReady:       onChunkFn,
			OnSessionCompleted: onSessionDoneFn,
		})
	}
	bridge := ttsinfra.NewRenCrowTTSBridge(ttsinfra.RenCrowTTSBridgeConfig{
		HTTPBaseURL:        cfg.TTS.HTTPBaseURL,
		OutputDir:          cfg.TTS.OutputDir,
		VoiceID:            cfg.TTS.VoiceID,
		Speed:              cfg.TTS.Speed,
		TLSSkipVerify:      cfg.TTS.TLSSkipVerify,
		RequestTimeout:     time.Duration(cfg.TTS.TimeoutMS) * time.Millisecond,
		ProviderParams:     cfg.TTS.ProviderParams,
		Sink:               sink,
		OnChunkReady:       onChunkFn,
		OnSessionCompleted: onSessionDoneFn,
	})
	log.Printf("TTS RenCrow bridge enabled (/synthesis base=%s)", cfg.TTS.HTTPBaseURL)
	return bridge
}
