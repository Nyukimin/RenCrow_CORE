package dci

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
)

type JSONLStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONLStore(path string) *JSONLStore {
	return &JSONLStore{path: path}
}

func (s *JSONLStore) SaveSearchTrace(_ context.Context, trace domaindci.SearchTrace) error {
	if err := domaindci.ValidateSearchTrace(trace); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(trace)
}

func (s *JSONLStore) ListRecent(limit int) ([]domaindci.SearchTrace, error) {
	if limit <= 0 {
		limit = 50
	}
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []domaindci.SearchTrace{}, nil
		}
		return nil, err
	}
	defer f.Close()
	var traces []domaindci.SearchTrace
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var trace domaindci.SearchTrace
		if err := json.Unmarshal(scanner.Bytes(), &trace); err != nil {
			continue
		}
		traces = append(traces, trace)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(traces)-1; i < j; i, j = i+1, j-1 {
		traces[i], traces[j] = traces[j], traces[i]
	}
	if len(traces) > limit {
		traces = traces[:limit]
	}
	return traces, nil
}
