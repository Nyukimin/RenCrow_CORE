package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// Mock LLMProvider
type mockLLMProvider struct {
	generateFunc func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error)
}

func (m *mockLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, req)
	}
	return llm.GenerateResponse{Content: "Mock response"}, nil
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

// Mock Classifier
type mockClassifier struct {
	classifyFunc func(ctx context.Context, t task.Task) (routing.Decision, error)
}

func (m *mockClassifier) Classify(ctx context.Context, t task.Task) (routing.Decision, error) {
	if m.classifyFunc != nil {
		return m.classifyFunc(ctx, t)
	}
	return routing.NewDecision(routing.RouteCHAT, 0.8, "Mock classification"), nil
}

// Mock RuleDictionary
type mockRuleDictionary struct {
	matchFunc func(t task.Task) (routing.Route, float64, bool)
}

func (m *mockRuleDictionary) Match(t task.Task) (routing.Route, float64, bool) {
	if m.matchFunc != nil {
		return m.matchFunc(t)
	}
	return "", 0.0, false
}

func TestMioAgentDecideAction_ExplicitCommand(t *testing.T) {
	mio := NewMioAgent(
		&mockLLMProvider{},
		&mockClassifier{},
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		nil, // conversationEngine=nil（テスト環境）
	)

	tests := []struct {
		message       string
		expectedRoute routing.Route
	}{
		{"/chat hello", routing.RouteCHAT},
		{"/plan create project", routing.RoutePLAN},
		{"/analyze logs", routing.RouteANALYZE},
		{"/heavy logs", routing.RouteANALYZE},
		{"/ops deploy", routing.RouteOPS},
		{"/research topic", routing.RouteRESEARCH},
		{"/wild image prompt", routing.RouteWILD},
		{"/code", routing.RouteCODE},
		{"/code fix bug", routing.RouteCODE},
		{"/code1 design spec", routing.RouteCODE1},
		{"/code2 implement feature", routing.RouteCODE2},
		{"/code3 implement feature", routing.RouteCODE3},
		{"/code4 prototype feature", routing.RouteCODE4},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			jobID := task.NewJobID()
			testTask := task.NewTask(jobID, tt.message, "line", "U123")

			decision, err := mio.DecideAction(context.Background(), testTask)
			if err != nil {
				t.Fatalf("DecideAction failed: %v", err)
			}

			if decision.Route != tt.expectedRoute {
				t.Errorf("Expected route %s, got %s", tt.expectedRoute, decision.Route)
			}

			if decision.Confidence != 1.0 {
				t.Errorf("Expected confidence 1.0 for explicit command, got %f", decision.Confidence)
			}
			if len(decision.Evidence) == 0 || decision.Evidence[0].Source != routing.EvidenceSourceExplicitCommand {
				t.Fatalf("explicit command evidence missing: %#v", decision.Evidence)
			}
			if !decision.Evidence[0].Matched || decision.Evidence[0].Route != tt.expectedRoute {
				t.Fatalf("unexpected explicit command evidence: %#v", decision.Evidence[0])
			}
		})
	}
}

func TestMioAgentDecideAction_RuleDictionary(t *testing.T) {
	ruleDictionary := &mockRuleDictionary{
		matchFunc: func(t task.Task) (routing.Route, float64, bool) {
			if t.UserMessage() == "ファイルを作成" {
				return routing.RouteCODE, 0.95, true
			}
			return "", 0.0, false
		},
	}

	mio := NewMioAgent(
		&mockLLMProvider{},
		&mockClassifier{},
		ruleDictionary,
		&mockToolRunner{},
		&mockMCPClient{},
		nil, // conversationEngine=nil（テスト環境）
	)

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "ファイルを作成", "line", "U123")

	decision, err := mio.DecideAction(context.Background(), testTask)
	if err != nil {
		t.Fatalf("DecideAction failed: %v", err)
	}

	if decision.Route != routing.RouteCODE {
		t.Errorf("Expected route CODE, got %s", decision.Route)
	}

	if decision.Confidence != 0.95 {
		t.Errorf("Expected confidence 0.95, got %f", decision.Confidence)
	}
	if len(decision.Evidence) < 2 {
		t.Fatalf("expected explicit and rule evidence, got %#v", decision.Evidence)
	}
	if decision.Evidence[0].Source != routing.EvidenceSourceExplicitCommand || decision.Evidence[0].Matched {
		t.Fatalf("unexpected explicit evidence: %#v", decision.Evidence[0])
	}
	if decision.Evidence[1].Source != routing.EvidenceSourceRuleDictionary || !decision.Evidence[1].Matched {
		t.Fatalf("unexpected rule evidence: %#v", decision.Evidence[1])
	}
}

func TestMioAgentDecideAction_ClassifierWhenNoRuleMatch(t *testing.T) {
	// ルール辞書にマッチしない場合、LLM分類器で次の経路を判定する。
	classifierCalled := false
	classifier := &mockClassifier{
		classifyFunc: func(ctx context.Context, tk task.Task) (routing.Decision, error) {
			classifierCalled = true
			return routing.NewDecisionWithEvidence(routing.RouteCODE2, 0.8, "classifier selected code",
				routing.DecisionEvidence{
					Source:     routing.EvidenceSourceClassifier,
					Matched:    true,
					Route:      routing.RouteCODE2,
					Confidence: 0.8,
					Reason:     "implementation request",
				},
			), nil
		},
	}

	mio := NewMioAgent(
		&mockLLMProvider{},
		classifier,
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		nil, // conversationEngine=nil（テスト環境）
	)

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "Worker/Coder経路に届くか確認してください", "line", "U123")

	decision, err := mio.DecideAction(context.Background(), testTask)
	if err != nil {
		t.Fatalf("DecideAction failed: %v", err)
	}

	if decision.Route != routing.RouteCODE2 {
		t.Errorf("Expected route CODE2, got %s", decision.Route)
	}

	if decision.Confidence != 0.8 {
		t.Errorf("Expected confidence 0.8, got %f", decision.Confidence)
	}

	if !classifierCalled {
		t.Error("Classifier should be called after rule dictionary miss")
	}
	if len(decision.Evidence) != 3 {
		t.Fatalf("evidence count=%d, want 3: %#v", len(decision.Evidence), decision.Evidence)
	}
	wantSources := []string{
		routing.EvidenceSourceExplicitCommand,
		routing.EvidenceSourceRuleDictionary,
		routing.EvidenceSourceClassifier,
	}
	for i, source := range wantSources {
		if decision.Evidence[i].Source != source {
			t.Fatalf("evidence[%d].source=%q, want %q", i, decision.Evidence[i].Source, source)
		}
	}
	if !decision.Evidence[2].Matched || decision.Evidence[2].Route != routing.RouteCODE2 {
		t.Fatalf("unexpected classifier evidence: %#v", decision.Evidence[2])
	}
}

func TestMioAgentDecideAction_DefaultChatWhenClassifierFails(t *testing.T) {
	classifier := &mockClassifier{
		classifyFunc: func(ctx context.Context, tk task.Task) (routing.Decision, error) {
			return routing.Decision{}, fmt.Errorf("classifier unavailable")
		},
	}

	mio := NewMioAgent(
		&mockLLMProvider{},
		classifier,
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		nil,
	)

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "こんにちは", "line", "U123")

	decision, err := mio.DecideAction(context.Background(), testTask)
	if err != nil {
		t.Fatalf("DecideAction failed: %v", err)
	}

	if decision.Route != routing.RouteCHAT {
		t.Errorf("Expected route CHAT, got %s", decision.Route)
	}
	if len(decision.Evidence) != 4 {
		t.Fatalf("evidence count=%d, want 4: %#v", len(decision.Evidence), decision.Evidence)
	}
	wantSources := []string{
		routing.EvidenceSourceExplicitCommand,
		routing.EvidenceSourceRuleDictionary,
		routing.EvidenceSourceClassifier,
		routing.EvidenceSourceSafeFallback,
	}
	for i, source := range wantSources {
		if decision.Evidence[i].Source != source {
			t.Fatalf("evidence[%d].source=%q, want %q", i, decision.Evidence[i].Source, source)
		}
	}
	if !decision.Evidence[3].Matched || decision.Evidence[3].Route != routing.RouteCHAT {
		t.Fatalf("unexpected fallback evidence: %#v", decision.Evidence[3])
	}
}

func TestMioAgentChat(t *testing.T) {
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content:      "こんにちは！何かお手伝いできますか？",
				TokensUsed:   20,
				FinishReason: "stop",
			}, nil
		},
	}

	mio := NewMioAgent(
		llmProvider,
		&mockClassifier{},
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		nil, // conversationEngine=nil（テスト環境）
	)

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "こんにちは", "line", "U123")

	response, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if response != "こんにちは！何かお手伝いできますか？" {
		t.Errorf("Unexpected chat response: %s", response)
	}
}

func TestMioAgentChat_UsesSystemPrompt(t *testing.T) {
	var gotMessages []llm.Message
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			gotMessages = req.Messages
			return llm.GenerateResponse{Content: "了解しました"}, nil
		},
	}

	mio := NewMioAgent(
		llmProvider,
		&mockClassifier{},
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		nil,
	).WithSystemPrompt("Mio system prompt")

	_, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "こんにちは", "line", "U123"))
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if len(gotMessages) == 0 {
		t.Fatal("expected messages")
	}
	if gotMessages[0].Role != "system" || gotMessages[0].Content != "Mio system prompt" {
		t.Fatalf("expected system prompt first, got %#v", gotMessages[0])
	}
}

func TestMioAgentChat_UsesViewerRecipientSystemPromptWithoutChangingUserMessage(t *testing.T) {
	var gotReq llm.GenerateRequest
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			gotReq = req
			return llm.GenerateResponse{Content: "了解しました"}, nil
		},
	}
	mio := NewMioAgent(
		llmProvider,
		&mockClassifier{},
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		nil,
	)

	task := task.NewTask(task.NewJobID(), "合言葉 RC_kuro_current で返答して", "viewer", "viewer-user").WithViewerRecipient("kuro")
	if _, err := mio.Chat(context.Background(), task); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	foundRecipientPrompt := false
	foundTokenGuard := false
	if len(gotReq.Messages) == 0 {
		t.Fatal("expected messages")
	}
	for _, msg := range gotReq.Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "requested_to=kuro") {
			foundRecipientPrompt = true
		}
		if msg.Role == "system" && strings.Contains(msg.Content, "Token contract") {
			foundTokenGuard = true
		}
	}
	if !foundRecipientPrompt {
		t.Fatalf("missing recipient prompt in messages: %#v", gotReq.Messages)
	}
	if !foundTokenGuard {
		t.Fatalf("missing token guard prompt in messages: %#v", gotReq.Messages)
	}
	last := gotReq.Messages[len(gotReq.Messages)-1]
	if last.Role != "user" || last.Content != "合言葉 RC_kuro_current で返答して" {
		t.Fatalf("user message changed: %#v", last)
	}
}

func TestMioAgentChat_UsesFullShiroPromptForShiroChat(t *testing.T) {
	var gotReq llm.GenerateRequest
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			gotReq = req
			return llm.GenerateResponse{Content: "Shiroです"}, nil
		},
	}
	mio := NewMioAgent(
		llmProvider,
		&mockClassifier{},
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		nil,
	).WithSystemPrompt("Mio full prompt").WithViewerRecipientPrompts(map[string]string{
		"shiro": "Shiro full prompt",
	})

	task := task.NewTask(task.NewJobID(), "名前を答えて", "viewer", "viewer-user").WithViewerRecipient("shiro")
	if _, err := mio.Chat(context.Background(), task); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if len(gotReq.Messages) == 0 || gotReq.Messages[0].Role != "system" || gotReq.Messages[0].Content != "Shiro full prompt" {
		t.Fatalf("expected full Shiro prompt first, got %#v", gotReq.Messages)
	}
	for _, msg := range gotReq.Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "Mio full prompt") {
			t.Fatalf("Mio prompt leaked into Shiro chat: %#v", gotReq.Messages)
		}
	}
}

// === mockConversationEngine ===

type mockConversationEngine struct {
	beginTurnFunc func(ctx context.Context, sessionID string, userMessage string) (*conversation.RecallPack, error)
	endTurnFunc   func(ctx context.Context, sessionID string, userMessage string, response string) error
	flushFunc     func(ctx context.Context, sessionID string) error
	statusFunc    func(ctx context.Context, sessionID string) (*conversation.ConversationStatus, error)
	resetFunc     func(ctx context.Context, sessionID string) error
	persona       conversation.PersonaState
}

func (m *mockConversationEngine) BeginTurn(ctx context.Context, sessionID string, userMessage string) (*conversation.RecallPack, error) {
	if m.beginTurnFunc != nil {
		return m.beginTurnFunc(ctx, sessionID, userMessage)
	}
	return &conversation.RecallPack{Persona: m.persona}, nil
}

func (m *mockConversationEngine) EndTurn(ctx context.Context, sessionID string, userMessage string, response string) error {
	if m.endTurnFunc != nil {
		return m.endTurnFunc(ctx, sessionID, userMessage, response)
	}
	return nil
}

func (m *mockConversationEngine) GetPersona() conversation.PersonaState { return m.persona }
func (m *mockConversationEngine) FlushCurrentThread(ctx context.Context, sessionID string) error {
	if m.flushFunc != nil {
		return m.flushFunc(ctx, sessionID)
	}
	return nil
}
func (m *mockConversationEngine) GetStatus(ctx context.Context, sessionID string) (*conversation.ConversationStatus, error) {
	if m.statusFunc != nil {
		return m.statusFunc(ctx, sessionID)
	}
	return &conversation.ConversationStatus{}, nil
}
func (m *mockConversationEngine) ResetSession(ctx context.Context, sessionID string) error {
	if m.resetFunc != nil {
		return m.resetFunc(ctx, sessionID)
	}
	return nil
}

// === Phase 1C: ConversationEngine integration tests ===

func TestMioAgent_Chat_WithConversationEngine(t *testing.T) {
	beginCalled := false
	endCalled := false

	engine := &mockConversationEngine{
		beginTurnFunc: func(ctx context.Context, sessionID, msg string) (*conversation.RecallPack, error) {
			beginCalled = true
			return &conversation.RecallPack{
				Persona: conversation.PersonaState{SystemPrompt: "You are Mio."},
				ShortContext: []conversation.Message{
					{Speaker: conversation.SpeakerUser, Msg: "previous msg"},
				},
			}, nil
		},
		endTurnFunc: func(ctx context.Context, sessionID, msg, resp string) error {
			endCalled = true
			return nil
		},
	}

	var capturedReq llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{Content: "response"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine)
	testTask := task.NewTask(task.NewJobID(), "hello", "line", "U123")

	resp, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp != "response" {
		t.Errorf("response: want 'response', got %q", resp)
	}
	if !beginCalled {
		t.Error("BeginTurn should have been called")
	}
	if !endCalled {
		t.Error("EndTurn should have been called")
	}
	// Verify RecallPack was injected: system prompt + short context + user message
	if len(capturedReq.Messages) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(capturedReq.Messages))
	}
	if capturedReq.Messages[0].Role != "system" {
		t.Errorf("msg[0] role: want 'system', got %q", capturedReq.Messages[0].Role)
	}
}

func TestMioAgent_Chat_UsesConfiguredGenerationOptions(t *testing.T) {
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = req
			return llm.GenerateResponse{Content: "ok"}, nil
		},
	}
	topP := 0.9
	topK := 40
	minP := 0.0
	enableThinking := false
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil).
		WithGenerationOptions(MioGenerationOptions{
			Stream:         true,
			MaxTokens:      256,
			Temperature:    0.3,
			TopP:           &topP,
			TopK:           &topK,
			MinP:           &minP,
			EnableThinking: &enableThinking,
		})

	if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "hello", "line", "U123")); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if captured.MaxTokens != 256 || captured.Temperature != 0.3 {
		t.Fatalf("unexpected generation basics: %+v", captured)
	}
	if captured.OnToken == nil {
		t.Fatal("configured stream=true should select the streaming provider path")
	}
	if captured.ProviderOptions["top_p"] != 0.9 || captured.ProviderOptions["top_k"] != 40 || captured.ProviderOptions["min_p"] != 0.0 {
		t.Fatalf("unexpected provider options: %#v", captured.ProviderOptions)
	}
	kwargs, ok := captured.ProviderOptions["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("unexpected chat_template_kwargs: %#v", captured.ProviderOptions)
	}
	if _, exists := captured.ProviderOptions["seed"]; exists {
		t.Fatalf("nil seed should be omitted: %#v", captured.ProviderOptions)
	}
}

func TestMioAgent_Chat_AppliesChatRecallRoleFilter(t *testing.T) {
	engine := &mockConversationEngine{
		beginTurnFunc: func(ctx context.Context, sessionID, msg string) (*conversation.RecallPack, error) {
			return &conversation.RecallPack{
				Persona: conversation.PersonaState{SystemPrompt: "You are Mio."},
				MidSummaries: []conversation.ThreadSummary{
					{Summary: "chat memory", Roles: []string{"chat"}},
					{Summary: "worker memory", Roles: []string{"worker"}},
				},
				SearchCacheSnippets: []conversation.SearchCacheSnippet{
					{Query: "chat search", Roles: []string{"chat"}},
					{Query: "wild search", Roles: []string{"wild"}},
				},
			}, nil
		},
	}

	var capturedReq llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{Content: "response"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine)
	if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "hello", "line", "U123")); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	var prompt strings.Builder
	for _, msg := range capturedReq.Messages {
		prompt.WriteString(msg.Content)
		prompt.WriteString("\n")
	}
	got := prompt.String()
	if !strings.Contains(got, "chat memory") {
		t.Fatalf("chat role recall should be included, got:\n%s", got)
	}
	if !strings.Contains(got, "chat search") {
		t.Fatalf("chat role search cache should be included, got:\n%s", got)
	}
	if strings.Contains(got, "worker memory") || strings.Contains(got, "wild search") {
		t.Fatalf("non-chat role recall should be filtered, got:\n%s", got)
	}
}

func TestMioAgent_Chat_ConversationEngine_BeginTurnError(t *testing.T) {
	engine := &mockConversationEngine{
		beginTurnFunc: func(ctx context.Context, sessionID, msg string) (*conversation.RecallPack, error) {
			return nil, fmt.Errorf("redis down")
		},
	}

	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "fallback response"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine)
	testTask := task.NewTask(task.NewJobID(), "hello", "line", "U123")

	resp, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat should succeed even when BeginTurn fails: %v", err)
	}
	if resp != "fallback response" {
		t.Errorf("response: want 'fallback response', got %q", resp)
	}
}

func TestMioAgent_Chat_ConversationEngine_EndTurnError(t *testing.T) {
	engine := &mockConversationEngine{
		endTurnFunc: func(ctx context.Context, sessionID, msg, resp string) error {
			return fmt.Errorf("storage failure")
		},
	}

	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "my response"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, engine)
	testTask := task.NewTask(task.NewJobID(), "hello", "line", "U123")

	resp, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat should succeed even when EndTurn fails: %v", err)
	}
	if resp != "my response" {
		t.Errorf("response should still be returned: want 'my response', got %q", resp)
	}
}

// === Web search tests ===

func TestMioAgent_Chat_WebSearchTriggered(t *testing.T) {
	searchCalled := false
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			if toolName == "web_search" {
				searchCalled = true
				return "search results", nil
			}
			return "", nil
		},
	}

	var capturedReq llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{Content: "answer"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil)
	testTask := task.NewTask(task.NewJobID(), "Go言語を検索して", "line", "U123")

	_, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if !searchCalled {
		t.Error("web_search should have been called for explicit search request")
	}
	// Verify search results injected into messages
	hasSearchContext := false
	for _, msg := range capturedReq.Messages {
		if strings.Contains(msg.Content, "Web検索の結果") {
			hasSearchContext = true
			break
		}
	}
	if !hasSearchContext {
		t.Error("search results should be injected into LLM messages")
	}
}

func TestMioAgent_Chat_WebSearchNotTriggered(t *testing.T) {
	searchCalled := false
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			if toolName == "web_search" {
				searchCalled = true
			}
			return "", nil
		},
	}

	provider := &mockLLMProvider{}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil)
	testTask := task.NewTask(task.NewJobID(), "こんにちは", "line", "U123")

	_, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if searchCalled {
		t.Error("web_search should NOT be called for simple greeting")
	}
}

func TestMioAgent_Chat_WebSearchNotTriggeredForTimelyKeywordOnly(t *testing.T) {
	searchCalled := false
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			if toolName == "web_search" {
				searchCalled = true
			}
			return "", nil
		},
	}

	provider := &mockLLMProvider{}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil)
	testTask := task.NewTask(task.NewJobID(), "今日のニュースについて教えて", "line", "U123")

	_, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if searchCalled {
		t.Error("web_search should NOT be called for timely keyword without explicit search instruction")
	}
}

func TestMioAgent_Chat_WebSearchNotTriggeredForMemoryRecallQuestion(t *testing.T) {
	searchCalled := false
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			if toolName == "web_search" {
				searchCalled = true
			}
			return "", nil
		},
	}

	provider := &mockLLMProvider{}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil)
	testTask := task.NewTask(task.NewJobID(), "俺が映画が好きってこと知ってる？", "viewer", "viewer-user")

	_, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if searchCalled {
		t.Error("web_search should NOT be called for user-memory recall question")
	}
}

func TestMioAgent_Chat_WebSearchError(t *testing.T) {
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			return "", fmt.Errorf("API error")
		},
	}

	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "response without search"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil)
	testTask := task.NewTask(task.NewJobID(), "最新のニュースを検索して", "line", "U123")

	resp, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat should succeed even when web search fails: %v", err)
	}
	if resp != "response without search" {
		t.Errorf("response: want 'response without search', got %q", resp)
	}
}

func TestMioAgent_Chat_WebSearchUsesFreshCache(t *testing.T) {
	searchCalled := false
	toolRunner := &mockToolRunner{
		executeFunc: func(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
			if toolName == "web_search" {
				searchCalled = true
			}
			return "live search results", nil
		},
	}
	cache := &mockSearchCacheManager{
		hit: true,
		results: []WebSearchResult{
			{Title: "Cached RenCrow", Link: "https://example.com/cache", Snippet: "cached snippet"},
		},
	}

	var capturedReq llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{Content: "answer"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil).
		WithSearchCacheManager(cache)
	testTask := task.NewTask(task.NewJobID(), "RenCrow 最新仕様を検索して", "line", "U123")

	_, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if searchCalled {
		t.Error("web_search should not be called on fresh cache hit")
	}
	if cache.lastGetQuery != "RenCrow 最新仕様" {
		t.Fatalf("unexpected cache lookup query: %q", cache.lastGetQuery)
	}
	foundCached := false
	for _, msg := range capturedReq.Messages {
		if strings.Contains(msg.Content, "Cached RenCrow") && strings.Contains(msg.Content, "https://example.com/cache") {
			foundCached = true
			break
		}
	}
	if !foundCached {
		t.Fatalf("cached web search result was not injected: %+v", capturedReq.Messages)
	}
}

func TestMioAgent_Chat_WebSearchSavesCacheOnMiss(t *testing.T) {
	cache := &mockSearchCacheManager{}
	toolRunner := &mockToolRunner{
		executeV2Func: func(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
			if toolName != "web_search" {
				t.Fatalf("unexpected tool: %s", toolName)
			}
			resp := tool.NewSuccess("live search results")
			resp.Metadata = map[string]any{
				"search_items": []map[string]any{
					{"title": "Live RenCrow", "link": "https://example.com/live", "snippet": "live snippet"},
				},
			}
			return resp, nil
		},
	}

	provider := &mockLLMProvider{}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, toolRunner, &mockMCPClient{}, nil).
		WithSearchCacheManager(cache)
	testTask := task.NewTask(task.NewJobID(), "RenCrow 最新仕様を検索して", "line", "U123")

	if _, err := mio.Chat(context.Background(), testTask); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if !cache.saveCalled {
		t.Fatal("expected search cache save on live search miss")
	}
	if cache.lastSaveQuery != "RenCrow 最新仕様" {
		t.Fatalf("unexpected saved query: %q", cache.lastSaveQuery)
	}
	if len(cache.savedResults) != 1 || cache.savedResults[0].Title != "Live RenCrow" {
		t.Fatalf("unexpected saved results: %+v", cache.savedResults)
	}
	if cache.lastTTL <= 0 {
		t.Fatalf("expected positive cache ttl, got %s", cache.lastTTL)
	}
}

// === LLM error test ===

func TestMioAgent_Chat_LLMError(t *testing.T) {
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, fmt.Errorf("LLM unavailable")
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	testTask := task.NewTask(task.NewJobID(), "hello", "line", "U123")

	_, err := mio.Chat(context.Background(), testTask)
	if err == nil {
		t.Fatal("Chat should return error when LLM fails")
	}
	if !strings.Contains(err.Error(), "LLM unavailable") {
		t.Errorf("error should contain 'LLM unavailable', got: %v", err)
	}
}

// === Command parsing tests ===

func TestParseExplicitCommand_AllRoutes(t *testing.T) {
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)

	tests := []struct {
		message string
		route   routing.Route
	}{
		{"/chat hello", routing.RouteCHAT},
		{"/plan create project", routing.RoutePLAN},
		{"/analyze logs", routing.RouteANALYZE},
		{"/heavy logs", routing.RouteANALYZE},
		{"/ops deploy", routing.RouteOPS},
		{"/research topic", routing.RouteRESEARCH},
		{"/wild image prompt", routing.RouteWILD},
		{"/code fix bug", routing.RouteCODE},
		{"/code1 design spec", routing.RouteCODE1},
		{"/code2 implement feature", routing.RouteCODE2},
		{"/code3 review code", routing.RouteCODE3},
		{"/code4 prototype feature", routing.RouteCODE4},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			result := mio.parseExplicitCommand(tt.message)
			if result != tt.route {
				t.Errorf("parseExplicitCommand(%q): want %s, got %s", tt.message, tt.route, result)
			}
		})
	}
}

func TestParseExplicitCommand_PrefixOverlap(t *testing.T) {
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)

	// /code3 should match CODE3, not CODE
	result := mio.parseExplicitCommand("/code3 task")
	if result != routing.RouteCODE3 {
		t.Errorf("/code3 should match CODE3, got %s", result)
	}

	// /code4 should match CODE4, not CODE
	result = mio.parseExplicitCommand("/code4 task")
	if result != routing.RouteCODE4 {
		t.Errorf("/code4 should match CODE4, got %s", result)
	}
}

func TestParseExplicitCommand_RequiresCommandBoundary(t *testing.T) {
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)

	tests := []string{
		"/codebase を説明して",
		"/coder task",
		"/code3extra task",
		"/opslog 見せて",
		"/wildcard pattern",
		"/researcher profile",
	}
	for _, message := range tests {
		t.Run(message, func(t *testing.T) {
			if got := mio.parseExplicitCommand(message); got != "" {
				t.Fatalf("parseExplicitCommand(%q) = %s, want empty", message, got)
			}
		})
	}
}

func TestParseExplicitCommand_EmptyMessage(t *testing.T) {
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	result := mio.parseExplicitCommand("")
	if result != "" {
		t.Errorf("empty message should return empty route, got %s", result)
	}
}

func TestParseExplicitCommand_NoCommand(t *testing.T) {
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	result := mio.parseExplicitCommand("hello world")
	if result != "" {
		t.Errorf("non-command message should return empty route, got %s", result)
	}
}

func TestParseExplicitCommand_LeadingSpaces(t *testing.T) {
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	result := mio.parseExplicitCommand("  /chat hello")
	if result != routing.RouteCHAT {
		t.Errorf("leading spaces should be trimmed, got %s", result)
	}
}

// === cleanSearchQuery tests ===

func TestCleanSearchQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Go言語について教えて", "Go言語"},
		{"最新のニュースを検索して", "最新のニュース"},
		{"Rustとは", "Rust"},
		{"hello", "hello"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := cleanSearchQuery(tt.input)
			if got != tt.want {
				t.Errorf("cleanSearchQuery(%q): want %q, got %q", tt.input, tt.want, got)
			}
		})
	}
}

func TestInferDomain(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		// プログラミング関連
		{"Rustについて教えて", "programming"},
		{"Pythonのコード例", "programming"},
		{"Goの関数", "programming"},
		{"JavaScriptの変数", "programming"},
		{"アルゴリズムを調べて", "programming"},

		// エンターテイメント関連
		{"最新の映画", "entertainment"},
		{"人気のアニメ", "entertainment"},
		{"ゲームのレビュー", "entertainment"},
		{"音楽について", "entertainment"},

		// 料理関連
		{"カレーのレシピ", "cooking"},
		{"食材の選び方", "cooking"},
		{"レストラン情報", "cooking"},

		// 科学関連
		{"量子力学について", "science"},
		{"AIの技術", "science"},
		{"機械学習のアルゴリズム", "programming"}, // programming が優先

		// 一般
		{"天気について", "general"},
		{"ニュース", "general"},
		{"こんにちは", "general"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := inferDomain(tt.query)
			if got != tt.want {
				t.Errorf("inferDomain(%q): want %q, got %q", tt.query, tt.want, got)
			}
		})
	}
}

func TestWithKBManager(t *testing.T) {
	provider := &mockLLMProvider{}
	classifier := &mockClassifier{}
	ruleDict := &mockRuleDictionary{}
	toolRunner := &mockToolRunner{}
	mcpClient := &mockMCPClient{}

	mio := NewMioAgent(provider, classifier, ruleDict, toolRunner, mcpClient, nil)

	// WithKBManager should return the same agent instance
	mockConvMgr := &mockKBManager{}
	result := mio.WithKBManager(mockConvMgr)

	if result != mio {
		t.Error("WithKBManager should return the same agent instance")
	}

	// Verify the manager was set by checking if Process can use it
	// (This is indirectly verified through integration tests)
}

func TestMioAgentOptionSetters(t *testing.T) {
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)

	cacheManager := &mockSearchCacheManager{}
	if got := mio.WithSearchCacheManager(cacheManager); got != mio {
		t.Fatal("WithSearchCacheManager should return the same agent")
	}
	if mio.searchCacheManager != cacheManager {
		t.Fatal("search cache manager was not set")
	}

	userMemory := &mockUserMemoryManager{}
	if got := mio.WithUserMemoryManager(userMemory); got != mio {
		t.Fatal("WithUserMemoryManager should return the same agent")
	}
	if mio.userMemoryManager != userMemory {
		t.Fatal("user memory manager was not set")
	}

	editor := &mockPersonaEditor{}
	if got := mio.WithPersonaEditor(editor); got != mio {
		t.Fatal("WithPersonaEditor should return the same agent")
	}
	if mio.personaEditor != editor {
		t.Fatal("persona editor was not set")
	}

	provider := func(context.Context, int) (string, error) {
		return "recent context", nil
	}
	if got := mio.WithRecentContextProvider(provider); got != mio {
		t.Fatal("WithRecentContextProvider should return the same agent")
	}
	recent, err := mio.recentContext(context.Background(), 3)
	if err != nil || recent != "recent context" {
		t.Fatalf("unexpected recent context result: %q, %v", recent, err)
	}

	mio.WithSystemPrompt("  custom prompt  ")
	if mio.systemPrompt != "custom prompt" {
		t.Fatalf("system prompt should be trimmed, got %q", mio.systemPrompt)
	}
}

type mockCachedKBManager struct {
	mockKBManager
	mockSearchCacheManager
}

func TestWithKBManagerAlsoSetsSearchCacheWhenSupported(t *testing.T) {
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	manager := &mockCachedKBManager{}

	mio.WithKBManager(manager)

	if mio.kbManager != manager {
		t.Fatal("KB manager was not set")
	}
	if mio.searchCacheManager != manager {
		t.Fatal("search cache manager should be set from KB manager when supported")
	}
}

// mockKBManager は KBManager のモック
type mockKBManager struct {
	saveWebSearchCalled bool
	searchKBCalled      bool
}

func (m *mockKBManager) SaveWebSearchToKB(ctx context.Context, domain string, query string, results []WebSearchResult) error {
	m.saveWebSearchCalled = true
	return nil
}

func (m *mockKBManager) SearchKB(ctx context.Context, domain string, query string, topK int) ([]*conversation.Document, error) {
	m.searchKBCalled = true
	return []*conversation.Document{}, nil
}

type mockSearchCacheManager struct {
	hit           bool
	results       []WebSearchResult
	lastGetQuery  string
	saveCalled    bool
	lastSaveQuery string
	savedResults  []WebSearchResult
	lastTTL       time.Duration
}

func (m *mockSearchCacheManager) GetFreshWebSearchCache(_ context.Context, query string) ([]WebSearchResult, bool, error) {
	m.lastGetQuery = query
	if !m.hit {
		return nil, false, nil
	}
	return m.results, true, nil
}

func (m *mockSearchCacheManager) SaveWebSearchCache(_ context.Context, query string, results []WebSearchResult, ttl time.Duration) error {
	m.saveCalled = true
	m.lastSaveQuery = query
	m.savedResults = append([]WebSearchResult{}, results...)
	m.lastTTL = ttl
	return nil
}

// === Persona self-edit tests ===

// mockPersonaEditor はPersonaEditorのモック
type mockPersonaEditor struct {
	content    string
	readErr    error
	writeErr   error
	writeCalls int
	lastWrite  string
}

func (m *mockPersonaEditor) ReadPersona() (string, error) {
	if m.readErr != nil {
		return "", m.readErr
	}
	return m.content, nil
}

func (m *mockPersonaEditor) WritePersona(content string) error {
	m.writeCalls++
	m.lastWrite = content
	if m.writeErr != nil {
		return m.writeErr
	}
	m.content = content
	return nil
}

func TestDetectPersonaEditIntent(t *testing.T) {
	tests := []struct {
		message string
		want    bool
	}{
		// Should trigger (topic + action)
		{"口調をカジュアルにして", true},
		{"敬語やめて", true},
		{"ペルソナを修正して", true},
		{"話し方を変えて", true},
		{"キャラを調整して", true},
		{"もっとカジュアルにして", true},
		{"テンションを変えて", true},
		{"語尾を直して", true},
		// Should NOT trigger (topic only, no action)
		{"口調はどんな感じ？", false},
		{"ペルソナって何？", false},
		// Should NOT trigger (action only, no topic)
		{"設定を変えて", false},
		{"ファイルを修正して", false},
		// Should NOT trigger (unrelated)
		{"こんにちは", false},
		{"天気を教えて", false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := detectPersonaEditIntent(tt.message)
			if got != tt.want {
				t.Errorf("detectPersonaEditIntent(%q): want %v, got %v", tt.message, tt.want, got)
			}
		})
	}
}

func TestMioAgent_Chat_PersonaEdit(t *testing.T) {
	editor := &mockPersonaEditor{
		content: "旧ペルソナ設定",
	}

	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			// LLM should receive prompt with current persona + user request
			if !strings.Contains(req.Messages[0].Content, "旧ペルソナ設定") {
				t.Error("LLM prompt should contain current persona")
			}
			return llm.GenerateResponse{Content: "新ペルソナ設定"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	mio = mio.WithPersonaEditor(editor)

	testTask := task.NewTask(task.NewJobID(), "口調をカジュアルにして", "line", "U123")
	resp, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if !strings.Contains(resp, "ペルソナ設定を更新") {
		t.Errorf("response should confirm persona update, got %q", resp)
	}
	if editor.writeCalls != 1 {
		t.Errorf("WritePersona should be called once, called %d times", editor.writeCalls)
	}
	if editor.lastWrite != "新ペルソナ設定" {
		t.Errorf("written persona: want '新ペルソナ設定', got %q", editor.lastWrite)
	}
}

func TestMioAgent_Chat_PersonaEditFallback(t *testing.T) {
	// PersonaEditor が nil の場合は通常の会話として処理
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "通常の応答"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	// PersonaEditor is nil (not set)

	testTask := task.NewTask(task.NewJobID(), "口調をカジュアルにして", "line", "U123")
	resp, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat should succeed without PersonaEditor: %v", err)
	}
	if resp != "通常の応答" {
		t.Errorf("should fall back to normal chat, got %q", resp)
	}
}

func TestMioAgent_Chat_PersonaEditReadError(t *testing.T) {
	editor := &mockPersonaEditor{
		readErr: fmt.Errorf("file not found"),
	}

	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "通常の応答"}, nil
		},
	}

	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil)
	mio = mio.WithPersonaEditor(editor)

	testTask := task.NewTask(task.NewJobID(), "口調をカジュアルにして", "line", "U123")
	resp, err := mio.Chat(context.Background(), testTask)
	if err != nil {
		t.Fatalf("Chat should succeed with persona read error (fallback): %v", err)
	}
	// Should fall back to normal chat
	if resp != "通常の応答" {
		t.Errorf("should fall back to normal chat, got %q", resp)
	}
}

func TestGetStringField(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]any
		key      string
		expected string
	}{
		{
			name:     "valid string field",
			m:        map[string]any{"name": "test"},
			key:      "name",
			expected: "test",
		},
		{
			name:     "missing field",
			m:        map[string]any{"other": "value"},
			key:      "name",
			expected: "",
		},
		{
			name:     "non-string field",
			m:        map[string]any{"count": 123},
			key:      "count",
			expected: "",
		},
		{
			name:     "nil map",
			m:        nil,
			key:      "name",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getStringField(tt.m, tt.key)
			if got != tt.expected {
				t.Errorf("getStringField(%v, %q): want %q, got %q", tt.m, tt.key, tt.expected, got)
			}
		})
	}
}

func TestBuildAttributionContextsFromShort(t *testing.T) {
	short := []conversation.Message{
		{Speaker: conversation.SpeakerUser, Msg: "A案はどう？"},
		{Speaker: conversation.SpeakerMio, Msg: "A案に乗るよ"},
		{Speaker: conversation.SpeakerUser, Msg: "じゃあB案も見たい"},
	}
	selfCtx, otherCtx := buildAttributionContextsFromShort(short, conversation.SpeakerMio, 3)
	if len(selfCtx) == 0 || len(otherCtx) == 0 {
		t.Fatal("expected both self/other contexts")
	}
	if selfCtx[0] != "A案に乗るよ" {
		t.Fatalf("unexpected self context: %v", selfCtx)
	}
	if !strings.Contains(otherCtx[0], "user:") {
		t.Fatalf("unexpected other context: %v", otherCtx)
	}
}

func TestViolatesAttributionInChat(t *testing.T) {
	other := "世界の調律師という設定はどう？"
	if !violatesAttributionInChat("世界の調律師という設定はどう？", other) {
		t.Fatal("expected exact reuse without attribution to be blocked")
	}
	if violatesAttributionInChat("あなたの『世界の調律師』案いいね。", other) {
		t.Fatal("expected explicit attribution to pass")
	}
}

func TestMioAgentChatInjectsConfirmedUserMemory(t *testing.T) {
	var captured []llm.Message
	provider := &mockLLMProvider{generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		captured = append([]llm.Message(nil), req.Messages...)
		return llm.GenerateResponse{Content: "了解"}, nil
	}}
	mem := &mockUserMemoryManager{
		listItems: []domainmemory.UserMemory{
			{
				ID:          "mem-confirmed",
				Namespace:   "user:ren",
				UserID:      "ren",
				Statement:   "日本語で短く論理的に答える",
				State:       domainmemory.MemoryStateConfirmed,
				Sensitivity: "normal",
				Active:      true,
			},
			{
				ID:          "mem-candidate",
				Namespace:   "user:ren",
				UserID:      "ren",
				Statement:   "candidate は注入しない",
				State:       domainmemory.MemoryStateCandidate,
				Sensitivity: "normal",
				Active:      true,
			},
			{
				ID:          "mem-sensitive",
				Namespace:   "user:ren",
				UserID:      "ren",
				Statement:   "sensitive は注入しない",
				State:       domainmemory.MemoryStateConfirmed,
				Sensitivity: "sensitive",
				Active:      true,
			},
		},
	}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil).
		WithUserMemoryManager(mem)

	_, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "こんにちは", "viewer", "chat-1"))
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	var joined string
	for _, msg := range captured {
		joined += "\n" + msg.Content
	}
	if !strings.Contains(joined, "思い出したこと") || !strings.Contains(joined, "日本語で短く論理的に答える") {
		t.Fatalf("confirmed user memory was not injected: %s", joined)
	}
	if strings.Contains(joined, "candidate は注入しない") || strings.Contains(joined, "sensitive は注入しない") {
		t.Fatalf("unsafe user memory leaked into prompt: %s", joined)
	}
}

func TestMioAgentChatCreatesCandidateFromUserPreference(t *testing.T) {
	provider := &mockLLMProvider{generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{Content: "了解"}, nil
	}}
	mem := &mockUserMemoryManager{}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil).
		WithUserMemoryManager(mem)

	_, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "俺は映画が好き", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if len(mem.createInputs) != 1 {
		t.Fatalf("expected one candidate memory, got %d", len(mem.createInputs))
	}
	input := mem.createInputs[0]
	if input.UserID != "ren" ||
		input.Type != domainmemory.UserMemoryTypePreference ||
		input.State != domainmemory.MemoryStateCandidate ||
		input.Statement != "映画が好き" ||
		input.Source != "chat_auto_candidate" ||
		input.Confidence <= 0 ||
		len(input.EvidenceEventIDs) == 0 {
		t.Fatalf("unexpected candidate input: %+v", input)
	}
}

func TestMioAgentChatDoesNotCreateDuplicateCandidate(t *testing.T) {
	provider := &mockLLMProvider{generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{Content: "了解"}, nil
	}}
	mem := &mockUserMemoryManager{
		listItems: []domainmemory.UserMemory{{
			ID:          "mem-existing",
			Namespace:   "user:ren",
			UserID:      "ren",
			Type:        domainmemory.UserMemoryTypePreference,
			Statement:   "映画が好き",
			State:       domainmemory.MemoryStateCandidate,
			Sensitivity: "normal",
			Active:      true,
		}},
	}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, &mockToolRunner{}, &mockMCPClient{}, nil).
		WithUserMemoryManager(mem)

	_, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "俺は映画が好き", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if len(mem.createInputs) != 0 {
		t.Fatalf("duplicate candidate should not be created: %+v", mem.createInputs)
	}
}
