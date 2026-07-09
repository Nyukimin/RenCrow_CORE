package routing

import (
	"context"
	"fmt"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// mockLLMProvider はテスト用のLLMプロバイダー
type mockLLMProvider struct {
	response string
	err      error
}

func (m *mockLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if m.err != nil {
		return llm.GenerateResponse{}, m.err
	}
	return llm.GenerateResponse{
		Content:    m.response,
		TokensUsed: 100,
	}, nil
}

func (m *mockLLMProvider) Name() string {
	return "mock-llm"
}

func TestNewLLMClassifier(t *testing.T) {
	mock := &mockLLMProvider{response: "CHAT"}
	classifier := NewLLMClassifier(mock, "test prompt")

	if classifier == nil {
		t.Fatal("NewLLMClassifier should not return nil")
	}
}

func TestLLMClassifier_Classify_AllRoutes(t *testing.T) {
	tests := []struct {
		response string
		want     routing.Route
	}{
		{"CHAT", routing.RouteCHAT},
		{"PLAN", routing.RoutePLAN},
		{"ANALYZE", routing.RouteANALYZE},
		{"OPS", routing.RouteOPS},
		{"RESEARCH", routing.RouteRESEARCH},
		{"WILD", routing.RouteWILD},
		{"CODE", routing.RouteCODE},
		{"CODE1", routing.RouteCODE1},
		{"CODE2", routing.RouteCODE2},
		{"CODE3", routing.RouteCODE3},
		{"CODE4", routing.RouteCODE4},
	}

	for _, tt := range tests {
		t.Run(tt.response, func(t *testing.T) {
			classifier := NewLLMClassifier(&mockLLMProvider{response: tt.response}, "test prompt")

			decision, err := classifier.Classify(context.Background(), task.NewTask(task.NewJobID(), "test", "line", "U123"))
			if err != nil {
				t.Fatalf("Classify failed: %v", err)
			}
			if decision.Route != tt.want {
				t.Fatalf("route = %s, want %s", decision.Route, tt.want)
			}
			if decision.Evidence[0].Source != routing.EvidenceSourceClassifier {
				t.Fatalf("source = %s, want classifier", decision.Evidence[0].Source)
			}
		})
	}
}

func TestLLMClassifier_ParseRoute_LongestCodeRouteFirst(t *testing.T) {
	classifier := NewLLMClassifier(&mockLLMProvider{}, "test prompt")

	tests := []struct {
		response string
		want     routing.Route
	}{
		{"route: CODE4", routing.RouteCODE4},
		{"route: CODE3", routing.RouteCODE3},
		{"route: CODE2", routing.RouteCODE2},
		{"route: CODE1", routing.RouteCODE1},
		{"route: CODE", routing.RouteCODE},
	}
	for _, tt := range tests {
		t.Run(tt.response, func(t *testing.T) {
			if got := classifier.parseRoute(tt.response); got != tt.want {
				t.Fatalf("parseRoute(%q) = %s, want %s", tt.response, got, tt.want)
			}
		})
	}
}

func TestLLMClassifier_Classify_CHAT(t *testing.T) {
	mock := &mockLLMProvider{response: "CHAT"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "こんにちは、調子はどう？", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteCHAT {
		t.Errorf("Expected route CHAT, got '%s'", decision.Route)
	}

	if decision.Confidence < 0.6 {
		t.Errorf("Expected confidence >= 0.6, got %f", decision.Confidence)
	}

	if decision.Reason == "" {
		t.Error("Reason should not be empty")
	}
	if len(decision.Evidence) != 1 {
		t.Fatalf("evidence count=%d, want 1", len(decision.Evidence))
	}
	if decision.Evidence[0].Source != routing.EvidenceSourceClassifier || !decision.Evidence[0].Matched {
		t.Fatalf("unexpected classifier evidence: %#v", decision.Evidence[0])
	}
}

func TestLLMClassifier_Classify_CODE(t *testing.T) {
	mock := &mockLLMProvider{response: "CODE"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "新しい機能を追加したい", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteCODE {
		t.Errorf("Expected route CODE, got '%s'", decision.Route)
	}
}

func TestLLMClassifier_Classify_PLAN(t *testing.T) {
	mock := &mockLLMProvider{response: "PLAN"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "この機能の実装アプローチを考えたい", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RoutePLAN {
		t.Errorf("Expected route PLAN, got '%s'", decision.Route)
	}
}

func TestLLMClassifier_Classify_ANALYZE(t *testing.T) {
	mock := &mockLLMProvider{response: "ANALYZE"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "このエラーの原因を特定したい", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteANALYZE {
		t.Errorf("Expected route ANALYZE, got '%s'", decision.Route)
	}
}

func TestLLMClassifier_Classify_OPS(t *testing.T) {
	mock := &mockLLMProvider{response: "OPS"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "ログを確認したい", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteOPS {
		t.Errorf("Expected route OPS, got '%s'", decision.Route)
	}
}

func TestLLMClassifier_Classify_RESEARCH(t *testing.T) {
	mock := &mockLLMProvider{response: "RESEARCH"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "Goのベストプラクティスを知りたい", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteRESEARCH {
		t.Errorf("Expected route RESEARCH, got '%s'", decision.Route)
	}
}

func TestLLMClassifier_Classify_InvalidRoute(t *testing.T) {
	// LLMが無効なルート名を返した場合、CHATにフォールバック
	mock := &mockLLMProvider{response: "INVALID_ROUTE"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "テストメッセージ", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteCHAT {
		t.Errorf("Expected fallback to CHAT, got '%s'", decision.Route)
	}

	if decision.Confidence > 0.5 {
		t.Errorf("Expected low confidence for invalid route, got %f", decision.Confidence)
	}
	if len(decision.Evidence) != 1 {
		t.Fatalf("evidence count=%d, want 1", len(decision.Evidence))
	}
	if decision.Evidence[0].Source != routing.EvidenceSourceSafeFallback || !decision.Evidence[0].Matched {
		t.Fatalf("unexpected fallback evidence: %#v", decision.Evidence[0])
	}
}

func TestLLMClassifier_Classify_CODE1(t *testing.T) {
	mock := &mockLLMProvider{response: "CODE1"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "仕様を設計して", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteCODE1 {
		t.Errorf("Expected route CODE1, got '%s'", decision.Route)
	}
}

func TestLLMClassifier_Classify_CODE2(t *testing.T) {
	mock := &mockLLMProvider{response: "CODE2"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "コードを実装して", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteCODE2 {
		t.Errorf("Expected route CODE2, got '%s'", decision.Route)
	}
}

func TestLLMClassifier_Classify_CODE3(t *testing.T) {
	mock := &mockLLMProvider{response: "CODE3"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "高品質なコードレビューをして", "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteCODE3 {
		t.Errorf("Expected route CODE3, got '%s'", decision.Route)
	}
}

func TestLLMClassifier_Classify_LLMError(t *testing.T) {
	// LLMがエラーを返した場合
	mock := &mockLLMProvider{err: fmt.Errorf("LLM error")}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "テストメッセージ", "line", "U123")

	_, err := classifier.Classify(context.Background(), testTask)
	if err == nil {
		t.Error("Expected error when LLM fails")
	}
}

func TestLLMClassifier_Classify_MultilineMessage(t *testing.T) {
	mock := &mockLLMProvider{response: "CODE"}
	classifier := NewLLMClassifier(mock, "test prompt")

	jobID := task.NewJobID()
	multilineMessage := `このファイルに以下の機能を追加して：
1. ユーザー認証
2. ログイン機能
3. セッション管理`
	testTask := task.NewTask(jobID, multilineMessage, "line", "U123")

	decision, err := classifier.Classify(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if decision.Route != routing.RouteCODE {
		t.Errorf("Expected route CODE, got '%s'", decision.Route)
	}
}
