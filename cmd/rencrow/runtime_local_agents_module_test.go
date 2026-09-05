package main

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type recordingLocalWorkerExecution struct {
	workspace string
}

func (r *recordingLocalWorkerExecution) ExecuteObservation(_ context.Context, _ []service.ObservationAction) ([]service.ObservationActionResult, error) {
	return nil, nil
}

func (r *recordingLocalWorkerExecution) ExecuteProposal(_ context.Context, _ modulecore.TaskID, _ *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	result := patch.NewPatchExecutionResult()
	result.AddResult(patch.CommandResult{Success: true, Output: "default"})
	return result.WithSummary("default workspace"), nil
}

func (r *recordingLocalWorkerExecution) ExecuteProposalInWorkspace(_ context.Context, _ modulecore.TaskID, _ *proposal.Proposal, workspace string) (*patch.PatchExecutionResult, error) {
	r.workspace = workspace
	result := patch.NewPatchExecutionResult()
	result.AddResult(patch.CommandResult{Success: true, Output: "override"})
	return result.WithSummary("override workspace"), nil
}

func TestExecuteLocalWorkerProposalUsesModuleRootContext(t *testing.T) {
	worker := &recordingLocalWorkerExecution{}
	taskID := modulecore.NewTaskID()
	msg := domaintransport.NewMessage("mio", "shiro", "sess-1", taskID, "Execute coder proposal")
	msg.Context = map[string]interface{}{
		"module_root": "/home/nyukimi/RenCrow/RenCrow_STT",
	}
	p := proposal.NewProposal("plan", "[]", "risk", "cost")

	_, err := executeLocalWorkerProposal(context.Background(), worker, taskID, p, msg)
	if err != nil {
		t.Fatalf("executeLocalWorkerProposal failed: %v", err)
	}
	if worker.workspace != "/home/nyukimi/RenCrow/RenCrow_STT" {
		t.Fatalf("workspace=%q, want RenCrow_STT root", worker.workspace)
	}
}
