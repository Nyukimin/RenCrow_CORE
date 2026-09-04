package conversation

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	redisstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/redis"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// redisStoreIface はRedisStoreのインターフェース（テスト用モック差し替え可能）
type redisStoreIface interface {
	SaveSession(ctx context.Context, sess *conversation.SessionConversation) error
	GetSession(ctx context.Context, sessionID string) (*conversation.SessionConversation, error)
	DeleteSession(ctx context.Context, sessionID string) error
	ListActiveSessions(ctx context.Context) ([]string, error)
	SaveThread(ctx context.Context, thread *conversation.Thread) error
	GetThread(ctx context.Context, threadID modulecore.ThreadID) (*conversation.Thread, error)
	DeleteThread(ctx context.Context, threadID modulecore.ThreadID) error
	Close() error
}

// archiveStoreIface はArchiveSQLiteStoreのインターフェース
type archiveStoreIface interface {
	SaveThreadSummary(ctx context.Context, summary *conversation.ThreadSummary) error
	SaveThreadSummaryWithReceipt(ctx context.Context, summary *conversation.ThreadSummary, receipt *conversation.ThreadSummaryReceipt) error
	GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]*conversation.ThreadSummary, error)
	SearchByDomain(ctx context.Context, domain string, limit int) ([]*conversation.ThreadSummary, error)
	SearchKnowledgeArchiveFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
	ExportThreadSummariesParquet(ctx context.Context, outputPath string) error
	ExportL1ArchivesParquet(ctx context.Context, outputDir string) (map[string]string, error)
	CleanupOldRecords(ctx context.Context) (int64, error)
	Close() error
}

// vectordbStoreIface はVectorDBStoreのインターフェース
type vectordbStoreIface interface {
	SaveThreadSummary(ctx context.Context, summary *conversation.ThreadSummary) error
	SearchSimilar(ctx context.Context, queryEmbedding []float32, topK int) ([]*conversation.ThreadSummary, error)
	SearchByDomain(ctx context.Context, domain string, limit int) ([]*conversation.ThreadSummary, error)
	IsNovelQuery(ctx context.Context, queryEmbedding []float32, threshold float32) (bool, float32, error)
	// KB (Knowledge Base) メソッド
	SaveKB(ctx context.Context, doc *conversation.Document) error
	ValidateKBVectorContract(ctx context.Context, domain string) error
	SearchKB(ctx context.Context, domain string, queryEmbedding []float32, topK int) ([]*conversation.Document, error)
	// KB管理メソッド (kb-admin用)
	ListKBDocuments(ctx context.Context, domain string, limit int) ([]*conversation.Document, error)
	GetKBCollections(ctx context.Context) ([]string, error)
	GetKBStats(ctx context.Context, domain string) (*vectordb.KBStats, error)
	DeleteOldKBDocuments(ctx context.Context, domain string, before time.Time) (int, error)
	CleanupMemoryVectors(ctx context.Context, items []l1sqlite.L1VectorCleanupItem) (*l1sqlite.L1VectorCleanupResult, error)
	Close() error
}

type l1StoreIface interface {
	SaveMessage(ctx context.Context, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, namespace string, msg conversation.Message, memoryState string) error
	SaveSearchCache(ctx context.Context, provider string, rawQuery string, resultsJSON string, sourceURLs []string, ttl time.Duration) (*l1sqlite.L1SearchCacheEntry, error)
	GetFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error)
	GetSimilarFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time, threshold float64) (*l1sqlite.L1SearchCacheEntry, error)
	InvalidateSearchCache(ctx context.Context, provider string, rawQuery string) (int64, error)
	SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
	SearchWikiPageIndex(ctx context.Context, query string, limit int) ([]l1sqlite.WikiPageIndexItem, error)
	AppendEvent(ctx context.Context, eventType string, namespace string, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, payload map[string]interface{}, source string) (*l1sqlite.L1EventLogEntry, error)
	RecentEvents(ctx context.Context, namespace string, limit int) ([]l1sqlite.L1EventLogEntry, error)
	UpdateMemoryState(ctx context.Context, id string, memoryState string) error
	PromoteMemoryToNamespace(ctx context.Context, id string, targetNamespace string, promotedBy string) (*l1sqlite.L1MemoryEvent, error)
	RecentByNamespace(ctx context.Context, namespace string, limit int) ([]l1sqlite.L1MemoryEvent, error)
	RecentByState(ctx context.Context, memoryState string, limit int) ([]l1sqlite.L1MemoryEvent, error)
	RecentBySession(ctx context.Context, sessionID string, limit int) ([]l1sqlite.L1MemoryEvent, error)
	LatestConversationThreadReference(ctx context.Context, sessionID string) (modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, bool, error)
	SaveRecallTrace(ctx context.Context, trace conversation.RecallTrace) error
	RecentRecallTraces(ctx context.Context, sessionID string, limit int) ([]conversation.RecallTrace, error)
	Close() error
}

// conversationTurnL1Store is the bounded EndTurn/follower surface. It stays
// separate from the legacy Store interface so old callers and test doubles do
// not silently acquire a different commit contract.
type conversationTurnL1Store interface {
	CommitConversationTurn(context.Context, conversation.ConversationTurnRequest) (conversation.ConversationTurnResult, error)
	GetConversationTurnReceipt(context.Context, string) (conversation.ConversationTurnResult, error)
	ClaimConversationTurnOutbox(context.Context, string, time.Time, time.Duration) (*conversation.ConversationTurnOutbox, error)
	ClaimNextConversationTurnOutbox(context.Context, time.Time, time.Duration) (*conversation.ConversationTurnOutbox, error)
	CompleteConversationTurnOutbox(context.Context, string, string, string, time.Time) (conversation.ConversationTurnResult, error)
	FailConversationTurnOutbox(context.Context, string, string, string, conversation.ConversationTurnErrorCode, time.Time) (conversation.ConversationTurnResult, error)
	LoadConversationThreadProjection(context.Context, string, modulecore.ThreadID) ([]l1sqlite.L1MemoryEvent, error)
	LoadActiveConversationThreadProjection(context.Context, string) ([]l1sqlite.L1MemoryEvent, error)
}

type conversationThreadArchiveStore interface {
	GetThreadSummary(context.Context, modulecore.ThreadID) (*conversation.ThreadSummary, error)
}

// noveltyThreshold は「新規情報」と判定する類似度の閾値
const noveltyThreshold = float32(0.85)

// _ はコンパイル時のインターフェース適合チェック
var _ redisStoreIface = (*redisstore.RedisStore)(nil)
var _ archiveStoreIface = (*archivesqlite.ArchiveSQLiteStore)(nil)
var _ vectordbStoreIface = (*vectordb.VectorDBStore)(nil)
var _ l1StoreIface = (*l1sqlite.L1SQLiteStore)(nil)
var _ conversationTurnL1Store = (*l1sqlite.L1SQLiteStore)(nil)
var _ conversationThreadArchiveStore = (*archivesqlite.ArchiveSQLiteStore)(nil)
