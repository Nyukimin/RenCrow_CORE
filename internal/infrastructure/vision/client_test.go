package vision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestClientAnalyzeUsesVisionMultipartContract(t *testing.T) {
	requestID := string(modulecore.NewRequestID())
	var gotHeaderRequestID string
	var gotFormRequestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/vision/analyze" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotHeaderRequestID = r.Header.Get("X-Request-Id")
		gotFormRequestID = r.FormValue("request_id")
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
		fmt.Fprintf(w, `{"ok":true,"request_id":%q,"provider":"rencrow_vision","model":"Wild","kind":"image","summary":"要約","text":"解析結果","segments":[],"metadata":{"width":1}}`, requestID)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.Analyze(context.Background(), domainvision.AnalyzeRequest{
		RequestID:   requestID,
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
	if gotHeaderRequestID != requestID || gotFormRequestID != requestID {
		t.Fatalf("request IDs: header=%q form=%q want=%q", gotHeaderRequestID, gotFormRequestID, requestID)
	}
	if !result.OK || result.Text != "解析結果" || result.Model != "Wild" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientAnalyzeRejectsInvalidRequestIDBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cases := []struct {
		name string
		id   string
	}{
		{name: "empty"},
		{name: "malformed", id: "trace-secret-request-id"},
		{name: "wrong-kind", id: string(modulecore.NewTraceID())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Analyze(context.Background(), domainvision.AnalyzeRequest{
				RequestID: tc.id,
				Data:      []byte("image"),
			})
			var serviceErr *ServiceError
			if !errorsAs(err, &serviceErr) {
				t.Fatalf("error = %T %v, want ServiceError", err, err)
			}
			if serviceErr.Code != "VISION_INVALID_REQUEST_ID" {
				t.Fatalf("error code = %q, want VISION_INVALID_REQUEST_ID", serviceErr.Code)
			}
			if tc.id != "" && strings.Contains(err.Error(), tc.id) {
				t.Fatalf("invalid request ID leaked in error: %v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls)
	}
}

func TestClientAnalyzeRejectsUnmatchedSuccessfulResponseRequestID(t *testing.T) {
	requestID := string(modulecore.NewRequestID())
	cases := []struct {
		name       string
		responseID string
	}{
		{name: "missing"},
		{name: "malformed", responseID: "trace-secret-response-id"},
		{name: "other", responseID: string(modulecore.NewRequestID())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.responseID == "" {
					fmt.Fprint(w, `{"ok":true,"provider":"rencrow_vision","model":"Wild","kind":"image","text":"解析結果"}`)
					return
				}
				fmt.Fprintf(w, `{"ok":true,"request_id":%q,"provider":"rencrow_vision","model":"Wild","kind":"image","text":"解析結果"}`, tc.responseID)
			}))
			defer server.Close()

			client, err := NewClient(server.URL, time.Second)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			result, err := client.Analyze(context.Background(), domainvision.AnalyzeRequest{
				RequestID: requestID,
				Data:      []byte("image"),
			})
			if !reflect.DeepEqual(result, domainvision.AnalyzeResult{}) {
				t.Fatalf("result accepted despite identity mismatch: %+v", result)
			}
			var serviceErr *ServiceError
			if !errorsAs(err, &serviceErr) {
				t.Fatalf("error = %T %v, want ServiceError", err, err)
			}
			if serviceErr.Code != "VISION_IDENTITY_MISMATCH" {
				t.Fatalf("error code = %q, want VISION_IDENTITY_MISMATCH", serviceErr.Code)
			}
			if strings.Contains(err.Error(), requestID) || (tc.responseID != "" && strings.Contains(err.Error(), tc.responseID)) || strings.Contains(err.Error(), "解析結果") {
				t.Fatalf("request identity leaked in error: %v", err)
			}
		})
	}
}

func TestClientAnalyzePreservesVisionErrorCode(t *testing.T) {
	requestID := string(modulecore.NewRequestID())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		fmt.Fprintf(w, `{"ok":false,"request_id":%q,"error_code":"VISION_UNSUPPORTED_MEDIA","message":"unsupported"}`, requestID)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Analyze(context.Background(), domainvision.AnalyzeRequest{
		RequestID: requestID,
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
			"provider": "rencrow_vision", "model": "Vision",
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
