package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
)

func TestHandleSendUsesViewerRecipientContract(t *testing.T) {
	received := make(chan SendRequest, 1)
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		received <- req
		return "ok", nil
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{
			"message":"作業手順を相談したい",
			"to":"shiro",
			"viewer_client_id":"portal-tab-1",
			"input_source":"stt",
			"user_id":"viewer-user",
			"device_name":"Linux x86_64"
		}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	req.Header.Set("X-Forwarded-For", "203.0.113.42")
	req.Header.Set("User-Agent", "Mozilla/5.0 test-browser")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	for _, key := range []string{"model_alias", "base_url", "model", "route_prefix"} {
		if _, ok := body[key]; ok {
			t.Fatalf("normal viewer send response must not include legacy %s: %#v", key, body)
		}
	}
	jobID, _ := body["job_id"].(string)
	if jobID == "" {
		t.Fatalf("viewer send response must include job_id: %#v", body)
	}
	if body["viewer_client_id"] != "portal-tab-1" || body["recipient"] != "shiro" {
		t.Fatalf("viewer send response must echo correlation fields: %#v", body)
	}

	select {
	case got := <-received:
		if got.Message != "作業手順を相談したい" {
			t.Fatalf("unexpected handler message: %q", got.Message)
		}
		if got.To != "shiro" {
			t.Fatalf("recipient = %q, want shiro", got.To)
		}
		if got.JobID != jobID || got.ViewerClientID != "portal-tab-1" {
			t.Fatalf("correlation = job:%q client:%q, want job:%q client:portal-tab-1", got.JobID, got.ViewerClientID, jobID)
		}
		want := RequestProvenance{
			OperationSource: "RenCrow_PORTAL",
			InputSource:     "stt",
			UserID:          "viewer-user",
			DeviceName:      "Linux x86_64",
			SourceIPMasked:  "203.0.113.x",
			UserAgent:       "Mozilla/5.0 test-browser",
		}
		if got.Provenance.OperationSource != want.OperationSource ||
			got.Provenance.InputSource != want.InputSource ||
			got.Provenance.UserID != want.UserID ||
			got.Provenance.DeviceName != want.DeviceName ||
			got.Provenance.SourceIPMasked != want.SourceIPMasked ||
			got.Provenance.SourceIPHash == "" ||
			got.Provenance.UserAgent != want.UserAgent {
			t.Fatalf("provenance = %#v, want fields %#v and a non-empty IP hash", got.Provenance, want)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestHandleSendRejectsUnknownViewerRecipient(t *testing.T) {
	received := make(chan SendRequest, 1)
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		received <- req
		return "ok", nil
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{
		"message":"作業して",
		"to":"worker"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-received:
		t.Fatalf("handler should not be called, got %#v", got)
	default:
	}
}

func TestHandleSendLogsCorrelationFields(t *testing.T) {
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousWriter)

	completed := make(chan struct{})
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		return "", errors.New("test failure")
	}, func(req SendRequest, err error) {
		close(completed)
	})
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{
		"message":"ログ確認",
		"to":"midori",
		"viewer_client_id":"portal-tab-log",
		"input_source":"text",
		"user_id":"viewer-user",
		"device_name":"Linux x86_64"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	req.Header.Set("X-Forwarded-For", "203.0.113.42")
	req.Header.Set("User-Agent", "Mozilla/5.0 test-browser")
	rec := httptest.NewRecorder()
	h(rec, req)

	var body struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
	for _, marker := range []string{
		"job_id=" + body.JobID,
		`viewer_client_id="portal-tab-log"`,
		"recipient=midori",
		`operation_source="RenCrow_PORTAL"`,
		"input_source=text",
		`user_id="viewer-user"`,
		`device_name="Linux x86_64"`,
		`source_ip_masked="203.0.113.x"`,
		"source_ip_hash=",
		`user_agent="Mozilla/5.0 test-browser"`,
	} {
		if !strings.Contains(logs.String(), marker) {
			t.Fatalf("correlation log marker %q is missing: %s", marker, logs.String())
		}
	}
}

func TestHandleSendRejectsUnknownInputSource(t *testing.T) {
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		return "ok", nil
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{
		"message":"invalid source",
		"to":"mio",
		"input_source":"microphone-ish"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSendAppliesLegacyViewerLLMAlias(t *testing.T) {
	received := make(chan string, 1)
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		received <- req.Message
		return "ok", nil
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{
		"message":"この文章を要約して",
		"model_alias":"Worker",
		"base_url":"http://127.0.0.1:8082",
		"model":"Worker",
		"route_prefix":"/ops"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		OK          bool   `json:"ok"`
		ModelAlias  string `json:"model_alias"`
		BaseURL     string `json:"base_url"`
		Model       string `json:"model"`
		RoutePrefix string `json:"route_prefix"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !body.OK || body.ModelAlias != "Worker" || body.BaseURL != "http://127.0.0.1:8082" || body.Model != "Worker" || body.RoutePrefix != "/ops" {
		t.Fatalf("unexpected response: %+v", body)
	}

	select {
	case got := <-received:
		if got != "/ops この文章を要約して" {
			t.Fatalf("unexpected handler message: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestHandleSendExplicitRouteWinsOverAlias(t *testing.T) {
	received := make(chan string, 1)
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		received <- req.Message
		return "ok", nil
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{
		"message":"/wild 物語にして",
		"model_alias":"Worker"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	select {
	case got := <-received:
		if got != "/wild 物語にして" {
			t.Fatalf("unexpected handler message: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestHandleSendUsesLegacyRuntimeAliasFields(t *testing.T) {
	received := make(chan string, 1)
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		received <- req.Message
		return "ok", nil
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{
		"message":"原因を調べて",
		"model_alias":"Heavy",
		"base_url":"http://192.168.1.31:18083",
		"model":"HeavyRuntime",
		"route_prefix":"/heavy"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		ModelAlias  string `json:"model_alias"`
		BaseURL     string `json:"base_url"`
		Model       string `json:"model"`
		RoutePrefix string `json:"route_prefix"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body.ModelAlias != "Heavy" || body.BaseURL != "http://192.168.1.31:18083" || body.Model != "HeavyRuntime" || body.RoutePrefix != "/heavy" {
		t.Fatalf("unexpected response: %+v", body)
	}

	select {
	case got := <-received:
		if got != "/heavy 原因を調べて" {
			t.Fatalf("unexpected handler message: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestHandleSendAcceptsMultipartAttachments(t *testing.T) {
	received := make(chan SendRequest, 1)
	saver := &fakeAttachmentSaver{attachments: []domainattachment.Attachment{{
		ID:          "att-1",
		Kind:        domainattachment.KindImage,
		Filename:    "camera.png",
		ContentType: "image/png",
		SizeBytes:   8,
		Path:        "/tmp/camera.png",
		Data:        []byte("png-data"),
	}}}
	h := HandleSendWithAttachments(func(_ context.Context, req SendRequest) (string, error) {
		received <- req
		return "ok", nil
	}, nil, saver)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("message", "画像を見て"); err != nil {
		t.Fatalf("WriteField failed: %v", err)
	}
	part, err := writer.CreateFormFile("attachments[]", "camera.png")
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	if _, err := part.Write([]byte("png-data")); err != nil {
		t.Fatalf("part write failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer close failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(saver.files) != 1 || saver.files[0].Filename != "camera.png" {
		t.Fatalf("saver received unexpected files: %#v", saver.files)
	}
	select {
	case got := <-received:
		if got.Message != "画像を見て" {
			t.Fatalf("Message = %q, want %q", got.Message, "画像を見て")
		}
		if got.To != "mio" {
			t.Fatalf("To = %q, want mio", got.To)
		}
		if len(got.Attachments) != 1 || got.Attachments[0].ID != "att-1" {
			t.Fatalf("Attachments = %#v", got.Attachments)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

type fakeAttachmentSaver struct {
	files       []appattachment.IncomingFile
	attachments []domainattachment.Attachment
}

func (f *fakeAttachmentSaver) SaveAll(_ context.Context, files []appattachment.IncomingFile) ([]domainattachment.Attachment, error) {
	f.files = files
	return f.attachments, nil
}
