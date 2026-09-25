package heartbeat

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
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
	if o.consumeFailure() {
		return domaintask.Task{}, deferredWriteFailure
	}
	return o.TaskOwner.Fail(ctx, taskID, summary, nextActions)
}

func (o *flakyFinalizingTaskOwner) Succeed(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	if o.consumeFailure() {
		return domaintask.Task{}, deferredWriteFailure
	}
	return o.TaskOwner.Succeed(ctx, taskID, summary)
}

func (o *flakyFinalizingTaskOwner) Cancel(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	if o.consumeFailure() {
		return domaintask.Task{}, deferredWriteFailure
	}
	return o.TaskOwner.Cancel(ctx, taskID, summary)
}

func newDeferredFinalizationHarness(t *testing.T, worker *mockWorkerAgent, failures int) (*HeartbeatService, *taskmanager.Manager) {
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
	owner := &flakyFinalizingTaskOwner{TaskOwner: manager, failures: failures}
	return NewHeartbeatService(worker, &mockSender{}, dir, 30).WithTaskOwner(owner, "shiro"), manager
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
// 既にTaskを終端済みなら、再試行は書き込まずに除去だけ行う（冪等）。
func TestDeferredWorkerFinalizationIsDroppedWhenTaskAlreadyTerminal(t *testing.T) {
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc, manager := newDeferredFinalizationHarness(t, worker, 100)
	ctx := context.Background()

	if err := svc.tick(ctx); err == nil || !errors.Is(err, deferredWriteFailure) {
		t.Fatalf("tick did not surface the deferred finalization error: %v", err)
	}
	stuckID := worker.lastInput.RootTaskID()
	if _, err := manager.Fail(ctx, stuckID, "recovered by restart recovery", nil); err != nil {
		t.Fatal(err)
	}

	report := svc.sweepDeferredWorkerFinalizations(ctx, time.Now().UTC())
	if report.Deferred != 1 || report.Retried != 1 || report.Recovered != 1 || report.GivenUp != 0 {
		t.Fatalf("sweep report=%+v, want deferred=1 retried=1 recovered=1 given_up=0", report)
	}
	if got := svc.deferredWorkerFinalizationCount(); got != 0 {
		t.Fatalf("deferred finalization count=%d, want 0", got)
	}
	task, err := manager.Get(ctx, stuckID)
	if err != nil || task.Status != domaintask.StatusFailed || task.Summary != "recovered by restart recovery" {
		t.Fatalf("already terminal Task was rewritten: %+v err=%v", task, err)
	}
	if allowed, _, err := manager.CanStart(ctx, operationsRouteCandidate()); err != nil || !allowed {
		t.Fatalf("operations slot stayed blocked: allowed=%t err=%v", allowed, err)
	}
}

// TestDeferredWorkerFinalizationIsAbandonedAfterWindow 再試行には上限があり、
// 恒久障害でdeferred集合が無限に育たないことを検証する。
func TestDeferredWorkerFinalizationIsAbandonedAfterWindow(t *testing.T) {
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc, manager := newDeferredFinalizationHarness(t, worker, 1000)
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
