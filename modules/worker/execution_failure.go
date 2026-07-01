package worker

import "strings"

type ExecutionFailureClassification struct {
	Kind      string
	Reason    string
	Retryable bool
}

func ClassifyExecutionFailure(errText, output string) ExecutionFailureClassification {
	text := strings.ToLower(strings.TrimSpace(errText + "\n" + output))
	switch {
	case strings.Contains(text, "patch parse error"):
		return ExecutionFailureClassification{Kind: "patch_parse_failed", Reason: strings.TrimSpace(errText), Retryable: true}
	case strings.Contains(text, "security error"), strings.Contains(text, "protected file"):
		return ExecutionFailureClassification{Kind: "unsafe_operation", Reason: strings.TrimSpace(errText), Retryable: false}
	case strings.Contains(text, "command not found"), strings.Contains(text, "not found"), strings.Contains(text, "exit status 127"):
		return ExecutionFailureClassification{Kind: "missing_command", Reason: strings.TrimSpace(errText), Retryable: true}
	case strings.Contains(text, "no module named"), strings.Contains(text, "module not found"), strings.Contains(text, "cannot find package"), strings.Contains(text, "missing dependency"):
		return ExecutionFailureClassification{Kind: "missing_dependency", Reason: strings.TrimSpace(errText), Retryable: true}
	case strings.Contains(text, "verification failed"), strings.Contains(text, "test failed"), strings.Contains(text, "assert"):
		return ExecutionFailureClassification{Kind: "verification_failed", Reason: strings.TrimSpace(errText), Retryable: true}
	case strings.Contains(text, "spec missing"), strings.Contains(text, "missing required"), strings.Contains(text, "insufficient"):
		return ExecutionFailureClassification{Kind: "spec_missing", Reason: strings.TrimSpace(errText), Retryable: true}
	default:
		reason := strings.TrimSpace(errText)
		if reason == "" {
			reason = strings.TrimSpace(output)
		}
		if reason == "" {
			reason = "execution failed"
		}
		return ExecutionFailureClassification{Kind: "unknown", Reason: reason, Retryable: false}
	}
}
