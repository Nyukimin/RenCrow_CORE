package toolharness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/toolharness"
)

type JSONLRecorder struct {
	path string
	mu   sync.Mutex
}

func NewJSONLRecorder(path string) (*JSONLRecorder, error) {
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("touch file: %w", err)
	}
	_ = f.Close()
	return &JSONLRecorder{path: path}, nil
}

func (r *JSONLRecorder) RecordToolMediationEvent(event domain.Event) error {
	if err := domain.ValidateEvent(event); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open for append: %w", err)
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(event); err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	return nil
}

func (r *JSONLRecorder) ListRecent(limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 50
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	f, err := os.Open(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.Event{}, nil
		}
		return nil, fmt.Errorf("open for read: %w", err)
	}
	defer f.Close()

	events := make([]domain.Event, 0)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event domain.Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan events: %w", err)
	}

	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}
