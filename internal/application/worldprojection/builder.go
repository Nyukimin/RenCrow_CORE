// Package worldprojection projects CORE-owned Task/Run and policy snapshots
// into the WORLD v1.4 contract. It never starts, finishes, or retries CORE
// work; a WorkSet is evidence of CORE's current assignment, not a result.
package worldprojection

import (
	"context"
	"errors"
	"fmt"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// DefaultClassificationReasonCode is used when CORE has no more specific
// reason for an active assigned task.
const DefaultClassificationReasonCode = "assigned_task"

// TaskRunSource is the read-only CORE Task/Run owner view used for a full
// snapshot. It must return the complete current set, not a filtered page.
type TaskRunSource interface {
	ListTasks(context.Context, domaintask.Filter) ([]domaintask.Task, error)
	ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error)
}

// BuildWorkSetInput is one full CORE snapshot decision. Presence and reason
// are explicit CORE decisions; they are not inferred from task status.
type BuildWorkSetInput struct {
	WorldID                   string
	ActorID                   string
	SourceEpoch               string
	SourceRevision            uint64
	ValidUntil                time.Time
	Tasks                     []domaintask.Task
	Runs                      []domaintask.Run
	RequiresHousePresence     map[modulecore.TaskID]bool
	ClassificationReasonCode  string
	ClassificationReasonCodes map[modulecore.TaskID]string
}

// BuildFullWorkSet reads the complete CORE Task/Run set and projects it. A
// stale or unavailable store must fail this call; it must not become an empty
// WorkSet.
func BuildFullWorkSet(ctx context.Context, source TaskRunSource, input BuildWorkSetInput) (domainprojection.WorkSet, error) {
	if source == nil {
		return domainprojection.WorkSet{}, fmt.Errorf("world projection: task/run source is nil")
	}
	if ctx == nil {
		return domainprojection.WorkSet{}, fmt.Errorf("world projection: context is nil")
	}
	// A WorkSet is a full active set, not a full history read. Terminal Tasks
	// and closed Runs are deliberately excluded at the store boundary so the
	// snapshot does not decode every historical record.
	var tasks []domaintask.Task
	for _, status := range []domaintask.Status{
		domaintask.StatusQueued,
		domaintask.StatusRunning,
		domaintask.StatusWaiting,
		domaintask.StatusBlocked,
	} {
		batch, err := source.ListTasks(ctx, domaintask.Filter{Status: status})
		if err != nil {
			return domainprojection.WorkSet{}, fmt.Errorf("world projection: list %s tasks: %w", status, err)
		}
		tasks = append(tasks, batch...)
	}
	runs, err := source.ListRuns(ctx, domaintask.RunFilter{Status: domaintask.RunStatusRunning})
	if err != nil {
		return domainprojection.WorkSet{}, fmt.Errorf("world projection: list running runs: %w", err)
	}
	input.Tasks = tasks
	input.Runs = runs
	return BuildWorkSet(input)
}

// BuildWorkSet maps active CORE tasks to WorkSet v2 items. Terminal tasks are
// absent from the full set; a running task must have exactly one running Run.
func BuildWorkSet(input BuildWorkSetInput) (domainprojection.WorkSet, error) {
	if input.SourceRevision == 0 {
		return domainprojection.WorkSet{}, fmt.Errorf("world projection: source revision is required")
	}
	runningByTask := make(map[modulecore.TaskID][]domaintask.Run)
	for _, run := range input.Runs {
		if run.Status != domaintask.RunStatusRunning {
			continue
		}
		if err := run.Validate(); err != nil {
			return domainprojection.WorkSet{}, fmt.Errorf("world projection: invalid running run %s: %w", run.RunID, err)
		}
		runningByTask[run.TaskID] = append(runningByTask[run.TaskID], run)
	}

	items := make([]domainprojection.WorkItem, 0, len(input.Tasks))
	seen := make(map[modulecore.TaskID]struct{}, len(input.Tasks))
	for _, task := range input.Tasks {
		if domaintask.IsTerminal(task.Status) {
			continue
		}
		if err := task.Validate(); err != nil {
			return domainprojection.WorkSet{}, fmt.Errorf("world projection: invalid active task %s: %w", task.TaskID, err)
		}
		if _, exists := seen[task.TaskID]; exists {
			return domainprojection.WorkSet{}, fmt.Errorf("world projection: duplicate task %s", task.TaskID)
		}
		seen[task.TaskID] = struct{}{}

		item := domainprojection.WorkItem{
			TaskID:                   string(task.TaskID),
			ExecutionState:           executionState(task.Status),
			WorkClass:                domainprojection.WorkClassAssignedWork,
			RequiresHousePresence:    input.RequiresHousePresence[task.TaskID],
			ClassificationReasonCode: classificationReason(input, task.TaskID),
		}
		if task.Status == domaintask.StatusRunning {
			runs := runningByTask[task.TaskID]
			if len(runs) != 1 {
				return domainprojection.WorkSet{}, fmt.Errorf(
					"world projection: running task %s has %d running runs, want exactly one", task.TaskID, len(runs))
			}
			item.RunID = string(runs[0].RunID)
		}
		items = append(items, item)
	}
	return domainprojection.NewWorkSet(input.WorldID, input.ActorID, input.SourceEpoch,
		input.SourceRevision, input.ValidUntil, items)
}

func executionState(status domaintask.Status) string {
	switch status {
	case domaintask.StatusQueued:
		return domainprojection.ExecutionQueued
	case domaintask.StatusRunning:
		return domainprojection.ExecutionRunning
	case domaintask.StatusWaiting:
		return domainprojection.ExecutionWaiting
	case domaintask.StatusBlocked:
		return domainprojection.ExecutionBlocked
	default:
		return ""
	}
}

func classificationReason(input BuildWorkSetInput, taskID modulecore.TaskID) string {
	if reason := input.ClassificationReasonCodes[taskID]; reason != "" {
		return reason
	}
	if input.ClassificationReasonCode != "" {
		return input.ClassificationReasonCode
	}
	return DefaultClassificationReasonCode
}

// BuildLifePolicyInput is the policy owner's explicit LifePolicy decision.
// CORE's Persona/Memory/Model bodies never enter this input.
type BuildLifePolicyInput struct {
	WorldID                        string
	ActorID                        string
	SourceEpoch                    string
	SourceRevision                 uint64
	ValidUntil                     time.Time
	PersonaRevision                string
	ConditionProfileRef            string
	AllowedNonCognitiveActivityIDs []string
	ExpiredFallbackActivityIDs     []string
}

// BuildLifePolicy validates and constructs the CORE-owned LifePolicy payload.
func BuildLifePolicy(input BuildLifePolicyInput) (domainprojection.LifePolicy, error) {
	if input.SourceRevision == 0 {
		return domainprojection.LifePolicy{}, errors.New("world projection: source revision is required")
	}
	return domainprojection.NewLifePolicy(
		input.WorldID,
		input.ActorID,
		input.SourceEpoch,
		input.SourceRevision,
		input.ValidUntil,
		input.PersonaRevision,
		input.ConditionProfileRef,
		input.AllowedNonCognitiveActivityIDs,
		input.ExpiredFallbackActivityIDs,
	)
}
