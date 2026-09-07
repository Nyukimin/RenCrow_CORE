package main

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
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

type projectingLocalWorkerExecution struct {
	result patch.PatchExecutionResult
	calls  int
	ctx    context.Context
}

func (p *projectingLocalWorkerExecution) ExecuteObservation(_ context.Context, _ []service.ObservationAction) ([]service.ObservationActionResult, error) {
	return nil, nil
}

func (p *projectingLocalWorkerExecution) ExecuteProposal(ctx context.Context, _ modulecore.TaskID, _ *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	p.calls++
	p.ctx = ctx
	result := p.result
	return &result, nil
}

func (p *projectingLocalWorkerExecution) ExecuteProposalInWorkspace(_ context.Context, _ modulecore.TaskID, _ *proposal.Proposal, _ string) (*patch.PatchExecutionResult, error) {
	result := p.result
	return &result, nil
}

func TestWorkerResultProjectionLocal(t *testing.T) {
	for _, tc := range workerResultProjectionCases() {
		t.Run(tc.name, func(t *testing.T) {
			msg := workerResultProjectionMessage()
			owner, ctx := runtimeToolOwnerFixture(t, "", "shiro")
			identity, err := domainexecution.IdentityFromContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			msg.TaskID = identity.TaskID
			got := handleLocalWorkerMessage(ctx, owner, "shiro", msg, nil, &projectingLocalWorkerExecution{result: tc.result})
			assertWorkerResultProjection(t, msg, got, tc.result)
		})
	}
}

type workerResultProjectionCase struct {
	name   string
	result patch.PatchExecutionResult
	calls  int
	ctx    context.Context
}

func workerResultProjectionCases() []workerResultProjectionCase {
	return []workerResultProjectionCase{
		{
			name: "passed",
			result: patch.PatchExecutionResult{
				Success: true, Summary: "passed summary", ExecutedCmds: 2, FailedCmds: 0,
				GitCommit: "commit-passed", FailedIndex: -1, TestStatus: "passed", TestReceipt: "Tmp/test-results/passed.json",
			},
		},
		{
			name: "failed_zero_commands",
			result: patch.PatchExecutionResult{
				Success: false, Summary: "failed without commands", ExecutedCmds: 0, FailedCmds: 0,
				FailureKind: "execution_failed", FailureReason: "owner reported failure", Retryable: true,
				FailedIndex: -1, TestStatus: "failed", TestReceipt: "Tmp/test-results/failed-zero.json",
			},
		},
		{
			name: "blocked_zero_commands",
			result: patch.PatchExecutionResult{
				Success: false, Summary: "blocked before commands", ExecutedCmds: 0, FailedCmds: 0,
				FailureKind: "test_impact_blocked", FailureReason: "canonical owner unavailable", Retryable: false,
				FailedIndex: -1, TestStatus: "blocked", TestReceipt: "Tmp/test-results/blocked.json",
			},
		},
		{
			name: "command_failure",
			result: patch.PatchExecutionResult{
				Success: false, Summary: "command failed", ExecutedCmds: 2, FailedCmds: 1,
				FailureKind: "command_failed", FailureReason: "shell exit", Retryable: true,
				FailedIndex: 1, TestStatus: "not_run", TestReceipt: "Tmp/test-results/command-failure.json",
			},
		},
	}
}

func workerResultProjectionMessage() domaintransport.Message {
	msg := domaintransport.NewMessage("mio", "shiro", string(modulecore.NewSessionID()), modulecore.NewTaskID(), "Execute coder proposal")
	msg.Proposal = &domaintransport.ProposalPayload{Plan: "plan", Patch: "[]", Risk: "risk", CostHint: "cost"}
	return msg
}

func assertWorkerResultProjection(t *testing.T, input, got domaintransport.Message, want patch.PatchExecutionResult) {
	t.Helper()
	if got.Type != domaintransport.MessageTypeResult {
		t.Fatalf("response type=%q, want result", got.Type)
	}
	if got.From != input.To || got.To != input.From || got.SessionID != input.SessionID || got.TaskID != input.TaskID {
		t.Fatalf("response address/identity changed: got from=%q to=%q session=%q task=%q, want from=%q to=%q session=%q task=%q", got.From, got.To, got.SessionID, got.TaskID, input.To, input.From, input.SessionID, input.TaskID)
	}
	if got.Result == nil {
		t.Fatal("response result is nil")
	}
	if got.Result.Success != want.Success || got.Result.Summary != want.Summary || got.Result.ExecutedCmds != want.ExecutedCmds || got.Result.FailedCmds != want.FailedCmds || got.Result.GitCommit != want.GitCommit || got.Result.FailureKind != want.FailureKind || got.Result.FailureReason != want.FailureReason || got.Result.Retryable != want.Retryable || got.Result.FailedIndex != want.FailedIndex || got.Result.TestStatus != want.TestStatus || got.Result.TestReceipt != want.TestReceipt {
		t.Fatalf("result projection=%#v, want success=%t summary=%q executed=%d failed=%d commit=%q kind=%q reason=%q retryable=%t index=%d status=%q receipt=%q", got.Result, want.Success, want.Summary, want.ExecutedCmds, want.FailedCmds, want.GitCommit, want.FailureKind, want.FailureReason, want.Retryable, want.FailedIndex, want.TestStatus, want.TestReceipt)
	}
}

func TestLocalWorkerContextAdmission(t *testing.T) {
	for _, name := range []string{"valid", "missing", "canceled", "wrong_task", "wrong_destination", "wrong_actor", "missing_owner", "wrong_trace", "valid_trace"} {
		t.Run(name, func(t *testing.T) {
			actor := "shiro"
			if name == "wrong_actor" {
				actor = "mio"
			}
			owner, ctx := runtimeToolOwnerFixture(t, "", actor)
			identity, err := domainexecution.IdentityFromContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			msg := workerResultProjectionMessage()
			msg.TaskID = identity.TaskID
			switch name {
			case "missing":
				ctx = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "wrong_task":
				msg.TaskID = modulecore.NewTaskID()
			case "wrong_destination":
				msg.To = "mio"
			case "missing_owner":
				owner = nil
			case "wrong_trace", "valid_trace":
				address, err := conversation.NewChannelAddress("viewer", "local-context")
				if err != nil {
					t.Fatal(err)
				}
				input, err := conversation.NewTurnInput(modulecore.NewTaskID(), msg.Content, address)
				if err != nil {
					t.Fatal(err)
				}
				input = input.WithSessionID(msg.SessionID)
				projected, err := domaintransport.NewTurnInputMessage(msg.From, msg.To, msg.TaskID, input)
				if err != nil {
					t.Fatal(err)
				}
				projected.Proposal = msg.Proposal
				msg = projected
				if name == "valid_trace" {
					scope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
					ctx, err = domainexecution.WithIdentity(context.Background(), identity.TaskID, identity.RunID, input.TraceID())
					if err != nil {
						t.Fatal(err)
					}
					ctx = domaintool.WithToolExecutionScope(ctx, scope)
				}
			}
			worker := &projectingLocalWorkerExecution{result: patch.PatchExecutionResult{Success: true, Summary: "done"}}
			got := handleLocalWorkerMessage(ctx, owner, "shiro", msg, nil, worker)
			if name == "valid" || name == "valid_trace" {
				if got.Type != domaintransport.MessageTypeResult || worker.calls != 1 || worker.ctx != ctx {
					t.Fatalf("context not preserved: response=%#v calls=%d sameContext=%t", got, worker.calls, worker.ctx == ctx)
				}
			} else if got.Type != domaintransport.MessageTypeError || worker.calls != 0 {
				t.Fatalf("invalid context executed: response=%#v calls=%d", got, worker.calls)
			}
		})
	}
}
