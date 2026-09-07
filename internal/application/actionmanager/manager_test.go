package actionmanager_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestCreateActionRetryPreservesActionID(t *testing.T) {
	t.Parallel()
	manager, _, action, first := newActionManagerFixture(t)
	ctx := context.Background()
	if err := action.ActionID.Validate(); err != nil {
		t.Fatalf("action id: %v", err)
	}
	if first.StartReason != domainaction.AttemptStartReasonFirst {
		t.Fatalf("first reason: %s", first.StartReason)
	}

	retried, second, err := manager.StartAttempt(ctx, action.ActionID, domainaction.AttemptStartReasonRetry)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retried.ActionID != action.ActionID {
		t.Fatalf("ActionID changed on retry: %s -> %s", action.ActionID, retried.ActionID)
	}
	if second.AttemptID == first.AttemptID {
		t.Fatal("AttemptID must change on retry")
	}
	if second.StartReason != domainaction.AttemptStartReasonRetry {
		t.Fatalf("retry reason: %s", second.StartReason)
	}

	attempts, err := manager.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("want 2 attempts, got %d", len(attempts))
	}
	closed, ok := attempts[0], attempts[0].Status == domainaction.AttemptStatusFailed
	if !ok && attempts[0].AttemptID == first.AttemptID {
		t.Fatalf("first attempt not closed: %+v", closed)
	}
	for _, item := range attempts {
		if item.AttemptID == first.AttemptID && item.Status != domainaction.AttemptStatusFailed {
			t.Fatalf("first attempt status: %s", item.Status)
		}
		if item.AttemptID == second.AttemptID && item.Status != domainaction.AttemptStatusRunning {
			t.Fatalf("second attempt status: %s", item.Status)
		}
	}

	completed, finalAttempt, err := manager.CompleteAttempt(ctx, action.ActionID, second.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.Status != domainaction.StatusSucceeded || finalAttempt.Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("complete result action=%s attempt=%s", completed.Status, finalAttempt.Status)
	}
}

func TestCompleteAttemptRejectsStaleRetryResult(t *testing.T) {
	t.Parallel()
	manager, root, action, first := newActionManagerFixture(t)
	ctx := context.Background()
	_, second, err := manager.StartAttempt(ctx, action.ActionID, domainaction.AttemptStartReasonRetry)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	before := captureActionManagerState(t, manager, root)

	_, _, err = manager.CompleteAttempt(ctx, action.ActionID, first.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "stale")
	if !errors.Is(err, actionmanager.ErrAttemptConflict) {
		t.Fatalf("stale completion error = %v, want ErrAttemptConflict", err)
	}
	assertActionManagerStateUnchanged(t, manager, root, before)

	actionAfter, err := manager.GetAction(ctx, action.ActionID)
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if actionAfter.CurrentAttemptID != second.AttemptID || actionAfter.Status != domainaction.StatusOpen {
		t.Fatalf("stale completion changed current action: %+v", actionAfter)
	}
	attempts, err := manager.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	for _, attempt := range attempts {
		switch attempt.AttemptID {
		case first.AttemptID:
			if attempt.Status != domainaction.AttemptStatusFailed {
				t.Fatalf("stale completion changed first attempt: %+v", attempt)
			}
		case second.AttemptID:
			if attempt.Status != domainaction.AttemptStatusRunning {
				t.Fatalf("stale completion changed second attempt: %+v", attempt)
			}
		}
	}
}

func TestCompleteAttemptRejectsForeignAttempt(t *testing.T) {
	t.Parallel()
	manager, root, action, _ := newActionManagerFixture(t)
	ctx := context.Background()
	foreignAction, foreignAttempt, err := manager.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: modulecore.NewTaskID(),
		RunID:  modulecore.NewRunID(),
		Kind:   domainaction.KindTool,
		Name:   "browser.type",
	})
	if err != nil {
		t.Fatalf("create foreign action: %v", err)
	}
	before := captureActionManagerState(t, manager, root)

	_, _, err = manager.CompleteAttempt(ctx, action.ActionID, foreignAttempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "foreign")
	if !errors.Is(err, actionmanager.ErrAttemptConflict) {
		t.Fatalf("foreign completion error = %v, want ErrAttemptConflict", err)
	}
	assertActionManagerStateUnchanged(t, manager, root, before)

	foreignAfter, err := manager.GetAction(ctx, foreignAction.ActionID)
	if err != nil {
		t.Fatalf("get foreign action: %v", err)
	}
	if foreignAfter.Status != domainaction.StatusOpen || foreignAfter.CurrentAttemptID != foreignAttempt.AttemptID {
		t.Fatalf("foreign action changed: %+v", foreignAfter)
	}
}

func TestCompleteAttemptRejectsInvalidActionStatusWithoutMutation(t *testing.T) {
	t.Parallel()
	manager, root, action, attempt := newActionManagerFixture(t)
	ctx := context.Background()
	before := captureActionManagerState(t, manager, root)

	_, _, err := manager.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.Status("invalid"), "bad action status")
	if err == nil {
		t.Fatal("invalid action status unexpectedly completed")
	}
	assertActionManagerStateUnchanged(t, manager, root, before)
}

func TestCompleteAttemptRejectsNonterminalAttemptStatusWithoutMutation(t *testing.T) {
	t.Parallel()
	manager, root, action, attempt := newActionManagerFixture(t)
	ctx := context.Background()
	before := captureActionManagerState(t, manager, root)

	_, _, err := manager.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusRunning, domainaction.StatusSucceeded, "running")
	if err == nil {
		t.Fatal("nonterminal attempt status unexpectedly completed")
	}
	assertActionManagerStateUnchanged(t, manager, root, before)
}

func TestCompleteAttemptRejectsRepeatWithoutMutation(t *testing.T) {
	t.Parallel()
	manager, root, action, attempt := newActionManagerFixture(t)
	ctx := context.Background()
	if _, _, err := manager.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done"); err != nil {
		t.Fatalf("first completion: %v", err)
	}
	before := captureActionManagerState(t, manager, root)

	_, _, err := manager.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "again")
	if !errors.Is(err, actionmanager.ErrAttemptConflict) {
		t.Fatalf("repeat completion error = %v, want ErrAttemptConflict", err)
	}
	assertActionManagerStateUnchanged(t, manager, root, before)
}

func TestValidateToolAttemptRequiresOwnedCurrentActiveToolPair(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *actionmanager.Manager, domainaction.Action, domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string)
		valid bool
	}{
		{
			name: "owner pair",
			setup: func(_ *testing.T, _ *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				return action.ActionID, attempt.AttemptID, action.TaskID, action.RunID, action.Name
			},
			valid: true,
		},
		{
			name: "task mismatch",
			setup: func(_ *testing.T, _ *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				return action.ActionID, attempt.AttemptID, modulecore.NewTaskID(), action.RunID, action.Name
			},
		},
		{
			name: "run mismatch",
			setup: func(_ *testing.T, _ *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				return action.ActionID, attempt.AttemptID, action.TaskID, modulecore.NewRunID(), action.Name
			},
		},
		{
			name: "name mismatch",
			setup: func(_ *testing.T, _ *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				return action.ActionID, attempt.AttemptID, action.TaskID, action.RunID, "browser.type"
			},
		},
		{
			name: "noncanonical tool name",
			setup: func(_ *testing.T, _ *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				return action.ActionID, attempt.AttemptID, action.TaskID, action.RunID, " browser.click "
			},
		},
		{
			name: "empty tool name",
			setup: func(_ *testing.T, _ *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				return action.ActionID, attempt.AttemptID, action.TaskID, action.RunID, ""
			},
		},
		{
			name: "kind mismatch",
			setup: func(t *testing.T, manager *actionmanager.Manager, _ domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				other, otherAttempt, err := manager.CreateAction(context.Background(), actionmanager.CreateInput{
					TaskID: modulecore.NewTaskID(),
					RunID:  modulecore.NewRunID(),
					Kind:   domainaction.KindLLM,
					Name:   "browser.click",
				})
				if err != nil {
					t.Fatalf("create non-tool action: %v", err)
				}
				return other.ActionID, otherAttempt.AttemptID, other.TaskID, other.RunID, other.Name
			},
		},
		{
			name: "foreign attempt",
			setup: func(t *testing.T, manager *actionmanager.Manager, action domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				_, foreignAttempt, err := manager.CreateAction(context.Background(), actionmanager.CreateInput{
					TaskID: modulecore.NewTaskID(),
					RunID:  modulecore.NewRunID(),
					Kind:   domainaction.KindTool,
					Name:   action.Name,
				})
				if err != nil {
					t.Fatalf("create foreign action: %v", err)
				}
				return action.ActionID, foreignAttempt.AttemptID, action.TaskID, action.RunID, action.Name
			},
		},
		{
			name: "stale attempt",
			setup: func(t *testing.T, manager *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				if _, _, err := manager.StartAttempt(context.Background(), action.ActionID, domainaction.AttemptStartReasonRetry); err != nil {
					t.Fatalf("start retry: %v", err)
				}
				return action.ActionID, attempt.AttemptID, action.TaskID, action.RunID, action.Name
			},
		},
		{
			name: "closed pair",
			setup: func(t *testing.T, manager *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				if _, _, err := manager.CompleteAttempt(context.Background(), action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done"); err != nil {
					t.Fatalf("complete pair: %v", err)
				}
				return action.ActionID, attempt.AttemptID, action.TaskID, action.RunID, action.Name
			},
		},
		{
			name: "missing pair",
			setup: func(_ *testing.T, _ *actionmanager.Manager, action domainaction.Action, _ domainaction.Attempt) (modulecore.ActionID, modulecore.AttemptID, modulecore.TaskID, modulecore.RunID, string) {
				return modulecore.NewActionID(), modulecore.NewAttemptID(), action.TaskID, action.RunID, action.Name
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, root, action, attempt := newActionManagerFixture(t)
			actionID, attemptID, taskID, runID, toolName := tt.setup(t, manager, action, attempt)
			before := captureActionManagerState(t, manager, root)
			err := manager.ValidateToolAttempt(context.Background(), actionID, attemptID, taskID, runID, toolName)
			if tt.valid {
				if err != nil {
					t.Fatalf("ValidateToolAttempt() error = %v", err)
				}
				return
			}
			if !errors.Is(err, actionmanager.ErrAttemptConflict) {
				t.Fatalf("ValidateToolAttempt() error = %v, want ErrAttemptConflict", err)
			}
			assertActionManagerStateUnchanged(t, manager, root, before)
		})
	}
}

func TestValidateToolAttemptRejectsMalformedStoredPair(t *testing.T) {
	t.Parallel()
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	actionID := modulecore.NewActionID()
	attemptID := modulecore.NewAttemptID()
	now := time.Now().UTC()
	validAction := domainaction.Action{
		ActionID:         actionID,
		TaskID:           taskID,
		RunID:            runID,
		Kind:             domainaction.KindTool,
		Name:             "shell",
		Status:           domainaction.StatusOpen,
		CurrentAttemptID: attemptID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	validAttempt := domainaction.Attempt{
		AttemptID:   attemptID,
		ActionID:    actionID,
		StartReason: domainaction.AttemptStartReasonFirst,
		Status:      domainaction.AttemptStatusRunning,
		StartedAt:   now,
	}

	for _, tt := range []struct {
		name    string
		action  domainaction.Action
		attempt domainaction.Attempt
		want    string
	}{
		{name: "action", action: func() domainaction.Action { item := validAction; item.Kind = domainaction.Kind("invalid"); return item }(), attempt: validAttempt, want: "invalid action kind"},
		{name: "attempt", action: validAction, attempt: func() domainaction.Attempt {
			item := validAttempt
			item.Status = domainaction.AttemptStatus("invalid")
			return item
		}(), want: "invalid attempt status"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := actionmanager.New(&malformedPairStore{action: tt.action, attempt: tt.attempt})
			err := manager.ValidateToolAttempt(context.Background(), actionID, attemptID, taskID, runID, "shell")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateToolAttempt() error = %v, want %q", err, tt.want)
			}
		})
	}
}

type malformedPairStore struct {
	actionmanager.Store
	action  domainaction.Action
	attempt domainaction.Attempt
}

func (s *malformedPairStore) GetAction(context.Context, modulecore.ActionID) (domainaction.Action, error) {
	return s.action, nil
}

func (s *malformedPairStore) GetAttempt(context.Context, modulecore.AttemptID) (domainaction.Attempt, error) {
	return s.attempt, nil
}

func TestCompleteToolAttemptMapsTerminalStates(t *testing.T) {
	t.Parallel()
	canceledContext := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	deadlineContext := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		cancel()
		return ctx
	}
	tests := []struct {
		name        string
		ctx         func() context.Context
		response    *tool.ToolResponse
		toolErr     error
		wantAttempt domainaction.AttemptStatus
		wantAction  domainaction.Status
		wantSummary string
	}{
		{
			name:        "success",
			ctx:         context.Background,
			response:    tool.NewSuccess("result"),
			wantAttempt: domainaction.AttemptStatusSucceeded,
			wantAction:  domainaction.StatusSucceeded,
			wantSummary: "tool succeeded",
		},
		{
			name:        "error response",
			ctx:         context.Background,
			response:    tool.NewError(tool.ErrInternalError, "sensitive details", nil),
			wantAttempt: domainaction.AttemptStatusFailed,
			wantAction:  domainaction.StatusFailed,
			wantSummary: "tool failed",
		},
		{
			name:        "go error",
			ctx:         context.Background,
			toolErr:     errors.New("tool execution failed"),
			wantAttempt: domainaction.AttemptStatusFailed,
			wantAction:  domainaction.StatusFailed,
			wantSummary: "tool failed",
		},
		{
			name:        "nil response",
			ctx:         context.Background,
			wantAttempt: domainaction.AttemptStatusFailed,
			wantAction:  domainaction.StatusFailed,
			wantSummary: "tool failed",
		},
		{
			name:        "cancelled",
			ctx:         canceledContext,
			wantAttempt: domainaction.AttemptStatusCancelled,
			wantAction:  domainaction.StatusCancelled,
			wantSummary: "tool cancelled",
		},
		{
			name:        "success after cancellation",
			ctx:         canceledContext,
			response:    tool.NewSuccess("result"),
			wantAttempt: domainaction.AttemptStatusSucceeded,
			wantAction:  domainaction.StatusSucceeded,
			wantSummary: "tool succeeded",
		},
		{
			name:        "deadline",
			ctx:         deadlineContext,
			wantAttempt: domainaction.AttemptStatusTimedOut,
			wantAction:  domainaction.StatusFailed,
			wantSummary: "tool timed out",
		},
		{
			name:        "structured timeout",
			ctx:         context.Background,
			response:    tool.NewError(tool.ErrTimeout, "timed out", nil),
			wantAttempt: domainaction.AttemptStatusTimedOut,
			wantAction:  domainaction.StatusFailed,
			wantSummary: "tool timed out",
		},
		{
			name:        "typed deadline outranks cancellation",
			ctx:         canceledContext,
			toolErr:     context.DeadlineExceeded,
			wantAttempt: domainaction.AttemptStatusTimedOut,
			wantAction:  domainaction.StatusFailed,
			wantSummary: "tool timed out",
		},
		{
			name:        "typed cancellation outranks deadline",
			ctx:         deadlineContext,
			toolErr:     context.Canceled,
			wantAttempt: domainaction.AttemptStatusCancelled,
			wantAction:  domainaction.StatusCancelled,
			wantSummary: "tool cancelled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			manager, _, action, attempt := newActionManagerFixture(t)
			if err := manager.CompleteToolAttempt(tt.ctx(), action.ActionID, attempt.AttemptID, tt.response, tt.toolErr); err != nil {
				t.Fatalf("CompleteToolAttempt() error = %v", err)
			}
			gotAction, err := manager.GetAction(context.Background(), action.ActionID)
			if err != nil {
				t.Fatalf("GetAction() error = %v", err)
			}
			if gotAction.Status != tt.wantAction || gotAction.Summary != tt.wantSummary {
				t.Fatalf("action status/summary = %s/%q, want %s/%q", gotAction.Status, gotAction.Summary, tt.wantAction, tt.wantSummary)
			}
			gotAttempts, err := manager.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
			if err != nil {
				t.Fatalf("ListAttempts() error = %v", err)
			}
			if len(gotAttempts) != 1 {
				t.Fatalf("attempt count = %d, want 1", len(gotAttempts))
			}
			gotAttempt := gotAttempts[0]
			if gotAttempt.AttemptID != attempt.AttemptID || gotAttempt.Status != tt.wantAttempt || gotAttempt.Summary != tt.wantSummary {
				t.Fatalf("attempt = %#v, want id=%s status=%s summary=%q", gotAttempt, attempt.AttemptID, tt.wantAttempt, tt.wantSummary)
			}
		})
	}
}

func TestCompleteToolAttemptCancellationPreservesContextValues(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "actions")
	store, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	observed := &contextObservingStore{Store: store}
	manager := actionmanager.New(observed)
	action, attempt, err := manager.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: modulecore.NewTaskID(),
		RunID:  modulecore.NewRunID(),
		Kind:   domainaction.KindTool,
		Name:   "browser.click",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextValueKey{}, "owner-identity"))
	cancel()

	if err := manager.CompleteToolAttempt(ctx, action.ActionID, attempt.AttemptID, nil, nil); err != nil {
		t.Fatalf("CompleteToolAttempt() error = %v", err)
	}
	if len(observed.values) != 2 || observed.values[0] != "owner-identity" || observed.values[1] != "owner-identity" {
		t.Fatalf("terminal store context values = %#v, want owner identity on SaveAttempt and SaveAction", observed.values)
	}
	gotAction, err := manager.GetAction(context.Background(), action.ActionID)
	if err != nil {
		t.Fatalf("GetAction() error = %v", err)
	}
	if gotAction.Status != domainaction.StatusCancelled {
		t.Fatalf("action status = %s, want cancelled", gotAction.Status)
	}
}

func TestCompleteToolAttemptPropagatesStaleCompletionError(t *testing.T) {
	t.Parallel()
	manager, _, action, first := newActionManagerFixture(t)
	_, second, err := manager.StartAttempt(context.Background(), action.ActionID, domainaction.AttemptStartReasonRetry)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	err = manager.CompleteToolAttempt(context.Background(), action.ActionID, first.AttemptID, tool.NewSuccess("stale"), nil)
	if !errors.Is(err, actionmanager.ErrAttemptConflict) {
		t.Fatalf("stale completion error = %v, want ErrAttemptConflict", err)
	}
	gotAction, err := manager.GetAction(context.Background(), action.ActionID)
	if err != nil {
		t.Fatalf("GetAction() error = %v", err)
	}
	if gotAction.Status != domainaction.StatusOpen || gotAction.CurrentAttemptID != second.AttemptID {
		t.Fatalf("action after stale completion = %#v", gotAction)
	}
}

func TestCompleteToolAttemptRejectsNilContext(t *testing.T) {
	t.Parallel()
	manager, _, action, attempt := newActionManagerFixture(t)
	if err := manager.CompleteToolAttempt(nil, action.ActionID, attempt.AttemptID, tool.NewSuccess("result"), nil); err == nil {
		t.Fatal("nil context unexpectedly accepted")
	}
	gotAction, err := manager.GetAction(context.Background(), action.ActionID)
	if err != nil {
		t.Fatalf("GetAction() error = %v", err)
	}
	if gotAction.Status != domainaction.StatusOpen {
		t.Fatalf("action status after nil context = %s, want open", gotAction.Status)
	}
}

type contextValueKey struct{}

type contextObservingStore struct {
	actionmanager.Store
	values []string
}

func (s *contextObservingStore) SaveAction(ctx context.Context, value domainaction.Action) error {
	s.recordContextValue(ctx)
	return s.Store.SaveAction(ctx, value)
}

func (s *contextObservingStore) SaveAttempt(ctx context.Context, value domainaction.Attempt) error {
	s.recordContextValue(ctx)
	return s.Store.SaveAttempt(ctx, value)
}

func (s *contextObservingStore) recordContextValue(ctx context.Context) {
	if value, ok := ctx.Value(contextValueKey{}).(string); ok {
		s.values = append(s.values, value)
	}
}

type actionManagerState struct {
	actions           []domainaction.Action
	attempts          []domainaction.Attempt
	actionFileLength  int64
	attemptFileLength int64
}

func newActionManagerFixture(t *testing.T) (*actionmanager.Manager, string, domainaction.Action, domainaction.Attempt) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "actions")
	store, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	manager := actionmanager.New(store)
	action, attempt, err := manager.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: modulecore.NewTaskID(),
		RunID:  modulecore.NewRunID(),
		Kind:   domainaction.KindTool,
		Name:   "browser.click",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return manager, root, action, attempt
}

func captureActionManagerState(t *testing.T, manager *actionmanager.Manager, root string) actionManagerState {
	t.Helper()
	actions, err := manager.ListActions(context.Background(), domainaction.Filter{})
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	attempts, err := manager.ListAttempts(context.Background(), domainaction.AttemptFilter{})
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	actionFileLength, attemptFileLength := actionStoreFileLengths(t, root)
	return actionManagerState{
		actions:           actions,
		attempts:          attempts,
		actionFileLength:  actionFileLength,
		attemptFileLength: attemptFileLength,
	}
}

func assertActionManagerStateUnchanged(t *testing.T, manager *actionmanager.Manager, root string, before actionManagerState) {
	t.Helper()
	after := captureActionManagerState(t, manager, root)
	if !reflect.DeepEqual(after.actions, before.actions) {
		t.Fatalf("actions changed after rejected completion:\nbefore=%+v\nafter=%+v", before.actions, after.actions)
	}
	if !reflect.DeepEqual(after.attempts, before.attempts) {
		t.Fatalf("attempts changed after rejected completion:\nbefore=%+v\nafter=%+v", before.attempts, after.attempts)
	}
	if after.actionFileLength != before.actionFileLength || after.attemptFileLength != before.attemptFileLength {
		t.Fatalf("store file lengths changed after rejected completion: before=(%d,%d), after=(%d,%d)", before.actionFileLength, before.attemptFileLength, after.actionFileLength, after.attemptFileLength)
	}
}

func actionStoreFileLengths(t *testing.T, root string) (int64, int64) {
	t.Helper()
	actionInfo, err := os.Stat(filepath.Join(root, "action_state.jsonl"))
	if err != nil {
		t.Fatalf("stat action store: %v", err)
	}
	attemptInfo, err := os.Stat(filepath.Join(root, "action_attempt.jsonl"))
	if err != nil {
		t.Fatalf("stat attempt store: %v", err)
	}
	return actionInfo.Size(), attemptInfo.Size()
}
