package trade

import (
	"fmt"
	"strings"
)

const PrivateContractVersion = "trade-private/v1"

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
