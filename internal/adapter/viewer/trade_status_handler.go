package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type TradeStatusReader interface {
	Status(ctx context.Context, correlationID string) (moduletrade.PrivateStatus, error)
}

type TradeStatusOptions struct {
	Enabled bool
	Reader  TradeStatusReader
}

type tradeStatusProjection struct {
	BridgeStatus         string                          `json:"bridge_status"`
	ContractVersion      string                          `json:"contract_version,omitempty"`
	ServiceStatus        string                          `json:"service_status,omitempty"`
	CorrelationID        string                          `json:"correlation_id,omitempty"`
	ExecutionMode        string                          `json:"execution_mode,omitempty"`
	LearningMode         string                          `json:"learning_mode,omitempty"`
	Ready                bool                            `json:"ready"`
	KillSwitch           string                          `json:"kill_switch,omitempty"`
	Dependencies         moduletrade.DependencyStatuses  `json:"dependencies,omitempty"`
	PolicyID             string                          `json:"policy_id,omitempty"`
	ModulePolicyRevision string                          `json:"module_policy_revision,omitempty"`
	BinaryPolicyRevision string                          `json:"binary_contract_revision,omitempty"`
	Capabilities         map[string]bool                 `json:"capabilities,omitempty"`
	Portfolio            moduletrade.PortfolioProjection `json:"portfolio"`
	ReasonCode           string                          `json:"reason_code,omitempty"`
}

func HandleTradeStatus(options TradeStatusOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		if request.Method != http.MethodGet {
			http.Error(writer, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if !options.Enabled {
			writeTradeStatus(writer, http.StatusOK, tradeStatusProjection{
				BridgeStatus: "disabled",
				Ready:        false,
				Portfolio:    moduletrade.PortfolioProjection{Status: "unconfigured"},
				ReasonCode:   "TRADE_NOT_CONFIGURED",
			})
			return
		}
		if options.Reader == nil {
			writeTradeStatus(writer, http.StatusServiceUnavailable, tradeStatusProjection{
				BridgeStatus: "unavailable",
				Ready:        false,
				Portfolio:    moduletrade.PortfolioProjection{Status: "unavailable"},
				ReasonCode:   "TRADE_CLIENT_UNAVAILABLE",
			})
			return
		}
		status, err := options.Reader.Status(request.Context(), strings.TrimSpace(request.Header.Get("X-Request-ID")))
		if err != nil {
			writeTradeStatus(writer, http.StatusServiceUnavailable, tradeStatusProjection{
				BridgeStatus: "unavailable",
				Ready:        false,
				Portfolio:    moduletrade.PortfolioProjection{Status: "unavailable"},
				ReasonCode:   "TRADE_STATUS_UNAVAILABLE",
			})
			return
		}
		writeTradeStatus(writer, http.StatusOK, tradeStatusProjection{
			BridgeStatus:         "connected",
			ContractVersion:      status.ContractVersion,
			ServiceStatus:        status.ServiceStatus,
			CorrelationID:        status.CorrelationID,
			ExecutionMode:        status.ExecutionMode,
			LearningMode:         status.LearningMode,
			Ready:                status.Ready,
			KillSwitch:           status.KillSwitch,
			Dependencies:         status.Dependencies,
			PolicyID:             status.Policy.PolicyID,
			ModulePolicyRevision: status.Policy.ModulePolicyRevision,
			BinaryPolicyRevision: status.Policy.BinaryContractRevision,
			Capabilities:         status.Policy.Capabilities,
			Portfolio:            status.Portfolio,
		})
	}
}

func writeTradeStatus(writer http.ResponseWriter, status int, projection tradeStatusProjection) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(projection)
}
