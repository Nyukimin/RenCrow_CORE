package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	chatgptimport "github.com/Nyukimin/RenCrow_CORE/internal/application/chatgptimport"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const chatGPTOwnerTestToken = "owner-token-012345678901234567890123"

type chatGPTOwnerTestService struct {
	calls   int
	request chatgptimport.ImportRequest
	apply   bool
	err     error
	manifest,
	artifact []byte
	readErr error
}

func (service *chatGPTOwnerTestService) Import(_ context.Context, request chatgptimport.ImportRequest) (chatgptimport.ImportResult, error) {
	service.calls++
	service.request = request
	service.apply = request.Apply
	if service.err != nil {
		return chatgptimport.ImportResult{}, service.err
	}
	service.manifest, service.readErr = os.ReadFile(request.ManifestPath)
	if service.readErr == nil {
		service.artifact, service.readErr = os.ReadFile(request.ArtifactPath)
	}
	return chatgptimport.ImportResult{View: domainmemory.ChatGPTImportView{
		EventID: "event-1", ImportID: "import-1", RequestID: request.RequestID, ExportID: "export-1",
		State: domainmemory.ChatGPTImportStateCompleted,
	}}, nil
}

type chatGPTOwnerTestStore struct {
	statusCalls                  int
	statusErr                    error
	statusRequestID              string
	statusOwnerID, statusActorID string
	statusExportID               string
	confirmErr                   error
	statusView                   domainmemory.ChatGPTImportView
	confirmInput                 domainmemory.ChatGPTImportConfirmInput
}

type chatGPTOwnerStageCloserStub struct {
	calls int
	err   error
}

func (closer *chatGPTOwnerStageCloserStub) Close() error {
	closer.calls++
	return closer.err
}

func TestFinishChatGPTOwnerStageDoesNotTurnSuccessIntoFailure(t *testing.T) {
	closer := &chatGPTOwnerStageCloserStub{err: errors.New("cleanup failed")}
	if err := finishChatGPTOwnerStage(closer, nil); err != nil {
		t.Fatalf("success became error: %v", err)
	}
	if closer.calls != 1 {
		t.Fatalf("cleanup calls=%d", closer.calls)
	}
	resultErr := errors.New("import failed")
	joined := finishChatGPTOwnerStage(closer, resultErr)
	if !errors.Is(joined, resultErr) || !errors.Is(joined, errChatGPTOwnerFilesystem) {
		t.Fatalf("joined error=%v", joined)
	}
	domainErr := finishChatGPTOwnerStage(closer, domainmemory.ErrChatGPTImportArtifactInvalid)
	if status, code := chatGPTOwnerErrorStatus(domainErr); status != http.StatusUnprocessableEntity || code != "artifact_invalid" {
		t.Fatalf("cleanup hid domain error: status=%d code=%s err=%v", status, code, domainErr)
	}
}

func (store *chatGPTOwnerTestStore) GetChatGPTImportStatus(_ context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportView, error) {
	store.statusCalls++
	store.statusRequestID, store.statusOwnerID, store.statusActorID, store.statusExportID = requestID, ownerID, actorID, exportID
	if store.statusErr != nil {
		return domainmemory.ChatGPTImportView{}, store.statusErr
	}
	return store.statusView, nil
}

func (store *chatGPTOwnerTestStore) ConfirmChatGPTImportCandidates(_ context.Context, input domainmemory.ChatGPTImportConfirmInput) (domainmemory.ChatGPTImportConfirmResult, error) {
	store.confirmInput = input
	if store.confirmErr != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, store.confirmErr
	}
	return domainmemory.ChatGPTImportConfirmResult{RequestID: input.RequestID, ExportID: input.ExportID, Apply: input.Apply}, nil
}

func TestMemoryChatGPTOwnerHandlerRejectsMultipartPreamble(t *testing.T) {
	h := NewMemoryChatGPTOwnerHandler(&chatGPTOwnerTestService{}, &chatGPTOwnerTestStore{}, chatGPTOwnerPrivateTempDir(t), "ren", []byte(chatGPTOwnerTestToken))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/memory/import/chatgpt", strings.NewReader("preamble\r\n--boundary--\r\n"))
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer "+chatGPTOwnerTestToken)
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want strict multipart rejection", rec.Code, rec.Body.String())
	}
}

func TestMemoryChatGPTOwnerHandlerValidStreamingUploadUsesScopeAndCleansStage(t *testing.T) {
	root := chatGPTOwnerPrivateTempDir(t)
	service := &chatGPTOwnerTestService{}
	h := NewMemoryChatGPTOwnerHandler(service, &chatGPTOwnerTestStore{}, root, "ren", []byte(chatGPTOwnerTestToken))
	body, contentType := chatGPTOwnerMultipartBody("false", []byte(`{"schema":"test"}`), []byte("tar-bytes"), nil)
	rec := httptest.NewRecorder()
	h(rec, chatGPTOwnerRequest(http.MethodPost, "/v1/memory/import/chatgpt", contentType, body, "cmd-control"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d calls=%d request=%+v body=%s", rec.Code, service.calls, service.request, rec.Body.String())
	}
	if service.calls != 1 || service.apply {
		t.Fatalf("calls=%d apply=%v request=%+v", service.calls, service.apply, service.request)
	}
	if service.request.OwnerID != "ren" || service.request.ActorID != "ren" || service.request.RequestID == "" {
		t.Fatalf("request identity=%+v", service.request)
	}
	if service.readErr != nil {
		t.Fatalf("service stage read: %v", service.readErr)
	}
	if string(service.manifest) != `{"schema":"test"}` {
		t.Fatalf("manifest=%q", service.manifest)
	}
	if string(service.artifact) != "tar-bytes" {
		t.Fatalf("artifact=%q", service.artifact)
	}
	if _, err := os.Stat(service.request.StageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage root err=%v, want cleaned", err)
	}
	for _, private := range []string{"owner_id", "actor_id", "raw", "path", "manifest_path", "artifact_path"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(`"`+private+`"`)) {
			t.Fatalf("response exposes private field %q: %s", private, rec.Body.String())
		}
	}
}

func TestMemoryChatGPTOwnerHandlerAuthenticatesBeforeStageOrBody(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "raw")
	service := &chatGPTOwnerTestService{}
	h := NewMemoryChatGPTOwnerHandler(service, &chatGPTOwnerTestStore{}, root, "ren", []byte(chatGPTOwnerTestToken))
	body, contentType := chatGPTOwnerMultipartBody("false", []byte("manifest"), []byte("artifact"), nil)
	req := chatGPTOwnerRequest(http.MethodPost, "/v1/memory/import/chatgpt", contentType, body, "cmd-control")
	req.Header.Del("Authorization")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.calls != 0 {
		t.Fatalf("service calls=%d", service.calls)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw root err=%v, auth must precede stage creation", err)
	}
}

func TestMemoryChatGPTOwnerHandlerStrictMultipartMatrix(t *testing.T) {
	valid, contentType := chatGPTOwnerMultipartBody("false", []byte("manifest"), []byte("artifact"), nil)
	cases := []struct {
		name string
		body []byte
		ct   string
	}{
		{name: "preamble", body: append([]byte("preamble\r\n"), valid...), ct: contentType},
		{name: "epilogue", body: append(append([]byte(nil), valid...), []byte("epilogue")...), ct: contentType},
		{name: "final-again-after-epilogue", body: append(append(append([]byte(nil), valid...), []byte("evil")...), []byte("\r\n--owner-boundary--\r\n")...), ct: contentType},
		{name: "wrong-order", body: mustMultipartParts([]chatGPTOwnerMultipartPart{{"manifest", "application/json", []byte("manifest")}, {"apply", "text/plain", []byte("false")}, {"artifact", "application/x-tar", []byte("artifact")}}), ct: contentType},
		{name: "extra-part", body: append(append([]byte(nil), valid[:len(valid)-len("\r\n--owner-boundary--\r\n")]...), []byte("\r\n--owner-boundary\r\nContent-Disposition: form-data; name=\"extra\"\r\nContent-Type: text/plain\r\n\r\nx\r\n--owner-boundary--\r\n")...), ct: contentType},
		{name: "filename", body: replaceFirst(valid, "name=\"apply\"", "name=\"apply\"; filename=\"x\""), ct: contentType},
		{name: "extra-header", body: replaceFirst(valid, "Content-Type: text/plain\r\n", "Content-Type: text/plain\r\nX-Test: value\r\n"), ct: contentType},
		{name: "wrong-media", body: replaceFirst(valid, "Content-Type: application/json", "Content-Type: text/plain"), ct: contentType},
		{name: "bad-final", body: valid[:len(valid)-2], ct: contentType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := chatGPTOwnerPrivateTempDir(t)
			service := &chatGPTOwnerTestService{}
			h := NewMemoryChatGPTOwnerHandler(service, &chatGPTOwnerTestStore{}, root, "ren", []byte(chatGPTOwnerTestToken))
			rec := httptest.NewRecorder()
			h(rec, chatGPTOwnerRequest(http.MethodPost, "/v1/memory/import/chatgpt", tc.ct, tc.body, "cmd-control"))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if service.calls != 0 {
				t.Fatalf("service calls=%d", service.calls)
			}
		})
	}
}

func TestMemoryChatGPTOwnerHandlerBoundedUploadAndErrorMap(t *testing.T) {
	body, contentType := chatGPTOwnerMultipartBody("false", []byte("manifest"), []byte("artifact"), nil)
	service := &chatGPTOwnerTestService{}
	h := NewMemoryChatGPTOwnerHandlerWithLimits(service, &chatGPTOwnerTestStore{}, chatGPTOwnerPrivateTempDir(t), "ren", []byte(chatGPTOwnerTestToken), ChatGPTImportOwnerLimits{ManifestMaxBytes: 4, ArtifactMaxBytes: 4})
	rec := httptest.NewRecorder()
	h(rec, chatGPTOwnerRequest(http.MethodPost, "/v1/memory/import/chatgpt", contentType, body, "cmd-control"))
	if rec.Code != http.StatusRequestEntityTooLarge || service.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
	}

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid", err: domainmemory.ErrChatGPTImportInvalid, want: http.StatusBadRequest},
		{name: "forbidden", err: domainmemory.ErrChatGPTImportForbidden, want: http.StatusForbidden},
		{name: "not-found", err: domainmemory.ErrChatGPTImportNotFound, want: http.StatusNotFound},
		{name: "conflict", err: domainmemory.ErrChatGPTImportConflict, want: http.StatusConflict},
		{name: "source-changed", err: domainmemory.ErrChatGPTImportSourceChanged, want: http.StatusConflict},
		{name: "too-large", err: domainmemory.ErrChatGPTImportTooLarge, want: http.StatusRequestEntityTooLarge},
		{name: "artifact", err: domainmemory.ErrChatGPTImportArtifactInvalid, want: http.StatusUnprocessableEntity},
		{name: "unavailable", err: domainmemory.ErrChatGPTImportUnavailable, want: http.StatusServiceUnavailable},
		{name: "internal", err: domainmemory.ErrChatGPTImportInternal, want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := chatGPTOwnerPrivateTempDir(t)
			service := &chatGPTOwnerTestService{err: tc.err}
			h := NewMemoryChatGPTOwnerHandler(service, &chatGPTOwnerTestStore{}, root, "ren", []byte(chatGPTOwnerTestToken))
			rec := httptest.NewRecorder()
			h(rec, chatGPTOwnerRequest(http.MethodPost, "/v1/memory/import/chatgpt", contentType, body, "cmd-control"))
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestMemoryChatGPTOwnerHandlerStatusAndConfirmStrictContracts(t *testing.T) {
	view := domainmemory.ChatGPTImportView{EventID: "event", ImportID: "import", RequestID: "request", ExportID: "export-1", State: domainmemory.ChatGPTImportStateCompleted, AuditReference: "audit"}
	store := &chatGPTOwnerTestStore{statusView: view}
	h := NewMemoryChatGPTOwnerHandler(&chatGPTOwnerTestService{}, store, chatGPTOwnerPrivateTempDir(t), "ren", []byte(chatGPTOwnerTestToken))
	rec := httptest.NewRecorder()
	h(rec, chatGPTOwnerRequest(http.MethodGet, "/v1/memory/import/chatgpt/export-1", "", nil, "cmd-diagnostics"))
	if rec.Code != http.StatusOK || store.statusCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, store.statusCalls, rec.Body.String())
	}
	if store.statusRequestID == "" || store.statusOwnerID != "ren" || store.statusActorID != "ren" || store.statusExportID != "export-1" {
		t.Fatalf("status scope=%+v", store)
	}
	var statusBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	if statusBody["idempotent_replay"] != false {
		t.Fatalf("status replay=%#v", statusBody["idempotent_replay"])
	}
	withBody := chatGPTOwnerRequest(http.MethodGet, "/v1/memory/import/chatgpt/export-1", "", nil, "cmd-diagnostics")
	withBody.Body = io.NopCloser(strings.NewReader(""))
	withBody.ContentLength = 0
	rec = httptest.NewRecorder()
	h(rec, withBody)
	if rec.Code != http.StatusBadRequest || store.statusCalls != 1 {
		t.Fatalf("status body accepted: status=%d calls=%d body=%s", rec.Code, store.statusCalls, rec.Body.String())
	}
	wrongProfile := chatGPTOwnerRequest(http.MethodGet, "/v1/memory/import/chatgpt/export-1", "", nil, "cmd-control")
	rec = httptest.NewRecorder()
	h(rec, wrongProfile)
	if rec.Code != http.StatusForbidden || store.statusCalls != 1 {
		t.Fatalf("wrong profile: status=%d calls=%d body=%s", rec.Code, store.statusCalls, rec.Body.String())
	}
	for _, private := range []string{"owner_id", "actor_id", "path", "raw", "statement"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(`"`+private+`"`)) {
			t.Fatalf("status exposes private field %q", private)
		}
	}

	for _, tc := range []struct {
		name string
		body string
		ct   string
		want int
	}{
		{name: "default-dry-run", body: `{"export_id":"export-1","reason":"reviewed"}`, ct: "application/json", want: http.StatusOK},
		{name: "apply-true", body: `{"export_id":"export-1","reason":"reviewed","apply":true}`, ct: "application/json", want: http.StatusOK},
		{name: "duplicate", body: `{"export_id":"export-1","reason":"a","reason":"b"}`, ct: "application/json", want: http.StatusBadRequest},
		{name: "unknown", body: `{"export_id":"export-1","reason":"a","x":1}`, ct: "application/json", want: http.StatusBadRequest},
		{name: "trailing", body: `{"export_id":"export-1","reason":"a"}{}`, ct: "application/json", want: http.StatusBadRequest},
		{name: "missing-reason", body: `{"export_id":"export-1"}`, ct: "application/json", want: http.StatusBadRequest},
		{name: "wrong-content-type", body: `{"export_id":"export-1","reason":"a"}`, ct: "text/plain", want: http.StatusBadRequest},
		{name: "content-type-params", body: `{"export_id":"export-1","reason":"a"}`, ct: "application/json; charset=utf-8", want: http.StatusBadRequest},
		{name: "missing-content-type", body: `{"export_id":"export-1","reason":"a"}`, ct: "", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := store.confirmInput
			rec := httptest.NewRecorder()
			h(rec, chatGPTOwnerRequest(http.MethodPost, "/v1/memory/import/chatgpt/confirm", tc.ct, []byte(tc.body), "cmd-control"))
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.name == "default-dry-run" && (store.confirmInput.Apply || store.confirmInput.Reason != "reviewed") {
				t.Fatalf("confirm input=%+v", store.confirmInput)
			}
			if tc.want != http.StatusOK && store.confirmInput != before {
				t.Fatalf("invalid request reached store: before=%+v after=%+v", before, store.confirmInput)
			}
		})
	}
}

func TestMemoryChatGPTOwnerHandlerRejectsInvalidUTF8ConfirmBeforeStore(t *testing.T) {
	store := &chatGPTOwnerTestStore{}
	h := NewMemoryChatGPTOwnerHandler(&chatGPTOwnerTestService{}, store, chatGPTOwnerPrivateTempDir(t), "ren", []byte(chatGPTOwnerTestToken))
	payload := append([]byte(`{"export_id":"export-1","reason":"`), 0xff)
	payload = append(payload, []byte(`"}`)...)
	rec := httptest.NewRecorder()
	h(rec, chatGPTOwnerRequest(http.MethodPost, "/v1/memory/import/chatgpt/confirm", "application/json", payload, "cmd-control"))
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte(`"invalid_request"`)) {
		t.Fatalf("status=%d body=%s, want invalid_request 400", rec.Code, rec.Body.String())
	}
	if store.confirmInput != (domainmemory.ChatGPTImportConfirmInput{}) {
		t.Fatalf("invalid UTF-8 reached store: %+v", store.confirmInput)
	}
}

func TestMemoryChatGPTOwnerHandlerRejectsUnknownOwnerStatusAndBadPath(t *testing.T) {
	store := &chatGPTOwnerTestStore{statusErr: domainmemory.ErrChatGPTImportNotFound}
	h := NewMemoryChatGPTOwnerHandler(&chatGPTOwnerTestService{}, store, t.TempDir(), "ren", []byte(chatGPTOwnerTestToken))
	for _, path := range []string{
		"/v1/memory/import/chatgpt/unknown",
		"/v1/memory/import/chatgpt/a%2Fb",
		"/v1/memory/import/chatgpt/a%5Cb",
		"/v1/memory/import/chatgpt/%zz",
		"/v1/memory/import/chatgpt/%00",
		"/v1/memory/import/chatgpt/%ff",
		"/v1/memory/import/chatgpt/a/extra",
		"/v1/memory/import/chatgpt/",
	} {
		rec := httptest.NewRecorder()
		var req *http.Request
		if path == "/v1/memory/import/chatgpt/%zz" {
			req = chatGPTOwnerMalformedPathRequest(path)
		} else {
			req = chatGPTOwnerRequest(http.MethodGet, path, "", nil, "cmd-diagnostics")
		}
		h(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("path=%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h(rec, chatGPTOwnerRequest(http.MethodGet, "/v1/memory/import/chatgpt/export-1", "", nil, "cmd-diagnostics"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown owner status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type chatGPTOwnerMultipartPart struct {
	name      string
	mediaType string
	body      []byte
}

func chatGPTOwnerMultipartBody(apply string, manifest, artifact []byte, mutate func([]byte) []byte) ([]byte, string) {
	body := mustMultipartParts([]chatGPTOwnerMultipartPart{
		{name: "apply", mediaType: "text/plain", body: []byte(apply)},
		{name: "manifest", mediaType: "application/json", body: manifest},
		{name: "artifact", mediaType: "application/x-tar", body: artifact},
	})
	if mutate != nil {
		body = mutate(body)
	}
	return body, "multipart/form-data; boundary=owner-boundary"
}

func mustMultipartParts(parts []chatGPTOwnerMultipartPart) []byte {
	var body bytes.Buffer
	for _, part := range parts {
		body.WriteString("--owner-boundary\r\n")
		body.WriteString("Content-Disposition: form-data; name=\"")
		body.WriteString(part.name)
		body.WriteString("\"\r\nContent-Type: ")
		body.WriteString(part.mediaType)
		body.WriteString("\r\n\r\n")
		body.Write(part.body)
		body.WriteString("\r\n")
	}
	body.WriteString("--owner-boundary--\r\n")
	return body.Bytes()
}

func replaceFirst(body []byte, from, to string) []byte {
	return bytes.Replace(body, []byte(from), []byte(to), 1)
}

func chatGPTOwnerRequest(method, path, contentType string, body []byte, profile string) *http.Request {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, reader)
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer "+chatGPTOwnerTestToken)
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", profile)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

func chatGPTOwnerMalformedPathRequest(path string) *http.Request {
	req := chatGPTOwnerRequest(http.MethodGet, "/", "", nil, "cmd-diagnostics")
	req.URL = &url.URL{Path: path, RawPath: path}
	return req
}

func chatGPTOwnerPrivateTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod private test root: %v", err)
	}
	return root
}
