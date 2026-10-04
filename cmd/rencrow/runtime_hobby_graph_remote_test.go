package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	musiccatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/musiccatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	_ "modernc.org/sqlite"
)

func TestNewRuntimeMusicCatalogLookupUsesSelectedRPCOwnerWithoutLocalFallback(t *testing.T) {
	fixture := openRuntimeHobbyGraphRPCFixture(t)
	defer fixture.close()
	localPath := filepath.Join(t.TempDir(), "missing-parent", "local-hobby.db")
	lookup, err := newRuntimeMusicCatalogLookup(context.Background(), localPath, fixture.remote)
	if err != nil {
		t.Fatalf("selected HobbyGraph owner fell back to local opener: %v", err)
	}
	if lookup == nil || lookup.remote != fixture.remote || lookup.dbPath != "" {
		t.Fatalf("selected HobbyGraph lookup=%+v, want remote-only wrapper", lookup)
	}
	catalogValue, err := lookup.LookupMusic(context.Background(), "song", "Blue Bird", "", 5)
	if err != nil {
		t.Fatalf("remote HobbyGraph catalog lookup: %v", err)
	}
	catalog, ok := catalogValue.(musiccatalogapp.CatalogResult)
	if !ok || catalog.Status != "ok" || len(catalog.Items) != 1 || catalog.Items[0].ItemID != "song-1" {
		t.Fatalf("remote catalog DTO=%#v (%T)", catalogValue, catalogValue)
	}
	lyricsValue, err := lookup.LookupLyrics(context.Background(), "Blue Bird", "", "ja", "rights", 5)
	if err != nil {
		t.Fatalf("remote rights-conditioned lyrics lookup: %v", err)
	}
	lyrics, ok := lyricsValue.(musiccatalogapp.LyricsResult)
	if !ok || lyrics.Information != "rights" || lyrics.Status != "ok" || len(lyrics.Syntax) != 0 {
		t.Fatalf("remote rights DTO=%#v (%T)", lyricsValue, lyricsValue)
	}
	for _, entry := range lyrics.Lyrics {
		if entry.LyricsText != "" {
			t.Fatalf("rights-only lyrics result exposed text: %+v", entry)
		}
	}
}

func TestRuntimeHobbyGraphRemoteViewerUsesTypedOwnerForStatsAndOverview(t *testing.T) {
	fixture := openRuntimeHobbyGraphRPCFixture(t)
	defer fixture.close()
	localPath := filepath.Join(t.TempDir(), "missing-parent", "local-hobby.db")
	handler := runtimeHobbyGraphViewerHandler(viewer.HobbyGraphOptions{DBPath: localPath}, fixture.remote)
	for _, query := range []string{"", "?action=overview&limit=10"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/viewer/hobby-graph"+query, nil))
		if response.Code != 200 {
			t.Fatalf("remote HobbyGraph Viewer %s status=%d body=%s", query, response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["available"] != true || payload["stats"] == nil {
			t.Fatalf("remote HobbyGraph Viewer %s payload=%v", query, payload)
		}
	}
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("remote HobbyGraph Viewer opened local DB path %q", localPath)
	}
}

func TestRuntimeHobbyGraphRemoteCandidateUsesAuthenticatedOwnerScope(t *testing.T) {
	fixture := openRuntimeHobbyGraphRPCFixture(t)
	defer fixture.close()
	localPath := filepath.Join(t.TempDir(), "missing-parent", "local-hobby.db")
	lookup, err := newRuntimeMusicCatalogLookup(context.Background(), localPath, fixture.remote)
	if err != nil {
		t.Fatalf("select remote HobbyGraph owner: %v", err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteHobbyGraph(writeRegistry, lookup); err != nil {
		t.Fatalf("register remote HobbyGraph candidate writer: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallHobbyGraph(recallRegistry, lookup); err != nil {
		t.Fatalf("register remote HobbyGraph candidate recall: %v", err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	ctx := runtimeHobbyGraphUserContext(t, "hobby-request-rpc-1", "user-1", "shiro", false)
	beforeSignals := runtimeHobbyGraphCanonicalSignalCountDB(t, fixture.db)
	write := runtimeHobbyGraphExecuteWrite(t, worker, ctx, map[string]any{
		"target_item_id": "song-1", "signal_type": "like", "note": "remote proposal",
	})
	if write.OwnerRoute != "hobby_graph/propose_preference_candidate" || write.IdempotentReplay || write.AuditRef == "" {
		t.Fatalf("remote candidate data.write receipt=%+v", write)
	}
	if got := runtimeHobbyGraphCanonicalSignalCountDB(t, fixture.db); got != beforeSignals {
		t.Fatalf("remote candidate mutated canonical preference signals: before=%d after=%d", beforeSignals, got)
	}
	candidate := runtimeHobbyGraphExecuteRecall(t, worker, ctx, "preference_candidate", write.AuditRef)
	if len(candidate.Records) != 1 || candidate.Records[0]["user_id"] != "user-1" || candidate.Records[0]["target_item_id"] != "song-1" {
		t.Fatalf("remote private candidate recall=%+v", candidate)
	}
	request := runtimeHobbyGraphExecuteRecall(t, worker, ctx, "requests", "hobby-request-rpc-1")
	if len(request.Records) != 1 || request.Records[0]["candidate_id"] != write.AuditRef {
		t.Fatalf("remote private request recall=%+v", request)
	}
	otherUser := runtimeHobbyGraphUserContext(t, "hobby-request-other-user", "user-2", "shiro", false)
	if got := runtimeHobbyGraphExecuteRecall(t, worker, otherUser, "preference_candidate", write.AuditRef); len(got.Records) != 0 {
		t.Fatalf("remote candidate escaped authenticated user scope: %+v", got)
	}
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("remote HobbyGraph candidate path opened local DB %q", localPath)
	}
}

type runtimeHobbyGraphRPCFixture struct {
	db      *sql.DB
	handler *storagehost.Handler
	server  *httptest.Server
	client  *storagehost.Client
	remote  *storagehost.HobbyGraphClient
}

func openRuntimeHobbyGraphRPCFixture(t *testing.T) *runtimeHobbyGraphRPCFixture {
	t.Helper()
	path := seedRuntimeHobbyGraphOwnerDatabase(t)
	prepareRuntimeHobbyGraphRPCSchema(t, path)
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	owner, err := musiccatalogapp.NewStorageHostSQLiteStore(context.Background(), db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("open SQLite HobbyGraph owner: %v", err)
	}
	root := t.TempDir()
	handler, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: "runtime-hobby-graph-test", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := storagehost.RegisterHobbyGraphGroup(handler, owner); err != nil {
		_ = handler.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: server.URL, Token: "runtime-hobby-graph-test", HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = handler.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = handler.Close()
		_ = db.Close()
		t.Fatalf("HobbyGraph handshake: %v", err)
	}
	return &runtimeHobbyGraphRPCFixture{db: db, handler: handler, server: server, client: client, remote: storagehost.NewHobbyGraphClient(client)}
}

func prepareRuntimeHobbyGraphRPCSchema(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open HobbyGraph RPC fixture for schema prep: %v", err)
	}
	for _, statement := range []string{
		`ALTER TABLE hobby_items ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hobby_items ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hobby_relations ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hobby_interactions ADD COLUMN category TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hobby_interactions ADD COLUMN interaction_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hobby_interactions ADD COLUMN original_title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hobby_interactions ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hobby_interactions ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare HobbyGraph RPC Viewer schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close HobbyGraph RPC fixture schema connection: %v", err)
	}
	response := httptest.NewRecorder()
	viewer.HandleHobbyGraphBootstrap(viewer.HobbyGraphOptions{DBPath: path}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/bootstrap", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("bootstrap HobbyGraph RPC fixture schema: status=%d body=%s", response.Code, response.Body.String())
	}
}

func (fixture *runtimeHobbyGraphRPCFixture) close() {
	if fixture == nil {
		return
	}
	if fixture.server != nil {
		fixture.server.Close()
	}
	if fixture.handler != nil {
		_ = fixture.handler.Close()
	}
	if fixture.db != nil {
		_ = fixture.db.Close()
	}
}
