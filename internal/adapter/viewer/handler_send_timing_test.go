package viewer

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
)

func TestHandleSendOptInTimingRecordsAcceptanceAndAsyncStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		body func(t *testing.T) (*bytes.Buffer, string)
	}{
		{
			name: "json text",
			body: func(*testing.T) (*bytes.Buffer, string) {
				return bytes.NewBufferString(`{"message":"hello","timings":true}`), "application/json"
			},
		},
		{
			name: "multipart text",
			body: func(t *testing.T) (*bytes.Buffer, string) {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				if err := writer.WriteField("message", "hello"); err != nil {
					t.Fatal(err)
				}
				if err := writer.WriteField("timings", "true"); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				return &body, writer.FormDataContentType()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			points := make(chan map[string]float64, 1)
			h := HandleSendWithAttachments(func(ctx context.Context, _ SendRequest) (string, error) {
				if recorder := turntiming.FromContext(ctx); recorder != nil {
					points <- recorder.Snapshot().PointsMS
				} else {
					points <- nil
				}
				return "ok", nil
			}, nil, &fakeAttachmentSaver{})

			body, contentType := tc.body(t)
			req := httptest.NewRequest(http.MethodPost, "/viewer/send", body)
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}

			select {
			case got := <-points:
				for _, point := range []string{"handler_received", "accepted", "async_start"} {
					if _, ok := got[point]; !ok {
						t.Errorf("timing point %q missing from async handler context: %#v", point, got)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("async handler was not called")
			}
		})
	}
}

func TestHandleSendTimingsOffDoesNotAddTimingContext(t *testing.T) {
	ctxSeen := make(chan bool, 1)
	h := HandleSend(func(ctx context.Context, _ SendRequest) (string, error) {
		ctxSeen <- turntiming.FromContext(ctx) != nil
		return "ok", nil
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-ctxSeen:
		if got {
			t.Fatal("default request unexpectedly carried a timing recorder")
		}
	case <-time.After(time.Second):
		t.Fatal("async handler was not called")
	}
}

func TestHandleSendRejectsNonBooleanMultipartTimings(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("message", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("timings", "1"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	h := HandleSendWithAttachments(func(context.Context, SendRequest) (string, error) {
		called = true
		return "ok", nil
	}, nil, &fakeAttachmentSaver{})
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("async handler called for invalid timings value")
	}
}
