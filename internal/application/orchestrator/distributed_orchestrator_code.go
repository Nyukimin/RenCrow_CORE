package orchestrator

import (
	"context"
	"fmt"
	"log"

	moduleapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moduleregistry"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

type distributedCoderSelector func(route routing.Route, userMessage string) string
type distributedCoderConfigProvider func() map[string]interface{}
type distributedRetryMaxResolver func() int
type distributedMailboxExecutor func(ctx context.Context, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error)
type distributedAgentExecutor func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error)

type distributedCodeExecutionCoordinator struct {
	memory           *session.CentralMemory
	emit             messageEventEmitter
	emitNote         distributedNoteEmitter
	selectCoder      distributedCoderSelector
	coderConfigs     distributedCoderConfigProvider
	coderRetryMax    distributedRetryMaxResolver
	executeMailbox   distributedMailboxExecutor
	executeToAgent   distributedAgentExecutor
	proposalEvidence CoderProposalEvidenceRecorder
	moduleResolver   ModuleResolver
}

func newDistributedCodeExecutionCoordinator(
	memory *session.CentralMemory,
	emit messageEventEmitter,
	emitNote distributedNoteEmitter,
	selectCoder distributedCoderSelector,
	coderConfigs distributedCoderConfigProvider,
	coderRetryMax distributedRetryMaxResolver,
	executeMailbox distributedMailboxExecutor,
	executeToAgent distributedAgentExecutor,
) *distributedCodeExecutionCoordinator {
	return &distributedCodeExecutionCoordinator{
		memory:         memory,
		emit:           emit,
		emitNote:       emitNote,
		selectCoder:    selectCoder,
		coderConfigs:   coderConfigs,
		coderRetryMax:  coderRetryMax,
		executeMailbox: executeMailbox,
		executeToAgent: executeToAgent,
	}
}

func (c *distributedCodeExecutionCoordinator) SetCoderProposalEvidenceRecorder(recorder CoderProposalEvidenceRecorder) {
	c.proposalEvidence = recorder
}

func (c *distributedCodeExecutionCoordinator) SetModuleResolver(resolver ModuleResolver) {
	c.moduleResolver = resolver
}

func (c *distributedCodeExecutionCoordinator) Execute(ctx context.Context, t task.Task, route routing.Route, sessionID, jid string) (string, error) {
	coderAgent := c.selectCoder(route, t.UserMessage())
	if coderAgent == "" {
		return "", fmt.Errorf("no coder mapped for route %s", route)
	}
	log.Printf("[DistributedOrch] code handoff route=%s target=%s job=%s", route, coderAgent, jid)

	c.emit("agent.start", "mio", "shiro", "コードタスクをShiro経由で実行", string(route), jid, sessionID, t.Channel(), t.ChatID())
	c.emitNote("mio", "user", "しろにコード実装の取りまとめをお願いしたよ。", string(route), jid, sessionID, t.Channel(), t.ChatID())
	requestText := t.UserMessage()
	if c.moduleResolver == nil {
		c.moduleResolver = moduleapp.DefaultRegistry()
	}
	if resolved := c.moduleResolver.Resolve(t.UserMessage()); resolved.Found() {
		c.emit("module.selected", "mio", "shiro", resolved.Summary(), string(route), jid, sessionID, t.Channel(), t.ChatID())
		requestText = appendModuleContextToCodeRequest(requestText, resolved)
	}

	for attempt := 0; attempt <= c.coderRetryMax(); attempt++ {
		c.emit("agent.start", "shiro", coderAgent, requestText, string(route), jid, sessionID, t.Channel(), t.ChatID())
		if attempt == 0 {
			c.emitNote("shiro", "mio", fmt.Sprintf("%sにコーディング依頼しました。進捗を監視して、必要なら作業を前に進めます。", displayAgentName(coderAgent)), string(route), jid, sessionID, t.Channel(), t.ChatID())
		} else {
			c.emit("worker.retry_request", "shiro", coderAgent, fmt.Sprintf("retry=%d", attempt), string(route), jid, sessionID, t.Channel(), t.ChatID())
			c.emitNote("shiro", "mio", fmt.Sprintf("%sに修正版patchを再依頼します。retry=%d", displayAgentName(coderAgent), attempt), string(route), jid, sessionID, t.Channel(), t.ChatID())
		}

		coderMsg := c.buildCoderMessage(coderAgent, sessionID, jid, requestText, route, t, attempt)
		c.memory.RecordMessage(coderMsg)

		coderResult, err := c.executeMailbox(ctx, coderAgent, coderMsg, "mio")
		if err != nil {
			failureKind, reason, retryable := classifyDistributedExecutionError(err)
			if retryable && attempt < c.coderRetryMax() {
				c.emit("worker.classified_failure", "shiro", coderAgent, fmt.Sprintf("%s: %s", failureKind, reason), string(route), jid, sessionID, t.Channel(), t.ChatID())
				requestText = buildCoderRetryInstruction(t.UserMessage(), nil, failureKind, reason, attempt+1)
				continue
			}
			return "", err
		}
		c.emit("agent.response", coderAgent, "shiro", coderResult.Content, string(route), jid, sessionID, t.Channel(), t.ChatID())
		c.emitNote(coderAgent, "shiro", "おわったっす。", string(route), jid, sessionID, t.Channel(), t.ChatID())
		c.emitNote("shiro", "mio", fmt.Sprintf("%sの結果を受け取って、内容確認と仕上げを進めます。", displayAgentName(coderAgent)), string(route), jid, sessionID, t.Channel(), t.ChatID())

		if coderResult.Proposal == nil {
			return c.finishWithoutProposal(ctx, t, route, sessionID, jid, coderAgent, coderResult)
		}

		response, retryReq, retryable, err := c.executeProposal(ctx, t, route, sessionID, jid, coderAgent, coderResult, attempt)
		if err != nil {
			return "", err
		}
		if retryable {
			requestText = retryReq
			continue
		}
		return response, nil
	}
	return "", fmt.Errorf("coder retry budget exhausted for job %s", jid)
}

func (c *distributedCodeExecutionCoordinator) buildCoderMessage(coderAgent, sessionID, jid, requestText string, route routing.Route, t task.Task, attempt int) domaintransport.Message {
	coderMsg := domaintransport.NewMessage("shiro", coderAgent, sessionID, jid, requestText)
	coderMsg.Type = domaintransport.MessageTypeTask
	coderMsg.Context = map[string]interface{}{
		"route":         string(route),
		"retry_attempt": attempt,
		"channel":       t.Channel(),
		"chat_id":       t.ChatID(),
	}
	if configs := c.coderConfigs(); configs != nil {
		if coderCfg, ok := configs[coderAgent]; ok {
			coderMsg.Context["coder_config"] = coderCfg
		}
	}
	return coderMsg
}

func (c *distributedCodeExecutionCoordinator) finishWithoutProposal(ctx context.Context, t task.Task, route routing.Route, sessionID, jid, coderAgent string, coderResult domaintransport.Message) (string, error) {
	c.emit("agent.start", "shiro", "mio", "Coder結果をShiroで整形", string(route), jid, sessionID, t.Channel(), t.ChatID())
	shiroTask := domaintransport.NewMessage("mio", "shiro", sessionID, jid, coderResult.Content)
	shiroTask.Type = domaintransport.MessageTypeTask
	shiroTask.Context = map[string]interface{}{
		"route":       string(route),
		"coder_agent": coderAgent,
		"channel":     t.Channel(),
		"chat_id":     t.ChatID(),
	}
	c.memory.RecordMessage(shiroTask)
	shiroResult, err := c.executeToAgent(ctx, "shiro", shiroTask)
	if err != nil {
		return "", err
	}
	c.emit("agent.response", "shiro", "mio", shiroResult.Content, string(route), jid, sessionID, t.Channel(), t.ChatID())
	c.emitNote("shiro", "mio", fmt.Sprintf("%sの作業が終わりました。", displayAgentName(coderAgent)), string(route), jid, sessionID, t.Channel(), t.ChatID())
	return shiroResult.Content, nil
}

func (c *distributedCodeExecutionCoordinator) executeProposal(ctx context.Context, t task.Task, route routing.Route, sessionID, jid, coderAgent string, coderResult domaintransport.Message, attempt int) (response, retryRequest string, retryable bool, err error) {
	c.emit("agent.start", "shiro", "mio", "CoderのProposalをWorker実行", string(route), jid, sessionID, t.Channel(), t.ChatID())
	execMsg := domaintransport.NewMessage("mio", "shiro", sessionID, jid, "Execute coder proposal")
	execMsg.Type = domaintransport.MessageTypeTask
	execMsg.Context = map[string]interface{}{
		"route":         string(route),
		"coder_agent":   coderAgent,
		"retry_attempt": attempt,
		"channel":       t.Channel(),
		"chat_id":       t.ChatID(),
	}
	if c.moduleResolver != nil {
		if resolved := c.moduleResolver.Resolve(t.UserMessage()); resolved.Found() {
			execMsg.Context["module_id"] = resolved.Module.ID
			execMsg.Context["module_root"] = resolved.Module.Root
			execMsg.Context["module_display_name"] = resolved.Module.DisplayName
		}
	}
	execMsg.Proposal = coderResult.Proposal
	c.memory.RecordMessage(execMsg)

	shiroResult, err := c.executeToAgent(ctx, "shiro", execMsg)
	if err != nil {
		failureKind, reason, retryableFailure := classifyDistributedExecutionError(err)
		if retryableFailure && attempt < c.coderRetryMax() {
			c.emit("worker.classified_failure", "shiro", coderAgent, fmt.Sprintf("%s: %s", failureKind, reason), string(route), jid, sessionID, t.Channel(), t.ChatID())
			c.recordCoderProposalEvidence(ctx, t, route, sessionID, jid, coderAgent, coderResult.Proposal, domaintransport.Message{}, err)
			return "", buildCoderRetryInstruction(t.UserMessage(), coderResult.Proposal, failureKind, reason, attempt+1), true, nil
		}
		c.recordCoderProposalEvidence(ctx, t, route, sessionID, jid, coderAgent, coderResult.Proposal, domaintransport.Message{}, err)
		return "", "", false, err
	}
	c.emit("agent.response", "shiro", "mio", shiroResult.Content, string(route), jid, sessionID, t.Channel(), t.ChatID())
	c.emitNote("shiro", "mio", fmt.Sprintf("%sの作業が終わりました。", displayAgentName(coderAgent)), string(route), jid, sessionID, t.Channel(), t.ChatID())
	c.recordCoderProposalEvidence(ctx, t, route, sessionID, jid, coderAgent, coderResult.Proposal, shiroResult, nil)

	if retryReq, ok := nextCoderRetryRequest(t.UserMessage(), coderResult.Proposal, shiroResult, attempt); ok {
		return "", retryReq, true, nil
	}
	return shiroResult.Content, "", false, nil
}

func (c *distributedCodeExecutionCoordinator) recordCoderProposalEvidence(
	ctx context.Context,
	t task.Task,
	route routing.Route,
	sessionID, jid, coderAgent string,
	p *domaintransport.ProposalPayload,
	shiroResult domaintransport.Message,
	runErr error,
) {
	if c.proposalEvidence == nil || p == nil {
		return
	}
	evidence := domainskill.CoderProposalEvidence{
		JobID:            jid,
		SessionID:        sessionID,
		Route:            string(route),
		Agent:            coderAgent,
		TaskText:         t.UserMessage(),
		Plan:             p.Plan,
		Patch:            p.Patch,
		Risk:             p.Risk,
		CostHint:         p.CostHint,
		FormattedResult:  shiroResult.Content,
		ExecutionSummary: distributedResultSummary(shiroResult),
		Success:          runErr == nil,
	}
	if shiroResult.Result != nil {
		evidence.Success = shiroResult.Result.Success
	}
	if runErr != nil {
		evidence.ExecutionError = runErr.Error()
	}
	paths, err := c.proposalEvidence.SaveCoderProposalEvidence(ctx, evidence)
	if err != nil {
		log.Printf("WARN: failed to save distributed coder proposal evidence job=%s route=%s: %v", jid, route, err)
		return
	}
	if paths.SkillDiffPath != "" || paths.AgentTranscriptPath != "" {
		log.Printf("Distributed coder proposal evidence saved job=%s skill_diff=%s agent_transcript=%s", jid, paths.SkillDiffPath, paths.AgentTranscriptPath)
	}
}

func distributedResultSummary(msg domaintransport.Message) string {
	if msg.Result == nil {
		return ""
	}
	if msg.Result.Summary != "" {
		return msg.Result.Summary
	}
	return fmt.Sprintf("実行: %d 件, 成功: %d 件, 失敗: %d 件", msg.Result.ExecutedCmds, msg.Result.ExecutedCmds-msg.Result.FailedCmds, msg.Result.FailedCmds)
}
