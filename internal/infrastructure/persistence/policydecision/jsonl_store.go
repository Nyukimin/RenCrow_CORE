package policydecision

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
)

type JSONLStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONLStore(path string) (*JSONLStore, error) {
	if path == "" {
		return nil, fmt.Errorf("policy decision path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create policy decision directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create policy decision store: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close policy decision store: %w", err)
	}
	return &JSONLStore{path: path}, nil
}

func (s *JSONLStore) Save(_ context.Context, record domainpolicy.Record) error {
	if err := domainpolicy.Validate(record); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open policy decision store: %w", err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(record); err != nil {
		return fmt.Errorf("append policy decision: %w", err)
	}
	return nil
}

func (s *JSONLStore) List(_ context.Context, limit int) ([]domainpolicy.Record, error) {
	if limit <= 0 || limit > 100 {
		return nil, fmt.Errorf("limit must be between 1 and 100")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open policy decision store: %w", err)
	}
	defer file.Close()
	items := make([]domainpolicy.Record, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record domainpolicy.Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode policy decision record: %w", err)
		}
		if err := domainpolicy.Validate(record); err != nil {
			return nil, fmt.Errorf("validate policy decision record: %w", err)
		}
		items = append(items, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan policy decision store: %w", err)
	}
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return items, nil
}
