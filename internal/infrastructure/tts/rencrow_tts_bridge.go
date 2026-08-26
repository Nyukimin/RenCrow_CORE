package tts

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type RenCrowTTSBridgeConfig struct {
	HTTPBaseURL        string
	OutputDir          string
	VoiceID            string
	Speed              float64
	TLSSkipVerify      bool
	RequestTimeout     time.Duration
	DownloadAudio      bool
	Sink               AudioSink
	OnChunkReady       func(sessionID, responseID string, chunkIndex int, characterID, text, displayText, audioPath, audioURL string)
	OnSessionCompleted func(sessionID, characterID string)
}

type renCrowTTSSession struct {
	characterID string
	responseID  string
	voiceID     string
	nextChunk   int
}

type RenCrowTTSBridge struct {
	cfg      RenCrowTTSBridgeConfig
	client   *http.Client
	mu       sync.Mutex
	sessions map[string]*renCrowTTSSession
}

func NewRenCrowTTSBridge(cfg RenCrowTTSBridgeConfig) *RenCrowTTSBridge {
	defaults := moduletts.ApplyRenCrowBridgeConfigDefaults(moduletts.RenCrowBridgeConfigDefaultsInput{
		VoiceID:        cfg.VoiceID,
		RequestTimeout: cfg.RequestTimeout,
	})
	cfg.VoiceID = defaults.VoiceID
	cfg.RequestTimeout = defaults.RequestTimeout
	transport := &http.Transport{}
	if cfg.TLSSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &RenCrowTTSBridge{
		cfg:      cfg,
		client:   &http.Client{Timeout: cfg.RequestTimeout, Transport: transport},
		sessions: make(map[string]*renCrowTTSSession),
	}
}

func (b *RenCrowTTSBridge) StartSession(_ context.Context, req orchestrator.TTSSessionStart) error {
	start, err := moduletts.BuildRenCrowSessionStart(moduletts.RenCrowSessionStartInput{
		SessionID:      req.SessionID,
		CharacterID:    req.CharacterID,
		ResponseID:     req.ResponseID,
		RequestedVoice: req.VoiceID,
		DefaultVoice:   b.cfg.VoiceID,
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.sessions[start.SessionID] = &renCrowTTSSession{
		characterID: start.CharacterID,
		responseID:  start.ResponseID,
		voiceID:     start.VoiceID,
		nextChunk:   0,
	}
	b.mu.Unlock()
	return nil
}

func (b *RenCrowTTSBridge) PushText(ctx context.Context, sessionID string, text string, emotion *moduletts.EmotionState) error {
	return b.PushTextWithDisplay(ctx, sessionID, text, text, emotion)
}

func (b *RenCrowTTSBridge) PushTextWithDisplay(ctx context.Context, sessionID string, text string, displayText string, emotion *moduletts.EmotionState) error {
	rawText, empty, err := moduletts.PrepareRenCrowSpeechText(text)
	if err != nil {
		return invalidRequestError(err.Error())
	}
	if empty {
		return nil
	}
	plan := planTTSChunks(rawText, displayText)
	if len(plan) == 0 {
		return nil
	}

	session := b.getOrCreateSession(sessionID)
	characterID := session.characterID
	responseID := session.responseID
	voiceID := moduletts.ChooseNonEmpty(session.voiceID, b.cfg.VoiceID)

	for planIndex, item := range plan {
		speechText := moduletts.EnsureEmotionPrefixForCharacter(item.SpeechText, emotion, characterID)
		payload, err := moduletts.BuildSynthesisPayload(moduletts.SynthesisPayloadInput{
			Text:           speechText,
			DefaultVoiceID: voiceID,
			Speed:          b.cfg.Speed,
			Emotion:        emotion,
		})
		if err != nil {
			return invalidRequestError(err.Error())
		}

		reqBody, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal /synthesis request: %w", err)
		}
		chunkIndex := session.nextChunk
		startedAt := time.Now()
		log.Printf("[TTS] synthesis request start: session=%s chunk=%d plan_index=%d/%d speech_runes=%d", sessionID, chunkIndex, planIndex+1, len(plan), moduletts.SpeechTextRuneCount(speechText))
		body, err := b.postSynthesisWithRetry(ctx, reqBody, sessionID, chunkIndex)
		if err != nil {
			log.Printf("[TTS] synthesis request failed: session=%s chunk=%d elapsed_ms=%d error=%v", sessionID, chunkIndex, time.Since(startedAt).Milliseconds(), err)
			return err
		}
		log.Printf("[TTS] synthesis request done: session=%s chunk=%d elapsed_ms=%d", sessionID, chunkIndex, time.Since(startedAt).Milliseconds())

		out, err := decodeGatewaySynthesisResponse(body)
		if err != nil {
			return err
		}

		audioPath := strings.TrimSpace(out.AudioPath)
		audioURL, err := resolveGatewayRelayURL(b.cfg.HTTPBaseURL, audioPath)
		if err != nil {
			return err
		}
		sinkAudioPath := audioPath
		if b.cfg.DownloadAudio {
			sinkAudioPath, audioURL, err = downloadGatewayAudio(ctx, b.client, b.cfg.HTTPBaseURL, audioPath, b.cfg.OutputDir, "viewer-tts")
			if err != nil {
				return err
			}
			if rel, ok := moduletts.LocalAudioRelPath(b.cfg.OutputDir, sinkAudioPath); ok {
				audioPath = rel
			} else {
				audioPath = sinkAudioPath
			}
		}
		ch := audioChunk{
			ChunkIndex: session.nextChunk,
			Text:       speechText,
			AudioPath:  sinkAudioPath,
			AudioURL:   audioURL,
			PauseAfter: chunkPauseForText(speechText),
		}
		session.nextChunk++

		if b.cfg.OnChunkReady != nil {
			b.cfg.OnChunkReady(sessionID, responseID, ch.ChunkIndex, characterID, speechText, strings.TrimSpace(item.DisplayText), audioPath, ch.AudioURL)
		}
		if b.cfg.Sink != nil {
			if err := b.cfg.Sink.SubmitChunk(ctx, sessionID, ch); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *RenCrowTTSBridge) EndSession(ctx context.Context, sessionID string) error {
	if b == nil {
		return nil
	}
	var characterID string
	b.mu.Lock()
	if session, ok := b.sessions[sessionID]; ok && session != nil {
		characterID = strings.TrimSpace(session.characterID)
	}
	delete(b.sessions, sessionID)
	b.mu.Unlock()
	if b.cfg.Sink != nil {
		if err := b.cfg.Sink.CompleteSession(ctx, sessionID); err != nil {
			return err
		}
	}
	if b.cfg.OnSessionCompleted != nil {
		b.cfg.OnSessionCompleted(sessionID, characterID)
	}
	return nil
}
