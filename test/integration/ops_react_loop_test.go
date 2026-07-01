package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	infraRouting "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/routing"
)

// mockSubagentManager は SubagentManager のモック
type mockSubagentManager struct {
	runSyncCalled bool
	runSyncFunc   func(ctx context.Context, task agent.SubagentTask) (agent.SubagentResult, error)
}

func (m *mockSubagentManager) RunSync(ctx context.Context, task agent.SubagentTask) (agent.SubagentResult, error) {
	m.runSyncCalled = true
	if m.runSyncFunc != nil {
		return m.runSyncFunc(ctx, task)
	}
	return agent.SubagentResult{
		AgentName:  "shiro",
		Output:     "ReActLoop executed successfully",
		Iterations: 2,
	}, nil
}

// TestOPSRoute_WithSubagentManager_CallsReActLoop は
// SubagentManager が設定されている場合、OPS ルートで ReActLoop が使われることを確認
func TestOPSRoute_WithSubagentManager_CallsReActLoop(t *testing.T) {
	// Setup
	sessionRepo := newMockSessionRepo()

	llmGenerateCalled := false
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			llmGenerateCalled = true
			return llm.GenerateResponse{Content: "fallback response"}, nil
		},
	}

	subagentMgr := &mockSubagentManager{
		runSyncFunc: func(ctx context.Context, task agent.SubagentTask) (agent.SubagentResult, error) {
			// SubagentTask の検証
			if task.AgentName != "shiro" {
				t.Errorf("Expected AgentName 'shiro', got '%s'", task.AgentName)
			}
			if task.Instruction != "テストを実行して" {
				t.Errorf("Expected instruction 'テストを実行して', got '%s'", task.Instruction)
			}
			return agent.SubagentResult{
				AgentName:  "shiro",
				Output:     "file1.txt\nfile2.txt\nfile3.txt",
				Iterations: 3,
			}, nil
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, &mockToolRunner{}, &mockMCPClient{}, nil)

	// Shiro に SubagentManager を渡す
	shiro := agent.NewShiroAgent(provider, &mockToolRunner{}, &mockMCPClient{}, "You are a worker", subagentMgr)

	orch := orchestrator.NewMessageOrchestrator(sessionRepo, mio, shiro, nil, nil, nil, nil, nil)

	// Execute - OPS ルートをトリガー
	req := orchestrator.ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "テストを実行して", // OPS ルートにマッチ
	}

	resp, err := orch.ProcessMessage(context.Background(), req)

	// Assert
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// ルートが OPS であることを確認
	if resp.Route != routing.RouteOPS {
		t.Errorf("Expected route OPS, got %s", resp.Route)
	}

	// SubagentManager.RunSync が呼ばれたことを確認
	if !subagentMgr.runSyncCalled {
		t.Error("Expected SubagentManager.RunSync to be called, but it wasn't")
	}

	// 従来の LLM.Generate がスキップされたことを確認
	if llmGenerateCalled {
		t.Error("Expected LLM.Generate to be skipped when SubagentManager is available")
	}

	// レスポンスが SubagentManager からの出力であることを確認
	if resp.Response != "file1.txt\nfile2.txt\nfile3.txt" {
		t.Errorf("Expected SubagentManager output, got '%s'", resp.Response)
	}
}

// TestOPSRoute_WithoutSubagentManager_UsesFallback は
// SubagentManager が nil の場合、従来の LLM.Generate が使われることを確認
func TestOPSRoute_WithoutSubagentManager_UsesFallback(t *testing.T) {
	// Setup
	sessionRepo := newMockSessionRepo()

	llmGenerateCalled := false
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			llmGenerateCalled = true

			// システムプロンプトの確認
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
				if !strings.Contains(req.Messages[0].Content, "You are a worker") {
					t.Errorf("Expected system prompt to contain 'You are a worker', got '%s'", req.Messages[0].Content)
				}
				if !strings.Contains(req.Messages[0].Content, "必ず自然な日本語で応答") {
					t.Errorf("Expected system prompt to require Japanese response, got '%s'", req.Messages[0].Content)
				}
			}

			return llm.GenerateResponse{Content: "Fallback: file list retrieved"}, nil
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, &mockToolRunner{}, &mockMCPClient{}, nil)

	// Shiro に SubagentManager を渡さない（nil）
	shiro := agent.NewShiroAgent(provider, &mockToolRunner{}, &mockMCPClient{}, "You are a worker", nil)

	orch := orchestrator.NewMessageOrchestrator(sessionRepo, mio, shiro, nil, nil, nil, nil, nil)

	// Execute - OPS ルートをトリガー
	req := orchestrator.ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "テストを実行して", // OPS ルートにマッチ
	}

	resp, err := orch.ProcessMessage(context.Background(), req)

	// Assert
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// ルートが OPS であることを確認
	if resp.Route != routing.RouteOPS {
		t.Errorf("Expected route OPS, got %s", resp.Route)
	}

	// 従来の LLM.Generate が呼ばれたことを確認（フォールバック）
	if !llmGenerateCalled {
		t.Error("Expected LLM.Generate to be called as fallback, but it wasn't")
	}

	// レスポンスがフォールバックからの出力であることを確認
	if resp.Response != "Fallback: file list retrieved" {
		t.Errorf("Expected fallback output, got '%s'", resp.Response)
	}
}

// TestOPSRoute_SubagentManagerError_PropagatesError は
// SubagentManager がエラーを返した場合、エラーが伝播することを確認
func TestOPSRoute_SubagentManagerError_PropagatesError(t *testing.T) {
	// Setup
	sessionRepo := newMockSessionRepo()

	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "should not reach here"}, nil
		},
	}

	subagentMgr := &mockSubagentManager{
		runSyncFunc: func(ctx context.Context, task agent.SubagentTask) (agent.SubagentResult, error) {
			return agent.SubagentResult{}, errors.New("subagent execution failed")
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, &mockToolRunner{}, &mockMCPClient{}, nil)
	shiro := agent.NewShiroAgent(provider, &mockToolRunner{}, &mockMCPClient{}, "You are a worker", subagentMgr)

	orch := orchestrator.NewMessageOrchestrator(sessionRepo, mio, shiro, nil, nil, nil, nil, nil)

	// Execute - OPS ルートをトリガー
	req := orchestrator.ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "テストを実行して",
	}

	_, err := orch.ProcessMessage(context.Background(), req)

	// Assert
	if err == nil {
		t.Fatal("Expected error from SubagentManager, but got nil")
	}

	// エラーメッセージの確認（部分一致）
	if err.Error() == "" {
		t.Error("Expected non-empty error message")
	}
}
