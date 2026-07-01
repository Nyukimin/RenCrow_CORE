package stt

import (
	"encoding/json"
	"strings"
)

const (
	WebSocketModeProvider = "provider"
	WebSocketModeHTTP     = "http"
	WebSocketModeGateway  = "gateway"
)

var WebSocketRoutePaths = []string{"/stt", "/stt-ws", "/ws"}

type WebSocketHandlerPlan struct {
	Mode        string
	ProviderURL string
	GatewayURL  string
}

func BuildWebSocketHandlerPlan(providerAvailable bool, providerURL, gatewayURL string) WebSocketHandlerPlan {
	gatewayURL = strings.TrimSpace(gatewayURL)
	if gatewayURL != "" {
		return WebSocketHandlerPlan{
			Mode:       WebSocketModeGateway,
			GatewayURL: gatewayURL,
		}
	}
	providerURL = strings.TrimSpace(providerURL)
	if providerAvailable {
		return WebSocketHandlerPlan{
			Mode:        WebSocketModeProvider,
			ProviderURL: providerURL,
		}
	}
	return WebSocketHandlerPlan{
		Mode:        WebSocketModeHTTP,
		ProviderURL: providerURL,
	}
}

func IsWebSocketTextFramePayload(payload []byte) bool {
	if len(payload) == 0 {
		return true
	}
	switch payload[0] {
	case '{', '[', '"':
		return json.Valid(payload)
	default:
		return false
	}
}
