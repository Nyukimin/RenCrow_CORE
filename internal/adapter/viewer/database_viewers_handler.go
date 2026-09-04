package viewer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/datacapability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

// DatabaseViewerOptions identifies one configured SQLite database exposed by a
// read-only Viewer. Handlers never create, migrate, or modify the configured DB.
type DatabaseViewerOptions struct {
	DBPath       string
	RuntimeTools func(context.Context) ([]domaintool.ToolMetadata, error)
}

type DataCapabilityCatalogProvider func(context.Context) ([]datacapability.Entry, error)

type dataCapabilitySummary struct {
	Available   int `json:"available"`
	Unavailable int `json:"unavailable"`
	Restricted  int `json:"restricted"`
	Blocked     int `json:"blocked"`
}

type dataCapabilityCatalogResponse struct {
	Available bool                   `json:"available"`
	Total     int                    `json:"total"`
	Summary   dataCapabilitySummary  `json:"summary"`
	Items     []datacapability.Entry `json:"items"`
	Error     string                 `json:"error,omitempty"`
}

func HandleDataCapabilityCatalog(provider DataCapabilityCatalogProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		if provider == nil {
			writeJSON(w, http.StatusOK, dataCapabilityCatalogResponse{Items: []datacapability.Entry{}, Error: "data capability catalog is unavailable"})
			return
		}
		items, err := provider(r.Context())
		if err != nil {
			writeJSON(w, http.StatusOK, dataCapabilityCatalogResponse{Items: []datacapability.Entry{}, Error: "data capability catalog is unavailable"})
			return
		}
		if items == nil {
			items = []datacapability.Entry{}
		}
		summary := dataCapabilitySummary{}
		for _, item := range items {
			switch item.Status {
			case "available":
				summary.Available++
			case "unavailable":
				summary.Unavailable++
			case "restricted":
				summary.Restricted++
			case "blocked":
				summary.Blocked++
			}
		}
		writeJSON(w, http.StatusOK, dataCapabilityCatalogResponse{Available: true, Total: len(items), Summary: summary, Items: items})
	}
}

type databaseViewerResponse struct {
	Available bool   `json:"available"`
	DBPath    string `json:"db_path,omitempty"`
	Total     int    `json:"total"`
	Items     any    `json:"items"`
	Error     string `json:"error,omitempty"`
}

type conversationArchiveViewerItem struct {
	ThreadID   modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind modulecore.ThreadKind `json:"thread_kind"`
	SessionID  string                `json:"session_id"`
	StartTime  string                `json:"start_time"`
	EndTime    string                `json:"end_time"`
	Domain     string                `json:"domain"`
	Summary    string                `json:"summary"`
	Keywords   []string              `json:"keywords"`
	IsNovel    bool                  `json:"is_novel"`
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
	Origin      string   `json:"origin"`
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
			SELECT thread_id, thread_seq, thread_kind, session_id, CAST(ts_start AS TEXT), CAST(ts_end AS TEXT),
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
			var rawThreadID string
			var rawThreadSeq int64
			var rawThreadKind string
			var keywordsJSON string
			if err := rows.Scan(&rawThreadID, &rawThreadSeq, &rawThreadKind, &item.SessionID, &item.StartTime, &item.EndTime, &item.Domain, &item.Summary, &keywordsJSON, &item.IsNovel); err != nil {
				http.Error(w, "failed to scan conversation archive", http.StatusInternalServerError)
				return
			}
			item.ThreadID = modulecore.ThreadID(rawThreadID)
			item.ThreadSeq = modulecore.ThreadSeq(rawThreadSeq)
			item.ThreadKind = modulecore.ThreadKind(rawThreadKind)
			if err := validateViewerThreadTuple(item.ThreadID, item.ThreadSeq, item.ThreadKind, false); err != nil {
				http.Error(w, "invalid conversation archive thread tuple: "+err.Error(), http.StatusInternalServerError)
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
		platform := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("platform")))
		itemsByName := map[string]toolRegistryDatabaseViewerItem{}
		partialErrors := make([]string, 0, 2)
		if opts.RuntimeTools != nil {
			metas, runtimeErr := opts.RuntimeTools(r.Context())
			if runtimeErr != nil {
				partialErrors = append(partialErrors, "runtime tools unavailable")
			} else {
				for _, meta := range metas {
					if platform != "" && platform != runtime.GOOS {
						continue
					}
					name := strings.TrimSpace(meta.ToolID)
					if name == "" {
						continue
					}
					origin := strings.TrimSpace(meta.Origin)
					if origin == "" {
						origin = domaintool.OriginCoreRuntime
					}
					schemaJSON := "{}"
					if len(meta.Parameters) > 0 {
						if encoded, err := json.Marshal(meta.Parameters); err == nil {
							schemaJSON = string(encoded)
						}
					}
					itemsByName[name] = toolRegistryDatabaseViewerItem{Name: name, Description: meta.Description, SchemaJSON: schemaJSON, Platforms: []string{runtime.GOOS}, Source: "runtime", Origin: origin}
				}
			}
		}

		path := ""
		databaseAvailable := false
		if strings.TrimSpace(opts.DBPath) != "" {
			db, resolvedPath, openErr := openViewerSQLiteReadOnlyResult(opts.DBPath)
			path = resolvedPath
			if openErr != nil {
				partialErrors = append(partialErrors, openErr.Error())
			} else {
				databaseAvailable = true
				defer db.Close()
				where := ""
				args := make([]any, 0, 2)
				if platform != "" {
					where = " WHERE platforms LIKE ?"
					args = append(args, "%\""+platform+"\"%")
				}
				rows, queryErr := db.QueryContext(r.Context(), `
			SELECT name, description, schema_json, platforms, source,
			       CAST(created_at AS TEXT), created_by
			FROM tool_registry`+where+` ORDER BY name LIMIT 300`, args...)
				if queryErr != nil {
					partialErrors = append(partialErrors, "failed to load Tool Registry")
				} else {
					for rows.Next() {
						var item toolRegistryDatabaseViewerItem
						var platformsJSON string
						if err := rows.Scan(&item.Name, &item.Description, &item.SchemaJSON, &platformsJSON, &item.Source, &item.CreatedAt, &item.CreatedBy); err != nil {
							partialErrors = append(partialErrors, "failed to scan Tool Registry")
							break
						}
						if _, exists := itemsByName[item.Name]; exists {
							continue
						}
						if err := json.Unmarshal([]byte(platformsJSON), &item.Platforms); err != nil {
							item.Platforms = []string{}
						}
						item.Origin = domaintool.OriginDynamicRegistry
						itemsByName[item.Name] = item
					}
					if err := rows.Err(); err != nil {
						partialErrors = append(partialErrors, "failed to read Tool Registry")
					}
					rows.Close()
				}
			}
		}

		items := make([]toolRegistryDatabaseViewerItem, 0, len(itemsByName))
		for _, item := range itemsByName {
			items = append(items, item)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		total := len(items)
		if len(items) > limit {
			items = items[:limit]
		}
		available := total > 0 || databaseAvailable
		errorMessage := strings.Join(partialErrors, "; ")
		if !available && errorMessage == "" {
			errorMessage = "database is not configured"
		}
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: available, DBPath: path, Total: total, Items: items, Error: errorMessage})
	}
}

func openViewerSQLiteReadOnlyResult(configuredPath string) (*sql.DB, string, error) {
	path := strings.TrimSpace(configuredPath)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, path, fmt.Errorf("database path is invalid")
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, absPath, fmt.Errorf("database not found")
		}
		return nil, absPath, fmt.Errorf("database stat failed")
	}
	if info.IsDir() {
		return nil, absPath, fmt.Errorf("database path is a directory")
	}
	dsnURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}
	query := dsnURL.Query()
	query.Set("mode", "ro")
	query.Set("_time_format", "sqlite")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, absPath, fmt.Errorf("database open failed")
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, absPath, fmt.Errorf("database ping failed: %v", err)
	}
	return db, absPath, nil
}

func openViewerSQLiteReadOnly(w http.ResponseWriter, configuredPath string) (*sql.DB, string, bool) {
	path := strings.TrimSpace(configuredPath)
	if path == "" {
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: false, Total: 0, Items: []any{}, Error: "database is not configured"})
		return nil, "", false
	}
	db, resolvedPath, err := openViewerSQLiteReadOnlyResult(path)
	if err != nil {
		writeJSON(w, http.StatusOK, databaseViewerResponse{Available: false, DBPath: resolvedPath, Total: 0, Items: []any{}, Error: err.Error()})
		return nil, resolvedPath, false
	}
	return db, resolvedPath, true
}
