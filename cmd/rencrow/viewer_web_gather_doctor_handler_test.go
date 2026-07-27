package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleViewerWebGatherDoctorReportsChecks は
// GET /viewer/web-gather/doctor が診断結果を返すことを確認する
//
// CMD は実装本体を持たず CORE Public API 経由で診断するため、CLI の
// web-gather doctor と同じ情報を HTTP で取得できる必要がある。
func TestHandleViewerWebGatherDoctorReportsChecks(t *testing.T) {
	handler := handleViewerWebGatherDoctor(func() webGatherCLIDeps { return webGatherCLIDeps{} })

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/web-gather/doctor", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload webGatherDoctorResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(payload.Checks) == 0 {
		t.Fatal("expected at least one check")
	}
	// 依存が未設定なので少なくとも1件は失敗しているはず
	if payload.OK {
		t.Fatalf("expected ok=false when dependencies are unset: %+v", payload)
	}
}

// TestHandleViewerWebGatherDoctorRejectsNonGET は読み取り専用 endpoint が
// GET 以外を拒否することを確認する
func TestHandleViewerWebGatherDoctorRejectsNonGET(t *testing.T) {
	handler := handleViewerWebGatherDoctor(func() webGatherCLIDeps { return webGatherCLIDeps{} })

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(method, "/viewer/web-gather/doctor", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

// TestHandleViewerWebGatherDoctorHandlesMissingProvider は依存の供給元が
// nil でも panic せず 503 を返すことを確認する
func TestHandleViewerWebGatherDoctorHandlesMissingProvider(t *testing.T) {
	handler := handleViewerWebGatherDoctor(nil)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/web-gather/doctor", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
