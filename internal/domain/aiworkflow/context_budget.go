package aiworkflow

import "fmt"

const (
	ContextBudgetStatusOK   = "ok"
	ContextBudgetStatusWarn = "warn"
	ContextBudgetStatusStop = "stop"
)

type ContextBudgetPolicy struct {
	MaxContextTokens int     `json:"max_context_tokens"`
	WarnAtRatio      float64 `json:"warn_at_ratio"`
	StopAtRatio      float64 `json:"stop_at_ratio"`
}

type ContextBudgetDecision struct {
	Status           string  `json:"status"`
	Reason           string  `json:"reason"`
	ContextTokens    int     `json:"context_tokens"`
	MaxContextTokens int     `json:"max_context_tokens"`
	UsageRatio       float64 `json:"usage_ratio"`
}

func EvaluateContextBudget(usage ContextUsage, policy ContextBudgetPolicy) (ContextBudgetDecision, error) {
	if err := ValidateContextUsage(usage); err != nil {
		return ContextBudgetDecision{}, err
	}
	policy = normalizeContextBudgetPolicy(policy)
	decision := ContextBudgetDecision{
		Status:           ContextBudgetStatusOK,
		Reason:           "context budget disabled",
		ContextTokens:    usage.ContextTokens,
		MaxContextTokens: policy.MaxContextTokens,
	}
	if policy.MaxContextTokens <= 0 {
		return decision, nil
	}
	ratio := float64(usage.ContextTokens) / float64(policy.MaxContextTokens)
	decision.UsageRatio = ratio
	switch {
	case ratio >= policy.StopAtRatio:
		decision.Status = ContextBudgetStatusStop
		decision.Reason = fmt.Sprintf("context budget stop threshold reached: %.2f >= %.2f", ratio, policy.StopAtRatio)
	case ratio >= policy.WarnAtRatio:
		decision.Status = ContextBudgetStatusWarn
		decision.Reason = fmt.Sprintf("context budget warning threshold reached: %.2f >= %.2f", ratio, policy.WarnAtRatio)
	default:
		decision.Status = ContextBudgetStatusOK
		decision.Reason = fmt.Sprintf("context budget within limit: %.2f < %.2f", ratio, policy.WarnAtRatio)
	}
	return decision, nil
}

func normalizeContextBudgetPolicy(policy ContextBudgetPolicy) ContextBudgetPolicy {
	if policy.WarnAtRatio <= 0 {
		policy.WarnAtRatio = 0.8
	}
	if policy.StopAtRatio <= 0 {
		policy.StopAtRatio = 0.95
	}
	if policy.StopAtRatio < policy.WarnAtRatio {
		policy.StopAtRatio = policy.WarnAtRatio
	}
	return policy
}
