package tts

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

type ProviderTTSBridgeConfig struct {
	Provider           Provider
	Sink               AudioSink
	OutputDir          string
	HTTPBaseURL        string
	OnChunkReady       func(sessionID, responseID string, chunkIndex int, characterID, text, displayText, audioPath, audioURL string)
	OnSessionCompleted func(sessionID, characterID string)
}

type providerBridgeSession struct {
	characterID string
	responseID  string
	voiceID     string
	nextChunk   int
}

type ProviderTTSBridge struct {
	cfg      ProviderTTSBridgeConfig
	mu       sync.Mutex
	sessions map[string]*providerBridgeSession
}

func NewProviderTTSBridge(cfg ProviderTTSBridgeConfig) *ProviderTTSBridge {
	return &ProviderTTSBridge{
		cfg:      cfg,
		sessions: make(map[string]*providerBridgeSession),
	}
}

func (b *ProviderTTSBridge) StartSession(_ context.Context, req orchestrator.TTSSessionStart) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	b.mu.Lock()
	b.sessions[req.SessionID] = &providerBridgeSession{
		characterID: strings.TrimSpace(req.CharacterID),
		responseID:  strings.TrimSpace(req.ResponseID),
		voiceID:     strings.TrimSpace(req.VoiceID),
	}
	b.mu.Unlock()
	return nil
}

func (b *ProviderTTSBridge) PushText(ctx context.Context, sessionID string, text string, emotion *moduletts.EmotionState) error {
	return b.PushTextWithDisplay(ctx, sessionID, text, text, emotion)
}

func (b *ProviderTTSBridge) PushTextWithDisplay(ctx context.Context, sessionID string, text string, displayText string, emotion *moduletts.EmotionState) error {
	if b.cfg.Provider == nil {
		return fmt.Errorf("tts provider is not configured")
	}
	plan := planTTSChunks(text, displayText)
	if len(plan) == 0 {
		return nil
	}
	s := b.getOrCreateSession(sessionID)
	for _, item := range plan {
		speechText := moduletts.EnsureEmotionPrefixForCharacter(item.SpeechText, emotion, s.characterID)
		out, stats, err := b.synthesizeChunk(ctx, s, speechText)
		if err != nil {
			return err
		}
		log.Printf("[TTS] chunk_ready session=%s response=%s chunk=%d voice=%s duration_ms=%d rms=%d peak=%d text=%q audio_path=%s",
			sessionID,
			s.responseID,
			s.nextChunk,
			s.voiceID,
			stats.DurationMS,
			stats.RMS,
			stats.Peak,
			speechText,
			localAudioPathForViewer(b.cfg.OutputDir, out.AudioFilePath),
		)
		ch := audioChunk{
			ChunkIndex: s.nextChunk,
			Text:       speechText,
			AudioPath:  localAudioPathForViewer(b.cfg.OutputDir, out.AudioFilePath),
			AudioURL:   resolveAudioURL(mediaBaseURL(b.cfg.HTTPBaseURL), out.AudioFilePath, out.AudioURL),
			PauseAfter: chunkPauseForText(speechText),
		}
		s.nextChunk++
		if b.cfg.OnChunkReady != nil {
			b.cfg.OnChunkReady(sessionID, s.responseID, ch.ChunkIndex, s.characterID, ch.Text, item.DisplayText, ch.AudioPath, ch.AudioURL)
		}
		if b.cfg.Sink != nil {
			if err := b.cfg.Sink.SubmitChunk(ctx, sessionID, ch); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *ProviderTTSBridge) synthesizeChunk(ctx context.Context, s *providerBridgeSession, chunkText string) (SynthesisOutput, wavStats, error) {
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		out, err := b.cfg.Provider.Synthesize(ctx, SynthesisInput{
			Text:       chunkText,
			OutputDir:  b.cfg.OutputDir,
			FilePrefix: "viewer-tts",
			VoiceProfile: VoiceProfile{
				VoiceID: s.voiceID,
			},
		})
		if err != nil {
			lastErr = err
			if attempt == 1 && strings.Contains(err.Error(), "silent") {
				log.Printf("[TTS] retrying near-silent chunk voice=%s text=%q err=%v", s.voiceID, chunkText, err)
				continue
			}
			return SynthesisOutput{}, wavStats{}, err
		}
		stats, ok, err := inspectPCM16WAV(out.AudioFilePath)
		if err != nil {
			return SynthesisOutput{}, wavStats{}, err
		}
		if ok && stats.NearSilent {
			lastErr = fmt.Errorf("%w: generated wav is near silent duration_ms=%d rms=%d peak=%d", ErrSynthesisFailed, stats.DurationMS, stats.RMS, stats.Peak)
			if attempt == 1 {
				log.Printf("[TTS] retrying near-silent chunk voice=%s text=%q audio_path=%s duration_ms=%d rms=%d peak=%d",
					s.voiceID, chunkText, out.AudioFilePath, stats.DurationMS, stats.RMS, stats.Peak)
				continue
			}
			return SynthesisOutput{}, stats, lastErr
		}
		return out, stats, nil
	}
	return SynthesisOutput{}, wavStats{}, lastErr
}

func localAudioPathForViewer(outputDir, audioPath string) string {
	if rel, ok := moduletts.LocalAudioRelPath(outputDir, audioPath); ok {
		return rel
	}
	return strings.TrimSpace(audioPath)
}

func (b *ProviderTTSBridge) EndSession(ctx context.Context, sessionID string) error {
	var characterID string
	b.mu.Lock()
	if s, ok := b.sessions[sessionID]; ok && s != nil {
		characterID = s.characterID
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

func (b *ProviderTTSBridge) getOrCreateSession(sessionID string) *providerBridgeSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.sessions[sessionID]; ok {
		return s
	}
	s := &providerBridgeSession{}
	b.sessions[sessionID] = s
	return s
}
