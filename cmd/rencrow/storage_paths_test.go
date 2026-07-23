package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestResolveMovieCatalogBackfillDBPathUsesConfiguredPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "eiga_catalog.sqlite")
	if err := os.WriteFile(dbPath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("RENCROW_MOVIE_CATALOG_DB", filepath.Join(t.TempDir(), "environment.sqlite"))

	if got := resolveMovieCatalogBackfillDBPath(dbPath); got != dbPath {
		t.Fatalf("resolved path = %q, want %q", got, dbPath)
	}
}

func TestConfiguredViewerDatabasePaths(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{
			MovieCatalog: "/state/movie.sqlite",
			HobbyGraph:   "/state/hobby.sqlite",
			Investment:   "/state/investment.db",
		}},
	}

	got := viewerDatabasePaths(cfg)
	if got.MovieCatalog != "/state/movie.sqlite" ||
		got.HobbyGraph != "/state/hobby.sqlite" ||
		got.Investment != "/state/investment.db" {
		t.Fatalf("viewer database paths = %+v", got)
	}
}
