package conversation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// === Mocks ===

type mockManager struct {
	recallFunc           func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error)
	storeFunc            func(ctx context.Context, sessionID string, msg domconv.Message) error
	getActiveThreadFunc  func(ctx context.Context, sessionID string) (*domconv.Thread, error)
	flushThreadFunc      func(ctx context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error)
	createThreadFunc     func(ctx context.Context, sessionID, domain string) (*domconv.Thread, error)
	commitTurnFunc       func(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error)
	commitRequests       []domconv.ConversationTurnRequest
	targets              []domconv.ConversationTurnTarget
	saveRecallTraceCalls int
	userMemories         []domainmemory.UserMemory
	userMemoryErr        error
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

func (m *mockManager) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	m.commitRequests = append(m.commitRequests, request)
	if m.commitTurnFunc != nil {
		return m.commitTurnFunc(ctx, request)
	}
	if request.Boundary {
		if _, err := m.FlushThread(ctx, modulecore.NewThreadID()); err != nil {
			return domconv.ConversationTurnResult{}, err
		}
		if _, err := m.CreateThread(ctx, request.SessionID, request.Domain); err != nil {
			return domconv.ConversationTurnResult{}, err
		}
	}
	user := domconv.NewMessage(domconv.SpeakerUser, request.UserMessage, map[string]interface{}{"from": string(domconv.SpeakerUser), "to": string(request.AgentSpeaker)})
	if err := m.Store(ctx, request.SessionID, user); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	agent := domconv.NewMessage(request.AgentSpeaker, request.AgentMessage, map[string]interface{}{"from": string(request.AgentSpeaker), "to": string(domconv.SpeakerUser)})
	if err := m.Store(ctx, request.SessionID, agent); err != nil {
		return domconv.ConversationTurnResult{}, err
	}
	return domconv.ConversationTurnResult{
		TurnID:         request.TurnID,
		TraceID:        request.TraceID,
		RootTaskID:     request.RootTaskID,
		UserMessageID:  request.UserMessageID,
		AgentMessageID: request.AgentMessageID,
		MessageIDs:     []string{string(request.UserMessageID), string(request.AgentMessageID)},
		SessionID:      request.SessionID,
		Status:         domconv.ConversationTurnCompleted,
	}, nil
}

func (m *mockManager) ConversationTurnTargets() []domconv.ConversationTurnTarget {
	return append([]domconv.ConversationTurnTarget(nil), m.targets...)
}

func (m *mockManager) LoadActiveConversationThread(ctx context.Context, sessionID string) (*domconv.Thread, error) {
	return m.GetActiveThread(ctx, sessionID)
}

func (m *mockManager) FlushThread(ctx context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error) {
	if m.flushThreadFunc != nil {
		return m.flushThreadFunc(ctx, threadID)
	}
	return &domconv.ThreadSummary{}, nil
}

func (m *mockManager) GetActiveThread(ctx context.Context, sessionID string) (*domconv.Thread, error) {
	if m.getActiveThreadFunc != nil {
		return m.getActiveThreadFunc(ctx, sessionID)
	}
	return newEngineTestThread(sessionID, "general"), nil
}

func (m *mockManager) CreateThread(ctx context.Context, sessionID, domain string) (*domconv.Thread, error) {
	if m.createThreadFunc != nil {
		return m.createThreadFunc(ctx, sessionID, domain)
	}
	return newEngineTestThread(sessionID, domain), nil
}

func newEngineTestThread(sessionID, domain string) *domconv.Thread {
	return &domconv.Thread{
		ID:         modulecore.NewThreadID(),
		ThreadSeq:  1,
		ThreadKind: modulecore.ThreadKindUserConversation,
		SessionID:  sessionID,
		Domain:     domain,
		Status:     domconv.ThreadActive,
	}
}

func engineTestConversationTurnRequest(sessionID, userMessage, agentMessage string, speaker domconv.Speaker) domconv.ConversationTurnRequest {
	return domconv.ConversationTurnRequest{
		TurnID:         modulecore.NewTurnID(),
		TraceID:        modulecore.NewTraceID(),
		RootTaskID:     modulecore.NewTaskID(),
		UserMessageID:  modulecore.NewMessageID(),
		AgentMessageID: modulecore.NewMessageID(),
		SessionID:      sessionID,
		UserMessage:    userMessage,
		AgentMessage:   agentMessage,
		AgentSpeaker:   speaker,
	}
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

func (m *mockManager) ListUserMemories(context.Context, string, string, bool, int) ([]domainmemory.UserMemory, error) {
	return append([]domainmemory.UserMemory(nil), m.userMemories...), m.userMemoryErr
}

func (m *mockManager) SaveRecallTrace(context.Context, domconv.RecallTrace) error {
	m.saveRecallTraceCalls++
	return nil
}

type mockDetector struct {
	result domconv.ThreadBoundaryResult
}

func (m *mockDetector) Detect(currentThread *domconv.Thread, newMessage, newDomain string) domconv.ThreadBoundaryResult {
	return m.result
}

type mockExternalRecallManager struct {
	*mockManager
	items     []l1sqlite.L1KnowledgeItem
	hits      map[string][]l1sqlite.L1KnowledgeRelationHit
	vectorErr error
}

type categoryRecallRegistryStub struct {
	result  domconv.CategoryRecallResult
	err     error
	queries []domconv.CategoryRecallQuery
}

func (s *categoryRecallRegistryStub) Recall(_ context.Context, query domconv.CategoryRecallQuery) (domconv.CategoryRecallResult, error) {
	s.queries = append(s.queries, query)
	return s.result, s.err
}

func (m *mockExternalRecallManager) GetFreshSearchCache(context.Context, string, string, time.Time) (*l1sqlite.L1SearchCacheEntry, error) {
	return nil, nil
}

func (m *mockExternalRecallManager) SearchKnowledgeItemsFTS(context.Context, string, string, int) ([]l1sqlite.L1KnowledgeItem, error) {
	return append([]l1sqlite.L1KnowledgeItem(nil), m.items...), nil
}

func (m *mockExternalRecallManager) SearchWikiPageIndex(context.Context, string, int) ([]l1sqlite.WikiPageIndexItem, error) {
	return nil, nil
}

func (m *mockExternalRecallManager) SearchKB(context.Context, string, string, int) ([]*domconv.Document, error) {
	return nil, m.vectorErr
}

func (m *mockExternalRecallManager) RelatedKnowledgeItems(_ context.Context, itemID string, _ int, _ int) ([]l1sqlite.L1KnowledgeRelationHit, error) {
	return append([]l1sqlite.L1KnowledgeRelationHit(nil), m.hits[itemID]...), nil
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

func TestBeginTurn_CategoryRecallRunsForRelatedNormalUtterance(t *testing.T) {
	registry := &categoryRecallRegistryStub{result: domconv.CategoryRecallResult{Records: []domconv.CategoryRecallRecord{{
		Category: "movie", SourceID: "movie_catalog", RecordID: "m1", Title: "マトリックス", Summary: "公開概要",
		ProvenanceURLs: []string{"https://example.test/m1"}, State: domconv.CategoryRecordStateValidated,
		Roles: []string{"chat", "worker", "heavy", "creative"},
	}}}}
	mgr := &mockManager{}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithCategoryRecallRegistry(registry).WithCategoryRecallScope("ren")
	pack, err := engine.BeginTurn(context.Background(), "s1", "映画マトリックスの話をしよう")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(registry.queries) != 1 || registry.queries[0].Message == "" || registry.queries[0].UserScope != "ren" {
		t.Fatalf("category query=%#v", registry.queries)
	}
	if len(pack.CategorySnippets) != 1 || pack.CategorySnippets[0].RecordID != "m1" {
		t.Fatalf("category snippets=%#v", pack.CategorySnippets)
	}
	if len(mgr.userMemories) != 0 {
		t.Fatalf("category recall must not store history: %#v", mgr.userMemories)
	}
}

func TestBeginTurn_CategoryRecallFailureIsPartialTrace(t *testing.T) {
	registry := &categoryRecallRegistryStub{result: domconv.CategoryRecallResult{Failures: []domconv.CategoryRecallFailure{{
		Category: "movie", SourceID: "movie_catalog", Code: domconv.CategoryRecallFailureSourceUnavailable,
		State: "unavailable", Reason: "missing DB", Retryable: true,
	}}}}
	engine := NewRealConversationEngine(&mockManager{}, domconv.PersonaState{}).
		WithCategoryRecallRegistry(registry)
	pack, err := engine.BeginTurn(context.Background(), "s1", "映画の話")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	found := false
	for _, item := range pack.ToTraceItems() {
		if item.Kind == "category_recall_failure" && item.Status == domconv.CategoryRecallFailureSourceUnavailable {
			found = true
		}
	}
	if !found {
		t.Fatalf("category failure was not traced in returned pack: %#v", pack.ToTraceItems())
	}
}

func TestBeginTurn_DoesNotDuplicateL1KnowledgeAfterCategoryRecall(t *testing.T) {
	registry := &categoryRecallRegistryStub{result: domconv.CategoryRecallResult{Records: []domconv.CategoryRecallRecord{{
		Category: "movie", SourceID: "knowledge_l1", RecordID: "kb-movie", Title: "Movie fact", Summary: "Validated movie fact",
		ProvenanceURLs: []string{"https://example.test/kb-movie"}, State: domconv.CategoryRecordStateValidated,
	}}}}
	mgr := &mockExternalRecallManager{
		mockManager: &mockManager{},
		items: []l1sqlite.L1KnowledgeItem{{
			ID: "kb-movie", Domain: "movie", Title: "Movie fact", SummaryDraft: "Validated movie fact",
		}},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithCategoryRecallRegistry(registry)
	pack, err := engine.BeginTurn(context.Background(), "s1", "映画を検索して")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.CategorySnippets) != 1 || pack.CategorySnippets[0].SourceID != "knowledge_l1" {
		t.Fatalf("expected adopted L1 category snippet: %#v", pack.CategorySnippets)
	}
	if len(pack.KBSnippets) != 0 {
		t.Fatalf("L1 KBSnippet duplicated adopted category recall: %#v", pack.KBSnippets)
	}
}

func TestBeginTurn_KeepsDifferentL1KnowledgeAfterCategoryRecall(t *testing.T) {
	registry := &categoryRecallRegistryStub{result: domconv.CategoryRecallResult{Records: []domconv.CategoryRecallRecord{{
		Category: "movie", SourceID: "knowledge_l1", RecordID: "kb-category", Title: "Category fact", Summary: "Validated category fact",
		ProvenanceURLs: []string{"https://example.test/kb-category"}, State: domconv.CategoryRecordStateValidated,
	}}}}
	mgr := &mockExternalRecallManager{
		mockManager: &mockManager{},
		items: []l1sqlite.L1KnowledgeItem{{
			ID: "kb-other", Domain: "movie", Title: "Other fact", SummaryDraft: "Different L1 fact",
		}},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithCategoryRecallRegistry(registry)
	pack, err := engine.BeginTurn(context.Background(), "s1", "映画を検索して")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.KBSnippets) != 1 || !strings.Contains(pack.KBSnippets[0], "Different L1 fact") {
		t.Fatalf("different external L1 record should remain: %#v", pack.KBSnippets)
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

func TestBeginTurnExpandsKnowledgeRelationsWhenVectorDBIsUnavailable(t *testing.T) {
	mgr := &mockExternalRecallManager{
		mockManager: &mockManager{},
		items:       []l1sqlite.L1KnowledgeItem{{ID: "seed", Domain: "general", SummaryDraft: "seed summary"}},
		hits: map[string][]l1sqlite.L1KnowledgeRelationHit{
			"seed": {{
				Item: l1sqlite.L1KnowledgeItem{ID: "related", Domain: "github", Title: "Related", SummaryDraft: "related summary"},
				Hop:  1, RelationType: "same_entity", Score: 5, Evidence: "same entity: mlx",
			}},
		},
		vectorErr: fmt.Errorf("vector db unavailable"),
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).
		WithKnowledgeRelationRecall(2)
	pack, err := engine.BeginTurn(context.Background(), "s1", "RenCrow仕様について調べて")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.RelationSnippets) != 1 || pack.RelationSnippets[0].Hop != 1 || pack.RelationSnippets[0].Evidence == "" {
		t.Fatalf("relation snippets=%#v", pack.RelationSnippets)
	}
	foundTrace := false
	for _, item := range pack.ToTraceItems() {
		if item.Kind == "knowledge_relation" && strings.Contains(item.Summary, "hop=1") && strings.Contains(item.Summary, "same entity: mlx") {
			foundTrace = true
		}
	}
	if !foundTrace {
		t.Fatalf("knowledge relation trace missing hop/evidence: %#v", pack.ToTraceItems())
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
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "ren")

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

func TestBeginTurn_SharesAllCharacterMessagesAsShortContext(t *testing.T) {
	want := []domconv.Message{
		{Speaker: domconv.SpeakerUser, Msg: "合言葉は青い水路"},
		{Speaker: domconv.SpeakerMio, Msg: "覚えたよ"},
		{Speaker: domconv.SpeakerShiro, Msg: "確認しました"},
		{Speaker: domconv.SpeakerKuro, Msg: "関連を分析した"},
		{Speaker: domconv.SpeakerMidori, Msg: "物語にも使えるね"},
	}
	mgr := &mockManager{
		recallFunc: func(context.Context, string, string, int) ([]domconv.Message, error) {
			return append([]domconv.Message(nil), want...), nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "ren")

	pack, err := engine.BeginTurn(context.Background(), "shared-session", "合言葉は？")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.ShortContext) != len(want) {
		t.Fatalf("ShortContext=%#v, want all %d messages", pack.ShortContext, len(want))
	}
	for i := range want {
		if pack.ShortContext[i].Speaker != want[i].Speaker || pack.ShortContext[i].Msg != want[i].Msg {
			t.Fatalf("ShortContext[%d]=%#v, want %#v", i, pack.ShortContext[i], want[i])
		}
	}
}

func TestBeginTurn_NormalizesLegacyWildAndHeavySpeakers(t *testing.T) {
	mgr := &mockManager{recallFunc: func(context.Context, string, string, int) ([]domconv.Message, error) {
		return []domconv.Message{
			{Speaker: domconv.Speaker("heavy"), Msg: "旧Kuro発言"},
			{Speaker: domconv.Speaker("wild"), Msg: "旧Midori発言"},
		}, nil
	}}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(context.Background(), "shared-session", "前回の続き")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if len(pack.ShortContext) != 2 || pack.ShortContext[0].Speaker != domconv.SpeakerKuro || pack.ShortContext[1].Speaker != domconv.SpeakerMidori {
		t.Fatalf("legacy speakers were not normalized: %#v", pack.ShortContext)
	}
}

func TestCommitConversationTurnStoresFromToAttribution(t *testing.T) {
	var stored []domconv.Message
	mgr := &mockManager{storeFunc: func(_ context.Context, _ string, msg domconv.Message) error {
		stored = append(stored, msg)
		return nil
	}}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "ren")

	request := engineTestConversationTurnRequest(string(modulecore.NewSessionID()), "前回の続き", "Kuroの返答", domconv.SpeakerKuro)
	if _, err := engine.CommitConversationTurn(context.Background(), request); err != nil {
		t.Fatalf("CommitConversationTurn failed: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored=%#v, want user and Agent messages", stored)
	}
	if stored[0].Meta["from"] != "user" || stored[0].Meta["to"] != "kuro" {
		t.Fatalf("user attribution=%#v", stored[0].Meta)
	}
	if stored[1].Meta["from"] != "kuro" || stored[1].Meta["to"] != "user" {
		t.Fatalf("Agent attribution=%#v", stored[1].Meta)
	}
}

func TestCommitConversationTurnUsesTrustedOwnerTargetsAndL1Boundary(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	thread := &domconv.Thread{ID: modulecore.NewThreadID(), ThreadSeq: 1, ThreadKind: modulecore.ThreadKindUserConversation, SessionID: sessionID, Domain: "authoritative", Status: domconv.ThreadActive}
	var got domconv.ConversationTurnRequest
	mgr := &mockManager{
		targets: []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers},
		getActiveThreadFunc: func(context.Context, string) (*domconv.Thread, error) {
			return thread, nil
		},
		commitTurnFunc: func(_ context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
			got = request
			return domconv.ConversationTurnResult{
				TurnID: request.TurnID, TraceID: request.TraceID, RootTaskID: request.RootTaskID,
				UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
				SessionID: request.SessionID, Status: domconv.ConversationTurnCompleted,
			}, nil
		},
		storeFunc: func(context.Context, string, domconv.Message) error {
			t.Fatal("typed engine must not use legacy Store")
			return nil
		},
	}
	detector := &mockDetector{result: domconv.ThreadBoundaryResult{ShouldCreateNew: true, Reason: domconv.BoundaryKeyword}}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).
		WithUserMemoryStore(mgr, "trusted-owner").WithDetector(detector)
	request := engineTestConversationTurnRequest(sessionID, "new topic", "response", domconv.SpeakerKuro)
	request.OwnerID = "caller-owner"
	request.Domain = "caller-domain"
	request.Boundary = true
	request.BoundaryReason = "caller decision"
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := engine.CommitConversationTurn(context.Background(), request); err != nil {
		t.Fatalf("CommitConversationTurn failed: %v", err)
	}
	if got.OwnerID != "trusted-owner" || got.Domain != "authoritative" || !got.Boundary || got.BoundaryReason != string(domconv.BoundaryKeyword) {
		t.Fatalf("trusted/boundary fields=%#v", got)
	}
	if len(got.Targets) != 2 || got.Targets[0] != domconv.ConversationTurnTargetRedisProjection || got.Targets[1] != domconv.ConversationTurnTargetThreadFollowers {
		t.Fatalf("manager targets=%#v", got.Targets)
	}
}

func TestCommitConversationTurnNoActiveThreadUsesGeneralWithoutLegacyWrites(t *testing.T) {
	var got domconv.ConversationTurnRequest
	mgr := &mockManager{
		getActiveThreadFunc: func(context.Context, string) (*domconv.Thread, error) {
			return nil, domconv.ErrThreadNotFound
		},
		commitTurnFunc: func(_ context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
			got = request
			return domconv.ConversationTurnResult{
				TurnID: request.TurnID, TraceID: request.TraceID, RootTaskID: request.RootTaskID,
				UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
				SessionID: request.SessionID, Status: domconv.ConversationTurnCompleted,
			}, nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "trusted-owner")
	request := engineTestConversationTurnRequest(string(modulecore.NewSessionID()), "hello", "hi", domconv.SpeakerMio)
	if _, err := engine.CommitConversationTurn(context.Background(), request); err != nil {
		t.Fatalf("CommitConversationTurn failed: %v", err)
	}
	if got.Domain != "general" || got.Boundary || got.BoundaryReason != "" {
		t.Fatalf("no-active defaults=%#v", got)
	}
}

func TestBeginTurn_LoadsConfirmedAndPinnedUserMemoryForAllCharacters(t *testing.T) {
	mgr := &mockManager{userMemories: []domainmemory.UserMemory{
		{Statement: "れんは青が好き", State: domainmemory.MemoryStateConfirmed, Scope: "mio_only", Active: true, Sensitivity: "normal"},
		{Statement: "合言葉は青い水路", State: domainmemory.MemoryStatePinned, Scope: "midori_only", Active: true, Sensitivity: "normal"},
		{Statement: "未確認候補", State: domainmemory.MemoryStateCandidate, Scope: "all", Active: true, Sensitivity: "normal"},
	}}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "ren")

	pack, err := engine.BeginTurn(context.Background(), "shared-session", "覚えている？")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	got := strings.Join(pack.UserProfile.Facts, " / ")
	if !strings.Contains(got, "れんは青が好き") || !strings.Contains(got, "[優先] 合言葉は青い水路") {
		t.Fatalf("shared UserMemory missing: %q", got)
	}
	if strings.Contains(got, "未確認候補") {
		t.Fatalf("candidate UserMemory must not be injected: %q", got)
	}
}

func TestBeginTurn_RanksOlderRelevantUserMemoryAheadOfRecentUnrelatedMemory(t *testing.T) {
	now := time.Now().UTC()
	memories := make([]domainmemory.UserMemory, 0, 20)
	for i := 0; i < 19; i++ {
		memories = append(memories, domainmemory.UserMemory{ID: fmt.Sprintf("recent-%d", i), UserID: "ren", Statement: fmt.Sprintf("最近の無関係な料理メモ%d", i), State: domainmemory.MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: true, UpdatedAt: now.Add(time.Duration(i) * time.Second)})
	}
	memories = append(memories, domainmemory.UserMemory{ID: "old-movie", UserID: "ren", Statement: "Renは映画ブレードランナーが好き", State: domainmemory.MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: true, UpdatedAt: now.Add(-time.Hour)})
	mgr := &mockManager{userMemories: memories}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "ren")
	pack, err := engine.BeginTurn(context.Background(), "shared-session", "ブレードランナーについて覚えている？")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(pack.UserProfile.Facts, "\n")
	if !strings.Contains(got, "ブレードランナー") {
		t.Fatalf("relevant old memory missing: %q", got)
	}
}

func TestBeginTurn_DoesNotPersistRecallTraceSeparately(t *testing.T) {
	mgr := &mockManager{
		recallFunc: func(ctx context.Context, sessionID, query string, topK int) ([]domconv.Message, error) {
			return []domconv.Message{{Speaker: domconv.SpeakerUser, Msg: "prev question"}}, nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	if _, err := engine.BeginTurn(context.Background(), "chat-1", "hello"); err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if mgr.saveRecallTraceCalls != 0 {
		t.Fatalf("BeginTurn must not persist recall trace separately: SaveRecallTrace calls=%d", mgr.saveRecallTraceCalls)
	}
}

func TestBeginTurn_UserMemoryTraceCarriesOwnerAndSelectionDecisions(t *testing.T) {
	mgr := &mockManager{userMemories: []domainmemory.UserMemory{
		{ID: "mem-selected", UserID: "ren", Statement: "Ren likes blue", State: domainmemory.MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: true},
		{ID: "mem-candidate", UserID: "ren", Statement: "Ren may like blue", State: domainmemory.MemoryStateCandidate, Sensitivity: "normal", Scope: "all_personas", Active: true},
		{ID: "mem-sensitive", UserID: "ren", Statement: "private blue detail", State: domainmemory.MemoryStateConfirmed, Sensitivity: "sensitive", Scope: "all_personas", Active: true},
		{ID: "mem-inactive", UserID: "ren", Statement: "inactive blue detail", State: domainmemory.MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: false},
		{ID: "mem-superseded", UserID: "ren", Statement: "old blue detail", State: domainmemory.MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: true, SupersededBy: "mem-newer"},
		{ID: "mem-decayed", UserID: "ren", Statement: "decayed blue detail", State: domainmemory.MemoryStateConfirmed, Sensitivity: "normal", Scope: "all_personas", Active: true, LifecycleStatus: "decayed"},
	}}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).
		WithUserMemoryStore(mgr, "ren")

	pack, err := engine.BeginTurn(context.Background(), "chat-owner", "blue")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	statuses := make(map[string]string)
	for _, item := range pack.ToTraceItems() {
		if item.MemoryID != "" {
			statuses[item.MemoryID] = item.Status
		}
	}
	if got := statuses["mem-selected"]; got != domconv.TraceStatusInjected {
		t.Errorf("selected UserMemory trace status=%q, want %q; statuses=%v", got, domconv.TraceStatusInjected, statuses)
	}
	for _, id := range []string{"mem-candidate", "mem-sensitive", "mem-inactive", "mem-superseded", "mem-decayed"} {
		if got, ok := statuses[id]; !ok || got == domconv.TraceStatusInjected {
			t.Errorf("rejected UserMemory %q trace status=%q present=%v, want non-injected reasoned item; statuses=%v", id, got, ok, statuses)
		}
	}
}

func TestBeginTurn_UserMemorySourceFailureIsPartialTrace(t *testing.T) {
	mgr := &mockManager{userMemoryErr: fmt.Errorf("user memory source unavailable")}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).
		WithUserMemoryStore(mgr, "ren")

	pack, err := engine.BeginTurn(context.Background(), "user-memory-failure", "blue")
	if err != nil {
		t.Fatalf("BeginTurn should continue after UserMemory source failure: %v", err)
	}
	if pack == nil {
		t.Fatal("BeginTurn returned a nil RecallPack")
	}
	foundFailure := false
	for _, item := range pack.ToTraceItems() {
		if item.Kind != "user_memory_source_failure" {
			continue
		}
		foundFailure = true
		if item.Status != domconv.TraceStatusSourceFailure {
			t.Errorf("UserMemory source failure status=%q, want %q", item.Status, domconv.TraceStatusSourceFailure)
		}
		if item.Summary != "" {
			t.Errorf("UserMemory source failure summary=%q, want redacted empty summary", item.Summary)
		}
	}
	if !foundFailure {
		t.Fatalf("UserMemory source failure was not traced: %+v", pack.ToTraceItems())
	}
}

func TestBeginTurn_ConversationRecallSourceFailureIsPartialTrace(t *testing.T) {
	mgr := &mockManager{recallFunc: func(context.Context, string, string, int) ([]domconv.Message, error) {
		return nil, fmt.Errorf("conversation recall source unavailable")
	}}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{})

	pack, err := engine.BeginTurn(context.Background(), "conversation-failure", "hello")
	if err != nil {
		t.Fatalf("BeginTurn should continue after conversation Recall failure: %v", err)
	}
	if pack == nil {
		t.Fatal("BeginTurn returned a nil RecallPack")
	}
	foundFailure := false
	for _, item := range pack.ToTraceItems() {
		if item.Kind != "conversation_recall_source_failure" {
			continue
		}
		foundFailure = true
		if item.Status != domconv.TraceStatusSourceFailure {
			t.Errorf("conversation source failure status=%q, want %q", item.Status, domconv.TraceStatusSourceFailure)
		}
	}
	if !foundFailure {
		t.Fatalf("conversation source failure was not traced: %+v", pack.ToTraceItems())
	}
}

func TestBeginTurn_AddsL0RollingSummaryFromActiveThread(t *testing.T) {
	thread := newEngineTestThread("s1", "general")
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

func TestCommitConversationTurn_BasicStore(t *testing.T) {
	stored := []string{}
	mgr := &mockManager{
		storeFunc: func(ctx context.Context, sessionID string, msg domconv.Message) error {
			stored = append(stored, string(msg.Speaker)+":"+msg.Msg)
			return nil
		},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "ren")

	request := engineTestConversationTurnRequest(string(modulecore.NewSessionID()), "hello", "hi there", domconv.SpeakerMio)
	if _, err := engine.CommitConversationTurn(context.Background(), request); err != nil {
		t.Fatalf("CommitConversationTurn failed: %v", err)
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

func TestCommitConversationTurn_WithDetector_NoBoundary(t *testing.T) {
	flushCalled := false
	mgr := &mockManager{
		flushThreadFunc: func(ctx context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error) {
			flushCalled = true
			return &domconv.ThreadSummary{}, nil
		},
	}
	detector := &mockDetector{
		result: domconv.ThreadBoundaryResult{ShouldCreateNew: false},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "ren").WithDetector(detector)

	request := engineTestConversationTurnRequest(string(modulecore.NewSessionID()), "私はGoが好きです", "覚えておきます", domconv.SpeakerMio)
	if _, err := engine.CommitConversationTurn(context.Background(), request); err != nil {
		t.Fatalf("CommitConversationTurn failed: %v", err)
	}
	if flushCalled {
		t.Error("FlushThread should NOT be called when no boundary detected")
	}
}

func TestCommitConversationTurn_WithDetector_Boundary(t *testing.T) {
	flushCalled := false
	createCalled := false
	mgr := &mockManager{
		flushThreadFunc: func(ctx context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error) {
			flushCalled = true
			return &domconv.ThreadSummary{}, nil
		},
		createThreadFunc: func(ctx context.Context, sessionID, domain string) (*domconv.Thread, error) {
			createCalled = true
			return newEngineTestThread(sessionID, domain), nil
		},
	}
	detector := &mockDetector{
		result: domconv.ThreadBoundaryResult{ShouldCreateNew: true, Reason: domconv.BoundaryKeyword},
	}
	engine := NewRealConversationEngine(mgr, domconv.PersonaState{}).WithUserMemoryStore(mgr, "ren").WithDetector(detector)

	request := engineTestConversationTurnRequest(string(modulecore.NewSessionID()), "new topic", "response", domconv.SpeakerMio)
	if _, err := engine.CommitConversationTurn(context.Background(), request); err != nil {
		t.Fatalf("CommitConversationTurn failed: %v", err)
	}
	if !flushCalled {
		t.Error("FlushThread should be called when boundary detected")
	}
	if !createCalled {
		t.Error("CreateThread should be called after flush")
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
		flushThreadFunc: func(ctx context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error) {
			flushCalled = true
			return &domconv.ThreadSummary{}, nil
		},
		createThreadFunc: func(ctx context.Context, sessionID, domain string) (*domconv.Thread, error) {
			createCalled = true
			return newEngineTestThread(sessionID, domain), nil
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
	thread := newEngineTestThread("s1", "programming")
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
		flushThreadFunc: func(ctx context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error) {
			flushCalled = true
			return &domconv.ThreadSummary{}, nil
		},
		createThreadFunc: func(ctx context.Context, sessionID, domain string) (*domconv.Thread, error) {
			createDomain = domain
			return newEngineTestThread(sessionID, domain), nil
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
