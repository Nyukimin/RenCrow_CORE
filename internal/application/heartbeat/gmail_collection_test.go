package heartbeat

import (
	"context"
	"errors"
	"testing"
	"time"

	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type gmailIntakeFunc func(context.Context) (gmailapp.GmailRunReport, error)

func (f gmailIntakeFunc) Run(ctx context.Context) (gmailapp.GmailRunReport, error) { return f(ctx) }
func (f gmailIntakeFunc) RunDiaryBackfill(ctx context.Context) (gmailapp.GmailRunReport, error) {
	return f(ctx)
}

type gmailRunOnlyFunc func(context.Context) (gmailapp.GmailRunReport, error)

func (f gmailRunOnlyFunc) Run(ctx context.Context) (gmailapp.GmailRunReport, error) {
	return f(ctx)
}

type gmailDailyIntake struct {
	calls chan string
	run   func(context.Context) (gmailapp.GmailRunReport, error)
	diary func(context.Context) (gmailapp.GmailRunReport, error)
}

func (g gmailDailyIntake) Run(ctx context.Context) (gmailapp.GmailRunReport, error) {
	g.calls <- "normal"
	if g.run != nil {
		return g.run(ctx)
	}
	return gmailapp.GmailRunReport{}, nil
}

func (g gmailDailyIntake) RunDiaryBackfill(ctx context.Context) (gmailapp.GmailRunReport, error) {
	g.calls <- "diary"
	if g.diary != nil {
		return g.diary(ctx)
	}
	return gmailapp.GmailRunReport{}, nil
}

func receiveGmailCall(t *testing.T, calls <-chan string) string {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("Gmail intake did not start")
		return ""
	}
}

func TestGmailStartsRejectAfterHeartbeatStop(t *testing.T) {
	for _, start := range []struct {
		name string
		call func(*HeartbeatService) bool
	}{
		{name: "regular intake", call: (*HeartbeatService).startGmailIntake},
		{name: "diary backfill", call: (*HeartbeatService).StartGmailDiaryBackfill},
	} {
		t.Run(start.name, func(t *testing.T) {
			called := false
			svc := NewHeartbeatService(nil, nil, t.TempDir(), 30)
			svc.WithGmailIntake(gmailIntakeFunc(func(context.Context) (gmailapp.GmailRunReport, error) {
				called = true
				return gmailapp.GmailRunReport{}, nil
			}), "ren", time.Hour, time.Minute, false)
			close(svc.stopCh)
			if start.call(svc) {
				t.Fatal("stopped service accepted Gmail work")
			}
			svc.waitForGmailIntake()
			if called {
				t.Fatal("intake ran after stop")
			}
		})
	}
}

func TestGmailIntakeRunsWithDurableShiroIdentityAndOwnerScope(t *testing.T) {
	for _, fail := range []bool{false, true} {
		svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
		var identity execution.Identity
		intake := gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
			var err error
			identity, err = execution.IdentityFromContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
			if !ok || scope.ActorID != "shiro" || scope.AuthenticatedUserID != "ren" || !scope.Allows(domaintool.DataScopeUser) {
				t.Fatal("wrong worker scope")
			}
			if fail {
				return gmailapp.GmailRunReport{}, errors.New("source unavailable")
			}
			return gmailapp.GmailRunReport{}, nil
		})
		svc.WithGmailIntake(intake, "ren", time.Hour, time.Minute, false)
		_, err := svc.runGmailIntake(context.Background())
		if (err != nil) != fail {
			t.Fatalf("err=%v", err)
		}
		reader := svc.taskOwner.(interface {
			Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
		})
		task, err := reader.Get(context.Background(), identity.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		want := domaintask.StatusSucceeded
		if fail {
			want = domaintask.StatusFailed
		}
		if task.Assignee != "shiro" || task.Status != want {
			t.Fatalf("terminal task=%+v", task)
		}
	}
}

func TestGmailCollectionSingleFlightAndShutdownCancel(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	started := make(chan struct{})
	stopped := make(chan struct{})
	svc.WithGmailIntake(gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return gmailapp.GmailRunReport{}, ctx.Err()
	}), "ren", time.Hour, time.Minute, false)
	if !svc.startGmailIntake() {
		t.Fatal("did not start")
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("intake did not enter")
	}
	if svc.startGmailIntake() {
		t.Fatal("concurrent intake started")
	}
	svc.stopGmailIntake()
	select {
	case <-stopped:
	default:
		t.Fatal("shutdown did not await cancellation")
	}
}

func TestGmailScheduledDiaryBackfillUsesJSTDateAndThenNormalPolling(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	calls := make(chan string, 6)
	now := time.Date(2026, time.November, 18, 14, 59, 0, 0, time.UTC)
	var diaryRuns int
	intake := gmailDailyIntake{calls: calls, diary: func(context.Context) (gmailapp.GmailRunReport, error) {
		diaryRuns++
		if diaryRuns == 1 {
			// The first scan starts before JST midnight and completes after it.
			now = time.Date(2026, time.November, 18, 15, 3, 0, 0, time.UTC)
		}
		return gmailapp.GmailRunReport{}, nil
	}}
	svc.WithGmailIntake(intake, "ren", time.Hour, time.Minute, false)
	svc.gmail.now = func() time.Time { return now }

	if !svc.startGmailIntake() {
		t.Fatal("daily diary backfill did not start")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("first call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("first tick follow-up = %q, want normal intake", got)
	}
	svc.waitForGmailIntake()

	if !svc.startGmailIntake() {
		t.Fatal("next-JST-day diary backfill did not start after the prior-day scan crossed midnight")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("next-JST-day call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("next-day follow-up = %q, want normal intake", got)
	}
	svc.waitForGmailIntake()

	if !svc.startGmailIntake() {
		t.Fatal("same-JST-day normal intake did not start")
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("same-JST-day call = %q, want normal intake", got)
	}
	svc.waitForGmailIntake()

	// A fresh service has no process-local success checkpoint, so startup
	// catch-up runs the diary scan once again on the same JST date.
	restarted := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	restartCalls := make(chan string, 2)
	restarted.WithGmailIntake(gmailDailyIntake{calls: restartCalls}, "ren", time.Hour, time.Minute, false)
	restarted.gmail.now = func() time.Time { return now }
	if !restarted.startGmailIntake() {
		t.Fatal("restart catch-up diary backfill did not start")
	}
	if got := receiveGmailCall(t, restartCalls); got != "diary" {
		t.Fatalf("restart call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, restartCalls); got != "normal" {
		t.Fatalf("restart follow-up = %q, want normal intake", got)
	}
	restarted.waitForGmailIntake()
}

func TestGmailScheduledDiaryBackfillFailureRetriesAndBusySlotPreservesDueWork(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	calls := make(chan string, 5)
	releaseFirst := make(chan struct{})
	var diaryRuns int
	intake := gmailDailyIntake{calls: calls, diary: func(context.Context) (gmailapp.GmailRunReport, error) {
		diaryRuns++
		if diaryRuns == 1 {
			<-releaseFirst
			return gmailapp.GmailRunReport{}, errors.New("owner backfill did not complete")
		}
		return gmailapp.GmailRunReport{}, nil
	}}
	svc.WithGmailIntake(intake, "ren", time.Hour, time.Minute, false)
	svc.gmail.now = func() time.Time { return time.Date(2026, time.November, 18, 6, 0, 0, 0, time.UTC) }

	if !svc.startGmailIntake() {
		t.Fatal("scheduled diary backfill did not start")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("scheduled call = %q, want diary backfill", got)
	}
	if svc.startGmailIntake() {
		t.Fatal("scheduled intake started while the diary backfill held the shared slot")
	}
	close(releaseFirst)
	svc.waitForGmailIntake()
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("normal poll after failed daily scan = %q, want normal", got)
	}

	if !svc.startGmailIntake() {
		t.Fatal("due diary backfill was dropped after the busy tick")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("retry call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("normal poll after successful retry = %q, want normal", got)
	}
	svc.waitForGmailIntake()

	if !svc.startGmailIntake() {
		t.Fatal("normal polling did not resume after successful daily scan")
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("post-success call = %q, want normal intake", got)
	}
	svc.waitForGmailIntake()
}

func TestGmailOwnerDiaryBackfillDoesNotMarkScheduledDateComplete(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	calls := make(chan string, 2)
	listener := &recordingEventListener{}
	var ownerIdentity execution.Identity
	var diaryRuns int
	intake := gmailDailyIntake{calls: calls, diary: func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		diaryRuns++
		if diaryRuns == 1 {
			var err error
			ownerIdentity, err = execution.IdentityFromContext(ctx)
			if err != nil {
				return gmailapp.GmailRunReport{}, err
			}
			return gmailapp.GmailRunReport{Processed: 1, Blocked: 1}, nil
		}
		return gmailapp.GmailRunReport{Processed: 1}, nil
	}}
	svc.WithGmailIntake(intake, "ren", time.Hour, time.Minute, false).WithEventListener(listener)
	svc.gmail.now = func() time.Time { return time.Date(2026, time.November, 18, 6, 0, 0, 0, time.UTC) }

	if !svc.StartGmailDiaryBackfill() {
		t.Fatal("owner backfill did not start")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("owner call = %q, want diary backfill", got)
	}
	svc.waitForGmailIntake()
	ownerTask, err := svc.taskOwner.(interface {
		Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	}).Get(context.Background(), ownerIdentity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if ownerTask.Status != domaintask.StatusSucceeded {
		t.Fatalf("manual owner scan status = %q, want succeeded despite its blocked report", ownerTask.Status)
	}
	events := eventTypes(listener)
	if events["heartbeat.gmail.diary_backfill.completed"] != 1 || events["heartbeat.gmail.diary_backfill.partial"] != 0 {
		t.Fatalf("manual owner events = %v, want legacy completed semantics", events)
	}

	if !svc.startGmailIntake() {
		t.Fatal("daily scan was skipped after owner backfill")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("scheduled call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("normal poll after scheduled scan = %q, want normal", got)
	}
	svc.waitForGmailIntake()
}

func TestGmailScheduledDiaryBackfillBlockedReportRetriesBeforeNormalPolling(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	calls := make(chan string, 5)
	listener := &recordingEventListener{}
	var firstIdentity execution.Identity
	var diaryRuns int
	intake := gmailDailyIntake{calls: calls, diary: func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		diaryRuns++
		if diaryRuns == 1 {
			var err error
			firstIdentity, err = execution.IdentityFromContext(ctx)
			if err != nil {
				return gmailapp.GmailRunReport{}, err
			}
			return gmailapp.GmailRunReport{Processed: 1, Blocked: 1}, nil
		}
		return gmailapp.GmailRunReport{Processed: 1}, nil
	}}
	svc.WithGmailIntake(intake, "ren", time.Hour, time.Minute, false).WithEventListener(listener)
	svc.gmail.now = func() time.Time { return time.Date(2026, time.November, 18, 6, 0, 0, 0, time.UTC) }

	if !svc.startGmailIntake() {
		t.Fatal("daily diary backfill did not start")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("first call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("normal poll after partial scan = %q, want normal", got)
	}
	svc.waitForGmailIntake()

	svc.gmail.mu.Lock()
	lastSuccessfulDate := svc.gmail.lastSuccessfulDiaryBackfillJSTDate
	svc.gmail.mu.Unlock()
	if lastSuccessfulDate != "" {
		t.Fatalf("blocked scan was marked successful for %q", lastSuccessfulDate)
	}
	firstTask, err := svc.taskOwner.(interface {
		Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	}).Get(context.Background(), firstIdentity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if firstTask.Status != domaintask.StatusFailed {
		t.Fatalf("scheduled partial task status = %q, want failed", firstTask.Status)
	}
	events := eventTypes(listener)
	if events["heartbeat.gmail.diary_backfill.partial"] != 1 || events["heartbeat.gmail.diary_backfill.completed"] != 0 {
		t.Fatalf("partial scan events = %v, want explicit partial and no success", events)
	}

	if !svc.startGmailIntake() {
		t.Fatal("blocked daily scan was not retried")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("retry call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("normal poll after unblocked retry = %q, want normal", got)
	}
	svc.waitForGmailIntake()
	svc.gmail.mu.Lock()
	lastSuccessfulDate = svc.gmail.lastSuccessfulDiaryBackfillJSTDate
	svc.gmail.mu.Unlock()
	if lastSuccessfulDate != "2026-11-18" {
		t.Fatalf("unblocked retry success date = %q, want current JST date", lastSuccessfulDate)
	}

	if !svc.startGmailIntake() {
		t.Fatal("normal polling did not resume after the unblocked daily scan")
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("post-success call = %q, want normal intake", got)
	}
	svc.waitForGmailIntake()
}

func TestGmailScheduledDiaryBackfillRetriesAfterCancellation(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	calls := make(chan string, 2)
	var diaryRuns int
	intake := gmailDailyIntake{calls: calls, diary: func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		diaryRuns++
		if diaryRuns == 1 {
			<-ctx.Done()
			return gmailapp.GmailRunReport{}, ctx.Err()
		}
		return gmailapp.GmailRunReport{}, nil
	}}
	svc.WithGmailIntake(intake, "ren", time.Hour, time.Minute, false)
	svc.gmail.now = func() time.Time { return time.Date(2026, time.November, 18, 6, 0, 0, 0, time.UTC) }

	if !svc.startGmailIntake() {
		t.Fatal("scheduled diary backfill did not start")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("first call = %q, want diary backfill", got)
	}
	svc.cancelGmailIntake()
	svc.waitForGmailIntake()

	if !svc.startGmailIntake() {
		t.Fatal("cancelled daily scan was not retried")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("retry call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("normal poll after retry = %q, want normal", got)
	}
	svc.waitForGmailIntake()
}

func TestGmailScheduledDiaryBackfillRetriesAfterAdmissionDeferral(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	calls := make(chan string, 2)
	listener := &recordingEventListener{}
	svc.WithGmailIntake(gmailDailyIntake{calls: calls}, "ren", time.Hour, time.Minute, false)
	svc.WithEventListener(listener)
	svc.WithActorAdmission(&heartbeatActorAdmissionFake{})
	svc.gmail.now = func() time.Time { return time.Date(2026, time.November, 18, 6, 0, 0, 0, time.UTC) }

	if !svc.startGmailIntake() {
		t.Fatal("deferred daily scan was not scheduled")
	}
	svc.waitForGmailIntake()
	if len(calls) != 0 {
		t.Fatal("intake ran despite actor admission deferral")
	}
	counts := eventTypes(listener)
	if counts["heartbeat.gmail.diary_backfill.deferred"] != 1 || counts["heartbeat.gmail.diary_backfill.error"] != 0 {
		t.Fatalf("events=%v, want one daily defer and no daily error", counts)
	}
	svc.gmail.mu.Lock()
	lastSuccessfulDate := svc.gmail.lastSuccessfulDiaryBackfillJSTDate
	svc.gmail.mu.Unlock()
	if lastSuccessfulDate != "" {
		t.Fatalf("deferred scan was marked successful for %q", lastSuccessfulDate)
	}

	svc.WithActorAdmission(&heartbeatActorAdmissionFake{available: true})
	if !svc.startGmailIntake() {
		t.Fatal("daily scan was not retried after admission deferral")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("retry call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("normal poll after retry = %q, want normal", got)
	}
	svc.waitForGmailIntake()
}

func TestGmailScheduledDiarySuccessSurvivesNormalPollFailure(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	calls := make(chan string, 2)
	listener := &recordingEventListener{}
	intake := gmailDailyIntake{
		calls: calls,
		run: func(context.Context) (gmailapp.GmailRunReport, error) {
			return gmailapp.GmailRunReport{}, errors.New("normal poll unavailable")
		},
	}
	svc.WithGmailIntake(intake, "ren", time.Hour, time.Minute, false).WithEventListener(listener)
	svc.gmail.now = func() time.Time { return time.Date(2026, time.November, 18, 6, 0, 0, 0, time.UTC) }

	if !svc.startGmailIntake() {
		t.Fatal("daily scan did not start")
	}
	if got := receiveGmailCall(t, calls); got != "diary" {
		t.Fatalf("first call = %q, want diary backfill", got)
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("second call = %q, want normal intake", got)
	}
	svc.waitForGmailIntake()

	svc.gmail.mu.Lock()
	lastSuccessfulDate := svc.gmail.lastSuccessfulDiaryBackfillJSTDate
	svc.gmail.mu.Unlock()
	if lastSuccessfulDate != "2026-11-18" {
		t.Fatalf("normal failure erased daily success date: %q", lastSuccessfulDate)
	}
	events := eventTypes(listener)
	if events["heartbeat.gmail.diary_backfill.completed"] != 1 || events["heartbeat.gmail.error"] != 1 {
		t.Fatalf("daily and normal events = %v, want daily completed and normal error", events)
	}
}

func TestGmailScheduledIntakeWithoutDiaryBackfillKeepsNormalPolling(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	calls := make(chan string, 1)
	svc.WithGmailIntake(gmailRunOnlyFunc(func(context.Context) (gmailapp.GmailRunReport, error) {
		calls <- "normal"
		return gmailapp.GmailRunReport{}, nil
	}), "ren", time.Hour, time.Minute, false)

	if !svc.startGmailIntake() {
		t.Fatal("normal intake did not start")
	}
	if got := receiveGmailCall(t, calls); got != "normal" {
		t.Fatalf("call = %q, want normal intake", got)
	}
	svc.waitForGmailIntake()
}
