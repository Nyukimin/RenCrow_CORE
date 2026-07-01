package voicechat

import (
	"fmt"
	"net/url"
	"strings"
)

func InferGatewayURL(explicitGatewayURL, rencrowChatWSURL, chatBaseURL string) string {
	if v := strings.TrimSpace(explicitGatewayURL); v != "" {
		return v
	}
	if v := strings.TrimSpace(rencrowChatWSURL); v != "" {
		return v
	}
	return InferGatewayURLFromChatBase(chatBaseURL)
}

func InferGatewayURLFromChatBase(chatBaseURL string) string {
	u, err := url.Parse(strings.TrimSpace(chatBaseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	scheme := "ws"
	if strings.EqualFold(u.Scheme, "https") {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s/v1/chat/audio/sessions", scheme, u.Host)
}
