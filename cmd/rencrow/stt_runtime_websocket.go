package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	"golang.org/x/net/websocket"
)

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func handleSTTWebSocketProvider(provider sttinfra.Provider) http.Handler {
	return websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		if provider == nil {
			_ = sendSTTError(conn, "stt provider is not configured")
			return
		}
		sendSTTSessionReady(conn, provider.Name())

		autoFinalTimeout := sttFinalTimeoutFromEnv()
		silenceThreshold := sttSilenceAbsThresholdFromEnv()
		draftState := modulestt.DraftState{}
		trace := newSTTTimingTrace("provider")
		for {
			_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			var payload []byte
			if err := websocket.Message.Receive(conn, &payload); err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					if finalText, ok := modulestt.FinalTextAfterDraftTimeout(draftState, time.Now(), autoFinalTimeout); ok {
						if sendErr := sendSTTEvent(conn, modulestt.BuildFinalEvent(finalText)); sendErr == nil {
							trace.logFinal("provider_timeout", "draft_timeout", finalText)
						}
						draftState = modulestt.ResetDraftAfterFinal(draftState, false)
					}
					continue
				}
				return
			}
			if len(payload) == 0 {
				continue
			}
			control, isControl := parseSTTControlMessage(payload)
			if isControl {
				if isSTTFinalControl(control) {
					if finalText, ok := modulestt.FinalTextForPending(draftState); ok {
						if sendErr := sendSTTEvent(conn, modulestt.BuildFinalEvent(finalText)); sendErr == nil {
							trace.logFinal("provider_pending", control, finalText)
						}
						draftState = modulestt.ResetDraftAfterFinal(draftState, false)
					}
				}
				continue
			}
			audioPayload := normalizeSTTAudioPayload(payload)
			now := time.Now()
			trace.markAudio(now)
			if !isLikelySilentWAV(audioPayload, silenceThreshold) {
				trace.markVoice(now)
			}
			if isLikelySilentWAV(audioPayload, silenceThreshold) {
				if finalText, ok := modulestt.FinalTextAfterSilence(draftState, time.Now(), autoFinalTimeout); ok {
					if sendErr := sendSTTEvent(conn, modulestt.BuildFinalEvent(finalText)); sendErr == nil {
						trace.logFinal("provider_silence", "silence_timeout", finalText)
					}
					draftState = modulestt.ResetDraftAfterFinal(draftState, true)
				}
				continue
			}
			draftState = modulestt.MarkVoiceObserved(draftState, now)
			var started bool
			draftState, started = modulestt.MarkSpeechStarted(draftState)
			if started {
				_ = sendSTTEvent(conn, modulestt.BuildSpeechStartEvent())
			}
			result, err := provider.Transcribe(context.Background(), audioPayload)
			if err != nil {
				if finalText, ok := modulestt.FinalTextOnProviderError(draftState); ok {
					if sendErr := sendSTTEvent(conn, modulestt.BuildFinalEvent(finalText)); sendErr == nil {
						trace.logFinal("provider_error", "provider_error", finalText)
					}
					draftState = modulestt.ResetDraftAfterFinal(draftState, true)
					continue
				}
				_ = sendSTTError(conn, "stt inference failed: "+err.Error())
				continue
			}
			if modulestt.IsProviderErrorTranscriptText(result.Text) {
				_ = sendSTTError(conn, modulestt.ProviderTranscriptErrorMessage)
				continue
			}
			normalized := modulestt.NormalizeTranscriptText(result.Text)
			if normalized == "" {
				continue
			}
			draftState = modulestt.ApplyDraftTranscript(draftState, normalized, time.Now())
			trace.markProvisional(time.Now())
			_ = sendSTTEvent(conn, modulestt.BuildDraftEvent(normalized))
		}
	})
}

func sendSTTSessionReady(conn *websocket.Conn, provider string) {
	_ = sendSTTEvent(conn, modulestt.BuildSessionInfoEvent(sttinfra.NextEventID(time.Now()), provider))
	_ = sendSTTEvent(conn, modulestt.BuildReadyEvent())
}

func parseSTTControlMessage(payload []byte) (string, bool) {
	return modulestt.ParseControlMessage(payload)
}

func isSTTFinalControl(control string) bool {
	switch strings.TrimSpace(control) {
	case "final_pending", "stop":
		return true
	default:
		return false
	}
}

func sendSTTEvent(conn *websocket.Conn, event map[string]any) error {
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, string(b))
}

func sendSTTError(conn *websocket.Conn, message string) error {
	return sendSTTEvent(conn, modulestt.BuildErrorEvent(message))
}
