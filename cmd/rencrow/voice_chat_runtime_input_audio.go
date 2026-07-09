package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
	"golang.org/x/net/websocket"
)

const voiceChatInputAudioTimeout = 180 * time.Second

type voiceChatInputAudioSession struct {
	utteranceID string
	sessionID   string
	channel     string
	chatID      string
	prompt      string
	sampleRate  int
	channels    int
	startedAt   time.Time
	pcm         bytes.Buffer
}

func handleVoiceChatInputAudioBridge(gatewayURL string, voiceDirect voiceDirectFinalHandler, idleNotifier orchestrator.IdleNotifier) http.Handler {
	return websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		viewerClientID := voiceChatViewerClientID(conn)
		baseURL := voiceChatHTTPBaseURLFromGateway(gatewayURL)
		if baseURL == "" {
			_ = sendVoiceChatError(conn, modulevoicechat.ErrorLLMSessionUnavailable, "RenCrow LLM input_audio endpoint is not configured")
			return
		}
		log.Printf("[voice-chat] viewer connected viewer_client_id=%s input_audio_base=%s", viewerClientID, baseURL)
		if err := serveVoiceChatInputAudio(conn, baseURL, voiceDirect, idleNotifier, viewerClientID); err != nil {
			log.Printf("[voice-chat] input_audio bridge closed viewer_client_id=%s err=%v", viewerClientID, err)
		}
	})
}

func serveVoiceChatInputAudio(conn *websocket.Conn, baseURL string, voiceDirect voiceDirectFinalHandler, idleNotifier orchestrator.IdleNotifier, viewerClientID string) error {
	var sess *voiceChatInputAudioSession
	chatBusy := false
	clearChatBusy := func() {
		if idleNotifier != nil && chatBusy {
			idleNotifier.SetChatBusy(false)
			chatBusy = false
		}
	}
	defer clearChatBusy()
	for {
		var msg []byte
		if err := websocket.Message.Receive(conn, &msg); err != nil {
			return err
		}
		if modulevoicechat.IsWebSocketTextFramePayload(msg) {
			logVoiceChatTextFrame("viewer_to_input_audio", viewerClientID, msg)
			var ev map[string]any
			if err := json.Unmarshal(msg, &ev); err != nil {
				_ = sendVoiceChatError(conn, modulevoicechat.ErrorInvalidRequest, "invalid voice chat control frame")
				continue
			}
			switch stringField(ev, "type") {
			case modulevoicechat.EventSessionStart:
				if idleNotifier != nil {
					idleNotifier.NotifyActivity()
					if !chatBusy {
						idleNotifier.SetChatBusy(true)
						chatBusy = true
					}
				}
				sess = newVoiceChatInputAudioSession(ev)
				if err := sendVoiceChatJSON(conn, map[string]any{
					"type":         modulevoicechat.EventSessionReady,
					"utterance_id": sess.utteranceID,
					"session_id":   sess.sessionID,
				}); err != nil {
					return err
				}
			case modulevoicechat.EventSessionCommit:
				if sess == nil {
					_ = sendVoiceChatError(conn, modulevoicechat.ErrorInvalidRequest, "session.commit received before session.start")
					continue
				}
				if utteranceID := stringField(ev, "utterance_id"); utteranceID != "" {
					sess.utteranceID = utteranceID
				}
				text, err := postVoiceChatInputAudio(context.Background(), baseURL, sess)
				if err != nil {
					_ = sendVoiceChatError(conn, modulevoicechat.ErrorLLMInferenceFailed, err.Error())
					sess = nil
					continue
				}
				if strings.TrimSpace(text) == "" {
					_ = sendVoiceChatError(conn, modulevoicechat.ErrorLLMInferenceFailed, "RenCrow LLM returned empty input_audio response")
					sess = nil
					continue
				}
				if err := sendVoiceChatJSON(conn, map[string]any{
					"type":         modulevoicechat.EventLLMDelta,
					"utterance_id": sess.utteranceID,
					"session_id":   sess.sessionID,
					"seq":          1,
					"text":         text,
				}); err != nil {
					return err
				}
				if err := sendVoiceChatJSON(conn, map[string]any{
					"type":         modulevoicechat.EventLLMFinal,
					"utterance_id": sess.utteranceID,
					"session_id":   sess.sessionID,
					"text":         text,
				}); err != nil {
					return err
				}
				log.Printf("[voice-chat] input_audio finalized utterance_id=%s bytes=%d text_len=%d", sess.utteranceID, sess.pcm.Len(), len([]rune(text)))
				processVoiceChatInputAudioFinalAsync(voiceDirect, sess, text)
				sess = nil
				clearChatBusy()
			case modulevoicechat.EventSessionCancel:
				sess = nil
				clearChatBusy()
			}
			continue
		}
		if sess == nil {
			continue
		}
		if _, err := sess.pcm.Write(msg); err != nil {
			return err
		}
	}
}

func newVoiceChatInputAudioSession(ev map[string]any) *voiceChatInputAudioSession {
	utteranceID := stringField(ev, "utterance_id")
	if utteranceID == "" {
		utteranceID = fmt.Sprintf("utt-%d", time.Now().UnixNano())
	}
	sessionID := voiceChatFirstNonEmpty(stringField(ev, "viewer_session_id"), stringField(ev, "session_id"))
	if sessionID == "" {
		sessionID = fmt.Sprintf("vds-sess-%d", time.Now().UnixNano())
	}
	sampleRate := intField(ev, "sample_rate")
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := intField(ev, "channels")
	if channels <= 0 {
		channels = 1
	}
	return &voiceChatInputAudioSession{
		utteranceID: utteranceID,
		sessionID:   sessionID,
		channel:     voiceChatFirstNonEmpty(stringField(ev, "channel"), "viewer"),
		chatID:      stringField(ev, "chat_id"),
		prompt:      voiceChatFirstNonEmpty(stringField(ev, "prompt"), "聞こえた音声内容を日本語で短く確認してください。"),
		sampleRate:  sampleRate,
		channels:    channels,
		startedAt:   time.Now(),
	}
}

func postVoiceChatInputAudio(ctx context.Context, baseURL string, sess *voiceChatInputAudioSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("voice chat session is nil")
	}
	if sess.pcm.Len() == 0 {
		return "", fmt.Errorf("voice chat audio is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, voiceChatInputAudioTimeout)
	defer cancel()
	wav := encodePCM16WAV(sess.pcm.Bytes(), sess.sampleRate, sess.channels)
	payload := map[string]any{
		"model":      "Chat",
		"think":      false,
		"max_tokens": 160,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": sess.prompt},
					{"type": "input_audio", "input_audio": map[string]any{
						"data":   base64.StdEncoding.EncodeToString(wav),
						"format": "wav",
					}},
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("RenCrow LLM input_audio failed: status=%d body=%s", resp.StatusCode, voiceChatShortLogText(string(data), 500))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("RenCrow LLM input_audio returned no choices")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func processVoiceChatInputAudioFinalAsync(handler voiceDirectFinalHandler, sess *voiceChatInputAudioSession, text string) {
	if handler == nil || sess == nil || strings.TrimSpace(text) == "" {
		return
	}
	snapshot := *sess
	go processVoiceChatInputAudioFinal(handler, &snapshot, text)
}

func processVoiceChatInputAudioFinal(handler voiceDirectFinalHandler, sess *voiceChatInputAudioSession, text string) {
	started := time.Now()
	req := orchestrator.ProcessVoiceDirectRequest{
		UtteranceID: sess.utteranceID,
		SessionID:   sess.sessionID,
		Channel:     sess.channel,
		ChatID:      sess.chatID,
		Prompt:      sess.prompt,
		SampleRate:  sess.sampleRate,
		Channels:    sess.channels,
		StartedAt:   sess.startedAt,
		FinalText:   text,
	}
	handler.NotifyVoiceDirectFirstToken(context.Background(), req, task.NewJobID(), time.Now())
	if _, err := handler.ProcessVoiceDirect(context.Background(), req); err != nil {
		log.Printf("[voice-chat] ProcessVoiceDirect failed utterance_id=%s: %v", req.UtteranceID, err)
		return
	}
	log.Printf("[voice-chat] ProcessVoiceDirect completed utterance_id=%s text_len=%d elapsed_ms=%d", req.UtteranceID, len([]rune(text)), time.Since(started).Milliseconds())
}

func encodePCM16WAV(pcm []byte, sampleRate, channels int) []byte {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	if channels <= 0 {
		channels = 1
	}
	var out bytes.Buffer
	dataLen := uint32(len(pcm))
	byteRate := uint32(sampleRate * channels * 2)
	blockAlign := uint16(channels * 2)
	_ = binary.Write(&out, binary.LittleEndian, []byte("RIFF"))
	_ = binary.Write(&out, binary.LittleEndian, uint32(36)+dataLen)
	_ = binary.Write(&out, binary.LittleEndian, []byte("WAVE"))
	_ = binary.Write(&out, binary.LittleEndian, []byte("fmt "))
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&out, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&out, binary.LittleEndian, byteRate)
	_ = binary.Write(&out, binary.LittleEndian, blockAlign)
	_ = binary.Write(&out, binary.LittleEndian, uint16(16))
	_ = binary.Write(&out, binary.LittleEndian, []byte("data"))
	_ = binary.Write(&out, binary.LittleEndian, dataLen)
	_, _ = out.Write(pcm)
	return out.Bytes()
}

func sendVoiceChatJSON(conn *websocket.Conn, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, string(data))
}

func voiceChatHTTPBaseURLFromGateway(gatewayURL string) string {
	u, err := url.Parse(strings.TrimSpace(gatewayURL))
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
	default:
		return ""
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}
