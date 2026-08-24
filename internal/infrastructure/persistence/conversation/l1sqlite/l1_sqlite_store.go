package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

var l1IDSequence atomic.Uint64

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
	db                       *sql.DB
	readDB                   *sql.DB
	archiveStore             L1ArchiveStore
	ownerArchiveStore        OwnerArchiveStore
	parquetArchiveStore      OwnerParquetArchiveStore
	rawLifecycleArchiveStore L1RawLifecycleArchiveStore
	parquetExportRoot        string
	rawSourceRoot            string
	rawMu                    sync.Mutex
	dailyDigestSummarizer    DailyDigestSummarizer
	knowledgeVectorSink      L1KnowledgeVectorSink
	vectorCleanupSink        L1VectorCleanupSink
	lifecycleMu              sync.Mutex
}

func NewL1SQLiteStore(dbPath string) (*L1SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		return nil, fmt.Errorf("failed to open l1 sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &L1SQLiteStore{db: db}
	if err := store.initTables(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	readDB, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_pragma=query_only%3d1&_time_format=sqlite")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to open l1 sqlite read connection: %w", err)
	}
	readDB.SetMaxOpenConns(1)
	readDB.SetMaxIdleConns(1)
	if err := readDB.PingContext(context.Background()); err != nil {
		readDB.Close()
		db.Close()
		return nil, fmt.Errorf("failed to initialize l1 sqlite read connection: %w", err)
	}
	store.readDB = readDB
	return store, nil
}

func (s *L1SQLiteStore) Close() error {
	var readErr error
	if s.readDB != nil {
		readErr = s.readDB.Close()
	}
	writeErr := s.db.Close()
	if readErr != nil {
		return readErr
	}
	return writeErr
}

func (s *L1SQLiteStore) WithArchiveStore(archiveStore L1ArchiveStore) *L1SQLiteStore {
	s.archiveStore = archiveStore
	s.ownerArchiveStore = nil
	s.parquetArchiveStore = nil
	s.rawLifecycleArchiveStore = nil
	if typed, ok := archiveStore.(OwnerArchiveStore); ok {
		s.ownerArchiveStore = typed
	}
	if typed, ok := archiveStore.(OwnerParquetArchiveStore); ok {
		s.parquetArchiveStore = typed
	}
	if typed, ok := archiveStore.(L1RawLifecycleArchiveStore); ok {
		s.rawLifecycleArchiveStore = typed
	}
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

type l1SQLExecer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func rollbackL1Tx(tx *sql.Tx, err error) error {
	if tx == nil {
		return err
	}
	if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
		return errors.Join(err, fmt.Errorf("failed to rollback l1 sqlite transaction: %w", rbErr))
	}
	return err
}

func appendL1EventLog(ctx context.Context, execer l1SQLExecer, eventType string, namespace string, sessionID string, threadID int64, payload map[string]interface{}, source string) (*L1EventLogEntry, error) {
	eventType = strings.TrimSpace(eventType)
	namespace = strings.TrimSpace(namespace)
	if eventType == "" {
		return nil, errors.New("l1 event type is required")
	}
	if err := ValidateL1Namespace(namespace); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]interface{}{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 event payload: %w", err)
	}
	now := time.Now().UTC()
	entry := &L1EventLogEntry{
		ID:        fmt.Sprintf("%s:%s:%d:%d", namespace, eventType, now.UnixNano(), l1IDSequence.Add(1)),
		EventType: eventType,
		Namespace: namespace,
		SessionID: sessionID,
		ThreadID:  threadID,
		Payload:   payload,
		Source:    source,
		CreatedAt: now,
	}
	_, err = execer.ExecContext(ctx, `
INSERT INTO l1_event_log (
	id, event_type, namespace, session_id, thread_id, payload_json, source, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, entry.ID, entry.EventType, entry.Namespace, entry.SessionID, entry.ThreadID, string(payloadJSON), entry.Source, entry.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to append l1 event log: %w", err)
	}
	return entry, nil
}
