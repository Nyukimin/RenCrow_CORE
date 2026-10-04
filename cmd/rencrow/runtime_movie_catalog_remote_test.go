package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	_ "modernc.org/sqlite"
)

func TestNewRuntimeMovieCatalogLookupUsesSelectedRPCOwnerWithoutLocalFallback(t *testing.T) {
	fixture := openRuntimeMovieCatalogRPCFixture(t)
	defer fixture.close()
	localPath := filepath.Join(t.TempDir(), "missing-parent", "local-movie.db")
	lookup, err := newRuntimeMovieCatalogLookup(context.Background(), localPath, fixture.remote)
	if err != nil {
		t.Fatalf("selected MovieCatalog owner fell back to local opener: %v", err)
	}
	if lookup == nil || lookup.remote != fixture.remote || lookup.dbPath != "" {
		t.Fatalf("selected MovieCatalog lookup=%+v, want remote-only wrapper", lookup)
	}
	resultValue, err := lookup.Lookup(context.Background(), "movie", "Heat", "all", 5)
	if err != nil {
		t.Fatalf("remote movie lookup: %v", err)
	}
	result, ok := resultValue.(moviecatalogapp.LookupResult)
	if !ok || result.Kind != "movie" || len(result.Movies) != 1 || result.Detail == nil {
		t.Fatalf("remote movie lookup result=%#v (%T)", resultValue, resultValue)
	}
}

func TestRuntimeMovieCatalogRemoteViewerUsesTypedOwnerForSixSurfaces(t *testing.T) {
	fixture := openRuntimeMovieCatalogRPCFixture(t)
	defer fixture.close()
	localPath := filepath.Join(t.TempDir(), "missing-parent", "local-movie.db")
	handler := runtimeMovieCatalogViewerHandler(viewer.MovieCatalogOptions{DBPath: localPath}, fixture.remote)
	tests := []struct {
		action string
		query  string
		field  string
	}{
		{action: "stats", field: "stats"},
		{action: "movies", field: "items"},
		{action: "people", field: "items"},
		{action: "cards", field: "items"},
		{action: "movie", query: "&id=m1", field: "detail"},
		{action: "person", query: "&id=p1", field: "detail"},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/viewer/movie-catalog?action="+test.action+test.query, nil))
			if response.Code != 200 {
				t.Fatalf("remote Viewer status=%d body=%s", response.Code, response.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["available"] != true || payload["action"] != test.action || payload[test.field] == nil {
				t.Fatalf("remote Viewer %s payload=%v", test.action, payload)
			}
		})
	}
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("remote MovieCatalog Viewer opened local DB path %q", localPath)
	}
}

func TestRuntimeMovieCatalogRemoteCandidateUsesAuthenticatedOwnerScope(t *testing.T) {
	fixture := openRuntimeMovieCatalogRPCFixture(t)
	defer fixture.close()
	localPath := filepath.Join(t.TempDir(), "missing-parent", "local-movie.db")
	lookup, err := newRuntimeMovieCatalogLookup(context.Background(), localPath, fixture.remote)
	if err != nil {
		t.Fatalf("select remote MovieCatalog owner: %v", err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteMovieCatalog(writeRegistry, lookup); err != nil {
		t.Fatalf("register remote MovieCatalog candidate writer: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallMovieCatalog(recallRegistry, lookup); err != nil {
		t.Fatalf("register remote MovieCatalog candidate lookup: %v", err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	ctx := runtimeDataWriteOwnerContext(t, "movie-request-rpc-1", true)
	write := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "movie_catalog", "propose_preference_candidate", map[string]any{
		"target_kind": "movie", "target_id": "m1", "familiarity": "known", "sentiment": "like", "note": "remote candidate",
	})
	if write.OwnerRoute != "movie_catalog/propose_preference_candidate" || write.IdempotentReplay || write.AuditRef == "" {
		t.Fatalf("remote candidate data.write receipt=%+v", write)
	}
	recalled := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "movie_catalog", "preference_candidate", write.AuditRef)
	if len(recalled.Records) != 1 || recalled.Records[0]["target_id"] != "m1" {
		t.Fatalf("remote candidate data.recall=%+v", recalled)
	}
	request := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "movie_catalog", "requests", "movie-request-rpc-1")
	if len(request.Records) != 1 || request.Records[0]["candidate_id"] != write.AuditRef {
		t.Fatalf("remote private request recall=%+v", request)
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		t.Fatal("user scope missing from remote candidate test context")
	}
	scope.AuthenticatedUserID = "user-2"
	otherUserContext := domaintool.WithToolExecutionScope(context.Background(), scope)
	otherUser := runtimeDataWriteOwnerExecuteRecall(t, worker, otherUserContext, "movie_catalog", "preference_candidate", write.AuditRef)
	if len(otherUser.Records) != 0 {
		t.Fatalf("remote private candidate escaped authenticated user scope: %+v", otherUser)
	}
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("remote MovieCatalog candidate path opened local DB %q", localPath)
	}
}

type runtimeMovieCatalogRPCFixture struct {
	db      *sql.DB
	handler *storagehost.Handler
	server  *httptest.Server
	client  *storagehost.Client
	remote  *storagehost.MovieCatalogClient
}

func openRuntimeMovieCatalogRPCFixture(t *testing.T) *runtimeMovieCatalogRPCFixture {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "host-movie.db")+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE movies(movie_id TEXT PRIMARY KEY,title TEXT NOT NULL,url TEXT NOT NULL,synopsis TEXT);
CREATE TABLE people(person_id TEXT PRIMARY KEY,name TEXT NOT NULL,url TEXT NOT NULL,profile_json TEXT,biography TEXT);
CREATE TABLE movie_people(movie_id TEXT NOT NULL,person_id TEXT NOT NULL,role TEXT NOT NULL,source TEXT NOT NULL,movie_title TEXT,person_name TEXT,movie_url TEXT,person_url TEXT);
CREATE TABLE fetch_log(source TEXT NOT NULL);
CREATE TABLE movie_catalog_assessments(kind TEXT NOT NULL,target_id TEXT NOT NULL,target_label TEXT NOT NULL,familiarity TEXT NOT NULL DEFAULT '',sentiment TEXT NOT NULL DEFAULT '',updated_by TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(kind,target_id));
INSERT INTO movies(movie_id,title,url,synopsis) VALUES('m1','Heat','https://example.test/m1','A crime drama');
INSERT INTO people(person_id,name,url,profile_json,biography) VALUES('p1','Al Pacino','https://example.test/p1','{}','Profile');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url) VALUES('m1','p1','actor','test','Heat','Al Pacino','https://example.test/m1','https://example.test/p1');
INSERT INTO movie_catalog_assessments(kind,target_id,target_label,familiarity,sentiment,updated_by,updated_at) VALUES('movie','m1','Heat','known','like','test','2026-10-03T00:00:00Z');`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	owner, err := moviecatalogapp.NewStorageHostSQLiteStore(context.Background(), db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("open SQLite MovieCatalog owner: %v", err)
	}
	handler, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: "runtime-movie-catalog-test", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := storagehost.RegisterMovieCatalogGroup(handler, owner); err != nil {
		_ = handler.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: server.URL, Token: "runtime-movie-catalog-test", HTTPClient: server.Client()})
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
		t.Fatalf("MovieCatalog handshake: %v", err)
	}
	return &runtimeMovieCatalogRPCFixture{db: db, handler: handler, server: server, client: client, remote: storagehost.NewMovieCatalogClient(client)}
}

func (fixture *runtimeMovieCatalogRPCFixture) close() {
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
