package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/voiceinput"
	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
	"golang.org/x/net/websocket"
)

func registerVoiceChatRoutes(mux *http.ServeMux, handler http.Handler) {
	if mux == nil || handler == nil {
		return
	}
	for _, path := range modulevoicechat.WebSocketRoutePaths {
		mux.Handle(path, handler)
	}
}

func resolveVoiceChatWebSocketHandler(plan modulevoicechat.BridgePlan, inputAudio voiceChatInputAudioSettings, voiceDirect voiceDirectFinalHandler, idleNotifier orchestrator.IdleNotifier) http.Handler {
	switch {
	case plan.Disabled:
		return handleVoiceChatDisabled()
	case !plan.Available:
		return handleVoiceChatUnavailable()
	default:
		if voiceChatHTTPBaseURLFromGateway(plan.GatewayURL) != "" {
			return handleVoiceChatInputAudioBridge(plan.GatewayURL, inputAudio, voiceDirect, idleNotifier)
		}
		return handleVoiceChatWebSocketBridge(plan.GatewayURL, voiceDirect, idleNotifier)
	}
}

func handleVoiceChatDisabled() http.Handler {
	return voiceChatWebSocketHandler(func(conn *websocket.Conn) {
		defer conn.Close()
		_ = sendVoiceChatError(conn, modulevoicechat.ErrorVoiceChatDisabled, "voice chat is disabled")
	})
}

func handleVoiceChatUnavailable() http.Handler {
	return voiceChatWebSocketHandler(func(conn *websocket.Conn) {
		defer conn.Close()
		_ = sendVoiceChatError(conn, modulevoicechat.ErrorLLMSessionUnavailable, "voice chat gateway is not configured")
	})
}

func handleVoiceChatWebSocketBridge(gatewayURL string, voiceDirect voiceDirectFinalHandler, idleNotifier orchestrator.IdleNotifier) http.Handler {
	return voiceChatWebSocketHandler(func(conn *websocket.Conn) {
		defer conn.Close()
		viewerClientID := voiceChatViewerClientID(conn)
		log.Printf("[voice-chat] viewer connected viewer_client_id=%s gateway=%s", viewerClientID, gatewayURL)
		origin := "http://localhost/"
		gw, err := websocket.Dial(gatewayURL, "", origin)
		if err != nil {
			log.Printf("[voice-chat] gateway dial failed viewer_client_id=%s gateway=%s err=%v", viewerClientID, gatewayURL, err)
			_ = sendVoiceChatError(conn, modulevoicechat.ErrorLLMSessionUnavailable, "RenCrow LLM voice bridge unavailable: "+err.Error())
			return
		}
		defer gw.Close()
		log.Printf("[voice-chat] gateway connected viewer_client_id=%s", viewerClientID)

		tracker := newVoiceChatBridgeTracker(voiceDirect, idleNotifier)
		defer tracker.reset()
		errc := make(chan error, 2)
		timing := &voiceChatRelayTiming{}
		go relayVoiceChatFrames(conn, gw, tracker, timing, true, viewerClientID, errc)
		go relayVoiceChatFrames(gw, conn, tracker, timing, false, viewerClientID, errc)
		err = <-errc
		log.Printf("[voice-chat] bridge closed viewer_client_id=%s err=%v", viewerClientID, err)
	})
}

func voiceChatWebSocketHandler(handler websocket.Handler) http.Handler {
	return websocket.Server{
		Handler: handler,
		Handshake: func(config *websocket.Config, req *http.Request) error {
			origin, err := websocket.Origin(config, req)
			if err != nil {
				return err
			}
			config.Origin = origin
			return nil
		},
	}
}

type voiceChatRelayTiming struct {
	mu        sync.Mutex
	commitIn  time.Time
	commitOut time.Time
}

func (t *voiceChatRelayTiming) markCommitIn(at time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commitIn = at
	t.commitOut = time.Time{}
}

func (t *voiceChatRelayTiming) markCommitOut(at time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commitOut = at
}

func (t *voiceChatRelayTiming) snapshot() (time.Time, time.Time) {
	if t == nil {
		return time.Time{}, time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commitIn, t.commitOut
}

func relayVoiceChatFrames(src, dst *websocket.Conn, tracker *voiceChatBridgeTracker, timing *voiceChatRelayTiming, fromClient bool, viewerClientID string, errc chan error) {
	direction := "gateway_to_viewer"
	if fromClient {
		direction = "viewer_to_gateway"
	}
	binaryFrames := 0
	binaryBytes := 0
	forwardedDelta := false
	for {
		var msg []byte
		if err := websocket.Message.Receive(src, &msg); err != nil {
			log.Printf("[voice-chat] relay receive closed direction=%s viewer_client_id=%s binary_frames=%d binary_bytes=%d err=%v", direction, viewerClientID, binaryFrames, binaryBytes, err)
			errc <- err
			return
		}
		eventType := ""
		receivedAt := time.Now()
		if modulevoicechat.IsWebSocketTextFramePayload(msg) {
			eventType = voiceChatTextFrameType(msg)
		}
		if fromClient && eventType == modulevoicechat.EventSessionCommit {
			timing.markCommitIn(receivedAt)
		}
		if !fromClient && eventType == modulevoicechat.EventLLMFinal {
			msg = annotateVoiceChatFinalMetrics(msg, timing, receivedAt)
			var transcriptText string
			msg, transcriptText = splitVoiceChatStructuredFinal(msg)
			if transcriptText != "" {
				if sendErr := sendVoiceChatTranscriptFinal(dst, msg, transcriptText); sendErr != nil {
					log.Printf("[voice-chat] relay transcript send failed direction=%s viewer_client_id=%s err=%v", direction, viewerClientID, sendErr)
					errc <- sendErr
					return
				}
			}
		}
		observeGatewayAfterSend := false
		if tracker != nil && modulevoicechat.IsWebSocketTextFramePayload(msg) {
			logVoiceChatTextFrame(direction, viewerClientID, msg)
			if fromClient {
				tracker.observeClientText(msg)
			} else {
				if eventType == modulevoicechat.EventSessionProgress {
					continue
				}
				if eventType == modulevoicechat.EventLLMDelta {
					if forwardedDelta {
						continue
					}
					forwardedDelta = true
				}
				if eventType == modulevoicechat.EventLLMFinal {
					observeGatewayAfterSend = true
				} else {
					tracker.observeGatewayText(msg)
				}
			}
		} else if tracker == nil && !fromClient && modulevoicechat.IsWebSocketTextFramePayload(msg) {
			eventType := voiceChatTextFrameType(msg)
			if eventType == modulevoicechat.EventSessionProgress {
				continue
			}
			if eventType == modulevoicechat.EventLLMDelta {
				if forwardedDelta {
					continue
				}
				forwardedDelta = true
			}
		} else if !modulevoicechat.IsWebSocketTextFramePayload(msg) {
			binaryFrames++
			binaryBytes += len(msg)
			if binaryFrames == 1 || binaryFrames%50 == 0 {
				log.Printf("[voice-chat] binary relay direction=%s viewer_client_id=%s frames=%d bytes=%d last_bytes=%d", direction, viewerClientID, binaryFrames, binaryBytes, len(msg))
			}
		}
		var sendErr error
		if modulevoicechat.IsWebSocketTextFramePayload(msg) {
			sendErr = websocket.Message.Send(dst, string(msg))
		} else {
			sendErr = websocket.Message.Send(dst, msg)
		}
		if sendErr != nil {
			log.Printf("[voice-chat] relay send failed direction=%s viewer_client_id=%s binary_frames=%d binary_bytes=%d err=%v", direction, viewerClientID, binaryFrames, binaryBytes, sendErr)
			errc <- sendErr
			return
		}
		if fromClient && eventType == modulevoicechat.EventSessionCommit {
			timing.markCommitOut(time.Now())
		}
		if observeGatewayAfterSend {
			tracker.observeGatewayText(msg)
		}
	}
}

func splitVoiceChatStructuredFinal(msg []byte) ([]byte, string) {
	var ev map[string]any
	if err := json.Unmarshal(msg, &ev); err != nil {
		return msg, ""
	}
	text := strings.TrimSpace(stringField(ev, "text"))
	if text == "" {
		return msg, ""
	}
	reply, transcript := voiceinput.SplitStructuredText(text)
	if strings.TrimSpace(reply) == "" || strings.TrimSpace(transcript) == "" {
		return msg, ""
	}
	ev["text"] = reply
	ev["user_text"] = transcript
	updated, err := json.Marshal(ev)
	if err != nil {
		return msg, ""
	}
	return updated, transcript
}

func sendVoiceChatTranscriptFinal(dst *websocket.Conn, finalMsg []byte, transcriptText string) error {
	var ev map[string]any
	if err := json.Unmarshal(finalMsg, &ev); err != nil {
		return err
	}
	payload := map[string]any{
		"type":         "transcript.final",
		"utterance_id": stringField(ev, "utterance_id"),
		"session_id":   stringField(ev, "session_id"),
		"text":         strings.TrimSpace(transcriptText),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return websocket.Message.Send(dst, string(data))
}

func annotateVoiceChatFinalMetrics(msg []byte, timing *voiceChatRelayTiming, finalIn time.Time) []byte {
	var ev map[string]any
	if err := json.Unmarshal(msg, &ev); err != nil {
		return msg
	}
	metrics, _ := ev["metrics"].(map[string]any)
	if metrics == nil {
		metrics = map[string]any{}
	}
	commitIn, commitOut := timing.snapshot()
	metrics["rencrow_final_recv_unix_ms"] = finalIn.UnixMilli()
	if !commitIn.IsZero() {
		metrics["rencrow_commit_recv_unix_ms"] = commitIn.UnixMilli()
		metrics["rencrow_commit_recv_to_final_recv_ms"] = roundDurationMillis(finalIn.Sub(commitIn))
	}
	if !commitOut.IsZero() {
		metrics["rencrow_commit_sent_unix_ms"] = commitOut.UnixMilli()
		metrics["rencrow_commit_sent_to_final_recv_ms"] = roundDurationMillis(finalIn.Sub(commitOut))
		if !commitIn.IsZero() {
			metrics["rencrow_commit_recv_to_sent_ms"] = roundDurationMillis(commitOut.Sub(commitIn))
		}
	}
	ev["metrics"] = metrics
	annotated, err := json.Marshal(ev)
	if err != nil {
		return msg
	}
	return annotated
}

func roundDurationMillis(d time.Duration) float64 {
	return float64(d.Round(100*time.Microsecond)) / float64(time.Millisecond)
}

func voiceChatTextFrameType(msg []byte) string {
	var ev map[string]any
	if err := json.Unmarshal(msg, &ev); err != nil {
		return ""
	}
	eventType, _ := ev["type"].(string)
	return eventType
}

func voiceChatViewerClientID(conn *websocket.Conn) string {
	if conn == nil || conn.Request() == nil || conn.Request().URL == nil {
		return ""
	}
	return strings.TrimSpace(conn.Request().URL.Query().Get("viewer_client_id"))
}

func logVoiceChatTextFrame(direction, viewerClientID string, msg []byte) {
	var ev map[string]any
	if err := json.Unmarshal(msg, &ev); err != nil {
		log.Printf("[voice-chat] text relay direction=%s viewer_client_id=%s invalid_json bytes=%d", direction, viewerClientID, len(msg))
		return
	}
	eventType, _ := ev["type"].(string)
	if eventType == modulevoicechat.EventSessionProgress || eventType == modulevoicechat.EventLLMDelta {
		return
	}
	utteranceID, _ := ev["utterance_id"].(string)
	sessionID, _ := ev["session_id"].(string)
	text := voiceChatFirstNonEmpty(stringField(ev, "text"), stringField(ev, "message"), stringField(ev, "error_code"))
	log.Printf(
		"[voice-chat] text relay direction=%s viewer_client_id=%s type=%s utterance_id=%s session_id=%s text_len=%d text_sample=%q",
		direction,
		viewerClientID,
		eventType,
		strings.TrimSpace(utteranceID),
		strings.TrimSpace(sessionID),
		len([]rune(text)),
		voiceChatShortLogText(text, 120),
	)
}

func voiceChatShortLogText(text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func sendVoiceChatError(conn *websocket.Conn, errorCode, message string) error {
	payload := map[string]string{
		"type":       modulevoicechat.EventError,
		"error_code": errorCode,
		"message":    message,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, string(data))
}

func isVoiceChatTextFramePayload(payload []byte) bool {
	return modulevoicechat.IsWebSocketTextFramePayload(payload)
}
