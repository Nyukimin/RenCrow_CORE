package tts

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const gatewayServiceID = "tts-gateway"

type gatewaySynthesisResponse struct {
	GatewayService string `json:"gateway_service"`
	RequestID      string `json:"request_id"`
	AudioPath      string `json:"audio_path"`
	AudioURL       string `json:"audio_url"`
	VoiceID        string `json:"voice_id"`
	TargetID       string `json:"target_id"`
}

func decodeGatewaySynthesisResponse(body []byte) (gatewaySynthesisResponse, error) {
	var out gatewaySynthesisResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return gatewaySynthesisResponse{}, fmt.Errorf("decode RenCrow_TTS Gateway response: %w", err)
	}
	if strings.TrimSpace(out.GatewayService) != gatewayServiceID {
		return gatewaySynthesisResponse{}, fmt.Errorf("RenCrow_TTS response missing gateway_service=%q", gatewayServiceID)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.AudioPath), "/audio/") {
		return gatewaySynthesisResponse{}, fmt.Errorf("RenCrow_TTS Gateway response audio_path must start with /audio/")
	}
	return out, nil
}

func resolveGatewayRelayURL(baseURL, audioPath string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(mediaBaseURL(baseURL)))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid RenCrow_TTS Gateway base URL")
	}
	rawPath := strings.TrimSpace(audioPath)
	if !strings.HasPrefix(rawPath, "/audio/") {
		return "", fmt.Errorf("RenCrow_TTS Gateway audio_path must start with /audio/")
	}
	relative, err := url.Parse(rawPath)
	if err != nil || relative.IsAbs() || relative.Host != "" {
		return "", fmt.Errorf("invalid RenCrow_TTS Gateway audio_path")
	}
	return base.ResolveReference(relative).String(), nil
}
