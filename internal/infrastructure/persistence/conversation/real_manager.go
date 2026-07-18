package conversation

import (
	"context"
	"fmt"
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	redisstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/redis"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// RealConversationManager は実ストアを統合した会話管理実装
type RealConversationManager struct {
	redisStore                  redisStoreIface
	l1Store                     l1StoreIface
	archiveStore                archiveStoreIface
	vectordbStore               vectordbStoreIface
	embedder                    domconv.EmbeddingProvider      // nilの場合はVectorDB機能無効
	summarizer                  domconv.ConversationSummarizer // nilの場合は簡易実装
	agentStatuses               map[string]*domconv.AgentStatus
	knowledgeRelationImportHook func(context.Context, l1sqlite.L1KnowledgeItem) error
}

func (r *RealConversationManager) WithKnowledgeRelationImportHook(hook func(context.Context, l1sqlite.L1KnowledgeItem) error) *RealConversationManager {
	if r != nil {
		r.knowledgeRelationImportHook = hook
	}
	return r
}

// NewRealConversationManager は新しいRealConversationManagerを生成
func NewRealConversationManager(redisURL, archiveSQLitePath, vectordbURL string) (*RealConversationManager, error) {
	return NewRealConversationManagerWithVectorOptions(redisURL, archiveSQLitePath, vectordbURL, "rencrow_memory", 768)
}

func NewRealConversationManagerWithVectorOptions(redisURL, archiveSQLitePath, vectordbURL string, vectorCollection string, vectorDimension uint64) (*RealConversationManager, error) {
	redisStore, err := redisstore.NewRedisStore(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create redis store: %w", err)
	}

	archiveStore, err := openArchiveSQLiteStore(archiveSQLitePath)
	if err != nil {
		log.Printf("WARN: L2 SQLite archive disabled: %v", err)
	}

	if vectorCollection == "" {
		vectorCollection = "rencrow_memory"
	}
	vectordbStore, err := vectordb.NewVectorDBStoreWithDimension(vectordbURL, vectorCollection, vectorDimension)
	if err != nil {
		redisStore.Close()
		if archiveStore != nil {
			archiveStore.Close()
		}
		return nil, fmt.Errorf("failed to create vectordb store: %w", err)
	}

	return &RealConversationManager{
		redisStore:    redisStore,
		archiveStore:  archiveStore,
		vectordbStore: vectordbStore,
		agentStatuses: map[string]*domconv.AgentStatus{},
	}, nil
}

func openArchiveSQLiteStore(path string) (archiveStoreIface, error) {
	store, err := archivesqlite.NewArchiveSQLiteStore(path)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// WithEmbedder はEmbeddingProviderを注入する（チェーン可能）
func (r *RealConversationManager) WithEmbedder(e domconv.EmbeddingProvider) *RealConversationManager {
	r.embedder = e
	return r
}

// WithSummarizer はConversationSummarizerを注入する（チェーン可能）
func (r *RealConversationManager) WithSummarizer(s domconv.ConversationSummarizer) *RealConversationManager {
	r.summarizer = s
	return r
}

func (r *RealConversationManager) WithL1Store(store l1StoreIface) *RealConversationManager {
	if l1, ok := store.(*l1sqlite.L1SQLiteStore); ok {
		if archiveStore, ok := r.archiveStore.(*archivesqlite.ArchiveSQLiteStore); ok && archiveStore != nil {
			l1.WithArchiveStore(archiveStore)
		}
		l1.WithKnowledgeVectorSink(r)
		l1.WithVectorCleanupSink(r)
	}
	r.l1Store = store
	return r
}

// Close はすべてのストアを閉じる
func (r *RealConversationManager) Close() error {
	var errs []error
	if err := r.redisStore.Close(); err != nil {
		errs = append(errs, fmt.Errorf("redis close: %w", err))
	}
	if r.archiveStore != nil {
		if err := r.archiveStore.Close(); err != nil {
			errs = append(errs, fmt.Errorf("archive sqlite close: %w", err))
		}
	}
	if err := r.vectordbStore.Close(); err != nil {
		errs = append(errs, fmt.Errorf("vectordb close: %w", err))
	}
	if r.l1Store != nil {
		if err := r.l1Store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("l1 sqlite close: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing stores: %v", errs)
	}
	return nil
}

// --- 内部ヘルパー ---

// --- KB管理API (kb-admin用) ---
