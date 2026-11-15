package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// AdmitAutonomousTask is the only Task-owner entry point for a self-generated
// Task. Its deterministic ID makes retries recover the exact prior candidate;
// it does not start the Task or create a Run.
func (m *Manager) AdmitAutonomousTask(ctx context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, error) {
	if ctx == nil {
		return domaintask.Task{}, errors.New("autonomous Task context is nil")
	}
	if m == nil || m.store == nil {
		return domaintask.Task{}, errors.New("task manager store is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, err
	}
	if draft.Autonomous == nil {
		return domaintask.Task{}, errors.New("autonomous Task metadata is required")
	}
	if shared.AutonomousDecision != nil || shared.PrivateText != nil {
		return domaintask.Task{}, errors.New("autonomous Task intake cannot accept owner-managed context")
	}
	if shared.TaskID != "" && shared.TaskID != draft.TaskID {
		return domaintask.Task{}, errors.New("autonomous context task_id must match Task")
	}
	draft.Status = valueOr(draft.Status, domaintask.StatusQueued)
	draft.Priority = valueOr(draft.Priority, domaintask.PriorityLow)
	draft.InterruptPolicy = valueOr(draft.InterruptPolicy, domaintask.InterruptSilent)
	if shared.TaskID == "" {
		shared.TaskID = draft.TaskID
	}
	moduleID, err := domaintask.AutonomousModuleID(draft.Assignee)
	if err != nil {
		return domaintask.Task{}, err
	}
	if draft.ModuleID == "" {
		draft.ModuleID = moduleID
	}
	if shared.ModuleID == "" {
		shared.ModuleID = draft.ModuleID
	}
	draft.ApplyDefaults(m.now())
	if err := draft.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("autonomous Task is invalid: %w", err)
	}
	if err := validateAutonomousCandidateShape(draft, shared); err != nil {
		return domaintask.Task{}, err
	}

	var admitted domaintask.Task
	err = m.taskTransaction(ctx, draft.TaskID, func(txManager *Manager) error {
		if err := txManager.validateAutonomousSource(ctx, draft, shared); err != nil {
			return err
		}
		if current, err := txManager.store.GetTask(ctx, draft.TaskID); err == nil {
			if err := txManager.validateExistingAutonomousCandidate(ctx, current, draft, shared); err != nil {
				return err
			}
			admitted = current
			return nil
		} else if !errors.Is(err, domaintask.ErrNotFound) {
			return fmt.Errorf("read autonomous Task candidate: %w", err)
		}
		created, err := txManager.createAutonomousInTransaction(ctx, draft, shared)
		if err != nil {
			return err
		}
		admitted = created
		return nil
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return admitted, nil
}

// SaveAutonomousDecision stores the immutable decision for the active plan
// Run. It only records intent; execution admission remains a separate call.
func (m *Manager) SaveAutonomousDecision(ctx context.Context, planTaskID modulecore.TaskID, runID modulecore.RunID, actorID string, decision domaintask.AutonomousDecision) error {
	if ctx == nil {
		return errors.New("autonomous decision context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := planTaskID.Validate(); err != nil {
		return fmt.Errorf("autonomous plan task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return fmt.Errorf("autonomous plan run_id is invalid: %w", err)
	}
	if !domaintask.AutonomousActor(actorID) {
		return errors.New("autonomous decision actor_id is invalid")
	}
	if err := decision.Validate(planTaskID); err != nil {
		return err
	}
	return m.taskTransaction(ctx, planTaskID, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(ctx, planTaskID, runID, actorID); err != nil {
			return err
		}
		if _, err := txManager.validateLatestRunInTransaction(ctx, planTaskID, runID); err != nil {
			return err
		}
		task, err := txManager.store.GetTask(ctx, planTaskID)
		if err != nil {
			return err
		}
		if task.Autonomous == nil || task.Autonomous.Kind != domaintask.AutonomousTaskKindPlan || task.Assignee != actorID {
			return fmt.Errorf("%w: decision does not belong to an autonomous plan owned by actor", ErrRunConflict)
		}
		shared, err := loadAutonomousSharedContext(ctx, txManager.store, planTaskID)
		if err != nil {
			return err
		}
		if shared.PrivateText == nil {
			return fmt.Errorf("%w: autonomous plan output checkpoint is absent", ErrRunConflict)
		}
		checkpoint := *shared.PrivateText
		if err := validatePrivateTextCheckpointOwner(task, checkpoint); err != nil || checkpoint.RunID != runID || checkpoint.ActorID != actorID || checkpoint.Output == "" {
			return fmt.Errorf("%w: autonomous decision requires the active Run's saved private output", ErrRunConflict)
		}
		if shared.AutonomousDecision != nil {
			if reflect.DeepEqual(*shared.AutonomousDecision, decision) {
				return nil
			}
			return fmt.Errorf("%w: autonomous plan decision is immutable", ErrRunConflict)
		}
		stored := decision
		shared.AutonomousDecision = &stored
		shared.UpdatedAt = txManager.now()
		return txManager.store.SaveContext(ctx, shared)
	})
}

func validateAutonomousCandidateShape(task domaintask.Task, shared domaintask.SharedRoleContext) error {
	if !domaintask.AutonomousActor(task.Assignee) || task.Route != domaintask.RouteCHAT || task.Priority != domaintask.PriorityLow ||
		!task.ReadOnly || task.InterruptPolicy != domaintask.InterruptSilent || task.Status != domaintask.StatusQueued {
		return errors.New("autonomous Task must be queued, low-priority, read-only private CHAT assigned to its owner")
	}
	if task.Autonomous == nil {
		return errors.New("autonomous Task metadata is required")
	}
	maxInputBytes := domaintask.AutonomousWorkInputMaxBytes
	if task.Autonomous.Kind == domaintask.AutonomousTaskKindPlan {
		maxInputBytes = domaintask.AutonomousPlanInputMaxBytes
	}
	if shared.ModuleRoot != "" || shared.ModuleID != task.ModuleID || strings.TrimSpace(shared.UserIntent) == "" || len(shared.UserIntent) > maxInputBytes ||
		strings.TrimSpace(shared.CurrentPlan) == "" || len(shared.CurrentPlan) > 4096 || len(shared.LatestStatus) > 2048 || len(shared.HandoffNotes) > 2048 {
		return errors.New("autonomous Task context is incomplete or exceeds its bounds")
	}
	for _, values := range [][]string{shared.RelevantFiles, shared.Decisions, shared.Constraints, shared.Artifacts} {
		if len(values) > 16 {
			return errors.New("autonomous Task context list exceeds its bound")
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 512 {
				return errors.New("autonomous Task context entry is empty or exceeds its bound")
			}
		}
	}
	return nil
}

func (m *Manager) validateExistingAutonomousCandidate(ctx context.Context, current, draft domaintask.Task, shared domaintask.SharedRoleContext) error {
	if err := current.Validate(); err != nil || current.Autonomous == nil {
		return fmt.Errorf("%w: existing autonomous candidate is invalid", ErrRunConflict)
	}
	if !reflect.DeepEqual(canonicalAutonomousTaskIdentity(current), canonicalAutonomousTaskIdentity(draft)) {
		return fmt.Errorf("%w: deterministic autonomous Task ID has different candidate identity", ErrRunConflict)
	}
	currentContext, err := loadAutonomousSharedContext(ctx, m.store, current.TaskID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(autonomousContextContent(currentContext), autonomousContextContent(shared)) {
		return fmt.Errorf("%w: deterministic autonomous Task ID has different candidate context", ErrRunConflict)
	}
	return nil
}

type autonomousTaskIdentityValue struct {
	TaskID     modulecore.TaskID
	Title      string
	ModuleID   string
	OwnerID    string
	Assignee   string
	Route      domaintask.Route
	Priority   domaintask.Priority
	Interrupt  domaintask.InterruptPolicy
	ReadOnly   bool
	Autonomous domaintask.AutonomousTask
}

func canonicalAutonomousTaskIdentity(task domaintask.Task) autonomousTaskIdentityValue {
	return autonomousTaskIdentityValue{
		TaskID: task.TaskID, Title: task.Title, ModuleID: task.ModuleID, OwnerID: task.OwnerID,
		Assignee: task.Assignee, Route: task.Route, Priority: task.Priority,
		Interrupt: task.InterruptPolicy, ReadOnly: task.ReadOnly, Autonomous: *task.Autonomous,
	}
}

type autonomousContextPayload struct {
	UserIntent    string
	ModuleID      string
	RelevantFiles []string
	Decisions     []string
	Constraints   []string
	CurrentPlan   string
	LatestStatus  string
	Artifacts     []string
	HandoffNotes  string
}

func autonomousContextContent(shared domaintask.SharedRoleContext) autonomousContextPayload {
	return autonomousContextPayload{
		UserIntent: shared.UserIntent, ModuleID: shared.ModuleID, RelevantFiles: append([]string(nil), shared.RelevantFiles...),
		Decisions: append([]string(nil), shared.Decisions...), Constraints: append([]string(nil), shared.Constraints...),
		CurrentPlan: shared.CurrentPlan, LatestStatus: shared.LatestStatus, Artifacts: append([]string(nil), shared.Artifacts...),
		HandoffNotes: shared.HandoffNotes,
	}
}

func preserveAutonomousContextContent(target *domaintask.SharedRoleContext, source domaintask.SharedRoleContext) {
	if target == nil {
		return
	}
	target.UserIntent = source.UserIntent
	target.ModuleID = source.ModuleID
	target.RelevantFiles = append([]string(nil), source.RelevantFiles...)
	target.Decisions = append([]string(nil), source.Decisions...)
	target.Constraints = append([]string(nil), source.Constraints...)
	target.CurrentPlan = source.CurrentPlan
	target.LatestStatus = source.LatestStatus
	target.Artifacts = append([]string(nil), source.Artifacts...)
	target.HandoffNotes = source.HandoffNotes
}

func (m *Manager) validateAutonomousSource(ctx context.Context, candidate domaintask.Task, shared domaintask.SharedRoleContext) error {
	autonomous := candidate.Autonomous
	if autonomous == nil {
		return errors.New("autonomous Task metadata is required")
	}
	if autonomous.SourceTaskID == "" {
		if autonomous.Kind != domaintask.AutonomousTaskKindPlan {
			return errors.New("autonomous source-less candidate must be a root plan")
		}
		return nil
	}
	source, err := m.store.GetTask(ctx, autonomous.SourceTaskID)
	if err != nil {
		return fmt.Errorf("read autonomous source Task: %w", err)
	}
	if err := source.Validate(); err != nil || source.Autonomous == nil || source.OwnerID != candidate.OwnerID || source.Assignee != candidate.Assignee ||
		source.Autonomous.RootTaskID != autonomous.RootTaskID || source.Status != domaintask.StatusSucceeded {
		return fmt.Errorf("%w: autonomous source Task is not a successful same-owner lineage item", ErrRunConflict)
	}
	run, err := m.store.GetRun(ctx, autonomous.SourceRunID)
	if err != nil {
		return fmt.Errorf("read autonomous source Run: %w", err)
	}
	if err := run.Validate(); err != nil || run.TaskID != source.TaskID || run.Assignee != candidate.Assignee || run.Status != domaintask.RunStatusSucceeded {
		return fmt.Errorf("%w: autonomous source Run is not a successful same-owner Run", ErrRunConflict)
	}
	latest, err := m.validateLatestRunInTransaction(ctx, source.TaskID, run.RunID)
	if err != nil || latest.Status != domaintask.RunStatusSucceeded {
		return fmt.Errorf("%w: autonomous source Run is not the latest successful Run", ErrRunConflict)
	}
	sourceContext, err := loadPrivateTextSharedContext(ctx, m.store, source.TaskID)
	if err != nil || sourceContext.PrivateText == nil {
		return fmt.Errorf("%w: autonomous source output checkpoint is unavailable", ErrRunConflict)
	}
	checkpoint := sourceContext.PrivateText
	if checkpoint.TaskID != source.TaskID || checkpoint.RunID != run.RunID || checkpoint.ActorID != candidate.Assignee || checkpoint.Output == "" || checkpoint.Validate() != nil {
		return fmt.Errorf("%w: autonomous source output checkpoint ownership is invalid", ErrRunConflict)
	}
	if source.Summary != checkpoint.Output {
		return fmt.Errorf("%w: autonomous source output does not match the succeeded Task result", ErrRunConflict)
	}
	outputDigest := sha256.Sum256([]byte(checkpoint.Output))
	outputHash := hex.EncodeToString(outputDigest[:])
	switch autonomous.Kind {
	case domaintask.AutonomousTaskKindPlan:
		if source.Autonomous.Kind != domaintask.AutonomousTaskKindWork || autonomous.Date != source.Autonomous.Date ||
			autonomous.Depth != source.Autonomous.Depth || autonomous.SourceOutputSHA256 != outputHash {
			return fmt.Errorf("%w: evaluation plan does not match its successful work source", ErrRunConflict)
		}
		if err := m.validateAutonomousWorkLineage(ctx, source); err != nil {
			return err
		}
	case domaintask.AutonomousTaskKindWork:
		if source.Autonomous.Kind != domaintask.AutonomousTaskKindPlan || autonomous.Date != source.Autonomous.Date ||
			autonomous.Depth != source.Autonomous.Depth+1 || autonomous.Depth > domaintask.AutonomousMaxDepth {
			return fmt.Errorf("%w: work candidate does not descend from its source plan", ErrRunConflict)
		}
		if sourceContext.AutonomousDecision == nil {
			return fmt.Errorf("%w: source plan has no saved autonomous decision", ErrRunConflict)
		}
		decision := sourceContext.AutonomousDecision
		if decision.Decision != domaintask.AutonomousDecisionContinue || decision.CandidateTaskID != candidate.TaskID ||
			decision.Title != candidate.Title || decision.Objective != shared.UserIntent || decision.Completion != shared.CurrentPlan || autonomous.SourceRunID != run.RunID {
			return fmt.Errorf("%w: work candidate differs from the source plan decision", ErrRunConflict)
		}
	default:
		return errors.New("autonomous candidate kind is invalid")
	}
	return nil
}

func (m *Manager) validateAutonomousWorkLineage(ctx context.Context, work domaintask.Task) error {
	autonomous := work.Autonomous
	if autonomous == nil || autonomous.Kind != domaintask.AutonomousTaskKindWork || autonomous.SourceTaskID == "" {
		return fmt.Errorf("%w: autonomous work lineage is incomplete", ErrRunConflict)
	}
	plan, err := m.store.GetTask(ctx, autonomous.SourceTaskID)
	if err != nil {
		return fmt.Errorf("read autonomous source plan: %w", err)
	}
	if plan.Validate() != nil || plan.Autonomous == nil || plan.Autonomous.Kind != domaintask.AutonomousTaskKindPlan ||
		plan.OwnerID != work.OwnerID || plan.Assignee != work.Assignee || plan.Autonomous.Date != autonomous.Date ||
		plan.Autonomous.RootTaskID != autonomous.RootTaskID || autonomous.Depth != plan.Autonomous.Depth+1 ||
		plan.Status != domaintask.StatusSucceeded {
		return fmt.Errorf("%w: autonomous work source plan identity, root, or depth does not match", ErrRunConflict)
	}
	planRun, err := m.store.GetRun(ctx, autonomous.SourceRunID)
	if err != nil || planRun.Validate() != nil || planRun.TaskID != plan.TaskID || planRun.Assignee != work.Assignee || planRun.Status != domaintask.RunStatusSucceeded {
		return fmt.Errorf("%w: autonomous work source plan Run is not successful", ErrRunConflict)
	}
	latestPlanRun, err := m.validateLatestRunInTransaction(ctx, plan.TaskID, planRun.RunID)
	if err != nil || latestPlanRun.Status != domaintask.RunStatusSucceeded {
		return fmt.Errorf("%w: autonomous work source plan Run is not the latest successful Run", ErrRunConflict)
	}
	shared, err := loadAutonomousSharedContext(ctx, m.store, plan.TaskID)
	if err != nil || shared.AutonomousDecision == nil {
		return fmt.Errorf("%w: autonomous work source plan decision is unavailable", ErrRunConflict)
	}
	decision := shared.AutonomousDecision
	if decision.Decision != domaintask.AutonomousDecisionContinue || decision.CandidateTaskID != work.TaskID ||
		decision.Title != work.Title {
		return fmt.Errorf("%w: autonomous work does not match its source plan decision", ErrRunConflict)
	}
	workContext, err := loadAutonomousSharedContext(ctx, m.store, work.TaskID)
	if err != nil || decision.Objective != workContext.UserIntent || decision.Completion != workContext.CurrentPlan {
		return fmt.Errorf("%w: autonomous work context differs from its source plan decision", ErrRunConflict)
	}
	return nil
}

func loadAutonomousSharedContext(ctx context.Context, store Store, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	shared, err := store.GetContext(ctx, taskID)
	if errors.Is(err, domaintask.ErrNotFound) {
		return domaintask.SharedRoleContext{TaskID: taskID}, nil
	}
	if err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	if shared.TaskID != taskID {
		return domaintask.SharedRoleContext{}, fmt.Errorf("%w: Task context identity does not match", ErrRunConflict)
	}
	return shared, nil
}

func cloneAutonomousDecision(value *domaintask.AutonomousDecision) *domaintask.AutonomousDecision {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func valueOr[T comparable](value, fallback T) T {
	var zero T
	if value != zero {
		return value
	}
	return fallback
}
