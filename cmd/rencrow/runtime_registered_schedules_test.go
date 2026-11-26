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

func TestRegisteredSchedulesRouteRequiresBoardProfileAndOwnerBearer(t *testing.T) {
	token := strings.Repeat("r", 64)
	tokenPath := filepath.Join(t.TempDir(), "local-agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}}
	mux := http.NewServeMux()
	registerRuntimeTaskActivitiesRoute(mux, cfg, &Dependencies{taskCenterUserID: "ren"})
	handler := withInteractionProfileGuard(mux)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/board/registered-schedules?source=scheduler", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	request.Header.Set(interactionProfileHeader, "board-studio-readonly")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer response=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/board/registered-schedules?source=scheduler", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	request.Header.Set(interactionProfileHeader, "board-studio-readonly")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"availability":"unavailable"`) || !strings.Contains(recorder.Body.String(), `"items":[]`) {
		t.Fatalf("missing stores response=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
