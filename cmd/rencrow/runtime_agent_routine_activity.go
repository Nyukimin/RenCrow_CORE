package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

// CurrentRoutineActivity reads durable routine state and, for relaxation,
// verifies that WORLD still places the actor in the exact confirmed room.
// It never creates tasks or writes routine state.
func (r *agentRoutineRuntime) CurrentRoutineActivity(ctx context.Context, actorID string) (viewer.RoutineActivity, error) {
	values, err := r.CurrentRoutineActivities(ctx, []string{actorID})
	if err != nil {
		return viewer.RoutineActivity{}, err
	}
	value, ok := values[actorID]
	if !ok {
		return viewer.RoutineActivity{Availability: "unavailable"}, nil
	}
	return value, nil
}

// CurrentRoutineActivities batches durable-state reads and shares one fresh
// WORLD snapshot across actors whose live rest windows or private-room moves
// need verification.
func (r *agentRoutineRuntime) CurrentRoutineActivities(ctx context.Context, actorIDs []string) (map[string]viewer.RoutineActivity, error) {
	result := make(map[string]viewer.RoutineActivity, len(actorIDs))
	if r == nil || r.store == nil {
		for _, actorID := range actorIDs {
			result[actorID] = viewer.RoutineActivity{Availability: "disabled"}
		}
		return result, nil
	}
	now := r.now()
	restCandidates := make(map[string]agentroutine.RestWindow)
	moveCandidates := make(map[string][]routineWorldOperation)
	worldActorToCore := make(map[string]string)
	for _, actorID := range actorIDs {
		state, err := r.store.Load(ctx, actorID)
		if err != nil {
			result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
			continue
		}
		projection := viewer.RoutineActivity{Availability: "available", OngoingActivity: "waiting"}
		if state.Pending != nil {
			if privateRoom := r.routinePrivateRoom(actorID); privateRoom != "" {
				if operations := routinePendingPrivateHomeMoves(state, actorID, privateRoom); len(operations) > 0 {
					worldActorID, found := r.routineWorldActorID(actorID)
					if found {
						moveCandidates[actorID] = operations
						worldActorToCore[worldActorID] = actorID
					} else {
						result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
						continue
					}
				}
			}
			if routineSleepWindowLive(state, now) {
				projection.OngoingActivity = "sleep_window"
			}
			result[actorID] = projection
			continue
		}
		if routineSleepWindowLive(state, now) {
			projection.OngoingActivity = "sleep_window"
			result[actorID] = projection
			continue
		}
		if state.RestWindow == nil || !state.RestWindow.Until.After(now) || state.RestWindow.ObservedAt.After(now) || state.LastFinishedIntentID == "" {
			result[actorID] = projection
			continue
		}
		worldActorID, found := r.routineWorldActorID(actorID)
		if !r.routineRelaxRoomAllowed(actorID, state.RestWindow.RoomID) || !found || r.world == nil {
			result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
			continue
		}
		restCandidates[actorID] = *state.RestWindow
		worldActorToCore[worldActorID] = actorID
	}
	if len(restCandidates) == 0 && len(moveCandidates) == 0 {
		return result, nil
	}
	if r.world == nil {
		for actorID := range restCandidates {
			result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
		}
		for actorID := range moveCandidates {
			result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
		}
		return result, nil
	}
	snapshot, err := r.world.Snapshot(ctx)
	if err != nil {
		for actorID := range restCandidates {
			result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
		}
		for actorID := range moveCandidates {
			result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
		}
		return result, nil
	}
	foundActors := make(map[string]bool, len(restCandidates)+len(moveCandidates))
	for _, actor := range snapshot.Actors {
		actorID, relevant := worldActorToCore[actor.ActorID]
		if !relevant {
			continue
		}
		foundActors[actorID] = true
		if operations := moveCandidates[actorID]; len(operations) > 0 {
			for _, operation := range operations {
				if routineWorldMoveMatchesPrivateHome(snapshot, actor, operation) {
					result[actorID] = viewer.RoutineActivity{Availability: "available", OngoingActivity: "moving_home"}
					break
				}
			}
		}
		if _, isMoveCandidate := moveCandidates[actorID]; !isMoveCandidate {
			window := restCandidates[actorID]
			if actor.Present && actor.RoomID == window.RoomID && actor.ActivityID == "" && actor.ActivityKind == "" {
				result[actorID] = viewer.RoutineActivity{Availability: "available", OngoingActivity: "rest"}
			} else {
				result[actorID] = viewer.RoutineActivity{Availability: "available", OngoingActivity: "waiting"}
			}
		}
	}
	for actorID := range restCandidates {
		if !foundActors[actorID] {
			result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
		}
	}
	for actorID := range moveCandidates {
		if !foundActors[actorID] {
			result[actorID] = viewer.RoutineActivity{Availability: "unavailable"}
		}
	}
	return result, nil
}

func (r *agentRoutineRuntime) routinePrivateRoom(actorID string) string {
	if r == nil || r.cfg == nil {
		return ""
	}
	return r.cfg.Heartbeat.Routine.PrivateRooms[actorID]
}

// finishRoutineIntent is the success-path finish hook for routine work. Only a
// confirmed relaxation in an allowed room receives a durable rest interval.
func (r *agentRoutineRuntime) finishRoutineIntent(ctx context.Context, state agentroutine.State, progress routineProgress) (agentroutine.State, error) {
	if state.Pending == nil {
		return agentroutine.State{}, agentroutine.ErrIntentNotFound
	}
	intent := *state.Pending
	retryAfter := r.now().Add(routineCooldown(intent.Kind))
	rest, eligible := r.routineRestWindow(state, intent, progress)
	if !eligible {
		return r.store.FinishIntent(ctx, intent.ActorID, intent.ID, state.Revision, retryAfter)
	}
	retryAfter = rest.Until
	return r.store.FinishIntentWithRestWindow(ctx, intent.ActorID, intent.ID, state.Revision, retryAfter, rest)
}

func (r *agentRoutineRuntime) routineRestWindow(state agentroutine.State, intent agentroutine.Intent, progress routineProgress) (agentroutine.RestWindow, bool) {
	if intent.Kind != agentroutine.ActivityRelax || progress.World == nil || progress.World.RelaxObservedAt.IsZero() ||
		progress.World.RelaxRoomID == "" || progress.CompletedAt.IsZero() || r == nil || !r.routineRelaxRoomAllowed(intent.ActorID, progress.World.RelaxRoomID) {
		return agentroutine.RestWindow{}, false
	}
	until := progress.CompletedAt.Add(routineCooldown(agentroutine.ActivityRelax))
	if state.PlannedSleep.After(progress.CompletedAt) && state.PlannedSleep.Before(until) {
		until = state.PlannedSleep
	}
	if !until.After(progress.World.RelaxObservedAt) {
		return agentroutine.RestWindow{}, false
	}
	return agentroutine.RestWindow{
		ObservedAt: progress.World.RelaxObservedAt,
		Until:      until,
		RoomID:     progress.World.RelaxRoomID,
	}, true
}

func (r *agentRoutineRuntime) routineRelaxRoomAllowed(actorID, roomID string) bool {
	if r == nil || r.cfg == nil || roomID == "" {
		return false
	}
	if r.cfg.Heartbeat.Routine.PrivateRooms[actorID] == roomID {
		return true
	}
	for _, sharedRoom := range r.cfg.Heartbeat.Routine.SharedRooms {
		if sharedRoom == roomID {
			return true
		}
	}
	return false
}

func routineSleepWindowLive(state agentroutine.State, now time.Time) bool {
	if state.ScheduleDate == "" || state.PlannedSleep.IsZero() || state.PlannedWake.IsZero() ||
		!state.LogicalSleepUntil.Equal(state.PlannedWake) || !now.Before(state.LogicalSleepUntil) || now.Before(state.PlannedSleep) {
		return false
	}
	sleepAt, wakeAt, err := agentroutine.ScheduleFor(state.ActorID, state.ScheduleDate)
	return err == nil && state.PlannedSleep.Equal(sleepAt) && state.PlannedWake.Equal(wakeAt) &&
		now.In(wakeAt.Location()).Format("2006-01-02") == state.ScheduleDate
}
