package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
)

func TestBuildSessionRuntimeUsesOperationMemoryDir(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Session:            config.SessionConfig{StorageDir: filepath.Join(dir, "sessions")},
		WorkspaceDir:       filepath.Join(dir, "workspace"),
		OperationMemoryDir: filepath.Join(dir, "rencrow", "memory"),
	}

	runtime := buildSessionRuntime(cfg)

	if err := runtime.MemoryStore.WriteLongTerm("operation memory"); err != nil {
		t.Fatalf("WriteLongTerm: %v", err)
	}
	if got := runtime.MemoryStore.ReadLongTerm(); got != "operation memory" {
		t.Fatalf("unexpected memory content: %q", got)
	}
	wantFile := filepath.Join(cfg.OperationMemoryDir, "MEMORY.md")
	if !fileExists(wantFile) {
		t.Fatalf("operation memory file was not created at %s", wantFile)
	}
	oldWorkspaceFile := filepath.Join(cfg.WorkspaceDir, "memory", "MEMORY.md")
	if fileExists(oldWorkspaceFile) {
		t.Fatalf("operation memory should not be written under workspace: %s", oldWorkspaceFile)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
