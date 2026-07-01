package knowledgememory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	domainkm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/knowledgememory"
)

type JSONLStore struct {
	personalPath string
	creativePath string
	newsPath     string
	intakePath   string
	temporalPath string
	dreamPath    string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/knowledge_memory"
	}
	return &JSONLStore{
		personalPath: filepath.Join(root, "personal_archive.jsonl"),
		creativePath: filepath.Join(root, "creative_knowledge.jsonl"),
		newsPath:     filepath.Join(root, "news_knowledge.jsonl"),
		intakePath:   filepath.Join(root, "daily_intake_rule.jsonl"),
		temporalPath: filepath.Join(root, "temporal_memory_marker.jsonl"),
		dreamPath:    filepath.Join(root, "dream_consolidation_run.jsonl"),
	}
}

func (s *JSONLStore) SavePersonalArchiveEntry(_ context.Context, item domainkm.PersonalArchiveEntry) error {
	if err := domainkm.ValidatePersonalArchiveEntry(item); err != nil {
		return err
	}
	return appendJSONL(s.personalPath, item)
}
func (s *JSONLStore) ListPersonalArchiveEntries(_ context.Context, limit int) ([]domainkm.PersonalArchiveEntry, error) {
	var items []domainkm.PersonalArchiveEntry
	err := readJSONL(s.personalPath, func(line []byte) error {
		var item domainkm.PersonalArchiveEntry
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return latestByID(items, normalizedLimit(limit), func(item domainkm.PersonalArchiveEntry) string {
		return item.EntryID
	}), err
}

func (s *JSONLStore) SaveCreativeKnowledgeItem(_ context.Context, item domainkm.CreativeKnowledgeItem) error {
	if err := domainkm.ValidateCreativeKnowledgeItem(item); err != nil {
		return err
	}
	return appendJSONL(s.creativePath, item)
}
func (s *JSONLStore) ListCreativeKnowledgeItems(_ context.Context, limit int) ([]domainkm.CreativeKnowledgeItem, error) {
	var items []domainkm.CreativeKnowledgeItem
	err := readJSONL(s.creativePath, func(line []byte) error {
		var item domainkm.CreativeKnowledgeItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return latestByID(items, normalizedLimit(limit), func(item domainkm.CreativeKnowledgeItem) string {
		return item.ItemID
	}), err
}

func (s *JSONLStore) SaveNewsKnowledgeItem(_ context.Context, item domainkm.NewsKnowledgeItem) error {
	if err := domainkm.ValidateNewsKnowledgeItem(item); err != nil {
		return err
	}
	return appendJSONL(s.newsPath, item)
}
func (s *JSONLStore) ListNewsKnowledgeItems(_ context.Context, limit int) ([]domainkm.NewsKnowledgeItem, error) {
	var items []domainkm.NewsKnowledgeItem
	err := readJSONL(s.newsPath, func(line []byte) error {
		var item domainkm.NewsKnowledgeItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return latestByID(items, normalizedLimit(limit), func(item domainkm.NewsKnowledgeItem) string {
		return item.ItemID
	}), err
}

func (s *JSONLStore) SaveDailyIntakeRule(_ context.Context, item domainkm.DailyIntakeRule) error {
	if err := domainkm.ValidateDailyIntakeRule(item); err != nil {
		return err
	}
	return appendJSONL(s.intakePath, item)
}
func (s *JSONLStore) ListDailyIntakeRules(_ context.Context, limit int) ([]domainkm.DailyIntakeRule, error) {
	var items []domainkm.DailyIntakeRule
	err := readJSONL(s.intakePath, func(line []byte) error {
		var item domainkm.DailyIntakeRule
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return latestByID(items, normalizedLimit(limit), func(item domainkm.DailyIntakeRule) string {
		return item.RuleID
	}), err
}

func (s *JSONLStore) SaveTemporalMemoryMarker(_ context.Context, item domainkm.TemporalMemoryMarker) error {
	if err := domainkm.ValidateTemporalMemoryMarker(item); err != nil {
		return err
	}
	return appendJSONL(s.temporalPath, item)
}
func (s *JSONLStore) ListTemporalMemoryMarkers(_ context.Context, limit int) ([]domainkm.TemporalMemoryMarker, error) {
	var items []domainkm.TemporalMemoryMarker
	err := readJSONL(s.temporalPath, func(line []byte) error {
		var item domainkm.TemporalMemoryMarker
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return latestByID(items, normalizedLimit(limit), func(item domainkm.TemporalMemoryMarker) string {
		return item.MarkerID
	}), err
}

func (s *JSONLStore) SaveDreamConsolidationRun(_ context.Context, item domainkm.DreamConsolidationRun) error {
	if err := domainkm.ValidateDreamConsolidationRun(item); err != nil {
		return err
	}
	return appendJSONL(s.dreamPath, item)
}
func (s *JSONLStore) ListDreamConsolidationRuns(_ context.Context, limit int) ([]domainkm.DreamConsolidationRun, error) {
	var items []domainkm.DreamConsolidationRun
	err := readJSONL(s.dreamPath, func(line []byte) error {
		var item domainkm.DreamConsolidationRun
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return latestByID(items, normalizedLimit(limit), func(item domainkm.DreamConsolidationRun) string {
		return item.RunID
	}), err
}

func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func readJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func normalizedLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return limit
}

func latestByID[T any](items []T, limit int, idOf func(T) string) []T {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]T, 0, limit)
	seen := map[string]struct{}{}
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		id := idOf(items[i])
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, items[i])
	}
	return out
}
