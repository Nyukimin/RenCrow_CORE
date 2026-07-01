package sandbox

import (
	"fmt"
	"strings"
	"time"
)

const (
	ApprovalPending  = "pending"
	ApprovalGranted  = "granted"
	ApprovalRejected = "rejected"

	GateStatusApproved      = "approve"
	GateStatusRejected      = "reject"
	GateStatusNeedsReview   = "needs_review"
	GateStatusNeedsMoreTest = "needs_more_tests"
	GateStatusApplied       = "promotion_applied"
	GateStatusRolledBack    = "rollback_executed"
)

type PromotionRequest struct {
	PromotionID               string    `json:"promotion_id"`
	SandboxID                 string    `json:"sandbox_id"`
	WorkstreamID              string    `json:"workstream_id,omitempty"`
	GoalID                    string    `json:"goal_id,omitempty"`
	RequestedBy               string    `json:"requested_by,omitempty"`
	TargetPath                string    `json:"target_path"`
	DiffPath                  string    `json:"diff_path"`
	TestResultPath            string    `json:"test_result_path"`
	RiskLevel                 string    `json:"risk_level,omitempty"`
	Reason                    string    `json:"reason"`
	RollbackPlanPath          string    `json:"rollback_plan_path"`
	PostApplyVerificationPath string    `json:"post_apply_verification_path,omitempty"`
	HumanApprovalStatus       string    `json:"human_approval_status"`
	CreatedAt                 time.Time `json:"created_at"`
}

type PromotionGateDecision struct {
	Status              string   `json:"status"`
	Reason              string   `json:"reason"`
	MissingRequirements []string `json:"missing_requirements,omitempty"`
}

type PromotionGateLog struct {
	EventID               string    `json:"event_id"`
	PromotionID           string    `json:"promotion_id"`
	GateStatus            string    `json:"gate_status"`
	Reason                string    `json:"reason"`
	HumanApprovalStatus   string    `json:"human_approval_status"`
	PostApplyVerification string    `json:"post_apply_verification,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

type PromotionApplyRequest struct {
	Promotion                    PromotionRequest `json:"promotion"`
	AppliedBy                    string           `json:"applied_by,omitempty"`
	ApplyTarget                  string           `json:"apply_target,omitempty"`
	PostApplyVerificationPath    string           `json:"post_apply_verification_path"`
	PostApplyVerificationCommand string           `json:"post_apply_verification_command,omitempty"`
	HumanApproved                bool             `json:"human_approved"`
}

type PromotionApplyDecision struct {
	Status              string   `json:"status"`
	Reason              string   `json:"reason"`
	MissingRequirements []string `json:"missing_requirements,omitempty"`
}

func EvaluatePromotionRequest(req PromotionRequest) PromotionGateDecision {
	missing := missingPromotionRequirements(req)
	if len(missing) > 0 {
		status := GateStatusNeedsReview
		if contains(missing, "test_result_path") {
			status = GateStatusNeedsMoreTest
		}
		return PromotionGateDecision{
			Status:              status,
			Reason:              fmt.Sprintf("promotion requirements missing: %s", strings.Join(missing, ", ")),
			MissingRequirements: missing,
		}
	}
	if req.HumanApprovalStatus == ApprovalRejected {
		return PromotionGateDecision{
			Status: GateStatusRejected,
			Reason: "human approval rejected",
		}
	}
	return PromotionGateDecision{
		Status: GateStatusApproved,
		Reason: "promotion requirements satisfied",
	}
}

func EvaluatePromotionApplyRequest(req PromotionApplyRequest) PromotionApplyDecision {
	gate := EvaluatePromotionRequest(req.Promotion)
	if gate.Status != GateStatusApproved {
		return PromotionApplyDecision{
			Status:              gate.Status,
			Reason:              gate.Reason,
			MissingRequirements: gate.MissingRequirements,
		}
	}
	var missing []string
	if !req.HumanApproved {
		missing = append(missing, "human_approved")
	}
	if strings.TrimSpace(req.PostApplyVerificationPath) == "" {
		missing = append(missing, "post_apply_verification_path")
	}
	if len(missing) > 0 {
		return PromotionApplyDecision{
			Status:              GateStatusNeedsReview,
			Reason:              fmt.Sprintf("promotion apply requirements missing: %s", strings.Join(missing, ", ")),
			MissingRequirements: missing,
		}
	}
	return PromotionApplyDecision{
		Status: GateStatusApplied,
		Reason: "promotion apply checkpoint recorded",
	}
}

func EvaluatePromotionRollbackRequest(req PromotionApplyRequest) PromotionApplyDecision {
	decision := EvaluatePromotionApplyRequest(req)
	if decision.Status != GateStatusApplied {
		return decision
	}
	return PromotionApplyDecision{
		Status: GateStatusRolledBack,
		Reason: "promotion rollback checkpoint recorded",
	}
}

func missingPromotionRequirements(req PromotionRequest) []string {
	var missing []string
	if strings.TrimSpace(req.PromotionID) == "" {
		missing = append(missing, "promotion_id")
	}
	if strings.TrimSpace(req.SandboxID) == "" {
		missing = append(missing, "sandbox_id")
	}
	if strings.TrimSpace(req.TargetPath) == "" {
		missing = append(missing, "target_path")
	}
	if strings.TrimSpace(req.DiffPath) == "" {
		missing = append(missing, "diff_path")
	}
	if strings.TrimSpace(req.Reason) == "" {
		missing = append(missing, "reason")
	}
	if strings.TrimSpace(req.TestResultPath) == "" {
		missing = append(missing, "test_result_path")
	}
	if strings.TrimSpace(req.RollbackPlanPath) == "" {
		missing = append(missing, "rollback_plan_path")
	}
	if strings.TrimSpace(req.HumanApprovalStatus) != ApprovalGranted && strings.TrimSpace(req.HumanApprovalStatus) != ApprovalRejected {
		missing = append(missing, "human_approval")
	}
	return missing
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
