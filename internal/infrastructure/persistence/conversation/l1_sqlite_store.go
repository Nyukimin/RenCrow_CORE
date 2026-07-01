package conversation

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

const (
	MemoryStateObserved  = "observed"
	MemoryStateCandidate = "candidate"
	MemoryStateConfirmed = "confirmed"
	MemoryStatePinned    = "pinned"
	MemoryLayerL1        = "L1"
)

const (
	L1StagingKindExternalFetch   = "external_fetch"
	L1StagingKindMemoryCandidate = "memory_candidate"
	L1StagingKindSearchResult    = "search_result"

	L1StagingStatusPending   = "pending"
	L1StagingStatusValidated = "validated"
	L1StagingStatusRejected  = "rejected"
)

const (
	L1SourceKindRSS            = "rss"
	L1SourceKindAtom           = "atom"
	L1SourceKindOfficialAPI    = "official_api"
	L1SourceKindGitHub         = "github"
	L1SourceKindHuggingFace    = "huggingface"
	L1SourceKindPyPI           = "pypi"
	L1SourceKindMediaWiki      = "mediawiki"
	L1SourceKindSearchFallback = "search_fallback"
	L1SourceKindWebGather      = "web_gather"

	L1SourceFetchStatusOK    = "ok"
	L1SourceFetchStatusError = "error"
)

const (
	L1DailyDigestSlotDay     = "day"
	L1DailyDigestSlotMorning = "morning"
	L1DailyDigestSlotNoon    = "noon"
	L1DailyDigestSlotEvening = "evening"
)

type L1SQLiteStore struct {
	db                    *sql.DB
	archiveStore          L1ArchiveStore
	dailyDigestSummarizer DailyDigestSummarizer
	knowledgeVectorSink   L1KnowledgeVectorSink
	vectorCleanupSink     L1VectorCleanupSink
}

func NewL1SQLiteStore(dbPath string) (*L1SQLiteStore, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open l1 sqlite: %w", err)
	}
	store := &L1SQLiteStore{db: db}
	if err := store.initTables(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *L1SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *L1SQLiteStore) WithArchiveStore(archiveStore L1ArchiveStore) *L1SQLiteStore {
	s.archiveStore = archiveStore
	return s
}

func (s *L1SQLiteStore) WithDailyDigestSummarizer(summarizer DailyDigestSummarizer) *L1SQLiteStore {
	s.dailyDigestSummarizer = summarizer
	return s
}

func (s *L1SQLiteStore) WithKnowledgeVectorSink(sink L1KnowledgeVectorSink) *L1SQLiteStore {
	s.knowledgeVectorSink = sink
	return s
}

func (s *L1SQLiteStore) WithVectorCleanupSink(sink L1VectorCleanupSink) *L1SQLiteStore {
	s.vectorCleanupSink = sink
	return s
}
