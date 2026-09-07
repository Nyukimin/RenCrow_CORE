package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	execrepo "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type fakeRunner struct {
	metas []tool.ToolMetadata
}

func (f *fakeRunner) ExecuteV2(_ context.Context, _ string, _ map[string]any) (*tool.ToolResponse, error) {
	return tool.NewSuccess("ok"), nil
}

func (f *fakeRunner) ListTools(_ context.Context) ([]tool.ToolMetadata, error) {
	return f.metas, nil
}

func newTestPolicyRunner(t *testing.T, inner tool.RunnerV2, engine *PolicyEngine, repo domainexecution.Repository) *PolicyRunner {
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
	return runner
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
	runner := newTestPolicyRunner(t, inner, engine, repo)

	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	actionID := modulecore.NewActionID()
	attemptID := modulecore.NewAttemptID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, "")
	if err != nil {
		t.Fatalf("WithIdentity failed: %v", err)
	}
	ctx, err = domainexecution.WithBoundActionAttempt(ctx, actionID, attemptID)
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
	if repo.record.ActionID != actionID || repo.record.AttemptID != attemptID {
		t.Fatalf("record ids = action %q attempt %q, want bound action %q attempt %q", repo.record.ActionID, repo.record.AttemptID, actionID, attemptID)
	}
}

type recordingExecutionRepository struct {
	record domainexecution.Record
}

func (r *recordingExecutionRepository) Create(_ context.Context, record domainexecution.Record) error {
	r.record = record
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
