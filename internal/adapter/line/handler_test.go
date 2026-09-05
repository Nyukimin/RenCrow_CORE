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
	"reflect"
	"testing"
	"time"

	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	knowledgememoryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainsecurity "github.com/Nyukimin/RenCrow_CORE/internal/domain/security"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

// mockOrchestrator はテスト用のOrchestrator
type mockOrchestrator struct {
	response orchestrator.ProcessMessageResponse
	err      error
	reqCh    chan orchestrator.ProcessMessageRequest
	ctxCh    chan context.Context
}

func (m *mockOrchestrator) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	if m.ctxCh != nil {
		m.ctxCh <- ctx
	}
	if m.reqCh != nil {
		m.reqCh <- req
	}
	if m.err != nil {
		return orchestrator.ProcessMessageResponse{}, m.err
	}
	return m.response, nil
}

func signedMessagePayload(messageID, userID, text string) []byte {
	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "text",
					"id":   messageID,
					"text": text,
				},
				"source": map[string]interface{}{
					"type":   "user",
					"userId": userID,
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	return body
}

func postSignedWebhook(t *testing.T, handler *Handler, body []byte, signatureBody []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", generateSignature(signatureBody, "test-secret"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func awaitScope(t *testing.T, ctxCh <-chan context.Context) domaintool.ToolExecutionScope {
	t.Helper()
	select {
	case ctx := <-ctxCh:
		scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
		if !ok {
			t.Fatal("orchestrator context did not contain a ToolExecutionScope")
		}
		return scope
	case <-time.After(time.Second):
		t.Fatal("orchestrator was not called")
		return domaintool.ToolExecutionScope{}
	}
}

func assertNoOrchestratorCall(t *testing.T, ctxCh <-chan context.Context) {
	t.Helper()
	select {
	case <-ctxCh:
		t.Fatal("orchestrator must not be called")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHandler_WebhookEndpoint_ValidSignedMessageCarriesPrivateToolScope(t *testing.T) {
	ctxCh := make(chan context.Context, 1)
	handler := NewHandler(&mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{Response: "ok", Route: routing.RouteCHAT},
		ctxCh:    ctxCh,
	}, "test-secret", "test-token")
	body := signedMessagePayload("message-1", "user-1", "private lookup")

	if rec := postSignedWebhook(t, handler, body, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	scope := awaitScope(t, ctxCh)
	want := domaintool.ToolExecutionScope{
		RequestID:            "line:message-1",
		ActorKind:            domaintool.ActorKindUser,
		ActorID:              "line:user-1",
		AuthenticatedUserID:  "line:user-1",
		AllowedDataScopes:    []string{domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceHTTP,
	}
	if !reflect.DeepEqual(scope, want) {
		t.Fatalf("scope = %#v, want %#v", scope, want)
	}
}

func TestHandler_WebhookEndpoint_SignedUsersRemainSeparated(t *testing.T) {
	ctxCh := make(chan context.Context, 2)
	handler := NewHandler(&mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{Response: "ok", Route: routing.RouteCHAT},
		ctxCh:    ctxCh,
	}, "test-secret", "test-token")

	for _, item := range []struct {
		messageID string
		userID    string
	}{
		{messageID: "message-a", userID: "user-a"},
		{messageID: "message-b", userID: "user-b"},
	} {
		body := signedMessagePayload(item.messageID, item.userID, "private lookup")
		if rec := postSignedWebhook(t, handler, body, body); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	}

	a := awaitScope(t, ctxCh)
	b := awaitScope(t, ctxCh)
	if a.AuthenticatedUserID == b.AuthenticatedUserID || a.ActorID == b.ActorID || a.RequestID == b.RequestID {
		t.Fatalf("signed user scopes were not separated: a=%#v b=%#v", a, b)
	}
}

func TestHandler_WebhookEndpoint_FailsClosedForMissingSignedIdentity(t *testing.T) {
	for _, item := range []struct {
		name      string
		messageID string
		userID    string
	}{
		{name: "missing message id", messageID: "", userID: "user-1"},
		{name: "missing source user id", messageID: "message-1", userID: ""},
	} {
		t.Run(item.name, func(t *testing.T) {
			ctxCh := make(chan context.Context, 1)
			handler := NewHandler(&mockOrchestrator{ctxCh: ctxCh}, "test-secret", "test-token")
			body := signedMessagePayload(item.messageID, item.userID, "private lookup")
			if rec := postSignedWebhook(t, handler, body, body); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			assertNoOrchestratorCall(t, ctxCh)
		})
	}
}

func TestHandler_WebhookEndpoint_InvalidOrMutatedSignatureDoesNotInvokeOrchestrator(t *testing.T) {
	ctxCh := make(chan context.Context, 1)
	handler := NewHandler(&mockOrchestrator{ctxCh: ctxCh}, "test-secret", "test-token")
	original := signedMessagePayload("message-1", "user-1", "original")
	mutated := signedMessagePayload("message-1", "user-1", "mutated")

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(mutated))
	req.Header.Set("X-Line-Signature", generateSignature(original, "test-secret"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("mutated body status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	assertNoOrchestratorCall(t, ctxCh)

	invalid := signedMessagePayload("message-2", "user-2", "invalid")
	req = httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(invalid))
	req.Header.Set("X-Line-Signature", "invalid-signature")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	assertNoOrchestratorCall(t, ctxCh)
}

type webhookKnowledgeSearcher struct {
	requests chan knowledgememoryapp.SearchRequest
}

func (s *webhookKnowledgeSearcher) Search(_ context.Context, request knowledgememoryapp.SearchRequest) ([]knowledgememoryapp.SearchResult, error) {
	s.requests <- request
	return []knowledgememoryapp.SearchResult{{
		RecordType: "creative_knowledge",
		RecordID:   "private-result-" + request.Scope.UserID,
		Scope:      request.Scope.Scope,
		UserID:     request.Scope.UserID,
		Title:      "認証済み利用者だけの記録",
		Visibility: "private",
	}}, nil
}

type knowledgeToolOrchestrator struct {
	runner    *toolinfra.ToolRunner
	responses chan *domaintool.ToolResponse
}

func (o *knowledgeToolOrchestrator) ProcessMessage(ctx context.Context, _ orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	response, err := o.runner.ExecuteV2(ctx, "knowledge.search", map[string]any{
		"query":       "認証済み利用者",
		"record_type": "creative_knowledge",
		"limit":       10,
	})
	if response != nil {
		o.responses <- response
	}
	return orchestrator.ProcessMessageResponse{Response: "ok", Route: routing.RouteCHAT}, err
}

func TestHandler_WebhookEndpoint_SignedIdentityExecutesKnowledgeSearchWithoutPublicFallback(t *testing.T) {
	searchRequests := make(chan knowledgememoryapp.SearchRequest, 2)
	toolResponses := make(chan *domaintool.ToolResponse, 2)
	runner := toolinfra.NewToolRunner(toolinfra.ToolRunnerConfig{
		KnowledgeMemorySearcher:    &webhookKnowledgeSearcher{requests: searchRequests},
		KnowledgeMemorySearchReady: true,
		DisableToolHarness:         true,
	})
	handler := NewHandler(&knowledgeToolOrchestrator{runner: runner, responses: toolResponses}, "test-secret", "test-token")

	for _, userID := range []string{"user-a", "user-b"} {
		body := signedMessagePayload("message-"+userID, userID, "private knowledge lookup")
		if rec := postSignedWebhook(t, handler, body, body); rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", userID, rec.Code, http.StatusOK)
		}
	}

	seenUsers := map[string]bool{}
	for range 2 {
		select {
		case request := <-searchRequests:
			if request.Scope.Scope != knowledgememoryapp.SearchScopeUser {
				t.Fatalf("signed webhook search scope = %#v, want user with no public fallback", request.Scope)
			}
			seenUsers[request.Scope.UserID] = true
		case <-time.After(time.Second):
			t.Fatal("signed webhook did not reach knowledge.search")
		}
		select {
		case response := <-toolResponses:
			if response.Error != nil {
				t.Fatalf("knowledge.search response error = %#v", response.Error)
			}
		case <-time.After(time.Second):
			t.Fatal("knowledge.search did not return a response")
		}
	}
	if !seenUsers["line:user-a"] || !seenUsers["line:user-b"] || len(seenUsers) != 2 {
		t.Fatalf("signed users were not kept separate: %#v", seenUsers)
	}

	mutated := signedMessagePayload("message-user-a", "user-b", "private knowledge lookup")
	original := signedMessagePayload("message-user-a", "user-a", "private knowledge lookup")
	if rec := postSignedWebhook(t, handler, mutated, original); rec.Code != http.StatusUnauthorized {
		t.Fatalf("mutated signed identity status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	select {
	case request := <-searchRequests:
		t.Fatalf("forged identity reached knowledge.search: %#v", request)
	case <-time.After(100 * time.Millisecond):
	}
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
			TaskID:     "tsk_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b1c2d",
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestHandler_WebhookEndpoint_LineAliasAcceptsSignedPost(t *testing.T) {
	handler := NewHandler(&mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response: "ok",
			Route:    routing.RouteCHAT,
		},
	}, "test-secret", "test-token")

	body := []byte(`{"events":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
	req.Header.Set("X-Line-Signature", generateSignature(body, "test-secret"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandler_WebhookEndpoint_RecordsSignedDirectUserTarget(t *testing.T) {
	store := NewDirectUserTargetStore(t.TempDir())
	reqCh := make(chan orchestrator.ProcessMessageRequest, 1)
	handler := NewHandler(&mockOrchestrator{
		response: orchestrator.ProcessMessageResponse{
			Response: "ok",
			Route:    routing.RouteCHAT,
		},
		reqCh: reqCh,
	}, "test-secret", "test-token")
	handler.SetDirectUserTargetRecorder(store)

	userID := "U0123456789abcdef0123456789abcdef"
	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type": "message",
				"message": map[string]interface{}{
					"type": "text",
					"text": "通知先登録",
				},
				"source": map[string]interface{}{
					"type":   "user",
					"userId": userID,
				},
				"replyToken": "test-reply-token",
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
	req.Header.Set("X-Line-Signature", generateSignature(body, "test-secret"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got != userID {
		t.Fatalf("recorded target = %q, want %q", got, userID)
	}
	select {
	case <-reqCh:
		t.Fatal("enrollment command must not enter CHAT or send a LINE reply")
	case <-time.After(100 * time.Millisecond):
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
	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
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
	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 非テキストメッセージは無視してOK返す
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
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
			TaskID:     "tsk_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b1c2d",
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
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
			TaskID:     "tsk_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b1c2d",
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/line", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Line-Signature", signature)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}
