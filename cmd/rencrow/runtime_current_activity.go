package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const agentActivityModuleID = "rencrow.core.agent_activity"

// initializeAgentCurrentActivities is a startup write path. Read paths use
// SnapshotCurrentActivities directly and never call this helper.
func initializeAgentCurrentActivities(ctx context.Context, owner *taskmanager.Manager, ownerID string, routine *agentRoutineRuntime, gameSessions gameObserverActiveSessionsReader) error {
	if owner == nil {
		return errors.New("canonical Task owner is unavailable")
	}
	for _, actorID := range agentHeartbeatActorIDs {
		if _, err := owner.SnapshotCurrentActivities(ctx, []string{actorID}); err == nil {
			continue
		} else if !errors.Is(err, domaintask.ErrCurrentActivityNotFound) {
			return fmt.Errorf("preflight current activity for %s: %w", actorID, err)
		}
		if restored, err := restorePendingRoutineTask(ctx, owner, routine, actorID); err != nil {
			return fmt.Errorf("restore running routine Task for %s: %w", actorID, err)
		} else if restored {
			continue
		}
		if restored, err := restoreRunningWorkTask(ctx, owner, actorID); err != nil {
			return fmt.Errorf("restore running work Task for %s: %w", actorID, err)
		} else if restored {
			continue
		}
	}
	if gameSessions != nil {
		if err := adoptExternalGamesAtStartup(ctx, owner, ownerID, gameSessions); err != nil {
			return fmt.Errorf("adopt confirmed external GAMES sessions: %w", err)
		}
	}
	for _, actorID := range agentHeartbeatActorIDs {
		_, err := owner.SnapshotCurrentActivities(ctx, []string{actorID})
		if err == nil {
			continue
		}
		if !errors.Is(err, domaintask.ErrCurrentActivityNotFound) {
			return fmt.Errorf("read current activity for %s: %w", actorID, err)
		}
		if routine != nil {
			if restored, restoreErr := restorePendingRoutineTask(ctx, owner, routine, actorID); restoreErr != nil {
				return fmt.Errorf("restore pending routine Task for %s: %w", actorID, restoreErr)
			} else if restored {
				continue
			}
			projected, projectionErr := routine.CurrentRoutineActivity(ctx, actorID)
			if projectionErr != nil {
				return fmt.Errorf("inspect persisted current routine state for %s: %w", actorID, projectionErr)
			}
			if projected.Availability == "available" {
				switch projected.OngoingActivity {
				case "rest":
					state, stateErr := routine.store.Load(ctx, actorID)
					if stateErr != nil {
						return fmt.Errorf("load verified rest state for %s: %w", actorID, stateErr)
					}
					if state.RestWindow == nil {
						return fmt.Errorf("verified rest projection for %s has no durable rest window", actorID)
					}
					activity := activityMetadataForActor(domaintask.ActivityKindRest, "Rest", "resting", true, actorID, "")
					timing := modulecore.TaskTiming{Timezone: routine.location().String(), DeadlineAt: &state.RestWindow.Until}
					_, createErr := createCurrentActivityOrUseConcurrent(ctx, owner, domaintask.Task{
						Title: "Agent activity: Rest", ModuleID: agentActivityModuleID, OwnerID: ownerID,
						Assignee: actorID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
						InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true, Timing: &timing,
					}, activity)
					if createErr != nil {
						return fmt.Errorf("restore verified rest activity for %s: %w", actorID, createErr)
					}
					continue
				case "sleep_window":
					state, stateErr := routine.store.Load(ctx, actorID)
					if stateErr != nil {
						return fmt.Errorf("load planned sleep state for %s: %w", actorID, stateErr)
					}
					if !state.LogicalSleepUntil.After(routine.now()) {
						return fmt.Errorf("planned sleep projection for %s has expired", actorID)
					}
					activity := activityMetadataForActor(domaintask.ActivityKindSleep, "Sleep", "sleep_window", true, actorID, "")
					timing := modulecore.TaskTiming{Timezone: routine.location().String(), DeadlineAt: &state.LogicalSleepUntil}
					_, createErr := createCurrentActivityOrUseConcurrent(ctx, owner, domaintask.Task{
						Title: "Agent activity: Sleep", ModuleID: agentActivityModuleID, OwnerID: ownerID,
						Assignee: actorID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
						InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true, Timing: &timing,
					}, activity)
					if createErr != nil {
						return fmt.Errorf("restore planned sleep activity for %s: %w", actorID, createErr)
					}
					continue
				}
			}
		}
		restored, restoreErr := restoreRunningWorkTask(ctx, owner, actorID)
		if restoreErr != nil {
			return fmt.Errorf("restore running Task for %s: %w", actorID, restoreErr)
		}
		if restored {
			continue
		}
		activity := domaintask.ActivityMetadata{
			Kind: domaintask.ActivityKindEvaluating, Label: "Evaluating", Phase: "waiting", Passive: true,
			Participants: []domaintask.ActivityParticipant{{ActorID: actorID}},
		}
		_, err = createCurrentActivityOrUseConcurrent(ctx, owner, domaintask.Task{
			Title: "Agent activity: evaluating", ModuleID: agentActivityModuleID, OwnerID: ownerID,
			Assignee: actorID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
			InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
		}, activity)
		if err != nil {
			return fmt.Errorf("initialize current activity for %s: %w", actorID, err)
		}
	}
	if gameSessions != nil {
		if err := reconcileTerminalGameActivitiesAtStartup(ctx, owner, gameSessions); err != nil {
			return fmt.Errorf("reconcile confirmed terminal GAMES sessions: %w", err)
		}
	}
	if _, err := owner.SnapshotCurrentActivities(ctx, agentHeartbeatActorIDs); err != nil {
		return fmt.Errorf("verify canonical current activities: %w", err)
	}
	return nil
}

// createCurrentActivityOrUseConcurrent handles the startup race where an
// active work owner binds a Task after bootstrap's read but before its
// initialize-only passive Task transaction. The concurrent binding wins.
func createCurrentActivityOrUseConcurrent(ctx context.Context, owner *taskmanager.Manager, draft domaintask.Task, activity domaintask.ActivityMetadata) (domaintask.Task, error) {
	task, err := owner.CreateCurrentActivity(ctx, draft, activity)
	if !errors.Is(err, domaintask.ErrCurrentActivityConflict) {
		return task, err
	}
	actorID := ""
	if len(activity.Participants) == 1 {
		actorID = activity.Participants[0].ActorID
	}
	if actorID == "" {
		return domaintask.Task{}, err
	}
	snapshot, snapshotErr := owner.SnapshotCurrentActivities(ctx, []string{actorID})
	if snapshotErr != nil {
		return domaintask.Task{}, fmt.Errorf("read concurrent current activity for %s: %w", actorID, snapshotErr)
	}
	current, ok := snapshot[actorID]
	if !ok {
		return domaintask.Task{}, fmt.Errorf("current activity conflict for %s but snapshot omitted actor", actorID)
	}
	return current.Task, nil
}

func adoptExternalGamesAtStartup(ctx context.Context, owner *taskmanager.Manager, ownerID string, source gameObserverActiveSessionsReader) error {
	sessions, err := source.ActiveAgentSessions(ctx)
	if err != nil {
		return fmt.Errorf("read active Agent-owned GAMES sessions: %w", err)
	}
	if err := validateGameObserverActiveAgentSessions(sessions); err != nil {
		return fmt.Errorf("validate active Agent-owned GAMES sessions: %w", err)
	}
	states, err := owner.ListGameTaskStates(ctx)
	if err != nil {
		return fmt.Errorf("read existing canonical GAME Tasks: %w", err)
	}
	for _, session := range sessions {
		matchedTask := false
		for _, state := range states {
			if state.Spec.GameID != session.GameID || state.Progress.SessionID != session.SessionID || !sameActorRoster(state.Spec.Personas, session.Personas) {
				continue
			}
			matchedTask = true
			if _, ensureErr := owner.EnsureTypedGameTaskActivity(ctx, state.Task.TaskID, session.GameID, session.SessionID, "playing"); ensureErr != nil {
				return fmt.Errorf("restore confirmed typed GAME Task %s: %w", state.Task.TaskID, ensureErr)
			}
		}
		if matchedTask {
			continue
		}
		participants := make([]domaintask.ActivityParticipant, 0, len(session.Personas))
		for _, actorID := range session.Personas {
			participants = append(participants, domaintask.ActivityParticipant{ActorID: actorID, Role: "player"})
		}
		if _, err := owner.EnsureExternalGameActivity(ctx, ownerID, session.GameID, session.SessionID, "playing", participants); err != nil {
			return fmt.Errorf("adopt session %s for game %s: %w", session.SessionID, session.GameID, err)
		}
	}
	return nil
}

func reconcileTerminalGameActivitiesAtStartup(ctx context.Context, owner *taskmanager.Manager, source gameObserverActiveSessionsReader) error {
	deps := &Dependencies{taskManager: owner, gameTaskAdmission: newGameTaskRuntimeAdmission(source, owner)}
	return reconcileTerminalGameTasks(ctx, deps)
}

// wrapWorkSourceWithGameResume restores an observer-confirmed off-lease GAME
// after foreground work ends. Discovery stays read-only; the handoff occurs
// only after the scheduler has selected and run this actor's Work.
func wrapWorkSourceWithGameResume(source agentheartbeat.Source, deps *Dependencies) agentheartbeat.Source {
	if source == nil || deps == nil {
		return source
	}
	return func(ctx context.Context, actorID string) (agentheartbeat.Work, bool, error) {
		work, found, err := source(ctx, actorID)
		if err != nil || !found || work.Run == nil {
			return work, found, err
		}
		run := work.Run
		work.Run = func(runCtx context.Context, stop <-chan struct{}) error {
			runErr := run(runCtx, stop)
			resumeErr := resumeConfirmedExternalGameActivity(runCtx, deps, actorID)
			if runErr != nil && resumeErr != nil {
				return errors.Join(runErr, resumeErr)
			}
			if runErr != nil {
				return runErr
			}
			return resumeErr
		}
		return work, found, nil
	}
}

// externalGameResumeIdleSource makes a resumed GAME a scheduler-selected
// write after foreground CHAT completion, where no BOARD/private Work closure
// exists to carry the handoff. Discovery only reads observer and Task state.
func externalGameResumeIdleSource(fallback agentheartbeat.Source, deps *Dependencies) agentheartbeat.Source {
	return func(ctx context.Context, actorID string) (agentheartbeat.Work, bool, error) {
		if deps == nil || deps.taskManager == nil || deps.gameTaskAdmission == nil || deps.gameTaskAdmission.sessions == nil {
			if fallback == nil {
				return agentheartbeat.Work{}, false, nil
			}
			return fallback(ctx, actorID)
		}
		currentByActor, err := deps.taskManager.SnapshotCurrentActivities(ctx, []string{actorID})
		if err != nil {
			return agentheartbeat.Work{}, false, err
		}
		current := currentByActor[actorID]
		if current.Task.Activity != nil && current.Task.Activity.Kind == domaintask.ActivityKindGame {
			return observedGameTerminalWork(ctx, deps, current)
		}
		sessions, err := deps.gameTaskAdmission.sessions.ActiveAgentSessions(ctx)
		if err != nil {
			return agentheartbeat.Work{}, false, fmt.Errorf("read active GAMES before idle resume: %w", err)
		}
		if err := validateGameObserverActiveAgentSessions(sessions); err != nil {
			return agentheartbeat.Work{}, false, fmt.Errorf("validate active GAMES before idle resume: %w", err)
		}
		for _, session := range sessions {
			if current.Task.Activity != nil && current.Task.Activity.Passive && containsStringExact(session.Personas, actorID) {
				return agentheartbeat.Work{
					ID:       "resume-game:" + session.GameID + ":" + session.SessionID,
					Priority: domaintask.PriorityNormal, CreatedAt: time.Now().UTC(),
					Run: func(runCtx context.Context, _ <-chan struct{}) error {
						return resumeConfirmedExternalGameActivity(runCtx, deps, actorID)
					},
				}, true, nil
			}
		}
		if fallback != nil {
			return fallback(ctx, actorID)
		}
		return agentheartbeat.Work{}, false, nil
	}
}

func observedGameTerminalWork(ctx context.Context, deps *Dependencies, current domaintask.CurrentActivitySnapshot) (agentheartbeat.Work, bool, error) {
	return observedGameTerminalTaskWork(ctx, deps, current.Task)
}

func observedGameTerminalTaskWork(ctx context.Context, deps *Dependencies, gameTask domaintask.Task) (agentheartbeat.Work, bool, error) {
	if gameTask.Activity != nil && gameTask.Activity.Kind == domaintask.ActivityKindGame && gameTask.Activity.Phase == "preparing" &&
		gameTask.Activity.Game != nil && gameTask.Activity.Game.SessionID == "" {
		return agentheartbeat.Work{}, false, nil
	}
	if deps == nil || deps.taskManager == nil || deps.gameTaskAdmission == nil || deps.gameTaskAdmission.sessionDetails == nil ||
		gameTask.Activity == nil || gameTask.Activity.Kind != domaintask.ActivityKindGame || gameTask.Activity.Game == nil || gameTask.Activity.Game.SessionID == "" {
		return agentheartbeat.Work{}, false, errors.New("current GAME lacks an exact terminal-status observer")
	}
	if !gameTask.Activity.Passive {
		// Active controller Tasks retain their existing Run and launch receipt
		// lifecycle; only runless external coordination Tasks use this owner path.
		return agentheartbeat.Work{}, false, nil
	}
	gameRef := *gameTask.Activity.Game
	session, err := deps.gameTaskAdmission.sessionDetails.SessionSummary(ctx, gameRef.SessionID)
	if err != nil {
		return agentheartbeat.Work{}, false, fmt.Errorf("read exact GAME session status: %w", err)
	}
	if err := validateGameObserverSessionSummary(session); err != nil {
		return agentheartbeat.Work{}, false, fmt.Errorf("validate exact GAME session status: %w", err)
	}
	if session.GameID != gameRef.GameID || session.SessionID != gameRef.SessionID {
		return agentheartbeat.Work{}, false, errors.New("GAMES observer returned a different session identity")
	}
	gameRoster := make([]string, 0, len(gameTask.Activity.Participants))
	for _, participant := range gameTask.Activity.Participants {
		gameRoster = append(gameRoster, participant.ActorID)
	}
	observedRoster := session.Personas
	if len(observedRoster) == 0 && session.Persona != "" {
		observedRoster = []string{session.Persona}
	}
	if !sameActorRoster(gameRoster, observedRoster) {
		return agentheartbeat.Work{}, false, errors.New("GAMES observer session roster does not match the canonical GAME Task")
	}
	if session.Status == "launching" || session.Status == "running" {
		return agentheartbeat.Work{}, false, nil
	}
	terminalStatus := domaintask.StatusSucceeded
	if session.Status == "failed" {
		terminalStatus = domaintask.StatusFailed
	} else if session.Status != "completed" {
		return agentheartbeat.Work{}, false, fmt.Errorf("GAMES observer returned an unknown terminal status %q", session.Status)
	}
	actorIDs := append([]string(nil), gameRoster...)
	if len(actorIDs) == 0 {
		return agentheartbeat.Work{}, false, errors.New("canonical GAME Task has an empty participant roster")
	}
	snapshots, err := deps.taskManager.SnapshotCurrentActivities(ctx, actorIDs)
	if err != nil {
		return agentheartbeat.Work{}, false, fmt.Errorf("snapshot shared GAME participants: %w", err)
	}
	expected := make(map[string]uint64, len(actorIDs))
	for _, actorID := range actorIDs {
		participant := snapshots[actorID]
		if participant.Task.TaskID == gameTask.TaskID && participant.Participant.IsCurrent {
			expected[actorID] = participant.Participant.Revision
		}
	}
	status := terminalStatus
	summary := "confirmed GAMES session completed"
	if status == domaintask.StatusFailed {
		summary = "confirmed GAMES session failed"
	}
	return agentheartbeat.Work{
		ID:       "complete-game:" + gameRef.GameID + ":" + gameRef.SessionID,
		Priority: domaintask.PriorityHigh, CreatedAt: time.Now().UTC(),
		Run: func(runCtx context.Context, _ <-chan struct{}) error {
			_, completeErr := deps.taskManager.CompleteObservedGameActivity(runCtx, gameTask.TaskID,
				gameRef.GameID, gameRef.SessionID, expected, status, summary)
			return completeErr
		},
	}, true, nil
}

func runAgentActivityReconciler(ctx context.Context, deps *Dependencies) {
	ticker := time.NewTicker(agentheartbeat.Interval)
	defer ticker.Stop()
	reconcile := func(actorID string) {
		boundedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := reconcileAgentActivityAfterForeground(boundedCtx, deps, actorID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("WARN: current activity reconciliation failed actor=%s: %v", actorID, err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case actorID := <-deps.activityReconcileSignal:
			reconcile(actorID)
		case <-ticker.C:
			for _, actorID := range agentHeartbeatActorIDs {
				if ctx.Err() != nil {
					return
				}
				reconcile(actorID)
			}
			boundedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := reconcileTerminalGameTasks(boundedCtx, deps); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("WARN: off-lease GAME reconciliation failed: %v", err)
			}
			cancel()
		}
	}
}

func reconcileTerminalGameTasks(ctx context.Context, deps *Dependencies) error {
	if deps == nil || deps.taskManager == nil || deps.gameTaskAdmission == nil {
		return nil
	}
	tasks, err := listNonterminalGameTasksForActivityReconciliation(ctx, deps.taskManager)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Activity == nil || task.Activity.Kind != domaintask.ActivityKindGame {
			continue
		}
		work, found, workErr := observedGameTerminalTaskWork(ctx, deps, task)
		if workErr != nil {
			return fmt.Errorf("inspect off-lease GAME Task %s: %w", task.TaskID, workErr)
		}
		if found && work.Run != nil {
			if runErr := work.Run(ctx, nil); runErr != nil {
				return fmt.Errorf("complete off-lease GAME Task %s: %w", task.TaskID, runErr)
			}
		}
	}
	return nil
}

func listNonterminalGameTasksForActivityReconciliation(ctx context.Context, owner *taskmanager.Manager) ([]domaintask.Task, error) {
	externalTasks, err := owner.ListActiveExternalGameActivities(ctx)
	if err != nil {
		return nil, err
	}
	controllerStates, err := owner.ListGameTaskStates(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[modulecore.TaskID]struct{}, len(externalTasks)+len(controllerStates))
	tasks := make([]domaintask.Task, 0, len(externalTasks)+len(controllerStates))
	for _, task := range externalTasks {
		if task.Activity == nil || task.Activity.Kind != domaintask.ActivityKindGame {
			continue
		}
		seen[task.TaskID] = struct{}{}
		tasks = append(tasks, task)
	}
	for _, state := range controllerStates {
		if state.Task.Activity == nil || state.Task.Activity.Kind != domaintask.ActivityKindGame {
			continue
		}
		if _, duplicate := seen[state.Task.TaskID]; duplicate {
			continue
		}
		seen[state.Task.TaskID] = struct{}{}
		tasks = append(tasks, state.Task)
	}
	return tasks, nil
}

func reconcileAgentActivityAfterForeground(ctx context.Context, deps *Dependencies, actorID string) error {
	if deps == nil || deps.taskManager == nil || deps.gameTaskAdmission == nil {
		return nil
	}
	snapshots, err := deps.taskManager.SnapshotCurrentActivities(ctx, []string{actorID})
	if err != nil {
		return err
	}
	current := snapshots[actorID]
	if current.Task.Activity == nil {
		return domaintask.ErrCurrentActivityInvalid
	}
	if current.Task.Activity.Kind == domaintask.ActivityKindGame {
		work, found, err := observedGameTerminalWork(ctx, deps, current)
		if err != nil || !found {
			return err
		}
		if work.Run == nil {
			return errors.New("observed terminal GAME work has no Run")
		}
		return work.Run(ctx, nil)
	}
	if current.Task.Activity.Passive {
		return resumeConfirmedExternalGameActivity(ctx, deps, actorID)
	}
	return nil
}

func resumeConfirmedExternalGameActivity(ctx context.Context, deps *Dependencies, actorID string) error {
	if deps.taskManager == nil || deps.gameTaskAdmission == nil || deps.gameTaskAdmission.sessions == nil {
		return nil
	}
	ownerID := deps.localAgentOpsOwnerID
	if ownerID == "" {
		return errors.New("configured owner is unavailable for external GAME activity resume")
	}
	sessions, err := deps.gameTaskAdmission.sessions.ActiveAgentSessions(ctx)
	if err != nil {
		return fmt.Errorf("read external GAME sessions after foreground work: %w", err)
	}
	if err := validateGameObserverActiveAgentSessions(sessions); err != nil {
		return fmt.Errorf("validate external GAME sessions after foreground work: %w", err)
	}
	for _, session := range sessions {
		if !containsStringExact(session.Personas, actorID) {
			continue
		}
		target, typed, err := findConfirmedTypedGameTask(ctx, deps.taskManager, session)
		if err != nil {
			return err
		}
		if !typed {
			participants := make([]domaintask.ActivityParticipant, 0, len(session.Personas))
			for _, participantID := range session.Personas {
				participants = append(participants, domaintask.ActivityParticipant{ActorID: participantID, Role: "player"})
			}
			target, err = deps.taskManager.EnsureExternalGameActivity(ctx, ownerID, session.GameID, session.SessionID, "playing", participants)
			if err != nil {
				return fmt.Errorf("ensure confirmed external GAME Task: %w", err)
			}
		}
		currentByActor, err := deps.taskManager.SnapshotCurrentActivities(ctx, []string{actorID})
		if err != nil {
			return fmt.Errorf("read current activity before GAME resume: %w", err)
		}
		current := currentByActor[actorID]
		if current.Task.TaskID == target.TaskID {
			return nil
		}
		if current.Task.Activity == nil || !current.Task.Activity.Passive ||
			(current.Task.Activity.Kind == domaintask.ActivityKindGame && current.Task.TaskID != target.TaskID) {
			return nil
		}
		role := "player"
		for _, participant := range target.Activity.Participants {
			if participant.ActorID == actorID && participant.Role != "" {
				role = participant.Role
				break
			}
		}
		activity := domaintask.ActivityMetadata{
			Kind: domaintask.ActivityKindGame, Label: target.Activity.Label, Phase: target.Activity.Phase, Passive: target.Activity.Passive,
			Game:         &domaintask.ActivityGameReference{GameID: session.GameID, SessionID: session.SessionID},
			Participants: []domaintask.ActivityParticipant{{ActorID: actorID, Role: role}},
		}
		if _, err := deps.taskManager.BindCurrentActivity(ctx, actorID, current.Task.TaskID, current.Participant.Revision, target.TaskID, activity); err != nil {
			return fmt.Errorf("restore confirmed external GAME Task: %w", err)
		}
		return nil
	}
	return nil
}

func findConfirmedTypedGameTask(ctx context.Context, owner *taskmanager.Manager, session gameObserverActiveAgentSession) (domaintask.Task, bool, error) {
	states, err := owner.ListGameTaskStates(ctx)
	if err != nil {
		return domaintask.Task{}, false, fmt.Errorf("read typed GAME Tasks before external adoption: %w", err)
	}
	for _, state := range states {
		if state.Spec.GameID != session.GameID || state.Progress.SessionID != session.SessionID {
			continue
		}
		if !sameActorRoster(state.Spec.Personas, session.Personas) {
			return domaintask.Task{}, false, fmt.Errorf("typed GAME Task %s session roster conflicts with the exact observer session", state.Task.TaskID)
		}
		target, ensureErr := owner.EnsureTypedGameTaskActivity(ctx, state.Task.TaskID, session.GameID, session.SessionID, "playing")
		if ensureErr != nil {
			return domaintask.Task{}, false, fmt.Errorf("ensure exact typed GAME Task %s activity: %w", state.Task.TaskID, ensureErr)
		}
		if target.Activity == nil || target.Activity.Game == nil || target.Activity.Kind != domaintask.ActivityKindGame ||
			target.Activity.Game.GameID != session.GameID || target.Activity.Game.SessionID != session.SessionID || target.Activity.Passive {
			return domaintask.Task{}, false, fmt.Errorf("typed GAME Task %s has no exact active Activity binding", state.Task.TaskID)
		}
		return target, true, nil
	}
	for _, actorID := range session.Personas {
		terminal, err := owner.HasTerminalGameTaskSession(ctx, session.GameID, session.SessionID, actorID)
		if err != nil {
			return domaintask.Task{}, false, fmt.Errorf("check terminal typed GAME session %s: %w", session.SessionID, err)
		}
		if terminal {
			return domaintask.Task{}, false, fmt.Errorf("active observer session %s belongs to a terminal typed GAME Task", session.SessionID)
		}
	}
	return domaintask.Task{}, false, nil
}

func sameActorRoster(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, actorID := range left {
		seen[actorID]++
	}
	for _, actorID := range right {
		seen[actorID]--
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

func restorePendingRoutineTask(ctx context.Context, owner *taskmanager.Manager, routine *agentRoutineRuntime, actorID string) (bool, error) {
	if owner == nil || routine == nil || routine.store == nil {
		return false, nil
	}
	state, err := routine.store.Load(ctx, actorID)
	if err != nil {
		return false, err
	}
	if state.Pending == nil {
		return false, nil
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		return false, err
	}
	for _, actorProgress := range progress.Actors {
		if actorProgress.ActorID != actorID || actorProgress.TaskID == "" {
			continue
		}
		taskID := modulecore.TaskID(actorProgress.TaskID)
		task, getErr := owner.Get(ctx, taskID)
		if getErr != nil {
			return false, getErr
		}
		if task.OwnerID != routine.ownerID || task.ModuleID != agentRoutineModuleID || task.Assignee != actorID || task.Route != domaintask.RouteCHAT {
			return false, errors.New("pending routine checkpoint does not identify an active exact Task")
		}
		if domaintask.IsTerminal(task.Status) {
			return false, nil
		}
		if task.Status == domaintask.StatusWaiting {
			return false, nil
		}
		if task.Status != domaintask.StatusRunning {
			return false, errors.New("pending routine checkpoint does not identify a running exact Task")
		}
		activity := agentRoutineTaskActivity(state.Pending, actorID)
		activity.Passive = false
		_, setErr := owner.SetCurrentActivity(ctx, taskID, activity)
		if setErr != nil {
			return false, setErr
		}
		return true, nil
	}
	return false, nil
}

func restoreRunningWorkTask(ctx context.Context, owner *taskmanager.Manager, actorID string) (bool, error) {
	if owner == nil {
		return false, errors.New("canonical Task owner is unavailable")
	}
	tasks, err := owner.List(ctx, domaintask.Filter{Assignee: actorID, Status: domaintask.StatusRunning, Limit: domaintask.MaxTaskListPageSize})
	if err != nil {
		return false, err
	}
	if len(tasks) >= domaintask.MaxTaskListPageSize {
		return false, errors.New("running Task recovery reached its bounded scan limit")
	}
	candidates := make([]domaintask.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Assignee == actorID && task.Status == domaintask.StatusRunning && task.Route != domaintask.RouteGAME && !domaintask.IsTerminal(task.Status) {
			candidates = append(candidates, task)
		}
	}
	if len(candidates) == 0 {
		return false, nil
	}
	rootCandidates := make([]domaintask.Task, 0, len(candidates))
	for _, candidate := range candidates {
		hasRunningAncestor := false
		for _, other := range candidates {
			if candidate.TaskID == other.TaskID {
				continue
			}
			ancestor, relationErr := taskIsAncestor(ctx, owner, other.TaskID, candidate)
			if relationErr != nil {
				return false, relationErr
			}
			if ancestor {
				hasRunningAncestor = true
				break
			}
		}
		if !hasRunningAncestor {
			rootCandidates = append(rootCandidates, candidate)
		}
	}
	if len(rootCandidates) != 1 {
		return false, errors.New("multiple unrelated running Task roots prevent unambiguous current activity recovery")
	}
	kind, label, phase := domaintask.ActivityKindWork, "Work", "working"
	if rootCandidates[0].Route == domaintask.RouteCHAT {
		kind, label, phase = domaintask.ActivityKindChat, "Chat", "talking"
	} else if rootCandidates[0].Title == "CORE agent routine: daily diary" {
		label = "Diary"
	}
	activity := activityMetadataForActor(kind, label, phase, false, actorID, "")
	_, err = owner.SetCurrentActivity(ctx, rootCandidates[0].TaskID, activity)
	return err == nil, err
}

func taskIsAncestor(ctx context.Context, owner *taskmanager.Manager, ancestorID modulecore.TaskID, task domaintask.Task) (bool, error) {
	seen := map[modulecore.TaskID]struct{}{task.TaskID: {}}
	parentID := task.ParentTaskID
	for parentID != "" {
		if parentID == ancestorID {
			return true, nil
		}
		if _, duplicate := seen[parentID]; duplicate {
			return false, errors.New("running Task parent chain contains a cycle")
		}
		seen[parentID] = struct{}{}
		parent, err := owner.Get(ctx, parentID)
		if err != nil {
			return false, fmt.Errorf("read running Task parent %s: %w", parentID, err)
		}
		parentID = parent.ParentTaskID
	}
	return false, nil
}

func snapshotAgentCurrentActivities(ctx context.Context, owner *taskmanager.Manager) (map[string]domaintask.CurrentActivitySnapshot, error) {
	if owner == nil {
		return nil, errors.New("canonical Task owner is unavailable")
	}
	return owner.SnapshotCurrentActivities(ctx, agentHeartbeatActorIDs)
}

func activityMetadataForActor(kind domaintask.ActivityKind, label, phase string, passive bool, actorID, role string) domaintask.ActivityMetadata {
	return domaintask.ActivityMetadata{
		Kind: kind, Label: label, Phase: phase, Passive: passive,
		Participants: []domaintask.ActivityParticipant{{ActorID: actorID, Role: role}},
	}
}

func (r *agentRoutineRuntime) updateCurrentRoutinePhase(ctx context.Context, actorID string, kind domaintask.ActivityKind, label, phase string) error {
	if r == nil || r.tasks == nil {
		return errors.New("routine phase update requires the canonical Task owner")
	}
	snapshots, err := r.tasks.SnapshotCurrentActivities(ctx, []string{actorID})
	if err != nil {
		return err
	}
	snapshot, found := snapshots[actorID]
	if !found || snapshot.Task.Activity == nil || snapshot.Task.Activity.Kind != kind || !snapshot.Participant.IsCurrent {
		return domaintask.ErrCurrentActivityConflict
	}
	activity := activityMetadataForActor(kind, label, phase, snapshot.Task.Activity.Passive, actorID, "")
	_, err = r.tasks.UpdateCurrentActivity(ctx, actorID, snapshot.Task.TaskID, snapshot.Participant.Revision, activity)
	return err
}

func publicCurrentActivity(snapshot domaintask.CurrentActivitySnapshot) (kind, label, phase string, revision uint64, ok bool) {
	activity := snapshot.Task.Activity
	if activity == nil || activity.Validate(snapshot.Task.Status) != nil || !snapshot.Participant.IsCurrent ||
		snapshot.Participant.ActorID == "" || snapshot.Participant.Revision == 0 {
		return "", "", "", 0, false
	}
	publicLabel := activity.Label
	if activity.Game != nil {
		publicLabel = activity.Game.GameID
	}
	formatted, valid := viewer.FormatPublicCurrentActivity(string(activity.Kind), publicLabel, activity.Phase,
		snapshot.Participant.ActorID, activity.Revision)
	if !valid {
		return "", "", "", 0, false
	}
	return formatted.Kind, formatted.Label, formatted.Phase, formatted.Revision, true
}

func agentRoutineTaskActivity(intent *agentroutine.Intent, actorID string) domaintask.ActivityMetadata {
	kind, label, phase := domaintask.ActivityKindRoutine, "Routine", "working"
	if intent != nil {
		switch intent.Kind {
		case agentroutine.ActivityMorningChat:
			kind, label = domaintask.ActivityKindChat, "Chat"
			for _, participant := range intent.Participants {
				if participant != actorID && isAgentHeartbeatActor(participant) {
					label = "chat-with-" + participant
					break
				}
			}
		case agentroutine.ActivityHobby:
			kind, label = domaintask.ActivityKindHobby, intent.Label
		case agentroutine.ActivityRelax:
			kind, label = domaintask.ActivityKindRest, "Rest"
		case agentroutine.ActivitySleep:
			kind, label, phase = domaintask.ActivityKindSleep, "Sleep", "preparing"
		case agentroutine.ActivityWake:
			kind, label, phase = domaintask.ActivityKindSleep, "Sleep", "waking"
		case agentroutine.ActivityDiary:
			kind, label = domaintask.ActivityKindWork, "Diary"
		}
	}
	return activityMetadataForActor(kind, label, phase, false, actorID, "")
}

func (r *agentRoutineRuntime) transitionPassiveActivity(
	ctx context.Context,
	actorID string,
	kind domaintask.ActivityKind,
	label, phase string,
	deadline time.Time,
	terminalStatus domaintask.Status,
	summary string,
) (domaintask.Task, error) {
	if r == nil || r.tasks == nil || deadline.IsZero() {
		return domaintask.Task{}, errors.New("passive activity transition requires the canonical Task owner and a deadline")
	}
	current, err := r.tasks.SnapshotCurrentActivities(ctx, []string{actorID})
	if err != nil {
		return domaintask.Task{}, fmt.Errorf("read current activity before %s handoff: %w", kind, err)
	}
	snapshot, found := current[actorID]
	if !found || snapshot.Task.Activity == nil || !snapshot.Participant.IsCurrent {
		return domaintask.Task{}, domaintask.ErrCurrentActivityConflict
	}
	if snapshot.Task.Activity.Kind == kind && snapshot.Task.Activity.Label == label &&
		snapshot.Task.Activity.Phase == phase && snapshot.Task.Activity.Passive &&
		snapshot.Task.Timing != nil && snapshot.Task.Timing.DeadlineAt != nil &&
		snapshot.Task.Timing.DeadlineAt.Equal(deadline) {
		return snapshot.Task, nil
	}
	timing := modulecore.TaskTiming{Timezone: r.location().String(), DeadlineAt: &deadline}
	draft := domaintask.Task{
		Title: "Agent activity: " + label, ModuleID: agentActivityModuleID, OwnerID: r.ownerID,
		Assignee: actorID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true, Timing: &timing,
	}
	activity := activityMetadataForActor(kind, label, phase, true, actorID, "")
	if snapshot.Task.Activity.Passive {
		return r.tasks.CompleteAndTransitionCurrentActivity(ctx, actorID, snapshot.Task.TaskID, snapshot.Participant.Revision,
			terminalStatus, summary, draft, activity)
	}
	return r.tasks.TransitionCurrentActivity(ctx, actorID, snapshot.Task.TaskID, snapshot.Participant.Revision, draft, activity)
}

func (r *agentRoutineRuntime) setRoutineMovingActivity(ctx context.Context, state agentroutine.State, actorID string, dueAt time.Time) error {
	if r == nil || r.tasks == nil || state.Pending == nil || dueAt.IsZero() {
		return errors.New("confirmed WORLD movement has no exact routine Task or due time")
	}
	taskID := modulecore.TaskID(state.Pending.TaskID)
	if taskID == "" {
		progress, err := decodeRoutineProgress(state.Pending.Output)
		if err != nil {
			return err
		}
		for _, actorProgress := range progress.Actors {
			if actorProgress.ActorID == actorID && actorProgress.TaskID != "" {
				taskID = modulecore.TaskID(actorProgress.TaskID)
				break
			}
		}
	}
	if taskID == "" {
		return errors.New("confirmed WORLD movement has no exact actor Task binding")
	}
	snapshots, err := r.tasks.SnapshotCurrentActivities(ctx, []string{actorID})
	if err != nil {
		return err
	}
	current, found := snapshots[actorID]
	if !found || current.Task.Activity == nil || !current.Participant.IsCurrent {
		return domaintask.ErrCurrentActivityConflict
	}
	if current.Task.Activity.Kind == domaintask.ActivityKindMoving && current.Task.ParentTaskID == taskID {
		return nil
	}
	if current.Task.TaskID != taskID {
		return domaintask.ErrCurrentActivityConflict
	}
	timing := modulecore.TaskTiming{Timezone: r.location().String(), DeadlineAt: &dueAt}
	draft := domaintask.Task{
		Title: "Agent activity: Moving", ModuleID: agentActivityModuleID, OwnerID: r.ownerID,
		Assignee: actorID, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true, ParentTaskID: taskID, Timing: &timing,
	}
	activity := activityMetadataForActor(domaintask.ActivityKindMoving, "Moving", "in_transit", true, actorID, "")
	_, err = r.tasks.TransitionCurrentActivity(ctx, actorID, current.Task.TaskID, current.Participant.Revision, draft, activity)
	return err
}

func (r *agentRoutineRuntime) restoreRoutineTaskCurrent(ctx context.Context, state agentroutine.State, actorID string) error {
	if r == nil || r.tasks == nil || state.Pending == nil {
		return errors.New("routine movement recovery has no exact owner context")
	}
	taskID := modulecore.TaskID(state.Pending.TaskID)
	if taskID == "" {
		progress, err := decodeRoutineProgress(state.Pending.Output)
		if err != nil {
			return err
		}
		for _, actorProgress := range progress.Actors {
			if actorProgress.ActorID == actorID && actorProgress.TaskID != "" {
				taskID = modulecore.TaskID(actorProgress.TaskID)
				break
			}
		}
	}
	if taskID == "" {
		return errors.New("routine movement recovery has no exact actor Task binding")
	}
	snapshots, err := r.tasks.SnapshotCurrentActivities(ctx, []string{actorID})
	if err != nil {
		return err
	}
	current, found := snapshots[actorID]
	if !found || current.Task.Activity == nil || !current.Participant.IsCurrent {
		return domaintask.ErrCurrentActivityConflict
	}
	if current.Task.TaskID == taskID {
		return nil
	}
	if current.Task.Activity.Kind != domaintask.ActivityKindMoving || current.Task.ParentTaskID != taskID {
		return domaintask.ErrCurrentActivityConflict
	}
	activity := agentRoutineTaskActivity(state.Pending, actorID)
	_, err = r.tasks.BindCurrentActivity(ctx, actorID, current.Task.TaskID, current.Participant.Revision, taskID, activity)
	return err
}

// expiredPassiveActivityWork is read-only during scheduler discovery. The
// reserved Work.Run performs the CAS transition, so a concurrent foreground
// Task cannot be overwritten by an expiry decision made from an old snapshot.
func (r *agentRoutineRuntime) expiredPassiveActivityWork(ctx context.Context, actorID string, now time.Time) (agentheartbeat.Work, bool, error) {
	if r == nil || r.tasks == nil {
		return agentheartbeat.Work{}, false, nil
	}
	snapshots, err := r.tasks.SnapshotCurrentActivities(ctx, []string{actorID})
	if err != nil {
		return agentheartbeat.Work{}, false, err
	}
	snapshot, found := snapshots[actorID]
	if !found || snapshot.Task.Activity == nil || !snapshot.Task.Activity.Passive || snapshot.Task.Timing == nil ||
		snapshot.Task.Timing.DeadlineAt == nil || snapshot.Task.Timing.DeadlineAt.After(now) ||
		(snapshot.Task.Activity.Kind != domaintask.ActivityKindRest && snapshot.Task.Activity.Kind != domaintask.ActivityKindSleep) {
		return agentheartbeat.Work{}, false, nil
	}
	actor := actorID
	return agentheartbeat.Work{
		ID:       "activity-expiry:" + actor + ":" + string(snapshot.Task.TaskID),
		Priority: domaintask.PriorityLow, CreatedAt: now.UTC(), ActivityKind: agentheartbeat.ActivityKindRoutine,
		Run: func(runCtx context.Context, _ <-chan struct{}) error {
			current, readErr := r.tasks.SnapshotCurrentActivities(runCtx, []string{actor})
			if readErr != nil {
				return readErr
			}
			latest, exists := current[actor]
			if !exists || latest.Task.TaskID != snapshot.Task.TaskID || latest.Participant.Revision != snapshot.Participant.Revision {
				return domaintask.ErrCurrentActivityConflict
			}
			if latest.Task.Timing == nil || latest.Task.Timing.DeadlineAt == nil || latest.Task.Timing.DeadlineAt.After(r.now()) {
				return domaintask.ErrCurrentActivityConflict
			}
			deadline := *latest.Task.Timing.DeadlineAt
			draft := domaintask.Task{
				Title: "Agent activity: evaluating", ModuleID: agentActivityModuleID, OwnerID: r.ownerID,
				Assignee: actor, Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
				InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
			}
			activity := activityMetadataForActor(domaintask.ActivityKindEvaluating, "Evaluating", "waiting", true, actor, "")
			_, transitionErr := r.tasks.CompleteAndTransitionCurrentActivity(runCtx, actor, latest.Task.TaskID,
				latest.Participant.Revision, domaintask.StatusSucceeded,
				"passive activity interval elapsed at "+deadline.UTC().Format(time.RFC3339), draft, activity)
			return transitionErr
		},
	}, true, nil
}

func (r *agentRoutineRuntime) ensureExistingRoutineTaskCurrent(ctx context.Context, taskID modulecore.TaskID, intent *agentroutine.Intent, actorID string) (domaintask.Task, error) {
	if r == nil || r.tasks == nil {
		return domaintask.Task{}, errors.New("routine activity requires the canonical Task owner")
	}
	snapshots, err := r.tasks.SnapshotCurrentActivities(ctx, []string{actorID})
	if err != nil {
		return domaintask.Task{}, fmt.Errorf("read current activity before routine resume: %w", err)
	}
	snapshot, found := snapshots[actorID]
	if !found || snapshot.Task.Activity == nil || !snapshot.Participant.IsCurrent {
		return domaintask.Task{}, domaintask.ErrCurrentActivityConflict
	}
	wanted := agentRoutineTaskActivity(intent, actorID)
	if snapshot.Task.TaskID != taskID {
		if snapshot.Task.Activity.Kind == domaintask.ActivityKindMoving && snapshot.Task.ParentTaskID == taskID {
			return r.tasks.Get(ctx, taskID)
		}
		if snapshot.Task.Activity.Kind != domaintask.ActivityKindEvaluating && snapshot.Task.Activity.Kind != domaintask.ActivityKindRest &&
			!(intent != nil && intent.Kind == agentroutine.ActivityWake && snapshot.Task.Activity.Kind == domaintask.ActivityKindSleep) {
			return domaintask.Task{}, domaintask.ErrCurrentActivityConflict
		}
		return r.tasks.BindCurrentActivity(ctx, actorID, snapshot.Task.TaskID, snapshot.Participant.Revision, taskID, wanted)
	}
	if snapshot.Task.Activity.Kind == wanted.Kind && snapshot.Task.Activity.Label == wanted.Label &&
		snapshot.Task.Activity.Phase == wanted.Phase && !snapshot.Task.Activity.Passive {
		return snapshot.Task, nil
	}
	if wanted.Kind == domaintask.ActivityKindSleep && snapshot.Task.Activity.Kind == wanted.Kind && !snapshot.Task.Activity.Passive &&
		(snapshot.Task.Activity.Phase == "sleeping" || snapshot.Task.Activity.Phase == "sleep_window") {
		return snapshot.Task, nil
	}
	wanted.Participants = nil
	return r.tasks.UpdateCurrentActivity(ctx, actorID, taskID, snapshot.Participant.Revision, wanted)
}
