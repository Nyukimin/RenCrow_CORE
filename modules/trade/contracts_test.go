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
