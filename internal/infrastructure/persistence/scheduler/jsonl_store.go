package scheduler

import (
	"context"
	"encoding/json"
	"path/filepath"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlutil"
)

type JSONLStore struct {
	jobPath string
	runPath string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/scheduler"
	}
	return &JSONLStore{
		jobPath: filepath.Join(root, "scheduler_job.jsonl"),
		runPath: filepath.Join(root, "scheduler_run.jsonl"),
	}
}

func (s *JSONLStore) SaveJob(_ context.Context, job domainscheduler.Job) error {
	if err := domainscheduler.ValidateJob(job); err != nil {
		return err
	}
	return jsonlutil.Append(s.jobPath, job)
}

func (s *JSONLStore) ListJobs(_ context.Context, limit int) ([]domainscheduler.Job, error) {
	return listLatestByKey(s.jobPath, limit, func(job domainscheduler.Job) string { return job.JobID })
}

func (s *JSONLStore) SaveRunLog(_ context.Context, log domainscheduler.RunLog) error {
	if err := domainscheduler.ValidateRunLog(log); err != nil {
		return err
	}
	return jsonlutil.Append(s.runPath, log)
}

func (s *JSONLStore) ListRunLogs(_ context.Context, limit int) ([]domainscheduler.RunLog, error) {
	return jsonlutil.ListLatest[domainscheduler.RunLog](s.runPath, limit)
}

func listLatestByKey[T any](path string, limit int, keyFn func(T) string) ([]T, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []T
	if err := jsonlutil.Read(path, func(line []byte) error {
		var item T
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]T, 0, min(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		key := keyFn(items[i])
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, items[i])
	}
	return out, nil
}
