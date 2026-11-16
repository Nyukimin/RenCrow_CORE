package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

type livingSeatActivity struct{ room, slot, pose string }

var livingRestSeatLayout = [...]livingSeatActivity{
	{room: "living", slot: "sofa"},
	{room: "living", slot: "sofa-guest"},
	{room: "dining", slot: "dining"},
	{room: "dining", slot: "dining-guest"},
	{room: "dining", slot: "dining-aisle-north"},
	{room: "dining", slot: "dining-aisle-south"},
	{room: "dining", slot: "dining-window-north"},
	{room: "dining", slot: "dining-window-south"},
}

// livingRestSeatCandidates orders shared seats per actor and filters current
// occupants plus WORLD's in-transit target-slot reservations. WORLD still owns
// the atomic reservation decision when the caller issues life.home.move.
func livingRestSeatCandidates(snapshot worldread.Snapshot, actorID string) []livingSeatActivity {
	if actorID == "" {
		return nil
	}
	blocked := make(map[string]struct{}, len(livingRestSeatLayout))
	var ownerSeat livingSeatActivity
	hasOwnerSeat := false
	var ownerTarget livingSeatActivity
	hasOwnerTarget := false

	for _, actor := range snapshot.Actors {
		if !actor.Present || actor.ActorID == "" {
			continue
		}
		if seat, ok := livingRestSeatBySlot(actor.SlotID); ok {
			blocked[seat.slot] = struct{}{}
			if actor.ActorID == actorID && actor.ActivityKind != "move" && actor.RoomID == seat.room {
				ownerSeat, hasOwnerSeat = seat, true
			}
		}
		if seat, ok := livingRestSeatBySlot(actor.TargetSlotID); ok {
			blocked[seat.slot] = struct{}{}
		}
		if actor.ActorID == actorID && actor.ActivityKind == "move" && actor.ActivityID != "" {
			if seat, ok := livingRestSeatAt(actor.TargetRoomID, actor.TargetSlotID); ok {
				ownerTarget, hasOwnerTarget = seat, true
			}
		}
	}

	candidates := make([]livingSeatActivity, 0, len(livingRestSeatLayout))
	if hasOwnerTarget {
		candidates = append(candidates, ownerTarget)
	} else if hasOwnerSeat {
		candidates = append(candidates, ownerSeat)
	}
	start := livingRestSeatActorOffset(actorID)
	for offset := range livingRestSeatLayout {
		seat := livingRestSeatLayout[(start+offset)%len(livingRestSeatLayout)]
		if _, occupied := blocked[seat.slot]; occupied {
			continue
		}
		blocked[seat.slot] = struct{}{}
		candidates = append(candidates, seat)
	}
	return candidates
}

func livingRestSeatActorOffset(actorID string) int {
	const (
		offsetBasis = uint32(2166136261)
		prime       = uint32(16777619)
	)
	hash := offsetBasis
	for i := 0; i < len(actorID); i++ {
		hash ^= uint32(actorID[i])
		hash *= prime
	}
	return int(hash % uint32(len(livingRestSeatLayout)))
}

func livingRestSeatBySlot(slotID string) (livingSeatActivity, bool) {
	for _, seat := range livingRestSeatLayout {
		if seat.slot == slotID {
			return seat, true
		}
	}
	return livingSeatActivity{}, false
}

func livingRestSeatAt(roomID, slotID string) (livingSeatActivity, bool) {
	seat, ok := livingRestSeatBySlot(slotID)
	return seat, ok && seat.room == roomID
}

func livingSeatRoom(slot string) string {
	switch slot {
	case "dining", "dining-guest", "dining-aisle-north", "dining-aisle-south", "dining-window-north", "dining-window-south":
		return "dining"
	case "sofa", "sofa-guest":
		return "living"
	}
	if actor, ok := strings.CutPrefix(slot, "desk-"); ok && isAgentHeartbeatActor(actor) {
		return "living"
	}
	return ""
}

func (r *agentRoutineRuntime) livingSeats() (map[string]livingSeatActivity, bool) {
	if r == nil || r.world == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return nil, false
	}
	desired, _, ready := r.livingSeatsWithSnapshot(ctx, read.snapshot)
	return desired, ready
}

func (r *agentRoutineRuntime) livingSeatsWithSnapshot(ctx context.Context, snapshot worldread.Snapshot) (map[string]livingSeatActivity, map[string]livingSeatActivity, bool) {
	if r.deps == nil || r.deps.agentHeartbeat == nil {
		return nil, nil, false
	}
	activities, ready := r.deps.agentHeartbeat.LivingSnapshot(ctx, nil)
	if !ready {
		return nil, nil, false
	}
	desired := make(map[string]livingSeatActivity)
	r.livingConversationMu.Lock()
	for _, activity := range activities {
		if activity.State != "running" {
			continue
		}
		id := activity.ActorID
		switch activity.ActivityKind {
		case "private", "board":
			pose := "type"
			switch {
			case activity.ActivityKind == "board", id == "kuro", id == "kin":
				pose = "review"
			case id == "midori", id == "gin":
				pose = "notes"
			}
			desired[id] = livingSeatActivity{"living", "desk-" + id, pose}
		case "routine":
			if seat, ok := r.livingConversation[id]; ok {
				desired[id] = seat
			}
		}
	}
	r.livingConversationMu.Unlock()

	relevant := make(map[string]worldread.Actor)
	for _, actor := range snapshot.Actors {
		coreID, ok := r.worldActorIDs[actor.ActorID]
		if !ok || !actor.Present {
			continue
		}
		if r.routineSeatReconcileRelevant(actor) {
			relevant[coreID] = actor
		}
	}
	states := make(map[string]agentroutine.State, len(relevant))
	for actorID := range relevant {
		state, err := r.store.Load(ctx, actorID)
		if err == nil {
			states[actorID] = state
		}
	}
	pending := make(map[string]livingSeatActivity)
	for _, state := range states {
		if state.Pending == nil || state.Pending.Kind != agentroutine.ActivityRelax {
			continue
		}
		progress, err := decodeRoutineProgress(state.Pending.Output)
		if err != nil || progress.Rest == nil || progress.Rest.Scope != agentroutine.RelaxRoomShared {
			continue
		}
		for actorID, checkpoint := range progress.Rest.Seats {
			if checkpoint.RoomID != "" && checkpoint.SlotID != "" {
				pending[actorID] = livingSeatActivity{room: checkpoint.RoomID, slot: checkpoint.SlotID}
			}
			if checkpoint.Confirmed && desired[actorID].slot == "" &&
				routineRestSeatOccupiedByActor(snapshot, r.worldActorIDFor(actorID), checkpoint) {
				desired[actorID] = livingSeatActivity{room: checkpoint.RoomID, slot: checkpoint.SlotID}
			}
		}
	}
	// A completed routine's recorded seat remains held while the same fresh
	// snapshot still shows the actor idle there. Higher-priority work wins.
	for actorID, actor := range relevant {
		if desired[actorID].slot != "" {
			continue
		}
		state, ok := states[actorID]
		if !ok || state.Pending != nil {
			continue
		}
		seat, found := r.persistedRoutineRestSeatFromState(ctx, state, actorID)
		if found && routineRestSeatOccupiedByActor(snapshot, actor.ActorID,
			routineRestSeatCheckpoint{RoomID: seat.room, SlotID: seat.slot}) {
			desired[actorID] = seat
		}
	}
	return desired, pending, true
}

func (r *agentRoutineRuntime) routineSeatReconcileRelevant(actor worldread.Actor) bool {
	if livingSeatRoom(actor.SlotID) != "" || livingSeatRoom(actor.TargetSlotID) != "" {
		return true
	}
	for _, room := range r.cfg.Heartbeat.Routine.SharedRooms {
		if actor.RoomID == room || actor.TargetRoomID == room {
			return true
		}
	}
	return false
}

func (r *agentRoutineRuntime) showLivingConversation(first, second, room string) func() {
	if room != "living" && room != "dining" {
		return func() {}
	}
	slot := "sofa"
	if room == "dining" {
		slot = "dining"
	}
	r.livingConversationMu.Lock()
	if r.livingConversation == nil {
		r.livingConversation = make(map[string]livingSeatActivity)
	}
	r.livingConversation[first] = livingSeatActivity{room, slot, ""}
	r.livingConversation[second] = livingSeatActivity{room, slot + "-guest", ""}
	r.livingConversationMu.Unlock()
	return func() {
		r.livingConversationMu.Lock()
		delete(r.livingConversation, first)
		delete(r.livingConversation, second)
		r.livingConversationMu.Unlock()
	}
}

func (r *agentRoutineRuntime) readyLivingConversation(ctx context.Context, first, second string) error {
	wait, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.livingConversationMu.Lock()
		one, two := r.livingConversation[first], r.livingConversation[second]
		r.livingConversationMu.Unlock()
		if one.slot == "" || two.slot == "" {
			return nil
		}
		_, a, aErr := r.routineWorldFreshActor(wait, first)
		_, b, bErr := r.routineWorldFreshActor(wait, second)
		if aErr == nil && bErr == nil && a.Present && b.Present &&
			a.SlotID == one.slot && b.SlotID == two.slot &&
			a.ActivityID == "" && b.ActivityID == "" {
			return nil
		}
		select {
		case <-wait.Done():
			return wait.Err()
		case <-ticker.C:
		}
	}
}

func (r *agentRoutineRuntime) livingConversationSpeaking(actorID string, speaking bool) {
	r.livingConversationMu.Lock()
	defer r.livingConversationMu.Unlock()
	seat, ok := r.livingConversation[actorID]
	if !ok {
		return
	}
	seat.pose = ""
	if speaking {
		seat.pose = "talk"
	}
	r.livingConversation[actorID] = seat
}

func (r *agentRoutineRuntime) runLivingSeats(ctx context.Context) {
	if r.worldLife == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick, cancel := context.WithTimeout(ctx, 4*time.Second)
			r.reconcileLivingSeats(tick)
			cancel()
		}
	}
}

func (r *agentRoutineRuntime) reconcileLivingSeats(ctx context.Context) {
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return
	}
	desired, pendingSeats, ready := r.livingSeatsWithSnapshot(ctx, read.snapshot)
	if !ready {
		return
	}
	for _, actor := range read.snapshot.Actors {
		coreID, ok := r.worldActorIDs[actor.ActorID]
		if !ok || !actor.Present || !actor.HasPosition {
			continue
		}
		target := desired[coreID]
		pendingSeat, pendingRest := pendingSeats[coreID]
		if shouldDeferRestSeatReconcile(actor, target, pendingSeat, pendingRest) {
			continue // The rest Task owns this target until an ordinary move takes precedence.
		}
		sourceRoom := livingSeatRoom(actor.SlotID)
		action := worldread.HomeActionMove
		payload := worldread.HomeActionPayload{ActorID: actor.ActorID}
		key := fmt.Sprintf("living-seat:%s:%d", actor.ActorID, r.worldNowUTC().Unix())
		if actor.ActivityID != "" {
			seatMove := livingSeatRoom(actor.TargetSlotID) != "" ||
				(sourceRoom != "" && actor.TargetSlotID == "" && actor.TargetRoomID == sourceRoom)
			if actor.ActivityKind != "move" || !seatMove || actor.ActivityDueAt.IsZero() || r.worldNowUTC().Before(actor.ActivityDueAt) {
				continue
			}
			action = worldread.HomeActionFinish
			payload.ActivityID = actor.ActivityID
			key = "living-finish:" + actor.ActivityID
		} else if sourceRoom != "" && actor.SlotID != target.slot {
			payload.RoomID = sourceRoom
		} else if sourceRoom == "" && target.slot != "" {
			payload.RoomID = target.room
			payload.SlotID = target.slot
		} else {
			continue
		}
		_, _ = r.worldLife.Command(ctx, key, action, payload)
	}
}

func shouldDeferRestSeatReconcile(actor worldread.Actor, target, pendingSeat livingSeatActivity, pending bool) bool {
	if !pending {
		return false
	}
	if target.slot == "" || (target.room == pendingSeat.room && target.slot == pendingSeat.slot) {
		return true
	}
	return actor.ActivityKind == "move" &&
		actor.TargetRoomID == pendingSeat.room && actor.TargetSlotID == pendingSeat.slot
}
