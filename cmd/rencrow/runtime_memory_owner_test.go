package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestBuildViewerRuntimeHandlersChatGPTImportOwnerRequiresUnavailableStore(t *testing.T) {
	cfg := chatGPTImportOwnerRuntimeTestConfig(t)
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(cfg, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerMemoryChatGPTImportOwner == nil {
		t.Fatal("ChatGPT owner handler is nil")
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/memory/import/chatgpt", nil)
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer "+chatGPTImportOwnerRuntimeTestToken)
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	req.Header.Set("Content-Type", "multipart/form-data; boundary=runtime-boundary")
	rec := httptest.NewRecorder()
	deps.viewerMemoryChatGPTImportOwner.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want nil-store unavailable", rec.Code, rec.Body.String())
	}
}

func TestNewConfiguredMemoryOwnerHandlersShareCredentialAndIdentity(t *testing.T) {
	cfg := chatGPTImportOwnerRuntimeTestConfig(t)
	ownerHandler, chatGPTHandler, err := newConfiguredMemoryOwnerHandlers(cfg, nil)
	if err != nil {
		t.Fatalf("newConfiguredMemoryOwnerHandlers() error=%v", err)
	}
	if ownerHandler == nil || chatGPTHandler == nil {
		t.Fatalf("handlers owner=%v chatgpt=%v, want both enabled", ownerHandler != nil, chatGPTHandler != nil)
	}

	ownerRec := httptest.NewRecorder()
	ownerHandler.ServeHTTP(ownerRec, runtimeOwnerRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user", "cmd-diagnostics", chatGPTImportOwnerRuntimeTestToken, nil))
	if ownerRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("owner status=%d body=%s, want nil-store unavailable", ownerRec.Code, ownerRec.Body.String())
	}
	chatGPTRec := httptest.NewRecorder()
	chatGPTHandler.ServeHTTP(chatGPTRec, runtimeOwnerRequest(http.MethodGet, "http://127.0.0.1/v1/memory/import/chatgpt/export-1", "cmd-diagnostics", chatGPTImportOwnerRuntimeTestToken, nil))
	if chatGPTRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ChatGPT status=%d body=%s, want nil-store unavailable", chatGPTRec.Code, chatGPTRec.Body.String())
	}

	wrongOwnerRec := httptest.NewRecorder()
	ownerHandler.ServeHTTP(wrongOwnerRec, runtimeOwnerRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user", "cmd-diagnostics", "wrong-runtime-token-012345678901234567890", nil))
	wrongChatGPTRec := httptest.NewRecorder()
	chatGPTHandler.ServeHTTP(wrongChatGPTRec, runtimeOwnerRequest(http.MethodGet, "http://127.0.0.1/v1/memory/import/chatgpt/export-1", "cmd-diagnostics", "wrong-runtime-token-012345678901234567890", nil))
	if wrongOwnerRec.Code != http.StatusUnauthorized || wrongChatGPTRec.Code != http.StatusUnauthorized {
		t.Fatalf("shared auth owner=%d ChatGPT=%d, want both unauthorized", wrongOwnerRec.Code, wrongChatGPTRec.Code)
	}
}

func TestNewConfiguredMemoryOwnerHandlersWireL1StoreForChatGPTStatus(t *testing.T) {
	cfg := chatGPTImportOwnerRuntimeTestConfig(t)
	cfg.Storage.Memory.RawSourceDir = filepath.Join(t.TempDir(), "raw-source")
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.sqlite"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore() error=%v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("store.Close() error=%v", err)
		}
	}()

	ownerHandler, chatGPTHandler, err := newConfiguredMemoryOwnerHandlers(cfg, store)
	if err != nil {
		t.Fatalf("newConfiguredMemoryOwnerHandlers() error=%v", err)
	}
	if ownerHandler == nil || chatGPTHandler == nil {
		t.Fatalf("handlers owner=%v ChatGPT=%v, want both enabled", ownerHandler != nil, chatGPTHandler != nil)
	}

	rec := httptest.NewRecorder()
	chatGPTHandler.ServeHTTP(rec, runtimeOwnerRequest(
		http.MethodGet,
		"http://127.0.0.1/v1/memory/import/chatgpt/export-not-in-ledger",
		"cmd-diagnostics",
		chatGPTImportOwnerRuntimeTestToken,
		nil,
	))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want store-backed unknown export not_found", rec.Code, rec.Body.String())
	}
}

func TestNewConfiguredMemoryOwnerHandlersNilStoreRejectsUploadBeforeBodyOrStage(t *testing.T) {
	cfg := chatGPTImportOwnerRuntimeTestConfig(t)
	rawRoot := filepath.Join(t.TempDir(), "raw-source")
	cfg.Storage.Memory.RawSourceDir = rawRoot
	_, chatGPTHandler, err := newConfiguredMemoryOwnerHandlers(cfg, nil)
	if err != nil {
		t.Fatalf("newConfiguredMemoryOwnerHandlers() error=%v", err)
	}
	body := &runtimeCountingReader{reader: strings.NewReader("body-must-not-be-read")}
	req := runtimeOwnerRequest(http.MethodPost, "http://127.0.0.1/v1/memory/import/chatgpt", "cmd-control", chatGPTImportOwnerRuntimeTestToken, nil)
	req.Body = io.NopCloser(body)
	req.ContentLength = -1
	req.Header.Set("Content-Type", "multipart/form-data; boundary=runtime-boundary")
	rec := httptest.NewRecorder()
	chatGPTHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want nil-service unavailable", rec.Code, rec.Body.String())
	}
	if body.reads != 0 {
		t.Fatalf("body reads=%d, auth/store gate must precede body", body.reads)
	}
	if _, err := os.Stat(rawRoot); !os.IsNotExist(err) {
		t.Fatalf("raw root err=%v, stage must not be created", err)
	}
}

func TestBuildViewerRuntimeHandlersDisablesBothOwnerHandlersWhenDisabledOrTokenInvalid(t *testing.T) {
	disabled := chatGPTImportOwnerRuntimeTestConfig(t)
	disabled.LocalAgentOps.Enabled = false
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(disabled, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerMemoryOwner != nil || deps.viewerMemoryChatGPTImportOwner != nil {
		t.Fatalf("disabled owner handlers owner=%v ChatGPT=%v, want nil", deps.viewerMemoryOwner != nil, deps.viewerMemoryChatGPTImportOwner != nil)
	}

	badToken := chatGPTImportOwnerRuntimeTestConfig(t)
	badToken.LocalAgentOps.AuthTokenFile = filepath.Join(t.TempDir(), "missing.token")
	deps = &Dependencies{}
	buildViewerRuntimeHandlers(badToken, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerMemoryOwner != nil || deps.viewerMemoryChatGPTImportOwner != nil {
		t.Fatalf("bad-token owner handlers owner=%v ChatGPT=%v, want nil", deps.viewerMemoryOwner != nil, deps.viewerMemoryChatGPTImportOwner != nil)
	}
}

func TestRegisterKnowledgeMemorySourceRoutesReachesChatGPTOwnerHandler(t *testing.T) {
	cfg := chatGPTImportOwnerRuntimeTestConfig(t)
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(cfg, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	mux := http.NewServeMux()
	registerKnowledgeMemorySourceRoutes(mux, deps)
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/v1/memory/import/chatgpt"},
		{method: http.MethodGet, path: "/v1/memory/import/chatgpt/export-1"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(route.method, route.path, nil)
		req.RemoteAddr = "127.0.0.1:18790"
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("path=%s status=%d body=%s, want exact ChatGPT owner handler", route.path, rec.Code, rec.Body.String())
		}
	}
}

const chatGPTImportOwnerRuntimeTestToken = "runtime-owner-token-012345678901234567890123"

func chatGPTImportOwnerRuntimeTestConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	tokenPath := filepath.Join(root, "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(chatGPTImportOwnerRuntimeTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		WorkspaceDir: root,
		LocalAgentOps: config.LocalAgentOpsConfig{
			Enabled:       true,
			UserID:        "ren",
			AuthTokenFile: tokenPath,
		},
	}
}

func runtimeOwnerRequest(method, target, profile, token string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", profile)
	return req
}

type runtimeCountingReader struct {
	reader io.Reader
	reads  int
}

func (reader *runtimeCountingReader) Read(data []byte) (int, error) {
	reader.reads++
	return reader.reader.Read(data)
}
