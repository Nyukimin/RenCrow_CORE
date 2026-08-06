package trade

import (
	"fmt"
	"regexp"
	"strings"
)

const PrivateContractVersion = "trade-private/v1"
const PolicyEvaluationContractVersion = "trade-policy-evaluation/v1"

type PolicyStatus struct {
	SchemaVersion          int             `json:"schema_version"`
	PolicyID               string          `json:"policy_id"`
	CoreContractRevision   string          `json:"core_contract_revision"`
	ModulePolicyRevision   string          `json:"module_policy_revision"`
	BinaryContractRevision string          `json:"binary_contract_revision"`
	ExecutionMode          string          `json:"execution_mode"`
	BrokerAdapter          string          `json:"broker_adapter"`
	KillSwitch             string          `json:"kill_switch"`
	Capabilities           map[string]bool `json:"capabilities"`
	BlockedCapabilities    []string        `json:"blocked_capabilities"`
}

type DependencyStatuses struct {
	Broker     string `json:"broker"`
	Ledger     string `json:"ledger"`
	MarketData string `json:"market_data"`
}

type PrivateStatus struct {
	ContractVersion string              `json:"contract_version"`
	ServiceStatus   string              `json:"service_status"`
	CorrelationID   string              `json:"correlation_id"`
	ExecutionMode   string              `json:"execution_mode"`
	LearningMode    string              `json:"learning_mode"`
	Ready           bool                `json:"ready"`
	KillSwitch      string              `json:"kill_switch"`
	Dependencies    DependencyStatuses  `json:"dependencies"`
	Policy          PolicyStatus        `json:"policy"`
	Portfolio       PortfolioProjection `json:"portfolio"`
}

type PortfolioProjection struct {
	Status   string             `json:"status"`
	Snapshot *PortfolioSnapshot `json:"snapshot,omitempty"`
}

type PortfolioSnapshot struct {
	SchemaVersion    int                 `json:"schema_version"`
	PortfolioID      string              `json:"portfolio_id"`
	Mode             string              `json:"mode"`
	Guaranteed       bool                `json:"guaranteed"`
	InitialCashJPY   int64               `json:"initial_cash_jpy"`
	CashJPY          int64               `json:"cash_jpy"`
	RealizedPnLJPY   int64               `json:"realized_pnl_jpy"`
	UnrealizedPnLJPY *int64              `json:"unrealized_pnl_jpy,omitempty"`
	NAVJPY           *int64              `json:"nav_jpy,omitempty"`
	ValuationStatus  string              `json:"valuation_status"`
	Positions        []PortfolioPosition `json:"positions"`
	EventCount       int64               `json:"event_count"`
	LatestEventHash  string              `json:"latest_event_hash"`
}

type PortfolioPosition struct {
	InstrumentID     string `json:"instrument_id"`
	Quantity         int64  `json:"quantity"`
	CostBasisJPY     int64  `json:"cost_basis_jpy"`
	MarkPriceJPY     *int64 `json:"mark_price_jpy,omitempty"`
	MarketValueJPY   *int64 `json:"market_value_jpy,omitempty"`
	UnrealizedPnLJPY *int64 `json:"unrealized_pnl_jpy,omitempty"`
}

type GlobalPolicyInput struct {
	ContractRevision string `json:"contract_revision"`
	BundleRevision   string `json:"bundle_revision"`
	ContentSHA256    string `json:"content_sha256"`
	Allowed          bool   `json:"allowed"`
}

type PolicyLayerInput struct {
	Revision string `json:"revision"`
	Allowed  bool   `json:"allowed"`
}

type PolicyEvaluationRequest struct {
	ContractVersion string            `json:"contract_version"`
	RequestID       string            `json:"request_id"`
	Capability      string            `json:"capability"`
	GlobalPolicy    GlobalPolicyInput `json:"global_policy"`
	Deployment      PolicyLayerInput  `json:"deployment"`
	RequestScope    PolicyLayerInput  `json:"request_scope"`
}

type PolicyDecision struct {
	Capability             string `json:"capability"`
	Status                 string `json:"status"`
	ReasonCode             string `json:"reason_code"`
	Reason                 string `json:"reason"`
	BinaryContractRevision string `json:"binary_contract_revision"`
	ModulePolicyRevision   string `json:"module_policy_revision"`
	PolicyID               string `json:"policy_id"`
	GlobalBundleRevision   string `json:"global_bundle_revision"`
	DeploymentRevision     string `json:"deployment_revision"`
	RequestScopeRevision   string `json:"request_scope_revision"`
}

type PrivatePolicyEvaluation struct {
	ContractVersion string         `json:"contract_version"`
	ServiceStatus   string         `json:"service_status"`
	CorrelationID   string         `json:"correlation_id"`
	ExecutionMode   string         `json:"execution_mode"`
	LearningMode    string         `json:"learning_mode"`
	Decision        PolicyDecision `json:"decision"`
}

var policySHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (status PrivateStatus) ValidateDisabledFoundation() error {
	if status.ContractVersion != PrivateContractVersion {
		return fmt.Errorf("unsupported TRADE contract version %q", status.ContractVersion)
	}
	if strings.TrimSpace(status.CorrelationID) == "" || strings.TrimSpace(status.ServiceStatus) == "" {
		return fmt.Errorf("TRADE status envelope is incomplete")
	}
	if status.ExecutionMode != "DISABLED" || status.Policy.ExecutionMode != "DISABLED" {
		return fmt.Errorf("TRADE execution mode is not authorized by the disabled foundation")
	}
	if status.KillSwitch != "ON" || status.Policy.KillSwitch != "ON" {
		return fmt.Errorf("TRADE kill switch is not ON")
	}
	if status.Policy.BrokerAdapter != "none" || status.Dependencies.Broker != "disabled" {
		return fmt.Errorf("TRADE broker boundary is not disabled")
	}
	if status.Policy.Capabilities["broker_network"] || status.Policy.Capabilities["paper_order"] || status.Policy.Capabilities["live_order"] {
		return fmt.Errorf("TRADE reports an unauthorized external trading capability")
	}
	if strings.TrimSpace(status.Policy.ModulePolicyRevision) == "" || strings.TrimSpace(status.Policy.BinaryContractRevision) == "" {
		return fmt.Errorf("TRADE policy revisions are incomplete")
	}
	switch status.Portfolio.Status {
	case "unconfigured", "not_initialized", "unavailable":
		if status.Portfolio.Snapshot != nil {
			return fmt.Errorf("TRADE portfolio snapshot is present for status %q", status.Portfolio.Status)
		}
	case "ready":
		if status.Portfolio.Snapshot == nil || status.Portfolio.Snapshot.Mode != "SIMULATION" || status.Portfolio.Snapshot.InitialCashJPY != 1_000_000 || status.Portfolio.Snapshot.Guaranteed {
			return fmt.Errorf("TRADE portfolio snapshot violates the 1000000 JPY simulation contract")
		}
	default:
		return fmt.Errorf("TRADE portfolio status is invalid")
	}
	return nil
}

func (request PolicyEvaluationRequest) Validate() error {
	if request.ContractVersion != PolicyEvaluationContractVersion {
		return fmt.Errorf("unsupported TRADE policy evaluation contract version %q", request.ContractVersion)
	}
	for name, value := range map[string]string{
		"request_id":                    request.RequestID,
		"capability":                    request.Capability,
		"global_policy.bundle_revision": request.GlobalPolicy.BundleRevision,
		"deployment.revision":           request.Deployment.Revision,
		"request_scope.revision":        request.RequestScope.Revision,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(request.RequestID) > 128 {
		return fmt.Errorf("request_id must not exceed 128 bytes")
	}
	if request.GlobalPolicy.ContractRevision != "global-policy/v1" {
		return fmt.Errorf("unsupported Global Policy contract revision %q", request.GlobalPolicy.ContractRevision)
	}
	if !policySHA256Pattern.MatchString(request.GlobalPolicy.ContentSHA256) {
		return fmt.Errorf("global_policy.content_sha256 must be lowercase SHA-256")
	}
	return nil
}

func (response PrivatePolicyEvaluation) Validate(request PolicyEvaluationRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if response.ContractVersion != PrivateContractVersion {
		return fmt.Errorf("unsupported TRADE contract version %q", response.ContractVersion)
	}
	if strings.TrimSpace(response.ServiceStatus) == "" || strings.TrimSpace(response.CorrelationID) == "" {
		return fmt.Errorf("TRADE policy evaluation envelope is incomplete")
	}
	if response.ExecutionMode != "DISABLED" {
		return fmt.Errorf("TRADE execution mode is not DISABLED")
	}
	decision := response.Decision
	if decision.Capability != request.Capability || decision.GlobalBundleRevision != request.GlobalPolicy.BundleRevision || decision.DeploymentRevision != request.Deployment.Revision || decision.RequestScopeRevision != request.RequestScope.Revision {
		return fmt.Errorf("TRADE policy evaluation revisions do not match the request")
	}
	for name, value := range map[string]string{
		"reason_code":              decision.ReasonCode,
		"reason":                   decision.Reason,
		"binary_contract_revision": decision.BinaryContractRevision,
		"module_policy_revision":   decision.ModulePolicyRevision,
		"policy_id":                decision.PolicyID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("TRADE policy decision %s is required", name)
		}
	}
	if decision.Status != "allowed" && decision.Status != "blocked" {
		return fmt.Errorf("TRADE policy decision status is invalid")
	}
	validReasonCode := map[string]bool{
		"UNKNOWN_CAPABILITY":        true,
		"BINARY_HARD_LIMIT_BLOCKED": true,
		"MODULE_POLICY_BLOCKED":     true,
		"GLOBAL_POLICY_BLOCKED":     true,
		"DEPLOYMENT_BLOCKED":        true,
		"REQUEST_SCOPE_BLOCKED":     true,
		"ALL_POLICY_LAYERS_ALLOWED": true,
	}
	if !validReasonCode[decision.ReasonCode] {
		return fmt.Errorf("TRADE policy decision reason code is invalid")
	}
	if (decision.Status == "allowed") != (decision.ReasonCode == "ALL_POLICY_LAYERS_ALLOWED") {
		return fmt.Errorf("TRADE policy decision status and reason code are inconsistent")
	}
	if decision.Status == "allowed" {
		switch decision.Capability {
		case "broker_network", "knowledge_auto_promotion", "live_order", "paper_order":
			return fmt.Errorf("TRADE binary-blocked capability was reported allowed")
		}
	} else {
		switch decision.Capability {
		case "broker_network", "knowledge_auto_promotion", "live_order", "paper_order":
			if decision.ReasonCode != "BINARY_HARD_LIMIT_BLOCKED" {
				return fmt.Errorf("TRADE binary hard-limit reason is missing")
			}
		}
	}
	return nil
}
