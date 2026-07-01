package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

type IrodoriConfig struct {
	BaseURL               string
	EndpointPath          string
	VoiceID               string
	VoiceName             string
	Speed                 float64
	ReferenceAudio        string
	ReferenceAudioURL     string
	Timeout               time.Duration
	Checkpoint            string
	ModelDevice           string
	ModelPrecision        string
	CodecDevice           string
	CodecPrecision        string
	EnableWatermark       bool
	NumSteps              int
	NumCandidates         int
	SeedRaw               string
	CFGGuidanceMode       string
	CFGScaleText          float64
	CFGScaleSpeaker       float64
	CFGScaleRaw           string
	CFGMinT               float64
	CFGMaxT               float64
	ContextKVCache        bool
	TruncationFactorRaw   string
	RescaleKRaw           string
	RescaleSigmaRaw       string
	SpeakerKVScaleRaw     string
	SpeakerKVMinTRaw      string
	SpeakerKVMaxLayersRaw string
}

type IrodoriProvider struct {
	baseURL string
	voiceID string
	client  *http.Client
	cfg     IrodoriConfig
	refMu   sync.Mutex
	refPath string
}

func NewIrodoriProvider(cfg IrodoriConfig) *IrodoriProvider {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &IrodoriProvider{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		voiceID: cfg.VoiceID,
		client:  &http.Client{Timeout: timeout},
		cfg:     withIrodoriDefaults(cfg),
	}
}

func (p *IrodoriProvider) Name() string {
	return "irodori"
}

func (p *IrodoriProvider) Synthesize(ctx context.Context, in SynthesisInput) (SynthesisOutput, error) {
	if strings.TrimSpace(p.baseURL) == "" {
		return SynthesisOutput{}, fmt.Errorf("%w: irodori base_url is empty", ErrProviderUnavailable)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return SynthesisOutput{}, fmt.Errorf("text is required")
	}
	voiceID := resolveIrodoriVoiceID(moduletts.ChooseNonEmpty(in.VoiceProfile.VoiceID, p.voiceID))
	voice := resolveIrodoriVoiceName(moduletts.ChooseNonEmpty(in.VoiceProfile.VoiceID, p.cfg.VoiceName, p.voiceID))
	style := resolveIrodoriStyle(in.Emotion)
	payload := irodoriSynthesisPayload(voice, style, text, p.cfg.Speed)
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("marshal irodori request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.synthesisURL(), bytes.NewReader(reqBody))
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("build irodori request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("irodori request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return SynthesisOutput{}, fmt.Errorf("irodori bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	audioURL, err := parseIrodoriAudioURL(resp.Body)
	if err != nil {
		return SynthesisOutput{}, err
	}
	audioResp, err := p.downloadAudio(ctx, audioURL)
	if err != nil {
		return SynthesisOutput{}, err
	}
	defer audioResp.Body.Close()
	if audioResp.StatusCode < 200 || audioResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(audioResp.Body, 2048))
		return SynthesisOutput{}, fmt.Errorf("irodori audio bad status=%d body=%s", audioResp.StatusCode, strings.TrimSpace(string(body)))
	}

	audioPath, err := saveEditorWAV(audioResp.Body, in.OutputDir, in.FilePrefix)
	if err != nil {
		return SynthesisOutput{}, err
	}
	return SynthesisOutput{
		Provider:      "irodori",
		VoiceID:       voiceID,
		AudioFilePath: audioPath,
		AudioURL:      audioURL,
	}, nil
}
