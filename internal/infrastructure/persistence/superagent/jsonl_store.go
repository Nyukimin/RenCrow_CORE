package superagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type JSONLStore struct {
	runQueueMu         sync.Mutex
	agentRunPath       string
	subagentTaskPath   string
	contextPackPath    string
	messageChannelPath string
	runQueuePath       string
	maxContextTokens   int
	writeQueue         *persistencequeue.Queue[persistencequeue.PersistenceCommand]
}

func NewJSONLStore(root string, maxContextTokens int) *JSONLStore {
	return newJSONLStore(root, maxContextTokens, nil)
}

// NewJSONLStoreWithWriteQueue configures the store to enqueue writes admitted
// by the same process-local FIFO used by the rest of the runtime.
func NewJSONLStoreWithWriteQueue(root string, maxContextTokens int, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) (*JSONLStore, error) {
	if queue == nil {
		return nil, persistencequeue.ErrNilPersistenceQueue
	}
	return newJSONLStore(root, maxContextTokens, queue), nil
}

func newJSONLStore(root string, maxContextTokens int, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) *JSONLStore {
	if root == "" {
		root = "workspace/logs/superagent_harness"
	}
	return &JSONLStore{
		agentRunPath:       filepath.Join(root, "agent_run.jsonl"),
		subagentTaskPath:   filepath.Join(root, "subagent_task.jsonl"),
		contextPackPath:    filepath.Join(root, "context_pack.jsonl"),
		messageChannelPath: filepath.Join(root, "message_channel.jsonl"),
		runQueuePath:       filepath.Join(root, "run_queue.jsonl"),
		maxContextTokens:   maxContextTokens,
		writeQueue:         queue,
	}
}

func (s *JSONLStore) SaveAgentRun(ctx context.Context, item domainsuperagent.AgentRun) error {
	if err := domainsuperagent.ValidateAgentRun(item); err != nil {
		return err
	}
	return s.appendAdmitted(ctx, s.agentRunPath, "jsonl.agent_run.append", item)
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

// FindAgentRunByID returns the latest valid record with exactly the requested run ID.
func (s *JSONLStore) FindAgentRunByID(_ context.Context, runID string) (domainsuperagent.AgentRun, bool, error) {
	var found domainsuperagent.AgentRun
	foundRecord := false
	err := readJSONL(s.agentRunPath, func(line []byte) error {
		var item domainsuperagent.AgentRun
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if err := domainsuperagent.ValidateAgentRun(item); err != nil {
			return err
		}
		if string(item.RunID) == runID {
			found = item
			foundRecord = true
		}
		return nil
	})
	if err != nil {
		return domainsuperagent.AgentRun{}, false, err
	}
	return found, foundRecord, err
}

func latestAgentRuns(items []domainsuperagent.AgentRun, limit int) []domainsuperagent.AgentRun {
	if limit <= 0 {
		limit = len(items)
	}
	seen := map[string]struct{}{}
	out := make([]domainsuperagent.AgentRun, 0, minRunQueueLimit(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		item := items[i]
		if _, ok := seen[string(item.RunID)]; ok {
			continue
		}
		seen[string(item.RunID)] = struct{}{}
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
	return latestSubagentTasks(items, normalizedLimit(limit)), err
}

func latestSubagentTasks(items []domainsuperagent.SubagentTask, limit int) []domainsuperagent.SubagentTask {
	if limit <= 0 {
		limit = len(items)
	}
	seen := map[string]struct{}{}
	out := make([]domainsuperagent.SubagentTask, 0, minRunQueueLimit(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		item := items[i]
		key := string(item.TaskID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func (s *JSONLStore) SaveContextPack(ctx context.Context, item domainsuperagent.ContextPack) error {
	if err := domainsuperagent.ValidateContextPack(item, s.maxContextTokens); err != nil {
		return err
	}
	return s.appendAdmitted(ctx, s.contextPackPath, "jsonl.context_pack.append", item)
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

func (s *JSONLStore) SaveRunQueueItem(_ context.Context, item domainsuperagent.RunQueueItem) error {
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return err
	}
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	return appendJSONL(s.runQueuePath, item)
}

// FindRunQueueItemByTaskIntent returns the earliest receipt for one canonical
// Task and intent key with that queue item's latest appended state. It scans
// the full append-only history so an old completed receipt remains a dedup hit.
func (s *JSONLStore) FindRunQueueItemByTaskIntent(ctx context.Context, taskID modulecore.TaskID, intent string) (domainsuperagent.RunQueueItem, bool, error) {
	if s == nil {
		return domainsuperagent.RunQueueItem{}, false, errors.New("superagent JSONL store is unavailable")
	}
	if err := taskID.Validate(); err != nil {
		return domainsuperagent.RunQueueItem{}, false, fmt.Errorf("task_id is invalid: %w", err)
	}
	if strings.TrimSpace(intent) == "" || strings.TrimSpace(intent) != intent {
		return domainsuperagent.RunQueueItem{}, false, errors.New("run queue intent is required")
	}
	if ctx == nil {
		return domainsuperagent.RunQueueItem{}, false, errors.New("run queue lookup context is required")
	}
	if err := ctx.Err(); err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	return s.findRunQueueItemByTaskIntentUnlocked(taskID, intent)
}

// InsertRunQueueItemIfAbsent appends one first-run intent under the same
// process-local queue lock used by queue claims and lifecycle updates. A retry
// returns the existing item and never appends a replacement receipt.
func (s *JSONLStore) InsertRunQueueItemIfAbsent(ctx context.Context, item domainsuperagent.RunQueueItem) (domainsuperagent.RunQueueItem, bool, error) {
	if s == nil {
		return domainsuperagent.RunQueueItem{}, false, errors.New("superagent JSONL store is unavailable")
	}
	if ctx == nil {
		return domainsuperagent.RunQueueItem{}, false, errors.New("run queue insert context is required")
	}
	if err := ctx.Err(); err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	if strings.TrimSpace(item.IdempotencyKey) == "" || strings.TrimSpace(item.IdempotencyKey) != item.IdempotencyKey {
		return domainsuperagent.RunQueueItem{}, false, errors.New("run queue intent is required")
	}
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	existing, found, err := s.findRunQueueItemByTaskIntentUnlocked(item.TaskID, item.IdempotencyKey)
	if err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	if found {
		return existing, false, nil
	}
	if err := appendJSONL(s.runQueuePath, item); err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	return item, true, nil
}

func (s *JSONLStore) findRunQueueItemByTaskIntentUnlocked(taskID modulecore.TaskID, intent string) (domainsuperagent.RunQueueItem, bool, error) {
	var first domainsuperagent.RunQueueItem
	var latestFirst domainsuperagent.RunQueueItem
	found := false
	err := readJSONL(s.runQueuePath, func(line []byte) error {
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.TaskID != taskID || item.IdempotencyKey != intent {
			return nil
		}
		if !found {
			first, latestFirst, found = item, item, true
			return nil
		}
		if item.QueueItemID == first.QueueItemID {
			latestFirst = item
		}
		return nil
	})
	return latestFirst, found, err
}

func (s *JSONLStore) ClaimNextRunQueueItem(_ context.Context, now, leaseUntil time.Time, leaseToken string) (*domainsuperagent.RunQueueItem, error) {
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	items, err := s.listRunQueueItemsUnlocked(500)
	if err != nil {
		return nil, err
	}
	var selected *domainsuperagent.RunQueueItem
	for index := range items {
		item := items[index]
		if !runQueueItemClaimable(item, now) {
			continue
		}
		if selected == nil || runQueueItemBefore(item, *selected) {
			copy := item
			selected = &copy
		}
	}
	if selected == nil {
		return nil, nil
	}
	// JSONL storage owns only the lease reservation. The canonical Task owner
	// issues the execution Run afterwards, so a claim must never carry forward
	// or invent a RunID.
	selected.Status, selected.ClaimedAt, selected.LeaseToken, selected.LeaseUntil = "reserved", now, leaseToken, leaseUntil
	selected.RunID = ""
	selected.AttemptCount++
	selected.CompletedAt = time.Time{}
	if err := appendJSONL(s.runQueuePath, *selected); err != nil {
		return nil, err
	}
	return selected, nil
}

// AttachRunQueueRun atomically binds a canonical Task-owned Run to the current
// reserved lease. A stale or already-attached token returns false without a
// write.
func (s *JSONLStore) AttachRunQueueRun(_ context.Context, queueID, leaseToken string, canonicalRunID modulecore.RunID) (bool, error) {
	if err := canonicalRunID.Validate(); err != nil {
		return false, fmt.Errorf("canonical run_id is invalid: %w", err)
	}
	if strings.TrimSpace(leaseToken) == "" {
		return false, nil
	}
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	items, err := s.listRunQueueItemsUnlocked(500)
	if err != nil {
		return false, err
	}
	for index := range items {
		if items[index].QueueItemID != modulecore.QueueItemID(queueID) {
			continue
		}
		item := items[index]
		if item.Status != "reserved" || item.RunID != "" || item.LeaseToken != leaseToken {
			return false, nil
		}
		item.Status = "claimed"
		item.RunID = canonicalRunID
		if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
			return false, err
		}
		return true, appendJSONL(s.runQueuePath, item)
	}
	return false, nil
}

func (s *JSONLStore) RenewRunQueueLease(_ context.Context, queueID, leaseToken string, leaseUntil time.Time) (bool, error) {
	return s.updateRunQueueLease(queueID, leaseToken, false, false, func(item *domainsuperagent.RunQueueItem) { item.LeaseUntil = leaseUntil })
}

func (s *JSONLStore) CompleteRunQueueItem(_ context.Context, queueID, leaseToken, status, reason string, completedAt time.Time) (bool, error) {
	return s.updateRunQueueLease(queueID, leaseToken, true, status == "blocked", func(item *domainsuperagent.RunQueueItem) {
		item.Status, item.Reason, item.CompletedAt = status, reason, completedAt
		item.LeaseToken, item.LeaseUntil = "", time.Time{}
	})
}

func (s *JSONLStore) updateRunQueueLease(queueID, leaseToken string, claimedOnly, allowReservedBlock bool, mutate func(*domainsuperagent.RunQueueItem)) (bool, error) {
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	items, err := s.listRunQueueItemsUnlocked(500)
	if err != nil {
		return false, err
	}
	for index := range items {
		if items[index].QueueItemID != modulecore.QueueItemID(queueID) {
			continue
		}
		item := items[index]
		if (claimedOnly && item.Status != "claimed" && !(allowReservedBlock && item.Status == "reserved")) || (!claimedOnly && item.Status != "reserved" && item.Status != "claimed") || item.LeaseToken != leaseToken {
			return false, nil
		}
		mutate(&item)
		if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
			return false, err
		}
		return true, appendJSONL(s.runQueuePath, item)
	}
	return false, nil
}

func (s *JSONLStore) listRunQueueItemsUnlocked(limit int) ([]domainsuperagent.RunQueueItem, error) {
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

func (s *JSONLStore) ListRunQueueItems(_ context.Context, limit int) ([]domainsuperagent.RunQueueItem, error) {
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	return s.listRunQueueItemsUnlocked(limit)
}

// GetRunQueueItem reads the latest append-only receipt for the exact ID.
func (s *JSONLStore) GetRunQueueItem(ctx context.Context, queueID modulecore.QueueItemID) (domainsuperagent.RunQueueItem, bool, error) {
	items, err := s.GetRunQueueItems(ctx, []modulecore.QueueItemID{queueID})
	item, found := items[queueID]
	return item, found, err
}

// GetRunQueueItems reads exact IDs in one append-only scan, avoiding a
// full-history rescan for each bounded startup recovery candidate.
func (s *JSONLStore) GetRunQueueItems(ctx context.Context, queueIDs []modulecore.QueueItemID) (map[modulecore.QueueItemID]domainsuperagent.RunQueueItem, error) {
	if s == nil {
		return nil, errors.New("superagent JSONL store is unavailable")
	}
	wanted := make(map[modulecore.QueueItemID]struct{}, len(queueIDs))
	for _, queueID := range queueIDs {
		if err := queueID.Validate(); err != nil {
			return nil, fmt.Errorf("queue_item_id is invalid: %w", err)
		}
		wanted[queueID] = struct{}{}
	}
	items := make(map[modulecore.QueueItemID]domainsuperagent.RunQueueItem, len(wanted))
	if len(wanted) == 0 {
		return items, nil
	}
	if ctx == nil {
		return nil, errors.New("run queue lookup context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	err := readJSONL(s.runQueuePath, func(line []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if _, ok := wanted[item.QueueItemID]; ok {
			items[item.QueueItemID] = item
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for queueID, item := range items {
		if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
			return nil, err
		}
		if item.QueueItemID != queueID {
			return nil, fmt.Errorf("stored queue_item_id %q does not match requested ID %q", item.QueueItemID, queueID)
		}
	}
	return items, nil
}

// ListActiveRunQueueItems returns bounded unfinished reserved/claimed receipts
// after reconciling append-only updates by queue_item_id.
func (s *JSONLStore) ListActiveRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error) {
	if s == nil {
		return nil, errors.New("superagent JSONL store is unavailable")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("active run queue limit must be positive")
	}
	if ctx == nil {
		return nil, errors.New("active run queue context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	latest := make(map[modulecore.QueueItemID]domainsuperagent.RunQueueItem)
	err := readJSONL(s.runQueuePath, func(line []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		latest[item.QueueItemID] = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	items := make([]domainsuperagent.RunQueueItem, 0)
	for _, item := range latest {
		if item.Status != "reserved" && item.Status != "claimed" {
			continue
		}
		if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return string(items[i].QueueItemID) < string(items[j].QueueItemID)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func latestRunQueueItems(items []domainsuperagent.RunQueueItem, limit int) []domainsuperagent.RunQueueItem {
	if limit <= 0 {
		limit = len(items)
	}
	seen := map[string]struct{}{}
	out := make([]domainsuperagent.RunQueueItem, 0, minRunQueueLimit(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		item := items[i]
		if _, ok := seen[string(item.QueueItemID)]; ok {
			continue
		}
		seen[string(item.QueueItemID)] = struct{}{}
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

func (s *JSONLStore) appendAdmitted(ctx context.Context, path, kind string, value any) error {
	if s == nil {
		return errors.New("superagent JSONL store is unavailable")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	handled, err := enqueueAdmittedSuperAgentWrite(ctx, s.writeQueue, kind, string(payload), func(context.Context) error {
		return appendJSONLLine(path, string(payload))
	})
	if handled || err != nil {
		return err
	}
	return appendJSONLLine(path, string(payload))
}

func appendJSONLLine(path string, payload string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := make([]byte, len(payload)+1)
	copy(line, payload)
	line[len(payload)] = '\n'
	_, err = f.Write(line)
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

func (s *JSONLStore) ListRunQueueCandidatesForTasks(ctx context.Context, taskIDs []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error) {
	if s == nil {
		return nil, fmt.Errorf("superagent jsonl store is unavailable")
	}
	if len(taskIDs) == 0 {
		return nil, nil
	}
	allowed := make(map[modulecore.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if err := taskID.Validate(); err != nil {
			return nil, fmt.Errorf("list run queue items: invalid task_id: %w", err)
		}
		allowed[taskID] = struct{}{}
	}

	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	latest := make(map[modulecore.QueueItemID]domainsuperagent.RunQueueItem)
	overflow := false
	err := readJSONL(s.runQueuePath, func(line []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if _, ok := allowed[item.TaskID]; !ok {
			return nil
		}
		if _, exists := latest[item.QueueItemID]; !exists && len(latest) >= runQueueActorCandidateLimit {
			overflow = true
			return nil
		}
		latest[item.QueueItemID] = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	if overflow {
		return nil, fmt.Errorf("run queue actor candidate limit exceeded")
	}
	items := make([]domainsuperagent.RunQueueItem, 0, len(latest))
	for _, item := range latest {
		if item.Status == "queued" || item.Status == "reserved" {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *JSONLStore) ClaimRunQueueItem(ctx context.Context, queueID modulecore.QueueItemID, now, leaseUntil time.Time, leaseToken string) (*domainsuperagent.RunQueueItem, error) {
	if !validExactRunQueueLease(queueID, leaseToken) {
		return nil, fmt.Errorf("run queue exact claim requires a valid queue_item_id and lease token")
	}
	if !leaseUntil.After(now) {
		return nil, fmt.Errorf("run queue exact claim lease must expire after now")
	}
	if s == nil {
		return nil, fmt.Errorf("superagent jsonl store is unavailable")
	}
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	var current domainsuperagent.RunQueueItem
	found := false
	if err := readJSONL(s.runQueuePath, func(line []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.QueueItemID == queueID {
			current, found = item, true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if !found || !runQueueItemExactActorClaimable(current, now) {
		return nil, nil
	}
	reserveExactRunQueueItem(&current, now, leaseUntil, leaseToken)
	if err := domainsuperagent.ValidateRunQueueItem(current); err != nil {
		return nil, err
	}
	if err := appendJSONL(s.runQueuePath, current); err != nil {
		return nil, err
	}
	return &current, nil
}

func (s *JSONLStore) ReleaseRunQueueItem(ctx context.Context, queueID modulecore.QueueItemID, leaseToken string) (bool, error) {
	if !validExactRunQueueLease(queueID, leaseToken) {
		return false, nil
	}
	if s == nil {
		return false, fmt.Errorf("superagent jsonl store is unavailable")
	}
	s.runQueueMu.Lock()
	defer s.runQueueMu.Unlock()
	var current domainsuperagent.RunQueueItem
	found := false
	if err := readJSONL(s.runQueuePath, func(line []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.QueueItemID == queueID {
			current, found = item, true
		}
		return nil
	}); err != nil {
		return false, err
	}
	if !found || current.Status != "reserved" || current.LeaseToken != leaseToken || current.RunID != "" || current.AttemptCount <= 0 {
		return false, nil
	}
	releaseExactRunQueueItem(&current)
	if err := domainsuperagent.ValidateRunQueueItem(current); err != nil {
		return false, err
	}
	if err := appendJSONL(s.runQueuePath, current); err != nil {
		return false, err
	}
	return true, nil
}
