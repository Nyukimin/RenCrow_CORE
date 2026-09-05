package orchestrator

import (
	"context"
	"fmt"
	"log"

	moduleapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moduleregistry"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

func (c *distributedCodeExecutionCoordinator) Execute(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	jid := jobID.String()
	coderAgent := c.selectCoder(route, input.MessageText())
	if coderAgent == "" {
		return "", fmt.Errorf("no coder mapped for route %s", route)
	}
	log.Printf("[DistributedOrch] code handoff route=%s target=%s job=%s", route, coderAgent, jid)

	shiroWork := fmt.Sprintf("route=%s job=%s のコード作業取りまとめ", route, jid)
	c.emit("agent.delegate", "mio", "shiro", formatAgentHandoffSpeech("mio", "shiro", shiroWork, input.MessageText()), string(route), jid, sessionID, channel, chatID)
	c.emit("agent.acknowledge", "shiro", "mio", formatAgentHandoffReadbackSpeech("mio", "shiro", shiroWork, input.MessageText()), string(route), jid, sessionID, channel, chatID)
	c.emit("agent.start", "mio", "shiro", "コードタスクをShiro経由で実行", string(route), jid, sessionID, channel, chatID)
	c.emitNote("mio", "user", "しろにコード実装の取りまとめをお願いしたよ。", string(route), jid, sessionID, channel, chatID)
	requestText := input.MessageText()
	if c.moduleResolver == nil {
		c.moduleResolver = moduleapp.DefaultRegistry()
	}
	if resolved := c.moduleResolver.Resolve(input.MessageText()); resolved.Found() {
		c.emit("module.selected", "mio", "shiro", resolved.Summary(), string(route), jid, sessionID, channel, chatID)
		requestText = appendModuleContextToCodeRequest(requestText, resolved)
	}

	for attempt := 0; attempt <= c.coderRetryMax(); attempt++ {
		coderWork := fmt.Sprintf("route=%s job=%s retry=%d の設計・コード生成", route, jid, attempt)
		c.emit("agent.delegate", "shiro", coderAgent, formatAgentHandoffSpeech("shiro", coderAgent, coderWork, requestText), string(route), jid, sessionID, channel, chatID)
		c.emit("agent.acknowledge", coderAgent, "shiro", formatAgentHandoffReadbackSpeech("shiro", coderAgent, coderWork, requestText), string(route), jid, sessionID, channel, chatID)
		c.emit("agent.start", "shiro", coderAgent, requestText, string(route), jid, sessionID, channel, chatID)
		if attempt == 0 {
			c.emitNote("shiro", "mio", fmt.Sprintf("%sにコーディング依頼しました。進捗を監視して、必要なら作業を前に進めます。", displayAgentName(coderAgent)), string(route), jid, sessionID, channel, chatID)
		} else {
			c.emit("worker.retry_request", "shiro", coderAgent, fmt.Sprintf("retry=%d", attempt), string(route), jid, sessionID, channel, chatID)
			c.emitNote("shiro", "mio", fmt.Sprintf("%sに修正版patchを再依頼します。retry=%d", displayAgentName(coderAgent), attempt), string(route), jid, sessionID, channel, chatID)
		}

		coderMsg, err := c.buildCoderMessage(coderAgent, requestText, route, input, jobID, attempt)
		if err != nil {
			return "", err
		}
		c.memory.RecordMessage(coderMsg)
		coderResult, err := c.executeMailbox(ctx, coderAgent, coderMsg, "mio")
		if err != nil {
			failureKind, reason, retryable := classifyDistributedExecutionError(err)
			failureReport := fmt.Sprintf("実行失敗: %s: %s", failureKind, reason)
			c.emit("agent.report", coderAgent, "shiro", formatAgentHandoffCompletionSpeech("shiro", coderAgent, failureReport), string(route), jid, sessionID, channel, chatID)
			if retryable && attempt < c.coderRetryMax() {
				c.emit("worker.classified_failure", "shiro", coderAgent, fmt.Sprintf("%s: %s", failureKind, reason), string(route), jid, sessionID, channel, chatID)
				requestText = buildCoderRetryInstruction(input.MessageText(), nil, failureKind, reason, attempt+1)
				continue
			}
			c.emit("agent.report", "shiro", "mio", formatAgentHandoffCompletionSpeech("mio", "shiro", failureReport), string(route), jid, sessionID, channel, chatID)
			return "", err
		}
		c.emit("agent.response", coderAgent, "shiro", coderResult.Content, string(route), jid, sessionID, channel, chatID)
		c.emit("agent.report", coderAgent, "shiro", formatAgentHandoffCompletionSpeech("shiro", coderAgent, coderResult.Content), string(route), jid, sessionID, channel, chatID)
		c.emitNote(coderAgent, "shiro", "おわったっす。", string(route), jid, sessionID, channel, chatID)
		c.emitNote("shiro", "mio", fmt.Sprintf("%sの結果を受け取って、内容確認と仕上げを進めます。", displayAgentName(coderAgent)), string(route), jid, sessionID, channel, chatID)

		if coderResult.Proposal == nil {
			return c.finishWithoutProposal(ctx, input, route, jobID, coderAgent, coderResult)
		}
		response, retryReq, retryable, err := c.executeProposal(ctx, input, route, jobID, coderAgent, coderResult, attempt)
		if err != nil {
			return "", err
		}
		if retryable {
			requestText = retryReq
			continue
		}
		return response, nil
	}
	err := fmt.Errorf("coder retry budget exhausted for job %s", jid)
	c.emit("agent.report", "shiro", "mio", formatAgentHandoffCompletionSpeech("mio", "shiro", "実行失敗: "+err.Error()), string(route), jid, sessionID, channel, chatID)
	return "", err
}

func (c *distributedCodeExecutionCoordinator) buildCoderMessage(coderAgent, requestText string, route routing.Route, input domainconversation.TurnInput, jobID modulecore.TaskID, attempt int) (domaintransport.Message, error) {
	_, channel, chatID := turnInputMetadata(input)
	coderInput := input.WithMessageText(requestText).WithRoute(route)
	coderMsg, err := domaintransport.NewTurnInputMessage("shiro", coderAgent, jobID.String(), coderInput)
	if err != nil {
		return domaintransport.Message{}, fmt.Errorf("build coder turn message: %w", err)
	}
	coderMsg.Type = domaintransport.MessageTypeTask
	coderMsg.Context = map[string]interface{}{
		"route":         string(route),
		"retry_attempt": attempt,
		"channel":       channel,
		"chat_id":       chatID,
	}
	if configs := c.coderConfigs(); configs != nil {
		if coderCfg, ok := configs[coderAgent]; ok {
			coderMsg.Context["coder_config"] = coderCfg
		}
	}
	return coderMsg, nil
}

func (c *distributedCodeExecutionCoordinator) finishWithoutProposal(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID, coderAgent string, coderResult domaintransport.Message) (string, error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	jid := jobID.String()
	c.emit("agent.start", "shiro", "mio", "Coder結果をShiroで整形", string(route), jid, sessionID, channel, chatID)
	shiroInput := input.WithMessageText(coderResult.Content).WithRoute(route)
	shiroTask, err := domaintransport.NewTurnInputMessage("mio", "shiro", jid, shiroInput)
	if err != nil {
		return "", fmt.Errorf("build shiro formatted turn message: %w", err)
	}
	shiroTask.Type = domaintransport.MessageTypeTask
	shiroTask.Context = map[string]interface{}{
		"route":       string(route),
		"coder_agent": coderAgent,
		"channel":     channel,
		"chat_id":     chatID,
	}
	c.memory.RecordMessage(shiroTask)
	shiroResult, err := c.executeToAgent(ctx, "shiro", shiroTask)
	if err != nil {
		c.emit("agent.report", "shiro", "mio", formatAgentHandoffCompletionSpeech("mio", "shiro", "実行失敗: "+err.Error()), string(route), jid, sessionID, channel, chatID)
		return "", err
	}
	c.emit("agent.response", "shiro", "mio", shiroResult.Content, string(route), jid, sessionID, channel, chatID)
	c.emit("agent.report", "shiro", "mio", formatAgentHandoffCompletionSpeech("mio", "shiro", shiroResult.Content), string(route), jid, sessionID, channel, chatID)
	c.emitNote("shiro", "mio", fmt.Sprintf("%sの作業が終わりました。", displayAgentName(coderAgent)), string(route), jid, sessionID, channel, chatID)
	return shiroResult.Content, nil
}

func (c *distributedCodeExecutionCoordinator) executeProposal(ctx context.Context, input domainconversation.TurnInput, route routing.Route, jobID modulecore.TaskID, coderAgent string, coderResult domaintransport.Message, attempt int) (response, retryRequest string, retryable bool, err error) {
	sessionID, channel, chatID := turnInputMetadata(input)
	jid := jobID.String()
	c.emit("agent.start", "shiro", "mio", "CoderのProposalをWorker実行", string(route), jid, sessionID, channel, chatID)
	execMsg := domaintransport.NewMessage("mio", "shiro", sessionID, jid, "Execute coder proposal")
	execMsg.Type = domaintransport.MessageTypeTask
	execMsg.Context = map[string]interface{}{
		"route":         string(route),
		"coder_agent":   coderAgent,
		"retry_attempt": attempt,
		"channel":       channel,
		"chat_id":       chatID,
	}
	if c.moduleResolver != nil {
		if resolved := c.moduleResolver.Resolve(input.MessageText()); resolved.Found() {
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
			c.emit("worker.classified_failure", "shiro", coderAgent, fmt.Sprintf("%s: %s", failureKind, reason), string(route), jid, sessionID, channel, chatID)
			c.recordCoderProposalEvidence(ctx, input, route, jobID, coderAgent, coderResult.Proposal, domaintransport.Message{}, err)
			return "", buildCoderRetryInstruction(input.MessageText(), coderResult.Proposal, failureKind, reason, attempt+1), true, nil
		}
		c.recordCoderProposalEvidence(ctx, input, route, jobID, coderAgent, coderResult.Proposal, domaintransport.Message{}, err)
		c.emit("agent.report", "shiro", "mio", formatAgentHandoffCompletionSpeech("mio", "shiro", "実行失敗: "+err.Error()), string(route), jid, sessionID, channel, chatID)
		return "", "", false, err
	}
	c.emit("agent.response", "shiro", "mio", shiroResult.Content, string(route), jid, sessionID, channel, chatID)
	c.emit("agent.report", "shiro", "mio", formatAgentHandoffCompletionSpeech("mio", "shiro", shiroResult.Content), string(route), jid, sessionID, channel, chatID)
	c.emitNote("shiro", "mio", fmt.Sprintf("%sの作業が終わりました。", displayAgentName(coderAgent)), string(route), jid, sessionID, channel, chatID)
	c.recordCoderProposalEvidence(ctx, input, route, jobID, coderAgent, coderResult.Proposal, shiroResult, nil)

	if retryReq, ok := nextCoderRetryRequest(input.MessageText(), coderResult.Proposal, shiroResult, attempt); ok {
		return "", retryReq, true, nil
	}
	return shiroResult.Content, "", false, nil
}

func (c *distributedCodeExecutionCoordinator) recordCoderProposalEvidence(
	ctx context.Context,
	input domainconversation.TurnInput,
	route routing.Route,
	jobID modulecore.TaskID,
	coderAgent string,
	p *domaintransport.ProposalPayload,
	shiroResult domaintransport.Message,
	runErr error,
) {
	if c.proposalEvidence == nil || p == nil {
		return
	}
	evidence := domainskill.CoderProposalEvidence{
		JobID:            jobID.String(),
		SessionID:        input.SessionID(),
		Route:            string(route),
		Agent:            coderAgent,
		TaskText:         input.MessageText(),
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
		log.Printf("WARN: failed to save distributed coder proposal evidence job=%s route=%s: %v", jobID.String(), route, err)
		return
	}
	if paths.SkillDiffPath != "" || paths.AgentTranscriptPath != "" {
		log.Printf("Distributed coder proposal evidence saved job=%s skill_diff=%s agent_transcript=%s", jobID.String(), paths.SkillDiffPath, paths.AgentTranscriptPath)
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
