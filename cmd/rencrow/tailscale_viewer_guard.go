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
		if isTailscaleViewerHost(r) && !isAllowedTailscaleRequest(r) {
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

func isAllowedTailscaleRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method == http.MethodPost && r.URL.Path == "/webhook/line" {
		return true
	}
	// tailscaled marks internet-originated Funnel requests. Keep every Viewer,
	// Debug, Ops, and voice route private even though port 443 must be placed in
	// Funnel mode for LINE. Tailnet Serve traffic is not marked and retains the
	// existing allowlist below, including access from tagged devices.
	if strings.TrimSpace(r.Header.Get("Tailscale-Funnel-Request")) != "" {
		return false
	}
	if isAllowedTailscaleCMDInteractionRequest(r) {
		return true
	}
	if isAllowedTailscalePortalInteractionRequest(r) {
		return true
	}
	client := strings.TrimSpace(r.Header.Get("X-RenCrow-Client"))
	profile := strings.ToLower(strings.TrimSpace(r.Header.Get(interactionProfileHeader)))
	if client == "RenCrow_CMD" || strings.HasPrefix(profile, "cmd-") {
		return false
	}
	path := r.URL.Path
	if path == "/viewer" || strings.HasPrefix(path, "/viewer/") {
		return true
	}
	return path == "/audio-router/events" || path == "/stt" || path == "/voice-chat" || path == "/voice-chat-ws"
}

func isAllowedTailscalePortalInteractionRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost || r.URL.Path != "/stt/chat-input" {
		return false
	}
	client := strings.TrimSpace(r.Header.Get("X-RenCrow-Client"))
	profile := strings.ToLower(strings.TrimSpace(r.Header.Get(interactionProfileHeader)))
	if client != "RenCrow_PORTAL" || profile != "portal-chat" {
		return false
	}
	policy, ok := interactionProfilePolicies[profile]
	if !ok || policy.client != client || policy.allow == nil || !policy.allow(r.Method, r.URL.Path) {
		return false
	}
	values := headerValuesCaseInsensitive(r.Header, "X-RenCrow-Authenticated-Actor")
	return len(values) == 1 && validTailscaleAuthenticatedActor(values[0])
}

func headerValuesCaseInsensitive(header http.Header, name string) []string {
	var values []string
	for key, entries := range header {
		if strings.EqualFold(key, name) {
			values = append(values, entries...)
		}
	}
	return values
}

func validTailscaleAuthenticatedActor(value string) bool {
	const prefix = "tailscale-sha256:"
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	for _, character := range strings.TrimPrefix(value, prefix) {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func isAllowedTailscaleCMDInteractionRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	client := strings.TrimSpace(r.Header.Get("X-RenCrow-Client"))
	profile := strings.ToLower(strings.TrimSpace(r.Header.Get(interactionProfileHeader)))
	if client != "RenCrow_CMD" || !strings.HasPrefix(profile, "cmd-") {
		return false
	}
	policy, ok := interactionProfilePolicies[profile]
	return ok && policy.client == "RenCrow_CMD" && policy.allow != nil && policy.allow(r.Method, r.URL.Path)
}

func firstHeaderValue(value string) string {
	if i := strings.Index(value, ","); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}
