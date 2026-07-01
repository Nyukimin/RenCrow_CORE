package security

import "strings"

const (
	PromptInjectionIgnoreInstructions = "prompt_injection.ignore_instructions"
	PromptInjectionSystemPrompt       = "prompt_injection.system_prompt"
	PromptInjectionToolOverride       = "prompt_injection.tool_override"
)

// DetectPromptInjectionWarnings returns risk metadata for untrusted external text.
func DetectPromptInjectionWarnings(text string) []string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return nil
	}
	var warnings []string
	if strings.Contains(normalized, "ignore previous instructions") ||
		strings.Contains(normalized, "ignore all previous") ||
		strings.Contains(normalized, "これまでの指示を無視") ||
		strings.Contains(normalized, "以前の指示を無視") {
		warnings = append(warnings, PromptInjectionIgnoreInstructions)
	}
	if strings.Contains(normalized, "system prompt") ||
		strings.Contains(normalized, "システムプロンプト") {
		warnings = append(warnings, PromptInjectionSystemPrompt)
	}
	if strings.Contains(normalized, "tool call") ||
		strings.Contains(normalized, "run shell") ||
		strings.Contains(normalized, "execute command") ||
		strings.Contains(normalized, "コマンドを実行") {
		warnings = append(warnings, PromptInjectionToolOverride)
	}
	return uniqueWarnings(warnings)
}

func uniqueWarnings(warnings []string) []string {
	if len(warnings) < 2 {
		return warnings
	}
	seen := make(map[string]bool, len(warnings))
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		if seen[warning] {
			continue
		}
		seen[warning] = true
		out = append(out, warning)
	}
	return out
}
