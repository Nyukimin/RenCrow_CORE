package worker

import (
	"fmt"
	"strings"
)

const (
	CapabilityGenericExecution = "generic_execution"
	CapabilityCodeChange       = "code_change"

	FailureProposalInvalid     = "proposal_invalid"
	FailureCommandMissing      = "command_missing"
	FailureProviderUnavailable = "provider_unavailable"
	FailureApprovalRequired    = "approval_required"
	FailureApply               = "apply"
)

func IsAutonomousRoute(route string) bool {
	switch normalizeRuntimeRoute(route) {
	case "OPS", "CODE", "CODE1", "CODE2", "CODE3", "CODE4", "PLAN", "ANALYZE", "RESEARCH", "WILD":
		return true
	default:
		return false
	}
}

func CapabilityForRoute(route string) string {
	if isCodeRouteName(route) {
		return CapabilityCodeChange
	}
	return CapabilityGenericExecution
}

func RouteExecutionSteps(route string, ok bool) []string {
	items := []string{"routing.decision"}
	switch normalizeRuntimeRoute(route) {
	case "OPS":
		items = append(items, "shiro.execute")
	case "CODE", "CODE1", "CODE2", "CODE3", "CODE4":
		items = append(items, "shiro.delegate", "coder.execute", "shiro.verify")
	case "PLAN":
		items = append(items, "mio.plan")
	case "ANALYZE":
		items = append(items, "heavy.analyze")
	case "RESEARCH":
		items = append(items, "mio.research")
	case "WILD":
		items = append(items, "wild.generate")
	}
	if ok {
		items = append(items, "done")
	} else {
		items = append(items, "error")
	}
	return items
}

func ClassifyExecutorFailure(err error) string {
	if err == nil {
		return ""
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "approval required"):
		return FailureApprovalRequired
	case strings.Contains(lower, "proposal"):
		return FailureProposalInvalid
	case strings.Contains(lower, "not found"), strings.Contains(lower, "exit status 127"):
		return FailureCommandMissing
	case strings.Contains(lower, "provider"), strings.Contains(lower, "model"), strings.Contains(lower, "ollama"):
		return FailureProviderUnavailable
	default:
		return FailureApply
	}
}

func BuildExecutorRetryMessage(userMessage string, route string, failureKind, failureReason string, attempt int) string {
	return fmt.Sprintf(`%s

## Executor Retry Context
- retry_attempt: %d
- route: %s
- failure_kind: %s
- failure_reason: %s

## Requirements
- Keep the response executable and directly verifiable
- Include the missing repair steps in the next result
- Do not include manual-only lifecycle commands in executable output; report them as approval-required steps
`, userMessage, attempt, route, fallbackString(failureKind, "unknown"), fallbackString(failureReason, "execution failed"))
}

func normalizeRuntimeRoute(route string) string {
	return strings.ToUpper(strings.TrimSpace(route))
}

func isCodeRouteName(route string) bool {
	switch normalizeRuntimeRoute(route) {
	case "CODE", "CODE1", "CODE2", "CODE3", "CODE4":
		return true
	default:
		return false
	}
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
