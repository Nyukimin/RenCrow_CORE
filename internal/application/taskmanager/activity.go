package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type externalGameActivityReceipt struct {
	TaskID modulecore.TaskID `json:"task_id"`
}

type externalGameActivityRequest struct {
	OwnerID      string                           `json:"owner_id"`
	GameID       string                           `json:"game_id"`
	SessionID    string                           `json:"session_id"`
	Phase        string                           `json:"phase"`
	Participants []domaintask.ActivityParticipant `json:"participants"`
}

type externalGameActivityOperationStore interface {
	ExecuteIdempotentGlobalTaskOperation(context.Context, string, string, func(Store) (json.RawMessage, error)) (json.RawMessage, error)
}

// SetCurrentActivity atomically makes taskID current for every participant in
// activity and clears their prior current Task assignments in the same store
// transaction. Other participants already assigned to the target Task remain.
func (m *Manager) SetCurrentActivity(ctx context.Context, taskID modulecore.TaskID, activity domaintask.ActivityMetadata) (domaintask.Task, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, errors.New("current activity context is nil")
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("current activity task_id is invalid: %w", err)
	}
	var updated domaintask.Task
	err := m.activityTransaction(ctx, []modulecore.TaskID{taskID}, func(tx Store) error {
		if tx == nil {
			return errors.New("current activity transaction store is unavailable")
		}
		txManager := m.withStore(tx)
		current, err := readCurrentActivityTasks(ctx, tx, activityActorIDs(activity))
		if err != nil {
			return err
		}
		for actorID, prior := range current {
			if prior.TaskID != taskID && prior.Activity != nil && prior.Activity.Passive {
				return fmt.Errorf("actor %s must explicitly complete passive Task %s before handoff: %w", actorID, prior.TaskID, domaintask.ErrCurrentActivityConflict)
			}
		}
		updated, err = txManager.setCurrentActivityInTransaction(ctx, taskID, activity)
		return err
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return updated, nil
}

// SetCurrentActivityInTransaction is the activity-only hook for typed Task
// owners. Call it from that owner's already-authorized transaction after its
// identity and admission checks; it changes no status, Run, context, or effect.
func (m *Manager) SetCurrentActivityInTransaction(ctx context.Context, taskID modulecore.TaskID, activity domaintask.ActivityMetadata) (domaintask.Task, error) {
	return m.setCurrentActivityInTransaction(ctx, taskID, activity)
}

// PrepareTerminalActivityTransition clears current markers and creates a
// minimal evaluating Task for the current participants. Typed/private owners
// call this only after their own authorization checks and before their
// existing terminal Task/Run writes in the same transaction.
func (m *Manager) PrepareTerminalActivityTransition(ctx context.Context, task domaintask.Task) (domaintask.Task, error) {
	return m.transitionTerminalCurrentActivity(ctx, task)
}

// CreateAndStartActivityRun creates an active Task, starts its first Run, and
// assigns its participants in one owner transaction. Generic Task creation
// deliberately cannot set Activity metadata directly.
func (m *Manager) CreateAndStartActivityRun(ctx context.Context, draft domaintask.Task, activity domaintask.ActivityMetadata, shared domaintask.SharedRoleContext) (domaintask.Task, domaintask.Run, error) {
	if m == nil || m.store == nil || ctx == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("activity Task creation is unavailable")
	}
	if draft.Activity != nil || activity.Passive || draft.Autonomous != nil || shared.AutonomousDecision != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrActivityTaskRequiresOwner
	}
	if draft.TaskID == "" {
		draft.TaskID = modulecore.NewTaskID()
	}
	priorTasks, err := m.currentTasksForActors(ctx, activityActorIDs(activity))
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	transactionIDs := []modulecore.TaskID{draft.TaskID}
	for _, prior := range priorTasks {
		transactionIDs = append(transactionIDs, prior.TaskID)
	}
	transactionIDs = uniqueActivityTaskIDs(transactionIDs)
	var started domaintask.Task
	var run domaintask.Run
	err = m.activityTransaction(ctx, transactionIDs, func(store Store) error {
		txManager := m.withStore(store)
		current, err := readCurrentActivityTasks(ctx, store, activityActorIDs(activity))
		if err != nil {
			return err
		}
		if !sameActivityTaskAssignments(priorTasks, current) {
			return domaintask.ErrCurrentActivityConflict
		}
		started, run, err = txManager.createAndStartRunInTransaction(ctx, draft, shared)
		if err != nil {
			return err
		}
		started, err = txManager.setCurrentActivityInTransaction(ctx, started.TaskID, activity)
		if err != nil {
			return err
		}
		for _, prior := range priorTasks {
			if prior.Activity == nil || !prior.Activity.Passive || prior.Activity.Kind == domaintask.ActivityKindGame || prior.TaskID == started.TaskID {
				continue
			}
			updatedPrior, getErr := store.(domaintask.ActivityTaskWriter).GetTaskActivity(ctx, prior.TaskID)
			if getErr != nil {
				return getErr
			}
			if activityHasCurrentParticipant(updatedPrior) {
				continue
			}
			summary := "passive activity interrupted by a new active Task"
			if prior.Activity.Kind == domaintask.ActivityKindGame {
				summary = "all participants left the passive game activity"
			}
			if _, err := txManager.updateStatusInTransaction(ctx, prior.TaskID, domaintask.StatusCancelled, summary, "", nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	return started, run, nil
}

// EnsureExternalGameActivity adopts an observer-confirmed legacy game session
// as a real passive CORE Task. It never launches a game or creates a Run. The
// operation identity is stable for owner/game/session and its request hash
// binds the observed phase and participant set.
func (m *Manager) EnsureExternalGameActivity(ctx context.Context, ownerID, gameID, sessionID, phase string, participants []domaintask.ActivityParticipant) (domaintask.Task, error) {
	if m == nil || m.store == nil || ctx == nil || !validActivityOwnerID(ownerID) {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	participants = append([]domaintask.ActivityParticipant(nil), participants...)
	for index := range participants {
		participants[index].IsCurrent = false
		participants[index].Revision = 1
	}
	sort.Slice(participants, func(i, j int) bool { return participants[i].ActorID < participants[j].ActorID })
	if strings.TrimSpace(phase) == "" {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	request := externalGameActivityRequest{OwnerID: ownerID, GameID: gameID, SessionID: sessionID, Phase: strings.TrimSpace(phase), Participants: participants}
	activity := domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindGame, Label: "Playing " + gameID, Phase: request.Phase,
		Passive: true, Revision: 1, Game: &domaintask.ActivityGameReference{GameID: gameID, SessionID: sessionID},
		Participants: participants,
	}
	if err := activity.Validate(domaintask.StatusRunning); err != nil {
		return domaintask.Task{}, err
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return domaintask.Task{}, err
	}
	identity := sha256.Sum256([]byte(ownerID + "\x00" + gameID + "\x00" + sessionID))
	requestDigest := sha256.Sum256(requestBytes)
	operationID := "external-game-activity:" + hex.EncodeToString(identity[:])
	requestHash := hex.EncodeToString(requestDigest[:])
	operationStore, ok := m.store.(externalGameActivityOperationStore)
	if !ok {
		return domaintask.Task{}, ErrGlobalTaskOperationUnsupported
	}
	encoded, err := operationStore.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, func(tx Store) (json.RawMessage, error) {
		if tx == nil {
			return nil, errors.New("external game Task transaction is unavailable")
		}
		txManager := m.withStore(tx)
		now := m.now()
		started := now
		task := domaintask.Task{
			TaskID: modulecore.NewTaskID(), Title: "Playing " + gameID, Assignee: participants[0].ActorID,
			Route: domaintask.RouteGeneral, Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal,
			InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true, CreatedAt: now, UpdatedAt: now, StartedAt: &started,
			Activity: cloneExternalGameMetadata(activity),
		}
		writer, ok := tx.(domaintask.ActivityTaskWriter)
		if !ok {
			return nil, domaintask.ErrActivityTaskWriterUnsupported
		}
		if err := writer.SaveNewPassiveActivityTask(ctx, task); err != nil {
			return nil, err
		}
		current, err := readCurrentActivityTasks(ctx, tx, activityActorIDs(activity))
		if err != nil {
			return nil, err
		}
		eligible := make([]domaintask.ActivityParticipant, 0, len(participants))
		passivePrior := make(map[modulecore.TaskID]domaintask.Task)
		for _, participant := range participants {
			prior, occupied := current[participant.ActorID]
			if !occupied {
				eligible = append(eligible, domaintask.ActivityParticipant{ActorID: participant.ActorID, Role: participant.Role})
			} else if prior.Activity != nil && prior.Activity.Passive && prior.Activity.Kind != domaintask.ActivityKindGame {
				eligible = append(eligible, domaintask.ActivityParticipant{ActorID: participant.ActorID, Role: participant.Role})
				passivePrior[prior.TaskID] = prior
			}
		}
		if len(eligible) > 0 {
			activity.Participants = eligible
			if _, err := txManager.setCurrentActivityInTransaction(ctx, task.TaskID, activity); err != nil {
				return nil, err
			}
		}
		for priorID, prior := range passivePrior {
			updatedPrior, err := tx.GetTask(ctx, priorID)
			if err != nil {
				return nil, err
			}
			if activityHasCurrentParticipant(updatedPrior) {
				continue
			}
			summary := "passive activity superseded by an observed game session"
			if prior.Activity.Kind == domaintask.ActivityKindEvaluating {
				summary = "activity evaluation superseded by an observed game session"
			}
			if _, err := txManager.updateStatusInTransaction(ctx, priorID, domaintask.StatusCancelled, summary, "", nil); err != nil {
				return nil, err
			}
		}
		return json.Marshal(externalGameActivityReceipt{TaskID: task.TaskID})
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	var receipt externalGameActivityReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil || receipt.TaskID.Validate() != nil {
		return domaintask.Task{}, errors.New("external game Task receipt is invalid")
	}
	task, err := m.Get(ctx, receipt.TaskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	if task.Activity == nil || task.Activity.Kind != domaintask.ActivityKindGame || task.Activity.Game == nil ||
		task.Activity.Game.GameID != gameID || task.Activity.Game.SessionID != sessionID {
		return domaintask.Task{}, domaintask.ErrCurrentActivityConflict
	}
	return task, nil
}

func cloneExternalGameMetadata(activity domaintask.ActivityMetadata) *domaintask.ActivityMetadata {
	activity.Participants = append([]domaintask.ActivityParticipant(nil), activity.Participants...)
	if activity.Game != nil {
		game := *activity.Game
		activity.Game = &game
	}
	return &activity
}

func activityForRootTask(task domaintask.Task) (domaintask.ActivityMetadata, bool) {
	fallbackKind := domaintask.ActivityKindWork
	if task.Route == domaintask.RouteCHAT {
		fallbackKind = domaintask.ActivityKindChat
	}
	return activityForStartedTask(task, fallbackKind)
}

func activityForStartedTask(task domaintask.Task, fallbackKind domaintask.ActivityKind) (domaintask.ActivityMetadata, bool) {
	if task.Route == domaintask.RouteGAME || !canonicalActivityActor(task.Assignee) || strings.TrimSpace(task.Title) == "" {
		return domaintask.ActivityMetadata{}, false
	}
	if task.Activity != nil {
		if task.Activity.Passive {
			return domaintask.ActivityMetadata{}, false
		}
		activity := *task.Activity
		activity.Participants = append([]domaintask.ActivityParticipant(nil), task.Activity.Participants...)
		if task.Activity.Game != nil {
			game := *task.Activity.Game
			activity.Game = &game
		}
		foundAssignee := false
		for i := range activity.Participants {
			activity.Participants[i].IsCurrent = false
			activity.Participants[i].Revision = 0
			foundAssignee = foundAssignee || activity.Participants[i].ActorID == task.Assignee
		}
		if !foundAssignee {
			activity.Participants = append(activity.Participants, domaintask.ActivityParticipant{ActorID: task.Assignee})
		}
		return activity, true
	}
	label := "Working"
	if fallbackKind == domaintask.ActivityKindChat {
		label = "Chatting"
	} else if fallbackKind != domaintask.ActivityKindWork {
		return domaintask.ActivityMetadata{}, false
	}
	return domaintask.ActivityMetadata{
		Kind: fallbackKind, Label: label, Phase: "active",
		Participants: []domaintask.ActivityParticipant{{ActorID: task.Assignee}},
	}, true
}

func (m *Manager) childSharesCurrentAncestor(ctx context.Context, task domaintask.Task) (bool, error) {
	if task.ParentTaskID == "" {
		return false, nil
	}
	current, err := m.currentTasksForActors(ctx, []string{task.Assignee})
	if err != nil {
		return false, err
	}
	currentTask, found := current[task.Assignee]
	if !found {
		return false, nil
	}
	ancestorID := task.ParentTaskID
	seen := make(map[modulecore.TaskID]struct{})
	for ancestorID != "" {
		if ancestorID == currentTask.TaskID {
			return true, nil
		}
		if _, duplicate := seen[ancestorID]; duplicate {
			return false, domaintask.ErrCurrentActivityInvalid
		}
		seen[ancestorID] = struct{}{}
		ancestor, err := m.store.GetTask(ctx, ancestorID)
		if err != nil {
			return false, err
		}
		ancestorID = ancestor.ParentTaskID
	}
	return false, nil
}

func (m *Manager) requireDormantActivityFeature(ctx context.Context, actorID string, task domaintask.Task) error {
	if task.Activity != nil {
		return domaintask.ErrActivityTaskWriterUnsupported
	}
	current, err := m.currentTasksForActors(ctx, []string{actorID})
	if err != nil {
		return err
	}
	if active, found := current[actorID]; found {
		return fmt.Errorf("actor %s already has canonical current Task %s but store lacks activity transaction support: %w", actorID, active.TaskID, domaintask.ErrActivityTaskWriterUnsupported)
	}
	return nil
}

func (m *Manager) startExistingActivityTask(ctx context.Context, taskID modulecore.TaskID, activity domaintask.ActivityMetadata, start func(*Manager) (domaintask.Task, domaintask.Run, error)) (domaintask.Task, domaintask.Run, error) {
	if start == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("activity start callback is required")
	}
	return m.startExistingActivityMutation(ctx, taskID, activity, func(txManager *Manager) (domaintask.Task, domaintask.Run, bool, error) {
		task, run, err := start(txManager)
		return task, run, true, err
	})
}

func (m *Manager) startExistingActivityMutation(
	ctx context.Context,
	taskID modulecore.TaskID,
	activity domaintask.ActivityMetadata,
	mutate func(*Manager) (domaintask.Task, domaintask.Run, bool, error),
) (domaintask.Task, domaintask.Run, error) {
	if mutate == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("activity mutation callback is required")
	}
	if _, supported := m.store.(domaintask.ActivityTaskTransaction); !supported {
		var started domaintask.Task
		var run domaintask.Run
		err := m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
			var shouldBind bool
			var err error
			started, run, shouldBind, err = mutate(txManager)
			if err != nil || !shouldBind {
				return err
			}
			if started.Activity != nil {
				return domaintask.ErrActivityTaskWriterUnsupported
			}
			current, err := txManager.currentTasksForActors(ctx, activityActorIDs(activity))
			if err != nil {
				return err
			}
			if len(current) != 0 {
				return domaintask.ErrActivityTaskWriterUnsupported
			}
			return nil
		})
		return started, run, err
	}
	priorTasks, err := m.currentTasksForActors(ctx, activityActorIDs(activity))
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	transactionIDs := []modulecore.TaskID{taskID}
	for _, prior := range priorTasks {
		transactionIDs = append(transactionIDs, prior.TaskID)
	}
	transactionIDs = uniqueActivityTaskIDs(transactionIDs)
	var started domaintask.Task
	var run domaintask.Run
	err = m.activityTransaction(ctx, transactionIDs, func(store Store) error {
		current, err := readCurrentActivityTasks(ctx, store, activityActorIDs(activity))
		if err != nil {
			return err
		}
		if !sameActivityTaskAssignments(priorTasks, current) {
			return domaintask.ErrCurrentActivityConflict
		}
		txManager := m.withStore(store)
		var shouldBind bool
		started, run, shouldBind, err = mutate(txManager)
		if err != nil {
			return err
		}
		if !shouldBind {
			return nil
		}
		currentActivity, eligible := activityForStartedTask(started, activity.Kind)
		if !eligible || !sameActivityIntent(currentActivity, activity) {
			return domaintask.ErrCurrentActivityConflict
		}
		started, err = txManager.setCurrentActivityInTransaction(ctx, taskID, activity)
		if err != nil {
			return err
		}
		for _, prior := range priorTasks {
			if prior.Activity == nil || !prior.Activity.Passive || prior.Activity.Kind == domaintask.ActivityKindGame || prior.TaskID == taskID {
				continue
			}
			updatedPrior, getErr := store.(domaintask.ActivityTaskWriter).GetTaskActivity(ctx, prior.TaskID)
			if getErr != nil {
				return getErr
			}
			if activityHasCurrentParticipant(updatedPrior) {
				continue
			}
			summary := "passive activity interrupted by a new active Task"
			if prior.Activity.Kind == domaintask.ActivityKindGame {
				summary = "all participants left the passive game activity"
			}
			if _, err := txManager.updateStatusInTransaction(ctx, prior.TaskID, domaintask.StatusCancelled, summary, "", nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	return started, run, nil
}

func sameActivityIntent(left, right domaintask.ActivityMetadata) bool {
	if left.Kind != right.Kind || left.Label != right.Label || left.Phase != right.Phase || left.Passive != right.Passive || len(left.Participants) != len(right.Participants) {
		return false
	}
	if (left.Game == nil) != (right.Game == nil) || left.Game != nil && *left.Game != *right.Game {
		return false
	}
	leftParticipants := append([]domaintask.ActivityParticipant(nil), left.Participants...)
	rightParticipants := append([]domaintask.ActivityParticipant(nil), right.Participants...)
	sort.Slice(leftParticipants, func(i, j int) bool { return leftParticipants[i].ActorID < leftParticipants[j].ActorID })
	sort.Slice(rightParticipants, func(i, j int) bool { return rightParticipants[i].ActorID < rightParticipants[j].ActorID })
	for i := range leftParticipants {
		if leftParticipants[i].ActorID != rightParticipants[i].ActorID || leftParticipants[i].Role != rightParticipants[i].Role {
			return false
		}
	}
	return true
}

func (m *Manager) activateStartedActivityInTransaction(ctx context.Context, task domaintask.Task, activity domaintask.ActivityMetadata) error {
	if m == nil || m.store == nil || task.TaskID == "" || len(activity.Participants) == 0 {
		return domaintask.ErrCurrentActivityInvalid
	}
	actors := activityActorIDs(activity)
	priorTasks, err := readCurrentActivityTasks(ctx, m.store, actors)
	if err != nil {
		return err
	}
	preserveParent, err := m.childSharesCurrentAncestor(ctx, task)
	if err != nil {
		return err
	}
	if preserveParent {
		return nil
	}
	if _, err := m.setCurrentActivityInTransaction(ctx, task.TaskID, activity); err != nil {
		return err
	}
	writer, ok := m.store.(domaintask.ActivityTaskWriter)
	if !ok {
		return domaintask.ErrActivityTaskWriterUnsupported
	}
	for _, prior := range priorTasks {
		if prior.Activity == nil || !prior.Activity.Passive || prior.Activity.Kind == domaintask.ActivityKindGame || prior.TaskID == task.TaskID {
			continue
		}
		updatedPrior, getErr := writer.GetTaskActivity(ctx, prior.TaskID)
		if getErr != nil {
			return getErr
		}
		if activityHasCurrentParticipant(updatedPrior) {
			continue
		}
		if _, err := m.updateStatusInTransaction(ctx, prior.TaskID, domaintask.StatusCancelled, "passive activity interrupted by a new active Task", "", nil); err != nil {
			return err
		}
	}
	return nil
}

// CreateCurrentActivity atomically creates a passive, no-Run Task and assigns
// it as current for its participants. Runtime execution belongs to TaskManager
// Run APIs and is intentionally not started for passive activities.
func (m *Manager) CreateCurrentActivity(ctx context.Context, draft domaintask.Task, activity domaintask.ActivityMetadata) (domaintask.Task, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, errors.New("current activity context is nil")
	}
	if !activity.Passive {
		return domaintask.Task{}, errors.New("CreateCurrentActivity requires passive activity metadata")
	}
	if draft.TaskID == "" {
		draft.TaskID = modulecore.NewTaskID()
	}
	if draft.Activity != nil {
		return domaintask.Task{}, errors.New("activity metadata must be supplied through the owner API")
	}
	var created domaintask.Task
	err := m.activityTransaction(ctx, []modulecore.TaskID{draft.TaskID}, func(tx Store) error {
		if tx == nil {
			return errors.New("current activity transaction store is unavailable")
		}
		txManager := m.withStore(tx)
		actors := activityActorIDs(activity)
		current, err := readCurrentActivityTasks(ctx, tx, actors)
		if err != nil {
			return err
		}
		for actorID, task := range current {
			if hasCurrentParticipant(task, actorID) {
				return fmt.Errorf("actor %s already has current Task %s: %w", actorID, task.TaskID, domaintask.ErrCurrentActivityConflict)
			}
		}
		created, err = txManager.createInTransaction(ctx, draft, domaintask.SharedRoleContext{})
		if err != nil {
			return err
		}
		created, err = txManager.updateStatusInTransaction(ctx, created.TaskID, domaintask.StatusRunning, "", "", nil)
		if err != nil {
			return err
		}
		created, err = txManager.setCurrentActivityInTransaction(ctx, created.TaskID, activity)
		return err
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return created, nil
}

// TransitionCurrentActivity performs a compare-and-set handoff to a newly
// created passive Task. A late callback cannot overwrite a newer Task because
// both the current TaskID and participant revision are checked in the owner
// transaction before the replacement is created.
func (m *Manager) TransitionCurrentActivity(ctx context.Context, actorID string, expectedTaskID modulecore.TaskID, expectedRevision uint64, draft domaintask.Task, activity domaintask.ActivityMetadata) (domaintask.Task, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil || !canonicalActivityActor(actorID) || expectedRevision == 0 || !activity.Passive {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	if err := expectedTaskID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("expected current task_id is invalid: %w", err)
	}
	if draft.Activity != nil {
		return domaintask.Task{}, errors.New("activity metadata must be supplied through the owner API")
	}
	activity.Participants = []domaintask.ActivityParticipant{{ActorID: actorID}}
	if draft.TaskID == "" {
		draft.TaskID = modulecore.NewTaskID()
	}
	var created domaintask.Task
	err := m.activityTransaction(ctx, []modulecore.TaskID{expectedTaskID, draft.TaskID}, func(tx Store) error {
		current, err := readCurrentActivityTasks(ctx, tx, []string{actorID})
		if err != nil {
			return err
		}
		prior, found := current[actorID]
		if !found || prior.TaskID != expectedTaskID {
			return domaintask.ErrCurrentActivityConflict
		}
		participant, found := currentParticipant(prior, actorID)
		if !found || participant.Revision != expectedRevision {
			return domaintask.ErrCurrentActivityConflict
		}
		if prior.Activity != nil && prior.Activity.Passive {
			sharedGame := prior.Activity.Kind == domaintask.ActivityKindGame
			if !sharedGame {
				return errors.New("passive activity handoff requires an explicit terminal outcome")
			}
		}
		txManager := m.withStore(tx)
		created, err = txManager.createInTransaction(ctx, draft, domaintask.SharedRoleContext{})
		if err != nil {
			return err
		}
		created, err = txManager.updateStatusInTransaction(ctx, created.TaskID, domaintask.StatusRunning, "", "", nil)
		if err != nil {
			return err
		}
		_, err = txManager.setCurrentActivityInTransaction(ctx, created.TaskID, activity)
		return err
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return created, nil
}

// CompleteAndTransitionCurrentActivity closes the confirmed current Task and
// installs a new passive Task in the same revision-checked transaction. It is
// intended for an observed elapsed window or another confirmed terminal event.
func (m *Manager) CompleteAndTransitionCurrentActivity(ctx context.Context, actorID string, expectedTaskID modulecore.TaskID, expectedRevision uint64, terminalStatus domaintask.Status, summary string, draft domaintask.Task, activity domaintask.ActivityMetadata) (domaintask.Task, error) {
	if m == nil || m.store == nil || ctx == nil || !domaintask.IsTerminal(terminalStatus) || strings.TrimSpace(summary) == "" || !activity.Passive {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	if !canonicalActivityActor(actorID) || expectedRevision == 0 || expectedTaskID.Validate() != nil || draft.Activity != nil {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	activity.Participants = []domaintask.ActivityParticipant{{ActorID: actorID}}
	if draft.TaskID == "" {
		draft.TaskID = modulecore.NewTaskID()
	}
	var replacement domaintask.Task
	err := m.activityTransaction(ctx, []modulecore.TaskID{expectedTaskID, draft.TaskID}, func(tx Store) error {
		current, err := readCurrentActivityTasks(ctx, tx, []string{actorID})
		if err != nil {
			return err
		}
		prior, found := current[actorID]
		if !found || prior.TaskID != expectedTaskID {
			return domaintask.ErrCurrentActivityConflict
		}
		participant, found := currentParticipant(prior, actorID)
		if !found || participant.Revision != expectedRevision {
			return domaintask.ErrCurrentActivityConflict
		}
		if prior.Activity == nil || !prior.Activity.Passive {
			return errors.New("complete-and-transition requires a passive current Task")
		}
		for _, other := range prior.Activity.Participants {
			if other.ActorID != actorID && other.IsCurrent {
				return errors.New("shared current Task cannot be terminalized for one participant")
			}
		}
		txManager := m.withStore(tx)
		replacement, err = txManager.createInTransaction(ctx, draft, domaintask.SharedRoleContext{})
		if err != nil {
			return err
		}
		replacement, err = txManager.updateStatusInTransaction(ctx, replacement.TaskID, domaintask.StatusRunning, "", "", nil)
		if err != nil {
			return err
		}
		replacement, err = txManager.setCurrentActivityInTransaction(ctx, replacement.TaskID, activity)
		if err != nil {
			return err
		}
		_, err = txManager.updateStatusInTransaction(ctx, expectedTaskID, terminalStatus, strings.TrimSpace(summary), "", nil)
		return err
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return replacement, nil
}

// CompleteObservedGameActivity closes one observer-confirmed runless shared
// Game Task and atomically gives every still-current participant a passive
// evaluating Task. The exact session and every participant revision are CAS
// inputs so stale observer results cannot end a replacement session.
func (m *Manager) CompleteObservedGameActivity(ctx context.Context, gameTaskID modulecore.TaskID, gameID, sessionID string, expectedParticipants map[string]uint64, terminalStatus domaintask.Status, summary string) (map[string]domaintask.Task, error) {
	if m == nil || m.store == nil || ctx == nil || gameTaskID.Validate() != nil || !domaintask.IsTerminal(terminalStatus) ||
		!validActivityOwnerID(gameID) || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID ||
		len(sessionID) > 256 || strings.TrimSpace(summary) == "" {
		return nil, domaintask.ErrCurrentActivityInvalid
	}
	actors := make([]string, 0, len(expectedParticipants))
	for actorID, revision := range expectedParticipants {
		if !canonicalActivityActor(actorID) || revision == 0 {
			return nil, domaintask.ErrCurrentActivityInvalid
		}
		actors = append(actors, actorID)
	}
	sort.Strings(actors)
	transactionIDs := make([]modulecore.TaskID, 0, len(actors)+1)
	transactionIDs = append(transactionIDs, gameTaskID)
	for range actors {
		transactionIDs = append(transactionIDs, modulecore.NewTaskID())
	}
	var replacements map[string]domaintask.Task
	err := m.activityTransaction(ctx, transactionIDs, func(tx Store) error {
		game, err := tx.GetTask(ctx, gameTaskID)
		if err != nil {
			return err
		}
		if game.Status != domaintask.StatusRunning || game.Activity == nil || !game.Activity.Passive ||
			game.Activity.Kind != domaintask.ActivityKindGame || game.Activity.Game == nil ||
			game.Activity.Game.GameID != gameID || game.Activity.Game.SessionID != sessionID {
			return domaintask.ErrCurrentActivityConflict
		}
		currentParticipants := make(map[string]uint64)
		for _, participant := range game.Activity.Participants {
			if participant.IsCurrent {
				currentParticipants[participant.ActorID] = participant.Revision
			}
		}
		if len(currentParticipants) != len(expectedParticipants) {
			return domaintask.ErrCurrentActivityConflict
		}
		for actorID, revision := range expectedParticipants {
			if currentParticipants[actorID] != revision {
				return domaintask.ErrCurrentActivityConflict
			}
		}
		currentTasks := map[string]domaintask.Task{}
		if len(actors) > 0 {
			currentTasks, err = readCurrentActivityTasks(ctx, tx, actors)
			if err != nil {
				return err
			}
		}
		for _, actorID := range actors {
			current, found := currentTasks[actorID]
			participant, participantFound := currentParticipant(current, actorID)
			if !found || current.TaskID != gameTaskID || !participantFound || participant.Revision != expectedParticipants[actorID] {
				return domaintask.ErrCurrentActivityConflict
			}
		}
		txManager := m.withStore(tx)
		replacements = make(map[string]domaintask.Task, len(actors))
		for index, actorID := range actors {
			draft := domaintask.Task{TaskID: transactionIDs[index+1], Title: "Evaluating next activity", Assignee: actorID, Route: domaintask.RouteGeneral}
			created, err := txManager.createInTransaction(ctx, draft, domaintask.SharedRoleContext{})
			if err != nil {
				return err
			}
			created, err = txManager.updateStatusInTransaction(ctx, created.TaskID, domaintask.StatusRunning, "", "", nil)
			if err != nil {
				return err
			}
			created, err = txManager.setCurrentActivityInTransaction(ctx, created.TaskID, domaintask.ActivityMetadata{
				Kind: domaintask.ActivityKindEvaluating, Label: "Evaluating next activity", Phase: "evaluating", Passive: true,
				Participants: []domaintask.ActivityParticipant{{ActorID: actorID}},
			})
			if err != nil {
				return err
			}
			replacements[actorID] = created
		}
		_, err = txManager.updateStatusInTransaction(ctx, gameTaskID, terminalStatus, strings.TrimSpace(summary), "", nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return replacements, nil
}

// ListActiveExternalGameActivities returns only indexed live, runless passive
// Game Tasks. The persistence reader enforces a hard result bound and fails
// closed if that bound is exceeded; it never scans or truncates full history.
func (m *Manager) ListActiveExternalGameActivities(ctx context.Context) ([]domaintask.Task, error) {
	if m == nil || m.store == nil || ctx == nil {
		return nil, domaintask.ErrCurrentActivityInvalid
	}
	reader, ok := m.store.(domaintask.ActiveExternalGameActivityReader)
	if !ok {
		return nil, domaintask.ErrCurrentActivityReaderUnsupported
	}
	return reader.ListActiveExternalGameActivities(ctx)
}

// UpdateCurrentActivity applies a metadata phase update only if the actor's
// current Task and participant revision still match the caller's observation.
func (m *Manager) UpdateCurrentActivity(ctx context.Context, actorID string, expectedTaskID modulecore.TaskID, expectedRevision uint64, activity domaintask.ActivityMetadata) (domaintask.Task, error) {
	if m == nil || m.store == nil || ctx == nil || !canonicalActivityActor(actorID) || expectedRevision == 0 || activity.Revision != 0 {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	activity.Participants = nil
	var updated domaintask.Task
	err := m.activityTransaction(ctx, []modulecore.TaskID{expectedTaskID}, func(tx Store) error {
		current, err := readCurrentActivityTasks(ctx, tx, []string{actorID})
		if err != nil {
			return err
		}
		task, found := current[actorID]
		if !found || task.TaskID != expectedTaskID {
			return domaintask.ErrCurrentActivityConflict
		}
		participant, found := currentParticipant(task, actorID)
		if !found || participant.Revision != expectedRevision {
			return domaintask.ErrCurrentActivityConflict
		}
		if task.Activity == nil {
			return domaintask.ErrCurrentActivityInvalid
		}
		for _, existing := range task.Activity.Participants {
			if existing.IsCurrent && existing.ActorID != actorID {
				return errors.New("shared activity metadata cannot be changed by one participant")
			}
		}
		writer, ok := tx.(domaintask.ActivityTaskWriter)
		if !ok {
			return domaintask.ErrActivityTaskWriterUnsupported
		}
		updated, err = writer.GetTaskActivity(ctx, expectedTaskID)
		if err != nil {
			return err
		}
		participant.Revision++
		if participant.Revision == 0 || updated.Activity.Revision == ^uint64(0) {
			return errors.New("current activity revision overflow")
		}
		updated.Activity.Kind = activity.Kind
		updated.Activity.Label = strings.TrimSpace(activity.Label)
		updated.Activity.Phase = strings.TrimSpace(activity.Phase)
		updated.Activity.Passive = activity.Passive
		if activity.Game == nil {
			updated.Activity.Game = nil
		} else {
			game := *activity.Game
			updated.Activity.Game = &game
		}
		updated.Activity.Revision++
		if err := replaceActivityParticipant(&updated, participant); err != nil {
			return err
		}
		updated.UpdatedAt = m.now()
		if err := updated.Validate(); err != nil {
			return err
		}
		return writer.SaveTaskActivity(ctx, updated)
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return updated, nil
}

// BindCurrentActivity moves an actor to an existing Task under a current-task
// and participant-revision compare. A passive single-actor predecessor is
// cancelled with an explicit interruption summary in the same transaction.
func (m *Manager) BindCurrentActivity(ctx context.Context, actorID string, expectedTaskID modulecore.TaskID, expectedRevision uint64, targetTaskID modulecore.TaskID, activity domaintask.ActivityMetadata) (domaintask.Task, error) {
	if m == nil || m.store == nil || ctx == nil || !canonicalActivityActor(actorID) || expectedRevision == 0 || expectedTaskID.Validate() != nil || targetTaskID.Validate() != nil {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	if expectedTaskID == targetTaskID || activity.Passive && activity.Kind != domaintask.ActivityKindGame || activity.Revision != 0 {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	activity.Participants = []domaintask.ActivityParticipant{{ActorID: actorID}}
	var updated domaintask.Task
	err := m.activityTransaction(ctx, []modulecore.TaskID{expectedTaskID, targetTaskID}, func(tx Store) error {
		current, err := readCurrentActivityTasks(ctx, tx, []string{actorID})
		if err != nil {
			return err
		}
		prior, found := current[actorID]
		if !found || prior.TaskID != expectedTaskID {
			return domaintask.ErrCurrentActivityConflict
		}
		participant, found := currentParticipant(prior, actorID)
		if !found || participant.Revision != expectedRevision {
			return domaintask.ErrCurrentActivityConflict
		}
		writer, ok := tx.(domaintask.ActivityTaskWriter)
		if !ok {
			return domaintask.ErrActivityTaskWriterUnsupported
		}
		target, err := writer.GetTaskActivity(ctx, targetTaskID)
		if err != nil {
			return err
		}
		if domaintask.IsTerminal(target.Status) {
			return domaintask.ErrCurrentActivityInvalid
		}
		if activity.Kind == domaintask.ActivityKindGame &&
			(target.Activity == nil || target.Activity.Kind != domaintask.ActivityKindGame || target.Activity.Game == nil || activity.Game == nil || *target.Activity.Game != *activity.Game) {
			return domaintask.ErrCurrentActivityConflict
		}
		txManager := m.withStore(tx)
		updated, err = txManager.setCurrentActivityInTransaction(ctx, targetTaskID, activity)
		if err != nil {
			return err
		}
		if prior.Activity != nil && prior.Activity.Passive {
			otherCurrent := false
			for _, previous := range prior.Activity.Participants {
				if previous.ActorID != actorID && previous.IsCurrent {
					otherCurrent = true
					break
				}
			}
			if !otherCurrent && prior.Activity.Kind != domaintask.ActivityKindGame {
				if _, err := txManager.updateStatusInTransaction(ctx, expectedTaskID, domaintask.StatusCancelled, "passive activity interrupted by a new active Task", "", nil); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return updated, nil
}

// SnapshotCurrentActivities reads one committed store snapshot and resolves
// every requested actor to its canonical Task. Missing or duplicate bindings
// are errors; this method never synthesizes activity state.
func (m *Manager) SnapshotCurrentActivities(ctx context.Context, actorIDs []string) (map[string]domaintask.CurrentActivitySnapshot, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("current activity snapshot context is nil")
	}
	requested := make(map[string]struct{}, len(actorIDs))
	for _, actorID := range actorIDs {
		if !canonicalActivityActor(actorID) {
			return nil, fmt.Errorf("current activity actor_id %q is invalid", actorID)
		}
		if _, exists := requested[actorID]; exists {
			return nil, fmt.Errorf("current activity actor_id %q is duplicated", actorID)
		}
		requested[actorID] = struct{}{}
	}
	if len(requested) == 0 {
		return nil, errors.New("current activity actor IDs are required")
	}
	result := make(map[string]domaintask.CurrentActivitySnapshot, len(requested))
	var currentTasks map[string]domaintask.Task
	var err error
	if reader, ok := m.store.(domaintask.CurrentActivityTaskReader); ok {
		currentTasks, err = reader.CurrentActivityTasks(ctx, actorIDs)
		if !errors.Is(err, domaintask.ErrCurrentActivityReaderUnsupported) {
			if err != nil {
				return nil, err
			}
		}
	}
	if currentTasks == nil {
		err = m.store.ReadTransaction(ctx, func(tx Store) error {
			var readErr error
			currentTasks, readErr = readCurrentActivityTasks(ctx, tx, actorIDs)
			return readErr
		})
	}
	if err != nil {
		return nil, err
	}
	for actorID := range requested {
		task, found := currentTasks[actorID]
		if !found {
			return nil, fmt.Errorf("actor %s: %w", actorID, domaintask.ErrCurrentActivityNotFound)
		}
		if task.Activity == nil {
			return nil, fmt.Errorf("Task %s: %w: activity metadata is absent", task.TaskID, domaintask.ErrCurrentActivityInvalid)
		}
		if err := task.Activity.Validate(task.Status); err != nil {
			return nil, fmt.Errorf("Task %s has invalid activity metadata: %w", task.TaskID, err)
		}
		participant, found := currentParticipant(task, actorID)
		if !found || !participant.IsCurrent || domaintask.IsTerminal(task.Status) {
			return nil, fmt.Errorf("Task %s: %w", task.TaskID, domaintask.ErrCurrentActivityInvalid)
		}
		result[actorID] = domaintask.CurrentActivitySnapshot{Task: task, Participant: participant}
	}
	return result, nil
}

// SnapshotCurrentActivitiesAuthorized resolves current Tasks and returns only
// content-minimal summaries after the caller authorizes each Task identity.
func (m *Manager) SnapshotCurrentActivitiesAuthorized(ctx context.Context, actorIDs []string, authorize domaintask.TaskReadAuthorizationCheck) (map[string]domaintask.CurrentActivitySummary, error) {
	if authorize == nil {
		return nil, domaintask.ErrPrivateTaskAccessRequired
	}
	current, err := m.SnapshotCurrentActivities(ctx, actorIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]domaintask.CurrentActivitySummary, len(current))
	authorizationInputs := make(map[string]domaintask.TaskReadAuthorizationInput, len(current))
	actors := make([]string, 0, len(current))
	for actorID := range current {
		actors = append(actors, actorID)
	}
	sort.Strings(actors)
	for _, actorID := range actors {
		snapshot := current[actorID]
		task := snapshot.Task
		input := domaintask.TaskReadAuthorizationInput{
			Resource: domaintask.TaskReadResourceTask, TaskID: task.TaskID,
			WorkstreamID: task.WorkstreamID, GoalID: task.GoalID,
			OriginSessionID: task.OriginSessionID, OriginPublication: cloneActivityOriginPublication(task.OriginPublication),
		}
		if task.PrivateAccess != nil {
			input, err = domaintask.TaskReadInput(task, domaintask.TaskReadResourceTask, "")
			if err != nil {
				return nil, domaintask.ErrPrivateTaskAccessInvalid
			}
		}
		if err := authorize(ctx, input); err != nil {
			if task.PrivateAccess != nil {
				return nil, domaintask.ErrPrivateTaskAccessForbidden
			}
			return nil, err
		}
		authorizationInputs[actorID] = input
		row := domaintask.TaskActivityInventorySummary(task)
		result[actorID] = domaintask.CurrentActivitySummary{TaskActivityInventoryRow: row, Revision: snapshot.Participant.Revision}
	}
	for _, actorID := range actors {
		input := authorizationInputs[actorID]
		if err := authorize(ctx, input); err != nil {
			if current[actorID].Task.PrivateAccess != nil {
				return nil, domaintask.ErrPrivateTaskAccessForbidden
			}
			return nil, err
		}
	}
	return result, nil
}

func (m *Manager) setCurrentActivityInTransaction(ctx context.Context, taskID modulecore.TaskID, input domaintask.ActivityMetadata) (domaintask.Task, error) {
	if input.Passive && input.Kind != domaintask.ActivityKindRest && input.Kind != domaintask.ActivityKindSleep &&
		input.Kind != domaintask.ActivityKindMoving && input.Kind != domaintask.ActivityKindEvaluating && input.Kind != domaintask.ActivityKindGame {
		return domaintask.Task{}, errors.New("only rest, sleep, moving, evaluating, or game activity may be passive")
	}
	if len(input.Participants) == 0 {
		return domaintask.Task{}, errors.New("activity participants are required")
	}
	actors := make(map[string]string, len(input.Participants))
	for _, participant := range input.Participants {
		if !canonicalActivityActor(participant.ActorID) {
			return domaintask.Task{}, fmt.Errorf("activity participant actor_id %q is invalid", participant.ActorID)
		}
		if _, exists := actors[participant.ActorID]; exists {
			return domaintask.Task{}, fmt.Errorf("activity participant %q is duplicated", participant.ActorID)
		}
		if participant.IsCurrent || participant.Revision != 0 {
			return domaintask.Task{}, errors.New("activity owner input cannot set current state or revision")
		}
		actors[participant.ActorID] = participant.Role
	}
	activityWriter, ok := m.store.(domaintask.ActivityTaskWriter)
	if !ok {
		return domaintask.Task{}, domaintask.ErrActivityTaskWriterUnsupported
	}
	target, err := activityWriter.GetTaskActivity(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	if domaintask.IsTerminal(target.Status) {
		return domaintask.Task{}, fmt.Errorf("target Task is terminal: %w", domaintask.ErrCurrentActivityInvalid)
	}
	actorIDs := make([]string, 0, len(actors))
	for actorID := range actors {
		actorIDs = append(actorIDs, actorID)
	}
	currentTasks, err := readCurrentActivityTasks(ctx, m.store, actorIDs)
	if err != nil {
		return domaintask.Task{}, err
	}
	maxRevision := make(map[string]uint64, len(actors))
	if target.Activity != nil {
		for actorID := range actors {
			if participant, found := currentParticipant(target, actorID); found {
				maxRevision[actorID] = participant.Revision
			}
		}
	}
	oldTasks := make(map[modulecore.TaskID]domaintask.Task)
	for actorID, currentTask := range currentTasks {
		if alreadyUpdated, exists := oldTasks[currentTask.TaskID]; exists {
			currentTask = alreadyUpdated
		}
		participant, found := currentParticipant(currentTask, actorID)
		if !found || !participant.IsCurrent {
			continue
		}
		maxRevision[actorID] = participant.Revision
		if currentTask.TaskID != taskID {
			if participant.Revision == ^uint64(0) {
				return domaintask.Task{}, errors.New("activity participant revision overflow")
			}
			participant.IsCurrent = false
			participant.Revision++
			if err := replaceActivityParticipant(&currentTask, participant); err != nil {
				return domaintask.Task{}, err
			}
			oldTasks[currentTask.TaskID] = currentTask
		}
	}
	for _, oldTask := range oldTasks {
		if oldTask.Activity.Revision == ^uint64(0) {
			return domaintask.Task{}, errors.New("activity revision overflow")
		}
		oldTask.Activity.Revision++
		oldTask.UpdatedAt = m.now()
		if err := saveActivityTask(ctx, m.store, oldTask); err != nil {
			return domaintask.Task{}, err
		}
	}
	if target.Activity != nil {
		target.Activity.Participants = append([]domaintask.ActivityParticipant(nil), target.Activity.Participants...)
	}
	if target.Activity == nil {
		target.Activity = &domaintask.ActivityMetadata{}
	}
	target.Activity.Kind = input.Kind
	target.Activity.Label = strings.TrimSpace(input.Label)
	target.Activity.Phase = strings.TrimSpace(input.Phase)
	target.Activity.Passive = input.Passive
	if input.Game == nil {
		target.Activity.Game = nil
	} else {
		game := *input.Game
		target.Activity.Game = &game
	}
	for actorID, role := range actors {
		if maxRevision[actorID] == ^uint64(0) {
			return domaintask.Task{}, errors.New("activity participant revision overflow")
		}
		participant := domaintask.ActivityParticipant{ActorID: actorID, Role: role, IsCurrent: true, Revision: maxRevision[actorID] + 1}
		if participant.Revision == 0 {
			participant.Revision = 1
		}
		if err := replaceActivityParticipant(&target, participant); err != nil {
			return domaintask.Task{}, err
		}
	}
	sort.Slice(target.Activity.Participants, func(i, j int) bool {
		return target.Activity.Participants[i].ActorID < target.Activity.Participants[j].ActorID
	})
	if target.Activity.Revision == ^uint64(0) {
		return domaintask.Task{}, errors.New("activity revision overflow")
	}
	target.Activity.Revision++
	if target.Activity.Revision == 0 {
		target.Activity.Revision = 1
	}
	target.UpdatedAt = m.now()
	if err := target.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if err := saveActivityTask(ctx, m.store, target); err != nil {
		return domaintask.Task{}, err
	}
	return target, nil
}

func (m *Manager) withStore(store Store) *Manager {
	return &Manager{store: store, limits: m.limits, expectedCriteriaRevision: m.expectedCriteriaRevision, idIndexed: m.idIndexed, now: m.now}
}

func (m *Manager) activityTransaction(ctx context.Context, taskIDs []modulecore.TaskID, fn func(Store) error) error {
	if m == nil || m.store == nil {
		return errors.New("task manager store is unavailable")
	}
	transaction, ok := m.store.(domaintask.ActivityTaskTransaction)
	if !ok {
		return domaintask.ErrActivityTaskWriterUnsupported
	}
	return transaction.ActivityTransaction(ctx, taskIDs, fn)
}

func listAllCurrentActivityTasks(ctx context.Context, store Store) ([]domaintask.Task, error) {
	var tasks []domaintask.Task
	var after modulecore.TaskID
	for {
		page, err := store.ListTasks(ctx, domaintask.Filter{Limit: domaintask.MaxTaskListPageSize, TaskIDAfter: &after})
		if err != nil {
			return nil, fmt.Errorf("list Tasks for current activity: %w", err)
		}
		tasks = append(tasks, page...)
		if len(page) < domaintask.MaxTaskListPageSize {
			return tasks, nil
		}
		after = page[len(page)-1].TaskID
	}
}

func readCurrentActivityTasks(ctx context.Context, store Store, actorIDs []string) (map[string]domaintask.Task, error) {
	if reader, ok := store.(domaintask.CurrentActivityTaskReader); ok {
		return reader.CurrentActivityTasks(ctx, actorIDs)
	}
	tasks, err := listAllCurrentActivityTasks(ctx, store)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(actorIDs))
	for _, actorID := range actorIDs {
		wanted[actorID] = struct{}{}
	}
	result := make(map[string]domaintask.Task, len(actorIDs))
	for _, task := range tasks {
		if task.Activity == nil {
			continue
		}
		for _, participant := range task.Activity.Participants {
			if !participant.IsCurrent {
				continue
			}
			if _, ok := wanted[participant.ActorID]; !ok {
				continue
			}
			if prior, duplicate := result[participant.ActorID]; duplicate && prior.TaskID != task.TaskID {
				return nil, fmt.Errorf("actor %s: %w", participant.ActorID, domaintask.ErrCurrentActivityConflict)
			}
			result[participant.ActorID] = task
		}
	}
	return result, nil
}

func activityActorIDs(activity domaintask.ActivityMetadata) []string {
	actors := make([]string, 0, len(activity.Participants))
	for _, participant := range activity.Participants {
		actors = append(actors, participant.ActorID)
	}
	return actors
}

func currentParticipant(task domaintask.Task, actorID string) (domaintask.ActivityParticipant, bool) {
	if task.Activity == nil {
		return domaintask.ActivityParticipant{}, false
	}
	for _, participant := range task.Activity.Participants {
		if participant.ActorID == actorID {
			return participant, true
		}
	}
	return domaintask.ActivityParticipant{}, false
}

func hasCurrentParticipant(task domaintask.Task, actorID string) bool {
	participant, found := currentParticipant(task, actorID)
	return found && participant.IsCurrent
}

func activityHasCurrentParticipant(task domaintask.Task) bool {
	if task.Activity == nil {
		return false
	}
	for _, participant := range task.Activity.Participants {
		if participant.IsCurrent {
			return true
		}
	}
	return false
}

func (m *Manager) currentTasksForActors(ctx context.Context, actorIDs []string) (map[string]domaintask.Task, error) {
	if reader, ok := m.store.(domaintask.CurrentActivityTaskReader); ok {
		current, err := reader.CurrentActivityTasks(ctx, actorIDs)
		if !errors.Is(err, domaintask.ErrCurrentActivityReaderUnsupported) {
			return current, err
		}
	}
	var current map[string]domaintask.Task
	err := m.store.ReadTransaction(ctx, func(tx Store) error {
		var readErr error
		current, readErr = readCurrentActivityTasks(ctx, tx, actorIDs)
		return readErr
	})
	return current, err
}

func sameActivityTaskAssignments(before, after map[string]domaintask.Task) bool {
	if len(before) != len(after) {
		return false
	}
	for actorID, prior := range before {
		current, found := after[actorID]
		if !found || current.TaskID != prior.TaskID {
			return false
		}
		priorParticipant, priorFound := currentParticipant(prior, actorID)
		currentParticipant, currentFound := currentParticipant(current, actorID)
		if !priorFound || !currentFound || priorParticipant.Revision != currentParticipant.Revision {
			return false
		}
	}
	return true
}

func uniqueActivityTaskIDs(taskIDs []modulecore.TaskID) []modulecore.TaskID {
	seen := make(map[modulecore.TaskID]struct{}, len(taskIDs))
	unique := make([]modulecore.TaskID, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if _, exists := seen[taskID]; exists {
			continue
		}
		seen[taskID] = struct{}{}
		unique = append(unique, taskID)
	}
	return unique
}

func replaceActivityParticipant(task *domaintask.Task, replacement domaintask.ActivityParticipant) error {
	if task == nil || task.Activity == nil {
		return domaintask.ErrCurrentActivityInvalid
	}
	for i, participant := range task.Activity.Participants {
		if participant.ActorID == replacement.ActorID {
			task.Activity.Participants[i] = replacement
			return nil
		}
	}
	task.Activity.Participants = append(task.Activity.Participants, replacement)
	return nil
}

func saveActivityTask(ctx context.Context, store Store, task domaintask.Task) error {
	writer, ok := store.(domaintask.ActivityTaskWriter)
	if !ok {
		return domaintask.ErrActivityTaskWriterUnsupported
	}
	return writer.SaveTaskActivity(ctx, task)
}

func canonicalActivityActor(actorID string) bool {
	return len(actorID) > 0 && len(actorID) <= 64 && strings.TrimSpace(actorID) == actorID && strings.ToLower(actorID) == actorID && !strings.ContainsAny(actorID, "\r\n\x00")
}

func validActivityOwnerID(ownerID string) bool {
	if ownerID == "" || len(ownerID) > 256 || strings.TrimSpace(ownerID) != ownerID {
		return false
	}
	for _, r := range ownerID {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func cloneActivityOriginPublication(input *domaintask.OriginPublicationProvenance) *domaintask.OriginPublicationProvenance {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}
