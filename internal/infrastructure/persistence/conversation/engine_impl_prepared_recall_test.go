package conversation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type preparedRecallReadyCaller interface {
	BeginTurnReady(context.Context, string, string, PreparedRecallReadyOption) (*domconv.RecallPack, error)
}

type preparedRecallMemoryStoreSpy struct{ calls int }

func (s *preparedRecallMemoryStoreSpy) ListUserMemories(context.Context, string, string, bool, int) ([]domainmemory.UserMemory, error) {
	s.calls++
	return nil, errors.New("legacy UserMemory read must not be called")
}

type preparedRecallCategoryRegistrySpy struct{ calls int }

func (s *preparedRecallCategoryRegistrySpy) Recall(context.Context, domconv.CategoryRecallQuery) (domconv.CategoryRecallResult, error) {
	s.calls++
	return domconv.CategoryRecallResult{}, errors.New("legacy category registry must not be called")
}

type preparedRecallExternalSpy struct {
	*mockManager
	calls int
}

func (s *preparedRecallExternalSpy) GetFreshSearchCache(context.Context, string, string, time.Time) (*l1sqlite.L1SearchCacheEntry, error) {
	s.calls++
	return nil, errors.New("legacy SearchCache read must not be called")
}

func (s *preparedRecallExternalSpy) SearchKnowledgeItemsFTS(context.Context, string, string, int) ([]l1sqlite.L1KnowledgeItem, error) {
	s.calls++
	return nil, errors.New("legacy Knowledge FTS read must not be called")
}

func (s *preparedRecallExternalSpy) SearchWikiPageIndex(context.Context, string, int) ([]l1sqlite.WikiPageIndexItem, error) {
	s.calls++
	return nil, errors.New("legacy Wiki index read must not be called")
}

func (s *preparedRecallExternalSpy) SearchKB(context.Context, string, string, int) ([]*domconv.Document, error) {
	s.calls++
	return nil, errors.New("legacy vector KB read must not be called")
}

func (s *preparedRecallExternalSpy) RelatedKnowledgeItems(context.Context, string, int, int) ([]l1sqlite.L1KnowledgeRelationHit, error) {
	s.calls++
	return nil, errors.New("legacy Knowledge relation read must not be called")
}

func TestBeginTurnReadyUsesPreparedOwnerViewAndRAMHead(t *testing.T) {
	const (
		ownerID       = "authenticated-user"
		userScope     = "user:authenticated-user"
		sourceVersion = "source-v1"
	)
	sessionID := string(modulecore.NewSessionID())

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	memory := domainmemory.UserMemory{
		ID: "memory-tea", UserID: ownerID, State: domainmemory.MemoryStateConfirmed,
		Scope: "all", Sensitivity: "normal", Active: true, Statement: "I prefer green tea",
	}
	knowledge := domconv.CategoryRecallRecord{
		Category: "knowledge", SourceID: "knowledge_l1", RecordID: "knowledge-tea",
		Title: "Green tea", Summary: "Green tea contains catechins.",
		ProvenanceURLs: []string{"https://example.test/green-tea"},
		RetrievedAt:    now.Add(-time.Minute), ValidatedAt: now.Add(-time.Minute),
		State: domconv.CategoryRecordStateValidated, Sensitivity: "normal", Scope: userScope,
		Roles: []string{"chat"},
	}
	projection, err := NewPreparedRecallProjectionOwner(ownerID, userScope, domconv.PreparedRecallMaxAggregateBytes)
	if err != nil {
		t.Fatalf("NewPreparedRecallProjectionOwner: %v", err)
	}
	if _, err := projection.BuildAndPublish(domconv.PreparedRecallViewInput{
		AuthenticatedOwnerID: ownerID, UserScope: userScope, SourceVersion: sourceVersion,
		MemoryBudget:         domconv.PreparedRecallMemoryBudget{AggregateLimitBytes: domconv.PreparedRecallMaxAggregateBytes},
		Coverage:             domconv.PreparedRecallCoverage{UserMemory: domconv.PreparedRecallCoveragePartial, Knowledge: domconv.PreparedRecallCoverageFull},
		EligibleUserMemories: []domainmemory.UserMemory{memory}, ValidatedCategoryRecords: []domconv.CategoryRecallRecord{knowledge},
	}, func(gotOwner, gotScope, gotVersion string) (PreparedRecallEligibilityRelease, bool) {
		if gotOwner != ownerID || gotScope != userScope || gotVersion != sourceVersion {
			return nil, false
		}
		return func() {}, true
	}); err != nil {
		t.Fatalf("BuildAndPublish: %v", err)
	}

	baseManager := &mockManager{
		recallFunc: func(context.Context, string, string, int) ([]domconv.Message, error) {
			t.Fatal("legacy ConversationManager.Recall was called")
			return nil, nil
		},
		getActiveThreadFunc: func(context.Context, string) (*domconv.Thread, error) {
			t.Fatal("legacy GetActiveThread was called")
			return nil, nil
		},
	}
	manager := &preparedRecallExternalSpy{mockManager: baseManager}
	validateCalls := 0
	baseManager.validatePreparationFunc = func(_ context.Context, preparation *domconv.ConversationTurnPreparation) error {
		validateCalls++
		if preparation == nil || preparation.SessionID != sessionID || preparation.OwnerID != ownerID {
			t.Fatalf("unexpected preparation: %#v", preparation)
		}
		return nil
	}
	thread := newEngineTestThread(sessionID, "tea")
	thread.Turns = []domconv.Message{
		domconv.NewMessage(domconv.SpeakerUser, "I asked about tea", nil),
		domconv.NewMessage(domconv.SpeakerMio, "We discussed green tea", nil),
	}
	preparation := &domconv.ConversationTurnPreparation{
		TurnID: modulecore.NewTurnID(), SessionID: sessionID, OwnerID: ownerID, Revision: 1, ActiveThread: thread,
	}

	userMemorySpy := &preparedRecallMemoryStoreSpy{}
	categorySpy := &preparedRecallCategoryRegistrySpy{}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{Name: "mio"})
	engine.WithUserMemoryStore(userMemorySpy, "spoofed-config-user")
	engine.WithCategoryRecallRegistry(categorySpy)
	engine.WithCategoryRecallScope("spoofed-config-scope")
	engine.WithKnowledgeRelationRecall(2)
	engineWithReady, ok := any(engine).(preparedRecallReadyCaller)
	if !ok {
		t.Fatal("RealConversationEngine does not expose the opt-in BeginTurnReady API")
	}

	scope, err := domaintool.NewToolExecutionScope(
		"request-prepared-recall", domaintool.ActorKindUser, ownerID, ownerID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	ctx := withPrivateSessionBinding(t, domaintool.WithToolExecutionScope(context.Background(), scope), sessionID, ownerID, 1)
	pack, err := engineWithReady.BeginTurnReady(ctx, sessionID, "green tea 最新", PreparedRecallReadyOption{
		ProjectionOwner: projection, ExpectedSourceVersion: sourceVersion, Preparation: preparation,
	})
	if err != nil {
		t.Fatalf("BeginTurnReady: %v", err)
	}
	if pack == nil {
		t.Fatal("BeginTurnReady returned nil RecallPack")
	}
	if pack.UserProfile.UserID != ownerID || len(pack.UserProfile.Facts) != 1 || !strings.Contains(pack.UserProfile.Facts[0], "green tea") {
		t.Fatalf("prepared UserMemory not mapped into owner-bound UserProfile: %#v", pack.UserProfile)
	}
	if len(pack.UserMemoryRecallDecisions) != 1 || pack.UserMemoryRecallDecisions[0].Item.UserID != ownerID || !pack.UserMemoryRecallDecisions[0].Selected {
		t.Fatalf("prepared UserMemory decisions not preserved: %#v", pack.UserMemoryRecallDecisions)
	}
	if len(pack.CategorySnippets) != 1 || pack.CategorySnippets[0].RecordID != "knowledge-tea" {
		t.Fatalf("prepared Knowledge not mapped into CategorySnippets: %#v", pack.CategorySnippets)
	}
	if filtered := pack.FilterForRole("chat"); len(filtered.CategorySnippets) != 1 {
		t.Fatalf("owner-authorized prepared Knowledge was dropped by the existing chat role filter: %#v", filtered.RejectedTraceItems)
	}
	traceItems := pack.ToTraceItems()
	coverageFound := false
	for _, item := range traceItems {
		if item.Kind == "prepared_recall_projection" && item.SourceType == "user_memory" {
			coverageFound = item.SourceID == sourceVersion && item.Status == domconv.CategoryRecallStatusPartial && strings.Contains(item.Reason, "posting_visits=")
		}
	}
	if !coverageFound {
		t.Fatalf("prepared recall coverage/version/visit trace missing: %#v", traceItems)
	}
	if len(pack.ShortContext) != 2 || pack.ShortContext[1].Msg != "We discussed green tea" {
		t.Fatalf("prepared RAM head was not kept as short context: %#v", pack.ShortContext)
	}
	if validateCalls != 1 {
		t.Fatalf("prepared conversation lease validation calls = %d, want 1", validateCalls)
	}
	if userMemorySpy.calls != 0 || categorySpy.calls != 0 || manager.calls != 0 {
		t.Fatalf("legacy recall dependencies called: user_memory=%d category=%d external=%d", userMemorySpy.calls, categorySpy.calls, manager.calls)
	}
	if len(baseManager.commitRequests) != 0 {
		t.Fatalf("BeginTurnReady must not commit conversation state: %d commits", len(baseManager.commitRequests))
	}
}

func TestBeginTurnReadyFailsClosedForMissingOrSpoofedScope(t *testing.T) {
	const (
		ownerID       = "authenticated-user"
		userScope     = "user:authenticated-user"
		sourceVersion = "source-v1"
	)
	sessionID := string(modulecore.NewSessionID())
	projection, err := NewPreparedRecallProjectionOwner(ownerID, userScope, domconv.PreparedRecallMaxAggregateBytes)
	if err != nil {
		t.Fatalf("NewPreparedRecallProjectionOwner: %v", err)
	}
	if _, err := projection.BuildAndPublish(domconv.PreparedRecallViewInput{
		AuthenticatedOwnerID: ownerID, UserScope: userScope, SourceVersion: sourceVersion,
		MemoryBudget: domconv.PreparedRecallMemoryBudget{AggregateLimitBytes: domconv.PreparedRecallMaxAggregateBytes},
		Coverage:     domconv.PreparedRecallCoverage{UserMemory: domconv.PreparedRecallCoverageNone, Knowledge: domconv.PreparedRecallCoverageNone},
	}, func(string, string, string) (PreparedRecallEligibilityRelease, bool) { return func() {}, true }); err != nil {
		t.Fatalf("BuildAndPublish: %v", err)
	}

	engine := NewRealConversationEngine(&mockManager{}, domconv.PersonaState{Name: "mio"})
	ready, ok := any(engine).(preparedRecallReadyCaller)
	if !ok {
		t.Fatal("RealConversationEngine does not expose the opt-in BeginTurnReady API")
	}
	preparation := &domconv.ConversationTurnPreparation{
		TurnID: modulecore.NewTurnID(), SessionID: sessionID, OwnerID: ownerID, Revision: 1,
		ActiveThread: newEngineTestThread(sessionID, "general"),
	}
	option := PreparedRecallReadyOption{ProjectionOwner: projection, ExpectedSourceVersion: sourceVersion, Preparation: preparation}
	if _, err := ready.BeginTurnReady(withPrivateSessionBinding(t, preparedRecallContext(t, ownerID), sessionID, ownerID, 1), sessionID, "remember this", PreparedRecallReadyOption{
		ProjectionOwner: projection, ExpectedSourceVersion: sourceVersion,
	}); err == nil || PreparedRecallReadyErrorCodeOf(err) != PreparedRecallReadyErrorContextNotReady {
		t.Fatalf("missing prepared head error code = %q, err=%v", PreparedRecallReadyErrorCodeOf(err), err)
	}

	for _, tc := range []struct {
		name     string
		ctx      context.Context
		wantCode string
	}{
		{name: "missing scope", ctx: withPrivateSessionBinding(t, context.Background(), sessionID, ownerID, 1), wantCode: PreparedRecallReadyErrorScopeDenied},
		{name: "spoofed owner scope", ctx: withPrivateSessionBinding(t, preparedRecallContext(t, "other-user"), sessionID, ownerID, 1), wantCode: PreparedRecallReadyErrorScopeDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ready.BeginTurnReady(tc.ctx, sessionID, "remember this", option)
			if err == nil || PreparedRecallReadyErrorCodeOf(err) != tc.wantCode {
				t.Fatalf("BeginTurnReady error code = %q, err=%v; want %q", PreparedRecallReadyErrorCodeOf(err), err, tc.wantCode)
			}
		})
	}
}

func TestBeginTurnReadyStaleProjectionDoesNotFallBack(t *testing.T) {
	const (
		ownerID       = "authenticated-user"
		userScope     = "user:authenticated-user"
		sourceVersion = "source-v1"
	)
	sessionID := string(modulecore.NewSessionID())
	projection, err := NewPreparedRecallProjectionOwner(ownerID, userScope, domconv.PreparedRecallMaxAggregateBytes)
	if err != nil {
		t.Fatalf("NewPreparedRecallProjectionOwner: %v", err)
	}
	if _, err := projection.BuildAndPublish(domconv.PreparedRecallViewInput{
		AuthenticatedOwnerID: ownerID, UserScope: userScope, SourceVersion: sourceVersion,
		MemoryBudget: domconv.PreparedRecallMemoryBudget{AggregateLimitBytes: domconv.PreparedRecallMaxAggregateBytes},
		Coverage:     domconv.PreparedRecallCoverage{UserMemory: domconv.PreparedRecallCoverageNone, Knowledge: domconv.PreparedRecallCoverageNone},
	}, func(string, string, string) (PreparedRecallEligibilityRelease, bool) { return func() {}, true }); err != nil {
		t.Fatalf("BuildAndPublish: %v", err)
	}

	manager := &mockManager{}
	userMemorySpy := &preparedRecallMemoryStoreSpy{}
	categorySpy := &preparedRecallCategoryRegistrySpy{}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{Name: "mio"})
	engine.WithUserMemoryStore(userMemorySpy, ownerID)
	engine.WithCategoryRecallRegistry(categorySpy)
	ready, ok := any(engine).(preparedRecallReadyCaller)
	if !ok {
		t.Fatal("RealConversationEngine does not expose the opt-in BeginTurnReady API")
	}
	ctx := withPrivateSessionBinding(t, preparedRecallContext(t, ownerID), sessionID, ownerID, 1)
	option := PreparedRecallReadyOption{
		ProjectionOwner: projection, ExpectedSourceVersion: "stale-version",
		Preparation: &domconv.ConversationTurnPreparation{
			TurnID: modulecore.NewTurnID(), SessionID: sessionID, OwnerID: ownerID, Revision: 1,
			ActiveThread: newEngineTestThread(sessionID, "general"),
		},
	}
	if _, err := ready.BeginTurnReady(ctx, sessionID, "remember this", option); err == nil || PreparedRecallReadyErrorCodeOf(err) != PreparedRecallReadyErrorProjectionStale {
		t.Fatalf("stale ready projection error code = %q, err=%v", PreparedRecallReadyErrorCodeOf(err), err)
	}
	if userMemorySpy.calls != 0 || categorySpy.calls != 0 || len(manager.commitRequests) != 0 {
		t.Fatalf("stale projection fell back to sync path: user_memory=%d category=%d commits=%d", userMemorySpy.calls, categorySpy.calls, len(manager.commitRequests))
	}
}

func preparedRecallContext(t *testing.T, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		"request-prepared-recall-test", domaintool.ActorKindUser, ownerID, ownerID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func withPrivateSessionBinding(t *testing.T, ctx context.Context, sessionID, ownerID string, generation domconv.BindingGeneration) context.Context {
	t.Helper()
	address, err := domconv.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	binding, err := domconv.NewSessionBinding(modulecore.SessionID(sessionID), address, domconv.SessionAudiencePrivate, ownerID, generation)
	if err != nil {
		t.Fatalf("NewSessionBinding: %v", err)
	}
	return domconv.WithSessionBinding(ctx, binding)
}

type preparedChatTurnCaller interface {
	PrepareChatTurn(context.Context, string, string, string) (*domconv.PreparedChatTurn, error)
}

type preparedRecallRuntimeWirer interface {
	WithPreparedRecallRuntime(*PreparedRecallRuntime) *RealConversationEngine
}

type preparedChatTurnManager struct {
	*mockManager
	prepareCalls    int
	releaseCalls    int
	preparedOwner   string
	preparedTurn    string
	preparedSession string
	prepareErr      error
	activeThread    *domconv.Thread
}

func (m *preparedChatTurnManager) PrepareConversationTurn(_ context.Context, turnID modulecore.TurnID, sessionID, ownerID string) (*domconv.ConversationTurnPreparation, error) {
	m.prepareCalls++
	m.preparedTurn = string(turnID)
	m.preparedSession = sessionID
	m.preparedOwner = ownerID
	if m.prepareErr != nil {
		return nil, m.prepareErr
	}
	activeThread := m.activeThread
	if activeThread == nil {
		activeThread = newEngineTestThread(sessionID, "prepared-chat")
	}
	return &domconv.ConversationTurnPreparation{
		TurnID: turnID, SessionID: sessionID, OwnerID: ownerID, Revision: 7,
		ActiveThread: activeThread,
	}, nil
}

func (m *preparedChatTurnManager) ReleaseConversationTurnPreparation(_ context.Context, preparation *domconv.ConversationTurnPreparation) error {
	m.releaseCalls++
	if preparation == nil {
		return domconv.ErrConversationTurnInvalid
	}
	return nil
}

func (m *preparedChatTurnManager) retainConversationTurnPublicationEpoch(preparation *domconv.ConversationTurnPreparation) (conversationTurnPublicationEpochGuard, error) {
	if preparation == nil {
		return nil, domconv.ErrConversationTurnInvalid
	}
	epoch := &conversationTurnPublicationEpoch{generation: 1}
	epoch.refs.Store(1)
	manager := &RealConversationManager{publicationEpochs: map[string]*conversationTurnPublicationEpoch{preparation.SessionID: epoch}}
	return &conversationTurnPublicationEpochLease{
		manager: manager, sessionID: preparation.SessionID, ownerID: preparation.OwnerID,
		epoch: epoch, generation: epoch.generation,
	}, nil
}

func TestPrepareChatTurnUsesCanonicalPrivateBindingAndWarmRecall(t *testing.T) {
	const ownerID = "ren"
	sessionID := string(modulecore.NewSessionID())
	legacyRecallCalls := 0
	manager := &preparedChatTurnManager{mockManager: &mockManager{
		recallFunc: func(context.Context, string, string, int) ([]domconv.Message, error) {
			legacyRecallCalls++
			t.Fatal("prepared chat called legacy conversation recall")
			return nil, nil
		},
	}}
	source := &preparedRecallRuntimeFakeSource{read: func(_ context.Context, gotOwner string, _ int) ([]domainmemory.UserMemory, string, error) {
		if gotOwner != ownerID {
			return nil, "", errors.New("prepared recall read used wrong owner")
		}
		return []domainmemory.UserMemory{{
			ID: "ren-tea", UserID: ownerID, Namespace: "user:ren", Type: domainmemory.UserMemoryTypePreference,
			Statement: "Ren prefers green tea", State: domainmemory.MemoryStateConfirmed, Sensitivity: "normal", Active: true,
		}}, domconv.PreparedRecallCoverageFull, nil
	}}
	runtime, err := NewPreparedRecallRuntime(ownerID, source)
	if err != nil {
		t.Fatalf("NewPreparedRecallRuntime: %v", err)
	}
	defer runtime.Close()
	if err := runtime.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{Name: "mio"}).
		WithUserMemoryStore(manager, "spoofed-config-owner")
	wirer, ok := any(engine).(preparedRecallRuntimeWirer)
	if !ok {
		t.Fatal("RealConversationEngine does not expose WithPreparedRecallRuntime")
	}
	engine = wirer.WithPreparedRecallRuntime(runtime)
	ctx := privateSessionBindingContext(t, sessionID, ownerID, true)
	caller, ok := any(engine).(preparedChatTurnCaller)
	if !ok {
		t.Fatal("RealConversationEngine does not expose PrepareChatTurn")
	}
	prepared, err := caller.PrepareChatTurn(ctx, string(modulecore.NewTurnID()), sessionID, "green tea")
	if err != nil {
		t.Fatalf("PrepareChatTurn: %v", err)
	}
	if prepared == nil || prepared.Guard == nil {
		t.Fatal("PrepareChatTurn did not return a turn guard")
	}
	defer func() {
		if err := prepared.Guard.Release(); err != nil {
			t.Errorf("PreparedChatTurn.Guard.Release: %v", err)
		}
	}()
	pack, preparation := prepared.RecallPack, prepared.Preparation
	if preparation == nil || preparation.OwnerID != ownerID || manager.preparedOwner != ownerID || manager.prepareCalls != 1 {
		t.Fatalf("prepared owner/calls = %q/%d, prep=%+v; want binding owner and one manager head acquisition", manager.preparedOwner, manager.prepareCalls, preparation)
	}
	if pack == nil || pack.UserProfile.UserID != ownerID || len(pack.UserProfile.Facts) != 1 {
		t.Fatalf("RecallPack did not use the private prepared projection: %+v", pack)
	}
	if prepared.Guard.OwnerID() != ownerID || prepared.Guard.SourceVersion() == "" || prepared.Guard.BindingGeneration() != 1 || !prepared.Guard.MatchesPreparation(preparation) {
		t.Fatalf("prepared guard identity does not match the private turn: owner=%q source=%q generation=%d", prepared.Guard.OwnerID(), prepared.Guard.SourceVersion(), prepared.Guard.BindingGeneration())
	}
	if err := prepared.Guard.Validate(); err != nil {
		t.Fatalf("prepared guard validation: %v", err)
	}
	enqueued := false
	if err := prepared.Guard.Use(func() error { enqueued = true; return nil }); err != nil || !enqueued {
		t.Fatalf("guarded enqueue: called=%t err=%v", enqueued, err)
	}
	if source.callCount() != 1 || len(manager.commitRequests) != 0 || legacyRecallCalls != 0 {
		t.Fatalf("prepared path performed per-turn or legacy I/O: source reads=%d legacy recall=%d commits=%d", source.callCount(), legacyRecallCalls, len(manager.commitRequests))
	}
	runtime.HandleUserMemoryMutation(l1sqlite.UserMemoryMutationNotice{UserID: ownerID, ChangeID: "after-prepare", MemoryIDs: []string{"ren-tea"}})
	if err := prepared.Guard.Validate(); PreparedRecallReadyErrorCodeOf(err) != PreparedRecallReadyErrorProjectionStale {
		t.Fatalf("guard validation after UserMemory revoke error=%v; want projection stale", err)
	}
	enqueued = false
	if err := prepared.Guard.Use(func() error { enqueued = true; return nil }); PreparedRecallReadyErrorCodeOf(err) != PreparedRecallReadyErrorProjectionStale || enqueued {
		t.Fatalf("revoked guard admitted enqueue: called=%t err=%v", enqueued, err)
	}
}

func TestPreparedChatTurnPublicationGuardSurvivesHeadReleaseAndDropsOnProjectionRevoke(t *testing.T) {
	guard, projection, generation := newPreparedChatTurnPublicationGuardTest(t)
	headEpoch := guard.headEpoch.(*conversationTurnPublicationEpochLease)
	headManager := headEpoch.manager
	var retained domconv.PreparedChatTurnPublicationGuard
	if err := guard.Use(func() error {
		var err error
		retained, err = guard.RetainPublication()
		return err
	}); err != nil {
		t.Fatalf("retain publication qualification inside Use: %v", err)
	}
	if retained == nil {
		t.Fatal("RetainPublication returned no writer qualification")
	}
	if got := generation.readers.Load(); got != 2 {
		t.Fatalf("generation readers after retain=%d, want source lease and publication ticket", got)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("release prepared-turn head guard: %v", err)
	}
	claimed := false
	if err := retained.Use(func() error { claimed = true; return nil }); err != nil || !claimed {
		t.Fatalf("retained claim after head release: claimed=%t err=%v", claimed, err)
	}
	reservation, err := headManager.beginConversationThreadLifecycle(headEpoch.sessionID, headEpoch.ownerID, false)
	if err != nil {
		t.Fatalf("begin reset/head lifecycle: %v", err)
	}
	headManager.finishConversationThreadLifecycle(headEpoch.sessionID, reservation, false)
	claimed = false
	if err := retained.Use(func() error { claimed = true; return nil }); !errors.Is(err, domconv.ErrConversationTurnConflict) || claimed {
		t.Fatalf("claim after reset lifecycle: claimed=%t err=%v, want stale and unclaimed", claimed, err)
	}
	if err := projection.Revoke("owner", "user:owner"); err != nil {
		t.Fatalf("revoke source projection: %v", err)
	}
	claimed = false
	if err := retained.Use(func() error { claimed = true; return nil }); !errors.Is(err, ErrPreparedRecallProjectionStale) || claimed {
		t.Fatalf("claim after projection revoke: claimed=%t err=%v, want stale and unclaimed", claimed, err)
	}
	retained.Release()
	if got := generation.readers.Load(); got != 0 {
		t.Fatalf("generation readers after ticket release=%d, want zero", got)
	}
	projection.mu.Lock()
	_, generationTracked := projection.generations[generation]
	projection.mu.Unlock()
	if generationTracked {
		t.Fatal("revoked generation remained retained after its publication ticket was released")
	}
}

func newPreparedChatTurnPublicationGuardTest(t *testing.T) (*preparedChatTurnGuard, *PreparedRecallProjectionOwner, *preparedRecallProjectionGeneration) {
	t.Helper()
	const ownerID = "owner"
	const scope = "user:owner"
	sessionID := modulecore.NewSessionID()
	address, err := domconv.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	binding, err := domconv.NewSessionBinding(sessionID, address, domconv.SessionAudiencePrivate, ownerID, 7)
	if err != nil {
		t.Fatalf("NewSessionBinding: %v", err)
	}
	generation := &preparedRecallProjectionGeneration{sourceVersion: "source-v1", current: true}
	projection := &PreparedRecallProjectionOwner{
		ownerID: ownerID, scope: scope, current: generation,
		generations: map[*preparedRecallProjectionGeneration]struct{}{generation: {}},
	}
	lease, status, err := projection.Acquire(ownerID, scope, generation.sourceVersion)
	if err != nil || status != domconv.PreparedRecallPreparationReady {
		t.Fatalf("Acquire projection status=%q err=%v", status, err)
	}
	preparation := &domconv.ConversationTurnPreparation{
		TurnID: modulecore.NewTurnID(), SessionID: string(sessionID), OwnerID: ownerID, Revision: 1,
	}
	headEpochManager := &RealConversationManager{
		turnOverlays: map[string]*conversationTurnOverlay{
			string(sessionID): {ownerID: ownerID, revision: preparation.Revision, preparation: cloneConversationTurnPreparation(preparation)},
		},
	}
	headEpoch, err := headEpochManager.retainConversationTurnPublicationEpoch(preparation)
	if err != nil || headEpoch == nil {
		t.Fatalf("retain head lifecycle epoch: ticket=%v err=%v", headEpoch, err)
	}
	guard := &preparedChatTurnGuard{
		binding:     binding,
		preparation: preparation,
		ownerID:     ownerID, sourceVersion: generation.sourceVersion, userScope: scope, projectionLease: lease,
		headEpoch: headEpoch,
	}
	return guard, projection, generation
}

func privateSessionBindingContext(t *testing.T, sessionID, ownerID string, delegated bool) context.Context {
	t.Helper()
	address, err := domconv.NewChannelAddress("viewer", "cmd:"+ownerID)
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	binding, err := domconv.NewSessionBinding(modulecore.SessionID(sessionID), address, domconv.SessionAudiencePrivate, ownerID, 1)
	if err != nil {
		t.Fatalf("NewSessionBinding: %v", err)
	}
	scope, err := domaintool.NewToolExecutionScope(
		"prepared-chat-test", domaintool.ActorKindUser, ownerID, ownerID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	ctx := domconv.WithSessionBinding(domaintool.WithToolExecutionScope(context.Background(), scope), binding)
	if delegated {
		ctx, err = domaintool.DeriveAgentToolExecutionScope(ctx, "mio-turn", "mio", "conversation", "reply", false)
		if err != nil {
			t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
		}
	}
	return ctx
}

func TestPrepareChatTurnPublicUsesOnlyBoundConversationHead(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	thread := newEngineTestThread(sessionID, "public-topic")
	thread.Turns = []domconv.Message{
		domconv.NewMessage(domconv.SpeakerUser, "public question", nil),
		domconv.NewMessage(domconv.SpeakerMio, "public answer", nil),
		domconv.NewMessage(domconv.SpeakerSystem, "must not enter public context", nil),
	}
	manager := &preparedChatTurnManager{mockManager: &mockManager{}, activeThread: thread}
	userMemorySpy := &preparedRecallMemoryStoreSpy{}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{Name: "mio"}).
		WithUserMemoryStore(userMemorySpy, "configured-user")
	address, err := domconv.NewChannelAddress("viewer", "anonymous-chat")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	binding, err := domconv.NewSessionBinding(modulecore.SessionID(sessionID), address, domconv.SessionAudiencePublic, "", 4)
	if err != nil {
		t.Fatalf("NewSessionBinding: %v", err)
	}
	ctx := domconv.WithSessionBinding(context.Background(), binding)
	caller := any(engine).(preparedChatTurnCaller)
	prepared, err := caller.PrepareChatTurn(ctx, string(modulecore.NewTurnID()), sessionID, "next question")
	if err != nil {
		t.Fatalf("PrepareChatTurn: %v", err)
	}
	if prepared == nil || prepared.Guard == nil || prepared.Preparation == nil || prepared.Preparation.OwnerID != "public:"+sessionID || manager.preparedOwner != "public:"+sessionID {
		t.Fatalf("public preparation owner=%q, manager owner=%q, want public partition", prepared.Preparation.OwnerID, manager.preparedOwner)
	}
	defer func() {
		if err := prepared.Guard.Release(); err != nil {
			t.Errorf("public PreparedChatTurn.Guard.Release: %v", err)
		}
	}()
	pack := prepared.RecallPack
	if prepared.Guard.OwnerID() != "" || prepared.Guard.SourceVersion() != "" || prepared.Guard.BindingGeneration() != 4 {
		t.Fatalf("public guard exposed a user-memory owner: owner=%q source=%q generation=%d", prepared.Guard.OwnerID(), prepared.Guard.SourceVersion(), prepared.Guard.BindingGeneration())
	}
	if err := prepared.Guard.Validate(); err != nil {
		t.Fatalf("public guard validation: %v", err)
	}
	if pack.UserProfile.UserID != "" || len(pack.UserMemoryRecallDecisions) != 0 || userMemorySpy.calls != 0 {
		t.Fatalf("public ready pack accessed UserMemory: profile=%+v decisions=%d reads=%d", pack.UserProfile, len(pack.UserMemoryRecallDecisions), userMemorySpy.calls)
	}
	if len(pack.ShortContext) != 2 || pack.ShortContext[0].Msg != "public question" || pack.ShortContext[1].Msg != "public answer" {
		t.Fatalf("public pack was not head-only: %#v", pack.ShortContext)
	}
}

func TestPrepareChatTurnPublicRejectsConflictingPresentToolScope(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	address, err := domconv.NewChannelAddress("viewer", "anonymous-chat")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	binding, err := domconv.NewSessionBinding(modulecore.SessionID(sessionID), address, domconv.SessionAudiencePublic, "", 1)
	if err != nil {
		t.Fatalf("NewSessionBinding: %v", err)
	}
	userScope, err := domaintool.NewToolExecutionScope(
		"private-user-scope", domaintool.ActorKindUser, "ren", "ren",
		[]string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	manager := &preparedChatTurnManager{mockManager: &mockManager{}}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{}).WithUserMemoryStore(manager, "ren")
	caller := any(engine).(preparedChatTurnCaller)
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "private scope cannot authorize public session", ctx: domconv.WithSessionBinding(domaintool.WithToolExecutionScope(context.Background(), userScope), binding)},
		{name: "invalid scope is rejected", ctx: domconv.WithSessionBinding(domaintool.WithToolExecutionScope(context.Background(), domaintool.ToolExecutionScope{}), binding)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := caller.PrepareChatTurn(tc.ctx, string(modulecore.NewTurnID()), sessionID, "hello")
			if err == nil || PreparedRecallReadyErrorCodeOf(err) != PreparedRecallReadyErrorScopeDenied {
				t.Fatalf("PrepareChatTurn error code=%q err=%v; want scope denied", PreparedRecallReadyErrorCodeOf(err), err)
			}
		})
	}
	if manager.prepareCalls != 0 {
		t.Fatalf("conflicting scope reached manager preparation %d times", manager.prepareCalls)
	}
}

func TestPrepareChatTurnFailsClosedForAbsentStaleOrMismatchedBinding(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	manager := &preparedChatTurnManager{mockManager: &mockManager{}}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{}).WithUserMemoryStore(manager, "configured-user")
	caller := any(engine).(preparedChatTurnCaller)
	turnID := string(modulecore.NewTurnID())
	otherSessionID := string(modulecore.NewSessionID())
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		wantCode string
	}{
		{name: "absent binding cannot use configured owner", ctx: preparedRecallContext(t, "configured-user"), wantCode: PreparedRecallReadyErrorContextNotReady},
		{name: "zero generation", ctx: withPrivateSessionBinding(t, preparedRecallContext(t, "configured-user"), sessionID, "configured-user", domconv.BindingGenerationUnavailable), wantCode: PreparedRecallReadyErrorContextNotReady},
		{name: "session mismatch", ctx: withPrivateSessionBinding(t, preparedRecallContext(t, "configured-user"), otherSessionID, "configured-user", 1), wantCode: PreparedRecallReadyErrorConflict},
		{name: "owner scope mismatch", ctx: withPrivateSessionBinding(t, preparedRecallContext(t, "other-user"), sessionID, "configured-user", 1), wantCode: PreparedRecallReadyErrorScopeDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := caller.PrepareChatTurn(tc.ctx, turnID, sessionID, "hello")
			if err == nil || PreparedRecallReadyErrorCodeOf(err) != tc.wantCode {
				t.Fatalf("PrepareChatTurn error code=%q err=%v; want %q", PreparedRecallReadyErrorCodeOf(err), err, tc.wantCode)
			}
		})
	}
	if manager.prepareCalls != 0 {
		t.Fatalf("invalid binding reached manager head preparation %d times", manager.prepareCalls)
	}
}

func TestPrepareChatTurnReleasesHeadWhenPrivateRuntimeIsUnavailable(t *testing.T) {
	const ownerID = "ren"
	sessionID := string(modulecore.NewSessionID())
	manager := &preparedChatTurnManager{mockManager: &mockManager{}}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{}).WithUserMemoryStore(manager, "spoofed-owner")
	caller := any(engine).(preparedChatTurnCaller)
	prepared, err := caller.PrepareChatTurn(privateSessionBindingContext(t, sessionID, ownerID, false), string(modulecore.NewTurnID()), sessionID, "hello")
	if err == nil || PreparedRecallReadyErrorCodeOf(err) != PreparedRecallReadyErrorProjectionMissing {
		t.Fatalf("PrepareChatTurn error code=%q err=%v; want projection unavailable", PreparedRecallReadyErrorCodeOf(err), err)
	}
	if prepared != nil || manager.prepareCalls != 1 || manager.releaseCalls != 1 || manager.preparedOwner != ownerID {
		t.Fatalf("head release state: returned result=%+v prepare calls=%d release calls=%d owner=%q", prepared, manager.prepareCalls, manager.releaseCalls, manager.preparedOwner)
	}
}

func TestPreparedChatTurnHeadLeaseBlocksCompetingTurnsUntilCommit(t *testing.T) {
	const ownerID = "ren"
	ctx := context.Background()
	sessionID := string(modulecore.NewSessionID())
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "prepared-chat-head.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	seed := managerTurnTestRequest("prepared-chat-head-seed", sessionID)
	seed.OwnerID = ownerID
	if _, err := store.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed conversation head: %v", err)
	}
	redis := newMockRedisStore()
	if err := redis.SaveSession(ctx, domconv.NewSessionConversation(sessionID, ownerID)); err != nil {
		t.Fatalf("save canonical session owner: %v", err)
	}
	manager := &RealConversationManager{redisStore: redis, l1Store: store}
	source := &preparedRecallRuntimeFakeSource{read: func(_ context.Context, gotOwner string, _ int) ([]domainmemory.UserMemory, string, error) {
		if gotOwner != ownerID {
			return nil, "", errors.New("prepared recall warm used wrong owner")
		}
		return nil, domconv.PreparedRecallCoverageFull, nil
	}}
	runtime, err := NewPreparedRecallRuntime(ownerID, source)
	if err != nil {
		t.Fatalf("NewPreparedRecallRuntime: %v", err)
	}
	defer runtime.Close()
	if err := runtime.Warm(ctx); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{Name: "mio"}).WithPreparedRecallRuntime(runtime)
	boundCtx := privateSessionBindingContext(t, sessionID, ownerID, false)
	prepared, err := engine.PrepareChatTurn(boundCtx, string(modulecore.NewTurnID()), sessionID, "continue")
	if err != nil {
		t.Fatalf("PrepareChatTurn: %v", err)
	}
	defer func() {
		if err := prepared.Guard.Release(); err != nil {
			t.Errorf("PreparedChatTurn.Guard.Release: %v", err)
		}
	}()
	if err := prepared.Guard.Validate(); err != nil {
		t.Fatalf("validate live prepared head: %v", err)
	}
	if competing, err := manager.PrepareConversationTurn(ctx, modulecore.NewTurnID(), sessionID, ownerID); competing != nil || !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("competing head preparation=%+v err=%v, want conflict while current lease is live", competing, err)
	}

	request := engineTestConversationTurnRequest(sessionID, "continue", "reply", domconv.SpeakerMio)
	request.TurnID = prepared.Preparation.TurnID
	request.Preparation = prepared.Preparation
	if _, err := engine.CommitConversationTurn(boundCtx, request); err != nil {
		t.Fatalf("commit prepared head: %v", err)
	}
	if err := prepared.Guard.Validate(); err == nil {
		t.Fatal("Validate succeeded after EndTurn consumed the prepared head")
	}
	used := false
	if err := prepared.Guard.Use(func() error { used = true; return nil }); err != nil || !used {
		t.Fatalf("post-commit publication guard: used=%t err=%v; want projection/binding-only success", used, err)
	}
	next, err := engine.PrepareConversationTurn(boundCtx, string(modulecore.NewTurnID()), sessionID)
	if err != nil || next == nil {
		t.Fatalf("next head preparation after commit=%+v err=%v, want the advanced head", next, err)
	}
	if err := engine.ReleaseConversationTurnPreparation(ctx, next); err != nil {
		t.Fatalf("release next head preparation: %v", err)
	}
}

func TestCommitConversationTurnUsesBindingOwnerAndPublicPartition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		public bool
	}{
		{name: "private authenticated user"},
		{name: "public session partition", public: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ownerID := "ren"
			sessionID := string(modulecore.NewSessionID())
			ctx := privateSessionBindingContext(t, sessionID, ownerID, true)
			wantOwner := ownerID
			if tc.public {
				address, err := domconv.NewChannelAddress("viewer", "anonymous")
				if err != nil {
					t.Fatalf("NewChannelAddress: %v", err)
				}
				binding, err := domconv.NewSessionBinding(modulecore.SessionID(sessionID), address, domconv.SessionAudiencePublic, "", 3)
				if err != nil {
					t.Fatalf("NewSessionBinding: %v", err)
				}
				scope, err := domaintool.NewToolExecutionScope("public-end-turn", domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
				if err != nil {
					t.Fatalf("NewToolExecutionScope: %v", err)
				}
				ctx = domconv.WithSessionBinding(domaintool.WithToolExecutionScope(context.Background(), scope), binding)
				wantOwner = "public:" + sessionID
			}
			var committed domconv.ConversationTurnRequest
			manager := &mockManager{commitTurnFunc: func(_ context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
				committed = request
				return domconv.ConversationTurnResult{TurnID: request.TurnID, SessionID: request.SessionID, Status: domconv.ConversationTurnCompleted}, nil
			}}
			engine := NewRealConversationEngine(manager, domconv.PersonaState{}).WithUserMemoryStore(manager, "spoofed-config-owner")
			request := engineTestConversationTurnRequest(sessionID, "user message", "reply", domconv.SpeakerMio)
			request.Preparation = &domconv.ConversationTurnPreparation{
				TurnID: request.TurnID, SessionID: sessionID, OwnerID: wantOwner, Revision: 2,
				ActiveThread: newEngineTestThread(sessionID, "bound-domain"),
			}
			if _, err := engine.CommitConversationTurn(ctx, request); err != nil {
				t.Fatalf("CommitConversationTurn: %v", err)
			}
			if committed.OwnerID != wantOwner {
				t.Fatalf("committed owner=%q, want %q", committed.OwnerID, wantOwner)
			}
		})
	}
}

type mioThreadBoundaryClassifierTestStub struct {
	classification domainagent.MioTurnClassification
	calls          int
}

func (c *mioThreadBoundaryClassifierTestStub) Classify(context.Context, domconv.TurnInput) (routing.Decision, error) {
	return routing.NewDecision(routing.RouteCHAT, 0.95, "chat"), nil
}

func (c *mioThreadBoundaryClassifierTestStub) ClassifyWithMioTurnOutcome(context.Context, llm.GenerateRequest, *routing.Route) (domainagent.MioTurnClassification, error) {
	c.calls++
	return c.classification, nil
}

type mioThreadBoundaryRuleDictionaryTestStub struct{}

func (mioThreadBoundaryRuleDictionaryTestStub) Match(domconv.TurnInput) (routing.Route, float64, bool) {
	return "", 0, false
}

type mioThreadBoundaryProviderTestStub struct{ calls int }

func (p *mioThreadBoundaryProviderTestStub) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	return llm.GenerateResponse{}, errors.New("combined Mio test must not invoke a second provider")
}

func (*mioThreadBoundaryProviderTestStub) Name() string { return "boundary-test" }

func newPreparedMioBoundaryHarness(t *testing.T, userMessage string, proposal domconv.ThreadBoundaryProposal) (
	*preparedChatTurnManager,
	domconv.TurnInput,
	context.Context,
	*domainagent.MioAgent,
	*domainagent.MioChatDraft,
	*mioThreadBoundaryClassifierTestStub,
	*mioThreadBoundaryProviderTestStub,
) {
	t.Helper()
	sessionID := string(modulecore.NewSessionID())
	address, err := domconv.NewChannelAddress("viewer", "mio-boundary")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	input, err := domconv.NewTurnInput(modulecore.NewTaskID(), userMessage, address)
	if err != nil {
		t.Fatalf("NewTurnInput: %v", err)
	}
	input = input.WithSessionID(sessionID).WithViewerRecipient("mio")
	binding, err := domconv.NewSessionBinding(modulecore.SessionID(sessionID), address, domconv.SessionAudiencePublic, "", 1)
	if err != nil {
		t.Fatalf("NewSessionBinding: %v", err)
	}
	ctx := domconv.WithSessionBinding(context.Background(), binding)
	active := newEngineTestThread(sessionID, "general")
	active.Turns = []domconv.Message{
		{Speaker: domconv.SpeakerUser, Msg: "先ほどの相談", Timestamp: time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)},
		{Speaker: domconv.SpeakerMio, Msg: "返答", Timestamp: time.Date(2026, 10, 25, 1, 1, 0, 0, time.UTC)},
	}
	manager := &preparedChatTurnManager{mockManager: &mockManager{}, activeThread: active}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{Name: "mio"}).WithUserMemoryStore(manager, "configured-owner").WithDetector(NewThreadBoundaryDetector())
	classifier := &mioThreadBoundaryClassifierTestStub{classification: domainagent.MioTurnClassification{
		Decision:   routing.NewDecision(routing.RouteCHAT, 0.95, "combined Mio chat"),
		Boundary:   proposal,
		Outcome:    domainagent.MioTurnOutcome{Kind: domainagent.MioTurnOutcomeAnswer, Text: "了解だよ。"},
		Generation: llm.GenerateResponse{Content: "combined outcome"},
	}}
	provider := &mioThreadBoundaryProviderTestStub{}
	mio := domainagent.NewMioAgent(provider, classifier, mioThreadBoundaryRuleDictionaryTestStub{}, nil, nil, engine)
	decision, draft, err := mio.DecideActionWithChatDraft(ctx, input, true)
	if err != nil || decision.Route != routing.RouteCHAT || draft == nil || draft.Failed() {
		t.Fatalf("DecideActionWithChatDraft route=%q draft=%v err=%v", decision.Route, draft != nil, err)
	}
	return manager, input, ctx, mio, draft, classifier, provider
}

func TestPreparedMioBoundaryProposalUsesOwnerPriorityAndFixedReasons(t *testing.T) {
	for _, tc := range []struct {
		name         string
		message      string
		proposal     domconv.ThreadBoundaryProposal
		wantRule     domconv.ThreadBoundaryProposal
		wantBoundary bool
		wantReason   string
	}{
		{name: "semantic new follows unknown rule", message: "今日の交通情報を教えて", proposal: domconv.ThreadBoundaryNew, wantRule: domconv.ThreadBoundaryUnknown, wantBoundary: true, wantReason: string(domconv.BoundaryMioTopicChange)},
		{name: "semantic keep follows unknown rule", message: "今日の交通情報を教えて", proposal: domconv.ThreadBoundaryKeep, wantRule: domconv.ThreadBoundaryUnknown},
		{name: "unknown semantic proposal keeps after unknown rule", message: "今日の交通情報を教えて", proposal: domconv.ThreadBoundaryUnknown, wantRule: domconv.ThreadBoundaryUnknown},
		{name: "explicit continuation outranks Mio new", message: "さっきの続きを教えて", proposal: domconv.ThreadBoundaryNew, wantRule: domconv.ThreadBoundaryKeep},
		{name: "explicit new outranks Mio keep", message: "別件です。旅行の持ち物を教えて", proposal: domconv.ThreadBoundaryKeep, wantRule: domconv.ThreadBoundaryNew, wantBoundary: true, wantReason: string(domconv.BoundaryExplicitTopicChange)},
		{name: "explicit new is not masked by added fee wording", message: "別件です。追加料金はいくらですか", proposal: domconv.ThreadBoundaryKeep, wantRule: domconv.ThreadBoundaryNew, wantBoundary: true, wantReason: string(domconv.BoundaryExplicitTopicChange)},
		{name: "conflicting explicit cues keep", message: "別件です。前の回答の続きをお願いします", proposal: domconv.ThreadBoundaryNew, wantRule: domconv.ThreadBoundaryKeep},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := NewThreadBoundaryDetector().Detect(newEngineTestThread("detector-session", "general"), tc.message)
			if rule.Proposal != tc.wantRule {
				t.Fatalf("explicit rule proposal=%q, want %q (reason=%q)", rule.Proposal, tc.wantRule, rule.Reason)
			}
			manager, input, ctx, mio, draft, classifier, provider := newPreparedMioBoundaryHarness(t, tc.message, tc.proposal)
			if _, err := mio.ChatWithDraft(ctx, input, draft); err != nil {
				t.Fatalf("ChatWithDraft: %v", err)
			}
			if classifier.calls != 1 {
				t.Fatalf("Mio provider calls=%d, want one combined generation", classifier.calls)
			}
			if provider.calls != 0 {
				t.Fatalf("secondary LLM calls=%d, want zero", provider.calls)
			}
			if len(manager.commitRequests) != 1 {
				t.Fatalf("committed turns=%d, want one", len(manager.commitRequests))
			}
			got := manager.commitRequests[0]
			if got.Boundary != tc.wantBoundary || got.BoundaryReason != tc.wantReason {
				t.Fatalf("boundary=(%t,%q), want (%t,%q)", got.Boundary, got.BoundaryReason, tc.wantBoundary, tc.wantReason)
			}
		})
	}
}

func TestConversationEndTurnIgnoresCallerBoundaryAndUsesOnlyExplicitRuleOutsideMio(t *testing.T) {
	for _, tc := range []struct {
		name         string
		message      string
		callerFlag   bool
		callerReason string
		wantBoundary bool
		wantReason   string
	}{
		{name: "caller proposal ignored", message: "ところで、少し聞きたい", callerFlag: true, callerReason: "caller supplied"},
		{name: "quoted cue with no Mio proposal keeps", message: "『別件です』という表現の意味を教えて"},
		{name: "non Mio explicit rule", message: "別件です。旅行の持ち物を教えて", wantBoundary: true, wantReason: string(domconv.BoundaryExplicitTopicChange)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := string(modulecore.NewSessionID())
			manager := &mockManager{}
			engine := NewRealConversationEngine(manager, domconv.PersonaState{}).
				WithUserMemoryStore(manager, "owner").WithDetector(NewThreadBoundaryDetector())
			request := engineTestConversationTurnRequest(sessionID, tc.message, "response", domconv.SpeakerKuro)
			request.Boundary, request.BoundaryReason = tc.callerFlag, tc.callerReason
			if _, err := engine.CommitConversationTurn(context.Background(), request); err != nil {
				t.Fatalf("CommitConversationTurn: %v", err)
			}
			if len(manager.commitRequests) != 1 {
				t.Fatalf("commits=%d, want one", len(manager.commitRequests))
			}
			got := manager.commitRequests[0]
			if got.Boundary != tc.wantBoundary || got.BoundaryReason != tc.wantReason {
				t.Fatalf("boundary=(%t,%q), want (%t,%q)", got.Boundary, got.BoundaryReason, tc.wantBoundary, tc.wantReason)
			}
		})
	}
}

func TestPreparedMioBoundaryRejectsStaleHeadBeforeCommit(t *testing.T) {
	manager, input, ctx, mio, draft, classifier, _ := newPreparedMioBoundaryHarness(t, "今日の交通情報を教えて", domconv.ThreadBoundaryNew)
	if classifier.calls != 1 {
		t.Fatalf("initial Mio calls=%d, want one", classifier.calls)
	}
	manager.validatePreparationFunc = func(context.Context, *domconv.ConversationTurnPreparation) error {
		return domconv.ErrConversationTurnConflict
	}
	if _, err := mio.ChatWithDraft(ctx, input, draft); err == nil {
		t.Fatal("ChatWithDraft succeeded after the prepared head became stale")
	}
	if len(manager.commitRequests) != 0 {
		t.Fatalf("stale prepared head committed %d turns", len(manager.commitRequests))
	}
}
