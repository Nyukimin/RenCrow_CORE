package vision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
)

func TestClientAnalyzeUsesVisionMultipartContract(t *testing.T) {
	var gotRequestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/vision/analyze" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotRequestID = r.Header.Get("X-Request-Id")
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		if header.Filename != "sample.png" || r.FormValue("kind") != "image" {
			http.Error(w, "wrong media fields", http.StatusBadRequest)
			return
		}
		if r.FormValue("prompt") != "説明して" || r.FormValue("session_id") != "session-1" {
			http.Error(w, "wrong context fields", http.StatusBadRequest)
			return
		}
		if r.FormValue("max_frames") != "8" || r.FormValue("output_format") != "json" {
			http.Error(w, "wrong output fields", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"request_id":"trace-1","provider":"openai_compatible","model":"Wild","kind":"image","summary":"要約","text":"解析結果","segments":[],"metadata":{"width":1}}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.Analyze(context.Background(), domainvision.AnalyzeRequest{
		RequestID:   "trace-1",
		SessionID:   "session-1",
		Prompt:      "説明して",
		Kind:        "image",
		Filename:    "sample.png",
		ContentType: "image/png",
		Data:        []byte("png-data"),
		MaxFrames:   8,
		Language:    "ja",
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if gotRequestID != "trace-1" {
		t.Fatalf("X-Request-Id = %q", gotRequestID)
	}
	if !result.OK || result.Text != "解析結果" || result.Model != "Wild" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientAnalyzePreservesVisionErrorCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		fmt.Fprint(w, `{"ok":false,"request_id":"trace-2","error_code":"VISION_UNSUPPORTED_MEDIA","message":"unsupported"}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Analyze(context.Background(), domainvision.AnalyzeRequest{
		RequestID: "trace-2",
		Filename:  "bad.txt",
		Data:      []byte("bad"),
	})
	var serviceErr *ServiceError
	if !errorsAs(err, &serviceErr) {
		t.Fatalf("error = %T %v, want ServiceError", err, err)
	}
	if serviceErr.Code != "VISION_UNSUPPORTED_MEDIA" || serviceErr.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("unexpected service error: %+v", serviceErr)
	}
}

func TestClientHealthRequiresReadyModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "status": "ready", "service": "rencrow-vision",
			"provider": "openai_compatible", "model": "Vision",
			"ready": map[string]any{"model_loaded": true, "tmp_writable": true},
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	report, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !report.OK || report.Status != "ready" || !report.Ready.ModelLoaded {
		t.Fatalf("unexpected health: %+v", report)
	}
}

func TestNewClientRejectsNonAbsoluteHTTPURL(t *testing.T) {
	_, err := NewClient("127.0.0.1:8770", time.Second)
	if err == nil || !strings.Contains(err.Error(), "absolute HTTP URL") {
		t.Fatalf("error = %v", err)
	}
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
