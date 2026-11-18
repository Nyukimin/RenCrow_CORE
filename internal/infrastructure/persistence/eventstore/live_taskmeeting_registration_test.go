package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func taskMeetingRegistrationFixture(revision uint64) (TaskMeetingRegistrationRequest, error) {
	audience := []string{"mio", "shiro", "aka", "ao", "kin", "gin", "midori", "kuro"}
	bindings := make([]domainmeeting.WorldActorBinding, 0, len(audience))
	for _, actorID := range audience {
		bindings = append(bindings, domainmeeting.WorldActorBinding{CoreActorID: actorID, WorldActorID: "world-" + actorID})
	}
	declaration := domainmeeting.TaskMeetingDeclaration{
		RegistrationKey:          domainmeeting.TaskMeetingRegistrationKey,
		OwnerUserID:              "owner-ren",
		Revision:                 revision,
		DataScope:                "user",
		CoordinatorActorID:       "mio",
		Audience:                 audience,
		WorldActorBindings:       bindings,
		WorldBaseURL:             "http://127.0.0.1:8080",
		WorldCredentialEnv:       "REN_WORLD_TOKEN",
		WorldID:                  "home-world",
		TimelineID:               "main-timeline",
		TimelineEpoch:            "epoch-17",
		Rooms:                    domainmeeting.RoomRefs{Living: "room-living", Dining: "room-dining"},
		Timing:                   modulecore.TaskTiming{Timezone: domainmeeting.TimezoneJST, AllowedWindows: []modulecore.TaskTimeWindow{}, Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00", "12:00", "17:00"}, Weekdays: []time.Weekday{}}},
		MeetingDeadlineAfterSecs: 3600,
	}
	encoded, _, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		return TaskMeetingRegistrationRequest{}, err
	}
	return TaskMeetingRegistrationRequest{Declaration: declaration, FrozenDeclaration: encoded}, nil
}

func TestTaskMeetingScheduleRegistrationIssuesStableOwnerIDsAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "task-meeting-registration.sqlite")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(first): %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	authorize := func(context.Context, domainmeeting.TaskMeetingDeclaration) error { return nil }
	firstRequest, err := taskMeetingRegistrationFixture(1)
	if err != nil {
		t.Fatal(err)
	}
	one, err := first.EnsureTaskMeetingScheduleRegistration(ctx, firstRequest, authorize)
	if err != nil {
		t.Fatalf("EnsureTaskMeetingScheduleRegistration(first): %v", err)
	}
	retry, err := first.EnsureTaskMeetingScheduleRegistration(ctx, firstRequest, authorize)
	if err != nil || retry.ScheduleID != one.ScheduleID || retry.RegistrationEvidenceID != one.RegistrationEvidenceID || retry.EventID != one.EventID {
		t.Fatalf("exact retry = %#v, %v; want identical issued IDs", retry, err)
	}

	changedSameRevision, err := taskMeetingRegistrationFixture(1)
	if err != nil {
		t.Fatal(err)
	}
	changedSameRevision.Declaration.TimelineEpoch = "epoch-different"
	changedSameRevision.FrozenDeclaration, _, err = domainmeeting.CanonicalTaskMeetingDeclarationJSON(changedSameRevision.Declaration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnsureTaskMeetingScheduleRegistration(ctx, changedSameRevision, authorize); !errors.Is(err, ErrTaskMeetingConflict) {
		t.Fatalf("same revision changed payload error = %v, want conflict", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen for revision update): %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	secondRequest, err := taskMeetingRegistrationFixture(4) // skipped revision numbers are allowed
	if err != nil {
		t.Fatal(err)
	}
	secondRequest.Declaration.TimelineEpoch = "epoch-18"
	secondRequest.FrozenDeclaration, _, err = domainmeeting.CanonicalTaskMeetingDeclarationJSON(secondRequest.Declaration)
	if err != nil {
		t.Fatal(err)
	}
	four, err := second.EnsureTaskMeetingScheduleRegistration(ctx, secondRequest, authorize)
	if err != nil || four.ScheduleID != one.ScheduleID || four.Revision != 4 || four.RegistrationEvidenceID == one.RegistrationEvidenceID {
		t.Fatalf("higher revision receipt = %#v, %v; want same schedule and new evidence", four, err)
	}

	stale, err := taskMeetingRegistrationFixture(3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.EnsureTaskMeetingScheduleRegistration(ctx, stale, authorize); !errors.Is(err, ErrTaskMeetingConflict) {
		t.Fatalf("rollback revision error = %v, want conflict", err)
	}

	readAuth := func(context.Context, string, string, uint64) error { return nil }
	old, err := second.FindTaskMeetingScheduleRegistration(ctx, domainmeeting.TaskMeetingRegistrationKey, "owner-ren", 1, readAuth)
	if err != nil || old.ScheduleID != one.ScheduleID || old.ConfigSHA256 != one.ConfigSHA256 || string(old.FrozenDeclaration) != string(one.FrozenDeclaration) {
		t.Fatalf("historical revision read = %#v, %v; want frozen revision 1", old, err)
	}
	latest, err := second.FindLatestTaskMeetingScheduleRegistration(ctx, domainmeeting.TaskMeetingRegistrationKey, "owner-ren", func(_ context.Context, owner, key string, revision uint64) error {
		if owner != "owner-ren" || key != domainmeeting.TaskMeetingRegistrationKey || revision != 0 {
			t.Fatalf("latest read authorization tuple = %q, %q, %d", owner, key, revision)
		}
		return nil
	})
	if err != nil || latest.Revision != 4 || latest.ScheduleID != one.ScheduleID || latest.EventID != four.EventID {
		t.Fatalf("latest registration read = %#v, %v; want revision 4 without target scan", latest, err)
	}
	gotEvent, found, err := second.GetByID(ctx, one.EventID)
	if err != nil || found || gotEvent.EventID != "" {
		t.Fatalf("generic GetByID leaked private registration event: %#v, %t, %v", gotEvent, found, err)
	}
	if events, err := second.ListByComponent(ctx, taskMeetingComponent, 20); err != nil || len(events) != 0 {
		t.Fatalf("generic ListByComponent exposed TaskMeeting receipts: %d events, %v", len(events), err)
	}
	var receiptCount int
	if err := second.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_kind = ?`, taskMeetingRegistrationOperationKind).Scan(&receiptCount); err != nil || receiptCount != 2 {
		t.Fatalf("registration receipt count = %d, %v; want exactly two immutable revisions", receiptCount, err)
	}
	var persisted []byte
	if err := second.db.QueryRowContext(ctx, `SELECT result_json FROM live_operation_receipt WHERE operation_id = ?`, taskMeetingRegistrationOperationID("owner-ren", domainmeeting.TaskMeetingRegistrationKey, 4)).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	var decoded TaskMeetingScheduleRegistrationReceipt
	if err := json.Unmarshal(persisted, &decoded); err != nil || decoded.ScheduleID != one.ScheduleID {
		t.Fatalf("stored revision 4 receipt decode = %#v, %v", decoded, err)
	}

	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err := reopened.EnsureTaskMeetingScheduleRegistration(ctx, secondRequest, authorize)
	if err != nil || replayed.ScheduleID != four.ScheduleID || replayed.EventID != four.EventID || replayed.RegistrationEvidenceID != four.RegistrationEvidenceID {
		t.Fatalf("restart replay = %#v, %v; want exact revision 4 IDs", replayed, err)
	}
}

func TestTaskMeetingRegistrationRequiresCanonicalTypedDeclaration(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	request, err := taskMeetingRegistrationFixture(1)
	if err != nil {
		t.Fatal(err)
	}
	request.FrozenDeclaration = append(request.FrozenDeclaration, []byte(" ")...)
	called := false
	if _, err := store.EnsureTaskMeetingScheduleRegistration(ctx, request, func(context.Context, domainmeeting.TaskMeetingDeclaration) error { called = true; return nil }); !errors.Is(err, ErrTaskMeetingConflict) {
		t.Fatalf("noncanonical declaration error = %v, want conflict", err)
	}
	if called {
		t.Fatal("authorization callback ran before canonical declaration validation")
	}
}
