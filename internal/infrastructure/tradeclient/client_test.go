package tradeclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const testToken = "0123456789abcdef0123456789abcdef"

func writeToken(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trade.token")
	if err := os.WriteFile(path, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func disabledStatus() moduletrade.PrivateStatus {
	return moduletrade.PrivateStatus{
		ContractVersion: moduletrade.PrivateContractVersion,
		ServiceStatus:   "ready",
		CorrelationID:   "core-1",
		ExecutionMode:   "DISABLED",
		LearningMode:    "OFFLINE_AVAILABLE",
		Ready:           true,
		KillSwitch:      "ON",
		Dependencies:    moduletrade.DependencyStatuses{Broker: "disabled", Ledger: "unconfigured", MarketData: "unavailable"},
		Policy: moduletrade.PolicyStatus{
			ExecutionMode:          "DISABLED",
			KillSwitch:             "ON",
			BrokerAdapter:          "none",
			ModulePolicyRevision:   "sha256:module",
			BinaryContractRevision: "trade-binary/v1",
			Capabilities:           map[string]bool{"broker_network": false, "paper_order": false, "live_order": false},
		},
		Portfolio: moduletrade.PortfolioProjection{Status: "unconfigured"},
	}
}

func TestClientStatusUsesAuthenticatedPrivateRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/status" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+testToken || request.Header.Get("X-Correlation-ID") != "core-1" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(disabledStatus())
	}))
	defer server.Close()

	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background(), "core-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Policy.ModulePolicyRevision != "sha256:module" || status.Dependencies.Broker != "disabled" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestClientStatusRejectsExpandedTradingCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		status := disabledStatus()
		status.Policy.Capabilities["live_order"] = true
		_ = json.NewEncoder(writer).Encode(status)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background(), "core-1"); err == nil {
		t.Fatal("expected unauthorized capability rejection")
	}
}

func TestNewClientRejectsLooseTokenPermissions(t *testing.T) {
	path := writeToken(t)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient("http://127.0.0.1:8766", path, time.Second); err == nil {
		t.Fatal("expected token permissions rejection")
	}
}
