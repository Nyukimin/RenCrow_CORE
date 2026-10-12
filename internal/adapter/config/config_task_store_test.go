package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// task_store.index.enabled selects the in-memory ID index of the canonical Task
// store. It is off unless the operator turns it on: the index changes how the
// store reads, so enabling it is a deployment decision.
func TestTaskStoreIndexIsDisabledByDefault(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Port: 8080}}
	cfg.setDefaults()
	if cfg.TaskStore.Index.Enabled {
		t.Fatal("task_store.index.enabled must default to false")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
}

func TestTaskStoreIndexEnabledLoadsFromYAML(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		yaml string
		want bool
	}{
		{"absent", "server:\n  port: 8080\n", false},
		{"explicit false", "server:\n  port: 8080\ntask_store:\n  index:\n    enabled: false\n", false},
		{"enabled", "server:\n  port: 8080\ntask_store:\n  index:\n    enabled: true\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")+".yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.TaskStore.Index.Enabled != tc.want {
				t.Fatalf("task_store.index.enabled = %v, want %v", cfg.TaskStore.Index.Enabled, tc.want)
			}
		})
	}
}

func TestConfigExampleKeepsTaskStoreIndexDisabled(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "..", "config", "config.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskStore.Index.Enabled || cfg.TaskStore.Index.Persist {
		t.Fatal("config.yaml.example must not enable the Task store index or its sidecar")
	}
}

// task_store.index.persist keeps the index in a sidecar file so the next start
// restores it instead of reading the whole log. It is off unless configured and
// only means something on top of the index.
func TestTaskStoreIndexPersistIsDisabledByDefaultAndRequiresTheIndex(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Port: 8080}}
	cfg.setDefaults()
	if cfg.TaskStore.Index.Persist {
		t.Fatal("task_store.index.persist must default to false")
	}

	cfg.TaskStore.Index.Persist = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "task_store.index.persist requires task_store.index.enabled") {
		t.Fatalf("persist without the index: Validate error = %v, want a rejection naming both keys", err)
	}

	cfg.TaskStore.Index.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("persist with the index rejected: %v", err)
	}
}

func TestTaskStoreIndexPersistLoadsFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.yaml")
	yaml := "server:\n  port: 8080\ntask_store:\n  index:\n    enabled: true\n    persist: true\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.TaskStore.Index.Enabled || !cfg.TaskStore.Index.Persist {
		t.Fatalf("task_store.index = %+v, want enabled and persist", cfg.TaskStore.Index)
	}

	bad := filepath.Join(t.TempDir(), "persist-only.yaml")
	if err := os.WriteFile(bad, []byte("server:\n  port: 8080\ntask_store:\n  index:\n    persist: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bad); err == nil || !strings.Contains(err.Error(), "task_store.index.persist requires task_store.index.enabled") {
		t.Fatalf("persist without enabled must not load: err = %v", err)
	}
}

// task_store.rotation.enabled moves the store to daily segments. It is off unless
// the operator turns it on, and it only means something on top of the persisted
// index.
func TestTaskStoreRotationIsDisabledByDefaultAndRequiresThePersistedIndex(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Port: 8080}}
	cfg.setDefaults()
	if cfg.TaskStore.Rotation.Enabled || cfg.TaskStore.Rotation.SizeValveBytes != 0 {
		t.Fatalf("task_store.rotation defaults = %+v, want off with the default valve", cfg.TaskStore.Rotation)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}

	const want = "task_store.rotation.enabled requires task_store.index.enabled=true and task_store.index.persist=true"
	cfg.TaskStore.Rotation.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("rotation without the index: Validate error = %v, want %q", err, want)
	}
	cfg.TaskStore.Index.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("rotation without persist: Validate error = %v, want %q", err, want)
	}
	cfg.TaskStore.Index.Persist = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rotation with the persisted index rejected: %v", err)
	}
}

func TestTaskStoreRotationSizeValveIsBounded(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Port: 8080}}
	cfg.setDefaults()
	for _, tc := range []struct {
		valve int64
		ok    bool
	}{{0, true}, {MinTaskStoreRotationSizeValve, true}, {8 << 20, true}, {MinTaskStoreRotationSizeValve - 1, false}, {-1, false}, {1, false}} {
		cfg.TaskStore.Rotation.SizeValveBytes = tc.valve
		err := cfg.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("size_valve_bytes=%d: Validate error = %v, want ok=%v", tc.valve, err, tc.ok)
		}
	}
}

func TestTaskStoreRotationLoadsFromYAMLAndTheExampleKeepsItOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotation.yaml")
	yaml := "server:\n  port: 8080\ntask_store:\n  index:\n    enabled: true\n    persist: true\n  rotation:\n    enabled: true\n    size_valve_bytes: 4194304\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.TaskStore.Rotation.Enabled || cfg.TaskStore.Rotation.SizeValveBytes != 4<<20 {
		t.Fatalf("task_store.rotation = %+v", cfg.TaskStore.Rotation)
	}
	example, err := LoadConfig(filepath.Join("..", "..", "..", "config", "config.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if example.TaskStore.Rotation.Enabled {
		t.Fatal("config.yaml.example must not enable Task store rotation")
	}
}
