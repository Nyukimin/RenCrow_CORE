package trade

import "testing"

func validDisabledStatus() PrivateStatus {
	return PrivateStatus{
		ContractVersion: PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "trade-1",
		ExecutionMode:   "DISABLED",
		KillSwitch:      "ON",
		Dependencies:    DependencyStatuses{Broker: "disabled", Ledger: "unconfigured", MarketData: "unavailable"},
		Policy: PolicyStatus{
			ExecutionMode:          "DISABLED",
			KillSwitch:             "ON",
			BrokerAdapter:          "none",
			ModulePolicyRevision:   "sha256:module",
			BinaryContractRevision: "trade-binary/v1",
			Capabilities:           map[string]bool{"live_order": false},
		},
		Portfolio: PortfolioProjection{Status: "unconfigured"},
	}
}

func TestPrivateStatusValidateDisabledFoundation(t *testing.T) {
	status := validDisabledStatus()
	if err := status.ValidateDisabledFoundation(); err != nil {
		t.Fatal(err)
	}
	status.Policy.Capabilities["live_order"] = true
	if err := status.ValidateDisabledFoundation(); err == nil {
		t.Fatal("expected unauthorized capability rejection")
	}
}

func TestPrivatePolicyEvaluationValidate(t *testing.T) {
	request := PolicyEvaluationRequest{
		ContractVersion: PolicyEvaluationContractVersion,
		RequestID:       "core-policy-1",
		Capability:      "live_order",
		GlobalPolicy: GlobalPolicyInput{
			ContractRevision: "global-policy/v1",
			BundleRevision:   "2026-08-06.1",
			ContentSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Allowed:          true,
		},
		Deployment:   PolicyLayerInput{Revision: "deployment-1", Allowed: true},
		RequestScope: PolicyLayerInput{Revision: "scope-1", Allowed: true},
	}
	response := PrivatePolicyEvaluation{
		ContractVersion: PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "core-policy-1",
		ExecutionMode:   "DISABLED",
		LearningMode:    "OFFLINE_AVAILABLE",
		Decision: PolicyDecision{
			Capability:             "live_order",
			Status:                 "blocked",
			ReasonCode:             "BINARY_HARD_LIMIT_BLOCKED",
			Reason:                 "binary hard limit blocks capability",
			BinaryContractRevision: "trade-binary/v1",
			ModulePolicyRevision:   "sha256:module",
			PolicyID:               "trade-disabled",
			GlobalBundleRevision:   "2026-08-06.1",
			DeploymentRevision:     "deployment-1",
			RequestScopeRevision:   "scope-1",
		},
	}
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.Decision.Status = "allowed"
	if err := response.Validate(request); err == nil {
		t.Fatal("expected binary-blocked capability rejection")
	}
	response.Decision.Status = "blocked"
	response.Decision.ReasonCode = "GLOBAL_POLICY_BLOCKED"
	if err := response.Validate(request); err == nil {
		t.Fatal("expected missing binary hard-limit reason rejection")
	}
}
