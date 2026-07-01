package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appattachment "github.com/Nyukimin/picoclaw_multiLLM/internal/application/attachment"
	domainattachment "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/attachment"
)

func TestHandleSendUsesViewerRecipientContract(t *testing.T) {
	received := make(chan SendRequest, 1)
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		received <- req
		return "ok", nil
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{
		"message":"作業手順を相談したい",
		"to":"shiro"
	}`))
	req.Header.Set("Content-Type", "application/json")
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

	select {
	case got := <-received:
		if got.Message != "作業手順を相談したい" {
			t.Fatalf("unexpected handler message: %q", got.Message)
		}
		if got.To != "shiro" {
			t.Fatalf("recipient = %q, want shiro", got.To)
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
