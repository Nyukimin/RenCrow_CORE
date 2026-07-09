package modulebridge

import (
	"context"
	"fmt"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

type fakeWorkerExecutionService struct {
	jobID task.JobID
	plan  string
	patch string
	err   error
}

func (s *fakeWorkerExecutionService) ExecuteProposal(_ context.Context, jobID task.JobID, p *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	s.jobID = jobID
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

	health := adapter.Health(context.Background())
	if health.Status != core.HealthReady || !health.Ready {
		t.Fatalf("unexpected health: %+v", health)
	}

	got, err := adapter.Execute(context.Background(), moduleworker.Action{
		JobID: "20260301-120000-abcd1234",
		Tool:  moduleworker.ToolProposalPatch,
		Arguments: map[string]any{
			"plan":  "plan text",
			"patch": `[{"type":"shell_command","action":"run","target":"true"}]`,
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if service.jobID.String() != "20260301-120000-abcd1234" {
		t.Fatalf("job id was not mapped: %s", service.jobID.String())
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
		JobID: "20260301-120000-abcd1234",
		Tool:  "tts",
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
		JobID: "20260301-120000-abcd1234",
		Tool:  moduleworker.ToolProposalPatch,
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
