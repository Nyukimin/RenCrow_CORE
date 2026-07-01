package orchestrator

import "context"

// executeCoderGeneratePath は通常のGenerate実行パス
func (e *DefaultCodeExecutor) executeCoderGeneratePath(
	ctx context.Context,
	req CodeExecutionRequest,
	target codeTarget,
) (CodeExecutionResponse, error) {
	resp, err := target.coder.Generate(ctx, req.Task, target.systemPrompt)
	if err != nil {
		e.emitCoderGenerateError(req, target, err)
		return CodeExecutionResponse{}, err
	}

	e.emitCoderGenerateResponse(req, target, resp)

	return buildCoderGenerateResponse(resp), nil
}

func (e *DefaultCodeExecutor) emitCoderGenerateError(req CodeExecutionRequest, target codeTarget, err error) {
	e.emit("agent.response", target.name, "shiro", "エラー: "+err.Error(), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
}

func (e *DefaultCodeExecutor) emitCoderGenerateResponse(req CodeExecutionRequest, target codeTarget, response string) {
	content := truncate(response, 500)
	e.emit("agent.response", target.name, "shiro", content, req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
	e.emit("agent.response", "shiro", "mio", content, req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
}
