package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

const routineWorldMoveWait = 5 * time.Minute

var (
	errRoutineWorldUnavailable = errors.New("authenticated WORLD life actions are unavailable")
	errRoutineWorldNotDue      = errors.New("WORLD activity is still in progress")
	errRoutineWorldBusy        = errors.New("WORLD Actor has an unrelated active activity")
)

var routineMirrorPoseIDs = [...]string{
	"mirror-greeting", "mirror-hero", "mirror-side", "mirror-focus",
	"mirror-hands-on-hips", "mirror-over-shoulder", "mirror-open-arms", "mirror-bow",
}

type routineWorldProgress struct {
	Operations              []routineWorldOperation   `json:"operations"`
	MorningRoom             string                    `json:"morning_room,omitempty"`
	RelaxRoomID             string                    `json:"relax_room_id,omitempty"`
	RelaxObservedAt         time.Time                 `json:"relax_observed_at,omitempty"`
	SleepStart              *routineWorldSleepBinding `json:"sleep_start,omitempty"`
	MirrorPoseID            string                    `json:"mirror_pose_id,omitempty"`
	MirrorCompletionBase    uint64                    `json:"mirror_completion_base,omitempty"`
	MirrorCompletionBaseSet bool                      `json:"mirror_completion_base_set,omitempty"`
	NextDueAt               time.Time                 `json:"next_due_at,omitempty"`
}

type routineWorldSleepBinding struct {
	ActorID       string    `json:"actor_id"`
	WorldActorID  string    `json:"world_actor_id"`
	ActivityID    string    `json:"activity_id"`
	RoomID        string    `json:"room_id"`
	SlotID        string    `json:"slot_id"`
	StartedAt     time.Time `json:"started_at"`
	EventID       string    `json:"event_id"`
	TransactionID string    `json:"transaction_id"`
	Seq           uint64    `json:"seq"`
	LayoutSHA256  string    `json:"layout_sha256"`
	SceneSHA256   string    `json:"scene_sha256"`
}

type routineWorldOperation struct {
	Purpose           string                      `json:"purpose"`
	Key               string                      `json:"key"`
	Action            string                      `json:"action"`
	EventKind         string                      `json:"event_kind"`
	ActorID           string                      `json:"actor_id"`
	Payload           worldread.HomeActionPayload `json:"payload"`
	ReceiptID         string                      `json:"receipt_id,omitempty"`
	ReceiptStatus     string                      `json:"receipt_status,omitempty"`
	ReceiptReason     string                      `json:"receipt_reason,omitempty"`
	TransactionID     string                      `json:"transaction_id,omitempty"`
	EventIDs          []string                    `json:"event_ids,omitempty"`
	FirstSeq          uint64                      `json:"first_seq,omitempty"`
	LastSeq           uint64                      `json:"last_seq,omitempty"`
	StateSeq          uint64                      `json:"state_seq,omitempty"`
	RecordedAt        time.Time                   `json:"recorded_at,omitempty"`
	EventVerified     bool                        `json:"event_verified,omitempty"`
	ActivityID        string                      `json:"activity_id,omitempty"`
	ActivityKind      string                      `json:"activity_kind,omitempty"`
	RoomID            string                      `json:"room_id,omitempty"`
	TargetRoomID      string                      `json:"target_room_id,omitempty"`
	SlotID            string                      `json:"slot_id,omitempty"`
	PoseID            string                      `json:"pose_id,omitempty"`
	RouteID           string                      `json:"route_id,omitempty"`
	LayoutSHA256      string                      `json:"layout_sha256,omitempty"`
	SceneSHA256       string                      `json:"scene_sha256,omitempty"`
	StartedAt         time.Time                   `json:"started_at,omitempty"`
	DueAt             time.Time                   `json:"due_at,omitempty"`
	EndedAt           time.Time                   `json:"ended_at,omitempty"`
	MirrorCompletions uint64                      `json:"mirror_pose_completions,omitempty"`
}

func (r *agentRoutineRuntime) routineWorldActorID(actorID string) (string, bool) {
	for worldActorID, coreActorID := range r.worldActorIDs {
		if coreActorID == actorID {
			return worldActorID, true
		}
	}
	return "", false
}

func routineWorldHomeHobby(actorID, label string) bool {
	return (actorID == "shiro" && label == "fitness_research") ||
		(actorID == "kuro" && label == "creative_mirror_ideas")
}

func (r *agentRoutineRuntime) routineWorldPreferredSharedRoom() string {
	rooms := append([]string(nil), r.cfg.Heartbeat.Routine.SharedRooms...)
	for _, room := range rooms {
		if room == "living" {
			return room
		}
	}
	if len(rooms) == 0 {
		return ""
	}
	sortRoutineStrings(rooms)
	return rooms[0]
}

func sortRoutineStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (r *agentRoutineRuntime) routineWorldTargetRoom(actorID string) string {
	if actorID == "mio" {
		for _, room := range r.cfg.Heartbeat.Routine.SharedRooms {
			if room == "living" {
				return room
			}
		}
	}
	return r.cfg.Heartbeat.Routine.PrivateRooms[actorID]
}

func routineWorldOperationKey(intentID, actorID, purpose string) string {
	digest := sha256.Sum256([]byte(intentID + "\x00" + actorID + "\x00" + purpose))
	return "routine-world-" + hex.EncodeToString(digest[:16])
}

func (r *agentRoutineRuntime) routineWorldOperation(progress routineProgress, purpose string) (int, bool) {
	if progress.World == nil {
		return -1, false
	}
	for index := len(progress.World.Operations) - 1; index >= 0; index-- {
		candidate := progress.World.Operations[index].Purpose
		if candidate == purpose || strings.HasPrefix(candidate, purpose+":retry:") {
			return index, true
		}
	}
	return -1, false
}

func routineWorldExactOperation(progress routineProgress, purpose string) (int, bool) {
	if progress.World == nil {
		return -1, false
	}
	for index := len(progress.World.Operations) - 1; index >= 0; index-- {
		if progress.World.Operations[index].Purpose == purpose {
			return index, true
		}
	}
	return -1, false
}

func routineWorldActivityWasConfirmedStopped(progress routineProgress, actorID, activityID string) bool {
	if progress.World == nil || actorID == "" || activityID == "" {
		return false
	}
	for _, operation := range progress.World.Operations {
		if operation.Action == worldread.HomeActionStop && operation.EventKind == "life.home.activity.stopped" &&
			operation.ActorID == actorID && operation.Payload.ActivityID == activityID &&
			operation.ReceiptStatus == "committed" && operation.EventVerified {
			return true
		}
	}
	return false
}

func routineWorldActivityWasConfirmedFinished(progress routineProgress, actorID, activityID string) (routineWorldOperation, bool) {
	if progress.World == nil || actorID == "" || activityID == "" {
		return routineWorldOperation{}, false
	}
	for _, operation := range progress.World.Operations {
		if operation.Action == worldread.HomeActionFinish && operation.ActorID == actorID &&
			operation.Payload.ActivityID == activityID && operation.ReceiptStatus == "committed" && operation.EventVerified {
			return operation, true
		}
	}
	return routineWorldOperation{}, false
}

func routineWorldRetryPurpose(progress routineProgress, base string) string {
	attempt := 1
	if progress.World != nil {
		for _, operation := range progress.World.Operations {
			if !strings.HasPrefix(operation.Purpose, base+":retry:") {
				continue
			}
			var current int
			if _, err := fmt.Sscanf(strings.TrimPrefix(operation.Purpose, base+":retry:"), "%d", &current); err == nil && current >= attempt {
				attempt = current + 1
			}
		}
	}
	return fmt.Sprintf("%s:retry:%d", base, attempt)
}

func (r *agentRoutineRuntime) performRoutineWorldAction(
	ctx context.Context,
	state agentroutine.State,
	progress routineProgress,
	purpose string,
	action string,
	eventKind string,
	payload worldread.HomeActionPayload,
) (agentroutine.State, routineProgress, routineWorldOperation, error) {
	if r == nil || r.world == nil || r.worldLife == nil || state.Pending == nil {
		return state, progress, routineWorldOperation{}, errRoutineWorldUnavailable
	}
	if progress.World == nil {
		progress.World = &routineWorldProgress{}
	}
	index, found := r.routineWorldOperation(progress, purpose)
	if found && progress.World.Operations[index].ReceiptStatus == "rejected" {
		if progress.World.Operations[index].ReceiptReason != "resource_conflict" {
			return state, progress, progress.World.Operations[index], worldread.ErrCommandRejected
		}
		purpose = routineWorldRetryPurpose(progress, purpose)
		index, found = routineWorldExactOperation(progress, purpose)
	}
	if !found {
		operation := routineWorldOperation{
			Purpose: purpose, Key: routineWorldOperationKey(state.Pending.ID, payload.ActorID, purpose),
			Action: action, EventKind: eventKind, ActorID: payload.ActorID, Payload: payload,
		}
		progress.World.Operations = append(progress.World.Operations, operation)
		index = len(progress.World.Operations) - 1
		var err error
		state, progress, err = r.saveProgress(ctx, state, "world_action_prepared", progress)
		if err != nil {
			return state, progress, routineWorldOperation{}, err
		}
	} else {
		operation := progress.World.Operations[index]
		if operation.Action != action || operation.EventKind != eventKind || operation.ActorID != payload.ActorID || operation.Payload != payload {
			return state, progress, routineWorldOperation{}, errors.New("persisted WORLD operation scope changed")
		}
	}
	operation := progress.World.Operations[index]
	if operation.ReceiptStatus == "rejected" {
		return state, progress, operation, worldread.ErrCommandRejected
	}
	if operation.ReceiptID == "" {
		result, commandErr := r.worldLife.Command(ctx, operation.Key, operation.Action, operation.Payload)
		receipt := result.Receipt
		if receipt.ReceiptID != "" && receipt.Status != "retry" {
			operation.ReceiptID = receipt.ReceiptID
			operation.ReceiptStatus = receipt.Status
			operation.ReceiptReason = receipt.Reason
			operation.TransactionID = receipt.TransactionID
			operation.EventIDs = append([]string(nil), receipt.EventIDs...)
			operation.FirstSeq, operation.LastSeq, operation.StateSeq = receipt.FirstSeq, receipt.LastSeq, receipt.StateSeq
			operation.RecordedAt = receipt.RecordedAt.UTC()
			progress.World.Operations[index] = operation
			var saveErr error
			state, progress, saveErr = r.saveProgress(ctx, state, "world_receipt_saved", progress)
			if saveErr != nil {
				return state, progress, operation, saveErr
			}
		}
		if commandErr != nil {
			return state, progress, operation, commandErr
		}
	}
	operation = progress.World.Operations[index]
	if operation.ReceiptStatus != "committed" || operation.ReceiptID == "" {
		return state, progress, operation, errors.New("WORLD action has no committed durable receipt")
	}
	if !operation.EventVerified {
		event, fields, err := r.routineWorldReceiptEvent(ctx, operation)
		if err != nil {
			return state, progress, operation, err
		}
		operation.ActivityID, _ = routineWorldString(fields, "activity_id")
		operation.ActivityKind, _ = routineWorldString(fields, "activity_kind")
		operation.RoomID, _ = routineWorldString(fields, "room_id")
		operation.TargetRoomID, _ = routineWorldString(fields, "target_room_id")
		operation.SlotID, _ = routineWorldString(fields, "slot_id")
		operation.PoseID, _ = routineWorldString(fields, "pose_id")
		operation.RouteID, _ = routineWorldString(fields, "route_id")
		operation.LayoutSHA256, _ = routineWorldString(fields, "layout_sha256")
		operation.SceneSHA256, _ = routineWorldString(fields, "scene_sha256")
		operation.StartedAt = routineWorldTime(fields, "started_at")
		operation.DueAt = routineWorldTime(fields, "due_at")
		operation.EndedAt = routineWorldTime(fields, "ended_at")
		operation.MirrorCompletions = routineWorldUint(fields, "mirror_pose_completions")
		if event.RecordedAt.IsZero() {
			return state, progress, operation, errors.New("WORLD lifecycle event has no server timestamp")
		}
		if operation.Action == worldread.HomeActionMove && operation.EventKind == "life.home.move.started" && operation.RouteID == "" {
			return state, progress, operation, errors.New("WORLD move receipt has no route binding")
		}
		if operation.EventKindHasScene() && !routineWorldDigest(operation.SceneSHA256) {
			return state, progress, operation, errors.New("WORLD lifecycle event has no valid pinned scene digest")
		}
		if operation.EventKindHasLayout() && !routineWorldDigest(operation.LayoutSHA256) {
			return state, progress, operation, errors.New("WORLD lifecycle event has no valid layout digest")
		}
		if operation.Action == worldread.HomeActionFinish || operation.Action == worldread.HomeActionWake || operation.Action == worldread.HomeActionStop {
			if err := r.verifyRoutineWorldActivityBinding(progress, operation); err != nil {
				return state, progress, operation, err
			}
		}
		operation.EventVerified = true
		progress.World.Operations[index] = operation
		state, progress, err = r.saveProgress(ctx, state, "world_event_verified", progress)
		if err != nil {
			return state, progress, operation, err
		}
	}
	return state, progress, operation, nil
}

func (o routineWorldOperation) EventKindHasScene() bool {
	return strings.HasPrefix(o.EventKind, "life.home.")
}

func (o routineWorldOperation) EventKindHasLayout() bool {
	return strings.HasPrefix(o.EventKind, "life.home.")
}

func (r *agentRoutineRuntime) routineWorldReceiptEvent(ctx context.Context, operation routineWorldOperation) (worldread.Event, map[string]json.RawMessage, error) {
	if len(operation.EventIDs) == 0 || operation.FirstSeq == 0 || operation.StateSeq < operation.LastSeq {
		return worldread.Event{}, nil, errors.New("WORLD receipt has no bounded event range")
	}
	batch, err := r.world.Events(ctx, operation.FirstSeq-1)
	if err != nil {
		return worldread.Event{}, nil, fmt.Errorf("read WORLD receipt events: %w", err)
	}
	if batch.NextSeq < operation.LastSeq {
		return worldread.Event{}, nil, errors.New("WORLD event page does not include the complete receipt transaction")
	}
	worldActorID, ok := r.routineWorldActorID(operation.ActorID)
	if !ok {
		return worldread.Event{}, nil, errors.New("WORLD event actor is outside the configured CORE binding")
	}
	wanted := make(map[string]struct{}, len(operation.EventIDs))
	for _, eventID := range operation.EventIDs {
		wanted[eventID] = struct{}{}
	}
	var selected worldread.Event
	var selectedFields map[string]json.RawMessage
	found := make(map[string]struct{}, len(wanted))
	for _, event := range batch.Events {
		if _, relevant := wanted[event.EventID]; !relevant {
			continue
		}
		if event.TransactionID != operation.TransactionID || event.Kind != operation.EventKind {
			return worldread.Event{}, nil, errors.New("WORLD receipt EventID does not match its committed transaction and lifecycle kind")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(event.Payload, &fields); err != nil {
			return worldread.Event{}, nil, errors.New("WORLD lifecycle Event payload is not an object")
		}
		actorID, ok := routineWorldString(fields, "actor_id")
		if !ok || actorID != worldActorID {
			return worldread.Event{}, nil, errors.New("WORLD lifecycle Event actor does not match the exact configured Actor")
		}
		if operation.Payload.ActivityID != "" {
			activityID, ok := routineWorldString(fields, "activity_id")
			if !ok || activityID != operation.Payload.ActivityID {
				return worldread.Event{}, nil, errors.New("WORLD lifecycle Event does not match the exact active activity instance")
			}
		}
		found[event.EventID] = struct{}{}
		selected, selectedFields = event, fields
	}
	if len(found) != len(wanted) || selected.EventID == "" {
		return worldread.Event{}, nil, errors.New("WORLD receipt EventIDs are not present in the authenticated event page")
	}
	return selected, selectedFields, nil
}

func routineWorldSleepBindingFromFeed(feed worldread.Feed, current worldread.Snapshot, actorID, worldActorID, roomID, slotID string, active worldread.Actor) (routineWorldSleepBinding, error) {
	if actorID == "" || worldActorID == "" || roomID == "" || slotID == "" || !active.Present || active.ActivityID == "" ||
		active.ActivityKind != "sleep" || active.RoomID != roomID || active.SlotID != slotID || active.ActivityStartedAt.IsZero() {
		return routineWorldSleepBinding{}, errors.New("fresh WORLD snapshot does not confirm the exact active bed sleep")
	}
	if feed.Snapshot.WorldID == "" || feed.Snapshot.WorldID != current.WorldID || feed.Snapshot.TimelineID != current.TimelineID ||
		feed.Snapshot.TimelineEpoch != current.TimelineEpoch || feed.Snapshot.StateSeq < current.StateSeq || feed.Cursor < feed.Snapshot.StateSeq {
		return routineWorldSleepBinding{}, errors.New("authenticated WORLD feed does not cover the current sleep snapshot scope")
	}
	var feedActor worldread.Actor
	feedActorFound := false
	for _, candidate := range feed.Snapshot.Actors {
		if candidate.ActorID == worldActorID && candidate.Present {
			feedActor, feedActorFound = candidate, true
			break
		}
	}
	if !feedActorFound || feedActor.ActivityID != active.ActivityID || feedActor.ActivityKind != "sleep" ||
		feedActor.RoomID != roomID || feedActor.SlotID != slotID || !feedActor.ActivityStartedAt.Equal(active.ActivityStartedAt) {
		return routineWorldSleepBinding{}, errors.New("authenticated WORLD feed snapshot changed the exact active sleep instance")
	}
	if active.HomeSceneSHA256 != "" && feedActor.HomeSceneSHA256 != active.HomeSceneSHA256 {
		return routineWorldSleepBinding{}, errors.New("authenticated WORLD feed snapshot changed the sleep scene binding")
	}
	var binding routineWorldSleepBinding
	found := false
	for _, event := range feed.Events {
		if event.Kind != "life.home.sleep.started" {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(event.Payload, &fields) != nil {
			return routineWorldSleepBinding{}, errors.New("WORLD sleep-start event payload is invalid")
		}
		payloadActor, actorOK := routineWorldString(fields, "actor_id")
		activityID, activityOK := routineWorldString(fields, "activity_id")
		if !actorOK || payloadActor != worldActorID || !activityOK || activityID != active.ActivityID {
			continue
		}
		if found {
			return routineWorldSleepBinding{}, errors.New("WORLD feed contains duplicate sleep-start events for the active instance")
		}
		activityKind, kindOK := routineWorldString(fields, "activity_kind")
		eventRoom, roomOK := routineWorldString(fields, "room_id")
		eventSlot, slotOK := routineWorldString(fields, "slot_id")
		startedAt := routineWorldTime(fields, "started_at")
		layoutSHA256, _ := routineWorldString(fields, "layout_sha256")
		sceneSHA256, _ := routineWorldString(fields, "scene_sha256")
		if event.EventID == "" || event.TransactionID == "" || event.Seq == 0 || event.Seq > feed.Snapshot.StateSeq || event.RecordedAt.IsZero() ||
			!kindOK || activityKind != "sleep" || !roomOK || eventRoom != roomID || !slotOK || eventSlot != slotID ||
			!startedAt.Equal(active.ActivityStartedAt) ||
			!routineWorldDigest(layoutSHA256) || !routineWorldDigest(sceneSHA256) {
			return routineWorldSleepBinding{}, errors.New("WORLD sleep-start event does not match the exact bed, activity, and server time")
		}
		if (active.HomeSceneSHA256 != "" && active.HomeSceneSHA256 != sceneSHA256) ||
			(feed.Snapshot.HomeSceneSHA256 != "" && feed.Snapshot.HomeSceneSHA256 != sceneSHA256) {
			return routineWorldSleepBinding{}, errors.New("WORLD sleep-start event changed the pinned scene digest")
		}
		binding = routineWorldSleepBinding{
			ActorID: actorID, WorldActorID: payloadActor, ActivityID: activityID, RoomID: eventRoom, SlotID: eventSlot, StartedAt: startedAt.UTC(),
			EventID: event.EventID, TransactionID: event.TransactionID, Seq: event.Seq,
			LayoutSHA256: layoutSHA256, SceneSHA256: sceneSHA256,
		}
		found = true
	}
	if !found {
		return routineWorldSleepBinding{}, errors.New("authenticated WORLD event feed lacks the exact sleep-start event")
	}
	return binding, nil
}

func (r *agentRoutineRuntime) routineWorldSleepStartBinding(ctx context.Context, actorID string, current routineSnapshotRead, active worldread.Actor) (routineWorldSleepBinding, error) {
	if r == nil || r.world == nil || r.cfg == nil {
		return routineWorldSleepBinding{}, errors.New("WORLD sleep binding requires the configured actor and authenticated reader")
	}
	worldActorID, ok := r.routineWorldActorID(actorID)
	if !ok {
		return routineWorldSleepBinding{}, errors.New("WORLD sleep binding requires the configured actor and authenticated reader")
	}
	roomID := r.cfg.Heartbeat.Routine.PrivateRooms[actorID]
	if roomID == "" {
		return routineWorldSleepBinding{}, errors.New("WORLD sleep binding requires the actor's configured private room")
	}
	feed, err := r.world.ReadFeed(ctx, 0)
	if err != nil {
		return routineWorldSleepBinding{}, fmt.Errorf("read authenticated WORLD sleep-start feed: %w", err)
	}
	return routineWorldSleepBindingFromFeed(feed, current.snapshot, actorID, worldActorID, roomID, "bed-"+actorID, active)
}

func sameRoutineWorldSleepBinding(left, right routineWorldSleepBinding) bool {
	return left.ActorID == right.ActorID && left.WorldActorID == right.WorldActorID && left.ActivityID == right.ActivityID && left.RoomID == right.RoomID &&
		left.SlotID == right.SlotID && left.StartedAt.Equal(right.StartedAt) && left.EventID == right.EventID &&
		left.TransactionID == right.TransactionID && left.Seq == right.Seq &&
		left.LayoutSHA256 == right.LayoutSHA256 && left.SceneSHA256 == right.SceneSHA256
}

func (r *agentRoutineRuntime) verifyRoutineWorldActivityBinding(progress routineProgress, terminal routineWorldOperation) error {
	if terminal.Action == worldread.HomeActionWake {
		if progress.World == nil {
			return errors.New("WORLD wake terminal event has no authenticated sleep-start binding")
		}
		binding := progress.World.SleepStart
		if binding == nil || terminal.EventKind != "life.home.sleep.woke" || terminal.ActorID != binding.ActorID ||
			terminal.Payload.ActorID != binding.ActorID || terminal.Payload.ActivityID != binding.ActivityID || terminal.ActivityID != binding.ActivityID ||
			terminal.ActivityKind != "sleep" || terminal.RoomID != binding.RoomID || terminal.SlotID != binding.SlotID ||
			!terminal.StartedAt.Equal(binding.StartedAt) || terminal.EndedAt.IsZero() || terminal.EndedAt.Before(binding.StartedAt) ||
			terminal.FirstSeq == 0 || binding.Seq >= terminal.FirstSeq ||
			terminal.LayoutSHA256 != binding.LayoutSHA256 || terminal.SceneSHA256 != binding.SceneSHA256 ||
			binding.EventID == "" || binding.TransactionID == "" || binding.Seq == 0 {
			return errors.New("WORLD wake terminal event does not match its authenticated sleep-start binding")
		}
		return nil
	}
	for _, start := range progress.World.Operations {
		if start.ActivityID != terminal.Payload.ActivityID || start.ActivityID == "" || !strings.HasSuffix(start.EventKind, ".started") {
			continue
		}
		if terminal.RouteID != start.RouteID || terminal.SceneSHA256 != start.SceneSHA256 || terminal.LayoutSHA256 != start.LayoutSHA256 {
			return errors.New("WORLD terminal event changed its pinned route or scene binding")
		}
		return nil
	}
	return errors.New("WORLD terminal event has no checkpointed start receipt for this activity instance")
}

func routineWorldString(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, exists := fields[key]
	if !exists {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return "", false
	}
	return value, true
}

func routineWorldTime(fields map[string]json.RawMessage, key string) time.Time {
	value, ok := routineWorldString(fields, key)
	if !ok {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func routineWorldUint(fields map[string]json.RawMessage, key string) uint64 {
	raw, exists := fields[key]
	if !exists {
		return 0
	}
	var value uint64
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	return value
}

func routineWorldDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (r *agentRoutineRuntime) worldNowUTC() time.Time {
	if r != nil && r.worldNow != nil {
		return r.worldNow().UTC()
	}
	return time.Now().UTC()
}

func (r *agentRoutineRuntime) waitRoutineWorldDue(ctx context.Context, stop <-chan struct{}, due time.Time, maxWait time.Duration) error {
	return waitRoutineWorldDue(ctx, stop, r.worldNowUTC(), due, maxWait)
}

func (r *agentRoutineRuntime) waitRoutineWorldActivityDue(
	ctx context.Context,
	stop <-chan struct{},
	due time.Time,
	maxWait time.Duration,
	state agentroutine.State,
) error {
	if state.Pending == nil || (state.Pending.Kind != agentroutine.ActivityHobby && state.Pending.Kind != agentroutine.ActivityRelax) {
		return r.waitRoutineWorldDue(ctx, stop, due, maxWait)
	}
	windowCtx, cancelWindow := r.withRoutineWindow(ctx, state.Pending, state)
	defer cancelWindow()
	return r.waitRoutineWorldDue(windowCtx, stop, due, maxWait)
}

func (r *agentRoutineRuntime) routineWorldFreshActor(ctx context.Context, actorID string) (routineSnapshotRead, worldread.Actor, error) {
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return routineSnapshotRead{}, worldread.Actor{}, err
	}
	worldActorID, ok := r.routineWorldActorID(actorID)
	if !ok || read.snapshot.WorldID == "" {
		return routineSnapshotRead{}, worldread.Actor{}, errors.New("WORLD snapshot does not contain the exact configured actor binding")
	}
	for _, actor := range read.snapshot.Actors {
		if actor.ActorID == worldActorID && actor.Present {
			return read, actor, nil
		}
	}
	return routineSnapshotRead{}, worldread.Actor{}, errors.New("WORLD snapshot does not confirm the configured actor is present")
}

func (r *agentRoutineRuntime) verifyRoutineWorldSnapshot(read routineSnapshotRead, actor worldread.Actor, operation routineWorldOperation) error {
	if read.snapshot.StateSeq < operation.StateSeq || !actor.Present {
		return errors.New("fresh WORLD snapshot predates the committed life receipt")
	}
	return nil
}

func (r *agentRoutineRuntime) ensureRoutineWorldArrival(
	ctx context.Context,
	stop <-chan struct{},
	state agentroutine.State,
	progress routineProgress,
	actorID string,
	roomID string,
	slotID string,
	purpose string,
	maxWait time.Duration,
) (agentroutine.State, routineProgress, worldread.Actor, error) {
	if r.worldLife == nil || roomID == "" {
		return state, progress, worldread.Actor{}, errRoutineWorldUnavailable
	}
	read, actor, err := r.routineWorldFreshActor(ctx, actorID)
	if err != nil {
		return state, progress, worldread.Actor{}, err
	}
	if actor.RoomID == roomID && (slotID == "" || actor.SlotID == slotID) && actor.ActivityKind != "move" {
		return state, progress, actor, nil
	}
	movePurpose := purpose + ":move"
	moveIndex, moveSaved := r.routineWorldOperation(progress, movePurpose)
	if actor.ActivityID == "" {
		if moveSaved {
			move := progress.World.Operations[moveIndex]
			finishPurpose := purpose + ":finish:" + move.ActivityID
			if move.ActivityID != "" {
				if _, finished := r.routineWorldOperation(progress, finishPurpose); finished && actor.RoomID == roomID && (slotID == "" || actor.SlotID == slotID) {
					state, progress, _, err = r.performRoutineWorldAction(ctx, state, progress, finishPurpose, worldread.HomeActionFinish, "life.home.move.arrived", worldread.HomeActionPayload{ActorID: actorID, ActivityID: move.ActivityID})
					if err != nil {
						return state, progress, worldread.Actor{}, err
					}
					_, actor, err = r.routineWorldFreshActor(ctx, actorID)
					if err != nil {
						return state, progress, worldread.Actor{}, err
					}
					return state, progress, actor, r.verifyRoutineWorldArrivalState(actor, roomID, slotID)
				}
				if !routineWorldActivityWasConfirmedStopped(progress, actorID, move.ActivityID) {
					return state, progress, worldread.Actor{}, errors.New("checkpointed WORLD move is no longer active and has no verified stop receipt")
				}
				movePurpose = routineWorldRetryPurpose(progress, movePurpose)
				moveIndex, moveSaved = routineWorldExactOperation(progress, movePurpose)
			}
		}
		var moveOperation routineWorldOperation
		state, progress, moveOperation, err = r.performRoutineWorldAction(ctx, state, progress, movePurpose, worldread.HomeActionMove,
			"life.home.move.started", worldread.HomeActionPayload{ActorID: actorID, RoomID: roomID, SlotID: slotID})
		if err != nil {
			return state, progress, worldread.Actor{}, err
		}
		moveIndex, moveSaved = routineWorldExactOperation(progress, moveOperation.Purpose)
		if !moveSaved {
			return state, progress, worldread.Actor{}, errors.New("WORLD move receipt was not checkpointed after its committed action")
		}
		read, actor, err = r.routineWorldFreshActor(ctx, actorID)
		if err != nil {
			return state, progress, worldread.Actor{}, err
		}
		if err := r.verifyRoutineWorldSnapshot(read, actor, moveOperation); err != nil {
			return state, progress, worldread.Actor{}, err
		}
	}
	if actor.ActivityKind != "move" || actor.ActivityID == "" || actor.TargetRoomID != roomID ||
		(slotID != "" && actor.TargetSlotID != slotID) {
		return state, progress, worldread.Actor{}, errRoutineWorldBusy
	}
	if !moveSaved {
		return state, progress, worldread.Actor{}, errors.New("active WORLD move has no matching checkpointed receipt")
	}
	move := progress.World.Operations[moveIndex]
	if move.ActivityID != actor.ActivityID || !move.EventVerified || move.ReceiptStatus != "committed" {
		return state, progress, worldread.Actor{}, errors.New("active WORLD move does not match its checkpointed start receipt")
	}
	if actor.HomeSceneSHA256 != move.SceneSHA256 || actor.MoveRouteID != move.RouteID {
		return state, progress, worldread.Actor{}, errors.New("active WORLD move route is not bound to the checkpointed scene")
	}
	if actor.ActivityDueAt.IsZero() {
		return state, progress, worldread.Actor{}, errors.New("active WORLD move has no server due time")
	}
	progress.World.NextDueAt = actor.ActivityDueAt.UTC()
	move.DueAt = actor.ActivityDueAt.UTC()
	progress.World.Operations[moveIndex] = move
	state, progress, err = r.saveProgress(ctx, state, "world_move_due_saved", progress)
	if err != nil {
		return state, progress, worldread.Actor{}, err
	}
	if err := r.waitRoutineWorldActivityDue(ctx, stop, actor.ActivityDueAt, maxWait, state); err != nil {
		return state, progress, worldread.Actor{}, err
	}
	read, actor, err = r.routineWorldFreshActor(ctx, actorID)
	if err != nil {
		return state, progress, worldread.Actor{}, err
	}
	if actor.ActivityID != move.ActivityID || actor.ActivityKind != "move" || actor.TargetRoomID != roomID ||
		(slotID != "" && actor.TargetSlotID != slotID) {
		return state, progress, worldread.Actor{}, errors.New("WORLD move changed before its due-time finish")
	}
	if r.worldNowUTC().Before(actor.ActivityDueAt) {
		return state, progress, worldread.Actor{}, errRoutineWorldNotDue
	}
	finishPurpose := purpose + ":finish:" + move.ActivityID
	state, progress, finish, err := r.performRoutineWorldAction(ctx, state, progress, finishPurpose, worldread.HomeActionFinish,
		"life.home.move.arrived", worldread.HomeActionPayload{ActorID: actorID, ActivityID: move.ActivityID})
	if err != nil {
		return state, progress, worldread.Actor{}, err
	}
	read, actor, err = r.routineWorldFreshActor(ctx, actorID)
	if err != nil {
		return state, progress, worldread.Actor{}, err
	}
	if err := r.verifyRoutineWorldSnapshot(read, actor, finish); err != nil {
		return state, progress, worldread.Actor{}, err
	}
	if err := r.verifyRoutineWorldArrivalState(actor, roomID, slotID); err != nil {
		return state, progress, worldread.Actor{}, err
	}
	progress.World.NextDueAt = time.Time{}
	state, progress, err = r.saveProgress(ctx, state, "world_move_arrival_confirmed", progress)
	return state, progress, actor, err
}

func (r *agentRoutineRuntime) ensureRoutineMorningArrivals(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || len(state.Pending.Participants) != 1 || r.worldLife == nil {
		return state, progress, errors.New("WORLD morning chat needs one selected eligible peer and a life-scoped client")
	}
	roomID := r.routineWorldPreferredSharedRoom()
	if roomID == "" {
		return state, progress, errors.New("WORLD morning chat needs a configured shared room")
	}
	if progress.World == nil {
		progress.World = &routineWorldProgress{}
	}
	if progress.World.MorningRoom == "" {
		progress.World.MorningRoom = roomID
		var err error
		state, progress, err = r.saveProgress(ctx, state, "morning_shared_room_saved", progress)
		if err != nil {
			return state, progress, err
		}
	} else if progress.World.MorningRoom != roomID {
		return state, progress, errors.New("persisted WORLD morning target no longer matches configured shared rooms")
	}
	actors := []string{state.Pending.ActorID, state.Pending.Participants[0]}
	for _, actorID := range actors {
		var err error
		state, progress, _, err = r.ensureRoutineWorldArrival(ctx, stop, state, progress, actorID, roomID, "", "morning-shared:"+actorID, routineWorldMoveWait)
		if err != nil {
			return state, progress, err
		}
	}
	for _, actorID := range actors {
		_, actor, err := r.routineWorldFreshActor(ctx, actorID)
		if err != nil {
			return state, progress, err
		}
		if actor.RoomID != roomID || actor.ActivityKind == "move" {
			return state, progress, errors.New("fresh WORLD snapshot does not confirm both morning participants in the shared room")
		}
	}
	return state, progress, nil
}

func (r *agentRoutineRuntime) prepareRoutineWorldChoice(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || r.worldLife == nil {
		return state, progress, errRoutineWorldUnavailable
	}
	if routineStopRequested(ctx, stop) {
		return state, progress, errAgentRoutineStopped
	}
	intent := state.Pending
	switch intent.Kind {
	case agentroutine.ActivityMorningChat:
		return r.ensureRoutineMorningArrivals(ctx, stop, state, progress)
	case agentroutine.ActivityHobby:
		roomID := r.cfg.Heartbeat.Routine.PrivateRooms[intent.ActorID]
		if roomID == "" {
			return state, progress, errors.New("WORLD hobby choice requires the actor's configured private room")
		}
		slotID := ""
		purpose := "choice-hobby:" + intent.ActorID
		if intent.ActorID == "shiro" && intent.Label == "fitness_research" {
			slotID, purpose = "shiro-exercise", "exercise-location"
		} else if intent.ActorID == "kuro" && intent.Label == "creative_mirror_ideas" {
			slotID, purpose = "kuro-mirror", "mirror-location"
		}
		state, progress, _, err := r.ensureRoutineWorldArrival(ctx, stop, state, progress, intent.ActorID, roomID, slotID, purpose, routineWorldMoveWait)
		return state, progress, err
	default:
		return state, progress, nil
	}
}

func (r *agentRoutineRuntime) routineWorldPendingMorningEligible(ctx context.Context, state agentroutine.State, now time.Time, fresh agentroutine.Plan, progress routineProgress) bool {
	intent := state.Pending
	if intent == nil || progress.World == nil || progress.World.MorningRoom == "" || len(intent.Participants) != 1 ||
		now.In(r.location()).Hour() >= 9 || fresh.PlannedWake.After(now) || r.worldLife == nil {
		return false
	}
	roomID := progress.World.MorningRoom
	for _, actorID := range []string{intent.ActorID, intent.Participants[0]} {
		_, actor, err := r.routineWorldFreshActor(ctx, actorID)
		if err != nil || strings.EqualFold(actor.ActivityKind, "sleep") {
			return false
		}
		if actor.ActivityID == "" && actor.ActivityKind == "" {
			continue
		}
		if actor.ActivityKind != "move" || actor.ActivityID == "" || actor.TargetRoomID != roomID {
			return false
		}
		purpose := "morning-shared:" + actorID + ":move"
		index, ok := r.routineWorldOperation(progress, purpose)
		if !ok || progress.World.Operations[index].ActivityID != actor.ActivityID || !progress.World.Operations[index].EventVerified {
			return false
		}
	}
	return r.routinePeerAvailableForChat(intent.Participants[0], now)
}

func (r *agentRoutineRuntime) routineWorldPendingHobbyEligible(ctx context.Context, state agentroutine.State, now time.Time, progress routineProgress) bool {
	intent := state.Pending
	if intent == nil || progress.World == nil || r.worldLife == nil || !routineWorldHomeHobby(intent.ActorID, intent.Label) {
		return false
	}
	localNow := now.In(r.location())
	if localNow.Hour() >= 9 && localNow.Hour() < 22 {
		return false
	}
	if localNow.Hour() < 2 && !freshRoutineSleepAt(state, localNow).After(localNow) {
		return false
	}
	roomID := r.cfg.Heartbeat.Routine.PrivateRooms[intent.ActorID]
	if roomID == "" {
		return false
	}
	_, actor, err := r.routineWorldFreshActor(ctx, intent.ActorID)
	if err != nil || strings.EqualFold(actor.ActivityKind, "sleep") {
		return false
	}
	if actor.RoomID == roomID && actor.ActivityKind != "move" {
		return true
	}
	if actor.ActivityKind != "move" || actor.ActivityID == "" || actor.TargetRoomID != roomID {
		return false
	}
	purpose := "exercise-location:move"
	if intent.ActorID == "kuro" {
		purpose = "mirror-location:move"
	}
	index, ok := r.routineWorldOperation(progress, purpose)
	return ok && progress.World.Operations[index].ActivityID == actor.ActivityID && progress.World.Operations[index].EventVerified
}

func freshRoutineSleepAt(state agentroutine.State, now time.Time) time.Time {
	if state.ScheduleDate == now.Format("2006-01-02") && !state.PlannedSleep.IsZero() {
		return state.PlannedSleep.In(now.Location())
	}
	sleepAt, _, err := agentroutine.ScheduleFor(state.ActorID, now.Format("2006-01-02"))
	if err != nil {
		return time.Time{}
	}
	return sleepAt.In(now.Location())
}

func moveIndexSafe(r *agentRoutineRuntime, progress routineProgress, purpose string) int {
	index, ok := r.routineWorldOperation(progress, purpose)
	if !ok {
		return 0
	}
	return index
}

func (r *agentRoutineRuntime) verifyRoutineWorldArrivalState(actor worldread.Actor, roomID, slotID string) error {
	if actor.RoomID != roomID || (slotID != "" && actor.SlotID != slotID) || actor.ActivityKind == "move" {
		return errors.New("fresh WORLD snapshot does not confirm the requested room and slot arrival")
	}
	return nil
}

func waitRoutineWorldDue(ctx context.Context, stop <-chan struct{}, now, due time.Time, maxWait time.Duration) error {
	if due.IsZero() {
		return errors.New("WORLD activity has no server due time")
	}
	delay := due.Sub(now)
	if delay <= 0 {
		return nil
	}
	if maxWait > 0 && delay > maxWait {
		return errRoutineWorldNotDue
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errAgentRoutineStopped
	case <-stop:
		return errAgentRoutineStopped
	case <-timer.C:
		return nil
	}
}

func (r *agentRoutineRuntime) executeRoutineWorldActivity(
	ctx context.Context,
	stop <-chan struct{},
	state agentroutine.State,
	progress routineProgress,
) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || r.worldLife == nil {
		return state, progress, errRoutineWorldUnavailable
	}
	intent := state.Pending
	var err error
	switch intent.Kind {
	case agentroutine.ActivitySleep:
		roomID := r.cfg.Heartbeat.Routine.PrivateRooms[intent.ActorID]
		if roomID == "" {
			return state, progress, errors.New("WORLD sleep requires the actor's configured private room")
		}
		state, progress, _, err = r.ensureRoutineWorldArrival(ctx, stop, state, progress, intent.ActorID, roomID, "bed-"+intent.ActorID, "sleep-bed", routineWorldMoveWait)
		if err != nil {
			return state, progress, err
		}
		read, actor, err := r.routineWorldFreshActor(ctx, intent.ActorID)
		if err != nil {
			return state, progress, err
		}
		purpose := "sleep-start"
		if actor.ActivityKind != "sleep" || actor.ActivityID == "" || actor.RoomID != roomID || actor.SlotID != "bed-"+intent.ActorID {
			state, progress, _, err = r.performRoutineWorldAction(ctx, state, progress, purpose, worldread.HomeActionSleep,
				"life.home.sleep.started", worldread.HomeActionPayload{ActorID: intent.ActorID})
			if err != nil {
				return state, progress, err
			}
			read, actor, err = r.routineWorldFreshActor(ctx, intent.ActorID)
			if err != nil {
				return state, progress, err
			}
			index, ok := r.routineWorldOperation(progress, purpose)
			if !ok {
				return state, progress, errors.New("WORLD sleep receipt was not checkpointed")
			}
			op := progress.World.Operations[index]
			if err := r.verifyRoutineWorldSnapshot(read, actor, op); err != nil || actor.ActivityKind != "sleep" || actor.ActivityID != op.ActivityID || actor.RoomID != roomID || actor.SlotID != "bed-"+intent.ActorID {
				return state, progress, errors.Join(err, errors.New("fresh WORLD snapshot does not confirm the exact sleep activity"))
			}
		} else {
			index, ok := routineWorldExactOperation(progress, purpose)
			if !ok {
				return state, progress, errors.New("active WORLD sleep has no checkpointed CORE start receipt")
			}
			op := progress.World.Operations[index]
			if op.ActivityID != actor.ActivityID || op.ReceiptStatus != "committed" || !op.EventVerified {
				return state, progress, errors.New("active WORLD sleep does not match its checkpointed CORE receipt")
			}
			if err := r.verifyRoutineWorldSnapshot(read, actor, op); err != nil {
				return state, progress, err
			}
		}
		if !state.LogicalSleepUntil.Equal(state.PlannedWake) {
			state.LogicalSleepUntil = state.PlannedWake
			state, err = routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
				return r.store.Save(persistCtx, state, state.Revision)
			})
			if err != nil {
				return state, progress, err
			}
		}
		progress.CompletedAt = read.readAt.UTC()
	case agentroutine.ActivityWake:
		read, actor, err := r.routineWorldFreshActor(ctx, intent.ActorID)
		if err != nil {
			return state, progress, err
		}
		if actor.ActivityKind == "sleep" && actor.ActivityID != "" {
			purpose := "wake:" + actor.ActivityID
			binding, bindingErr := r.routineWorldSleepStartBinding(ctx, intent.ActorID, read, actor)
			if bindingErr != nil {
				return state, progress, bindingErr
			}
			if progress.World == nil {
				progress.World = &routineWorldProgress{}
			}
			if progress.World.SleepStart != nil && !sameRoutineWorldSleepBinding(*progress.World.SleepStart, binding) {
				return state, progress, errors.New("persisted WORLD sleep-start binding changed during wake")
			}
			if progress.World.SleepStart == nil {
				progress.World.SleepStart = &binding
				state, progress, err = r.saveProgress(ctx, state, "world_sleep_start_bound", progress)
				if err != nil {
					return state, progress, err
				}
			}
			var operation routineWorldOperation
			state, progress, operation, err = r.performRoutineWorldAction(ctx, state, progress, purpose, worldread.HomeActionWake,
				"life.home.sleep.woke", worldread.HomeActionPayload{ActorID: intent.ActorID, ActivityID: actor.ActivityID})
			if err != nil {
				return state, progress, err
			}
			read, actor, err = r.routineWorldFreshActor(ctx, intent.ActorID)
			if err != nil {
				return state, progress, err
			}
			if err := r.verifyRoutineWorldSnapshot(read, actor, operation); err != nil || actor.ActivityID == operation.Payload.ActivityID {
				return state, progress, errors.Join(err, errors.New("fresh WORLD snapshot still shows the prior sleep activity"))
			}
			progress.CompletedAt = operation.EndedAt
		} else if index, exists := r.routineWorldOperationByAction(progress, worldread.HomeActionWake, intent.ActorID); exists {
			operation := progress.World.Operations[index]
			if !routineWorldWakeOperationCanReplay(state, progress, intent.ActorID, operation) {
				return state, progress, errors.New("WORLD wake state lacks a matching prepared operation and authenticated sleep-start binding")
			}
			state, progress, operation, err = r.performRoutineWorldAction(ctx, state, progress, operation.Purpose,
				worldread.HomeActionWake, "life.home.sleep.woke", operation.Payload)
			if err != nil {
				return state, progress, err
			}
			read, actor, err = r.routineWorldFreshActor(ctx, intent.ActorID)
			if err != nil {
				return state, progress, err
			}
			if err := r.verifyRoutineWorldSnapshot(read, actor, operation); err != nil || actor.ActivityID == operation.Payload.ActivityID {
				return state, progress, errors.Join(err, errors.New("fresh WORLD snapshot still shows the prior sleep activity"))
			}
			if operation.EndedAt.IsZero() {
				return state, progress, errors.New("verified WORLD wake event has no server end timestamp")
			}
			progress.CompletedAt = operation.EndedAt
		} else {
			return state, progress, errors.New("WORLD snapshot does not confirm an active sleep instance to wake")
		}
		if !state.LogicalSleepUntil.IsZero() {
			state.LogicalSleepUntil = time.Time{}
			state, err = routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
				return r.store.Save(persistCtx, state, state.Revision)
			})
			if err != nil {
				return state, progress, err
			}
		}
	case agentroutine.ActivityRelax:
		fresh, _, freshErr := r.planFresh(ctx, intent.ActorID, true, nil)
		if freshErr != nil {
			return state, progress, freshErr
		}
		if fresh.Kind != agentroutine.ActivityRelax {
			return state, progress, errors.New("WORLD relaxation is no longer eligible under fresh routine policy")
		}
		roomID := r.routineWorldRelaxRoom(intent.ActorID, fresh.RelaxRoom)
		if roomID == "" {
			return state, progress, errors.New("WORLD relaxation requires a configured policy-selected room")
		}
		state, progress, _, err = r.ensureRoutineWorldArrival(ctx, stop, state, progress, intent.ActorID, roomID, "", "relax-room", routineWorldMoveWait)
		if err != nil {
			return state, progress, err
		}
		read, actor, err := r.routineWorldFreshActor(ctx, intent.ActorID)
		if err != nil {
			return state, progress, err
		}
		if err := r.verifyRoutineWorldArrivalState(actor, roomID, ""); err != nil {
			return state, progress, err
		}
		if progress.World == nil {
			progress.World = &routineWorldProgress{}
		}
		progress.World.RelaxRoomID = actor.RoomID
		progress.World.RelaxObservedAt = read.readAt.UTC()
		progress.CompletedAt = read.readAt.UTC()
	case agentroutine.ActivityHobby:
		if intent.ActorID == "shiro" && intent.Label == "fitness_research" {
			state, progress, err = r.executeRoutineWorldExercise(ctx, stop, state, progress)
		} else if intent.ActorID == "kuro" && intent.Label == "creative_mirror_ideas" {
			state, progress, err = r.executeRoutineWorldMirrorPose(ctx, stop, state, progress)
		} else {
			return state, progress, errors.New("routine hobby is not a WORLD home activity")
		}
		if err != nil {
			return state, progress, err
		}
	default:
		return state, progress, fmt.Errorf("routine kind %q is not a WORLD home activity", intent.Kind)
	}
	progress.EffectCompleted = true
	state, progress, err = r.saveProgress(ctx, state, "world_life_activity_completed", progress)
	return state, progress, err
}

func (r *agentRoutineRuntime) routineWorldRelaxRoom(actorID string, scope agentroutine.RelaxRoomScope) string {
	if r == nil || r.cfg == nil {
		return ""
	}
	switch scope {
	case agentroutine.RelaxRoomPrivate:
		return r.cfg.Heartbeat.Routine.PrivateRooms[actorID]
	case agentroutine.RelaxRoomShared:
		return r.routineWorldPreferredSharedRoom()
	default:
		return ""
	}
}
func (r *agentRoutineRuntime) routineWorldOperationByAction(progress routineProgress, action, actorID string) (int, bool) {
	if progress.World == nil {
		return -1, false
	}
	for index := range progress.World.Operations {
		operation := progress.World.Operations[index]
		if operation.Action == action && operation.ActorID == actorID {
			return index, true
		}
	}
	return -1, false
}

func routineWorldWakeOperationCanReplay(state agentroutine.State, progress routineProgress, actorID string, operation routineWorldOperation) bool {
	if state.Pending == nil || state.Pending.ID == "" || progress.World == nil || progress.World.SleepStart == nil {
		return false
	}
	binding := progress.World.SleepStart
	purpose := "wake:" + binding.ActivityID
	return actorID != "" && binding.ActorID == actorID && binding.ActivityID != "" && binding.EventID != "" &&
		binding.TransactionID != "" && binding.Seq != 0 && operation.Purpose == purpose &&
		operation.Key == routineWorldOperationKey(state.Pending.ID, actorID, purpose) && operation.Action == worldread.HomeActionWake &&
		operation.EventKind == "life.home.sleep.woke" && operation.ActorID == actorID && operation.Payload.ActorID == actorID &&
		operation.Payload.ActivityID == binding.ActivityID
}

func (r *agentRoutineRuntime) executeRoutineWorldExercise(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	roomID := r.cfg.Heartbeat.Routine.PrivateRooms["shiro"]
	if roomID == "" {
		return state, progress, errors.New("WORLD exercise requires Shiro's configured private room")
	}
	var err error
	state, progress, _, err = r.ensureRoutineWorldArrival(ctx, stop, state, progress, "shiro", roomID, "shiro-exercise", "exercise-location", routineWorldMoveWait)
	if err != nil {
		return state, progress, err
	}
	return r.executeRoutineWorldTimedActivity(ctx, stop, state, progress,
		"exercise-start", worldread.HomeActionExercise, "life.home.exercise.started", "life.home.exercise.completed", "shiro", "exercise", roomID, "shiro-exercise", "")
}

func (r *agentRoutineRuntime) executeRoutineWorldMirrorPose(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	roomID := r.cfg.Heartbeat.Routine.PrivateRooms["kuro"]
	if roomID == "" {
		return state, progress, errors.New("WORLD mirror pose requires Kuro's configured private room")
	}
	var err error
	state, progress, _, err = r.ensureRoutineWorldArrival(ctx, stop, state, progress, "kuro", roomID, "kuro-mirror", "mirror-location", routineWorldMoveWait)
	if err != nil {
		return state, progress, err
	}
	if progress.World == nil {
		progress.World = &routineWorldProgress{}
	}
	if !progress.World.MirrorCompletionBaseSet {
		_, actor, readErr := r.routineWorldFreshActor(ctx, "kuro")
		if readErr != nil {
			return state, progress, readErr
		}
		progress.World.MirrorCompletionBase = actor.MirrorPoseCompletions
		progress.World.MirrorPoseID = routineMirrorPoseIDs[actor.MirrorPoseCompletions%uint64(len(routineMirrorPoseIDs))]
		progress.World.MirrorCompletionBaseSet = true
		state, progress, err = r.saveProgress(ctx, state, "mirror_pose_selection_saved", progress)
		if err != nil {
			return state, progress, err
		}
	}
	return r.executeRoutineWorldTimedActivity(ctx, stop, state, progress,
		"mirror-pose-start", worldread.HomeActionMirror, "life.home.mirror_pose.started", "life.home.mirror_pose.completed",
		"kuro", "mirror_pose", roomID, "kuro-mirror", progress.World.MirrorPoseID)
}

func (r *agentRoutineRuntime) executeRoutineWorldTimedActivity(
	ctx context.Context,
	stop <-chan struct{},
	state agentroutine.State,
	progress routineProgress,
	startPurpose string,
	startAction string,
	startEvent string,
	finishEvent string,
	actorID string,
	activityKind string,
	roomID string,
	slotID string,
	poseID string,
) (agentroutine.State, routineProgress, error) {
	read, actor, err := r.routineWorldFreshActor(ctx, actorID)
	if err != nil {
		return state, progress, err
	}
	var start routineWorldOperation
	if actor.ActivityKind == activityKind && actor.ActivityID != "" {
		index, ok := r.routineWorldOperation(progress, startPurpose)
		if !ok {
			return state, progress, errRoutineWorldBusy
		}
		start = progress.World.Operations[index]
		if start.ActivityID != actor.ActivityID || start.ReceiptStatus != "committed" || !start.EventVerified {
			return state, progress, errors.New("active WORLD activity does not match its saved start receipt")
		}
	} else {
		if actor.ActivityID != "" {
			return state, progress, errRoutineWorldBusy
		}
		if startIndex, ok := r.routineWorldOperation(progress, startPurpose); ok {
			previous := progress.World.Operations[startIndex]
			if previous.ActivityID != "" {
				if terminal, finished := routineWorldActivityWasConfirmedFinished(progress, actorID, previous.ActivityID); finished {
					if terminal.EventKind != finishEvent || terminal.EndedAt.IsZero() {
						return state, progress, errors.New("confirmed interrupted finish lacks the expected event or owner timestamp")
					}
					if read.snapshot.StateSeq < terminal.StateSeq {
						return state, progress, errors.New("fresh WORLD snapshot predates the confirmed interrupted finish")
					}
					if activityKind == "mirror_pose" && actor.MirrorPoseCompletions != terminal.MirrorCompletions {
						return state, progress, errors.New("WORLD mirror completion count changed after its confirmed finish")
					}
					progress.CompletedAt = terminal.EndedAt.UTC()
					progress.World.NextDueAt = time.Time{}
					return r.saveProgress(ctx, state, "world_interrupted_activity_finish_confirmed", progress)
				}
				if routineWorldActivityWasConfirmedStopped(progress, actorID, previous.ActivityID) {
					startPurpose = routineWorldRetryPurpose(progress, startPurpose)
				}
			}
		}
		payload := worldread.HomeActionPayload{ActorID: actorID}
		switch startAction {
		case worldread.HomeActionMirror:
			payload.PoseID = poseID
		}
		state, progress, start, err = r.performRoutineWorldAction(ctx, state, progress, startPurpose, startAction, startEvent, payload)
		if err != nil {
			return state, progress, err
		}
		read, actor, err = r.routineWorldFreshActor(ctx, actorID)
		if err != nil {
			return state, progress, err
		}
		if err := r.verifyRoutineWorldSnapshot(read, actor, start); err != nil || actor.ActivityID != start.ActivityID || actor.ActivityKind != activityKind || actor.RoomID != roomID || actor.SlotID != slotID || (poseID != "" && actor.PoseID != poseID) {
			return state, progress, errors.Join(err, errors.New("fresh WORLD snapshot does not confirm the exact home activity"))
		}
		if start.ActivityID == "" || actor.HomeSceneSHA256 != start.SceneSHA256 {
			return state, progress, errors.New("WORLD activity snapshot is not bound to the committed start scene")
		}
		index, ok := r.routineWorldOperation(progress, startPurpose)
		if !ok {
			return state, progress, errors.New("WORLD activity start receipt was not checkpointed")
		}
		start = progress.World.Operations[index]
		start.DueAt = actor.ActivityDueAt.UTC()
		progress.World.Operations[index] = start
		progress.World.NextDueAt = start.DueAt
		state, progress, err = r.saveProgress(ctx, state, "world_activity_due_saved", progress)
		if err != nil {
			return state, progress, err
		}
	}
	if actor.RoomID != roomID || actor.SlotID != slotID || (poseID != "" && actor.PoseID != poseID) || actor.ActivityID != start.ActivityID {
		return state, progress, errors.New("fresh WORLD snapshot activity identity or home slot changed")
	}
	if actor.ActivityDueAt.IsZero() {
		return state, progress, errors.New("active WORLD activity has no server due time")
	}
	if err := r.waitRoutineWorldActivityDue(ctx, stop, actor.ActivityDueAt, routineWorldMoveWait, state); err != nil {
		return state, progress, err
	}
	read, actor, err = r.routineWorldFreshActor(ctx, actorID)
	if err != nil {
		return state, progress, err
	}
	if actor.ActivityID != start.ActivityID || actor.ActivityKind != activityKind || actor.RoomID != roomID || actor.SlotID != slotID ||
		(poseID != "" && actor.PoseID != poseID) || r.worldNowUTC().Before(actor.ActivityDueAt) {
		return state, progress, errRoutineWorldNotDue
	}
	finishPurpose := startPurpose + ":finish:" + start.ActivityID
	state, progress, finish, err := r.performRoutineWorldAction(ctx, state, progress, finishPurpose, worldread.HomeActionFinish,
		finishEvent, worldread.HomeActionPayload{ActorID: actorID, ActivityID: start.ActivityID})
	if err != nil {
		return state, progress, err
	}
	read, actor, err = r.routineWorldFreshActor(ctx, actorID)
	if err != nil {
		return state, progress, err
	}
	if err := r.verifyRoutineWorldSnapshot(read, actor, finish); err != nil || actor.ActivityID == start.ActivityID {
		return state, progress, errors.Join(err, errors.New("fresh WORLD snapshot still shows the completed activity instance"))
	}
	if activityKind == "mirror_pose" {
		want := progress.World.MirrorCompletionBase + 1
		if actor.MirrorPoseCompletions != want || finish.MirrorCompletions != want {
			return state, progress, errors.New("WORLD mirror pose completion counter did not increment exactly once")
		}
	}
	progress.CompletedAt = finish.EndedAt.UTC()
	progress.World.NextDueAt = time.Time{}
	state, progress, err = r.saveProgress(ctx, state, "world_home_activity_finished", progress)
	return state, progress, err
}

func (r *agentRoutineRuntime) routineWorldStopActiveOperations(ctx context.Context, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if r.world == nil || r.worldLife == nil || state.Pending == nil || progress.World == nil {
		return state, progress, nil
	}
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return state, progress, err
	}
	var failures []error
	for index, operation := range progress.World.Operations {
		if operation.ActivityID == "" || operation.ReceiptStatus != "committed" || !operation.EventVerified ||
			(operation.ActivityKind != "move" && operation.ActivityKind != "exercise" && operation.ActivityKind != "mirror_pose") {
			continue
		}
		worldActorID, ok := r.routineWorldActorID(operation.ActorID)
		if !ok {
			failures = append(failures, errors.New("WORLD operation Actor binding is no longer configured"))
			continue
		}
		var actor worldread.Actor
		found := false
		for _, candidate := range read.snapshot.Actors {
			if candidate.ActorID == worldActorID && candidate.Present {
				actor, found = candidate, true
				break
			}
		}
		if !found || actor.ActivityID != operation.ActivityID {
			continue
		}
		action, eventKind := worldread.HomeActionStop, "life.home.activity.stopped"
		purpose := "interrupt-stop:" + operation.ActivityID
		mirrorCompletionsBefore := actor.MirrorPoseCompletions
		if !actor.ActivityDueAt.IsZero() && !r.worldNowUTC().Before(actor.ActivityDueAt) {
			action = worldread.HomeActionFinish
			switch actor.ActivityKind {
			case "move":
				eventKind = "life.home.move.arrived"
			case "exercise":
				eventKind = "life.home.exercise.completed"
			case "mirror_pose":
				eventKind = "life.home.mirror_pose.completed"
			}
			purpose = "interrupt-finish:" + operation.ActivityID
		}
		payload := worldread.HomeActionPayload{ActorID: operation.ActorID, ActivityID: operation.ActivityID}
		var actionErr error
		state, progress, _, actionErr = r.performRoutineWorldAction(ctx, state, progress, purpose, action, eventKind, payload)
		if actionErr != nil {
			failures = append(failures, actionErr)
			continue
		}
		read, err = r.readSnapshotFresh(ctx)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		operationIndex, ok := r.routineWorldOperation(progress, purpose)
		if !ok || read.snapshot.StateSeq < progress.World.Operations[operationIndex].StateSeq {
			failures = append(failures, errors.New("fresh WORLD snapshot did not settle the interrupted activity receipt"))
			continue
		}
		settled := false
		var after worldread.Actor
		for _, candidate := range read.snapshot.Actors {
			if candidate.ActorID == worldActorID && candidate.Present {
				after, settled = candidate, true
				break
			}
		}
		if !settled || after.ActivityID == operation.ActivityID {
			failures = append(failures, errors.New("fresh WORLD snapshot still shows the interrupted activity instance"))
			continue
		}
		terminal := progress.World.Operations[operationIndex]
		if action == worldread.HomeActionStop && after.RoomID != actor.RoomID {
			failures = append(failures, errors.New("WORLD stop changed the last confirmed room"))
			continue
		}
		if action == worldread.HomeActionFinish {
			switch actor.ActivityKind {
			case "move":
				if after.RoomID != actor.TargetRoomID || (actor.TargetSlotID != "" && after.SlotID != actor.TargetSlotID) {
					failures = append(failures, errors.New("WORLD move finish did not confirm its exact destination"))
					continue
				}
			case "mirror_pose":
				if after.MirrorPoseCompletions != mirrorCompletionsBefore+1 || terminal.MirrorCompletions != after.MirrorPoseCompletions {
					failures = append(failures, errors.New("WORLD mirror finish did not increment its completion counter exactly once"))
					continue
				}
			}
		}
		progress.World.Operations[index].DueAt = operation.DueAt
	}
	progress.World.NextDueAt = time.Time{}
	state, progress, saveErr := r.saveProgress(ctx, state, "world_interrupt_settled", progress)
	return state, progress, errors.Join(errors.Join(failures...), saveErr)
}

func (r *agentRoutineRuntime) collectRoutineWorldFacts(ctx context.Context, actorID, date string) ([]agentroutine.Activity, error) {
	if ctx == nil || r == nil || r.world == nil || !routineFactActorID(actorID) {
		return []agentroutine.Activity{}, nil
	}
	dayStart, dayEnd, err := routineFactDay(date)
	if err != nil {
		return nil, errors.New("WORLD diary fact date is invalid")
	}
	worldActorID, ok := r.routineWorldActorID(actorID)
	if !ok {
		return []agentroutine.Activity{}, nil
	}
	feed, err := r.world.ReadFeed(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("read authenticated WORLD lifecycle events: %w", err)
	}
	activities := make([]agentroutine.Activity, 0)
	for _, event := range feed.Events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		summary, relevant := routineWorldEventSummary(event)
		if !relevant {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(event.Payload, &fields); err != nil {
			return nil, errors.New("WORLD diary event payload is invalid")
		}
		payloadActor, ok := routineWorldString(fields, "actor_id")
		if !ok || payloadActor != worldActorID {
			continue
		}
		timestampField := "ended_at"
		if strings.HasSuffix(event.Kind, ".started") {
			timestampField = "started_at"
		}
		at := routineWorldTime(fields, timestampField)
		if at.IsZero() {
			at = event.RecordedAt.UTC()
		}
		if at.Before(dayStart) || !at.Before(dayEnd) {
			continue
		}
		reference, err := json.Marshal(struct {
			WorldID       string `json:"world_id"`
			TimelineID    string `json:"timeline_id"`
			TimelineEpoch string `json:"timeline_epoch"`
			EventID       string `json:"event_id"`
			TransactionID string `json:"transaction_id"`
			Seq           uint64 `json:"seq"`
			Kind          string `json:"kind"`
			RouteID       string `json:"route_id,omitempty"`
			SceneSHA256   string `json:"scene_sha256,omitempty"`
		}{feed.Snapshot.WorldID, feed.Snapshot.TimelineID, feed.Snapshot.TimelineEpoch, event.EventID,
			event.TransactionID, event.Seq, event.Kind, safeRoutineWorldField(fields, "route_id"), safeRoutineWorldField(fields, "scene_sha256")})
		if err != nil {
			return nil, errors.New("encode WORLD diary fact evidence")
		}
		activities = append(activities, agentroutine.Activity{
			ActorID: actorID, Date: date, Key: "world-event:" + event.EventID, Kind: routineWorldDiaryActivityKind(event),
			At: at.UTC(), Source: "world_life_event", SourceID: event.EventID, Confirmed: true,
			Summary: summary, Reference: string(reference),
		})
	}
	return activities, nil
}

func safeRoutineWorldField(fields map[string]json.RawMessage, key string) string {
	value, _ := routineWorldString(fields, key)
	return value
}

func routineWorldEventSummary(event worldread.Event) (string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(event.Payload, &fields) != nil {
		return "", false
	}
	activityKind, _ := routineWorldString(fields, "activity_kind")
	switch event.Kind {
	case "life.home.move.started":
		return "WORLDで自宅内の" + routineWorldRoomDiaryLabel(safeRoutineWorldField(fields, "target_room_id")) + "への移動を開始しました。", true
	case "life.home.move.arrived":
		return "WORLDで自宅内の" + routineWorldRoomDiaryLabel(safeRoutineWorldField(fields, "room_id")) + "への移動完了を確認しました。", true
	case "life.home.sleep.started":
		return "WORLDで" + routineWorldRoomDiaryLabel(safeRoutineWorldField(fields, "room_id")) + "のベッドで就寝を確認しました。", true
	case "life.home.sleep.woke":
		return "WORLDで起床を確認しました。", true
	case "life.home.exercise.started":
		return "WORLDで運動の開始を確認しました。", true
	case "life.home.exercise.completed":
		return "WORLDで運動の完了を確認しました。", true
	case "life.home.mirror_pose.started":
		poseID, _ := routineWorldString(fields, "pose_id")
		return "WORLDで鏡のポーズ「" + routineMirrorPoseLabel(poseID) + "」の練習開始を確認しました。", true
	case "life.home.mirror_pose.completed":
		poseID, _ := routineWorldString(fields, "pose_id")
		return "WORLDで鏡のポーズ「" + routineMirrorPoseLabel(poseID) + "」の完了を確認しました。", true
	case "life.home.activity.stopped":
		switch activityKind {
		case "move":
			return "WORLDで自宅内の移動を途中で停止したことを確認しました。", true
		case "exercise":
			return "WORLDで運動を途中で停止したことを確認しました。", true
		case "mirror_pose":
			return "WORLDで鏡のポーズ練習を途中で停止したことを確認しました。", true
		}
	}
	return "", false
}

func routineMirrorPoseLabel(poseID string) string {
	labels := map[string]string{
		"mirror-greeting": "挨拶", "mirror-hero": "ヒーロー", "mirror-side": "横向き",
		"mirror-focus": "集中", "mirror-hands-on-hips": "腰に手を当てる", "mirror-over-shoulder": "肩越し",
		"mirror-open-arms": "両腕を開く", "mirror-bow": "お辞儀",
	}
	if label := labels[poseID]; label != "" {
		return label
	}
	return "ポーズ"
}

func routineWorldRoomDiaryLabel(roomID string) string {
	switch roomID {
	case "living":
		return "リビング"
	case "dining":
		return "ダイニング"
	case "upper-room-2-1":
		return "ミオの部屋"
	case "upper-room-2-2":
		return "シロの部屋"
	case "upper-room-2-3":
		return "クロの部屋"
	case "upper-room-2-4":
		return "ミドリの部屋"
	case "upper-room-3-1":
		return "アカの部屋"
	case "upper-room-3-2":
		return "アオの部屋"
	case "upper-room-3-3":
		return "キンの部屋"
	case "attic-window":
		return "ギンの部屋"
	default:
		return "部屋"
	}
}

func routineWorldDiaryActivityKind(event worldread.Event) agentroutine.ActivityKind {
	switch event.Kind {
	case "life.home.sleep.started":
		return agentroutine.ActivitySleep
	case "life.home.sleep.woke":
		return agentroutine.ActivityWake
	case "life.home.move.started", "life.home.move.arrived":
		return agentroutine.ActivityRelax
	case "life.home.activity.stopped":
		var fields map[string]json.RawMessage
		if json.Unmarshal(event.Payload, &fields) == nil {
			if activityKind, ok := routineWorldString(fields, "activity_kind"); ok && activityKind == "move" {
				return agentroutine.ActivityRelax
			}
		}
	}
	return agentroutine.ActivityHobby
}
