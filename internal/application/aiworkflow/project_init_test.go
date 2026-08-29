package aiworkflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type memoryProjectInitStore struct {
	events  []modulecore.EventEnvelope
	indexes []domainai.ProjectMemoryIndex
}

func (s *memoryProjectInitStore) Append(_ context.Context, item modulecore.EventEnvelope) error {
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
	if len(store.events) != 1 || store.events[0].EventType != "project_init.completed" {
		t.Fatalf("unexpected workflow events: %+v", store.events)
	}
	assertFileContains(t, filepath.Join(root, ".ai", "project_profile.md"), "Repository: example")
	assertFileContains(t, filepath.Join(root, ".ai", "test_commands.md"), "go test ./...")
	assertFileContains(t, filepath.Join(root, ".ai", "source_map.md"), "cmd/")
}

// TestProjectScannerPersistsOSIndependentIdentifiers は永続化される ID と
// FilePath が OS に依存しないことを確認する
//
// ProjectMemoryIndex は json タグ付きで JSONL 永続化されるため、
// filepath.Rel の戻り値をそのまま使うと Windows では ".ai\project_profile.md"、
// Linux では ".ai/project_profile.md" となり、同じ生成物の ID が OS ごとに
// 分裂して重複レコードになる。
func TestProjectScannerPersistsOSIndependentIdentifiers(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test\n")
	store := &memoryProjectInitStore{}
	scanner := NewProjectScanner(store)

	result, err := scanner.Run(context.Background(), ProjectInitOptions{
		RepoRoot:          root,
		ProjectMemoryRoot: ".ai",
		RepoName:          "example",
		Now:               func() time.Time { return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	for _, idx := range store.indexes {
		if strings.Contains(idx.ID, "\\") {
			t.Errorf("ProjectMemoryIndex.ID contains a backslash: %q", idx.ID)
		}
		if strings.Contains(idx.FilePath, "\\") {
			t.Errorf("ProjectMemoryIndex.FilePath contains a backslash: %q", idx.FilePath)
		}
		if want := "project_init:" + idx.FilePath; idx.ID != want {
			t.Errorf("ID = %q, want %q", idx.ID, want)
		}
	}
	for _, generated := range result.GeneratedFiles {
		if strings.Contains(generated, "\\") {
			t.Errorf("GeneratedFiles entry contains a backslash: %q", generated)
		}
	}
	if len(store.indexes) == 0 {
		t.Fatal("expected project memory indexes")
	}
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
