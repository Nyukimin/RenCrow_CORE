package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	authToken, err := readTTSOwnerToken(cfg.TTS.AuthTokenFile)
	if err != nil {
		log.Printf("WARN: TTS owner authentication unavailable: %v", err)
		return nil
	}
	cmds := buildTTSCommandSpecs(cfg)
	var sessionResponseIDs sync.Map

	sink := ttsinfra.AudioSink(ttsinfra.NewNoopAudioSink())
	if len(cmds) == 0 {
		log.Printf("TTS browser-only mode enabled (local playback disabled)")
	} else {
		player := ttsinfra.NewCommandPlayer(cmds)
		sink = ttsinfra.NewAsyncAudioSink(ttsinfra.NewPlaybackAudioSink(player, ""))
	}
	onChunkFn := func(sessionID, responseID, traceID string, chunkIndex int, characterID, text, displayText, audioPath, audioURL string) {
		if isStaleTTSPublicSession(sessionID) {
			log.Printf("[TTS] dropping stale idlechat chunk session=%s response=%s chunk=%d", sessionID, responseID, chunkIndex)
			return
		}
		notifyIdleChatTTSSynthesisReady(sessionID)
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
			onChunk(orchestrator.NewEventWithTraceID(modulecore.TraceID(traceID), "metrics.latency", "metrics", "viewer", string(metricJSON), "TTS", payload.ResponseID, payload.SessionID, route.Channel, route.ChatID))
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			log.Printf("WARN: tts chunk payload marshal failed: %v", err)
			return
		}
		route := moduletts.PlaybackEventRouteForSession(payload.SessionID)
		event := orchestrator.NewEventWithTraceID(modulecore.TraceID(traceID), "tts.audio_chunk", "tts", "user", string(payloadJSON), "TTS", payload.ResponseID, payload.SessionID, route.Channel, route.ChatID)
		event.MessageID = payload.MessageID
		event.TurnIndex = payload.TurnIndex
		onChunk(event)
	}
	onSessionDoneFn := func(sessionID, traceID, characterID string) {
		if isStaleTTSPublicSession(sessionID) {
			log.Printf("[TTS] dropping stale idlechat completion session=%s", sessionID)
			retireTTSPublicSession(sessionID)
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
				event := orchestrator.NewEventWithTraceID(modulecore.TraceID(traceID), "tts.session_completed", "tts", "user", string(payloadJSON), "TTS", payload.ResponseID, payload.SessionID, route.Channel, route.ChatID)
				event.MessageID = payload.MessageID
				event.TurnIndex = payload.TurnIndex
				onChunk(event)
			}
		}
		if onSessionCompleted != nil {
			onSessionCompleted(payload.SessionID, payload.CharacterID)
		}
	}
	gatewayBaseURL := cfg.TTS.GatewayURL()
	bridge := ttsinfra.NewRenCrowTTSBridge(ttsinfra.RenCrowTTSBridgeConfig{
		HTTPBaseURL:        gatewayBaseURL,
		AuthToken:          authToken,
		OutputDir:          cfg.TTS.OutputDir,
		VoiceID:            cfg.TTS.VoiceID,
		Speed:              cfg.TTS.Speed,
		TLSSkipVerify:      cfg.TTS.TLSSkipVerify,
		RequestTimeout:     time.Duration(cfg.TTS.TimeoutMS) * time.Millisecond,
		DownloadAudio:      len(cmds) > 0,
		Sink:               sink,
		OnChunkReady:       onChunkFn,
		OnSessionCompleted: onSessionDoneFn,
	})
	log.Printf("TTS RenCrow_TTS Gateway bridge enabled (/api/tts base=%s)", gatewayBaseURL)
	return bridge
}

func readTTSOwnerToken(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("TTS auth token file must be a regular non-symlink file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("TTS auth token file must be owner-only")
	}
	if info.Size() <= 0 || info.Size() > 64<<10 {
		return "", fmt.Errorf("TTS auth token file must contain one bounded token")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return "", fmt.Errorf("read TTS auth token: %w", err)
	}
	if len(data) > 64<<10 {
		return "", fmt.Errorf("TTS auth token exceeds bounded size")
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("TTS auth token must contain one non-empty line")
	}
	return token, nil
}
