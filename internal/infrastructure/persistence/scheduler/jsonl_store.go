package scheduler

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

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

// ListSchedulesFiltered returns the latest schedules after filtering each
// schedule identity and before applying the visible-row bound. Excluded IDs
// are returned so callers can also hide their associated run logs.
func (s *JSONLStore) ListSchedulesFiltered(
	ctx context.Context,
	limit int,
	keep func(domainscheduler.Schedule) bool,
) ([]domainscheduler.Schedule, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	var items []domainscheduler.Schedule
	if err := jsonlutil.Read(s.schedulePath, func(line []byte) error {
		var schedule domainscheduler.Schedule
		if err := json.Unmarshal(line, &schedule); err != nil {
			return err
		}
		items = append(items, schedule)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	seen := make(map[string]struct{}, len(items))
	visible := make([]domainscheduler.Schedule, 0, min(limit, len(items)))
	excludedIDs := make([]string, 0)
	for index := len(items) - 1; index >= 0; index-- {
		schedule := items[index]
		key := strings.TrimSpace(string(schedule.ScheduleID))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if keep != nil && !keep(schedule) {
			excludedIDs = append(excludedIDs, key)
			continue
		}
		if len(visible) < limit {
			visible = append(visible, schedule)
		}
	}
	return visible, excludedIDs, nil
}

// FindScheduleByID reads the latest exact schedule identity without a page
// limit, allowing owner policy checks to avoid inferring absence from a list.
func (s *JSONLStore) FindScheduleByID(ctx context.Context, scheduleID string) (domainscheduler.Schedule, bool, error) {
	if err := ctx.Err(); err != nil {
		return domainscheduler.Schedule{}, false, err
	}
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return domainscheduler.Schedule{}, false, nil
	}
	var found domainscheduler.Schedule
	matched := false
	err := jsonlutil.Read(s.schedulePath, func(line []byte) error {
		var schedule domainscheduler.Schedule
		if err := json.Unmarshal(line, &schedule); err != nil {
			return err
		}
		if strings.TrimSpace(string(schedule.ScheduleID)) == scheduleID {
			found = schedule
			matched = true
		}
		return nil
	})
	return found, matched, err
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
