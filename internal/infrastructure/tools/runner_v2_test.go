package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/toolharness"
)

func TestToolRunner_ExecuteV2_Shell_Success(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	resp, err := runner.ExecuteV2(context.Background(), "shell", map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got error: %s", resp.Error.Message)
	}
	if resp.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should be set")
	}
	if resp.String() == "" {
		t.Error("result should not be empty")
	}
}

func TestToolRunner_ExecuteV2_Shell_ValidationError(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	// Empty command should fail validation with VALIDATION_FAILED
	resp, err := runner.ExecuteV2(context.Background(), "shell", map[string]any{
		"command": "",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !resp.IsError() {
		t.Fatal("expected error for empty command")
	}
	if resp.Error.Code != tool.ErrValidationFailed {
		t.Errorf("error code = %s, want VALIDATION_FAILED", resp.Error.Code)
	}
}

func TestToolRunner_ExecuteV2_FileRead_Success(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello v2"), 0644)

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"path": testFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got error: %s", resp.Error.Message)
	}
	if resp.String() != "hello v2" {
		t.Errorf("result = %q, want %q", resp.String(), "hello v2")
	}
}

func TestToolRunner_ExecuteV2_FileRead_MediatesPlaceholderArgs(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello mediated"), 0644)

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"args": map[string]any{"path": testFile},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got error: %s", resp.Error.Message)
	}
	if resp.String() != "hello mediated" {
		t.Errorf("result = %q", resp.String())
	}
	if resp.Metadata["tool_harness_status"] != "repaired" {
		t.Fatalf("expected repaired metadata, got: %#v", resp.Metadata)
	}
}

func TestToolRunner_ExecuteV2_RecordsMediationEvent(t *testing.T) {
	recorder := &mockToolHarnessRecorder{}
	runner := NewToolRunner(ToolRunnerConfig{ToolHarnessRecorder: recorder})

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello event"), 0644)

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"args": map[string]any{"path": testFile},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got error: %s", resp.Error.Message)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("events = %d, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.ToolName != "file_read" {
		t.Fatalf("tool = %s", event.ToolName)
	}
	if event.ValidationStatus != toolharness.ValidationStatusRepaired {
		t.Fatalf("status = %s", event.ValidationStatus)
	}
	if event.RawInputHash == "" {
		t.Fatal("raw input hash should be set")
	}
}

func TestToolRunner_ExecuteV2_FileRead_RelationDefaultMetadata(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("a\nb\nc\n"), 0644)

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"path":  testFile,
		"limit": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got error: %s", resp.Error.Message)
	}
	if resp.Metadata["tool_harness_status"] != "repaired" {
		t.Fatalf("expected repaired metadata, got: %#v", resp.Metadata)
	}
	defaults, ok := resp.Metadata["relation_defaults_applied"].([]map[string]any)
	if !ok || len(defaults) != 1 {
		t.Fatalf("relation defaults metadata = %#v", resp.Metadata["relation_defaults_applied"])
	}
	if defaults[0]["field"] != "offset" || defaults[0]["value"] != 0 {
		t.Fatalf("unexpected relation default: %#v", defaults[0])
	}
}

func TestToolRunner_ExecuteV2_FileWrite_DoesNotRepairContentMarkdown(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "out.txt")
	content := "[click](https://example.com)"

	resp, err := runner.ExecuteV2(context.Background(), "file_write", map[string]any{
		"path":    testFile,
		"content": content,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got error: %s", resp.Error.Message)
	}
	got, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content changed: got %q want %q", string(got), content)
	}
}

func TestToolRunner_ExecuteV2_PathTraversal(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"path": "../../../etc/passwd",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !resp.IsError() {
		t.Fatal("expected error for path traversal")
	}
}

func TestToolRunner_ExecuteV2_UnknownTool(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	_, err := runner.ExecuteV2(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestToolRunner_ExecuteV2_JSON(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	resp, err := runner.ExecuteV2(context.Background(), "shell", map[string]any{
		"command": "echo ok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, err := resp.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := parsed["result"]; !ok {
		t.Error("JSON should contain 'result' field")
	}
	if _, ok := parsed["generated_at"]; !ok {
		t.Error("JSON should contain 'generated_at' field")
	}
}

func TestToolRunner_ExecuteV2_WebSearch_UsesFreshCache(t *testing.T) {
	cache := &mockWebSearchCache{
		hit: true,
		items: []GoogleSearchItem{
			{Title: "Cached V2 Result", Link: "https://example.com/v2", Snippet: "from cache"},
		},
	}
	runner := NewToolRunner(ToolRunnerConfig{
		GoogleAPIKey:         "test-api-key",
		GoogleSearchEngineID: "test-engine-id",
		WebSearchCache:       cache,
	})

	resp, err := runner.ExecuteV2(context.Background(), "web_search", map[string]any{
		"query": "RenCrow 最新仕様",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got error: %s", resp.Error.Message)
	}
	if !strings.Contains(resp.String(), "Cached V2 Result") {
		t.Fatalf("expected cached result, got: %s", resp.String())
	}
	if resp.Metadata["cache_hit"] != true {
		t.Fatalf("expected cache_hit metadata, got: %+v", resp.Metadata)
	}
}

func TestToolRunner_ListTools(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(metas) < 5 {
		t.Errorf("expected at least 5 tools, got %d", len(metas))
	}

	// Check metadata fields
	found := map[string]bool{}
	for _, m := range metas {
		found[m.ToolID] = true
		if m.Version == "" {
			t.Errorf("tool %s has empty version", m.ToolID)
		}
		if m.Category == "" {
			t.Errorf("tool %s has empty category", m.ToolID)
		}
	}

	for _, name := range []string{"shell", "file_read", "file_write", "file_list", "web_search"} {
		if !found[name] {
			t.Errorf("expected tool %s in metadata list", name)
		}
	}
}

func TestToolRunner_ListTools_MutationCategory(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, m := range metas {
		switch m.ToolID {
		case "shell", "file_write":
			if m.Category != "mutation" {
				t.Errorf("%s category = %q, want %q", m.ToolID, m.Category, "mutation")
			}
		case "file_read", "file_list", "web_search":
			if m.Category != "query" {
				t.Errorf("%s category = %q, want %q", m.ToolID, m.Category, "query")
			}
		}
	}
}

func TestToolRunner_DisableWebSearch_V2(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{DisableWebSearch: true})

	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range metas {
		if m.ToolID == "web_search" {
			t.Fatalf("web_search should be disabled, got metadata: %+v", metas)
		}
	}

	_, err = runner.ExecuteV2(context.Background(), "web_search", map[string]any{"query": "test"})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("expected unknown tool error, got: %v", err)
	}
}

func TestToolRunner_FileWrite_DryRun_NewFile(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "new_file.txt")

	result, err := runner.Execute(context.Background(), "file_write", map[string]any{
		"path":    path,
		"content": "hello\nworld\n",
		"mode":    "plan",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "[DRY-RUN]") {
		t.Error("expected [DRY-RUN] marker")
	}
	if !strings.Contains(result, "exists: false") {
		t.Error("expected exists: false for new file")
	}
	if !strings.Contains(result, "action: create") {
		t.Error("expected action: create")
	}
	// Verify file was NOT created
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should not have been created in dry-run mode")
	}
}

type mockToolHarnessRecorder struct {
	events []toolharness.Event
}

func (m *mockToolHarnessRecorder) RecordToolMediationEvent(event toolharness.Event) error {
	m.events = append(m.events, event)
	return nil
}

func TestToolRunner_FileWrite_DryRun_ExistingFile(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "existing.txt")
	os.WriteFile(path, []byte("old content"), 0644)

	result, err := runner.Execute(context.Background(), "file_write", map[string]any{
		"path":    path,
		"content": "new content",
		"mode":    "plan",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "exists: true") {
		t.Error("expected exists: true for existing file")
	}
	if !strings.Contains(result, "action: overwrite") {
		t.Error("expected action: overwrite")
	}
	// Verify file was NOT modified
	content, _ := os.ReadFile(path)
	if string(content) != "old content" {
		t.Error("file should not have been modified in dry-run mode")
	}
}

func TestToolRunner_Shell_DryRun(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	result, err := runner.Execute(context.Background(), "shell", map[string]any{
		"command": "rm -rf /",
		"mode":    "plan",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "[DRY-RUN]") {
		t.Error("expected [DRY-RUN] marker")
	}
	if !strings.Contains(result, "rm -rf /") {
		t.Error("expected command in dry-run output")
	}
}

func TestToolRunner_Shell_BlocksDangerousCommandWithoutAllowlist(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	resp, err := runner.ExecuteV2(context.Background(), "shell", map[string]any{
		"command": "rm -rf /tmp/rencrow-danger",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !resp.IsError() {
		t.Fatal("expected dangerous shell command to be blocked")
	}
	if resp.Error.Code != tool.ErrPermissionDenied {
		t.Fatalf("error code = %s, want PERMISSION_DENIED", resp.Error.Code)
	}
}

func TestToolRunner_Shell_AllowedCommands(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		AllowedShellCommands: []string{"echo", "ls", "cat"},
	})

	// Allowed command
	result, err := runner.Execute(context.Background(), "shell", map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("allowed command failed: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("result = %q, want contains 'hello'", result)
	}

	// Denied command
	_, err = runner.Execute(context.Background(), "shell", map[string]any{
		"command": "rm -rf /tmp",
	})
	if err == nil {
		t.Fatal("expected error for denied command")
	}
	if !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Errorf("error = %q, want PERMISSION_DENIED", err.Error())
	}
}

func TestToolRunner_Shell_AllowedCommands_V2_ErrorCode(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		AllowedShellCommands: []string{"echo"},
	})

	resp, err := runner.ExecuteV2(context.Background(), "shell", map[string]any{
		"command": "rm -rf /",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !resp.IsError() {
		t.Fatal("expected error")
	}
	if resp.Error.Code != tool.ErrPermissionDenied {
		t.Errorf("error code = %s, want PERMISSION_DENIED", resp.Error.Code)
	}
}

func TestToolRunner_FileWrite_AllowedWritePaths(t *testing.T) {
	tmpDir := t.TempDir()
	allowed := filepath.Join(tmpDir, "CHAT_PERSONA.md")
	denied := filepath.Join(tmpDir, "OTHER.md")

	runner := NewToolRunner(ToolRunnerConfig{
		AllowedWritePaths: []string{allowed},
	})

	// Allowed path
	if _, err := runner.Execute(context.Background(), "file_write", map[string]any{
		"path":    allowed,
		"content": "ok",
	}); err != nil {
		t.Fatalf("allowed path should succeed: %v", err)
	}

	// Denied path
	_, err := runner.Execute(context.Background(), "file_write", map[string]any{
		"path":    denied,
		"content": "ng",
	})
	if err == nil {
		t.Fatal("expected permission error for denied path")
	}
	if !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Errorf("error = %q, want PERMISSION_DENIED", err.Error())
	}
}

func TestToolRunner_FileWrite_AllowedWritePaths_V2_ErrorCode(t *testing.T) {
	tmpDir := t.TempDir()
	allowed := filepath.Join(tmpDir, "CHAT_PERSONA.md")
	denied := filepath.Join(tmpDir, "OTHER.md")

	runner := NewToolRunner(ToolRunnerConfig{
		AllowedWritePaths: []string{allowed},
	})

	resp, err := runner.ExecuteV2(context.Background(), "file_write", map[string]any{
		"path":    denied,
		"content": "ng",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !resp.IsError() {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != tool.ErrPermissionDenied {
		t.Errorf("error code = %s, want PERMISSION_DENIED", resp.Error.Code)
	}
}

func TestToolRunner_FileList_Pagination(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("file_%02d.txt", i)), []byte("x"), 0644)
	}

	// Default: limit=100 (returns all 10)
	result, err := runner.Execute(context.Background(), "file_list", map[string]any{
		"path": tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 10 {
		t.Errorf("default: got %d lines, want 10", len(lines))
	}

	// limit=3, offset=0
	result, err = runner.Execute(context.Background(), "file_list", map[string]any{
		"path":   tmpDir,
		"limit":  float64(3),
		"offset": float64(0),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "next offset: 3") {
		t.Errorf("expected pagination info, got: %s", result)
	}

	// limit=3, offset=8 (near end)
	result, err = runner.Execute(context.Background(), "file_list", map[string]any{
		"path":   tmpDir,
		"limit":  float64(3),
		"offset": float64(8),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only 2 entries (8,9), no "next offset" since we're at end
	if strings.Contains(result, "next offset") {
		t.Error("should not have next offset at end of list")
	}
}

func TestToolRunner_FileRead_LineLimit(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "multiline.txt")
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	os.WriteFile(testFile, []byte(content), 0644)

	// Read first 3 lines
	result, err := runner.Execute(context.Background(), "file_read", map[string]any{
		"path":  testFile,
		"limit": float64(3),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "line1") {
		t.Error("expected line1")
	}
	if !strings.Contains(result, "line3") {
		t.Error("expected line3")
	}
	if strings.Contains(result, "line4\n") {
		t.Error("should not contain line4 content")
	}
	if !strings.Contains(result, "showing lines") {
		t.Error("expected pagination info")
	}

	// Read with offset
	result, err = runner.Execute(context.Background(), "file_read", map[string]any{
		"path":   testFile,
		"limit":  float64(2),
		"offset": float64(3),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "line4") {
		t.Error("expected line4 at offset 3")
	}
	if !strings.Contains(result, "line5") {
		t.Error("expected line5 at offset 3 + limit 2")
	}
}

func TestToolRunner_V2_ErrorClassification_NotFound(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"path": "/nonexistent/file.txt",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !resp.IsError() {
		t.Fatal("expected error")
	}
	if resp.Error.Code != tool.ErrNotFound {
		t.Errorf("error code = %s, want NOT_FOUND", resp.Error.Code)
	}
}

func TestToolRunner_V2_ErrorClassification_PathTraversal(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"path": "../../../etc/shadow",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !resp.IsError() {
		t.Fatal("expected error")
	}
	if resp.Error.Code != tool.ErrValidationFailed {
		t.Errorf("error code = %s, want VALIDATION_FAILED", resp.Error.Code)
	}
}

func TestToolRunner_V2_SatisfiesRunnerV2Interface(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	// Compile-time check: ToolRunner implements RunnerV2
	var _ tool.RunnerV2 = runner
}
