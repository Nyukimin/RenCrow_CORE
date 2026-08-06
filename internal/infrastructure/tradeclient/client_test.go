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

func TestClientEvaluateUsesAuthenticatedPurePolicyRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/policy/evaluate" || request.Method != http.MethodPost {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+testToken || request.Header.Get("X-Correlation-ID") != "core-policy-1" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		var input moduletrade.PolicyEvaluationRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(writer).Encode(moduletrade.PrivatePolicyEvaluation{
			ContractVersion: moduletrade.PrivateContractVersion,
			ServiceStatus:   "ready",
			CorrelationID:   "core-policy-1",
			ExecutionMode:   "DISABLED",
			LearningMode:    "OFFLINE_AVAILABLE",
			Decision: moduletrade.PolicyDecision{
				Capability:             input.Capability,
				Status:                 "blocked",
				ReasonCode:             "BINARY_HARD_LIMIT_BLOCKED",
				Reason:                 "binary hard limit blocks capability",
				BinaryContractRevision: "trade-binary/v1",
				ModulePolicyRevision:   "sha256:module",
				PolicyID:               "trade-disabled",
				GlobalBundleRevision:   input.GlobalPolicy.BundleRevision,
				DeploymentRevision:     input.Deployment.Revision,
				RequestScopeRevision:   input.RequestScope.Revision,
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, writeToken(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := moduletrade.PolicyEvaluationRequest{
		ContractVersion: moduletrade.PolicyEvaluationContractVersion,
		RequestID:       "core-policy-1",
		Capability:      "live_order",
		GlobalPolicy: moduletrade.GlobalPolicyInput{
			ContractRevision: "global-policy/v1",
			BundleRevision:   "2026-08-06.1",
			ContentSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Allowed:          true,
		},
		Deployment:   moduletrade.PolicyLayerInput{Revision: "deployment-1", Allowed: true},
		RequestScope: moduletrade.PolicyLayerInput{Revision: "scope-1", Allowed: true},
	}
	response, err := client.Evaluate(context.Background(), "core-policy-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Decision.ReasonCode != "BINARY_HARD_LIMIT_BLOCKED" {
		t.Fatalf("response=%+v", response)
	}
}
