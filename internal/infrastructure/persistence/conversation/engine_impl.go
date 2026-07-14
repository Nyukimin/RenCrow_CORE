package conversation

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"log"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// RealConversationEngine は ConversationEngine の実装
// 既存の RealConversationManager をラップし、RecallPack 生成を追加
type RealConversationEngine struct {
	manager                  domconv.ConversationManager
	persona                  domconv.PersonaState
	detector                 domconv.ThreadBoundaryDetector // nil の場合はスレッド自動検出無効
	profileExtractor         domconv.ProfileExtractor       // nil の場合はプロファイル抽出無効
	profiles                 map[string]domconv.UserProfile // インメモリキャッシュ
	recallTraceStore         domconv.RecallTraceStore
	knowledgeRelationEnabled bool
	knowledgeRelationMaxHops int
}

type conversationEngineExternalRecall interface {
	GetFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error)
	SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
	SearchWikiPageIndex(ctx context.Context, query string, limit int) ([]l1sqlite.WikiPageIndexItem, error)
	SearchKB(ctx context.Context, domain string, query string, topK int) ([]*domconv.Document, error)
	RelatedKnowledgeItems(ctx context.Context, itemID string, maxHop int, limit int) ([]l1sqlite.L1KnowledgeRelationHit, error)
}

type conversationEngineRecallTraceRecorder interface {
	SaveRecallTrace(ctx context.Context, trace domconv.RecallTrace) error
}

// NewRealConversationEngine は新しい ConversationEngine を作成
func NewRealConversationEngine(
	manager domconv.ConversationManager,
	persona domconv.PersonaState,
) *RealConversationEngine {
	return &RealConversationEngine{
		manager:  manager,
		persona:  persona,
		profiles: make(map[string]domconv.UserProfile),
	}
}

// WithDetector はスレッド境界検出器を設定する（オプション）
func (e *RealConversationEngine) WithDetector(d domconv.ThreadBoundaryDetector) *RealConversationEngine {
	e.detector = d
	return e
}

// WithProfileExtractor はプロファイル抽出器を設定する（オプション）
func (e *RealConversationEngine) WithProfileExtractor(pe domconv.ProfileExtractor) *RealConversationEngine {
	e.profileExtractor = pe
	return e
}

func (e *RealConversationEngine) WithRecallTraceStore(store domconv.RecallTraceStore) *RealConversationEngine {
	e.recallTraceStore = store
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

	// UserProfile 読み込み
	if profile, ok := e.profiles[sessionID]; ok {
		pack.UserProfile = profile
	}

	// Recall（想起）
	recallMessages, err := e.manager.Recall(ctx, sessionID, userMessage, 3)
	if err != nil {
		log.Printf("[ConversationEngine] WARN: Recall failed: %v", err)
		return pack, nil
	}

	// Recall 結果を RecallPack に分類
	for _, msg := range recallMessages {
		switch {
		case msg.Speaker == domconv.SpeakerUser || msg.Speaker == domconv.SpeakerMio:
			// 短期記憶（Thread.Turns）: そのまま ShortContext に
			pack.ShortContext = append(pack.ShortContext, msg)

		case msg.Speaker == domconv.SpeakerSystem && strings.HasPrefix(msg.Msg, "[Summary]"):
			// 中期記憶（DuckDB ThreadSummary）: MidSummaries に変換
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

		// 現在のドメインを取得
		domain := "general"
		if thread, err := e.manager.GetActiveThread(ctx, sessionID); err == nil && thread != nil {
			domain = thread.Domain
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
				if snippet != "" {
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
	e.saveBeginTurnRecallTrace(ctx, sessionID, userMessage, &budgeted, "completed")
	return &budgeted, nil
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

func (e *RealConversationEngine) saveBeginTurnRecallTrace(ctx context.Context, sessionID string, userMessage string, pack *domconv.RecallPack, status string) {
	if e.recallTraceStore == nil || pack == nil {
		return
	}
	now := timeNowUTC()
	traceID := l1sqlite.RecallTraceID(sessionID, now, userMessage)
	items := l1sqlite.TraceItemRecordsFromPack(traceID, pack.ToTraceItems())
	injectedCount := 0
	totalTokens := 0
	for _, item := range items {
		if item.Injected {
			injectedCount++
			totalTokens += item.TokenCount
		}
	}
	if err := e.recallTraceStore.StartRecallTrace(ctx, domconv.RecallTraceRecord{
		TraceID:             traceID,
		TurnID:              traceID,
		ChatID:              sessionID,
		Persona:             "mio",
		Route:               "chat",
		UserMessageHash:     l1sqlite.HashRecallText(userMessage),
		QueryTextRedacted:   l1sqlite.RedactedRecallQuery(userMessage),
		CreatedAt:           now,
		RecallPolicyVersion: "memory-lifecycle-v1",
		TotalCandidates:     len(items),
		InjectedCount:       injectedCount,
		TotalInjectedTokens: totalTokens,
		Status:              status,
	}); err != nil {
		log.Printf("[ConversationEngine] WARN: StartRecallTrace failed: %v", err)
		return
	}
	if err := e.recallTraceStore.AddRecallTraceItems(ctx, traceID, items); err != nil {
		log.Printf("[ConversationEngine] WARN: AddRecallTraceItems failed: %v", err)
		return
	}
	if err := e.recallTraceStore.AddPromptInjectionEvents(ctx, traceID, l1sqlite.PromptInjectionEventsFromItems(traceID, items, now)); err != nil {
		log.Printf("[ConversationEngine] WARN: AddPromptInjectionEvents failed: %v", err)
		return
	}
	if err := e.recallTraceStore.FinishRecallTrace(ctx, traceID, status, injectedCount, totalTokens); err != nil {
		log.Printf("[ConversationEngine] WARN: FinishRecallTrace failed: %v", err)
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

// EndTurn はターン終了時にメッセージ保存を実行
// スレッド境界検出器が設定されている場合、Store前にトピック変化を検出する
func (e *RealConversationEngine) EndTurn(ctx context.Context, sessionID string, userMessage string, response string) error {
	return e.EndTurnAs(ctx, sessionID, userMessage, response, domconv.SpeakerMio)
}

func (e *RealConversationEngine) EndTurnAs(ctx context.Context, sessionID string, userMessage string, response string, speaker domconv.Speaker) error {
	// スレッド境界検出（detector が設定されている場合）
	if e.detector != nil {
		thread, err := e.manager.GetActiveThread(ctx, sessionID)
		if err == nil && thread != nil {
			result := e.detector.Detect(thread, userMessage, "")
			if result.ShouldCreateNew {
				log.Printf("[ConversationEngine] Thread boundary detected: %s (score=%.2f)", result.Reason, result.Score)
				if _, err := e.manager.FlushThread(ctx, thread.ID); err != nil {
					log.Printf("[ConversationEngine] WARN: FlushThread failed: %v", err)
				}
				if _, err := e.manager.CreateThread(ctx, sessionID, thread.Domain); err != nil {
					log.Printf("[ConversationEngine] WARN: CreateThread failed: %v", err)
				}
			}
		}
	}

	// ユーザーメッセージを記憶
	userMsg := domconv.NewMessage(domconv.SpeakerUser, userMessage, nil)
	if err := e.manager.Store(ctx, sessionID, userMsg); err != nil {
		log.Printf("[ConversationEngine] WARN: Store (user) failed: %v", err)
	}

	// Agent の応答を記憶
	if strings.TrimSpace(string(speaker)) == "" {
		speaker = domconv.SpeakerMio
	}
	agentMsg := domconv.NewMessage(speaker, response, nil)
	if err := e.manager.Store(ctx, sessionID, agentMsg); err != nil {
		log.Printf("[ConversationEngine] WARN: Store (%s) failed: %v", speaker, err)
	}

	// UserProfile 自動抽出（best-effort）
	if e.profileExtractor != nil {
		thread, err := e.manager.GetActiveThread(ctx, sessionID)
		if err == nil && thread != nil {
			existing := e.profiles[sessionID]
			result, err := e.profileExtractor.Extract(ctx, thread, existing)
			if err != nil {
				log.Printf("[ConversationEngine] WARN: ProfileExtract failed: %v", err)
			} else if result != nil && result.HasData() {
				if existing.UserID == "" {
					existing = domconv.NewUserProfile(sessionID)
				}
				existing.Merge(result.NewPreferences, result.NewFacts)
				e.profiles[sessionID] = existing
				log.Printf("[ConversationEngine] UserProfile updated: +%d prefs, +%d facts",
					len(result.NewPreferences), len(result.NewFacts))
			}
		}
	}

	return nil
}

func (e *RealConversationEngine) RecordRecallTrace(ctx context.Context, sessionID string, responseID string, role string, pack domconv.RecallPack) error {
	items := pack.ToTraceItems()
	if len(items) == 0 {
		return nil
	}
	recorder, ok := e.manager.(conversationEngineRecallTraceRecorder)
	if !ok {
		return nil
	}
	return recorder.SaveRecallTrace(ctx, domconv.RecallTrace{
		ResponseID: responseID,
		SessionID:  sessionID,
		Role:       role,
		Items:      items,
		CreatedAt:  timeNowUTC(),
	})
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
