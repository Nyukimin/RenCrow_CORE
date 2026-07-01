package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// Mock ToolRunner
type mockToolRunner struct {
	executeFunc   func(ctx context.Context, toolName string, args map[string]interface{}) (string, error)
	executeV2Func func(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error)
	listFunc      func(ctx context.Context) ([]tool.ToolMetadata, error)
}

func (m *mockToolRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx)
	}
	return []tool.ToolMetadata{
		{ToolID: "tool1"},
		{ToolID: "tool2"},
	}, nil
}

// Mock MCPClient
type mockMCPClient struct {
	callToolFunc  func(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error)
	listToolsFunc func(ctx context.Context, serverName string) ([]string, error)
}

type mockSubagentManager struct {
	runSyncFunc func(ctx context.Context, task SubagentTask) (SubagentResult, error)
}

func (m *mockSubagentManager) RunSync(ctx context.Context, task SubagentTask) (SubagentResult, error) {
	if m.runSyncFunc != nil {
		return m.runSyncFunc(ctx, task)
	}
	return SubagentResult{AgentName: task.AgentName, Output: "subagent ok"}, nil
}

type panicSubagentManager struct{}

func (m *panicSubagentManager) RunSync(ctx context.Context, task SubagentTask) (SubagentResult, error) {
	panic("boom")
}

func (m *mockMCPClient) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error) {
	if m.callToolFunc != nil {
		return m.callToolFunc(ctx, serverName, toolName, args)
	}
	return "mcp tool executed", nil
}

func (m *mockMCPClient) ListTools(ctx context.Context, serverName string) ([]string, error) {
	if m.listToolsFunc != nil {
		return m.listToolsFunc(ctx, serverName)
	}
	return []string{"mcp_tool1", "mcp_tool2"}, nil
}

func TestNewShiroAgent(t *testing.T) {
	llmProvider := &mockLLMProvider{}
	toolRunner := &mockToolRunner{}
	mcpClient := &mockMCPClient{}

	shiro := NewShiroAgent(llmProvider, toolRunner, mcpClient, "test prompt", nil)

	if shiro == nil {
		t.Fatal("NewShiroAgent should not return nil")
	}

	if shiro.llmProvider != llmProvider {
		t.Error("llmProvider not set correctly")
	}

	// toolRunner はインターフェース型なので直接比較できない（省略）

	if shiro.mcpClient != mcpClient {
		t.Error("mcpClient not set correctly")
	}
}

func TestShiroAgentWithPersona(t *testing.T) {
	shiro := NewShiroAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt", nil)
	persona := AgentPersona{Name: "Shiro", Personality: "precise worker"}

	if got := shiro.WithPersona(persona); got != shiro {
		t.Fatal("WithPersona should return the same agent")
	}
	if shiro.persona == nil {
		t.Fatal("persona was not set")
	}
	if shiro.persona.Name != "Shiro" || shiro.persona.Personality != "precise worker" {
		t.Fatalf("unexpected persona: %#v", shiro.persona)
	}
}

func TestShiroAgentWithLightMemory(t *testing.T) {
	shiro := NewShiroAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt", nil)
	memory := NewLightMemory(2)

	if got := shiro.WithLightMemory(memory); got != shiro {
		t.Fatal("WithLightMemory should return the same agent")
	}
	if shiro.lightMemory != memory {
		t.Fatalf("LightMemory not set: %#v", shiro.lightMemory)
	}
}

func TestShiroAgentExecute(t *testing.T) {
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			// システムプロンプトが注入されているか確認
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
				if !strings.Contains(req.Messages[0].Content, "test prompt") {
					t.Errorf("Unexpected system prompt: %s", req.Messages[0].Content)
				}
				if !strings.Contains(req.Messages[0].Content, "必ず自然な日本語で応答") {
					t.Errorf("Shiro system prompt should force Japanese response: %s", req.Messages[0].Content)
				}
			}

			return llm.GenerateResponse{
				Content:      "Task executed successfully",
				TokensUsed:   50,
				FinishReason: "stop",
			}, nil
		},
	}

	shiro := NewShiroAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt", nil)

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "ファイルを作成して", "line", "U123")

	result, err := shiro.Execute(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result != "Task executed successfully" {
		t.Errorf("Expected 'Task executed successfully', got '%s'", result)
	}
}

func TestShiroAgentExecuteUsesLightMemory(t *testing.T) {
	var captured []llm.Message
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = append([]llm.Message(nil), req.Messages...)
			return llm.GenerateResponse{Content: "second worker response"}, nil
		},
	}
	memory := NewLightMemory(3)
	memory.Record("U123", "first worker task", "first worker response")
	shiro := NewShiroAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt", nil).WithLightMemory(memory)

	if _, err := shiro.Execute(context.Background(), task.NewTask(task.NewJobID(), "second worker task", "line", "U123")); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(captured) != 4 {
		t.Fatalf("messages=%#v", captured)
	}
	if captured[1].Role != "user" || captured[1].Content != "first worker task" ||
		captured[2].Role != "assistant" || captured[2].Content != "first worker response" ||
		captured[3].Content != "second worker task" {
		t.Fatalf("LightMemory messages not injected in order: %#v", captured)
	}
	recent := memory.RecentMessages("U123")
	if len(recent) != 4 || recent[3].Content != "second worker response" {
		t.Fatalf("LightMemory did not record response: %#v", recent)
	}
}

func TestShiroAgentExecuteAppliesWorkerRecallRoleFilter(t *testing.T) {
	engine := &mockConversationEngine{
		beginTurnFunc: func(ctx context.Context, sessionID, msg string) (*conversation.RecallPack, error) {
			return &conversation.RecallPack{
				MidSummaries: []conversation.ThreadSummary{
					{Summary: "worker memory", Roles: []string{"worker"}},
					{Summary: "chat memory", Roles: []string{"chat"}},
				},
			}, nil
		},
	}
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = req
			return llm.GenerateResponse{Content: "done"}, nil
		},
	}
	shiro := NewShiroAgent(provider, &mockToolRunner{}, &mockMCPClient{}, "test prompt", nil).WithConversationEngine(engine)

	if _, err := shiro.Execute(context.Background(), task.NewTask(task.NewJobID(), "整理して", "line", "U123")); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	var prompt strings.Builder
	for _, msg := range captured.Messages {
		prompt.WriteString(msg.Content)
		prompt.WriteString("\n")
	}
	got := prompt.String()
	if !strings.Contains(got, "worker memory") {
		t.Fatalf("worker recall should be included, got:\n%s", got)
	}
	if strings.Contains(got, "chat memory") {
		t.Fatalf("chat recall should be filtered for worker, got:\n%s", got)
	}
}

func TestShiroAgentExecute_LLMError(t *testing.T) {
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, errors.New("LLM connection failed")
		},
	}

	shiro := NewShiroAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt", nil)

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "テスト", "line", "U123")

	_, err := shiro.Execute(context.Background(), testTask)
	if err == nil {
		t.Error("Expected error when LLM fails")
	}

	if err.Error() != "LLM connection failed" {
		t.Errorf("Expected 'LLM connection failed', got '%s'", err.Error())
	}
}

func TestShiroAgentExecute_TypedNilSubagentManagerReturnsError(t *testing.T) {
	llmProvider := &mockLLMProvider{}
	var typedNilManager *mockSubagentManager
	shiro := NewShiroAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt", typedNilManager)

	_, err := shiro.Execute(context.Background(), task.NewTask(task.NewJobID(), "テスト", "line", "U123"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "subagent runtime panic") {
		t.Fatalf("expected subagent panic error, got %v", err)
	}
}

func TestShiroAgentExecute_SubagentPanicReturnsError(t *testing.T) {
	llmProvider := &mockLLMProvider{}
	shiro := NewShiroAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt", &panicSubagentManager{})

	_, err := shiro.Execute(context.Background(), task.NewTask(task.NewJobID(), "テスト", "line", "U123"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "subagent runtime panic") {
		t.Fatalf("expected subagent panic error, got %v", err)
	}
}

func TestShiroAgentExecuteTool(t *testing.T) {
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			if toolName != "file_read" {
				t.Errorf("Expected tool 'file_read', got '%s'", toolName)
			}

			path, ok := args["path"].(string)
			if !ok || path != "/test/file.txt" {
				t.Errorf("Expected path '/test/file.txt', got '%v'", args["path"])
			}

			return "File content", nil
		},
	}

	shiro := NewShiroAgent(&mockLLMProvider{}, toolRunner, &mockMCPClient{}, "test prompt", nil)

	result, err := shiro.ExecuteTool(context.Background(), "file_read", map[string]interface{}{
		"path": "/test/file.txt",
	})

	if err != nil {
		t.Fatalf("ExecuteTool failed: %v", err)
	}

	if result != "File content" {
		t.Errorf("Expected 'File content', got '%s'", result)
	}
}

func TestShiroAgentExecuteMCPTool(t *testing.T) {
	mcpClient := &mockMCPClient{
		callToolFunc: func(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error) {
			if serverName != "browser" {
				t.Errorf("Expected server 'browser', got '%s'", serverName)
			}

			if toolName != "navigate" {
				t.Errorf("Expected tool 'navigate', got '%s'", toolName)
			}

			url, ok := args["url"].(string)
			if !ok || url != "https://example.com" {
				t.Errorf("Expected url 'https://example.com', got '%v'", args["url"])
			}

			return "Navigated to https://example.com", nil
		},
	}

	shiro := NewShiroAgent(&mockLLMProvider{}, &mockToolRunner{}, mcpClient, "test prompt", nil)

	result, err := shiro.ExecuteMCPTool(context.Background(), "browser", "navigate", map[string]interface{}{
		"url": "https://example.com",
	})

	if err != nil {
		t.Fatalf("ExecuteMCPTool failed: %v", err)
	}

	if result != "Navigated to https://example.com" {
		t.Errorf("Expected navigation message, got '%s'", result)
	}
}

func TestEnsureShiroJapaneseResponsePromptBranches(t *testing.T) {
	guarded := ensureShiroJapaneseResponsePrompt("")
	if !strings.Contains(guarded, "必ず自然な日本語で応答") {
		t.Fatalf("empty prompt should return Japanese guard, got %q", guarded)
	}

	alreadyGuarded := "Shiroは必ず自然な日本語で応答します"
	if got := ensureShiroJapaneseResponsePrompt(alreadyGuarded); got != alreadyGuarded {
		t.Fatalf("existing guard should be preserved, got %q", got)
	}
}

func TestShiroAgentExecuteTool_Error(t *testing.T) {
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			return "", errors.New("tool execution failed")
		},
	}

	shiro := NewShiroAgent(&mockLLMProvider{}, toolRunner, &mockMCPClient{}, "test prompt", nil)

	_, err := shiro.ExecuteTool(context.Background(), "failing_tool", map[string]interface{}{})

	if err == nil {
		t.Error("Expected error when tool fails")
	}

	if err.Error() != "tool execution failed" {
		t.Errorf("Expected 'tool execution failed', got '%s'", err.Error())
	}
}

func TestShiroAgentExecuteTool_ToolResponseError(t *testing.T) {
	toolRunner := &mockToolRunner{
		executeV2Func: func(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
			return tool.NewError(tool.ErrValidationFailed, "bad input", nil), nil
		},
	}
	shiro := NewShiroAgent(&mockLLMProvider{}, toolRunner, &mockMCPClient{}, "test prompt", nil)

	_, err := shiro.ExecuteTool(context.Background(), "bad_tool", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected ToolResponse error")
	}
	if err.Error() != "bad input" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func (m *mockToolRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if m.executeV2Func != nil {
		return m.executeV2Func(ctx, toolName, args)
	}
	if m.executeFunc != nil {
		result, err := m.executeFunc(ctx, toolName, args)
		if err != nil {
			return tool.NewError(tool.ErrInternalError, err.Error(), nil), nil
		}
		return tool.NewSuccess(result), nil
	}
	return tool.NewSuccess("tool executed"), nil
}
