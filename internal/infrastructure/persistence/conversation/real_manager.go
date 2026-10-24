package conversation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	redisstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/redis"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// RealConversationManager は実ストアを統合した会話管理実装
type RealConversationManager struct {
	redisStore                  redisStoreIface
	l1Store                     l1StoreIface
	archiveStore                ConversationArchiveStore
	vectordbStore               vectordbStoreIface
	embedder                    domconv.EmbeddingProvider      // nilの場合はVectorDB機能無効
	summarizer                  domconv.ConversationSummarizer // nilの場合は簡易実装
	agentStatuses               map[string]*domconv.AgentStatus
	knowledgeRelationImportHook func(context.Context, l1sqlite.L1KnowledgeItem) error
	backgroundMu                sync.Mutex
	turnProjectionMu            turnProjectionLock
	publicationReconcileMu      sync.Mutex
	publicationCursor           string
	threadMu                    sync.Mutex
	turnOverlayMu               sync.RWMutex
	turnOverlays                map[string]*conversationTurnOverlay
	// publicationEpochs contains only sessions with live prepared-turn or
	// pending-publication tickets. Entries disappear with the last ticket.
	publicationEpochs      map[string]*conversationTurnPublicationEpoch
	backgroundWG           sync.WaitGroup
	backgroundClosed       bool
	backgroundFlushTimeout time.Duration
}

type acceptedConversationTurn struct {
	request    domconv.ConversationTurnRequest
	payloadSHA string
	result     domconv.ConversationTurnResult
}

type conversationTurnOverlay struct {
	ownerID         string
	head            *domconv.Thread
	closed          *domconv.Thread
	closedByTurnID  string
	revision        uint64
	preparingTurnID modulecore.TurnID
	preparation     *domconv.ConversationTurnPreparation
	syncTurnID      modulecore.TurnID
	pending         []acceptedConversationTurn
	headFailure     error
}

// conversationTurnPublicationEpoch is a RAM-only reset fence. Its generation
// changes when a lifecycle operation replaces or flushes the active head; a
// normal EndTurn keeps the generation stable. refs bounds map retention to
// active prepared-turn/publication tickets.
type conversationTurnPublicationEpoch struct {
	generation uint64 // protected by RealConversationManager.turnOverlayMu
	refs       atomic.Int64
}

type conversationTurnPublicationEpochGuard interface {
	Use(func() error) error
	Retain() conversationTurnPublicationEpochGuard
	Release()
}

// turnProjectionLock serializes the required active Redis projection with
// background recovery while allowing request cancellation and worker skips.
type turnProjectionLock struct {
	init sync.Once
	gate chan struct{}
}

func (l *turnProjectionLock) initialize() {
	l.init.Do(func() { l.gate = make(chan struct{}, 1) })
}

func (l *turnProjectionLock) Lock(ctx context.Context) error {
	if ctx == nil {
		return errors.New("projection lock context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.initialize()
	select {
	case l.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-l.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *turnProjectionLock) TryLock() bool {
	l.initialize()
	select {
	case l.gate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *turnProjectionLock) Unlock() {
	<-l.gate
}

func (r *RealConversationManager) WithKnowledgeRelationImportHook(hook func(context.Context, l1sqlite.L1KnowledgeItem) error) *RealConversationManager {
	if r != nil {
		r.knowledgeRelationImportHook = hook
	}
	return r
}

// RedisHealth checks the manager's already-open Redis store. It does not
// create a client or participate in CORE readiness decisions.
func (r *RealConversationManager) RedisHealth(ctx context.Context) error {
	if r == nil || r.redisStore == nil {
		return errors.New("redis store is unavailable")
	}
	pinger, ok := r.redisStore.(interface{ Ping(context.Context) error })
	if !ok {
		return errors.New("redis health check is unavailable")
	}
	return pinger.Ping(ctx)
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
	return newRealConversationManagerWithStores(redisStore, archiveStore, vectordbURL, vectorCollection, vectorDimension)
}

// NewRealConversationManagerWithVectorOptionsAndArchiveStore preserves the
// normal Redis/vector manager while injecting the selected archive owner. It
// never opens a local archive path and the archive client does not own the
// shared storage-host connection.
func NewRealConversationManagerWithVectorOptionsAndArchiveStore(
	redisURL string,
	vectordbURL string,
	vectorCollection string,
	vectorDimension uint64,
	archiveStore ConversationArchiveStore,
) (*RealConversationManager, error) {
	redisStore, err := redisstore.NewRedisStore(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create redis store: %w", err)
	}
	return newRealConversationManagerWithStores(redisStore, archiveStore, vectordbURL, vectorCollection, vectorDimension)
}

func newRealConversationManagerWithStores(
	redisStore redisStoreIface,
	archiveStore ConversationArchiveStore,
	vectordbURL string,
	vectorCollection string,
	vectorDimension uint64,
) (*RealConversationManager, error) {

	if vectorCollection == "" {
		vectorCollection = "rencrow_memory"
	}
	vectordbStore, err := vectordb.NewVectorDBStoreWithDimension(vectordbURL, vectorCollection, vectorDimension)
	if err != nil {
		redisStore.Close()
		if closer, ok := archiveStore.(interface{ Close() error }); ok {
			_ = closer.Close()
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
	r.backgroundMu.Lock()
	r.backgroundClosed = true
	r.backgroundMu.Unlock()
	r.backgroundWG.Wait()

	var errs []error
	if err := r.redisStore.Close(); err != nil {
		errs = append(errs, fmt.Errorf("redis close: %w", err))
	}
	if r.archiveStore != nil {
		if closer, ok := r.archiveStore.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, fmt.Errorf("archive store close: %w", err))
			}
		}
	}
	if err := r.vectordbStore.Close(); err != nil {
		errs = append(errs, fmt.Errorf("vectordb close: %w", err))
	}
	if r.l1Store != nil {
		if closer, ok := r.l1Store.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, fmt.Errorf("l1 store close: %w", err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing stores: %v", errs)
	}
	return nil
}

// --- 内部ヘルパー ---

// --- KB管理API (kb-admin用) ---
