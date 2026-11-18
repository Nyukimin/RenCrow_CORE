package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEnsureNativePlanningInvocationUsesOwnerReceiptAndFencesGenericLifecycle(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	parent, parentRun, ctx, requestID := nativePlanningParent(t, manager, "shiro", false)
	envelope := json.RawMessage(`{"operation":"tasks.list_remaining","input":{"limit":10}}`)
	child, run, shared, created, err := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, parentRun.RunID, "shiro", "aka", requestID, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !created || child.NativePlanningInvocation == nil || child.Assignee != "aka" || child.ParentTaskID != parent.TaskID || child.Route != domaintask.RoutePLAN || !child.ReadOnly {
		t.Fatalf("issued child=%+v created=%v", child, created)
	}
	if run.TaskID != child.TaskID || run.Assignee != "aka" || shared.TaskID != child.TaskID {
		t.Fatalf("issued run/context mismatch: %+v %+v", run, shared)
	}
	retry, retryRun, _, created, err := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, parentRun.RunID, "shiro", "aka", requestID, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if created || retry.TaskID != child.TaskID || retryRun.RunID != run.RunID {
		t.Fatalf("receipt replay child/run=%s/%s created=%v", retry.TaskID, retryRun.RunID, created)
	}
	changed := json.RawMessage(`{"operation":"tasks.list_claimable","input":{"limit":10}}`)
	if _, _, _, _, err := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, parentRun.RunID, "shiro", "aka", requestID, changed); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed envelope error=%v, want receipt conflict", err)
	}
	if err := manager.ValidateRunExecution(ctx, child.TaskID, run.RunID, "aka"); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("generic execution validation error=%v, want owner fence", err)
	}
	if _, err := manager.Get(ctx, child.TaskID); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("generic Task read error=%v, want owner fence", err)
	}
	if _, err := manager.Context(ctx, child.TaskID); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("generic context read error=%v, want owner fence", err)
	}
	if _, err := manager.GetRun(ctx, run.RunID); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("generic Run read error=%v, want owner fence", err)
	}
	if listed, err := manager.List(ctx, domaintask.Filter{Assignee: "aka"}); err != nil || len(listed) != 0 {
		t.Fatalf("generic Task list leaked planning child: items=%+v err=%v", listed, err)
	}
	if listed, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: child.TaskID}); err != nil || len(listed) != 0 {
		t.Fatalf("generic Run list leaked planning child: runs=%+v err=%v", listed, err)
	}
	if err := manager.CompleteNativePlanningInvocation(ctx, child.TaskID, run.RunID, parent.TaskID, parentRun.RunID, "shiro", "aka", `{"items":[]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, parent.TaskID, parentRun.RunID, "shiro", domaintask.StatusSucceeded, "parent finished", ""); err != nil {
		t.Fatal(err)
	}
	finished, finishedRun, _, created, err := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, parentRun.RunID, "shiro", "aka", requestID, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if created || finished.Status != domaintask.StatusSucceeded || finishedRun.Status != domaintask.RunStatusSucceeded || finished.Summary != `{"items":[]}` {
		t.Fatalf("finished replay=%+v run=%+v created=%v", finished, finishedRun, created)
	}
	operationID, requestHash := nativePlanningOperationIdentity(parent.TaskID, parentRun.RunID, "aka", requestID, envelope)
	if _, found, err := store.LookupGlobalTaskOperation(ctx, operationID, requestHash); found || err != nil && !errors.Is(err, taskpersistence.ErrTaskOperationNotFound) {
		t.Fatalf("generic owner-only receipt lookup=(found %v, err %v)", found, err)
	}
	newRequestID := modulecore.NewRequestID()
	newScope, err := tool.NewToolExecutionScope(string(newRequestID), tool.ActorKindAgent, "shiro", "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	newCtx := tool.WithToolExecutionScope(ctx, newScope)
	if _, _, _, _, err := manager.EnsureNativePlanningInvocation(newCtx, parent.TaskID, parentRun.RunID, "shiro", "aka", newRequestID, envelope); err == nil {
		t.Fatal("inactive parent issued a new planning child")
	}
}

func TestEnsureNativePlanningInvocationConcurrentRetryHasSingleIssuer(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	parent, parentRun, ctx, requestID := nativePlanningParent(t, manager, "shiro", false)
	envelope := json.RawMessage(`{"operation":"tasks.get","input":{"goal_id":"g"}}`)
	var wg sync.WaitGroup
	created := make(chan bool, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, wasCreated, callErr := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, parentRun.RunID, "shiro", "aka", requestID, envelope)
			created <- wasCreated
			errs <- callErr
		}()
	}
	wg.Wait()
	close(created)
	close(errs)
	count := 0
	for wasCreated := range created {
		if wasCreated {
			count++
		}
	}
	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("concurrent Ensure: %v", callErr)
		}
	}
	if count != 1 {
		t.Fatalf("new child issuers=%d, want exactly one", count)
	}
}

func TestEnsureNativePlanningInvocationInheritsReadonly(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	parent, parentRun, ctx, requestID := nativePlanningParent(t, manager, "mio", true)
	envelope := json.RawMessage(`{"operation":"tasks.list_remaining","input":{"limit":2}}`)
	child, _, _, _, err := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, parentRun.RunID, "mio", "mio", requestID, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !child.ReadOnly || child.NativePlanningInvocation == nil || !child.NativePlanningInvocation.ParentReadOnly {
		t.Fatalf("read-only inheritance missing: %+v", child)
	}
}

func TestNativePlanningOwnerReadsStayFencedWithIndexedTaskStore(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	parent, parentRun, ctx, requestID := nativePlanningParent(t, manager, "mio", false)
	envelope := json.RawMessage(`{"operation":"tasks.capabilities","input":{}}`)
	child, run, _, _, err := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, parentRun.RunID, "mio", "mio", requestID, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(ctx, child.TaskID); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("indexed generic Task read=%v", err)
	}
	if _, err := manager.GetRun(ctx, run.RunID); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("indexed generic Run read=%v", err)
	}
	if _, err := store.GetContext(ctx, child.TaskID); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("indexed generic context read=%v", err)
	}
}

func TestNativePlanningInvocationPinsAndDerivesExactUserScope(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	parent, run, ctx, requestID := nativePlanningParent(t, manager, "mio", false)
	userScope, err := tool.NewToolExecutionScope(string(requestID), tool.ActorKindAgent, "mio", "user-a", []string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	ctx = tool.WithToolExecutionScope(ctx, userScope)
	envelope := json.RawMessage(`{"operation":"tasks.list_remaining","input":{"limit":3}}`)
	child, childRun, _, _, err := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, run.RunID, "mio", "mio", requestID, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if child.NativePlanningInvocation.DataScope != "user" || child.NativePlanningInvocation.AuthenticatedUserID != "user-a" {
		t.Fatalf("user scope was not pinned: %+v", child.NativePlanningInvocation)
	}
	childCtx, err := BindNativePlanningInvocationContext(ctx, child, childRun)
	if err != nil {
		t.Fatal(err)
	}
	derived, ok := tool.ToolExecutionScopeFromContext(childCtx)
	if !ok || derived.ActorID != "mio" || derived.AuthenticatedUserID != "user-a" || !derived.Allows(tool.DataScopeUser) || derived.Allows(tool.DataScopeInternal) {
		t.Fatalf("derived planning scope=%+v found=%v", derived, ok)
	}
	changedUserScope, err := tool.NewToolExecutionScope(string(requestID), tool.ActorKindAgent, "mio", "user-b", []string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := manager.EnsureNativePlanningInvocation(tool.WithToolExecutionScope(ctx, changedUserScope), parent.TaskID, run.RunID, "mio", "mio", requestID, envelope); err == nil {
		t.Fatal("receipt replay accepted a changed authenticated user scope")
	}
}

func TestCompleteNativePlanningInvocationAcceptsMaximumPersistedResponseAndReplays(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	parent, parentRun, ctx, requestID := nativePlanningParent(t, manager, "mio", false)
	envelope := json.RawMessage(`{"operation":"tasks.capabilities","input":{}}`)
	child, run, _, _, err := manager.EnsureNativePlanningInvocation(ctx, parent.TaskID, parentRun.RunID, "mio", "mio", requestID, envelope)
	if err != nil {
		t.Fatal(err)
	}
	response := strings.Repeat("x", 48<<10)
	if err := manager.CompleteNativePlanningInvocation(ctx, child.TaskID, run.RunID, parent.TaskID, parentRun.RunID, "mio", "mio", response); err != nil {
		t.Fatal(err)
	}
	if err := manager.CompleteNativePlanningInvocation(ctx, child.TaskID, run.RunID, parent.TaskID, parentRun.RunID, "mio", "mio", response); err != nil {
		t.Fatalf("exact maximum response replay: %v", err)
	}
	if err := manager.CompleteNativePlanningInvocation(ctx, child.TaskID, run.RunID, parent.TaskID, parentRun.RunID, "mio", "mio", response+"x"); err == nil {
		t.Fatal("oversized response unexpectedly replaced the canonical result")
	}
}

func nativePlanningParent(t *testing.T, manager *Manager, actor string, readOnly bool) (domaintask.Task, domaintask.Run, context.Context, modulecore.RequestID) {
	t.Helper()
	parent, run, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Title: "native parent", Route: domaintask.RouteGeneral, Assignee: actor, ReadOnly: readOnly}, domaintask.SharedRoleContext{CurrentPlan: "frozen parent plan"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), parent.TaskID, run.RunID, "")
	if err != nil {
		t.Fatal(err)
	}
	requestID := modulecore.NewRequestID()
	scope, err := tool.NewToolExecutionScope(string(requestID), tool.ActorKindAgent, actor, "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return parent, run, tool.WithToolExecutionScope(ctx, scope), requestID
}

func nativePlanningOperationIdentity(parentTaskID modulecore.TaskID, parentRunID modulecore.RunID, actor string, requestID modulecore.RequestID, envelope []byte) (string, string) {
	key := string(parentTaskID) + "\x00" + string(parentRunID) + "\x00" + actor + "\x00task_center_planning\x00" + string(requestID)
	opDigest := sha256.Sum256([]byte(key))
	inputDigest := sha256.Sum256(envelope)
	requestDigest := sha256.Sum256([]byte(key + "\x00" + hex.EncodeToString(inputDigest[:])))
	return "native-planning:" + hex.EncodeToString(opDigest[:]), hex.EncodeToString(requestDigest[:])
}
