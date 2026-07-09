package integration_test

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	infraRouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
)

// === Mock KBManager ===

type mockKBManager struct {
	saveWebSearchCalled bool
	savedDomain         string
	savedQuery          string
	savedResults        []agent.WebSearchResult
	searchKBCalled      bool
}

func (m *mockKBManager) SaveWebSearchToKB(ctx context.Context, domain string, query string, results []agent.WebSearchResult) error {
	m.saveWebSearchCalled = true
	m.savedDomain = domain
	m.savedQuery = query
	m.savedResults = results
	return nil
}

func (m *mockKBManager) SearchKB(ctx context.Context, domain string, query string, topK int) ([]*conversation.Document, error) {
	m.searchKBCalled = true
	return []*conversation.Document{}, nil
}

// === Tests ===

// TestKBAutosave_WebSearch_SavesCalled は、Web検索実行時にKB保存が呼ばれることを検証
func TestKBAutosave_WebSearch_SavesCalled(t *testing.T) {
	mockConvMgr := &mockKBManager{}

	// ToolRunner: web_search を実行すると ExecuteV2 で構造化データを返す
	toolRunner := &mockToolRunnerWithV2{
		mockToolRunner: &mockToolRunner{
			executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
				if toolName == "web_search" {
					return "検索結果:\n1. Rustプログラミング言語\n   https://www.rust-lang.org/", nil
				}
				return "", nil
			},
		},
		executeV2Func: func(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
			if toolName == "web_search" {
				metadata := map[string]any{
					"query": args["query"],
					"search_items": []any{
						map[string]any{
							"title":   "Rustプログラミング言語",
							"link":    "https://www.rust-lang.org/",
							"snippet": "Rust is a systems programming language...",
						},
					},
					"total_count": 1,
				}
				resp := tool.NewSuccess("検索結果:\n1. Rustプログラミング言語\n   https://www.rust-lang.org/")
				resp.Metadata = metadata
				return resp, nil
			}
			return tool.NewSuccess("tool result"), nil
		},
	}

	// LLM Provider
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "Rustは安全性と速度を兼ね備えた言語です"}, nil
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, toolRunner, &mockMCPClient{}, nil)
	mio = mio.WithKBManager(mockConvMgr) // KB自動保存を有効化

	shiro := agent.NewShiroAgent(provider, toolRunner, &mockMCPClient{}, "", nil)
	repo := newMockSessionRepo()
	orch := orchestrator.NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	// "検索して" キーワードでweb_search が自動実行される
	_, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("Rustについて検索して"))
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// KB保存が呼ばれたことを確認
	if !mockConvMgr.saveWebSearchCalled {
		t.Error("Expected SaveWebSearchToKB to be called, but it wasn't")
	}

	// 引数の検証（"Rustについて検索して" → "programming" domain）
	if mockConvMgr.savedDomain != "programming" {
		t.Errorf("Expected domain 'programming', got '%s'", mockConvMgr.savedDomain)
	}

	if mockConvMgr.savedQuery == "" {
		t.Error("Expected non-empty query")
	}

	if len(mockConvMgr.savedResults) == 0 {
		t.Error("Expected non-empty search results")
	}
}

// TestKBAutosave_NoKBManager_GracefulDegradation は、
// KBManager=nil の場合でもエラーにならないことを検証
func TestKBAutosave_NoKBManager_GracefulDegradation(t *testing.T) {
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			if toolName == "web_search" {
				return "検索結果", nil
			}
			return "", nil
		},
	}

	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "Goは高速な言語です"}, nil
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, toolRunner, &mockMCPClient{}, nil)
	// WithKBManager を呼ばない（nil のまま）

	shiro := agent.NewShiroAgent(provider, toolRunner, &mockMCPClient{}, "", nil)
	repo := newMockSessionRepo()
	orch := orchestrator.NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	// エラーにならないことを確認
	_, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("Goについて教えて"))
	if err != nil {
		t.Fatalf("Expected no error even without ConversationManager, got: %v", err)
	}
}

// TestKBAutosave_MetadataExtraction は、
// Metadata から構造化データが正しく抽出されることを検証
func TestKBAutosave_MetadataExtraction(t *testing.T) {
	mockConvMgr := &mockKBManager{}

	// ExecuteV2 で構造化データを返す拡張 ToolRunner
	toolRunner := &mockToolRunnerWithV2{
		mockToolRunner: &mockToolRunner{
			executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
				return "検索結果", nil
			},
		},
		executeV2Func: func(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
			if toolName == "web_search" {
				metadata := map[string]any{
					"query": args["query"],
					"search_items": []any{
						map[string]any{
							"title":   "Python公式",
							"link":    "https://www.python.org/",
							"snippet": "Python is a programming language...",
						},
					},
					"total_count": 1,
				}
				resp := tool.NewSuccess("検索完了")
				resp.Metadata = metadata
				return resp, nil
			}
			return tool.NewSuccess("tool result"), nil
		},
	}

	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "Pythonは汎用言語です"}, nil
		},
	}

	ruleDict := infraRouting.NewRuleDictionary()
	mio := agent.NewMioAgent(provider, &mockClassifier{}, ruleDict, toolRunner, &mockMCPClient{}, nil)
	mio = mio.WithKBManager(mockConvMgr)

	shiro := agent.NewShiroAgent(provider, toolRunner, &mockMCPClient{}, "", nil)
	repo := newMockSessionRepo()
	orch := orchestrator.NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	_, err := orch.ProcessMessage(context.Background(), defaultIntegrationReq("Pythonについて検索して"))
	if err != nil {
		t.Fatalf("Error: %v", err)
	}

	// Metadata 検証
	if !mockConvMgr.saveWebSearchCalled {
		t.Fatal("SaveWebSearchToKB not called")
	}

	if len(mockConvMgr.savedResults) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(mockConvMgr.savedResults))
	}

	result := mockConvMgr.savedResults[0]
	if result.Title != "Python公式" {
		t.Errorf("Expected title 'Python公式', got '%s'", result.Title)
	}
	if result.Link != "https://www.python.org/" {
		t.Errorf("Expected link 'https://www.python.org/', got '%s'", result.Link)
	}
	if result.Snippet != "Python is a programming language..." {
		t.Errorf("Expected specific snippet, got '%s'", result.Snippet)
	}
}

// === Helper: mockToolRunnerWithV2 ===

type mockToolRunnerWithV2 struct {
	*mockToolRunner
	executeV2Func func(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error)
}

func (m *mockToolRunnerWithV2) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if m.executeV2Func != nil {
		return m.executeV2Func(ctx, toolName, args)
	}
	// フォールバック: Execute を呼んで ToolResponse に変換
	result, err := m.Execute(ctx, toolName, args)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, err.Error(), nil), nil
	}
	return tool.NewSuccess(result), nil
}
