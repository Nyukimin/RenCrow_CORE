package viewer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type tradeStatusReaderStub struct {
	status moduletrade.PrivateStatus
	err    error
}

func (stub tradeStatusReaderStub) Status(_ context.Context, _ string) (moduletrade.PrivateStatus, error) {
	return stub.status, stub.err
}

func TestHandleTradeStatusDisabled(t *testing.T) {
	response := httptest.NewRecorder()
	HandleTradeStatus(TradeStatusOptions{})(response, httptest.NewRequest(http.MethodGet, "/viewer/trade/status", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"bridge_status":"disabled"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandleTradeStatusConnectedProjection(t *testing.T) {
	status := moduletrade.PrivateStatus{
		ContractVersion: moduletrade.PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "trade-1",
		ExecutionMode:   "DISABLED",
		LearningMode:    "OFFLINE_AVAILABLE",
		Ready:           true,
		KillSwitch:      "ON",
		Dependencies:    moduletrade.DependencyStatuses{Broker: "disabled", Ledger: "unconfigured", MarketData: "unavailable"},
		Policy:          moduletrade.PolicyStatus{PolicyID: "disabled", ModulePolicyRevision: "sha256:module", BinaryContractRevision: "trade-binary/v1", Capabilities: map[string]bool{"live_order": false}},
		Portfolio:       moduletrade.PortfolioProjection{Status: "unconfigured"},
	}
	response := httptest.NewRecorder()
	HandleTradeStatus(TradeStatusOptions{Enabled: true, Reader: tradeStatusReaderStub{status: status}})(response, httptest.NewRequest(http.MethodGet, "/viewer/trade/status", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"bridge_status":"connected"`) || !strings.Contains(response.Body.String(), `"broker":"disabled"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandleTradeStatusMasksUpstreamFailure(t *testing.T) {
	response := httptest.NewRecorder()
	reader := tradeStatusReaderStub{err: errors.New("secret token leaked")}
	HandleTradeStatus(TradeStatusOptions{Enabled: true, Reader: reader})(response, httptest.NewRequest(http.MethodGet, "/viewer/trade/status", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "secret token") || !strings.Contains(response.Body.String(), "TRADE_STATUS_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
