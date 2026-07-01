package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

type SBV2Config struct {
	BaseURL       string
	VoiceID       string
	Timeout       time.Duration
	AudioPathRoot string
}

type SBV2Provider struct {
	baseURL       string
	voiceID       string
	audioPathRoot string
	client        *http.Client
	mu            sync.RWMutex
	models        []sbv2ModelInfo
	modelsUntil   time.Time
}

func NewSBV2Provider(cfg SBV2Config) *SBV2Provider {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &SBV2Provider{
		baseURL:       strings.TrimRight(cfg.BaseURL, "/"),
		voiceID:       cfg.VoiceID,
		audioPathRoot: cfg.AudioPathRoot,
		client:        &http.Client{Timeout: timeout},
	}
}

func (p *SBV2Provider) Name() string {
	return "sbv2"
}

func (p *SBV2Provider) Synthesize(ctx context.Context, in SynthesisInput) (SynthesisOutput, error) {
	if strings.TrimSpace(p.baseURL) == "" {
		return SynthesisOutput{}, fmt.Errorf("%w: sbv2 base_url is empty", ErrProviderUnavailable)
	}
	if strings.TrimSpace(in.Text) == "" {
		return SynthesisOutput{}, fmt.Errorf("text is required")
	}
	if p.isEditorAPI() {
		return p.synthesizeEditor(ctx, in)
	}

	voice := resolveSBV2VoiceParams(moduletts.ChooseNonEmpty(in.VoiceProfile.VoiceID, p.voiceID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.voiceURL(in.Text, voice), nil)
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("build request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("sbv2 request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return SynthesisOutput{}, fmt.Errorf("sbv2 bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	audioPath, err := saveEditorWAV(resp.Body, in.OutputDir, in.FilePrefix)
	if err != nil {
		return SynthesisOutput{}, err
	}
	return SynthesisOutput{
		Provider:      "sbv2",
		VoiceID:       voice.Name,
		AudioFilePath: audioPath,
	}, nil
}

type sbv2VoiceParams = moduletts.SBV2VoiceParams

func resolveSBV2VoiceParams(name string) sbv2VoiceParams {
	return moduletts.ResolveSBV2VoiceParams(name)
}

func (p *SBV2Provider) voiceURL(text string, voice sbv2VoiceParams) string {
	return moduletts.SBV2VoiceURL(p.baseURL, text, voice)
}

type sbv2ModelInfo struct {
	Name     string   `json:"name"`
	Files    []string `json:"files"`
	Styles   []string `json:"styles"`
	Speakers []string `json:"speakers"`
}

type sbv2ResolvedVoice struct {
	Model     string
	ModelFile string
	Speaker   string
}

func (p *SBV2Provider) isEditorAPI() bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimRight(p.baseURL, "/")), "/api/synthesis")
}

func (p *SBV2Provider) synthesizeEditor(ctx context.Context, in SynthesisInput) (SynthesisOutput, error) {
	text := ensureTTSPunctuation(in.Text)
	requestedSpeaker := moduletts.ChooseNonEmpty(in.VoiceProfile.VoiceID, p.voiceID)
	voice, err := p.resolveEditorVoice(ctx, requestedSpeaker)
	if err != nil {
		return SynthesisOutput{}, err
	}

	moraToneList, err := p.fetchMoraToneList(ctx, text)
	if err != nil {
		return SynthesisOutput{}, err
	}

	payload := moduletts.BuildSBV2EditorSynthesisPayload(moduletts.SBV2EditorSynthesisPayloadInput{
		Model:        voice.Model,
		ModelFile:    voice.ModelFile,
		Text:         text,
		MoraToneList: moraToneList,
		Speaker:      voice.Speaker,
	})
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("marshal editor synthesis request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("build editor synthesis request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("sbv2 editor synthesis failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return SynthesisOutput{}, fmt.Errorf("sbv2 editor bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	audioPath, err := saveEditorWAV(resp.Body, in.OutputDir, in.FilePrefix)
	if err != nil {
		return SynthesisOutput{}, err
	}
	return SynthesisOutput{
		Provider:      "sbv2",
		VoiceID:       voice.Speaker,
		AudioFilePath: audioPath,
	}, nil
}

func (p *SBV2Provider) resolveEditorVoice(ctx context.Context, requestedSpeaker string) (sbv2ResolvedVoice, error) {
	models, err := p.fetchModelInfos(ctx)
	if err != nil {
		return sbv2ResolvedVoice{}, err
	}
	for _, model := range models {
		if len(model.Files) == 0 || len(model.Speakers) == 0 {
			continue
		}
		for _, speaker := range model.Speakers {
			if moduletts.EqualFoldTrim(speaker, requestedSpeaker) || moduletts.EqualFoldTrim(model.Name, requestedSpeaker) {
				return sbv2ResolvedVoice{
					Model:     model.Name,
					ModelFile: model.Files[0],
					Speaker:   speaker,
				}, nil
			}
		}
	}
	for _, model := range models {
		if len(model.Files) == 0 || len(model.Speakers) == 0 {
			continue
		}
		return sbv2ResolvedVoice{
			Model:     model.Name,
			ModelFile: model.Files[0],
			Speaker:   model.Speakers[0],
		}, nil
	}
	if strings.TrimSpace(requestedSpeaker) == "" {
		return sbv2ResolvedVoice{}, fmt.Errorf("sbv2 models_info returned no usable speaker")
	}
	return sbv2ResolvedVoice{}, fmt.Errorf("sbv2 speaker not found: %s", requestedSpeaker)
}

func (p *SBV2Provider) fetchModelInfos(ctx context.Context) ([]sbv2ModelInfo, error) {
	p.mu.RLock()
	if time.Now().Before(p.modelsUntil) && len(p.models) > 0 {
		cached := append([]sbv2ModelInfo(nil), p.models...)
		p.mu.RUnlock()
		return cached, nil
	}
	p.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.editorURL("models_info"), nil)
	if err != nil {
		return nil, fmt.Errorf("build models_info request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sbv2 models_info failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("sbv2 models_info bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var models []sbv2ModelInfo
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return nil, fmt.Errorf("decode models_info: %w", err)
	}
	p.mu.Lock()
	p.models = append([]sbv2ModelInfo(nil), models...)
	p.modelsUntil = time.Now().Add(30 * time.Second)
	p.mu.Unlock()
	return models, nil
}

func (p *SBV2Provider) fetchMoraToneList(ctx context.Context, text string) ([]map[string]any, error) {
	reqBody, err := json.Marshal(moduletts.BuildSBV2G2PRequestPayload(text))
	if err != nil {
		return nil, fmt.Errorf("marshal g2p request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.editorURL("g2p"), bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("build g2p request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sbv2 g2p failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("sbv2 g2p bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode g2p response: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sbv2 g2p returned empty moraToneList")
	}
	return out, nil
}

func (p *SBV2Provider) editorURL(endpoint string) string {
	return moduletts.SBV2EditorURL(p.baseURL, endpoint)
}

func saveEditorWAV(body io.Reader, outputDir, prefix string) (string, error) {
	dir := strings.TrimSpace(outputDir)
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir tts output dir: %w", err)
	}
	safePrefix := sanitizeAudioPrefix(prefix)
	if safePrefix == "" {
		safePrefix = "sbv2"
	}
	pattern := safePrefix + "-*.wav"
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("create temp wav: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		return "", fmt.Errorf("write wav response: %w", err)
	}
	if err := rejectSilentWAV(f.Name()); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return filepath.Clean(f.Name()), nil
}

func rejectSilentWAV(path string) error {
	stats, ok, err := inspectPCM16WAV(path)
	if err != nil {
		return err
	}
	if !ok || !stats.NearSilent {
		return nil
	}
	return fmt.Errorf("%w: generated wav is silent or near silent duration_ms=%d rms=%d peak=%d", ErrSynthesisFailed, stats.DurationMS, stats.RMS, stats.Peak)
}

func sanitizeAudioPrefix(prefix string) string {
	return moduletts.SanitizeAudioPrefix(prefix)
}

func ensureTTSPunctuation(text string) string {
	return moduletts.EnsureTTSPunctuation(text)
}

func chunkPauseForText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "200ms"
	}
	if strings.HasSuffix(text, "。") || strings.HasSuffix(text, "！") || strings.HasSuffix(text, "？") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") {
		return "320ms"
	}
	if strings.HasSuffix(text, "、") || strings.HasSuffix(text, ",") {
		return "180ms"
	}
	ms := 120 + len([]rune(text))*8
	if ms > 400 {
		ms = 400
	}
	return strconv.Itoa(ms) + "ms"
}
