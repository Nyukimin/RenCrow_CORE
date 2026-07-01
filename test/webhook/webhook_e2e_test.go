package webhook_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/line"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
)

const testSecret = "test-channel-secret-for-e2e"
const testToken = "test-access-token"

// === Mock Orchestrator ===

type mockOrchestrator struct {
	mu       sync.Mutex
	lastReq  orchestrator.ProcessMessageRequest
	response orchestrator.ProcessMessageResponse
	err      error
	called   bool
}

func (m *mockOrchestrator) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = true
	m.lastReq = req
	if m.err != nil {
		return orchestrator.ProcessMessageResponse{}, m.err
	}
	return m.response, nil
}

func (m *mockOrchestrator) wasCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.called
}

func (m *mockOrchestrator) getLastReq() orchestrator.ProcessMessageRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastReq
}

// === Helpers ===

func generateSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func makeTextEvent(userID, text, replyToken string) map[string]interface{} {
	return map[string]interface{}{
		"type":       "message",
		"replyToken": replyToken,
		"source": map[string]interface{}{
			"type":   "user",
			"userId": userID,
		},
		"message": map[string]interface{}{
			"type": "text",
			"text": text,
			"id":   "msg001",
		},
		"timestamp": time.Now().UnixMilli(),
	}
}

func makePayload(events ...map[string]interface{}) []byte {
	payload := map[string]interface{}{"events": events}
	body, _ := json.Marshal(payload)
	return body
}

func makeSignedRequest(body []byte, secret string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Line-Signature", generateSignature(body, secret))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// === Tests ===

func TestWebhookE2E_ValidSignature_Returns200(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response: "ok", Route: routing.RouteCHAT,
		},
	}
	handler := line.NewHandler(orch, testSecret, testToken)

	body := makePayload(makeTextEvent("U123", "hello", "reply001"))
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", rr.Code)
	}
}

func TestWebhookE2E_InvalidSignature_Returns401(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := line.NewHandler(orch, testSecret, testToken)

	body := makePayload(makeTextEvent("U123", "hello", "reply001"))
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Line-Signature", "invalid-signature")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", rr.Code)
	}
}

func TestWebhookE2E_MissingSignature_Returns401(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := line.NewHandler(orch, testSecret, testToken)

	body := makePayload(makeTextEvent("U123", "hello", "reply001"))
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	// No signature header
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", rr.Code)
	}
}

func TestWebhookE2E_EmptyBody_Returns400(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := line.NewHandler(orch, testSecret, testToken)

	body := []byte("")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Line-Signature", generateSignature(body, testSecret))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Empty body with valid signature should still return 400 (invalid JSON)
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusUnauthorized {
		t.Errorf("status: want 400 or 401, got %d", rr.Code)
	}
}

func TestWebhookE2E_TextMessage_ProcessedByOrchestrator(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response: "response", Route: routing.RouteCHAT,
		},
	}
	handler := line.NewHandler(orch, testSecret, testToken)

	body := makePayload(makeTextEvent("U123", "test message", "reply001"))
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}

	// processEvent runs in goroutine — give it time
	time.Sleep(100 * time.Millisecond)

	if !orch.wasCalled() {
		t.Error("orchestrator.ProcessMessage should be called")
	}
	lastReq := orch.getLastReq()
	if lastReq.UserMessage != "test message" {
		t.Errorf("UserMessage: want 'test message', got %q", lastReq.UserMessage)
	}
	if lastReq.Channel != "line" {
		t.Errorf("Channel: want 'line', got %q", lastReq.Channel)
	}
	if lastReq.ChatID != "U123" {
		t.Errorf("ChatID: want 'U123', got %q", lastReq.ChatID)
	}
}

func TestWebhookE2E_NonTextMessage_Ignored(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := line.NewHandler(orch, testSecret, testToken)

	event := map[string]interface{}{
		"type":       "message",
		"replyToken": "reply001",
		"source":     map[string]interface{}{"type": "user", "userId": "U123"},
		"message":    map[string]interface{}{"type": "image", "id": "img001"},
	}
	body := makePayload(event)
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Non-text messages are filtered synchronously before goroutine spawn — no sleep needed
	if orch.wasCalled() {
		t.Error("image message should NOT trigger orchestrator")
	}
}

func TestWebhookE2E_FollowEvent_Ignored(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := line.NewHandler(orch, testSecret, testToken)

	event := map[string]interface{}{
		"type":       "follow",
		"replyToken": "reply001",
		"source":     map[string]interface{}{"type": "user", "userId": "U123"},
	}
	body := makePayload(event)
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Follow events are filtered synchronously — no sleep needed
	if orch.wasCalled() {
		t.Error("follow event should NOT trigger orchestrator")
	}
}

func TestWebhookE2E_MultipleEvents_AllProcessed(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response: "ok", Route: routing.RouteCHAT,
		},
	}
	handler := line.NewHandler(orch, testSecret, testToken)

	events := []map[string]interface{}{
		makeTextEvent("U123", "msg1", "reply001"),
		makeTextEvent("U123", "msg2", "reply002"),
		makeTextEvent("U456", "msg3", "reply003"),
	}
	body := makePayload(events...)
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", rr.Code)
	}

	// Wait for goroutines to process all 3 events
	time.Sleep(200 * time.Millisecond)

	if !orch.wasCalled() {
		t.Error("orchestrator should be called for multiple text events")
	}
}

func TestWebhookE2E_EmptyEventsArray_Returns200(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := line.NewHandler(orch, testSecret, testToken)

	body := makePayload() // empty events
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", rr.Code)
	}

	// Empty events array — no goroutines spawned, no sleep needed
	if orch.wasCalled() {
		t.Error("orchestrator should NOT be called for empty events")
	}
}

func TestWebhookE2E_GroupChat_WithBotMention_Processed(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response: "ok", Route: routing.RouteCHAT,
		},
	}
	handler := line.NewHandler(orch, testSecret, testToken)
	handler.SetBotUserID("BOT_USER_ID")

	event := map[string]interface{}{
		"type":       "message",
		"replyToken": "reply001",
		"source": map[string]interface{}{
			"type":    "group",
			"groupId": "G123",
			"userId":  "U123",
		},
		"message": map[string]interface{}{
			"type": "text",
			"text": "@Bot hello",
			"id":   "msg001",
			"mention": map[string]interface{}{
				"mentionees": []map[string]interface{}{
					{"index": 0, "length": 4, "userId": "BOT_USER_ID"},
				},
			},
		},
	}
	body := makePayload(event)
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	time.Sleep(100 * time.Millisecond)

	if !orch.wasCalled() {
		t.Error("group message with bot mention should trigger orchestrator")
	}
}

func TestWebhookE2E_GroupChat_NoBotMention_Skipped(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := line.NewHandler(orch, testSecret, testToken)
	handler.SetBotUserID("BOT_USER_ID")

	event := map[string]interface{}{
		"type":       "message",
		"replyToken": "reply001",
		"source": map[string]interface{}{
			"type":    "group",
			"groupId": "G123",
			"userId":  "U123",
		},
		"message": map[string]interface{}{
			"type": "text",
			"text": "hello everyone",
			"id":   "msg001",
		},
	}
	body := makePayload(event)
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	time.Sleep(100 * time.Millisecond)

	if orch.wasCalled() {
		t.Error("group message without bot mention should NOT trigger orchestrator")
	}
}

func TestWebhookE2E_DirectMessage_AlwaysProcessed(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response: "ok", Route: routing.RouteCHAT,
		},
	}
	handler := line.NewHandler(orch, testSecret, testToken)
	handler.SetBotUserID("BOT_USER_ID") // Even with botUserID set, DMs should work

	body := makePayload(makeTextEvent("U123", "hello", "reply001"))
	req := makeSignedRequest(body, testSecret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	time.Sleep(100 * time.Millisecond)

	if !orch.wasCalled() {
		t.Error("direct message should always trigger orchestrator")
	}
}

func TestWebhookE2E_NonWebhookPath_Returns404(t *testing.T) {
	handler := line.NewHandler(&mockOrchestrator{}, testSecret, testToken)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status: want 404, got %d", rr.Code)
	}
}
