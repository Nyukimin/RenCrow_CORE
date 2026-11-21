package task

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const activityRootCandidateProbeLimit = domaintask.MaxActivityRootCandidateScan + 1

var _ domaintask.ActivityRootTaskCandidateReader = (*JSONLStore)(nil)
var _ domaintask.ActivityRootTaskCandidateReader = (*transactionStore)(nil)

// FindRunningRootActivityCandidate is a trusted CORE activity-owner read. It
// reads through a transaction snapshot so private and owner-managed roots can
// be considered without weakening public TaskStore read guards. Do not expose
// this projection through viewer, RPC, or MCP read endpoints.
func (s *JSONLStore) FindRunningRootActivityCandidate(ctx context.Context, actorID string, excludeTaskID modulecore.TaskID) (domaintask.ActivityRootTaskCandidate, bool, error) {
	if s == nil || ctx == nil {
		return domaintask.ActivityRootTaskCandidate{}, false, errors.New("activity root candidate reader is unavailable")
	}
	var candidate domaintask.ActivityRootTaskCandidate
	var found bool
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		reader, ok := store.(domaintask.ActivityRootTaskCandidateReader)
		if !ok {
			return errors.New("activity root candidate transaction reader is unavailable")
		}
		var err error
		candidate, found, err = reader.FindRunningRootActivityCandidate(ctx, actorID, excludeTaskID)
		return err
	})
	if err != nil {
		return domaintask.ActivityRootTaskCandidate{}, false, err
	}
	return candidate, found, nil
}

// FindRunningRootActivityCandidate evaluates the current transaction view and
// returns only the narrow activity facts the owner needs for a handoff.
func (s *transactionStore) FindRunningRootActivityCandidate(ctx context.Context, actorID string, excludeTaskID modulecore.TaskID) (domaintask.ActivityRootTaskCandidate, bool, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.ActivityRootTaskCandidate{}, false, err
	}
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(actorID) != actorID {
		return domaintask.ActivityRootTaskCandidate{}, false, errors.New("activity root candidate actor is invalid")
	}
	if excludeTaskID != "" {
		if err := excludeTaskID.Validate(); err != nil {
			return domaintask.ActivityRootTaskCandidate{}, false, fmt.Errorf("excluded activity Task ID is invalid: %w", err)
		}
	}
	if s.parent == nil {
		return domaintask.ActivityRootTaskCandidate{}, false, errors.New("activity root candidate Task store is unavailable")
	}
	if s.parent.idx == nil {
		if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
			return domaintask.ActivityRootTaskCandidate{}, false, ErrTaskAdmissionRequiresIndex
		}
		return s.findRunningRootActivityCandidateFromSnapshot(ctx, actorID, excludeTaskID)
	}
	return s.findRunningRootActivityCandidateFromIndex(ctx, actorID, excludeTaskID)
}

func (s *transactionStore) findRunningRootActivityCandidateFromSnapshot(ctx context.Context, actorID string, excludeTaskID modulecore.TaskID) (domaintask.ActivityRootTaskCandidate, bool, error) {
	tasks, err := s.loadTasks(ctx)
	if err != nil {
		return domaintask.ActivityRootTaskCandidate{}, false, err
	}
	runs, err := s.loadRuns(ctx)
	if err != nil {
		return domaintask.ActivityRootTaskCandidate{}, false, err
	}
	page := newBestN(activityRootCandidateProbeLimit, func(a, b domaintask.Task) bool { return a.TaskID < b.TaskID })
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return domaintask.ActivityRootTaskCandidate{}, false, err
		}
		if task.TaskID == excludeTaskID || !isRawActivityRootCandidate(task, actorID) {
			continue
		}
		page.add(task)
	}
	candidates := page.result()
	if len(candidates) > domaintask.MaxActivityRootCandidateScan {
		candidates = candidates[:domaintask.MaxActivityRootCandidateScan]
	}
	selected := make(map[modulecore.TaskID]struct{}, len(candidates))
	for _, task := range candidates {
		selected[task.TaskID] = struct{}{}
	}
	activeRuns := make(map[modulecore.TaskID]struct{}, len(candidates))
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return domaintask.ActivityRootTaskCandidate{}, false, err
		}
		if run.Status == domaintask.RunStatusRunning && run.Assignee == actorID {
			if _, wanted := selected[run.TaskID]; wanted {
				activeRuns[run.TaskID] = struct{}{}
			}
		}
	}
	for _, task := range candidates {
		if err := ctx.Err(); err != nil {
			return domaintask.ActivityRootTaskCandidate{}, false, err
		}
		_, hasActiveRun := activeRuns[task.TaskID]
		candidate, eligible := projectActivityRootCandidate(task, actorID, hasActiveRun)
		if eligible {
			return candidate, true, nil
		}
	}
	if len(page.items) > domaintask.MaxActivityRootCandidateScan {
		return domaintask.ActivityRootTaskCandidate{}, false, domaintask.ErrActivityRootCandidateSearchLimit
	}
	return domaintask.ActivityRootTaskCandidate{}, false, nil
}

func (s *transactionStore) findRunningRootActivityCandidateFromIndex(ctx context.Context, actorID string, excludeTaskID modulecore.TaskID) (domaintask.ActivityRootTaskCandidate, bool, error) {
	ix := s.parent.idx
	if ix == nil {
		return domaintask.ActivityRootTaskCandidate{}, false, errors.New("activity root candidate index is unavailable")
	}
	overlay := s.overlaySnapshot()
	var excludeKey key16
	if excludeTaskID != "" {
		var err error
		excludeKey, err = parseCanonicalKey(string(excludeTaskID), taskKeyPrefix)
		if err != nil {
			return domaintask.ActivityRootTaskCandidate{}, false, fmt.Errorf("parse excluded activity Task ID: %w", err)
		}
	}
	unlock := s.indexLock()
	assigneeIDs := ix.assignees.equalFold(actorID)
	runningStatus, hasRunningStatus := ix.enums.lookup(string(domaintask.StatusRunning))
	gameRoute, hasGameRoute := ix.enums.lookup(string(domaintask.RouteGAME))
	runningRunStatus, hasRunningRunStatus := ix.enums.lookup(string(domaintask.RunStatusRunning))
	if len(assigneeIDs) == 0 || !hasRunningStatus {
		unlock()
		return domaintask.ActivityRootTaskCandidate{}, false, nil
	}
	page := newBestN(activityRootCandidateProbeLimit, taskCandByIDBefore)
	for slot := range ix.tasks {
		if err := ctx.Err(); err != nil {
			unlock()
			return domaintask.ActivityRootTaskCandidate{}, false, err
		}
		rec := &ix.tasks[slot]
		if (excludeTaskID != "" && rec.key == excludeKey) || uint32(rec.status) != runningStatus ||
			!containsID(assigneeIDs, rec.assignee) || ix.assignees.value(rec.assignee) != actorID || (hasGameRoute && uint32(rec.route) == gameRoute) {
			continue
		}
		if relations, found := ix.relations[uint32(slot)]; found && relations.parent != "" {
			continue
		}
		if _, overridden := overlay.tasks[rec.key]; overridden {
			continue
		}
		pos, found := ix.taskTailLocked(rec.key)
		if !found {
			unlock()
			return domaintask.ActivityRootTaskCandidate{}, false, fmt.Errorf("activity root candidate has no indexed Task state: %w", ErrIndexInconsistent)
		}
		page.add(taskCand{key: rec.key, pos: pos})
	}
	for key, task := range overlay.tasks {
		if (excludeTaskID != "" && key == excludeKey) || !isRawActivityRootCandidate(task, actorID) {
			continue
		}
		value := cloneTransactionTask(task)
		page.add(taskCand{key: key, value: &value})
	}
	candidates := page.result()
	scanCount := len(candidates)
	if scanCount > domaintask.MaxActivityRootCandidateScan {
		scanCount = domaintask.MaxActivityRootCandidateScan
	}
	activeRuns := make(map[key16]bool, scanCount)
	for _, item := range candidates[:scanCount] {
		if err := ctx.Err(); err != nil {
			unlock()
			return domaintask.ActivityRootTaskCandidate{}, false, err
		}
		taskID := modulecore.TaskID(item.key.id(taskKeyPrefix))
		if item.value != nil {
			taskID = item.value.TaskID
		}
		active, err := taskHasActiveRunFromOverlayOrIndex(ctx, ix, item.key, taskID, actorID, runningRunStatus, hasRunningRunStatus, overlay.runs)
		if err != nil {
			unlock()
			return domaintask.ActivityRootTaskCandidate{}, false, err
		}
		activeRuns[item.key] = active
	}
	unlock()
	for _, item := range candidates[:scanCount] {
		if err := ctx.Err(); err != nil {
			return domaintask.ActivityRootTaskCandidate{}, false, err
		}
		var task domaintask.Task
		if item.value != nil {
			task = cloneTransactionTask(*item.value)
		} else {
			var err error
			task, err = ix.readTaskAt(item.pos, item.key)
			if err != nil {
				return domaintask.ActivityRootTaskCandidate{}, false, err
			}
		}
		candidate, eligible := projectActivityRootCandidate(task, actorID, activeRuns[item.key])
		if eligible {
			return candidate, true, nil
		}
	}
	if len(page.items) > domaintask.MaxActivityRootCandidateScan {
		return domaintask.ActivityRootTaskCandidate{}, false, domaintask.ErrActivityRootCandidateSearchLimit
	}
	return domaintask.ActivityRootTaskCandidate{}, false, nil
}

func isRawActivityRootCandidate(task domaintask.Task, actorID string) bool {
	return task.Assignee == actorID && task.Status == domaintask.StatusRunning && task.ParentTaskID == "" && task.Route != domaintask.RouteGAME
}

func projectActivityRootCandidate(task domaintask.Task, actorID string, hasActiveRun bool) (domaintask.ActivityRootTaskCandidate, bool) {
	if !hasActiveRun || !isRawActivityRootCandidate(task, actorID) || task.Activity == nil || task.Activity.Passive || task.Activity.Kind == domaintask.ActivityKindGame {
		return domaintask.ActivityRootTaskCandidate{}, false
	}
	for _, participant := range task.Activity.Participants {
		if participant.ActorID == actorID {
			return domaintask.ActivityRootTaskCandidate{
				TaskID: task.TaskID, Kind: task.Activity.Kind, Label: task.Activity.Label, Phase: task.Activity.Phase,
				ParticipantRole: participant.Role,
			}, true
		}
	}
	return domaintask.ActivityRootTaskCandidate{}, false
}

func indexedTaskHasActiveRun(ctx context.Context, ix *taskIndex, task *taskRec, actorID string, runningRunStatus uint32, hasRunningRunStatus bool, pending map[key16]domaintask.Run) (bool, error) {
	for run := task.runHead; hasRunningRunStatus && run != 0; run = ix.runs[run-1].nextRun {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		rec := &ix.runs[run-1]
		if pendingRun, replaced := pending[rec.key]; replaced {
			if pendingRun.Status == domaintask.RunStatusRunning && pendingRun.Assignee == actorID {
				return true, nil
			}
			continue
		}
		if uint32(rec.status) == runningRunStatus && ix.assignees.value(rec.assignee) == actorID {
			return true, nil
		}
	}
	return false, nil
}

func taskHasActiveRunFromOverlayOrIndex(ctx context.Context, ix *taskIndex, key key16, taskID modulecore.TaskID, actorID string, runningRunStatus uint32, hasRunningRunStatus bool, pending map[key16]domaintask.Run) (bool, error) {
	if slot, found := ix.taskMap[key]; found {
		active, err := indexedTaskHasActiveRun(ctx, ix, &ix.tasks[slot], actorID, runningRunStatus, hasRunningRunStatus, pending)
		if err != nil || active {
			return active, err
		}
	}
	for _, run := range pending {
		if run.TaskID == taskID && run.Status == domaintask.RunStatusRunning && run.Assignee == actorID {
			return true, nil
		}
	}
	return false, nil
}
