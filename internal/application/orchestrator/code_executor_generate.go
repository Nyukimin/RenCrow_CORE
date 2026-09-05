package orchestrator

import "context"

// executeCoderGeneratePath は通常のGenerate実行パス
func (e *DefaultCodeExecutor) executeCoderGeneratePath(
	ctx context.Context,
	req CodeExecutionRequest,
	target codeTarget,
) (CodeExecutionResponse, error) {
	resp, err := target.coder.Generate(ctx, req.Input, target.systemPrompt)
	if err != nil {
		e.emitCoderGenerateError(req, target, err)
		return CodeExecutionResponse{}, err
	}

	e.emitCoderGenerateResponse(req, target, resp)

	return buildCoderGenerateResponse(resp), nil
}

func (e *DefaultCodeExecutor) emitCoderGenerateError(req CodeExecutionRequest, target codeTarget, err error) {
	report := "実行失敗: " + err.Error()
	sessionID, channel, chatID := turnInputMetadata(req.Input)
	route := req.Input.Route()
	e.emit("agent.response", target.name, "shiro", "エラー: "+err.Error(), route.String(), req.JobID, sessionID, channel, chatID)
	e.emit("agent.report", target.name, "shiro", formatAgentHandoffCompletionSpeech("shiro", target.name, report), route.String(), req.JobID, sessionID, channel, chatID)
	e.emit("agent.report", "shiro", "mio", formatShiroToMioReport(route, req.JobID, report), route.String(), req.JobID, sessionID, channel, chatID)
}

func (e *DefaultCodeExecutor) emitCoderGenerateResponse(req CodeExecutionRequest, target codeTarget, response string) {
	content := truncate(response, 500)
	sessionID, channel, chatID := turnInputMetadata(req.Input)
	route := req.Input.Route()
	e.emit("agent.response", target.name, "shiro", content, route.String(), req.JobID, sessionID, channel, chatID)
	e.emit("agent.report", target.name, "shiro", formatAgentHandoffCompletionSpeech("shiro", target.name, content), route.String(), req.JobID, sessionID, channel, chatID)
	e.emit("agent.response", "shiro", "mio", content, route.String(), req.JobID, sessionID, channel, chatID)
	e.emit("agent.report", "shiro", "mio", formatShiroToMioReport(route, req.JobID, content), route.String(), req.JobID, sessionID, channel, chatID)
}
