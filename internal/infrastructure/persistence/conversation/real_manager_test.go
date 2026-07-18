package conversation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"
)

// --- モック実装 ---

type mockRedisStore struct {
	sessions map[string]*domconv.SessionConversation
	threads  map[int64]*domconv.Thread
}

func newMockRedisStore() *mockRedisStore {
	return &mockRedisStore{
		sessions: make(map[string]*domconv.SessionConversation),
		threads:  make(map[int64]*domconv.Thread),
	}
}

func (m *mockRedisStore) SaveSession(_ context.Context, sess *domconv.SessionConversation) error {
	m.sessions[sess.ID] = sess
	return nil
}
func (m *mockRedisStore) GetSession(_ context.Context, sessionID string) (*domconv.SessionConversation, error) {
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, domconv.ErrSessionNotFound
	}
	return s, nil
}
func (m *mockRedisStore) DeleteSession(_ context.Context, sessionID string) error {
	delete(m.sessions, sessionID)
	return nil
}
func (m *mockRedisStore) ListActiveSessions(_ context.Context) ([]string, error) {
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids, nil
}
func (m *mockRedisStore) SaveThread(_ context.Context, thread *domconv.Thread) error {
	m.threads[thread.ID] = thread
	return nil
}
func (m *mockRedisStore) GetThread(_ context.Context, threadID int64) (*domconv.Thread, error) {
	t, ok := m.threads[threadID]
	if !ok {
		return nil, domconv.ErrThreadNotFound
	}
	return t, nil
}
func (m *mockRedisStore) DeleteThread(_ context.Context, threadID int64) error {
	delete(m.threads, threadID)
	return nil
}
func (m *mockRedisStore) Close() error { return nil }

type mockArchiveSQLiteStore struct {
	saved     []*domconv.ThreadSummary
	kbArchive []l1sqlite.L1KnowledgeItem
}

func (m *mockArchiveSQLiteStore) SaveThreadSummary(_ context.Context, s *domconv.ThreadSummary) error {
	m.saved = append(m.saved, s)
	return nil
}
func (m *mockArchiveSQLiteStore) GetSessionHistory(_ context.Context, _ string, _ int) ([]*domconv.ThreadSummary, error) {
	return m.saved, nil
}
func (m *mockArchiveSQLiteStore) SearchByDomain(_ context.Context, _ string, _ int) ([]*domconv.ThreadSummary, error) {
	return nil, nil
}
func (m *mockArchiveSQLiteStore) SearchKnowledgeArchiveFTS(_ context.Context, _ string, _ string, _ int) ([]l1sqlite.L1KnowledgeItem, error) {
	return m.kbArchive, nil
}
func (m *mockArchiveSQLiteStore) ExportThreadSummariesParquet(_ context.Context, _ string) error {
	return nil
}
func (m *mockArchiveSQLiteStore) ExportL1ArchivesParquet(_ context.Context, _ string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (m *mockArchiveSQLiteStore) CleanupOldRecords(_ context.Context) (int64, error) { return 0, nil }
func (m *mockArchiveSQLiteStore) Close() error                                       { return nil }

type mockVectorDBStore struct {
	saved     []*domconv.ThreadSummary
	kbSaved   []*domconv.Document
	mockScore float32
}

func (m *mockVectorDBStore) SaveThreadSummary(_ context.Context, s *domconv.ThreadSummary) error {
	m.saved = append(m.saved, s)
	return nil
}
func (m *mockVectorDBStore) SearchSimilar(_ context.Context, _ []float32, _ int) ([]*domconv.ThreadSummary, error) {
	if len(m.saved) == 0 {
		return nil, nil
	}
	result := make([]*domconv.ThreadSummary, 0, len(m.saved))
	for _, s := range m.saved {
		cp := *s
		cp.Score = m.mockScore
		result = append(result, &cp)
	}
	return result, nil
}
func (m *mockVectorDBStore) SearchByDomain(_ context.Context, _ string, _ int) ([]*domconv.ThreadSummary, error) {
	return nil, nil
}
func (m *mockVectorDBStore) IsNovelQuery(_ context.Context, _ []float32, threshold float32) (bool, float32, error) {
	return m.mockScore < threshold, m.mockScore, nil
}
func (m *mockVectorDBStore) SaveKB(_ context.Context, doc *domconv.Document) error {
	m.kbSaved = append(m.kbSaved, doc)
	return nil
}
func (m *mockVectorDBStore) SearchKB(_ context.Context, _ string, _ []float32, _ int) ([]*domconv.Document, error) {
	return []*domconv.Document{}, nil
}
func (m *mockVectorDBStore) ListKBDocuments(_ context.Context, _ string, _ int) ([]*domconv.Document, error) {
	return []*domconv.Document{}, nil
}
func (m *mockVectorDBStore) GetKBCollections(_ context.Context) ([]string, error) {
	return []string{}, nil
}
func (m *mockVectorDBStore) GetKBStats(_ context.Context, _ string) (*vectordb.KBStats, error) {
	return &vectordb.KBStats{Domain: "test", DocumentCount: 0, VectorSize: 768}, nil
}
func (m *mockVectorDBStore) DeleteOldKBDocuments(_ context.Context, _ string, _ time.Time) (int, error) {
	return 0, nil
}
func (m *mockVectorDBStore) CleanupMemoryVectors(_ context.Context, items []l1sqlite.L1VectorCleanupItem) (*l1sqlite.L1VectorCleanupResult, error) {
	return &l1sqlite.L1VectorCleanupResult{Deleted: len(items)}, nil
}
func (m *mockVectorDBStore) Close() error { return nil }

type mockL1Store struct {
	saved     []l1sqlite.L1MemoryEvent
	cache     *l1sqlite.L1SearchCacheEntry
	knowledge []l1sqlite.L1KnowledgeItem
	wiki      []l1sqlite.WikiPageIndexItem
	events    []l1sqlite.L1EventLogEntry
	traces    []domconv.RecallTrace
}

func (m *mockL1Store) SaveMessage(_ context.Context, sessionID string, threadID int64, namespace string, msg domconv.Message, memoryState string) error {
	m.saved = append(m.saved, l1sqlite.L1MemoryEvent{
		Namespace:   namespace,
		SessionID:   sessionID,
		ThreadID:    threadID,
		Speaker:     msg.Speaker,
		Message:     msg.Msg,
		Meta:        msg.Meta,
		MemoryState: memoryState,
		Layer:       l1sqlite.MemoryLayerL1,
	})
	return nil
}
func (m *mockL1Store) SaveSearchCache(_ context.Context, provider string, rawQuery string, resultsJSON string, sourceURLs []string, ttl time.Duration) (*l1sqlite.L1SearchCacheEntry, error) {
	m.cache = &l1sqlite.L1SearchCacheEntry{
		Provider:    provider,
		RawQuery:    rawQuery,
		ResultsJSON: resultsJSON,
		SourceURLs:  sourceURLs,
		RetrievedAt: time.Now(),
		ExpiresAt:   time.Now().Add(ttl),
	}
	return m.cache, nil
}
func (m *mockL1Store) GetFreshSearchCache(_ context.Context, provider string, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error) {
	if m.cache == nil || m.cache.Provider != provider || m.cache.RawQuery != rawQuery || !m.cache.ExpiresAt.After(now) {
		return nil, nil
	}
	return m.cache, nil
}
func (m *mockL1Store) GetSimilarFreshSearchCache(_ context.Context, provider string, _ string, now time.Time, _ float64) (*l1sqlite.L1SearchCacheEntry, error) {
	if m.cache == nil || m.cache.Provider != provider || !m.cache.ExpiresAt.After(now) {
		return nil, nil
	}
	return m.cache, nil
}
func (m *mockL1Store) InvalidateSearchCache(_ context.Context, provider string, rawQuery string) (int64, error) {
	if m.cache == nil || m.cache.Provider != provider || m.cache.RawQuery != rawQuery {
		return 0, nil
	}
	m.cache = nil
	return 1, nil
}
func (m *mockL1Store) SearchKnowledgeItemsFTS(_ context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error) {
	var out []l1sqlite.L1KnowledgeItem
	query = strings.ToLower(strings.TrimSpace(query))
	for _, item := range m.knowledge {
		if item.Domain != domain {
			continue
		}
		haystack := strings.ToLower(item.Title + " " + item.RawText + " " + item.SummaryDraft + " " + strings.Join(item.Keywords, " "))
		if query == "" || strings.Contains(haystack, query) || anyQueryTermMatches(haystack, query) {
			out = append(out, item)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
func (m *mockL1Store) SearchWikiPageIndex(_ context.Context, query string, limit int) ([]l1sqlite.WikiPageIndexItem, error) {
	var out []l1sqlite.WikiPageIndexItem
	query = strings.ToLower(strings.TrimSpace(query))
	for _, item := range m.wiki {
		if item.Status == l1sqlite.WikiPageStatusArchived || item.Status == l1sqlite.WikiPageStatusDeprecated {
			continue
		}
		haystack := strings.ToLower(item.Title + " " + item.Path + " " + item.Summary + " " + strings.Join(item.SourcePaths, " ") + " " + strings.Join(item.Related, " "))
		if query == "" || strings.Contains(haystack, query) || anyQueryTermMatches(haystack, query) {
			out = append(out, item)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func anyQueryTermMatches(haystack string, query string) bool {
	for _, term := range strings.Fields(query) {
		if len([]rune(term)) >= 3 && strings.Contains(haystack, term) {
			return true
		}
	}
	return false
}
func (m *mockL1Store) AppendEvent(_ context.Context, eventType string, namespace string, sessionID string, threadID int64, payload map[string]interface{}, source string) (*l1sqlite.L1EventLogEntry, error) {
	entry := l1sqlite.L1EventLogEntry{
		ID:        fmt.Sprintf("%s:%s:%d", namespace, eventType, len(m.events)+1),
		EventType: eventType,
		Namespace: namespace,
		SessionID: sessionID,
		ThreadID:  threadID,
		Payload:   payload,
		Source:    source,
		CreatedAt: time.Now(),
	}
	m.events = append(m.events, entry)
	return &entry, nil
}
func (m *mockL1Store) RecentEvents(_ context.Context, namespace string, _ int) ([]l1sqlite.L1EventLogEntry, error) {
	var out []l1sqlite.L1EventLogEntry
	for i := len(m.events) - 1; i >= 0; i-- {
		if m.events[i].Namespace == namespace {
			out = append(out, m.events[i])
		}
	}
	return out, nil
}
func (m *mockL1Store) UpdateMemoryState(_ context.Context, id string, memoryState string) error {
	for i := range m.saved {
		if m.saved[i].ID == id {
			m.saved[i].MemoryState = memoryState
			return nil
		}
	}
	return nil
}
func (m *mockL1Store) PromoteMemoryToNamespace(_ context.Context, id string, targetNamespace string, promotedBy string) (*l1sqlite.L1MemoryEvent, error) {
	for _, ev := range m.saved {
		if ev.ID == id {
			promoted := ev
			promoted.ID = fmt.Sprintf("%s:%s", targetNamespace, id)
			promoted.Namespace = targetNamespace
			promoted.MemoryState = l1sqlite.MemoryStateConfirmed
			if promoted.Meta == nil {
				promoted.Meta = map[string]interface{}{}
			}
			promoted.Meta["promoted_by"] = promotedBy
			m.saved = append(m.saved, promoted)
			return &promoted, nil
		}
	}
	return nil, nil
}
func (m *mockL1Store) RecentByNamespace(_ context.Context, namespace string, _ int) ([]l1sqlite.L1MemoryEvent, error) {
	var out []l1sqlite.L1MemoryEvent
	for _, ev := range m.saved {
		if ev.Namespace == namespace {
			out = append(out, ev)
		}
	}
	return out, nil
}
func (m *mockL1Store) RecentByState(_ context.Context, memoryState string, _ int) ([]l1sqlite.L1MemoryEvent, error) {
	var out []l1sqlite.L1MemoryEvent
	for _, ev := range m.saved {
		if ev.MemoryState == memoryState {
			out = append(out, ev)
		}
	}
	return out, nil
}
func (m *mockL1Store) RecentBySession(_ context.Context, sessionID string, _ int) ([]l1sqlite.L1MemoryEvent, error) {
	var out []l1sqlite.L1MemoryEvent
	for _, ev := range m.saved {
		if ev.SessionID == sessionID {
			out = append(out, ev)
		}
	}
	return out, nil
}
func (m *mockL1Store) SaveRecallTrace(_ context.Context, trace domconv.RecallTrace) error {
	m.traces = append(m.traces, trace)
	return nil
}
func (m *mockL1Store) RecentRecallTraces(_ context.Context, sessionID string, _ int) ([]domconv.RecallTrace, error) {
	var out []domconv.RecallTrace
	for _, trace := range m.traces {
		if sessionID == "" || trace.SessionID == sessionID {
			out = append(out, trace)
		}
	}
	return out, nil
}
func (m *mockL1Store) Close() error { return nil }

type mockEmbeddingProvider struct {
	vec []float32
	err error
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return m.vec, m.err
}

type mockSummarizer struct {
	summary  string
	keywords []string
	err      error
}

func (m *mockSummarizer) Summarize(_ context.Context, _ *domconv.Thread) (string, error) {
	return m.summary, m.err
}
func (m *mockSummarizer) ExtractKeywords(_ context.Context, _ *domconv.Thread) ([]string, error) {
	return m.keywords, m.err
}

// dummy for time import
var _ = time.Duration(0)

// --- ヘルパー ---

func newTestManager(embedder domconv.EmbeddingProvider, summarizer domconv.ConversationSummarizer) *RealConversationManager {
	return &RealConversationManager{
		redisStore:    newMockRedisStore(),
		archiveStore:  &mockArchiveSQLiteStore{},
		vectordbStore: &mockVectorDBStore{mockScore: 0.5},
		embedder:      embedder,
		summarizer:    summarizer,
	}
}

// --- テスト ---

func TestFlushThread_WithLLMSummary(t *testing.T) {
	embedder := &mockEmbeddingProvider{vec: []float32{0.1, 0.2, 0.3}}
	summarizer := &mockSummarizer{
		summary:  "Go言語の基本について話し合った",
		keywords: []string{"Go", "プログラミング", "言語"},
	}
	mgr := newTestManager(embedder, summarizer)
	ctx := context.Background()

	thread, err := mgr.CreateThread(ctx, "sess-1", "programming")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "Go言語について教えて", nil))
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerMio, "Go言語はGoogleが開発したシステム言語です", nil))
	mgr.redisStore.(*mockRedisStore).threads[thread.ID] = thread

	summary, err := mgr.FlushThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("FlushThread failed: %v", err)
	}

	if summary.Summary != "Go言語の基本について話し合った" {
		t.Errorf("Expected LLM summary, got: %s", summary.Summary)
	}
	if len(summary.Keywords) != 3 {
		t.Errorf("Expected 3 keywords, got %d", len(summary.Keywords))
	}
	if len(summary.Embedding) == 0 {
		t.Error("Expected embedding to be generated")
	}
}

func TestStore_MirrorsMessageToL1SQLiteStore(t *testing.T) {
	mgr := newTestManager(nil, nil)
	l1 := &mockL1Store{}
	mgr.WithL1Store(l1)
	ctx := context.Background()

	msg := domconv.NewMessage(domconv.SpeakerUser, "L1にも保存する", map[string]interface{}{"kind": "test"})
	if err := mgr.Store(ctx, "sess-l1", msg); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if len(l1.saved) != 1 {
		t.Fatalf("expected 1 l1 event, got %d", len(l1.saved))
	}
	ev := l1.saved[0]
	if ev.SessionID != "sess-l1" {
		t.Fatalf("unexpected session: %s", ev.SessionID)
	}
	if !strings.HasPrefix(ev.Namespace, "conv:") {
		t.Fatalf("unexpected namespace: %s", ev.Namespace)
	}
	if ev.MemoryState != l1sqlite.MemoryStateObserved {
		t.Fatalf("unexpected state: %s", ev.MemoryState)
	}
	if ev.Layer != l1sqlite.MemoryLayerL1 {
		t.Fatalf("unexpected layer: %s", ev.Layer)
	}
}

func TestRecall_UsesL1WhenRedisThreadMissing(t *testing.T) {
	mgr := newTestManager(nil, nil)
	l1 := &mockL1Store{}
	mgr.WithL1Store(l1)
	ctx := context.Background()

	l1.saved = append(l1.saved,
		l1sqlite.L1MemoryEvent{
			Namespace:   "conv:100",
			SessionID:   "sess-l1-recall",
			ThreadID:    100,
			Speaker:     domconv.SpeakerUser,
			Message:     "前回の話題",
			Meta:        map[string]interface{}{"kind": "original"},
			MemoryState: l1sqlite.MemoryStateObserved,
			Layer:       l1sqlite.MemoryLayerL1,
			Source:      "conversation",
			CreatedAt:   time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		},
		l1sqlite.L1MemoryEvent{
			Namespace:   "conv:100",
			SessionID:   "sess-l1-recall",
			ThreadID:    100,
			Speaker:     domconv.SpeakerMio,
			Message:     "前回の返答",
			Meta:        map[string]interface{}{},
			MemoryState: l1sqlite.MemoryStateObserved,
			Layer:       l1sqlite.MemoryLayerL1,
			Source:      "conversation",
			CreatedAt:   time.Date(2026, 5, 5, 10, 1, 0, 0, time.UTC),
		},
	)

	messages, err := mgr.Recall(ctx, "sess-l1-recall", "続き", 3)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Msg != "前回の話題" || messages[1].Msg != "前回の返答" {
		t.Fatalf("unexpected recall order: %+v", messages)
	}
	if messages[0].Meta["namespace"] != "conv:100" {
		t.Fatalf("expected namespace meta, got %+v", messages[0].Meta)
	}
	if messages[0].Meta["memory_state"] != l1sqlite.MemoryStateObserved {
		t.Fatalf("expected memory_state meta, got %+v", messages[0].Meta)
	}
	if messages[0].Meta["kind"] != "original" {
		t.Fatalf("expected original meta to be preserved, got %+v", messages[0].Meta)
	}
}

func TestRecall_SkipsSQLiteArchiveWhenArchiveDisabled(t *testing.T) {
	embedder := &mockEmbeddingProvider{vec: []float32{0.1, 0.2, 0.3}}
	vdb := &mockVectorDBStore{mockScore: 0.42}
	vdb.saved = []*domconv.ThreadSummary{{
		ThreadID: 301,
		Summary:  "vector fallback summary",
	}}
	mgr := &RealConversationManager{
		redisStore:    newMockRedisStore(),
		archiveStore:  nil,
		vectordbStore: vdb,
		embedder:      embedder,
	}
	ctx := context.Background()

	messages, err := mgr.Recall(ctx, "sess-archive_sqlite-disabled", "fallback", 3)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected vector fallback message, got %d", len(messages))
	}
	if !strings.Contains(messages[0].Msg, "[LongTermMemory] vector fallback summary") {
		t.Fatalf("unexpected recall message: %+v", messages[0])
	}
	if messages[0].Meta["score"] != float32(0.42) {
		t.Fatalf("unexpected score meta: %+v", messages[0].Meta)
	}
}

func TestOpenArchiveSQLiteStoreReturnsNilInterfaceForLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.duckdb")
	if err := os.WriteFile(path, []byte("legacy non-sqlite database"), 0600); err != nil {
		t.Fatalf("write legacy database: %v", err)
	}
	store, err := openArchiveSQLiteStore(path)
	if err == nil {
		t.Fatal("expected non-SQLite archive to be rejected")
	}
	if store != nil {
		t.Fatalf("failed archive initialization must return a nil interface: %#v", store)
	}
}

func TestFlushThread_SkipsSQLiteArchiveWhenArchiveDisabled(t *testing.T) {
	embedder := &mockEmbeddingProvider{vec: []float32{0.1, 0.2, 0.3}}
	vdb := &mockVectorDBStore{mockScore: 0.5}
	mgr := &RealConversationManager{
		redisStore:    newMockRedisStore(),
		archiveStore:  nil,
		vectordbStore: vdb,
		embedder:      embedder,
		summarizer:    &mockSummarizer{summary: "summary without archive_sqlite", keywords: []string{"memory"}},
	}
	ctx := context.Background()

	thread, err := mgr.CreateThread(ctx, "sess-flush-no-archive_sqlite", "memory")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "SQLite archiveなしでflush", nil))
	mgr.redisStore.(*mockRedisStore).threads[thread.ID] = thread

	summary, err := mgr.FlushThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("FlushThread failed: %v", err)
	}
	if summary.Summary != "summary without archive_sqlite" {
		t.Fatalf("unexpected summary: %s", summary.Summary)
	}
	if len(vdb.saved) != 1 {
		t.Fatalf("expected vector save to continue, got %d", len(vdb.saved))
	}
	if _, err := mgr.redisStore.(*mockRedisStore).GetThread(ctx, thread.ID); err != domconv.ErrThreadNotFound {
		t.Fatalf("expected flushed thread to be deleted, got err=%v", err)
	}
}

func TestRealConversationManager_WebSearchCacheRoundTrip(t *testing.T) {
	mgr := newTestManager(nil, nil)
	l1 := &mockL1Store{}
	mgr.WithL1Store(l1)
	ctx := context.Background()

	results := []WebSearchResult{
		{Title: "RenCrow memo", Link: "https://example.com/rencrow", Snippet: "cacheable result"},
	}
	if err := mgr.SaveWebSearchCache(ctx, "RenCrow 最新仕様", results, time.Hour); err != nil {
		t.Fatalf("SaveWebSearchCache failed: %v", err)
	}

	cached, hit, err := mgr.GetFreshWebSearchCache(ctx, "RenCrow 最新仕様")
	if err != nil {
		t.Fatalf("GetFreshWebSearchCache failed: %v", err)
	}
	if !hit {
		t.Fatal("expected web search cache hit")
	}
	if len(cached) != 1 || cached[0].Title != "RenCrow memo" || cached[0].Link != "https://example.com/rencrow" {
		t.Fatalf("unexpected cached results: %+v", cached)
	}
	if len(l1.cache.SourceURLs) != 1 || l1.cache.SourceURLs[0] != "https://example.com/rencrow" {
		t.Fatalf("unexpected cached source urls: %+v", l1.cache.SourceURLs)
	}
}

func TestSaveL1KnowledgeItemSavesVectorKBDocument(t *testing.T) {
	ctx := context.Background()
	vdb := &mockVectorDBStore{}
	mgr := &RealConversationManager{
		redisStore:    newMockRedisStore(),
		archiveStore:  &mockArchiveSQLiteStore{},
		vectordbStore: vdb,
		embedder:      &mockEmbeddingProvider{vec: []float32{0.1, 0.2, 0.3}},
	}

	err := mgr.SaveL1KnowledgeItem(ctx, l1sqlite.L1KnowledgeItem{
		ID:           "kb:movie:001",
		Domain:       "movie",
		Title:        "Example Movie",
		SourceID:     "api:movie",
		SourceURL:    "https://example.com/movie/1",
		SummaryDraft: "映画の要約",
		RawText:      "映画の本文",
		RawHash:      "hash-001",
		Keywords:     []string{"SF"},
		LicenseNote:  "official api",
		CreatedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("SaveL1KnowledgeItem failed: %v", err)
	}
	if len(vdb.kbSaved) != 1 {
		t.Fatalf("expected one vector KB document, got %d", len(vdb.kbSaved))
	}
	doc := vdb.kbSaved[0]
	if doc.ID != "kb:movie:001" || doc.Domain != "movie" || doc.Content != "映画の要約" || doc.Source != "https://example.com/movie/1" {
		t.Fatalf("unexpected vector KB document: %+v", doc)
	}
	if doc.Meta["title"] != "Example Movie" || doc.Meta["source_id"] != "api:movie" {
		t.Fatalf("unexpected vector KB meta: %+v", doc.Meta)
	}
}

func TestSaveL1KnowledgeItemRunsRelationHookWithoutVectorAndDegradesGracefully(t *testing.T) {
	called := 0
	mgr := (&RealConversationManager{}).WithKnowledgeRelationImportHook(func(_ context.Context, item l1sqlite.L1KnowledgeItem) error {
		called++
		if item.ID != "kb:test:1" {
			t.Fatalf("item=%#v", item)
		}
		return fmt.Errorf("temporary relation failure")
	})
	if err := mgr.SaveL1KnowledgeItem(context.Background(), l1sqlite.L1KnowledgeItem{ID: "kb:test:1"}); err != nil {
		t.Fatalf("relation failure must not fail knowledge save: %v", err)
	}
	if called != 1 {
		t.Fatalf("hook calls=%d", called)
	}
}

func TestFlushThread_EmbedderError_FallsBackToSimple(t *testing.T) {
	embedder := &mockEmbeddingProvider{err: fmt.Errorf("API error")}
	summarizer := &mockSummarizer{summary: "summary", keywords: []string{"kw"}}
	mgr := newTestManager(embedder, summarizer)
	ctx := context.Background()

	thread, _ := mgr.CreateThread(ctx, "sess-2", "general")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "hello", nil))
	mgr.redisStore.(*mockRedisStore).threads[thread.ID] = thread

	// Embedderエラーでも FlushThread は成功する（embeddingなしで保存）
	summary, err := mgr.FlushThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("FlushThread should not fail on embedder error: %v", err)
	}
	if len(summary.Embedding) != 0 {
		t.Error("Embedding should be empty on error")
	}
}

func TestFlushThread_NoSummarizer_FallsBackToSimple(t *testing.T) {
	mgr := newTestManager(nil, nil)
	ctx := context.Background()

	thread, _ := mgr.CreateThread(ctx, "sess-3", "general")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "こんにちは", nil))
	mgr.redisStore.(*mockRedisStore).threads[thread.ID] = thread

	summary, err := mgr.FlushThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("FlushThread failed: %v", err)
	}
	if summary.Summary == "" {
		t.Error("Summary should not be empty")
	}
	// 簡易実装のフォールバック確認
	if len(summary.Keywords) == 0 {
		t.Error("Keywords should not be empty (domain fallback)")
	}
}

func TestIsNovelInformation_EmptyVectorDB_IsNovel(t *testing.T) {
	embedder := &mockEmbeddingProvider{vec: []float32{0.1, 0.2, 0.3}}
	mgr := newTestManager(embedder, &mockSummarizer{})
	ctx := context.Background()

	msg := domconv.NewMessage(domconv.SpeakerUser, "新しい情報です", nil)

	isNovel, _, err := mgr.IsNovelInformation(ctx, msg)
	if err != nil {
		t.Fatalf("IsNovelInformation failed: %v", err)
	}
	// vectordbが空（score=0.5 < threshold=0.85） → 新規情報
	if !isNovel {
		t.Error("Should be novel when similarity is below threshold")
	}
}

func TestIsNovelInformation_HighSimilarity_NotNovel(t *testing.T) {
	embedding := []float32{0.1, 0.2, 0.3}
	embedder := &mockEmbeddingProvider{vec: embedding}
	// 類似度スコア0.95（閾値0.85を超える → 新規でない）
	vdb := &mockVectorDBStore{mockScore: 0.95}
	vdb.saved = []*domconv.ThreadSummary{{Summary: "既存の記憶"}}
	mgr := &RealConversationManager{
		redisStore:    newMockRedisStore(),
		archiveStore:  &mockArchiveSQLiteStore{},
		vectordbStore: vdb,
		embedder:      embedder,
		summarizer:    &mockSummarizer{},
	}
	ctx := context.Background()

	msg := domconv.NewMessage(domconv.SpeakerUser, "似たような情報", nil)
	isNovel, score, err := mgr.IsNovelInformation(ctx, msg)
	if err != nil {
		t.Fatalf("IsNovelInformation failed: %v", err)
	}
	if isNovel {
		t.Errorf("Should not be novel when similarity=%.2f >= threshold", score)
	}
}

func TestIsNovelInformation_NoEmbedder_ReturnsFalse(t *testing.T) {
	mgr := newTestManager(nil, &mockSummarizer{})
	ctx := context.Background()
	msg := domconv.NewMessage(domconv.SpeakerUser, "何か", nil)

	isNovel, _, err := mgr.IsNovelInformation(ctx, msg)
	if err != nil {
		t.Fatalf("should not error: %v", err)
	}
	if isNovel {
		t.Error("Should return false when embedder is not configured")
	}
}

func TestRealConversationManager_UpdateAgentStatusPersistsKPI(t *testing.T) {
	mgr := newTestManager(nil, nil)
	ctx := context.Background()
	status := domconv.NewAgentStatus("mio")
	status.ApplyKPI("user_thumbs_up", 10)
	if err := mgr.UpdateAgentStatus(ctx, status); err != nil {
		t.Fatalf("UpdateAgentStatus failed: %v", err)
	}
	got, err := mgr.GetAgentStatus(ctx, "mio")
	if err != nil {
		t.Fatalf("GetAgentStatus failed: %v", err)
	}
	if got.KPI["user_thumbs_up"] != 10 || got.Level != 1 {
		t.Fatalf("agent KPI status was not persisted: %+v", got)
	}
}

// --- KB管理APIのテスト ---

func TestListKBDocuments_Success(t *testing.T) {
	mgr := newTestManager(&mockEmbeddingProvider{vec: []float32{0.1, 0.2}}, nil)
	ctx := context.Background()

	docs, err := mgr.ListKBDocuments(ctx, "programming", 10)
	if err != nil {
		t.Fatalf("ListKBDocuments failed: %v", err)
	}

	// mockVectorDBStore は空スライスを返す
	if docs == nil {
		t.Fatal("Expected non-nil docs slice")
	}
	if len(docs) != 0 {
		t.Errorf("Expected 0 docs from mock, got %d", len(docs))
	}
}

func TestGetKBCollections_Success(t *testing.T) {
	mgr := newTestManager(nil, nil)
	ctx := context.Background()

	collections, err := mgr.GetKBCollections(ctx)
	if err != nil {
		t.Fatalf("GetKBCollections failed: %v", err)
	}

	// mockVectorDBStore は空スライスを返す
	if collections == nil {
		t.Fatal("Expected non-nil collections slice")
	}
	if len(collections) != 0 {
		t.Errorf("Expected 0 collections from mock, got %d", len(collections))
	}
}

func TestGetKBStats_Success(t *testing.T) {
	mgr := newTestManager(nil, nil)
	ctx := context.Background()

	stats, err := mgr.GetKBStats(ctx, "programming")
	if err != nil {
		t.Fatalf("GetKBStats failed: %v", err)
	}

	if stats == nil {
		t.Fatal("Expected non-nil stats")
	}
	if stats.Domain != "test" {
		t.Errorf("Expected domain 'test', got '%s'", stats.Domain)
	}
	if stats.DocumentCount != 0 {
		t.Errorf("Expected 0 documents, got %d", stats.DocumentCount)
	}
	if stats.VectorSize != 768 {
		t.Errorf("Expected vector size 768, got %d", stats.VectorSize)
	}
}

func TestDeleteOldKBDocuments_Success(t *testing.T) {
	mgr := newTestManager(nil, nil)
	ctx := context.Background()

	cutoff := time.Now().AddDate(0, 0, -30)
	deletedCount, err := mgr.DeleteOldKBDocuments(ctx, "programming", cutoff)
	if err != nil {
		t.Fatalf("DeleteOldKBDocuments failed: %v", err)
	}

	// mockVectorDBStore は 0 を返す
	if deletedCount != 0 {
		t.Errorf("Expected 0 deleted, got %d", deletedCount)
	}
}

func TestStore_CreatesThreadWhenNotFound(t *testing.T) {
	mgr := newTestManager(nil, nil)
	ctx := context.Background()

	msg := domconv.NewMessage(domconv.SpeakerUser, "Hello", nil)
	err := mgr.Store(ctx, "session123", msg)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// スレッドが作成されたことを確認
	thread, err := mgr.GetActiveThread(ctx, "session123")
	if err != nil {
		t.Fatalf("GetActiveThread failed: %v", err)
	}
	if thread == nil {
		t.Fatal("Expected thread to be created")
	}
	if len(thread.Turns) != 1 {
		t.Errorf("Expected 1 turn, got %d", len(thread.Turns))
	}
	if thread.Turns[0].Msg != "Hello" {
		t.Errorf("Expected message 'Hello', got '%s'", thread.Turns[0].Msg)
	}
}

func TestStore_AppendsToExistingThread(t *testing.T) {
	mgr := newTestManager(nil, nil)
	ctx := context.Background()

	// 最初のメッセージ
	msg1 := domconv.NewMessage(domconv.SpeakerUser, "Hello", nil)
	if err := mgr.Store(ctx, "session123", msg1); err != nil {
		t.Fatalf("First Store failed: %v", err)
	}

	// 2番目のメッセージ
	msg2 := domconv.NewMessage(domconv.SpeakerMio, "Hi there", nil)
	if err := mgr.Store(ctx, "session123", msg2); err != nil {
		t.Fatalf("Second Store failed: %v", err)
	}

	// スレッドが2つのメッセージを持つことを確認
	thread, err := mgr.GetActiveThread(ctx, "session123")
	if err != nil {
		t.Fatalf("GetActiveThread failed: %v", err)
	}
	if len(thread.Turns) != 2 {
		t.Errorf("Expected 2 turns, got %d", len(thread.Turns))
	}
}

func TestWithEmbedder_ReturnsManager(t *testing.T) {
	mgr := newTestManager(nil, nil)
	embedder := &mockEmbeddingProvider{vec: []float32{0.1, 0.2}}

	result := mgr.WithEmbedder(embedder)
	if result == nil {
		t.Fatal("WithEmbedder should return manager")
	}
	// チェーン可能であることを確認
	if result != mgr {
		t.Error("WithEmbedder should return the same manager instance")
	}
}

func TestWithSummarizer_ReturnsManager(t *testing.T) {
	mgr := newTestManager(nil, nil)
	summarizer := &mockSummarizer{summary: "test summary", keywords: []string{"test"}}

	result := mgr.WithSummarizer(summarizer)
	if result == nil {
		t.Fatal("WithSummarizer should return manager")
	}
	// チェーン可能であることを確認
	if result != mgr {
		t.Error("WithSummarizer should return the same manager instance")
	}
}
