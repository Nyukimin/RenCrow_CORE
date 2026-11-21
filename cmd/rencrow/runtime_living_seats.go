package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type livingSeatActivity struct{ room, slot, pose string }

func livingSeatRoom(slot string) string {
	switch slot {
	case "dining", "dining-guest":
		return "dining"
	case "sofa", "sofa-guest":
		return "living"
	}
	if actor, ok := strings.CutPrefix(slot, "desk-"); ok && isAgentHeartbeatActor(actor) {
		return "living"
	}
	return ""
}

func (r *agentRoutineRuntime) livingSeats(ctx context.Context) (map[string]livingSeatActivity, bool, error) {
	if r == nil || r.deps == nil || r.deps.taskManager == nil {
		return nil, false, nil
	}
	activities, err := snapshotAgentCurrentActivities(ctx, r.deps.taskManager)
	if err != nil {
		return nil, false, fmt.Errorf("read canonical living activities: %w", err)
	}
	r.livingConversationMu.Lock()
	defer r.livingConversationMu.Unlock()
	return buildCanonicalLivingSeatTargets(activities, r.livingConversation), true, nil
}

func buildCanonicalLivingSeatTargets(
	activities map[string]domaintask.CurrentActivitySnapshot,
	conversations map[string]livingSeatActivity,
) map[string]livingSeatActivity {
	desired := make(map[string]livingSeatActivity)
	for actorID, snapshot := range activities {
		if snapshot.Task.Activity == nil || !snapshot.Participant.IsCurrent {
			continue
		}
		switch snapshot.Task.Activity.Kind {
		case domaintask.ActivityKindWork, domaintask.ActivityKindGame:
			desired[actorID] = livingSeatActivity{"living", "desk-" + actorID, "type"}
		case domaintask.ActivityKindChat:
			if seat, ok := conversations[actorID]; ok {
				desired[actorID] = seat
				continue
			}
			if partner := currentChatPartner(snapshot); partner != "" {
				slot := "sofa"
				if actorID > partner {
					slot = "sofa-guest"
				}
				desired[actorID] = livingSeatActivity{"living", slot, ""}
			}
		}
	}
	return desired
}

func currentChatPartner(snapshot domaintask.CurrentActivitySnapshot) string {
	if snapshot.Task.Activity == nil {
		return ""
	}
	const prefix = "chat-with-"
	if partner, ok := strings.CutPrefix(snapshot.Task.Activity.Label, prefix); ok && isAgentHeartbeatActor(partner) && partner != snapshot.Participant.ActorID {
		return partner
	}
	for _, participant := range snapshot.Task.Activity.Participants {
		if participant.ActorID != snapshot.Participant.ActorID && isAgentHeartbeatActor(participant.ActorID) {
			return participant.ActorID
		}
	}
	return ""
}

func buildLivingSeatTargets(sessions []gameObserverActiveAgentSession, activities []agentheartbeat.LivingActivity, conversations map[string]livingSeatActivity, runningTasks []domaintask.Task) map[string]livingSeatActivity {
	desired := make(map[string]livingSeatActivity)
	seatFallbackSuppressed := make(map[string]struct{}, len(activities))
	for _, activity := range activities {
		if activity.State == "stopping" || activity.State == "running" && activity.ActivityKind == string(agentheartbeat.ActivityKindRoutine) {
			seatFallbackSuppressed[activity.ActorID] = struct{}{}
		}
	}
	for _, task := range runningTasks {
		_, suppressFallback := seatFallbackSuppressed[task.Assignee]
		if task.Status == domaintask.StatusRunning && isAgentHeartbeatActor(task.Assignee) && !suppressFallback {
			desired[task.Assignee] = livingSeatActivity{"living", "desk-" + task.Assignee, "type"}
		}
	}
	for _, session := range sessions {
		for _, persona := range session.Personas {
			desired[persona] = livingSeatActivity{"living", "desk-" + persona, "type"}
		}
	}
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
		case "game":
			desired[id] = livingSeatActivity{"living", "desk-" + id, "type"}
		case "routine":
			if seat, ok := conversations[id]; ok {
				desired[id] = seat
			}
		}
	}
	return desired
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
	desired, ready, err := r.livingSeats(ctx)
	if err != nil || !ready {
		return
	}
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return
	}
	for _, actor := range read.snapshot.Actors {
		coreID, ok := r.worldActorIDs[actor.ActorID]
		if !ok || !actor.Present || !actor.HasPosition {
			continue
		}
		target := desired[coreID]
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
