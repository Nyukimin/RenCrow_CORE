package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domainstore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
)

func TestBuildDurableStoreRuntimeFromCanonicalManifest(t *testing.T) {
	cfg := &config.Config{DurableStore: config.DurableStoreConfig{Enabled: true, ManifestPath: filepath.Join("..", "..", "config", "durable-stores.json")}, Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{DurableStoreWorkflow: filepath.Join(t.TempDir(), "workflow.db")}}}
	workflow, closer, err := buildDurableStoreRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	result, handled, err := workflow.Handle(context.Background(), appstore.Input{RequestID: "req-runtime", RequestedBy: "ren", Message: "XのBookmarkを保存するDBの設計を確認して"})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if result.Status != domainstore.StatusCompleted || result.Classification.StoreID != "core.conversation_l1" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBuildDurableStoreRuntimeRejectsUnknownManifestFields(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join("..", "..", "config", "durable-stores.json"))
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(canonical), `"module_id":`, `"unexpected": true, "module_id":`, 1)
	if _, err := decodeDurableStoreManifest([]byte(withUnknown)); err == nil {
		t.Fatal("unknown manifest field must be rejected")
	}
}
