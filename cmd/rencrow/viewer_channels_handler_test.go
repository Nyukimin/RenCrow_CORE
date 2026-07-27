package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubChannelRegistry struct {
	names  []string
	probes map[string]error
}

func (s stubChannelRegistry) List() []string { return s.names }

func (s stubChannelRegistry) ProbeAll(_ context.Context) map[string]error { return s.probes }

func fixedChannelNow() time.Time {
	return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
}

// TestHandleViewerChannelsListReturnsConfiguredChannels は
// GET /viewer/channels が設定済みチャネル一覧を返すことを確認する
//
// CMD は実装本体を持たず CORE Public API 経由で診断するため、CLI の
// channels list と同じ情報を HTTP で取得できる必要がある。
func TestHandleViewerChannelsListReturnsConfiguredChannels(t *testing.T) {
	handler := handleViewerChannelsList(stubChannelRegistry{names: []string{"line", "slack"}}, fixedChannelNow)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/channels", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK        bool   `json:"ok"`
		Component string `json:"component"`
		Status    string `json:"status"`
		Details   struct {
			Channels []string `json:"channels"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK || payload.Component != "channels" || payload.Status != "configured" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if len(payload.Details.Channels) != 2 {
		t.Fatalf("channels = %v, want 2 entries", payload.Details.Channels)
	}
}

func TestHandleViewerChannelsListReportsEmpty(t *testing.T) {
	handler := handleViewerChannelsList(stubChannelRegistry{}, fixedChannelNow)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/channels", nil))

	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload.Status != "empty" {
		t.Fatalf("status = %q, want empty", payload.Status)
	}
}

// TestHandleViewerChannelsProbeReportsDegraded は疎通失敗が degraded として
// 報告されることを確認する
func TestHandleViewerChannelsProbeReportsDegraded(t *testing.T) {
	handler := handleViewerChannelsProbe(stubChannelRegistry{
		names:  []string{"line", "slack"},
		probes: map[string]error{"line": nil, "slack": errors.New("unreachable")},
	}, fixedChannelNow)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/channels/probe", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		OK      bool   `json:"ok"`
		Status  string `json:"status"`
		Details struct {
			Results map[string]struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"results"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload.OK || payload.Status != "degraded" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.Details.Results["slack"].OK || payload.Details.Results["slack"].Error != "unreachable" {
		t.Fatalf("unexpected slack result: %+v", payload.Details.Results["slack"])
	}
	if !payload.Details.Results["line"].OK {
		t.Fatalf("unexpected line result: %+v", payload.Details.Results["line"])
	}
}

// TestViewerChannelsHandlersRejectNonGET は読み取り専用 endpoint が
// GET 以外を拒否することを確認する
func TestViewerChannelsHandlersRejectNonGET(t *testing.T) {
	registry := stubChannelRegistry{names: []string{"line"}}
	handlers := map[string]http.HandlerFunc{
		"/viewer/channels":       handleViewerChannelsList(registry, fixedChannelNow),
		"/viewer/channels/probe": handleViewerChannelsProbe(registry, fixedChannelNow),
	}
	for path, handler := range handlers {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want %d", method, path, rec.Code, http.StatusMethodNotAllowed)
			}
		}
	}
}
