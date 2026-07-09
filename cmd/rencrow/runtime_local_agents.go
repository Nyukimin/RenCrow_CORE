package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

func localAgentEnabled(agentName string, coder1Adapter, coder2Adapter, coder3Adapter, coder4Adapter *coderAdapter) bool {
	return moduleworker.LocalAgentEnabled(agentName, moduleworker.LocalAgentAvailability{
		Coder1: coder1Adapter != nil,
		Coder2: coder2Adapter != nil,
		Coder3: coder3Adapter != nil,
		Coder4: coder4Adapter != nil,
	})
}

type sshTransportConnector interface {
	Connect() error
}

func registerSSHTransport(
	agentName string,
	connector sshTransportConnector,
	tr domaintransport.Transport,
	sshTransports map[string]domaintransport.Transport,
) error {
	if err := connector.Connect(); err != nil {
		log.Printf("WARN: SSH transport unavailable for agent '%s': %v", agentName, err)
		return err
	}
	sshTransports[agentName] = tr
	log.Printf("Connected SSHTransport for agent '%s'", agentName)
	return nil
}

func markAgentUnavailable(store *viewer.MonitorStore, agentName, reason string) {
	if store == nil {
		return
	}
	store.SetAgentUnavailable(agentName, reason)
}

func formatAgentUnavailableReason(prefix string, err error) string {
	return moduleworker.FormatAgentUnavailableReason(prefix, err)
}

func distributedAgentAvailable(
	agentName string,
	localTransports map[string]*transport.LocalTransport,
	sshTransports map[string]domaintransport.Transport,
) bool {
	if _, ok := localTransports[agentName]; ok {
		return moduleworker.DistributedAgentAvailable(agentName, true, false)
	}
	if _, ok := sshTransports[agentName]; ok {
		return moduleworker.DistributedAgentAvailable(agentName, false, true)
	}
	return moduleworker.DistributedAgentAvailable(agentName, false, false)
}

func (d *Dependencies) ensureLocalTransport(agentName string) *transport.LocalTransport {
	if d.localTransports == nil {
		d.localTransports = make(map[string]*transport.LocalTransport)
	}
	if lt, ok := d.localTransports[agentName]; ok {
		return lt
	}
	if d.router != nil {
		if lt, ok := d.router.GetAgent(agentName); ok {
			d.localTransports[agentName] = lt
			return lt
		}
	}
	lt := transport.NewLocalTransport()
	d.router.RegisterAgent(agentName, lt)
	d.localTransports[agentName] = lt
	log.Printf("Registered implicit LocalTransport for agent '%s'", agentName)
	return lt
}

func (d *Dependencies) startLocalWorkerAgent(agentName string, lt *transport.LocalTransport, shiroAgent *agent.ShiroAgent, workerExecution service.WorkerExecutionService) {
	if lt == nil || shiroAgent == nil {
		return
	}
	go func() {
		for {
			msg, err := lt.Receive(context.Background())
			if err != nil {
				log.Printf("Local worker '%s' loop stopped: %v", agentName, err)
				return
			}
			resp := handleLocalWorkerMessage(agentName, msg, shiroAgent, workerExecution)
			d.deliverLocalAgentResponse(resp)
		}
	}()
}

func handleLocalWorkerMessage(agentName string, msg domaintransport.Message, shiroAgent *agent.ShiroAgent, workerExecution service.WorkerExecutionService) domaintransport.Message {
	log.Printf("[LocalWorker] recv agent=%s from=%s to=%s type=%s job=%s content_len=%d has_proposal=%t", agentName, msg.From, msg.To, msg.Type, msg.JobID, len(msg.Content), msg.Proposal != nil)
	if msg.Proposal != nil && workerExecution != nil {
		p := proposal.Reconstruct(msg.Proposal.Plan, msg.Proposal.Patch, msg.Proposal.Risk, msg.Proposal.CostHint)
		jobID, err := task.ParseJobID(msg.JobID)
		if err != nil {
			log.Printf("[LocalWorker] invalid job id agent=%s job=%s err=%v", agentName, msg.JobID, err)
			return newLocalAgentError(agentName, msg, fmt.Sprintf("invalid job ID: %v", err))
		}
		log.Printf("[LocalWorker] proposal execute start agent=%s job=%s", agentName, msg.JobID)
		result, err := executeLocalWorkerProposal(context.Background(), workerExecution, jobID, p, msg)
		if err != nil {
			log.Printf("[LocalWorker] proposal execute error agent=%s job=%s err=%v", agentName, msg.JobID, err)
			return newLocalAgentError(agentName, msg, fmt.Sprintf("patch execution failed: %v", err))
		}
		resp := domaintransport.NewMessage(agentName, msg.From, msg.SessionID, msg.JobID, result.Summary)
		resp.Type = domaintransport.MessageTypeResult
		resp.Result = &domaintransport.ResultPayload{
			Success:       result.FailedCmds == 0,
			Summary:       result.Summary,
			ExecutedCmds:  result.ExecutedCmds,
			FailedCmds:    result.FailedCmds,
			GitCommit:     result.GitCommit,
			FailureKind:   result.FailureKind,
			FailureReason: result.FailureReason,
			Retryable:     result.Retryable,
			FailedIndex:   result.FailedIndex,
		}
		log.Printf("[LocalWorker] proposal execute complete agent=%s job=%s success=%t summary_len=%d", agentName, msg.JobID, result.FailedCmds == 0, len(result.Summary))
		return resp
	}

	jobID, err := task.ParseJobID(msg.JobID)
	if err != nil {
		jobID = task.NewJobID()
	}
	t := task.NewTask(jobID, msg.Content, "distributed", msg.SessionID)
	log.Printf("[LocalWorker] shiro execute start agent=%s job=%s", agentName, msg.JobID)
	result, err := shiroAgent.Execute(context.Background(), t)
	if err != nil {
		log.Printf("[LocalWorker] shiro execute error agent=%s job=%s err=%v", agentName, msg.JobID, err)
		return newLocalAgentError(agentName, msg, fmt.Sprintf("worker execution failed: %v", err))
	}
	resp := domaintransport.NewMessage(agentName, msg.From, msg.SessionID, msg.JobID, result)
	resp.Type = domaintransport.MessageTypeResult
	resp.Result = &domaintransport.ResultPayload{
		Success: true,
		Summary: result,
	}
	log.Printf("[LocalWorker] shiro execute complete agent=%s job=%s result_len=%d", agentName, msg.JobID, len(result))
	return resp
}

func executeLocalWorkerProposal(ctx context.Context, workerExecution service.WorkerExecutionService, jobID task.JobID, p *proposal.Proposal, msg domaintransport.Message) (*patch.PatchExecutionResult, error) {
	if root := localMessageContextString(msg, "module_root"); root != "" {
		if worker, ok := workerExecution.(service.WorkspaceOverrideWorkerExecutionService); ok {
			log.Printf("[LocalWorker] proposal workspace override job=%s module_root=%s", msg.JobID, root)
			return worker.ExecuteProposalInWorkspace(ctx, jobID, p, root)
		}
		log.Printf("[LocalWorker] workspace override unavailable job=%s module_root=%s", msg.JobID, root)
	}
	return workerExecution.ExecuteProposal(ctx, jobID, p)
}

func localMessageContextString(msg domaintransport.Message, key string) string {
	if msg.Context == nil {
		return ""
	}
	value, ok := msg.Context[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func (d *Dependencies) startLocalCoderAgent(agentName string, lt *transport.LocalTransport, coder *coderAdapter) {
	if lt == nil || coder == nil {
		return
	}
	go func() {
		for {
			msg, err := lt.Receive(context.Background())
			if err != nil {
				log.Printf("Local coder '%s' loop stopped: %v", agentName, err)
				return
			}
			log.Printf("[LocalCoder] recv agent=%s from=%s to=%s type=%s job=%s content_len=%d", agentName, msg.From, msg.To, msg.Type, msg.JobID, len(msg.Content))
			d.emitLocalAgentNote(agentName, msg.From, "依頼を受領しました。", msg)
			jobID, parseErr := task.ParseJobID(msg.JobID)
			if parseErr != nil {
				jobID = task.NewJobID()
			}
			t := task.NewTask(jobID, msg.Content, "distributed", msg.SessionID)
			log.Printf("[LocalCoder] proposal start agent=%s job=%s", agentName, msg.JobID)
			d.emitLocalAgentNote(agentName, msg.From, "proposal 生成を開始しました。", msg)
			p, err := coder.GenerateProposal(context.Background(), t)
			if err != nil {
				log.Printf("[LocalCoder] proposal error agent=%s job=%s err=%v", agentName, msg.JobID, err)
				d.emitLocalAgentNote(agentName, msg.From, "proposal 生成で失敗しました。", msg)
				d.deliverLocalAgentResponse(newLocalAgentError(agentName, msg, fmt.Sprintf("proposal generation failed: %v", err)))
				continue
			}
			if p == nil {
				log.Printf("[LocalCoder] proposal empty agent=%s job=%s", agentName, msg.JobID)
				d.emitLocalAgentNote(agentName, msg.From, "proposal が空でした。", msg)
				d.deliverLocalAgentResponse(newLocalAgentError(agentName, msg, "proposal generation returned empty result"))
				continue
			}
			log.Printf("[LocalCoder] proposal complete agent=%s job=%s plan_len=%d patch_len=%d", agentName, msg.JobID, len(p.Plan()), len(p.Patch()))
			d.emitLocalAgentNote(agentName, msg.From, "proposal 生成が完了しました。", msg)
			resp := domaintransport.NewMessage(agentName, localCoderReplyTarget(msg), msg.SessionID, msg.JobID, fmt.Sprintf("Proposal generated by %s", agentName))
			resp.Type = domaintransport.MessageTypeResult
			resp.Proposal = &domaintransport.ProposalPayload{
				Plan:     p.Plan(),
				Patch:    p.Patch(),
				Risk:     p.Risk(),
				CostHint: p.CostHint(),
			}
			d.deliverLocalAgentResponse(resp)
		}
	}()
}

func (d *Dependencies) deliverLocalAgentResponse(msg domaintransport.Message) {
	if d.router == nil {
		log.Printf("[LocalDeliver] drop reason=no_router to=%s from=%s job=%s", msg.To, msg.From, msg.JobID)
		return
	}
	target, ok := d.router.GetAgent(msg.To)
	if !ok {
		log.Printf("Local agent response dropped: target '%s' not registered", msg.To)
		return
	}
	log.Printf("[LocalDeliver] send to=%s from=%s type=%s job=%s content_len=%d has_proposal=%t", msg.To, msg.From, msg.Type, msg.JobID, len(msg.Content), msg.Proposal != nil)
	if err := target.PutInboundMessage(msg); err != nil {
		log.Printf("Local agent response delivery failed to '%s': %v", msg.To, err)
		return
	}
	log.Printf("[LocalDeliver] sent to=%s from=%s job=%s", msg.To, msg.From, msg.JobID)
}

func newLocalAgentError(agentName string, msg domaintransport.Message, errMsg string) domaintransport.Message {
	resp := domaintransport.NewMessage(agentName, localCoderReplyTarget(msg), msg.SessionID, msg.JobID, errMsg)
	resp.Type = domaintransport.MessageTypeError
	return resp
}

func localCoderReplyTarget(msg domaintransport.Message) string {
	return moduleworker.LocalCoderReplyTarget(msg.From)
}

func (d *Dependencies) emitLocalAgentNote(from, to, content string, msg domaintransport.Message) {
	if d.eventRelay == nil {
		return
	}
	route := ""
	if msg.Context != nil {
		if v, ok := msg.Context["route"].(string); ok {
			route = v
		}
	}
	d.eventRelay.OnEvent(orchestrator.NewEvent(
		"agent.note",
		from,
		to,
		content,
		route,
		msg.JobID,
		msg.SessionID,
		"distributed",
		msg.SessionID,
	))
}
