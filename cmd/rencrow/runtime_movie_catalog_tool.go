package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	_ "modernc.org/sqlite"
)

type runtimeMovieCatalogLookup struct {
	dbPath string
}

// prepareRuntimeMovieCatalogLookup is the sole schema-migration boundary for
// the runtime Tool. Missing or unmigratable optional DBs return no capability.
func prepareRuntimeMovieCatalogLookup(ctx context.Context, configuredPath string) (*runtimeMovieCatalogLookup, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return nil, fmt.Errorf("movie catalog database path is not configured")
	}
	absPath, err := filepath.Abs(configuredPath)
	if err != nil {
		return nil, fmt.Errorf("resolve movie catalog database path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat movie catalog database: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("movie catalog database path is a directory")
	}
	db, err := sql.Open("sqlite", "file:"+absPath+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open movie catalog database for indexed migration: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect movie catalog database for indexed migration: %w", err)
	}
	if err := moviecatalogapp.EnsureIndexedLookupSchema(ctx, db); err != nil {
		return nil, fmt.Errorf("prepare movie catalog indexed lookup: %w", err)
	}
	return &runtimeMovieCatalogLookup{dbPath: absPath}, nil
}

func (l *runtimeMovieCatalogLookup) Lookup(ctx context.Context, kind string, name string, information string, limit int) (any, error) {
	if l == nil || strings.TrimSpace(l.dbPath) == "" {
		return nil, fmt.Errorf("movie catalog lookup is unavailable")
	}
	db, err := openRuntimeMovieCatalogReadOnly(l.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open movie catalog read-only: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect movie catalog read-only: %w", err)
	}
	return moviecatalogapp.Lookup(db, moviecatalogapp.LookupRequest{Kind: kind, Name: name, Information: information, Limit: limit})
}

func openRuntimeMovieCatalogReadOnly(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_time_format=sqlite")
}
