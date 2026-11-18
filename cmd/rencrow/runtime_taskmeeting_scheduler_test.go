package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	schedulerpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/scheduler"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSchedulerTargetRouterRejectsManualMeetingRun(t *testing.T) {
	executor := &recordingTaskMeetingOccurrenceExecutor{}
	router := schedulerTargetRouter{taskMeeting: executor}
	schedule := domainscheduler.Schedule{ScheduleID: modulecore.NewScheduleID(), Target: taskMeetingScheduledTarget}
	if _, err := router.ExecuteSchedule(context.Background(), schedule); !errors.Is(err, errTaskMeetingRequiresScheduledOccurrence) {
		t.Fatalf("manual meeting run error=%v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("manual run reached scheduled executor %d times", executor.calls)
	}
	if _, err := router.ExecuteScheduledOccurrence(context.Background(), schedule, domainscheduler.ScheduledOccurrence{}); !errors.Is(err, errTaskMeetingRequiresScheduledOccurrence) {
		t.Fatalf("empty scheduled occurrence error=%v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("invalid occurrence reached scheduled executor %d times", executor.calls)
	}
}

func TestEnsureTaskMeetingScheduleUsesOnlyOwnerIssuedIDAndPreservesPendingDue(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	location, err := time.LoadLocation(domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	issuedScheduleID := modulecore.NewScheduleID()
	dueAt := time.Date(2026, time.November, 18, 8, 0, 0, 0, location).UTC()
	now := dueAt.Add(4 * time.Hour)
	service := schedulerapp.NewService(store, nil).WithNow(func() time.Time { return now })
	timing := validRuntimeTaskMeetingTiming()

	firstID, err := ensureTaskMeetingSchedule(context.Background(), store, service, issuedScheduleID, timing)
	if err != nil {
		t.Fatalf("first ensure error=%v", err)
	}
	if firstID != issuedScheduleID {
		t.Fatalf("first schedule ID=%s want owner-issued %s", firstID, issuedScheduleID)
	}
	schedules, err := store.ListSchedules(context.Background(), 10)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("initial schedules=%+v err=%v", schedules, err)
	}
	schedule := schedules[0]
	pendingDue := dueAt.Add(-24 * time.Hour)
	schedule.PendingOccurrenceAt = &pendingDue
	schedule.NextRunAt = now.Add(30 * time.Second)
	if err := store.SaveSchedule(context.Background(), schedule); err != nil {
		t.Fatalf("save pending schedule: %v", err)
	}

	secondID, err := ensureTaskMeetingSchedule(context.Background(), store, service, issuedScheduleID, timing)
	if err != nil {
		t.Fatalf("second ensure error=%v", err)
	}
	if secondID != firstID {
		t.Fatalf("schedule ID changed across config revision: %s then %s", firstID, secondID)
	}
	schedules, err = store.ListSchedules(context.Background(), 10)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("recovered schedules=%+v err=%v", schedules, err)
	}
	if schedules[0].PendingOccurrenceAt == nil || !schedules[0].PendingOccurrenceAt.Equal(pendingDue) || !schedules[0].NextRunAt.Equal(schedule.NextRunAt) {
		t.Fatalf("pending original due was not preserved: %+v", schedules[0])
	}
}

func TestEnsureTaskMeetingScheduleDoesNotAdoptUnregisteredTarget(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	now := time.Now().UTC()
	unregisteredID := modulecore.NewScheduleID()
	if err := store.SaveSchedule(context.Background(), domainscheduler.Schedule{
		ScheduleID: unregisteredID, Name: "legacy candidate", Schedule: "every 1h", Target: taskMeetingScheduledTarget,
		Enabled: true, CreatedAt: now, UpdatedAt: now, NextRunAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	service := schedulerapp.NewService(store, nil)
	issuedID := modulecore.NewScheduleID()
	if _, err := ensureTaskMeetingSchedule(context.Background(), store, service, issuedID, validRuntimeTaskMeetingTiming()); err == nil {
		t.Fatal("unregistered target was adopted")
	}
	schedules, err := store.ListSchedules(context.Background(), 10)
	if err != nil || len(schedules) != 1 || schedules[0].ScheduleID != unregisteredID {
		t.Fatalf("unregistered target was replaced or duplicated: schedules=%+v err=%v", schedules, err)
	}
}

func TestEnsureTaskMeetingScheduleUsesExactOwnerAndFilteredScanAtScale(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	now := time.Now().UTC()
	ownerID := modulecore.NewScheduleID()
	ownerSchedule := domainscheduler.Schedule{
		ScheduleID: ownerID, Name: "Daily report meeting", Target: taskMeetingScheduledTarget,
		Timing:  func() *modulecore.TaskTiming { value := validRuntimeTaskMeetingTiming(); return &value }(),
		Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	if err := store.SaveSchedule(context.Background(), ownerSchedule); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < taskMeetingScheduleReadLimit; index++ {
		schedule := domainscheduler.Schedule{
			ScheduleID: modulecore.NewScheduleID(), Name: "public schedule", Schedule: "every 1h", Target: "public",
			Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		}
		if err := store.SaveSchedule(context.Background(), schedule); err != nil {
			t.Fatalf("save unrelated schedule %d: %v", index, err)
		}
	}
	service := schedulerapp.NewService(store, nil).WithNow(func() time.Time { return now })
	if got, err := ensureTaskMeetingSchedule(context.Background(), store, service, ownerID, validRuntimeTaskMeetingTiming()); err != nil || got != ownerID {
		t.Fatalf("exact owner schedule lookup at scale got=%s err=%v", got, err)
	}
	rogue := domainscheduler.Schedule{
		ScheduleID: modulecore.NewScheduleID(), Name: "unregistered meeting", Schedule: "every 1h", Target: taskMeetingScheduledTarget,
		Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	if err := store.SaveSchedule(context.Background(), rogue); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTaskMeetingSchedule(context.Background(), store, service, ownerID, validRuntimeTaskMeetingTiming()); err == nil {
		t.Fatal("filtered full-store scan missed an unregistered reserved target beyond the owner identity")
	}
}

func TestDisableTaskMeetingScheduleUsesExactIssuedIdentity(t *testing.T) {
	issuedID := modulecore.NewScheduleID()
	store := &exactOwnerScheduleStore{schedule: domainscheduler.Schedule{
		ScheduleID: issuedID, Name: "daily meeting", Target: taskMeetingScheduledTarget, Enabled: true,
	}}
	if err := disableTaskMeetingSchedule(context.Background(), store, issuedID); err != nil {
		t.Fatalf("disable exact owner schedule: %v", err)
	}
	if store.listCalls != 0 || !store.saved || store.schedule.Enabled || store.schedule.DisabledBy != "owner_config" {
		t.Fatalf("exact owner disable did not update only the issued identity: listCalls=%d saved=%t schedule=%+v", store.listCalls, store.saved, store.schedule)
	}
}

func TestMeetingDueRetryKeepsOriginalOccurrenceIdentity(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	location, err := time.LoadLocation(domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	dueAt := time.Date(2026, time.November, 18, 8, 0, 0, 0, location).UTC()
	current := dueAt.Add(4 * time.Hour)
	scheduleID := modulecore.NewScheduleID()
	timing := validRuntimeTaskMeetingTiming()
	schedule := domainscheduler.Schedule{
		ScheduleID: scheduleID, Name: "meeting", Target: taskMeetingScheduledTarget,
		Timing: &timing, Enabled: true,
		CreatedAt: dueAt.Add(-time.Hour), UpdatedAt: current,
		NextRunAt: current.Add(-time.Minute), PendingOccurrenceAt: &dueAt,
	}
	if err := store.SaveSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	executor := &recordingTaskMeetingOccurrenceExecutor{deferFirst: true}
	router := schedulerTargetRouter{taskMeeting: executor}
	service := schedulerapp.NewService(store, router).WithNow(func() time.Time { return current })

	first, ran, err := service.RunDueSchedule(context.Background(), string(scheduleID))
	if err != nil || !ran || first.Status != "deferred" {
		t.Fatalf("first due result=%+v ran=%t err=%v", first, ran, err)
	}
	saved, err := store.ListSchedules(context.Background(), 10)
	if err != nil || len(saved) != 1 || saved[0].PendingOccurrenceAt == nil || !saved[0].PendingOccurrenceAt.Equal(dueAt) {
		t.Fatalf("original due was not persisted: schedules=%+v err=%v", saved, err)
	}

	current = current.Add(31 * time.Second)
	second, ran, err := service.RunDueSchedule(context.Background(), string(scheduleID))
	if err != nil || !ran || second.Status != "completed" {
		t.Fatalf("retry result=%+v ran=%t err=%v", second, ran, err)
	}
	if len(executor.occurrences) != 2 || !executor.occurrences[0].ScheduledAt.Equal(dueAt) || !executor.occurrences[1].ScheduledAt.Equal(dueAt) || executor.occurrences[0].OccurrenceKey != executor.occurrences[1].OccurrenceKey {
		t.Fatalf("retry changed original slot: %+v", executor.occurrences)
	}
}

func TestMeetingOwnerFailureDefersSameDueWithoutPersistingPrivateError(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	location, err := time.LoadLocation(domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	dueAt := time.Date(2026, time.November, 18, 12, 0, 0, 0, location).UTC()
	now := dueAt.Add(2 * time.Minute)
	scheduleID := modulecore.NewScheduleID()
	timing := validRuntimeTaskMeetingTiming()
	schedule := domainscheduler.Schedule{
		ScheduleID: scheduleID, Name: "meeting", Target: taskMeetingScheduledTarget,
		Timing: &timing, Enabled: true, CreatedAt: dueAt.Add(-time.Hour), UpdatedAt: now,
		NextRunAt: now, PendingOccurrenceAt: &dueAt,
	}
	if err := store.SaveSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	privateCause := errors.New("private World response with credential details")
	executor := &recordingTaskMeetingOccurrenceExecutor{errFirst: privateCause}
	service := schedulerapp.NewService(store, schedulerTargetRouter{taskMeeting: executor}).WithNow(func() time.Time { return now })

	entry, ran, err := service.RunDueSchedule(context.Background(), string(scheduleID))
	if err != nil || !ran || entry.Status != "deferred" || entry.Error != "" || entry.Summary != errTaskMeetingCheckpointPending.Error() {
		t.Fatalf("owner failure should stay deferred with a safe summary: entry=%+v ran=%t err=%v", entry, ran, err)
	}
	schedules, err := store.ListSchedules(context.Background(), 10)
	if err != nil || len(schedules) != 1 || schedules[0].PendingOccurrenceAt == nil || !schedules[0].PendingOccurrenceAt.Equal(dueAt) {
		t.Fatalf("failed checkpoint did not preserve original due: schedules=%+v err=%v", schedules, err)
	}
	runLogs, err := store.ListRunLogs(context.Background(), 10)
	if err != nil || len(runLogs) != 1 || strings.Contains(runLogs[0].Summary, "credential details") || strings.Contains(runLogs[0].Error, "credential details") {
		t.Fatalf("private executor error escaped into scheduler log: logs=%+v err=%v", runLogs, err)
	}
}

func TestSharedDueLoopDoesNotLetPronunciationBlockMeeting(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	location, err := time.LoadLocation(domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, time.November, 18, 12, 30, 0, 0, location).UTC()
	meetingDue := time.Date(2026, time.November, 18, 12, 0, 0, 0, location).UTC()
	pncID := modulecore.NewScheduleID()
	meetingID := modulecore.NewScheduleID()
	meetingTiming := validRuntimeTaskMeetingTiming()
	for _, schedule := range []domainscheduler.Schedule{
		{
			ScheduleID: pncID, Name: "pronunciation", Schedule: "every 1h", Target: pronunciationapp.ScheduledTarget,
			Enabled: true, CreatedAt: current.Add(-time.Hour), UpdatedAt: current, NextRunAt: current.Add(-time.Minute),
		},
		{
			ScheduleID: meetingID, Name: "meeting", Target: taskMeetingScheduledTarget,
			Timing: &meetingTiming, Enabled: true,
			CreatedAt: current.Add(-time.Hour), UpdatedAt: current, NextRunAt: current.Add(-time.Minute), PendingOccurrenceAt: &meetingDue,
		},
	} {
		if err := store.SaveSchedule(context.Background(), schedule); err != nil {
			t.Fatal(err)
		}
	}
	pnc := &blockingPronunciationExecutor{started: make(chan struct{}), release: make(chan struct{})}
	meeting := &recordingTaskMeetingOccurrenceExecutor{called: make(chan struct{})}
	router := schedulerTargetRouter{pronunciation: pnc, taskMeeting: meeting}
	service := schedulerapp.NewService(store, router).WithNow(func() time.Time { return current })
	cancel, done := startSharedSchedulerLoop(context.Background(), service, []modulecore.ScheduleID{pncID, meetingID})
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("shared scheduler workers did not stop after cancellation")
		}
	}()

	select {
	case <-pnc.started:
	case <-time.After(2 * time.Second):
		t.Fatal("PronunciationCheck worker did not start")
	}
	select {
	case <-meeting.called:
	case <-time.After(2 * time.Second):
		t.Fatal("meeting due occurrence was blocked by PronunciationCheck")
	}
}

func TestSchedulerShutdownJoinIsBoundedAndReportsUnsafePersistenceClose(t *testing.T) {
	if !waitForSchedulerWorkers(nil, time.Millisecond) {
		t.Fatal("nil scheduler worker set was not considered drained")
	}
	closed := make(chan struct{})
	close(closed)
	if !waitForSchedulerWorkers(closed, time.Millisecond) {
		t.Fatal("completed scheduler workers were not joined")
	}
	blocked := make(chan struct{})
	started := time.Now()
	if waitForSchedulerWorkers(blocked, 10*time.Millisecond) {
		t.Fatal("live scheduler worker was reported drained")
	}
	if time.Since(started) > time.Second {
		t.Fatal("scheduler worker join exceeded its supplied bound")
	}
}

func TestTaskMeetingTimingComparisonUsesInstantsAcrossJSONLocations(t *testing.T) {
	location, err := time.LoadLocation(domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, time.November, 18, 8, 0, 0, 0, location)
	left, right := validRuntimeTaskMeetingTiming(), validRuntimeTaskMeetingTiming()
	left.NotBefore = &instant
	rightInstant := instant.In(time.FixedZone("serialized-offset", 9*60*60))
	right.NotBefore = &rightInstant
	if !sameTaskMeetingTiming(left, right) {
		t.Fatal("equivalent instants from configuration and JSONL were treated as a timing change")
	}
	right.NotBefore = nil
	if sameTaskMeetingTiming(left, right) {
		t.Fatal("unspecified and known not_before values were treated as equal")
	}
}

func TestDisabledTaskMeetingDoesNotCreateSchedulerSideEffects(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	deps := &Dependencies{schedulerStore: store}
	cfg := &config.Config{}
	buildPronunciationCheckRuntime(cfg, deps, agentRuntime{})
	schedules, err := store.ListSchedules(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 0 || deps.schedulerRuntimeCancel != nil {
		t.Fatalf("disabled meeting created scheduler state: schedules=%+v cancel=%t", schedules, deps.schedulerRuntimeCancel != nil)
	}
	if enabled, reason := taskMeetingEffectivelyEnabled(cfg); enabled || reason != "feature_disabled" {
		t.Fatalf("disabled feature state enabled=%t reason=%q", enabled, reason)
	}

	cfg.Heartbeat.Enabled = false
	cfg.Heartbeat.Routine.DailyReportMeetings.Enabled = true
	if enabled, reason := taskMeetingEffectivelyEnabled(cfg); enabled || reason != "parent_heartbeat_disabled" {
		t.Fatalf("parent-disabled routine state enabled=%t reason=%q", enabled, reason)
	}
}

func TestMissingWorldCredentialRecoversAndDisablesOnlyIssuedMeetingSchedule(t *testing.T) {
	t.Setenv("WORLD_READ_TOKEN", "")
	cfg := validRuntimeTaskMeetingConfig()
	prior := taskMeetingReceiptForConfig(t, cfg)
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	timing := cfg.Heartbeat.Routine.DailyReportMeetings.TaskTiming()
	now := time.Date(2026, time.November, 18, 7, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	if err := store.SaveSchedule(context.Background(), domainscheduler.Schedule{
		ScheduleID: prior.ScheduleID, Name: "daily meeting", Target: taskMeetingScheduledTarget,
		Timing: &timing, Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, NextRunAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed owner-issued schedule: %v", err)
	}
	owner := &taskMeetingRegistrationTestOwner{prior: prior}
	deps := &Dependencies{
		schedulerStore: store, canonicalEventStore: &taskMeetingRegistrationTestEventStore{taskMeetingRegistrationTestOwner: owner},
		taskCenterUserID: "owner-1", taskCenterUserToken: []byte("memory-only-auth-token"),
	}

	buildPronunciationCheckRuntime(cfg, deps, registeredMeetingActors(), registeredMeetingCoders()...)
	if deps.taskMeetingScheduleID != prior.ScheduleID || owner.ensureCalls != 1 || owner.latestReadCalls != 1 {
		t.Fatalf("missing credential did not recover only the exact owner identity: id=%q ensure=%d read=%d", deps.taskMeetingScheduleID, owner.ensureCalls, owner.latestReadCalls)
	}
	schedules, err := store.ListSchedules(context.Background(), 10)
	if err != nil || len(schedules) != 1 || schedules[0].ScheduleID != prior.ScheduleID || schedules[0].Enabled {
		t.Fatalf("owned schedule was not safely disabled: schedules=%+v err=%v", schedules, err)
	}
	if deps.schedulerRuntimeCancel != nil {
		t.Fatal("missing WORLD credential started the shared scheduler loop")
	}
}

type recordingTaskMeetingOccurrenceExecutor struct {
	calls       int
	occurrences []domainscheduler.ScheduledOccurrence
	deferFirst  bool
	errFirst    error
	called      chan struct{}
}

func (e *recordingTaskMeetingOccurrenceExecutor) ExecuteScheduledOccurrence(_ context.Context, _ domainscheduler.Schedule, occurrence domainscheduler.ScheduledOccurrence) (string, error) {
	e.calls++
	e.occurrences = append(e.occurrences, occurrence)
	if e.called != nil {
		select {
		case e.called <- struct{}{}:
		default:
		}
	}
	if e.deferFirst && e.calls == 1 {
		return "checkpoint pending", schedulerapp.NewDeferredError(30*time.Second, errors.New("owner checkpoint incomplete"))
	}
	if e.errFirst != nil && e.calls == 1 {
		return "checkpoint pending", e.errFirst
	}
	return "checkpoint complete", nil
}

type blockingPronunciationExecutor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockingPronunciationExecutor) ExecuteSchedule(ctx context.Context, _ domainscheduler.Schedule) (string, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return "complete", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type exactOwnerScheduleStore struct {
	schedule  domainscheduler.Schedule
	listCalls int
	saved     bool
}

func (s *exactOwnerScheduleStore) ListSchedules(context.Context, int) ([]domainscheduler.Schedule, error) {
	s.listCalls++
	return nil, errors.New("bounded list should not be used when exact identity exists")
}

func (s *exactOwnerScheduleStore) FindScheduleByID(_ context.Context, scheduleID string) (domainscheduler.Schedule, bool, error) {
	return s.schedule, string(s.schedule.ScheduleID) == scheduleID, nil
}

func (s *exactOwnerScheduleStore) SaveSchedule(_ context.Context, schedule domainscheduler.Schedule) error {
	s.schedule = schedule
	s.saved = true
	return nil
}

func (*exactOwnerScheduleStore) SaveRunLog(context.Context, domainscheduler.RunLog) error { return nil }

func (*exactOwnerScheduleStore) ListRunLogs(context.Context, int) ([]domainscheduler.RunLog, error) {
	return nil, nil
}

func validRuntimeTaskMeetingTiming() modulecore.TaskTiming {
	return modulecore.TaskTiming{
		Timezone:       domainmeeting.TimezoneJST,
		AllowedWindows: []modulecore.TaskTimeWindow{},
		Recurrence: &modulecore.TaskRecurrence{
			Frequency: modulecore.TaskRecurrenceDaily,
			Times:     []string{"08:00", "12:00", "17:00"},
			Weekdays:  []time.Weekday{},
		},
	}
}
