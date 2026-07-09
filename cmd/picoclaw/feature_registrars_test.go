package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/viewer"
)

func TestRegisterFeatureRoutesKeepsExistingRouteGroups(t *testing.T) {
	mux := http.NewServeMux()
	deps := &Dependencies{
		lineHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		eventHub: viewer.NewEventHub(10),
		viewerSend: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
		viewerGamesStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		viewerGamesDecision: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}),
		viewerGamesResult: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		viewerGamesSessions: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusPartialContent)
		}),
		viewerGamesEvents: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusResetContent)
		}),
		viewerGamesObserverPage: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}),
		viewerGamesObserverProxy: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}),
		viewerStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		viewerJobs: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
		viewerLogs: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusPartialContent)
		}),
		workstreamStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}),
		revenueStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		schedulerStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusResetContent)
		}),
		browserTraceAPIStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		complexityHotspotStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
		viewerMemorySnapshot: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		viewerSourceRegistry: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusPartialContent)
		}),
		knowledgeMemoryStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}),
		evidenceHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		skillGovernanceRecent: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		sandboxStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusPartialContent)
		}),
		superAgentStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusResetContent)
		}),
		aiWorkflowStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAlreadyReported)
		}),
		entryHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
		chromeBridgeStatus: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}),
	}
	cfg := &config.Config{WorkspaceDir: t.TempDir()}

	registerFeatureRoutes(
		mux,
		cfg,
		deps,
		sttRuntime{WSHandler: http.NotFoundHandler()},
		voiceChatRuntime{WSHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusIMUsed)
		})},
		viewer.DebugSystemOptions{},
	)

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "channel webhook", method: http.MethodGet, path: "/webhook", want: http.StatusNoContent},
		{name: "viewer page", method: http.MethodGet, path: "/viewer", want: http.StatusOK},
		{name: "viewer runtime config", method: http.MethodGet, path: "/viewer/runtime-config", want: http.StatusOK},
		{name: "viewer backlog", method: http.MethodGet, path: "/viewer/backlog", want: http.StatusOK},
		{name: "viewer scheduler", method: http.MethodGet, path: "/viewer/scheduler", want: http.StatusResetContent},
		{name: "viewer ops status", method: http.MethodGet, path: "/viewer/status", want: http.StatusOK},
		{name: "viewer jobs", method: http.MethodGet, path: "/viewer/jobs", want: http.StatusAccepted},
		{name: "viewer logs", method: http.MethodGet, path: "/viewer/logs", want: http.StatusPartialContent},
		{name: "viewer workstreams", method: http.MethodGet, path: "/viewer/workstreams", want: http.StatusCreated},
		{name: "viewer revenue", method: http.MethodGet, path: "/viewer/revenue", want: http.StatusNoContent},
		{name: "viewer dynamic send", method: http.MethodGet, path: "/viewer/send", want: http.StatusAccepted},
		{name: "viewer games status", method: http.MethodGet, path: "/viewer/games/status", want: http.StatusOK},
		{name: "viewer games decision", method: http.MethodGet, path: "/viewer/games/decision", want: http.StatusCreated},
		{name: "viewer games result", method: http.MethodGet, path: "/viewer/games/result", want: http.StatusNoContent},
		{name: "viewer games sessions", method: http.MethodGet, path: "/viewer/games/sessions", want: http.StatusPartialContent},
		{name: "viewer games events", method: http.MethodGet, path: "/viewer/games/events", want: http.StatusResetContent},
		{name: "viewer games observer", method: http.MethodGet, path: "/viewer/games/observer", want: http.StatusTeapot},
		{name: "viewer games observer proxy", method: http.MethodGet, path: "/viewer/games/observer-api/games/status", want: http.StatusBadGateway},
		{name: "module manifest", method: http.MethodGet, path: moduleManifestPath, want: http.StatusOK},
		{name: "stt chat input", method: http.MethodGet, path: "/stt/chat-input", want: http.StatusMethodNotAllowed},
		{name: "voice chat primary", method: http.MethodGet, path: "/voice-chat", want: http.StatusIMUsed},
		{name: "voice chat alias", method: http.MethodGet, path: "/voice-chat-ws", want: http.StatusIMUsed},
		{name: "browser trace api", method: http.MethodGet, path: "/viewer/browser-trace-api", want: http.StatusOK},
		{name: "complexity hotspots", method: http.MethodGet, path: "/viewer/complexity-hotspots", want: http.StatusAccepted},
		{name: "memory snapshot", method: http.MethodGet, path: "/viewer/memory/snapshot", want: http.StatusNoContent},
		{name: "source registry", method: http.MethodGet, path: "/viewer/source-registry", want: http.StatusPartialContent},
		{name: "knowledge memory", method: http.MethodGet, path: "/viewer/knowledge-memory", want: http.StatusCreated},
		{name: "evidence recent", method: http.MethodGet, path: "/viewer/evidence/recent", want: http.StatusOK},
		{name: "skill governance", method: http.MethodGet, path: "/viewer/skill-governance/recent", want: http.StatusNoContent},
		{name: "sandbox status", method: http.MethodGet, path: "/viewer/sandbox", want: http.StatusPartialContent},
		{name: "superagent status", method: http.MethodGet, path: "/viewer/superagent", want: http.StatusResetContent},
		{name: "ai workflow status", method: http.MethodGet, path: "/viewer/ai-workflow", want: http.StatusAlreadyReported},
		{name: "entry", method: http.MethodGet, path: "/entry", want: http.StatusAccepted},
		{name: "chrome bridge status", method: http.MethodGet, path: "/chrome/bridge/status", want: http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("%s %s status=%d want=%d body=%s", tt.method, tt.path, rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}
