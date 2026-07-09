package execution

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
)

// JSONLRepository は execution record を JSONL で保持する
type JSONLRepository struct {
	path string
	mu   sync.Mutex
}

func NewJSONLRepository(path string) (*JSONLRepository, error) {
	if path == "" {
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
	return &JSONLRepository{path: path}, nil
}

func (r *JSONLRepository) Create(_ context.Context, record domain.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.append(record)
}

func (r *JSONLRepository) UpdateStatus(ctx context.Context, jobID, actionID string, status domain.Status, errMsg string) (domain.Record, error) {
	rec, err := r.Get(ctx, jobID, actionID)
	if err != nil {
		return domain.Record{}, err
	}
	if !domain.CanTransition(rec.Status, status) {
		return domain.Record{}, fmt.Errorf("invalid status transition: %s -> %s", rec.Status, status)
	}
	rec.Status = status
	if errMsg != "" {
		rec.Error = errMsg
	}
	if status.IsTerminal() {
		now := time.Now().UTC()
		rec.FinishedAt = &now
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.append(rec); err != nil {
		return domain.Record{}, err
	}
	return rec, nil
}

func (r *JSONLRepository) Get(_ context.Context, jobID, actionID string) (domain.Record, error) {
	records, err := r.loadLatestByAction()
	if err != nil {
		return domain.Record{}, err
	}
	key := actionKey(jobID, actionID)
	rec, ok := records[key]
	if !ok {
		return domain.Record{}, errors.New("record not found")
	}
	return rec, nil
}

func (r *JSONLRepository) CountByStatus(_ context.Context) (map[domain.Status]int, error) {
	records, err := r.loadLatestByAction()
	if err != nil {
		return nil, err
	}
	counts := make(map[domain.Status]int)
	for _, rec := range records {
		counts[rec.Status]++
	}
	return counts, nil
}

func (r *JSONLRepository) append(record domain.Record) error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open for append: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(record); err != nil {
		return fmt.Errorf("encode record: %w", err)
	}
	return nil
}

func (r *JSONLRepository) loadLatestByAction() (map[string]domain.Record, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	latest := make(map[string]domain.Record)
	s := bufio.NewScanner(f)
	for s.Scan() {
		var rec domain.Record
		if err := json.Unmarshal(s.Bytes(), &rec); err != nil {
			continue
		}
		latest[actionKey(rec.JobID, rec.ActionID)] = rec
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}
	return latest, nil
}

func actionKey(jobID, actionID string) string {
	return jobID + "::" + actionID
}
