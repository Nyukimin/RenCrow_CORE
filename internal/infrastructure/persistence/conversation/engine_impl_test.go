package conversation

import (
	"context"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// === Mocks ===

type mockManager struct {
	recallFunc          func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error)
	storeFunc           func(ctx context.Context, sessionID string, msg domconv.Message) error
	getActiveThreadFunc func(ctx context.Context, sessionID string) (*domconv.Thread, error)
	flushThreadFunc     func(ctx context.Context, threadID int64) (*domconv.ThreadSummary, error)
	createThreadFunc    func(ctx context.Context, sessionID, domain string) (*domconv.Thread, error)
}

func (m *mockManager) Recall(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error) {
	if m.recallFunc != nil {
		return m.recallFunc(ctx, sessionID, query, topK)
	}
	return nil, nil
}

func (m *mockManager) Store(ctx context.Context, sessionID string, msg domconv.Message) error {
	if m.storeFunc != nil {
		return m.storeFunc(ctx, sessionID, msg)
	}
	return nil
}

func (m *mockManager) FlushThread(ctx context.Context, threadID int64) (*domconv.ThreadSummary, error) {
	if m.flushThreadFunc != nil {
		return m.flushThreadFunc(ctx, threadID)
	}
	return &domconv.ThreadSummary{}, nil
}

func (m *mockManager) GetActiveThread(ctx context.Context, sessionID string) (*domconv.Thread, error) {
	if m.getActiveThreadFunc != nil {
		return m.getActiveThreadFunc(ctx, sessionID)
	}
	return domconv.NewThread(sessionID, "general"), nil
}

func (m *mockManager) CreateThread(ctx context.Context, sessionID, domain string) (*domconv.Thread, error) {
	if m.createThreadFunc != nil {
		return m.createThreadFunc(ctx, sessionID, domain)
	}
	return domconv.NewThread(sessionID, domain), nil
}

func (m *mockManager) IsNovelInformation(ctx context.Context, msg domconv.Message) (bool, float32, error) {
	return true, 0.5, nil
}

func (m *mockManager) GetAgentStatus(ctx context.Context, agentName string) (*domconv.AgentStatus, error) {
	return nil, nil
}

func (m *mockManager) UpdateAgentStatus(ctx context.Context, status *domconv.AgentStatus) error {
	return nil
}

type mockDetector struct {
	result domconv.ThreadBoundaryResult
}

func (m *mockDetector) Detect(currentThread *domconv.Thread, newMessage, newDomain string) domconv.ThreadBoundaryResult {
	return m.result
}

type mockExtractor struct {
	result *domconv.ProfileExtractionResult
	err    error
}

func (m *mockExtractor) Extract(ctx context.Context, thread *domconv.Thread, existing domconv.UserProfile) (*domconv.ProfileExtractionResult, error) {
	return m.result, m.err
}

type mockRecallTraceStore struct {
	started  []domconv.RecallTraceRecord
	items    []domconv.RecallTraceItemRecord
	events   []domconv.PromptInjectionEventRecord
	finished []string
}

func (m *mockRecallTraceStore) StartRecallTrace(_ context.Context, trace domconv.RecallTraceRecord) error {
	m.started = append(m.started, trace)
	return nil
}

func (m *mockRecallTraceStore) AddRecallTraceItems(_ context.Context, _ string, items []domconv.RecallTraceItemRecord) error {
	m.items = append(m.items, items...)
	return nil
}

func (m *mockRecallTraceStore) AddPromptInjectionEvents(_ context.Context, _ string, events []domconv.PromptInjectionEventRecord) error {
	m.events = append(m.events, events...)
	return nil
}

func (m *mockRecallTraceStore) FinishRecallTrace(_ context.Context, traceID string, _ string, _ int, _ int) error {
	m.finished = append(m.finished, traceID)
	return nil
}

// === Tests ===

func TestBeginTurn_EmptyRecall(t *testing.T) {
	mgr := &mockManager{}
	persona := domconv.NewMioPersona("test prompt")
	engine := NewRealConversationEngine(mgr, persona)

	pack, err := engine.BeginTurn(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if pack.Persona.Name != "ミオ" {
		t.Errorf("Persona.Name: want 'ミオ', got %q", pack.Persona.Name)
	}
	if len(pack.ShortContext) != 0 {
		t.Errorf("ShortContext should be empty, got %d", len(pack.ShortContext))
	}
}

func TestShouldUseExternalRecallForUserMessage(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{name: "explicit search", message: "RenCrow 最新仕様を検索して", want: true},
		{name: "timely", message: "今日の天気を教えて", want: true},
		{name: "topic", message: "Go言語について教えて", want: true},
		{name: "memory recall", message: "俺が映画が好きってこと知ってる？", want: false},
		{name: "greeting", message: "Mioいる？", want: false},
		{name: "preference statement", message: "俺は映画が好き", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseExternalRecallForUserMessage(tt.message); got != tt.want {
				t.Fatalf("shouldUseExternalRecallForUserMessage(%q)=%v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestBeginTurn_WithShortContext(t *testing.T) {
	mgr := &mockManager{
		recallFunc: func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error) {
			return []domconv.Message{
				{Speaker: domconv.SpeakerUser, Msg: "prev question"},
				{Speaker: domconv.SpeakerMio, Msg: "prev answer"},
			}, nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.ShortContext) != 2 {
		t.Fatalf("ShortContext: want 2, got %d", len(pack.ShortContext))
	}
	if pack.ShortContext[0].Msg != "prev question" {
		t.Errorf("ShortContext[0]: want 'prev question', got %q", pack.ShortContext[0].Msg)
	}
}

func TestBeginTurn_SavesRecallTrace(t *testing.T) {
	mgr := &mockManager{
		recallFunc: func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error) {
			return []domconv.Message{{Speaker: domconv.SpeakerUser, Msg: "prev question"}}, nil
		},
	}
	traceStore := &mockRecallTraceStore{}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithRecallTraceStore(traceStore)

	if _, err := engine.BeginTurn(context.Background(), "chat-1", "hello"); err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(traceStore.started) != 1 {
		t.Fatalf("StartRecallTrace calls = %d, want 1", len(traceStore.started))
	}
	if traceStore.started[0].ChatID != "chat-1" || traceStore.started[0].UserMessageHash == "" {
		t.Fatalf("unexpected started trace: %+v", traceStore.started[0])
	}
	if len(traceStore.items) == 0 {
		t.Fatal("expected trace items")
	}
	if traceStore.items[0].Status != domconv.TraceStatusInjected || traceStore.items[0].PromptSection == "" {
		t.Fatalf("unexpected trace item: %+v", traceStore.items[0])
	}
	if len(traceStore.events) == 0 {
		t.Fatal("expected prompt injection events")
	}
	if len(traceStore.finished) != 1 || traceStore.finished[0] != traceStore.started[0].TraceID {
		t.Fatalf("unexpected finished traces: %+v started=%+v", traceStore.finished, traceStore.started)
	}
}

func TestBeginTurn_AddsL0RollingSummaryFromActiveThread(t *testing.T) {
	thread := domconv.NewThread("s1", "general")
	for i := 1; i <= 8; i++ {
		thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, fmt.Sprintf("user turn %d", i), nil))
		thread.AddMessage(domconv.NewMessage(domconv.SpeakerMio, fmt.Sprintf("mio turn %d", i), nil))
	}
	mgr := &mockManager{
		getActiveThreadFunc: func(ctx context.Context, sessionID string) (*domconv.Thread, error) {
			return thread, nil
		},
		recallFunc: func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error) {
			return thread.Turns, nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if pack.RollingSummary == "" {
		t.Fatal("RollingSummary should be populated for long active thread")
	}
	if !strings.Contains(pack.RollingSummary, "user turn 3") || !strings.Contains(pack.RollingSummary, "mio turn 5") {
		t.Fatalf("RollingSummary should summarize older L0 turns, got %q", pack.RollingSummary)
	}
	if strings.Contains(pack.RollingSummary, "user turn 8") {
		t.Fatalf("RollingSummary should leave newest turns in ShortContext, got %q", pack.RollingSummary)
	}
	if len(pack.ShortContext) != 6 {
		t.Fatalf("ShortContext should keep newest 6 turns, got %d", len(pack.ShortContext))
	}
	if pack.ShortContext[0].Msg != "user turn 6" || pack.ShortContext[5].Msg != "mio turn 8" {
		t.Fatalf("ShortContext should contain newest turns, got %+v", pack.ShortContext)
	}
}

func TestBeginTurn_WithMidSummaries(t *testing.T) {
	mgr := &mockManager{
		recallFunc: func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error) {
			return []domconv.Message{
				{Speaker: domconv.SpeakerSystem, Msg: "[Summary] Discussed Go testing"},
			}, nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.MidSummaries) != 1 {
		t.Fatalf("MidSummaries: want 1, got %d", len(pack.MidSummaries))
	}
	if pack.MidSummaries[0].Summary != "Discussed Go testing" {
		t.Errorf("MidSummaries[0].Summary: want 'Discussed Go testing', got %q", pack.MidSummaries[0].Summary)
	}
}

func TestBeginTurn_WithLongFacts(t *testing.T) {
	mgr := &mockManager{
		recallFunc: func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error) {
			return []domconv.Message{
				{Speaker: domconv.SpeakerSystem, Msg: "[LongTermMemory] User prefers Go"},
			}, nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.LongFacts) != 1 {
		t.Fatalf("LongFacts: want 1, got %d", len(pack.LongFacts))
	}
	if pack.LongFacts[0] != "User prefers Go" {
		t.Errorf("LongFacts[0]: want 'User prefers Go', got %q", pack.LongFacts[0])
	}
}

func TestBeginTurn_RecallError_GracefulDegradation(t *testing.T) {
	mgr := &mockManager{
		recallFunc: func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error) {
			return nil, fmt.Errorf("redis down")
		},
	}
	persona := domconv.NewMioPersona("test")
	engine := NewRealConversationEngine(mgr, persona)

	pack, err := engine.BeginTurn(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("BeginTurn should succeed even on recall error: %v", err)
	}
	if pack.Persona.Name != "ミオ" {
		t.Error("Persona should still be set")
	}
	if len(pack.ShortContext) != 0 {
		t.Error("ShortContext should be empty on recall failure")
	}
}

func TestBeginTurn_WithUserProfile(t *testing.T) {
	mgr := &mockManager{}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})
	// Pre-populate profile cache
	profile := domconv.NewUserProfile("s1")
	profile.Merge(map[string]string{"lang": "Go"}, nil)
	engine.profiles["s1"] = profile

	pack, err := engine.BeginTurn(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if pack.UserProfile.Preferences["lang"] != "Go" {
		t.Error("UserProfile should be loaded from cache")
	}
}

func TestBeginTurn_WithFreshSearchCache(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)
	l1 := &mockL1Store{}
	mgr.WithL1Store(l1)
	if _, err := l1.SaveSearchCache(ctx, "web", "RenCrow 最新仕様", `[{"title":"RenCrow memo"}]`, []string{"https://example.com/rencrow"}, time.Hour); err != nil {
		t.Fatalf("SaveSearchCache failed: %v", err)
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(ctx, "s1", "RenCrow 最新仕様")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.SearchCacheSnippets) != 1 {
		t.Fatalf("SearchCacheSnippets: want 1, got %d", len(pack.SearchCacheSnippets))
	}
	snippet := pack.SearchCacheSnippets[0]
	if snippet.Query != "RenCrow 最新仕様" || snippet.Provider != "web" {
		t.Fatalf("unexpected search cache snippet identity: %+v", snippet)
	}
	if snippet.ResultsJSON != `[{"title":"RenCrow memo"}]` {
		t.Fatalf("unexpected search cache results: %s", snippet.ResultsJSON)
	}
	if len(snippet.SourceURLs) != 1 || snippet.SourceURLs[0] != "https://example.com/rencrow" {
		t.Fatalf("unexpected search cache sources: %+v", snippet.SourceURLs)
	}
}

func TestBeginTurn_UsesL1KnowledgeFTSBeforeVectorKB(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)
	l1 := &mockL1Store{knowledge: []l1sqlite.L1KnowledgeItem{{
		ID:           "kb-1",
		Domain:       "general",
		Title:        "RenCrow memory",
		RawText:      "RenCrow memory lifecycle local first recall",
		SummaryDraft: "RenCrow memory lifecycle は local-first recall を優先する",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}}}
	mgr.WithL1Store(l1)
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(ctx, "s1", "RenCrow memory lifecycle 最新")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.KBSnippets) != 1 || !strings.Contains(pack.KBSnippets[0], "[L1KB]") {
		t.Fatalf("expected local L1 KB snippet, got %+v", pack.KBSnippets)
	}
	chat := pack.FilterForRole("mio")
	if len(chat.KBSnippets) != 1 {
		t.Fatalf("Mio/chat policy should keep local-first KB snippet: %+v", chat)
	}
}

func TestBeginTurn_UsesWikiPageIndexForSpecRecall(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)
	l1 := &mockL1Store{wiki: []l1sqlite.WikiPageIndexItem{{
		PageID:          "concept:recall-pack",
		Path:            "docs/wiki/concepts/recall-pack.md",
		Title:           "RecallPack",
		Type:            "concept",
		Status:          l1sqlite.WikiPageStatusActive,
		Owner:           "core",
		CanonicalSource: "docs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md",
		SourcePaths:     []string{"internal/domain/conversation/recall_pack.go"},
		Related:         []string{"docs/wiki/concepts/memory-lifecycle.md"},
		Summary:         "RecallPack は Mio に渡す文脈を選別済みにする。",
		UpdatedAt:       time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC),
	}}}
	mgr.WithL1Store(l1)
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(ctx, "s1", "RecallPack の仕様")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.WikiSnippets) != 1 || pack.WikiSnippets[0].PageID != "concept:recall-pack" {
		t.Fatalf("expected wiki snippet, got %+v", pack.WikiSnippets)
	}
	chat := pack.FilterForRole("mio")
	if len(chat.WikiSnippets) != 1 {
		t.Fatalf("Mio/chat policy should keep explicit wiki snippet: %+v", chat)
	}
	if got := chat.WikiSnippets[0].SourcePaths; len(got) != 1 || got[0] != "internal/domain/conversation/recall_pack.go" {
		t.Fatalf("wiki source paths should be preserved: %+v", got)
	}
}

func TestEndTurn_BasicStore(t *testing.T) {
	stored := []string{}
	mgr := &mockManager{
		storeFunc: func(ctx context.Context, sessionID string, msg domconv.Message) error {
			stored = append(stored, string(msg.Speaker)+":"+msg.Msg)
			return nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	err := engine.EndTurn(context.Background(), "s1", "hello", "hi there")
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 stores, got %d", len(stored))
	}
	if stored[0] != "user:hello" {
		t.Errorf("stored[0]: want 'user:hello', got %q", stored[0])
	}
	if stored[1] != "mio:hi there" {
		t.Errorf("stored[1]: want 'mio:hi there', got %q", stored[1])
	}
}

func TestEndTurn_WithDetector_NoBoundary(t *testing.T) {
	flushCalled := false
	mgr := &mockManager{
		flushThreadFunc: func(ctx context.Context, threadID int64) (*domconv.ThreadSummary, error) {
			flushCalled = true
			return &domconv.ThreadSummary{}, nil
		},
	}
	detector := &mockDetector{
		result: domconv.ThreadBoundaryResult{ShouldCreateNew: false},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithDetector(detector)

	err := engine.EndTurn(context.Background(), "s1", "hello", "hi")
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}
	if flushCalled {
		t.Error("FlushThread should NOT be called when no boundary detected")
	}
}

func TestEndTurn_WithDetector_Boundary(t *testing.T) {
	flushCalled := false
	createCalled := false
	mgr := &mockManager{
		flushThreadFunc: func(ctx context.Context, threadID int64) (*domconv.ThreadSummary, error) {
			flushCalled = true
			return &domconv.ThreadSummary{}, nil
		},
		createThreadFunc: func(ctx context.Context, sessionID, domain string) (*domconv.Thread, error) {
			createCalled = true
			return domconv.NewThread(sessionID, domain), nil
		},
	}
	detector := &mockDetector{
		result: domconv.ThreadBoundaryResult{ShouldCreateNew: true, Reason: domconv.BoundaryKeyword},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithDetector(detector)

	err := engine.EndTurn(context.Background(), "s1", "new topic", "response")
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}
	if !flushCalled {
		t.Error("FlushThread should be called when boundary detected")
	}
	if !createCalled {
		t.Error("CreateThread should be called after flush")
	}
}

func TestEndTurn_WithProfileExtractor(t *testing.T) {
	mgr := &mockManager{}
	extractor := &mockExtractor{
		result: &domconv.ProfileExtractionResult{
			NewPreferences: map[string]string{"lang": "Go"},
			NewFacts:       []string{"developer"},
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithProfileExtractor(extractor)

	err := engine.EndTurn(context.Background(), "s1", "hello", "hi")
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}
	profile, ok := engine.profiles["s1"]
	if !ok {
		t.Fatal("profile should be cached")
	}
	if profile.Preferences["lang"] != "Go" {
		t.Errorf("profile lang: want 'Go', got %q", profile.Preferences["lang"])
	}
	if len(profile.Facts) != 1 || profile.Facts[0] != "developer" {
		t.Errorf("profile facts: want ['developer'], got %v", profile.Facts)
	}
}

func TestEndTurn_ProfileExtractorError(t *testing.T) {
	mgr := &mockManager{}
	extractor := &mockExtractor{
		err: fmt.Errorf("LLM failed"),
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithProfileExtractor(extractor)

	// Should not return error — profile extraction is best-effort
	err := engine.EndTurn(context.Background(), "s1", "hello", "hi")
	if err != nil {
		t.Fatalf("EndTurn should succeed even on extractor error: %v", err)
	}
}

func TestGetPersona(t *testing.T) {
	persona := domconv.NewMioPersona("custom prompt")
	engine := NewRealConversationEngine(&mockManager{}, persona)
	got := engine.GetPersona()
	if got.Name != "ミオ" {
		t.Errorf("Name: want 'ミオ', got %q", got.Name)
	}
	if got.SystemPrompt != "custom prompt" {
		t.Errorf("SystemPrompt: want 'custom prompt', got %q", got.SystemPrompt)
	}
}

func TestFlushCurrentThread_Success(t *testing.T) {
	flushCalled := false
	createCalled := false
	mgr := &mockManager{
		flushThreadFunc: func(ctx context.Context, threadID int64) (*domconv.ThreadSummary, error) {
			flushCalled = true
			return &domconv.ThreadSummary{}, nil
		},
		createThreadFunc: func(ctx context.Context, sessionID, domain string) (*domconv.Thread, error) {
			createCalled = true
			return domconv.NewThread(sessionID, domain), nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	err := engine.FlushCurrentThread(context.Background(), "s1")
	if err != nil {
		t.Fatalf("FlushCurrentThread failed: %v", err)
	}
	if !flushCalled {
		t.Error("FlushThread should be called")
	}
	if !createCalled {
		t.Error("CreateThread should be called after flush")
	}
}

func TestFlushCurrentThread_NoActiveThread(t *testing.T) {
	mgr := &mockManager{
		getActiveThreadFunc: func(ctx context.Context, sessionID string) (*domconv.Thread, error) {
			return nil, fmt.Errorf("no active thread")
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	err := engine.FlushCurrentThread(context.Background(), "s1")
	if err == nil {
		t.Error("FlushCurrentThread should fail when no active thread")
	}
}

func TestGetStatus_WithActiveThread(t *testing.T) {
	thread := domconv.NewThread("s1", "programming")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "msg1", nil))
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerMio, "msg2", nil))

	mgr := &mockManager{
		getActiveThreadFunc: func(ctx context.Context, sessionID string) (*domconv.Thread, error) {
			return thread, nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	status, err := engine.GetStatus(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if status.SessionID != "s1" {
		t.Errorf("SessionID: want 's1', got %q", status.SessionID)
	}
	if status.ThreadDomain != "programming" {
		t.Errorf("ThreadDomain: want 'programming', got %q", status.ThreadDomain)
	}
	if status.TurnCount != 2 {
		t.Errorf("TurnCount: want 2, got %d", status.TurnCount)
	}
}

func TestGetStatus_NoActiveThread(t *testing.T) {
	mgr := &mockManager{
		getActiveThreadFunc: func(ctx context.Context, sessionID string) (*domconv.Thread, error) {
			return nil, fmt.Errorf("not found")
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	status, err := engine.GetStatus(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetStatus should not fail: %v", err)
	}
	if status.SessionID != "s1" {
		t.Errorf("SessionID: want 's1', got %q", status.SessionID)
	}
	if status.TurnCount != 0 {
		t.Errorf("TurnCount should be 0, got %d", status.TurnCount)
	}
}

func TestResetSession(t *testing.T) {
	flushCalled := false
	createDomain := ""
	mgr := &mockManager{
		flushThreadFunc: func(ctx context.Context, threadID int64) (*domconv.ThreadSummary, error) {
			flushCalled = true
			return &domconv.ThreadSummary{}, nil
		},
		createThreadFunc: func(ctx context.Context, sessionID, domain string) (*domconv.Thread, error) {
			createDomain = domain
			return domconv.NewThread(sessionID, domain), nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	err := engine.ResetSession(context.Background(), "s1")
	if err != nil {
		t.Fatalf("ResetSession failed: %v", err)
	}
	if !flushCalled {
		t.Error("FlushThread should be called during reset")
	}
	if createDomain != "general" {
		t.Errorf("new thread domain should be 'general', got %q", createDomain)
	}
}
