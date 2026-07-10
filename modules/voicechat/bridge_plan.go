package voicechat

import "strings"

type BridgePlan struct {
	Enabled    bool
	GatewayURL string
	Available  bool
	Disabled   bool
	InputMode  string
}

func BuildBridgePlan(enabled bool, gatewayURL, voiceInputMode string) BridgePlan {
	gatewayURL = strings.TrimSpace(gatewayURL)
	return BridgePlan{
		Enabled:    enabled,
		GatewayURL: gatewayURL,
		Available:  enabled && gatewayURL != "",
		Disabled:   !enabled,
		InputMode:  NormalizeVoiceInputMode(voiceInputMode),
	}
}
