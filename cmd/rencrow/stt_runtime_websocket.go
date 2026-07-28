package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	"golang.org/x/net/websocket"
)

// handleSTTWebSocketBridge は /stt で Viewer と、CORE設定から解決した
// RenCrow_STT Gatewayを中継する。
func handleSTTWebSocketBridge(gatewayURL string) http.Handler {
	return websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		origin := "http://localhost/"
		gw, err := websocket.Dial(gatewayURL, "", origin)
		if err != nil {
			_ = sendSTTError(conn, "RenCrow STT bridge unavailable: "+err.Error())
			return
		}
		defer gw.Close()

		silenceThreshold := sttSilenceAbsThresholdFromEnv()
		var finalMu sync.Mutex
		finalSent := false
		trace := newSTTTimingTrace("bridge")

		isFinalSent := func() bool {
			finalMu.Lock()
			defer finalMu.Unlock()
			return finalSent
		}
		markTerminalSent := func() bool {
			finalMu.Lock()
			if finalSent {
				finalMu.Unlock()
				return false
			}
			finalSent = true
			finalMu.Unlock()
			return true
		}

		errc := make(chan error, 2)
		relayBrowserToGateway := func(src, dst *websocket.Conn) {
			for {
				if isFinalSent() {
					return
				}
				var msg []byte
				if err := websocket.Message.Receive(src, &msg); err != nil {
					errc <- err
					return
				}
				if !isSTTTextFramePayload(msg) {
					now := time.Now()
					trace.markAudio(now)
					if !isLikelySilentWAV(normalizeSTTAudioPayload(msg), silenceThreshold) {
						trace.markVoice(now)
					}
				}
				var sendErr error
				if isSTTTextFramePayload(msg) {
					sendErr = websocket.Message.Send(dst, string(msg))
				} else {
					sendErr = websocket.Message.Send(dst, msg)
				}
				if sendErr != nil {
					errc <- sendErr
					return
				}
			}
		}
		go relayBrowserToGateway(conn, gw) // browser -> RenCrow_STT
		go relaySTTGatewayToBrowser(gw, conn, trace, isFinalSent, markTerminalSent, errc)
		<-errc
	})
}

func relaySTTGatewayToBrowser(src, dst *websocket.Conn, trace *sttTimingTrace, isFinalSent func() bool, markTerminalSent func() bool, errc chan error) {
	for {
		if isFinalSent() {
			return
		}
		var msg []byte
		if err := websocket.Message.Receive(src, &msg); err != nil {
			errc <- err
			return
		}
		if !isSTTTextFramePayload(msg) {
			if err := websocket.Message.Send(dst, msg); err != nil {
				errc <- err
				return
			}
			continue
		}

		transformed, handled := transformSTTGatewayTextFrame(msg)
		if !handled {
			if err := websocket.Message.Send(dst, string(msg)); err != nil {
				errc <- err
				return
			}
			continue
		}
		if transformed == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(transformed), &ev); err == nil {
			if typ, _ := ev["type"].(string); typ == modulestt.WebSocketEventTypeFinal || typ == modulestt.WebSocketEventTypeError {
				text, _ := ev["text"].(string)
				if typ == modulestt.WebSocketEventTypeFinal {
					trace.logFinal("gateway", "gateway_final", text)
				}
				if markTerminalSent() {
					if err := websocket.Message.Send(dst, transformed); err != nil {
						errc <- err
					}
					_ = src.Close()
					_ = dst.Close()
					return
				}
				return
			}
		}
		if evType, _ := ev["type"].(string); evType == modulestt.WebSocketEventTypePartial || evType == modulestt.WebSocketEventTypeDraft {
			trace.markProvisional(time.Now())
		}
		if err := websocket.Message.Send(dst, transformed); err != nil {
			errc <- err
			return
		}
	}
}

func transformSTTGatewayTextFrame(payload []byte) (string, bool) {
	var ev map[string]any
	if err := json.Unmarshal(payload, &ev); err != nil {
		return "", false
	}
	evType, _ := ev["type"].(string)
	if evType == "" {
		return "", false
	}

	text := ""
	if raw, ok := ev["text"].(string); ok {
		if modulestt.IsProviderErrorTranscriptText(raw) {
			return mustJSON(modulestt.BuildErrorEvent(modulestt.ProviderTranscriptErrorMessage)), true
		}
		text = modulestt.NormalizeTranscriptText(raw)
	}

	if evType != modulestt.WebSocketEventTypePartial && evType != modulestt.WebSocketEventTypeDraft && evType != modulestt.WebSocketEventTypeFinal {
		if text != "" {
			ev["text"] = text
			return mustJSON(ev), true
		}
		return "", false
	}

	if evType == modulestt.WebSocketEventTypeFinal && text == "" {
		return mustJSON(modulestt.BuildErrorEvent(modulestt.ProviderTranscriptErrorMessage)), true
	}

	if text == "" {
		return "", true
	}
	ev["text"] = text
	return mustJSON(ev), true
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func isSTTTextFramePayload(payload []byte) bool {
	return modulestt.IsWebSocketTextFramePayload(payload)
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
