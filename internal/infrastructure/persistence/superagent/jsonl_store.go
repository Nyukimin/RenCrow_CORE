package superagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	domainsuperagent "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/superagent"
)

type JSONLStore struct {
	agentRunPath       string
	subagentTaskPath   string
	contextPackPath    string
	messageChannelPath string
	traceEventPath     string
	runQueuePath       string
	maxContextTokens   int
}

func NewJSONLStore(root string, maxContextTokens int) *JSONLStore {
	if root == "" {
		root = "workspace/logs/superagent_harness"
	}
	return &JSONLStore{
		agentRunPath:       filepath.Join(root, "agent_run.jsonl"),
		subagentTaskPath:   filepath.Join(root, "subagent_task.jsonl"),
		contextPackPath:    filepath.Join(root, "context_pack.jsonl"),
		messageChannelPath: filepath.Join(root, "message_channel.jsonl"),
		traceEventPath:     filepath.Join(root, "trace_event.jsonl"),
		runQueuePath:       filepath.Join(root, "run_queue.jsonl"),
		maxContextTokens:   maxContextTokens,
	}
}

func (s *JSONLStore) SaveAgentRun(_ context.Context, item domainsuperagent.AgentRun) error {
	if err := domainsuperagent.ValidateAgentRun(item); err != nil {
		return err
	}
	return appendJSONL(s.agentRunPath, item)
}

func (s *JSONLStore) ListAgentRuns(_ context.Context, limit int) ([]domainsuperagent.AgentRun, error) {
	var items []domainsuperagent.AgentRun
	err := readJSONL(s.agentRunPath, func(line []byte) error {
		var item domainsuperagent.AgentRun
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return latestAgentRuns(items, normalizedLimit(limit)), err
}

func latestAgentRuns(items []domainsuperagent.AgentRun, limit int) []domainsuperagent.AgentRun {
	if limit <= 0 {
		limit = len(items)
	}
	seen := map[string]struct{}{}
	out := make([]domainsuperagent.AgentRun, 0, minRunQueueLimit(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		item := items[i]
		if _, ok := seen[item.RunID]; ok {
			continue
		}
		seen[item.RunID] = struct{}{}
		out = append(out, item)
	}
	return out
}

func (s *JSONLStore) SaveSubagentTask(_ context.Context, item domainsuperagent.SubagentTask) error {
	if err := domainsuperagent.ValidateSubagentTask(item); err != nil {
		return err
	}
	return appendJSONL(s.subagentTaskPath, item)
}

func (s *JSONLStore) ListSubagentTasks(_ context.Context, limit int) ([]domainsuperagent.SubagentTask, error) {
	var items []domainsuperagent.SubagentTask
	err := readJSONL(s.subagentTaskPath, func(line []byte) error {
		var item domainsuperagent.SubagentTask
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return reverseLimit(items, normalizedLimit(limit)), err
}

func (s *JSONLStore) SaveContextPack(_ context.Context, item domainsuperagent.ContextPack) error {
	if err := domainsuperagent.ValidateContextPack(item, s.maxContextTokens); err != nil {
		return err
	}
	return appendJSONL(s.contextPackPath, item)
}

func (s *JSONLStore) ListContextPacks(_ context.Context, limit int) ([]domainsuperagent.ContextPack, error) {
	var items []domainsuperagent.ContextPack
	err := readJSONL(s.contextPackPath, func(line []byte) error {
		var item domainsuperagent.ContextPack
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return reverseLimit(items, normalizedLimit(limit)), err
}

func (s *JSONLStore) SaveMessageChannel(_ context.Context, item domainsuperagent.MessageChannel) error {
	if err := domainsuperagent.ValidateMessageChannel(item); err != nil {
		return err
	}
	return appendJSONL(s.messageChannelPath, item)
}

func (s *JSONLStore) ListMessageChannels(_ context.Context, limit int) ([]domainsuperagent.MessageChannel, error) {
	var items []domainsuperagent.MessageChannel
	err := readJSONL(s.messageChannelPath, func(line []byte) error {
		var item domainsuperagent.MessageChannel
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return reverseLimit(items, normalizedLimit(limit)), err
}

func (s *JSONLStore) SaveTraceEvent(_ context.Context, item domainsuperagent.TraceEvent) error {
	if err := domainsuperagent.ValidateTraceEvent(item); err != nil {
		return err
	}
	return appendJSONL(s.traceEventPath, item)
}

func (s *JSONLStore) ListTraceEvents(_ context.Context, limit int) ([]domainsuperagent.TraceEvent, error) {
	var items []domainsuperagent.TraceEvent
	err := readJSONL(s.traceEventPath, func(line []byte) error {
		var item domainsuperagent.TraceEvent
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return reverseLimit(items, normalizedLimit(limit)), err
}

func (s *JSONLStore) SaveRunQueueItem(_ context.Context, item domainsuperagent.RunQueueItem) error {
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return err
	}
	return appendJSONL(s.runQueuePath, item)
}

func (s *JSONLStore) ListRunQueueItems(_ context.Context, limit int) ([]domainsuperagent.RunQueueItem, error) {
	var items []domainsuperagent.RunQueueItem
	err := readJSONL(s.runQueuePath, func(line []byte) error {
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return latestRunQueueItems(items, normalizedLimit(limit)), err
}

func latestRunQueueItems(items []domainsuperagent.RunQueueItem, limit int) []domainsuperagent.RunQueueItem {
	if limit <= 0 {
		limit = len(items)
	}
	seen := map[string]struct{}{}
	out := make([]domainsuperagent.RunQueueItem, 0, minRunQueueLimit(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		item := items[i]
		if _, ok := seen[item.QueueID]; ok {
			continue
		}
		seen[item.QueueID] = struct{}{}
		out = append(out, item)
	}
	return out
}

func minRunQueueLimit(a, b int) int {
	if a < b {
		return a
	}
	return b
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

func reverseLimit[T any](items []T, limit int) []T {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]T, 0, limit)
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, items[i])
	}
	return out
}
