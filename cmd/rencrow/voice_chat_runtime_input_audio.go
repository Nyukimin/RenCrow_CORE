package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
	"golang.org/x/net/websocket"
)

const voiceChatInputAudioTimeout = 180 * time.Second

const voiceChatInputAudioSystemPrompt = `Return exactly one JSON object with exactly these non-empty string fields: "user_text" (the audio transcript) and "reply" (the response following the existing client/config prompt). Do not return prose, Markdown, or any other format.`

type voiceChatInputAudioSettings struct {
	Model          string
	APIKey         string
	Timeout        time.Duration
	ModelContext   int
	Stream         bool
	MaxTokens      int
	Temperature    float64
	TopP           *float64
	TopK           *int
	MinP           *float64
	Seed           *int64
	EnableThinking *bool
	Prompt         string
}

type voiceChatInputAudioSession struct {
	utteranceID  string
	sessionID    string
	channel      string
	chatID       string
	prompt       string
	sampleRate   int
	channels     int
	startedAt    time.Time
	commitAt     time.Time
	firstTokenAt time.Time
	pcm          bytes.Buffer
}

func handleVoiceChatInputAudioBridge(gatewayURL string, settings voiceChatInputAudioSettings, voiceDirect voiceDirectFinalHandler, idleNotifier orchestrator.IdleNotifier) http.Handler {
	return websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		viewerClientID := voiceChatViewerClientID(conn)
		baseURL := voiceChatHTTPBaseURLFromGateway(gatewayURL)
		if baseURL == "" {
			_ = sendVoiceChatError(conn, modulevoicechat.ErrorLLMSessionUnavailable, "RenCrow LLM input_audio endpoint is not configured")
			return
		}
		log.Printf("[voice-chat] viewer connected viewer_client_id=%s input_audio_base=%s", viewerClientID, baseURL)
		if err := serveVoiceChatInputAudio(conn, baseURL, settings, voiceDirect, idleNotifier, viewerClientID); err != nil {
			log.Printf("[voice-chat] input_audio bridge closed viewer_client_id=%s err=%v", viewerClientID, err)
		}
	})
}

func serveVoiceChatInputAudio(conn *websocket.Conn, baseURL string, settings voiceChatInputAudioSettings, voiceDirect voiceDirectFinalHandler, idleNotifier orchestrator.IdleNotifier, viewerClientID string) error {
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
				sess = newVoiceChatInputAudioSession(ev, settings.Prompt)
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
				sess.commitAt = time.Now()
				text, err := postVoiceChatInputAudio(context.Background(), baseURL, settings, sess)
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
				sess.firstTokenAt = time.Now()
				response, err := processVoiceChatInputAudioFinal(voiceDirect, sess, text)
				if err != nil {
					_ = sendVoiceChatError(conn, modulevoicechat.ErrorVoiceResultPublishFailed, "voice response could not be published")
					sess = nil
					clearChatBusy()
					continue
				}
				publishedText := strings.TrimSpace(response.Response)
				if err := sendVoiceChatJSON(conn, map[string]any{
					"type":         modulevoicechat.EventLLMDelta,
					"utterance_id": sess.utteranceID,
					"session_id":   sess.sessionID,
					"seq":          1,
					"text":         publishedText,
					"trace_id":     response.TraceID,
					"job_id":       response.JobID,
					"message_id":   response.MessageID,
				}); err != nil {
					return err
				}
				if err := sendVoiceChatJSON(conn, map[string]any{
					"type":         modulevoicechat.EventLLMFinal,
					"utterance_id": sess.utteranceID,
					"session_id":   sess.sessionID,
					"text":         publishedText,
					"trace_id":     response.TraceID,
					"job_id":       response.JobID,
					"message_id":   response.MessageID,
				}); err != nil {
					return err
				}
				log.Printf("[voice-chat] input_audio finalized utterance_id=%s trace_id=%s job_id=%s message_id=%s bytes=%d text_len=%d",
					voiceChatShortLogText(sess.utteranceID, 128),
					voiceChatShortLogText(response.TraceID, 128),
					voiceChatShortLogText(response.JobID, 128),
					voiceChatShortLogText(response.MessageID, 128),
					sess.pcm.Len(),
					len([]rune(publishedText)),
				)
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

func newVoiceChatInputAudioSession(ev map[string]any, defaultPrompt string) *voiceChatInputAudioSession {
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
		prompt:      voiceChatFirstNonEmpty(stringField(ev, "prompt"), defaultPrompt, "音声の内容を理解し、日本語で短く自然に返答してください。"),
		sampleRate:  sampleRate,
		channels:    channels,
		startedAt:   time.Now(),
	}
}

func postVoiceChatInputAudio(ctx context.Context, baseURL string, settings voiceChatInputAudioSettings, sess *voiceChatInputAudioSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("voice chat session is nil")
	}
	if sess.pcm.Len() == 0 {
		return "", fmt.Errorf("voice chat audio is empty")
	}
	settings = normalizeVoiceChatInputAudioSettings(settings)
	ctx, cancel := context.WithTimeout(ctx, settings.Timeout)
	defer cancel()
	wav := encodePCM16WAV(sess.pcm.Bytes(), sess.sampleRate, sess.channels)
	providerOptions := make(map[string]any, 6)
	// input_audioも通常会話と同じCHAT契約であり、thinkingは常に無効にする。
	providerOptions["think"] = false
	providerOptions["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	if settings.TopP != nil {
		providerOptions["top_p"] = *settings.TopP
	}
	if settings.TopK != nil {
		providerOptions["top_k"] = *settings.TopK
	}
	if settings.MinP != nil {
		providerOptions["min_p"] = *settings.MinP
	}
	if settings.Seed != nil {
		providerOptions["seed"] = *settings.Seed
	}
	request := llm.GenerateRequest{
		Messages: []llm.Message{{
			Role: "user",
			Parts: []llm.MessagePart{
				{Type: llm.MessagePartAudio, MimeType: "audio/wav", Data: wav},
				{Type: llm.MessagePartText, Text: sess.prompt},
			},
		}},
		MaxTokens:       settings.MaxTokens,
		Temperature:     settings.Temperature,
		SystemPrompt:    voiceChatInputAudioSystemPrompt,
		ResponseFormat:  llm.ResponseFormatJSONObject,
		ProviderOptions: providerOptions,
	}
	if settings.Stream {
		request.OnToken = func(string) {}
	}
	provider := rencrowllm.NewGatewayProviderWithModelContext(settings.APIKey, settings.Model, baseURL, settings.Timeout, settings.ModelContext)
	resp, err := provider.Generate(ctx, request)
	if err != nil {
		return "", fmt.Errorf("RenCrow LLM input_audio failed: %w", err)
	}
	return strings.TrimSpace(resp.Content), nil
}

func normalizeVoiceChatInputAudioSettings(settings voiceChatInputAudioSettings) voiceChatInputAudioSettings {
	if strings.TrimSpace(settings.Model) == "" {
		settings.Model = "Chat"
	}
	if settings.Timeout <= 0 {
		settings.Timeout = voiceChatInputAudioTimeout
	}
	if settings.MaxTokens <= 0 {
		settings.MaxTokens = 160
	}
	return settings
}

func processVoiceChatInputAudioFinal(handler voiceDirectFinalHandler, sess *voiceChatInputAudioSession, text string) (orchestrator.ProcessMessageResponse, error) {
	started := time.Now()
	if handler == nil {
		err := voiceChatInputAudioPublishError(fmt.Errorf("voice direct owner is unavailable"))
		logVoiceChatInputAudioPublishFailure(sess, orchestrator.ProcessMessageResponse{}, err)
		return orchestrator.ProcessMessageResponse{}, err
	}
	if sess == nil || strings.TrimSpace(text) == "" {
		err := voiceChatInputAudioPublishError(fmt.Errorf("voice direct final input is incomplete"))
		logVoiceChatInputAudioPublishFailure(sess, orchestrator.ProcessMessageResponse{}, err)
		return orchestrator.ProcessMessageResponse{}, err
	}
	req := orchestrator.ProcessVoiceDirectRequest{
		UtteranceID:  sess.utteranceID,
		SessionID:    sess.sessionID,
		Channel:      sess.channel,
		ChatID:       sess.chatID,
		Prompt:       sess.prompt,
		SampleRate:   sess.sampleRate,
		Channels:     sess.channels,
		StartedAt:    sess.startedAt,
		CommitAt:     sess.commitAt,
		FirstTokenAt: sess.firstTokenAt,
		FinalText:    text,
	}
	response, err := handler.ProcessVoiceDirect(context.Background(), req)
	if err != nil {
		publishErr := voiceChatInputAudioPublishError(err)
		logVoiceChatInputAudioPublishFailure(sess, response, publishErr)
		return orchestrator.ProcessMessageResponse{}, publishErr
	}
	if err := validateVoiceChatProcessResponseIdentity(response); err != nil {
		publishErr := voiceChatInputAudioPublishError(err)
		logVoiceChatInputAudioPublishFailure(sess, response, publishErr)
		return orchestrator.ProcessMessageResponse{}, publishErr
	}
	publishedText := strings.TrimSpace(response.Response)
	if publishedText == "" {
		publishErr := voiceChatInputAudioPublishError(fmt.Errorf("voice direct owner response is empty"))
		logVoiceChatInputAudioPublishFailure(sess, response, publishErr)
		return orchestrator.ProcessMessageResponse{}, publishErr
	}
	log.Printf("[voice-chat] ProcessVoiceDirect completed utterance_id=%s trace_id=%s job_id=%s message_id=%s text_len=%d elapsed_ms=%d",
		voiceChatShortLogText(req.UtteranceID, 128),
		voiceChatShortLogText(response.TraceID, 128),
		voiceChatShortLogText(response.JobID, 128),
		voiceChatShortLogText(response.MessageID, 128),
		len([]rune(publishedText)),
		time.Since(started).Milliseconds(),
	)
	return response, nil
}

func validateVoiceChatProcessResponseIdentity(response orchestrator.ProcessMessageResponse) error {
	if strings.TrimSpace(response.TraceID) != response.TraceID {
		return fmt.Errorf("trace_id contains surrounding whitespace")
	}
	traceID := modulecore.TraceID(response.TraceID)
	if err := traceID.Validate(); err != nil {
		return fmt.Errorf("trace_id is invalid: %w", err)
	}
	if strings.TrimSpace(response.JobID) != response.JobID {
		return fmt.Errorf("job_id contains surrounding whitespace")
	}
	jobID, err := modulecore.ParseTaskID(response.JobID)
	if err != nil || jobID.IsZero() {
		if err == nil {
			err = fmt.Errorf("job_id is empty")
		}
		return fmt.Errorf("job_id is invalid: %w", err)
	}
	if jobID.String() == string(traceID) {
		return fmt.Errorf("trace_id must differ from job_id")
	}
	if strings.TrimSpace(response.MessageID) != response.MessageID {
		return fmt.Errorf("message_id contains surrounding whitespace")
	}
	messageID := modulecore.MessageID(response.MessageID)
	if err := messageID.Validate(); err != nil {
		return fmt.Errorf("message_id is invalid: %w", err)
	}
	return nil
}

func voiceChatInputAudioPublishError(err error) error {
	if err == nil {
		err = fmt.Errorf("voice result publish failed")
	}
	return fmt.Errorf("%s: %w", modulevoicechat.ErrorVoiceResultPublishFailed, err)
}

func logVoiceChatInputAudioPublishFailure(sess *voiceChatInputAudioSession, response orchestrator.ProcessMessageResponse, err error) {
	utteranceID := ""
	if sess != nil {
		utteranceID = sess.utteranceID
	}
	errText := ""
	if err != nil {
		errText = voiceChatShortLogText(err.Error(), 160)
	}
	log.Printf("[voice-chat] input_audio voice result publish failed utterance_id=%s trace_id=%s job_id=%s message_id=%s error=%s",
		voiceChatShortLogText(utteranceID, 128),
		voiceChatShortLogText(response.TraceID, 128),
		voiceChatShortLogText(response.JobID, 128),
		voiceChatShortLogText(response.MessageID, 128),
		errText,
	)
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
