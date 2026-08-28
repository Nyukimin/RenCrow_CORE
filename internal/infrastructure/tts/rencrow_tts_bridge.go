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
	AuthToken          string
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

const renCrowTTSMaxConcurrentSynthesis = 2

type renCrowTTSPlanRequest struct {
	chunkIndex  int
	planIndex   int
	item        ttsChunkPlanItem
	speechText  string
	requestBody []byte
}

type renCrowTTSPlanResult struct {
	request   renCrowTTSPlanRequest
	audioPath string
	audioURL  string
	sinkPath  string
	err       error
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

	session, firstChunk := b.reserveSessionChunks(sessionID, len(plan))
	characterID := session.characterID
	responseID := session.responseID
	voiceID := moduletts.ChooseNonEmpty(session.voiceID, b.cfg.VoiceID)
	requests := make([]renCrowTTSPlanRequest, 0, len(plan))
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
		requests = append(requests, renCrowTTSPlanRequest{
			chunkIndex:  firstChunk + planIndex,
			planIndex:   planIndex,
			item:        item,
			speechText:  speechText,
			requestBody: reqBody,
		})
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]chan renCrowTTSPlanResult, len(requests))
	type synthesisJob struct {
		index   int
		request renCrowTTSPlanRequest
	}
	jobs := make(chan synthesisJob, len(requests))
	for index, request := range requests {
		results[index] = make(chan renCrowTTSPlanResult, 1)
		jobs <- synthesisJob{index: index, request: request}
	}
	close(jobs)
	var workers sync.WaitGroup
	workerCount := min(renCrowTTSMaxConcurrentSynthesis, len(requests))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				if err := workCtx.Err(); err != nil {
					results[job.index] <- renCrowTTSPlanResult{request: job.request, err: err}
					continue
				}
				results[job.index] <- b.synthesizePlanRequest(workCtx, sessionID, len(requests), job.request)
			}
		}()
	}

	for index := range results {
		result := <-results[index]
		if result.err != nil {
			cancel()
			workers.Wait()
			return result.err
		}
		request := result.request
		ch := audioChunk{
			ChunkIndex: request.chunkIndex,
			Text:       request.speechText,
			AudioPath:  result.sinkPath,
			AudioURL:   result.audioURL,
			PauseAfter: chunkPauseForText(request.speechText),
		}
		if b.cfg.OnChunkReady != nil {
			b.cfg.OnChunkReady(sessionID, responseID, ch.ChunkIndex, characterID, request.speechText, strings.TrimSpace(request.item.DisplayText), result.audioPath, ch.AudioURL)
		}
		if b.cfg.Sink != nil {
			if err := b.cfg.Sink.SubmitChunk(ctx, sessionID, ch); err != nil {
				cancel()
				workers.Wait()
				return err
			}
		}
	}
	workers.Wait()
	return nil
}

func (b *RenCrowTTSBridge) synthesizePlanRequest(ctx context.Context, sessionID string, planLength int, request renCrowTTSPlanRequest) renCrowTTSPlanResult {
	result := renCrowTTSPlanResult{request: request}
	startedAt := time.Now()
	log.Printf("[TTS] synthesis request start: session=%s chunk=%d plan_index=%d/%d speech_runes=%d", sessionID, request.chunkIndex, request.planIndex+1, planLength, moduletts.SpeechTextRuneCount(request.speechText))
	body, err := b.postSynthesisWithRetry(ctx, request.requestBody, sessionID, request.chunkIndex)
	if err != nil {
		log.Printf("[TTS] synthesis request failed: session=%s chunk=%d elapsed_ms=%d error=%v", sessionID, request.chunkIndex, time.Since(startedAt).Milliseconds(), err)
		result.err = err
		return result
	}
	log.Printf("[TTS] synthesis request done: session=%s chunk=%d elapsed_ms=%d", sessionID, request.chunkIndex, time.Since(startedAt).Milliseconds())

	out, err := decodeGatewaySynthesisResponse(body)
	if err != nil {
		result.err = err
		return result
	}
	audioPath := strings.TrimSpace(out.AudioPath)
	audioURL, err := resolveGatewayRelayURL(b.cfg.HTTPBaseURL, audioPath)
	if err != nil {
		result.err = err
		return result
	}
	sinkAudioPath := audioPath
	if b.cfg.DownloadAudio {
		sinkAudioPath, audioURL, err = downloadGatewayAudio(ctx, b.client, b.cfg.HTTPBaseURL, audioPath, b.cfg.OutputDir, "viewer-tts")
		if err != nil {
			result.err = err
			return result
		}
		if rel, ok := moduletts.LocalAudioRelPath(b.cfg.OutputDir, sinkAudioPath); ok {
			audioPath = rel
		} else {
			audioPath = sinkAudioPath
		}
	}
	result.audioPath = audioPath
	result.audioURL = audioURL
	result.sinkPath = sinkAudioPath
	return result
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
