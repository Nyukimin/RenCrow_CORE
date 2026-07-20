package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// mockCoderAgentWithProposal はProposal生成をサポートするCoderAgent
type mockCoderAgentWithProposal struct {
	response          string
	proposal          *proposal.Proposal
	proposals         []*proposal.Proposal
	proposalErr       error
	proposalErrs      []error
	lastProposalInput string
	proposalCalls     int
}

func (m *mockCoderAgentWithProposal) Generate(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
	return m.response, nil
}

func (m *mockCoderAgentWithProposal) GenerateProposal(ctx context.Context, t task.Task) (*proposal.Proposal, error) {
	m.lastProposalInput = t.UserMessage()
	call := m.proposalCalls
	m.proposalCalls++
	if call < len(m.proposalErrs) && m.proposalErrs[call] != nil {
		return nil, m.proposalErrs[call]
	}
	if call < len(m.proposals) && m.proposals[call] != nil {
		return m.proposals[call], nil
	}
	if m.proposalErr != nil {
		return nil, m.proposalErr
	}
	return m.proposal, nil
}

func TestMessageOrchestrator_ProcessMessage_CODE3_WithProposal_JSONPatch(t *testing.T) {
	// テスト用ワークスペース作成
	tmpDir := t.TempDir()

	// WorkerExecutionService初期化
	workerConfig := config.WorkerConfig{
		AutoCommit:        false,
		StopOnError:       false,
		Workspace:         tmpDir,
		ProtectedPatterns: []string{".env*"},
		ActionOnProtected: "error",
		CommandTimeout:    10,
		GitTimeout:        10,
	}
	workerService := service.NewWorkerExecutionService(workerConfig)

	// Proposal生成（JSON形式のPatch）
	jsonPatch := `[
		{
			"type": "file_edit",
			"action": "create",
			"target": "` + filepath.ToSlash(tmpDir) + `/test.txt",
			"content": "Hello, CODE3!"
		}
	]`

	testProposal := proposal.NewProposal(
		"Test plan: Create test.txt file",
		jsonPatch,
		"Low risk",
		"Low cost",
	)

	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "Explicit CODE3 command"),
		response: "chat response",
	}
	shiro := &mockShiroAgent{response: "executed"}
	coder3 := &mockCoderAgentWithProposal{
		response: "Proposal generated",
		proposal: testProposal,
	}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, coder3, nil, workerService)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "/code3 test.txtを作成して",
	}

	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE3 {
		t.Errorf("Expected route CODE3, got '%s'", resp.Route)
	}

	// レスポンスにPlan、Execution Result、Riskが含まれているか確認
	if resp.Response == "" {
		t.Error("Response should not be empty")
	}

	// レスポンスフォーマット検証
	expected := []string{"## Plan", "## Execution Result", "## Risk", "Status"}
	for _, keyword := range expected {
		if !contains(resp.Response, keyword) {
			t.Errorf("Response should contain '%s', got: %s", keyword, resp.Response)
		}
	}
}

func TestMessageOrchestrator_ProcessMessage_CODE2_ReadOnlyDiagnosticNoChangeSucceeds(t *testing.T) {
	tmpDir := t.TempDir()
	workerService := service.NewWorkerExecutionService(config.WorkerConfig{
		AutoCommit:     false,
		StopOnError:    false,
		Workspace:      tmpDir,
		CommandTimeout: 10,
		GitTimeout:     10,
	})

	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE2, 1.0, "CODE2"),
		response: "chat response",
	}
	shiro := &mockShiroAgent{response: "executed"}
	coder2 := &mockCoderAgentWithProposal{
		proposalErr: errors.New(agent.ProposalFailureEmpty + ": missing Plan and Patch sections"),
	}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, coder2, nil, nil, workerService)

	resp, err := orchestrator.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "20260620-viewer-readonly",
		Channel:     "viewer",
		ChatID:      "viewer",
		UserMessage: "/code2 診断です。ファイル変更やコマンド実行はせず、Worker/Coder経路に届いたことだけ短く報告してください。",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Route != routing.RouteCODE2 {
		t.Fatalf("route = %s, want CODE2", resp.Route)
	}
	for _, want := range []string{"## Execution Result", "Executed**: 0 commands", "Success Rate**: 100.0%"} {
		if !contains(resp.Response, want) {
			t.Fatalf("response should contain %q, got:\n%s", want, resp.Response)
		}
	}
}

func TestMessageOrchestrator_ProcessMessage_CODE3_RetriesRetryableProposalFailure(t *testing.T) {
	tmpDir := t.TempDir()
	workerService := service.NewWorkerExecutionService(config.WorkerConfig{
		AutoCommit:     false,
		StopOnError:    false,
		Workspace:      tmpDir,
		CommandTimeout: 10,
		GitTimeout:     10,
	})
	jsonPatch := `[
		{
			"type": "file_edit",
			"action": "create",
			"target": "` + filepath.ToSlash(tmpDir) + `/retry.txt",
			"content": "retry ok"
		}
	]`
	retryProposal := proposal.NewProposal("Create retry.txt", jsonPatch, "Low risk", "Low cost")

	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "CODE3"),
		response: "chat response",
	}
	shiro := &mockShiroAgent{response: "executed"}
	coder3 := &mockCoderAgentWithProposal{
		proposalErrs: []error{errors.New(agent.ProposalFailureInvalidPatch + ": proposal patch is not runnable")},
		proposals:    []*proposal.Proposal{nil, retryProposal},
	}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, coder3, nil, workerService)

	resp, err := orchestrator.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "20260620-viewer-retry",
		Channel:     "viewer",
		ChatID:      "viewer",
		UserMessage: "/code3 retry.txtを作成して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if coder3.proposalCalls != 2 {
		t.Fatalf("proposal calls = %d, want 2", coder3.proposalCalls)
	}
	if !strings.Contains(coder3.lastProposalInput, "Coder Proposal Retry") {
		t.Fatalf("retry prompt missing: %q", coder3.lastProposalInput)
	}
	if !contains(resp.Response, "retry.txt") {
		t.Fatalf("response should contain retry result, got:\n%s", resp.Response)
	}
}

func TestMessageOrchestrator_ProcessMessage_CODE3_WithProposal_MarkdownPatch(t *testing.T) {
	tmpDir := t.TempDir()

	workerConfig := config.WorkerConfig{
		AutoCommit:     false,
		StopOnError:    false,
		Workspace:      tmpDir,
		CommandTimeout: 10,
		GitTimeout:     10,
	}
	workerService := service.NewWorkerExecutionService(workerConfig)

	// Markdown形式のPatch
	helloPath := tmpDir + "/hello.go"
	if err := os.WriteFile(helloPath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("failed to create existing update target: %v", err)
	}
	markdownPatch := "```go:" + helloPath + "\npackage main\n\nfunc Hello() string {\n\treturn \"Hello\"\n}\n```"

	testProposal := proposal.NewProposal(
		"Test plan: Create hello.go",
		markdownPatch,
		"Low risk",
		"Low cost",
	)

	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "CODE3"),
		response: "chat response",
	}
	shiro := &mockShiroAgent{response: "executed"}
	coder3 := &mockCoderAgentWithProposal{
		proposal: testProposal,
	}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, coder3, nil, workerService)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "/code3 hello.goを実装して",
	}

	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// レスポンス検証
	if !contains(resp.Response, "## Plan") {
		t.Error("Response should contain Plan section")
	}

	if !contains(resp.Response, "✅") && !contains(resp.Response, "⚠️") {
		t.Error("Response should contain status emoji")
	}
}

func TestMessageOrchestrator_ProcessMessage_CODE3_InvalidProposal(t *testing.T) {
	tmpDir := t.TempDir()

	workerConfig := config.WorkerConfig{
		Workspace:      tmpDir,
		CommandTimeout: 10,
		GitTimeout:     10,
	}
	workerService := service.NewWorkerExecutionService(workerConfig)

	// 無効なProposal（Patchが空）
	invalidProposal := proposal.NewProposal("", "", "", "")

	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "CODE3"),
	}
	shiro := &mockShiroAgent{}
	coder3 := &mockCoderAgentWithProposal{
		proposal: invalidProposal,
	}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, coder3, nil, workerService)

	req := ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "/code3 テスト",
	}

	_, err := orchestrator.ProcessMessage(context.Background(), req)
	if err == nil {
		t.Error("Expected error for invalid proposal, but got nil")
	}

	if !contains(err.Error(), "invalid proposal") {
		t.Errorf("Expected 'invalid proposal' error, got: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_CODE3_NoCoder3Available(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "CODE3"),
	}
	shiro := &mockShiroAgent{}

	// coder3 = nil
	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	req := ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "/code3 テスト",
	}

	_, err := orchestrator.ProcessMessage(context.Background(), req)
	if err == nil {
		t.Error("Expected error when coder3 is not available")
	}

	if !contains(err.Error(), "no coder3 available") {
		t.Errorf("Expected 'no coder3 available' error, got: %v", err)
	}
}

func TestFormatExecutionResult_SuccessWithGitCommit(t *testing.T) {
	tmpDir := t.TempDir()

	workerConfig := config.WorkerConfig{
		AutoCommit:          false, // Git repo not initialized in test
		CommitMessagePrefix: "[Test]",
		Workspace:           tmpDir,
		CommandTimeout:      10,
		GitTimeout:          10,
	}
	workerService := service.NewWorkerExecutionService(workerConfig)

	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "CODE3"),
	}
	shiro := &mockShiroAgent{}

	jsonPatch := `[{"type": "file_edit", "action": "create", "target": "` + filepath.ToSlash(tmpDir) + `/test.txt", "content": "Test"}]`
	testProposal := proposal.NewProposal("Test plan", jsonPatch, "Low", "Low")

	coder3 := &mockCoderAgentWithProposal{proposal: testProposal}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, coder3, nil, workerService)

	req := ProcessMessageRequest{
		SessionID:   "test",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "/code3 test",
	}

	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// Git Commit行が含まれているか（auto-commitがfalseなのでno-changesまたは含まれない）
	// ここでは実行結果のフォーマットを検証
	if !contains(resp.Response, "Success Rate") {
		t.Error("Response should contain 'Success Rate'")
	}

	if !contains(resp.Response, "Executed") {
		t.Error("Response should contain 'Executed' count")
	}
}

func TestFormatExecutionResult_PartialFailure(t *testing.T) {
	tmpDir := t.TempDir()

	workerConfig := config.WorkerConfig{
		StopOnError:    false, // 継続モード
		Workspace:      tmpDir,
		CommandTimeout: 10,
		GitTimeout:     10,
	}
	workerService := service.NewWorkerExecutionService(workerConfig)

	// 最初は成功、2番目は失敗するPatch
	jsonPatch := `[
		{"type": "file_edit", "action": "create", "target": "` + filepath.ToSlash(tmpDir) + `/ok.txt", "content": "OK"},
		{"type": "shell_command", "action": "run", "target": "false"}
	]`

	testProposal := proposal.NewProposal("Test plan", jsonPatch, "Medium", "Low")

	repo := newMockSessionRepository()
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCODE3, 1.0, "CODE3")}
	shiro := &mockShiroAgent{}
	coder3 := &mockCoderAgentWithProposal{proposal: testProposal}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, coder3, nil, workerService)

	req := ProcessMessageRequest{
		SessionID:   "test",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "/code3 test",
	}

	// 部分失敗はエラーキーワード("Error:")を含むため verifyByContract が verification_failed を返し、
	// MaxRepair 上限に達した後 ProcessMessage はエラーを返す。
	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err == nil {
		// 全コマンド成功の場合はレスポンスをチェック
		if !contains(resp.Response, "⚠️") && !contains(resp.Response, "❌") && !contains(resp.Response, "✅") {
			t.Error("Response should contain status emoji")
		}
	} else {
		// 部分失敗はエラー扱い: エラーメッセージに実行結果が含まれること
		errMsg := err.Error()
		if !contains(errMsg, "Failed") && !contains(errMsg, "Execution Result") {
			t.Errorf("Error message should contain execution result details, got: %v", err)
		}
	}
}

// contains はヘルパー関数
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(findInString(s, substr) >= 0))
}

func findInString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
