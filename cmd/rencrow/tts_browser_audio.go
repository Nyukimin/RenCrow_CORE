package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func buildLocalTTSAudioURL(outputDir, audioPath string) string {
	return moduletts.BuildLocalAudioURL(outputDir, audioPath)
}

func localTTSAudioRelPath(outputDir, audioPath string) (string, bool) {
	return moduletts.LocalAudioRelPath(outputDir, audioPath)
}

func handleLocalTTSAudio(outputDir string) http.HandlerFunc {
	return handleTTSAudio(outputDir, "")
}

func handleTTSAudio(outputDir, ttsBaseURL string) http.HandlerFunc {
	baseDir, ok := normalizeLocalTTSAudioBase(outputDir)
	allowedRemoteHost := allowedTTSAudioHost(ttsBaseURL)
	client := &http.Client{Timeout: 30 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		if rawURL := strings.TrimSpace(r.URL.Query().Get("url")); rawURL != "" {
			proxyRemoteTTSAudio(w, r, client, rawURL, allowedRemoteHost)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		target, ok := resolveLocalTTSAudioPath(baseDir, r.URL.Query().Get("path"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, target)
	}
}

func allowedTTSAudioHost(ttsBaseURL string) string {
	u, err := url.Parse(strings.TrimSpace(ttsBaseURL))
	if err != nil || u == nil {
		return ""
	}
	return strings.TrimSpace(u.Host)
}

func proxyRemoteTTSAudio(w http.ResponseWriter, r *http.Request, client *http.Client, rawURL, allowedHost string) {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil || !u.IsAbs() {
		http.NotFound(w, r)
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		http.NotFound(w, r)
		return
	}
	if !isAllowedRemoteTTSAudioHost(u.Host, allowedHost) {
		http.NotFound(w, r)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.NotFound(w, r)
		return
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "audio/wav"
	}
	w.Header().Set("Content-Type", contentType)
	if contentLength := strings.TrimSpace(resp.Header.Get("Content-Length")); contentLength != "" {
		w.Header().Set("Content-Length", contentLength)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

func isAllowedRemoteTTSAudioHost(rawHost, allowedHost string) bool {
	host := strings.TrimSpace(rawHost)
	configuredHost := strings.TrimSpace(allowedHost)
	if host == "" || configuredHost == "" {
		return false
	}
	return strings.EqualFold(host, configuredHost)
}

func normalizeLocalTTSAudioBase(outputDir string) (string, bool) {
	return moduletts.NormalizeLocalAudioBase(outputDir)
}

func resolveLocalTTSAudioPath(baseDir, rawRelPath string) (string, bool) {
	return moduletts.ResolveLocalAudioPath(baseDir, rawRelPath)
}
