package main

import (
	"net/http"
	"strings"
)

const interactionProfileHeader = "X-RenCrow-Interaction-Profile"

type interactionProfilePolicy struct {
	client string
	allow  func(method, path string) bool
}

var interactionProfilePolicies = map[string]interactionProfilePolicy{
	"portal-chat": {
		client: "RenCrow_PORTAL",
		allow:  portalChatInteractionAllowed,
	},
	"portal-idlechat": {
		client: "RenCrow_PORTAL",
		allow:  portalIdleChatInteractionAllowed,
	},
	"cmd-chat": {
		client: "RenCrow_CMD",
		allow: func(method, path string) bool {
			return (method == http.MethodGet && path == "/viewer/events") ||
				(method == http.MethodPost && path == "/viewer/send")
		},
	},
	"cmd-idlechat": {
		client: "RenCrow_CMD",
		allow: func(method, path string) bool {
			if method == http.MethodGet {
				return path == "/viewer/events" || path == "/viewer/idlechat/status"
			}
			return method == http.MethodPost &&
				(path == "/viewer/idlechat/start" || path == "/viewer/idlechat/stop")
		},
	},
	"assistant-core": {
		client: "RenCrow_ASSISTANT",
		allow: func(method, path string) bool {
			return (method == http.MethodGet && path == "/viewer/events") ||
				(method == http.MethodPost && path == "/viewer/send")
		},
	},
}

func withInteractionProfileGuard(next http.Handler) http.Handler {
	if next == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := strings.TrimSpace(r.Header.Get("X-RenCrow-Client"))
		profile := strings.ToLower(strings.TrimSpace(r.Header.Get(interactionProfileHeader)))
		if client == "" && profile == "" {
			next.ServeHTTP(w, r)
			return
		}
		policy, ok := interactionProfilePolicies[profile]
		if !ok || client != policy.client || !policy.allow(r.Method, r.URL.Path) {
			http.Error(w, "interaction profileでは許可されていない操作です", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func portalIdleChatInteractionAllowed(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/health",
		"/viewer/events",
		"/viewer/idlechat/status",
		"/viewer/runtime-config",
		"/viewer/character-states":
		return true
	default:
		return false
	}
}

func portalChatInteractionAllowed(method, path string) bool {
	if portalIdleChatInteractionAllowed(method, path) {
		return true
	}
	if method == http.MethodGet {
		return path == "/viewer/tts/audio" || path == "/stt"
	}
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/viewer/send",
		"/viewer/idlechat/start",
		"/viewer/idlechat/stop",
		"/viewer/recipient-selection",
		"/viewer/active-control",
		"/viewer/tts/playback-ack":
		return true
	default:
		return false
	}
}
