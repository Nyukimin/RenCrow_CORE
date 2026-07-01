package main

import (
	"net/http"
	"strings"
)

func withTailscaleViewerOnlyGuard(next http.Handler) http.Handler {
	if next == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isTailscaleViewerHost(r) && !isAllowedTailscaleViewerPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isTailscaleViewerHost(r *http.Request) bool {
	if r == nil {
		return false
	}
	host := firstHeaderValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	host = strings.ToLower(strings.TrimSpace(strings.TrimRight(host, ".")))
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i+1:], "]") {
		host = host[:i]
	}
	return strings.HasSuffix(host, ".ts.net")
}

func isAllowedTailscaleViewerPath(path string) bool {
	if path == "/viewer" || strings.HasPrefix(path, "/viewer/") {
		return true
	}
	return path == "/audio-router/events" || path == "/stt" || path == "/voice-chat" || path == "/voice-chat-ws"
}

func firstHeaderValue(value string) string {
	if i := strings.Index(value, ","); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}
