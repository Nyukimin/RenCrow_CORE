package main

import (
	"context"
	"errors"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestResolveTaskMeetingOccurrenceReadsPinnedRevisionBeforeCurrentDeclaration(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	t.Setenv("CHANGED_WORLD_TOKEN", "")
	ctx := context.Background()
	currentConfig := validRuntimeTaskMeetingConfig()
	currentConfig.Heartbeat.Routine.DailyReportMeetings.Revision = 2
	currentConfig.Heartbeat.Routine.DailyReportMeetings.DiningRoomRef = "new-owner-declared-dining"
	currentConfig.WorldProjection.BaseURL = "http://changed-world.example.test"
	currentConfig.WorldProjection.TokenEnv = "CHANGED_WORLD_TOKEN"
	currentConfig.WorldProjection.WorldID = "changed-world"
	currentConfig.WorldProjection.TimelineID = "changed-timeline"
	currentConfig.WorldProjection.TimelineEpoch = "changed-epoch"
	oldConfig := validRuntimeTaskMeetingConfig()
	registration := taskMeetingReceiptForConfig(t, oldConfig)
	key, schedule, occurrence := taskMeetingOccurrenceFixture(t, registration.ScheduleID, oldConfig.Heartbeat.Routine.DailyReportMeetings.TaskTiming())
	pin := taskMeetingOccurrencePinFixture(key, registration)
	owner := &taskMeetingOccurrenceTestOwner{
		registration: registration,
		pin:          pin,
		pinFound:     true,
	}
	deps := taskMeetingOccurrenceDependencies(owner)

	resolved, err := resolveRuntimeTaskMeetingOccurrence(ctx, currentConfig, deps, schedule, occurrence, registeredMeetingActors(), registeredMeetingCoders()...)
	if err != nil {
		t.Fatalf("resolve pinned occurrence: %v", err)
	}
	if owner.findPinCalls != 1 || owner.ensureCalls != 0 || owner.pinCalls != 0 || owner.registrationReads != 1 {
		t.Fatalf("existing occurrence must be read before current config is built: calls=%+v", owner.counts())
	}
	if resolved.Config.Revision != 1 || resolved.Config.Rooms.Dining != "room-private-dining" || resolved.Registration.ConfigSHA256 != pin.ConfigSHA256 {
		t.Fatalf("occurrence did not retain exact historical declaration: %+v", resolved)
	}
	if resolved.Config.WorldBaseURL != oldConfig.WorldProjection.BaseURL ||
		resolved.Config.WorldCredentialEnv != oldConfig.WorldProjection.TokenEnv ||
		resolved.Config.WorldID != oldConfig.WorldProjection.WorldID ||
		resolved.Config.TimelineID != oldConfig.WorldProjection.TimelineID ||
		resolved.Config.TimelineEpoch != oldConfig.WorldProjection.TimelineEpoch {
		t.Fatalf("occurrence World scope did not retain its frozen declaration: %+v", resolved.Config)
	}
	executor := &runtimeTaskMeetingOccurrenceExecutor{cfg: currentConfig, deps: deps}
	if err := executor.authorizeCurrentTaskMeetingEffect(resolved); err != nil {
		t.Fatalf("new pinned effect should use the frozen credential env while the current declaration's env is missing: %v", err)
	}
	pinnedReader, err := newRuntimeTaskMeetingPinnedWorldReader(currentConfig, resolved.Declaration)
	if err != nil {
		t.Fatalf("build pinned WORLD reader after current env changed: %v", err)
	}
	pinnedScope := pinnedReader.ReadScope()
	if pinnedScope.BaseURL != oldConfig.WorldProjection.BaseURL || pinnedScope.WorldID != oldConfig.WorldProjection.WorldID ||
		pinnedScope.TimelineID != oldConfig.WorldProjection.TimelineID || pinnedScope.TimelineEpoch != oldConfig.WorldProjection.TimelineEpoch {
		t.Fatalf("WORLD reader scope was rebuilt from current config: %+v", pinnedScope)
	}
}

func TestResolveTaskMeetingOccurrencePinsCurrentOnlyAfterExactNotFound(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	ctx := context.Background()
	cfg := validRuntimeTaskMeetingConfig()
	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		t.Fatal(err)
	}
	registration := taskMeetingReceiptForRequest(eventstore.TaskMeetingRegistrationRequest{Declaration: declaration, FrozenDeclaration: canonical})
	_, schedule, occurrence := taskMeetingOccurrenceFixture(t, registration.ScheduleID, declaration.Timing)
	owner := &taskMeetingOccurrenceTestOwner{registration: registration}
	deps := taskMeetingOccurrenceDependencies(owner)

	resolved, err := resolveRuntimeTaskMeetingOccurrence(ctx, cfg, deps, schedule, occurrence, registeredMeetingActors(), registeredMeetingCoders()...)
	if err != nil {
		t.Fatalf("pin current occurrence: %v", err)
	}
	if owner.findPinCalls != 1 || owner.ensureCalls != 1 || owner.pinCalls != 1 || owner.registrationReads != 1 {
		t.Fatalf("current pin order=%+v", owner.counts())
	}
	if resolved.Pin.ConfigSHA256 != digest || resolved.Pin.ConfigRevision != declaration.Revision || resolved.Config.ScheduleID != registration.ScheduleID {
		t.Fatalf("pinned result=%+v", resolved)
	}
}

func TestResolveTaskMeetingOccurrenceReadbackReconcilesLostPinReply(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	ctx := context.Background()
	cfg := validRuntimeTaskMeetingConfig()
	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		t.Fatal(err)
	}
	registration := taskMeetingReceiptForRequest(eventstore.TaskMeetingRegistrationRequest{Declaration: declaration, FrozenDeclaration: canonical})
	key, schedule, occurrence := taskMeetingOccurrenceFixture(t, registration.ScheduleID, declaration.Timing)
	owner := &taskMeetingOccurrenceTestOwner{registration: registration, losePinReply: true}
	deps := taskMeetingOccurrenceDependencies(owner)

	resolved, err := resolveRuntimeTaskMeetingOccurrence(ctx, cfg, deps, schedule, occurrence, registeredMeetingActors(), registeredMeetingCoders()...)
	if err != nil {
		t.Fatalf("resolve lost pin reply by exact readback: %v", err)
	}
	if owner.pinCalls != 1 || owner.findPinCalls != 2 || resolved.Pin.OccurrenceKey != key.String() {
		t.Fatalf("ambiguous pin was resent or not reconciled: calls=%+v pin=%+v", owner.counts(), resolved.Pin)
	}
}

func TestResolveTaskMeetingOccurrenceRejectsCurrentPinWithoutOwnerAuthority(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	cfg := validRuntimeTaskMeetingConfig()
	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		t.Fatal(err)
	}
	registration := taskMeetingReceiptForRequest(eventstore.TaskMeetingRegistrationRequest{Declaration: declaration, FrozenDeclaration: canonical})
	_, schedule, occurrence := taskMeetingOccurrenceFixture(t, registration.ScheduleID, declaration.Timing)
	owner := &taskMeetingOccurrenceTestOwner{registration: registration}
	deps := taskMeetingOccurrenceDependencies(owner)
	deps.taskCenterUserToken = nil

	if _, err := resolveRuntimeTaskMeetingOccurrence(context.Background(), cfg, deps, schedule, occurrence, registeredMeetingActors(), registeredMeetingCoders()...); err == nil {
		t.Fatal("current occurrence pin was admitted without the configured owner token")
	}
	if owner.pinCalls != 0 {
		t.Fatalf("unauthorized pin was written: calls=%+v", owner.counts())
	}
}

func TestResolveTaskMeetingOccurrenceRequiresCurrentCredentialForNewPin(t *testing.T) {
	t.Setenv("WORLD_READ_TOKEN", "")
	cfg := validRuntimeTaskMeetingConfig()
	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	registration := taskMeetingReceiptForConfig(t, cfg)
	_, schedule, occurrence := taskMeetingOccurrenceFixture(t, registration.ScheduleID, declaration.Timing)
	owner := &taskMeetingOccurrenceTestOwner{registration: registration}
	deps := taskMeetingOccurrenceDependencies(owner)

	if _, err := resolveRuntimeTaskMeetingOccurrence(context.Background(), cfg, deps, schedule, occurrence, registeredMeetingActors(), registeredMeetingCoders()...); err == nil {
		t.Fatal("new occurrence was pinned without its current declaration's WORLD credential")
	}
	if owner.ensureCalls != 0 || owner.pinCalls != 0 || owner.findPinCalls != 1 {
		t.Fatalf("missing current credential reached registration/pin writes: calls=%+v", owner.counts())
	}
}

func TestPinnedWorldCredentialIsRequiredOnlyWhenANewWorldReadIsNeeded(t *testing.T) {
	t.Setenv("WORLD_READ_TOKEN", "")
	cfg := validRuntimeTaskMeetingConfig()
	registration := taskMeetingReceiptForConfig(t, cfg)
	key, schedule, occurrence := taskMeetingOccurrenceFixture(t, registration.ScheduleID, cfg.Heartbeat.Routine.DailyReportMeetings.TaskTiming())
	owner := &taskMeetingOccurrenceTestOwner{registration: registration, pin: taskMeetingOccurrencePinFixture(key, registration), pinFound: true}
	deps := taskMeetingOccurrenceDependencies(owner)
	snapshot, err := resolveRuntimeTaskMeetingOccurrence(context.Background(), cfg, deps, schedule, occurrence, registeredMeetingActors(), registeredMeetingCoders()...)
	if err != nil {
		t.Fatalf("pinned occurrence metadata should be readable before effect admission: %v", err)
	}
	executor := &runtimeTaskMeetingOccurrenceExecutor{cfg: cfg, deps: deps}
	reader, err := newRuntimeTaskMeetingPinnedWorldReader(cfg, snapshot.Declaration)
	if err != nil {
		t.Fatalf("construct metadata-only pinned reader: %v", err)
	}
	readScope := reader.ReadScope()
	if readScope.BaseURL != snapshot.Declaration.WorldBaseURL || readScope.WorldID != snapshot.Declaration.WorldID ||
		readScope.TimelineID != snapshot.Declaration.TimelineID || readScope.TimelineEpoch != snapshot.Declaration.TimelineEpoch ||
		len(readScope.ActorIDs) != len(snapshot.Declaration.WorldActorBindings) {
		t.Fatal("pinned reader did not preserve the exact historical WORLD scope")
	}
	for _, binding := range snapshot.Declaration.WorldActorBindings {
		if !containsStringExact(readScope.ActorIDs, binding.WorldActorID) {
			t.Fatalf("pinned read scope omitted frozen actor %q", binding.WorldActorID)
		}
	}
	if _, err := reader.Snapshot(context.Background()); err == nil {
		t.Fatal("new WORLD snapshot was read without the pinned declaration's credential")
	}
	t.Setenv("WORLD_READ_TOKEN", "fixture-pinned-world-read-token")
	request := domaintask.TaskMeetingInvocationRequest{
		OccurrenceKey: snapshot.Key.String(), Purpose: domaintask.TaskMeetingPurposeRoot,
		ActorID: snapshot.Declaration.CoordinatorActorID, ConfigRevision: snapshot.Declaration.Revision,
		RegistrationEvidenceID: snapshot.Registration.RegistrationEvidenceID, ConfiguredOwnerUserID: snapshot.Declaration.OwnerUserID,
	}
	for _, edit := range []struct {
		name  string
		apply func()
	}{
		{name: "parent heartbeat disabled", apply: func() { cfg.Heartbeat.Enabled = false }},
		{name: "meeting feature disabled", apply: func() { cfg.Heartbeat.Enabled = true; cfg.Heartbeat.Routine.DailyReportMeetings.Enabled = false }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			edit.apply()
			defer func() {
				cfg = validRuntimeTaskMeetingConfig()
				executor.cfg = cfg
			}()
			if err := executor.authorizeAdmission(snapshot)(context.Background(), request, domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}); !errors.Is(err, eventstore.ErrTaskMeetingForbidden) {
				t.Fatalf("new effect with disabled current feature flag = %v, want denied admission", err)
			}
		})
	}
}

func TestTaskMeetingPendingProjectionUsesFrozenPinnedTiming(t *testing.T) {
	cfg := validRuntimeTaskMeetingConfig()
	cfg.Heartbeat.Routine.DailyReportMeetings.Revision = 2
	currentTiming := cfg.Heartbeat.Routine.DailyReportMeetings.TaskTiming()
	oldConfig := validRuntimeTaskMeetingConfig()
	oldTiming := oldConfig.Heartbeat.Routine.DailyReportMeetings.TaskTiming()
	oldTiming.NotBefore = timePtr(time.Date(2026, time.November, 18, 7, 55, 0, 0, time.FixedZone("JST", 9*60*60)))
	oldDeclaration, err := taskMeetingDeclarationFromRuntimeConfig(oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	oldDeclaration.Timing = oldTiming
	oldDeclaration.Revision = 1
	canonical, _, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(oldDeclaration)
	if err != nil {
		t.Fatal(err)
	}
	registration := taskMeetingReceiptForRequest(eventstore.TaskMeetingRegistrationRequest{Declaration: oldDeclaration, FrozenDeclaration: canonical})
	key, schedule, _ := taskMeetingOccurrenceFixture(t, registration.ScheduleID, oldTiming)
	due := occurrenceTimeFromMeetingKey(t, key)
	schedule.PendingOccurrenceAt = &due
	schedule.NextRunAt = due.Add(30 * time.Second)
	owner := &taskMeetingOccurrenceTestOwner{registration: registration, pin: taskMeetingOccurrencePinFixture(key, registration), pinFound: true}
	deps := taskMeetingOccurrenceDependencies(owner)
	deps.schedulerStore = &exactTaskMeetingScheduleSnapshotStore{exactOwnerScheduleStore: exactOwnerScheduleStore{schedule: schedule}}

	got, err := taskMeetingPendingProjectionTiming(context.Background(), cfg, deps, registration.ScheduleID, currentTiming)
	if err != nil {
		t.Fatalf("resolve pending pinned timing: %v", err)
	}
	if !sameTaskMeetingTiming(got, oldTiming) {
		t.Fatalf("pending pinned timing changed: got=%+v want=%+v", got, oldTiming)
	}
}

func TestTaskMeetingPendingProjectionDoesNotPinBeforeDueExecution(t *testing.T) {
	cfg := validRuntimeTaskMeetingConfig()
	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		t.Fatal(err)
	}
	registration := taskMeetingReceiptForRequest(eventstore.TaskMeetingRegistrationRequest{Declaration: declaration, FrozenDeclaration: canonical})
	key, schedule, _ := taskMeetingOccurrenceFixture(t, registration.ScheduleID, declaration.Timing)
	due := occurrenceTimeFromMeetingKey(t, key)
	schedule.PendingOccurrenceAt = &due
	schedule.NextRunAt = due.Add(time.Minute)
	owner := &taskMeetingOccurrenceTestOwner{registration: registration}
	deps := taskMeetingOccurrenceDependencies(owner)
	deps.schedulerStore = &exactTaskMeetingScheduleSnapshotStore{exactOwnerScheduleStore: exactOwnerScheduleStore{schedule: schedule}}
	current := validRuntimeTaskMeetingConfig().Heartbeat.Routine.DailyReportMeetings.TaskTiming()

	got, err := taskMeetingPendingProjectionTiming(context.Background(), cfg, deps, registration.ScheduleID, current)
	if err != nil || !sameTaskMeetingTiming(got, current) {
		t.Fatalf("unpinned due should retain current projection timing: got=%+v err=%v", got, err)
	}
	if owner.ensureCalls != 0 || owner.pinCalls != 0 {
		t.Fatalf("startup projection performed occurrence writes: calls=%+v", owner.counts())
	}
}

type taskMeetingOccurrenceTestOwner struct {
	runtimeCanonicalEventStore
	registration      eventstore.TaskMeetingScheduleRegistrationReceipt
	pin               eventstore.TaskMeetingOccurrenceRegistrationReceipt
	pinFound          bool
	losePinReply      bool
	findPinCalls      int
	ensureCalls       int
	pinCalls          int
	registrationReads int
}

func (o *taskMeetingOccurrenceTestOwner) EnsureTaskMeetingScheduleRegistration(
	ctx context.Context,
	request eventstore.TaskMeetingRegistrationRequest,
	authorize eventstore.TaskMeetingRegistrationAuthorizationCheck,
) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
	o.ensureCalls++
	if err := authorize(ctx, request.Declaration); err != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if o.registration.ScheduleID == "" {
		o.registration = taskMeetingReceiptForRequest(request)
	}
	return o.registration, nil
}

func (o *taskMeetingOccurrenceTestOwner) FindLatestTaskMeetingScheduleRegistration(ctx context.Context, key, owner string, auth eventstore.TaskMeetingRegistrationReadAuthorizationCheck) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
	if err := auth(ctx, owner, key, 0); err != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if o.registration.OwnerUserID != owner || o.registration.RegistrationKey != key {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, domaintask.ErrTaskMeetingNotFound
	}
	return o.registration, nil
}

func (o *taskMeetingOccurrenceTestOwner) FindTaskMeetingScheduleRegistration(ctx context.Context, key, owner string, revision uint64, auth eventstore.TaskMeetingRegistrationReadAuthorizationCheck) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
	o.registrationReads++
	if err := auth(ctx, owner, key, revision); err != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if o.registration.OwnerUserID != owner || o.registration.RegistrationKey != key || o.registration.Revision != revision {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, domaintask.ErrTaskMeetingNotFound
	}
	return o.registration, nil
}

func (o *taskMeetingOccurrenceTestOwner) FindTaskMeetingOccurrenceRegistration(ctx context.Context, key domainmeeting.OccurrenceKey, owner string, auth eventstore.TaskMeetingOccurrenceRegistrationReadAuthorizationCheck) (eventstore.TaskMeetingOccurrenceRegistrationReceipt, bool, error) {
	o.findPinCalls++
	if err := auth(ctx, owner, key.String()); err != nil {
		return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, err
	}
	if !o.pinFound || o.pin.OwnerUserID != owner {
		return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, domaintask.ErrTaskMeetingNotFound
	}
	return o.pin, true, nil
}

func (o *taskMeetingOccurrenceTestOwner) PinTaskMeetingOccurrenceRegistration(ctx context.Context, registration eventstore.TaskMeetingScheduleRegistrationReceipt, key domainmeeting.OccurrenceKey, auth eventstore.TaskMeetingOccurrenceRegistrationAuthorizationCheck) (eventstore.TaskMeetingOccurrenceRegistrationReceipt, error) {
	o.pinCalls++
	if err := auth(ctx, registration, key); err != nil {
		return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, err
	}
	if !o.pinFound {
		o.pin = taskMeetingOccurrencePinFixture(key, registration)
		o.pinFound = true
	}
	if o.losePinReply {
		o.losePinReply = false
		return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, errors.New("simulated lost EventStore reply")
	}
	return o.pin, nil
}

func (o *taskMeetingOccurrenceTestOwner) counts() map[string]int {
	return map[string]int{"find_pin": o.findPinCalls, "ensure": o.ensureCalls, "pin": o.pinCalls, "registration_read": o.registrationReads}
}

func taskMeetingOccurrenceDependencies(owner *taskMeetingOccurrenceTestOwner) *Dependencies {
	return &Dependencies{
		canonicalEventStore: &taskMeetingOccurrenceTestEventStore{taskMeetingOccurrenceTestOwner: owner},
		taskCenterUserID:    "owner-1",
		taskCenterUserToken: []byte("unit-test-token"),
	}
}

type taskMeetingOccurrenceTestEventStore struct {
	runtimeCanonicalEventStore
	*taskMeetingOccurrenceTestOwner
}

func taskMeetingOccurrenceFixture(t *testing.T, scheduleID modulecore.ScheduleID, timing modulecore.TaskTiming) (domainmeeting.OccurrenceKey, domainscheduler.Schedule, domainscheduler.ScheduledOccurrence) {
	t.Helper()
	location, err := time.LoadLocation(domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, time.November, 18, 8, 0, 0, 0, location)
	schedule := domainscheduler.Schedule{
		ScheduleID: scheduleID, Name: "meeting", Target: taskMeetingScheduledTarget,
		Timing: &timing, Enabled: true, CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour),
		NextRunAt: due,
	}
	keyText, err := domainscheduler.OccurrenceKey(schedule, due, domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	occurrence := domainscheduler.ScheduledOccurrence{OccurrenceKey: keyText, ScheduledAt: due, Timing: timing}
	key, err := taskMeetingOccurrenceKey(schedule, occurrence)
	if err != nil {
		t.Fatal(err)
	}
	return key, schedule, occurrence
}

func taskMeetingOccurrencePinFixture(key domainmeeting.OccurrenceKey, registration eventstore.TaskMeetingScheduleRegistrationReceipt) eventstore.TaskMeetingOccurrenceRegistrationReceipt {
	return eventstore.TaskMeetingOccurrenceRegistrationReceipt{
		OccurrenceKey: key.String(), ScheduleID: registration.ScheduleID, OwnerUserID: registration.OwnerUserID,
		ConfigRevision: registration.Revision, ConfigSHA256: registration.ConfigSHA256,
		RegistrationEvidenceID: registration.RegistrationEvidenceID, RegistrationEventID: registration.EventID,
		PinEventID: modulecore.NewEventID(), PinnedAt: time.Unix(1_800_000_000, 0).UTC(),
	}
}

func occurrenceTimeFromMeetingKey(t *testing.T, key domainmeeting.OccurrenceKey) time.Time {
	t.Helper()
	location, err := time.LoadLocation(key.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	date, err := time.ParseInLocation("2006-01-02 15:04", key.LocalDate+" "+key.Slot, location)
	if err != nil {
		t.Fatal(err)
	}
	return date
}

type exactTaskMeetingScheduleSnapshotStore struct {
	exactOwnerScheduleStore
}

func (s *exactTaskMeetingScheduleSnapshotStore) FindScheduleByID(_ context.Context, id string) (domainscheduler.Schedule, bool, error) {
	return s.schedule, string(s.schedule.ScheduleID) == id, nil
}
