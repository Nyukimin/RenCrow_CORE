package tts

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type IrodoriRunGenerationConfig struct {
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

type IrodoriStyleEmotion struct {
	Emotion        string
	Intensity      float64
	Speed          float64
	Pitch          float64
	Pause          string
	Expressiveness float64
}

type IrodoriSynthesisPayloadInput struct {
	Voice string
	Style string
	Text  string
	Speed float64
}

type IrodoriSynthesisPayload struct {
	Voice string  `json:"voice"`
	Style string  `json:"style"`
	Text  string  `json:"text"`
	Speed float64 `json:"speed,omitempty"`
}

type IrodoriUploadedAudioFileData struct {
	Path string                       `json:"path"`
	Meta IrodoriUploadedAudioFileMeta `json:"meta"`
}

type IrodoriUploadedAudioFileMeta struct {
	Type string `json:"_type"`
}

const (
	DefaultIrodoriEndpointPath    = "/api/tts"
	DefaultIrodoriCheckpoint      = "Aratako/Irodori-TTS-500M-v2"
	DefaultIrodoriModelDevice     = "mps"
	DefaultIrodoriModelPrecision  = "fp32"
	DefaultIrodoriCodecDevice     = "mps"
	DefaultIrodoriCodecPrecision  = "fp32"
	DefaultIrodoriNumSteps        = 16
	DefaultIrodoriNumCandidates   = 1
	DefaultIrodoriCFGGuidanceMode = "independent"
	DefaultIrodoriCFGScaleText    = 3.0
	DefaultIrodoriCFGScaleSpeaker = 5.0
	DefaultIrodoriCFGMinT         = 0.5
	DefaultIrodoriCFGMaxT         = 1.0
)

func IrodoriEndpointPath(endpointPath string) string {
	endpointPath = strings.TrimSpace(endpointPath)
	if endpointPath == "" {
		return DefaultIrodoriEndpointPath
	}
	return endpointPath
}

func ApplyIrodoriRunGenerationDefaults(cfg IrodoriRunGenerationConfig) IrodoriRunGenerationConfig {
	if cfg.Checkpoint == "" {
		cfg.Checkpoint = DefaultIrodoriCheckpoint
	}
	if cfg.ModelDevice == "" {
		cfg.ModelDevice = DefaultIrodoriModelDevice
	}
	if cfg.ModelPrecision == "" {
		cfg.ModelPrecision = DefaultIrodoriModelPrecision
	}
	if cfg.CodecDevice == "" {
		cfg.CodecDevice = DefaultIrodoriCodecDevice
	}
	if cfg.CodecPrecision == "" {
		cfg.CodecPrecision = DefaultIrodoriCodecPrecision
	}
	if cfg.NumSteps <= 0 {
		cfg.NumSteps = DefaultIrodoriNumSteps
	}
	if cfg.NumCandidates <= 0 {
		cfg.NumCandidates = DefaultIrodoriNumCandidates
	}
	if cfg.CFGGuidanceMode == "" {
		cfg.CFGGuidanceMode = DefaultIrodoriCFGGuidanceMode
	}
	if cfg.CFGScaleText == 0 {
		cfg.CFGScaleText = DefaultIrodoriCFGScaleText
	}
	if cfg.CFGScaleSpeaker == 0 {
		cfg.CFGScaleSpeaker = DefaultIrodoriCFGScaleSpeaker
	}
	if cfg.CFGMinT == 0 {
		cfg.CFGMinT = DefaultIrodoriCFGMinT
	}
	if cfg.CFGMaxT == 0 {
		cfg.CFGMaxT = DefaultIrodoriCFGMaxT
	}
	if !cfg.ContextKVCache {
		cfg.ContextKVCache = true
	}
	return cfg
}

func ResolveIrodoriVoiceID(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "shiro", "male", "male_01", "shi-gozaki", "shigozaki":
		return "shiro"
	default:
		return "mio"
	}
}

func ResolveIrodoriVoiceName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "shiro", "male", "male_01", "shi-gozaki", "shigozaki":
		return "Shiro"
	case "mio", "female", "female_01", "female_01_mio":
		return "Mio"
	default:
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return "Mio"
		}
		return trimmed
	}
}

func ResolveIrodoriStyle(emotion IrodoriStyleEmotion) string {
	switch strings.ToLower(strings.TrimSpace(emotion.Emotion)) {
	case "alert", "warning", "urgent":
		return "urgent"
	case "serious", "report":
		return "firm"
	case "cheerful", "happy":
		return "bright"
	case "warm", "soft":
		return "soft"
	case "flat":
		return "flat"
	case "calm":
		return "calm"
	}
	if emotion.Intensity == 0 && emotion.Expressiveness == 0 && emotion.Pitch == 0 && emotion.Speed == 0 && strings.TrimSpace(emotion.Pause) == "" {
		return "neutral"
	}
	if emotion.Intensity >= 0.75 {
		return "urgent"
	}
	if emotion.Expressiveness >= 0.65 || emotion.Pitch >= 0.58 {
		return "bright"
	}
	if (emotion.Speed > 0 && emotion.Speed <= 0.42) || emotion.Pause == "long" {
		return "calm"
	}
	return "neutral"
}

func BuildIrodoriSynthesisPayload(input IrodoriSynthesisPayloadInput) IrodoriSynthesisPayload {
	payload := IrodoriSynthesisPayload{
		Voice: strings.TrimSpace(input.Voice),
		Style: strings.TrimSpace(input.Style),
		Text:  EnsureTTSPunctuation(input.Text),
	}
	if input.Speed > 0 {
		payload.Speed = input.Speed
	}
	return payload
}

func BuildIrodoriUploadedAudioFileData(referenceAudio string) any {
	referenceAudio = strings.TrimSpace(referenceAudio)
	if referenceAudio == "" {
		return nil
	}
	return IrodoriUploadedAudioFileData{
		Path: referenceAudio,
		Meta: IrodoriUploadedAudioFileMeta{
			Type: "gradio.FileData",
		},
	}
}

func IrodoriSynthesisURL(baseURL, endpointPath string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil && strings.Trim(strings.TrimSpace(u.Path), "/") != "" {
		return base
	}
	return base + "/" + strings.TrimLeft(IrodoriEndpointPath(endpointPath), "/")
}

func IrodoriRunGenerationURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(strings.ToLower(base), "/gradio_api/run/_run_generation") {
		return base
	}
	return base + "/gradio_api/run/_run_generation"
}

func IrodoriRunGenerationData(cfg IrodoriRunGenerationConfig, text string, uploadedAudio any) []any {
	return []any{
		cfg.Checkpoint,
		cfg.ModelDevice,
		cfg.ModelPrecision,
		cfg.CodecDevice,
		cfg.CodecPrecision,
		cfg.EnableWatermark,
		text,
		uploadedAudio,
		cfg.NumSteps,
		cfg.NumCandidates,
		cfg.SeedRaw,
		cfg.CFGGuidanceMode,
		cfg.CFGScaleText,
		cfg.CFGScaleSpeaker,
		cfg.CFGScaleRaw,
		cfg.CFGMinT,
		cfg.CFGMaxT,
		cfg.ContextKVCache,
		cfg.TruncationFactorRaw,
		cfg.RescaleKRaw,
		cfg.RescaleSigmaRaw,
		cfg.SpeakerKVScaleRaw,
		cfg.SpeakerKVMinTRaw,
		cfg.SpeakerKVMaxLayersRaw,
	}
}

func RewriteLoopbackIrodoriFileURL(baseURL, rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return rawURL
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return rawURL
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base == nil || strings.TrimSpace(base.Host) == "" {
		return rawURL
	}
	parsed.Scheme = base.Scheme
	parsed.Host = base.Host
	return parsed.String()
}

func ResolveIrodoriAudioDownloadURL(baseURL, rawURL string) (string, error) {
	audioURL := strings.TrimSpace(rawURL)
	if audioURL == "" {
		return "", fmt.Errorf("irodori response did not include an audio url")
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasPrefix(audioURL, "/") {
		return base + audioURL, nil
	}
	if !strings.Contains(audioURL, "://") {
		return base + "/" + strings.TrimLeft(audioURL, "/"), nil
	}
	return RewriteLoopbackIrodoriFileURL(base, audioURL), nil
}

func ParseIrodoriSimpleAudioURL(raw json.RawMessage) string {
	var body struct {
		URL      string `json:"url"`
		AudioURL string `json:"audio_url"`
		WAVURL   string `json:"wav_url"`
		Path     string `json:"path"`
		Audio    *struct {
			URL      string `json:"url"`
			AudioURL string `json:"audio_url"`
			Path     string `json:"path"`
		} `json:"audio"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	for _, candidate := range []string{body.AudioURL, body.WAVURL, body.URL} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	if body.Audio != nil {
		for _, candidate := range []string{body.Audio.AudioURL, body.Audio.URL, body.Audio.Path} {
			if strings.TrimSpace(candidate) != "" {
				return candidate
			}
		}
	}
	return strings.TrimSpace(body.Path)
}

func ParseIrodoriAudioURL(raw json.RawMessage) (string, error) {
	if url := ParseIrodoriSimpleAudioURL(raw); strings.TrimSpace(url) != "" {
		return url, nil
	}
	var body struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("decode irodori response: %w", err)
	}
	if len(body.Data) == 0 || string(body.Data[0]) == "null" {
		return "", fmt.Errorf("irodori response has no generated audio candidate")
	}
	var candidate struct {
		URL      string `json:"url"`
		Path     string `json:"path"`
		OrigName string `json:"orig_name"`
		MIMEType string `json:"mime_type"`
		Value    *struct {
			URL      string `json:"url"`
			Path     string `json:"path"`
			OrigName string `json:"orig_name"`
			MIMEType string `json:"mime_type"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body.Data[0], &candidate); err != nil {
		return "", fmt.Errorf("decode irodori audio candidate: %w", err)
	}
	if strings.TrimSpace(candidate.URL) == "" && candidate.Value != nil {
		candidate.URL = candidate.Value.URL
	}
	if strings.TrimSpace(candidate.URL) == "" {
		return "", fmt.Errorf("irodori audio candidate has no url")
	}
	return candidate.URL, nil
}
