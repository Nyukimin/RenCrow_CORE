package patch

import "testing"

func TestNewPatchExecutionResult(t *testing.T) {
	result := NewPatchExecutionResult()

	if !result.Success {
		t.Error("New result should be successful initially")
	}

	if result.ExecutedCmds != 0 {
		t.Errorf("Expected 0 executed commands, got %d", result.ExecutedCmds)
	}

	if result.FailedCmds != 0 {
		t.Errorf("Expected 0 failed commands, got %d", result.FailedCmds)
	}

	if result.HasFailures() {
		t.Error("New result should not have failures")
	}
}

func TestPatchExecutionResultAddResult(t *testing.T) {
	result := NewPatchExecutionResult()

	// 成功コマンド追加
	successCmd := CommandResult{
		Command: NewPatchCommand(TypeFileEdit, ActionCreate, "test.go", "content"),
		Success: true,
		Output:  "File created",
	}
	result.AddResult(successCmd)

	if result.ExecutedCmds != 1 {
		t.Errorf("Expected 1 executed command, got %d", result.ExecutedCmds)
	}

	if result.FailedCmds != 0 {
		t.Errorf("Expected 0 failed commands, got %d", result.FailedCmds)
	}

	if !result.Success {
		t.Error("Result should still be successful")
	}

	// 失敗コマンド追加
	failedCmd := CommandResult{
		Command: NewPatchCommand(TypeFileEdit, ActionDelete, "test.go", ""),
		Success: false,
		Error:   "File not found",
	}
	result.AddResult(failedCmd)

	if result.ExecutedCmds != 2 {
		t.Errorf("Expected 2 executed commands, got %d", result.ExecutedCmds)
	}

	if result.FailedCmds != 1 {
		t.Errorf("Expected 1 failed command, got %d", result.FailedCmds)
	}

	if result.Success {
		t.Error("Result should be failed after adding failed command")
	}

	if !result.HasFailures() {
		t.Error("Result should have failures")
	}
}

func TestPatchExecutionResultSuccessRate(t *testing.T) {
	result := NewPatchExecutionResult()

	// 0件の場合
	if result.SuccessRate() != 0.0 {
		t.Errorf("Expected success rate 0.0 for 0 commands, got %f", result.SuccessRate())
	}

	// 2件成功、1件失敗 = 66.67%
	result.AddResult(CommandResult{Success: true})
	result.AddResult(CommandResult{Success: true})
	result.AddResult(CommandResult{Success: false})

	expectedRate := 2.0 / 3.0
	if result.SuccessRate() != expectedRate {
		t.Errorf("Expected success rate %f, got %f", expectedRate, result.SuccessRate())
	}
}

func TestPatchExecutionResultWithSummary(t *testing.T) {
	result := NewPatchExecutionResult()
	summary := "All commands executed successfully"

	resultWithSummary := result.WithSummary(summary)

	if resultWithSummary.Summary != summary {
		t.Errorf("Expected summary '%s', got '%s'", summary, resultWithSummary.Summary)
	}
}

func TestPatchExecutionResultWithGitCommit(t *testing.T) {
	result := NewPatchExecutionResult()
	commitHash := "abc123def456"

	resultWithCommit := result.WithGitCommit(commitHash)

	if resultWithCommit.GitCommit != commitHash {
		t.Errorf("Expected git commit '%s', got '%s'", commitHash, resultWithCommit.GitCommit)
	}
}

func TestPatchExecutionResultWithFailureMetadata(t *testing.T) {
	result := NewPatchExecutionResult().WithFailureMetadata("io_error", "disk full", true)

	if result.FailureKind != "io_error" || result.FailureReason != "disk full" || !result.Retryable {
		t.Fatalf("failure metadata not set: %#v", result)
	}
}
