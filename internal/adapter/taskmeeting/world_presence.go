// Package taskmeeting adapts CORE's authenticated WORLD snapshot client to the
// frozen TaskMeeting observation owner. It reads only the exact registered
// actor scope and never performs a WORLD write.
package taskmeeting

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	appmeeting "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmeeting"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type TaskMeetingWorldSnapshotReader interface {
	Snapshot(context.Context) (worldread.Snapshot, error)
}

type TaskMeetingWorldSnapshotScopeReader interface {
	TaskMeetingWorldSnapshotReader
	ReadScope() worldread.ReadScope
}

// TaskMeetingScopedWorldSnapshotClient exposes the source-level scope needed
// to bind a freshly built authenticated client to one frozen declaration.
type TaskMeetingScopedWorldSnapshotClient interface {
	TaskMeetingWorldSnapshotReader
	ReadScope() worldread.ReadScope
}

// TaskMeetingWorldSnapshotClientFactory creates a new authenticated client
// only when an actual source snapshot is required.
type TaskMeetingWorldSnapshotClientFactory func() (TaskMeetingScopedWorldSnapshotClient, error)

// NewConfiguredWorldSnapshotClient builds an authenticated client from the
// explicit WORLD projection settings. Construction validates and freezes its
// scope but does not issue a WORLD request.
func NewConfiguredWorldSnapshotClient(settings config.WorldProjectionConfig, timeout time.Duration) (TaskMeetingScopedWorldSnapshotClient, error) {
	baseURL := strings.TrimSpace(settings.BaseURL)
	tokenEnv := strings.TrimSpace(settings.TokenEnv)
	if baseURL == "" || tokenEnv == "" {
		return nil, fmt.Errorf("TaskMeeting World read: base_url and token_env are required")
	}
	credential := os.Getenv(tokenEnv)
	if credential == "" {
		return nil, fmt.Errorf("TaskMeeting World read: credential environment %s is empty", tokenEnv)
	}
	actorIDs := make([]string, 0, len(settings.Actors))
	for _, actor := range settings.Actors {
		actorIDs = append(actorIDs, actor.ActorID)
	}
	return worldread.New(worldread.Config{
		BaseURL: baseURL, Credential: credential,
		WorldID: settings.WorldID, TimelineID: settings.TimelineID, TimelineEpoch: settings.TimelineEpoch,
		ActorIDs: actorIDs, Timeout: timeout,
	})
}

// PinnedWorldSnapshotReader preserves a non-secret frozen scope while delaying
// credential lookup until Snapshot. This lets receipt replay and deadline
// settlement proceed without credentials while preventing fallback to a newer
// World scope when a new read is required.
type PinnedWorldSnapshotReader struct {
	scope     worldread.ReadScope
	newClient TaskMeetingWorldSnapshotClientFactory
}

func NewPinnedWorldSnapshotReader(
	declaration domainmeeting.TaskMeetingDeclaration,
	newClient TaskMeetingWorldSnapshotClientFactory,
) (*PinnedWorldSnapshotReader, error) {
	if err := declaration.Validate(); err != nil || newClient == nil {
		return nil, fmt.Errorf("TaskMeeting frozen World reader input is invalid")
	}
	actorIDs := make([]string, 0, len(declaration.WorldActorBindings))
	for _, binding := range declaration.WorldActorBindings {
		actorIDs = append(actorIDs, binding.WorldActorID)
	}
	sort.Strings(actorIDs)
	return &PinnedWorldSnapshotReader{scope: worldread.ReadScope{
		BaseURL: declaration.WorldBaseURL, WorldID: declaration.WorldID,
		TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch, ActorIDs: actorIDs,
	}, newClient: newClient}, nil
}

func (reader *PinnedWorldSnapshotReader) ReadScope() worldread.ReadScope {
	if reader == nil {
		return worldread.ReadScope{}
	}
	scope := reader.scope
	scope.ActorIDs = append([]string(nil), reader.scope.ActorIDs...)
	return scope
}

func (reader *PinnedWorldSnapshotReader) Snapshot(ctx context.Context) (worldread.Snapshot, error) {
	if reader == nil || reader.newClient == nil || ctx == nil {
		return worldread.Snapshot{}, appmeeting.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return worldread.Snapshot{}, err
	}
	client, err := reader.newClient()
	if err != nil {
		return worldread.Snapshot{}, err
	}
	if client == nil || !sameWorldReadScope(client.ReadScope(), reader.scope) {
		return worldread.Snapshot{}, fmt.Errorf("%w: authenticated World client differs from frozen scope", appmeeting.ErrUnavailable)
	}
	return client.Snapshot(ctx)
}

func sameWorldReadScope(actual, expected worldread.ReadScope) bool {
	if actual.BaseURL != expected.BaseURL || actual.WorldID != expected.WorldID || actual.TimelineID != expected.TimelineID ||
		actual.TimelineEpoch != expected.TimelineEpoch || len(actual.ActorIDs) != len(expected.ActorIDs) {
		return false
	}
	actualActors := append([]string(nil), actual.ActorIDs...)
	expectedActors := append([]string(nil), expected.ActorIDs...)
	sort.Strings(actualActors)
	sort.Strings(expectedActors)
	for index := range expectedActors {
		if actualActors[index] != expectedActors[index] {
			return false
		}
	}
	return true
}

// WorldPresenceOwner joins the authenticated source reader to the durable
// TaskManager and EventStore owners. Credential bytes remain encapsulated in
// the worldread.Client and never enter a TaskMeeting DTO or receipt.
type WorldPresenceOwner struct {
	SnapshotReader   TaskMeetingWorldSnapshotReader
	EventStore       *eventstore.SQLiteStore
	TaskReader       domaintask.TaskMeetingInvocationReader
	AuthorizeRead    domaintask.TaskMeetingReadAuthorizationCheck
	AuthorizeObserve eventstore.TaskMeetingWorldObservationAdmissionCheck
	CredentialEnv    string
	Now              func() time.Time
}

var _ appmeeting.WorldPresencePort = (*WorldPresenceOwner)(nil)

func (owner *WorldPresenceOwner) GatherAndRead(
	ctx context.Context,
	meeting domainmeeting.Meeting,
	requestID string,
	threadID modulecore.ThreadID,
) (domainmeeting.WorldScopeEvidence, []domainmeeting.PresenceEvidence, error) {
	if owner == nil || owner.SnapshotReader == nil || owner.EventStore == nil || owner.TaskReader == nil ||
		owner.AuthorizeRead == nil || owner.AuthorizeObserve == nil || ctx == nil {
		return domainmeeting.WorldScopeEvidence{}, nil, appmeeting.ErrUnavailable
	}
	if requestID == "" || threadID.Validate() != nil || meeting.WorldBaseURL == "" || meeting.WorldCredentialEnv == "" ||
		meeting.WorldID == "" || meeting.TimelineID == "" || meeting.TimelineEpoch == "" || meeting.RegistrationEvidenceID.Validate() != nil ||
		meeting.TaskRef == nil || meeting.RunRef == nil || meeting.Generation == 0 || meeting.Epoch == 0 || meeting.ScheduleRevision == 0 {
		return domainmeeting.WorldScopeEvidence{}, nil, fmt.Errorf("%w: frozen TaskMeeting World scope is incomplete", appmeeting.ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return domainmeeting.WorldScopeEvidence{}, nil, err
	}
	task, run, shared, err := owner.TaskReader.GetTaskMeetingInvocationState(ctx, *meeting.TaskRef, *meeting.RunRef, owner.AuthorizeRead)
	if err != nil {
		return domainmeeting.WorldScopeEvidence{}, nil, err
	}
	invocation := task.TaskMeetingInvocation
	if invocation == nil || invocation.Purpose != domaintask.TaskMeetingPurposeRoot || invocation.OccurrenceKey != meeting.Key.String() ||
		invocation.ConfigRevision != meeting.ScheduleRevision || invocation.RegistrationEvidenceID != meeting.RegistrationEvidenceID ||
		invocation.AdmissionEvidenceID != meeting.TaskManagerEvidenceID || task.TaskID != *meeting.TaskRef || run.RunID != *meeting.RunRef ||
		run.TaskID != task.TaskID || run.Status != domaintask.RunStatusRunning {
		return domainmeeting.WorldScopeEvidence{}, nil, eventstore.ErrTaskMeetingForbidden
	}
	declaration, digest, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(invocation.FrozenConfig)
	if err != nil || digest != invocation.ConfigSHA256 || declaration.OwnerUserID != invocation.ConfiguredOwnerUserID ||
		declaration.CoordinatorActorID != invocation.CoordinatorActorID || !declarationMatchesMeeting(declaration, meeting) {
		return domainmeeting.WorldScopeEvidence{}, nil, eventstore.ErrTaskMeetingForbidden
	}
	clientScope, hasClientScope := owner.SnapshotReader.(interface{ ReadScope() worldread.ReadScope })
	if !hasClientScope || !matchesConfiguredReadScope(clientScope.ReadScope(), declaration) || owner.CredentialEnv != declaration.WorldCredentialEnv {
		return domainmeeting.WorldScopeEvidence{}, nil, fmt.Errorf("%w: authenticated World client does not match frozen registration", appmeeting.ErrUnavailable)
	}
	session, found, err := owner.EventStore.FindTaskMeetingGroupSessionForRoot(ctx, meeting.Key.String(), task.TaskID, run.RunID, owner.TaskReader, owner.AuthorizeRead)
	if err != nil {
		return domainmeeting.WorldScopeEvidence{}, nil, err
	}
	if !found || session.ThreadID != threadID || session.ConfigSHA256 != invocation.ConfigSHA256 || session.RegistrationEvidence != invocation.RegistrationEvidenceID {
		return domainmeeting.WorldScopeEvidence{}, nil, eventstore.ErrTaskMeetingUnavailable
	}
	request := domainmeeting.WorldObservationRequest{
		Key: meeting.Key, TaskID: task.TaskID, RunID: run.RunID,
		ConfigRevision: invocation.ConfigRevision, ConfigSHA256: invocation.ConfigSHA256,
		OwnerUserID: invocation.ConfiguredOwnerUserID, RegistrationEvidenceID: invocation.RegistrationEvidenceID,
		CoordinatorActorID: invocation.CoordinatorActorID, Session: session.SessionID, Thread: threadID,
		Generation: meeting.Generation, Epoch: meeting.Epoch,
		Audience:      append([]string(nil), invocation.Audience...),
		ActorBindings: append([]domainmeeting.WorldActorBinding(nil), declaration.WorldActorBindings...),
		Rooms:         declaration.Rooms, RequestID: requestID,
	}
	prior, found, err := owner.EventStore.FindTaskMeetingWorldObservation(ctx, request, owner.TaskReader, owner.AuthorizeRead)
	if err != nil {
		return domainmeeting.WorldScopeEvidence{}, nil, err
	}
	if found {
		return prior.Scope, prior.Presence, nil
	}
	if err := owner.AuthorizeObserve(ctx, task, run, shared, request); err != nil {
		return domainmeeting.WorldScopeEvidence{}, nil, eventstore.ErrTaskMeetingForbidden
	}
	snapshot, err := owner.SnapshotReader.Snapshot(ctx)
	if err != nil {
		return domainmeeting.WorldScopeEvidence{}, nil, fmt.Errorf("%w: authenticated World snapshot unavailable", appmeeting.ErrUnavailable)
	}
	if snapshot.WorldID != meeting.WorldID || snapshot.TimelineID != meeting.TimelineID || snapshot.TimelineEpoch != meeting.TimelineEpoch ||
		len(snapshot.Actors) != len(declaration.WorldActorBindings) {
		return domainmeeting.WorldScopeEvidence{}, nil, fmt.Errorf("%w: World snapshot differs from frozen registration scope", appmeeting.ErrUnavailable)
	}
	actorsByID := make(map[string]worldread.Actor, len(snapshot.Actors))
	for _, actor := range snapshot.Actors {
		if _, exists := actorsByID[actor.ActorID]; exists {
			return domainmeeting.WorldScopeEvidence{}, nil, fmt.Errorf("%w: World snapshot actor scope is ambiguous", appmeeting.ErrUnavailable)
		}
		actorsByID[actor.ActorID] = actor
	}
	observedAt := time.Now().UTC()
	if owner.Now != nil {
		observedAt = owner.Now().UTC()
	}
	validUntil := meeting.DeadlineAt.UTC()
	if !validUntil.After(observedAt) {
		return domainmeeting.WorldScopeEvidence{}, nil, domainmeeting.ErrNotReady
	}
	request.Snapshot = domainmeeting.WorldSnapshotObservation{
		WorldID: snapshot.WorldID, TimelineID: snapshot.TimelineID, TimelineEpoch: snapshot.TimelineEpoch,
		StateSeq: snapshot.StateSeq, StateHash: snapshot.StateHash,
		Actors: make([]domainmeeting.WorldActorSnapshot, 0, len(declaration.WorldActorBindings)),
	}
	for _, binding := range declaration.WorldActorBindings {
		actor, ok := actorsByID[binding.WorldActorID]
		if !ok {
			return domainmeeting.WorldScopeEvidence{}, nil, fmt.Errorf("%w: World snapshot omitted an actor in the frozen registration", appmeeting.ErrUnavailable)
		}
		request.Snapshot.Actors = append(request.Snapshot.Actors, domainmeeting.WorldActorSnapshot{
			WorldActorID: binding.WorldActorID, Present: actor.Present, RoomID: actor.RoomID, ActivityID: actor.ActivityID,
		})
	}
	request.ObservedAt, request.ValidUntil = observedAt, validUntil
	receipt, err := owner.EventStore.RecordTaskMeetingWorldObservation(ctx, request, owner.TaskReader, owner.AuthorizeRead, owner.AuthorizeObserve)
	if err != nil {
		return domainmeeting.WorldScopeEvidence{}, nil, err
	}
	return receipt.Scope, receipt.Presence, nil
}

func matchesConfiguredReadScope(scope worldread.ReadScope, declaration domainmeeting.TaskMeetingDeclaration) bool {
	if scope.BaseURL != declaration.WorldBaseURL || scope.WorldID != declaration.WorldID || scope.TimelineID != declaration.TimelineID ||
		scope.TimelineEpoch != declaration.TimelineEpoch || len(scope.ActorIDs) != len(declaration.WorldActorBindings) {
		return false
	}
	expected := make([]string, 0, len(declaration.WorldActorBindings))
	for _, binding := range declaration.WorldActorBindings {
		expected = append(expected, binding.WorldActorID)
	}
	sort.Strings(expected)
	for index := range expected {
		if expected[index] != scope.ActorIDs[index] {
			return false
		}
	}
	return true
}

func declarationMatchesMeeting(declaration domainmeeting.TaskMeetingDeclaration, meeting domainmeeting.Meeting) bool {
	return declaration.OwnerUserID != "" && declaration.Revision == meeting.ScheduleRevision &&
		declaration.WorldBaseURL == meeting.WorldBaseURL && declaration.WorldCredentialEnv == meeting.WorldCredentialEnv &&
		declaration.WorldID == meeting.WorldID && declaration.TimelineID == meeting.TimelineID && declaration.TimelineEpoch == meeting.TimelineEpoch &&
		declaration.Rooms == meeting.Rooms && declaration.MeetingDeadlineAfterSecs == meeting.MeetingDeadlineAfterSecs &&
		containsWorldActor(meeting.Participants, declaration.CoordinatorActorID) && sameWorldStrings(declaration.Audience, meeting.Participants) &&
		sameWorldBindings(declaration.WorldActorBindings, meeting.WorldActorBindings) && sameTaskMeetingTiming(declaration.Timing, meeting.Timing)
}

func containsWorldActor(actors []string, actor string) bool {
	for _, candidate := range actors {
		if candidate == actor {
			return true
		}
	}
	return false
}

func sameTaskMeetingTiming(left, right modulecore.TaskTiming) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func sameWorldStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameWorldBindings(left, right []domainmeeting.WorldActorBinding) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
