package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func buildTTSClientBridge(
	cfg *config.Config,
	audioAccessOwner *ttsAudioAccessOwner,
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
	var sessionPublicPlaybackRefs sync.Map

	sink := ttsinfra.AudioSink(ttsinfra.NewNoopAudioSink())
	if len(cmds) == 0 {
		log.Printf("TTS browser-only mode enabled (local playback disabled)")
	} else {
		player := ttsinfra.NewCommandPlayer(cmds)
		sink = ttsinfra.NewAsyncAudioSink(ttsinfra.NewPlaybackAudioSink(player, ""))
	}
	publishChunk := func(sessionID, canonicalSessionID, publicPlaybackRef, traceID string, chunkIndex int, characterID, text, displayText, audioPath, audioURL, turnID string, pauseAfterMS int, playbackOutput string, binding conversation.SessionBinding) error {
		if isStaleTTSPublicSession(sessionID) {
			log.Printf("[TTS] dropping stale idlechat chunk session=%s public_playback_ref=%s chunk=%d", sessionID, publicPlaybackRef, chunkIndex)
			return nil
		}
		registrationAuthority := "canonical Session"
		var registrationErr error
		if strings.TrimSpace(canonicalSessionID) == "" {
			registrationAuthority = "IdleChat public route"
			registrationErr = registerTTSPublicAudioResource(audioAccessOwner, sessionID, audioPath, audioURL)
		} else {
			if string(binding.SessionID()) != strings.TrimSpace(canonicalSessionID) {
				registrationErr = errTTSAudioAccessRegistration
			} else {
				registrationErr = registerTTSAudioResource(audioAccessOwner, binding, audioPath, audioURL)
			}
		}
		if registrationErr != nil {
			log.Printf("[TTS] dropping audio chunk because its %s resource could not be registered session=%s canonical_session=%s chunk=%d: %v", registrationAuthority, sessionID, canonicalSessionID, chunkIndex, registrationErr)
			return fmt.Errorf("register TTS audio resource for %s: %w", registrationAuthority, registrationErr)
		}
		notifyIdleChatTTSSynthesisReady(sessionID)
		publicSessionID, publicChunkIndex := resolveTTSPublicChunk(sessionID, chunkIndex)
		if normalizedPublicPlaybackRef := strings.TrimSpace(publicPlaybackRef); normalizedPublicPlaybackRef != "" {
			sessionPublicPlaybackRefs.Store(sessionID, normalizedPublicPlaybackRef)
		}
		messageID, turnIndex, utteranceID := resolveTTSPublicMessage(sessionID)
		if utteranceID == "" {
			utteranceID = fmt.Sprintf("%s:%04d", publicSessionID, publicChunkIndex)
		}
		payload := moduletts.BuildAudioChunkEventPayload(moduletts.AudioChunkEventPayloadInput{
			SessionID:         publicSessionID,
			PublicPlaybackRef: publicPlaybackRef,
			MessageID:         messageID,
			TurnID:            turnID,
			PlaybackOutput:    playbackOutput,
			TurnIndex:         turnIndex,
			UtteranceID:       utteranceID,
			ChunkIndex:        publicChunkIndex,
			PauseAfterMS:      pauseAfterMS,
			CharacterID:       characterID,
			SpeechText:        text,
			DisplayText:       displayText,
			AudioPath:         audioPath,
			AudioURL:          audioURL,
		})
		if onChunkReady != nil {
			onChunkReady(payload.SessionID, payload.CharacterID, payload.DisplayText)
		}
		if onChunk == nil {
			return nil
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
			metric := orchestrator.NewEventWithTraceID(modulecore.TraceID(traceID), "metrics.latency", "metrics", "viewer", string(metricJSON), "TTS", canonicalTaskIDText(payload.PublicPlaybackRef), canonicalSessionIDText(payload.SessionID), route.Channel, route.SessionID)
			stampTTSEventWithSessionBinding(&metric, binding)
			onChunk(metric)
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			log.Printf("WARN: tts chunk payload marshal failed: %v", err)
			return fmt.Errorf("marshal TTS audio chunk payload: %w", err)
		}
		route := moduletts.PlaybackEventRouteForSession(payload.SessionID)
		event := orchestrator.NewEventWithTraceID(modulecore.TraceID(traceID), "tts.audio_chunk", "tts", "user", string(payloadJSON), "TTS", canonicalTaskIDText(payload.PublicPlaybackRef), canonicalSessionIDText(payload.SessionID), route.Channel, route.SessionID)
		stampTTSEventWithSessionBinding(&event, binding)
		event.MessageID = modulecore.MessageID(payload.MessageID)
		event.TurnIndex = payload.TurnIndex
		onChunk(event)
		return nil
	}
	onChunkFn := func(sessionID, canonicalSessionID, publicPlaybackRef, traceID string, chunkIndex int, characterID, text, displayText, audioPath, audioURL string, binding conversation.SessionBinding) error {
		return publishChunk(sessionID, canonicalSessionID, publicPlaybackRef, traceID, chunkIndex, characterID, text, displayText, audioPath, audioURL, "", 0, "", binding)
	}
	onSpeechChunkFn := func(chunk ttsinfra.SpeechPlanChunkReady) error {
		return publishChunk(chunk.TTSSessionID, chunk.ConversationSessionID, chunk.PublicPlaybackRef, chunk.TraceID, chunk.UtteranceIndex, chunk.SpeakerID, chunk.SpeechText, chunk.DisplayText, chunk.AudioPath, chunk.AudioURL, chunk.TurnID, chunk.PauseAfterMS, chunk.PlaybackOutput, chunk.SessionBinding)
	}
	onSessionDoneFn := func(sessionID, traceID, characterID string, binding conversation.SessionBinding) {
		if isStaleTTSPublicSession(sessionID) {
			log.Printf("[TTS] dropping stale idlechat completion session=%s", sessionID)
			retireTTSPublicSession(sessionID)
			return
		}
		publicSessionID := resolveTTSPublicSession(sessionID)
		publicPlaybackRef := resolveTTSPublicPlaybackRef(sessionID)
		if publicPlaybackRef == "" {
			if remembered, ok := sessionPublicPlaybackRefs.Load(sessionID); ok {
				publicPlaybackRef = strings.TrimSpace(remembered.(string))
			}
		}
		sessionPublicPlaybackRefs.Delete(sessionID)
		messageID, turnIndex, utteranceID := resolveTTSPublicMessage(sessionID)
		payload := moduletts.BuildSessionCompletedEventPayload(moduletts.SessionCompletedEventPayloadInput{
			SessionID:         publicSessionID,
			PublicPlaybackRef: publicPlaybackRef,
			MessageID:         messageID,
			TurnIndex:         turnIndex,
			UtteranceID:       utteranceID,
			CharacterID:       characterID,
		})
		if onChunk != nil {
			payloadJSON, err := json.Marshal(payload)
			if err != nil {
				log.Printf("WARN: tts session completed payload marshal failed: %v", err)
			} else {
				route := moduletts.PlaybackEventRouteForSession(payload.SessionID)
				event := orchestrator.NewEventWithTraceID(modulecore.TraceID(traceID), "tts.session_completed", "tts", "user", string(payloadJSON), "TTS", canonicalTaskIDText(payload.PublicPlaybackRef), canonicalSessionIDText(payload.SessionID), route.Channel, route.SessionID)
				stampTTSEventWithSessionBinding(&event, binding)
				event.MessageID = modulecore.MessageID(payload.MessageID)
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
		OnSpeechChunkReady: onSpeechChunkFn,
		OnSessionCompleted: onSessionDoneFn,
	})
	log.Printf("TTS RenCrow_TTS Gateway bridge enabled (/api/tts base=%s)", gatewayBaseURL)
	return bridge
}

func registerTTSAudioResource(owner *ttsAudioAccessOwner, binding conversation.SessionBinding, audioPath, audioURL string) error {
	if owner == nil {
		return fmt.Errorf("TTS audio access owner is unavailable")
	}
	audioURL = strings.TrimSpace(audioURL)
	if audioURL != "" {
		parsed, err := url.Parse(audioURL)
		if err != nil {
			return fmt.Errorf("parse TTS audio URL: %w", err)
		}
		if parsed.IsAbs() {
			return owner.RegisterURL(binding, audioURL)
		}
	}
	audioPath = strings.TrimSpace(audioPath)
	if audioPath == "" {
		return fmt.Errorf("TTS audio resource is unavailable")
	}
	return owner.RegisterPath(binding, audioPath)
}

func stampTTSEventWithSessionBinding(event *orchestrator.OrchestratorEvent, binding conversation.SessionBinding) {
	if event == nil || binding.ValidateForPublication() != nil || binding.BindingGeneration() == conversation.BindingGenerationUnavailable {
		return
	}
	address := binding.ChannelAddress()
	event.SessionID = binding.SessionID()
	event.PublicationIncarnation = binding.PublicationIncarnation()
	event.Channel = address.ChannelType()
	event.ChannelAddress = address.ExternalConversationID()
	event.Visibility = string(binding.Audience())
}

// canonicalTaskIDText preserves the TaskID text supplied by normal TTS callers.
// Opaque IdleChat playback refs remain in the payload, outside Event TaskID.
func canonicalTaskIDText(publicPlaybackRef string) string {
	taskID, err := modulecore.ParseTaskID(strings.TrimSpace(publicPlaybackRef))
	if err != nil {
		return ""
	}
	return taskID.String()
}

func canonicalSessionIDText(sessionID string) string {
	value := modulecore.SessionID(strings.TrimSpace(sessionID))
	if value.Validate() != nil {
		return ""
	}
	return string(value)
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
