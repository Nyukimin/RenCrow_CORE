package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// EnsureTypedGameTaskActivity attaches or refreshes the activity identity of an
// existing typed GAME Task after the caller has confirmed its observer session.
// It never starts a Run or a game. Existing active work keeps its assignment;
// idle actors and actors on passive activities may bind to this Task.
func (m *Manager) EnsureTypedGameTaskActivity(ctx context.Context, taskID modulecore.TaskID, gameID, sessionID, phase string) (domaintask.Task, error) {
	if m == nil || m.store == nil || ctx == nil || taskID.Validate() != nil || strings.TrimSpace(gameID) == "" ||
		strings.TrimSpace(phase) == "" || strings.TrimSpace(phase) != phase {
		return domaintask.Task{}, domaintask.ErrCurrentActivityInvalid
	}
	var result domaintask.Task
	err := m.withTypedGameActivityTransaction(ctx, taskID, func(txManager *Manager, actors []string, current map[string]domaintask.Task) error {
		tx := txManager.store
		task, err := tx.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		shared, err := loadPrivateTextSharedContext(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if task.Route != domaintask.RouteGAME || shared.GameSpec == nil || shared.GameProgress == nil {
			return fmt.Errorf("typed GAME Task state is incomplete: %w", ErrRunConflict)
		}
		state := GameTaskState{Task: task, Spec: *shared.GameSpec, Progress: *shared.GameProgress}
		if err := validateTypedGameActivityState(state, gameID, sessionID); err != nil {
			return err
		}
		result, err = txManager.ensureTypedGameTaskActivityInTransaction(ctx, taskID, gameID, sessionID, phase, state, actors, current)
		return err
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return result, nil
}

func (m *Manager) withTypedGameActivityTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(*Manager, []string, map[string]domaintask.Task) error) error {
	initial, err := m.GetGameTaskState(ctx, taskID)
	if err != nil {
		return err
	}
	actors, err := gameActivityActors(initial.Spec)
	if err != nil {
		return err
	}
	prior, err := m.currentTasksForActors(ctx, actors)
	if err != nil {
		return err
	}
	ids := []modulecore.TaskID{taskID}
	for _, task := range prior {
		ids = append(ids, task.TaskID)
	}
	return m.activityTransaction(ctx, uniqueActivityTaskIDs(ids), func(tx Store) error {
		current, err := readCurrentActivityTasks(ctx, tx, actors)
		if err != nil {
			return err
		}
		if !sameActivityTaskAssignments(prior, current) {
			return domaintask.ErrCurrentActivityConflict
		}
		return fn(m.withStore(tx), actors, current)
	})
}

func (m *Manager) ensureTypedGameTaskActivityInTransaction(ctx context.Context, taskID modulecore.TaskID, gameID, sessionID, phase string, state GameTaskState, actors []string, current map[string]domaintask.Task) (domaintask.Task, error) {
	eligible := make([]domaintask.ActivityParticipant, 0, len(actors))
	for _, actorID := range actors {
		currentTask, assigned := current[actorID]
		if !assigned || currentTask.TaskID == taskID || currentTask.Activity != nil && currentTask.Activity.Passive && currentTask.Activity.Kind != domaintask.ActivityKindGame {
			participant, _ := typedGameParticipant(state.Spec, actorID)
			if !assigned || currentTask.TaskID != taskID {
				eligible = append(eligible, participant)
			}
		}
	}
	priorPassive := make(map[modulecore.TaskID]domaintask.Task)
	for _, actorID := range actors {
		currentTask, assigned := current[actorID]
		if assigned && currentTask.TaskID != taskID && currentTask.Activity != nil && currentTask.Activity.Passive && currentTask.Activity.Kind != domaintask.ActivityKindGame {
			priorPassive[currentTask.TaskID] = currentTask
		}
	}
	if len(eligible) > 0 {
		if _, err := m.setCurrentActivityInTransaction(ctx, taskID, domaintask.ActivityMetadata{
			Kind: domaintask.ActivityKindGame, Label: "Playing " + gameID, Phase: phase,
			Game: &domaintask.ActivityGameReference{GameID: gameID, SessionID: sessionID}, Participants: eligible,
		}); err != nil {
			return domaintask.Task{}, err
		}
	}
	writer, ok := m.store.(domaintask.ActivityTaskWriter)
	if !ok {
		return domaintask.Task{}, domaintask.ErrActivityTaskWriterUnsupported
	}
	result, err := writer.GetTaskActivity(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	if result.Activity == nil {
		result.Activity = &domaintask.ActivityMetadata{Kind: domaintask.ActivityKindGame, Revision: 0}
	} else {
		result.Activity = cloneGameActivityMetadata(result.Activity)
		if result.Activity.Kind != domaintask.ActivityKindGame || result.Activity.Game == nil || result.Activity.Game.GameID != gameID {
			return domaintask.Task{}, domaintask.ErrCurrentActivityConflict
		}
		if result.Activity.Game.SessionID != "" && result.Activity.Game.SessionID != sessionID {
			return domaintask.Task{}, domaintask.ErrCurrentActivityConflict
		}
	}
	participants := make(map[string]domaintask.ActivityParticipant, len(result.Activity.Participants)+len(actors))
	for _, participant := range result.Activity.Participants {
		participants[participant.ActorID] = participant
	}
	for _, actorID := range actors {
		participant, _ := typedGameParticipant(state.Spec, actorID)
		if priorParticipant, found := participants[actorID]; found {
			participant.IsCurrent = priorParticipant.IsCurrent
			participant.Revision = priorParticipant.Revision
		}
		if currentTask, assigned := current[actorID]; assigned && currentTask.TaskID == taskID {
			participant.IsCurrent = true
		}
		if !participant.IsCurrent && participant.Revision == 0 {
			participant.Revision = 1
		}
		participants[actorID] = participant
	}
	result.Activity.Kind = domaintask.ActivityKindGame
	result.Activity.Label = "Playing " + gameID
	result.Activity.Phase = phase
	result.Activity.Passive = false
	result.Activity.Game = &domaintask.ActivityGameReference{GameID: gameID, SessionID: sessionID}
	result.Activity.Participants = result.Activity.Participants[:0]
	for _, actorID := range actors {
		result.Activity.Participants = append(result.Activity.Participants, participants[actorID])
	}
	if result.Activity.Revision == ^uint64(0) {
		return domaintask.Task{}, errors.New("activity revision overflow")
	}
	result.Activity.Revision++
	if result.Activity.Revision == 0 {
		result.Activity.Revision = 1
	}
	result.UpdatedAt = m.now()
	if err := writer.SaveTaskActivity(ctx, result); err != nil {
		return domaintask.Task{}, err
	}
	for taskID, priorTask := range priorPassive {
		latest, err := writer.GetTaskActivity(ctx, taskID)
		if err != nil {
			return domaintask.Task{}, err
		}
		if activityHasCurrentParticipant(latest) {
			continue
		}
		summary := "passive activity superseded by a typed game activity"
		if priorTask.Activity.Kind == domaintask.ActivityKindEvaluating {
			summary = "activity evaluation superseded by a typed game activity"
		}
		if _, err := m.updateStatusInTransaction(ctx, taskID, domaintask.StatusCancelled, summary, "", nil); err != nil {
			return domaintask.Task{}, err
		}
	}
	return result, nil
}

func validateTypedGameActivityState(state GameTaskState, gameID, sessionID string) error {
	if state.Task.Route != domaintask.RouteGAME || domaintask.IsTerminal(state.Task.Status) ||
		state.Spec.GameID != gameID || state.Progress.SessionID != sessionID ||
		(sessionID == "" && state.Progress.Status != domaintask.GameExecutionLaunching && state.Progress.Status != domaintask.GameExecutionUnknown) ||
		(sessionID != "" && state.Progress.Status != domaintask.GameExecutionRunning && state.Progress.Status != domaintask.GameExecutionUnknown && state.Progress.Status != domaintask.GameExecutionLaunching) {
		return domaintask.ErrCurrentActivityConflict
	}
	if err := state.Progress.Validate(state.Task.TaskID, state.Spec); err != nil {
		return fmt.Errorf("typed GAME Task session is invalid: %w", err)
	}
	return nil
}

func gameActivityPhase(status domaintask.GameExecutionStatus) string {
	switch status {
	case domaintask.GameExecutionPrepared:
		return "preparing"
	case domaintask.GameExecutionLaunching:
		return "launching"
	case domaintask.GameExecutionRunning:
		return "playing"
	case domaintask.GameExecutionUnknown:
		return "unknown"
	default:
		return "evaluating"
	}
}

func gameActivityActors(spec domaintask.GameSpec) ([]string, error) {
	actors := append([]string(nil), spec.Personas...)
	sort.Strings(actors)
	if len(actors) == 0 {
		return nil, domaintask.ErrCurrentActivityInvalid
	}
	for index, actorID := range actors {
		if !canonicalActivityActor(actorID) || index > 0 && actors[index-1] == actorID {
			return nil, domaintask.ErrCurrentActivityInvalid
		}
	}
	return actors, nil
}

func typedGameParticipant(spec domaintask.GameSpec, actorID string) (domaintask.ActivityParticipant, bool) {
	for _, persona := range spec.Personas {
		if persona == actorID {
			return domaintask.ActivityParticipant{ActorID: actorID, Role: "player"}, true
		}
	}
	return domaintask.ActivityParticipant{}, false
}

func cloneGameActivityMetadata(activity *domaintask.ActivityMetadata) *domaintask.ActivityMetadata {
	if activity == nil {
		return nil
	}
	cloned := *activity
	cloned.Participants = append([]domaintask.ActivityParticipant(nil), activity.Participants...)
	if activity.Game != nil {
		game := *activity.Game
		cloned.Game = &game
	}
	return &cloned
}
