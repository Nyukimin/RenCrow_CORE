package aiworkflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
)

type memoryProjectInitStore struct {
	events  []domainai.WorkflowEvent
	indexes []domainai.ProjectMemoryIndex
}

func (s *memoryProjectInitStore) SaveWorkflowEvent(_ context.Context, item domainai.WorkflowEvent) error {
	s.events = append(s.events, item)
	return nil
}

func (s *memoryProjectInitStore) SaveProjectMemoryIndex(_ context.Context, item domainai.ProjectMemoryIndex) error {
	s.indexes = append(s.indexes, item)
	return nil
}

func TestProjectScannerGeneratesInitPackAndIndexes(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test\n")
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "# rules\n")
	if err := os.Mkdir(filepath.Join(root, "cmd"), 0755); err != nil {
		t.Fatal(err)
	}
	store := &memoryProjectInitStore{}
	scanner := NewProjectScanner(store)
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	result, err := scanner.Run(context.Background(), ProjectInitOptions{
		RepoRoot:          root,
		ProjectMemoryRoot: ".ai",
		RepoName:          "example",
		Now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(result.GeneratedFiles) != 6 {
		t.Fatalf("expected 6 generated files, got %+v", result.GeneratedFiles)
	}
	if len(store.indexes) != 6 {
		t.Fatalf("expected 6 project memory indexes, got %d", len(store.indexes))
	}
	if len(store.events) != 1 || store.events[0].EventType != "project_init_completed" {
		t.Fatalf("unexpected workflow events: %+v", store.events)
	}
	assertFileContains(t, filepath.Join(root, ".ai", "project_profile.md"), "Repository: example")
	assertFileContains(t, filepath.Join(root, ".ai", "test_commands.md"), "go test ./...")
	assertFileContains(t, filepath.Join(root, ".ai", "source_map.md"), "cmd/")
}

func TestProjectScannerRejectsUnsafeProjectMemoryRoot(t *testing.T) {
	root := t.TempDir()
	scanner := NewProjectScanner(nil)

	if _, err := scanner.Run(context.Background(), ProjectInitOptions{
		RepoRoot:          root,
		ProjectMemoryRoot: "../outside",
	}); err == nil {
		t.Fatal("expected project_memory_root traversal to fail")
	}
}

func TestProjectScannerRejectsBroadRoot(t *testing.T) {
	scanner := NewProjectScanner(nil)

	if _, err := scanner.Run(context.Background(), ProjectInitOptions{RepoRoot: "/"}); err == nil {
		t.Fatal("expected broad root to fail")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), want) {
		t.Fatalf("%s does not contain %q:\n%s", path, want, body)
	}
}
