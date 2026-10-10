package heartbeat

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// deferredWriteFailure はTask ownerの終端書き込みがストアに拒否されたことを
// 表す有界エラー。本番のJSONLストア書き込み失敗と同じ形だけを再現する。
var deferredWriteFailure = errors.New("task store rejected terminal write")

// flakyFinalizingTaskOwner は終端書き込みを指定回数だけ失敗させるTask owner。
// Task生成・Run開始・読み取りは本番の taskmanager.Manager に委譲するので、
// 並列上限の判定は本番と同一の条件で評価される。
type flakyFinalizingTaskOwner struct {
	TaskOwner
	mu       sync.Mutex
	failures int
	// reader は読み取り境界。Heartbeatが終端書き込みの前にTaskを読みに来ない
	// (全走査を余計に1回払わない) ことを gets で検証する。
	reader interface {
		Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	}
	gets    int
	budgets []time.Duration
}

func (o *flakyFinalizingTaskOwner) Get(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	o.mu.Lock()
	o.gets++
	o.mu.Unlock()
	return o.reader.Get(ctx, taskID)
}

// observeWrite は終端書き込みに渡された予算 (ctxの残り時間) を記録する。
func (o *flakyFinalizingTaskOwner) observeWrite(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		o.budgets = append(o.budgets, time.Until(deadline))
	} else {
		o.budgets = append(o.budgets, -1)
	}
}

func (o *flakyFinalizingTaskOwner) observed() (gets int, budgets []time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.gets, append([]time.Duration(nil), o.budgets...)
}

func (o *flakyFinalizingTaskOwner) consumeFailure() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failures <= 0 {
		return false
	}
	o.failures--
	return true
}

func (o *flakyFinalizingTaskOwner) Fail(ctx context.Context, taskID modulecore.TaskID, summary string, nextActions []string) (domaintask.Task, error) {
	o.observeWrite(ctx)
	if o.consumeFailure() {
		return domaintask.Task{}, deferredWriteFailure
	}
	return o.TaskOwner.Fail(ctx, taskID, summary, nextActions)
}

func (o *flakyFinalizingTaskOwner) Succeed(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	o.observeWrite(ctx)
	if o.consumeFailure() {
		return domaintask.Task{}, deferredWriteFailure
	}
	return o.TaskOwner.Succeed(ctx, taskID, summary)
}

func (o *flakyFinalizingTaskOwner) Cancel(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	o.observeWrite(ctx)
	if o.consumeFailure() {
		return domaintask.Task{}, deferredWriteFailure
	}
	return o.TaskOwner.Cancel(ctx, taskID, summary)
}

func newDeferredFinalizationHarness(t *testing.T, worker *mockWorkerAgent, failures int) (*HeartbeatService, *taskmanager.Manager) {
	t.Helper()
	svc, manager, _ := newObservedDeferredFinalizationHarness(t, worker, failures)
	return svc, manager
}

func newObservedDeferredFinalizationHarness(t *testing.T, worker *mockWorkerAgent, failures int) (*HeartbeatService, *taskmanager.Manager, *flakyFinalizingTaskOwner) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("check status"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := taskpersistence.NewJSONLStore(filepath.Join(dir, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close heartbeat task store: %v", err)
		}
	})
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	owner := &flakyFinalizingTaskOwner{TaskOwner: manager, reader: manager, failures: failures}
	return NewHeartbeatService(worker, &mockSender{}, dir, 30).WithTaskOwner(owner, "shiro"), manager, owner
}

// makeDeferredFinalizationsDue は滞留中の全entryの再試行時刻を過去にする。
// backoffの待ち時間そのものを検証しないテストが、実時間を待たずに再試行させる。
func makeDeferredFinalizationsDue(svc *HeartbeatService) {
	svc.finalizationMu.Lock()
	defer svc.finalizationMu.Unlock()
	for _, entry := range svc.deferredFinalizations {
		entry.nextAttempt = time.Time{}
	}
}

func operationsRouteCandidate() domaintask.Task {
	now := time.Now().UTC()
	return domaintask.Task{
		TaskID:          modulecore.NewTaskID(),
		Title:           "Atlas backlog runner",
		Route:           domaintask.RouteOperations,
		Assignee:        "shiro",
		Status:          domaintask.StatusQueued,
		Priority:        domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func assertOperationsSlotDenied(t *testing.T, manager *taskmanager.Manager, taskID modulecore.TaskID) {
	t.Helper()
	allowed, reason, err := manager.CanStart(context.Background(), operationsRouteCandidate())
	if err != nil {
		t.Fatalf("CanStart returned an error: %v", err)
	}
	if allowed || reason != "operations task limit reached" {
		t.Fatalf("deferred Task %s did not deny the operations slot: allowed=%t reason=%q", taskID, allowed, reason)
	}
}

func captureHeartbeatLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := log.Writer()
	log.SetOutput(buffer)
	t.Cleanup(func() { log.SetOutput(previous) })
	return buffer
}

// TestHeartbeatTickRetriesDeferredWorkerFinalization 終端書き込みが失敗して
// running のまま残ったHeartbeat Taskを、次のtickが新しいTaskを開始する前に
// 終端させ、operations枠を返すことを検証する。
func TestHeartbeatTickRetriesDeferredWorkerFinalization(t *testing.T) {
	logs := captureHeartbeatLog(t)
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK", err: errors.New("worker crashed")}
	svc, manager := newDeferredFinalizationHarness(t, worker, 1)
	ctx := context.Background()

	if err := svc.tick(ctx); err == nil || !errors.Is(err, deferredWriteFailure) {
		t.Fatalf("tick did not surface the deferred finalization error: %v", err)
	}
	stuckID := worker.lastInput.RootTaskID()
	if stuckID == modulecore.TaskID("") {
		t.Fatal("worker task id is missing")
	}
	task, err := manager.Get(ctx, stuckID)
	if err != nil || task.Status != domaintask.StatusRunning {
		t.Fatalf("Task stayed %q instead of running for retry: %+v err=%v", task.Status, task, err)
	}
	if got := svc.deferredWorkerFinalizationCount(); got != 1 {
		t.Fatalf("deferred finalization count=%d, want 1", got)
	}
	assertOperationsSlotDenied(t, manager, stuckID)
	transcript := logs.String()
	if !strings.Contains(transcript, "worker finalization deferred") || !strings.Contains(transcript, string(stuckID)) {
		t.Fatalf("deferred finalization was not logged with its task id:\n%s", transcript)
	}

	worker.err = nil
	makeDeferredFinalizationsDue(svc)
	if err := svc.tick(ctx); err != nil {
		t.Fatalf("tick after recovery returned %v", err)
	}
	task, err = manager.Get(ctx, stuckID)
	if err != nil || task.Status != domaintask.StatusFailed {
		t.Fatalf("deferred Task=%+v err=%v, want failed", task, err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: stuckID})
	if err != nil || len(runs) != 1 || runs[0].Status == domaintask.RunStatusRunning {
		t.Fatalf("deferred Run stayed active: %+v err=%v", runs, err)
	}
	if got := svc.deferredWorkerFinalizationCount(); got != 0 {
		t.Fatalf("deferred finalization count=%d after recovery, want 0", got)
	}
	if allowed, reason, err := manager.CanStart(ctx, operationsRouteCandidate()); err != nil || !allowed {
		t.Fatalf("operations slot stayed blocked after recovery: allowed=%t reason=%q err=%v", allowed, reason, err)
	}
}

// TestDeferredWorkerFinalizationIsDroppedWhenTaskAlreadyTerminal 別の回収経路が
// 既にTaskを終端済みなら、終端書き込みの拒否 (終端状態からの無効遷移) を
// 「決着済み」と解釈して台帳から除くだけで、Taskは書き換えない（冪等）。
func TestDeferredWorkerFinalizationIsDroppedWhenTaskAlreadyTerminal(t *testing.T) {
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc, manager, owner := newObservedDeferredFinalizationHarness(t, worker, 1)
	ctx := context.Background()

	if err := svc.tick(ctx); err == nil || !errors.Is(err, deferredWriteFailure) {
		t.Fatalf("tick did not surface the deferred finalization error: %v", err)
	}
	stuckID := worker.lastInput.RootTaskID()
	if _, err := manager.Fail(ctx, stuckID, "recovered by restart recovery", nil); err != nil {
		t.Fatal(err)
	}

	makeDeferredFinalizationsDue(svc)
	report := svc.sweepDeferredWorkerFinalizations(ctx, time.Now().UTC())
	if report.Deferred != 1 || report.Retried != 1 || report.Recovered != 1 || report.GivenUp != 0 {
		t.Fatalf("sweep report=%+v, want deferred=1 retried=1 recovered=1 given_up=0", report)
	}
	if got := svc.deferredWorkerFinalizationCount(); got != 0 {
		t.Fatalf("deferred finalization count=%d, want 0", got)
	}
	// 決着済みの判定は終端書き込みの拒否だけで行う。事前のGetは store の全走査を
	// もう1回払うため、遅いstoreで再試行を構造的に不利にする。
	if gets, _ := owner.observed(); gets != 0 {
		t.Fatalf("sweep read the Task %d time(s) before writing, want 0", gets)
	}
	task, err := manager.Get(ctx, stuckID)
	if err != nil || task.Status != domaintask.StatusFailed || task.Summary != "recovered by restart recovery" {
		t.Fatalf("already terminal Task was rewritten: %+v err=%v", task, err)
	}
	if allowed, _, err := manager.CanStart(ctx, operationsRouteCandidate()); err != nil || !allowed {
		t.Fatalf("operations slot stayed blocked: allowed=%t err=%v", allowed, err)
	}
}

// TestDeferredWorkerFinalizationIsAbandonedOnlyAfterLongWindow 再試行は有界で、
// 恒久障害でdeferred集合が無限に育たないことを検証する。放棄は枠を再起動まで
// 握り続ける最後の手段なので、長い窓を超えた場合だけで、観測可能に記録する。
func TestDeferredWorkerFinalizationIsAbandonedOnlyAfterLongWindow(t *testing.T) {
	logs := captureHeartbeatLog(t)
	listener := &recordingEventListener{}
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc, manager := newDeferredFinalizationHarness(t, worker, 1000)
	svc.WithEventListener(listener)
	ctx := context.Background()

	if err := svc.tick(ctx); err == nil || !errors.Is(err, deferredWriteFailure) {
		t.Fatalf("tick did not surface the deferred finalization error: %v", err)
	}
	stuckID := worker.lastInput.RootTaskID()

	now := time.Now().UTC().Add(heartbeatFinalizationWindow + time.Minute)
	report := svc.sweepDeferredWorkerFinalizations(ctx, now)
	if report.Deferred != 1 || report.Retried != 1 || report.Recovered != 0 || report.GivenUp != 1 {
		t.Fatalf("sweep report=%+v, want deferred=1 retried=1 recovered=0 given_up=1", report)
	}
	if report.LastError == "" || !strings.Contains(report.LastError, "task store rejected terminal write") {
		t.Fatalf("sweep report lost the sanitized last error: %+v", report)
	}
	if got := svc.deferredWorkerFinalizationCount(); got != 0 {
		t.Fatalf("deferred finalization count=%d after give-up, want 0", got)
	}
	task, err := manager.Get(ctx, stuckID)
	if err != nil || task.Status != domaintask.StatusRunning {
		t.Fatalf("abandoned Task=%+v err=%v, want the running state to stay observable", task, err)
	}
	if later := svc.sweepDeferredWorkerFinalizations(ctx, now.Add(time.Hour)); later.Deferred != 0 || later.Retried != 0 {
		t.Fatalf("later sweep re-opened an abandoned Task: %+v", later)
	}
	if !strings.Contains(logs.String(), "worker finalization abandoned") {
		t.Fatalf("abandonment was not logged:\n%s", logs.String())
	}
	var abandonEvents int
	for _, event := range listener.events {
		if event.Type == "heartbeat.worker_finalization.abandoned" {
			abandonEvents++
			if !strings.Contains(event.Content, string(stuckID)) {
				t.Fatalf("abandon event lost the task id: %q", event.Content)
			}
		}
	}
	if abandonEvents != 1 {
		t.Fatalf("abandon events=%d, want 1", abandonEvents)
	}
}

// TestWorkerFinalizationErrorIsSanitized 終端書き込みのエラーは1行・有界で
// ないとログに出さない。
func TestWorkerFinalizationErrorIsSanitized(t *testing.T) {
	if got := sanitizeWorkerFinalizationError(nil); got != "none" {
		t.Fatalf("nil error sanitized to %q", got)
	}
	multiline := errors.New("write failed\n/home/nyukimi/.rencrow/workspace/tasks/task_state.jsonl: line 9\n\tretry later")
	got := sanitizeWorkerFinalizationError(multiline)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("sanitized error kept newlines or tabs: %q", got)
	}
	if !strings.Contains(got, "write failed") {
		t.Fatalf("sanitized error dropped the cause: %q", got)
	}
	long := errors.New(strings.Repeat("詳細", 400))
	if bounded := sanitizeWorkerFinalizationError(long); len([]rune(bounded)) > maxWorkerFinalizationErrorRunes+1 {
		t.Fatalf("sanitized error is unbounded: %d runes", len([]rune(bounded)))
	}
}

// scriptedTaskOwner は終端書き込みの振る舞いをテストごとに差し替えられるTask owner。
// 遅いstore (全走査とロック待ち) や即時拒否を、実時間をほとんど使わずに再現する。
type scriptedTaskOwner struct {
	write func(ctx context.Context) error
	mu    sync.Mutex
	calls []scriptedWrite
}

type scriptedWrite struct {
	elapsed time.Duration
	budget  time.Duration
	err     error
}

func (o *scriptedTaskOwner) terminal(ctx context.Context) (domaintask.Task, error) {
	start := time.Now()
	budget := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	err := o.write(ctx)
	o.mu.Lock()
	o.calls = append(o.calls, scriptedWrite{elapsed: time.Since(start), budget: budget, err: err})
	o.mu.Unlock()
	return domaintask.Task{}, err
}

func (o *scriptedTaskOwner) recorded() []scriptedWrite {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]scriptedWrite(nil), o.calls...)
}

func (o *scriptedTaskOwner) Create(context.Context, domaintask.Task, domaintask.SharedRoleContext) (domaintask.Task, error) {
	return domaintask.Task{}, errors.New("scripted owner does not create tasks")
}

func (o *scriptedTaskOwner) StartRunWithReason(context.Context, modulecore.TaskID, domaintask.RunStartReason) (domaintask.Run, error) {
	return domaintask.Run{}, errors.New("scripted owner does not start runs")
}

func (o *scriptedTaskOwner) Succeed(ctx context.Context, _ modulecore.TaskID, _ string) (domaintask.Task, error) {
	return o.terminal(ctx)
}

func (o *scriptedTaskOwner) Fail(ctx context.Context, _ modulecore.TaskID, _ string, _ []string) (domaintask.Task, error) {
	return o.terminal(ctx)
}

func (o *scriptedTaskOwner) Cancel(ctx context.Context, _ modulecore.TaskID, _ string) (domaintask.Task, error) {
	return o.terminal(ctx)
}

func (o *scriptedTaskOwner) Get(context.Context, modulecore.TaskID) (domaintask.Task, error) {
	return domaintask.Task{Status: domaintask.StatusRunning}, nil
}

// blockUntilContextEnds は store が詰まって書き込みが完了しない状態を模す。
func blockUntilContextEnds(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func rejectImmediately(context.Context) error { return deferredWriteFailure }

func newScriptedFinalizationService(t *testing.T, write func(context.Context) error) (*HeartbeatService, *scriptedTaskOwner) {
	t.Helper()
	owner := &scriptedTaskOwner{write: write}
	svc := NewHeartbeatService(&mockWorkerAgent{}, &mockSender{}, t.TempDir(), 30).WithTaskOwner(owner, "shiro")
	return svc, owner
}

// seedDeferredFinalizations は終端書き込みに失敗したTaskをn件、再試行可能な状態で登録する。
func seedDeferredFinalizations(svc *HeartbeatService, n int) []modulecore.TaskID {
	ids := make([]modulecore.TaskID, 0, n)
	for i := 0; i < n; i++ {
		id := modulecore.NewTaskID()
		svc.recordDeferredWorkerFinalization(id, nil, deferredWriteFailure)
		ids = append(ids, id)
		// firstSeenの順序をsweepの再試行順に反映させる。
		time.Sleep(time.Millisecond)
	}
	makeDeferredFinalizationsDue(svc)
	return ids
}

func deferredAttempts(svc *HeartbeatService, id modulecore.TaskID) (attempts int, found bool) {
	svc.finalizationMu.Lock()
	defer svc.finalizationMu.Unlock()
	entry, ok := svc.deferredFinalizations[id]
	if !ok {
		return 0, false
	}
	return entry.attempts, true
}

// TestTaskOwnerBoundaryHasNoPreReadGet は、終端再試行がTaskの事前読み取りに
// 依存しない構造を型で強制する。事前のGetは store の全走査をもう1回払う。
func TestTaskOwnerBoundaryHasNoPreReadGet(t *testing.T) {
	if _, found := reflect.TypeOf((*TaskOwner)(nil)).Elem().MethodByName("Get"); found {
		t.Fatal("TaskOwner still exposes Get: terminal retries must decide from the write result alone")
	}
}

// TestFinalizationWritesGetBudgetBeyondObservedStoreLatency は、終端書き込み1回に
// 渡す予算が本番で観測した store 1トランザクションの遅延に十分な余裕を持つことを
// 検証する。初回 (finishWorker) と再試行 (sweep) の両方が対象。
func TestFinalizationWritesGetBudgetBeyondObservedStoreLatency(t *testing.T) {
	// 本番 (2026-10) の1トランザクションは約11秒。ロック待ちを含めて3倍を最低条件にする。
	const observedStoreLatency = 11 * time.Second
	minimum := 3 * observedStoreLatency

	worker := &mockWorkerAgent{response: "HEARTBEAT_OK", err: errors.New("worker crashed")}
	svc, _, owner := newObservedDeferredFinalizationHarness(t, worker, 1)
	ctx := context.Background()

	if err := svc.tick(ctx); err == nil || !errors.Is(err, deferredWriteFailure) {
		t.Fatalf("tick did not surface the deferred finalization error: %v", err)
	}
	makeDeferredFinalizationsDue(svc)
	svc.sweepDeferredWorkerFinalizations(ctx, time.Now().UTC())

	_, budgets := owner.observed()
	if len(budgets) != 2 {
		t.Fatalf("terminal writes=%d, want 2 (finishWorker and one sweep retry): %v", len(budgets), budgets)
	}
	for i, budget := range budgets {
		if budget < minimum {
			t.Errorf("terminal write #%d got a %s budget, want at least %s", i+1, budget, minimum)
		}
	}
}

// TestSweepGivesEveryEntryItsOwnFinalizationBudget は、先のentryが予算を使い切っても
// 次のentryが期限切れのcontextで始まらないことを検証する。共有予算では、
// 1走査が予算の半分を超えた時点で再試行は構造的に成立しなくなる。
func TestSweepGivesEveryEntryItsOwnFinalizationBudget(t *testing.T) {
	svc, owner := newScriptedFinalizationService(t, blockUntilContextEnds)
	svc.finalizeTimeout = 80 * time.Millisecond
	seedDeferredFinalizations(svc, 2)

	done := make(chan workerFinalizationSweepReport, 1)
	go func() {
		done <- svc.sweepDeferredWorkerFinalizations(context.Background(), time.Now().UTC().Add(time.Hour))
	}()
	var report workerFinalizationSweepReport
	select {
	case report = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweep did not finish within 5s: entries share one budget or ignore the per-write timeout")
	}
	calls := owner.recorded()
	if report.Retried != 2 || len(calls) != 2 {
		t.Fatalf("report=%+v calls=%d, want 2 retries", report, len(calls))
	}
	for i, call := range calls {
		if !errors.Is(call.err, context.DeadlineExceeded) {
			t.Errorf("write #%d err=%v, want deadline exceeded", i+1, call.err)
		}
		if call.elapsed < 60*time.Millisecond {
			t.Errorf("write #%d ran for %s: it did not get its own full budget", i+1, call.elapsed)
		}
	}
}

// TestSweepBoundsAttemptsPerSweepAndKeepsTheRestForLaterTicks は、1回のsweepが
// tickを塞ぐ時間を上限付きにし、残りのentryを失わず次回に回すことを検証する。
func TestSweepBoundsAttemptsPerSweepAndKeepsTheRestForLaterTicks(t *testing.T) {
	svc, owner := newScriptedFinalizationService(t, rejectImmediately)
	ids := seedDeferredFinalizations(svc, maxFinalizationAttemptsPerSweep+1)
	now := time.Now().UTC().Add(time.Hour)

	first := svc.sweepDeferredWorkerFinalizations(context.Background(), now)
	if first.Deferred != len(ids) || first.Retried != maxFinalizationAttemptsPerSweep {
		t.Fatalf("first sweep=%+v, want deferred=%d retried=%d", first, len(ids), maxFinalizationAttemptsPerSweep)
	}
	second := svc.sweepDeferredWorkerFinalizations(context.Background(), now)
	if second.Retried != 1 {
		t.Fatalf("second sweep=%+v, want it to pick up only the entry the first sweep skipped", second)
	}
	if got := len(owner.recorded()); got != len(ids) {
		t.Fatalf("writes=%d, want exactly one per entry (%d)", got, len(ids))
	}
	if got := svc.deferredWorkerFinalizationCount(); got != len(ids) {
		t.Fatalf("deferred=%d, want all %d entries kept", got, len(ids))
	}
}

// TestSweepStopsPromptlyWhenServiceStops は、停止要求が来たら進行中の再試行を打ち切り、
// 失敗として数えず台帳を変えずに戻ることを検証する (残りは再起動時の孤児回収に委ねる)。
func TestSweepStopsPromptlyWhenServiceStops(t *testing.T) {
	t.Run("stop requested before sweep", func(t *testing.T) {
		svc, owner := newScriptedFinalizationService(t, rejectImmediately)
		ids := seedDeferredFinalizations(svc, 1)
		close(svc.stopCh)

		report := svc.sweepDeferredWorkerFinalizations(context.Background(), time.Now().UTC().Add(time.Hour))
		if report.Retried != 0 || len(owner.recorded()) != 0 {
			t.Fatalf("sweep wrote after stop: report=%+v writes=%d", report, len(owner.recorded()))
		}
		if attempts, found := deferredAttempts(svc, ids[0]); !found || attempts != 1 {
			t.Fatalf("entry changed by a stopped sweep: found=%t attempts=%d", found, attempts)
		}
	})

	t.Run("stop requested during a blocked write", func(t *testing.T) {
		svc, _ := newScriptedFinalizationService(t, blockUntilContextEnds)
		svc.finalizeTimeout = time.Minute
		ids := seedDeferredFinalizations(svc, 1)
		go func() {
			time.Sleep(50 * time.Millisecond)
			close(svc.stopCh)
		}()

		done := make(chan workerFinalizationSweepReport, 1)
		go func() {
			done <- svc.sweepDeferredWorkerFinalizations(context.Background(), time.Now().UTC().Add(time.Hour))
		}()
		select {
		case report := <-done:
			if report.GivenUp != 0 || report.Recovered != 0 {
				t.Fatalf("interrupted sweep changed outcomes: %+v", report)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("sweep kept blocking after the service was stopped")
		}
		if attempts, found := deferredAttempts(svc, ids[0]); !found || attempts != 1 {
			t.Fatalf("shutdown counted as a failed attempt: found=%t attempts=%d", found, attempts)
		}
	})
}

// TestFinalizationBackoffGrowsExponentiallyAndIsCapped は、再試行間隔が指数的に伸び、
// 上限で止まる (storeを叩き続けず、桁あふれもしない) ことを検証する。
func TestFinalizationBackoffGrowsExponentiallyAndIsCapped(t *testing.T) {
	want := map[int]time.Duration{
		0: time.Minute, 1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 4: 8 * time.Minute,
		5: heartbeatFinalizationBackoffMax, 6: heartbeatFinalizationBackoffMax,
		64: heartbeatFinalizationBackoffMax, 1 << 20: heartbeatFinalizationBackoffMax,
	}
	for attempts, expected := range want {
		if got := finalizationBackoff(attempts); got != expected {
			t.Errorf("finalizationBackoff(%d)=%s, want %s", attempts, got, expected)
		}
	}
	if heartbeatFinalizationBackoffMax != 15*time.Minute {
		t.Errorf("backoff cap=%s, want 15m", heartbeatFinalizationBackoffMax)
	}
}

// TestDeferredFinalizationBacksOffButNeverGivesUpAtTheOldThirtyMinuteMark は、
// 再試行がbackoffで間引かれつつ継続し、旧窓 (30分) では放棄されず、storeが
// 回復した時点で枠を返すことを実storeのTask ownerで検証する。
func TestDeferredFinalizationBacksOffButNeverGivesUpAtTheOldThirtyMinuteMark(t *testing.T) {
	logs := captureHeartbeatLog(t)
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK", err: errors.New("worker crashed")}
	// tickの初回書き込みと、2回の再試行が失敗し、その後storeが回復する。
	svc, manager, owner := newObservedDeferredFinalizationHarness(t, worker, 3)
	ctx := context.Background()
	base := time.Now().UTC()

	if err := svc.tick(ctx); err == nil || !errors.Is(err, deferredWriteFailure) {
		t.Fatalf("tick did not surface the deferred finalization error: %v", err)
	}
	stuckID := worker.lastInput.RootTaskID()
	svc.finalizationMu.Lock()
	scheduled := svc.deferredFinalizations[stuckID].nextAttempt
	svc.finalizationMu.Unlock()
	if !scheduled.After(base) {
		t.Fatalf("the first failure scheduled no backoff: next=%s base=%s", scheduled, base)
	}

	steps := []struct {
		name        string
		at          time.Duration
		wantRetried int
		wantNotDue  int
	}{
		{"inside the first backoff", time.Second, 0, 1},
		{"first retry becomes due", 2 * time.Minute, 1, 0},
		{"inside the doubled backoff", 2*time.Minute + 30*time.Second, 0, 1},
		{"still retrying past the old 30 minute window", 31 * time.Minute, 1, 0},
	}
	for _, step := range steps {
		report := svc.sweepDeferredWorkerFinalizations(ctx, base.Add(step.at))
		if report.Retried != step.wantRetried || report.NotDue != step.wantNotDue || report.GivenUp != 0 || report.Recovered != 0 {
			t.Fatalf("%s: sweep=%+v, want retried=%d not_due=%d given_up=0 recovered=0", step.name, report, step.wantRetried, step.wantNotDue)
		}
		if got := svc.deferredWorkerFinalizationCount(); got != 1 {
			t.Fatalf("%s: deferred=%d, want the entry kept", step.name, got)
		}
	}
	if !strings.Contains(logs.String(), "not_due=1") {
		t.Fatalf("a sweep that retried nothing hid the pending finalization:\n%s", logs.String())
	}

	report := svc.sweepDeferredWorkerFinalizations(ctx, base.Add(2*time.Hour))
	if report.Retried != 1 || report.Recovered != 1 || report.GivenUp != 0 {
		t.Fatalf("recovery sweep=%+v, want retried=1 recovered=1", report)
	}
	task, err := manager.Get(ctx, stuckID)
	if err != nil || task.Status != domaintask.StatusFailed {
		t.Fatalf("recovered Task=%+v err=%v, want failed", task, err)
	}
	if allowed, reason, err := manager.CanStart(ctx, operationsRouteCandidate()); err != nil || !allowed {
		t.Fatalf("operations slot stayed blocked after recovery: allowed=%t reason=%q err=%v", allowed, reason, err)
	}
	if gets, _ := owner.observed(); gets != 0 {
		t.Fatalf("retries read the Task %d time(s) before writing, want 0", gets)
	}
}

// TestDeferredFinalizationRetryAfterWriteLandedIsIdempotent は、書き込みが保存済みなのに
// 失敗として返った (期限切れ等) 場合でも、再試行が同じ終端を書き直して台帳を閉じる
// ことを検証する。
func TestDeferredFinalizationRetryAfterWriteLandedIsIdempotent(t *testing.T) {
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc, manager, owner := newObservedDeferredFinalizationHarness(t, worker, 0)
	ctx := context.Background()
	landed := &landedButReportedFailedOwner{TaskOwner: owner}
	svc.WithTaskOwner(landed, "shiro")

	if err := svc.tick(ctx); err == nil || !errors.Is(err, deferredWriteFailure) {
		t.Fatalf("tick did not surface the reported failure: %v", err)
	}
	stuckID := worker.lastInput.RootTaskID()
	if task, err := manager.Get(ctx, stuckID); err != nil || task.Status != domaintask.StatusSucceeded {
		t.Fatalf("the write did not land: %+v err=%v", task, err)
	}
	makeDeferredFinalizationsDue(svc)
	report := svc.sweepDeferredWorkerFinalizations(ctx, time.Now().UTC())
	if report.Retried != 1 || report.Recovered != 1 {
		t.Fatalf("sweep=%+v, want the retry to close the ledger", report)
	}
	if got := svc.deferredWorkerFinalizationCount(); got != 0 {
		t.Fatalf("deferred=%d, want 0", got)
	}
}

// landedButReportedFailedOwner は最初の終端書き込みだけ「保存したがエラーを返した」
// 状態を模す (期限切れが保存後に報告された場合に相当)。
type landedButReportedFailedOwner struct {
	TaskOwner
	once sync.Once
}

func (o *landedButReportedFailedOwner) Succeed(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	task, err := o.TaskOwner.Succeed(ctx, taskID, summary)
	if err != nil {
		return task, err
	}
	reported := false
	o.once.Do(func() { reported = true })
	if reported {
		return domaintask.Task{}, deferredWriteFailure
	}
	return task, nil
}

// TestDeferredFinalizationKeepsRetryingWhenTransitionIsInvalidFromNonTerminalState は、
// 非終端状態 (待機中) からの無効遷移を「決着済み」と誤認せず、原因が分かる形で
// 再試行対象に残すことを検証する。
func TestDeferredFinalizationKeepsRetryingWhenTransitionIsInvalidFromNonTerminalState(t *testing.T) {
	logs := captureHeartbeatLog(t)
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc, manager, _ := newObservedDeferredFinalizationHarness(t, worker, 1)
	ctx := context.Background()

	if err := svc.tick(ctx); err == nil || !errors.Is(err, deferredWriteFailure) {
		t.Fatalf("tick did not surface the deferred finalization error: %v", err)
	}
	stuckID := worker.lastInput.RootTaskID()
	if _, err := manager.Wait(ctx, stuckID, "external review"); err != nil {
		t.Fatal(err)
	}

	makeDeferredFinalizationsDue(svc)
	report := svc.sweepDeferredWorkerFinalizations(ctx, time.Now().UTC())
	if report.Retried != 1 || report.Recovered != 0 || report.GivenUp != 0 {
		t.Fatalf("sweep=%+v, want a retained failure", report)
	}
	if got := svc.deferredWorkerFinalizationCount(); got != 1 {
		t.Fatalf("deferred=%d, want the waiting Task to stay tracked", got)
	}
	if !strings.Contains(logs.String(), "invalid status transition: waiting -> succeeded") {
		t.Fatalf("the rejection reason was not logged:\n%s", logs.String())
	}
}

// TestSlowFinalizationWriteIsLoggedBeforeItExhaustsTheBudget は、予算の半分を超えた
// 終端書き込みを、失敗する前に1行で観測できることを検証する。storeの遅延は日々増える。
func TestSlowFinalizationWriteIsLoggedBeforeItExhaustsTheBudget(t *testing.T) {
	logs := captureHeartbeatLog(t)
	svc, _ := newScriptedFinalizationService(t, func(context.Context) error {
		time.Sleep(60 * time.Millisecond)
		return nil
	})
	svc.finalizeTimeout = 100 * time.Millisecond
	ids := seedDeferredFinalizations(svc, 1)

	report := svc.sweepDeferredWorkerFinalizations(context.Background(), time.Now().UTC().Add(time.Hour))
	if report.Recovered != 1 {
		t.Fatalf("sweep=%+v, want the slow write to count as recovered", report)
	}
	transcript := logs.String()
	if !strings.Contains(transcript, "worker finalization slow") || !strings.Contains(transcript, string(ids[0])) {
		t.Fatalf("a write that used over half of its budget was not logged:\n%s", transcript)
	}
}
