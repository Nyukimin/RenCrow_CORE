package integration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	infraRouting "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/routing"
)

// === Mock Infrastructure ===

type mockLLMProvider struct {
	generateFunc func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error)
}

func (m *mockLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, req)
	}
	return llm.GenerateResponse{Content: "Mock response"}, nil
}

func (m *mockLLMProvider) Name() string { return "mock" }

type mockToolRunner struct {
	executeFunc func(ctx context.Context, toolName string, args map[string]interface{}) (string, error)
}

func (m *mockToolRunner) Execute(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, toolName, args)
	}
	return "tool result", nil
}

func (m *mockToolRunner) List(ctx context.Context) ([]string, error) {
	return []string{"shell", "file_read", "web_search"}, nil
}

func (m *mockToolRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{
		{ToolID: "shell", Version: "test", Category: "admin"},
		{ToolID: "file_read", Version: "test", Category: "query"},
		{ToolID: "web_search", Version: "test", Category: "query"},
	}, nil
}

type mockMCPClient struct{}

func (m *mockMCPClient) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error) {
	return "", nil
}

func (m *mockMCPClient) ListTools(ctx context.Context, serverName string) ([]string, error) {
	return nil, nil
}

type mockSessionRepository struct {
	sessions map[string]*session.Session
}

func newMockSessionRepo() *mockSessionRepository {
	return &mockSessionRepository{sessions: make(map[string]*session.Session)}
}

func (m *mockSessionRepository) Save(ctx context.Context, s *session.Session) error {
	m.sessions[s.ID()] = s
	return nil
}

func (m *mockSessionRepository) Load(ctx context.Context, id string) (*session.Session, error) {
	s, ok := m.sessions[id]
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	return s, nil
}

func (m *mockSessionRepository) Exists(ctx context.Context, id string) (bool, error) {
	_, ok := m.sessions[id]
	return ok, nil
}

func (m *mockSessionRepository) Delete(ctx context.Context, id string) error {
	delete(m.sessions, id)
	return nil
}

type mockClassifier struct{}

func (m *mockClassifier) Classify(ctx context.Context, t task.Task) (routing.Decision, error) {
	return routing.NewDecision(routing.RouteCHAT, 0.8, "mock classifier"), nil
}

// === Helper ===

func buildOrchestrator(llmResp string, sessionRepo *mockSessionRepository) *orchestrator.MessageOrchestrator {
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: llmResp}, nil
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, &mockToolRunner{}, &mockMCPClient{}, nil)
	shiro := agent.NewShiroAgent(provider, &mockToolRunner{}, &mockMCPClient{}, "", nil)

	return orchestrator.NewMessageOrchestrator(sessionRepo, mio, shiro, nil, nil, nil, nil, nil)
}

func defaultIntegrationReq(msg string) orchestrator.ProcessMessageRequest {
	return orchestrator.ProcessMessageRequest{
		SessionID:   "integ-test-session",
		Channel:     "line",
		ChatID:      "U_TEST",
		UserMessage: msg,
	}
}

// === Integration Tests ===

func TestIntegration_ChatRoute_FullPath(t *testing.T) {
	repo := newMockSessionRepo()
	orch := buildOrchestrator("こんにちは！お元気ですか？", repo)

	resp, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("こんにちは"))
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Route != routing.RouteCHAT {
		t.Errorf("route: want CHAT, got %s", resp.Route)
	}
	if resp.Response != "こんにちは！お元気ですか？" {
		t.Errorf("response: want LLM output, got %q", resp.Response)
	}
}

func TestIntegration_ExplicitCode3Route_NoCoder(t *testing.T) {
	repo := newMockSessionRepo()
	orch := buildOrchestrator("LLM response", repo) // no coder3 configured

	_, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("/code3 implement feature"))
	if err == nil {
		t.Fatal("expected error for CODE3 with no coder")
	}
	if !strings.Contains(err.Error(), "no coder3 available") {
		t.Errorf("error should mention coder3, got: %v", err)
	}
}

func TestIntegration_OPSRoute_RuleDictionary(t *testing.T) {
	repo := newMockSessionRepo()
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "executed: ls -la"}, nil
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, &mockToolRunner{}, &mockMCPClient{}, nil)
	shiro := agent.NewShiroAgent(provider, &mockToolRunner{}, &mockMCPClient{}, "", nil)
	orch := orchestrator.NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	// "ls -la を実行" should match OPS rule
	resp, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("/ops ls -la を実行"))
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Route != routing.RouteOPS {
		t.Errorf("route: want OPS, got %s", resp.Route)
	}
}

func TestIntegration_FallbackToChat(t *testing.T) {
	repo := newMockSessionRepo()
	orch := buildOrchestrator("fallback response", repo)

	resp, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("天気はどうですか"))
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Route != routing.RouteCHAT {
		t.Errorf("unmatched message should fall back to CHAT, got %s", resp.Route)
	}
}

func TestIntegration_SessionCreatedOnFirstMessage(t *testing.T) {
	repo := newMockSessionRepo()
	orch := buildOrchestrator("response", repo)

	_, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("hello"))
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	exists, _ := repo.Exists(context.Background(), "integ-test-session")
	if !exists {
		t.Error("session should be saved after first message")
	}
}

func TestIntegration_SessionReusedOnSubsequentMessages(t *testing.T) {
	repo := newMockSessionRepo()
	orch := buildOrchestrator("response", repo)

	_, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("first"))
	if err != nil {
		t.Fatalf("first message failed: %v", err)
	}

	_, err = orch.ProcessMessage(context.Background(), defaultIntegrationReq("second"))
	if err != nil {
		t.Fatalf("second message failed: %v", err)
	}

	sess, _ := repo.Load(context.Background(), "integ-test-session")
	if sess.HistoryCount() != 2 {
		t.Errorf("expected 2 tasks in history, got %d", sess.HistoryCount())
	}
}

func TestIntegration_MultipleMessagesGrowHistory(t *testing.T) {
	repo := newMockSessionRepo()
	orch := buildOrchestrator("response", repo)

	for i := 0; i < 3; i++ {
		_, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq(fmt.Sprintf("msg %d", i)))
		if err != nil {
			t.Fatalf("message %d failed: %v", i, err)
		}
	}

	sess, _ := repo.Load(context.Background(), "integ-test-session")
	if sess.HistoryCount() != 3 {
		t.Errorf("expected 3 tasks, got %d", sess.HistoryCount())
	}
}

func TestIntegration_LLMFailure_PropagatesError(t *testing.T) {
	repo := newMockSessionRepo()
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, fmt.Errorf("connection refused")
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, &mockToolRunner{}, &mockMCPClient{}, nil)
	shiro := agent.NewShiroAgent(provider, &mockToolRunner{}, &mockMCPClient{}, "", nil)
	orch := orchestrator.NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	_, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("hello"))
	if err == nil {
		t.Fatal("expected error for LLM failure")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error should propagate LLM error, got: %v", err)
	}
}

func TestIntegration_WebSearchTriggered(t *testing.T) {
	searchCalled := false
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			if toolName == "web_search" {
				searchCalled = true
				return "search results for Go", nil
			}
			return "", nil
		},
	}

	var capturedMessages []llm.Message
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedMessages = req.Messages
			return llm.GenerateResponse{Content: "answer with search"}, nil
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, toolRunner, &mockMCPClient{}, nil)
	shiro := agent.NewShiroAgent(provider, toolRunner, &mockMCPClient{}, "", nil)
	repo := newMockSessionRepo()
	orch := orchestrator.NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	// "検索して" は明示的な検索語 → needsWebSearch=true で発火する
	resp, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("Go言語について検索して"))
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if !searchCalled {
		t.Error("web_search should be triggered for '検索して' keyword")
	}
	// Verify search results were injected
	hasSearchContext := false
	for _, msg := range capturedMessages {
		if strings.Contains(msg.Content, "Web検索の結果") {
			hasSearchContext = true
		}
	}
	if !hasSearchContext {
		t.Error("search results should be injected into LLM context")
	}
	if resp.Response != "answer with search" {
		t.Errorf("response: want 'answer with search', got %q", resp.Response)
	}
}

func (m *mockToolRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	result, err := m.Execute(ctx, toolName, args)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, err.Error(), nil), nil
	}
	return tool.NewSuccess(result), nil
}
