package heartbeat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type claimOrderWorker struct {
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
	events   chan string
	runID    modulecore.RunID
}

func (w *claimOrderWorker) Execute(ctx context.Context, _ conversation.TurnInput) (string, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return "", err
	}
	w.runID = identity.RunID
	w.events <- "execute-start"
	close(w.started)
	<-w.release
	w.events <- "execute-return"
	close(w.returned)
	return "accepted", nil
}

func runnerClaimTempDir(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(workingDir, "..", "..", ".."))
	tempRoot := filepath.Join(repoRoot, "Tmp", "test-runtime")
	if err := os.MkdirAll(tempRoot, 0700); err != nil {
		t.Fatalf("create repo-local test runtime: %v", err)
	}
	storeDir, err := os.MkdirTemp(tempRoot, "runner-execution-claim-")
	if err != nil {
		t.Fatalf("create repo-local Task store directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(storeDir); err != nil {
			t.Errorf("remove repo-local Task store directory: %v", err)
		}
	})
	return storeDir
}

func runnerClaimTaskOwner(t *testing.T) (*taskmanager.Manager, *taskpersistence.JSONLStore) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(filepath.Join(runnerClaimTempDir(t), "tasks"))
	if err != nil {
		t.Fatalf("create Task store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	return taskmanager.New(store, taskmanager.DefaultParallelLimits()), store
}

func TestRevision2RunnerEndsClaimAfterWorkerAndTaskFinalization(t *testing.T) {
	item := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: "claim-order-item",
		ImplementationUnit: "unit-claim-order-item", WorkstreamID: "ws-claim-order-item",
		Title: "claim ordering", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliverySpec, ImplementationRevision: 1,
	}
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{item}}
	manager, taskStore := runnerClaimTaskOwner(t)
	events := make(chan string, 8)
	worker := &claimOrderWorker{
		started: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{}), events: events,
	}
	owner := &revision2AtlasRunnerFake{
		result: appbacklog.AcquireRunnableResult{Item: item, Acquired: true}, events: events,
		endCalled: make(chan struct{}, 1),
	}
	owner.beginCheck = func(selected domainbacklog.Item) error {
		if !reflect.DeepEqual(selected, item) {
			return errors.New("BeginRunnerExecution received a different selected item")
		}
		tasks, err := taskStore.ListTasks(context.Background(), domaintask.Filter{})
		if err != nil {
			return err
		}
		runs, err := taskStore.ListRuns(context.Background(), domaintask.RunFilter{})
		if err != nil {
			return err
		}
		if len(tasks) != 0 || len(runs) != 0 {
			return errors.New("runner Task/Run started before BeginRunnerExecution")
		}
		return nil
	}
	owner.endCheck = func(token string) error {
		if token != "runner-claim-token" {
			return errors.New("EndRunnerExecution received a different claim token")
		}
		select {
		case <-worker.returned:
		default:
			return errors.New("EndRunnerExecution ran before Worker.Execute returned")
		}
		run, err := manager.GetRun(context.Background(), worker.runID)
		if err != nil {
			return err
		}
		if run.Status == domaintask.RunStatusRunning {
			return errors.New("EndRunnerExecution ran before Task finalization")
		}
		return nil
	}
	service := NewHeartbeatService(worker, &mockSender{}, runnerClaimTempDir(t), 30).
		WithBacklogStore(backlogStore).WithAtlasService(owner).WithTaskOwner(manager, "shiro")

	result := make(chan error, 1)
	go func() {
		_, err := service.RunBacklogRunner(context.Background(), time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
		result <- err
	}()
	select {
	case <-worker.started:
	case err := <-result:
		t.Fatalf("runner returned before Execute began: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Worker.Execute did not start")
	}
	select {
	case <-owner.endCalled:
		t.Fatal("runner claim ended while Worker.Execute was still blocked")
	default:
	}
	close(worker.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunBacklogRunner: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunBacklogRunner did not finish after Worker.Execute returned")
	}

	wantEvents := []string{"acquire", "begin", "execute-start", "execute-return", "end"}
	for _, want := range wantEvents {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("lifecycle event=%q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing lifecycle event %q", want)
		}
	}
	if owner.beginCalls != 1 || owner.endCalls != 1 || len(owner.endTokens) != 1 || owner.endTokens[0] != "runner-claim-token" {
		t.Fatalf("claim calls begin=%d end=%d tokens=%v", owner.beginCalls, owner.endCalls, owner.endTokens)
	}
}

func TestRevision2RunnerDoesNotCreateTaskWhenClaimBeginFails(t *testing.T) {
	item := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: "claim-begin-fail",
		ImplementationUnit: "unit-claim-begin-fail", WorkstreamID: "ws-claim-begin-fail",
		Title: "claim begin fail", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliverySpec, ImplementationRevision: 1,
	}
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{item}}
	manager, taskStore := runnerClaimTaskOwner(t)
	beginErr := errors.New("stale selected item")
	owner := &revision2AtlasRunnerFake{
		result: appbacklog.AcquireRunnableResult{Item: item, Acquired: true}, beginErr: beginErr,
	}
	worker := &mockWorkerAgent{response: "must not execute"}
	service := NewHeartbeatService(worker, &mockSender{}, runnerClaimTempDir(t), 30).
		WithBacklogStore(backlogStore).WithAtlasService(owner).WithTaskOwner(manager, "shiro")
	if _, err := service.RunBacklogRunner(context.Background(), time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)); !errors.Is(err, beginErr) {
		t.Fatalf("RunBacklogRunner begin error=%v, want stale selection error", err)
	}
	if worker.called || owner.endCalls != 0 {
		t.Fatalf("worker called=%t End calls=%d after failed BeginRunnerExecution", worker.called, owner.endCalls)
	}
	tasks, err := taskStore.ListTasks(context.Background(), domaintask.Filter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("failed claim Begin created tasks=%+v err=%v", tasks, err)
	}
}

func TestRevision2RunnerReturnsClaimEndError(t *testing.T) {
	item := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: "claim-end-fail",
		ImplementationUnit: "unit-claim-end-fail", WorkstreamID: "ws-claim-end-fail",
		Title: "claim end fail", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliverySpec, ImplementationRevision: 1,
	}
	manager, _ := runnerClaimTaskOwner(t)
	endErr := errors.New("claim release failed")
	owner := &revision2AtlasRunnerFake{
		result: appbacklog.AcquireRunnableResult{Item: item, Acquired: true}, endErr: endErr,
	}
	service := NewHeartbeatService(&mockWorkerAgent{response: "accepted"}, &mockSender{}, runnerClaimTempDir(t), 30).
		WithBacklogStore(&memoryBacklogStore{items: []domainbacklog.Item{item}}).
		WithAtlasService(owner).WithTaskOwner(manager, "shiro")
	if report, err := service.RunBacklogRunner(context.Background(), time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)); !errors.Is(err, endErr) || report.Failed != 1 {
		t.Fatalf("RunBacklogRunner end failure report=%+v err=%v, want visible claim release failure", report, err)
	}
	if owner.endCalls != 1 || len(owner.endTokens) != 1 || owner.endTokens[0] != "runner-claim-token" {
		t.Fatalf("End calls=%d tokens=%v", owner.endCalls, owner.endTokens)
	}
}

func TestRevision2RunnerEndsClaimAfterFailedWorkerFinalizationAndJoinsErrors(t *testing.T) {
	item := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: "claim-worker-failure",
		ImplementationUnit: "unit-claim-worker-failure", WorkstreamID: "ws-claim-worker-failure",
		Title: "claim worker failure", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliverySpec, ImplementationRevision: 1,
	}
	manager, _ := runnerClaimTaskOwner(t)
	workerErr := errors.New("worker execution failed")
	endErr := errors.New("claim release failed after worker failure")
	var runID modulecore.RunID
	var identityErr error
	worker := &mockWorkerAgent{
		response: "failed", err: workerErr,
		onExecute: func(ctx context.Context, _ conversation.TurnInput) {
			identity, err := domainexecution.IdentityFromContext(ctx)
			identityErr = err
			if err == nil {
				runID = identity.RunID
			}
		},
	}
	owner := &revision2AtlasRunnerFake{
		result: appbacklog.AcquireRunnableResult{Item: item, Acquired: true}, endErr: endErr,
	}
	owner.endCheck = func(token string) error {
		if token != "runner-claim-token" {
			return errors.New("EndRunnerExecution received a different claim token")
		}
		if identityErr != nil {
			return identityErr
		}
		run, err := manager.GetRun(context.Background(), runID)
		if err != nil {
			return err
		}
		if run.Status != domaintask.RunStatusFailed {
			return errors.New("EndRunnerExecution ran before failed Task finalization")
		}
		return nil
	}
	service := NewHeartbeatService(worker, &mockSender{}, runnerClaimTempDir(t), 30).
		WithBacklogStore(&memoryBacklogStore{items: []domainbacklog.Item{item}}).
		WithAtlasService(owner).WithTaskOwner(manager, "shiro")

	report, err := service.RunBacklogRunner(context.Background(), time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	if !errors.Is(err, workerErr) || !errors.Is(err, endErr) {
		t.Fatalf("RunBacklogRunner worker/end errors=%v, want both worker and End failures", err)
	}
	if report.Failed != 2 || owner.endCalls != 1 || len(owner.endTokens) != 1 || owner.endTokens[0] != "runner-claim-token" {
		t.Fatalf("RunBacklogRunner report=%+v End calls=%d tokens=%v, want both failures and one token-checked End", report, owner.endCalls, owner.endTokens)
	}
}
