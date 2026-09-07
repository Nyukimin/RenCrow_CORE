package scheduler

import (
	"context"
	"encoding/json"
	"path/filepath"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlutil"
)

type JSONLStore struct {
	schedulePath string
	runPath      string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/scheduler"
	}
	return &JSONLStore{
		schedulePath: filepath.Join(root, "scheduler_schedule.jsonl"),
		runPath:      filepath.Join(root, "scheduler_run.jsonl"),
	}
}

func (s *JSONLStore) SaveSchedule(_ context.Context, schedule domainscheduler.Schedule) error {
	if err := domainscheduler.ValidateSchedule(schedule); err != nil {
		return err
	}
	return jsonlutil.Append(s.schedulePath, schedule)
}

func (s *JSONLStore) ListSchedules(_ context.Context, limit int) ([]domainscheduler.Schedule, error) {
	return listLatestByKey(s.schedulePath, limit, func(schedule domainscheduler.Schedule) string { return string(schedule.ScheduleID) })
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
