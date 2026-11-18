package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
)

type taskMeetingWorldGatherFixture struct {
	store      *SQLiteStore
	path       string
	reader     taskMeetingStateReaderStub
	scope      domainmeeting.WorldObservationRequest
	readAuth   domaintask.TaskMeetingReadAuthorizationCheck
	effectAuth domainmeeting.TaskMeetingWorldGatherAuthorizationCheck
}

func newTaskMeetingWorldGatherFixture(t *testing.T) taskMeetingWorldGatherFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "task-meeting-world-gather.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	registrationRequest, err := taskMeetingRegistrationFixture(1)
	if err != nil {
		t.Fatalf("taskMeetingRegistrationFixture: %v", err)
	}
	registration, err := store.EnsureTaskMeetingScheduleRegistration(ctx, registrationRequest,
		func(context.Context, domainmeeting.TaskMeetingDeclaration) error { return nil })
	if err != nil {
		t.Fatalf("EnsureTaskMeetingScheduleRegistration: %v", err)
	}
	key := domainmeeting.OccurrenceKey{ScheduleID: registration.ScheduleID, LocalDate: "2026-11-18", Slot: "08:00", Timezone: domainmeeting.TimezoneJST}
	reader := taskMeetingRootStateFixture(t, registrationRequest, registration)
	reader.task.TaskMeetingInvocation.OccurrenceKey = key.String()
	if _, err := store.PinTaskMeetingOccurrenceRegistration(ctx, registration, key,
		func(context.Context, TaskMeetingScheduleRegistrationReceipt, domainmeeting.OccurrenceKey) error {
			return nil
		}); err != nil {
		t.Fatalf("PinTaskMeetingOccurrenceRegistration: %v", err)
	}
	readAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	groupSession, err := store.EnsureTaskMeetingGroupSession(ctx, TaskMeetingGroupSessionRequest{
		OccurrenceKey: key.String(), TaskID: reader.task.TaskID, RunID: reader.run.RunID,
		OwnerUserID: registration.OwnerUserID, CoordinatorActorID: registrationRequest.Declaration.CoordinatorActorID,
		RegistrationEvidenceID: registration.RegistrationEvidenceID, ConfigRevision: registration.Revision,
		ConfigSHA256: registration.ConfigSHA256, InputSHA256: reader.task.TaskMeetingInvocation.InputSHA256,
		RequestID: "meeting-session:" + key.String(), Audience: append([]string(nil), registrationRequest.Declaration.Audience...),
	}, reader, readAuth, func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil })
	if err != nil {
		t.Fatalf("EnsureTaskMeetingGroupSession: %v", err)
	}
	effectAuth := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domainmeeting.WorldGatherCommandIntent) error {
		return nil
	}
	scope := domainmeeting.WorldObservationRequest{
		Key: key, TaskID: reader.task.TaskID, RunID: reader.run.RunID,
		ConfigRevision: registration.Revision, ConfigSHA256: registration.ConfigSHA256,
		OwnerUserID: registration.OwnerUserID, RegistrationEvidenceID: registration.RegistrationEvidenceID,
		CoordinatorActorID: registrationRequest.Declaration.CoordinatorActorID,
		Session:            groupSession.SessionID, Thread: groupSession.ThreadID, Generation: 1, Epoch: 1,
		Audience:      append([]string(nil), registrationRequest.Declaration.Audience...),
		ActorBindings: append([]domainmeeting.WorldActorBinding(nil), registrationRequest.Declaration.WorldActorBindings...),
		Rooms:         registrationRequest.Declaration.Rooms,
	}
	return taskMeetingWorldGatherFixture{store: store, path: path, reader: reader, scope: scope, readAuth: readAuth, effectAuth: effectAuth}
}

func (fixture taskMeetingWorldGatherFixture) startIntent(t *testing.T) domainmeeting.WorldGatherCommandIntent {
	t.Helper()
	worldActorID := fixture.scope.ActorBindings[0].WorldActorID
	payload, err := json.Marshal(domainmeeting.WorldGatherCommandPayload{ActorID: worldActorID, RoomID: fixture.scope.Rooms.Living})
	if err != nil {
		t.Fatal(err)
	}
	intent := domainmeeting.WorldGatherCommandIntent{
		Scope: fixture.scope, ActorID: fixture.scope.Audience[0], WorldActorID: worldActorID,
		TargetRoomID: fixture.scope.Rooms.Living, Phase: domainmeeting.WorldGatherPhaseStart,
		Payload: payload,
	}
	intent.StableKey, err = domainmeeting.WorldGatherCommandStableKey(intent)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func taskMeetingWorldGatherOutcome(intent domainmeeting.WorldGatherCommandIntent) domainmeeting.WorldGatherCommandOutcome {
	return domainmeeting.WorldGatherCommandOutcome{
		StableKey: intent.StableKey, Phase: intent.Phase,
		Receipt: domainmeeting.WorldGatherCommandReceipt{
			ReceiptID: "world-receipt-" + string(intent.Phase), EventID: "world-event-" + string(intent.Phase),
			EventSeq: 41, ActivityID: "world-activity-123", RouteID: "world-route-456",
			SceneSHA256:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			TargetRoomID: intent.TargetRoomID, DueAt: time.Date(2026, 11, 18, 8, 5, 0, 0, time.UTC),
		},
	}
}

func (fixture taskMeetingWorldGatherFixture) finishIntent(t *testing.T, start domainmeeting.WorldGatherCommandIntent, outcome domainmeeting.WorldGatherCommandOutcome) domainmeeting.WorldGatherCommandIntent {
	t.Helper()
	payload, _ := json.Marshal(domainmeeting.WorldGatherCommandPayload{ActorID: start.WorldActorID, ActivityID: outcome.Receipt.ActivityID})
	intent := domainmeeting.WorldGatherCommandIntent{
		Scope: fixture.scope, ActorID: start.ActorID, WorldActorID: start.WorldActorID, TargetRoomID: start.TargetRoomID,
		Phase: domainmeeting.WorldGatherPhaseFinish, ActivityID: outcome.Receipt.ActivityID,
		RouteID: outcome.Receipt.RouteID, SceneSHA256: outcome.Receipt.SceneSHA256, DueAt: outcome.Receipt.DueAt,
		Payload: payload, StartStableKey: start.StableKey,
	}
	var err error
	intent.StableKey, err = domainmeeting.WorldGatherCommandStableKey(intent)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func TestTaskMeetingWorldGatherIntentIsPreparedBeforeEffectsAndReplays(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	intent := fixture.startIntent(t)
	prepared, created, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth, fixture.effectAuth)
	if err != nil || !created || prepared.StableKey != intent.StableKey {
		t.Fatalf("prepare gather intent = %#v created=%t err=%v; want newly persisted stable intent", prepared, created, err)
	}
	// No WORLD client is passed to the durable owner: successful prepare is the
	// checkpoint that must precede any source command, including after restart.
	replayed, replayCreated, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth, fixture.effectAuth)
	if err != nil || replayCreated || replayed.StableKey != intent.StableKey || string(replayed.Payload) != string(intent.Payload) {
		t.Fatalf("replay prepared intent = %#v created=%t err=%v; want exact stored intent", replayed, replayCreated, err)
	}
	foundIntent, foundOutcome, found, err := fixture.store.FindTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth)
	if err != nil || !found || foundIntent.StableKey != intent.StableKey || foundOutcome.StableKey != "" {
		t.Fatalf("find intent before outcome = %#v outcome=%#v found=%t err=%v", foundIntent, foundOutcome, found, err)
	}
	refreshedObservation := intent
	refreshedObservation.Scope.Generation++
	refreshedObservation.Scope.Epoch++
	refreshedObservation.Scope.RequestID = "refreshed-gather-read-request"
	refreshedObservation.Scope.ObservedAt = time.Now().UTC()
	refreshedObservation.Scope.ValidUntil = time.Now().UTC().Add(time.Minute)
	refreshedObservation.Scope.Snapshot = domainmeeting.WorldSnapshotObservation{
		WorldID: "newer-observation", StateSeq: 99,
	}
	refreshedKey, err := domainmeeting.WorldGatherCommandStableKey(refreshedObservation)
	if err != nil || refreshedKey != intent.StableKey {
		t.Fatalf("refresh/guard change altered stable key = %q err=%v, want %q", refreshedKey, err, intent.StableKey)
	}
	refreshedIntent, refreshedOutcome, found, err := fixture.store.FindTaskMeetingWorldGather(ctx, refreshedObservation, fixture.reader, fixture.readAuth)
	if err != nil || !found || refreshedIntent.StableKey != intent.StableKey || refreshedOutcome.StableKey != "" {
		t.Fatalf("find after mutable observation refresh = %#v outcome=%#v found=%t err=%v", refreshedIntent, refreshedOutcome, found, err)
	}

	changedActor := intent
	changedActor.ActorID = fixture.scope.Audience[1]
	changedActor.WorldActorID = fixture.scope.ActorBindings[1].WorldActorID
	changedActor.Payload, _ = json.Marshal(domainmeeting.WorldGatherCommandPayload{ActorID: changedActor.WorldActorID, RoomID: changedActor.TargetRoomID})
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, changedActor, fixture.reader, fixture.readAuth, fixture.effectAuth); err == nil {
		t.Fatal("same stable key with changed actor was accepted")
	}
	changedPayload := intent
	changedPayload.TargetRoomID = fixture.scope.Rooms.Dining
	changedPayload.Payload, _ = json.Marshal(domainmeeting.WorldGatherCommandPayload{ActorID: intent.WorldActorID, RoomID: changedPayload.TargetRoomID})
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, changedPayload, fixture.reader, fixture.readAuth, fixture.effectAuth); err == nil {
		t.Fatal("same stable key with changed payload was accepted")
	}
	changedScope := intent
	changedScope.Scope = fixture.scope
	changedScope.Scope.ConfigSHA256 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, changedScope, fixture.reader, fixture.readAuth, fixture.effectAuth); err == nil {
		t.Fatal("same stable key with changed frozen scope was accepted")
	}
	var intentReceipts int
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_kind = ?`, taskMeetingWorldGatherIntentOperation).Scan(&intentReceipts); err != nil {
		t.Fatal(err)
	}
	if intentReceipts != 1 {
		t.Fatalf("intent receipts after rejected changed inputs = %d, want one original intent", intentReceipts)
	}
}

func TestTaskMeetingWorldGatherConcurrentPrepareAcrossConnections(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	fixture.store.db.SetMaxOpenConns(2)
	conn1, err := fixture.store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("open first pooled SQLite connection: %v", err)
	}
	conn2, err := fixture.store.db.Conn(ctx)
	if err != nil {
		_ = conn1.Close()
		t.Fatalf("open second pooled SQLite connection: %v", err)
	}
	_ = conn1.Close()
	_ = conn2.Close()
	intent := fixture.startIntent(t)
	start := make(chan struct{})
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, created, callErr := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth, fixture.effectAuth)
			results <- result{created: created, err: callErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent PrepareTaskMeetingWorldGather: %v", result.err)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent prepare created count = %d, want exactly one", createdCount)
	}
}

func TestTaskMeetingWorldGatherOutcomeRequiresVerifiedStartAndReplays(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	start := fixture.startIntent(t)
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, start, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("prepare start: %v", err)
	}
	startOutcome := taskMeetingWorldGatherOutcome(start)
	if err := fixture.store.RecordTaskMeetingWorldGatherOutcome(ctx, start, startOutcome, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("record verified start outcome: %v", err)
	}
	if err := fixture.store.RecordTaskMeetingWorldGatherOutcome(ctx, start, startOutcome, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("idempotently replay start outcome: %v", err)
	}
	storedStart, replayedStart, found, err := fixture.store.FindTaskMeetingWorldGather(ctx, start, fixture.reader, fixture.readAuth)
	if err != nil || !found || storedStart.StableKey != start.StableKey || replayedStart != startOutcome {
		t.Fatalf("find recorded start = %#v outcome=%#v found=%t err=%v", storedStart, replayedStart, found, err)
	}

	finish := fixture.finishIntent(t, start, startOutcome)
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, finish, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("prepare finish bound to verified activity: %v", err)
	}
	finishOutcome := taskMeetingWorldGatherOutcome(finish)
	finishOutcome.Receipt.ActivityID = startOutcome.Receipt.ActivityID
	finishOutcome.Receipt.RouteID = startOutcome.Receipt.RouteID
	finishOutcome.Receipt.SceneSHA256 = startOutcome.Receipt.SceneSHA256
	finishOutcome.Receipt.DueAt = startOutcome.Receipt.DueAt
	if err := fixture.store.RecordTaskMeetingWorldGatherOutcome(ctx, finish, finishOutcome, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("record finish outcome for exact started activity: %v", err)
	}
	if err := fixture.store.RecordTaskMeetingWorldGatherOutcome(ctx, finish, finishOutcome, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("idempotently replay finish outcome: %v", err)
	}
	_, replayedFinish, found, err := fixture.store.FindTaskMeetingWorldGather(ctx, finish, fixture.reader, fixture.readAuth)
	if err != nil || !found || replayedFinish != finishOutcome {
		t.Fatalf("find finish outcome = %#v found=%t err=%v", replayedFinish, found, err)
	}

	for name, mutate := range map[string]func(*domainmeeting.WorldGatherCommandOutcome){
		"different activity": func(outcome *domainmeeting.WorldGatherCommandOutcome) {
			outcome.Receipt.ActivityID = "foreign-activity"
		},
		"different route": func(outcome *domainmeeting.WorldGatherCommandOutcome) { outcome.Receipt.RouteID = "foreign-route" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := finishOutcome
			mutate(&forged)
			if err := fixture.store.RecordTaskMeetingWorldGatherOutcome(ctx, finish, forged, fixture.reader, fixture.readAuth, fixture.effectAuth); err == nil {
				t.Fatal("forged finish result was accepted")
			}
			_, persisted, found, readErr := fixture.store.FindTaskMeetingWorldGather(ctx, finish, fixture.reader, fixture.readAuth)
			if readErr != nil || !found || persisted != finishOutcome {
				t.Fatalf("forged result changed persisted finish outcome = %#v found=%t err=%v", persisted, found, readErr)
			}
		})
	}
}

func TestTaskMeetingWorldGatherFinishCannotBePreparedBeforeVerifiedStart(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	start := fixture.startIntent(t)
	startOutcome := taskMeetingWorldGatherOutcome(start)
	finish := fixture.finishIntent(t, start, startOutcome)
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, finish, fixture.reader, fixture.readAuth, fixture.effectAuth); !errors.Is(err, ErrTaskMeetingReceiptBad) {
		t.Fatalf("prepare finish without start outcome error = %v, want bad/missing receipt", err)
	}
	if err := fixture.store.RecordTaskMeetingWorldGatherOutcome(ctx, finish, taskMeetingWorldGatherOutcome(finish), fixture.reader, fixture.readAuth, fixture.effectAuth); !errors.Is(err, ErrTaskMeetingReceiptBad) {
		t.Fatalf("record finish without start outcome error = %v, want bad/missing receipt", err)
	}
}

func TestTaskMeetingWorldGatherIntentSurvivesRestartAndTerminalReadOnly(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	intent := fixture.startIntent(t)
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("prepare intent: %v", err)
	}
	path := fixture.path
	if err := fixture.store.Close(); err != nil {
		t.Fatalf("close store before restart: %v", err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, outcome, found, err := reopened.FindTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth)
	if err != nil || !found || recovered.StableKey != intent.StableKey || outcome.StableKey != "" {
		t.Fatalf("recovered prepared intent = %#v outcome=%#v found=%t err=%v", recovered, outcome, found, err)
	}
	startOutcome := taskMeetingWorldGatherOutcome(intent)
	if err := reopened.RecordTaskMeetingWorldGatherOutcome(ctx, intent, startOutcome, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("record outcome after restart: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close store before outcome recovery: %v", err)
	}
	finalStore, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen store after outcome: %v", err)
	}
	t.Cleanup(func() { _ = finalStore.Close() })
	fixture.reader.run.Status = domaintask.RunStatusSucceeded
	terminalRead := func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil }
	gotIntent, gotOutcome, found, err := finalStore.FindTaskMeetingWorldGather(ctx, intent, fixture.reader, terminalRead)
	if err != nil || !found || gotIntent.StableKey != intent.StableKey || gotOutcome != startOutcome {
		t.Fatalf("terminal root read of durable receipt = %#v outcome=%#v found=%t err=%v", gotIntent, gotOutcome, found, err)
	}
	if _, _, err := finalStore.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, terminalRead, fixture.effectAuth); !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("terminal root new effect prepare error = %v, want forbidden", err)
	}
	if err := finalStore.RecordTaskMeetingWorldGatherOutcome(ctx, intent, startOutcome, fixture.reader, terminalRead, fixture.effectAuth); !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("terminal root new outcome effect error = %v, want forbidden", err)
	}
}

func TestTaskMeetingWorldGatherRejectsForeignActorBinding(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	intent := fixture.startIntent(t)
	intent.WorldActorID = "world-outsider"
	intent.Payload, _ = json.Marshal(domainmeeting.WorldGatherCommandPayload{ActorID: intent.WorldActorID, RoomID: intent.TargetRoomID})
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth, fixture.effectAuth); err == nil {
		t.Fatal("foreign actor binding was accepted")
	}
}

func TestTaskMeetingWorldGatherIntentEventCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	intent := fixture.startIntent(t)
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("prepare intent: %v", err)
	}
	operationID := taskMeetingWorldGatherIntentOperationID(intent.StableKey)
	var storedJSON string
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT result_json FROM live_operation_receipt WHERE operation_id = ?`, operationID).Scan(&storedJSON); err != nil {
		t.Fatalf("read intent receipt: %v", err)
	}
	var record taskMeetingWorldGatherStoredRecord
	if err := json.Unmarshal([]byte(storedJSON), &record); err != nil {
		t.Fatalf("decode stored intent record: %v", err)
	}
	if _, err := fixture.store.db.ExecContext(ctx, `DROP TRIGGER event_envelope_append_only_delete`); err != nil {
		t.Fatalf("disable append-only delete guard in isolated corruption fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.store.db.ExecContext(context.Background(), `CREATE TRIGGER IF NOT EXISTS event_envelope_append_only_delete
			BEFORE DELETE ON event_envelope
			BEGIN
				SELECT RAISE(ABORT, 'event_envelope is append-only');
			END`)
	})
	if _, err := fixture.store.db.ExecContext(ctx, `DELETE FROM event_envelope WHERE event_id = ?`, string(record.EventID)); err != nil {
		t.Fatalf("corrupt canonical intent event: %v", err)
	}
	if _, _, found, err := fixture.store.FindTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth); found || !errors.Is(err, ErrTaskMeetingReceiptBad) {
		t.Fatalf("find after canonical event corruption found=%t err=%v; want receipt corruption", found, err)
	}
}

func TestTaskMeetingWorldGatherOutcomeReceiptCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	intent := fixture.startIntent(t)
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("prepare intent: %v", err)
	}
	if err := fixture.store.RecordTaskMeetingWorldGatherOutcome(ctx, intent, taskMeetingWorldGatherOutcome(intent), fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("record outcome: %v", err)
	}
	operationID := taskMeetingWorldGatherOutcomeOperationID(intent.StableKey)
	if _, err := fixture.store.db.ExecContext(ctx, `DROP TRIGGER live_operation_receipt_append_only_update`); err != nil {
		t.Fatalf("disable append-only update guard in isolated corruption fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.store.db.ExecContext(context.Background(), `CREATE TRIGGER IF NOT EXISTS live_operation_receipt_append_only_update
			BEFORE UPDATE ON live_operation_receipt
			BEGIN
				SELECT RAISE(ABORT, 'live_operation_receipt is append-only');
			END`)
	})
	if _, err := fixture.store.db.ExecContext(ctx, `UPDATE live_operation_receipt SET result_json = ? WHERE operation_id = ?`, "{", operationID); err != nil {
		t.Fatalf("corrupt stored outcome receipt: %v", err)
	}
	if _, _, found, err := fixture.store.FindTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth); found || !errors.Is(err, ErrTaskMeetingReceiptBad) {
		t.Fatalf("find after stored outcome corruption found=%t err=%v; want receipt corruption", found, err)
	}
}

func TestTaskMeetingWorldGatherForeignRootCannotReadOrPrepare(t *testing.T) {
	ctx := context.Background()
	fixture := newTaskMeetingWorldGatherFixture(t)
	intent := fixture.startIntent(t)
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, fixture.reader, fixture.readAuth, fixture.effectAuth); err != nil {
		t.Fatalf("prepare intent: %v", err)
	}
	foreign := fixture.reader
	foreign.task.TaskMeetingInvocation.OccurrenceKey = "foreign-occurrence"
	if _, _, found, err := fixture.store.FindTaskMeetingWorldGather(ctx, intent, foreign, fixture.readAuth); found || !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("foreign root read found=%t err=%v; want forbidden", found, err)
	}
	if _, _, err := fixture.store.PrepareTaskMeetingWorldGather(ctx, intent, foreign, fixture.readAuth, fixture.effectAuth); !errors.Is(err, ErrTaskMeetingForbidden) {
		t.Fatalf("foreign root prepare error = %v, want forbidden", err)
	}
}
