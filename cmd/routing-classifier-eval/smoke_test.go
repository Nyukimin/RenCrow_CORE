package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

func TestSmokeHTTPContractWithFakeServer(t *testing.T) {
	// This tests only the HTTP adapter. It does not write smoke evidence and can
	// never mark the real SDK adapter contract VERIFIED.
	artifacts := smokeTestArtifacts()
	config := classifierlab.DefaultConfig()
	fixtures, err := loadSmokeFixtures(filepathSmokeFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	var classifyRequests, unauthorizedProbes int
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/readyz":
			if request.Method != http.MethodGet {
				http.Error(writer, "method", http.StatusMethodNotAllowed)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"status": "ready", "offline_configuration_verified": true,
				"network_isolation_status": "UNVERIFIED",
			})
		case "/v1/classify":
			if request.Method != http.MethodPost {
				http.Error(writer, "method", http.StatusMethodNotAllowed)
				return
			}
			if request.Header.Get("Authorization") != "Bearer smoke-test-secret" {
				unauthorizedProbes++
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"reason_code":"UNAUTHORIZED"}`))
				return
			}
			classifyRequests++
			var input classifierlab.CandidateInput
			if err := decodeStrictJSON(readTestBody(t, request), &input); err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"reason_code":"INVALID_REQUEST"}`))
				return
			}
			result := smokeTestResult(input, artifacts)
			if input.RequestID == "smoke-smoke-long" {
				result.Status = classifierlab.CandidateAbstain
				result.Route = nil
				result.Probabilities = nil
				result.ReasonCode = "INPUT_TOO_LONG"
				result.Input = classifierlab.CandidateInputMetadata{}
				result.Timing = classifierlab.CandidateTiming{}
			}
			_ = json.NewEncoder(writer).Encode(result)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer fake.Close()

	endpoint, err := validateSmokeEndpoint(fake.URL)
	if err != nil {
		t.Fatalf("fake endpoint is not loopback: %v", err)
	}
	client, err := newSmokeHTTPClient(endpoint, smokeHTTPTimeout)
	if err != nil {
		t.Fatal(err)
	}
	execution := executeSmokeHTTP(context.Background(), client, endpoint, "smoke-test-secret", artifacts, config, fixtures)
	if execution.FailureCode != "" {
		t.Fatalf("fake HTTP smoke execution failed: %s (%s)", execution.FailureCode, execution.FailureReason)
	}
	if !execution.Ready || !execution.AuthenticationGuardVerified || !execution.ModelInvoked || classifyRequests != len(fixtures) || unauthorizedProbes != 1 {
		t.Fatalf("fake HTTP counters/status unexpected: %+v classify=%d unauth=%d", execution, classifyRequests, unauthorizedProbes)
	}
	if !execution.OfflineConfigurationVerified || execution.NetworkIsolationStatus != "UNVERIFIED" {
		t.Fatalf("fake readiness fields were not recorded distinctly: %+v", execution)
	}
	if len(execution.Results) != len(fixtures) || countSmokeCases(execution.Results, "PASS") != len(fixtures) {
		t.Fatalf("fake HTTP case results = %+v", execution.Results)
	}
	checks := adapterChecksFromSmoke(execution.Checks)
	if !checks["offline_configuration_verified"] || checks["network_isolation_verified"] || checks["choice_budget_overflow"] ||
		allSmokeChecksPassed(checks) || smokeAdapterContractVerified(execution) {
		t.Fatalf("fake HTTP transport incorrectly proves network isolation, a complete adapter contract, or M07: %+v", checks)
	}
	if !checks["token_truncation_metadata"] {
		t.Fatalf("fake response did not exercise the explicit long-input abstention contract: %+v", checks)
	}
	for _, result := range execution.Results {
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if bytesContain(data, []byte("The final instruction")) || bytesContain(data, []byte("日本語の通常入力")) {
			t.Fatalf("smoke result leaked input message: %s", data)
		}
	}
}

func TestSmokeHTTPFailsClosedOnUnreadyOrExternalEndpoint(t *testing.T) {
	if _, err := validateSmokeEndpoint("http://example.com:18190"); err == nil {
		t.Fatal("external candidate endpoint was accepted")
	}
	artifacts := smokeTestArtifacts()
	config := classifierlab.DefaultConfig()
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":"not_ready","offline_configuration_verified":false,"network_isolation_status":"UNVERIFIED","reason_code":"MODEL_NOT_READY"}`))
	}))
	defer fake.Close()
	endpoint, err := validateSmokeEndpoint(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newSmokeHTTPClient(endpoint, smokeHTTPTimeout)
	if err != nil {
		t.Fatal(err)
	}
	execution := executeSmokeHTTP(context.Background(), client, endpoint, "secret", artifacts, config, nil)
	if execution.FailureCode != "SERVER_NOT_READY" || execution.Ready || execution.InferenceAttempted || len(execution.Results) != 0 {
		t.Fatalf("unready server was not rejected before inference: %+v", execution)
	}
}

func TestSmokeFailureCannotVerifyAdapterContract(t *testing.T) {
	execution := smokeExecution{
		FailureCode: "CLASSIFY_REQUEST_FAILED",
		Checks: map[string]bool{
			"pinned_sdk_version":              true,
			"router_predict_signature":        true,
			"multilingual_checkpoint_fixed":   true,
			"choice_output_and_probabilities": true,
			"token_truncation_metadata":       true,
			"offline_configuration_verified":  true,
			"network_isolation_verified":      false,
		},
	}
	if smokeAdapterContractVerified(execution) {
		t.Fatal("smoke without independent egress-isolation evidence was marked as a verified adapter contract")
	}
}

func TestSmokeReadyRequiresExactChildAttestationSchema(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "current child attestation",
			body: `{"status":"ready","offline_configuration_verified":true,"network_isolation_status":"UNVERIFIED"}`,
			want: true,
		},
		{
			name: "old startup field rejected",
			body: `{"status":"ready","offline_startup_verified":true}`,
		},
		{
			name: "missing isolation field rejected",
			body: `{"status":"ready","offline_configuration_verified":true}`,
		},
		{
			name: "unexpected isolation claim rejected",
			body: `{"status":"ready","offline_configuration_verified":true,"network_isolation_status":"VERIFIED"}`,
		},
		{
			name: "unknown field rejected",
			body: `{"status":"ready","offline_configuration_verified":true,"network_isolation_status":"UNVERIFIED","extra":true}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.body))
			}))
			defer fake.Close()
			endpoint, err := validateSmokeEndpoint(fake.URL)
			if err != nil {
				t.Fatal(err)
			}
			client, err := newSmokeHTTPClient(endpoint, smokeHTTPTimeout)
			if err != nil {
				t.Fatal(err)
			}
			ready, _, err := smokeGETReady(context.Background(), client, endpoint+"/readyz")
			if (err == nil) != test.want {
				t.Fatalf("smokeGETReady() ready=%+v err=%v, want success=%v", ready, err, test.want)
			}
		})
	}
}

func TestCandidateReplayBlocksBeforeHTTPWithoutIsolationEvidence(t *testing.T) {
	status := variantReplayStatus{Status: "BLOCKED", CaseCount: 3}
	if allowCandidateReplayWithoutNetworkIsolation(&status) {
		t.Fatal("candidate replay proceeded without network isolation evidence")
	}
	if status.ReasonCode != "OFFLINE_ISOLATION_UNVERIFIED" || status.NetworkIsolationStatus != "UNVERIFIED" ||
		status.OfflineConfigurationVerified || status.ModelInvoked || status.Calls != 0 || status.PredictionRows != 0 {
		t.Fatalf("candidate isolation gate status = %+v", status)
	}
}

func TestSmokeOutcomeNeverPromotesReadinessClaimToPass(t *testing.T) {
	for _, isolationStatus := range []string{"UNVERIFIED", "VERIFIED"} {
		t.Run(isolationStatus, func(t *testing.T) {
			status, code, _ := smokeOutcome(smokeExecution{
				OfflineConfigurationVerified: true,
				NetworkIsolationStatus:       isolationStatus,
				ChoiceBudgetOverflowObserved: true,
			})
			if status != "INCOMPLETE" || code != "OFFLINE_ISOLATION_UNVERIFIED" {
				t.Fatalf("smoke outcome trusted readiness self-claim %q: %s/%s", isolationStatus, status, code)
			}
		})
	}
}

func TestSmokeFixtureRequiresRealLongBoundaryPayload(t *testing.T) {
	fixtures, err := loadSmokeFixtures(filepathSmokeFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	var long smokeFixture
	for _, fixture := range fixtures {
		if fixture.Boundary == "long_input" {
			long = fixture
			break
		}
	}
	message, err := smokeFixtureMessage(long)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(message)) < 8500 || len(message) > smokeMaxExpandedBytes || !strings.Contains(message, long.Suffix) {
		t.Fatalf("long fixture boundary is not bounded and explicit: bytes=%d runes=%d", len(message), len([]rune(message)))
	}
}

func smokeTestArtifacts() classifierlab.PreparedArtifacts {
	routes := []string{"CHAT", "PLAN", "ANALYZE", "OPS", "RESEARCH", "CODE", "CODE1", "CODE2", "CODE3", "CODE4", "WILD"}
	model := classifierlab.PreparedModelManifest{
		Family: "laya-multilingual", SourceID: "convaiinnovations/laya-multilingual", Revision: strings.Repeat("a", 40), Device: "cpu", DType: "float32",
	}
	sdk := classifierlab.PreparedSDKManifest{Distribution: "laya", Version: "1.2.3", SourceURL: "https://github.com/NandhaKishorM/laya"}
	return classifierlab.PreparedArtifacts{
		Policy: classifierlab.RoutePolicy{Routes: routes}, PolicySHA256: strings.Repeat("b", 64),
		Manifest: classifierlab.PreparedManifest{Model: model, SDK: sdk},
	}
}

func smokeTestResult(input classifierlab.CandidateInput, artifacts classifierlab.PreparedArtifacts) classifierlab.CandidateResult {
	probabilities := make(map[classifierlab.RouteLabel]float64, len(artifacts.Policy.Routes))
	for _, route := range artifacts.Policy.Routes {
		probabilities[classifierlab.RouteLabel(route)] = 0
	}
	probabilities[classifierlab.RouteLabel("CHAT")] = 1
	route := classifierlab.RouteLabel("CHAT")
	return classifierlab.CandidateResult{
		SchemaVersion: classifierlab.SchemaVersion, RequestID: input.RequestID,
		Status: classifierlab.CandidateClassified, Route: &route, Probabilities: probabilities,
		SelectedProbability: 1, TopTwoMargin: 1,
		ProbabilityStatus: classifierlab.ProbabilityStatusUnvalidated,
		Model: classifierlab.CandidateModel{
			Family: artifacts.Manifest.Model.Family, SourceID: artifacts.Manifest.Model.SourceID,
			Revision: artifacts.Manifest.Model.Revision, SDKVersion: artifacts.Manifest.SDK.Version,
			Device: artifacts.Manifest.Model.Device, DType: artifacts.Manifest.Model.DType,
		},
		PolicySHA256: input.PolicySHA256,
		Input:        classifierlab.CandidateInputMetadata{EffectiveSequenceTokens: 12},
		Timing:       classifierlab.CandidateTiming{Prepare: 0.1, Inference: 2, Postprocess: 0.1, ServerTotal: 2.2},
		ReasonCode:   "OK",
	}
}

func filepathSmokeFixtures(t *testing.T) string {
	t.Helper()
	return filepath.Join(findCoreRoot(t), "testdata", "routing_classifier", "smoke.jsonl")
}

func readTestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	data, err := io.ReadAll(request.Body)
	if err != nil {
		t.Errorf("read fake server request body: %v", err)
		return nil
	}
	return data
}

func bytesContain(value, fragment []byte) bool {
	return strings.Contains(string(value), string(fragment))
}
