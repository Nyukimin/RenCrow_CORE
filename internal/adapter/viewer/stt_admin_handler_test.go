package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleSTTRestartPostsRestartAndWaitsForReadyHealth(t *testing.T) {
	healthCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/restart":
			if r.Method != http.MethodPost {
				t.Fatalf("restart method=%s, want POST", r.Method)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true,"status":"restarting","service":"stt-gateway"}`))
		case "/health":
			healthCalls++
			w.WriteHeader(http.StatusOK)
			if healthCalls == 1 {
				_, _ = w.Write([]byte(`{"ok":true,"status":"warming","ready":{"model_loaded":false}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"status":"ready","ready":{"model_loaded":true},"provider":{"model_loaded":true}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	handler := HandleSTTRestart(STTAdminOptions{
		BaseURL:      server.URL,
		InitialDelay: time.Millisecond,
		PollInterval: time.Millisecond,
		MaxWait:      time.Second,
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/viewer/stt/admin/restart", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["status"] != "ready" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if healthCalls < 2 {
		t.Fatalf("healthCalls=%d, want polling until ready", healthCalls)
	}
}

func TestHandleSTTRestartTimesOutWhenModelDoesNotLoad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/restart":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true,"status":"restarting","service":"stt-gateway"}`))
		case "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"status":"ready","ready":{"model_loaded":false}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	handler := HandleSTTRestart(STTAdminOptions{
		BaseURL:      server.URL,
		InitialDelay: time.Millisecond,
		PollInterval: time.Millisecond,
		MaxWait:      5 * time.Millisecond,
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/viewer/stt/admin/restart", nil))

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "restart_wait_timeout") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestIsSTTHealthReadyRequiresReadyStatusAndModelLoaded(t *testing.T) {
	if !isSTTHealthReady(`{"ok":true,"status":"ready","ready":{"model_loaded":true}}`) {
		t.Fatal("expected ready health accepted")
	}
	for _, body := range []string{
		`{"ok":true,"status":"ready","ready":{"model_loaded":false}}`,
		`{"ok":true,"status":"warming","ready":{"model_loaded":true}}`,
		`{"ok":false,"status":"ready","ready":{"model_loaded":true}}`,
		`not json`,
	} {
		if isSTTHealthReady(body) {
			t.Fatalf("expected not ready: %s", body)
		}
	}
}
