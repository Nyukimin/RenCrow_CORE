package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	"gopkg.in/yaml.v3"
)

const testAuthToken = "test-only-token"

type testArtifacts struct {
	config    loadedConfig
	artifacts classifierlab.PreparedArtifacts
}

func makeTestArtifacts(t *testing.T, semanticStatus string) testArtifacts {
	t.Helper()
	root := t.TempDir()
	modelDirectory := filepath.Join(root, "model")
	if err := os.MkdirAll(modelDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	modelData := []byte("synthetic model file; never a Laya checkpoint")
	if err := os.WriteFile(filepath.Join(modelDirectory, "weights.bin"), modelData, 0o600); err != nil {
		t.Fatal(err)
	}
	policy := classifierlab.RoutePolicy{
		PolicyVersion:        "rencrow-route-policy-v1",
		Routes:               testRouteNames(),
		Instructions:         "Choose exactly one route for the isolated contract test.",
		Criteria:             makeTestCriteria(),
		SourcePaths:          []string{"rules/routing-policy.md"},
		LegacyPromptSHA256:   strings.Repeat("0", 64),
		SemanticReviewStatus: semanticStatus,
	}
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policySHA := digestHex(policyBytes)
	if err := os.WriteFile(filepath.Join(root, classifierlab.PreparedPolicyFileName), policyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	files := []classifierlab.PreparedModelFile{{Path: "weights.bin", SHA256: digestHex(modelData)}}
	filesBytes, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	lock := []byte("laya==0.4.11 --hash=sha256:" + strings.Repeat("1", 64) + "\n")
	if err := os.WriteFile(filepath.Join(root, "requirements.lock"), lock, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := classifierlab.PreparedManifest{
		SchemaVersion: classifierlab.PreparedManifestSchemaVersion,
		PolicySHA256:  policySHA,
		Model: classifierlab.PreparedModelManifest{
			Family:      "laya-multilingual",
			SourceID:    "convaiinnovations/laya-multilingual",
			Revision:    strings.Repeat("a", 40),
			Device:      "cpu",
			DType:       "float32",
			Files:       files,
			FilesSHA256: digestHex(filesBytes),
		},
		SDK: classifierlab.PreparedSDKManifest{
			Distribution: "laya",
			Version:      "0.4.11",
			SourceURL:    "https://github.com/NandhaKishorM/laya",
			LockFile:     "requirements.lock",
			LockSHA256:   digestHex(lock),
			Packages:     map[string]string{"laya": "0.4.11", "torch": "2.4.0"},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, classifierlab.PreparedManifestFileName), manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python interpreter is unavailable for isolated child-process contract tests")
	}
	python, err = filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	value := resolvedConfig{
		SchemaVersion: classifierlab.SchemaVersion,
		Mode:          classifierlab.ModeOff,
		ExperimentID:  "synthetic-server-contract",
		Shadow: shadowConfig{
			SampleRate: 0.1, QueueCapacity: 8, Workers: 1, MaxJobAgeMS: 10000, DrainTimeoutMS: 2000,
		},
		Candidate: candidateConfig{
			Endpoint:              "http://127.0.0.1:18190",
			AuthTokenEnv:          "ROUTING_TEST_TOKEN",
			TimeoutMS:             httpWaitTimeoutMS,
			Retries:               0,
			ExpectedFamily:        manifest.Model.Family,
			ExpectedModelRevision: manifest.Model.Revision,
			ExpectedPolicySHA256:  policySHA,
		},
		ReferenceWorker: referenceWorkerConfig{
			PythonExecutable: python,
			ModelDirectory:   modelDirectory,
			Device:           "cpu",
			DType:            "float32",
			IntraOpThreads:   1,
			InterOpThreads:   1,
			MaxLen:           8192,
			HeadMaxLen:       1024,
			HardTimeoutMS:    childHardTimeoutMS,
			OfflineRequired:  true,
		},
		Recording: recordingConfig{
			Directory: filepath.Join(root, "recording"), OutputQueueCapacity: 8, TemporaryRawRetentionDays: 7,
		},
		Evaluation: evaluationConfig{
			WarmupRequests: 1, LatencyRepetitions: 1, QualityRepetitions: 1, PerformanceConcurrency: []int{1, 2},
			BootstrapResamples: 10, NoninferiorityMarginMacroF1: 0.01,
		},
	}
	configYAML, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "resolved-config.yaml")
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadResolvedConfig(configPath)
	if err != nil {
		t.Fatalf("load synthetic config: %v", err)
	}
	config.Token = testAuthToken
	artifacts, err := classifierlab.LoadPreparedArtifacts(
		root,
		modelDirectory,
		value.Candidate.ExpectedFamily,
		value.Candidate.ExpectedModelRevision,
		value.ReferenceWorker.Device,
		value.ReferenceWorker.DType,
		value.Candidate.ExpectedPolicySHA256,
	)
	if err != nil && semanticStatus == "verified" {
		t.Fatalf("load synthetic prepared artifacts: %v", err)
	}
	return testArtifacts{config: config, artifacts: artifacts}
}

func testRouteNames() []string {
	return []string{"CHAT", "PLAN", "ANALYZE", "OPS", "RESEARCH", "CODE", "CODE1", "CODE2", "CODE3", "CODE4", "WILD"}
}

func testRouteLabels() []classifierlab.RouteLabel {
	routes := testRouteNames()
	labels := make([]classifierlab.RouteLabel, len(routes))
	for index, route := range routes {
		labels[index] = classifierlab.RouteLabel(route)
	}
	return labels
}

func makeTestCriteria() map[string]string {
	criteria := make(map[string]string, classifierlab.MaxProbabilityLabels)
	for _, route := range testRouteNames() {
		criteria[route] = "Use " + route + " only when the request matches this route."
	}
	return criteria
}

func digestHex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func classifiedTestResult(input classifierlab.CandidateInput, model classifierlab.CandidateModel) classifierlab.CandidateResult {
	probabilities := make(map[classifierlab.RouteLabel]float64, classifierlab.MaxProbabilityLabels)
	for _, label := range testRouteNames() {
		probabilities[classifierlab.RouteLabel(label)] = 0
	}
	probabilities[classifierlab.LabelCHAT] = 1
	route := classifierlab.LabelCHAT
	return classifierlab.CandidateResult{
		SchemaVersion:       classifierlab.SchemaVersion,
		RequestID:           input.RequestID,
		Status:              classifierlab.CandidateClassified,
		Route:               &route,
		Probabilities:       probabilities,
		SelectedProbability: 1,
		TopTwoMargin:        1,
		NativeConfidence:    nil,
		ProbabilityStatus:   classifierlab.ProbabilityStatusUnvalidated,
		Model:               model,
		PolicySHA256:        input.PolicySHA256,
		Input:               classifierlab.CandidateInputMetadata{EffectiveSequenceTokens: 12},
		Timing:              classifierlab.CandidateTiming{Prepare: 0.1, Inference: 2, Postprocess: 0.1, ServerTotal: 2.2},
		ReasonCode:          "OK",
	}
}

type fakeBackend struct {
	ready           bool
	readyReason     string
	offlineVerified bool
	classify        func(classifierlab.CandidateInput) (classifierlab.CandidateResult, error)
	calls           atomic.Int32
}

func (f *fakeBackend) Ready() (bool, string) { return f.ready, f.readyReason }

func (f *fakeBackend) OfflineConfigurationVerified() bool { return f.offlineVerified }

func (f *fakeBackend) Readiness() (bool, string, bool) {
	return f.ready, f.readyReason, f.ready && f.offlineVerified
}

func (f *fakeBackend) Classify(input classifierlab.CandidateInput) (classifierlab.CandidateResult, error) {
	f.calls.Add(1)
	return f.classify(input)
}

func TestPreparedPolicyUndefinedFailsClosedWithoutStartingWorker(t *testing.T) {
	fixture := makeTestArtifacts(t, "POLICY_UNDEFINED")
	fixture.config.Value.ReferenceWorker.PythonExecutable = filepath.Join(t.TempDir(), "must-not-start-python")
	worker, _, reason := startPreparedWorker(context.Background(), fixture.config, "unused", time.Second)
	if worker != nil || reason != "POLICY_UNDEFINED" {
		t.Fatalf("startPreparedWorker() = worker %v, reason %q; want nil worker and POLICY_UNDEFINED", worker, reason)
	}

	api := newClassificationServer(fixture.config, worker, reason, nil)
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "POLICY_UNDEFINED") {
		t.Fatalf("/readyz status/body = %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestPreparedManifestRejectsDeviceAndModelHashMismatch(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	if _, err := classifierlab.LoadPreparedArtifacts(
		fixture.config.Directory,
		fixture.config.Value.ReferenceWorker.ModelDirectory,
		fixture.config.Value.Candidate.ExpectedFamily,
		fixture.config.Value.Candidate.ExpectedModelRevision,
		"cuda:0",
		fixture.config.Value.ReferenceWorker.DType,
		fixture.config.Value.Candidate.ExpectedPolicySHA256,
	); err == nil {
		t.Fatal("prepared manifest with a different device was accepted")
	}
	if err := os.WriteFile(filepath.Join(fixture.config.Value.ReferenceWorker.ModelDirectory, "weights.bin"), []byte("mutated after prepare"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := classifierlab.LoadPreparedArtifacts(
		fixture.config.Directory,
		fixture.config.Value.ReferenceWorker.ModelDirectory,
		fixture.config.Value.Candidate.ExpectedFamily,
		fixture.config.Value.Candidate.ExpectedModelRevision,
		fixture.config.Value.ReferenceWorker.Device,
		fixture.config.Value.ReferenceWorker.DType,
		fixture.config.Value.Candidate.ExpectedPolicySHA256,
	); err == nil {
		t.Fatal("model file hash mismatch was accepted")
	}
}

func TestServerAuthPolicyBodyLimitAndStrictJSON(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	model := classifierlab.CandidateModel{
		Family: fixture.artifacts.Manifest.Model.Family, SourceID: fixture.artifacts.Manifest.Model.SourceID,
		Revision: fixture.artifacts.Manifest.Model.Revision, SDKVersion: fixture.artifacts.Manifest.SDK.Version,
		Device: fixture.artifacts.Manifest.Model.Device, DType: fixture.artifacts.Manifest.Model.DType,
	}
	backend := &fakeBackend{ready: true, offlineVerified: true, classify: func(input classifierlab.CandidateInput) (classifierlab.CandidateResult, error) {
		return classifiedTestResult(input, model), nil
	}}
	api := newClassificationServer(fixture.config, backend, "", &fixture.artifacts)
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	client := server.Client()
	baseInput := classifierlab.CandidateInput{
		SchemaVersion: classifierlab.SchemaVersion,
		RequestID:     "synthetic-case-001",
		PolicySHA256:  fixture.artifacts.PolicySHA256,
		Message:       "Synthetic contract input",
	}

	status, result := postCandidate(t, client, server.URL, testAuthToken, baseInput)
	if status != http.StatusOK || result.Status != classifierlab.CandidateClassified || result.Route == nil || *result.Route != classifierlab.LabelCHAT {
		t.Fatalf("valid request status/result = %d %+v", status, result)
	}
	if backend.calls.Load() != 1 {
		t.Fatalf("backend calls = %d, want 1", backend.calls.Load())
	}

	status, _ = postCandidate(t, client, server.URL, "wrong-token", baseInput)
	if status != http.StatusUnauthorized {
		t.Fatalf("bad auth status = %d, want 401", status)
	}
	wrongPolicy := baseInput
	wrongPolicy.PolicySHA256 = strings.Repeat("f", 64)
	status, _ = postCandidate(t, client, server.URL, testAuthToken, wrongPolicy)
	if status != http.StatusConflict {
		t.Fatalf("policy mismatch status = %d, want 409", status)
	}

	status = postRaw(t, client, server.URL, testAuthToken, []byte(`{"schema_version":"routing-classifier-test/v1","schema_version":"routing-classifier-test/v1"}`), "application/json")
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate JSON field status = %d, want 400", status)
	}
	status = postRaw(t, client, server.URL, testAuthToken, bytes.Repeat([]byte("x"), classifierlab.MaxHTTPBodyBytes+1), "application/json")
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d, want 413", status)
	}
	status = postRaw(t, client, server.URL, testAuthToken, []byte(`{}`), "text/plain")
	if status != http.StatusBadRequest {
		t.Fatalf("wrong content type status = %d, want 400", status)
	}
}

func TestReadyzReportsVerifiedOfflineConfigurationAndUnverifiedIsolation(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	backend := &fakeBackend{ready: true, offlineVerified: true}
	api := newClassificationServer(fixture.config, backend, "", &fixture.artifacts)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	decode := func(response *httptest.ResponseRecorder) (string, bool, string) {
		t.Helper()
		var body struct {
			Status                       string `json:"status"`
			OfflineConfigurationVerified bool   `json:"offline_configuration_verified"`
			NetworkIsolationStatus       string `json:"network_isolation_status"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode readiness body: %v", err)
		}
		return body.Status, body.OfflineConfigurationVerified, body.NetworkIsolationStatus
	}
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	status, verified, isolation := decode(response)
	if response.Code != http.StatusOK || status != "ready" || !verified || isolation != "UNVERIFIED" {
		t.Fatalf("ready response = %d, status=%q verified=%v isolation=%q", response.Code, status, verified, isolation)
	}

	backend.offlineVerified = false
	response = httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	status, verified, isolation = decode(response)
	if response.Code != http.StatusServiceUnavailable || status != "not_ready" || verified || isolation != "UNVERIFIED" {
		t.Fatalf("not-ready response = %d, status=%q verified=%v isolation=%q", response.Code, status, verified, isolation)
	}
}

func TestServerBusyAndHTTPWaitTimeoutDoNotQueueRequests(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	model := classifierlab.CandidateModel{
		Family: fixture.artifacts.Manifest.Model.Family, SourceID: fixture.artifacts.Manifest.Model.SourceID,
		Revision: fixture.artifacts.Manifest.Model.Revision, SDKVersion: fixture.artifacts.Manifest.SDK.Version,
		Device: fixture.artifacts.Manifest.Model.Device, DType: fixture.artifacts.Manifest.Model.DType,
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	backend := &fakeBackend{ready: true, offlineVerified: true, classify: func(input classifierlab.CandidateInput) (classifierlab.CandidateResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return classifiedTestResult(input, model), nil
	}}
	api := newClassificationServer(fixture.config, backend, "", &fixture.artifacts)
	api.httpTimeout = 20 * time.Millisecond
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	input := classifierlab.CandidateInput{SchemaVersion: classifierlab.SchemaVersion, RequestID: "busy-case", PolicySHA256: fixture.artifacts.PolicySHA256, Message: "Wait on a synthetic backend"}
	first := make(chan int, 1)
	go func() {
		status, _, err := sendCandidate(server.Client(), server.URL, testAuthToken, input)
		if err != nil {
			first <- 0
			return
		}
		first <- status
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fake backend did not receive first request")
	}
	secondStatus, _ := postCandidate(t, server.Client(), server.URL, testAuthToken, input)
	if secondStatus != http.StatusTooManyRequests {
		t.Fatalf("busy request status = %d, want 429", secondStatus)
	}
	firstStatus := <-first
	if firstStatus != http.StatusGatewayTimeout {
		t.Fatalf("first request status = %d, want 504", firstStatus)
	}
	close(release)
}

func postCandidate(t *testing.T, client *http.Client, endpoint, token string, input classifierlab.CandidateInput) (int, classifierlab.CandidateResult) {
	t.Helper()
	status, responseBytes, err := sendCandidate(client, endpoint, token, input)
	if err != nil {
		t.Fatal(err)
	}
	var result classifierlab.CandidateResult
	if status == http.StatusOK {
		if err := json.Unmarshal(responseBytes, &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
	}
	return status, result
}

func sendCandidate(client *http.Client, endpoint, token string, input classifierlab.CandidateInput) (int, []byte, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+"/v1/classify", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	responseBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, responseBytes, nil
}

func postRaw(t *testing.T, client *http.Client, endpoint, token string, body []byte, contentType string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint+"/v1/classify", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func TestSDKWorkerFakeChildProtocolTimeoutAndOwnedCleanup(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	script := fakePythonChildScript(false)
	worker := newSDKWorker(fixture.config, fixture.artifacts, script, time.Second)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start fake SDK child: %v", err)
	}
	waitForWorkerReady(t, worker)
	input := classifierlab.CandidateInput{SchemaVersion: classifierlab.SchemaVersion, RequestID: "worker-case", PolicySHA256: fixture.artifacts.PolicySHA256, Message: "synthetic"}
	result, err := worker.Classify(input)
	if err != nil {
		t.Fatalf("classify with fake child: %v", err)
	}
	model := classifierlab.CandidateModel{
		Family: fixture.artifacts.Manifest.Model.Family, SourceID: fixture.artifacts.Manifest.Model.SourceID,
		Revision: fixture.artifacts.Manifest.Model.Revision, SDKVersion: fixture.artifacts.Manifest.SDK.Version,
		Device: fixture.artifacts.Manifest.Model.Device, DType: fixture.artifacts.Manifest.Model.DType,
	}
	if err := validateCandidateResult(input, result, model, fixture.artifacts.PolicySHA256, testRouteLabels()); err != nil {
		t.Fatalf("fake child returned invalid result: %v", err)
	}
	ownedProcess := worker.cmd.Process
	worker.Close()
	select {
	case <-worker.done:
	default:
		t.Fatal("Close returned before reaping the owned child")
	}
	if ownedProcess == nil || ownedProcessStateMissing(worker) {
		t.Fatal("owned Python child did not have a completed process state")
	}

	timedWorker := newSDKWorker(fixture.config, fixture.artifacts, fakePythonChildScript(true), 30*time.Millisecond)
	if err := timedWorker.Start(context.Background()); err != nil {
		t.Fatalf("start timeout fake child: %v", err)
	}
	waitForWorkerReady(t, timedWorker)
	input.Message = "__sleep__"
	_, err = timedWorker.Classify(input)
	var callErr *workerCallError
	if !errors.As(err, &callErr) || callErr.reasonCode != "INFERENCE_TIMEOUT" {
		t.Fatalf("hard timeout error = %v, want INFERENCE_TIMEOUT", err)
	}
	select {
	case <-timedWorker.done:
	case <-time.After(time.Second):
		t.Fatal("timed-out child was not reaped")
	}
	if ready, _ := timedWorker.Ready(); ready {
		t.Fatal("worker remained ready after its child timed out")
	}
	timedWorker.Close()
}

func TestSDKWorkerRequiresChildOfflineAttestation(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "missing", mode: "missing"},
		{name: "false", mode: "false"},
		{name: "environment mismatch", mode: "environment-mismatch"},
		{name: "manifest mismatch", mode: "manifest-mismatch"},
		{name: "model not preloaded", mode: "preload-mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := newSDKWorker(fixture.config, fixture.artifacts, fakePythonChildScriptWithAttestation(false, test.mode, false), time.Second)
			if err := worker.Start(context.Background()); err != nil {
				t.Fatalf("start fake SDK child: %v", err)
			}
			select {
			case <-worker.readyDone:
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not finish readiness handshake")
			}
			if ready, reason := worker.Ready(); ready || reason != "SDK_ERROR" {
				t.Fatalf("worker readiness = %v, %q; want false, SDK_ERROR", ready, reason)
			}
			if worker.OfflineConfigurationVerified() {
				t.Fatal("worker retained offline attestation after rejecting ready frame")
			}
			worker.Close()
		})
	}
}

func TestSDKWorkerInvalidatesAttestationWhenChildExits(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	worker := newSDKWorker(fixture.config, fixture.artifacts, fakePythonChildScriptWithAttestation(false, "valid", true), time.Second)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start fake SDK child: %v", err)
	}
	waitForWorkerReady(t, worker)
	if !worker.OfflineConfigurationVerified() {
		t.Fatal("worker did not retain validated offline attestation while ready")
	}
	select {
	case <-worker.done:
	case <-time.After(3 * time.Second):
		t.Fatal("fake child did not exit")
	}
	if ready, _ := worker.Ready(); ready || worker.OfflineConfigurationVerified() {
		t.Fatal("worker readiness or offline attestation remained after child exit")
	}
	worker.Close()
}

func TestSDKWorkerActualChildRejectsMissingLocalModel(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	if err := os.RemoveAll(fixture.config.Value.ReferenceWorker.ModelDirectory); err != nil {
		t.Fatal(err)
	}
	worker := newSDKWorker(fixture.config, fixture.artifacts, sdkWorkerScript, time.Second)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start SDK worker with missing model: %v", err)
	}
	select {
	case <-worker.readyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not fail readiness with missing model")
	}
	if ready, _ := worker.Ready(); ready || worker.OfflineConfigurationVerified() {
		t.Fatal("missing local model produced ready/offline attestation")
	}
	select {
	case <-worker.done:
	case <-time.After(3 * time.Second):
		t.Fatal("child with missing local model was not reaped")
	}
	worker.Close()
}

func waitForWorkerReady(t *testing.T, worker *sdkWorker) {
	t.Helper()
	select {
	case <-worker.readyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("fake worker did not complete its ready handshake")
	}
	if ready, reason := worker.Ready(); !ready {
		t.Fatalf("fake worker is not ready: %s", reason)
	}
}

func ownedProcessStateMissing(worker *sdkWorker) bool {
	return worker.cmd.ProcessState == nil
}

func fakePythonChildScript(withSleep bool) string {
	return fakePythonChildScriptWithAttestation(withSleep, "valid", false)
}

func fakePythonChildScriptWithAttestation(withSleep bool, attestationMode string, exitAfterReady bool) string {
	sleepBody := ""
	if withSleep {
		sleepBody = "\n    if req['input']['message'] == '__sleep__': time.sleep(0.25)"
	}
	attestation := "'offline_configuration_verified':True, 'offline_attestation':{'offline_environment_checked_before_sdk_import':True,'environment':{'HF_HUB_OFFLINE':'1','TRANSFORMERS_OFFLINE':'1','HF_DATASETS_OFFLINE':'1','HF_HUB_DISABLE_TELEMETRY':'1','PYTHONNOUSERSITE':'1','PYTHONUTF8':'1','PYTHONIOENCODING':'utf-8','PYTHONDONTWRITEBYTECODE':'1','TOKENIZERS_PARALLELISM':'false'},'model_source':'prepared_local_directory','model_revision':model['revision'],'model_files_sha256':model['files_sha256'],'sdk_lock_sha256':m['sdk']['lock_sha256'],'preloaded_models':['multilingual']}"
	switch attestationMode {
	case "missing":
		attestation = ""
	case "false":
		attestation = strings.Replace(attestation, "'offline_configuration_verified':True", "'offline_configuration_verified':False", 1)
	case "environment-mismatch":
		attestation = strings.Replace(attestation, "'HF_HUB_OFFLINE':'1'", "'HF_HUB_OFFLINE':'0'", 1)
	case "manifest-mismatch":
		attestation = strings.Replace(attestation, "'model_revision':model['revision']", "'model_revision':'wrong-revision'", 1)
	case "preload-mismatch":
		attestation = strings.Replace(attestation, "'preloaded_models':['multilingual']", "'preloaded_models':[]", 1)
	}
	exitBody := ""
	if exitAfterReady {
		exitBody = "\ntime.sleep(0.1)\nsys.exit(0)"
	}
	return `import json, sys, time
cfg = json.loads(sys.stdin.readline())
m = cfg['manifest']
model = m['model']
ref = cfg['reference_worker']
ready = {'kind':'ready','model':{'family':model['family'],'source_id':model['source_id'],'revision':model['revision'],'sdk_version':m['sdk']['version'],'device':model['device'],'dtype':model['dtype']},'policy_sha256':m['policy_sha256'],'model_files_sha256':model['files_sha256'],'sdk_lock_sha256':m['sdk']['lock_sha256'],'effective_max_len':ref['max_len'],'effective_head_max_len':ref['head_max_len'],` + attestation + `}
print(json.dumps(ready,separators=(',',':')), flush=True)` + exitBody + `
for line in sys.stdin:
    req = json.loads(line)` + sleepBody + `
    value = req['input']
    probabilities = {'CHAT':1.0,'PLAN':0.0,'ANALYZE':0.0,'OPS':0.0,'RESEARCH':0.0,'CODE':0.0,'CODE1':0.0,'CODE2':0.0,'CODE3':0.0,'CODE4':0.0,'WILD':0.0}
    result = {'schema_version':value['schema_version'],'request_id':value['request_id'],'status':'classified','route':'CHAT','probabilities':probabilities,'selected_probability':1.0,'top_two_margin':1.0,'native_confidence':None,'probability_status':'upstream_default_not_validated_on_rencrow','model':{'family':model['family'],'source_id':model['source_id'],'revision':model['revision'],'sdk_version':m['sdk']['version'],'device':model['device'],'dtype':model['dtype']},'policy_sha256':value['policy_sha256'],'input':{'effective_sequence_tokens':12,'truncated':False},'timing_ms':{'prepare':0.1,'inference':2.0,'postprocess':0.1,'server_total':2.2},'reason_code':'OK'}
    print(json.dumps({'kind':'response','request_id':value['request_id'],'result':result},separators=(',',':')), flush=True)
`
}

func TestSDKWorkerPythonScriptCompilesWithoutInstallingPackages(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python interpreter is unavailable for syntax check")
	}
	command := exec.Command(python, "-c", "import sys; compile(sys.stdin.read(), 'sdk_worker.py', 'exec')")
	command.Env = offlineEnvironment(os.Environ())
	command.Stdin = strings.NewReader(sdkWorkerScript)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("compile Python worker source: %v: %s", err, output)
	}
}

func TestSDKWorkerChildChecksOfflineEnvironmentWithoutRewritingIt(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python interpreter is unavailable for offline environment check")
	}
	command := exec.Command(python, "-c", `import sys
namespace = {"__name__": "offline_attestation_test"}
exec(compile(sys.stdin.read(), "sdk_worker.py", "exec"), namespace)
try:
    namespace["verify_offline_environment"]()
except namespace["WorkerError"]:
    raise SystemExit(0)
raise SystemExit(1)`)
	command.Env = offlineEnvironment(os.Environ())
	for index, entry := range command.Env {
		if strings.HasPrefix(entry, "HF_HUB_OFFLINE=") {
			command.Env[index] = "HF_HUB_OFFLINE=0"
		}
	}
	command.Stdin = strings.NewReader(sdkWorkerScript)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("offline environment verification accepted a modified value: %v: %s", err, output)
	}
}

func TestValidateListenAddressRequiresLoopbackAndConfiguredPort(t *testing.T) {
	if _, err := validateListenAddress("0.0.0.0:18190", "http://127.0.0.1:18190"); err == nil {
		t.Fatal("non-loopback bind was accepted")
	}
	if _, err := validateListenAddress("127.0.0.1:18191", "http://127.0.0.1:18190"); err == nil {
		t.Fatal("listen/config port mismatch was accepted")
	}
	if got, err := validateListenAddress("127.0.0.1:18190", "http://127.0.0.1:18190"); err != nil || got != "127.0.0.1:18190" {
		t.Fatalf("valid loopback endpoint = %q, %v", got, err)
	}
}

func TestResolvedConfigRejectsIncompleteSpecSections(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	config := fixture.config.Value
	config.Evaluation.PerformanceConcurrency = nil
	if err := validateResolvedConfig(config); err == nil {
		t.Fatal("config with missing evaluation concurrency was accepted")
	}

	config = fixture.config.Value
	config.Recording.IncludeRawModelOutput = true
	if err := validateResolvedConfig(config); err == nil {
		t.Fatal("config enabling raw model output was accepted")
	}
}

func TestSDKWorkerKillsOwnedChildOnUnexpectedStdoutEOF(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	child := `import os, sys, time
sys.stdin.readline()
os.close(sys.stdout.fileno())
time.sleep(30)
`
	worker := newSDKWorker(fixture.config, fixture.artifacts, child, time.Second)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start EOF fake child: %v", err)
	}
	select {
	case <-worker.readyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not handle stdout EOF")
	}
	if ready, reason := worker.Ready(); ready || reason != "MODEL_NOT_READY" {
		t.Fatalf("worker ready state = %v, %q; want false, MODEL_NOT_READY", ready, reason)
	}
	select {
	case <-worker.done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker left owned child running after stdout EOF")
	}
	worker.Close()
}

func TestSDKWorkerStderrIsBoundedAndNeverForwardedToLogs(t *testing.T) {
	fixture := makeTestArtifacts(t, "verified")
	const secretMarker = "raw-message-secret-marker"
	child := `import os, sys, time
sys.stdin.readline()
sys.stderr.buffer.write(b"x" * 9000 + b"raw-message-secret-marker")
sys.stderr.flush()
os.close(sys.stdout.fileno())
time.sleep(30)
`
	worker := newSDKWorker(fixture.config, fixture.artifacts, child, time.Second)
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousWriter)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start stderr fake child: %v", err)
	}
	select {
	case <-worker.readyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not handle stderr fake child EOF")
	}
	select {
	case <-worker.done:
	case <-time.After(3 * time.Second):
		t.Fatal("stderr fake child remained running")
	}
	if retained := worker.stderr.snapshot(); len(retained) > childStderrLimit || !bytes.Contains(retained, []byte(secretMarker)) {
		t.Fatalf("stderr tail size/content invalid: size=%d", len(retained))
	}
	if strings.Contains(logs.String(), secretMarker) {
		t.Fatal("child stderr secret was forwarded to server logs")
	}
	worker.Close()
}
