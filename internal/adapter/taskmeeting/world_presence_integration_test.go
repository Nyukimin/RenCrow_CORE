package taskmeeting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestWorldPresenceOwnerGathersBeforeSnapshotAndRecoversImmutableCommands(t *testing.T) {
	ctx := context.Background()
	var snapshotCalls atomic.Int32
	declaration, audience, bindings := worldPresenceTestDeclaration()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		snapshotCalls.Add(1)
		actors := make(map[string]any, len(bindings))
		for _, binding := range bindings {
			actors[binding.WorldActorID] = map[string]any{"room_id": declaration.Rooms.Living}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "world_id": declaration.WorldID, "timeline_id": declaration.TimelineID,
			"timeline_epoch": declaration.TimelineEpoch, "state_seq": 7, "state_hash": strings.Repeat("a", 64),
			"state": map[string]any{"actors": actors, "activities": map[string]any{}, "resources": map[string]any{}, "reservations": []any{}}})
	}))
	defer server.Close()
	declaration.WorldBaseURL = server.URL
	frozenDeclaration, _, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		t.Fatal(err)
	}
	eventStorePath := filepath.Join(t.TempDir(), "meeting.sqlite")
	eventStore, err := eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer eventStore.Close()
	taskRoot, err := os.MkdirTemp(".", ".taskmeeting-world-presence-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(taskRoot) })
	taskStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer taskStore.Close()
	manager := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	registration, err := eventStore.EnsureTaskMeetingScheduleRegistration(ctx, eventstore.TaskMeetingRegistrationRequest{Declaration: declaration, FrozenDeclaration: frozenDeclaration},
		func(context.Context, domainmeeting.TaskMeetingDeclaration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	key := domainmeeting.OccurrenceKey{ScheduleID: registration.ScheduleID, LocalDate: "2026-11-18", Slot: "08:00", Timezone: domainmeeting.TimezoneJST}
	canonicalConfig, configSHA, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		t.Fatal(err)
	}
	frozenInput := []byte(`{"operation":"taskmeeting.root"}`)
	inputHash := sha256.Sum256(frozenInput)
	rootRequest := domaintask.TaskMeetingInvocationRequest{OccurrenceKey: key.String(), Purpose: domaintask.TaskMeetingPurposeRoot,
		CoordinatorActorID: declaration.CoordinatorActorID, ActorID: declaration.CoordinatorActorID, ConfiguredOwnerUserID: registration.OwnerUserID,
		ConfigRevision: registration.Revision, RegistrationEvidenceID: registration.RegistrationEvidenceID, FrozenConfig: canonicalConfig,
		Audience: append([]string(nil), audience...), FrozenInput: frozenInput, Timing: declaration.Timing}
	stateAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	root, rootRun, rootProof, err := manager.EnsureTaskMeetingRootInvocation(ctx, rootRequest,
		func(context.Context, domaintask.TaskMeetingInvocationRequest, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error {
			return nil
		}, func(context.Context) error { return nil }, stateAuth)
	if err != nil {
		t.Fatal(err)
	}
	_, err = eventStore.PinTaskMeetingOccurrenceRegistration(ctx, registration, key, func(context.Context, eventstore.TaskMeetingScheduleRegistrationReceipt, domainmeeting.OccurrenceKey) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	groupRequest := eventstore.TaskMeetingGroupSessionRequest{OccurrenceKey: key.String(), TaskID: root.TaskID, RunID: rootRun.RunID, OwnerUserID: registration.OwnerUserID,
		CoordinatorActorID: declaration.CoordinatorActorID, RegistrationEvidenceID: registration.RegistrationEvidenceID, ConfigRevision: registration.Revision,
		ConfigSHA256: configSHA, InputSHA256: hex.EncodeToString(inputHash[:]), RequestID: "meeting-session:" + key.String(), Audience: append([]string(nil), audience...)}
	session, err := eventStore.EnsureTaskMeetingGroupSession(ctx, groupRequest, taskStore, stateAuth, domaintask.TaskMeetingStateAuthorizationCheck(stateAuth))
	if err != nil {
		t.Fatal(err)
	}
	config := domainmeeting.ConfigSnapshot{ScheduleID: registration.ScheduleID, Revision: registration.Revision, Timing: declaration.Timing,
		WorldBaseURL: declaration.WorldBaseURL, WorldCredentialEnv: declaration.WorldCredentialEnv, WorldID: declaration.WorldID, TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch,
		ConfiguredActors: append([]string(nil), audience...), WorldActorBindings: append([]domainmeeting.WorldActorBinding(nil), bindings...), RegisteredActors: append([]string(nil), audience...),
		RegistrationEvidenceID: registration.RegistrationEvidenceID, Rooms: declaration.Rooms, MeetingDeadlineAfterSecs: declaration.MeetingDeadlineAfterSecs}
	meeting, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BindNativeExecution(meeting.Guard(), root.TaskID, rootRun.RunID, rootProof.AdmissionEvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	now := meeting.ScheduledAt.Add(time.Minute)
	meeting, err = meeting.BeginGathering(meeting.Guard(), now)
	if err != nil {
		t.Fatalf("begin gathering: %v", err)
	}
	actorIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		actorIDs = append(actorIDs, binding.WorldActorID)
	}
	client, err := worldread.New(worldread.Config{BaseURL: server.URL, Credential: "private-world-token", WorldID: declaration.WorldID, TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch, ActorIDs: actorIDs})
	if err != nil {
		t.Fatal(err)
	}
	fakeGather := newTaskMeetingGatherWorldFixture(declaration, bindings, func() time.Time { return now })
	fakeGather.loseStartActor = bindings[1].WorldActorID
	owner := WorldPresenceOwner{SnapshotReader: client, GatherRequired: true,
		GatherClientFactory: func() (TaskMeetingWorldGatherClient, error) { return fakeGather, nil },
		EventStore:          eventStore, TaskReader: taskStore, AuthorizeRead: stateAuth, CredentialEnv: declaration.WorldCredentialEnv,
		AuthorizeGather: func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domainmeeting.WorldGatherCommandIntent) error {
			return nil
		},
		AuthorizeObserve: func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domainmeeting.WorldObservationRequest) error {
			return nil
		},
		Now: func() time.Time { return now }}
	settlementScope := domainmeeting.WorldObservationRequest{Key: key, TaskID: root.TaskID, RunID: rootRun.RunID,
		ConfigRevision: registration.Revision, ConfigSHA256: configSHA, OwnerUserID: registration.OwnerUserID,
		RegistrationEvidenceID: registration.RegistrationEvidenceID, CoordinatorActorID: declaration.CoordinatorActorID,
		Session: session.SessionID, Thread: session.ThreadID, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Audience: append([]string(nil), audience...), ActorBindings: append([]domainmeeting.WorldActorBinding(nil), bindings...), Rooms: declaration.Rooms}
	neverStarted := owner
	neverStarted.GatherClientFactory, neverStarted.AuthorizeGather = nil, nil
	if err := neverStarted.SettlePendingGather(ctx, settlementScope, declaration); err != nil || snapshotCalls.Load() != 0 {
		t.Fatalf("settle terminal occurrence with no owned move/credentials: err=%v source_snapshots=%d", err, snapshotCalls.Load())
	}
	firstMember := worldGatherActor{coreID: bindings[0].CoreActorID, worldID: bindings[0].WorldActorID, target: declaration.Rooms.Living}
	unknownStart, err := gatherIntent(settlementScope, firstMember, domainmeeting.WorldGatherPhaseStart, domainmeeting.WorldGatherCommandReceipt{})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := eventStore.PrepareTaskMeetingWorldGather(ctx, unknownStart, taskStore, stateAuth,
		func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domainmeeting.WorldGatherCommandIntent) error {
			return nil
		}); err != nil || !created {
		t.Fatalf("prepare unknown start fixture: created=%t err=%v", created, err)
	}
	terminalUnknown := owner
	terminalUnknown.Now = func() time.Time { return meeting.DeadlineAt.Add(time.Second) }
	terminalUnknown.GatherClientFactory, terminalUnknown.AuthorizeGather = nil, nil
	if err := terminalUnknown.SettlePendingGather(ctx, settlementScope, declaration); !errors.Is(err, domainmeeting.ErrNotReady) {
		t.Fatalf("terminal unknown start reconciliation = %v, want pending safe unknown", err)
	}
	if got := fakeGather.commandAttemptsForActor(worldread.HomeActionMove, bindings[0].WorldActorID); got != 0 {
		t.Fatalf("terminal cleanup dispatched an unknown/new move: command attempts=%d", got)
	}
	requestID := key.String() + "|gather"
	if _, _, err := owner.GatherAndRead(ctx, meeting, requestID, session.ThreadID); !errors.Is(err, errWorldGatherResponseLost) {
		t.Fatalf("lost move-start response = %v, want injected response loss after effect", err)
	}
	if snapshotCalls.Load() != 0 {
		t.Fatalf("intermediate absent/in-transit snapshot was cached: calls=%d", snapshotCalls.Load())
	}
	if err := eventStore.Close(); err != nil {
		t.Fatal(err)
	}
	eventStore, err = eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("reopen gather journal after lost start response: %v", err)
	}
	defer eventStore.Close()
	owner.EventStore = eventStore
	if _, _, err := owner.GatherAndRead(ctx, meeting, requestID, session.ThreadID); !errors.Is(err, domainmeeting.ErrNotReady) {
		t.Fatalf("reconcile move start and prepare eight actors: %v, want not-ready pending due", err)
	}
	if fakeGather.actionCount(worldread.HomeActionMove) != 8 {
		t.Fatalf("stable start replays/new actor starts = %d, want exactly eight start operations", fakeGather.actionCount(worldread.HomeActionMove))
	}
	now = meeting.ScheduledAt.Add(3 * time.Minute)
	fakeGather.loseFinishActor = bindings[0].WorldActorID
	if _, _, err := owner.GatherAndRead(ctx, meeting, requestID, session.ThreadID); !errors.Is(err, errWorldGatherResponseLost) {
		t.Fatalf("lost finish response = %v, want injected response loss after effect", err)
	}
	if err := eventStore.Close(); err != nil {
		t.Fatal(err)
	}
	eventStore, err = eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatalf("reopen gather journal after lost finish response: %v", err)
	}
	defer eventStore.Close()
	owner.EventStore = eventStore
	firstActor := bindings[0].WorldActorID
	arrivedState := fakeGather.actors[firstActor]
	fakeGather.replaceActorWithForeignMove(firstActor, declaration.Rooms.Living)
	finishCalls := fakeGather.commandAttemptsForActor(worldread.HomeActionFinish, firstActor)
	if _, _, err := owner.GatherAndRead(ctx, meeting, requestID, session.ThreadID); !errors.Is(err, domainmeeting.ErrNotReady) {
		t.Fatalf("replacement activity reconciliation = %v, want not-ready", err)
	}
	if got := fakeGather.commandAttemptsForActor(worldread.HomeActionFinish, firstActor); got != finishCalls {
		t.Fatalf("foreign replacement activity was finished: finish calls=%d before=%d", got, finishCalls)
	}
	fakeGather.actors[firstActor] = arrivedState
	first, firstPresence, err := owner.GatherAndRead(ctx, meeting, requestID, session.ThreadID)
	if err != nil {
		t.Fatalf("replay exact finish key and record final snapshot: %v", err)
	}
	if snapshotCalls.Load() != 0 || len(firstPresence) != 8 || first.EvidenceID.Validate() != nil {
		t.Fatalf("final observation calls=%d evidence=%+v presence=%d", snapshotCalls.Load(), first, len(firstPresence))
	}
	for index, evidence := range firstPresence {
		wantRoom := declaration.Rooms.Living
		if index >= 4 {
			wantRoom = declaration.Rooms.Dining
		}
		if evidence.RoomRef != wantRoom {
			t.Fatalf("actor index %d room=%q want configured deterministic room %q", index, evidence.RoomRef, wantRoom)
		}
	}
	second, secondPresence, err := owner.GatherAndRead(ctx, meeting, requestID, session.ThreadID)
	if err != nil {
		t.Fatalf("recover saved World observation receipt: %v", err)
	}
	if snapshotCalls.Load() != 0 || second.EvidenceID != first.EvidenceID || second.Snapshot.OwnerProofID != first.Snapshot.OwnerProofID || len(secondPresence) != 8 || secondPresence[0].EvidenceID != firstPresence[0].EvidenceID {
		t.Fatalf("receipt replay called WORLD or changed evidence: calls=%d first=%+v second=%+v", snapshotCalls.Load(), first, second)
	}
	finishedState := fakeGather.actors[firstActor]
	fakeGather.replaceActorWithForeignMove(firstActor, declaration.Rooms.Living)
	completedWithoutCredentials := owner
	completedWithoutCredentials.GatherClientFactory, completedWithoutCredentials.AuthorizeGather = nil, nil
	finishAttempts := fakeGather.commandAttemptsForActor(worldread.HomeActionFinish, firstActor)
	if err := completedWithoutCredentials.SettlePendingGather(ctx, settlementScope, declaration); err != nil {
		t.Fatalf("terminal cleanup after all owned finishes: %v", err)
	}
	if got := fakeGather.commandAttemptsForActor(worldread.HomeActionFinish, firstActor); got != finishAttempts {
		t.Fatalf("terminal cleanup touched replacement activity: finish attempts=%d before=%d", got, finishAttempts)
	}
	fakeGather.actors[firstActor] = finishedState
	refresh, refreshPresence, err := owner.GatherAndRead(ctx, meeting, requestID+"|refresh", session.ThreadID)
	if err != nil || refresh.EvidenceID == first.EvidenceID || len(refreshPresence) != 8 || snapshotCalls.Load() != 0 {
		t.Fatalf("read-only post-gather refresh: evidence=%+v presence=%d source_snapshots=%d err=%v", refresh, len(refreshPresence), snapshotCalls.Load(), err)
	}
	changed := meeting
	changed.WorldID = "retargeted"
	if _, _, err := owner.GatherAndRead(ctx, changed, requestID, session.ThreadID); err == nil {
		t.Fatal("retargeted World config accepted")
	}
	if snapshotCalls.Load() != 0 {
		t.Fatalf("retargeted config reached WORLD: calls=%d", snapshotCalls.Load())
	}
}

func worldPresenceTestDeclaration() (domainmeeting.TaskMeetingDeclaration, []string, []domainmeeting.WorldActorBinding) {
	audience := []string{"mio", "shiro", "aka", "ao", "kin", "gin", "midori", "kuro"}
	bindings := make([]domainmeeting.WorldActorBinding, 0, len(audience))
	for _, actor := range audience {
		bindings = append(bindings, domainmeeting.WorldActorBinding{CoreActorID: actor, WorldActorID: "world-" + actor})
	}
	declaration := domainmeeting.TaskMeetingDeclaration{RegistrationKey: domainmeeting.TaskMeetingRegistrationKey, OwnerUserID: "owner-ren", Revision: 1, DataScope: "user",
		CoordinatorActorID: "mio", Audience: audience, WorldActorBindings: bindings, WorldBaseURL: "http://127.0.0.1:8080", WorldCredentialEnv: "REN_WORLD_TOKEN",
		WorldID: "home-world", TimelineID: "main-timeline", TimelineEpoch: "epoch-17", Rooms: domainmeeting.RoomRefs{Living: "room-living", Dining: "room-dining"},
		Timing: modulecore.TaskTiming{Timezone: domainmeeting.TimezoneJST, AllowedWindows: []modulecore.TaskTimeWindow{}, Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00", "12:00", "17:00"}, Weekdays: []time.Weekday{}}}, MeetingDeadlineAfterSecs: 3600}
	return declaration, audience, bindings
}

func TestConfiguredTaskMeetingGatherClientUsesFrozenScopeAndDistinctLifeCredential(t *testing.T) {
	declaration, _, _ := worldPresenceTestDeclaration()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	declaration.WorldBaseURL = server.URL
	t.Setenv(declaration.WorldCredentialEnv, "reader-token")
	lifePath := filepath.Join(t.TempDir(), "life.token")
	if err := os.WriteFile(lifePath, []byte("life-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewConfiguredWorldGatherClient(declaration, lifePath, time.Second)
	if err != nil {
		t.Fatalf("build scoped gather client: %v", err)
	}
	if !matchesConfiguredReadScope(client.ReadScope(), declaration) || requests.Load() != 0 {
		t.Fatalf("gather client scope=%+v requests=%d; want exact frozen scope and no request", client.ReadScope(), requests.Load())
	}
	if err := os.WriteFile(lifePath, []byte("reader-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewConfiguredWorldGatherClient(declaration, lifePath, time.Second); err == nil {
		t.Fatal("life token identical to frozen reader token was accepted")
	}
	if requests.Load() != 0 {
		t.Fatalf("client construction sent %d WORLD requests", requests.Load())
	}
}

var errWorldGatherResponseLost = errors.New("injected WORLD response loss after committed command")

type taskMeetingGatherWorldFixture struct {
	declaration     domainmeeting.TaskMeetingDeclaration
	now             func() time.Time
	actors          map[string]worldread.Actor
	events          []worldread.Event
	commands        map[string]worldread.CommandResult
	actions         map[string]int
	actionsByActor  map[string]int
	commandAttempts map[string]int
	loseStartActor  string
	loseFinishActor string
	stateSeq        uint64
	snapshotCalls   int
}

func newTaskMeetingGatherWorldFixture(declaration domainmeeting.TaskMeetingDeclaration, bindings []domainmeeting.WorldActorBinding, now func() time.Time) *taskMeetingGatherWorldFixture {
	fixture := &taskMeetingGatherWorldFixture{declaration: declaration, now: now, actors: map[string]worldread.Actor{},
		commands: map[string]worldread.CommandResult{}, actions: map[string]int{}, actionsByActor: map[string]int{}, commandAttempts: map[string]int{}}
	for _, binding := range bindings {
		fixture.actors[binding.WorldActorID] = worldread.Actor{ActorID: binding.WorldActorID, Present: true, RoomID: "outside-meeting-venue"}
	}
	return fixture
}

func (fixture *taskMeetingGatherWorldFixture) ReadScope() worldread.ReadScope {
	ids := make([]string, 0, len(fixture.declaration.WorldActorBindings))
	for _, binding := range fixture.declaration.WorldActorBindings {
		ids = append(ids, binding.WorldActorID)
	}
	sort.Strings(ids)
	return worldread.ReadScope{BaseURL: fixture.declaration.WorldBaseURL, WorldID: fixture.declaration.WorldID,
		TimelineID: fixture.declaration.TimelineID, TimelineEpoch: fixture.declaration.TimelineEpoch, ActorIDs: ids}
}

func (fixture *taskMeetingGatherWorldFixture) Snapshot(ctx context.Context) (worldread.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return worldread.Snapshot{}, err
	}
	fixture.snapshotCalls++
	actors := make([]worldread.Actor, 0, len(fixture.actors))
	for _, binding := range fixture.declaration.WorldActorBindings {
		actors = append(actors, fixture.actors[binding.WorldActorID])
	}
	hash := sha256.Sum256([]byte("taskmeeting gathering fixture snapshot"))
	return worldread.Snapshot{SchemaVersion: 1, WorldID: fixture.declaration.WorldID, TimelineID: fixture.declaration.TimelineID,
		TimelineEpoch: fixture.declaration.TimelineEpoch, StateSeq: fixture.stateSeq + 1, StateHash: hex.EncodeToString(hash[:]), Actors: actors}, nil
}

func (fixture *taskMeetingGatherWorldFixture) Events(ctx context.Context, after uint64) (worldread.EventBatch, error) {
	if err := ctx.Err(); err != nil {
		return worldread.EventBatch{}, err
	}
	events := make([]worldread.Event, 0, len(fixture.events))
	for _, event := range fixture.events {
		if event.Seq > after {
			events = append(events, event)
		}
	}
	return worldread.EventBatch{Events: events, NextSeq: fixture.stateSeq, Done: true}, nil
}

func (fixture *taskMeetingGatherWorldFixture) Command(ctx context.Context, key, action string, payload worldread.HomeActionPayload) (worldread.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return worldread.CommandResult{}, err
	}
	fixture.commandAttempts[action+"\x00"+payload.ActorID]++
	if existing, ok := fixture.commands[key]; ok {
		return existing, nil
	}
	actor, ok := fixture.actors[payload.ActorID]
	if !ok {
		return worldread.CommandResult{}, errors.New("fake WORLD rejected foreign actor")
	}
	if action == worldread.HomeActionMove {
		activityID := "taskmeeting-move-" + payload.ActorID
		dueAt := fixture.now().Add(time.Minute).UTC()
		routeID := "route-" + payload.ActorID
		sceneHash := strings.Repeat("b", 64)
		actor.ActivityID, actor.ActivityKind, actor.TargetRoomID = activityID, "move", payload.RoomID
		actor.ActivityDueAt, actor.ActivityStartedAt, actor.MovePhase = dueAt, fixture.now().UTC(), "in_transit"
		actor.MoveRouteID, actor.HomeSceneSHA256 = routeID, sceneHash
		fixture.actors[payload.ActorID] = actor
		result := fixture.appendGatherEvent(key, "life.home.move.started", map[string]any{"actor_id": payload.ActorID,
			"activity_id": activityID, "activity_kind": "move", "target_room_id": payload.RoomID, "route_id": routeID,
			"scene_sha256": sceneHash, "started_at": actor.ActivityStartedAt, "due_at": dueAt})
		if payload.ActorID == fixture.loseStartActor {
			fixture.loseStartActor = ""
			return worldread.CommandResult{}, errWorldGatherResponseLost
		}
		return result, nil
	}
	if action != worldread.HomeActionFinish || actor.ActivityKind != "move" || actor.ActivityID != payload.ActivityID || fixture.now().Before(actor.ActivityDueAt) {
		return worldread.CommandResult{}, errors.New("fake WORLD rejected non-owned or not-due finish")
	}
	target, activityID, routeID, sceneHash := actor.TargetRoomID, actor.ActivityID, actor.MoveRouteID, actor.HomeSceneSHA256
	actor.RoomID, actor.ActivityID, actor.ActivityKind, actor.TargetRoomID = target, "", "", ""
	actor.MovePhase, actor.MoveRouteID, actor.ActivityDueAt, actor.ActivityStartedAt = "", "", time.Time{}, time.Time{}
	fixture.actors[payload.ActorID] = actor
	result := fixture.appendGatherEvent(key, "life.home.move.arrived", map[string]any{"actor_id": payload.ActorID,
		"activity_id": activityID, "room_id": target, "route_id": routeID, "scene_sha256": sceneHash})
	if payload.ActorID == fixture.loseFinishActor {
		fixture.loseFinishActor = ""
		return worldread.CommandResult{}, errWorldGatherResponseLost
	}
	return result, nil
}

func (fixture *taskMeetingGatherWorldFixture) appendGatherEvent(key, kind string, payload map[string]any) worldread.CommandResult {
	fixture.stateSeq++
	encoded, _ := json.Marshal(payload)
	event := worldread.Event{Seq: fixture.stateSeq, TransactionID: "transaction-" + key, EventID: "event-" + key, Kind: kind, Payload: encoded, RecordedAt: fixture.now().UTC()}
	fixture.events = append(fixture.events, event)
	result := worldread.CommandResult{SchemaVersion: 1, Receipt: worldread.CommandReceipt{ReceiptID: "receipt-" + key, Status: "committed",
		TransactionID: event.TransactionID, EventIDs: []string{event.EventID}, FirstSeq: event.Seq, LastSeq: event.Seq, StateSeq: event.Seq, RecordedAt: event.RecordedAt}}
	fixture.commands[key] = result
	fixture.actions[kind]++
	actorID, _ := payload["actor_id"].(string)
	fixture.actionsByActor[kind+"\x00"+actorID]++
	return result
}

func (fixture *taskMeetingGatherWorldFixture) actionCount(action string) int {
	kind := "life.home.move.started"
	if action == worldread.HomeActionFinish {
		kind = "life.home.move.arrived"
	}
	return fixture.actions[kind]
}

func (fixture *taskMeetingGatherWorldFixture) actionCountForActor(action, actorID string) int {
	kind := "life.home.move.started"
	if action == worldread.HomeActionFinish {
		kind = "life.home.move.arrived"
	}
	return fixture.actionsByActor[kind+"\x00"+actorID]
}

func (fixture *taskMeetingGatherWorldFixture) commandAttemptsForActor(action, actorID string) int {
	return fixture.commandAttempts[action+"\x00"+actorID]
}

func (fixture *taskMeetingGatherWorldFixture) replaceActorWithForeignMove(actorID, destination string) {
	actor := fixture.actors[actorID]
	actor.ActivityID, actor.ActivityKind, actor.TargetRoomID = "foreign-move-instance", "move", destination
	actor.MovePhase, actor.MoveRouteID, actor.HomeSceneSHA256 = "in_transit", "foreign-route", strings.Repeat("c", 64)
	actor.ActivityDueAt = fixture.now().Add(-time.Second)
	fixture.actors[actorID] = actor
}
