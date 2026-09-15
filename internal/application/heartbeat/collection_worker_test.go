package heartbeat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type collectionXFunc func(context.Context) (XBookmarkCollectionReport, error)

func (f collectionXFunc) Collect(ctx context.Context) (XBookmarkCollectionReport, error) {
	return f(ctx)
}

type collectionRunRecorder struct {
	mu         sync.Mutex
	active     int
	overlap    bool
	identities []execution.Identity
}

func (r *collectionRunRecorder) enter(ctx context.Context) error {
	identity, err := execution.IdentityFromContext(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active > 0 {
		r.overlap = true
	}
	r.active++
	r.identities = append(r.identities, identity)
	return nil
}

func (r *collectionRunRecorder) leave() {
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
}

func (r *collectionRunRecorder) snapshot() (bool, []execution.Identity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.overlap, append([]execution.Identity(nil), r.identities...)
}

func collectionTaskOwner(t *testing.T, svc *HeartbeatService) interface {
	Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	GetRun(context.Context, modulecore.RunID) (domaintask.Run, error)
	List(context.Context, domaintask.Filter) ([]domaintask.Task, error)
} {
	t.Helper()
	owner, ok := svc.taskOwner.(interface {
		Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
		GetRun(context.Context, modulecore.RunID) (domaintask.Run, error)
		List(context.Context, domaintask.Filter) ([]domaintask.Task, error)
	})
	if !ok {
		t.Fatalf("test Task owner does not expose inspection methods")
	}
	return owner
}

func TestCollectionWorkersSerializeWithDistinctCanonicalIdentities(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	recorder := &collectionRunRecorder{}
	gmailStarted := make(chan struct{}, 1)
	releaseGmail := make(chan struct{})
	xStarted := make(chan struct{}, 1)

	svc.WithGmailIntake(gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		if err := recorder.enter(ctx); err != nil {
			return gmailapp.GmailRunReport{}, err
		}
		defer recorder.leave()
		gmailStarted <- struct{}{}
		select {
		case <-ctx.Done():
			return gmailapp.GmailRunReport{}, ctx.Err()
		case <-releaseGmail:
			return gmailapp.GmailRunReport{}, nil
		}
	}), "ren", time.Hour, time.Minute, false)
	svc.WithXBookmarkCollection(collectionXFunc(func(ctx context.Context) (XBookmarkCollectionReport, error) {
		if err := recorder.enter(ctx); err != nil {
			return XBookmarkCollectionReport{}, err
		}
		defer recorder.leave()
		xStarted <- struct{}{}
		return XBookmarkCollectionReport{}, nil
	}), time.Hour, time.Minute, false)

	gmailDone := make(chan error, 1)
	go func() {
		_, err := svc.runGmailIntake(context.Background())
		gmailDone <- err
	}()
	select {
	case <-gmailStarted:
	case <-time.After(time.Second):
		t.Fatal("Gmail collector did not start")
	}

	xDone := make(chan error, 1)
	go func() {
		_, err := svc.runXBookmarkCollection(context.Background())
		xDone <- err
	}()
	select {
	case <-xStarted:
		t.Fatal("X collector started while Gmail collection held the shared slot")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseGmail)
	select {
	case err := <-gmailDone:
		if err != nil {
			t.Fatalf("Gmail collection failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Gmail collection did not finish")
	}
	select {
	case err := <-xDone:
		if err != nil {
			t.Fatalf("X collection failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("X collection did not finish after Gmail released the slot")
	}

	overlap, identities := recorder.snapshot()
	if overlap {
		t.Fatal("Gmail and X collectors overlapped")
	}
	if len(identities) != 2 {
		t.Fatalf("collector identities=%d, want 2", len(identities))
	}
	if identities[0].TaskID == identities[1].TaskID || identities[0].RunID == identities[1].RunID || identities[0].TraceID == identities[1].TraceID {
		t.Fatalf("collector identities are not distinct: %+v", identities)
	}

	owner := collectionTaskOwner(t, svc)
	for _, identity := range identities {
		task, err := owner.Get(context.Background(), identity.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != domaintask.StatusSucceeded || task.Assignee != "shiro" || task.Route != domaintask.RouteOperations {
			t.Fatalf("unexpected terminal Task=%+v", task)
		}
		run, err := owner.GetRun(context.Background(), identity.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != domaintask.RunStatusSucceeded || run.TaskID != identity.TaskID || run.Assignee != "shiro" {
			t.Fatalf("unexpected terminal Run=%+v", run)
		}
	}
}

func TestCanceledWaitingCollectionCreatesNoTaskOrCollectorCall(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	gmailStarted := make(chan struct{}, 1)
	releaseGmail := make(chan struct{})
	var xCalls atomic.Int32

	svc.WithGmailIntake(gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		gmailStarted <- struct{}{}
		select {
		case <-ctx.Done():
			return gmailapp.GmailRunReport{}, ctx.Err()
		case <-releaseGmail:
			return gmailapp.GmailRunReport{}, nil
		}
	}), "ren", time.Hour, time.Minute, false)
	svc.WithXBookmarkCollection(collectionXFunc(func(context.Context) (XBookmarkCollectionReport, error) {
		xCalls.Add(1)
		return XBookmarkCollectionReport{}, nil
	}), time.Hour, time.Minute, false)

	gmailDone := make(chan error, 1)
	go func() {
		_, err := svc.runGmailIntake(context.Background())
		gmailDone <- err
	}()
	select {
	case <-gmailStarted:
	case <-time.After(time.Second):
		t.Fatal("Gmail collector did not start")
	}

	owner := collectionTaskOwner(t, svc)
	before, err := owner.List(context.Background(), domaintask.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	xDone := make(chan struct {
		report XBookmarkCollectionReport
		err    error
	}, 1)
	go func() {
		report, err := svc.runXBookmarkCollection(canceledCtx)
		xDone <- struct {
			report XBookmarkCollectionReport
			err    error
		}{report: report, err: err}
	}()
	select {
	case result := <-xDone:
		t.Fatalf("X collection completed before cancellation: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	result := <-xDone
	xReport, xErr := result.report, result.err
	if xReport != (XBookmarkCollectionReport{}) {
		t.Fatalf("canceled X report=%+v", xReport)
	}
	if !errors.Is(xErr, context.Canceled) {
		t.Fatalf("canceled X error=%v", xErr)
	}
	if xCalls.Load() != 0 {
		t.Fatalf("canceled waiting X collector calls=%d, want 0", xCalls.Load())
	}
	after, err := owner.List(context.Background(), domaintask.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("canceled waiting X created a Task: before=%d after=%d", len(before), len(after))
	}

	close(releaseGmail)
	select {
	case err := <-gmailDone:
		if err != nil {
			t.Fatalf("Gmail collection failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Gmail collection did not finish")
	}
}

func TestFailedCollectionReleasesSharedSlotForNextCollection(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	recorder := &collectionRunRecorder{}
	firstErr := errors.New("Gmail source unavailable")
	svc.WithGmailIntake(gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		if err := recorder.enter(ctx); err != nil {
			return gmailapp.GmailRunReport{}, err
		}
		defer recorder.leave()
		return gmailapp.GmailRunReport{}, firstErr
	}), "ren", time.Hour, time.Minute, false)
	svc.WithXBookmarkCollection(collectionXFunc(func(ctx context.Context) (XBookmarkCollectionReport, error) {
		if err := recorder.enter(ctx); err != nil {
			return XBookmarkCollectionReport{}, err
		}
		defer recorder.leave()
		return XBookmarkCollectionReport{Collected: 1, Imported: 1}, nil
	}), time.Hour, time.Minute, false)

	if _, err := svc.runGmailIntake(context.Background()); !errors.Is(err, firstErr) {
		t.Fatalf("first collection error=%v", err)
	}
	if report, err := svc.runXBookmarkCollection(context.Background()); err != nil || report.Collected != 1 || report.Imported != 1 {
		t.Fatalf("next collection report=%+v err=%v", report, err)
	}

	overlap, identities := recorder.snapshot()
	if overlap || len(identities) != 2 {
		t.Fatalf("failed-first slot release identities=%d overlap=%t", len(identities), overlap)
	}
	owner := collectionTaskOwner(t, svc)
	for index, identity := range identities {
		task, err := owner.Get(context.Background(), identity.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		want := domaintask.StatusFailed
		if index == 1 {
			want = domaintask.StatusSucceeded
		}
		if task.Status != want {
			t.Fatalf("Task %s status=%s, want %s", identity.TaskID, task.Status, want)
		}
	}
}

func TestHeartbeatStopCancelsBothCollectionWaitersBeforeWaiting(t *testing.T) {
	t.Run("gmail_holder_x_waiter", func(t *testing.T) {
		svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
		gmailStarted := make(chan struct{}, 1)
		xStopped := make(chan struct{})
		var xCalls atomic.Int32

		svc.WithGmailIntake(gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
			gmailStarted <- struct{}{}
			<-ctx.Done()
			<-xStopped
			return gmailapp.GmailRunReport{}, ctx.Err()
		}), "ren", time.Hour, time.Minute, false)
		svc.WithXBookmarkCollection(collectionXFunc(func(context.Context) (XBookmarkCollectionReport, error) {
			xCalls.Add(1)
			return XBookmarkCollectionReport{}, nil
		}), time.Hour, time.Minute, false)

		if !svc.startGmailIntake() {
			t.Fatal("expected Gmail collection request to start")
		}
		select {
		case <-gmailStarted:
		case <-time.After(time.Second):
			t.Fatal("Gmail collector did not start")
		}
		if !svc.startXBookmarkCollection() {
			t.Fatal("expected X collection request to start")
		}
		go func() {
			svc.xBookmarkWG.Wait()
			close(xStopped)
		}()

		svc.Start()
		stopDone := make(chan struct{})
		go func() {
			svc.Stop()
			close(stopDone)
		}()
		select {
		case <-stopDone:
		case <-time.After(2 * time.Second):
			// Keep the test goroutine bounded even when run against the old
			// sequential shutdown implementation.
			svc.stopXBookmarkCollection()
			<-stopDone
			t.Fatal("Heartbeat Stop waited for Gmail before canceling waiting X")
		}
		if xCalls.Load() != 0 {
			t.Fatalf("waiting X collector calls=%d, want 0", xCalls.Load())
		}
		assertSingleCanceledCollectionTask(t, svc)
	})

	t.Run("x_holder_gmail_waiter", func(t *testing.T) {
		svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
		xStarted := make(chan struct{}, 1)
		gmailStopped := make(chan struct{})
		var gmailCalls atomic.Int32

		svc.WithGmailIntake(gmailIntakeFunc(func(context.Context) (gmailapp.GmailRunReport, error) {
			gmailCalls.Add(1)
			return gmailapp.GmailRunReport{}, nil
		}), "ren", time.Hour, time.Minute, false)
		svc.WithXBookmarkCollection(collectionXFunc(func(ctx context.Context) (XBookmarkCollectionReport, error) {
			xStarted <- struct{}{}
			<-ctx.Done()
			<-gmailStopped
			return XBookmarkCollectionReport{}, ctx.Err()
		}), time.Hour, time.Minute, false)

		if !svc.startXBookmarkCollection() {
			t.Fatal("expected X collection request to start")
		}
		select {
		case <-xStarted:
		case <-time.After(time.Second):
			t.Fatal("X collector did not start")
		}
		if !svc.startGmailIntake() {
			t.Fatal("expected Gmail collection request to start")
		}
		go func() {
			svc.gmail.wg.Wait()
			close(gmailStopped)
		}()

		svc.Start()
		stopDone := make(chan struct{})
		go func() {
			svc.Stop()
			close(stopDone)
		}()
		select {
		case <-stopDone:
		case <-time.After(2 * time.Second):
			svc.stopGmailIntake()
			<-stopDone
			t.Fatal("Heartbeat Stop did not cancel both collection requests")
		}
		if gmailCalls.Load() != 0 {
			t.Fatalf("waiting Gmail collector calls=%d, want 0", gmailCalls.Load())
		}
		assertSingleCanceledCollectionTask(t, svc)
	})
}

func assertSingleCanceledCollectionTask(t *testing.T, svc *HeartbeatService) {
	t.Helper()
	owner := collectionTaskOwner(t, svc)
	tasks, err := owner.List(context.Background(), domaintask.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("collection stop created unexpected Tasks: %+v", tasks)
	}
	if tasks[0].Status != domaintask.StatusCancelled || tasks[0].Assignee != "shiro" || tasks[0].Route != domaintask.RouteOperations {
		t.Fatalf("unexpected terminal collection Task=%+v", tasks[0])
	}
}

func TestCollectionWorkerRejectsAdmissionAfterStopSignalWithReleasedSlot(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	gmailStarted := make(chan struct{}, 1)
	holderCtx, cancelHolder := context.WithCancel(context.Background())
	defer cancelHolder()
	var xCalls atomic.Int32

	svc.WithGmailIntake(gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		gmailStarted <- struct{}{}
		<-ctx.Done()
		return gmailapp.GmailRunReport{}, ctx.Err()
	}), "ren", time.Hour, time.Minute, false)
	svc.WithXBookmarkCollection(collectionXFunc(func(context.Context) (XBookmarkCollectionReport, error) {
		xCalls.Add(1)
		return XBookmarkCollectionReport{}, nil
	}), time.Hour, time.Minute, false)

	holderDone := make(chan error, 1)
	go func() {
		_, err := svc.runGmailIntake(holderCtx)
		holderDone <- err
	}()
	select {
	case <-gmailStarted:
	case <-time.After(time.Second):
		t.Fatal("Gmail holder did not start")
	}

	waiterDone := make(chan struct {
		report XBookmarkCollectionReport
		err    error
	}, 1)
	go func() {
		report, err := svc.runXBookmarkCollection(context.Background())
		waiterDone <- struct {
			report XBookmarkCollectionReport
			err    error
		}{report: report, err: err}
	}()
	select {
	case result := <-waiterDone:
		t.Fatalf("X waiter completed before stop: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}

	// Closing the canonical service signal while the holder is still running
	// makes the subsequent token release and stop check race-ready. The holder
	// exits immediately when canceled; the stopped service must still reject X.
	close(svc.stopCh)
	cancelHolder()

	select {
	case err := <-holderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Gmail holder error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Gmail holder did not finish after cancellation")
	}
	select {
	case result := <-waiterDone:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("stopped X waiter error=%v, want context.Canceled", result.err)
		}
		if result.report != (XBookmarkCollectionReport{}) {
			t.Fatalf("stopped X waiter report=%+v, want zero", result.report)
		}
	case <-time.After(time.Second):
		t.Fatal("stopped X waiter did not finish")
	}
	if xCalls.Load() != 0 {
		t.Fatalf("stopped X waiter collector calls=%d, want 0", xCalls.Load())
	}
	assertSingleCanceledCollectionTask(t, svc)
}
