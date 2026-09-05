package modulebridge

import (
	"context"
	"fmt"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

type fakeWorkerExecutionService struct {
	taskID core.TaskID
	plan   string
	patch  string
	err    error
}

func (s *fakeWorkerExecutionService) ExecuteProposal(_ context.Context, taskID core.TaskID, p *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	s.taskID = taskID
	s.plan = p.Plan()
	s.patch = p.Patch()
	if s.err != nil {
		return nil, s.err
	}
	return patch.NewPatchExecutionResult().WithSummary("実行: 1 件, 成功: 1 件, 失敗: 0 件"), nil
}

func (s *fakeWorkerExecutionService) ExecuteObservation(_ context.Context, _ []service.ObservationAction) ([]service.ObservationActionResult, error) {
	return nil, nil
}

func TestWorkerExecutorAdapterExecuteProposalPatch(t *testing.T) {
	service := &fakeWorkerExecutionService{}
	adapter := NewWorkerExecutorAdapter(service)
	taskID := core.NewTaskID()

	health := adapter.Health(context.Background())
	if health.Status != core.HealthReady || !health.Ready {
		t.Fatalf("unexpected health: %+v", health)
	}

	got, err := adapter.Execute(context.Background(), moduleworker.Action{
		TaskID: taskID,
		Tool:   moduleworker.ToolProposalPatch,
		Arguments: map[string]any{
			"plan":  "plan text",
			"patch": `[{"type":"shell_command","action":"run","target":"true"}]`,
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if service.taskID != taskID {
		t.Fatalf("task ID was not mapped: %s", service.taskID.String())
	}
	if service.plan != "plan text" || service.patch == "" {
		t.Fatalf("proposal was not mapped: plan=%q patch=%q", service.plan, service.patch)
	}
	if got.Status != moduleworker.StatusSucceeded {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.Metadata["executed_cmds"] != 0 {
		t.Fatalf("metadata was not mapped from patch result: %+v", got.Metadata)
	}
}

func TestNewRuntimeWorkerExecutor(t *testing.T) {
	executor := NewRuntimeWorkerExecutor(&fakeWorkerExecutionService{})
	if executor == nil {
		t.Fatal("runtime worker executor is nil")
	}
	health := executor.Health(context.Background())
	if health.Status != core.HealthReady || !health.Ready {
		t.Fatalf("unexpected runtime worker health: %+v", health)
	}
}

func TestWorkerExecutorAdapterRejectsUnsupportedTool(t *testing.T) {
	adapter := NewWorkerExecutorAdapter(&fakeWorkerExecutionService{})
	got, err := adapter.Execute(context.Background(), moduleworker.Action{
		TaskID: core.NewTaskID(),
		Tool:   "tts",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got.Status != moduleworker.StatusDenied {
		t.Fatalf("unsupported tool should be denied, got %+v", got)
	}
}

func TestWorkerExecutorAdapterPropagatesWorkerError(t *testing.T) {
	adapter := NewWorkerExecutorAdapter(&fakeWorkerExecutionService{err: fmt.Errorf("boom")})
	got, err := adapter.Execute(context.Background(), moduleworker.Action{
		TaskID: core.NewTaskID(),
		Tool:   moduleworker.ToolProposalPatch,
		Arguments: map[string]any{
			"plan":  "plan text",
			"patch": "patch text",
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got.Status != moduleworker.StatusFailed || got.Error != "boom" {
		t.Fatalf("worker error was not mapped: %+v", got)
	}
}

func TestWorkerExecutorAdapterRejectsMissingOrMalformedTaskID(t *testing.T) {
	adapter := NewWorkerExecutorAdapter(&fakeWorkerExecutionService{})
	for _, taskID := range []core.TaskID{"", "not-a-task-id"} {
		got, err := adapter.Execute(context.Background(), moduleworker.Action{
			TaskID: taskID,
			Tool:   moduleworker.ToolProposalPatch,
			Arguments: map[string]any{
				"plan":  "plan text",
				"patch": "patch text",
			},
		})
		if err == nil {
			t.Fatalf("Execute() accepted invalid TaskID %q", taskID)
		}
		if !got.TaskID.IsZero() {
			t.Fatalf("invalid action produced a correlated result: %+v", got)
		}
	}
}
