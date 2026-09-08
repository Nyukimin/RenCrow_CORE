package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	execrepo "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type fakeRunner struct {
	metas         []tool.ToolMetadata
	resp          *tool.ToolResponse
	err           error
	returnNil     bool
	seenActionID  modulecore.ActionID
	seenAttemptID modulecore.AttemptID
	seenBoundPair bool
	calls         int
}

func (f *fakeRunner) ExecuteV2(ctx context.Context, _ string, _ map[string]any) (*tool.ToolResponse, error) {
	f.calls++
	f.seenActionID, f.seenAttemptID, f.seenBoundPair = domainexecution.BoundActionAttemptFromContext(ctx)
	if f.returnNil {
		return nil, f.err
	}
	if f.resp == nil && f.err == nil {
		return tool.NewSuccess("ok"), nil
	}
	return f.resp, f.err
}

func (f *fakeRunner) ListTools(_ context.Context) ([]tool.ToolMetadata, error) {
	return f.metas, nil
}

type metadataRunner struct {
	mu           sync.RWMutex
	metas        []tool.ToolMetadata
	listErr      error
	listCalls    int
	executeCalls int
}

func (r *metadataRunner) ExecuteV2(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
	r.mu.Lock()
	r.executeCalls++
	r.mu.Unlock()
	return tool.NewSuccess("ok"), nil
}

func (r *metadataRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	if ctx == nil {
		return nil, errors.New("metadata context is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]tool.ToolMetadata(nil), r.metas...), nil
}

func (r *metadataRunner) setMetadata(metas []tool.ToolMetadata) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metas = append([]tool.ToolMetadata(nil), metas...)
}

func (r *metadataRunner) setListError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listErr = err
}

func (r *metadataRunner) counts() (listCalls, executeCalls int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listCalls, r.executeCalls
}

func newTestPolicyRunner(t *testing.T, inner tool.RunnerV2, engine *PolicyEngine, repo domainexecution.Repository) *PolicyRunner {
	runner, _ := newTestPolicyRunnerWithActions(t, inner, engine, repo)
	return runner
}

func newTestPolicyRunnerWithActions(t *testing.T, inner tool.RunnerV2, engine *PolicyEngine, repo domainexecution.Repository) (*PolicyRunner, *actionmanager.Manager) {
	t.Helper()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("action store init failed: %v", err)
	}
	actions := actionmanager.New(store)
	runner, err := NewPolicyRunner(inner, engine, repo, actions, "test")
	if err != nil {
		t.Fatalf("NewPolicyRunner failed: %v", err)
	}
	return runner, actions
}

func TestPolicyRunner_DenyBlockedCommand(t *testing.T) {
	repo, err := execrepo.NewJSONLRepository(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatalf("repo init failed: %v", err)
	}

	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	engine := NewPolicyEngine(PolicyConfig{DenyCommands: []string{"rm -rf"}})
	runner := newTestPolicyRunner(t, inner, engine, repo)

	taskID := modulecore.NewTaskID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}
	resp, err := runner.ExecuteV2(ctx, "shell", map[string]any{"command": "rm -rf /tmp/x"})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != tool.ErrPermissionDenied {
		t.Fatalf("expected permission denied, got %+v", resp)
	}

	counts, err := repo.CountByStatus(context.Background())
	if err != nil {
		t.Fatalf("CountByStatus failed: %v", err)
	}
	if counts[domainexecution.StatusDenied] == 0 {
		t.Fatalf("expected denied count > 0, got %v", counts)
	}
}

func TestPolicyRunner_DeniesMediatedFileWriteOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")

	inner := tools.NewToolRunner(tools.ToolRunnerConfig{DisableToolHarness: true})
	engine := NewPolicyEngine(PolicyConfig{
		Workspace:         workspace,
		WorkspaceEnforced: true,
	})
	policyRunner := newTestPolicyRunner(t, inner, engine, nil)
	runner := tools.NewToolHarnessRunner(policyRunner, nil)

	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}
	resp, err := runner.ExecuteV2(ctx, "file_write", map[string]any{
		"args": map[string]any{
			"path":    outside,
			"content": "blocked",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != tool.ErrPermissionDenied {
		t.Fatalf("expected permission denied, got %+v", resp)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside file should not be created, stat err=%v", err)
	}
}

func TestPolicyRunner_RefreshesToolMetadataAfterDynamicRegistration(t *testing.T) {
	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	engine := NewPolicyEngine(PolicyConfig{})
	runner := newTestPolicyRunner(t, inner, engine, nil)

	inner.metas = append(inner.metas, tool.ToolMetadata{ToolID: "subagent"})
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}
	resp, err := runner.ExecuteV2(ctx, "subagent", map[string]any{})
	if err != nil {
		t.Fatalf("ExecuteV2 should refresh dynamic metadata: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected tool error: %+v", resp.Error)
	}
}

func TestPolicyRunnerRejectsRemovedToolFromCurrentMetadata(t *testing.T) {
	inner := &metadataRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	runner, actions := newTestPolicyRunnerWithActions(t, inner, NewPolicyEngine(PolicyConfig{}), nil)
	inner.setMetadata(nil)

	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}
	if _, err := runner.ExecuteV2(ctx, "shell", map[string]any{}); err == nil || err.Error() != "unknown tool: shell" {
		t.Fatalf("ExecuteV2 error = %v, want current metadata rejection", err)
	}
	if _, got := inner.counts(); got != 0 {
		t.Fatalf("inner execute calls = %d, want zero", got)
	}
	owned, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil {
		t.Fatalf("ListActions failed: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("actions after removed tool = %#v, want none", owned)
	}
}

func TestPolicyRunnerPreservesCurrentMetadataListError(t *testing.T) {
	listErr := errors.New("metadata unavailable")
	inner := &metadataRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	runner, actions := newTestPolicyRunnerWithActions(t, inner, NewPolicyEngine(PolicyConfig{}), nil)
	inner.setListError(listErr)

	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}
	if _, err := runner.ExecuteV2(ctx, "shell", map[string]any{}); !errors.Is(err, listErr) {
		t.Fatalf("ExecuteV2 error = %v, want wrapped metadata error", err)
	}
	if _, got := inner.counts(); got != 0 {
		t.Fatalf("inner execute calls = %d, want zero", got)
	}
	owned, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil {
		t.Fatalf("ListActions failed: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("actions after metadata error = %#v, want none", owned)
	}
}

func TestPolicyRunnerQueriesCurrentMetadataForParallelExecutions(t *testing.T) {
	inner := &metadataRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	runner, _ := newTestPolicyRunnerWithActions(t, inner, NewPolicyEngine(PolicyConfig{}), nil)
	const executions = 16
	errs := make(chan error, executions)
	var wg sync.WaitGroup
	wg.Add(executions)
	for i := 0; i < executions; i++ {
		go func() {
			defer wg.Done()
			ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
			if err != nil {
				errs <- err
				return
			}
			resp, err := runner.ExecuteV2(ctx, "shell", map[string]any{})
			if err != nil {
				errs <- err
				return
			}
			if resp == nil || resp.Error != nil {
				errs <- errors.New("parallel execution returned an invalid response")
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("parallel ExecuteV2 failed: %v", err)
		}
	}
	listCalls, executeCalls := inner.counts()
	if listCalls != executions+1 {
		t.Fatalf("ListTools calls = %d, want constructor plus each execution (%d)", listCalls, executions+1)
	}
	if executeCalls != executions {
		t.Fatalf("inner execute calls = %d, want %d", executeCalls, executions)
	}
}

func TestPolicyRunnerRequiresOwnerProvidedTaskIdentity(t *testing.T) {
	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	engine := NewPolicyEngine(PolicyConfig{DenyCommands: []string{"blocked"}})
	repo := &recordingExecutionRepository{}
	runner := newTestPolicyRunner(t, inner, engine, repo)

	if _, err := runner.ExecuteV2(context.Background(), "shell", map[string]any{}); err == nil {
		t.Fatal("expected missing owner task identity error")
	}
	if _, err := domainexecution.WithIdentity(context.Background(), "legacy", modulecore.NewRunID(), ""); err == nil {
		t.Fatal("expected invalid task identity rejection")
	}

	taskID := modulecore.NewTaskID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}
	if _, err := runner.ExecuteV2(ctx, "shell", map[string]any{"command": "blocked"}); err != nil {
		t.Fatalf("ExecuteV2 failed: %v", err)
	}
	if repo.record.TaskID != taskID || repo.record.TraceID != "" {
		t.Fatalf("record identities = task %q trace %q, want owner task %q and empty trace", repo.record.TaskID, repo.record.TraceID, taskID)
	}
	if err := repo.record.ActionID.Validate(); err != nil {
		t.Fatalf("record action_id must be canonical: %v", err)
	}
}

func TestNewPolicyRunnerRejectsNilActionManager(t *testing.T) {
	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	engine := NewPolicyEngine(PolicyConfig{})
	if _, err := NewPolicyRunner(inner, engine, nil, nil, "test"); err == nil {
		t.Fatal("expected nil action manager rejection")
	}
}

func TestPolicyRunnerUsesBoundActionAttemptWithoutMinting(t *testing.T) {
	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	engine := NewPolicyEngine(PolicyConfig{})
	repo := &recordingExecutionRepository{}
	runner, actions := newTestPolicyRunnerWithActions(t, inner, engine, repo)

	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}
	action, attempt, err := actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: taskID,
		RunID:  runID,
		Kind:   domainaction.KindTool,
		Name:   "shell",
	})
	if err != nil {
		t.Fatalf("CreateAction failed: %v", err)
	}
	ctx, err = domainexecution.WithBoundActionAttempt(ctx, action.ActionID, attempt.AttemptID)
	if err != nil {
		t.Fatalf("WithBoundActionAttempt failed: %v", err)
	}

	resp, err := runner.ExecuteV2(ctx, "shell", map[string]any{"command": "echo ok"})
	if err != nil {
		t.Fatalf("ExecuteV2 failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected tool error: %+v", resp.Error)
	}
	if repo.record.ActionID != action.ActionID || repo.record.AttemptID != attempt.AttemptID {
		t.Fatalf("record ids = action %q attempt %q, want bound action %q attempt %q", repo.record.ActionID, repo.record.AttemptID, action.ActionID, attempt.AttemptID)
	}
	if !inner.seenBoundPair || inner.seenActionID != action.ActionID || inner.seenAttemptID != attempt.AttemptID {
		t.Fatalf("inner bound pair = action %q attempt %q bound=%v, want action %q attempt %q", inner.seenActionID, inner.seenAttemptID, inner.seenBoundPair, action.ActionID, attempt.AttemptID)
	}
	gotAction, err := actions.GetAction(context.Background(), action.ActionID)
	if err != nil {
		t.Fatalf("GetAction failed: %v", err)
	}
	if gotAction.Status != domainaction.StatusOpen {
		t.Fatalf("bound action status = %s, want open for outer owner", gotAction.Status)
	}
	gotAttempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatalf("ListAttempts failed: %v", err)
	}
	if len(gotAttempts) != 1 || gotAttempts[0].Status != domainaction.AttemptStatusRunning {
		t.Fatalf("bound attempts = %#v, want one running attempt for outer owner", gotAttempts)
	}
	if repo.createCalls != 1 {
		t.Fatalf("audit create calls = %d, want one bound execution record", repo.createCalls)
	}
}

func TestPolicyRunnerRejectsInvalidBoundToolAttemptBeforeExecutionOrAudit(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *actionmanager.Manager, modulecore.TaskID, modulecore.RunID, domainaction.Action, domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID)
	}{
		{
			name: "task mismatch",
			setup: func(t *testing.T, actions *actionmanager.Manager, _ modulecore.TaskID, runID modulecore.RunID, _ domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID) {
				action, attempt, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
					TaskID: modulecore.NewTaskID(),
					RunID:  runID,
					Kind:   domainaction.KindTool,
					Name:   "shell",
				})
				if err != nil {
					t.Fatalf("create task mismatch action: %v", err)
				}
				return action.ActionID, attempt.AttemptID
			},
		},
		{
			name: "run mismatch",
			setup: func(t *testing.T, actions *actionmanager.Manager, taskID modulecore.TaskID, _ modulecore.RunID, _ domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID) {
				action, attempt, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
					TaskID: taskID,
					RunID:  modulecore.NewRunID(),
					Kind:   domainaction.KindTool,
					Name:   "shell",
				})
				if err != nil {
					t.Fatalf("create run mismatch action: %v", err)
				}
				return action.ActionID, attempt.AttemptID
			},
		},
		{
			name: "name mismatch",
			setup: func(t *testing.T, actions *actionmanager.Manager, taskID modulecore.TaskID, runID modulecore.RunID, _ domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID) {
				action, attempt, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
					TaskID: taskID,
					RunID:  runID,
					Kind:   domainaction.KindTool,
					Name:   "browser.type",
				})
				if err != nil {
					t.Fatalf("create name mismatch action: %v", err)
				}
				return action.ActionID, attempt.AttemptID
			},
		},
		{
			name: "kind mismatch",
			setup: func(t *testing.T, actions *actionmanager.Manager, taskID modulecore.TaskID, runID modulecore.RunID, _ domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID) {
				action, attempt, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
					TaskID: taskID,
					RunID:  runID,
					Kind:   domainaction.KindLLM,
					Name:   "shell",
				})
				if err != nil {
					t.Fatalf("create kind mismatch action: %v", err)
				}
				return action.ActionID, attempt.AttemptID
			},
		},
		{
			name: "foreign attempt",
			setup: func(t *testing.T, actions *actionmanager.Manager, _ modulecore.TaskID, _ modulecore.RunID, action domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID) {
				_, attempt, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
					TaskID: modulecore.NewTaskID(),
					RunID:  modulecore.NewRunID(),
					Kind:   domainaction.KindTool,
					Name:   action.Name,
				})
				if err != nil {
					t.Fatalf("create foreign action: %v", err)
				}
				return action.ActionID, attempt.AttemptID
			},
		},
		{
			name: "stale attempt",
			setup: func(t *testing.T, actions *actionmanager.Manager, _ modulecore.TaskID, _ modulecore.RunID, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID) {
				if _, _, err := actions.StartAttempt(context.Background(), action.ActionID, domainaction.AttemptStartReasonRetry); err != nil {
					t.Fatalf("start retry: %v", err)
				}
				return action.ActionID, attempt.AttemptID
			},
		},
		{
			name: "closed pair",
			setup: func(t *testing.T, actions *actionmanager.Manager, _ modulecore.TaskID, _ modulecore.RunID, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID) {
				if _, _, err := actions.CompleteAttempt(context.Background(), action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done"); err != nil {
					t.Fatalf("complete pair: %v", err)
				}
				return action.ActionID, attempt.AttemptID
			},
		},
		{
			name: "missing pair",
			setup: func(_ *testing.T, _ *actionmanager.Manager, _ modulecore.TaskID, _ modulecore.RunID, _ domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID) {
				return modulecore.NewActionID(), modulecore.NewAttemptID()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
			repo := &recordingExecutionRepository{}
			runner, actions := newTestPolicyRunnerWithActions(t, inner, NewPolicyEngine(PolicyConfig{}), repo)
			taskID := modulecore.NewTaskID()
			runID := modulecore.NewRunID()
			ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, "")
			if err != nil {
				t.Fatalf("WithIdentity failed: %v", err)
			}
			ownerAction, ownerAttempt, err := actions.CreateAction(ctx, actionmanager.CreateInput{
				TaskID: taskID,
				RunID:  runID,
				Kind:   domainaction.KindTool,
				Name:   "shell",
			})
			if err != nil {
				t.Fatalf("create owner action: %v", err)
			}
			actionID, attemptID := tt.setup(t, actions, taskID, runID, ownerAction, ownerAttempt)
			beforeActions, err := actions.ListActions(context.Background(), domainaction.Filter{})
			if err != nil {
				t.Fatalf("list actions before rejection: %v", err)
			}
			beforeAttempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{})
			if err != nil {
				t.Fatalf("list attempts before rejection: %v", err)
			}
			beforeAuditCalls := repo.createCalls

			boundCtx, err := domainexecution.WithBoundActionAttempt(ctx, actionID, attemptID)
			if err != nil {
				t.Fatalf("WithBoundActionAttempt failed: %v", err)
			}
			if _, err := runner.ExecuteV2(boundCtx, "shell", map[string]any{"command": "echo should-not-run"}); !errors.Is(err, actionmanager.ErrAttemptConflict) {
				t.Fatalf("ExecuteV2 error = %v, want ErrAttemptConflict", err)
			}
			if inner.calls != 0 {
				t.Fatalf("inner calls = %d, want zero", inner.calls)
			}
			if repo.createCalls != beforeAuditCalls {
				t.Fatalf("audit create calls = %d, want unchanged at %d", repo.createCalls, beforeAuditCalls)
			}
			afterActions, err := actions.ListActions(context.Background(), domainaction.Filter{})
			if err != nil {
				t.Fatalf("list actions after rejection: %v", err)
			}
			afterAttempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{})
			if err != nil {
				t.Fatalf("list attempts after rejection: %v", err)
			}
			if !reflect.DeepEqual(afterActions, beforeActions) || !reflect.DeepEqual(afterAttempts, beforeAttempts) {
				t.Fatalf("owner action state changed after rejection: actions before=%#v after=%#v attempts before=%#v after=%#v", beforeActions, afterActions, beforeAttempts, afterAttempts)
			}
		})
	}
}

func TestPolicyRunnerCompletesOwnedActionAttemptForEachOutcome(t *testing.T) {
	tests := []struct {
		name              string
		engine            *PolicyEngine
		response          *tool.ToolResponse
		toolErr           error
		returnNil         bool
		wantErr           bool
		wantResponseCode  tool.ErrorCode
		wantActionStatus  domainaction.Status
		wantAttemptStatus domainaction.AttemptStatus
		wantCalls         int
	}{
		{
			name:              "success",
			response:          tool.NewSuccess("ok"),
			wantActionStatus:  domainaction.StatusSucceeded,
			wantAttemptStatus: domainaction.AttemptStatusSucceeded,
			wantCalls:         1,
		},
		{
			name:              "structured failure",
			response:          tool.NewError(tool.ErrInternalError, "failed", nil),
			wantResponseCode:  tool.ErrInternalError,
			wantActionStatus:  domainaction.StatusFailed,
			wantAttemptStatus: domainaction.AttemptStatusFailed,
			wantCalls:         1,
		},
		{
			name:              "go failure",
			toolErr:           errors.New("executor failed"),
			returnNil:         true,
			wantErr:           true,
			wantActionStatus:  domainaction.StatusFailed,
			wantAttemptStatus: domainaction.AttemptStatusFailed,
			wantCalls:         1,
		},
		{
			name:              "timeout",
			toolErr:           context.DeadlineExceeded,
			returnNil:         true,
			wantErr:           true,
			wantActionStatus:  domainaction.StatusFailed,
			wantAttemptStatus: domainaction.AttemptStatusTimedOut,
			wantCalls:         1,
		},
		{
			name:              "cancel",
			toolErr:           context.Canceled,
			returnNil:         true,
			wantErr:           true,
			wantActionStatus:  domainaction.StatusCancelled,
			wantAttemptStatus: domainaction.AttemptStatusCancelled,
			wantCalls:         1,
		},
		{
			name:              "deny",
			engine:            NewPolicyEngine(PolicyConfig{DenyCommands: []string{"blocked"}}),
			wantResponseCode:  tool.ErrPermissionDenied,
			wantActionStatus:  domainaction.StatusFailed,
			wantAttemptStatus: domainaction.AttemptStatusFailed,
		},
		{
			name:              "nil response",
			returnNil:         true,
			wantResponseCode:  tool.ErrInternalError,
			wantActionStatus:  domainaction.StatusFailed,
			wantAttemptStatus: domainaction.AttemptStatusFailed,
			wantCalls:         1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeRunner{
				metas:     []tool.ToolMetadata{{ToolID: "shell"}},
				resp:      tt.response,
				err:       tt.toolErr,
				returnNil: tt.returnNil,
			}
			engine := tt.engine
			if engine == nil {
				engine = NewPolicyEngine(PolicyConfig{})
			}
			runner, actions := newTestPolicyRunnerWithActions(t, inner, engine, nil)
			taskID := modulecore.NewTaskID()
			ctx, err := domainexecution.WithIdentity(context.Background(), taskID, modulecore.NewRunID(), "")
			if err != nil {
				t.Fatalf("WithIdentity failed: %v", err)
			}

			args := map[string]any{"command": "echo ok"}
			if tt.name == "deny" {
				args = map[string]any{"command": "blocked now"}
			}
			resp, runErr := runner.ExecuteV2(ctx, "shell", args)
			if tt.wantErr && runErr == nil {
				t.Fatal("ExecuteV2 unexpectedly succeeded")
			}
			if !tt.wantErr && runErr != nil {
				t.Fatalf("ExecuteV2 failed: %v", runErr)
			}
			if tt.wantResponseCode != "" {
				if resp == nil || resp.Error == nil || resp.Error.Code != tt.wantResponseCode {
					t.Fatalf("response = %#v, want error code %s", resp, tt.wantResponseCode)
				}
			}
			if inner.calls != tt.wantCalls {
				t.Fatalf("inner calls = %d, want %d", inner.calls, tt.wantCalls)
			}

			owned, err := actions.ListActions(context.Background(), domainaction.Filter{TaskID: taskID})
			if err != nil {
				t.Fatalf("ListActions failed: %v", err)
			}
			if len(owned) != 1 {
				t.Fatalf("owned actions = %#v, want one action", owned)
			}
			if owned[0].Status != tt.wantActionStatus {
				t.Fatalf("action status = %s, want %s", owned[0].Status, tt.wantActionStatus)
			}
			attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: owned[0].ActionID})
			if err != nil {
				t.Fatalf("ListAttempts failed: %v", err)
			}
			if len(attempts) != 1 || attempts[0].Status != tt.wantAttemptStatus {
				t.Fatalf("attempts = %#v, want one status %s", attempts, tt.wantAttemptStatus)
			}
			if inner.calls > 0 && (!inner.seenBoundPair || inner.seenActionID != owned[0].ActionID || inner.seenAttemptID != attempts[0].AttemptID) {
				t.Fatalf("inner bound pair = action %q attempt %q bound=%v, want action %q attempt %q", inner.seenActionID, inner.seenAttemptID, inner.seenBoundPair, owned[0].ActionID, attempts[0].AttemptID)
			}
		})
	}
}

func TestPolicyRunnerCreateSaveFailurePropagatesWithoutRetry(t *testing.T) {
	saveErr := errors.New("action save failed")
	store := newFailingActionStore()
	store.saveActionErr = saveErr
	actions := actionmanager.New(store)
	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	runner, err := NewPolicyRunner(inner, NewPolicyEngine(PolicyConfig{}), nil, actions, "test")
	if err != nil {
		t.Fatalf("NewPolicyRunner failed: %v", err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}

	if _, err := runner.ExecuteV2(ctx, "shell", map[string]any{"command": "echo ok"}); !errors.Is(err, saveErr) {
		t.Fatalf("ExecuteV2 error = %v, want action save error", err)
	}
	if inner.calls != 0 {
		t.Fatalf("inner calls = %d, want no retry after action save failure", inner.calls)
	}
	if store.saveActionCalls != 1 || store.saveAttemptCalls != 0 {
		t.Fatalf("save calls = action %d attempt %d, want one action save and no attempt save retry", store.saveActionCalls, store.saveAttemptCalls)
	}
	if len(store.actions) != 0 || len(store.attempts) != 0 {
		t.Fatalf("store after failed create = actions=%#v attempts=%#v, want no persisted action pair", store.actions, store.attempts)
	}
}

func TestPolicyRunnerCompletionSaveFailurePropagatesWithoutRetry(t *testing.T) {
	completionErr := errors.New("attempt save failed")
	store := newFailingActionStore()
	store.failAttemptAfter = 1
	store.saveAttemptErr = completionErr
	actions := actionmanager.New(store)
	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	runner, err := NewPolicyRunner(inner, NewPolicyEngine(PolicyConfig{}), nil, actions, "test")
	if err != nil {
		t.Fatalf("NewPolicyRunner failed: %v", err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}

	response, err := runner.ExecuteV2(ctx, "shell", map[string]any{"command": "echo ok"})
	if !errors.Is(err, completionErr) {
		t.Fatalf("ExecuteV2 error = %v, want completion save error", err)
	}
	if response == nil || response.Error != nil {
		t.Fatalf("response = %#v, want original successful response", response)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want one execution and no retry", inner.calls)
	}
	if store.saveAttemptCalls != 2 {
		t.Fatalf("attempt save calls = %d, want create plus one completion attempt", store.saveAttemptCalls)
	}
}

type failingActionStore struct {
	actions          map[modulecore.ActionID]domainaction.Action
	attempts         map[modulecore.AttemptID]domainaction.Attempt
	saveActionErr    error
	saveAttemptErr   error
	failAttemptAfter int
	saveActionCalls  int
	saveAttemptCalls int
}

func newFailingActionStore() *failingActionStore {
	return &failingActionStore{
		actions:  make(map[modulecore.ActionID]domainaction.Action),
		attempts: make(map[modulecore.AttemptID]domainaction.Attempt),
	}
}

func (s *failingActionStore) Transaction(ctx context.Context, callback func(actionmanager.Store) error) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx := &failingActionTx{
		parent:   s,
		actions:  clonePolicyActions(s.actions),
		attempts: clonePolicyAttempts(s.attempts),
	}
	if err := callback(tx); err != nil {
		return err
	}
	s.actions = tx.actions
	s.attempts = tx.attempts
	return nil
}

func (s *failingActionStore) ReadTransaction(ctx context.Context, callback func(actionmanager.Store) error) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx := &failingActionTx{
		parent:   s,
		actions:  clonePolicyActions(s.actions),
		attempts: clonePolicyAttempts(s.attempts),
		readOnly: true,
	}
	return callback(&readOnlyFailingActionTx{failingActionTx: tx})
}

type failingActionTx struct {
	parent   *failingActionStore
	actions  map[modulecore.ActionID]domainaction.Action
	attempts map[modulecore.AttemptID]domainaction.Attempt
	readOnly bool
}

func (s *failingActionTx) Transaction(_ context.Context, callback func(actionmanager.Store) error) error {
	if s.readOnly {
		return errors.New("action transaction is read-only")
	}
	return callback(s)
}

func (s *failingActionTx) ReadTransaction(_ context.Context, callback func(actionmanager.Store) error) error {
	return callback(s)
}

func (s *failingActionTx) SaveAction(ctx context.Context, action domainaction.Action) error {
	if s.readOnly {
		return errors.New("action transaction is read-only")
	}
	s.parent.saveActionCalls++
	if s.parent.saveActionErr != nil {
		return s.parent.saveActionErr
	}
	if err := action.Validate(); err != nil {
		return err
	}
	s.actions[action.ActionID] = action
	return nil
}

func (s *failingActionTx) GetAction(_ context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	action, ok := s.actions[actionID]
	if !ok {
		return domainaction.Action{}, domainaction.ErrNotFound
	}
	return action, nil
}

func (s *failingActionTx) ListActions(_ context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	result := make([]domainaction.Action, 0, len(s.actions))
	for _, action := range s.actions {
		if filter.TaskID != "" && action.TaskID != filter.TaskID {
			continue
		}
		if filter.RunID != "" && action.RunID != filter.RunID {
			continue
		}
		if filter.Status != "" && action.Status != filter.Status {
			continue
		}
		result = append(result, action)
	}
	return result, nil
}

func (s *failingActionTx) SaveAttempt(ctx context.Context, attempt domainaction.Attempt) error {
	if s.readOnly {
		return errors.New("action transaction is read-only")
	}
	s.parent.saveAttemptCalls++
	if s.parent.saveAttemptErr != nil && (s.parent.failAttemptAfter == 0 || s.parent.saveAttemptCalls > s.parent.failAttemptAfter) {
		return s.parent.saveAttemptErr
	}
	if err := attempt.Validate(); err != nil {
		return err
	}
	if _, ok := s.actions[attempt.ActionID]; !ok {
		return domainaction.ErrNotFound
	}
	s.attempts[attempt.AttemptID] = attempt
	return nil
}

func (s *failingActionTx) GetAttempt(_ context.Context, attemptID modulecore.AttemptID) (domainaction.Attempt, error) {
	attempt, ok := s.attempts[attemptID]
	if !ok {
		return domainaction.Attempt{}, domainaction.ErrNotFound
	}
	return attempt, nil
}

func (s *failingActionTx) ListAttempts(_ context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	result := make([]domainaction.Attempt, 0, len(s.attempts))
	for _, attempt := range s.attempts {
		if filter.ActionID != "" && attempt.ActionID != filter.ActionID {
			continue
		}
		if filter.Status != "" && attempt.Status != filter.Status {
			continue
		}
		result = append(result, attempt)
	}
	return result, nil
}

type readOnlyFailingActionTx struct {
	*failingActionTx
}

func (s *readOnlyFailingActionTx) SaveAction(context.Context, domainaction.Action) error {
	return errors.New("action transaction is read-only")
}

func (s *readOnlyFailingActionTx) SaveAttempt(context.Context, domainaction.Attempt) error {
	return errors.New("action transaction is read-only")
}

func (s *readOnlyFailingActionTx) Transaction(_ context.Context, _ func(actionmanager.Store) error) error {
	return errors.New("action transaction is read-only")
}

func (s *readOnlyFailingActionTx) ReadTransaction(_ context.Context, callback func(actionmanager.Store) error) error {
	return callback(s)
}

func clonePolicyActions(values map[modulecore.ActionID]domainaction.Action) map[modulecore.ActionID]domainaction.Action {
	result := make(map[modulecore.ActionID]domainaction.Action, len(values))
	for id, value := range values {
		result[id] = value
	}
	return result
}

func clonePolicyAttempts(values map[modulecore.AttemptID]domainaction.Attempt) map[modulecore.AttemptID]domainaction.Attempt {
	result := make(map[modulecore.AttemptID]domainaction.Attempt, len(values))
	for id, value := range values {
		result[id] = value
	}
	return result
}

func (s *failingActionStore) SaveAction(_ context.Context, action domainaction.Action) error {
	s.saveActionCalls++
	if s.saveActionErr != nil {
		return s.saveActionErr
	}
	s.actions[action.ActionID] = action
	return nil
}

func (s *failingActionStore) GetAction(_ context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	action, ok := s.actions[actionID]
	if !ok {
		return domainaction.Action{}, domainaction.ErrNotFound
	}
	return action, nil
}

func (s *failingActionStore) ListActions(_ context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	result := make([]domainaction.Action, 0, len(s.actions))
	for _, action := range s.actions {
		if filter.TaskID != "" && action.TaskID != filter.TaskID {
			continue
		}
		if filter.RunID != "" && action.RunID != filter.RunID {
			continue
		}
		if filter.Status != "" && action.Status != filter.Status {
			continue
		}
		result = append(result, action)
	}
	return result, nil
}

func (s *failingActionStore) SaveAttempt(_ context.Context, attempt domainaction.Attempt) error {
	s.saveAttemptCalls++
	if s.saveAttemptErr != nil && (s.failAttemptAfter == 0 || s.saveAttemptCalls > s.failAttemptAfter) {
		return s.saveAttemptErr
	}
	s.attempts[attempt.AttemptID] = attempt
	return nil
}

func (s *failingActionStore) GetAttempt(_ context.Context, attemptID modulecore.AttemptID) (domainaction.Attempt, error) {
	attempt, ok := s.attempts[attemptID]
	if !ok {
		return domainaction.Attempt{}, domainaction.ErrNotFound
	}
	return attempt, nil
}

func (s *failingActionStore) ListAttempts(_ context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	result := make([]domainaction.Attempt, 0, len(s.attempts))
	for _, attempt := range s.attempts {
		if filter.ActionID != "" && attempt.ActionID != filter.ActionID {
			continue
		}
		if filter.Status != "" && attempt.Status != filter.Status {
			continue
		}
		result = append(result, attempt)
	}
	return result, nil
}

type recordingExecutionRepository struct {
	record      domainexecution.Record
	records     []domainexecution.Record
	createCalls int
}

func (r *recordingExecutionRepository) Create(_ context.Context, record domainexecution.Record) error {
	r.createCalls++
	r.record = record
	r.records = append(r.records, record)
	return nil
}

func (r *recordingExecutionRepository) UpdateStatus(_ context.Context, taskID modulecore.TaskID, actionID modulecore.ActionID, status domainexecution.Status, errMsg string) (domainexecution.Record, error) {
	r.record.TaskID = taskID
	r.record.ActionID = actionID
	r.record.Status = status
	r.record.Error = errMsg
	return r.record, nil
}

func (r *recordingExecutionRepository) Get(_ context.Context, _ modulecore.TaskID, _ modulecore.ActionID) (domainexecution.Record, error) {
	return r.record, nil
}

func (r *recordingExecutionRepository) CountByStatus(_ context.Context) (map[domainexecution.Status]int, error) {
	return map[domainexecution.Status]int{r.record.Status: 1}, nil
}
