package execution

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

	domain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
)

// JSONLReportStore persists execution reports in JSONL format.
type JSONLReportStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONLReportStore(path string) (*JSONLReportStore, error) {
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
	return &JSONLReportStore{path: path}, nil
}

func (s *JSONLReportStore) Save(_ context.Context, report domain.ExecutionReport) error {
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

func (s *JSONLReportStore) ListRecent(_ context.Context, limit int) ([]domain.ExecutionReport, error) {
	if limit <= 0 {
		limit = 20
	}

	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	items := make([]domain.ExecutionReport, 0)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r domain.ExecutionReport
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		items = append(items, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *JSONLReportStore) GetByJobID(_ context.Context, jobID string) (domain.ExecutionReport, error) {
	if jobID == "" {
		return domain.ExecutionReport{}, errors.New("job_id is required")
	}

	f, err := os.Open(s.path)
	if err != nil {
		return domain.ExecutionReport{}, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var best domain.ExecutionReport
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r domain.ExecutionReport
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if r.JobID != jobID {
			continue
		}
		if !found || r.CreatedAt.After(best.CreatedAt) {
			best = r
			found = true
		}
	}
	if err := sc.Err(); err != nil {
		return domain.ExecutionReport{}, fmt.Errorf("scan file: %w", err)
	}
	if !found {
		return domain.ExecutionReport{}, errors.New("report not found")
	}
	return best, nil
}

// ListRecentUnique returns recent execution reports, deduplicated by JobID.
// For each JobID, only the latest report (by CreatedAt) is returned.
// This prevents showing intermediate failures when a job eventually succeeded after retry.
func (s *JSONLReportStore) ListRecentUnique(_ context.Context, limit int) ([]domain.ExecutionReport, error) {
	if limit <= 0 {
		limit = 20
	}

	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	// Map: JobID -> latest report
	latest := make(map[string]domain.ExecutionReport)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r domain.ExecutionReport
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		existing, found := latest[r.JobID]
		if !found || r.CreatedAt.After(existing.CreatedAt) {
			latest[r.JobID] = r
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}

	// Convert map to slice
	items := make([]domain.ExecutionReport, 0, len(latest))
	for _, r := range latest {
		items = append(items, r)
	}

	// Sort by CreatedAt descending (newest first)
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})

	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *JSONLReportStore) Summary(_ context.Context) (map[string]map[string]int, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	out := map[string]map[string]int{
		"status": {
			"passed": 0,
			"failed": 0,
			"other":  0,
		},
		"error_kind": {
			"apply":  0,
			"verify": 0,
			"repair": 0,
			"none":   0,
			"other":  0,
		},
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r domain.ExecutionReport
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		switch r.Status {
		case "passed":
			out["status"]["passed"]++
		case "failed":
			out["status"]["failed"]++
		default:
			out["status"]["other"]++
		}

		switch r.ErrorKind {
		case "apply":
			out["error_kind"]["apply"]++
		case "verify":
			out["error_kind"]["verify"]++
		case "repair":
			out["error_kind"]["repair"]++
		case "":
			out["error_kind"]["none"]++
		default:
			out["error_kind"]["other"]++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}

	return out, nil
}

// SummaryUnique returns summary counts with deduplication by JobID.
// For jobs with multiple reports (e.g., retry/repair), only the latest report is counted.
// This provides accurate job-level statistics rather than report-level statistics.
func (s *JSONLReportStore) SummaryUnique(_ context.Context) (map[string]map[string]int, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	// Map: JobID -> latest report
	latest := make(map[string]domain.ExecutionReport)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r domain.ExecutionReport
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		existing, found := latest[r.JobID]
		if !found || r.CreatedAt.After(existing.CreatedAt) {
			latest[r.JobID] = r
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}

	out := map[string]map[string]int{
		"status": {
			"passed": 0,
			"failed": 0,
			"other":  0,
		},
		"error_kind": {
			"apply":  0,
			"verify": 0,
			"repair": 0,
			"none":   0,
			"other":  0,
		},
	}

	// Count only the latest report for each job
	for _, r := range latest {
		switch r.Status {
		case "passed":
			out["status"]["passed"]++
		case "failed":
			out["status"]["failed"]++
		default:
			out["status"]["other"]++
		}

		switch r.ErrorKind {
		case "apply":
			out["error_kind"]["apply"]++
		case "verify":
			out["error_kind"]["verify"]++
		case "repair":
			out["error_kind"]["repair"]++
		case "":
			out["error_kind"]["none"]++
		default:
			out["error_kind"]["other"]++
		}
	}

	return out, nil
}
