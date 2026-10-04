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
	domainglossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	glossarypersistence "github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

func TestRuntimeGlossaryDatabaseViewerUsesSelectedRPCOwnerWithoutLocalFallback(t *testing.T) {
	fixture := openRuntimeGlossaryDatabaseViewerRPCFixture(t)
	defer fixture.close()
	localPath := filepath.Join(t.TempDir(), "missing", "local-glossary.db")
	handler := runtimeGlossaryDatabaseViewerHandler(viewer.DatabaseViewerOptions{DBPath: localPath}, fixture.remote)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/viewer/databases/glossary?limit=1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("remote Glossary Viewer status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Available bool `json:"available"`
		Total     int  `json:"total"`
		Items     []struct {
			ID          string `json:"id"`
			Term        string `json:"term"`
			Explanation string `json:"explanation"`
			Source      string `json:"source"`
			Category    string `json:"category"`
			CreatedAt   string `json:"created_at"`
			UpdatedAt   string `json:"updated_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Available || payload.Total != 3 || len(payload.Items) != 1 {
		t.Fatalf("remote Glossary Viewer total/page=%+v", payload)
	}
	item := payload.Items[0]
	if item.ID != "glossary-viewer-2" || item.Term != "term-2" || item.Explanation != "explanation-2" || item.Source != "https://example.test/source" || item.Category != "viewer" || item.CreatedAt == "" || item.UpdatedAt == "" {
		t.Fatalf("remote Glossary Viewer lost typed row DTO: %+v", item)
	}
	clamped := httptest.NewRecorder()
	handler.ServeHTTP(clamped, httptest.NewRequest(http.MethodGet, "/viewer/databases/glossary?limit=201", nil))
	if clamped.Code != http.StatusOK {
		t.Fatalf("remote Glossary Viewer max-limit response status=%d body=%s", clamped.Code, clamped.Body.String())
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/viewer/databases/glossary?limit=0", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("remote Glossary Viewer accepted zero limit: status=%d", invalid.Code)
	}
	fixture.server.Close()
	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/viewer/databases/glossary", nil))
	if unavailable.Code != http.StatusInternalServerError {
		t.Fatalf("selected Glossary owner outage status=%d body=%s, want fail-closed 500", unavailable.Code, unavailable.Body.String())
	}
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("selected Glossary Viewer opened local DB path %q", localPath)
	}
}

type runtimeGlossaryDatabaseViewerRPCFixture struct {
	owner   *glossarypersistence.SQLiteGlossaryRepository
	handler *storagehost.Handler
	server  *httptest.Server
	remote  *storagehost.GlossaryStoreClient
}

func openRuntimeGlossaryDatabaseViewerRPCFixture(t *testing.T) *runtimeGlossaryDatabaseViewerRPCFixture {
	t.Helper()
	root := t.TempDir()
	owner, err := glossarypersistence.NewSQLiteGlossaryRepository(filepath.Join(root, "host-glossary.db"))
	if err != nil {
		t.Fatalf("open Glossary SQLite owner: %v", err)
	}
	for index := 0; index < 3; index++ {
		item := &domainglossary.GlossaryItem{
			ID: "glossary-viewer-" + string(rune('0'+index)), Term: "term-" + string(rune('0'+index)),
			Explanation: "explanation-" + string(rune('0'+index)), Source: "https://example.test/source", Category: "viewer",
			CreatedAt: time.Unix(int64(index+1), 0).UTC(), UpdatedAt: time.Unix(int64(index+2), 0).UTC(),
		}
		if err := owner.Save(context.Background(), item); err != nil {
			_ = owner.Close()
			t.Fatalf("seed Glossary SQLite owner: %v", err)
		}
	}
	storageHandler, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: "runtime-glossary-database-viewer-test", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = owner.Close()
		t.Fatal(err)
	}
	if err := storagehost.RegisterGlossaryGroup(storageHandler, owner); err != nil {
		_ = storageHandler.Close()
		_ = owner.Close()
		t.Fatalf("register Glossary RPC owner: %v", err)
	}
	server := httptest.NewServer(storageHandler)
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: server.URL, Token: "runtime-glossary-database-viewer-test", HTTPClient: server.Client()})
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
		t.Fatalf("Glossary database Viewer RPC handshake: %v", err)
	}
	return &runtimeGlossaryDatabaseViewerRPCFixture{owner: owner, handler: storageHandler, server: server, remote: storagehost.NewGlossaryStoreClient(client)}
}

func (fixture *runtimeGlossaryDatabaseViewerRPCFixture) close() {
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
