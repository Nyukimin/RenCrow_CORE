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

func TestRegisteredTaskActivitiesRoutePassesBoardProfileAndOwnerBearer(t *testing.T) {
	token := strings.Repeat("a", 64)
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
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/board/task-activities?view=current", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	request.Header.Set(interactionProfileHeader, "board-studio-readonly")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "task_activities_unavailable") {
		t.Fatalf("registered Task activities response=%d body=%s; want authenticated handler response, not profile denial", recorder.Code, recorder.Body.String())
	}
}
