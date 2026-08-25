package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
			ConversationArchive: "/state/archive.db",
			Glossary:            "/state/glossary.db",
			ToolRegistry:        "/state/tools.db",
			MovieCatalog:        "/state/movie.sqlite",
			HobbyGraph:          "/state/hobby.sqlite",
		}},
	}

	got := viewerDatabasePaths(cfg)
	if got.ConversationArchive != "/state/archive.db" ||
		got.Glossary != "/state/glossary.db" ||
		got.ToolRegistry != "/state/tools.db" ||
		got.MovieCatalog != "/state/movie.sqlite" ||
		got.HobbyGraph != "/state/hobby.sqlite" {
		t.Fatalf("viewer database paths = %+v", got)
	}
}

func TestViewerRouteRegistrationUsesTradeOwnerForInvestment(t *testing.T) {
	source, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}
	routes := string(source)
	if !strings.Contains(routes, "viewerTradeStatus") {
		t.Fatal("Viewer route registration must retain the canonical Trade owner status route")
	}
	for _, forbidden := range []string{
		"HandleInvestmentStatus",
		"HandleInvestmentNotify",
		"RENCROW_DATA_DB",
	} {
		if strings.Contains(routes, forbidden) {
			t.Fatalf("routes.go still contains retired local investment wiring %q", forbidden)
		}
	}
}

func TestRetiredCoreInvestmentSystemIsAbsent(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, relativePath := range []string{
		"rencrow-data",
		"scripts/rencrow_data_scheduler.sh",
		"systemd/user/rencrow-data-daily.service",
		"systemd/user/rencrow-data-daily.timer",
		"systemd/user/rencrow-data-weekly.service",
		"systemd/user/rencrow-data-weekly.timer",
	} {
		if _, err := os.Stat(filepath.Join(root, relativePath)); !os.IsNotExist(err) {
			t.Fatalf("retired CORE investment artifact still exists: %s", relativePath)
		}
	}

	for _, relativePath := range []string{
		"Makefile",
		"config/config.yaml.example",
		"internal/adapter/config/config_types.go",
		"internal/adapter/config/storage_config.go",
		"scripts/test-local.plan.json",
	} {
		content, err := os.ReadFile(filepath.Join(root, relativePath))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"rencrow-data", "storage.databases.investment", `yaml:"investment"`} {
			if bytes.Contains(content, []byte(forbidden)) {
				t.Fatalf("%s still contains retired CORE investment contract %q", relativePath, forbidden)
			}
		}
	}
}
