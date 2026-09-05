package orchestrator

import (
	"context"
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmodule "github.com/Nyukimin/RenCrow_CORE/internal/domain/moduleregistry"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// CodeExecutor はコード生成タスクの実行を担当
type CodeExecutor interface {
	ExecuteCode(ctx context.Context, req CodeExecutionRequest) (CodeExecutionResponse, error)
}

// CodeExecutionRequest はコード実行リクエスト
type CodeExecutionRequest struct {
	Input  domainconversation.TurnInput
	TaskID modulecore.TaskID
	Module domainmodule.Resolution
}

// DefaultCodeExecutor は標準的なCodeExecutor実装
type DefaultCodeExecutor struct {
	coder1           CoderAgent
	coder2           CoderAgent
	coder3           CoderAgent
	coder4           CoderAgent // v4.1: 4th coder slot
	workerExecution  service.WorkerExecutionService
	coderStatus      *CoderStatus // optional: coder busy state management
	eventEmitter     func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string)
	coderCaps        []capability.CoderCapability // 診断用。Coder 自動切替には使わない。
	externalCoders   map[string]bool              // true の coder は明示 route でのみ使う。
	proposalEvidence CoderProposalEvidenceRecorder
	coderLoopPrompts map[string]string // coder名 → CoderLoop システムプロンプト
	moduleResolver   ModuleResolver
}

type ModuleResolver interface {
	Resolve(message string) domainmodule.Resolution
}

// NewDefaultCodeExecutor は新しいDefaultCodeExecutorを作成
func NewDefaultCodeExecutor(
	coder1, coder2, coder3, coder4 CoderAgent,
	workerExecution service.WorkerExecutionService,
	coderStatus *CoderStatus,
	eventEmitter func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string),
) *DefaultCodeExecutor {
	return &DefaultCodeExecutor{
		coder1:          coder1,
		coder2:          coder2,
		coder3:          coder3,
		coder4:          coder4,
		workerExecution: workerExecution,
		coderStatus:     coderStatus,
		eventEmitter:    eventEmitter,
	}
}

// WithCapabilities は診断用の能力情報を保持する。Coder 選択は明示 route と Coder1 既定に限定する。
func (e *DefaultCodeExecutor) WithCapabilities(caps []capability.CoderCapability) *DefaultCodeExecutor {
	e.coderCaps = caps
	return e
}

func (e *DefaultCodeExecutor) WithExternalCoderPolicy(external map[string]bool) *DefaultCodeExecutor {
	e.externalCoders = make(map[string]bool, len(external))
	for name, isExternal := range external {
		e.externalCoders[name] = isExternal
	}
	return e
}

func (e *DefaultCodeExecutor) WithCoderProposalEvidenceRecorder(recorder CoderProposalEvidenceRecorder) *DefaultCodeExecutor {
	e.proposalEvidence = recorder
	return e
}

// WithCoderLoopPrompts は CoderLoop 用のシステムプロンプトを設定する（coder名 → プロンプト）
func (e *DefaultCodeExecutor) WithCoderLoopPrompts(prompts map[string]string) *DefaultCodeExecutor {
	e.coderLoopPrompts = prompts
	return e
}

func (e *DefaultCodeExecutor) WithModuleResolver(resolver ModuleResolver) *DefaultCodeExecutor {
	e.moduleResolver = resolver
	return e
}

// ExecuteCode はコード生成タスクを実行
func (e *DefaultCodeExecutor) ExecuteCode(ctx context.Context, req CodeExecutionRequest) (CodeExecutionResponse, error) {
	req = e.resolveModuleForRequest(req)
	route := req.Input.Route()
	target, err := e.selectCoderForRoute(route)
	if err != nil {
		return CodeExecutionResponse{}, err
	}
	// CoderStatusのrelease処理
	if target.release != nil {
		defer target.release()
	}

	log.Printf("[CodeExecutor] code handoff route=%s target=%s task=%s", route, target.name, req.TaskID)

	e.emitCodeHandoffStart(req, target)

	// CoderLoop パス: CoderAgentWithLoop かつ loopPrompt が設定されている場合
	if loopPrompt, ok := e.coderLoopPrompts[target.name]; ok && loopPrompt != "" {
		if loopCoder, ok := target.coder.(CoderAgentWithLoop); ok && e.workerExecution != nil {
			loopExec := NewCoderLoopExecutor(loopCoder, e.workerExecution, target.name, loopPrompt, e.eventEmitter)
			return loopExec.Execute(ctx, req)
		}
	}

	// CODE 系の明示ルートは、Proposal生成が可能ならWorkerで即時実行する。
	if shouldUseProposalPath(route, target) && e.workerExecution != nil {
		if resp, handled, err := e.tryExecuteProposalPath(ctx, req, target); handled {
			return resp, err
		}
	}

	return e.executeCoderGeneratePath(ctx, req, target)
}

func (e *DefaultCodeExecutor) resolveModuleForRequest(req CodeExecutionRequest) CodeExecutionRequest {
	if req.Module.Found() || e.moduleResolver == nil {
		return req
	}
	resolved := e.moduleResolver.Resolve(req.Input.MessageText())
	sessionID, channel, chatID := turnInputMetadata(req.Input)
	if !resolved.Found() {
		if resolved.Ambiguous {
			e.emit("module.unresolved", "mio", "shiro", resolved.Summary(), req.Input.Route().String(), req.TaskID, sessionID, channel, chatID)
		}
		return req
	}
	req.Module = resolved
	e.emit("module.selected", "mio", "shiro", resolved.Summary(), req.Input.Route().String(), req.TaskID, sessionID, channel, chatID)
	req.Input = req.Input.WithMessageText(appendModuleContextToCodeRequest(req.Input.MessageText(), resolved))
	return req
}
