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
	"cmd-diagnostics": {
		client: "RenCrow_CMD",
		allow:  cmdDiagnosticsInteractionAllowed,
	},
	"cmd-control": {
		client: "RenCrow_CMD",
		allow:  cmdControlInteractionAllowed,
	},
	"assistant-core": {
		client: "RenCrow_ASSISTANT",
		allow: func(method, path string) bool {
			return (method == http.MethodGet && path == "/viewer/events") ||
				(method == http.MethodPost &&
					(path == "/viewer/send" || path == "/internal/assistant/notifications/line"))
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
		if r.URL.Path == "/internal/assistant/notifications/line" &&
			(client != "RenCrow_ASSISTANT" || profile != "assistant-core") {
			http.Error(w, "interaction profileでは許可されていない操作です", http.StatusForbidden)
			return
		}
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

// cmdDiagnosticsInteractionAllowed は CMD の診断・状態取得を許可する
//
// 読み取り専用に限定する。状態を変更する操作は cmd-control 側で扱う。
// CMD 仕様の「一般操作と repair／process-control を認証scopeまたは
// capability profileで分ける」に従った分割である。
func cmdDiagnosticsInteractionAllowed(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/health",
		"/health/live",
		"/ready",
		"/viewer/status",
		"/viewer/logs",
		"/viewer/evidence/recent",
		"/viewer/evidence/detail",
		"/viewer/evidence/summary",
		"/viewer/source-registry",
		"/viewer/knowledge-memory",
		"/viewer/debug/system",
		"/viewer/channels",
		"/viewer/channels/probe",
		"/viewer/web-gather/doctor":
		return true
	default:
		return false
	}
}

// cmdControlInteractionAllowed は CMD の制御操作を許可する
//
// process 制御と repair 実行という影響の大きい操作を含むため、対話系
// （/viewer/send、/viewer/events、IdleChat）は対象外とする。制御結果の
// 確認に必要な読み取りだけを cmd-diagnostics から引き継ぐ。
func cmdControlInteractionAllowed(method, path string) bool {
	if method == http.MethodGet {
		switch path {
		case "/health",
			"/health/live",
			"/ready",
			"/viewer/status":
			return true
		default:
			return false
		}
	}
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/viewer/repair/run",
		"/viewer/source-registry":
		return true
	default:
		return false
	}
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
