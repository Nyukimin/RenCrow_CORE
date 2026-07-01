package viewer

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	appattachment "github.com/Nyukimin/picoclaw_multiLLM/internal/application/attachment"
	domainattachment "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/attachment"
)

func TestHandleSendAttachmentE2EAcceptsImageAndText(t *testing.T) {
	store := appattachment.NewStore(t.TempDir())
	store.Now = func() time.Time { return time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC) }
	var nextID int
	store.NewID = func() string {
		nextID++
		return "att-" + string(rune('0'+nextID))
	}

	received := make(chan SendRequest, 1)
	h := HandleSendWithAttachments(func(_ context.Context, req SendRequest) (string, error) {
		received <- req
		return "ok", nil
	}, nil, store)

	body, contentType := multipartBody(t, map[string]string{"message": "添付を確認して"}, []testUpload{
		{name: "camera.png", contentType: "image/png", content: "png-data"},
		{name: "memo.txt", contentType: "text/plain", content: "hello text"},
	})
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-received:
		if got.Message != "添付を確認して" {
			t.Fatalf("Message = %q", got.Message)
		}
		if len(got.Attachments) != 2 {
			t.Fatalf("attachment count = %d, want 2", len(got.Attachments))
		}
		if got.Attachments[0].Kind != domainattachment.KindImage {
			t.Fatalf("first attachment kind = %q", got.Attachments[0].Kind)
		}
		if got.Attachments[1].Kind != domainattachment.KindDocument {
			t.Fatalf("second attachment kind = %q", got.Attachments[1].Kind)
		}
		if got.Attachments[1].ExtractedText != "hello text" {
			t.Fatalf("text attachment ExtractedText = %q", got.Attachments[1].ExtractedText)
		}
		for _, att := range got.Attachments {
			if _, err := os.Stat(att.Path); err != nil {
				t.Fatalf("stored attachment missing: %s: %v", att.Path, err)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestHandleSendAttachmentE2EAcceptsVideo(t *testing.T) {
	store := appattachment.NewStore(t.TempDir())
	received := make(chan SendRequest, 1)
	h := HandleSendWithAttachments(func(_ context.Context, req SendRequest) (string, error) {
		received <- req
		return "ok", nil
	}, nil, store)

	body, contentType := multipartBody(t, map[string]string{"message": "動画を見て"}, []testUpload{
		{name: "clip.mp4", contentType: "video/mp4", content: strings.Repeat("0", 32)},
	})
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-received:
		if len(got.Attachments) != 1 || got.Attachments[0].Kind != domainattachment.KindVideo {
			t.Fatalf("video attachment was not passed through: %#v", got.Attachments)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

type testUpload struct {
	name        string
	contentType string
	content     string
}

func multipartBody(t *testing.T, fields map[string]string, files []testUpload) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("WriteField(%s) failed: %v", k, err)
		}
	}
	for _, file := range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="attachments[]"; filename="`+file.name+`"`)
		header.Set("Content-Type", file.contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("CreatePart failed: %v", err)
		}
		if _, err := part.Write([]byte(file.content)); err != nil {
			t.Fatalf("part write failed: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer close failed: %v", err)
	}
	return &body, writer.FormDataContentType()
}
