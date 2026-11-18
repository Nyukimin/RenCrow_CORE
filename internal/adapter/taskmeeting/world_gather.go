package taskmeeting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	appmeeting "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmeeting"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
)

// TaskMeetingWorldGatherClient is the narrow authenticated boundary needed to
// reconcile one frozen meeting's actor movement and its lifecycle receipts.
type TaskMeetingWorldGatherClient interface {
	ReadScope() worldread.ReadScope
	Snapshot(context.Context) (worldread.Snapshot, error)
	Events(context.Context, uint64) (worldread.EventBatch, error)
	Command(context.Context, string, string, worldread.HomeActionPayload) (worldread.CommandResult, error)
}

type taskMeetingWorldGatherClient struct {
	read *worldread.Client
	life *worldread.LifeClient
}

func (client *taskMeetingWorldGatherClient) ReadScope() worldread.ReadScope {
	return client.read.ReadScope()
}
func (client *taskMeetingWorldGatherClient) Snapshot(ctx context.Context) (worldread.Snapshot, error) {
	return client.read.Snapshot(ctx)
}
func (client *taskMeetingWorldGatherClient) Events(ctx context.Context, after uint64) (worldread.EventBatch, error) {
	return client.read.Events(ctx, after)
}
func (client *taskMeetingWorldGatherClient) Command(ctx context.Context, key, action string, payload worldread.HomeActionPayload) (worldread.CommandResult, error) {
	return client.life.Command(ctx, key, action, payload)
}

// NewConfiguredWorldGatherClient resolves the frozen reader token and the
// existing owner-only, distinct life token. It builds clients without making
// a WORLD request; each client is restricted to the declaration's actors.
func NewConfiguredWorldGatherClient(declaration domainmeeting.TaskMeetingDeclaration, lifeTokenFile string, timeout time.Duration) (TaskMeetingWorldGatherClient, error) {
	if err := declaration.Validate(); err != nil || lifeTokenFile == "" {
		return nil, fmt.Errorf("TaskMeeting World gather scope is unavailable")
	}
	readCredential := os.Getenv(declaration.WorldCredentialEnv)
	if readCredential == "" {
		return nil, fmt.Errorf("TaskMeeting World gather reader credential is unavailable")
	}
	lifeCredential, err := config.ReadRoutineWorldLifeBearerToken(lifeTokenFile)
	if err != nil {
		return nil, fmt.Errorf("TaskMeeting World gather life credential is unavailable: %w", err)
	}
	if readCredential == lifeCredential {
		return nil, fmt.Errorf("TaskMeeting World gather life credential must be distinct from the frozen reader credential")
	}
	actors := make([]string, 0, len(declaration.WorldActorBindings))
	for _, binding := range declaration.WorldActorBindings {
		actors = append(actors, binding.WorldActorID)
	}
	read, err := worldread.New(worldread.Config{BaseURL: declaration.WorldBaseURL, Credential: readCredential,
		WorldID: declaration.WorldID, TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch,
		ActorIDs: actors, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	life, err := worldread.NewLife(worldread.LifeConfig{BaseURL: declaration.WorldBaseURL, Credential: lifeCredential,
		WorldID: declaration.WorldID, TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch,
		ActorIDs: actors, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	if !sameWorldReadScope(read.ReadScope(), worldread.ReadScope{BaseURL: declaration.WorldBaseURL, WorldID: declaration.WorldID,
		TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch, ActorIDs: actors}) {
		return nil, fmt.Errorf("TaskMeeting World gather reader scope differs from frozen declaration")
	}
	return &taskMeetingWorldGatherClient{read: read, life: life}, nil
}

type worldGatherActor struct {
	coreID  string
	worldID string
	target  string
	actor   worldread.Actor
}

func ensureTaskMeetingGather(ctx context.Context, owner *WorldPresenceOwner, request domainmeeting.WorldObservationRequest, declaration domainmeeting.TaskMeetingDeclaration, verified *worldread.Snapshot) error {
	if owner == nil || owner.GatherClientFactory == nil || owner.AuthorizeGather == nil {
		return fmt.Errorf("%w: TaskMeeting WORLD gathering is not configured", appmeeting.ErrUnavailable)
	}
	client, err := owner.GatherClientFactory()
	if err != nil || client == nil {
		return fmt.Errorf("%w: TaskMeeting WORLD life client unavailable", appmeeting.ErrUnavailable)
	}
	reader, ok := owner.SnapshotReader.(TaskMeetingWorldSnapshotScopeReader)
	if !ok || !sameWorldReadScope(client.ReadScope(), reader.ReadScope()) {
		return fmt.Errorf("%w: TaskMeeting WORLD gather client differs from frozen reader scope", appmeeting.ErrUnavailable)
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("%w: TaskMeeting WORLD gather snapshot unavailable", appmeeting.ErrUnavailable)
	}
	if snapshot.WorldID != declaration.WorldID || snapshot.TimelineID != declaration.TimelineID || snapshot.TimelineEpoch != declaration.TimelineEpoch ||
		len(snapshot.Actors) != len(declaration.WorldActorBindings) {
		return fmt.Errorf("%w: WORLD gather snapshot differs from frozen registration scope", appmeeting.ErrUnavailable)
	}
	actors := make(map[string]worldread.Actor, len(snapshot.Actors))
	for _, actor := range snapshot.Actors {
		if _, exists := actors[actor.ActorID]; exists {
			return fmt.Errorf("%w: WORLD gather snapshot actor scope is ambiguous", appmeeting.ErrUnavailable)
		}
		actors[actor.ActorID] = actor
	}
	members := make([]worldGatherActor, 0, 8)
	for index, binding := range declaration.WorldActorBindings {
		actor, found := actors[binding.WorldActorID]
		if !found {
			return fmt.Errorf("%w: WORLD gather snapshot omitted a frozen actor", appmeeting.ErrUnavailable)
		}
		target := declaration.Rooms.Living
		if index >= 4 {
			target = declaration.Rooms.Dining
		}
		members = append(members, worldGatherActor{coreID: binding.CoreActorID, worldID: binding.WorldActorID, target: target, actor: actor})
	}
	var pending bool
	for _, member := range members {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !member.actor.Present {
			pending = true
			continue
		}
		if member.actor.ActivityKind != "" && member.actor.ActivityKind != "move" {
			pending = true
			continue
		}
		startIntent, intentErr := gatherIntent(request, member, domainmeeting.WorldGatherPhaseStart, domainmeeting.WorldGatherCommandReceipt{})
		if intentErr != nil {
			return intentErr
		}
		if err := reconcileGatherActor(ctx, owner, client, startIntent, member.actor, ownerWorldNow(owner).Before(request.ValidUntil)); err != nil {
			if errors.Is(err, domainmeeting.ErrNotReady) {
				pending = true
				continue
			}
			return err
		}
	}
	if pending {
		return domainmeeting.ErrNotReady
	}
	final, err := client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("%w: final WORLD gather snapshot unavailable", appmeeting.ErrUnavailable)
	}
	if final.WorldID != declaration.WorldID || final.TimelineID != declaration.TimelineID || final.TimelineEpoch != declaration.TimelineEpoch || len(final.Actors) != 8 {
		return fmt.Errorf("%w: final WORLD gather snapshot differs from frozen scope", appmeeting.ErrUnavailable)
	}
	finalActors := make(map[string]worldread.Actor, len(final.Actors))
	for _, actor := range final.Actors {
		if _, exists := finalActors[actor.ActorID]; exists {
			return fmt.Errorf("%w: final WORLD gather snapshot actor scope is ambiguous", appmeeting.ErrUnavailable)
		}
		finalActors[actor.ActorID] = actor
	}
	for _, binding := range declaration.WorldActorBindings {
		actor, ok := finalActors[binding.WorldActorID]
		if !ok || !actor.Present || (actor.RoomID != declaration.Rooms.Living && actor.RoomID != declaration.Rooms.Dining) || actor.ActivityKind != "" || actor.MovePhase != "" {
			return domainmeeting.ErrNotReady
		}
	}
	if verified != nil {
		*verified = final
	}
	return nil
}

// SettlePendingGather finishes only already persisted TaskMeeting moves. It
// never prepares a start intent, so terminal occurrences cannot create new
// movement while still recovering a due finish after scheduler deferral.
func (owner *WorldPresenceOwner) SettlePendingGather(ctx context.Context, request domainmeeting.WorldObservationRequest, declaration domainmeeting.TaskMeetingDeclaration) error {
	if owner == nil || owner.EventStore == nil || owner.TaskReader == nil || owner.AuthorizeRead == nil {
		return appmeeting.ErrUnavailable
	}
	type persistedGather struct {
		binding domainmeeting.WorldActorBinding
		intent  domainmeeting.WorldGatherCommandIntent
	}
	persistedMoves := make([]persistedGather, 0, len(declaration.WorldActorBindings))
	unknownStart := false
	for index, binding := range declaration.WorldActorBindings {
		start, err := gatherIntent(request, worldGatherActor{coreID: binding.CoreActorID, worldID: binding.WorldActorID,
			target: gatherTargetRoom(declaration, index)}, domainmeeting.WorldGatherPhaseStart, domainmeeting.WorldGatherCommandReceipt{})
		if err != nil {
			return err
		}
		persisted, startOutcome, found, err := owner.EventStore.FindTaskMeetingWorldGather(ctx, start, owner.TaskReader, owner.AuthorizeRead)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if startOutcome.Receipt.EventID == "" {
			unknownStart = true
			continue
		}
		if err := startOutcome.ValidateFor(persisted); err != nil {
			return err
		}
		finish, err := gatherIntent(persisted.Scope, worldGatherActor{coreID: persisted.ActorID, worldID: persisted.WorldActorID,
			target: persisted.TargetRoomID}, domainmeeting.WorldGatherPhaseFinish, startOutcome.Receipt)
		if err != nil {
			return err
		}
		_, finishOutcome, finishFound, err := owner.EventStore.FindTaskMeetingWorldGather(ctx, finish, owner.TaskReader, owner.AuthorizeRead)
		if err != nil {
			return err
		}
		if finishFound && finishOutcome.Receipt.EventID != "" {
			if err := finishOutcome.ValidateFor(finish); err != nil {
				return err
			}
			continue
		}
		persistedMoves = append(persistedMoves, persistedGather{binding: binding, intent: persisted})
	}
	if len(persistedMoves) == 0 {
		if unknownStart {
			return domainmeeting.ErrNotReady
		}
		return nil
	}
	if owner.GatherClientFactory == nil || owner.AuthorizeGather == nil {
		return appmeeting.ErrUnavailable
	}
	client, err := owner.GatherClientFactory()
	if err != nil || client == nil {
		return fmt.Errorf("%w: TaskMeeting WORLD life client unavailable", appmeeting.ErrUnavailable)
	}
	if !matchesConfiguredReadScope(client.ReadScope(), declaration) {
		return fmt.Errorf("%w: TaskMeeting gather client scope differs from frozen reader", appmeeting.ErrUnavailable)
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("%w: TaskMeeting WORLD settlement snapshot unavailable", appmeeting.ErrUnavailable)
	}
	actors := make(map[string]worldread.Actor, len(snapshot.Actors))
	for _, actor := range snapshot.Actors {
		actors[actor.ActorID] = actor
	}
	pending := false
	for _, operation := range persistedMoves {
		binding, persisted := operation.binding, operation.intent
		actor, exists := actors[binding.WorldActorID]
		if !exists {
			pending = true
			continue
		}
		if err := reconcileGatherActor(ctx, owner, client, persisted, actor, false); err != nil {
			if errors.Is(err, domainmeeting.ErrNotReady) {
				pending = true
				continue
			}
			return err
		}
	}
	if pending || unknownStart {
		return domainmeeting.ErrNotReady
	}
	return nil
}

func gatherTargetRoom(declaration domainmeeting.TaskMeetingDeclaration, index int) string {
	if index < 4 {
		return declaration.Rooms.Living
	}
	return declaration.Rooms.Dining
}

func gatherIntent(request domainmeeting.WorldObservationRequest, member worldGatherActor, phase domainmeeting.WorldGatherCommandPhase, started domainmeeting.WorldGatherCommandReceipt) (domainmeeting.WorldGatherCommandIntent, error) {
	payload := worldread.HomeActionPayload{ActorID: member.worldID, RoomID: member.target}
	encoded, _ := json.Marshal(payload)
	intent := domainmeeting.WorldGatherCommandIntent{Scope: request, ActorID: member.coreID, WorldActorID: member.worldID,
		TargetRoomID: member.target, Phase: phase, ActivityID: started.ActivityID, Payload: encoded}
	if phase == domainmeeting.WorldGatherPhaseFinish {
		intent.RouteID, intent.SceneSHA256, intent.DueAt = started.RouteID, started.SceneSHA256, started.DueAt
		start, err := gatherIntent(request, member, domainmeeting.WorldGatherPhaseStart, domainmeeting.WorldGatherCommandReceipt{})
		if err != nil {
			return domainmeeting.WorldGatherCommandIntent{}, err
		}
		intent.StartStableKey = start.StableKey
		intent.Payload, _ = json.Marshal(worldread.HomeActionPayload{ActorID: member.worldID, ActivityID: started.ActivityID})
	}
	stableKey, err := domainmeeting.WorldGatherCommandStableKey(intent)
	if err != nil {
		return domainmeeting.WorldGatherCommandIntent{}, err
	}
	intent.StableKey = stableKey
	return intent, nil
}

func reconcileGatherActor(ctx context.Context, owner *WorldPresenceOwner, client TaskMeetingWorldGatherClient, start domainmeeting.WorldGatherCommandIntent, actor worldread.Actor, allowNewStart bool) error {
	persisted, startOutcome, found, err := owner.EventStore.FindTaskMeetingWorldGather(ctx, start, owner.TaskReader, owner.AuthorizeRead)
	if err != nil {
		return err
	}
	if found {
		start = persisted
	}
	if !found {
		if actor.ActivityKind == "move" {
			return domainmeeting.ErrNotReady
		}
		if actor.RoomID == start.Scope.Rooms.Living || actor.RoomID == start.Scope.Rooms.Dining {
			return nil
		}
		if actor.ActivityKind != "" || !actor.Present {
			return domainmeeting.ErrNotReady
		}
		if !allowNewStart {
			return domainmeeting.ErrNotReady
		}
		intent, _, err := owner.EventStore.PrepareTaskMeetingWorldGather(ctx, start, owner.TaskReader, owner.AuthorizeRead, owner.AuthorizeGather)
		if err != nil {
			return err
		}
		if err := dispatchGatherStart(ctx, owner, client, intent); err != nil {
			return err
		}
		return domainmeeting.ErrNotReady
	}
	if startOutcome.Receipt.ActivityID == "" {
		if !allowNewStart {
			return domainmeeting.ErrNotReady
		}
		if err := dispatchGatherStart(ctx, owner, client, start); err != nil {
			return err
		}
		return domainmeeting.ErrNotReady
	}
	started := startOutcome.Receipt
	finish, err := gatherIntent(start.Scope, worldGatherActor{coreID: start.ActorID, worldID: start.WorldActorID, target: start.TargetRoomID}, domainmeeting.WorldGatherPhaseFinish, started)
	if err != nil {
		return err
	}
	finishIntent, finishOutcome, finishFound, err := owner.EventStore.FindTaskMeetingWorldGather(ctx, finish, owner.TaskReader, owner.AuthorizeRead)
	if err != nil {
		return err
	}
	if finishFound {
		finish = finishIntent
	}
	if finishFound && finishOutcome.Receipt.EventID != "" {
		if err := finishOutcome.ValidateFor(finishIntent); err != nil {
			return err
		}
		if !allowNewStart {
			return nil
		}
		return verifyGatherArrivalSnapshot(ctx, client, start, started)
	}
	if actor.ActivityKind == "move" {
		if actor.ActivityID != started.ActivityID || actor.TargetRoomID != started.TargetRoomID || actor.MoveRouteID != started.RouteID ||
			actor.HomeSceneSHA256 != started.SceneSHA256 || !actor.ActivityDueAt.Equal(started.DueAt) || actor.MovePhase != "in_transit" {
			return domainmeeting.ErrNotReady
		}
	} else if actor.ActivityKind != "" || actor.RoomID != started.TargetRoomID || !finishFound {
		return domainmeeting.ErrNotReady
	}
	now := ownerWorldNow(owner)
	if now.Before(started.DueAt) {
		return domainmeeting.ErrNotReady
	}
	if !finishFound {
		finishIntent, _, err = owner.EventStore.PrepareTaskMeetingWorldGather(ctx, finish, owner.TaskReader, owner.AuthorizeRead, owner.AuthorizeGather)
		if err != nil {
			return err
		}
	}
	if err := authorizeTaskMeetingGatherEffect(ctx, owner, finishIntent); err != nil {
		return err
	}
	result, commandErr := client.Command(ctx, finishIntent.StableKey, worldread.HomeActionFinish, worldread.HomeActionPayload{ActorID: start.WorldActorID, ActivityID: started.ActivityID})
	if commandErr != nil {
		return commandErr
	}
	finishOutcome, err = verifyGatherReceipt(ctx, client, finishIntent, result, "life.home.move.arrived", started)
	if err != nil {
		return err
	}
	if err := owner.EventStore.RecordTaskMeetingWorldGatherOutcome(ctx, finishIntent, finishOutcome, owner.TaskReader, owner.AuthorizeRead, owner.AuthorizeGather); err != nil {
		return err
	}
	return verifyGatherArrivalSnapshot(ctx, client, start, started)
}

func ownerWorldNow(owner *WorldPresenceOwner) time.Time {
	if owner != nil && owner.Now != nil {
		return owner.Now().UTC()
	}
	return time.Now().UTC()
}

func dispatchGatherStart(ctx context.Context, owner *WorldPresenceOwner, client TaskMeetingWorldGatherClient, intent domainmeeting.WorldGatherCommandIntent) error {
	if err := authorizeTaskMeetingGatherEffect(ctx, owner, intent); err != nil {
		return err
	}
	result, commandErr := client.Command(ctx, intent.StableKey, worldread.HomeActionMove, worldread.HomeActionPayload{ActorID: intent.WorldActorID, RoomID: intent.TargetRoomID})
	if commandErr != nil {
		return commandErr
	}
	outcome, err := verifyGatherReceipt(ctx, client, intent, result, "life.home.move.started", domainmeeting.WorldGatherCommandReceipt{})
	if err != nil {
		return err
	}
	return owner.EventStore.RecordTaskMeetingWorldGatherOutcome(ctx, intent, outcome, owner.TaskReader, owner.AuthorizeRead, owner.AuthorizeGather)
}

func verifyGatherArrivalSnapshot(ctx context.Context, client TaskMeetingWorldGatherClient, start domainmeeting.WorldGatherCommandIntent, started domainmeeting.WorldGatherCommandReceipt) error {
	fresh, err := client.Snapshot(ctx)
	if err != nil {
		return err
	}
	arrived, ok := gatherActor(fresh, start.WorldActorID)
	if !ok || !arrived.Present || arrived.RoomID != start.TargetRoomID || arrived.ActivityKind == "move" || arrived.ActivityID == started.ActivityID {
		return domainmeeting.ErrNotReady
	}
	return nil
}

func verifyGatherReceipt(ctx context.Context, client TaskMeetingWorldGatherClient, intent domainmeeting.WorldGatherCommandIntent, result worldread.CommandResult, expectedKind string, started domainmeeting.WorldGatherCommandReceipt) (domainmeeting.WorldGatherCommandOutcome, error) {
	receipt := result.Receipt
	if receipt.Status != "committed" || receipt.ReceiptID == "" || receipt.TransactionID == "" || receipt.FirstSeq == 0 || receipt.LastSeq < receipt.FirstSeq || receipt.StateSeq < receipt.LastSeq || len(receipt.EventIDs) == 0 {
		return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD gather command did not return a committed bounded receipt")
	}
	batch, err := client.Events(ctx, receipt.FirstSeq-1)
	if err != nil {
		return domainmeeting.WorldGatherCommandOutcome{}, err
	}
	if batch.NextSeq < receipt.LastSeq {
		return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD gather receipt events are incomplete")
	}
	wanted := make(map[string]struct{}, len(receipt.EventIDs))
	for _, id := range receipt.EventIDs {
		wanted[id] = struct{}{}
	}
	var selected *worldread.Event
	found := make(map[string]struct{}, len(wanted))
	for index := range batch.Events {
		event := &batch.Events[index]
		if _, ok := wanted[event.EventID]; !ok {
			continue
		}
		if event.TransactionID != receipt.TransactionID || event.Kind != expectedKind || event.Seq < receipt.FirstSeq || event.Seq > receipt.LastSeq {
			return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD gather Event does not match its exact receipt")
		}
		var eventFields map[string]json.RawMessage
		if json.Unmarshal(event.Payload, &eventFields) != nil {
			return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD gather Event payload is invalid")
		}
		var eventActor, eventActivity string
		if json.Unmarshal(eventFields["actor_id"], &eventActor) != nil || eventActor != intent.WorldActorID ||
			json.Unmarshal(eventFields["activity_id"], &eventActivity) != nil || (selected != nil && eventActivity != stringField(selected.Payload, "activity_id")) {
			return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD gather receipt Event actor or activity does not match exact intent")
		}
		found[event.EventID] = struct{}{}
		selected = event
	}
	if selected == nil || len(found) != len(wanted) {
		return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD gather receipt EventIDs are not present in the authenticated event page")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(selected.Payload, &fields) != nil {
		return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD gather lifecycle payload is invalid")
	}
	var actorID, activityID, target, room, route, scene string
	if json.Unmarshal(fields["actor_id"], &actorID) != nil || actorID != intent.WorldActorID || json.Unmarshal(fields["activity_id"], &activityID) != nil {
		return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD gather lifecycle event does not match the exact actor and activity")
	}
	_ = json.Unmarshal(fields["target_room_id"], &target)
	_ = json.Unmarshal(fields["room_id"], &room)
	_ = json.Unmarshal(fields["route_id"], &route)
	_ = json.Unmarshal(fields["scene_sha256"], &scene)
	var due time.Time
	_ = json.Unmarshal(fields["due_at"], &due)
	outcome := domainmeeting.WorldGatherCommandOutcome{StableKey: intent.StableKey, Phase: intent.Phase,
		Receipt: domainmeeting.WorldGatherCommandReceipt{ReceiptID: receipt.ReceiptID, EventID: selected.EventID, EventSeq: selected.Seq,
			ActivityID: activityID, RouteID: route, SceneSHA256: scene, TargetRoomID: target, DueAt: due.UTC()}}
	if intent.Phase == domainmeeting.WorldGatherPhaseStart && (activityID == "" || target != intent.TargetRoomID || route == "" || scene == "" || due.IsZero() || selected.RecordedAt.IsZero()) {
		return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD move start event omitted verified lifecycle bindings")
	}
	if intent.Phase == domainmeeting.WorldGatherPhaseFinish {
		if activityID != started.ActivityID || (target != "" && target != started.TargetRoomID) || (room != "" && room != started.TargetRoomID) ||
			(route != "" && route != started.RouteID) || (scene != "" && scene != started.SceneSHA256) || (!due.IsZero() && !due.Equal(started.DueAt)) {
			return domainmeeting.WorldGatherCommandOutcome{}, errors.New("WORLD move arrival event differs from its exact started activity")
		}
		outcome.Receipt.RouteID, outcome.Receipt.SceneSHA256 = started.RouteID, started.SceneSHA256
		outcome.Receipt.TargetRoomID, outcome.Receipt.DueAt = started.TargetRoomID, started.DueAt
	}
	if err := outcome.ValidateFor(intent); err != nil {
		return domainmeeting.WorldGatherCommandOutcome{}, err
	}
	return outcome, nil
}

func stringField(payload json.RawMessage, name string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(fields[name], &value)
	return value
}

func gatherActor(snapshot worldread.Snapshot, actorID string) (worldread.Actor, bool) {
	for _, actor := range snapshot.Actors {
		if actor.ActorID == actorID {
			return actor, true
		}
	}
	return worldread.Actor{}, false
}

func authorizeTaskMeetingGatherEffect(ctx context.Context, owner *WorldPresenceOwner, intent domainmeeting.WorldGatherCommandIntent) error {
	if owner == nil || owner.TaskReader == nil || owner.AuthorizeRead == nil || owner.AuthorizeGather == nil {
		return appmeeting.ErrUnavailable
	}
	task, run, shared, err := owner.TaskReader.GetTaskMeetingInvocationState(ctx, intent.Scope.TaskID, intent.Scope.RunID, owner.AuthorizeRead)
	if err != nil {
		return err
	}
	if task.TaskID != intent.Scope.TaskID || run.RunID != intent.Scope.RunID || run.TaskID != task.TaskID || run.Status != "running" {
		return appmeeting.ErrUnavailable
	}
	return owner.AuthorizeGather(ctx, task, run, shared, intent)
}
