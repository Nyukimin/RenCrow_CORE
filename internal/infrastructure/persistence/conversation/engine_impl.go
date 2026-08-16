package conversation

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/google/uuid"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// RealConversationEngine は ConversationEngine の実装
// 既存の RealConversationManager をラップし、RecallPack 生成を追加
type RealConversationEngine struct {
	manager                  domconv.ConversationManager
	persona                  domconv.PersonaState
	detector                 domconv.ThreadBoundaryDetector // nil の場合はスレッド自動検出無効
	knowledgeRelationEnabled bool
	knowledgeRelationMaxHops int
	userMemoryStore          conversationEngineUserMemoryStore
	userID                   string
	categoryRecallRegistry   domconv.CategoryRecallRegistry
	categoryRecallScope      string
}

type conversationEngineExternalRecall interface {
	GetFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error)
	SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
	SearchWikiPageIndex(ctx context.Context, query string, limit int) ([]l1sqlite.WikiPageIndexItem, error)
	SearchKB(ctx context.Context, domain string, query string, topK int) ([]*domconv.Document, error)
	RelatedKnowledgeItems(ctx context.Context, itemID string, maxHop int, limit int) ([]l1sqlite.L1KnowledgeRelationHit, error)
}

// conversationTurnCommitter is intentionally narrower than the legacy
// ConversationManager interface. Only managers that own the canonical L1
// EndTurn transaction may satisfy it.
type conversationTurnCommitter interface {
	CommitConversationTurn(context.Context, domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error)
}

type conversationTurnTargetProvider interface {
	ConversationTurnTargets() []domconv.ConversationTurnTarget
}

type conversationActiveThreadProvider interface {
	LoadActiveConversationThread(context.Context, string) (*domconv.Thread, error)
}

type conversationEngineUserMemoryStore interface {
	ListUserMemories(ctx context.Context, userID string, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error)
}

// NewRealConversationEngine は新しい ConversationEngine を作成
func NewRealConversationEngine(
	manager domconv.ConversationManager,
	persona domconv.PersonaState,
) *RealConversationEngine {
	return &RealConversationEngine{
		manager: manager,
		persona: persona,
	}
}

// WithDetector はスレッド境界検出器を設定する（オプション）
func (e *RealConversationEngine) WithDetector(d domconv.ThreadBoundaryDetector) *RealConversationEngine {
	e.detector = d
	return e
}

func (e *RealConversationEngine) WithUserMemoryStore(store conversationEngineUserMemoryStore, userID string) *RealConversationEngine {
	e.userMemoryStore = store
	e.userID = strings.TrimSpace(userID)
	return e
}

// WithCategoryRecallRegistry wires deterministic category source selection into
// every normal conversation turn. It does not write recall results to history.
func (e *RealConversationEngine) WithCategoryRecallRegistry(registry domconv.CategoryRecallRegistry) *RealConversationEngine {
	e.categoryRecallRegistry = registry
	return e
}

func (e *RealConversationEngine) WithCategoryRecallScope(scope string) *RealConversationEngine {
	e.categoryRecallScope = strings.TrimSpace(scope)
	return e
}

func (e *RealConversationEngine) WithKnowledgeRelationRecall(maxHops int) *RealConversationEngine {
	if maxHops < 1 {
		maxHops = 1
	}
	if maxHops > 2 {
		maxHops = 2
	}
	e.knowledgeRelationEnabled = true
	e.knowledgeRelationMaxHops = maxHops
	return e
}

// BeginTurn はターン開始時に Recall + RecallPack 構築を実行
func (e *RealConversationEngine) BeginTurn(ctx context.Context, sessionID string, userMessage string) (*domconv.RecallPack, error) {
	pack := &domconv.RecallPack{
		Persona:     e.persona,
		Constraints: domconv.DefaultConstraints(),
	}
	userMemoryErr := e.loadSharedUserMemory(ctx, userMessage, pack)
	if userMemoryErr != nil {
		log.Printf("[ConversationEngine] WARN: UserMemory recall failed: %v", userMemoryErr)
	}

	// Recall（想起）
	recallMessages, err := e.manager.Recall(ctx, sessionID, userMessage, 3)
	if err != nil {
		log.Printf("[ConversationEngine] WARN: Recall failed: %v", err)
		pack.RejectedTraceItems = append(pack.RejectedTraceItems, domconv.RecallTraceItem{
			Layer: "L0", Kind: "conversation_recall_source_failure", Status: domconv.TraceStatusSourceFailure,
			Decision: "rejected", PromptSection: domconv.PromptSectionConversation,
			Reason: "conversation recall source unavailable", PromptIndex: -1,
		})
	} else {
		// Recall 結果を RecallPack に分類
		for _, msg := range recallMessages {
			if speaker, ok := domconv.CanonicalChatAgentSpeaker(msg.Speaker); ok {
				msg.Speaker = speaker
				pack.ShortContext = append(pack.ShortContext, msg)
				continue
			}
			switch {
			case msg.Speaker == domconv.SpeakerUser:
				// 短期記憶（Thread.Turns）: そのまま ShortContext に
				pack.ShortContext = append(pack.ShortContext, msg)

			case msg.Speaker == domconv.SpeakerSystem && strings.HasPrefix(msg.Msg, "[Summary]"):
				// 中期記憶（SQLite archive ThreadSummary）: MidSummaries に変換
				summary := strings.TrimPrefix(msg.Msg, "[Summary] ")
				pack.MidSummaries = append(pack.MidSummaries, domconv.ThreadSummary{
					Summary: summary,
				})

			case msg.Speaker == domconv.SpeakerSystem && strings.HasPrefix(msg.Msg, "[LongTermMemory]"):
				// 長期記憶（VectorDB）: LongFacts に変換
				fact := strings.TrimPrefix(msg.Msg, "[LongTermMemory] ")
				pack.LongFacts = append(pack.LongFacts, fact)

			default:
				// その他のシステムメッセージは LongFacts に
				if msg.Msg != "" {
					pack.LongFacts = append(pack.LongFacts, msg.Msg)
				}
			}
		}
	}

	activeDomain := ""
	if thread, threadErr := e.manager.GetActiveThread(ctx, sessionID); threadErr == nil && thread != nil {
		activeDomain = strings.TrimSpace(thread.Domain)
	}
	categoryL1KnowledgeRecordIDs := map[string]struct{}{}
	categoryL1KnowledgeSummaries := map[string]struct{}{}
	if e.categoryRecallRegistry != nil {
		scope := strings.TrimSpace(e.categoryRecallScope)
		if scope == "" {
			scope = strings.TrimSpace(e.userID)
		}
		if scope == "" {
			scope = "public"
		}
		categoryResult, categoryErr := e.categoryRecallRegistry.Recall(ctx, domconv.CategoryRecallQuery{
			Message: userMessage, ActiveDomain: activeDomain, UserScope: scope, Time: timeNowUTC(), Limit: 3,
		})
		if categoryErr != nil {
			log.Printf("[ConversationEngine] WARN: Category recall failed: %v", categoryErr)
			pack.CategoryFailures = append(pack.CategoryFailures, domconv.CategoryRecallFailure{
				SourceID:   "category_registry",
				Code:       domconv.CategoryRecallFailureSourceUnavailable,
				State:      "unavailable",
				Reason:     categoryErr.Error(),
				Retryable:  true,
				ObservedAt: timeNowUTC(),
			})
		} else {
			for _, record := range categoryResult.Records {
				snippet := domconv.CategorySnippetFromRecord(record)
				pack.CategorySnippets = append(pack.CategorySnippets, snippet)
				if strings.EqualFold(strings.TrimSpace(snippet.SourceID), "knowledge_l1") {
					if recordID := strings.TrimSpace(snippet.RecordID); recordID != "" {
						categoryL1KnowledgeRecordIDs[recordID] = struct{}{}
					} else if summary := strings.TrimSpace(snippet.Summary); summary != "" {
						categoryL1KnowledgeSummaries[summary] = struct{}{}
					}
				}
			}
			pack.CategoryFailures = append(pack.CategoryFailures, categoryResult.Failures...)
		}
	}

	// Knowledge Base / SearchCache は、外部情報要求が明確な発話だけに使う。
	if externalRecall, ok := e.manager.(conversationEngineExternalRecall); ok && shouldUseExternalRecallForUserMessage(userMessage) {
		cacheEntry, err := externalRecall.GetFreshSearchCache(ctx, "web", userMessage, timeNowUTC())
		if err != nil {
			log.Printf("[ConversationEngine] WARN: SearchCache lookup failed: %v", err)
		} else if cacheEntry != nil {
			pack.SearchCacheSnippets = append(pack.SearchCacheSnippets, domconv.SearchCacheSnippet{
				Query:       cacheEntry.RawQuery,
				Provider:    cacheEntry.Provider,
				ResultsJSON: cacheEntry.ResultsJSON,
				SourceURLs:  cacheEntry.SourceURLs,
				RetrievedAt: cacheEntry.RetrievedAt,
				Roles:       []string{"chat", "worker", "coder"},
			})
		}

		// 現在のドメインを取得（CategoryRecallと共有）
		domain := activeDomain
		if domain == "" {
			domain = "general"
		}

		items, err := externalRecall.SearchKnowledgeItemsFTS(ctx, domain, userMessage, 3)
		if err != nil {
			log.Printf("[ConversationEngine] WARN: L1 Knowledge FTS failed: %v", err)
		} else {
			for _, item := range items {
				snippet := strings.TrimSpace(item.SummaryDraft)
				if snippet == "" {
					snippet = strings.TrimSpace(item.RawText)
				}
				if snippet != "" && !isAdoptedCategoryL1Knowledge(item, categoryL1KnowledgeRecordIDs, categoryL1KnowledgeSummaries) {
					pack.KBSnippets = append(pack.KBSnippets, "[L1KB] "+snippet)
				}
			}
		}
		if e.knowledgeRelationEnabled && len(items) > 0 {
			e.expandKnowledgeRelations(ctx, externalRecall, items, pack)
		}

		wikiItems, err := externalRecall.SearchWikiPageIndex(ctx, userMessage, 3)
		if err != nil {
			log.Printf("[ConversationEngine] WARN: WikiPageIndex search failed: %v", err)
		} else {
			for _, item := range wikiItems {
				summary := strings.TrimSpace(item.Summary)
				if summary == "" {
					summary = strings.TrimSpace(item.Title)
				}
				if summary != "" {
					pack.WikiSnippets = append(pack.WikiSnippets, domconv.WikiSnippet{
						PageID:      item.PageID,
						Title:       item.Title,
						Path:        item.Path,
						Summary:     summary,
						SourcePaths: append([]string(nil), item.SourcePaths...),
						Related:     append([]string(nil), item.Related...),
						UpdatedAt:   item.UpdatedAt,
						Roles:       []string{"chat", "worker", "coder"},
					})
				}
			}
		}

		// KB検索を実行
		kbDocs, err := externalRecall.SearchKB(ctx, domain, userMessage, 3)
		if err != nil {
			log.Printf("[ConversationEngine] WARN: SearchKB failed: %v", err)
		} else if len(kbDocs) > 0 {
			for _, doc := range kbDocs {
				pack.KBSnippets = append(pack.KBSnippets, "[VectorKB] "+doc.Content)
			}
		}
	}

	applyL0RollingSummary(pack, 6)
	budgeted := pack.ApplyRecallBudget(pack.Constraints.MaxTotalTokens, pack.Constraints.RecallBudgetRatio)
	return &budgeted, nil
}

func isAdoptedCategoryL1Knowledge(item l1sqlite.L1KnowledgeItem, recordIDs map[string]struct{}, summaries map[string]struct{}) bool {
	// Record IDs are the authoritative deduplication key. Summaries are only a
	// fallback for legacy category records that have no RecordID.
	if _, ok := recordIDs[strings.TrimSpace(item.ID)]; ok && strings.TrimSpace(item.ID) != "" {
		return true
	}
	summary := strings.TrimSpace(item.SummaryDraft)
	if summary == "" {
		summary = strings.TrimSpace(item.RawText)
	}
	if summary == "" {
		return false
	}
	_, ok := summaries[summary]
	return ok
}

func (e *RealConversationEngine) loadSharedUserMemory(ctx context.Context, query string, pack *domconv.RecallPack) error {
	if e.userMemoryStore == nil || pack == nil || e.userID == "" {
		return nil
	}
	items, err := e.userMemoryStore.ListUserMemories(ctx, e.userID, "", true, domainmemory.UserMemoryRecallMaxScan)
	if err != nil {
		pack.UserMemoryRecallDecisions = append(pack.UserMemoryRecallDecisions, domainmemory.UserMemoryRecallDecision{
			Status: domainmemory.UserMemoryRecallStatusSourceFailure,
			Reason: "user memory source unavailable",
		})
		return err
	}
	decisions := domainmemory.RankUserMemoriesForRecallForPersona(query, items, domainmemory.UserMemoryRecallDefaultLimit, e.persona.Name)
	pack.UserMemoryRecallDecisions = decisions
	profile := domconv.NewUserProfile(e.userID)
	for i := range pack.UserMemoryRecallDecisions {
		decision := &pack.UserMemoryRecallDecisions[i]
		if decision.Selected && !isSharedUserMemoryPromptInjectable(decision.Item) {
			decision.Selected = false
			decision.Status = domainmemory.UserMemoryRecallStatusFilteredScope
			decision.Reason = "memory scope is not available to this persona"
			continue
		}
		if !decision.Selected {
			continue
		}
		item := decision.Item
		statement := strings.TrimSpace(item.Statement)
		if statement == "" {
			continue
		}
		if item.State == domainmemory.MemoryStatePinned {
			statement = "[優先] " + statement
		}
		profile.Facts = append(profile.Facts, statement)
		if len(profile.Facts) >= 12 {
			break
		}
	}
	if len(profile.Facts) > 0 {
		pack.UserProfile = profile
	}
	return nil
}

func isSharedUserMemoryPromptInjectable(item domainmemory.UserMemory) bool {
	for _, persona := range []string{"mio", "shiro", "kuro", "midori"} {
		if domainmemory.IsUserMemoryPromptInjectable(item, persona) {
			return true
		}
	}
	return false
}

func (e *RealConversationEngine) expandKnowledgeRelations(ctx context.Context, recall conversationEngineExternalRecall, seeds []l1sqlite.L1KnowledgeItem, pack *domconv.RecallPack) {
	if pack == nil || recall == nil {
		return
	}
	seen := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		seen[seed.ID] = true
	}
	for _, seed := range seeds {
		hits, err := recall.RelatedKnowledgeItems(ctx, seed.ID, e.knowledgeRelationMaxHops, 3)
		if err != nil {
			log.Printf("[ConversationEngine] WARN: Knowledge Relation lookup failed for item=%s: %v", seed.ID, err)
			continue
		}
		for _, hit := range hits {
			if seen[hit.Item.ID] {
				continue
			}
			seen[hit.Item.ID] = true
			summary := strings.TrimSpace(hit.Item.SummaryDraft)
			if summary == "" {
				summary = strings.TrimSpace(hit.Item.RawText)
			}
			if summary == "" {
				continue
			}
			pack.RelationSnippets = append(pack.RelationSnippets, domconv.RelationSnippet{
				ItemID: hit.Item.ID, Title: hit.Item.Title, Summary: summary, SourceType: hit.Item.Domain,
				RelationType: hit.RelationType, Score: hit.Score, Evidence: hit.Evidence, Hop: hit.Hop,
				Roles: []string{"chat", "worker", "coder"},
			})
			if len(pack.RelationSnippets) >= 3 {
				return
			}
		}
	}
}

func shouldUseExternalRecallForUserMessage(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" || looksLikePersonalMemoryQuestion(message) {
		return false
	}
	direct := []string{"検索", "調べて", "調査して"}
	for _, marker := range direct {
		if strings.Contains(message, marker) {
			return true
		}
	}
	timely := []string{
		"最新", "ニュース", "今日", "昨日", "今週", "今月", "今年", "最近", "現在", "速報",
		"2024", "2025", "2026", "2027", "天気", "価格", "相場", "株価", "為替",
	}
	for _, marker := range timely {
		if strings.Contains(message, marker) {
			return true
		}
	}
	topic := []string{"について教えて", "について調べて", "について検索", "とは", "仕様", "API", "Wiki", "RecallPack", "Source Registry", "RenCrow_CMD", "rencrow"}
	for _, marker := range topic {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func looksLikePersonalMemoryQuestion(message string) bool {
	selfMarkers := []string{"俺", "私", "僕", "ぼく", "わたし", "自分"}
	recallMarkers := []string{"知ってる", "覚えてる", "覚えていた", "覚えている", "記憶してる", "記憶している"}
	hasSelf := false
	for _, marker := range selfMarkers {
		if strings.Contains(message, marker) {
			hasSelf = true
			break
		}
	}
	if !hasSelf {
		return false
	}
	for _, marker := range recallMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func applyL0RollingSummary(pack *domconv.RecallPack, keepRecent int) {
	if pack == nil || keepRecent <= 0 || len(pack.ShortContext) <= keepRecent {
		return
	}
	cut := len(pack.ShortContext) - keepRecent
	older := pack.ShortContext[:cut]
	pack.ShortContext = append([]domconv.Message(nil), pack.ShortContext[cut:]...)
	var lines []string
	for _, msg := range older {
		text := strings.TrimSpace(msg.Msg)
		if text == "" {
			continue
		}
		lines = append(lines, string(msg.Speaker)+": "+text)
	}
	if len(lines) == 0 {
		return
	}
	summary := strings.Join(lines, " / ")
	if strings.TrimSpace(pack.RollingSummary) != "" {
		pack.RollingSummary = strings.TrimSpace(pack.RollingSummary) + " / " + summary
		return
	}
	pack.RollingSummary = summary
}

var timeNowUTC = func() time.Time {
	return time.Now().UTC()
}

// CommitConversationTurn is the typed atomic EndTurn route. Caller-supplied
// ownership, targets, domain and boundary decisions are deliberately ignored.
func (e *RealConversationEngine) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	base := failedConversationTurnManagerResult(request, domconv.ConversationTurnErrorUnavailable)
	if e == nil || e.manager == nil || strings.TrimSpace(e.userID) == "" {
		return base, domconv.ErrConversationTurnUnavailable
	}
	committer, ok := e.manager.(conversationTurnCommitter)
	if !ok {
		return base, domconv.ErrConversationTurnUnavailable
	}
	provider, ok := e.manager.(conversationTurnTargetProvider)
	if !ok {
		return base, domconv.ErrConversationTurnUnavailable
	}
	activeProvider, ok := e.manager.(conversationActiveThreadProvider)
	if !ok {
		return base, domconv.ErrConversationTurnUnavailable
	}

	canonicalSpeaker, ok := domconv.CanonicalChatAgentSpeaker(request.AgentSpeaker)
	if !ok || canonicalSpeaker != request.AgentSpeaker {
		base.ErrorCode = domconv.ConversationTurnErrorInvalid
		return base, domconv.ErrConversationTurnInvalid
	}

	activeThread, err := activeProvider.LoadActiveConversationThread(ctx, request.SessionID)
	domain := "general"
	boundary := false
	reason := ""
	if err == nil {
		if activeThread == nil {
			return base, domconv.ErrConversationTurnUnavailable
		}
		domain = strings.TrimSpace(activeThread.Domain)
		if domain == "" {
			return base, domconv.ErrConversationTurnInvalid
		}
		if e.detector != nil {
			decision := e.detector.Detect(activeThread, request.UserMessage, domain)
			boundary = decision.ShouldCreateNew
			if boundary {
				reason = strings.TrimSpace(string(decision.Reason))
				if reason == "" {
					return base, domconv.ErrConversationTurnInvalid
				}
			}
		}
	} else if !errors.Is(err, domconv.ErrThreadNotFound) {
		return base, err
	}

	canonical := request
	canonical.OwnerID = strings.TrimSpace(e.userID)
	canonical.Domain = domain
	canonical.AgentSpeaker = canonicalSpeaker
	canonical.Boundary = boundary
	canonical.BoundaryReason = reason
	canonical.Targets = append([]domconv.ConversationTurnTarget(nil), provider.ConversationTurnTargets()...)
	result, err := committer.CommitConversationTurn(ctx, canonical)
	if err != nil {
		return result, err
	}
	return result, nil
}

// EndTurn and EndTurnAs remain compatibility wrappers for callers that have
// not migrated to the typed route. They never perform legacy Store writes.
func (e *RealConversationEngine) EndTurn(ctx context.Context, sessionID string, userMessage string, response string) error {
	return e.EndTurnAs(ctx, sessionID, userMessage, response, domconv.SpeakerMio)
}

func (e *RealConversationEngine) EndTurnAs(ctx context.Context, sessionID string, userMessage string, response string, speaker domconv.Speaker) error {
	if strings.TrimSpace(string(speaker)) == "" {
		speaker = domconv.SpeakerMio
	} else if canonical, ok := domconv.CanonicalChatAgentSpeaker(speaker); ok {
		speaker = canonical
	}
	_, err := e.CommitConversationTurn(ctx, domconv.ConversationTurnRequest{
		TurnID:       uuid.NewString(),
		SessionID:    sessionID,
		UserMessage:  userMessage,
		AgentMessage: response,
		AgentSpeaker: speaker,
	})
	return err
}

// GetPersona は現在のペルソナ設定を返す
func (e *RealConversationEngine) GetPersona() domconv.PersonaState {
	return e.persona
}

// FlushCurrentThread は現在のスレッドを強制フラッシュする
func (e *RealConversationEngine) FlushCurrentThread(ctx context.Context, sessionID string) error {
	thread, err := e.manager.GetActiveThread(ctx, sessionID)
	if err != nil {
		return err
	}
	if _, err := e.manager.FlushThread(ctx, thread.ID); err != nil {
		return err
	}
	_, err = e.manager.CreateThread(ctx, sessionID, thread.Domain)
	return err
}

// GetStatus は会話セッションの現在状態を返す
func (e *RealConversationEngine) GetStatus(ctx context.Context, sessionID string) (*domconv.ConversationStatus, error) {
	thread, err := e.manager.GetActiveThread(ctx, sessionID)
	if err != nil {
		return &domconv.ConversationStatus{
			SessionID: sessionID,
		}, nil
	}
	return &domconv.ConversationStatus{
		SessionID:    sessionID,
		ThreadID:     thread.ID,
		ThreadDomain: thread.Domain,
		TurnCount:    len(thread.Turns),
		ThreadStart:  thread.StartTime,
		ThreadStatus: thread.Status,
	}, nil
}

// ResetSession はセッションをリセットする
func (e *RealConversationEngine) ResetSession(ctx context.Context, sessionID string) error {
	thread, err := e.manager.GetActiveThread(ctx, sessionID)
	if err == nil && thread != nil {
		if _, err := e.manager.FlushThread(ctx, thread.ID); err != nil {
			log.Printf("[ConversationEngine] WARN: FlushThread during reset failed: %v", err)
		}
	}
	_, err = e.manager.CreateThread(ctx, sessionID, "general")
	return err
}
