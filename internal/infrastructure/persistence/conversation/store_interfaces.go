package conversation

import (
	"context"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/duckdb"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	redisstore "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/redis"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/vectordb"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

// redisStoreIface はRedisStoreのインターフェース（テスト用モック差し替え可能）
type redisStoreIface interface {
	SaveSession(ctx context.Context, sess *conversation.SessionConversation) error
	GetSession(ctx context.Context, sessionID string) (*conversation.SessionConversation, error)
	DeleteSession(ctx context.Context, sessionID string) error
	ListActiveSessions(ctx context.Context) ([]string, error)
	SaveThread(ctx context.Context, thread *conversation.Thread) error
	GetThread(ctx context.Context, threadID int64) (*conversation.Thread, error)
	DeleteThread(ctx context.Context, threadID int64) error
	Close() error
}

// duckdbStoreIface はDuckDBStoreのインターフェース
type duckdbStoreIface interface {
	SaveThreadSummary(ctx context.Context, summary *conversation.ThreadSummary) error
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
	SaveMessage(ctx context.Context, sessionID string, threadID int64, namespace string, msg conversation.Message, memoryState string) error
	SaveSearchCache(ctx context.Context, provider string, rawQuery string, resultsJSON string, sourceURLs []string, ttl time.Duration) (*l1sqlite.L1SearchCacheEntry, error)
	GetFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error)
	GetSimilarFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time, threshold float64) (*l1sqlite.L1SearchCacheEntry, error)
	InvalidateSearchCache(ctx context.Context, provider string, rawQuery string) (int64, error)
	SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
	SearchWikiPageIndex(ctx context.Context, query string, limit int) ([]l1sqlite.WikiPageIndexItem, error)
	AppendEvent(ctx context.Context, eventType string, namespace string, sessionID string, threadID int64, payload map[string]interface{}, source string) (*l1sqlite.L1EventLogEntry, error)
	RecentEvents(ctx context.Context, namespace string, limit int) ([]l1sqlite.L1EventLogEntry, error)
	UpdateMemoryState(ctx context.Context, id string, memoryState string) error
	PromoteMemoryToNamespace(ctx context.Context, id string, targetNamespace string, promotedBy string) (*l1sqlite.L1MemoryEvent, error)
	RecentByNamespace(ctx context.Context, namespace string, limit int) ([]l1sqlite.L1MemoryEvent, error)
	RecentByState(ctx context.Context, memoryState string, limit int) ([]l1sqlite.L1MemoryEvent, error)
	RecentBySession(ctx context.Context, sessionID string, limit int) ([]l1sqlite.L1MemoryEvent, error)
	SaveRecallTrace(ctx context.Context, trace conversation.RecallTrace) error
	RecentRecallTraces(ctx context.Context, sessionID string, limit int) ([]conversation.RecallTrace, error)
	Close() error
}

// noveltyThreshold は「新規情報」と判定する類似度の閾値
const noveltyThreshold = float32(0.85)

// _ はコンパイル時のインターフェース適合チェック
var _ redisStoreIface = (*redisstore.RedisStore)(nil)
var _ duckdbStoreIface = (*duckdb.DuckDBStore)(nil)
var _ vectordbStoreIface = (*vectordb.VectorDBStore)(nil)
var _ l1StoreIface = (*l1sqlite.L1SQLiteStore)(nil)
