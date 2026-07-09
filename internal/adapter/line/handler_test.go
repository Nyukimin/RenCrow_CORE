package line

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainsecurity "github.com/Nyukimin/RenCrow_CORE/internal/domain/security"
)

// mockOrchestrator はテスト用のOrchestrator
type mockOrchestrator struct {
	response orchestrator.ProcessMessageResponse
	err      error
	reqCh    chan orchestrator.ProcessMessageRequest
}

func (m *mockOrchestrator) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	if m.reqCh != nil {
		m.reqCh <- req
	}
	if m.err != nil {
		return orchestrator.ProcessMessageResponse{}, m.err
	}
	return m.response, nil
}

func TestNewHandler(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response: "test",
			Route:    routing.RouteCHAT,
		},
	}

	handler := NewHandler(orch, "test-channel-secret", "test-access-token")

	if handler == nil {
		t.Fatal("NewHandler should not return nil")
	}
}

func TestHandler_WebhookEndpoint_ValidMessage(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response:   "こんにちは！",
			Route:      routing.RouteCHAT,
			Confidence: 1.0,
			JobID:      "20260302-120000-abcd1234",
		},
	}

	handler := NewHandler(orch, "test-secret", "test-token")

	// LINE webhook payload
	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "text",
					"text": "こんにちは",
				},
				"source": map[string]interface{}{
					"type":   "user",
					"userId": "U123456",
				},
				"replyToken": "test-reply-token",
			},
		},
	}

	body, _ := json.Marshal(payload)

	// Generate valid signature
	signature := generateSignature(body, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestHandler_WebhookEndpoint_LineFileMessageBecomesAttachment(t *testing.T) {
	reqCh := make(chan orchestrator.ProcessMessageRequest, 1)
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{Response: "ok", Route: routing.RouteCHAT},
		reqCh:    reqCh,
	}
	handler := NewHandler(orch, "test-secret", "test-token")
	handler.SetAttachmentSaver(appattachment.NewStore(t.TempDir()))

	mediaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("missing authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("line attachment text"))
	}))
	defer mediaServer.Close()
	handler.mediaDownloader.contentEndpoint = mediaServer.URL + "/%s/content"

	replyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer replyServer.Close()
	handler.sender.replyEndpoint = replyServer.URL

	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type":     "file",
					"id":       "line-file-1",
					"fileName": "memo.txt",
				},
				"source": map[string]interface{}{
					"type":   "user",
					"userId": "U123456",
				},
				"replyToken": "reply-token",
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", generateSignature(body, "test-secret"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-reqCh:
		if got.UserMessage != "添付ファイルを確認してください。" {
			t.Fatalf("UserMessage = %q", got.UserMessage)
		}
		if len(got.Attachments) != 1 {
			t.Fatalf("Attachments = %#v", got.Attachments)
		}
		att := got.Attachments[0]
		if att.Filename != "memo.txt" || att.ExtractedText != "line attachment text" {
			t.Fatalf("unexpected attachment: %#v", att)
		}
		if filepath.Base(att.Path) != "memo.txt" {
			t.Fatalf("attachment path = %q", att.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("orchestrator was not called")
	}
}

func TestHandler_WebhookEndpoint_ChannelPolicyRejectsUnpairedGroup(t *testing.T) {
	reqCh := make(chan orchestrator.ProcessMessageRequest, 1)
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{Response: "ok", Route: routing.RouteCHAT},
		reqCh:    reqCh,
	}
	handler := NewHandler(orch, "test-secret", "test-token")
	handler.SetChannelPolicy(domainsecurity.ChannelPolicy{
		AllowDM:      true,
		AllowGroups:  true,
		PairedGroups: []string{"G-paired"},
	})
	replyCh := make(chan struct{}, 1)
	replyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		replyCh <- struct{}{}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer replyServer.Close()
	handler.sender.replyEndpoint = replyServer.URL

	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "text",
					"text": "こんにちは",
				},
				"source": map[string]interface{}{
					"type":    "group",
					"userId":  "U123456",
					"groupId": "G-new",
				},
				"replyToken": "reply-token",
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", generateSignature(body, "test-secret"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-reqCh:
		t.Fatal("orchestrator should not be called for denied group")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-replyCh:
	case <-time.After(time.Second):
		t.Fatal("expected rejection reply")
	}
}

func TestHandler_WebhookEndpoint_InvalidJSON(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")

	body := []byte("invalid json")
	signature := generateSignature(body, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}
}

func TestHandler_WebhookEndpoint_NonMessageEvent(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")

	// フォローイベント（メッセージではない）
	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "follow",
				"source": map[string]interface{}{
					"type":   "user",
					"userId": "U123456",
				},
			},
		},
	}

	body, _ := json.Marshal(payload)
	signature := generateSignature(body, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 非メッセージイベントは無視してOK返す
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestHandler_WebhookEndpoint_NonTextMessage(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")

	// 画像メッセージ
	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "image",
					"id":   "image123",
				},
				"source": map[string]interface{}{
					"type":   "user",
					"userId": "U123456",
				},
				"replyToken": "test-reply-token",
			},
		},
	}

	body, _ := json.Marshal(payload)
	signature := generateSignature(body, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 非テキストメッセージは無視してOK返す
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestHandler_GenerateSessionID(t *testing.T) {
	tests := []struct {
		name     string
		userID   string
		expected string // prefix check
	}{
		{
			name:     "Standard user ID",
			userID:   "U123456",
			expected: "20260302-line-U123456", // 日付部分は実行日により変わる
		},
		{
			name:     "Long user ID",
			userID:   "Uabcdefghijklmnop",
			expected: "20260302-line-Uabcdefghijklmnop",
		},
	}

	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := handler.generateSessionID(tt.userID)

			// 形式チェック: YYYYMMDD-line-{userID}
			if len(sessionID) < len("YYYYMMDD-line-") {
				t.Errorf("Session ID too short: %s", sessionID)
			}

			// "line-"が含まれているか
			if !contains(sessionID, "line-") {
				t.Errorf("Session ID should contain 'line-': %s", sessionID)
			}

			// userIDが含まれているか
			if !contains(sessionID, tt.userID) {
				t.Errorf("Session ID should contain userID '%s': %s", tt.userID, sessionID)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestHandler_HealthCheck_NotHandled(t *testing.T) {
	// /health は LINE handler ではなく専用の health handler が担当するため 404 を返す
	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec.Code)
	}
}

func TestHandler_WebhookEndpoint_InvalidSignature(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")

	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "text",
					"text": "Test",
				},
				"source": map[string]interface{}{
					"type":   "user",
					"userId": "U123456",
				},
				"replyToken": "test-reply-token",
			},
		},
	}

	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", "invalid-signature")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rec.Code)
	}
}

func TestHandler_NormalizeEvent(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")
	ev := WebhookEvent{
		Type: "message",
		Message: EventMessage{
			Type: "text",
			Text: "hello",
			ID:   "m1",
		},
		Source: EventSource{
			Type:   "user",
			UserID: "U123",
		},
		Timestamp: time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC).UnixMilli(),
	}
	got := handler.NormalizeEvent(ev, []byte(`{"raw":true}`))
	if got.Channel != "line" || got.ChatID != "U123" || got.UserID != "U123" || got.Text != "hello" || got.MessageID != "m1" {
		t.Fatalf("unexpected normalized event: %+v", got)
	}
}

func TestHandler_Verify(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")
	body := []byte(`{"events":[]}`)
	sig := generateSignature(body, "test-secret")
	if err := handler.Verify(nil, body, sig); err != nil {
		t.Fatalf("expected verify success, got %v", err)
	}
	if err := handler.Verify(nil, body, "invalid"); err == nil {
		t.Fatal("expected verify failure")
	}
}

func TestHandler_WebhookEndpoint_MissingSignature(t *testing.T) {
	orch := &mockOrchestrator{}
	handler := NewHandler(orch, "test-secret", "test-token")

	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "text",
					"text": "Test",
				},
				"source": map[string]interface{}{
					"type":   "user",
					"userId": "U123456",
				},
				"replyToken": "test-reply-token",
			},
		},
	}

	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-Line-Signature header

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rec.Code)
	}
}

// generateSignature generates HMAC-SHA256 signature for testing
func generateSignature(body []byte, channelSecret string) string {
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestHandler_WebhookEndpoint_GroupChatWithBotMention(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response:   "グループチャット返信",
			Route:      routing.RouteCHAT,
			Confidence: 1.0,
			JobID:      "test-job-id",
		},
	}

	handler := NewHandler(orch, "test-secret", "test-token")
	handler.SetBotUserID("U-BOT123") // Set bot user ID

	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "text",
					"text": "@bot こんにちは",
					"mention": map[string]interface{}{
						"mentionees": []map[string]interface{}{
							{
								"index":  0,
								"length": 4,
								"userId": "U-BOT123",
							},
						},
					},
				},
				"source": map[string]interface{}{
					"type":    "group",
					"userId":  "U123456",
					"groupId": "G123456",
				},
				"replyToken": "reply-token-123",
			},
		},
	}

	body, _ := json.Marshal(payload)
	signature := generateSignature(body, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestHandler_WebhookEndpoint_GroupChatWithoutBotMention(t *testing.T) {
	orch := &mockOrchestrator{}

	handler := NewHandler(orch, "test-secret", "test-token")
	handler.SetBotUserID("U-BOT123")

	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "text",
					"text": "こんにちは",
					// No mention
				},
				"source": map[string]interface{}{
					"type":    "group",
					"userId":  "U123456",
					"groupId": "G123456",
				},
				"replyToken": "reply-token-123",
			},
		},
	}

	body, _ := json.Marshal(payload)
	signature := generateSignature(body, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Bot mention無しの場合はスキップされるが、webhookは成功
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestHandler_WebhookEndpoint_WithQuoteToken(t *testing.T) {
	orch := &mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response:   "引用返信",
			Route:      routing.RouteCHAT,
			Confidence: 1.0,
			JobID:      "test-job-id",
		},
	}

	handler := NewHandler(orch, "test-secret", "test-token")

	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type":       "text",
					"text":       "返信します",
					"quoteToken": "quote-token-abc123",
				},
				"source": map[string]interface{}{
					"type":   "user",
					"userId": "U123456",
				},
				"replyToken": "reply-token-123",
			},
		},
	}

	body, _ := json.Marshal(payload)
	signature := generateSignature(body, "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}
