package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type GatewayProviderConfig struct {
	BaseURL       string
	AuthToken     string
	VoiceID       string
	Speed         float64
	TLSSkipVerify bool
	Timeout       time.Duration
}

// GatewayProvider is the only production Provider owned by CORE. Target
// selection and engine-specific parameters stay behind RenCrow_TTS Gateway.
type GatewayProvider struct {
	bridge *RenCrowTTSBridge
}

func NewGatewayProvider(cfg GatewayProviderConfig) *GatewayProvider {
	return &GatewayProvider{bridge: NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL:    cfg.BaseURL,
		AuthToken:      cfg.AuthToken,
		VoiceID:        cfg.VoiceID,
		Speed:          cfg.Speed,
		TLSSkipVerify:  cfg.TLSSkipVerify,
		RequestTimeout: cfg.Timeout,
	})}
}

func (p *GatewayProvider) Name() string {
	return gatewayServiceID
}

func (p *GatewayProvider) Health(ctx context.Context) core.HealthReport {
	report := core.HealthReport{Module: "tts", Status: core.HealthDown}
	if p == nil || p.bridge == nil {
		report.Detail = "RenCrow_TTS Gateway is not configured"
		return report
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayHealthURL(p.bridge.cfg.HTTPBaseURL), nil)
	if err != nil {
		report.Detail = err.Error()
		return report
	}
	resp, err := p.bridge.client.Do(req)
	if err != nil {
		report.Detail = err.Error()
		return report
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		report.Detail = err.Error()
		return report
	}
	var state struct {
		GatewayService string `json:"gateway_service"`
		Status         string `json:"status"`
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || json.Unmarshal(body, &state) != nil || state.GatewayService != gatewayServiceID {
		report.Detail = fmt.Sprintf("RenCrow_TTS Gateway readiness failed status=%d", resp.StatusCode)
		return report
	}
	report.Status = core.HealthReady
	report.Ready = true
	report.Metadata = map[string]any{"provider": gatewayServiceID, "gateway_status": state.Status}
	return report
}

func (p *GatewayProvider) Synthesize(ctx context.Context, in SynthesisInput) (SynthesisOutput, error) {
	if p == nil || p.bridge == nil {
		return SynthesisOutput{}, fmt.Errorf("%w: RenCrow_TTS Gateway is not configured", ErrProviderUnavailable)
	}
	emotion := &moduletts.EmotionState{
		PrimaryEmotion: in.Emotion.Emotion,
		Prosody: moduletts.Prosody{
			Speed:          in.Emotion.Speed,
			Pitch:          in.Emotion.Pitch,
			Expressiveness: in.Emotion.Expressiveness,
		},
	}
	payload, err := moduletts.BuildSynthesisPayload(moduletts.SynthesisPayloadInput{
		Text:           in.Text,
		DefaultVoiceID: moduletts.ChooseNonEmpty(in.VoiceProfile.VoiceID, p.bridge.cfg.VoiceID),
		Speed:          p.bridge.cfg.Speed,
		Emotion:        emotion,
	})
	if err != nil {
		return SynthesisOutput{}, err
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return SynthesisOutput{}, fmt.Errorf("marshal RenCrow_TTS Gateway request: %w", err)
	}
	transportResult, err := p.bridge.postSynthesisWithRetry(ctx, reqBody)
	if err != nil {
		return SynthesisOutput{}, err
	}
	out, err := decodeGatewaySynthesisResponse(transportResult.body)
	if err != nil {
		return SynthesisOutput{}, err
	}
	audioFile, audioURL, err := downloadGatewayAudio(ctx, p.bridge.client, p.bridge.cfg.HTTPBaseURL, out.AudioPath, in.OutputDir, in.FilePrefix)
	if err != nil {
		return SynthesisOutput{}, err
	}
	return SynthesisOutput{
		RequestID:     transportResult.requestID,
		ResponseID:    transportResult.responseID,
		Provider:      gatewayServiceID,
		VoiceID:       out.VoiceID,
		AudioFilePath: audioFile,
		AudioURL:      audioURL,
	}, nil
}

func downloadGatewayAudio(ctx context.Context, client *http.Client, baseURL, audioPath, outputDir, filePrefix string) (string, string, error) {
	audioURL, err := resolveGatewayRelayURL(baseURL, audioPath)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build RenCrow_TTS Gateway audio request: %w", err)
	}
	owner, owned := domaintransport.ReceiptOwnerFromContext(ctx)
	requestID := core.NewRequestID()
	if owned {
		request, err := owner.BeginRequest(ctx, "tts.audio_download")
		if err != nil {
			return "", "", fmt.Errorf("begin TTS audio transport request: %w", err)
		}
		requestID = request.RequestID
	}
	req.Header.Set("Accept", "audio/wav")
	req.Header.Set("X-RenCrow-Request-ID", string(requestID))
	resp, err := client.Do(req)
	if err != nil {
		if owned {
			if receiptErr := owner.FailWithoutResponse(context.WithoutCancel(ctx), requestID, err.Error()); receiptErr != nil {
				return "", "", fmt.Errorf("download RenCrow_TTS Gateway audio: %w; persist transport failure: %v", err, receiptErr)
			}
		}
		return "", "", fmt.Errorf("download RenCrow_TTS Gateway audio: %w", err)
	}
	defer resp.Body.Close()
	if owned {
		if _, receiptErr := owner.CompleteResponse(context.WithoutCancel(ctx), requestID, ttsHTTPExternalRef(resp)); receiptErr != nil {
			return "", "", fmt.Errorf("persist TTS audio transport response: %w", receiptErr)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", "", fmt.Errorf("RenCrow_TTS Gateway audio status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	audioFile, err := saveGatewayWAV(io.LimitReader(resp.Body, 64<<20), outputDir, filePrefix)
	if err != nil {
		return "", "", err
	}
	return audioFile, audioURL, nil
}

func ttsHTTPExternalRef(response *http.Response) string {
	for _, key := range []string{"X-Provider-Request-ID", "X-Request-ID", "Request-ID", "ETag"} {
		if value := strings.TrimSpace(response.Header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func gatewayHealthURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(mediaBaseURL(base)), "/")
	return base + "/health/ready"
}
