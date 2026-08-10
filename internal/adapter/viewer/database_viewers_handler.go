package viewer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// DatabaseViewerOptions identifies one configured SQLite database exposed by a
// read-only Viewer. Handlers never create, migrate, or modify the configured DB.
type DatabaseViewerOptions struct {
	DBPath string
}

type databaseViewerResponse struct {
	Available bool   `json:"available"`
	DBPath    string `json:"db_path,omitempty"`
	Total     int    `json:"total"`
	Items     any    `json:"items"`
	Error     string `json:"error,omitempty"`
}

type conversationArchiveViewerItem struct {
	ThreadID  int64    `json:"thread_id"`
	SessionID string   `json:"session_id"`
	StartTime string   `json:"start_time"`
	EndTime   string   `json:"end_time"`
	Domain    string   `json:"domain"`
	Summary   string   `json:"summary"`
	Keywords  []string `json:"keywords"`
	IsNovel   bool     `json:"is_novel"`
}

type glossaryDatabaseViewerItem struct {
	ID          string `json:"id"`
	Term        string `json:"term"`
	Explanation string `json:"explanation"`
	Source      string `json:"source"`
	Category    string `json:"category"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type toolRegistryDatabaseViewerItem struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SchemaJSON  string   `json:"schema_json"`
	Platforms   []string `json:"platforms"`
	Source      string   `json:"source"`
	CreatedAt   string   `json:"created_at"`
	CreatedBy   string   `json:"created_by"`
}

func HandleConversationArchiveDatabase(opts DatabaseViewerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 200)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		db, path, ok := openViewerSQLiteReadOnly(w, opts.DBPath)
		if !ok {
			return
		}
		defer db.Close()

		where := make([]string, 0, 2)
		args := make([]any, 0, 3)
		if sessionID := strings.TrimSpace(r.URL.Query().Get("session_id")); sessionID != "" {
			where = append(where, "session_id = ?")
			args = append(args, sessionID)
		}
		if domain := strings.TrimSpace(r.URL.Query().Get("domain")); domain != "" {
			where = append(where, "domain = ?")
			args = append(args, domain)
		}
		clause := ""
		if len(where) > 0 {
			clause = " WHERE " + strings.Join(where, " AND ")
		}
		var total int
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM session_thread"+clause, args...).Scan(&total); err != nil {
			http.Error(w, "failed to count conversation archive", http.StatusInternalServerError)
			return
		}
		queryArgs := append(append([]any{}, args...), limit)
		rows, err := db.QueryContext(r.Context(), `
			SELECT thread_id, session_id, CAST(ts_start AS TEXT), CAST(ts_end AS TEXT),
			       domain, summary, keywords, is_novel
			FROM session_thread`+clause+`
			ORDER BY ts_start DESC, rowid DESC LIMIT ?`, queryArgs...)
		if err != nil {
			http.Error(w, "failed to load conversation archive", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items := make([]conversationArchiveViewerItem, 0, limit)
		for rows.Next() {
			var item conversationArchiveViewerItem
			var keywordsJSON string
			if err := rows.Scan(&item.ThreadID, &item.SessionID, &item.StartTime, &item.EndTime, &item.Domain, &item.Summary, &keywordsJSON, &item.IsNovel); err != nil {
				http.Error(w, "failed to scan conversation archive", http.StatusInternalServerError)
				return
			}
			if err := json.Unmarshal([]byte(keywordsJSON), &item.Keywords); err != nil {
				item.Keywords = []string{}
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read conversation archive", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: true, DBPath: path, Total: total, Items: items})
	}
}

func HandleGlossaryDatabase(opts DatabaseViewerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 200)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		db, path, ok := openViewerSQLiteReadOnly(w, opts.DBPath)
		if !ok {
			return
		}
		defer db.Close()

		var total int
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM glossary_items").Scan(&total); err != nil {
			http.Error(w, "failed to count glossary", http.StatusInternalServerError)
			return
		}
		rows, err := db.QueryContext(r.Context(), `
			SELECT id, term, explanation, source, category,
			       CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
			FROM glossary_items ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
		if err != nil {
			http.Error(w, "failed to load glossary", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items := make([]glossaryDatabaseViewerItem, 0, limit)
		for rows.Next() {
			var item glossaryDatabaseViewerItem
			if err := rows.Scan(&item.ID, &item.Term, &item.Explanation, &item.Source, &item.Category, &item.CreatedAt, &item.UpdatedAt); err != nil {
				http.Error(w, "failed to scan glossary", http.StatusInternalServerError)
				return
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read glossary", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: true, DBPath: path, Total: total, Items: items})
	}
}

func HandleToolRegistryDatabase(opts DatabaseViewerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 100, 300)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		db, path, ok := openViewerSQLiteReadOnly(w, opts.DBPath)
		if !ok {
			return
		}
		defer db.Close()

		platform := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("platform")))
		where := ""
		args := make([]any, 0, 2)
		if platform != "" {
			where = " WHERE platforms LIKE ?"
			args = append(args, "%\""+platform+"\"%")
		}
		var total int
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM tool_registry"+where, args...).Scan(&total); err != nil {
			http.Error(w, "failed to count Tool Registry", http.StatusInternalServerError)
			return
		}
		queryArgs := append(append([]any{}, args...), limit)
		rows, err := db.QueryContext(r.Context(), `
			SELECT name, description, schema_json, platforms, source,
			       CAST(created_at AS TEXT), created_by
			FROM tool_registry`+where+` ORDER BY name LIMIT ?`, queryArgs...)
		if err != nil {
			http.Error(w, "failed to load Tool Registry", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items := make([]toolRegistryDatabaseViewerItem, 0, limit)
		for rows.Next() {
			var item toolRegistryDatabaseViewerItem
			var platformsJSON string
			if err := rows.Scan(&item.Name, &item.Description, &item.SchemaJSON, &platformsJSON, &item.Source, &item.CreatedAt, &item.CreatedBy); err != nil {
				http.Error(w, "failed to scan Tool Registry", http.StatusInternalServerError)
				return
			}
			if err := json.Unmarshal([]byte(platformsJSON), &item.Platforms); err != nil {
				item.Platforms = []string{}
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read Tool Registry", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: true, DBPath: path, Total: total, Items: items})
	}
}

func openViewerSQLiteReadOnly(w http.ResponseWriter, configuredPath string) (*sql.DB, string, bool) {
	path := strings.TrimSpace(configuredPath)
	if path == "" {
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: false, Total: 0, Items: []any{}, Error: "database is not configured"})
		return nil, "", false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: false, DBPath: path, Total: 0, Items: []any{}, Error: "database path is invalid"})
		return nil, path, false
	}
	info, err := os.Stat(absPath)
	if err != nil {
		message := "database not found"
		if !os.IsNotExist(err) {
			message = "database stat failed"
		}
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: false, DBPath: absPath, Total: 0, Items: []any{}, Error: message})
		return nil, absPath, false
	}
	if info.IsDir() {
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: false, DBPath: absPath, Total: 0, Items: []any{}, Error: "database path is a directory"})
		return nil, absPath, false
	}
	dsnURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}
	query := dsnURL.Query()
	query.Set("mode", "ro")
	query.Set("_time_format", "sqlite")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: false, DBPath: absPath, Total: 0, Items: []any{}, Error: "database open failed"})
		return nil, absPath, false
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: false, DBPath: absPath, Total: 0, Items: []any{}, Error: fmt.Sprintf("database ping failed: %v", err)})
		return nil, absPath, false
	}
	return db, absPath, true
}
