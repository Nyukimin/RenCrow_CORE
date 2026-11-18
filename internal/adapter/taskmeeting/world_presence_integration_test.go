package taskmeeting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestWorldPresenceOwnerReplaysReceiptBeforeWorldSnapshot(t *testing.T) {
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
	eventStore, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "meeting.sqlite"))
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
	actorIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		actorIDs = append(actorIDs, binding.WorldActorID)
	}
	client, err := worldread.New(worldread.Config{BaseURL: server.URL, Credential: "private-world-token", WorldID: declaration.WorldID, TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch, ActorIDs: actorIDs})
	if err != nil {
		t.Fatal(err)
	}
	owner := WorldPresenceOwner{SnapshotReader: client, EventStore: eventStore, TaskReader: taskStore, AuthorizeRead: stateAuth, CredentialEnv: declaration.WorldCredentialEnv,
		AuthorizeObserve: func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domainmeeting.WorldObservationRequest) error {
			return nil
		},
		Now: func() time.Time { return meeting.ScheduledAt.Add(time.Minute) }}
	requestID := key.String() + "|gather"
	first, firstPresence, err := owner.GatherAndRead(ctx, meeting, requestID, session.ThreadID)
	if err != nil {
		t.Fatalf("observe exact frozen scope: %v", err)
	}
	if snapshotCalls.Load() != 1 || len(firstPresence) != 8 || first.EvidenceID.Validate() != nil {
		t.Fatalf("first observation calls=%d evidence=%+v presence=%d", snapshotCalls.Load(), first, len(firstPresence))
	}
	second, secondPresence, err := owner.GatherAndRead(ctx, meeting, requestID, session.ThreadID)
	if err != nil {
		t.Fatalf("recover saved World receipt: %v", err)
	}
	if snapshotCalls.Load() != 1 || second.EvidenceID != first.EvidenceID || second.Snapshot.OwnerProofID != first.Snapshot.OwnerProofID || len(secondPresence) != 8 || secondPresence[0].EvidenceID != firstPresence[0].EvidenceID {
		t.Fatalf("receipt replay called WORLD or changed evidence: calls=%d first=%+v second=%+v", snapshotCalls.Load(), first, second)
	}
	changed := meeting
	changed.WorldID = "retargeted"
	if _, _, err := owner.GatherAndRead(ctx, changed, requestID, session.ThreadID); err == nil {
		t.Fatal("retargeted World config accepted")
	}
	if snapshotCalls.Load() != 1 {
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
