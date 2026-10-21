package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

const (
	smokeHTTPTimeout      = 3 * time.Second
	smokeExpectedTimeout  = 3000
	smokeMaxResponseBytes = classifierlab.MaxHTTPBodyBytes
	smokeMaxExpandedBytes = classifierlab.MaxHTTPBodyBytes - 1024
)

var (
	smokeFixtureIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	smokeEnvNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	smokeReasonPattern    = regexp.MustCompile(`^[A-Z0-9_]{1,64}$`)
)

type smokeStatus struct {
	SchemaVersion                string                        `json:"schema_version"`
	Status                       string                        `json:"status"`
	ReasonCode                   string                        `json:"reason_code"`
	Reasons                      []string                      `json:"reasons"`
	ManifestID                   string                        `json:"manifest_id,omitempty"`
	ExperimentID                 string                        `json:"experiment_id"`
	DatasetSHA256                string                        `json:"dataset_sha256"`
	Endpoint                     string                        `json:"endpoint,omitempty"`
	PolicyStatus                 string                        `json:"policy_status"`
	PolicySHA256                 string                        `json:"policy_sha256,omitempty"`
	Model                        *classifierlab.CandidateModel `json:"model,omitempty"`
	ModelFilesSHA256             string                        `json:"model_files_sha256,omitempty"`
	SDKLockSHA256                string                        `json:"sdk_lock_sha256,omitempty"`
	ServerReady                  bool                          `json:"server_ready"`
	AuthenticationGuardVerified  bool                          `json:"authentication_guard_verified"`
	OfflineRequired              bool                          `json:"offline_required"`
	OfflineConfigurationVerified bool                          `json:"offline_configuration_verified"`
	NetworkIsolationStatus       string                        `json:"network_isolation_status"`
	ChoiceBudgetOverflowObserved bool                          `json:"choice_budget_overflow_observed"`
	ChoiceBudgetBoundaryStatus   string                        `json:"choice_budget_boundary_status"`
	ExternalNetworkAllowed       *bool                         `json:"external_network_allowed,omitempty"`
	OnlineAcquisitionAttempted   bool                          `json:"online_acquisition_attempted"`
	InferenceAttempted           bool                          `json:"inference_attempted"`
	ModelInvoked                 bool                          `json:"model_invoked"`
	ClassifyRequestsAttempted    int                           `json:"classify_requests_attempted"`
	CaseCount                    int                           `json:"case_count"`
	PassedCases                  int                           `json:"passed_cases"`
	FailedCases                  int                           `json:"failed_cases"`
	AdapterContractStatus        string                        `json:"adapter_contract_status"`
	Checks                       map[string]bool               `json:"checks"`
}

type smokeCaseResult struct {
	CaseID     string                         `json:"case_id"`
	Language   string                         `json:"language"`
	Boundary   string                         `json:"boundary"`
	Status     string                         `json:"status"`
	ReasonCode string                         `json:"reason_code,omitempty"`
	HTTPStatus int                            `json:"http_status,omitempty"`
	HTTPWallMS float64                        `json:"http_wall_ms,omitempty"`
	Result     *classifierlab.CandidateResult `json:"result,omitempty"`
}

type smokeExecution struct {
	Ready                        bool
	AuthenticationGuardVerified  bool
	OfflineConfigurationVerified bool
	NetworkIsolationStatus       string
	InferenceAttempted           bool
	ModelInvoked                 bool
	ClassifyRequestsAttempted    int
	ChoiceBudgetOverflowObserved bool
	Results                      []smokeCaseResult
	Checks                       map[string]bool
	FailureCode                  string
	FailureReason                string
}

type smokeReadyResponse struct {
	Status                       string `json:"status"`
	OfflineConfigurationVerified bool   `json:"offline_configuration_verified"`
	NetworkIsolationStatus       string `json:"network_isolation_status"`
	ReasonCode                   string `json:"reason_code,omitempty"`
}

type smokeHTTPErrorResponse struct {
	ReasonCode string `json:"reason_code"`
}

func runSmoke(configPath, fixturesPath, outPath, repoRoot string) error {
	config, err := loadExperimentConfig(configPath)
	if err != nil {
		return err
	}
	fixtures, err := loadSmokeFixtures(fixturesPath)
	if err != nil {
		return err
	}
	if err := validateSmokeCoverage(fixtures); err != nil {
		return commandError{Code: "SMOKE_FIXTURES_INCOMPLETE", Err: err}
	}
	inspection, err := inspectRepository(repoRoot)
	if err != nil {
		return err
	}
	fixtureSHA, err := fileSHA256(fixturesPath)
	if err != nil {
		return err
	}
	directory, err := createPrivateOutputDirectory(outPath)
	if err != nil {
		return err
	}

	artifacts, token, blockCode, blockReason := validateSmokePreflight(configPath, config, inspection)
	if blockCode != "" {
		if err := writeRunEvidence(directory, config, inspection, fixtureSHA, "smoke", nil); err != nil {
			return err
		}
		status := smokeStatus{
			SchemaVersion: classifierlab.SchemaVersion, Status: "NOT_RUN", ReasonCode: blockCode,
			Reasons: []string{blockReason}, ExperimentID: config.ExperimentID, DatasetSHA256: fixtureSHA,
			PolicyStatus: inspection.Review.Status, CaseCount: len(fixtures), OfflineRequired: config.ReferenceWorker.OfflineRequired,
			NetworkIsolationStatus: "UNVERIFIED", OnlineAcquisitionAttempted: false,
			AdapterContractStatus: "UNVERIFIED", Checks: initialSmokeChecks(),
		}
		if err := writeJSONExclusive(filepath.Join(directory, "smoke_status.json"), status); err != nil {
			return err
		}
		return commandError{Code: blockCode, Err: errors.New(blockReason)}
	}

	endpoint, err := validateSmokeEndpoint(config.Candidate.Endpoint)
	if err != nil {
		return commandError{Code: "LOCAL_ENDPOINT_REQUIRED", Err: errors.New("candidate.endpoint must be a loopback HTTP origin")}
	}
	client, err := newSmokeHTTPClient(endpoint, smokeHTTPTimeout)
	if err != nil {
		return commandError{Code: "LOCAL_ENDPOINT_REQUIRED", Err: errors.New("cannot create a loopback-only HTTP client")}
	}
	execution := executeSmokeHTTP(context.Background(), client, endpoint, token, artifacts, config, fixtures)
	checks := execution.Checks
	contractChecks := adapterChecksFromSmoke(checks)
	statusValue, reasonCode, reason := smokeOutcome(execution)
	contractStatus := "UNVERIFIED"
	contractReason := "M01 external network isolation is unverified; this smoke cannot mark the adapter contract VERIFIED"
	status := smokeStatus{
		SchemaVersion: classifierlab.SchemaVersion, Status: statusValue, ReasonCode: reasonCode,
		Reasons: []string{reason}, ExperimentID: config.ExperimentID, DatasetSHA256: fixtureSHA,
		Endpoint: endpoint, PolicyStatus: "verified", PolicySHA256: artifacts.PolicySHA256,
		Model: &classifierlab.CandidateModel{
			Family: artifacts.Manifest.Model.Family, SourceID: artifacts.Manifest.Model.SourceID,
			Revision: artifacts.Manifest.Model.Revision, SDKVersion: artifacts.Manifest.SDK.Version,
			Device: artifacts.Manifest.Model.Device, DType: artifacts.Manifest.Model.DType,
		},
		ModelFilesSHA256: artifacts.ModelFilesSHA256, SDKLockSHA256: artifacts.SDKLockSHA256,
		ServerReady: execution.Ready, AuthenticationGuardVerified: execution.AuthenticationGuardVerified,
		OfflineRequired:              config.ReferenceWorker.OfflineRequired,
		OfflineConfigurationVerified: execution.OfflineConfigurationVerified,
		NetworkIsolationStatus:       execution.NetworkIsolationStatus,
		ChoiceBudgetOverflowObserved: execution.ChoiceBudgetOverflowObserved,
		ChoiceBudgetBoundaryStatus:   "NOT_OBSERVED",
		OnlineAcquisitionAttempted:   false,
		InferenceAttempted:           execution.InferenceAttempted, ModelInvoked: execution.ModelInvoked,
		ClassifyRequestsAttempted: execution.ClassifyRequestsAttempted, CaseCount: len(fixtures),
		PassedCases: countSmokeCases(execution.Results, "PASS"), FailedCases: countSmokeCases(execution.Results, "FAIL"),
		AdapterContractStatus: contractStatus, Checks: checks,
	}
	if execution.ChoiceBudgetOverflowObserved {
		status.ChoiceBudgetBoundaryStatus = "POLICY_TOO_LONG_OBSERVED"
	}
	manifestID, err := writeSmokeEvidence(directory, configPath, config, inspection, fixtureSHA, artifacts, status, execution.Results, contractChecks, contractStatus, contractReason)
	if err != nil {
		return err
	}
	status.ManifestID = manifestID
	if err := replaceJSON(filepath.Join(directory, "smoke_status.json"), status); err != nil {
		return err
	}
	if statusValue != "PASS" {
		return commandError{Code: reasonCode, Err: errors.New(reason)}
	}
	return nil
}

func validateSmokePreflight(configPath string, config classifierlab.Config, inspection repositoryInspection) (classifierlab.PreparedArtifacts, string, string, string) {
	if inspection.Review.Status == "POLICY_UNDEFINED" {
		return classifierlab.PreparedArtifacts{}, "", "POLICY_UNDEFINED", "canonical route policy sources conflict; smoke did not contact a server"
	}
	if inspection.Review.Status != "SOURCE_ALIGNED_REVIEW_REQUIRED" {
		return classifierlab.PreparedArtifacts{}, "", "POLICY_SOURCE_UNVERIFIED", "canonical route policy sources are not in a recognized reviewable state"
	}
	if config.Mode != classifierlab.ModeOff {
		return classifierlab.PreparedArtifacts{}, "", "RESOLVED_CONFIG_REQUIRED", "smoke requires the prepared mode=off resolved config"
	}
	if config.Candidate.TimeoutMS != smokeExpectedTimeout || config.Candidate.Retries != 0 {
		return classifierlab.PreparedArtifacts{}, "", "SMOKE_HTTP_CONFIG_INVALID", "smoke requires a 3000 ms timeout and zero retries"
	}
	if config.Candidate.ExpectedFamily != "laya-multilingual" || !modelRevisionPattern.MatchString(config.Candidate.ExpectedModelRevision) || !sha256Pattern.MatchString(config.Candidate.ExpectedPolicySHA256) {
		return classifierlab.PreparedArtifacts{}, "", "PREPARED_CONFIG_REQUIRED", "resolved config does not contain exact multilingual model, revision, and policy pins"
	}
	if !config.ReferenceWorker.OfflineRequired {
		return classifierlab.PreparedArtifacts{}, "", "OFFLINE_REQUIRED", "reference_worker.offline_required must be true"
	}
	endpoint, err := validateSmokeEndpoint(config.Candidate.Endpoint)
	if err != nil || endpoint == "" {
		return classifierlab.PreparedArtifacts{}, "", "LOCAL_ENDPOINT_REQUIRED", "candidate.endpoint must use http://127.0.0.1 with an explicit port"
	}
	if !smokeEnvNamePattern.MatchString(config.Candidate.AuthTokenEnv) {
		return classifierlab.PreparedArtifacts{}, "", "AUTH_CONFIG_INVALID", "candidate.auth_token_env is invalid"
	}
	token, found := os.LookupEnv(config.Candidate.AuthTokenEnv)
	if !found || strings.TrimSpace(token) == "" || token != strings.TrimSpace(token) || strings.ContainsAny(token, "\r\n") || len(token) > 4096 {
		return classifierlab.PreparedArtifacts{}, "", "AUTH_NOT_CONFIGURED", "candidate auth token environment variable is not configured"
	}

	artifactDirectory := filepath.Dir(configPath)
	artifacts, err := classifierlab.LoadPreparedArtifacts(
		artifactDirectory, config.ReferenceWorker.ModelDirectory, config.Candidate.ExpectedFamily,
		config.Candidate.ExpectedModelRevision, config.ReferenceWorker.Device, config.ReferenceWorker.DType,
		config.Candidate.ExpectedPolicySHA256,
	)
	if err != nil {
		return classifierlab.PreparedArtifacts{}, "", "PREPARED_ARTIFACTS_INVALID", "sibling route policy, prepared manifest, model tree, or SDK lock failed integrity validation"
	}
	if !samePreparedPolicySource(artifacts.Policy, inspection.Policy) {
		return classifierlab.PreparedArtifacts{}, "", "POLICY_SOURCE_MISMATCH", "prepared route policy does not match current source route order, criteria, or source paths"
	}
	return artifacts, token, "", ""
}

func samePreparedPolicySource(prepared, inspected classifierlab.RoutePolicy) bool {
	return prepared.PolicyVersion == inspected.PolicyVersion &&
		sameStringSequence(prepared.Routes, inspected.Routes) &&
		sameStringMap(prepared.Criteria, inspected.Criteria) &&
		sameStringSequence(prepared.SourcePaths, inspected.SourcePaths)
}

func validateSmokeCoverage(fixtures []smokeFixture) error {
	languages := map[string]bool{}
	boundaries := map[string]bool{}
	for _, fixture := range fixtures {
		if fixture.Boundary == "normal" {
			languages[fixture.Language] = true
		}
		boundaries[fixture.Boundary] = true
	}
	for _, language := range []string{"ja", "en", "mixed"} {
		if !languages[language] {
			return fmt.Errorf("smoke fixtures require a normal %s case", language)
		}
	}
	for _, boundary := range []string{"long_input", "policy_long"} {
		if !boundaries[boundary] {
			return fmt.Errorf("smoke fixtures require a %s case", boundary)
		}
	}
	return nil
}

func smokeFixtureMessage(fixture smokeFixture) (string, error) {
	if fixture.RepeatCount < 0 || fixture.RepeatCount > 12000 {
		return "", errors.New("repeat_count exceeds the smoke fixture bound")
	}
	var builder strings.Builder
	requested := len(fixture.Message) + len(fixture.Suffix) + len(fixture.RepeatText)*fixture.RepeatCount
	if requested > smokeMaxExpandedBytes {
		return "", errors.New("expanded message exceeds the HTTP request budget")
	}
	builder.Grow(requested)
	builder.WriteString(fixture.Message)
	for index := 0; index < fixture.RepeatCount; index++ {
		builder.WriteString(fixture.RepeatText)
	}
	builder.WriteString(fixture.Suffix)
	message := builder.String()
	if strings.TrimSpace(message) == "" || len(message) > smokeMaxExpandedBytes {
		return "", errors.New("expanded message is empty or too large")
	}
	for _, character := range message {
		if character == '\u0000' || character == '\r' || character == '\n' {
			return "", errors.New("expanded message contains a forbidden control character")
		}
	}
	return message, nil
}

func validateSmokeEndpoint(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("candidate endpoint is not a loopback HTTP origin")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != parsed.Port() {
		return "", errors.New("candidate endpoint port is invalid")
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
}

func newSmokeHTTPClient(endpoint string, timeout time.Duration) (*http.Client, error) {
	origin, err := validateSmokeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(origin)
	port := parsed.Port()
	transport := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: timeout,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, requestedPort, splitErr := net.SplitHostPort(address)
			if splitErr != nil || host != "127.0.0.1" || requestedPort != port {
				return nil, errors.New("smoke transport only permits the configured loopback endpoint")
			}
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, address)
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func executeSmokeHTTP(ctx context.Context, client *http.Client, endpoint, token string, artifacts classifierlab.PreparedArtifacts, config classifierlab.Config, fixtures []smokeFixture) smokeExecution {
	execution := smokeExecution{
		Results: make([]smokeCaseResult, 0, len(fixtures)), Checks: initialSmokeChecks(),
		NetworkIsolationStatus: "UNVERIFIED",
	}
	if ctx == nil || client == nil || token == "" || artifacts.PolicySHA256 == "" {
		execution.FailureCode = "SMOKE_CLIENT_INVALID"
		execution.FailureReason = "smoke HTTP client, auth token, or prepared policy is unavailable"
		return execution
	}
	origin, err := validateSmokeEndpoint(endpoint)
	if err != nil {
		execution.FailureCode = "LOCAL_ENDPOINT_REQUIRED"
		execution.FailureReason = "smoke endpoint must be loopback HTTP"
		return execution
	}
	if origin != endpoint {
		execution.FailureCode = "LOCAL_ENDPOINT_REQUIRED"
		execution.FailureReason = "smoke endpoint must use a canonical loopback origin"
		return execution
	}
	readyResponse, statusCode, readyErr := smokeGETReady(ctx, client, origin+"/readyz")
	if readyErr != nil || statusCode != http.StatusOK || validateSmokeReadyResponse(readyResponse) != nil {
		execution.FailureCode = "SERVER_NOT_READY"
		execution.FailureReason = "fixed candidate server did not return a valid ready response"
		return execution
	}
	execution.Ready = true
	execution.OfflineConfigurationVerified = readyResponse.OfflineConfigurationVerified
	execution.NetworkIsolationStatus = readyResponse.NetworkIsolationStatus
	execution.Checks["server_ready"] = true
	execution.Checks["offline_configuration_verified"] = config.ReferenceWorker.OfflineRequired && readyResponse.OfflineConfigurationVerified
	// The child attests its offline configuration; it cannot prove that the
	// process is externally isolated from the network.
	execution.Checks["network_isolation_verified"] = readyResponse.NetworkIsolationStatus == "VERIFIED"

	probe := classifierlab.CandidateInput{
		SchemaVersion: classifierlab.SchemaVersion, RequestID: "smoke-auth-probe",
		PolicySHA256: artifacts.PolicySHA256, Message: "synthetic auth guard probe",
	}
	probeStatus, probeReason, probeErr := smokePOSTStatus(ctx, client, origin+"/v1/classify", probe, "")
	if probeErr != nil || probeStatus != http.StatusUnauthorized || probeReason != "UNAUTHORIZED" {
		execution.FailureCode = "AUTH_GUARD_UNVERIFIED"
		execution.FailureReason = "candidate classify endpoint did not reject a request without the bearer token"
		return execution
	}
	execution.AuthenticationGuardVerified = true
	execution.Checks["authentication_guard"] = true

	expectedModel := classifierlab.CandidateModel{
		Family: artifacts.Manifest.Model.Family, SourceID: artifacts.Manifest.Model.SourceID,
		Revision: artifacts.Manifest.Model.Revision, SDKVersion: artifacts.Manifest.SDK.Version,
		Device: artifacts.Manifest.Model.Device, DType: artifacts.Manifest.Model.DType,
	}
	languageClassified := map[string]bool{"ja": false, "en": false, "mixed": false}
	var firstModel *classifierlab.CandidateModel
	allModelPinsMatch := true
	longInputBoundaryPassed, choiceBoundaryPassed, classifiedOutputSeen := false, false, false
	validOutputSeen := false
	for _, fixture := range fixtures {
		message, messageErr := smokeFixtureMessage(fixture)
		caseResult := smokeCaseResult{CaseID: fixture.ID, Language: fixture.Language, Boundary: fixture.Boundary, Status: "FAIL"}
		if messageErr != nil {
			caseResult.ReasonCode = "FIXTURE_INVALID"
			execution.Results = append(execution.Results, caseResult)
			execution.FailureCode = "SMOKE_FIXTURE_INVALID"
			execution.FailureReason = "expanded smoke fixture is invalid"
			continue
		}
		input := classifierlab.CandidateInput{
			SchemaVersion: classifierlab.SchemaVersion, RequestID: "smoke-" + fixture.ID,
			PolicySHA256: artifacts.PolicySHA256, Message: message,
		}
		started := time.Now()
		execution.ClassifyRequestsAttempted++
		execution.InferenceAttempted = true
		result, httpStatus, classifyErr := smokePOSTClassify(ctx, client, origin+"/v1/classify", input, token)
		caseResult.HTTPStatus = httpStatus
		caseResult.HTTPWallMS = float64(time.Since(started).Microseconds()) / 1000
		if classifyErr != nil {
			caseResult.ReasonCode = smokeErrorCode(classifyErr)
			execution.Results = append(execution.Results, caseResult)
			execution.FailureCode = "CLASSIFY_REQUEST_FAILED"
			execution.FailureReason = "candidate classify request failed or returned an invalid response"
			break
		}
		execution.ModelInvoked = true
		caseResult.Result = &result
		if err := validateSmokeResult(input, result, expectedModel, artifacts.PolicySHA256, artifacts.Policy.Routes); err != nil {
			caseResult.ReasonCode = "INVALID_MODEL_OUTPUT"
			execution.Results = append(execution.Results, caseResult)
			execution.FailureCode = "INVALID_MODEL_OUTPUT"
			execution.FailureReason = "candidate response did not match the fixed model and route contract"
			continue
		}
		validOutputSeen = true
		if firstModel == nil {
			model := result.Model
			firstModel = &model
		} else if *firstModel != result.Model {
			allModelPinsMatch = false
		}
		if result.Status == classifierlab.CandidateClassified {
			classifiedOutputSeen = true
			if fixture.Boundary == "normal" {
				languageClassified[fixture.Language] = true
			}
		}
		if err := validateSmokeBoundary(fixture, result); err != nil {
			caseResult.ReasonCode = smokeErrorCode(err)
			execution.Results = append(execution.Results, caseResult)
			execution.FailureCode = "SMOKE_BOUNDARY_FAILED"
			execution.FailureReason = "candidate response did not satisfy a required input or choice-budget boundary"
			continue
		}
		if fixture.Boundary == "long_input" && result.Status == classifierlab.CandidateAbstain && result.ReasonCode == "INPUT_TOO_LONG" {
			longInputBoundaryPassed = true
		}
		if fixture.Boundary == "policy_long" && result.Status == classifierlab.CandidateAbstain && result.ReasonCode == "POLICY_TOO_LONG" {
			choiceBoundaryPassed = true
			execution.ChoiceBudgetOverflowObserved = true
		}
		caseResult.Status = "PASS"
		execution.Results = append(execution.Results, caseResult)
	}

	execution.Checks["pinned_sdk_version"] = validOutputSeen && allModelPinsMatch && artifacts.Manifest.SDK.Version == expectedModel.SDKVersion
	execution.Checks["router_predict_signature"] = execution.Ready && validOutputSeen
	execution.Checks["multilingual_checkpoint_fixed"] = allModelPinsMatch && languageClassified["ja"] && languageClassified["en"] && languageClassified["mixed"]
	execution.Checks["choice_output_and_probabilities"] = classifiedOutputSeen
	execution.Checks["token_truncation_metadata"] = longInputBoundaryPassed
	execution.Checks["choice_budget_overflow"] = choiceBoundaryPassed
	return execution
}

func initialSmokeChecks() map[string]bool {
	return map[string]bool{
		"server_ready":                    false,
		"authentication_guard":            false,
		"pinned_sdk_version":              false,
		"router_predict_signature":        false,
		"multilingual_checkpoint_fixed":   false,
		"choice_output_and_probabilities": false,
		"token_truncation_metadata":       false,
		"choice_budget_overflow":          false,
		"offline_configuration_verified":  false,
		"network_isolation_verified":      false,
	}
}

func adapterChecksFromSmoke(checks map[string]bool) map[string]bool {
	return map[string]bool{
		"pinned_sdk_version":              checks["pinned_sdk_version"],
		"router_predict_signature":        checks["router_predict_signature"],
		"multilingual_checkpoint_fixed":   checks["multilingual_checkpoint_fixed"],
		"choice_output_and_probabilities": checks["choice_output_and_probabilities"],
		"token_truncation_metadata":       checks["token_truncation_metadata"],
		"offline_configuration_verified":  checks["offline_configuration_verified"],
		"network_isolation_verified":      checks["network_isolation_verified"],
	}
}

func allSmokeChecksPassed(checks map[string]bool) bool {
	if len(checks) == 0 {
		return false
	}
	for _, passed := range checks {
		if !passed {
			return false
		}
	}
	return true
}

func smokeAdapterContractVerified(execution smokeExecution) bool {
	// External egress isolation is not independently attested by /readyz. Keep
	// the adapter contract unverified even when all individual HTTP cases pass.
	return false
}

func smokeOutcome(execution smokeExecution) (status, reasonCode, reason string) {
	if execution.FailureCode != "" {
		return "FAILED", execution.FailureCode, execution.FailureReason
	}
	if !execution.OfflineConfigurationVerified {
		return "INCOMPLETE", "OFFLINE_CONFIGURATION_UNVERIFIED", "readyz did not attest offline configuration for the isolated child"
	}
	// No independent network-isolation proof is available to smoke. Even a
	// readiness response that claims VERIFIED cannot promote this run.
	return "INCOMPLETE", "OFFLINE_ISOLATION_UNVERIFIED", "child offline configuration is attested, but external network isolation has no independent evidence"
}

func validateSmokeReadyResponse(ready smokeReadyResponse) error {
	if ready.Status != "ready" || ready.ReasonCode != "" || !ready.OfflineConfigurationVerified ||
		ready.NetworkIsolationStatus != "UNVERIFIED" {
		return errors.New("ready response does not match the child offline configuration contract")
	}
	return nil
}

func smokeGETReady(ctx context.Context, client *http.Client, endpoint string) (smokeReadyResponse, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return smokeReadyResponse{}, 0, errors.New("build ready request")
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return smokeReadyResponse{}, 0, errors.New("ready request failed")
	}
	defer response.Body.Close()
	body, err := readSmokeHTTPBody(response)
	if err != nil {
		return smokeReadyResponse{}, response.StatusCode, errors.New("ready response is invalid")
	}
	if response.StatusCode != http.StatusOK {
		return smokeReadyResponse{}, response.StatusCode, errors.New("ready endpoint is not ready")
	}
	if !isJSONMediaType(response.Header.Get("Content-Type")) {
		return smokeReadyResponse{}, response.StatusCode, errors.New("ready response content type is invalid")
	}
	var ready smokeReadyResponse
	if err := decodeStrictJSON(body, &ready); err != nil {
		return smokeReadyResponse{}, response.StatusCode, errors.New("ready response schema is invalid")
	}
	if err := validateSmokeReadyResponse(ready); err != nil {
		return smokeReadyResponse{}, response.StatusCode, errors.New("ready response attestation is invalid")
	}
	return ready, response.StatusCode, nil
}

func smokePOSTStatus(ctx context.Context, client *http.Client, endpoint string, input classifierlab.CandidateInput, token string) (int, string, error) {
	response, statusCode, err := smokePOSTRaw(ctx, client, endpoint, input, token)
	if err != nil {
		return statusCode, "", err
	}
	if statusCode == http.StatusUnauthorized {
		var errorResponse smokeHTTPErrorResponse
		if !isJSONMediaType(response.Header.Get("Content-Type")) || decodeStrictJSON(response.Body, &errorResponse) != nil || !smokeReasonPattern.MatchString(errorResponse.ReasonCode) {
			return statusCode, "", errors.New("unauthorized response schema is invalid")
		}
		return statusCode, errorResponse.ReasonCode, nil
	}
	return statusCode, "", errors.New("auth guard probe was not rejected")
}

func smokePOSTClassify(ctx context.Context, client *http.Client, endpoint string, input classifierlab.CandidateInput, token string) (classifierlab.CandidateResult, int, error) {
	response, statusCode, err := smokePOSTRaw(ctx, client, endpoint, input, token)
	if err != nil {
		return classifierlab.CandidateResult{}, statusCode, err
	}
	if statusCode != http.StatusOK {
		return classifierlab.CandidateResult{}, statusCode, errors.New("classify endpoint returned a non-success HTTP status")
	}
	if !isJSONMediaType(response.Header.Get("Content-Type")) {
		return classifierlab.CandidateResult{}, statusCode, errors.New("classify response content type is invalid")
	}
	var result classifierlab.CandidateResult
	if err := decodeStrictJSON(response.Body, &result); err != nil {
		return classifierlab.CandidateResult{}, statusCode, errors.New("classify response schema is invalid")
	}
	return result, statusCode, nil
}

type smokeHTTPResponse struct {
	Header http.Header
	Body   []byte
}

func smokePOSTRaw(ctx context.Context, client *http.Client, endpoint string, input classifierlab.CandidateInput, token string) (smokeHTTPResponse, int, error) {
	body, err := json.Marshal(input)
	if err != nil || len(body) == 0 || len(body) > classifierlab.MaxHTTPBodyBytes {
		return smokeHTTPResponse{}, 0, errors.New("classify request exceeds the HTTP body contract")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return smokeHTTPResponse{}, 0, errors.New("build classify request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return smokeHTTPResponse{}, 0, errors.New("classify request failed")
	}
	defer response.Body.Close()
	responseBody, err := readSmokeHTTPBody(response)
	if err != nil {
		return smokeHTTPResponse{}, response.StatusCode, errors.New("classify response exceeds its bounded JSON contract")
	}
	return smokeHTTPResponse{Header: response.Header.Clone(), Body: responseBody}, response.StatusCode, nil
}

func readSmokeHTTPBody(response *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(response.Body, smokeMaxResponseBytes+1))
	if err != nil || len(body) > smokeMaxResponseBytes {
		return nil, errors.New("response body read failed or exceeded limit")
	}
	return body, nil
}

func isJSONMediaType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func validateSmokeResult(input classifierlab.CandidateInput, result classifierlab.CandidateResult, expectedModel classifierlab.CandidateModel, expectedPolicy string, routes []string) error {
	if result.SchemaVersion != classifierlab.SchemaVersion || result.RequestID != input.RequestID || result.PolicySHA256 != expectedPolicy || result.Model != expectedModel {
		return errors.New("response identity, model, or policy did not match")
	}
	switch result.Status {
	case classifierlab.CandidateAbstain:
		if result.Route != nil || len(result.Probabilities) != 0 || (result.ReasonCode != "INPUT_TOO_LONG" && result.ReasonCode != "POLICY_TOO_LONG") {
			return errors.New("abstention did not provide an allowed reason without a route")
		}
		return nil
	case classifierlab.CandidateClassified:
	default:
		return errors.New("response status is unknown")
	}
	if result.Route == nil || len(routes) != classifierlab.MaxProbabilityLabels || len(result.Probabilities) != len(routes) {
		return errors.New("classified response does not contain one choice and all route probabilities")
	}
	allowed := make(map[classifierlab.RouteLabel]struct{}, len(routes))
	var total float64
	top, second := -1.0, -1.0
	for _, rawRoute := range routes {
		route := classifierlab.RouteLabel(rawRoute)
		allowed[route] = struct{}{}
		value, exists := result.Probabilities[route]
		if !exists || !finiteSmokeUnit(value) {
			return errors.New("classified response has an invalid route probability")
		}
		total += value
		if value > top {
			second, top = top, value
		} else if value > second {
			second = value
		}
	}
	if _, exists := allowed[*result.Route]; !exists || math.Abs(total-1) > 1e-4 ||
		!finiteSmokeUnit(result.SelectedProbability) || math.Abs(result.SelectedProbability-result.Probabilities[*result.Route]) > 1e-4 ||
		!finiteSmokeUnit(result.TopTwoMargin) || math.Abs(result.TopTwoMargin-(top-second)) > 1e-4 {
		return errors.New("classified response route or probability summary is inconsistent")
	}
	if result.NativeConfidence != nil && !finiteSmokeUnit(*result.NativeConfidence) {
		return errors.New("native confidence is outside [0,1]")
	}
	if result.ProbabilityStatus != classifierlab.ProbabilityStatusUnvalidated || result.ReasonCode != "OK" ||
		result.Input.EffectiveSequenceTokens < 1 || result.Input.Truncated ||
		!finiteSmokeNonnegative(result.Timing.Prepare) || !finiteSmokeNonnegative(result.Timing.Inference) ||
		!finiteSmokeNonnegative(result.Timing.Postprocess) || !finiteSmokeNonnegative(result.Timing.ServerTotal) {
		return errors.New("classified response metadata or timing is invalid")
	}
	return nil
}

func validateSmokeBoundary(fixture smokeFixture, result classifierlab.CandidateResult) error {
	switch fixture.Boundary {
	case "normal":
		if result.Status != classifierlab.CandidateClassified {
			return errors.New("normal multilingual case must produce a classified response")
		}
	case "long_input":
		if result.Status != classifierlab.CandidateAbstain || result.ReasonCode != "INPUT_TOO_LONG" {
			return errors.New("long input must explicitly abstain with INPUT_TOO_LONG")
		}
	case "policy_long":
		if result.Status == classifierlab.CandidateClassified || result.Status == classifierlab.CandidateAbstain && result.ReasonCode == "POLICY_TOO_LONG" {
			return nil
		}
		return errors.New("choice budget must classify complete output or explicitly abstain with POLICY_TOO_LONG")
	default:
		return errors.New("unknown smoke boundary")
	}
	return nil
}

func finiteSmokeUnit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func finiteSmokeNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func smokeErrorCode(err error) string {
	if err == nil {
		return "UNKNOWN"
	}
	switch {
	case strings.Contains(err.Error(), "HTTP status"):
		return "HTTP_ERROR"
	case strings.Contains(err.Error(), "exceeds"):
		return "BODY_TOO_LARGE"
	case strings.Contains(err.Error(), "schema") || strings.Contains(err.Error(), "content type"):
		return "INVALID_RESPONSE"
	case strings.Contains(err.Error(), "request failed"):
		return "HTTP_REQUEST_FAILED"
	default:
		return "CONTRACT_INVALID"
	}
}

func countSmokeCases(results []smokeCaseResult, status string) int {
	count := 0
	for _, result := range results {
		if result.Status == status {
			count++
		}
	}
	return count
}

func writeSmokeEvidence(directory, configPath string, config classifierlab.Config, inspection repositoryInspection, datasetSHA string,
	artifacts classifierlab.PreparedArtifacts, status smokeStatus, results []smokeCaseResult, contractChecks map[string]bool, contractStatus, contractReason string) (string, error) {
	for _, item := range []struct {
		name  string
		value any
	}{
		{name: "baseline_manifest.json", value: inspection.Baseline},
		{name: classifierlab.PreparedPolicyFileName, value: artifacts.Policy},
		{name: "policy_review.json", value: inspection.Review},
	} {
		if err := writeJSONExclusive(filepath.Join(directory, item.name), item.value); err != nil {
			return "", err
		}
	}
	manifest, err := buildExperimentManifest(config, inspection, datasetSHA, "smoke", nil)
	if err != nil {
		return "", err
	}
	manifest.RunStatus = "BLOCKED"
	if status.Status == "PASS" {
		manifest.RunStatus = "COMPLETED"
	}
	manifest.PolicySHA256 = artifacts.PolicySHA256
	manifest.PolicyStatus = "verified"
	manifest.ModelFamily = artifacts.Manifest.Model.Family
	manifest.ModelRevision = artifacts.Manifest.Model.Revision
	manifest.Device = artifacts.Manifest.Model.Device
	manifest.DType = artifacts.Manifest.Model.DType
	sdkVersion, lockSHA := artifacts.Manifest.SDK.Version, artifacts.SDKLockSHA256
	manifest.SDKVersion = &sdkVersion
	manifest.SDKLockSHA256 = &lockSHA
	manifest.ManifestID, err = experimentManifestID(manifest)
	if err != nil {
		return "", err
	}
	if !validSHA256(manifest.ManifestID) {
		return "", errors.New("could not create smoke experiment manifest ID")
	}
	status.ManifestID = manifest.ManifestID
	if err := writeJSONExclusive(filepath.Join(directory, "experiment_manifest.json"), manifest); err != nil {
		return "", err
	}
	contract := adapterContract{
		Status: contractStatus, Distribution: artifacts.Manifest.SDK.Distribution,
		SourceURL: artifacts.Manifest.SDK.SourceURL, SDKVersion: &sdkVersion, LockSHA256: &lockSHA,
		Model:      artifacts.Manifest.Model.Family,
		PredictAPI: `Router.predict(message, questions, model="multilingual", max_len=..., head_max_len=...)`,
		MaxLen:     config.ReferenceWorker.MaxLen, HeadMaxLen: config.ReferenceWorker.HeadMaxLen,
		Checks: contractChecks, Reason: contractReason,
	}
	if err := writeJSONExclusive(filepath.Join(directory, "adapter_contract.json"), contract); err != nil {
		return "", err
	}
	preparedManifest, err := readBoundedFile(filepath.Join(artifacts.ArtifactDirectory, classifierlab.PreparedManifestFileName), classifierlab.MaxPreparedArtifactBytes)
	if err != nil {
		return "", fmt.Errorf("read prepared manifest for smoke evidence: %w", err)
	}
	if err := writeBytesExclusive(filepath.Join(directory, classifierlab.PreparedManifestFileName), preparedManifest); err != nil {
		return "", err
	}
	resolvedConfig, err := readBoundedFile(configPath, 1<<20)
	if err != nil {
		return "", fmt.Errorf("read resolved config for smoke evidence: %w", err)
	}
	if err := writeBytesExclusive(filepath.Join(directory, "resolved-config.yaml"), resolvedConfig); err != nil {
		return "", err
	}
	lockPath := filepath.Join(artifacts.ArtifactDirectory, filepath.FromSlash(artifacts.Manifest.SDK.LockFile))
	lockBytes, err := readBoundedFile(lockPath, classifierlab.MaxPreparedArtifactBytes)
	if err != nil {
		return "", fmt.Errorf("read SDK lock for smoke evidence: %w", err)
	}
	lockOutput := filepath.Join(directory, filepath.FromSlash(artifacts.Manifest.SDK.LockFile))
	if err := os.MkdirAll(filepath.Dir(lockOutput), 0o700); err != nil {
		return "", fmt.Errorf("create SDK lock evidence directory: %w", err)
	}
	if err := writeBytesExclusive(lockOutput, lockBytes); err != nil {
		return "", err
	}
	if err := writeSmokeResults(filepath.Join(directory, "smoke_results.jsonl"), results); err != nil {
		return "", err
	}
	if err := writeJSONExclusive(filepath.Join(directory, "smoke_status.json"), status); err != nil {
		return "", err
	}
	return manifest.ManifestID, nil
}

func writeSmokeResults(path string, results []smokeCaseResult) error {
	var output bytes.Buffer
	for _, result := range results {
		data, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode smoke case result: %w", err)
		}
		output.Write(data)
		output.WriteByte('\n')
	}
	return writeBytesExclusive(path, output.Bytes())
}

func replaceJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode output %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("update output %s: %w", filepath.Base(path), err)
	}
	return nil
}
