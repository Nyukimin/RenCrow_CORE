package idlechat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const maxStoreCache = 5000
const maxTopicStoreLineBytes = 32 * 1024 * 1024
const maxThreadSeq = modulecore.ThreadSeq(1<<63 - 1)

const topicStoreThreadOpenRecordType = "thread_open"

type topicStoreTuple struct {
	sessionID string
	threadSeq modulecore.ThreadSeq
}

type topicStoreLifecycle struct {
	threadID    modulecore.ThreadID
	threadKind  modulecore.ThreadKind
	hasOpen     bool
	openLine    int
	hasSummary  bool
	summaryLine int
}

type topicStoreThreadOpen struct {
	RecordType string                `json:"record_type"`
	SessionID  string                `json:"session_id"`
	ThreadID   modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind modulecore.ThreadKind `json:"thread_kind"`
	OpenedAt   string                `json:"opened_at,omitempty"`
}

// TopicStore is a lightweight persistent store for idleChat topic summaries.
// It appends one JSON record per line and keeps an in-memory cache for fast reads.
// The persisted tuple is canonical state: every record must identify one valid
// idleChat thread before it is loaded or appended.
type TopicStore struct {
	path string
	mu   sync.RWMutex

	summaries  []SessionSummary
	maxSeq     map[string]modulecore.ThreadSeq
	lifecycles map[topicStoreTuple]topicStoreLifecycle
	tupleByID  map[modulecore.ThreadID]topicStoreTuple
}

func NewTopicStore(path string) (*TopicStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir topic store dir: %w", err)
	}
	s := &TopicStore{
		path:       path,
		summaries:  make([]SessionSummary, 0, 256),
		maxSeq:     make(map[string]modulecore.ThreadSeq),
		lifecycles: make(map[topicStoreTuple]topicStoreLifecycle),
		tupleByID:  make(map[modulecore.ThreadID]topicStoreTuple),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *TopicStore) ensureMapsLocked() {
	if s.maxSeq == nil {
		s.maxSeq = make(map[string]modulecore.ThreadSeq)
	}
	if s.lifecycles == nil {
		s.lifecycles = make(map[topicStoreTuple]topicStoreLifecycle)
	}
	if s.tupleByID == nil {
		s.tupleByID = make(map[modulecore.ThreadID]topicStoreTuple)
	}
}

func validateTopicSummary(summary SessionSummary) error {
	if err := validateIdleChatSessionID(summary.SessionID); err != nil {
		return err
	}
	if err := summary.ThreadID.Validate(); err != nil {
		return fmt.Errorf("thread_id is invalid: %w", err)
	}
	if err := summary.ThreadSeq.Validate(); err != nil {
		return fmt.Errorf("thread_seq is invalid: %w", err)
	}
	if summary.ThreadKind != modulecore.ThreadKindIdleChat {
		return fmt.Errorf("thread_kind must be %q, got %q", modulecore.ThreadKindIdleChat, summary.ThreadKind)
	}
	return nil
}

func validateIdleChatSessionID(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return fmt.Errorf("session_id is invalid: %w", err)
	}
	return nil
}

func (s *TopicStore) checkOpenLocked(record topicStoreThreadOpen, lineNo int) error {
	s.ensureMapsLocked()
	tuple := topicStoreTuple{sessionID: record.SessionID, threadSeq: record.ThreadSeq}
	if existing, ok := s.lifecycles[tuple]; ok {
		if existing.threadID != record.ThreadID || existing.threadKind != record.ThreadKind {
			return topicStoreLineError(lineNo, "thread_open tuple/thread_id mismatch")
		}
		if existing.hasOpen {
			return topicStoreLineError(lineNo, "duplicate thread_open (first seen on line %d)", existing.openLine)
		}
		return topicStoreLineError(lineNo, "thread_open conflicts with finalized summary (first seen on line %d)", existing.summaryLine)
	}
	if existingTuple, ok := s.tupleByID[record.ThreadID]; ok {
		return topicStoreLineError(lineNo, "thread_open thread_id already belongs to session_id=%q thread_seq=%d", existingTuple.sessionID, existingTuple.threadSeq)
	}
	return nil
}

func (s *TopicStore) applyOpenLocked(record topicStoreThreadOpen, lineNo int) {
	s.ensureMapsLocked()
	tuple := topicStoreTuple{sessionID: record.SessionID, threadSeq: record.ThreadSeq}
	s.lifecycles[tuple] = topicStoreLifecycle{
		threadID:   record.ThreadID,
		threadKind: record.ThreadKind,
		hasOpen:    true,
		openLine:   lineNo,
		hasSummary: false,
	}
	s.tupleByID[record.ThreadID] = tuple
	if current := s.maxSeq[record.SessionID]; record.ThreadSeq > current {
		s.maxSeq[record.SessionID] = record.ThreadSeq
	}
}

func (s *TopicStore) checkSummaryLocked(summary SessionSummary, lineNo int) error {
	s.ensureMapsLocked()
	tuple := topicStoreTuple{sessionID: summary.SessionID, threadSeq: summary.ThreadSeq}
	if existing, ok := s.lifecycles[tuple]; ok {
		if existing.threadID != summary.ThreadID || existing.threadKind != summary.ThreadKind {
			return topicStoreLineError(lineNo, "summary tuple/thread_id mismatch with thread_open")
		}
		if existing.hasSummary {
			return topicStoreLineError(lineNo, "duplicate thread summary (first seen on line %d)", existing.summaryLine)
		}
		if !existing.hasOpen {
			return topicStoreLineError(lineNo, "duplicate standalone thread summary")
		}
		return nil
	}
	if existingTuple, ok := s.tupleByID[summary.ThreadID]; ok {
		return topicStoreLineError(lineNo, "summary thread_id belongs to session_id=%q thread_seq=%d", existingTuple.sessionID, existingTuple.threadSeq)
	}
	return nil
}

func (s *TopicStore) applySummaryLocked(summary SessionSummary, lineNo int) {
	s.ensureMapsLocked()
	tuple := topicStoreTuple{sessionID: summary.SessionID, threadSeq: summary.ThreadSeq}
	if existing, ok := s.lifecycles[tuple]; ok {
		existing.hasSummary = true
		existing.summaryLine = lineNo
		s.lifecycles[tuple] = existing
	} else {
		s.lifecycles[tuple] = topicStoreLifecycle{
			threadID:    summary.ThreadID,
			threadKind:  summary.ThreadKind,
			hasOpen:     false,
			openLine:    0,
			hasSummary:  true,
			summaryLine: lineNo,
		}
		s.tupleByID[summary.ThreadID] = tuple
	}
	if current := s.maxSeq[summary.SessionID]; summary.ThreadSeq > current {
		s.maxSeq[summary.SessionID] = summary.ThreadSeq
	}
	s.summaries = append(s.summaries, summary)
	if len(s.summaries) > maxStoreCache {
		s.summaries = s.summaries[len(s.summaries)-maxStoreCache:]
	}
}

func topicStoreLineError(lineNo int, format string, args ...any) error {
	if lineNo > 0 {
		return fmt.Errorf("topic store line %d: %s", lineNo, fmt.Sprintf(format, args...))
	}
	return fmt.Errorf("topic store: %s", fmt.Sprintf(format, args...))
}

func (s *TopicStore) load() error {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open topic store: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxTopicStoreLineBytes)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			return fmt.Errorf("topic store line %d: empty record", lineNo)
		}
		var envelope struct {
			RecordType string `json:"record_type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return fmt.Errorf("topic store line %d: malformed JSON: %w", lineNo, err)
		}
		switch envelope.RecordType {
		case "":
			var rec SessionSummary
			if err := json.Unmarshal(line, &rec); err != nil {
				return fmt.Errorf("topic store line %d: malformed summary JSON: %w", lineNo, err)
			}
			if err := validateTopicSummary(rec); err != nil {
				return fmt.Errorf("topic store line %d: invalid canonical idlechat tuple: %w", lineNo, err)
			}
			if err := s.checkSummaryLocked(rec, lineNo); err != nil {
				return err
			}
			s.applySummaryLocked(rec, lineNo)
		case topicStoreThreadOpenRecordType:
			var rec topicStoreThreadOpen
			if err := json.Unmarshal(line, &rec); err != nil {
				return fmt.Errorf("topic store line %d: malformed thread_open JSON: %w", lineNo, err)
			}
			if err := validateTopicThreadOpen(rec); err != nil {
				return fmt.Errorf("topic store line %d: invalid canonical thread_open tuple: %w", lineNo, err)
			}
			if err := s.checkOpenLocked(rec, lineNo); err != nil {
				return err
			}
			s.applyOpenLocked(rec, lineNo)
		default:
			return fmt.Errorf("topic store line %d: unknown record_type %q", lineNo, envelope.RecordType)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("topic store line %d: scan failed: %w", lineNo+1, err)
	}
	return nil
}

func validateTopicThreadOpen(record topicStoreThreadOpen) error {
	if record.RecordType != topicStoreThreadOpenRecordType {
		return fmt.Errorf("record_type must be %q, got %q", topicStoreThreadOpenRecordType, record.RecordType)
	}
	return validateTopicSummary(SessionSummary{
		SessionID:  record.SessionID,
		ThreadID:   record.ThreadID,
		ThreadSeq:  record.ThreadSeq,
		ThreadKind: record.ThreadKind,
	})
}

func appendTopicStoreRecord(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open topic store append: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return fmt.Errorf("append topic store: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync topic store: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close topic store append: %w", err)
	}
	return nil
}

// OpenThread durably reserves and opens the next thread for a public SessionID.
// The returned Thread is not exposed until its thread_open record has been
// synced to the same JSONL store, so a restart cannot reuse the tuple.
func (s *TopicStore) OpenThread(sessionID string) (*domconv.Thread, error) {
	if err := validateIdleChatSessionID(sessionID); err != nil {
		return nil, fmt.Errorf("open thread: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMapsLocked()
	current := s.maxSeq[sessionID]
	if current >= maxThreadSeq {
		return nil, fmt.Errorf("open thread: sequence overflow for session %q", sessionID)
	}
	thread, err := domconv.NewThread(sessionID, "idlechat", domconv.ThreadKindIdleChat, current+1)
	if err != nil {
		return nil, fmt.Errorf("open thread: create canonical thread: %w", err)
	}
	record := topicStoreThreadOpen{
		RecordType: topicStoreThreadOpenRecordType,
		SessionID:  sessionID,
		ThreadID:   thread.ID,
		ThreadSeq:  thread.ThreadSeq,
		ThreadKind: thread.ThreadKind,
		OpenedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := validateTopicThreadOpen(record); err != nil {
		return nil, fmt.Errorf("open thread: invalid canonical tuple: %w", err)
	}
	if err := s.checkOpenLocked(record, 0); err != nil {
		return nil, fmt.Errorf("open thread: %w", err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("open thread: marshal lifecycle: %w", err)
	}
	raw = append(raw, '\n')
	if err := appendTopicStoreRecord(s.path, raw); err != nil {
		return nil, err
	}
	s.applyOpenLocked(record, 0)
	return thread, nil
}

func (s *TopicStore) Append(summary SessionSummary) error {
	if err := validateTopicSummary(summary); err != nil {
		return fmt.Errorf("append topic summary: invalid canonical idlechat tuple: %w", err)
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal topic summary: %w", err)
	}
	raw = append(raw, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMapsLocked()
	if err := s.checkSummaryLocked(summary, 0); err != nil {
		return fmt.Errorf("append topic summary: %w", err)
	}
	if err := appendTopicStoreRecord(s.path, raw); err != nil {
		return err
	}
	s.applySummaryLocked(summary, 0)
	return nil
}

func (s *TopicStore) GetRecent(limit int) []SessionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.summaries) {
		limit = len(s.summaries)
	}
	out := make([]SessionSummary, 0, limit)
	for i := len(s.summaries) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.summaries[i])
	}
	return out
}
