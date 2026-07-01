package idlechat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const maxStoreCache = 5000
const maxTopicStoreLineBytes = 32 * 1024 * 1024

// TopicStore is a lightweight persistent store for idleChat topic summaries.
// It appends one JSON record per line and keeps an in-memory cache for fast reads.
type TopicStore struct {
	path      string
	mu        sync.RWMutex
	summaries []SessionSummary
}

func NewTopicStore(path string) (*TopicStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir topic store dir: %w", err)
	}
	s := &TopicStore{
		path:      path,
		summaries: make([]SessionSummary, 0, 256),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
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
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec SessionSummary
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		s.summaries = append(s.summaries, rec)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan topic store: %w", err)
	}
	if len(s.summaries) > maxStoreCache {
		s.summaries = s.summaries[len(s.summaries)-maxStoreCache:]
	}
	return nil
}

func (s *TopicStore) Append(summary SessionSummary) error {
	raw, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal topic summary: %w", err)
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open topic store append: %w", err)
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("append topic store: %w", err)
	}
	_ = f.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.summaries = append(s.summaries, summary)
	if len(s.summaries) > maxStoreCache {
		s.summaries = s.summaries[len(s.summaries)-maxStoreCache:]
	}
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
