package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmodule "github.com/Nyukimin/RenCrow_CORE/internal/domain/moduleregistry"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const codeDiagnosticNoWriteDirective = "[CODE DIAGNOSTIC ONLY: NO FILE OR COMMAND CHANGES]"

// CodeExecutor はコード生成タスクの実行を担当
type CodeExecutor interface {
	ExecuteCode(ctx context.Context, req CodeExecutionRequest) (CodeExecutionResponse, error)
}

// CodeExecutionRequest はコード実行リクエスト
type CodeExecutionRequest struct {
	Input             domainconversation.TurnInput
	TaskID            modulecore.TaskID
	Module            domainmodule.Resolution
	noWriteDiagnostic bool
}

// DefaultCodeExecutor は標準的なCodeExecutor実装
type DefaultCodeExecutor struct {
	coder1           CoderAgent
	coder2           CoderAgent
	coder3           CoderAgent
	coder4           CoderAgent // v4.1: 4th coder slot
	workerExecution  service.WorkerExecutionService
	coderStatus      *CoderStatus // atomic busy state and generic round-robin cursor
	eventEmitter     func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string)
	coderCaps        []capability.CoderCapability // enabled/probe-ready候補情報。汎用CODEの候補判定に使う。
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
	if coderStatus == nil {
		coderStatus = NewCoderStatus()
	}
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

// WithCapabilities は enabled/probe-ready 候補情報を注入する。明示 route は指定Coderをpinし、汎用CODEはreadyなローカル候補をround-robin選択する。
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
	req.noWriteDiagnostic = isExplicitCodeRoute(req.Input.Route()) && hasCodeDiagnosticNoWriteDirective(req.Input.MessageText())
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
	if strings.HasPrefix(strings.TrimSpace(req.Input.MessageText()), "/tasks ") {
		planningCoder, ok := target.coder.(interface {
			DispatchTaskPlanning(context.Context, string) (string, bool, error)
		})
		if !ok {
			return CodeExecutionResponse{}, fmt.Errorf("%s does not expose native TaskCenter planning", target.name)
		}
		response, handled, err := planningCoder.DispatchTaskPlanning(ctx, req.Input.MessageText())
		if err != nil {
			return CodeExecutionResponse{}, err
		}
		if !handled {
			return CodeExecutionResponse{}, fmt.Errorf("native TaskCenter command was not dispatched")
		}
		e.emitCoderGenerateResponse(req, target, response)
		return buildCoderGenerateResponse(response), nil
	}

	// CoderLoop パス: CoderAgentWithLoop かつ loopPrompt が設定されている場合
	if !req.noWriteDiagnostic {
		if loopPrompt, ok := e.coderLoopPrompts[target.name]; ok && loopPrompt != "" {
			if loopCoder, ok := target.coder.(CoderAgentWithLoop); ok && e.workerExecution != nil {
				loopExec := NewCoderLoopExecutor(loopCoder, e.workerExecution, target.name, loopPrompt, e.eventEmitter)
				return loopExec.Execute(ctx, req)
			}
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

func isExplicitCodeRoute(route routing.Route) bool {
	switch route {
	case routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4:
		return true
	default:
		return false
	}
}

func hasCodeDiagnosticNoWriteDirective(message string) bool {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	if len(lines) == 0 {
		return false
	}
	firstLine := strings.TrimSpace(lines[0])
	if firstLine == codeDiagnosticNoWriteDirective {
		return true
	}
	for _, routePrefix := range []string{"/code1", "/code2", "/code3", "/code4"} {
		if firstLine == routePrefix {
			return len(lines) > 1 && strings.TrimSpace(lines[1]) == codeDiagnosticNoWriteDirective
		}
		if !strings.HasPrefix(firstLine, routePrefix) || len(firstLine) == len(routePrefix) {
			continue
		}
		if !strings.ContainsAny(firstLine[len(routePrefix):len(routePrefix)+1], " \t\r") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(firstLine, routePrefix)) == codeDiagnosticNoWriteDirective
	}
	return false
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
