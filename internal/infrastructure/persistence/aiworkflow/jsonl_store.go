package aiworkflow

import (
	"context"
	"encoding/json"
	"path/filepath"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlutil"
)

const (
	contextUsageMaxRecords = 10000
	contextUsageMaxBytes   = int64(8 << 20)
)

type JSONLStore struct {
	projectMemoryPath string
	worktreePath      string
	commandPath       string
	contextUsagePath  string
	admissionWrites   *admissionState
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/ai_workflow"
	}
	return &JSONLStore{
		projectMemoryPath: filepath.Join(root, "project_memory_index.jsonl"),
		worktreePath:      filepath.Join(root, "worktree_registry.jsonl"),
		commandPath:       filepath.Join(root, "command_registry.jsonl"),
		contextUsagePath:  filepath.Join(root, "ai_context_usage.jsonl"),
		admissionWrites:   &admissionState{},
	}
}

func (s *JSONLStore) CompactOperationalLogs() error {
	return s.admissionWrites.withSynchronousFence(context.Background(), func() error {
		return jsonlutil.CompactLatestRecords(s.contextUsagePath, contextUsageMaxRecords)
	})
}

func (s *JSONLStore) SaveProjectMemoryIndex(ctx context.Context, item domainai.ProjectMemoryIndex) error {
	if err := domainai.ValidateProjectMemoryIndex(item); err != nil {
		return err
	}
	return s.save(ctx, "jsonl.project_memory_index.append", s.projectMemoryPath, item, false)
}

func (s *JSONLStore) ListProjectMemoryIndexes(ctx context.Context, limit int) ([]domainai.ProjectMemoryIndex, error) {
	var items []domainai.ProjectMemoryIndex
	err := s.admissionWrites.withSynchronousFence(ctx, func() error {
		var err error
		items, err = listLatestJSONLByKey(s.projectMemoryPath, limit, func(item domainai.ProjectMemoryIndex) string {
			return string(item.MemoryID)
		})
		return err
	})
	return items, err
}

func (s *JSONLStore) SaveWorktreeRegistry(ctx context.Context, item domainai.WorktreeRegistry) error {
	if err := domainai.ValidateWorktreeRegistry(item); err != nil {
		return err
	}
	return s.save(ctx, "jsonl.worktree_registry.append", s.worktreePath, item, false)
}

func (s *JSONLStore) ListWorktreeRegistries(ctx context.Context, limit int) ([]domainai.WorktreeRegistry, error) {
	var items []domainai.WorktreeRegistry
	err := s.admissionWrites.withSynchronousFence(ctx, func() error {
		var err error
		items, err = listLatestJSONLByKey(s.worktreePath, limit, func(item domainai.WorktreeRegistry) string {
			return item.WorktreeID
		})
		return err
	})
	return items, err
}

func (s *JSONLStore) SaveCommandRegistry(ctx context.Context, item domainai.CommandRegistry) error {
	if err := domainai.ValidateCommandRegistry(item); err != nil {
		return err
	}
	return s.save(ctx, "jsonl.command_registry.append", s.commandPath, item, false)
}

func (s *JSONLStore) ListCommandRegistries(ctx context.Context, limit int) ([]domainai.CommandRegistry, error) {
	var items []domainai.CommandRegistry
	err := s.admissionWrites.withSynchronousFence(ctx, func() error {
		var err error
		items, err = listLatestJSONLByKey(s.commandPath, limit, func(item domainai.CommandRegistry) string {
			return item.CommandName
		})
		return err
	})
	return items, err
}

func (s *JSONLStore) SaveContextUsage(ctx context.Context, item domainai.ContextUsage) error {
	if err := domainai.ValidateContextUsage(item); err != nil {
		return err
	}
	return s.save(ctx, "jsonl.ai_context_usage.append", s.contextUsagePath, item, true)
}

func (s *JSONLStore) ListContextUsages(ctx context.Context, limit int) ([]domainai.ContextUsage, error) {
	var items []domainai.ContextUsage
	err := s.admissionWrites.withSynchronousFence(ctx, func() error {
		var err error
		items, err = jsonlutil.ListLatest[domainai.ContextUsage](s.contextUsagePath, limit)
		return err
	})
	return items, err
}

func (s *JSONLStore) save(ctx context.Context, kind, path string, item any, bounded bool) error {
	if s == nil {
		return ErrAdmissionUnavailable
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return persist(ctx, s.admissionWrites, kind, payload, func(_ context.Context, immutablePayload []byte) error {
		value := json.RawMessage(immutablePayload)
		if bounded {
			return appendJSONLBounded(path, value, contextUsageMaxRecords, contextUsageMaxBytes)
		}
		return appendJSONL(path, value)
	})
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
