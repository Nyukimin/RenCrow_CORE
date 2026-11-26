package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestRegisteredSystemJobsRouteRequiresBoardProfileAndOwnerBearer(t *testing.T) {
	token := strings.Repeat("j", 64)
	tokenPath := filepath.Join(t.TempDir(), "local-agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{
		Enabled: true, AuthTokenFile: tokenPath, UserID: "ren",
	}}
	deps := &Dependencies{taskCenterUserID: "ren"}
	mux := http.NewServeMux()
	registerRuntimeTaskActivitiesRoute(mux, cfg, deps)
	handler := withInteractionProfileGuard(mux)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/board/system-jobs", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	request.Header.Set(interactionProfileHeader, "board-studio-readonly")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing owner bearer response=%d body=%s; want 401 from registered route", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/board/system-jobs?raw=1", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	request.Header.Set(interactionProfileHeader, "board-studio-readonly")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("query-bearing route response=%d body=%s; want exact path rejection", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/board/system-jobs", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing board profile response=%d body=%s; want 403", recorder.Code, recorder.Body.String())
	}
}
