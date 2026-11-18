package main

import (
	"context"
	"errors"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

var errRoutineRestUnavailable = errors.New("authenticated WORLD room observation does not permit in-place rest")
var errRoutineRestMoveRequired = errors.New("rest requires arrival in a configured room")

func (r *agentRoutineRuntime) executeRelaxation(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if progress.World != nil && len(progress.World.Operations) > 0 {
		return r.executeRoutineWorldActivity(ctx, stop, state, progress)
	}
	confirmedState, confirmedProgress, err := r.confirmInPlaceRelaxation(ctx, state, progress)
	if errors.Is(err, errRoutineRestMoveRequired) && r.worldLife != nil {
		return r.executeRoutineWorldActivity(ctx, stop, state, progress)
	}
	return confirmedState, confirmedProgress, err
}

// confirmInPlaceRelaxation starts quiet time where the actor already is. A
// reader-only WORLD binding permits this observation, but never a room move.
func (r *agentRoutineRuntime) confirmInPlaceRelaxation(ctx context.Context, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if r.world == nil || state.Pending == nil {
		return state, progress, errRoutineRestUnavailable
	}
	fresh, now, err := r.planFresh(ctx, state.Pending.ActorID, true, nil)
	if err != nil {
		return state, progress, err
	}
	if fresh.Kind != agentroutine.ActivityRelax {
		return state, progress, errRoutineRestUnavailable
	}
	worldActorID, ok := r.routineWorldActorID(state.Pending.ActorID)
	if !ok {
		return state, progress, errRoutineRestUnavailable
	}
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return state, progress, err
	}
	for _, actor := range read.snapshot.Actors {
		if actor.ActorID != worldActorID {
			continue
		}
		if !actor.Present || actor.ActivityID != "" || actor.ActivityKind != "" {
			return state, progress, errRoutineRestUnavailable
		}
		roomAllowed := r.routineRelaxRoomAllowed(state.Pending.ActorID, actor.RoomID)
		if now.Hour() < 9 || now.Hour() >= 22 {
			roomAllowed = actor.RoomID == r.routineWorldRelaxRoom(state.Pending.ActorID, fresh.RelaxRoom)
		}
		if !roomAllowed {
			return state, progress, errRoutineRestMoveRequired
		}
		if progress.World == nil {
			progress.World = &routineWorldProgress{}
		}
		progress.World.RelaxRoomID = actor.RoomID
		progress.World.RelaxObservedAt = read.readAt.UTC()
		progress.CompletedAt = read.readAt.UTC()
		progress.EffectCompleted = true
		return r.saveProgress(ctx, state, "relaxation_room_confirmed", progress)
	}
	return state, progress, errRoutineRestUnavailable
}
