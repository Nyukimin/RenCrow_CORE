package aiworkflow

import "strings"

const (
	HeavyWorkerStatusNotRequired = "not_required"
	HeavyWorkerStatusRequested   = "requested"
	HeavyWorkerStatusBlocked     = "blocked"
)

type HeavyWorkerPolicy struct {
	Enabled                 bool `json:"enabled"`
	RequireReason           bool `json:"require_reason"`
	FileCountThreshold      int  `json:"file_count_threshold"`
	SpecCountThreshold      int  `json:"spec_count_threshold"`
	FailedAttemptsThreshold int  `json:"failed_attempts_threshold"`
}

type HeavyWorkerRequest struct {
	EventID                     string `json:"event_id"`
	Agent                       string `json:"agent"`
	TargetFileCount             int    `json:"target_file_count,omitempty"`
	RelatedSpecCount            int    `json:"related_spec_count,omitempty"`
	CrossesArchitectureBoundary bool   `json:"crosses_architecture_boundary,omitempty"`
	HighUncertainty             bool   `json:"high_uncertainty,omitempty"`
	FailedAttempts              int    `json:"failed_attempts,omitempty"`
	UserRequestedDeepDive       bool   `json:"user_requested_deep_dive,omitempty"`
	Reason                      string `json:"reason,omitempty"`
}

type HeavyWorkerDecision struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

func EvaluateHeavyWorker(request HeavyWorkerRequest, policy HeavyWorkerPolicy) HeavyWorkerDecision {
	policy = normalizeHeavyWorkerPolicy(policy)
	if !policy.Enabled {
		return HeavyWorkerDecision{Status: HeavyWorkerStatusNotRequired, Reasons: []string{"heavy worker disabled"}}
	}
	var reasons []string
	if request.TargetFileCount > policy.FileCountThreshold {
		reasons = append(reasons, "target file count exceeds threshold")
	}
	if request.RelatedSpecCount > policy.SpecCountThreshold {
		reasons = append(reasons, "related spec count exceeds threshold")
	}
	if request.CrossesArchitectureBoundary {
		reasons = append(reasons, "architecture boundary is crossed")
	}
	if request.HighUncertainty {
		reasons = append(reasons, "normal agent reported high uncertainty")
	}
	if request.FailedAttempts >= policy.FailedAttemptsThreshold {
		reasons = append(reasons, "failed attempts reached threshold")
	}
	if request.UserRequestedDeepDive {
		reasons = append(reasons, "user requested deep dive")
	}
	if len(reasons) == 0 {
		return HeavyWorkerDecision{Status: HeavyWorkerStatusNotRequired, Reasons: []string{"heavy worker conditions were not met"}}
	}
	if policy.RequireReason && strings.TrimSpace(request.Reason) == "" {
		return HeavyWorkerDecision{Status: HeavyWorkerStatusBlocked, Reasons: append(reasons, "reason is required")}
	}
	return HeavyWorkerDecision{Status: HeavyWorkerStatusRequested, Reasons: reasons}
}

func normalizeHeavyWorkerPolicy(policy HeavyWorkerPolicy) HeavyWorkerPolicy {
	if policy.FileCountThreshold <= 0 {
		policy.FileCountThreshold = 20
	}
	if policy.SpecCountThreshold <= 0 {
		policy.SpecCountThreshold = 1
	}
	if policy.FailedAttemptsThreshold <= 0 {
		policy.FailedAttemptsThreshold = 2
	}
	return policy
}
