package orchestrator

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

func nextCoderRetryRequest(userMessage string, proposal *domaintransport.ProposalPayload, shiroResult domaintransport.Message, attempt int) (string, bool) {
	if shiroResult.Result == nil || shiroResult.Result.Success || !shiroResult.Result.Retryable {
		return "", false
	}
	if attempt >= distributedCoderRetryMax {
		return "", false
	}
	return buildCoderRetryInstruction(userMessage, proposal, shiroResult.Result.FailureKind, shiroResult.Result.FailureReason, attempt+1), true
}

func classifyDistributedExecutionError(err error) (string, string, bool) {
	if err == nil {
		return "", "", false
	}
	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "rate_limit") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "status=429") || strings.Contains(lower, " 429"):
		return "provider_rate_limited", text, true
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		return "timeout", text, true
	case strings.Contains(lower, agent.ProposalFailureEmpty),
		strings.Contains(lower, agent.ProposalFailureMissingPlan),
		strings.Contains(lower, agent.ProposalFailureMissingPatch),
		strings.Contains(lower, agent.ProposalFailureInvalidPatch):
		return proposalFailureKindFromText(lower), text, true
	case strings.Contains(lower, agent.ProposalFailureDisallowedCommand):
		return agent.ProposalFailureDisallowedCommand, text, false
	case strings.Contains(lower, "patch parse error"):
		return "patch_parse_failed", text, true
	case strings.Contains(lower, "command not found"), strings.Contains(lower, "exit status 127"), strings.Contains(lower, "not found"):
		return "missing_command", text, true
	case strings.Contains(lower, "security error"), strings.Contains(lower, "protected file"):
		return "unsafe_operation", text, false
	default:
		return "unknown", text, false
	}
}

func proposalFailureKindFromText(lower string) string {
	switch {
	case strings.Contains(lower, agent.ProposalFailureMissingPlan):
		return agent.ProposalFailureMissingPlan
	case strings.Contains(lower, agent.ProposalFailureMissingPatch):
		return agent.ProposalFailureMissingPatch
	case strings.Contains(lower, agent.ProposalFailureInvalidPatch):
		return agent.ProposalFailureInvalidPatch
	default:
		return agent.ProposalFailureEmpty
	}
}

func buildCoderRetryInstruction(userMessage string, proposal *domaintransport.ProposalPayload, failureKind, failureReason string, retry int) string {
	var prevPlan, prevPatch string
	if proposal != nil {
		prevPlan = strings.TrimSpace(proposal.Plan)
		prevPatch = strings.TrimSpace(proposal.Patch)
	}
	return fmt.Sprintf(`%s

## Retry Context
- retry_attempt: %d
- failure_kind: %s
- failure_reason: %s

## Worker Requirements
- Return a Worker-executable patch only
- Keep the patch directly parseable and runnable
- Include the environment repair or verification steps inside Patch
- Do not use bare pip; use python3 -m pip or python -m pip if Python package installation is truly required
- Prefer concrete file edits and deterministic non-interactive commands

## Previous Proposal Plan
%s

## Previous Proposal Patch
%s
`, userMessage, retry, fallbackString(failureKind, "unknown"), fallbackString(failureReason, "execution failed"), fallbackString(prevPlan, "(none)"), truncate(fallbackString(prevPatch, "(none)"), 1600))
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
