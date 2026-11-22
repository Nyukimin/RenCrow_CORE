package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskActivityInventoryScanLimit = 1000

func (s *JSONLStore) ListTaskActivityInventory(ctx context.Context, filter domaintask.TaskActivityInventoryFilter, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.TaskActivityInventoryPage, error) {
	var page domaintask.TaskActivityInventoryPage
	err := s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		reader, ok := tx.(domaintask.TaskActivityInventoryReader)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		var err error
		page, err = reader.ListTaskActivityInventory(ctx, filter, authorize)
		return err
	})
	return page, err
}

func (s *transactionStore) ListTaskActivityInventory(ctx context.Context, filter domaintask.TaskActivityInventoryFilter, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.TaskActivityInventoryPage, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.TaskActivityInventoryPage{}, err
	}
	if authorize == nil {
		return domaintask.TaskActivityInventoryPage{}, domaintask.ErrPrivateTaskAccessRequired
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return domaintask.TaskActivityInventoryPage{}, errors.New("task activity inventory limit must be between 1 and 200")
	}
	if filter.BeforeTaskID != "" {
		if _, err := parseCanonicalKey(string(filter.BeforeTaskID), taskKeyPrefix); err != nil {
			return domaintask.TaskActivityInventoryPage{}, fmt.Errorf("invalid task activity inventory cursor: %w", err)
		}
	}
	if !validInventoryFilter(filter) {
		return domaintask.TaskActivityInventoryPage{}, errors.New("task activity inventory filter is invalid")
	}
	candidates, more, err := s.taskActivityInventoryCandidates(ctx, filter.BeforeTaskID)
	if err != nil {
		return domaintask.TaskActivityInventoryPage{}, err
	}
	page := domaintask.TaskActivityInventoryPage{Items: make([]domaintask.TaskActivityInventoryRow, 0, filter.Limit)}
	selected := make([]domaintask.Task, 0, filter.Limit)
	var cursor modulecore.TaskID
	for index, item := range candidates {
		if err := ctx.Err(); err != nil {
			return domaintask.TaskActivityInventoryPage{}, err
		}
		cursor = item.TaskID
		input, err := inventoryTaskReadInput(item)
		if err != nil {
			return domaintask.TaskActivityInventoryPage{}, err
		}
		if err := authorize(ctx, input); err != nil {
			if errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden) {
				continue
			}
			return domaintask.TaskActivityInventoryPage{}, err
		}
		if !inventoryTaskMatches(item, filter) {
			continue
		}
		page.Items = append(page.Items, taskActivityInventoryRow(item))
		selected = append(selected, item)
		if len(page.Items) == filter.Limit {
			if index+1 < len(candidates) || more {
				page.NextCursor = cursor
			}
			if err := reauthorizeInventoryTasks(ctx, selected, authorize); err != nil {
				return domaintask.TaskActivityInventoryPage{}, err
			}
			return page, nil
		}
	}
	if more && cursor != "" {
		page.NextCursor = cursor
	}
	if err := reauthorizeInventoryTasks(ctx, selected, authorize); err != nil {
		return domaintask.TaskActivityInventoryPage{}, err
	}
	return page, nil
}

func (s *transactionStore) taskActivityInventoryCandidates(ctx context.Context, before modulecore.TaskID) ([]domaintask.Task, bool, error) {
	if s.parent.idx == nil {
		items, err := s.loadTasks(ctx)
		if err != nil {
			return nil, false, err
		}
		sort.Slice(items, func(i, j int) bool { return items[i].TaskID > items[j].TaskID })
		filtered := make([]domaintask.Task, 0, taskActivityInventoryScanLimit)
		for _, item := range items {
			if before != "" && item.TaskID >= before {
				continue
			}
			if len(filtered) == taskActivityInventoryScanLimit {
				return filtered, true, nil
			}
			filtered = append(filtered, item)
		}
		return filtered, false, nil
	}

	ix := s.parent.idx
	pending := s.overlaySnapshot().tasks
	unlock := s.indexLock()
	defer unlock()
	keys := make([]key16, 0, len(ix.tasks)+len(pending))
	seen := make(map[key16]struct{}, len(ix.tasks)+len(pending))
	for _, record := range ix.tasks {
		if _, overwritten := pending[record.key]; overwritten {
			continue
		}
		keys = append(keys, record.key)
		seen[record.key] = struct{}{}
	}
	for key := range pending {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) > 0 })
	cursorKey := key16{}
	if before != "" {
		var err error
		cursorKey, err = parseCanonicalKey(string(before), taskKeyPrefix)
		if err != nil {
			return nil, false, err
		}
	}
	candidates := make([]domaintask.Task, 0, taskActivityInventoryScanLimit)
	for _, key := range keys {
		if before != "" && bytes.Compare(key[:], cursorKey[:]) >= 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(candidates) == taskActivityInventoryScanLimit {
			return candidates, true, nil
		}
		if item, ok := pending[key]; ok {
			candidates = append(candidates, cloneTransactionTask(item))
			continue
		}
		position, ok := ix.taskTailLocked(key)
		if !ok {
			return nil, false, fmt.Errorf("Task activity index references missing Task %s: %w", key.id(taskKeyPrefix), ErrIndexInconsistent)
		}
		item, err := ix.readTaskAt(position, key)
		if err != nil {
			return nil, false, err
		}
		candidates = append(candidates, item)
	}
	return candidates, false, nil
}

func validInventoryFilter(filter domaintask.TaskActivityInventoryFilter) bool {
	for _, status := range []domaintask.Status{filter.Status, filter.ExcludeStatus} {
		if status == "" {
			continue
		}
		switch status {
		case domaintask.StatusQueued, domaintask.StatusRunning, domaintask.StatusWaiting, domaintask.StatusBlocked,
			domaintask.StatusFailed, domaintask.StatusSucceeded, domaintask.StatusCancelled, domaintask.StatusSuperseded:
		default:
			return false
		}
	}
	if filter.Category != "" {
		switch filter.Category {
		case domaintask.ActivityKindWork, domaintask.ActivityKindChat, domaintask.ActivityKindHobby, domaintask.ActivityKindRest,
			domaintask.ActivityKindSleep, domaintask.ActivityKindMoving, domaintask.ActivityKindGame, domaintask.ActivityKindEvaluating,
			domaintask.ActivityKindRoutine:
		default:
			return false
		}
	}
	return strings.TrimSpace(filter.Assignee) == filter.Assignee && len(filter.Assignee) <= 64
}

func inventoryTaskReadInput(item domaintask.Task) (domaintask.TaskReadAuthorizationInput, error) {
	if item.PrivateAccess != nil {
		input, err := domaintask.TaskReadInput(item, domaintask.TaskReadResourceTask, "")
		if err != nil {
			return domaintask.TaskReadAuthorizationInput{}, err
		}
		return input, nil
	}
	var publication *domaintask.OriginPublicationProvenance
	if item.OriginPublication != nil {
		copy := *item.OriginPublication
		publication = &copy
	}
	return domaintask.TaskReadAuthorizationInput{
		Resource: domaintask.TaskReadResourceTask, TaskID: item.TaskID,
		WorkstreamID: item.WorkstreamID, GoalID: item.GoalID,
		OriginSessionID: item.OriginSessionID, OriginPublication: publication,
	}, nil
}

func reauthorizeInventoryTasks(ctx context.Context, selected []domaintask.Task, authorize domaintask.TaskReadAuthorizationCheck) error {
	for _, item := range selected {
		input, err := inventoryTaskReadInput(item)
		if err != nil {
			return err
		}
		if err := authorize(ctx, input); err != nil {
			return err
		}
	}
	return nil
}

func inventoryTaskMatches(item domaintask.Task, filter domaintask.TaskActivityInventoryFilter) bool {
	if filter.Status != "" && item.Status != filter.Status {
		return false
	}
	if filter.ExcludeStatus != "" && item.Status == filter.ExcludeStatus {
		return false
	}
	if filter.Assignee != "" {
		match := strings.EqualFold(item.Assignee, filter.Assignee)
		if item.Activity != nil {
			for _, participant := range item.Activity.Participants {
				if participant.ActorID == filter.Assignee {
					match = true
					break
				}
			}
		}
		if !match {
			return false
		}
	}
	if filter.Category != "" && (item.Activity == nil || item.Activity.Kind != filter.Category) {
		return false
	}
	return true
}

func taskActivityInventoryRow(item domaintask.Task) domaintask.TaskActivityInventoryRow {
	return domaintask.TaskActivityInventorySummary(item)
}
