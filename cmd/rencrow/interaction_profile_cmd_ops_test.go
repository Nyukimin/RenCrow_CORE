package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCmdDiagnosticsProfileAllowsReadOnlyOps は cmd-diagnostics profile が
// 読み取り専用の診断 endpoint だけを許可することを確認する
//
// CMD は CORE の Public API 経由で health / status / logs / evidence /
// llm-ops の状態を取得する。従来は profile が cmd-chat と cmd-idlechat しか
// 無く、これらの endpoint は 403 になっていた。
func TestCmdDiagnosticsProfileAllowsReadOnlyOps(t *testing.T) {
	handler := newProfileTestHandler()

	allowed := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/health"},
		{http.MethodGet, "/health/live"},
		{http.MethodGet, "/ready"},
		{http.MethodGet, "/viewer/status"},
		{http.MethodGet, "/viewer/logs"},
		{http.MethodGet, "/viewer/evidence/recent"},
		{http.MethodGet, "/viewer/evidence/detail"},
		{http.MethodGet, "/viewer/evidence/summary"},
		{http.MethodGet, "/viewer/llm-ops/health"},
		{http.MethodGet, "/viewer/llm-ops/status"},
		{http.MethodGet, "/viewer/source-registry"},
		{http.MethodGet, "/viewer/knowledge-memory"},
		{http.MethodGet, "/viewer/debug/system"},
		{http.MethodGet, "/viewer/channels"},
		{http.MethodGet, "/viewer/channels/probe"},
		{http.MethodGet, "/viewer/web-gather/doctor"},
	}
	for _, tc := range allowed {
		if got := profileRequestStatus(handler, "RenCrow_CMD", "cmd-diagnostics", tc.method, tc.path); got != http.StatusNoContent {
			t.Errorf("cmd-diagnostics %s %s = %d, want %d", tc.method, tc.path, got, http.StatusNoContent)
		}
	}
}

// TestCmdDiagnosticsProfileRejectsMutations は cmd-diagnostics が状態を
// 変更する操作を拒否することを確認する
func TestCmdDiagnosticsProfileRejectsMutations(t *testing.T) {
	handler := newProfileTestHandler()

	denied := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/viewer/llm-ops/start"},
		{http.MethodPost, "/viewer/llm-ops/stop"},
		{http.MethodPost, "/viewer/llm-ops/restart"},
		{http.MethodPost, "/viewer/repair/run"},
		{http.MethodPost, "/viewer/source-registry"},
		{http.MethodPost, "/viewer/send"},
		{http.MethodPost, "/internal/assistant/notifications/line"},
	}
	for _, tc := range denied {
		if got := profileRequestStatus(handler, "RenCrow_CMD", "cmd-diagnostics", tc.method, tc.path); got != http.StatusForbidden {
			t.Errorf("cmd-diagnostics %s %s = %d, want %d", tc.method, tc.path, got, http.StatusForbidden)
		}
	}
}

// TestCmdControlProfileAllowsProcessControl は cmd-control profile が
// 明示的に許可した制御操作だけを実行できることを確認する
//
// 破壊的操作を診断用 profile から分離するのは、CMD 仕様の
// 「一般操作と repair／process-control を認証scopeまたはcapability profileで
// 分ける」に従うため。
func TestCmdControlProfileAllowsProcessControl(t *testing.T) {
	handler := newProfileTestHandler()

	allowed := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/viewer/llm-ops/start"},
		{http.MethodPost, "/viewer/llm-ops/stop"},
		{http.MethodPost, "/viewer/llm-ops/restart"},
		{http.MethodPost, "/viewer/repair/run"},
		{http.MethodPost, "/viewer/source-registry"},
		// 制御結果の確認に必要な読み取りは許可する
		{http.MethodGet, "/viewer/llm-ops/status"},
		{http.MethodGet, "/health"},
	}
	for _, tc := range allowed {
		if got := profileRequestStatus(handler, "RenCrow_CMD", "cmd-control", tc.method, tc.path); got != http.StatusNoContent {
			t.Errorf("cmd-control %s %s = %d, want %d", tc.method, tc.path, got, http.StatusNoContent)
		}
	}
}

// TestCmdControlProfileRejectsOutOfScope は cmd-control が対話系や
// ASSISTANT 専用 endpoint を扱えないことを確認する
func TestCmdControlProfileRejectsOutOfScope(t *testing.T) {
	handler := newProfileTestHandler()

	denied := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/viewer/send"},
		{http.MethodGet, "/viewer/events"},
		{http.MethodPost, "/internal/assistant/notifications/line"},
		{http.MethodPost, "/viewer/idlechat/start"},
	}
	for _, tc := range denied {
		if got := profileRequestStatus(handler, "RenCrow_CMD", "cmd-control", tc.method, tc.path); got != http.StatusForbidden {
			t.Errorf("cmd-control %s %s = %d, want %d", tc.method, tc.path, got, http.StatusForbidden)
		}
	}
}

// TestCmdOpsProfilesRejectOtherClients は新しい profile が CMD 以外の
// client から使えないことを確認する
func TestCmdOpsProfilesRejectOtherClients(t *testing.T) {
	handler := newProfileTestHandler()

	for _, profile := range []string{"cmd-diagnostics", "cmd-control"} {
		for _, client := range []string{"RenCrow_PORTAL", "RenCrow_ASSISTANT", "Unknown"} {
			if got := profileRequestStatus(handler, client, profile, http.MethodGet, "/health"); got != http.StatusForbidden {
				t.Errorf("%s with client %s = %d, want %d", profile, client, got, http.StatusForbidden)
			}
		}
	}
}

func newProfileTestHandler() http.Handler {
	return withInteractionProfileGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func profileRequestStatus(handler http.Handler, client, profile, method, path string) int {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-RenCrow-Client", client)
	req.Header.Set(interactionProfileHeader, profile)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}
