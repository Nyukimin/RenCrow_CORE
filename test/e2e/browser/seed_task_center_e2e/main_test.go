package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIsolatedMovieLookupReadsSeededCatalogRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie_catalog.sqlite")
	if err := initializeMovieCatalog(path); err != nil {
		t.Fatal(err)
	}
	value, err := (isolatedMovieLookup{path: path}).Lookup(context.Background(), "movie", "Task Center E2E Film", "overview", 1)
	if err != nil {
		t.Fatalf("existing indexed MovieCatalog owner lookup: %v", err)
	}
	if value == nil {
		t.Fatal("MovieCatalog owner returned a nil result")
	}
}

func TestConfiguredTaskCenterUserUsesRuntimeOwnerFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-agent-ops.user-id")
	if err := os.WriteFile(path, []byte("registered-e2e-user"), 0600); err != nil {
		t.Fatal(err)
	}
	userID, err := configuredTaskCenterUser(path)
	if err != nil || userID != "registered-e2e-user" {
		t.Fatalf("configured user=%q err=%v", userID, err)
	}
}
