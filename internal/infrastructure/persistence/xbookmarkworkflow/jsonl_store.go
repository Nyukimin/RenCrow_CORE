package xbookmarkworkflow

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	domainworkflow "github.com/Nyukimin/RenCrow_CORE/internal/domain/xbookmarkworkflow"
)

type JSONLStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONLStore(path string) *JSONLStore {
	return &JSONLStore{path: filepath.Clean(path)}
}

func (s *JSONLStore) Get(ctx context.Context, id string) (domainworkflow.Result, bool, error) {
	values, err := s.readLatest(ctx)
	if err != nil {
		return domainworkflow.Result{}, false, err
	}
	value, ok := values[strings.TrimSpace(id)]
	return value, ok, nil
}

func (s *JSONLStore) Save(ctx context.Context, result domainworkflow.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(result.ID) == "" || strings.TrimSpace(result.SourceRecordID) == "" || strings.TrimSpace(result.Workflow) == "" {
		return fmt.Errorf("x bookmark workflow result identity is required")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode x bookmark workflow result: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create x bookmark workflow result directory: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open x bookmark workflow result store: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("append x bookmark workflow result: %w", err)
	}
	return nil
}

func (s *JSONLStore) List(ctx context.Context, query domainworkflow.ResultQuery) ([]domainworkflow.Result, error) {
	values, err := s.readLatest(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domainworkflow.Result, 0, len(values))
	for _, value := range values {
		if query.SourceRecordID != "" && value.SourceRecordID != query.SourceRecordID {
			continue
		}
		if query.Workflow != "" && value.Workflow != query.Workflow {
			continue
		}
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].UpdatedAt != result[j].UpdatedAt {
			return result[i].UpdatedAt > result[j].UpdatedAt
		}
		return result[i].ID < result[j].ID
	})
	if query.Limit > 0 && len(result) > query.Limit {
		result = result[:query.Limit]
	}
	return result, nil
}

func (s *JSONLStore) readLatest(ctx context.Context) (map[string]domainworkflow.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]domainworkflow.Result{}
	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, fmt.Errorf("open x bookmark workflow result store: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var value domainworkflow.Result
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			return nil, fmt.Errorf("decode x bookmark workflow result line %d: %w", lineNumber, err)
		}
		if strings.TrimSpace(value.ID) == "" {
			return nil, fmt.Errorf("x bookmark workflow result line %d has no id", lineNumber)
		}
		result[value.ID] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read x bookmark workflow result store: %w", err)
	}
	return result, nil
}
