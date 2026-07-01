package aiworkflow

import (
	"context"
	"encoding/json"
	"path/filepath"

	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/jsonlutil"
)

const (
	contextUsageMaxRecords = 10000
	contextUsageMaxBytes   = int64(8 << 20)
)

type JSONLStore struct {
	eventPath         string
	projectMemoryPath string
	worktreePath      string
	commandPath       string
	contextUsagePath  string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/ai_workflow"
	}
	return &JSONLStore{
		eventPath:         filepath.Join(root, "ai_workflow_event.jsonl"),
		projectMemoryPath: filepath.Join(root, "project_memory_index.jsonl"),
		worktreePath:      filepath.Join(root, "worktree_registry.jsonl"),
		commandPath:       filepath.Join(root, "command_registry.jsonl"),
		contextUsagePath:  filepath.Join(root, "ai_context_usage.jsonl"),
	}
}

func (s *JSONLStore) CompactOperationalLogs() error {
	return jsonlutil.CompactLatestRecords(s.contextUsagePath, contextUsageMaxRecords)
}

func (s *JSONLStore) SaveWorkflowEvent(_ context.Context, item domainai.WorkflowEvent) error {
	if err := domainai.ValidateWorkflowEvent(item); err != nil {
		return err
	}
	return appendJSONL(s.eventPath, item)
}

func (s *JSONLStore) ListWorkflowEvents(_ context.Context, limit int) ([]domainai.WorkflowEvent, error) {
	return jsonlutil.ListLatest[domainai.WorkflowEvent](s.eventPath, limit)
}

func (s *JSONLStore) SaveProjectMemoryIndex(_ context.Context, item domainai.ProjectMemoryIndex) error {
	if err := domainai.ValidateProjectMemoryIndex(item); err != nil {
		return err
	}
	return appendJSONL(s.projectMemoryPath, item)
}

func (s *JSONLStore) ListProjectMemoryIndexes(_ context.Context, limit int) ([]domainai.ProjectMemoryIndex, error) {
	return listLatestJSONLByKey(s.projectMemoryPath, limit, func(item domainai.ProjectMemoryIndex) string {
		return item.ID
	})
}

func (s *JSONLStore) SaveWorktreeRegistry(_ context.Context, item domainai.WorktreeRegistry) error {
	if err := domainai.ValidateWorktreeRegistry(item); err != nil {
		return err
	}
	return appendJSONL(s.worktreePath, item)
}

func (s *JSONLStore) ListWorktreeRegistries(_ context.Context, limit int) ([]domainai.WorktreeRegistry, error) {
	return listLatestJSONLByKey(s.worktreePath, limit, func(item domainai.WorktreeRegistry) string {
		return item.WorktreeID
	})
}

func (s *JSONLStore) SaveCommandRegistry(_ context.Context, item domainai.CommandRegistry) error {
	if err := domainai.ValidateCommandRegistry(item); err != nil {
		return err
	}
	return appendJSONL(s.commandPath, item)
}

func (s *JSONLStore) ListCommandRegistries(_ context.Context, limit int) ([]domainai.CommandRegistry, error) {
	return listLatestJSONLByKey(s.commandPath, limit, func(item domainai.CommandRegistry) string {
		return item.CommandName
	})
}

func (s *JSONLStore) SaveContextUsage(_ context.Context, item domainai.ContextUsage) error {
	if err := domainai.ValidateContextUsage(item); err != nil {
		return err
	}
	return appendJSONLBounded(s.contextUsagePath, item, contextUsageMaxRecords, contextUsageMaxBytes)
}

func (s *JSONLStore) ListContextUsages(_ context.Context, limit int) ([]domainai.ContextUsage, error) {
	return jsonlutil.ListLatest[domainai.ContextUsage](s.contextUsagePath, limit)
}

func appendJSONL(path string, value any) error {
	return jsonlutil.Append(path, value)
}

func appendJSONLBounded(path string, value any, maxRecords int, maxBytes int64) error {
	return jsonlutil.AppendBounded(path, value, jsonlutil.BoundOptions{
		MaxRecords: maxRecords,
		MaxBytes:   maxBytes,
	})
}

func listLatestJSONLByKey[T any](path string, limit int, keyFn func(T) string) ([]T, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []T
	if err := readJSONL(path, func(line []byte) error {
		var item T
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []T{}, nil
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

func readJSONL(path string, fn func([]byte) error) error {
	return jsonlutil.Read(path, fn)
}
