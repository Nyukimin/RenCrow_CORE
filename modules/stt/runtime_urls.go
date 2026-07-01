package stt

import (
	"fmt"
	"net/url"
	"strings"
)

const ProviderExternalHTTP = "external_http"

type RuntimeURLConfig struct {
	Provider    string
	ProviderURL string
	StreamURL   string
	TTSBaseURL  string
	ServerHost  string
	ServerPort  int
	TLSEnabled  bool
}

func StreamURL(config RuntimeURLConfig) string {
	if raw := strings.TrimSpace(config.StreamURL); raw != "" {
		return raw
	}
	return InferStreamURLFromProviderURL(config.ProviderURL)
}

func InferStreamURLFromProviderURL(providerURL string) string {
	u, err := url.Parse(strings.TrimSpace(providerURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	scheme := "ws"
	if strings.EqualFold(u.Scheme, "https") {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s/ws/transcribe", scheme, u.Host)
}

func InferBaseURL(config RuntimeURLConfig) string {
	if base := ExtractBaseFromProviderURL(config.ProviderURL); base != "" {
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

func ExtractBaseFromProviderURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}

func InferProviderURL(config RuntimeURLConfig) string {
	raw := strings.TrimSpace(config.ProviderURL)
	if raw != "" && strings.EqualFold(strings.TrimSpace(config.Provider), ProviderExternalHTTP) {
		return raw
	}
	if raw != "" {
		return raw
	}
	base := InferBaseURL(config)
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/stt/file"
}

func InferLegacyInferenceProviderURL(ttsBaseURL, sttProviderURL string) string {
	raw := strings.TrimSpace(sttProviderURL)
	if raw != "" {
		return raw
	}
	base := InferBaseURL(RuntimeURLConfig{
		TTSBaseURL:  ttsBaseURL,
		ProviderURL: sttProviderURL,
	})
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/inference"
}

func InferGatewayURL(sttGatewayURL, rencrowSTTURL string) string {
	if v := strings.TrimSpace(sttGatewayURL); v != "" {
		return v
	}
	return strings.TrimSpace(rencrowSTTURL)
}
