package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	toolregistrypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/toolregistry"
)

func TestRuntimeToolRegistryDatabaseViewerUsesSelectedRPCOwnerWithoutLocalFallback(t *testing.T) {
	fixture := openRuntimeToolRegistryViewerRPCFixture(t)
	defer fixture.close()
	localPath := filepath.Join(t.TempDir(), "missing", "local-tool-registry.db")
	runtimeTools := func(context.Context) ([]domaintool.ToolMetadata, error) {
		return []domaintool.ToolMetadata{
			{ToolID: "movie-fetch", Description: "runtime wins", Origin: domaintool.OriginCoreRuntime},
			{ToolID: "shell", Description: "shell tool", Origin: domaintool.OriginCoreRuntime},
		}, nil
	}
	handler := runtimeToolRegistryDatabaseViewerHandler(viewer.DatabaseViewerOptions{DBPath: localPath, RuntimeTools: runtimeTools}, fixture.remote)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/viewer/databases/tool-registry?platform=linux&limit=10", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("remote Tool Registry Viewer status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Available bool   `json:"available"`
		Total     int    `json:"total"`
		Error     string `json:"error"`
		Items     []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Platforms   []string `json:"platforms"`
			Source      string   `json:"source"`
			CreatedBy   string   `json:"created_by"`
			Origin      string   `json:"origin"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Available || payload.Total != 3 || len(payload.Items) != 3 {
		t.Fatalf("remote Tool Registry Viewer payload=%+v", payload)
	}
	if payload.Items[0].Name != "dynamic-only" || payload.Items[0].Source != "shiro-generated" || payload.Items[0].CreatedBy != "shiro" || payload.Items[0].Origin != domaintool.OriginDynamicRegistry {
		t.Fatalf("remote dynamic entry mapping=%+v", payload.Items[0])
	}
	if payload.Items[1].Name != "movie-fetch" || payload.Items[1].Description != "runtime wins" || payload.Items[1].Origin != domaintool.OriginCoreRuntime {
		t.Fatalf("runtime precedence=%+v", payload.Items[1])
	}
	allPlatforms := httptest.NewRecorder()
	handler.ServeHTTP(allPlatforms, httptest.NewRequest(http.MethodGet, "/viewer/databases/tool-registry?limit=1", nil))
	var allPlatformPayload struct {
		Available bool `json:"available"`
		Total     int  `json:"total"`
		Items     []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(allPlatforms.Body.Bytes(), &allPlatformPayload); err != nil {
		t.Fatal(err)
	}
	if allPlatforms.Code != http.StatusOK || !allPlatformPayload.Available || allPlatformPayload.Total != 4 || len(allPlatformPayload.Items) != 1 || allPlatformPayload.Items[0].Name != "darwin-only" {
		t.Fatalf("remote Tool Registry all-platform/limit response status=%d payload=%+v", allPlatforms.Code, allPlatformPayload)
	}
	badLimit := httptest.NewRecorder()
	handler.ServeHTTP(badLimit, httptest.NewRequest(http.MethodGet, "/viewer/databases/tool-registry?limit=0", nil))
	if badLimit.Code != http.StatusBadRequest {
		t.Fatalf("non-positive Tool Registry limit status=%d body=%s", badLimit.Code, badLimit.Body.String())
	}
	fixture.server.Close()
	partial := httptest.NewRecorder()
	handler.ServeHTTP(partial, httptest.NewRequest(http.MethodGet, "/viewer/databases/tool-registry?platform=linux&limit=10", nil))
	var partialPayload struct {
		Available bool   `json:"available"`
		Total     int    `json:"total"`
		Error     string `json:"error"`
		Items     []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(partial.Body.Bytes(), &partialPayload); err != nil {
		t.Fatal(err)
	}
	if partial.Code != http.StatusOK || !partialPayload.Available || partialPayload.Total != 2 || partialPayload.Error != "Tool Registry unavailable" || len(partialPayload.Items) != 2 {
		t.Fatalf("remote Tool Registry partial failure status=%d payload=%+v", partial.Code, partialPayload)
	}
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("selected Tool Registry Viewer opened local DB path %q", localPath)
	}
}

type runtimeToolRegistryViewerRPCFixture struct {
	owner   *toolregistrypersistence.SQLiteToolRegistryStore
	handler *storagehost.Handler
	server  *httptest.Server
	remote  *storagehost.ToolRegistryClient
}

func openRuntimeToolRegistryViewerRPCFixture(t *testing.T) *runtimeToolRegistryViewerRPCFixture {
	t.Helper()
	root := t.TempDir()
	owner, err := toolregistrypersistence.NewSQLiteToolRegistryStore(filepath.Join(root, "host-registry.db"))
	if err != nil {
		t.Fatalf("open Tool Registry SQLite owner: %v", err)
	}
	for _, entry := range []capdomain.ToolEntry{
		{Name: "dynamic-only", Description: "dynamic description", SchemaJSON: `{"type":"object"}`, Platforms: []string{"linux", "windows"}, Source: capdomain.ToolSourceShiroGenerated, CreatedAt: time.Date(2026, 9, 1, 2, 3, 4, 0, time.UTC), CreatedBy: "shiro"},
		{Name: "movie-fetch", Description: "dynamic value", SchemaJSON: `{"type":"object"}`, Platforms: []string{"linux"}, Source: capdomain.ToolSourceBuiltin, CreatedAt: time.Date(2026, 9, 1, 2, 3, 5, 0, time.UTC), CreatedBy: "builtin"},
		{Name: "darwin-only", Description: "darwin tool", SchemaJSON: `{"type":"object"}`, Platforms: []string{"darwin"}, Source: capdomain.ToolSourceBuiltin, CreatedAt: time.Date(2026, 9, 1, 2, 3, 6, 0, time.UTC), CreatedBy: "builtin"},
	} {
		if err := owner.Register(context.Background(), entry); err != nil {
			_ = owner.Close()
			t.Fatalf("seed Tool Registry owner: %v", err)
		}
	}
	storageHandler, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: "runtime-tool-registry-viewer-test", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = owner.Close()
		t.Fatal(err)
	}
	if err := storagehost.RegisterToolRegistryGroup(storageHandler, owner); err != nil {
		_ = storageHandler.Close()
		_ = owner.Close()
		t.Fatalf("register Tool Registry RPC owner: %v", err)
	}
	server := httptest.NewServer(storageHandler)
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: server.URL, Token: "runtime-tool-registry-viewer-test", HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = storageHandler.Close()
		_ = owner.Close()
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = storageHandler.Close()
		_ = owner.Close()
		t.Fatalf("Tool Registry RPC handshake: %v", err)
	}
	return &runtimeToolRegistryViewerRPCFixture{owner: owner, handler: storageHandler, server: server, remote: storagehost.NewToolRegistryClient(client)}
}

func (fixture *runtimeToolRegistryViewerRPCFixture) close() {
	if fixture == nil {
		return
	}
	if fixture.server != nil {
		fixture.server.Close()
	}
	if fixture.handler != nil {
		_ = fixture.handler.Close()
	}
	if fixture.owner != nil {
		_ = fixture.owner.Close()
	}
}
