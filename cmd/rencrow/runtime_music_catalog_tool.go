package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	musiccatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/musiccatalog"
	_ "modernc.org/sqlite"
)

type runtimeMusicCatalogLookup struct{ dbPath string }

func prepareRuntimeMusicCatalogLookup(ctx context.Context, configuredPath string) (*runtimeMusicCatalogLookup, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return nil, fmt.Errorf("hobby graph database path is not configured")
	}
	absPath, err := filepath.Abs(configuredPath)
	if err != nil {
		return nil, fmt.Errorf("resolve hobby graph database path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat hobby graph database: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("hobby graph database path is a directory")
	}
	db, err := sql.Open("sqlite", "file:"+absPath+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open music catalog for indexed migration: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect music catalog read-only: %w", err)
	}
	if err := musiccatalogapp.EnsureIndexedLookupSchema(ctx, db); err != nil {
		return nil, fmt.Errorf("prepare music catalog indexed schema: %w", err)
	}
	return &runtimeMusicCatalogLookup{dbPath: absPath}, nil
}

func (l *runtimeMusicCatalogLookup) LookupMusic(ctx context.Context, kind string, name string, artist string, limit int) (any, error) {
	db, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return musiccatalogapp.LookupCatalog(ctx, db, musiccatalogapp.CatalogRequest{Kind: kind, Name: name, Artist: artist, Limit: limit})
}

func (l *runtimeMusicCatalogLookup) LookupLyrics(ctx context.Context, song string, artist string, language string, information string, limit int) (any, error) {
	db, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return musiccatalogapp.LookupLyrics(ctx, db, musiccatalogapp.LyricsRequest{Song: song, Artist: artist, Language: language, Information: information, Limit: limit})
}

func (l *runtimeMusicCatalogLookup) open(ctx context.Context) (*sql.DB, error) {
	if l == nil || strings.TrimSpace(l.dbPath) == "" {
		return nil, fmt.Errorf("music catalog lookup is unavailable")
	}
	db, err := openRuntimeMusicCatalogReadOnly(l.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open music catalog read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect music catalog read-only: %w", err)
	}
	return db, nil
}

func openRuntimeMusicCatalogReadOnly(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_time_format=sqlite")
}
