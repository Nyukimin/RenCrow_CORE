package viewer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/datacapability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	_ "modernc.org/sqlite"
)

func runtimeToolProvider(metas ...domaintool.ToolMetadata) func(context.Context) ([]domaintool.ToolMetadata, error) {
	return func(context.Context) ([]domaintool.ToolMetadata, error) { return metas, nil }
}

func TestHandleDataCapabilityCatalogReturnsSummaryWithoutPathsOrRows(t *testing.T) {
	items := make([]datacapability.Entry, 0, 20)
	statuses := []string{"available", "unavailable", "restricted", "blocked"}
	for i := 0; i < 20; i++ {
		items = append(items, datacapability.Entry{Name: fmt.Sprintf("store-%02d", i), PhysicalKey: fmt.Sprintf("storage.databases.store_%02d", i), Status: statuses[i%len(statuses)], Owner: "CORE", Categories: []string{"metadata"}, SafeOperations: []string{"describe"}, Sensitivity: "internal", Reason: "policy"})
	}
	rec := httptest.NewRecorder()
	HandleDataCapabilityCatalog(func(context.Context) ([]datacapability.Entry, error) { return items, nil }).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/databases/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Available bool                   `json:"available"`
		Total     int                    `json:"total"`
		Summary   dataCapabilitySummary  `json:"summary"`
		Items     []datacapability.Entry `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Available || out.Total != 20 || out.Summary.Available != 5 || out.Summary.Unavailable != 5 || out.Summary.Restricted != 5 || out.Summary.Blocked != 5 {
		t.Fatalf("unexpected response: %+v", out)
	}
	if strings.Contains(rec.Body.String(), "/srv/") || strings.Contains(rec.Body.String(), "db_path") {
		t.Fatalf("response leaked path field: %s", rec.Body.String())
	}
}

func TestHandleDataCapabilityCatalogUnavailable(t *testing.T) {
	for name, provider := range map[string]DataCapabilityCatalogProvider{"nil": nil, "error": func(context.Context) ([]datacapability.Entry, error) { return nil, errors.New("private detail") }} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			HandleDataCapabilityCatalog(provider).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if strings.Contains(rec.Body.String(), "private detail") {
				t.Fatalf("internal error leaked: %s", rec.Body.String())
			}
			var out dataCapabilityCatalogResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Available || out.Error == "" || len(out.Items) != 0 {
				t.Fatalf("unexpected response: %+v", out)
			}
		})
	}
}

func databaseViewerOptionsWithRuntimeTools(t *testing.T, dbPath string, metas ...domaintool.ToolMetadata) DatabaseViewerOptions {
	t.Helper()
	return DatabaseViewerOptions{DBPath: dbPath, RuntimeTools: runtimeToolProvider(metas...)}
}

func runtimeToolMetadata(t *testing.T, id, description, origin string) domaintool.ToolMetadata {
	t.Helper()
	return domaintool.ToolMetadata{ToolID: id, Description: description, Origin: origin}
}

func TestHandleConversationArchiveDatabaseListsOnlyArchiveRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory_archive.db")
	db := openDatabaseViewerTestDB(t, dbPath)
	mustExecDatabaseViewerTest(t, db, `CREATE TABLE session_thread (
		thread_id INTEGER PRIMARY KEY, session_id TEXT, ts_start DATETIME, ts_end DATETIME,
		domain TEXT, summary TEXT, keywords TEXT, embedding TEXT, is_novel BOOLEAN
	)`)
	mustExecDatabaseViewerTest(t, db, `INSERT INTO session_thread VALUES
		(1, 'session-a', '2026-08-01T10:00:00Z', '2026-08-01T10:10:00Z', 'movie', '映画の会話', '["映画"]', '[]', 1),
		(2, 'session-b', '2026-08-02T10:00:00Z', '2026-08-02T10:10:00Z', 'music', '音楽の会話', '["音楽"]', '[]', 0)`)
	db.Close()

	req := httptest.NewRequest(http.MethodGet, "/viewer/databases/conversation-archive?domain=movie&limit=10", nil)
	rec := httptest.NewRecorder()
	HandleConversationArchiveDatabase(DatabaseViewerOptions{DBPath: dbPath}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Available bool `json:"available"`
		Total     int  `json:"total"`
		Items     []struct {
			ThreadID int64    `json:"thread_id"`
			Domain   string   `json:"domain"`
			Keywords []string `json:"keywords"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !out.Available || out.Total != 1 || len(out.Items) != 1 || out.Items[0].ThreadID != 1 || out.Items[0].Domain != "movie" {
		t.Fatalf("unexpected archive response: %+v", out)
	}
	if len(out.Items[0].Keywords) != 1 || out.Items[0].Keywords[0] != "映画" {
		t.Fatalf("keywords = %#v", out.Items[0].Keywords)
	}
}

func TestHandleGlossaryDatabaseListsGlossaryRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "glossary.db")
	db := openDatabaseViewerTestDB(t, dbPath)
	mustExecDatabaseViewerTest(t, db, `CREATE TABLE glossary_items (
		id TEXT PRIMARY KEY, term TEXT, explanation TEXT, source TEXT, category TEXT,
		created_at DATETIME, updated_at DATETIME
	)`)
	mustExecDatabaseViewerTest(t, db, `INSERT INTO glossary_items VALUES
		('g-1', 'D0', 'root card', 'spec', 'movie', '2026-08-01T10:00:00Z', '2026-08-01T10:00:00Z')`)
	db.Close()

	req := httptest.NewRequest(http.MethodGet, "/viewer/databases/glossary?limit=10", nil)
	rec := httptest.NewRecorder()
	HandleGlossaryDatabase(DatabaseViewerOptions{DBPath: dbPath}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Available bool `json:"available"`
		Items     []struct {
			Term     string `json:"term"`
			Category string `json:"category"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !out.Available || len(out.Items) != 1 || out.Items[0].Term != "D0" || out.Items[0].Category != "movie" {
		t.Fatalf("unexpected glossary response: %+v", out)
	}
}

func TestHandleToolRegistryDatabaseListsRegistryRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tool_registry.db")
	db := openDatabaseViewerTestDB(t, dbPath)
	mustExecDatabaseViewerTest(t, db, `CREATE TABLE tool_registry (
		name TEXT PRIMARY KEY, description TEXT, schema_json TEXT, platforms TEXT,
		source TEXT, created_at DATETIME, created_by TEXT
	)`)
	mustExecDatabaseViewerTest(t, db, `INSERT INTO tool_registry VALUES
		('movie-fetch', '映画情報を取得', '{}', '["linux","windows"]', 'builtin', '2026-08-01T10:00:00Z', 'builtin')`)
	db.Close()

	req := httptest.NewRequest(http.MethodGet, "/viewer/databases/tool-registry?platform=linux&limit=10", nil)
	rec := httptest.NewRecorder()
	HandleToolRegistryDatabase(DatabaseViewerOptions{DBPath: dbPath}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Available bool `json:"available"`
		Items     []struct {
			Name      string   `json:"name"`
			Platforms []string `json:"platforms"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !out.Available || len(out.Items) != 1 || out.Items[0].Name != "movie-fetch" || len(out.Items[0].Platforms) != 2 {
		t.Fatalf("unexpected tool registry response: %+v", out)
	}
}

func TestHandleToolRegistryDatabaseListsRuntimeToolsWithoutDatabase(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/databases/tool-registry?limit=10", nil)
	rec := httptest.NewRecorder()
	HandleToolRegistryDatabase(databaseViewerOptionsWithRuntimeTools(t, "",
		runtimeToolMetadata(t, "shell", "shell", ""),
		runtimeToolMetadata(t, "browser.run", "browser", "rencrow_tools"),
	)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Available bool `json:"available"`
		Total     int  `json:"total"`
		Items     []struct {
			Name   string `json:"name"`
			Origin string `json:"origin"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Available || out.Total != 2 || out.Items[0].Name != "browser.run" || out.Items[0].Origin != "rencrow_tools" || out.Items[1].Name != "shell" || out.Items[1].Origin != "core_runtime" {
		t.Fatalf("unexpected runtime tools: %+v", out)
	}
}

func TestHandleToolRegistryDatabaseMergesRuntimeAndDynamicWithRuntimePrecedence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tool_registry.db")
	db := openDatabaseViewerTestDB(t, dbPath)
	mustExecDatabaseViewerTest(t, db, `CREATE TABLE tool_registry (
		name TEXT PRIMARY KEY, description TEXT, schema_json TEXT, platforms TEXT,
		source TEXT, created_at DATETIME, created_by TEXT
	)`)
	mustExecDatabaseViewerTest(t, db, `INSERT INTO tool_registry VALUES
		('alpha', 'dynamic collision', '{}', '["linux"]', 'shiro-generated', '2026-08-01T10:00:00Z', 'shiro'),
		('zeta', 'dynamic only', '{}', '["linux"]', 'shiro-generated', '2026-08-01T10:00:00Z', 'shiro')`)
	db.Close()

	req := httptest.NewRequest(http.MethodGet, "/viewer/databases/tool-registry?platform=linux&limit=10", nil)
	rec := httptest.NewRecorder()
	HandleToolRegistryDatabase(databaseViewerOptionsWithRuntimeTools(t, dbPath,
		runtimeToolMetadata(t, "alpha", "runtime collision", ""),
	)).ServeHTTP(rec, req)
	var out struct {
		Items []struct {
			Name, Description, Origin string
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 || out.Items[0].Name != "alpha" || out.Items[0].Description != "runtime collision" || out.Items[0].Origin != "core_runtime" || out.Items[1].Name != "zeta" || out.Items[1].Origin != "dynamic_registry" {
		t.Fatalf("unexpected merged tools: %+v", out.Items)
	}
}

func TestHandleToolRegistryDatabaseReturnsRuntimeToolsOnDatabaseError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/databases/tool-registry", nil)
	rec := httptest.NewRecorder()
	HandleToolRegistryDatabase(databaseViewerOptionsWithRuntimeTools(t, t.TempDir(),
		runtimeToolMetadata(t, "shell", "", ""),
	)).ServeHTTP(rec, req)
	var out struct {
		Available bool   `json:"available"`
		Total     int    `json:"total"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Available || out.Total != 1 || out.Error == "" {
		t.Fatalf("unexpected partial response: %+v", out)
	}
}

func TestDatabaseViewersReportUnconfiguredWithoutCreatingFiles(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"archive":  HandleConversationArchiveDatabase(DatabaseViewerOptions{}),
		"glossary": HandleGlossaryDatabase(DatabaseViewerOptions{}),
		"tools":    HandleToolRegistryDatabase(DatabaseViewerOptions{}),
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			var out struct {
				Available bool   `json:"available"`
				Error     string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if out.Available || out.Error != "database is not configured" {
				t.Fatalf("unexpected unavailable response: %+v", out)
			}
		})
	}
}

func openDatabaseViewerTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func mustExecDatabaseViewerTest(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec sqlite: %v", err)
	}
}
