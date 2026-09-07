package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

func reconstructLocalAgentInput(msg domaintransport.Message) (conversation.TurnInput, error) {
	input, err := msg.ReconstructTurnInput()
	if err != nil {
		return conversation.TurnInput{}, fmt.Errorf("local agent turn input is invalid: %w", err)
	}
	return input, nil
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
			delivery, err := lt.ReceiveDelivery(context.Background())
			if err != nil {
				log.Printf("Local worker '%s' loop stopped: %v", agentName, err)
				return
			}
			resp := handleLocalWorkerMessage(delivery.Context, d.taskManager, agentName, delivery.Message, shiroAgent, workerExecution)
			d.deliverLocalAgentResponse(resp)
		}
	}()
}

// validateLocalExecutionContext belongs to the receiving CORE runtime. Transport
// addresses do not create Actor identity; the persisted Task owner admits it.
func validateLocalExecutionContext(ctx context.Context, owner *taskmanager.Manager, recipient string, msg domaintransport.Message) error {
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("invalid local message: %w", err)
	}
	if msg.To != recipient {
		return fmt.Errorf("local message destination mismatch")
	}
	if owner == nil {
		return fmt.Errorf("local task owner is unavailable")
	}
	identity, err := owner.ValidateExecutionContext(ctx)
	if err != nil {
		return fmt.Errorf("local execution admission denied: %w", err)
	}
	if identity.TaskID != msg.TaskID {
		return fmt.Errorf("local execution task mismatch")
	}
	if msg.TurnInput != nil && identity.TraceID != msg.TurnInput.TraceID {
		return fmt.Errorf("local execution trace mismatch")
	}
	return nil
}

func handleLocalWorkerMessage(ctx context.Context, owner *taskmanager.Manager, agentName string, msg domaintransport.Message, shiroAgent *agent.ShiroAgent, workerExecution service.WorkerExecutionService) domaintransport.Message {
	if err := validateLocalExecutionContext(ctx, owner, agentName, msg); err != nil {
		return newLocalAgentError(agentName, msg, err.Error())
	}
	scope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
	if scope.ActorID != "shiro" {
		return newLocalAgentError(agentName, msg, "worker requires Shiro execution scope")
	}

	log.Printf("[LocalWorker] recv agent=%s from=%s to=%s type=%s task=%s content_len=%d has_proposal=%t", agentName, msg.From, msg.To, msg.Type, msg.TaskID, len(msg.Content), msg.Proposal != nil)
	if msg.Proposal != nil && workerExecution != nil {
		p := proposal.Reconstruct(msg.Proposal.Plan, msg.Proposal.Patch, msg.Proposal.Risk, msg.Proposal.CostHint)
		if err := msg.TaskID.Validate(); err != nil {
			log.Printf("[LocalWorker] invalid task id agent=%s task=%s err=%v", agentName, msg.TaskID, err)
			return newLocalAgentError(agentName, msg, fmt.Sprintf("invalid task ID: %v", err))
		}
		log.Printf("[LocalWorker] proposal execute start agent=%s task=%s", agentName, msg.TaskID)
		result, err := executeLocalWorkerProposal(ctx, workerExecution, msg.TaskID, p, msg)
		if err != nil {
			log.Printf("[LocalWorker] proposal execute error agent=%s task=%s err=%v", agentName, msg.TaskID, err)
			return newLocalAgentError(agentName, msg, fmt.Sprintf("patch execution failed: %v", err))
		}
		resp := domaintransport.NewMessage(agentName, msg.From, msg.SessionID, msg.TaskID, result.Summary)
		resp.Type = domaintransport.MessageTypeResult
		resp.Result = domaintransport.NewPatchResultPayload(*result)

		log.Printf("[LocalWorker] proposal execute complete agent=%s task=%s success=%t summary_len=%d", agentName, msg.TaskID, result.Success, len(result.Summary))
		return resp
	}

	if shiroAgent == nil {
		return newLocalAgentError(agentName, msg, "Shiro is unavailable")
	}
	input, err := reconstructLocalAgentInput(msg)
	if err != nil {
		log.Printf("[LocalWorker] invalid turn input agent=%s task=%s err=%v", agentName, msg.TaskID, err)
		return newLocalAgentError(agentName, msg, fmt.Sprintf("worker input is invalid: %v", err))
	}
	log.Printf("[LocalWorker] shiro execute start agent=%s task=%s", agentName, msg.TaskID)
	result, err := shiroAgent.Execute(ctx, input)
	if err != nil {
		log.Printf("[LocalWorker] shiro execute error agent=%s task=%s err=%v", agentName, msg.TaskID, err)
		return newLocalAgentError(agentName, msg, fmt.Sprintf("worker execution failed: %v", err))
	}
	resp := domaintransport.NewMessage(agentName, msg.From, msg.SessionID, msg.TaskID, result)
	resp.Type = domaintransport.MessageTypeResult
	resp.Result = &domaintransport.ResultPayload{
		Success: true,
		Summary: result,
	}
	log.Printf("[LocalWorker] shiro execute complete agent=%s task=%s result_len=%d", agentName, msg.TaskID, len(result))
	return resp
}

func executeLocalWorkerProposal(ctx context.Context, workerExecution service.WorkerExecutionService, taskID modulecore.TaskID, p *proposal.Proposal, msg domaintransport.Message) (*patch.PatchExecutionResult, error) {
	if root := localMessageContextString(msg, "module_root"); root != "" {
		if worker, ok := workerExecution.(service.WorkspaceOverrideWorkerExecutionService); ok {
			log.Printf("[LocalWorker] proposal workspace override task=%s module_root=%s", msg.TaskID, root)
			return worker.ExecuteProposalInWorkspace(ctx, taskID, p, root)
		}
		log.Printf("[LocalWorker] workspace override unavailable task=%s module_root=%s", msg.TaskID, root)
	}
	return workerExecution.ExecuteProposal(ctx, taskID, p)
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
			delivery, err := lt.ReceiveDelivery(context.Background())
			if err != nil {
				log.Printf("Local coder '%s' loop stopped: %v", agentName, err)
				return
			}
			d.deliverLocalAgentResponse(d.handleLocalCoderMessage(delivery.Context, agentName, delivery.Message, coder))
		}
	}()
}

func (d *Dependencies) handleLocalCoderMessage(ctx context.Context, agentName string, msg domaintransport.Message, coder *coderAdapter) domaintransport.Message {
	if err := validateLocalExecutionContext(ctx, d.taskManager, agentName, msg); err != nil {
		return newLocalAgentError(agentName, msg, err.Error())
	}
	if coder == nil || coder.domainCoder == nil {
		return newLocalAgentError(agentName, msg, "local coder is unavailable")
	}

	log.Printf("[LocalCoder] recv agent=%s from=%s to=%s type=%s task=%s content_len=%d", agentName, msg.From, msg.To, msg.Type, msg.TaskID, len(msg.Content))
	input, inputErr := reconstructLocalAgentInput(msg)
	if inputErr != nil {
		log.Printf("[LocalCoder] invalid turn input agent=%s task=%s err=%v", agentName, msg.TaskID, inputErr)
		d.emitLocalAgentNote(agentName, msg.From, "入力の正規ID検証で失敗しました。", msg)
		return newLocalAgentError(agentName, msg, fmt.Sprintf("coder input is invalid: %v", inputErr))
	}
	d.emitLocalAgentNote(agentName, msg.From, "依頼を受領しました。", msg)
	log.Printf("[LocalCoder] proposal start agent=%s task=%s", agentName, msg.TaskID)
	d.emitLocalAgentNote(agentName, msg.From, "proposal 生成を開始しました。", msg)
	p, err := coder.GenerateProposal(ctx, input)
	if err == nil {
		err = validateLocalExecutionContext(ctx, d.taskManager, agentName, msg)
	}
	if err != nil {
		log.Printf("[LocalCoder] proposal error agent=%s task=%s err=%v", agentName, msg.TaskID, err)
		d.emitLocalAgentNote(agentName, msg.From, "proposal 生成で失敗しました。", msg)
		return newLocalAgentError(agentName, msg, fmt.Sprintf("proposal generation failed: %v", err))
	}
	if p == nil {
		log.Printf("[LocalCoder] proposal empty agent=%s task=%s", agentName, msg.TaskID)
		d.emitLocalAgentNote(agentName, msg.From, "proposal が空でした。", msg)
		return newLocalAgentError(agentName, msg, "proposal generation returned empty result")
	}
	log.Printf("[LocalCoder] proposal complete agent=%s task=%s plan_len=%d patch_len=%d", agentName, msg.TaskID, len(p.Plan()), len(p.Patch()))
	d.emitLocalAgentNote(agentName, msg.From, "proposal 生成が完了しました。", msg)
	resp := domaintransport.NewMessage(agentName, msg.From, msg.SessionID, msg.TaskID, fmt.Sprintf("Proposal generated by %s", agentName))
	resp.Type = domaintransport.MessageTypeResult
	resp.Proposal = &domaintransport.ProposalPayload{
		Plan:     p.Plan(),
		Patch:    p.Patch(),
		Risk:     p.Risk(),
		CostHint: p.CostHint(),
	}
	return resp
}

func (d *Dependencies) deliverLocalAgentResponse(msg domaintransport.Message) {
	if d.router == nil {
		log.Printf("[LocalDeliver] drop reason=no_router to=%s from=%s task=%s", msg.To, msg.From, msg.TaskID)
		return
	}
	target, ok := d.router.GetAgent(msg.To)
	if !ok {
		log.Printf("Local agent response dropped: target '%s' not registered", msg.To)
		return
	}
	log.Printf("[LocalDeliver] send to=%s from=%s type=%s task=%s content_len=%d has_proposal=%t", msg.To, msg.From, msg.Type, msg.TaskID, len(msg.Content), msg.Proposal != nil)
	if err := target.PutInboundMessage(msg); err != nil {
		log.Printf("Local agent response delivery failed to '%s': %v", msg.To, err)
		return
	}
	log.Printf("[LocalDeliver] sent to=%s from=%s task=%s", msg.To, msg.From, msg.TaskID)
}

func newLocalAgentError(agentName string, msg domaintransport.Message, errMsg string) domaintransport.Message {
	resp := domaintransport.NewMessage(agentName, msg.From, msg.SessionID, msg.TaskID, errMsg)
	resp.Type = domaintransport.MessageTypeError
	return resp
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
	if err := d.eventRelay.OnEvent(orchestrator.NewEvent(
		"agent.note",
		from,
		to,
		content,
		route,
		msg.TaskID.String(),
		msg.SessionID,
		"distributed",
		msg.SessionID,
	)); err != nil {
		log.Printf("[LocalCoder] agent note event publication failed agent=%s task=%s: %v", from, msg.TaskID, err)
	}
}
