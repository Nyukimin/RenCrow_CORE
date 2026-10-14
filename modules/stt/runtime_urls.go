package stt

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	ProviderRenCrowSTT = "rencrow_stt"
)

var WebSocketRoutePaths = []string{"/stt"}

func GatewayTranscriptionURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "http://127.0.0.1:8766"
	}
	return base + "/v1/audio/transcriptions"
}

// StreamPath is the fixed path of the RenCrow_STT audio stream (stt.stream.v1).
const StreamPath = "/v1/audio/stream"

// GatewayStreamURL is the WebSocket URL of the RenCrow_STT audio stream for a
// Gateway base URL: the scheme becomes ws (wss for https) and the fixed
// StreamPath is appended the way GatewayTranscriptionURL appends its own. It
// returns "" for a base that is not an absolute http(s) URL, so the caller can
// report a configuration error instead of dialling something else.
func GatewayStreamURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "http://127.0.0.1:8766"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return ""
	}
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/") + StreamPath
}

type RuntimeURLConfig struct {
	Provider       string
	GatewayHTTPURL string
	TTSBaseURL     string
	ServerHost     string
	ServerPort     int
	TLSEnabled     bool
}

func InferBaseURL(config RuntimeURLConfig) string {
	if base := ExtractBaseFromGatewayHTTPURL(config.GatewayHTTPURL); base != "" {
		return base
	}
	if base := InferBaseURLFromTTS(config.TTSBaseURL); base != "" {
		return base
	}
	host := strings.TrimSpace(config.ServerHost)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if config.TLSEnabled {
		scheme = "https"
	}
	port := config.ServerPort
	if port <= 0 {
		port = 8080
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

func InferBaseURLFromTTS(ttsBaseURL string) string {
	u, err := url.Parse(strings.TrimSpace(ttsBaseURL))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s:%d", u.Scheme, u.Hostname(), 8080)
}

func ExtractBaseFromGatewayHTTPURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}

func InferGatewayHTTPURL(config RuntimeURLConfig) string {
	raw := strings.TrimSpace(config.GatewayHTTPURL)
	if raw != "" {
		return raw
	}
	base := InferBaseURL(config)
	if base == "" {
		return ""
	}
	return GatewayTranscriptionURL(base)
}
