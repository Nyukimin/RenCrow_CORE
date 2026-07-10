package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
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
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
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
		ID:        fmt.Sprintf("%s:%s:%d", namespace, eventType, now.UnixNano()),
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
