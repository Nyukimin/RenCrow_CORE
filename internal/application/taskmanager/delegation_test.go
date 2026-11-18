package taskmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEnsureTaskForDelegationCreatesOneTaskRunAndContext(t *testing.T) {
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	var checks int
	check := func(context.Context, modulecore.RequestID, uint64) error {
		checks++
		return nil
	}
	draft := domaintask.Task{Title: "delegated task", Route: domaintask.RouteResearch, Assignee: "shiro"}
	shared := domaintask.SharedRoleContext{UserIntent: "find current evidence", CurrentPlan: "research the request"}

	task, run, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 1, draft, shared, check)
	if err != nil {
		t.Fatalf("EnsureTaskForDelegation: %v", err)
	}
	if task.Status != domaintask.StatusRunning || task.StartedAt == nil {
		t.Fatalf("task=%#v, want running Task", task)
	}
	if run.TaskID != task.TaskID || run.RunID == "" || run.StartReason != domaintask.RunStartReasonFirst || run.Status != domaintask.RunStatusRunning {
		t.Fatalf("run=%#v, want the Task's first running Run", run)
	}
	if checks != 2 {
		t.Fatalf("admission checks=%d, want preflight and pre-commit checks", checks)
	}
	storedContext, err := manager.Context(context.Background(), task.TaskID)
	if err != nil || storedContext.TaskID != task.TaskID || storedContext.UserIntent != shared.UserIntent || storedContext.CurrentPlan != shared.CurrentPlan {
		t.Fatalf("context=%#v err=%v", storedContext, err)
	}
	assertStoreCounts(t, manager, 1, 1)

	receipt, err := store.LookupTaskOperation(context.Background(), fmt.Sprintf("delegation:%s:1:start", delegationID))
	if err != nil {
		t.Fatalf("LookupTaskOperation: %v", err)
	}
	if receipt.Scope != taskpersistence.TaskOperationScopeGlobal || receipt.TaskID != "" || len(receipt.Result) > 256 {
		t.Fatalf("receipt=%+v, want a small global receipt", receipt)
	}
}

func TestDelegationTaskOperationIDIsCanonicalCrossStoreIdentity(t *testing.T) {
	delegationID := modulecore.NewRequestID()
	if got, want := DelegationTaskOperationID(delegationID, 17), fmt.Sprintf("delegation:%s:17:start", delegationID); got != want {
		t.Fatalf("DelegationTaskOperationID()=%q, want %q", got, want)
	}
}

func TestDelegationRequestSnapshotPreservesJSONOperationHash(t *testing.T) {
	startedAt := time.Date(2026, 11, 17, 8, 12, 17, 123456789, time.FixedZone("JST", 9*60*60))
	draft := domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "nested frozen request", Route: domaintask.RouteGeneral,
		OriginPublication: &domaintask.OriginPublicationProvenance{
			PublicationIncarnation: "publication-3", Channel: "http", ChannelAddress: "request-7", Audience: "public",
		},
		CreatedAt: startedAt, UpdatedAt: startedAt, StartedAt: &startedAt,
		NextActions: []string{}, CoderRoles: []string{"reader"}, DependencyTaskIDs: []modulecore.TaskID{},
	}
	shared := domaintask.SharedRoleContext{
		UserIntent: "preserve the original canonical JSON hash", RelevantFiles: []string{}, Constraints: []string{"same source"}, UpdatedAt: startedAt,
	}
	request := delegationTaskOperationRequest{
		Operation: delegationStartOperation, DelegationID: modulecore.NewRequestID(),
		Revision: uint64(1<<63) + 19, Task: draft, Shared: shared,
	}
	directHash, err := hashDelegationTaskOperation(request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotDelegationTaskOperationRequest(request)
	if err != nil {
		t.Fatalf("snapshot request: %v", err)
	}
	snapshotHash, err := hashDelegationTaskOperation(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if directHash != snapshotHash {
		t.Fatalf("snapshot changed operation hash: direct=%s snapshot=%s", directHash, snapshotHash)
	}
	// Nil and empty omitempty slices had the same persisted JSON identity
	// before snapshotting and must keep that replay compatibility.
	request.Task.NextActions = nil
	request.Task.DependencyTaskIDs = nil
	request.Shared.RelevantFiles = nil
	nilHash, err := hashDelegationTaskOperation(request)
	if err != nil {
		t.Fatal(err)
	}
	if nilHash != directHash {
		t.Fatalf("nil versus empty omitempty slices changed persisted operation hash: nil=%s empty=%s", nilHash, directHash)
	}
}

func TestEnsureTaskForDelegationReplaysSameTaskAndRunAfterRestart(t *testing.T) {
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	draft := domaintask.Task{Title: "recover delegated task", Route: domaintask.RouteGeneral, Assignee: "shiro"}
	shared := domaintask.SharedRoleContext{UserIntent: "recover same task"}
	var checks int
	check := func(_ context.Context, gotDelegationID modulecore.RequestID, gotRevision uint64) error {
		checks++
		if gotDelegationID != delegationID || gotRevision != 4 {
			t.Errorf("admission check identity=(%s,%d), want=(%s,4)", gotDelegationID, gotRevision, delegationID)
		}
		return nil
	}

	firstTask, firstRun, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 4, draft, shared, check)
	if err != nil {
		t.Fatalf("first EnsureTaskForDelegation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Task store: %v", err)
	}

	reopened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen Task store: %v", err)
	}
	registerTaskStoreCleanup(t, reopened)
	restartedManager := New(reopened, DefaultParallelLimits())
	secondTask, secondRun, err := restartedManager.EnsureTaskForDelegation(context.Background(), delegationID, 4, draft, shared, check)
	if err != nil {
		t.Fatalf("replayed EnsureTaskForDelegation: %v", err)
	}
	if secondTask.TaskID != firstTask.TaskID || secondRun.RunID != firstRun.RunID {
		t.Fatalf("replay Task/Run=(%s,%s), want original=(%s,%s)", secondTask.TaskID, secondRun.RunID, firstTask.TaskID, firstRun.RunID)
	}
	if checks != 3 {
		t.Fatalf("admission checks=%d, want checks on first preflight/pre-commit and replay preflight only", checks)
	}
	assertStoreCounts(t, restartedManager, 1, 1)
}

func TestEnsureTaskForDelegationRejectsChangedRequestForSameRevision(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	check := func(context.Context, modulecore.RequestID, uint64) error { return nil }
	first, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 2,
		domaintask.Task{Title: "original", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{UserIntent: "original"}, check)
	if err != nil {
		t.Fatalf("first EnsureTaskForDelegation: %v", err)
	}
	if _, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 2,
		domaintask.Task{Title: "changed", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{UserIntent: "original"}, check); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed request error=%v, want ErrTaskOperationConflict", err)
	}
	stored, err := manager.Get(context.Background(), first.TaskID)
	if err != nil || stored.Title != "original" {
		t.Fatalf("stored Task=%#v err=%v, want original unchanged", stored, err)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestEnsureTaskForDelegationUsesNewOperationForNewRevision(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	check := func(context.Context, modulecore.RequestID, uint64) error { return nil }
	draft := domaintask.Task{Title: "revisioned delegated task", Route: domaintask.RouteGeneral}
	first, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 1, draft, domaintask.SharedRoleContext{}, check)
	if err != nil {
		t.Fatalf("revision 1: %v", err)
	}
	second, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 2, draft, domaintask.SharedRoleContext{}, check)
	if err != nil {
		t.Fatalf("revision 2: %v", err)
	}
	if first.TaskID == second.TaskID {
		t.Fatalf("revision 1 and 2 share TaskID %s", first.TaskID)
	}
	assertStoreCounts(t, manager, 2, 2)
}

func TestEnsureTaskForDelegationKeepsTaskCapacityAdmission(t *testing.T) {
	limits := DefaultParallelLimits()
	limits.Global = 1
	manager := newCreateAndStartManager(t, limits)
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "occupant", Route: domaintask.RouteGeneral,
	}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("start capacity occupant: %v", err)
	}
	delegationID := modulecore.NewRequestID()
	taskID := modulecore.NewTaskID()
	check := func(context.Context, modulecore.RequestID, uint64) error { return nil }
	_, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 1,
		domaintask.Task{TaskID: taskID, Title: "capacity refused", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}, check)
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("EnsureTaskForDelegation error=%v, want ErrParallelLimit", err)
	}
	assertStoreCounts(t, manager, 1, 1)
	assertDelegationReceiptAbsent(t, manager, delegationID, 1)
	if _, err := manager.Get(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("capacity-refused Task lookup error=%v, want ErrNotFound", err)
	}
	if _, err := manager.Context(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("capacity-refused context lookup error=%v, want ErrNotFound", err)
	}
}

func TestEnsureTaskForDelegationPreCommitRejectionRollsBackTaskRunAndContext(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	taskID := modulecore.NewTaskID()
	canceled := errors.New("delegation was canceled before Task commit")
	checks := 0
	check := func(context.Context, modulecore.RequestID, uint64) error {
		checks++
		if checks == 2 {
			return canceled
		}
		return nil
	}
	_, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 3,
		domaintask.Task{TaskID: taskID, Title: "must roll back", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{UserIntent: "must roll back"}, check)
	if !errors.Is(err, canceled) {
		t.Fatalf("EnsureTaskForDelegation error=%v, want pre-commit cancellation", err)
	}
	if checks != 2 {
		t.Fatalf("admission checks=%d, want preflight then pre-commit check", checks)
	}
	assertStoreCounts(t, manager, 0, 0)
	assertDelegationReceiptAbsent(t, manager, delegationID, 3)
	if _, err := manager.Get(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("rolled-back Task lookup error=%v, want ErrNotFound", err)
	}
	if _, err := manager.Context(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("rolled-back context lookup error=%v, want ErrNotFound", err)
	}
}

func TestEnsureTaskForDelegationRejectsUnauthorizedInputBeforeWriting(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	taskID := modulecore.NewTaskID()
	unauthorized := errors.New("delegation is not authorized")
	checks := 0
	check := func(context.Context, modulecore.RequestID, uint64) error {
		checks++
		return unauthorized
	}
	_, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 1,
		domaintask.Task{TaskID: taskID, Title: "unauthorized", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}, check)
	if !errors.Is(err, unauthorized) {
		t.Fatalf("unauthorized request error=%v", err)
	}
	if checks != 1 {
		t.Fatalf("admission checks=%d, want one preflight check", checks)
	}
	assertStoreCounts(t, manager, 0, 0)
	assertDelegationReceiptAbsent(t, manager, delegationID, 1)
	if _, err := manager.Get(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("unauthorized Task lookup error=%v, want ErrNotFound", err)
	}
	if _, err := manager.Context(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("unauthorized context lookup error=%v, want ErrNotFound", err)
	}
}

func assertDelegationReceiptAbsent(t *testing.T, manager *Manager, delegationID modulecore.RequestID, revision uint64) {
	t.Helper()
	store, ok := manager.store.(*taskpersistence.JSONLStore)
	if !ok {
		t.Fatalf("Task store type=%T, want JSONLStore", manager.store)
	}
	operationID := fmt.Sprintf("delegation:%s:%d:start", delegationID, revision)
	if _, err := store.LookupTaskOperation(context.Background(), operationID); !errors.Is(err, taskpersistence.ErrTaskOperationNotFound) {
		t.Fatalf("delegation receipt error=%v, want ErrTaskOperationNotFound", err)
	}
}

func TestLookupTaskForDelegationRecoversReceiptAfterRestartWithBoundReadAuthorization(t *testing.T) {
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	firstManager := New(store, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	draft := domaintask.Task{Title: "recover frozen request", Route: domaintask.RouteGeneral, Assignee: "shiro", NextActions: []string{"review"}}
	shared := domaintask.SharedRoleContext{UserIntent: "recover after timeout", Constraints: []string{"same frozen body"}}
	firstTask, firstRun, err := firstManager.EnsureTaskForDelegation(context.Background(), delegationID, 9, draft, shared,
		func(context.Context, modulecore.RequestID, uint64) error { return nil })
	if err != nil {
		t.Fatalf("EnsureTaskForDelegation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Task store: %v", err)
	}

	reopened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen Task store: %v", err)
	}
	registerTaskStoreCleanup(t, reopened)
	manager := New(reopened, DefaultParallelLimits())
	normalized, err := snapshotDelegationTaskOperationRequest(delegationTaskOperationRequest{
		Operation: delegationStartOperation, DelegationID: delegationID, Revision: 9, Task: draft, Shared: shared,
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedHash, err := hashDelegationTaskOperation(normalized)
	if err != nil {
		t.Fatal(err)
	}
	authorizationChecks := 0
	authorize := func(_ context.Context, input DelegationReceiptReadAuthorizationInput) error {
		authorizationChecks++
		if input.DelegationID != delegationID || input.Revision != 9 || input.RequestHash != expectedHash ||
			!reflect.DeepEqual(input.Draft, normalized.Task) || !reflect.DeepEqual(input.Shared, normalized.Shared) {
			return errors.New("owner source handoff does not match frozen request")
		}
		// The callback receives its own immutable snapshot. A buggy owner must
		// not be able to alter the later checks or the TaskManager lookup input.
		input.Draft.Title = "mutated by authorization callback"
		input.Shared.Constraints[0] = "mutated by authorization callback"
		return nil
	}
	gotTask, gotRun, err := manager.LookupTaskForDelegation(context.Background(), delegationID, 9, draft, shared, authorize)
	if err != nil {
		t.Fatalf("LookupTaskForDelegation after restart: %v", err)
	}
	if gotTask.TaskID != firstTask.TaskID || gotRun.RunID != firstRun.RunID || gotRun.TaskID != gotTask.TaskID {
		t.Fatalf("recovered Task/Run=(%s,%s) want original=(%s,%s)", gotTask.TaskID, gotRun.RunID, firstTask.TaskID, firstRun.RunID)
	}
	if authorizationChecks != 3 {
		t.Fatalf("read authorization checks=%d, want before receipt, before Task reads, and before return", authorizationChecks)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestLookupTaskForDelegationNotFoundIsReadOnly(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	draft := domaintask.Task{Title: "missing frozen request", Route: domaintask.RouteGeneral}
	shared := domaintask.SharedRoleContext{UserIntent: "read only"}
	checks := 0
	authorize := exactDelegationReceiptAuthorizer(t, delegationID, 2, draft, shared, &checks)
	if _, _, err := manager.LookupTaskForDelegation(context.Background(), delegationID, 2, draft, shared, authorize); !errors.Is(err, ErrDelegationReceiptNotFound) {
		t.Fatalf("LookupTaskForDelegation error=%v, want ErrDelegationReceiptNotFound", err)
	}
	if checks != 2 {
		t.Fatalf("authorization checks=%d, want before lookup and before not-found return", checks)
	}
	assertStoreCounts(t, manager, 0, 0)
	assertDelegationReceiptAbsent(t, manager, delegationID, 2)
}

func TestLookupTaskForDelegationDeniedForeignKnownRequestBeforeReceiptRead(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	foreignID := modulecore.NewRequestID()
	draft := domaintask.Task{Title: "private foreign Goal", Route: domaintask.RouteGeneral}
	shared := domaintask.SharedRoleContext{UserIntent: "foreign owner"}
	if _, _, err := manager.EnsureTaskForDelegation(context.Background(), foreignID, 5, draft, shared,
		func(context.Context, modulecore.RequestID, uint64) error { return nil }); err != nil {
		t.Fatalf("create known foreign receipt: %v", err)
	}
	baseStore, ok := manager.store.(*taskpersistence.JSONLStore)
	if !ok {
		t.Fatalf("Task store type=%T, want JSONLStore", manager.store)
	}
	readerStore := &countingDelegationReaderStore{Store: baseStore, reader: baseStore}
	readerManager := New(readerStore, DefaultParallelLimits())
	otherOwnedID := modulecore.NewRequestID()
	secretReason := errors.New("private source record is not visible to this owner")
	for _, requestID := range []modulecore.RequestID{foreignID, modulecore.NewRequestID()} {
		_, _, err := readerManager.LookupTaskForDelegation(context.Background(), requestID, 5, draft, shared,
			func(_ context.Context, input DelegationReceiptReadAuthorizationInput) error {
				if input.DelegationID == otherOwnedID {
					return nil
				}
				return secretReason
			})
		if !errors.Is(err, ErrDelegationReceiptReadForbidden) || strings.Contains(err.Error(), secretReason.Error()) {
			t.Fatalf("request %s authorization error=%v, want sanitized forbidden", requestID, err)
		}
	}
	if readerStore.reads != 0 {
		t.Fatalf("global receipt reads=%d, want authorization to deny before lookup", readerStore.reads)
	}
}

func TestLookupTaskForDelegationRechecksAuthorizationBeforeReturningReceipt(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	draft := domaintask.Task{Title: "authorization can expire", Route: domaintask.RouteGeneral}
	shared := domaintask.SharedRoleContext{UserIntent: "owner read"}
	if _, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 3, draft, shared,
		func(context.Context, modulecore.RequestID, uint64) error { return nil }); err != nil {
		t.Fatalf("create receipt: %v", err)
	}
	baseStore := manager.store.(*taskpersistence.JSONLStore)
	readerStore := &countingDelegationReaderStore{Store: baseStore, reader: baseStore}
	readerManager := New(readerStore, DefaultParallelLimits())
	checks := 0
	denied := errors.New("authorization expired after receipt probe")
	_, _, err := readerManager.LookupTaskForDelegation(context.Background(), delegationID, 3, draft, shared,
		func(context.Context, DelegationReceiptReadAuthorizationInput) error {
			checks++
			if checks == 1 {
				return nil
			}
			return denied
		})
	if !errors.Is(err, ErrDelegationReceiptReadForbidden) || strings.Contains(err.Error(), denied.Error()) {
		t.Fatalf("expired authorization error=%v, want sanitized forbidden", err)
	}
	if checks != 2 || readerStore.reads != 1 {
		t.Fatalf("authorization checks=%d receipt reads=%d, want two checks around one receipt read", checks, readerStore.reads)
	}
}

func TestLookupTaskForDelegationPreservesUncertainReadFailureWithoutCreatingTask(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	draft := domaintask.Task{Title: "uncertain receipt read", Route: domaintask.RouteGeneral}
	shared := domaintask.SharedRoleContext{UserIntent: "retry with owner reconciliation"}
	uncertain := errors.New("receipt storage read outcome is unknown")
	readerStore := &countingDelegationReaderStore{Store: manager.store, readErr: uncertain}
	readerManager := New(readerStore, DefaultParallelLimits())
	checks := 0
	authorize := exactDelegationReceiptAuthorizer(t, delegationID, 12, draft, shared, &checks)
	_, _, err := readerManager.LookupTaskForDelegation(context.Background(), delegationID, 12, draft, shared, authorize)
	if !errors.Is(err, uncertain) || errors.Is(err, ErrDelegationReceiptNotFound) {
		t.Fatalf("uncertain read error=%v, want original failure and not NotFound", err)
	}
	if checks != 2 || readerStore.reads != 1 {
		t.Fatalf("authorization checks=%d receipt reads=%d, want two auth checks around one read", checks, readerStore.reads)
	}
	assertStoreCounts(t, manager, 0, 0)
}

func TestLookupTaskForDelegationRequiresAuthorizationAndPreservesCanceledContext(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	draft := domaintask.Task{Title: "validation", Route: domaintask.RouteGeneral}
	shared := domaintask.SharedRoleContext{}
	if _, _, err := manager.LookupTaskForDelegation(context.Background(), delegationID, 1, draft, shared, nil); !errors.Is(err, ErrDelegationReceiptReadAuthorizationRequired) {
		t.Fatalf("missing authorization error=%v, want required", err)
	}
	if _, _, err := manager.LookupTaskForDelegation(context.Background(), modulecore.RequestID("invalid"), 1, draft, shared,
		func(context.Context, DelegationReceiptReadAuthorizationInput) error { return nil }); err == nil {
		t.Fatal("invalid RequestID unexpectedly passed validation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := manager.LookupTaskForDelegation(ctx, delegationID, 1, draft, shared,
		func(context.Context, DelegationReceiptReadAuthorizationInput) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error=%v, want context.Canceled", err)
	}
	baseStore := manager.store.(*taskpersistence.JSONLStore)
	unsupportedManager := New(&noGlobalReceiptReaderStore{Store: baseStore}, DefaultParallelLimits())
	checks := 0
	if _, _, err := unsupportedManager.LookupTaskForDelegation(context.Background(), delegationID, 1, draft, shared,
		func(context.Context, DelegationReceiptReadAuthorizationInput) error { checks++; return nil }); !errors.Is(err, ErrGlobalTaskOperationReadUnsupported) {
		t.Fatalf("unsupported reader error=%v, want ErrGlobalTaskOperationReadUnsupported", err)
	}
	if checks != 2 {
		t.Fatalf("unsupported reader authorization checks=%d, want check before and before return", checks)
	}
	secretContextError := fmt.Errorf("private source path: %w", context.Canceled)
	if _, _, err := unsupportedManager.LookupTaskForDelegation(context.Background(), delegationID, 1, draft, shared,
		func(context.Context, DelegationReceiptReadAuthorizationInput) error { return secretContextError }); err != context.Canceled {
		t.Fatalf("wrapped authorization context error=%v, want sanitized context.Canceled", err)
	}
	assertStoreCounts(t, manager, 0, 0)
}

func exactDelegationReceiptAuthorizer(
	t *testing.T,
	delegationID modulecore.RequestID,
	revision uint64,
	draft domaintask.Task,
	shared domaintask.SharedRoleContext,
	checks *int,
) DelegationReceiptReadAuthorizationCheck {
	t.Helper()
	request, err := snapshotDelegationTaskOperationRequest(delegationTaskOperationRequest{
		Operation: delegationStartOperation, DelegationID: delegationID, Revision: revision, Task: draft, Shared: shared,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hashDelegationTaskOperation(request)
	if err != nil {
		t.Fatal(err)
	}
	return func(_ context.Context, input DelegationReceiptReadAuthorizationInput) error {
		*checks = *checks + 1
		if input.DelegationID != delegationID || input.Revision != revision || input.RequestHash != hash ||
			!reflect.DeepEqual(input.Draft, request.Task) || !reflect.DeepEqual(input.Shared, request.Shared) {
			return errors.New("frozen owner source does not match requested receipt")
		}
		return nil
	}
}

type countingDelegationReaderStore struct {
	domaintask.Store
	reader  globalTaskOperationReader
	reads   int
	readErr error
}

func (s *countingDelegationReaderStore) LookupGlobalTaskOperation(ctx context.Context, operationID, requestHash string) (json.RawMessage, bool, error) {
	s.reads++
	if s.readErr != nil {
		return nil, false, s.readErr
	}
	return s.reader.LookupGlobalTaskOperation(ctx, operationID, requestHash)
}

type noGlobalReceiptReaderStore struct {
	domaintask.Store
}
