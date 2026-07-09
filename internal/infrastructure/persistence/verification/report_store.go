package verification

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
)

// JSONLReportStore persists verification reports separately from execution evidence.
type JSONLReportStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONLReportStore(path string) (*JSONLReportStore, error) {
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
	return &JSONLReportStore{path: path}, nil
}

func (s *JSONLReportStore) Save(_ context.Context, report domainverification.VerificationReport) error {
	if err := report.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open for append: %w", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(report); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return nil
}

func (s *JSONLReportStore) ListRecent(_ context.Context, limit int) ([]domainverification.VerificationReport, error) {
	if limit <= 0 {
		limit = 20
	}
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *JSONLReportStore) GetByJobID(_ context.Context, jobID string) (domainverification.VerificationReport, error) {
	if jobID == "" {
		return domainverification.VerificationReport{}, errors.New("job_id is required")
	}
	items, err := s.readAll()
	if err != nil {
		return domainverification.VerificationReport{}, err
	}
	var best domainverification.VerificationReport
	found := false
	for _, item := range items {
		if item.JobID != jobID {
			continue
		}
		if !found || item.CreatedAt.After(best.CreatedAt) {
			best = item
			found = true
		}
	}
	if !found {
		return domainverification.VerificationReport{}, errors.New("verification report not found")
	}
	return best, nil
}

func (s *JSONLReportStore) Summary(_ context.Context) (map[string]map[string]int, error) {
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int{
		"status": {
			string(domainverification.StatusVerified):        0,
			string(domainverification.StatusWeaklySupported): 0,
			string(domainverification.StatusUnsupported):     0,
			string(domainverification.StatusConflict):        0,
			string(domainverification.StatusNotChecked):      0,
		},
		"trigger_level": {
			string(domainverification.TriggerLow):    0,
			string(domainverification.TriggerMedium): 0,
			string(domainverification.TriggerHigh):   0,
		},
	}
	for _, item := range items {
		if _, ok := out["status"][string(item.Status)]; ok {
			out["status"][string(item.Status)]++
		}
		if _, ok := out["trigger_level"][string(item.TriggerLevel)]; ok {
			out["trigger_level"][string(item.TriggerLevel)]++
		}
	}
	return out, nil
}

func (s *JSONLReportStore) readAll() ([]domainverification.VerificationReport, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	items := make([]domainverification.VerificationReport, 0)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var report domainverification.VerificationReport
		if err := json.Unmarshal(sc.Bytes(), &report); err != nil {
			continue
		}
		items = append(items, report)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}
	return items, nil
}
