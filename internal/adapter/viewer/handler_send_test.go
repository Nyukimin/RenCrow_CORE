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
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestWithCMDMioOwnerAuthenticationEnforcesTheIngressContract(t *testing.T) {
	const token = "viewer-send-owner-token-0123456789"
	for _, tt := range []struct {
		name       string
		body       string
		client     string
		profile    string
		authority  []string
		remoteAddr string
		ownerID    string
		wantStatus int
		wantOwner  bool
	}{
		{name: "missing bearer defaults to mio", body: `{"message":"hello"}`, client: "RenCrow_CMD", profile: "cmd-chat", remoteAddr: "127.0.0.1:1234", ownerID: "ren", wantStatus: http.StatusUnauthorized},
		{name: "invalid bearer", body: `{"message":"hello","to":"mio"}`, client: "RenCrow_CMD", profile: "cmd-chat", authority: []string{"Bearer wrong-token"}, remoteAddr: "127.0.0.1:1234", ownerID: "ren", wantStatus: http.StatusUnauthorized},
		{name: "duplicate bearer", body: `{"message":"hello","to":"mio"}`, client: "RenCrow_CMD", profile: "cmd-chat", authority: []string{"Bearer " + token, "Bearer " + token}, remoteAddr: "127.0.0.1:1234", ownerID: "ren", wantStatus: http.StatusUnauthorized},
		{name: "valid bearer from remote host", body: `{"message":"hello","to":"mio"}`, client: "RenCrow_CMD", profile: "cmd-chat", authority: []string{"Bearer " + token}, remoteAddr: "192.0.2.10:1234", ownerID: "ren", wantStatus: http.StatusForbidden},
		{name: "credential outside cmd mio contract", body: `{"message":"hello","to":"mio"}`, client: "RenCrow_PORTAL", profile: "portal-chat", authority: []string{"Bearer " + token}, remoteAddr: "127.0.0.1:1234", ownerID: "ren", wantStatus: http.StatusForbidden},
		{name: "cmd profile mismatch", body: `{"message":"hello","to":"mio"}`, client: "RenCrow_CMD", profile: "cmd-control", remoteAddr: "127.0.0.1:1234", ownerID: "ren", wantStatus: http.StatusForbidden},
		{name: "owner credential unavailable", body: `{"message":"hello","to":"mio"}`, client: "RenCrow_CMD", profile: "cmd-chat", authority: []string{"Bearer " + token}, remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusServiceUnavailable},
		{name: "valid owner bearer uses configured identity", body: `{"message":"hello","user_id":"attacker"}`, client: "RenCrow_CMD", profile: "cmd-chat", authority: []string{"Bearer " + token}, remoteAddr: "127.0.0.1:1234", ownerID: "ren", wantStatus: http.StatusNoContent, wantOwner: true},
		{name: "portal mio stays public", body: `{"message":"hello","to":"mio"}`, client: "RenCrow_PORTAL", profile: "portal-chat", remoteAddr: "192.0.2.10:1234", ownerID: "ren", wantStatus: http.StatusNoContent},
		{name: "cmd non mio stays public", body: `{"message":"hello","to":"shiro"}`, client: "RenCrow_CMD", profile: "cmd-chat", remoteAddr: "127.0.0.1:1234", ownerID: "ren", wantStatus: http.StatusNoContent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var nextCalls int
			var gotScope domaintool.ToolExecutionScope
			var scopeFound bool
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				gotScope, scopeFound = domaintool.ToolExecutionScopeFromContext(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			handler := WithCMDMioOwnerAuthentication(next, tt.ownerID, []byte(token))
			request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			if tt.client != "" {
				request.Header.Set("X-RenCrow-Client", tt.client)
			}
			if tt.profile != "" {
				request.Header.Set("X-RenCrow-Interaction-Profile", tt.profile)
			}
			for _, value := range tt.authority {
				request.Header.Add("Authorization", value)
			}
			request.RemoteAddr = tt.remoteAddr
			if strings.HasPrefix(tt.remoteAddr, "127.0.0.1:") {
				request.Host = "127.0.0.1:18790"
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s want=%d", response.Code, response.Body.String(), tt.wantStatus)
			}
			if tt.wantStatus == http.StatusNoContent && nextCalls != 1 {
				t.Fatalf("next calls=%d, want 1", nextCalls)
			}
			if tt.wantStatus != http.StatusNoContent && nextCalls != 0 {
				t.Fatalf("rejected request reached next handler %d times", nextCalls)
			}
			if tt.wantOwner {
				if !scopeFound || gotScope.Validate() != nil || gotScope.ActorKind != domaintool.ActorKindUser || gotScope.AuthenticationSource != domaintool.AuthenticationSourceHTTP || gotScope.AuthenticatedUserID != "ren" || !gotScope.Allows(domaintool.DataScopeUser) {
					t.Fatalf("scope=%+v found=%t, want configured authenticated owner", gotScope, scopeFound)
				}
			} else if scopeFound && gotScope.Allows(domaintool.DataScopeUser) {
				t.Fatalf("unexpected user scope for public request: %+v", gotScope)
			}
		})
	}
}

func TestHandleSendPreservesAudioOutputIntent(t *testing.T) {
	for _, tt := range []struct {
		name        string
		contentType string
		body        func(t *testing.T) *bytes.Buffer
		want        string
	}{
		{
			name:        "json requested",
			contentType: "application/json",
			body: func(t *testing.T) *bytes.Buffer {
				return bytes.NewBufferString(`{"message":"hello","audio_output":"requested"}`)
			},
			want: "requested",
		},
		{
			name:        "json omitted",
			contentType: "application/json",
			body: func(t *testing.T) *bytes.Buffer {
				return bytes.NewBufferString(`{"message":"hello"}`)
			},
			want: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			received := make(chan SendRequest, 1)
			h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
				received <- req
				return "ok", nil
			}, nil)
			body := tt.body(t)
			req := httptest.NewRequest(http.MethodPost, "/viewer/send", body)
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			select {
			case got := <-received:
				if actual := string(got.AudioOutput); actual != tt.want {
					t.Fatalf("audio_output=%q want=%q", actual, tt.want)
				}
			case <-time.After(time.Second):
				t.Fatal("handler was not called")
			}
		})
	}
}

func TestHandleSendAcceptanceIsNotCacheable(t *testing.T) {
	started := make(chan struct{}, 1)
	h := HandleSend(func(context.Context, SendRequest) (string, error) {
		started <- struct{}{}
		return "ok", nil
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want=no-store", got)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestHandleSendCancellationCanLatchBeforeSpeechStarts(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	processingStarted := make(chan struct{}, 1)
	startSpeech := make(chan struct{})
	speechResult := make(chan error, 1)
	h := HandleSendWithSpeechCancellation(func(ctx context.Context, _ SendRequest) (string, error) {
		<-startSpeech
		processingStarted <- struct{}{}
		speechCtx, done := orchestrator.SpeechSynthesisContext(ctx)
		defer done()
		speechResult <- speechCtx.Err()
		return "ok", nil
	}, nil, nil, nil, registry)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want=no-store", got)
	}
	var accepted struct {
		TurnID            string `json:"turn_id"`
		RootTaskID        string `json:"root_task_id"`
		SpeechCancelToken string `json:"speech_cancel_token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.TurnID == "" || accepted.SpeechCancelToken == "" {
		t.Fatalf("acceptance missing turn or cancellation capability: %+v", accepted)
	}
	registry.mu.Lock()
	registeredEntry := registry.entries[accepted.SpeechCancelToken]
	registry.mu.Unlock()
	if registeredEntry == nil || registeredEntry.turnID != accepted.TurnID || registeredEntry.rootTaskID != accepted.RootTaskID {
		t.Fatalf("capability was not bound to accepted turn: acceptance=%+v entry=%+v", accepted, registeredEntry)
	}
	cancelBody, err := json.Marshal(map[string]string{"speech_cancel_token": accepted.SpeechCancelToken})
	if err != nil {
		t.Fatal(err)
	}
	cancelRec := httptest.NewRecorder()
	cancelReq := httptest.NewRequest(http.MethodPost, "/viewer/tts/cancel", strings.NewReader(string(cancelBody)))
	registry.HandleCancel().ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusNoContent {
		t.Fatalf("cancel status=%d want=%d body=%s", cancelRec.Code, http.StatusNoContent, cancelRec.Body.String())
	}
	close(startSpeech)
	select {
	case <-processingStarted:
	case <-time.After(time.Second):
		t.Fatal("async message handler did not start after release")
	}
	select {
	case err := <-speechResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("speech context error=%v want=%v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("speech context was not created")
	}
}

func TestHandleSendResolvesCanonicalSessionBeforeAcceptance(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	received := make(chan SendRequest, 1)
	h := HandleSendWithAttachmentsAndSessionResolver(func(_ context.Context, req SendRequest) (string, error) {
		received <- req
		return "ok", nil
	}, nil, nil, func(context.Context) (modulecore.SessionID, error) {
		return sessionID, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.SessionID != string(sessionID) {
		t.Fatalf("accepted session_id=%q want=%q", accepted.SessionID, sessionID)
	}
	select {
	case got := <-received:
		if got.SessionID != string(sessionID) {
			t.Fatalf("handler session_id=%q want=%q", got.SessionID, sessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestHandleSendRejectsBeforeAcceptanceWhenCanonicalSessionUnavailable(t *testing.T) {
	called := make(chan struct{}, 1)
	h := HandleSendWithAttachmentsAndSessionResolver(func(context.Context, SendRequest) (string, error) {
		called <- struct{}{}
		return "ok", nil
	}, nil, nil, func(context.Context) (modulecore.SessionID, error) {
		return "", errors.New("session store unavailable")
	})

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	select {
	case <-called:
		t.Fatal("handler must not run after session resolution failure")
	default:
	}
}

func TestHandleSendPreservesMultipartAudioOutputIntent(t *testing.T) {
	received := make(chan SendRequest, 1)
	h := HandleSendWithAttachments(func(_ context.Context, req SendRequest) (string, error) {
		received <- req
		return "ok", nil
	}, nil, &fakeAttachmentSaver{})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("message", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("audio_output", "disabled"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-received:
		if actual := string(got.AudioOutput); actual != "disabled" {
			t.Fatalf("audio_output=%q want=disabled", actual)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestHandleSendRejectsUnknownAudioOutput(t *testing.T) {
	h := HandleSend(func(_ context.Context, req SendRequest) (string, error) {
		return "ok", nil
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","audio_output":"speaker"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=400 body=%s", rec.Code, rec.Body.String())
	}
}

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
	req.Header.Set("X-RenCrow-Interaction-Profile", "portal-chat")
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
	traceID, _ := body["trace_id"].(string)
	messageID, _ := body["message_id"].(string)
	agentMessageID, _ := body["agent_message_id"].(string)
	turnID, _ := body["turn_id"].(string)
	rootTaskID, _ := body["root_task_id"].(string)
	if modulecore.TraceID(traceID).Validate() != nil || traceID == rootTaskID || modulecore.MessageID(messageID).Validate() != nil || modulecore.MessageID(agentMessageID).Validate() != nil || messageID == agentMessageID || modulecore.TurnID(turnID).Validate() != nil || modulecore.TaskID(rootTaskID).Validate() != nil {
		t.Fatalf("viewer send response must include generated message_id: %#v", body)
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
		if got.RootTaskID != rootTaskID || got.ViewerClientID != "portal-tab-1" {
			t.Fatalf("correlation = root_task:%q client:%q, want root_task:%q client:portal-tab-1", got.RootTaskID, got.ViewerClientID, rootTaskID)
		}
		if got.TurnID != turnID || got.TraceID != traceID || got.RootTaskID != rootTaskID || got.MessageID != messageID || got.AgentMessageID != agentMessageID {
			t.Fatalf("identity = (%q,%q,%q,%q,%q), want (%q,%q,%q,%q,%q)", got.TurnID, got.TraceID, got.RootTaskID, got.MessageID, got.AgentMessageID, turnID, traceID, rootTaskID, messageID, agentMessageID)
		}
		want := RequestProvenance{
			OperationSource:    "RenCrow_PORTAL",
			InteractionProfile: "portal-chat",
			InputSource:        "stt",
			UserID:             "viewer-user",
			DeviceName:         "Linux x86_64",
			SourceIPMasked:     "203.0.113.x",
			UserAgent:          "Mozilla/5.0 test-browser",
		}
		if got.Provenance.OperationSource != want.OperationSource ||
			got.Provenance.InteractionProfile != want.InteractionProfile ||
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
	}, func(_ context.Context, req SendRequest, err error) {
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
	req.Header.Set("X-RenCrow-Interaction-Profile", "portal-chat")
	req.Header.Set("X-Forwarded-For", "203.0.113.42")
	req.Header.Set("User-Agent", "Mozilla/5.0 test-browser")
	rec := httptest.NewRecorder()
	h(rec, req)

	var body struct {
		RootTaskID string `json:"root_task_id"`
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
		"root_task_id=" + body.RootTaskID,
		`viewer_client_id="portal-tab-log"`,
		"recipient=midori",
		`operation_source="RenCrow_PORTAL"`,
		`interaction_profile="portal-chat"`,
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
