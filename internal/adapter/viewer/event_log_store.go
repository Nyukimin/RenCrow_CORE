package viewer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
)

type EventLogReader interface {
	Query(ctx context.Context, filter LogFilter) ([]orchestrator.OrchestratorEvent, error)
}

type EventLogStore struct {
	path string
	mu   sync.Mutex
}

func NewEventLogStore(path string) (*EventLogStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("touch file: %w", err)
	}
	_ = f.Close()
	return &EventLogStore{path: path}, nil
}

func (s *EventLogStore) Path() string {
	return s.path
}

func (s *EventLogStore) Append(ev orchestrator.OrchestratorEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open for append: %w", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(ev); err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	return nil
}

func (s *EventLogStore) Query(_ context.Context, filter LogFilter) ([]orchestrator.OrchestratorEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if shouldUseTailEventLogQuery(filter) {
		return s.queryTail(filter)
	}

	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	items := make([]orchestrator.OrchestratorEvent, 0)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev orchestrator.OrchestratorEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if !matchesLogFilter(ev, filter) {
			continue
		}
		items = append(items, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func shouldUseTailEventLogQuery(filter LogFilter) bool {
	return filter.Limit > 0 &&
		strings.TrimSpace(filter.Type) == "" &&
		strings.TrimSpace(filter.Agent) == "" &&
		strings.TrimSpace(filter.Route) == "" &&
		strings.TrimSpace(filter.JobID) == "" &&
		strings.TrimSpace(filter.SessionID) == "" &&
		strings.TrimSpace(filter.ChatID) == ""
}

func (s *EventLogStore) queryTail(filter LogFilter) ([]orchestrator.OrchestratorEvent, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	size := st.Size()
	if size == 0 {
		return []orchestrator.OrchestratorEvent{}, nil
	}

	const initialWindow int64 = 1 << 20
	const maxWindow int64 = 16 << 20
	window := initialWindow
	for {
		if window > size {
			window = size
		}
		items, complete, err := readTailEventWindow(f, size, window, filter.Limit)
		if err != nil {
			return nil, err
		}
		if len(items) >= filter.Limit || complete || window >= maxWindow || window >= size {
			return items, nil
		}
		window *= 2
	}
}

func readTailEventWindow(f *os.File, size int64, window int64, limit int) ([]orchestrator.OrchestratorEvent, bool, error) {
	offset := size - window
	buf := make([]byte, window)
	n, err := f.ReadAt(buf, offset)
	if err != nil && n == 0 {
		return nil, false, fmt.Errorf("read tail: %w", err)
	}
	buf = buf[:n]
	lines := bytes.Split(buf, []byte{'\n'})
	complete := offset == 0
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	items := make([]orchestrator.OrchestratorEvent, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(items) < limit; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var ev orchestrator.OrchestratorEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		items = append(items, ev)
	}
	return items, complete, nil
}

func matchesLogFilter(ev orchestrator.OrchestratorEvent, filter LogFilter) bool {
	agent := strings.ToLower(strings.TrimSpace(filter.Agent))
	switch {
	case filter.Type != "" && !strings.EqualFold(ev.Type, filter.Type):
		return false
	case agent != "" && !strings.EqualFold(ev.From, agent) && !strings.EqualFold(ev.To, agent):
		return false
	case filter.Route != "" && !strings.EqualFold(ev.Route, filter.Route):
		return false
	case filter.JobID != "" && !strings.EqualFold(ev.JobID, filter.JobID):
		return false
	case filter.SessionID != "" && !strings.EqualFold(ev.SessionID, filter.SessionID):
		return false
	case filter.ChatID != "" && !strings.EqualFold(ev.ChatID, filter.ChatID):
		return false
	default:
		return true
	}
}
