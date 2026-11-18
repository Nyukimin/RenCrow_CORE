package task

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPrivateTaskOwnerReadBoundaryHidesGenericReadsAndPreservesPublicTasks(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(map[bool]string{false: "folded", true: "indexed"}[indexed], func(t *testing.T) {
			options := OpenOptions{Index: indexed}
			store, err := NewJSONLStoreWithOptions(t.TempDir(), options)
			if err != nil {
				t.Fatalf("NewJSONLStoreWithOptions: %v", err)
			}
			defer store.Close()
			ctx := context.Background()
			now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
			public := indexedTestTask(1, now)
			if err := store.SaveTask(ctx, public); err != nil {
				t.Fatalf("save legacy public Task: %v", err)
			}
			private := indexedTestTask(2, now.Add(time.Minute))
			private.WorkstreamID = modulecore.NewWorkstreamID()
			private.GoalID = modulecore.NewGoalID()
			private.PrivateAccess = &domaintask.TaskPrivateAccess{
				Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "exact-user-1",
			}
			writeChecks := 0
			if err := store.SaveTaskAuthorized(ctx, private, func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
				writeChecks++
				if input.Operation != domaintask.TaskWriteOperationPersist || input.TaskID != private.TaskID ||
					input.WorkstreamID != private.WorkstreamID || input.GoalID != private.GoalID ||
					input.Scope != domaintask.TaskAccessScopeUser || input.AuthenticatedUserID != "exact-user-1" {
					t.Fatalf("write authorization input = %#v", input)
				}
				return nil
			}); err != nil {
				t.Fatalf("SaveTaskAuthorized: %v", err)
			}
			if writeChecks != 1 {
				t.Fatalf("write authorization calls = %d, want 1", writeChecks)
			}
			otherPrivate := indexedTestTask(4, now.Add(2*time.Minute))
			otherPrivate.WorkstreamID = modulecore.NewWorkstreamID()
			otherPrivate.GoalID = modulecore.NewGoalID()
			otherPrivate.PrivateAccess = &domaintask.TaskPrivateAccess{
				Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "exact-user-2",
			}
			if err := store.SaveTaskAuthorized(ctx, otherPrivate, func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
				if input.TaskID != otherPrivate.TaskID || input.AuthenticatedUserID != "exact-user-2" {
					t.Fatalf("second private Task write authorization input = %#v", input)
				}
				return nil
			}); err != nil {
				t.Fatalf("save second private Task: %v", err)
			}
			operationHash := taskOperationHash("private receipt must stay hidden")
			if _, err := store.ExecuteIdempotentGlobalTaskOperation(ctx, "private-global-receipt", operationHash, func(domaintask.Store) (json.RawMessage, error) {
				return json.Marshal(struct {
					TaskID modulecore.TaskID `json:"task_id"`
				}{TaskID: private.TaskID})
			}); err != nil {
				t.Fatalf("write private-linked global receipt fixture: %v", err)
			}
			if _, found, err := store.LookupGlobalTaskOperation(ctx, "private-global-receipt", operationHash); err != nil || found {
				t.Fatalf("private-linked global receipt lookup = (found %t, err %v), want not found", found, err)
			}
			if _, err := store.LookupTaskOperation(ctx, "private-global-receipt"); !errors.Is(err, ErrTaskOperationNotFound) {
				t.Fatalf("private-linked direct receipt lookup = %v, want hidden not-found", err)
			}
			if _, err := store.GetTask(ctx, private.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("generic GetTask error = %v, want hidden not-found", err)
			}
			if indexed {
				if _, err := store.TaskHistory(ctx, private.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
					t.Fatalf("private TaskHistory error = %v, want hidden not-found", err)
				}
			}
			if _, err := store.GetTaskExecutionFence(ctx, private.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("private execution fence lookup error = %v, want hidden not-found", err)
			}
			listed, err := store.ListTasks(ctx, domaintask.Filter{Limit: 1})
			if err != nil {
				t.Fatalf("generic ListTasks: %v", err)
			}
			if len(listed) != 1 || listed[0].TaskID != public.TaskID {
				t.Fatalf("generic ListTasks(limit=1) = %#v, want only public Task after filtering", listed)
			}
			readChecks := 0
			got, err := store.GetTaskAuthorized(ctx, private.TaskID, func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
				readChecks++
				if input.Resource != domaintask.TaskReadResourceTask || input.TaskID != private.TaskID ||
					input.WorkstreamID != private.WorkstreamID || input.GoalID != private.GoalID ||
					input.Scope != domaintask.TaskAccessScopeUser || input.AuthenticatedUserID != "exact-user-1" {
					t.Fatalf("read authorization input = %#v", input)
				}
				return nil
			})
			if err != nil || got.TaskID != private.TaskID {
				t.Fatalf("authorized GetTask = (%#v, %v)", got, err)
			}
			if readChecks != 1 {
				t.Fatalf("read authorization calls = %d, want 1", readChecks)
			}
			if _, err := store.GetTaskAuthorized(ctx, private.TaskID, func(context.Context, domaintask.TaskReadAuthorizationInput) error {
				return errors.New("not the current Goal owner")
			}); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("unauthorized GetTask error = %v, want hidden not-found", err)
			}
			if err := store.SaveTask(ctx, private); !errors.Is(err, domaintask.ErrPrivateTaskWriteAuthorizationRequired) {
				t.Fatalf("generic SaveTask error = %v, want owner authorization requirement", err)
			}
			writerGeneration, err := store.WriterGeneration()
			if err != nil {
				t.Fatalf("WriterGeneration: %v", err)
			}
			run := domaintask.Run{
				WriterGeneration: writerGeneration, RunID: modulecore.NewRunID(), TaskID: private.TaskID,
				StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now,
			}
			publicRun := domaintask.Run{
				WriterGeneration: writerGeneration, RunID: modulecore.NewRunID(), TaskID: public.TaskID,
				StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now,
			}
			if err := store.SaveRun(ctx, publicRun); err != nil {
				t.Fatalf("save public Run: %v", err)
			}
			if gotRun, err := store.GetRun(ctx, publicRun.RunID); err != nil || gotRun.TaskID != public.TaskID {
				t.Fatalf("public GetRun = (%#v, %v), want public parent %s", gotRun, err, public.TaskID)
			}
			if err := store.SaveRun(ctx, run); !errors.Is(err, domaintask.ErrPrivateTaskWriteAuthorizationRequired) {
				t.Fatalf("generic SaveRun error = %v, want owner authorization requirement", err)
			}
			if err := store.AcquireTaskExecutionFence(ctx, private.TaskID, "private-fence"); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("private fence acquire error = %v, want hidden not-found", err)
			}
			if err := store.SaveRunAuthorized(ctx, run, func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
				if input.Resource != domaintask.TaskWriteResourceRun || input.TaskID != private.TaskID || input.RunID != run.RunID {
					t.Fatalf("Run write authorization input = %#v", input)
				}
				return nil
			}); err != nil {
				t.Fatalf("SaveRunAuthorized: %v", err)
			}
			otherRun := domaintask.Run{
				WriterGeneration: writerGeneration, RunID: modulecore.NewRunID(), TaskID: otherPrivate.TaskID,
				StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now.Add(time.Minute),
			}
			if err := store.SaveRunAuthorized(ctx, otherRun, func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
				if input.TaskID != otherPrivate.TaskID || input.RunID != otherRun.RunID {
					t.Fatalf("second private Run write authorization input = %#v", input)
				}
				return nil
			}); err != nil {
				t.Fatalf("save second private Run: %v", err)
			}
			shared := domaintask.SharedRoleContext{TaskID: private.TaskID, UserIntent: "private context", UpdatedAt: now}
			if _, err := store.GetContext(ctx, private.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("generic GetContext before write error = %v, want hidden not-found", err)
			}
			if err := store.SaveContextAuthorized(ctx, shared, func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
				if input.Resource != domaintask.TaskWriteResourceContext || input.TaskID != private.TaskID || input.GoalID != private.GoalID {
					t.Fatalf("context write authorization input = %#v", input)
				}
				return nil
			}); err != nil {
				t.Fatalf("SaveContextAuthorized: %v", err)
			}
			if err := store.SaveNotification(ctx, domaintask.Notification{TaskID: private.TaskID, Type: "private"}); !errors.Is(err, domaintask.ErrPrivateTaskWriteAuthorizationRequired) {
				t.Fatalf("generic SaveNotification error = %v, want owner authorization requirement", err)
			}
			if _, err := store.GetRun(ctx, run.RunID); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("generic GetRun error = %v, want hidden not-found", err)
			}
			publicRuns, err := store.ListRuns(ctx, domaintask.RunFilter{TaskID: private.TaskID})
			if err != nil || len(publicRuns) != 0 {
				t.Fatalf("generic ListRuns = (%#v, %v), want empty", publicRuns, err)
			}
			if _, err := store.GetContext(ctx, private.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("generic GetContext error = %v, want hidden not-found", err)
			}
			authorizedRunChecks := 0
			gotRun, err := store.GetRunAuthorized(ctx, run.RunID, func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
				authorizedRunChecks++
				if input.Resource != domaintask.TaskReadResourceRun || input.TaskID != private.TaskID || input.RunID != run.RunID {
					t.Fatalf("Run read authorization input = %#v", input)
				}
				return nil
			})
			if err != nil || gotRun.RunID != run.RunID {
				t.Fatalf("authorized GetRun = (%#v, %v)", gotRun, err)
			}
			if gotRun.TaskID != private.TaskID {
				t.Fatalf("authorized GetRun parent = %s, want %s", gotRun.TaskID, private.TaskID)
			}
			if authorizedRunChecks != 2 {
				t.Fatalf("Run source authorization calls = %d, want pre-read and before-return", authorizedRunChecks)
			}
			if _, err := store.GetRunAuthorized(ctx, otherRun.RunID, func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
				if input.TaskID != private.TaskID || input.AuthenticatedUserID != "exact-user-1" {
					return domaintask.ErrPrivateTaskAccessForbidden
				}
				return nil
			}); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("owner-1 read of adjacent owner-2 Run = %v, want hidden not-found", err)
			}
			if gotRun, err := store.GetRunAuthorized(ctx, otherRun.RunID, func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
				if input.TaskID != otherPrivate.TaskID || input.AuthenticatedUserID != "exact-user-2" {
					t.Fatalf("owner-2 Run read authorization input = %#v", input)
				}
				return nil
			}); err != nil || gotRun.TaskID != otherPrivate.TaskID || gotRun.RunID != otherRun.RunID {
				t.Fatalf("owner-2 authorized Run = (%#v, %v), want exact parent/run", gotRun, err)
			}
			contextChecks := 0
			if gotContext, err := store.GetContextAuthorized(ctx, private.TaskID, func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
				contextChecks++
				if input.Resource != domaintask.TaskReadResourceContext || input.TaskID != private.TaskID || input.GoalID != private.GoalID {
					t.Fatalf("context read authorization input = %#v", input)
				}
				return nil
			}); err != nil || gotContext.UserIntent != shared.UserIntent {
				t.Fatalf("authorized GetContext = (%#v, %v)", gotContext, err)
			}
			if contextChecks != 2 {
				t.Fatalf("context source authorization calls = %d, want pre-read and before-return", contextChecks)
			}
			denied := func(context.Context, domaintask.TaskReadAuthorizationInput) error { return errors.New("not owner") }
			if _, err := store.GetContextAuthorized(ctx, private.TaskID, denied); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("denied context read error = %v, want same hidden not-found", err)
			}
			missingContextTask := indexedTestTask(3, now)
			missingContextTask.WorkstreamID = private.WorkstreamID
			missingContextTask.GoalID = modulecore.NewGoalID()
			missingContextTask.PrivateAccess = &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "exact-user-1"}
			if err := store.SaveTaskAuthorized(ctx, missingContextTask, func(context.Context, domaintask.TaskWriteAuthorizationInput) error { return nil }); err != nil {
				t.Fatalf("save task without context: %v", err)
			}
			if _, err := store.GetContextAuthorized(ctx, missingContextTask.TaskID, denied); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("denied missing-context error = %v, want same hidden not-found", err)
			}
			stripped := private
			stripped.PrivateAccess = nil
			stripped.UpdatedAt = now.Add(2 * time.Minute)
			if err := store.SaveTask(ctx, stripped); !errors.Is(err, domaintask.ErrPrivateTaskAccessImmutable) {
				t.Fatalf("private metadata strip error = %v, want immutable metadata error", err)
			}
		})
	}
}
