package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	agentprofileapp "github.com/Nyukimin/RenCrow_CORE/internal/application/agentprofile"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	autonomousTaskListLimit       = 128
	autonomousRecentWorkLimit     = 3
	autonomousRecentOutputMax     = 4 << 10
	autonomousPlanPromptMax       = 32 << 10
	autonomousEvaluationPromptMax = 128 << 10
	autonomousRunQueueIntent      = domainsuperagent.RunQueueIntentPrivateFirstChat
)

type autonomousTaskWaker interface {
	Wake(string) error
}

type autonomousTaskRuntime struct {
	tasks    *taskmanager.Manager
	queue    taskEnqueueRunQueueStore
	waker    autonomousTaskWaker
	ownerID  string
	routine  *agentRoutineRuntime
	profiles *agentprofileapp.Catalog
	now      func() time.Time
}

func newAutonomousTaskRuntime(cfg *config.Config, deps *Dependencies, routine *agentRoutineRuntime) *autonomousTaskRuntime {
	if cfg == nil || deps == nil || deps.taskManager == nil || deps.superAgentStateStore == nil || !cfg.Heartbeat.AgentSchedulerEnabled || !cfg.Heartbeat.Routine.Enabled || routine == nil {
		return nil
	}
	return &autonomousTaskRuntime{
		tasks: deps.taskManager, queue: deps.superAgentStateStore,
		ownerID: strings.TrimSpace(cfg.LocalAgentOps.UserID), routine: routine,
		profiles: agentprofileapp.NewStaticCatalog(), now: time.Now,
	}
}

// idleSource admits one deterministic daily root only after ordinary daytime
// work and the existing routine policy have had their chance to run.
func (r *autonomousTaskRuntime) idleSource(daytimeFallback agentheartbeat.Source) agentheartbeat.Source {
	if r == nil {
		return daytimeFallback
	}
	return func(ctx context.Context, actorID string) (agentheartbeat.Work, bool, error) {
		if err := ctx.Err(); err != nil {
			return agentheartbeat.Work{}, false, err
		}
		if r.routine.workers[actorID] == nil {
			return runDaytimeFallback(ctx, daytimeFallback, actorID)
		}
		if !r.autonomousWindow(ctx, actorID, r.now()) {
			return agentheartbeat.Work{}, false, nil
		}
		if !domaintask.AutonomousActor(actorID) {
			return runDaytimeFallback(ctx, daytimeFallback, actorID)
		}
		moduleID, err := domaintask.AutonomousModuleID(actorID)
		if err != nil {
			return agentheartbeat.Work{}, false, err
		}
		tasks, err := r.listActorTasks(ctx, actorID, moduleID)
		if err != nil {
			return agentheartbeat.Work{}, false, err
		}
		activeRoots, err := r.listActiveRoots(ctx, actorID, moduleID)
		if err != nil {
			return agentheartbeat.Work{}, false, err
		}
		if len(activeRoots) > 0 {
			rootID := activeRoots[0].TaskID
			settled, err := r.rootSettled(ctx, rootID)
			if err != nil {
				return agentheartbeat.Work{}, false, err
			}
			if !settled {
				needsRepair, err := r.rootNeedsRepair(ctx, rootID)
				if err != nil {
					return agentheartbeat.Work{}, false, err
				}
				if needsRepair {
					return r.reconciliationWork(actorID, rootID, r.now().In(r.location()))
				}
			}
			return runDaytimeFallback(ctx, daytimeFallback, actorID)
		}
		root, found, err := r.latestRoot(ctx, actorID, tasks)
		if err != nil {
			return agentheartbeat.Work{}, false, err
		}
		localNow := r.now().In(r.location())
		date := localNow.Format("2006-01-02")
		if found {
			settled, err := r.rootSettled(ctx, root.TaskID)
			if err != nil {
				return agentheartbeat.Work{}, false, err
			}
			if !settled {
				needsRepair, repairErr := r.rootNeedsRepair(ctx, root.TaskID)
				if repairErr != nil {
					return agentheartbeat.Work{}, false, repairErr
				}
				if needsRepair {
					return r.reconciliationWork(actorID, root.TaskID, localNow)
				}
				return runDaytimeFallback(ctx, daytimeFallback, actorID)
			}
			if root.Autonomous != nil && root.Autonomous.Date == date {
				return runDaytimeFallback(ctx, daytimeFallback, actorID)
			}
		}
		rootID, err := domaintask.AutonomousPlanTaskID(r.ownerID, actorID, date, "")
		if err != nil {
			return agentheartbeat.Work{}, false, err
		}
		return agentheartbeat.Work{
			ID:       "autonomous-seed:" + actorID + ":" + date,
			Priority: domaintask.PriorityLow, CreatedAt: localNow.UTC(),
			Run: func(runCtx context.Context, stop <-chan struct{}) error {
				if boardWorkStopRequested(stop) {
					return nil
				}
				return r.seedRoot(runCtx, actorID, date, rootID)
			},
		}, true, nil
	}
}

func (r *autonomousTaskRuntime) reconciliationWork(actorID string, rootID modulecore.TaskID, createdAt time.Time) (agentheartbeat.Work, bool, error) {
	return agentheartbeat.Work{
		ID:       "autonomous-reconcile:" + actorID + ":" + string(rootID),
		Priority: domaintask.PriorityLow, CreatedAt: createdAt.UTC(),
		Run: func(runCtx context.Context, stop <-chan struct{}) error {
			if boardWorkStopRequested(stop) {
				return nil
			}
			return r.reconcileRoot(runCtx, actorID, rootID)
		},
	}, true, nil
}

func (r *autonomousTaskRuntime) autonomousWindow(ctx context.Context, actorID string, now time.Time) bool {
	if r == nil || r.routine == nil || !domaintask.AutonomousActor(actorID) || r.routine.workers[actorID] == nil {
		return false
	}
	local := now.In(r.location())
	if local.Hour() < 9 || local.Hour() >= 22 {
		return false
	}
	state, err := r.routine.store.Load(ctx, actorID)
	if err != nil {
		return false
	}
	date := local.Format("2006-01-02")
	scheduleDate := date
	if local.Hour() >= 22 {
		scheduleDate = local.AddDate(0, 0, 1).Format("2006-01-02")
	}
	if state.ScheduleDate != scheduleDate {
		sleepAt, wakeAt, scheduleErr := agentroutine.ScheduleFor(actorID, scheduleDate)
		if scheduleErr != nil {
			return false
		}
		state.ScheduleDate, state.PlannedSleep, state.PlannedWake = scheduleDate, sleepAt, wakeAt
		state.LogicalSleepUntil = time.Time{}
	}
	if state.ActivatedOn == "" {
		state.ActivatedOn, state.NextDiaryDate = date, date
	}
	if state.Pending != nil || state.PausedDiary != nil || state.CooldownUntil.After(local) ||
		(state.RestWindow != nil && state.RestWindow.Until.After(local)) || state.LogicalSleepUntil.After(local) {
		return false
	}
	presence := agentroutine.Presence{SharedRoom: agentroutine.AvailabilityUnknown, OwnRoom: agentroutine.AvailabilityUnknown}
	selection := r.routine.selectRoutineActivity(actorID, state, local, presence)
	return !selection.Deferred && selection.DaytimeFallback
}

func runDaytimeFallback(ctx context.Context, fallback agentheartbeat.Source, actorID string) (agentheartbeat.Work, bool, error) {
	if fallback == nil {
		return agentheartbeat.Work{}, false, nil
	}
	return fallback(ctx, actorID)
}

func (r *autonomousTaskRuntime) location() *time.Location {
	if r != nil && r.routine != nil {
		return r.routine.location()
	}
	return time.FixedZone("Asia/Tokyo", 9*60*60)
}

func (r *autonomousTaskRuntime) seedRoot(ctx context.Context, actorID, date string, taskID modulecore.TaskID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if appsuperagent.RunQueueStopRequested(ctx) {
		return nil
	}
	if !r.autonomousWindow(ctx, actorID, r.now()) || r.now().In(r.location()).Format("2006-01-02") != date {
		return nil
	}
	profile, ok := r.profiles.Get(actorID)
	if !ok {
		return fmt.Errorf("autonomous actor profile %q is unavailable", actorID)
	}
	history, err := r.recentWorkHistory(ctx, actorID)
	if err != nil {
		return err
	}
	prompt := autonomousRootPrompt(profile, history)
	moduleID, err := domaintask.AutonomousModuleID(actorID)
	if err != nil {
		return err
	}
	root := domaintask.Task{
		TaskID: taskID, Title: "Autonomous plan " + date, OwnerID: r.ownerID, Assignee: actorID,
		ModuleID: moduleID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{
			PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindPlan,
			Date: date, RootTaskID: taskID, Depth: 0,
		},
	}
	created, err := r.tasks.AdmitAutonomousTask(ctx, root, domaintask.SharedRoleContext{
		TaskID: taskID, UserIntent: prompt, ModuleID: moduleID,
		CurrentPlan: "Return one strict JSON stop or continue decision. A continue decision must include a small title, objective, and observable completion condition.",
		Constraints: []string{"No external actions, tools, file writes, shell commands, network calls, paid APIs, messages, or state changes."},
	})
	if err != nil {
		return fmt.Errorf("admit autonomous daily plan: %w", err)
	}
	return r.ensureQueued(ctx, created)
}

func (r *autonomousTaskRuntime) listActorTasks(ctx context.Context, actorID, moduleID string) ([]domaintask.Task, error) {
	items, err := r.tasks.List(ctx, domaintask.Filter{ModuleID: moduleID, Assignee: actorID, Limit: autonomousTaskListLimit + 1})
	if err != nil {
		return nil, fmt.Errorf("list bounded autonomous Tasks for %s: %w", actorID, err)
	}
	if len(items) > autonomousTaskListLimit {
		items = items[:autonomousTaskListLimit]
	}
	owned := items[:0]
	for _, task := range items {
		if task.OwnerID == r.ownerID && task.Assignee == actorID {
			owned = append(owned, task)
		}
	}
	return owned, nil
}

func (r *autonomousTaskRuntime) listActiveRoots(ctx context.Context, actorID, moduleID string) ([]domaintask.Task, error) {
	rootsByID := make(map[modulecore.TaskID]domaintask.Task)
	for _, status := range []domaintask.Status{domaintask.StatusQueued, domaintask.StatusWaiting, domaintask.StatusRunning} {
		items, err := r.tasks.List(ctx, domaintask.Filter{ModuleID: moduleID, Assignee: actorID, Status: status, Limit: autonomousTaskListLimit + 1})
		if err != nil {
			return nil, fmt.Errorf("list active autonomous %s Tasks for %s: %w", status, actorID, err)
		}
		if len(items) > autonomousTaskListLimit {
			return nil, fmt.Errorf("active autonomous Task limit exceeded for actor %s", actorID)
		}
		for _, task := range items {
			if task.OwnerID != r.ownerID || task.Assignee != actorID {
				continue
			}
			if task.Autonomous == nil {
				return nil, fmt.Errorf("autonomous module returned an unmarked active Task %s", task.TaskID)
			}
			rootID := task.Autonomous.RootTaskID
			if _, found := rootsByID[rootID]; found {
				continue
			}
			root, err := r.tasks.Get(ctx, rootID)
			if err != nil {
				return nil, fmt.Errorf("read root for active autonomous Task %s: %w", task.TaskID, err)
			}
			if root.Autonomous == nil || root.TaskID != root.Autonomous.RootTaskID || root.Assignee != actorID || root.OwnerID != r.ownerID || root.OwnerID != task.OwnerID {
				return nil, fmt.Errorf("active autonomous Task %s has an invalid root lineage", task.TaskID)
			}
			rootsByID[rootID] = root
		}
	}
	roots := make([]domaintask.Task, 0, len(rootsByID))
	for _, root := range rootsByID {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Autonomous.Date != roots[j].Autonomous.Date {
			return roots[i].Autonomous.Date < roots[j].Autonomous.Date
		}
		return roots[i].TaskID < roots[j].TaskID
	})
	return roots, nil
}

func (r *autonomousTaskRuntime) latestRoot(ctx context.Context, actorID string, tasks []domaintask.Task) (domaintask.Task, bool, error) {
	latestDate := ""
	for _, task := range tasks {
		if task.Autonomous != nil && task.Autonomous.Date > latestDate {
			latestDate = task.Autonomous.Date
		}
	}
	if latestDate == "" {
		return domaintask.Task{}, false, nil
	}
	rootID, err := domaintask.AutonomousPlanTaskID(r.ownerID, actorID, latestDate, "")
	if err != nil {
		return domaintask.Task{}, false, err
	}
	root, err := r.tasks.Get(ctx, rootID)
	if errors.Is(err, domaintask.ErrNotFound) {
		return domaintask.Task{}, false, nil
	}
	if err != nil {
		return domaintask.Task{}, false, err
	}
	return root, true, nil
}

func (r *autonomousTaskRuntime) rootSettled(ctx context.Context, rootID modulecore.TaskID) (bool, error) {
	root, err := r.tasks.Get(ctx, rootID)
	if err != nil {
		return false, err
	}
	if root.OwnerID != r.ownerID || !domaintask.AutonomousActor(root.Assignee) {
		return false, errors.New("autonomous root is outside the configured owner and actor scope")
	}
	ownerID, actorID := root.OwnerID, root.Assignee
	for step := 0; step < 2*domaintask.AutonomousMaxDepth+1; step++ {
		if root.Autonomous == nil || root.OwnerID != ownerID || root.Assignee != actorID {
			return false, errors.New("autonomous lineage Task has no metadata")
		}
		if root.Status == domaintask.StatusQueued || root.Status == domaintask.StatusWaiting || root.Status == domaintask.StatusRunning {
			return false, nil
		}
		if root.Autonomous.Kind == domaintask.AutonomousTaskKindPlan {
			if root.Status != domaintask.StatusSucceeded {
				return true, nil
			}
			shared, err := r.tasks.Context(ctx, root.TaskID)
			if err != nil {
				return false, err
			}
			if shared.AutonomousDecision == nil || shared.AutonomousDecision.Decision == domaintask.AutonomousDecisionStop {
				return true, nil
			}
			workID, err := domaintask.AutonomousWorkTaskID(root.TaskID)
			if err != nil {
				return false, err
			}
			root, err = r.tasks.Get(ctx, workID)
			if errors.Is(err, domaintask.ErrNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			continue
		}
		if root.Status != domaintask.StatusSucceeded || root.Autonomous.Depth >= domaintask.AutonomousMaxDepth {
			return true, nil
		}
		planID, err := domaintask.AutonomousPlanTaskID(root.OwnerID, root.Assignee, root.Autonomous.Date, root.TaskID)
		if err != nil {
			return false, err
		}
		root, err = r.tasks.Get(ctx, planID)
		if errors.Is(err, domaintask.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

func (r *autonomousTaskRuntime) rootNeedsRepair(ctx context.Context, rootID modulecore.TaskID) (bool, error) {
	task, err := r.tasks.Get(ctx, rootID)
	if err != nil {
		return false, err
	}
	if task.OwnerID != r.ownerID || !domaintask.AutonomousActor(task.Assignee) {
		return false, errors.New("autonomous root is outside the configured owner and actor scope")
	}
	ownerID, actorID := task.OwnerID, task.Assignee
	for step := 0; step < 2*domaintask.AutonomousMaxDepth+1; step++ {
		if task.Autonomous == nil || task.OwnerID != ownerID || task.Assignee != actorID {
			return false, errors.New("autonomous lineage Task has no metadata")
		}
		switch task.Status {
		case domaintask.StatusQueued:
			return true, nil
		case domaintask.StatusWaiting, domaintask.StatusRunning:
			return false, nil // The private checkpoint/active generation owns this Task.
		case domaintask.StatusFailed, domaintask.StatusCancelled, domaintask.StatusBlocked:
			return false, nil
		case domaintask.StatusSucceeded:
		default:
			return false, fmt.Errorf("autonomous lineage Task %s has unsupported status %q", task.TaskID, task.Status)
		}
		if task.Autonomous.Kind == domaintask.AutonomousTaskKindPlan {
			shared, err := r.tasks.Context(ctx, task.TaskID)
			if err != nil {
				return false, err
			}
			if shared.AutonomousDecision == nil || shared.AutonomousDecision.Decision == domaintask.AutonomousDecisionStop {
				return false, nil
			}
			task, err = r.tasks.Get(ctx, shared.AutonomousDecision.CandidateTaskID)
			if errors.Is(err, domaintask.ErrNotFound) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			continue
		}
		if task.Autonomous.Depth >= domaintask.AutonomousMaxDepth {
			return false, nil
		}
		planID, err := domaintask.AutonomousPlanTaskID(task.OwnerID, task.Assignee, task.Autonomous.Date, task.TaskID)
		if err != nil {
			return false, err
		}
		task, err = r.tasks.Get(ctx, planID)
		if errors.Is(err, domaintask.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

func (r *autonomousTaskRuntime) reconcileRoot(ctx context.Context, actorID string, rootID modulecore.TaskID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.autonomousWindow(ctx, actorID, r.now()) {
		return nil
	}
	task, err := r.tasks.Get(ctx, rootID)
	if err != nil {
		return err
	}
	if task.OwnerID != r.ownerID || task.Assignee != actorID {
		return errors.New("autonomous recovery root is outside the configured owner and actor scope")
	}
	for step := 0; step < 2*domaintask.AutonomousMaxDepth+1; step++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if task.OwnerID != r.ownerID || task.Assignee != actorID || task.Autonomous == nil || task.Autonomous.RootTaskID != rootID {
			return errors.New("autonomous recovery Task does not match its root")
		}
		switch task.Status {
		case domaintask.StatusQueued:
			return r.ensureQueued(ctx, task)
		case domaintask.StatusWaiting, domaintask.StatusRunning, domaintask.StatusFailed, domaintask.StatusCancelled, domaintask.StatusBlocked:
			return nil
		case domaintask.StatusSucceeded:
		default:
			return fmt.Errorf("autonomous recovery Task %s has unsupported status %q", task.TaskID, task.Status)
		}
		if task.Autonomous.Kind == domaintask.AutonomousTaskKindPlan {
			shared, err := r.tasks.Context(ctx, task.TaskID)
			if err != nil {
				return err
			}
			if shared.AutonomousDecision == nil || shared.AutonomousDecision.Decision == domaintask.AutonomousDecisionStop {
				return nil
			}
			if err := r.ensureWorkFromPlan(ctx, task); err != nil {
				return err
			}
			workID, err := domaintask.AutonomousWorkTaskID(task.TaskID)
			if err != nil {
				return err
			}
			task, err = r.tasks.Get(ctx, workID)
			if err != nil {
				return err
			}
			continue
		}
		if task.Autonomous.Depth >= domaintask.AutonomousMaxDepth {
			return nil
		}
		if err := r.ensureEvaluationPlan(ctx, task); err != nil {
			return err
		}
		planID, err := domaintask.AutonomousPlanTaskID(task.OwnerID, task.Assignee, task.Autonomous.Date, task.TaskID)
		if err != nil {
			return err
		}
		task, err = r.tasks.Get(ctx, planID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *autonomousTaskRuntime) ensureWorkFromPlan(ctx context.Context, plan domaintask.Task) error {
	if plan.OwnerID != r.ownerID || !domaintask.AutonomousActor(plan.Assignee) || plan.Autonomous == nil || plan.Autonomous.Kind != domaintask.AutonomousTaskKindPlan || plan.Status != domaintask.StatusSucceeded {
		return errors.New("autonomous work recovery requires a succeeded plan")
	}
	if appsuperagent.RunQueueStopRequested(ctx) || !r.autonomousWindow(ctx, plan.Assignee, r.now()) {
		return nil
	}
	shared, err := r.tasks.Context(ctx, plan.TaskID)
	if err != nil {
		return err
	}
	if shared.AutonomousDecision == nil || shared.AutonomousDecision.Decision == domaintask.AutonomousDecisionStop {
		return nil
	}
	decision := shared.AutonomousDecision
	workID, err := domaintask.AutonomousWorkTaskID(plan.TaskID)
	if err != nil {
		return err
	}
	if decision.CandidateTaskID != workID {
		return errors.New("saved autonomous decision candidate does not match deterministic work Task")
	}
	if current, err := r.tasks.Get(ctx, workID); err == nil {
		if err := validateExistingAutonomousDescendant(current, plan, domaintask.AutonomousTaskKindWork); err != nil {
			return err
		}
		if current.Status == domaintask.StatusQueued {
			return r.ensureQueued(ctx, current)
		}
		return nil
	} else if !errors.Is(err, domaintask.ErrNotFound) {
		return err
	}
	moduleID, err := domaintask.AutonomousModuleID(plan.Assignee)
	if err != nil {
		return err
	}
	work := domaintask.Task{
		TaskID: workID, Title: decision.Title, OwnerID: plan.OwnerID, Assignee: plan.Assignee,
		ModuleID: moduleID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{
			PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindWork,
			Date: plan.Autonomous.Date, RootTaskID: plan.Autonomous.RootTaskID,
			SourceTaskID: plan.TaskID, SourceRunID: latestSucceededRunID(ctx, r.tasks, plan.TaskID),
			Depth: plan.Autonomous.Depth + 1,
		},
	}
	if work.Autonomous.SourceRunID == "" {
		return errors.New("succeeded autonomous plan has no exact successful Run")
	}
	created, err := r.tasks.AdmitAutonomousTask(ctx, work, domaintask.SharedRoleContext{
		TaskID: workID, UserIntent: decision.Objective, CurrentPlan: decision.Completion,
		ModuleID: moduleID, Constraints: []string{"No external actions, tools, file writes, shell commands, network calls, paid APIs, messages, or state changes."},
	})
	if err != nil {
		return fmt.Errorf("admit autonomous work candidate: %w", err)
	}
	return r.ensureQueued(ctx, created)
}

func (r *autonomousTaskRuntime) ensureEvaluationPlan(ctx context.Context, work domaintask.Task) error {
	if work.OwnerID != r.ownerID || !domaintask.AutonomousActor(work.Assignee) || work.Autonomous == nil || work.Autonomous.Kind != domaintask.AutonomousTaskKindWork || work.Status != domaintask.StatusSucceeded || work.Autonomous.Depth >= domaintask.AutonomousMaxDepth {
		return nil
	}
	if appsuperagent.RunQueueStopRequested(ctx) || !r.autonomousWindow(ctx, work.Assignee, r.now()) {
		return nil
	}
	runID := latestSucceededRunID(ctx, r.tasks, work.TaskID)
	if runID == "" {
		return errors.New("succeeded autonomous work has no exact successful Run")
	}
	shared, err := r.tasks.Context(ctx, work.TaskID)
	if err != nil {
		return err
	}
	output := strings.TrimSpace(work.Summary)
	if output == "" || !validPrivateTaskOutput(output) {
		return errors.New("succeeded autonomous work has no valid saved private output")
	}
	digest := sha256.Sum256([]byte(output))
	planID, err := domaintask.AutonomousPlanTaskID(work.OwnerID, work.Assignee, work.Autonomous.Date, work.TaskID)
	if err != nil {
		return err
	}
	if current, err := r.tasks.Get(ctx, planID); err == nil {
		if err := validateExistingAutonomousDescendant(current, work, domaintask.AutonomousTaskKindPlan); err != nil {
			return err
		}
		if current.Status == domaintask.StatusQueued {
			return r.ensureQueued(ctx, current)
		}
		return nil
	} else if !errors.Is(err, domaintask.ErrNotFound) {
		return err
	}
	moduleID, err := domaintask.AutonomousModuleID(work.Assignee)
	if err != nil {
		return err
	}
	profile, ok := r.profiles.Get(work.Assignee)
	if !ok {
		return fmt.Errorf("autonomous actor profile %q is unavailable", work.Assignee)
	}
	history, err := r.recentWorkHistory(ctx, work.Assignee)
	if err != nil {
		return err
	}
	prompt := autonomousEvaluationPrompt(profile, work, shared, output, history)
	if len(prompt) > autonomousEvaluationPromptMax {
		return errors.New("autonomous evaluation prompt exceeds the bounded input size")
	}
	plan := domaintask.Task{
		TaskID: planID, Title: "Evaluate autonomous result " + string(work.TaskID), OwnerID: work.OwnerID, Assignee: work.Assignee,
		ModuleID: moduleID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{
			PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindPlan,
			Date: work.Autonomous.Date, RootTaskID: work.Autonomous.RootTaskID,
			SourceTaskID: work.TaskID, SourceRunID: runID, SourceOutputSHA256: hex.EncodeToString(digest[:]),
			Depth: work.Autonomous.Depth,
		},
	}
	created, err := r.tasks.AdmitAutonomousTask(ctx, plan, domaintask.SharedRoleContext{
		TaskID: planID, UserIntent: prompt, ModuleID: moduleID,
		CurrentPlan: "Return one strict JSON stop or continue decision. A continue decision must include a small title, objective, and observable completion condition.",
		Constraints: []string{"No external actions, tools, file writes, shell commands, network calls, paid APIs, messages, or state changes."},
	})
	if err != nil {
		return fmt.Errorf("admit autonomous evaluation plan: %w", err)
	}
	return r.ensureQueued(ctx, created)
}

func (r *autonomousTaskRuntime) preparePlanDecision(ctx context.Context, task domaintask.Task, output string) (domaintask.AutonomousDecision, error) {
	if task.Autonomous == nil || task.Autonomous.Kind != domaintask.AutonomousTaskKindPlan {
		return domaintask.AutonomousDecision{}, nil
	}
	if err := ctx.Err(); err != nil || appsuperagent.RunQueueStopRequested(ctx) {
		if err != nil {
			return domaintask.AutonomousDecision{}, err
		}
		return domaintask.AutonomousDecision{}, context.Canceled
	}
	decision, err := decodeAutonomousDecision(output, task.TaskID)
	if err != nil {
		return domaintask.AutonomousDecision{}, autonomousInvalidDecisionError{cause: err}
	}
	if decision.Decision == domaintask.AutonomousDecisionContinue {
		if err := r.rejectAncestorDuplicate(ctx, task, decision.Title, decision.Objective); err != nil {
			if errors.Is(err, errAutonomousDuplicateCandidate) {
				decision = domaintask.AutonomousDecision{Decision: domaintask.AutonomousDecisionStop, Reason: "candidate duplicates a title and objective already present in this chain"}
			} else {
				return domaintask.AutonomousDecision{}, err
			}
		}
	}
	return decision, nil
}

func (r *autonomousTaskRuntime) savePlanDecision(ctx context.Context, task domaintask.Task, run domaintask.Run, actorID string, decision domaintask.AutonomousDecision) error {
	if task.Autonomous == nil || task.Autonomous.Kind != domaintask.AutonomousTaskKindPlan {
		return nil
	}
	return r.tasks.SaveAutonomousDecision(ctx, task.TaskID, run.RunID, actorID, decision)
}

func (r *autonomousTaskRuntime) afterPrivateTaskSuccess(ctx context.Context, task domaintask.Task) error {
	if r == nil || task.Autonomous == nil {
		return nil
	}
	if appsuperagent.RunQueueStopRequested(ctx) {
		return nil
	}
	current, err := r.tasks.Get(ctx, task.TaskID)
	if err != nil {
		return fmt.Errorf("reread completed autonomous Task: %w", err)
	}
	if current.OwnerID != r.ownerID || current.Assignee != task.Assignee || current.Status != domaintask.StatusSucceeded || current.Autonomous == nil || *current.Autonomous != *task.Autonomous {
		return errors.New("autonomous successor callback requires the exact canonical succeeded Task")
	}
	if !r.autonomousWindow(ctx, task.Assignee, r.now()) {
		return nil
	}
	switch current.Autonomous.Kind {
	case domaintask.AutonomousTaskKindPlan:
		err = r.ensureWorkFromPlan(ctx, current)
	case domaintask.AutonomousTaskKindWork:
		err = r.ensureEvaluationPlan(ctx, current)
	}
	if err != nil {
		return fmt.Errorf("admit autonomous successor: %w", err)
	}
	return nil
}

func (r *autonomousTaskRuntime) ensureQueued(ctx context.Context, task domaintask.Task) error {
	if r == nil || r.queue == nil || r.waker == nil {
		return errors.New("autonomous private queue is unavailable")
	}
	moduleID, err := domaintask.AutonomousModuleID(task.Assignee)
	if err != nil {
		return err
	}
	if task.OwnerID != r.ownerID || task.ModuleID != moduleID || task.Autonomous == nil || task.Status != domaintask.StatusQueued || task.Route != domaintask.RouteCHAT || task.Priority != domaintask.PriorityLow || !task.ReadOnly {
		return errors.New("autonomous Task is outside the queued low-priority read-only CHAT contract")
	}
	existing, found, err := r.queue.FindRunQueueItemByTaskIntent(ctx, task.TaskID, autonomousRunQueueIntent)
	if err != nil {
		return fmt.Errorf("find autonomous private queue intent: %w", err)
	}
	if found {
		if existing.TaskID != task.TaskID || existing.IdempotencyKey != autonomousRunQueueIntent || existing.Action != "chat" || existing.Goal != domainsuperagent.RunQueueGoalPrivateFirstChat {
			return errors.New("existing autonomous queue intent does not match exact private CHAT semantics")
		}
	} else {
		item := domainsuperagent.RunQueueItem{
			QueueItemID: modulecore.NewQueueItemID(), TaskID: task.TaskID,
			RunStartReason: domaintask.RunStartReasonFirst,
			WorkstreamID:   string(task.WorkstreamID), Goal: domainsuperagent.RunQueueGoalPrivateFirstChat,
			Action: "chat", Status: "queued", Priority: taskEnqueuePriority(task.Priority),
			IdempotencyKey: autonomousRunQueueIntent, CreatedAt: task.CreatedAt.UTC(),
		}
		if _, _, err := r.queue.InsertRunQueueItemIfAbsent(ctx, item); err != nil {
			return fmt.Errorf("insert autonomous private queue intent: %w", err)
		}
	}
	if err := r.waker.Wake(task.Assignee); err != nil {
		return fmt.Errorf("wake autonomous private task actor: %w", err)
	}
	return nil
}

func (r *autonomousTaskRuntime) setWaker(waker autonomousTaskWaker) {
	if r != nil {
		r.waker = waker
	}
}

func autonomousProviderTimeout(now time.Time, location *time.Location) time.Duration {
	local := now.In(location)
	end := time.Date(local.Year(), local.Month(), local.Day(), 22, 0, 0, 0, location)
	untilWindowEnd := end.Sub(local)
	if untilWindowEnd <= 0 {
		return time.Nanosecond
	}
	if untilWindowEnd < 2*time.Minute {
		return untilWindowEnd
	}
	return 2 * time.Minute
}

func decodeAutonomousDecision(output string, planID modulecore.TaskID) (domaintask.AutonomousDecision, error) {
	if !utf8.ValidString(output) || len(output) == 0 || len(output) > conversation.MaxReportResultBytes {
		return domaintask.AutonomousDecision{}, errors.New("autonomous plan output is empty, invalid UTF-8, or too large")
	}
	var wire struct {
		Decision   string `json:"decision"`
		Reason     string `json:"reason"`
		Title      string `json:"title,omitempty"`
		Objective  string `json:"objective,omitempty"`
		Completion string `json:"completion,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(output)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return domaintask.AutonomousDecision{}, fmt.Errorf("decode autonomous plan decision: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return domaintask.AutonomousDecision{}, errors.New("autonomous plan output contains multiple JSON values")
		}
		return domaintask.AutonomousDecision{}, fmt.Errorf("decode trailing autonomous plan output: %w", err)
	}
	decision := domaintask.AutonomousDecision{
		Decision: domaintask.AutonomousDecisionKind(wire.Decision), Reason: strings.TrimSpace(wire.Reason),
		Title: strings.TrimSpace(wire.Title), Objective: strings.TrimSpace(wire.Objective), Completion: strings.TrimSpace(wire.Completion),
	}
	if decision.Decision == domaintask.AutonomousDecisionContinue {
		candidateID, err := domaintask.AutonomousWorkTaskID(planID)
		if err != nil {
			return domaintask.AutonomousDecision{}, err
		}
		decision.CandidateTaskID = candidateID
	}
	if err := decision.Validate(planID); err != nil {
		return domaintask.AutonomousDecision{}, err
	}
	return decision, nil
}

func (r *autonomousTaskRuntime) rejectAncestorDuplicate(ctx context.Context, plan domaintask.Task, title, objective string) error {
	title = normalizeAutonomousCandidate(title)
	objective = normalizeAutonomousCandidate(objective)
	if title == "" || objective == "" {
		return errors.New("autonomous candidate title and objective are required")
	}
	current := plan
	for range 2 * domaintask.AutonomousMaxDepth {
		if current.Autonomous == nil || current.Autonomous.SourceTaskID == "" {
			return nil
		}
		source, err := r.tasks.Get(ctx, current.Autonomous.SourceTaskID)
		if err != nil {
			return err
		}
		if source.Autonomous != nil && source.Autonomous.Kind == domaintask.AutonomousTaskKindWork {
			shared, err := r.tasks.Context(ctx, source.TaskID)
			if err != nil {
				return err
			}
			if normalizeAutonomousCandidate(source.Title) == title && normalizeAutonomousCandidate(shared.UserIntent) == objective {
				return errAutonomousDuplicateCandidate
			}
			if source.Autonomous.SourceTaskID == "" {
				return nil
			}
			current, err = r.tasks.Get(ctx, source.Autonomous.SourceTaskID)
			if err != nil {
				return err
			}
		} else {
			return nil
		}
	}
	return nil
}

var errAutonomousDuplicateCandidate = errors.New("autonomous candidate duplicates a title and objective already present in this chain")

type autonomousInvalidDecisionError struct{ cause error }

func (e autonomousInvalidDecisionError) Error() string { return e.cause.Error() }
func (e autonomousInvalidDecisionError) Unwrap() error { return e.cause }

func validateExistingAutonomousDescendant(task, source domaintask.Task, kind domaintask.AutonomousTaskKind) error {
	if task.Autonomous == nil || task.OwnerID != source.OwnerID || task.Assignee != source.Assignee ||
		task.Autonomous.RootTaskID != source.Autonomous.RootTaskID || task.Autonomous.SourceTaskID != source.TaskID ||
		task.Autonomous.Kind != kind || task.Autonomous.Date != source.Autonomous.Date {
		return errors.New("deterministic autonomous successor does not match its exact source lineage")
	}
	if err := task.Validate(); err != nil {
		return fmt.Errorf("existing autonomous successor is invalid: %w", err)
	}
	return nil
}

func normalizeAutonomousCandidate(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

type autonomousHistoryItem struct {
	Title  string `json:"title"`
	Output string `json:"output"`
}

func (r *autonomousTaskRuntime) recentWorkHistory(ctx context.Context, actorID string) ([]autonomousHistoryItem, error) {
	moduleID, err := domaintask.AutonomousModuleID(actorID)
	if err != nil {
		return nil, err
	}
	tasks, err := r.listActorTasks(ctx, actorID, moduleID)
	if err != nil {
		return nil, err
	}
	items := make([]autonomousHistoryItem, 0, autonomousRecentWorkLimit)
	for _, task := range tasks {
		if task.Autonomous == nil || task.Autonomous.Kind != domaintask.AutonomousTaskKindWork || task.Status != domaintask.StatusSucceeded {
			continue
		}
		output := truncateUTF8(task.Summary, autonomousRecentOutputMax)
		if strings.TrimSpace(output) == "" {
			continue
		}
		items = append(items, autonomousHistoryItem{Title: task.Title, Output: output})
		if len(items) == autonomousRecentWorkLimit {
			break
		}
	}
	return items, nil
}

func autonomousRootPrompt(profile agentprofile.Profile, history []autonomousHistoryItem) string {
	return boundedAutonomousPrompt(autonomousPlanPromptMax, func(buffer *strings.Builder) {
		buffer.WriteString("Decide whether to create one small autonomous task for this resident Agent, using only a private CHAT text response. Return exactly one JSON object with fields decision, reason, title, objective, completion. decision must be stop or continue. For stop, include only decision and reason. For continue, title, objective, and completion must be concise and specific. Do not include candidate_task_id.\n")
		writeAutonomousProfile(buffer, profile)
		writeAutonomousSafety(buffer)
		writeAutonomousHistory(buffer, history)
	})
}

func autonomousEvaluationPrompt(profile agentprofile.Profile, work domaintask.Task, shared domaintask.SharedRoleContext, output string, history []autonomousHistoryItem) string {
	return boundedAutonomousPrompt(autonomousEvaluationPromptMax, func(buffer *strings.Builder) {
		buffer.WriteString("Evaluate this saved autonomous task result. Decide whether to stop or propose one small next task, bounded to the same private CHAT text-only contract. Return exactly one JSON object with fields decision, reason, title, objective, completion. decision must be stop or continue. For stop, include only decision and reason. For continue, title, objective, and completion must be concise and specific. Do not include candidate_task_id.\n")
		writeAutonomousProfile(buffer, profile)
		fmt.Fprintf(buffer, "\nPrevious task: %s\nObjective: %s\nCompletion condition: %s\nSaved result:\n%s\n", work.Title, shared.UserIntent, shared.CurrentPlan, truncateUTF8(output, autonomousEvaluationPromptMax/2))
		writeAutonomousSafety(buffer)
		writeAutonomousHistory(buffer, history)
	})
}

func boundedAutonomousPrompt(max int, write func(*strings.Builder)) string {
	var builder strings.Builder
	write(&builder)
	result := builder.String()
	if len(result) <= max {
		return result
	}
	return truncateUTF8(result, max)
}

func writeAutonomousProfile(buffer *strings.Builder, profile agentprofile.Profile) {
	fmt.Fprintf(buffer, "Resident role: %s\n", profile.Role)
	if len(profile.Goals) > 0 {
		buffer.WriteString("Resident goals:\n")
		for i, goal := range profile.Goals {
			if i == 4 {
				break
			}
			fmt.Fprintf(buffer, "- %s\n", goal.Description)
		}
	}
	if len(profile.KnowledgeAffinity) > 0 {
		buffer.WriteString("Resident interests and knowledge affinities:\n")
		for i, interest := range profile.KnowledgeAffinity {
			if i == 4 {
				break
			}
			fmt.Fprintf(buffer, "- %s\n", interest.Topic)
		}
	}
}

func writeAutonomousSafety(buffer *strings.Builder) {
	buffer.WriteString("Authority is restricted to preparing a private CHAT text result. Do not ask for or propose file edits, shell commands, network access, purchases, paid services, external messages, publishing, or changes to state. If no useful small text-only task is grounded in the role, interests, or recent saved results, decide stop.\n")
}

func writeAutonomousHistory(buffer *strings.Builder, history []autonomousHistoryItem) {
	if len(history) == 0 {
		return
	}
	buffer.WriteString("Recent completed private tasks by this same Agent:\n")
	for _, item := range history {
		fmt.Fprintf(buffer, "- %s\n", item.Title)
		fmt.Fprintf(buffer, "  Saved result: %s\n", item.Output)
	}
}

func truncateUTF8(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func latestSucceededRunID(ctx context.Context, tasks *taskmanager.Manager, taskID modulecore.TaskID) modulecore.RunID {
	runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID, Limit: 16})
	if err != nil {
		return ""
	}
	for _, run := range runs {
		if run.TaskID == taskID && run.Status == domaintask.RunStatusSucceeded {
			return run.RunID
		}
	}
	return ""
}
