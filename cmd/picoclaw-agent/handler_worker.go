package main

import (
	"context"
	"fmt"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/proposal"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

// workerHandler はWorkerエージェントのハンドラ
type workerHandler struct {
	shiroAgent       *agent.ShiroAgent
	executionService service.WorkerExecutionService
}

func (h *workerHandler) HandleMessage(ctx context.Context, msg domaintransport.Message) (domaintransport.Message, error) {
	// Proposal付きメッセージ → Worker即時実行
	if msg.Proposal != nil {
		return h.executeProposal(ctx, msg)
	}

	// Proposalなし → ShiroAgentでタスク実行
	return h.executeTask(ctx, msg)
}

// executeProposal はProposalのPatchをWorkerが即時実行
func (h *workerHandler) executeProposal(ctx context.Context, msg domaintransport.Message) (domaintransport.Message, error) {
	// ProposalPayload → domain Proposal に復元
	p := proposal.Reconstruct(
		msg.Proposal.Plan,
		msg.Proposal.Patch,
		msg.Proposal.Risk,
		msg.Proposal.CostHint,
	)

	// JobID をパース
	jobID, err := task.ParseJobID(msg.JobID)
	if err != nil {
		return domaintransport.Message{}, fmt.Errorf("invalid job ID: %w", err)
	}

	// Patch実行
	result, err := h.executionService.ExecuteProposal(ctx, jobID, p)
	if err != nil {
		errResp := domaintransport.NewMessage(msg.To, msg.From, msg.SessionID, msg.JobID,
			fmt.Sprintf("patch execution failed: %v", err))
		errResp.Type = domaintransport.MessageTypeError
		return errResp, nil
	}

	// 結果をResultPayloadに変換
	response := domaintransport.NewMessage(msg.To, msg.From, msg.SessionID, msg.JobID, result.Summary)
	response.Type = domaintransport.MessageTypeResult
	response.Result = &domaintransport.ResultPayload{
		Success:      result.FailedCmds == 0,
		Summary:      result.Summary,
		ExecutedCmds: result.ExecutedCmds,
		FailedCmds:   result.FailedCmds,
		GitCommit:    result.GitCommit,
	}

	return response, nil
}

// executeTask はShiroAgentでタスクを実行
func (h *workerHandler) executeTask(ctx context.Context, msg domaintransport.Message) (domaintransport.Message, error) {
	jobID, err := task.ParseJobID(msg.JobID)
	if err != nil {
		jobID = task.NewJobID()
	}

	t := task.NewTask(jobID, msg.Content, "standalone", "agent")

	result, err := h.shiroAgent.Execute(ctx, t)
	if err != nil {
		errResp := domaintransport.NewMessage(msg.To, msg.From, msg.SessionID, msg.JobID,
			fmt.Sprintf("worker execution failed: %v", err))
		errResp.Type = domaintransport.MessageTypeError
		return errResp, nil
	}

	response := domaintransport.NewMessage(msg.To, msg.From, msg.SessionID, msg.JobID, result)
	response.Type = domaintransport.MessageTypeResult
	response.Result = &domaintransport.ResultPayload{
		Success: true,
		Summary: result,
	}
	return response, nil
}
