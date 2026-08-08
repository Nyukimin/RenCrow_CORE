package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDurableStoreWorkflowConfigRequiresExplicitAbsoluteSQLitePath(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Port: 8080}, DurableStore: DurableStoreConfig{Enabled: true, ManifestPath: "config/durable-stores.json"}, Storage: StorageConfig{Databases: DatabasePathsConfig{DurableStoreWorkflow: "relative/workflow.db"}}}
	cfg.setDefaults()
	cfg.populateCanonicalStoragePaths()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "storage.databases.durable_store_workflow must be an absolute path") {
		t.Fatalf("Validate error=%v", err)
	}
	cfg.Storage.Databases.DurableStoreWorkflow = filepath.Join(t.TempDir(), "workflow.db")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
