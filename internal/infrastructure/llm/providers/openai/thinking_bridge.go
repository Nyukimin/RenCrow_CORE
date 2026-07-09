package openai

import modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"

func (p *OpenAIProvider) addThinkingBridgeFields(req map[string]interface{}, streaming bool) {
	modulellm.ApplyThinkingBridgeFields(req, p.thinkingBridge, streaming)
}

func (p *OpenAIProvider) addProviderOptions(req map[string]interface{}, options map[string]any) {
	modulellm.ApplyThinkingBridgeProviderOptions(req, p.thinkingBridge, options)
}

func (p *OpenAIProvider) addModelContextOption(req map[string]interface{}) {
	if !p.thinkingBridge || p.modelContext <= 0 || req == nil {
		return
	}
	rawOptions := req["options"]
	if rawOptions == nil {
		req["options"] = map[string]any{"num_ctx": p.modelContext}
		return
	}
	options, ok := rawOptions.(map[string]any)
	if !ok {
		return
	}
	if _, exists := options["num_ctx"]; !exists {
		options["num_ctx"] = p.modelContext
	}
}

func (p *OpenAIProvider) sanitizeThinkingBridgeContent(content, parseStatus, _ string) string {
	return modulellm.SanitizeThinkingBridgeContent(p.thinkingBridge, content, parseStatus)
}

func looksLikeUntaggedReasoning(s string) bool {
	return modulellm.LooksLikeUntaggedReasoning(s)
}

func extractFinalAnswerFromUntaggedReasoning(s string) string {
	return modulellm.ExtractFinalAnswerFromUntaggedReasoning(s)
}
