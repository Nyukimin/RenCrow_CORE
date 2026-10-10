package storagehost

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskGroupTestToken = "storagehost-task-test-token"

type taskGroupFixture struct {
	host   *Handler
	server *httptest.Server
	owner  *taskpersistence.JSONLStore
	store  *TaskStoreClient
	root   string
}

type taskGroupA4ReadOwner struct {
	TaskGroupOwner

	slowTaskID       modulecore.TaskID
	slowReadStarted  chan struct{}
	allowSlowRead    chan struct{}
	ownerViewEnded   chan struct{}
	allowOwnerReturn chan struct{}
}

func (owner *taskGroupA4ReadOwner) ReadTransaction(ctx context.Context, fn func(domaintask.Store) error) error {
	err := owner.TaskGroupOwner.ReadTransaction(ctx, func(view domaintask.Store) error {
		return fn(&taskGroupA4ReadStore{Store: view, owner: owner})
	})
	close(owner.ownerViewEnded)
	<-owner.allowOwnerReturn
	return err
}

type taskGroupA4ReadStore struct {
	domaintask.Store
	owner *taskGroupA4ReadOwner
}

func (store *taskGroupA4ReadStore) GetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	value, err := store.Store.GetTask(ctx, taskID)
	if taskID == store.owner.slowTaskID {
		close(store.owner.slowReadStarted)
		<-store.owner.allowSlowRead
	}
	return value, err
}

type taskGroupSanitizedErrorReadOwner struct {
	TaskGroupOwner
	ownerErr error
}

func (owner *taskGroupSanitizedErrorReadOwner) ReadTransaction(ctx context.Context, fn func(domaintask.Store) error) error {
	return owner.TaskGroupOwner.ReadTransaction(ctx, func(view domaintask.Store) error {
		return fn(&taskGroupSanitizedErrorReadStore{Store: view, ownerErr: owner.ownerErr})
	})
}

type taskGroupSanitizedErrorReadStore struct {
	domaintask.Store
	ownerErr error
}

func (store *taskGroupSanitizedErrorReadStore) GetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if store.ownerErr != nil {
		return domaintask.Task{}, store.ownerErr
	}
	return store.Store.GetTask(ctx, taskID)
}

type taskGroupFaultTransport struct {
	mu        sync.Mutex
	next      http.RoundTripper
	dropOp    string
	dropped   bool
	captured  Request
	afterDrop func(Request)
}

func (transport *taskGroupFaultTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(body))
	var wire Request
	_ = json.Unmarshal(body, &wire)
	response, err := transport.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	shouldDrop := wire.Group == GroupTask && wire.Op == transport.dropOp && !transport.dropped
	if shouldDrop {
		transport.dropped = true
		transport.captured = wire
	}
	afterDrop := transport.afterDrop
	transport.mu.Unlock()
	if !shouldDrop {
		return response, nil
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if afterDrop != nil {
		afterDrop(wire)
	}
	return nil, errors.New("test dropped Task RPC response after owner handling")
}

func newTaskGroupFixture(t *testing.T, idleTimeout time.Duration) *taskGroupFixture {
	t.Helper()
	root := t.TempDir()
	owner, err := taskpersistence.NewJSONLStore(filepath.Join(root, "owner"))
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	host, err := NewHandler(HandlerConfig{Token: taskGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = owner.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	if err := registerTaskGroupWithTimeout(host, owner, idleTimeout); err != nil {
		_ = host.Close()
		_ = owner.Close()
		t.Fatalf("RegisterTaskGroup: %v", err)
	}
	server := httptest.NewServer(host)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: taskGroupTestToken, HTTPClient: server.Client(), Timeout: 2 * time.Second})
	if err != nil {
		server.Close()
		_ = host.Close()
		_ = owner.Close()
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = host.Close()
		_ = owner.Close()
		t.Fatalf("Handshake: %v", err)
	}
	store, err := NewTaskStoreClient(client)
	if err != nil {
		server.Close()
		_ = host.Close()
		_ = owner.Close()
		t.Fatalf("NewTaskStoreClient: %v", err)
	}
	fixture := &taskGroupFixture{host: host, server: server, owner: owner, store: store, root: root}
	t.Cleanup(func() {
		fixture.server.Close()
		_ = fixture.host.Close()
		_ = fixture.owner.Close()
	})
	return fixture
}

func taskGroupTask(id modulecore.TaskID, title string) domaintask.Task {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	return domaintask.Task{
		TaskID: id, Title: title, Route: domaintask.RouteGeneral,
		Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt:       now, UpdatedAt: now,
	}
}

func TestTaskGroupGlobalTransactionUsesOneOwnerReceiptAndReadYourWrites(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	taskValue := taskGroupTask(modulecore.NewTaskID(), "through storage host")
	shared := domaintask.SharedRoleContext{TaskID: taskValue.TaskID, UserIntent: "roundtrip", UpdatedAt: taskValue.CreatedAt}
	writerGeneration := uint64(0)
	err := fixture.store.Transaction(ctx, func(tx domaintask.Store) error {
		var err error
		writerGeneration, err = tx.WriterGeneration()
		if err != nil {
			return err
		}
		if err := tx.SaveTask(ctx, taskValue); err != nil {
			return err
		}
		if err := tx.SaveContext(ctx, shared); err != nil {
			return err
		}
		got, err := tx.GetTask(ctx, taskValue.TaskID)
		if err != nil || got.Title != taskValue.Title {
			return errors.New("transaction view did not read its pending Task")
		}
		gotContext, err := tx.GetContext(ctx, taskValue.TaskID)
		if err != nil || gotContext.UserIntent != shared.UserIntent {
			return errors.New("transaction view did not read its pending context")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if writerGeneration == 0 {
		t.Fatalf("transaction WriterGeneration=%d, want positive owner generation", writerGeneration)
	}
	got, err := fixture.store.GetTask(ctx, taskValue.TaskID)
	if err != nil || got.Title != taskValue.Title {
		t.Fatalf("GetTask after commit: got=%+v err=%v", got, err)
	}
	gotContext, err := fixture.store.GetContext(ctx, taskValue.TaskID)
	if err != nil || gotContext.UserIntent != shared.UserIntent {
		t.Fatalf("GetContext after commit: got=%+v err=%v", gotContext, err)
	}

	receiptBytes, err := os.ReadFile(filepath.Join(fixture.root, "owner", "task_operation_receipts.jsonl"))
	if err != nil {
		t.Fatalf("read atomic owner receipt: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(receiptBytes)), "\n")
	if len(lines) != 1 {
		t.Fatalf("global transaction receipt lines=%d, want one atomic receipt", len(lines))
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &receipt); err != nil {
		t.Fatalf("decode owner receipt: %v", err)
	}
	var scope string
	if err := json.Unmarshal(receipt["scope"], &scope); err != nil || scope != "global" {
		t.Fatalf("global receipt scope=%q err=%v, want explicit global scope", scope, err)
	}
	if _, exists := receipt["task_id"]; exists {
		t.Fatalf("global receipt must not encode a fake TaskID: %s", lines[0])
	}
}

func TestTaskGroupTaskTransactionBindsRunGenerationAndScope(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	taskValue := taskGroupTask(modulecore.NewTaskID(), "scoped")
	run := domaintask.Run{
		RunID: modulecore.NewRunID(), TaskID: taskValue.TaskID,
		StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning,
		StartedAt: taskValue.CreatedAt,
	}
	if err := fixture.store.TaskTransaction(ctx, taskValue.TaskID, func(tx domaintask.Store) error {
		generation, err := tx.WriterGeneration()
		if err != nil {
			return err
		}
		run.WriterGeneration = generation
		if err := tx.SaveTask(ctx, taskValue); err != nil {
			return err
		}
		if err := tx.SaveRun(ctx, run); err != nil {
			return err
		}
		got, err := tx.GetRun(ctx, run.RunID)
		if err != nil || got.TaskID != taskValue.TaskID || got.WriterGeneration == 0 {
			return errors.New("pending Run did not preserve task and writer generation")
		}
		return nil
	}); err != nil {
		t.Fatalf("TaskTransaction: %v", err)
	}
	got, err := fixture.store.GetRun(ctx, run.RunID)
	if err != nil || got.TaskID != taskValue.TaskID || got.WriterGeneration == 0 {
		t.Fatalf("GetRun: got=%+v err=%v", got, err)
	}
	if err := fixture.store.TaskTransaction(ctx, taskValue.TaskID, func(tx domaintask.Store) error {
		other := taskGroupTask(modulecore.NewTaskID(), "out of scope")
		return tx.SaveTask(ctx, other)
	}); err == nil {
		t.Fatal("TaskTransaction accepted a write outside its TaskID scope")
	}
}

func TestTaskGroupAcceptedOPSTaskClaimRoundTripsThroughRemoteStore(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	manager := taskmanager.New(fixture.store, taskmanager.DefaultParallelLimits())
	receipt := domainconversation.AcceptedOPSInputReceipt{
		AcceptanceSequence: 1,
		RequestID:          "request-remote-accepted-ops",
		OwnerID:            "user-remote",
		ActorID:            "user-remote",
		SessionID:          modulecore.NewSessionID(),
		ThreadID:           modulecore.NewThreadID(),
		ThreadSeq:          modulecore.ThreadSeq(1),
		ThreadKind:         modulecore.ThreadKindUserConversation,
		TaskID:             modulecore.NewTaskID(),
		TurnID:             modulecore.NewTurnID(),
		TraceID:            modulecore.NewTraceID(),
		UserMessageID:      modulecore.NewMessageID(),
		AgentMessageID:     modulecore.NewMessageID(),
		DeclaredOrigin:     domainconversation.AcceptedOPSInputOriginAutomation,
		PayloadSHA256:      strings.Repeat("a", 64),
		RawRecordID:        "raw_remote_accepted_ops",
		ManifestID:         "manifest_remote_accepted_ops",
		RawSHA256:          strings.Repeat("b", 64),
		ManifestSHA256:     strings.Repeat("c", 64),
		AcceptedAt:         time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC),
	}
	scope, err := domaintool.NewToolExecutionScope(
		receipt.RequestID,
		domaintool.ActorKindUser,
		receipt.OwnerID,
		receipt.OwnerID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	ctx := domaintool.WithToolExecutionScope(context.Background(), scope)
	claimed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != taskmanager.AcceptedOPSClaimed || !claimed.MayExecute {
		t.Fatalf("remote accepted OPS claim = %+v err=%v", claimed, err)
	}
	stored, err := manager.Get(ctx, receipt.TaskID)
	if err != nil || stored.AcceptedOPSClaim == nil || stored.AcceptedOPSClaim.ReceiptRef.OwnerID != receipt.OwnerID ||
		stored.AcceptedOPSClaim.ReceiptRef.RequestID != receipt.RequestID || stored.AcceptedOPSClaim.ReceiptRef.PayloadSHA256 != receipt.PayloadSHA256 ||
		stored.OriginSessionID != receipt.SessionID || stored.OriginThreadID != receipt.ThreadID ||
		stored.OriginTurnID != receipt.TurnID || stored.OriginMessageID != receipt.UserMessageID {
		t.Fatalf("remote Task metadata = %+v err=%v", stored, err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil || len(runs) != 1 || runs[0].RunID != claimed.Run.RunID || runs[0].WriterGeneration == 0 {
		t.Fatalf("remote Runs = %+v err=%v, want one first Run with writer generation", runs, err)
	}
	replayed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || replayed.Status != taskmanager.AcceptedOPSAlreadyRunning || replayed.MayExecute || replayed.Run.RunID != claimed.Run.RunID {
		t.Fatalf("remote replay = %+v err=%v, want same Run without execution right", replayed, err)
	}
}

func TestTaskGroupCallbackFailureRollsBackAndCallbackCancellationDoesNotCommit(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	sentinel := errors.New("callback rejected")
	rolledBack := taskGroupTask(modulecore.NewTaskID(), "must roll back")
	err := fixture.store.Transaction(ctx, func(tx domaintask.Store) error {
		if err := tx.SaveTask(ctx, rolledBack); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction error=%v, want callback error", err)
	}
	if _, err := fixture.store.GetTask(ctx, rolledBack.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("rolled-back Task lookup err=%v, want ErrNotFound", err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancelled := taskGroupTask(modulecore.NewTaskID(), "cancelled")
	err = fixture.store.Transaction(cancelCtx, func(tx domaintask.Store) error {
		if err := tx.SaveTask(cancelCtx, cancelled); err != nil {
			return err
		}
		cancel()
		return cancelCtx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Transaction error=%v, want context.Canceled", err)
	}
	if _, err := fixture.store.GetTask(ctx, cancelled.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("cancelled Task lookup err=%v, want ErrNotFound", err)
	}
}

func TestTaskGroupAbandonedBeginExpiresAndReleasesOwnerSession(t *testing.T) {
	fixture := newTaskGroupFixture(t, 30*time.Millisecond)
	ctx := context.Background()
	taskValue := taskGroupTask(modulecore.NewTaskID(), "idle transaction")
	err := fixture.store.Transaction(ctx, func(tx domaintask.Store) error {
		if err := tx.SaveTask(ctx, taskValue); err != nil {
			return err
		}
		time.Sleep(90 * time.Millisecond)
		return nil
	})
	if err == nil {
		t.Fatal("expired transaction unexpectedly committed")
	}
	if _, err := fixture.store.GetTask(ctx, taskValue.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("expired Task lookup err=%v, want ErrNotFound", err)
	}
	if err := fixture.store.SaveTask(ctx, taskGroupTask(modulecore.NewTaskID(), "after expiry")); err != nil {
		t.Fatalf("owner session gate leaked after expiry: %v", err)
	}
}

func TestTaskGroupFenceRunsOutsideBatchAndAllowsUnrelatedTaskProgress(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	fencedTaskID := modulecore.NewTaskID()
	entered := make(chan struct{})
	release := make(chan struct{})
	fenceDone := make(chan error, 1)
	go func() {
		fenceDone <- fixture.store.WithTaskExecutionFence(ctx, fencedTaskID, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fence callback did not start")
	}
	blockedSameTask := taskGroupTask(fencedTaskID, "waits for active fence")
	sameTaskProgress := make(chan error, 1)
	go func() { sameTaskProgress <- fixture.store.SaveTask(ctx, blockedSameTask) }()
	select {
	case err := <-sameTaskProgress:
		t.Fatalf("same-Task mutation returned while its execution callback was active: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	unrelated := taskGroupTask(modulecore.NewTaskID(), "unrelated progress")
	progress := make(chan error, 1)
	go func() { progress <- fixture.store.SaveTask(ctx, unrelated) }()
	select {
	case err := <-progress:
		if err != nil {
			t.Fatalf("unrelated Task mutation blocked by execution fence: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated Task mutation could not progress during external callback")
	}
	close(release)
	if err := <-fenceDone; err != nil {
		t.Fatalf("WithTaskExecutionFence: %v", err)
	}
	select {
	case err := <-sameTaskProgress:
		if err != nil {
			t.Fatalf("same-Task mutation did not resume after fence release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("same-Task mutation remained blocked after exact fence release")
	}
	status, err := fixture.owner.GetTaskExecutionFence(ctx, fencedTaskID)
	if err != nil || status.State != taskpersistence.TaskExecutionFenceStateReleased {
		t.Fatalf("durable fence status=%+v err=%v, want released", status, err)
	}
}

func TestTaskGroupBeginIdentityIsStableAndRejectsChangedScope(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	request := taskSessionBeginRequest{SessionID: newOpID(), Mode: taskSessionModeGlobal}
	var first, duplicate taskSessionBeginResult
	if err := fixture.store.client.Call(ctx, GroupTask, taskOpTxBegin, request, &first); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	if err := fixture.store.client.Call(ctx, GroupTask, taskOpTxBegin, request, &duplicate); err != nil {
		t.Fatalf("identical begin retry: %v", err)
	}
	if first.SessionID != request.SessionID || duplicate.SessionID != request.SessionID || first.Status != "active" || duplicate.Status != "active" {
		t.Fatalf("begin results first=%+v duplicate=%+v, want same active stable session", first, duplicate)
	}
	changed := taskSessionBeginRequest{SessionID: request.SessionID, Mode: taskSessionModeTask, TaskID: modulecore.NewTaskID()}
	err := fixture.store.client.Call(ctx, GroupTask, taskOpTxBegin, changed, nil)
	if !hasTaskGroupErrorCode(err, ErrorCodeDuplicateConflict) {
		t.Fatalf("changed-scope begin err=%v, want duplicate_op_conflict", err)
	}
	if err := fixture.store.client.Call(ctx, GroupTask, taskOpTxAbort, taskSessionIDRequest{SessionID: request.SessionID}, nil); err != nil {
		t.Fatalf("abort stable begin: %v", err)
	}
}

func TestTaskGroupBeginResponseLossRetriesSameSessionWithoutLeakingOwnerLock(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	fault := &taskGroupFaultTransport{next: fixture.server.Client().Transport, dropOp: taskOpTxBegin}
	fixture.store.client.httpClient = &http.Client{Transport: fault, Timeout: 2 * time.Second}
	ctx := context.Background()
	taskValue := taskGroupTask(modulecore.NewTaskID(), "begin response lost")
	callbackCalls := 0
	err := fixture.store.Transaction(ctx, func(tx domaintask.Store) error {
		callbackCalls++
		return tx.SaveTask(ctx, taskValue)
	})
	if err != nil {
		t.Fatalf("Transaction after begin response retry: %v", err)
	}
	if !fault.dropped || callbackCalls != 1 {
		t.Fatalf("begin response dropped=%v callback calls=%d, want one begin retry and one callback", fault.dropped, callbackCalls)
	}
	if _, err := fixture.store.GetTask(ctx, taskValue.TaskID); err != nil {
		t.Fatalf("Task lookup after begin retry: %v", err)
	}
}

func TestTaskGroupCommandReplayIsExactAndCommitOpRejectsDifferentTranscript(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	taskValue := taskGroupTask(modulecore.NewTaskID(), "one transcript")
	var sessionID string
	err := fixture.store.TaskTransaction(ctx, taskValue.TaskID, func(tx domaintask.Store) error {
		sessionID = tx.(*taskSessionStore).sessionID
		if err := tx.SaveTask(ctx, taskValue); err != nil {
			return err
		}
		replay := taskSessionCommandRequest{SessionID: sessionID, Sequence: 1, Kind: "save_task", Payload: mustJSON(taskValue)}
		var replayed taskSessionCommandResult
		if err := fixture.store.client.Call(ctx, GroupTask, taskOpTxCommand, replay, &replayed); err != nil {
			return err
		}
		if replayed.Sequence != 1 {
			return errors.New("exact replay returned the wrong cached sequence")
		}
		changedTask := taskValue
		changedTask.Title = "changed replay"
		conflictingReplay := replay
		conflictingReplay.Payload = mustJSON(changedTask)
		if err := fixture.store.client.Call(ctx, GroupTask, taskOpTxCommand, conflictingReplay, nil); !hasTaskGroupErrorCode(err, ErrorCodeDuplicateConflict) {
			return errors.New("changed command replay was not rejected")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TaskTransaction replay behavior: %v", err)
	}
	stateBytes, err := os.ReadFile(filepath.Join(fixture.root, "owner", "task_state.jsonl"))
	if err != nil {
		t.Fatalf("read Task JSONL: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(string(stateBytes)), "\n") + 1; lines != 1 {
		t.Fatalf("exact command replay appended %d Task rows, want one", lines)
	}
	operationID := fixture.store.client.lastOpID
	changed := taskCommitRequest{SessionID: operationID, Mode: taskSessionModeTask, TaskID: taskValue.TaskID, TranscriptHash: strings.Repeat("a", 64)}
	operation := &Operation{OpID: operationID, Group: GroupTask, Op: taskOpCommit, payload: mustJSON(changed)}
	if err := fixture.store.client.Do(ctx, operation, nil); !hasTaskGroupErrorCode(err, ErrorCodeDuplicateConflict) {
		t.Fatalf("same op_id/different transcript error=%v, want duplicate_op_conflict", err)
	}
}

func TestTaskGroupCommitResponseLossAndHostRestartReconcileOwnerReceipt(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	fault := &taskGroupFaultTransport{next: fixture.server.Client().Transport, dropOp: taskOpCommit}
	fault.afterDrop = func(Request) {
		fixture.server.Close()
		if err := fixture.host.Close(); err != nil {
			t.Errorf("close first host: %v", err)
			return
		}
		newHost, err := NewHandler(HandlerConfig{Token: taskGroupTestToken, JournalDir: filepath.Join(fixture.root, "journal")})
		if err != nil {
			t.Errorf("restart NewHandler: %v", err)
			return
		}
		if err := RegisterTaskGroup(newHost, fixture.owner); err != nil {
			_ = newHost.Close()
			t.Errorf("restart RegisterTaskGroup: %v", err)
			return
		}
		newServer := httptest.NewServer(newHost)
		fixture.host, fixture.server = newHost, newServer
		fixture.store.client.endpoint = strings.TrimRight(newServer.URL, "/") + RPCPath
	}
	fixture.store.client.httpClient = &http.Client{Transport: fault, Timeout: 2 * time.Second}
	ctx := context.Background()
	taskValue := taskGroupTask(modulecore.NewTaskID(), "committed before lost response")
	callbackCalls := 0
	if err := fixture.store.Transaction(ctx, func(tx domaintask.Store) error {
		callbackCalls++
		return tx.SaveTask(ctx, taskValue)
	}); err != nil {
		t.Fatalf("Transaction recovery: %v", err)
	}
	if !fault.dropped || callbackCalls != 1 {
		t.Fatalf("commit response dropped=%v callback calls=%d, want lost response and no callback replay", fault.dropped, callbackCalls)
	}
	receipt, err := fixture.owner.LookupTaskOperation(ctx, fault.captured.OpID)
	if err != nil || receipt.Scope != taskpersistence.TaskOperationScopeGlobal {
		t.Fatalf("durable global receipt=%+v err=%v", receipt, err)
	}
	if _, err := fixture.store.GetTask(ctx, taskValue.TaskID); err != nil {
		t.Fatalf("Task lookup after host restart: %v", err)
	}
}

func TestTaskGroupReadTransactionKeepsOneOwnerSnapshot(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	taskValue := taskGroupTask(modulecore.NewTaskID(), "before")
	if err := fixture.store.SaveTask(ctx, taskValue); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	updated := taskValue
	updated.Title = "after"
	writeStarted := make(chan struct{})
	writeDone := make(chan error, 1)
	err := fixture.store.ReadTransaction(ctx, func(tx domaintask.Store) error {
		first, err := tx.GetTask(ctx, taskValue.TaskID)
		if err != nil || first.Title != "before" {
			return errors.New("first read did not observe the original snapshot")
		}
		go func() {
			close(writeStarted)
			writeDone <- fixture.store.SaveTask(ctx, updated)
		}()
		<-writeStarted
		time.Sleep(60 * time.Millisecond)
		select {
		case err := <-writeDone:
			return fmt.Errorf("writer crossed active read snapshot: %w", err)
		default:
		}
		second, err := tx.GetTask(ctx, taskValue.TaskID)
		if err != nil || second.Title != "before" {
			return errors.New("second read did not observe the same owner snapshot")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadTransaction: %v", err)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("writer after read snapshot: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not progress after read session ended")
	}
	after, err := fixture.store.GetTask(ctx, taskValue.TaskID)
	if err != nil || after.Title != "after" {
		t.Fatalf("post-snapshot Task=%+v err=%v", after, err)
	}
}

func TestTaskGroupReadExpiryDoesNotPublishQueuedReplayOrInflightRead(t *testing.T) {
	root := t.TempDir()
	baseOwner, err := taskpersistence.NewJSONLStore(filepath.Join(root, "owner"))
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	defer baseOwner.Close()
	ctx := context.Background()
	firstTask := taskGroupTask(modulecore.NewTaskID(), "cached snapshot result")
	slowTask := taskGroupTask(modulecore.NewTaskID(), "in-flight snapshot result")
	if err := baseOwner.SaveTask(ctx, firstTask); err != nil {
		t.Fatalf("seed first Task: %v", err)
	}
	if err := baseOwner.SaveTask(ctx, slowTask); err != nil {
		t.Fatalf("seed slow Task: %v", err)
	}
	owner := &taskGroupA4ReadOwner{
		TaskGroupOwner:   baseOwner,
		slowTaskID:       slowTask.TaskID,
		slowReadStarted:  make(chan struct{}),
		allowSlowRead:    make(chan struct{}),
		ownerViewEnded:   make(chan struct{}),
		allowOwnerReturn: make(chan struct{}),
	}
	state := newTaskGroupState(owner, 120*time.Millisecond)
	sessionID := newOpID()
	if err := state.begin(ctx, taskSessionBeginRequest{SessionID: sessionID, Mode: taskSessionModeRead}); err != nil {
		t.Fatalf("read_begin: %v", err)
	}
	session, err := state.session(sessionID)
	if err != nil {
		t.Fatalf("read session lookup: %v", err)
	}
	defer func() {
		select {
		case <-owner.allowSlowRead:
		default:
			close(owner.allowSlowRead)
		}
		select {
		case <-owner.allowOwnerReturn:
		default:
			close(owner.allowOwnerReturn)
		}
		select {
		case <-session.done:
		case <-time.After(time.Second):
			t.Errorf("read session did not finish during cleanup")
		}
	}()
	select {
	case <-session.ready:
	case <-time.After(time.Second):
		t.Fatal("owner read view did not become ready")
	}
	firstRequest := taskSessionCommandRequest{
		SessionID: sessionID, Sequence: 1, Kind: "get_task",
		Payload: mustJSON(taskIDPayload{TaskID: firstTask.TaskID}),
	}
	firstResult, err := state.command(ctx, firstRequest, true)
	if err != nil || firstResult.ErrorCode != "" {
		t.Fatalf("initial cached read result=%+v err=%v", firstResult, err)
	}
	initialSequence, initialTranscript, err := state.commandTranscriptForTest(sessionID)
	if err != nil || initialSequence != 1 {
		t.Fatalf("initial read transcript sequence=%d err=%v", initialSequence, err)
	}
	slowRequest := taskSessionCommandRequest{
		SessionID: sessionID, Sequence: 2, Kind: "get_task",
		Payload: mustJSON(taskIDPayload{TaskID: slowTask.TaskID}),
	}
	type commandOutcome struct {
		result taskSessionCommandResult
		err    error
	}
	slowOutcome := make(chan commandOutcome, 1)
	go func() {
		result, err := state.command(ctx, slowRequest, true)
		slowOutcome <- commandOutcome{result: result, err: err}
	}()
	select {
	case <-owner.slowReadStarted:
	case <-time.After(time.Second):
		t.Fatal("slow owner-view read did not start")
	}
	replayOutcome := make(chan commandOutcome, 1)
	replayStarted := make(chan struct{})
	go func() {
		close(replayStarted)
		result, err := state.command(ctx, firstRequest, true)
		replayOutcome <- commandOutcome{result: result, err: err}
	}()
	<-replayStarted
	select {
	case outcome := <-replayOutcome:
		t.Fatalf("exact replay escaped while slow read held commandMu: result=%+v err=%v", outcome.result, outcome.err)
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-owner.ownerViewEnded:
	case <-time.After(time.Second):
		t.Fatal("owner read transaction did not expire while commandMu was held")
	}
	close(owner.allowSlowRead)
	slow := <-slowOutcome
	replay := <-replayOutcome
	if slow.err == nil && slow.result.ErrorCode == "" {
		t.Errorf("in-flight read published snapshot success after owner view expiry: %+v", slow.result)
	}
	if replay.err == nil && replay.result.ErrorCode == "" {
		t.Errorf("queued exact replay published cached snapshot success after owner view expiry: %+v", replay.result)
	}
	session.commandMu.Lock()
	sequence := session.sequence
	transcript := hex.EncodeToString(session.transcript[:])
	_, firstCached := session.commands[1]
	_, slowCached := session.commands[2]
	session.commandMu.Unlock()
	if sequence != initialSequence || !firstCached || slowCached || transcript != initialTranscript {
		t.Errorf("post-expiry command publication changed session: sequence=%d cached_first=%v cached_slow=%v transcript=%s", sequence, firstCached, slowCached, transcript)
	}
	close(owner.allowOwnerReturn)
	select {
	case <-session.done:
	case <-time.After(time.Second):
		t.Fatal("expired read session did not close")
	}
	select {
	case state.gate <- struct{}{}:
		state.releaseGate()
	case <-time.After(time.Second):
		t.Fatal("expired read session did not release its owner gate")
	}
}

func TestTaskGroupCompletesAllStoreReadAndNotificationMethods(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	taskValue := taskGroupTask(modulecore.NewTaskID(), "all methods")
	if err := fixture.store.SaveTask(ctx, taskValue); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	generation, err := fixture.store.WriterGeneration()
	if err != nil || generation == 0 {
		t.Fatalf("WriterGeneration=%d err=%v", generation, err)
	}
	run := domaintask.Run{WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: taskValue.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: taskValue.CreatedAt}
	if err := fixture.store.SaveRun(ctx, run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if runs, err := fixture.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskValue.TaskID, Limit: 10}); err != nil || len(runs) != 1 || runs[0].RunID != run.RunID {
		t.Fatalf("ListRuns=%+v err=%v", runs, err)
	}
	if tasks, err := fixture.store.ListTasks(ctx, domaintask.Filter{Limit: 10}); err != nil || len(tasks) != 1 || tasks[0].TaskID != taskValue.TaskID {
		t.Fatalf("ListTasks=%+v err=%v", tasks, err)
	}
	shared := domaintask.SharedRoleContext{TaskID: taskValue.TaskID, UserIntent: "context", UpdatedAt: taskValue.CreatedAt}
	if err := fixture.store.SaveContext(ctx, shared); err != nil {
		t.Fatalf("SaveContext: %v", err)
	}
	if got, err := fixture.store.GetContext(ctx, taskValue.TaskID); err != nil || got.UserIntent != shared.UserIntent {
		t.Fatalf("GetContext=%+v err=%v", got, err)
	}
	notification := domaintask.Notification{Type: "task.completed", Level: domaintask.NotificationDone, TaskID: taskValue.TaskID, Title: "complete", Status: domaintask.StatusSucceeded, Interrupt: true, CreatedAt: taskValue.CreatedAt}
	if err := fixture.store.SaveNotification(ctx, notification); err != nil {
		t.Fatalf("SaveNotification: %v", err)
	}
	if values, err := fixture.store.ListNotifications(ctx, 10, true); err != nil || len(values) != 1 || values[0].Title != notification.Title {
		t.Fatalf("ListNotifications=%+v err=%v", values, err)
	}
	if _, err := fixture.store.GetRun(ctx, run.RunID); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
}

func TestTaskGroupFenceCallbackErrorStillReleasesDurableCapability(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	sentinel := errors.New("external effect failed")
	taskID := modulecore.NewTaskID()
	err := fixture.store.WithTaskExecutionFence(ctx, taskID, func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTaskExecutionFence error=%v, want callback error", err)
	}
	status, err := fixture.owner.GetTaskExecutionFence(ctx, taskID)
	if err != nil || status.State != taskpersistence.TaskExecutionFenceStateReleased {
		t.Fatalf("fence status after callback error=%+v err=%v, want exact capability released", status, err)
	}
}

func TestTaskGroupDurableOldFenceAfterOwnerRestartBlocksCallback(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	taskID := modulecore.NewTaskID()
	if err := fixture.owner.AcquireTaskExecutionFence(ctx, taskID, "old-fence-capability"); err != nil {
		t.Fatalf("seed active owner fence: %v", err)
	}
	fixture.server.Close()
	if err := fixture.host.Close(); err != nil {
		t.Fatalf("close old host: %v", err)
	}
	if err := fixture.owner.Close(); err != nil {
		t.Fatalf("close old owner: %v", err)
	}
	owner, err := taskpersistence.NewJSONLStore(filepath.Join(fixture.root, "owner"))
	if err != nil {
		t.Fatalf("reopen owner: %v", err)
	}
	fixture.owner = owner
	host, err := NewHandler(HandlerConfig{Token: taskGroupTestToken, JournalDir: filepath.Join(fixture.root, "journal")})
	if err != nil {
		t.Fatalf("restart host: %v", err)
	}
	if err := RegisterTaskGroup(host, owner); err != nil {
		t.Fatalf("restart RegisterTaskGroup: %v", err)
	}
	server := httptest.NewServer(host)
	fixture.host, fixture.server = host, server
	fixture.store.client.endpoint = strings.TrimRight(server.URL, "/") + RPCPath
	if err := fixture.store.client.Handshake(ctx); err != nil {
		t.Fatalf("restart handshake: %v", err)
	}
	callbackCalls := 0
	err = fixture.store.WithTaskExecutionFence(ctx, taskID, func() error {
		callbackCalls++
		return nil
	})
	if err == nil || callbackCalls != 0 {
		t.Fatalf("old fence err=%v callback calls=%d, want blocked without callback replay", err, callbackCalls)
	}
	status, err := owner.GetTaskExecutionFence(ctx, taskID)
	if err != nil || status.FenceID != "old-fence-capability" || status.State != taskpersistence.TaskExecutionFenceStateActive {
		t.Fatalf("old fence status=%+v err=%v, want unchanged active capability", status, err)
	}
}

func TestTaskGroupReadCommandDoesNotWireOwnerErrorDetails(t *testing.T) {
	root := t.TempDir()
	baseOwner, err := taskpersistence.NewJSONLStore(filepath.Join(root, "owner"))
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	wrappedOwner := &taskGroupSanitizedErrorReadOwner{TaskGroupOwner: baseOwner}
	host, err := NewHandler(HandlerConfig{Token: taskGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = baseOwner.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	if err := registerTaskGroupWithTimeout(host, wrappedOwner, time.Second); err != nil {
		_ = host.Close()
		_ = baseOwner.Close()
		t.Fatalf("RegisterTaskGroup: %v", err)
	}
	server := httptest.NewServer(host)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: taskGroupTestToken, HTTPClient: server.Client(), Timeout: 2 * time.Second})
	if err != nil {
		server.Close()
		_ = host.Close()
		_ = baseOwner.Close()
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = host.Close()
		_ = baseOwner.Close()
		t.Fatalf("Handshake: %v", err)
	}
	store, err := NewTaskStoreClient(client)
	if err != nil {
		server.Close()
		_ = host.Close()
		_ = baseOwner.Close()
		t.Fatalf("NewTaskStoreClient: %v", err)
	}
	t.Cleanup(func() {
		server.Close()
		_ = host.Close()
		_ = baseOwner.Close()
	})

	const syntheticSecret = "synthetic-task-owner-credential-marker-6d4f"
	const syntheticLinuxPath = "/srv/private/owner-store/credential.db"
	const syntheticWindowsPath = `C:\Users\synthetic\AppData\Local\owner\credential.db`
	cases := []struct {
		name      string
		ownerErr  error
		wantCode  string
		wantIsErr error
	}{
		{name: "unknown_owner_error", ownerErr: fmt.Errorf("wrapped owner failure: %w", errors.New("credential="+syntheticSecret+" linux="+syntheticLinuxPath+" windows="+syntheticWindowsPath)), wantCode: "owner_error"},
		{name: "not_found", ownerErr: fmt.Errorf("owner wrapper: %w", domaintask.ErrNotFound), wantCode: "not_found", wantIsErr: domaintask.ErrNotFound},
		{name: "context_canceled", ownerErr: fmt.Errorf("owner wrapper: %w", context.Canceled), wantCode: "context_canceled", wantIsErr: context.Canceled},
		{name: "deadline_exceeded", ownerErr: fmt.Errorf("owner wrapper: %w", context.DeadlineExceeded), wantCode: "deadline_exceeded", wantIsErr: context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrappedOwner.ownerErr = tc.ownerErr
			_, err := store.GetTask(context.Background(), modulecore.NewTaskID())
			if err == nil {
				t.Fatal("GetTask unexpectedly succeeded")
			}
			var wireErr taskCommandWireError
			if !errors.As(err, &wireErr) {
				t.Fatalf("GetTask error=%T %v, want Task command wire error", err, err)
			}
			if wireErr.Code != tc.wantCode {
				t.Fatalf("wire error code=%q, want %q", wireErr.Code, tc.wantCode)
			}
			if wireErr.Message != "" {
				t.Fatalf("Task RPC exposed owner error details: %q", wireErr.Message)
			}
			if strings.Contains(err.Error(), syntheticSecret) || strings.Contains(err.Error(), syntheticLinuxPath) || strings.Contains(err.Error(), syntheticWindowsPath) {
				t.Fatalf("Task RPC error exposed synthetic owner details: %v", err)
			}
			if tc.wantIsErr != nil && !errors.Is(err, tc.wantIsErr) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tc.wantIsErr)
			}
		})
	}
}

func TestTaskGroupRejectsUnknownCommandAndOversizedPayload(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	ctx := context.Background()
	sessionID := newOpID()
	begin := taskSessionBeginRequest{SessionID: sessionID, Mode: taskSessionModeGlobal}
	if err := fixture.store.client.Call(ctx, GroupTask, taskOpTxBegin, begin, nil); err != nil {
		t.Fatalf("tx_begin: %v", err)
	}
	defer fixture.store.client.Call(context.Background(), GroupTask, taskOpTxAbort, taskSessionIDRequest{SessionID: sessionID}, nil)
	unknown := taskSessionCommandRequest{SessionID: sessionID, Sequence: 1, Kind: "sql", Payload: json.RawMessage(`{"query":"select *"}`)}
	if err := fixture.store.client.Call(ctx, GroupTask, taskOpTxCommand, unknown, nil); !hasTaskGroupErrorCode(err, ErrorCodeSchemaRejected) {
		t.Fatalf("unknown command error=%v, want schema_rejected", err)
	}
	oversizedPayload, err := json.Marshal(map[string]string{"title": strings.Repeat("x", maxTaskGroupCommandBytes)})
	if err != nil {
		t.Fatal(err)
	}
	oversized := taskSessionCommandRequest{SessionID: sessionID, Sequence: 1, Kind: "save_task", Payload: oversizedPayload}
	if err := fixture.store.client.Call(ctx, GroupTask, taskOpTxCommand, oversized, nil); !hasTaskGroupErrorCode(err, ErrorCodeSchemaRejected) {
		t.Fatalf("oversized command error=%v, want schema_rejected", err)
	}
}

func hasTaskGroupErrorCode(err error, code string) bool {
	var storageErr *Error
	return errors.As(err, &storageErr) && storageErr.Code == code
}

func TestTaskGroupContractIsClosedAndDoesNotExposeArbitraryOperations(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	contract, err := fixture.store.client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	allowed := map[string]bool{
		"writer_generation": false, "tx_begin": false, "tx_command": false,
		"tx_prepare_commit": false, "tx_abort": false,
		"read_begin": false, "read_command": false, "read_end": false,
		"commit": true, "fence_reservation": false, "fence_acquire": true,
		"fence_release": true,
	}
	for _, spec := range contract.Operations {
		if spec.Group == GroupTask {
			wantMutating, exists := allowed[spec.Op]
			if !exists {
				t.Errorf("unexpected Task operation %q", spec.Op)
			} else if spec.Mutating != wantMutating {
				t.Errorf("Task operation %q mutating=%v, want %v", spec.Op, spec.Mutating, wantMutating)
			}
			delete(allowed, spec.Op)
		}
	}
	if len(allowed) != 0 {
		t.Fatalf("missing fixed Task operations: %v", allowed)
	}
	var ignored json.RawMessage
	if err := fixture.store.client.Call(context.Background(), GroupTask, "sql", map[string]any{"query": "select *"}, &ignored); err == nil {
		t.Fatal("arbitrary operation reached the Task owner")
	}
}
