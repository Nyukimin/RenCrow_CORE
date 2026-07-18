package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestWorkerShellCommand_PrefersBashLoginShell(t *testing.T) {
	cmd := workerShellCommand(context.Background(), "echo ok")
	if filepath.Base(cmd.Path) != "bash" {
		t.Skip("bash is not available in test environment")
	}
	if len(cmd.Args) < 3 || cmd.Args[1] != "-lc" || cmd.Args[2] != "echo ok" {
		t.Fatalf("unexpected bash args: %#v", cmd.Args)
	}
}

func TestExecuteProposal_Success_JSONPatch(t *testing.T) {
	// テスト用ワークスペース作成
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		AutoCommit:           false,
		CommitMessagePrefix:  "[Test]",
		CommandTimeout:       10,
		GitTimeout:           10,
		StopOnError:          false,
		Workspace:            tmpDir,
		ProtectedPatterns:    []string{".env*"},
		ActionOnProtected:    "error",
		ShowExecutionSummary: false,
	}

	service := NewWorkerExecutionService(cfg)

	// JSON形式のPatch
	jsonPatch := `[
		{
			"type": "file_edit",
			"action": "create",
			"target": "` + filepath.Join(tmpDir, "test.txt") + `",
			"content": "Hello, World!"
		}
	]`

	p := proposal.NewProposal("Test plan", jsonPatch, "Low risk", "Low cost")
	jobID := task.NewJobID()

	result, err := service.ExecuteProposal(context.Background(), jobID, p)
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}

	if !result.Success {
		t.Error("Expected success, but got failure")
	}

	if result.ExecutedCmds != 1 {
		t.Errorf("Expected 1 executed command, got %d", result.ExecutedCmds)
	}

	if result.FailedCmds != 0 {
		t.Errorf("Expected 0 failed commands, got %d", result.FailedCmds)
	}

	// ファイルが作成されたか確認
	content, err := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}

	if string(content) != "Hello, World!" {
		t.Errorf("Expected 'Hello, World!', got '%s'", string(content))
	}
}

func TestExecuteProposal_Success_MarkdownPatch(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		AutoCommit:        false,
		CommandTimeout:    10,
		GitTimeout:        10,
		StopOnError:       false,
		Workspace:         tmpDir,
		ProtectedPatterns: []string{".env*"},
		ActionOnProtected: "error",
	}

	service := NewWorkerExecutionService(cfg)

	// Markdown形式のPatch
	testFilePath := filepath.Join(tmpDir, "hello.go")
	if err := os.WriteFile(testFilePath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("failed to create existing update target: %v", err)
	}
	markdownPatch := "```go:" + testFilePath + "\npackage main\n\nfunc main() {\n\tprintln(\"Hello\")\n}\n```"

	p := proposal.NewProposal("Test plan", markdownPatch, "Low risk", "Low cost")
	jobID := task.NewJobID()

	result, err := service.ExecuteProposal(context.Background(), jobID, p)
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}

	if !result.Success {
		t.Error("Expected success, but got failure")
	}

	// ファイルが更新されたか確認
	content, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}

	expected := "package main\n\nfunc main() {\n\tprintln(\"Hello\")\n}\n"
	if string(content) != expected {
		t.Errorf("Expected '%s', got '%s'", expected, string(content))
	}
}

func TestExecuteProposal_MarkdownPatchCreatesMissingFile(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		AutoCommit:        false,
		CommandTimeout:    10,
		GitTimeout:        10,
		StopOnError:       false,
		Workspace:         tmpDir,
		ProtectedPatterns: []string{".env*"},
		ActionOnProtected: "error",
	}

	service := NewWorkerExecutionService(cfg)

	testFilePath := filepath.Join(tmpDir, "application", "vocabulary", "app_service_test.go")
	markdownPatch := "```go:" + testFilePath + "\npackage main\n\nfunc main() {}\n```"

	p := proposal.NewProposal("Create missing file from Markdown patch", markdownPatch, "Low risk", "Low cost")
	jobID := task.NewJobID()

	result, err := service.ExecuteProposal(context.Background(), jobID, p)
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got result: %#v", result)
	}
	content, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	if !strings.Contains(string(content), "func main()") {
		t.Fatalf("created file content mismatch: %s", string(content))
	}
}

func TestExecuteProposal_ParseError(t *testing.T) {
	cfg := config.WorkerConfig{
		Workspace: t.TempDir(),
	}

	service := NewWorkerExecutionService(cfg)

	// 不正なPatch
	invalidPatch := "this is not valid JSON or Markdown"
	p := proposal.NewProposal("Test", invalidPatch, "", "")
	jobID := task.NewJobID()

	_, err := service.ExecuteProposal(context.Background(), jobID, p)
	if err == nil {
		t.Error("Expected parse error, but got nil")
	}
}

func TestExecuteProposal_BlocksSelfServiceRestartBeforeAnyCommandRuns(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.WorkerConfig{
		Workspace:      tmpDir,
		CommandTimeout: 10,
	}
	service := NewWorkerExecutionService(cfg)
	markerPath := filepath.Join(tmpDir, "ran.txt")
	patchJSON := `[
		{"type":"shell_command","action":"run","target":"echo should-not-run > ` + markerPath + `"},
		{"type":"shell_command","action":"run","target":"systemctl --user restart rencrow.service"}
	]`

	_, err := service.ExecuteProposal(context.Background(), task.NewJobID(), proposal.NewProposal("blocked", patchJSON, "", ""))
	if err == nil {
		t.Fatal("expected self lifecycle command to require approval")
	}
	if !strings.Contains(err.Error(), "approval required") || !strings.Contains(err.Error(), "rencrow.service lifecycle change") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected preflight to stop all commands before marker creation, statErr=%v", statErr)
	}
}

func TestExecuteProposal_BlocksSelfInstallCommands(t *testing.T) {
	cfg := config.WorkerConfig{
		Workspace:      t.TempDir(),
		CommandTimeout: 10,
	}
	service := NewWorkerExecutionService(cfg)
	patchJSON := `[
		{"type":"shell_command","action":"run","target":"make install"},
		{"type":"shell_command","action":"run","target":"cp build/rencrow-linux-amd64 ~/.local/bin/rencrow"}
	]`

	_, err := service.ExecuteProposal(context.Background(), task.NewJobID(), proposal.NewProposal("blocked", patchJSON, "", ""))
	if err == nil {
		t.Fatal("expected live binary install command to require approval")
	}
	if !strings.Contains(err.Error(), "approval required") || !strings.Contains(err.Error(), "RenCrow live binary install") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecuteProposal_ClassifiesMissingCommandAsRetryable(t *testing.T) {
	tmpDir := t.TempDir()
	const missingCommand = "rencrow-test-command-that-must-not-exist"
	if path, err := exec.LookPath(missingCommand); err == nil {
		t.Fatalf("test requires a missing command, but found %q at %s", missingCommand, path)
	}
	cfg := config.WorkerConfig{
		Workspace:      tmpDir,
		StopOnError:    true,
		CommandTimeout: 10,
	}
	service := NewWorkerExecutionService(cfg)
	jobID := task.NewJobID()
	p := proposal.NewProposal("retry test", `[{"type":"shell_command","action":"run","target":"`+missingCommand+`"}]`, "", "")

	result, err := service.ExecuteProposal(context.Background(), jobID, p)
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}
	if result.Success {
		t.Fatal("expected failed execution")
	}
	if result.FailureKind != "missing_command" {
		t.Fatalf("expected missing_command, got %q", result.FailureKind)
	}
	if !result.Retryable {
		t.Fatal("expected retryable failure")
	}
}

func TestExecuteFileEdit_Create(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ProtectedPatterns: []string{},
		ActionOnProtected: "error",
	}

	service := &workerExecutionService{config: cfg}

	testFile := filepath.Join(tmpDir, "create_test.txt")
	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + testFile + `", "content": "Created"}]`

	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, err := service.ExecuteProposal(context.Background(), jobID, p)
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}

	if !result.Success {
		t.Error("Expected success")
	}

	// ファイル確認
	content, _ := os.ReadFile(testFile)
	if string(content) != "Created" {
		t.Errorf("Expected 'Created', got '%s'", string(content))
	}
}

func TestExecuteFileEdit_Update(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "update_test.txt")

	// 既存ファイル作成
	os.WriteFile(testFile, []byte("Original"), 0644)

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ProtectedPatterns: []string{},
	}

	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "file_edit", "action": "update", "target": "` + testFile + `", "content": "Updated"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	if !result.Success {
		t.Error("Expected success")
	}

	content, _ := os.ReadFile(testFile)
	if string(content) != "Updated" {
		t.Errorf("Expected 'Updated', got '%s'", string(content))
	}
}

func TestExecuteFileEdit_UpdateRequiresExistingTarget(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "missing.go")
	cfg := config.WorkerConfig{Workspace: tmpDir}
	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "file_edit", "action": "update", "target": "` + testFile + `", "content": "package main\n"}]`
	p := proposal.NewProposal("update missing file", jsonPatch, "", "")

	_, err := service.ExecuteProposal(context.Background(), task.NewJobID(), p)
	if err == nil {
		t.Fatal("expected update of missing file to be rejected")
	}
	if !strings.Contains(err.Error(), "update target does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(testFile); !os.IsNotExist(statErr) {
		t.Fatalf("missing update target should not be created: %v", statErr)
	}
}

func TestExecuteFileEdit_RejectsPlaceholderTarget(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.WorkerConfig{Workspace: tmpDir}
	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + filepath.Join(tmpDir, "path", "to", "chat_module.go") + `", "content": "package main\n"}]`
	p := proposal.NewProposal("placeholder file", jsonPatch, "", "")

	_, err := service.ExecuteProposal(context.Background(), task.NewJobID(), p)
	if err == nil {
		t.Fatal("expected placeholder target to be rejected")
	}
	if !strings.Contains(err.Error(), "placeholder target") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecuteFileEdit_RejectsGoContentWithoutPackageDeclaration(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "internal", "new.go")
	cfg := config.WorkerConfig{Workspace: tmpDir}
	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + testFile + `", "content": "func main() {}\n"}]`
	p := proposal.NewProposal("bad go file", jsonPatch, "", "")

	_, err := service.ExecuteProposal(context.Background(), task.NewJobID(), p)
	if err == nil {
		t.Fatal("expected Go content without package declaration to be rejected")
	}
	if !strings.Contains(err.Error(), "package declaration") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(testFile); !os.IsNotExist(statErr) {
		t.Fatalf("bad Go file should not be created: %v", statErr)
	}
}

func TestExecuteFileEdit_RejectsRootGoFileCreation(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "chat.go")
	cfg := config.WorkerConfig{Workspace: tmpDir}
	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + testFile + `", "content": "package main\n"}]`
	p := proposal.NewProposal("root go file", jsonPatch, "", "")

	_, err := service.ExecuteProposal(context.Background(), task.NewJobID(), p)
	if err == nil {
		t.Fatal("expected root Go file creation to be rejected")
	}
	if !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(testFile); !os.IsNotExist(statErr) {
		t.Fatalf("root Go file should not be created: %v", statErr)
	}
}

func TestExecuteFileEdit_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "delete_test.txt")

	// 既存ファイル作成
	os.WriteFile(testFile, []byte("ToDelete"), 0644)

	cfg := config.WorkerConfig{
		Workspace: tmpDir,
	}

	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "file_edit", "action": "delete", "target": "` + testFile + `"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	if !result.Success {
		t.Error("Expected success")
	}

	// ファイルが削除されたか確認
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("File should have been deleted")
	}
}

func TestExecuteFileEdit_Append(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "append_test.txt")

	// 既存ファイル作成
	os.WriteFile(testFile, []byte("Line1\n"), 0644)

	cfg := config.WorkerConfig{
		Workspace: tmpDir,
	}

	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "file_edit", "action": "append", "target": "` + testFile + `", "content": "Line2\n"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	if !result.Success {
		t.Error("Expected success")
	}

	content, _ := os.ReadFile(testFile)
	expected := "Line1\nLine2\n"
	if string(content) != expected {
		t.Errorf("Expected '%s', got '%s'", expected, string(content))
	}
}

func TestExecuteFileEdit_Mkdir(t *testing.T) {
	tmpDir := t.TempDir()
	newDir := filepath.Join(tmpDir, "newdir")

	cfg := config.WorkerConfig{
		Workspace: tmpDir,
	}

	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "file_edit", "action": "mkdir", "target": "` + newDir + `"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	if !result.Success {
		t.Error("Expected success")
	}

	// ディレクトリが作成されたか確認
	if info, err := os.Stat(newDir); err != nil || !info.IsDir() {
		t.Error("Directory should have been created")
	}
}

func TestExecuteShellCommand_Success(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:      tmpDir,
		CommandTimeout: 10,
	}

	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{"type": "shell_command", "action": "run", "target": "echo 'test'"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	if !result.Success {
		t.Error("Expected success")
	}

	if len(result.Results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(result.Results))
	}

	if !result.Results[0].Success {
		t.Error("Shell command should succeed")
	}
}

func TestProtectedFile_Error(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ProtectedPatterns: []string{".env*"},
		ActionOnProtected: "error",
	}

	service := &workerExecutionService{config: cfg}

	// 保護ファイルへの書き込み試行
	envFile := filepath.Join(tmpDir, ".env")
	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + envFile + `", "content": "SECRET=xxx"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	// 失敗すべき
	if result.Success {
		t.Error("Expected failure for protected file")
	}

	if result.FailedCmds != 1 {
		t.Errorf("Expected 1 failed command, got %d", result.FailedCmds)
	}
}

func TestWorkspaceRestriction_Error(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace: tmpDir,
	}

	service := &workerExecutionService{config: cfg}

	// workspace外への書き込み試行
	outsideFile := "/tmp/outside.txt"
	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + outsideFile + `", "content": "Bad"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, err := service.ExecuteProposal(context.Background(), jobID, p)
	if err == nil {
		t.Fatal("expected outside workspace target to be rejected before execution")
	}
	if result != nil {
		t.Fatalf("preflight rejection should not return an execution result: %#v", result)
	}
	if !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecuteFileEdit_Rename(t *testing.T) {
	tmpDir := t.TempDir()
	oldFile := filepath.Join(tmpDir, "old.txt")
	newFile := filepath.Join(tmpDir, "new.txt")

	// 既存ファイル作成
	os.WriteFile(oldFile, []byte("Content"), 0644)

	cfg := config.WorkerConfig{
		Workspace: tmpDir,
	}

	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{
		"type": "file_edit",
		"action": "rename",
		"target": "` + oldFile + `",
		"metadata": {"new_name": "` + newFile + `"}
	}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	if !result.Success {
		t.Error("Expected success")
	}

	// 新しいファイルが存在し、古いファイルが消えているか確認
	if _, err := os.Stat(newFile); err != nil {
		t.Error("New file should exist")
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("Old file should not exist")
	}
}

func TestExecuteFileEdit_Copy(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	destFile := filepath.Join(tmpDir, "dest.txt")

	// ソースファイル作成
	os.WriteFile(srcFile, []byte("Source content"), 0644)

	cfg := config.WorkerConfig{
		Workspace: tmpDir,
	}

	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{
		"type": "file_edit",
		"action": "copy",
		"target": "` + srcFile + `",
		"metadata": {"destination": "` + destFile + `"}
	}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	if !result.Success {
		t.Error("Expected success")
	}

	// 両方のファイルが存在するか確認
	content, err := os.ReadFile(destFile)
	if err != nil || string(content) != "Source content" {
		t.Error("Destination file should have same content as source")
	}

	// ソースファイルも残っているはず
	if _, err := os.Stat(srcFile); err != nil {
		t.Error("Source file should still exist")
	}
}

func TestExecuteShellCommand_WithEnv(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:      tmpDir,
		CommandTimeout: 10,
	}

	service := &workerExecutionService{config: cfg}

	jsonPatch := `[{
		"type": "shell_command",
		"action": "run",
		"target": "echo $TEST_VAR",
		"metadata": {"env": "TEST_VAR=HelloFromEnv"}
	}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	if !result.Success {
		t.Error("Expected success")
	}

	if len(result.Results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(result.Results))
	}

	// 環境変数が設定されて実行されたか確認（出力にHelloFromEnvが含まれる）
	if !strings.Contains(result.Results[0].Output, "HelloFromEnv") {
		t.Errorf("Expected output to contain 'HelloFromEnv', got: %s", result.Results[0].Output)
	}
}

func TestShowExecutionSummary(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:            tmpDir,
		ShowExecutionSummary: true,
		CommandTimeout:       10,
	}

	service := NewWorkerExecutionService(cfg)

	jsonPatch := `[
		{"type": "file_edit", "action": "create", "target": "` + tmpDir + `/test1.txt", "content": "A"},
		{"type": "shell_command", "action": "run", "target": "echo test"}
	]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	// サマリが表示される（標準出力）
	result, err := service.ExecuteProposal(context.Background(), jobID, p)
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}

	if !result.Success {
		t.Error("Expected success")
	}

	// サマリが含まれているか（resultのSummaryフィールド）
	if result.Summary == "" {
		t.Error("Summary should not be empty when ShowExecutionSummary is true")
	}
}

func TestProtectedFile_Skip(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ProtectedPatterns: []string{".env*"},
		ActionOnProtected: "skip", // skip mode
	}

	service := &workerExecutionService{config: cfg}

	envFile := filepath.Join(tmpDir, ".env")
	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + envFile + `", "content": "SECRET=xxx"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	// skipモードなので成功する（ただしファイルは作成されない）
	if !result.Success {
		t.Error("Expected success in skip mode")
	}

	// ファイルは作成されないはず
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Error("Protected file should not be created in skip mode")
	}
}

func TestProtectedFile_Log(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ProtectedPatterns: []string{".env*"},
		ActionOnProtected: "log", // log mode
	}

	service := &workerExecutionService{config: cfg}

	envFile := filepath.Join(tmpDir, ".env")
	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + envFile + `", "content": "SECRET=xxx"}]`
	p := proposal.NewProposal("", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, _ := service.ExecuteProposal(context.Background(), jobID, p)

	// logモードなので成功する（警告ログが出る）
	if !result.Success {
		t.Error("Expected success in log mode")
	}

	// ファイルは作成される
	if _, err := os.Stat(envFile); err != nil {
		t.Error("File should be created in log mode")
	}
}

func TestStopOnError_vs_ContinueOnError(t *testing.T) {
	tmpDir := t.TempDir()

	// StopOnError=trueのテスト
	t.Run("StopOnError=true", func(t *testing.T) {
		cfg := config.WorkerConfig{
			Workspace:   tmpDir,
			StopOnError: true,
		}

		service := &workerExecutionService{config: cfg}

		// 最初は成功、2番目は失敗、3番目は実行されないはず
		file1 := filepath.Join(tmpDir, "file1.txt")
		file2 := filepath.Join(tmpDir, "file2.txt")
		missingFile := filepath.Join(tmpDir, "missing-stop.txt")

		jsonPatch := `[
			{"type": "file_edit", "action": "create", "target": "` + file1 + `", "content": "OK"},
			{"type": "file_edit", "action": "delete", "target": "` + missingFile + `"},
			{"type": "file_edit", "action": "create", "target": "` + file2 + `", "content": "Should not execute"}
		]`

		p := proposal.NewProposal("", jsonPatch, "", "")
		jobID := task.NewJobID()

		result, _ := service.ExecuteProposal(context.Background(), jobID, p)

		if result.ExecutedCmds != 2 {
			t.Errorf("Expected 2 executed commands (stopped on error), got %d", result.ExecutedCmds)
		}

		if result.FailedCmds != 1 {
			t.Errorf("Expected 1 failed command, got %d", result.FailedCmds)
		}

		// file2は作成されないはず
		if _, err := os.Stat(file2); !os.IsNotExist(err) {
			t.Error("file2 should not have been created (execution stopped)")
		}
	})

	// StopOnError=falseのテスト
	t.Run("StopOnError=false", func(t *testing.T) {
		cfg := config.WorkerConfig{
			Workspace:   tmpDir,
			StopOnError: false,
		}

		service := &workerExecutionService{config: cfg}

		file3 := filepath.Join(tmpDir, "file3.txt")
		file4 := filepath.Join(tmpDir, "file4.txt")
		missingFile := filepath.Join(tmpDir, "missing-continue.txt")

		jsonPatch := `[
			{"type": "file_edit", "action": "create", "target": "` + file3 + `", "content": "OK"},
			{"type": "file_edit", "action": "delete", "target": "` + missingFile + `"},
			{"type": "file_edit", "action": "create", "target": "` + file4 + `", "content": "Should execute"}
		]`

		p := proposal.NewProposal("", jsonPatch, "", "")
		jobID := task.NewJobID()

		result, _ := service.ExecuteProposal(context.Background(), jobID, p)

		if result.ExecutedCmds != 3 {
			t.Errorf("Expected 3 executed commands (continue on error), got %d", result.ExecutedCmds)
		}

		if result.FailedCmds != 1 {
			t.Errorf("Expected 1 failed command, got %d", result.FailedCmds)
		}

		// file4は作成されるはず
		if _, err := os.Stat(file4); err != nil {
			t.Error("file4 should have been created (execution continued)")
		}
	})
}

// === Step 1: executeParallel / executeGitOperation / autoCommitChanges テスト ===

// initGitRepo はtempdir内にgitリポジトリを初期化するヘルパー
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init failed: %v, output: %s", err, string(out))
		}
	}
}

func TestExecuteParallel_PhasedExecution(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ParallelExecution: true,
		MaxParallelism:    2,
		CommandTimeout:    10,
		GitTimeout:        10,
		StopOnError:       false,
	}

	svc := &workerExecutionService{config: cfg}
	jobID := task.NewJobID()

	// file_edit, shell_command, file_edit の順で混在
	// 実際のフェーズ順: file_edit → shell_command → git_operation
	file1 := filepath.Join(tmpDir, "phase_test1.txt")
	file2 := filepath.Join(tmpDir, "phase_test2.txt")

	commands := []patch.PatchCommand{
		{Type: patch.TypeFileEdit, Action: patch.ActionCreate, Target: file1, Content: "file1"},
		{Type: patch.TypeShellCommand, Action: patch.ActionRun, Target: "echo phase-shell"},
		{Type: patch.TypeFileEdit, Action: patch.ActionCreate, Target: file2, Content: "file2"},
	}

	result := svc.executeParallel(context.Background(), jobID, commands)

	// 全3コマンド実行
	if result.ExecutedCmds != 3 {
		t.Errorf("Expected 3 executed, got %d", result.ExecutedCmds)
	}
	if result.FailedCmds != 0 {
		t.Errorf("Expected 0 failed, got %d", result.FailedCmds)
	}

	// ファイルが作成されたか
	if _, err := os.Stat(file1); err != nil {
		t.Error("file1 should exist")
	}
	if _, err := os.Stat(file2); err != nil {
		t.Error("file2 should exist")
	}
}

func TestExecuteParallel_SemaphoreLimiting(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ParallelExecution: true,
		MaxParallelism:    1, // 並列度1 = 実質シーケンシャル
		CommandTimeout:    10,
		StopOnError:       false,
	}

	svc := &workerExecutionService{config: cfg}
	jobID := task.NewJobID()

	// 5つのfile_editを並列度1で実行（全て同フェーズ）
	commands := make([]patch.PatchCommand, 5)
	for i := 0; i < 5; i++ {
		f := filepath.Join(tmpDir, strings.Replace("sem_test_N.txt", "N", strings.Repeat("x", i+1), 1))
		commands[i] = patch.PatchCommand{
			Type: patch.TypeFileEdit, Action: patch.ActionCreate,
			Target: f, Content: "content",
		}
	}

	result := svc.executeParallel(context.Background(), jobID, commands)

	if result.ExecutedCmds != 5 {
		t.Errorf("Expected 5 executed, got %d", result.ExecutedCmds)
	}
	if result.FailedCmds != 0 {
		t.Errorf("Expected 0 failed, got %d", result.FailedCmds)
	}
}

func TestExecuteParallel_StopOnError(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ParallelExecution: true,
		MaxParallelism:    4,
		CommandTimeout:    10,
		StopOnError:       true,
	}

	svc := &workerExecutionService{config: cfg}
	jobID := task.NewJobID()

	// file_editフェーズで失敗 → shell_commandフェーズは実行されないはず
	commands := []patch.PatchCommand{
		{Type: patch.TypeFileEdit, Action: patch.ActionDelete, Target: "/nonexistent/no_such_file.txt"},
		{Type: patch.TypeShellCommand, Action: patch.ActionRun, Target: "echo should-not-run"},
	}

	result := svc.executeParallel(context.Background(), jobID, commands)

	// file_editフェーズだけ実行される（1コマンド失敗）
	if result.ExecutedCmds != 1 {
		t.Errorf("Expected 1 executed (stopped after first phase), got %d", result.ExecutedCmds)
	}
	if result.FailedCmds != 1 {
		t.Errorf("Expected 1 failed, got %d", result.FailedCmds)
	}
}

func TestExecuteParallel_DefaultMaxParallelism(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.WorkerConfig{
		Workspace:         tmpDir,
		ParallelExecution: true,
		MaxParallelism:    0, // デフォルト4にフォールバック
		CommandTimeout:    10,
		StopOnError:       false,
	}

	svc := &workerExecutionService{config: cfg}
	jobID := task.NewJobID()

	f := filepath.Join(tmpDir, "default_par.txt")
	commands := []patch.PatchCommand{
		{Type: patch.TypeFileEdit, Action: patch.ActionCreate, Target: f, Content: "ok"},
	}

	result := svc.executeParallel(context.Background(), jobID, commands)

	if result.ExecutedCmds != 1 {
		t.Errorf("Expected 1 executed, got %d", result.ExecutedCmds)
	}
}

func TestExecuteGitOperation(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// ファイルを作成してgit add
	testFile := filepath.Join(tmpDir, "git_test.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	cfg := config.WorkerConfig{
		Workspace:  tmpDir,
		GitTimeout: 10,
	}

	svc := &workerExecutionService{config: cfg}

	// git add コマンドテスト
	cmd := patch.PatchCommand{
		Type:   patch.TypeGitOperation,
		Action: patch.ActionAdd,
		Target: "add git_test.txt",
	}

	result := svc.executeCommand(context.Background(), task.NewJobID(), cmd, 0)

	if !result.Success {
		t.Errorf("git add should succeed, got error: %s", result.Error)
	}
}

func TestExecuteGitOperation_CommitAfterAdd(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// ファイル作成 → git add → git commit
	testFile := filepath.Join(tmpDir, "commit_test.txt")
	os.WriteFile(testFile, []byte("content"), 0644)

	cfg := config.WorkerConfig{
		Workspace:  tmpDir,
		GitTimeout: 10,
	}

	svc := &workerExecutionService{config: cfg}

	// git add
	addCmd := patch.PatchCommand{
		Type: patch.TypeGitOperation, Action: patch.ActionAdd, Target: "add commit_test.txt",
	}
	addResult := svc.executeCommand(context.Background(), task.NewJobID(), addCmd, 0)
	if !addResult.Success {
		t.Fatalf("git add failed: %s", addResult.Error)
	}

	// git commit
	commitCmd := patch.PatchCommand{
		Type: patch.TypeGitOperation, Action: patch.ActionCommit, Target: "commit -m test-commit",
	}
	commitResult := svc.executeCommand(context.Background(), task.NewJobID(), commitCmd, 1)
	if !commitResult.Success {
		t.Fatalf("git commit failed: %s", commitResult.Error)
	}
}

func TestAutoCommitChanges(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// 初回コミットが必要（git rev-parse HEAD 用）
	initFile := filepath.Join(tmpDir, "init.txt")
	os.WriteFile(initFile, []byte("init"), 0644)
	runGit(t, tmpDir, "add", "-A")
	runGit(t, tmpDir, "commit", "-m", "initial commit")

	// テスト用ファイル作成
	testFile := filepath.Join(tmpDir, "auto_commit_test.txt")
	os.WriteFile(testFile, []byte("auto-commit content"), 0644)

	cfg := config.WorkerConfig{
		Workspace:           tmpDir,
		AutoCommit:          true,
		CommitMessagePrefix: "[Test]",
		GitTimeout:          10,
	}

	svc := &workerExecutionService{config: cfg}
	jobID := task.NewJobID()

	hash, err := svc.autoCommitChanges(context.Background(), jobID, "test auto-commit")
	if err != nil {
		t.Fatalf("autoCommitChanges failed: %v", err)
	}

	if hash == "" {
		t.Error("Expected non-empty commit hash")
	}

	if len(hash) < 7 {
		t.Errorf("Expected valid git hash, got: %s", hash)
	}

	// コミットメッセージにJobIDとprefixが含まれるか確認
	logCmd := exec.Command("git", "log", "-1", "--pretty=%B")
	logCmd.Dir = tmpDir
	logOutput, err := logCmd.Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}

	logMsg := string(logOutput)
	if !strings.Contains(logMsg, "[Test]") {
		t.Errorf("Commit message should contain prefix '[Test]', got: %s", logMsg)
	}
	if !strings.Contains(logMsg, jobID.String()) {
		t.Errorf("Commit message should contain jobID, got: %s", logMsg)
	}
}

func TestAutoCommitChanges_NothingToCommit(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// 初回コミットを作成
	initFile := filepath.Join(tmpDir, "init.txt")
	os.WriteFile(initFile, []byte("init"), 0644)
	runGit(t, tmpDir, "add", "-A")
	runGit(t, tmpDir, "commit", "-m", "initial commit")

	// 変更なし → nothing to commit
	cfg := config.WorkerConfig{
		Workspace:           tmpDir,
		AutoCommit:          true,
		CommitMessagePrefix: "[Test]",
		GitTimeout:          10,
	}

	svc := &workerExecutionService{config: cfg}
	jobID := task.NewJobID()

	hash, err := svc.autoCommitChanges(context.Background(), jobID, "no changes")
	if err != nil {
		t.Fatalf("autoCommitChanges should not error on nothing to commit: %v", err)
	}

	if hash != "no-changes" {
		t.Errorf("Expected 'no-changes', got: %s", hash)
	}
}

func TestExecuteProposal_WithAutoCommit(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// 初回コミット
	initFile := filepath.Join(tmpDir, "init.txt")
	os.WriteFile(initFile, []byte("init"), 0644)
	runGit(t, tmpDir, "add", "-A")
	runGit(t, tmpDir, "commit", "-m", "initial commit")

	cfg := config.WorkerConfig{
		AutoCommit:          true,
		CommitMessagePrefix: "[Worker]",
		CommandTimeout:      10,
		GitTimeout:          10,
		StopOnError:         false,
		Workspace:           tmpDir,
	}

	svc := NewWorkerExecutionService(cfg)

	testFile := filepath.Join(tmpDir, "autocommit_test.txt")
	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + testFile + `", "content": "auto-committed"}]`
	p := proposal.NewProposal("Test plan", jsonPatch, "Low", "Low")
	jobID := task.NewJobID()

	result, err := svc.ExecuteProposal(context.Background(), jobID, p)
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}

	if result.GitCommit == "" {
		t.Error("Expected GitCommit hash when auto-commit is enabled")
	}
}

func TestExecuteProposal_ParallelWithMixedTypes(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	cfg := config.WorkerConfig{
		AutoCommit:        false,
		ParallelExecution: true,
		MaxParallelism:    4,
		CommandTimeout:    10,
		GitTimeout:        10,
		StopOnError:       false,
		Workspace:         tmpDir,
	}

	svc := NewWorkerExecutionService(cfg)

	file1 := filepath.Join(tmpDir, "par_mixed1.txt")
	file2 := filepath.Join(tmpDir, "par_mixed2.txt")

	// file_edit + shell_command + git_operation の混合
	jsonPatch := `[
		{"type": "file_edit", "action": "create", "target": "` + file1 + `", "content": "A"},
		{"type": "file_edit", "action": "create", "target": "` + file2 + `", "content": "B"},
		{"type": "shell_command", "action": "run", "target": "echo mixed-test"},
		{"type": "git_operation", "action": "add", "target": "add -A"}
	]`
	p := proposal.NewProposal("Parallel mixed", jsonPatch, "", "")
	jobID := task.NewJobID()

	result, err := svc.ExecuteProposal(context.Background(), jobID, p)
	if err != nil {
		t.Fatalf("ExecuteProposal failed: %v", err)
	}

	if result.ExecutedCmds != 4 {
		t.Errorf("Expected 4 executed, got %d", result.ExecutedCmds)
	}
	if result.FailedCmds != 0 {
		t.Errorf("Expected 0 failed, got %d", result.FailedCmds)
	}
}

// runGit はgitコマンドを実行するヘルパー
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v, output: %s", args, err, string(out))
	}
}
