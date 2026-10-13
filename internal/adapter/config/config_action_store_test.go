package config

import (
	"os"
	"path/filepath"
	"testing"
)

// action_store.index.enabled selects the in-memory snapshot of the Action store.
// It is off unless the operator turns it on: the snapshot changes how the store
// reads, so enabling it is a deployment decision.
func TestActionStoreIndexIsDisabledByDefault(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Port: 8080}}
	cfg.setDefaults()
	if cfg.ActionStore.Index.Enabled {
		t.Fatal("action_store.index.enabled must default to false")
	}
	if cfg.ActionStore.VerifyRunReferencesAtStart {
		t.Fatal("action_store.verify_run_references_at_start must default to false")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
}

func TestActionStoreSettingsLoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name   string
		yaml   string
		index  bool
		verify bool
	}{
		{"absent", "server:\n  port: 8080\n", false, false},
		{"explicit false", "server:\n  port: 8080\naction_store:\n  index:\n    enabled: false\n", false, false},
		{"index enabled", "server:\n  port: 8080\naction_store:\n  index:\n    enabled: true\n", true, false},
		{"verify at start", "server:\n  port: 8080\naction_store:\n  verify_run_references_at_start: true\n", false, true},
		{"both", "server:\n  port: 8080\naction_store:\n  index:\n    enabled: true\n  verify_run_references_at_start: true\n", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, filepath.Base(tc.name)+".yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.ActionStore.Index.Enabled != tc.index || cfg.ActionStore.VerifyRunReferencesAtStart != tc.verify {
				t.Fatalf("action_store = %+v, want index=%v verify=%v", cfg.ActionStore, tc.index, tc.verify)
			}
		})
	}
}

func TestConfigExampleKeepsTheActionStoreSnapshotDisabled(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "..", "config", "config.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActionStore.Index.Enabled || cfg.ActionStore.VerifyRunReferencesAtStart {
		t.Fatal("config.yaml.example must not enable the Action store snapshot or the startup reference check")
	}
}
