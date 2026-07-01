package llm

import "strings"

var thinkingBridgeReservedOptionKeys = map[string]struct{}{
	"":            {},
	"model":       {},
	"messages":    {},
	"max_tokens":  {},
	"temperature": {},
	"stream":      {},
}

func ApplyThinkingBridgeFields(req map[string]interface{}, enabled bool, streaming bool) {
	if !enabled || req == nil {
		return
	}
	req["think"] = false // gemma4 thinking モデルで response が空になる問題を回避
	req["parse_reasoning"] = true
	req["include_reasoning"] = false
	req["separate_reasoning"] = true
	if streaming {
		req["stream"] = true
	}
}

func ApplyThinkingBridgeProviderOptions(req map[string]interface{}, enabled bool, options map[string]any) {
	if !enabled || req == nil || len(options) == 0 {
		return
	}
	for key, value := range options {
		normalized := strings.TrimSpace(key)
		if _, reserved := thinkingBridgeReservedOptionKeys[normalized]; reserved {
			continue
		}
		req[normalized] = value
	}
}

func SanitizeThinkingBridgeContent(enabled bool, content, parseStatus string) string {
	if !enabled {
		return content
	}
	if strings.TrimSpace(parseStatus) != "no_reasoning" {
		return content
	}
	if !LooksLikeUntaggedReasoning(content) {
		return content
	}
	if final := ExtractFinalAnswerFromUntaggedReasoning(content); final != "" {
		return final
	}
	return ""
}

func LooksLikeUntaggedReasoning(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	startsLikeReasoning := strings.HasPrefix(lower, "okay,") ||
		strings.HasPrefix(lower, "ok,") ||
		strings.HasPrefix(lower, "let me ") ||
		strings.HasPrefix(lower, "we need ") ||
		strings.HasPrefix(lower, "i need ") ||
		strings.HasPrefix(lower, "i should ") ||
		strings.HasPrefix(lower, "the user ")
	if !startsLikeReasoning {
		return false
	}
	markers := []string{
		"the user",
		"they wrote",
		"the query",
		"let me",
		"i need to",
		"i should",
		"translates to",
		"asking for",
		"want me to",
		"need to respond",
		"final answer",
	}
	hits := 0
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			hits++
		}
	}
	return hits >= 2
}

func ExtractFinalAnswerFromUntaggedReasoning(s string) string {
	candidates := []string{
		"Final answer:",
		"Final Answer:",
		"final answer:",
		"最終回答:",
		"最終回答：",
		"回答:",
		"回答：",
	}
	for _, marker := range candidates {
		if idx := strings.LastIndex(s, marker); idx >= 0 {
			return strings.TrimSpace(s[idx+len(marker):])
		}
	}
	return ""
}
