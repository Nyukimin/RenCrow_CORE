package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
	executionpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/execution"
)

func TestLoadEvidenceStoreAndList(t *testing.T) {
	b := &testConfigBuilder{t: t}
	cfg := b.write()
	store, err := loadEvidenceStore(cfg)
	if err != nil {
		t.Fatalf("loadEvidenceStore failed: %v", err)
	}

	item := domainexecution.ExecutionReport{
		JobID:      "job-test-1",
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}
	if err := store.Save(t.Context(), item); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	items, err := store.ListRecent(t.Context(), 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one evidence item")
	}
}

func TestLoadEvidenceStoreRequiresAuditPath(t *testing.T) {
	td := t.TempDir()
	cfgPath := filepath.Join(td, "config.yaml")
	content := `server:
  host: "127.0.0.1"
  port: 18080
workspace_dir: "./workspace"
session:
  storage_dir: "./sessions"
line:
  channel_secret: ""
  access_token: ""
ollama:
  base_url: "http://127.0.0.1:11434"
  model: "qwen2.5:7b"
security:
  enabled: true
  audit:
    enabled: true
    backend: "jsonl"
    path: ""
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := executionpersistence.NewJSONLReportStore("")
	if err == nil {
		t.Fatal("expected NewJSONLReportStore error for empty path")
	}
}

type testConfigBuilder struct {
	t *testing.T
}

func (b *testConfigBuilder) write() string {
	b.t.Helper()
	td := b.t.TempDir()
	cfgPath := filepath.Join(td, "config.yaml")
	auditPath := filepath.Join(td, "execution_report.jsonl")
	content := "server:\n  host: \"127.0.0.1\"\n  port: 18080\nworkspace_dir: \"./workspace\"\nsession:\n  storage_dir: \"./sessions\"\nline:\n  channel_secret: \"\"\n  access_token: \"\"\nollama:\n  base_url: \"http://127.0.0.1:11434\"\n  model: \"qwen2.5:7b\"\nsecurity:\n  enabled: true\n  audit:\n    enabled: true\n    backend: \"jsonl\"\n    path: \"" + auditPath + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		b.t.Fatalf("write config failed: %v", err)
	}
	b.t.Setenv("PICOCLAW_CONFIG", cfgPath)
	return cfgPath
}
