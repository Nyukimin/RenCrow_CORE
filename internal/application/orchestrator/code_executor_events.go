package orchestrator

import modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

func (e *DefaultCodeExecutor) emitCodeHandoffStart(req CodeExecutionRequest, target codeTarget) {
	sessionID, channel, chatID := turnInputMetadata(req.Input)
	route := req.Input.Route()
	taskID := req.TaskID.String()
	e.emit("agent.delegate", "mio", "shiro", formatMioToShiroInstruction(req.Input, route, taskID), route.String(), req.TaskID, sessionID, channel, chatID)
	e.emit("agent.acknowledge", "shiro", "mio", formatShiroReadbackToMio(req.Input, route, taskID), route.String(), req.TaskID, sessionID, channel, chatID)
	e.emit("agent.start", "mio", "shiro", "コードタスクをShiro経由で実行", route.String(), req.TaskID, sessionID, channel, chatID)
	work := "route=" + route.String() + " task=" + taskID + " の設計・コード生成"
	e.emit("agent.delegate", "shiro", target.name, formatAgentHandoffSpeech("shiro", target.name, work, req.Input.MessageText()), route.String(), req.TaskID, sessionID, channel, chatID)
	e.emit("agent.acknowledge", target.name, "shiro", formatAgentHandoffReadbackSpeech("shiro", target.name, work, req.Input.MessageText()), route.String(), req.TaskID, sessionID, channel, chatID)
	e.emit("agent.start", "shiro", target.name, req.Input.MessageText(), route.String(), req.TaskID, sessionID, channel, chatID)
}

func (e *DefaultCodeExecutor) emit(eventType, from, to, content, route string, taskID modulecore.TaskID, sessionID, channel, chatID string) {
	if e.eventEmitter != nil {
		e.eventEmitter(eventType, from, to, content, route, taskID.String(), sessionID, channel, chatID)
	}
}

// SetEventEmitter はイベント発火関数を設定
func (e *DefaultCodeExecutor) SetEventEmitter(emitter func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string)) {
	e.eventEmitter = emitter
}
