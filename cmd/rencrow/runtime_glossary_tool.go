package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	_ "modernc.org/sqlite"
)

type runtimeGlossaryLookup struct{ dbPath string }

func prepareRuntimeGlossaryLookup(ctx context.Context, path string) (*runtimeGlossaryLookup, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("glossary database is not configured")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("glossary path invalid")
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("glossary database unavailable")
	}
	db, err := openRuntimeGlossaryReadOnly(abs)
	if err != nil {
		return nil, fmt.Errorf("glossary database unavailable")
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("glossary database unavailable")
	}
	if err := glossaryapp.ValidateIndexedSchema(ctx, db); err != nil {
		return nil, fmt.Errorf("glossary indexed lookup unavailable")
	}
	return &runtimeGlossaryLookup{dbPath: abs}, nil
}
func (l *runtimeGlossaryLookup) Lookup(ctx context.Context, operation, term, category string, limit int) (any, error) {
	if l == nil || l.dbPath == "" {
		return nil, fmt.Errorf("glossary unavailable")
	}
	db, err := openRuntimeGlossaryReadOnly(l.dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	return glossaryapp.Lookup(ctx, db, glossaryapp.LookupRequest{Operation: operation, Term: term, Category: category, Limit: limit})
}
func openRuntimeGlossaryReadOnly(path string) (*sql.DB, error) {
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_time_format", "sqlite")
	u.RawQuery = q.Encode()
	return sql.Open("sqlite", u.String())
}
