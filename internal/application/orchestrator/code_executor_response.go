package orchestrator

// CodeExecutionResponse はコード実行レスポンス
type CodeExecutionResponse struct {
	Response string
	Handled  bool // Proposal経由で処理された場合true
}

func buildProposalHandledResponse(response string) CodeExecutionResponse {
	return CodeExecutionResponse{Response: response, Handled: true}
}

func buildCoderGenerateResponse(response string) CodeExecutionResponse {
	return CodeExecutionResponse{Response: response, Handled: false}
}
