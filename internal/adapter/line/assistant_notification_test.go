package line

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

type recordingPushAdapter struct {
	probeErr error
	sendErr  error
	targets  []string
	messages []string
}

func (a *recordingPushAdapter) Probe(context.Context) error {
	return a.probeErr
}

func (a *recordingPushAdapter) Send(_ context.Context, target, message string) error {
	a.targets = append(a.targets, target)
	a.messages = append(a.messages, message)
	return a.sendErr
}

func TestAssistantNotificationHandlerSendsOnceAndPreservesCorrelation(t *testing.T) {
	adapter := &recordingPushAdapter{}
	store := NewAssistantPushReceiptStore(filepath.Join(t.TempDir(), "receipts.jsonl"))
	handler := NewAssistantNotificationHandler(
		adapter,
		func() (string, string, error) {
			return "U1234567890abcdef1234567890abcdef", "user", nil
		},
		store,
		func() time.Time { return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC) },
	)

	body := []byte(`{
		"delivery_id":"dly_001",
		"trace_id":"trc_001",
		"user_id":"user-001",
		"title":"RenCrow",
		"body":"通知テストです。"
	}`)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/internal/assistant/notifications/line", bytes.NewReader(body)))
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/internal/assistant/notifications/line", bytes.NewReader(body)))
	if second.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	if len(adapter.messages) != 1 {
		t.Fatalf("send count=%d want=1", len(adapter.messages))
	}
	if adapter.messages[0] != "【RenCrow】\n通知テストです。" {
		t.Fatalf("message=%q", adapter.messages[0])
	}
	var response AssistantNotificationResponse
	if err := json.Unmarshal(second.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Duplicate || response.DeliveryID != "dly_001" || response.TraceID != "trc_001" {
		t.Fatalf("unexpected duplicate response: %#v", response)
	}
}

func TestAssistantNotificationHandlerRejectsInvalidRequestBeforeSend(t *testing.T) {
	adapter := &recordingPushAdapter{}
	handler := NewAssistantNotificationHandler(
		adapter,
		func() (string, string, error) {
			return "U1234567890abcdef1234567890abcdef", "user", nil
		},
		NewAssistantPushReceiptStore(filepath.Join(t.TempDir(), "receipts.jsonl")),
		time.Now,
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/internal/assistant/notifications/line",
		bytes.NewBufferString(`{"delivery_id":"dly_001","title":"012345678901234567890","body":"test"}`),
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(adapter.messages) != 0 {
		t.Fatalf("invalid request sent %d messages", len(adapter.messages))
	}
}

func TestAssistantNotificationHandlerMarksAmbiguousSendAsUncertain(t *testing.T) {
	adapter := &recordingPushAdapter{sendErr: errors.New("connection reset")}
	store := NewAssistantPushReceiptStore(filepath.Join(t.TempDir(), "receipts.jsonl"))
	handler := NewAssistantNotificationHandler(
		adapter,
		func() (string, string, error) {
			return "U1234567890abcdef1234567890abcdef", "user", nil
		},
		store,
		time.Now,
	)
	requestBody := `{
		"delivery_id":"dly_uncertain",
		"trace_id":"trc_uncertain",
		"user_id":"user-001",
		"title":"RenCrow",
		"body":"通知です"
	}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/internal/assistant/notifications/line",
		bytes.NewBufferString(requestBody),
	))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	adapter.sendErr = nil
	retry := httptest.NewRecorder()
	handler.ServeHTTP(retry, httptest.NewRequest(
		http.MethodPost,
		"/internal/assistant/notifications/line",
		bytes.NewBufferString(requestBody),
	))
	if retry.Code != http.StatusConflict {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	if len(adapter.messages) != 1 {
		t.Fatalf("uncertain delivery was resent: count=%d", len(adapter.messages))
	}
}
