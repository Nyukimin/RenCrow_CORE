package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	"gopkg.in/yaml.v3"
)

func TestInspectWritesPrivateBaselineAndUnverifiedPolicy(t *testing.T) {
	root := findCoreRoot(t)
	config := writeOffConfig(t)
	out := filepath.Join(t.TempDir(), "inspect")
	if err := execute([]string{"inspect", "--config", config, "--out", out, "--repo", root}); err != nil {
		t.Fatal(err)
	}
	var baseline map[string]any
	readJSONFile(t, filepath.Join(out, "baseline_manifest.json"), &baseline)
	if baseline["loaded_prompt_sha256"] != nil || baseline["loaded_prompt_status"] != "UNOBSERVED_WORKSPACE_OVERRIDE_POSSIBLE" {
		t.Fatalf("loaded prompt observation = %#v / %#v", baseline["loaded_prompt_sha256"], baseline["loaded_prompt_status"])
	}
	if baseline["working_tree_status_sha256"] == "" || baseline["working_tree_fingerprint_scope"] == "" {
		t.Fatalf("working tree fingerprint is incomplete: %#v", baseline)
	}
	var policy classifierlab.RoutePolicy
	readJSONFile(t, filepath.Join(out, classifierlab.PreparedPolicyFileName), &policy)
	if policy.SemanticReviewStatus != "SOURCE_ALIGNED_REVIEW_REQUIRED" {
		t.Fatalf("inspection policy status = %q", policy.SemanticReviewStatus)
	}
	var baselineEvidence inspectBaselineEvidence
	readJSONFile(t, filepath.Join(out, "baseline_manifest.json"), &baselineEvidence)
	if !containsString(baselineEvidence.ClassifierParserLabels, "CODE4") || !containsString(baselineEvidence.ClassifierParserLabels, "WILD") ||
		containsString(baselineEvidence.RepositoryPromptLabels, "CODE4") || containsString(baselineEvidence.RepositoryPromptLabels, "WILD") {
		t.Fatalf("inspection did not preserve the separate parser/prompt inventories: parser=%v prompt=%v", baselineEvidence.ClassifierParserLabels, baselineEvidence.RepositoryPromptLabels)
	}
	if baselineEvidence.Code4Reachability.ExplicitCommandStatus != "REACHABLE" ||
		baselineEvidence.Code4Reachability.RuleDictionaryStatus != "NO_DEDICATED_RULE" ||
		baselineEvidence.Code4Reachability.NaturalLanguageAutomaticSelectionStatus != "UNDEFINED" {
		t.Fatalf("inspection CODE4 reachability evidence = %+v", baselineEvidence.Code4Reachability)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("output directory mode = %v, err=%v", info, err)
	}
	if info, err := os.Stat(filepath.Join(out, "baseline_manifest.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("baseline file mode = %v, err=%v", info, err)
	}
	if err := execute([]string{"inspect", "--config", config, "--out", out, "--repo", root}); err == nil {
		t.Fatal("inspect overwrote an existing output directory")
	}
}

func TestInspectAcceptsCompleteModeOffExampleConfig(t *testing.T) {
	root := findCoreRoot(t)
	config := filepath.Join(root, "testdata", "routing_classifier", "config.yaml.example")
	if _, err := loadExperimentConfig(config); err != nil {
		t.Fatalf("load example config: %v", err)
	}
	out := filepath.Join(t.TempDir(), "inspect-example")
	if err := execute([]string{"inspect", "--config", config, "--out", out, "--repo", root}); err != nil {
		t.Fatalf("inspect example config: %v", err)
	}
	var policy classifierlab.RoutePolicy
	readJSONFile(t, filepath.Join(out, classifierlab.PreparedPolicyFileName), &policy)
	if policy.SemanticReviewStatus != "SOURCE_ALIGNED_REVIEW_REQUIRED" {
		t.Fatalf("example policy status = %q", policy.SemanticReviewStatus)
	}
}

func TestValidatePreparedReplayEvidenceBindsPolicyModelSDKAndLock(t *testing.T) {
	root := t.TempDir()
	policy := classifierlab.RoutePolicy{
		PolicyVersion: "rencrow-route-policy-v1", Routes: []string{"code"},
		Instructions: "verified test policy", Criteria: map[string]string{"code": "test"},
		SourcePaths: []string{"rules/routing-policy.md"}, LegacyPromptSHA256: strings.Repeat("a", 64),
		SemanticReviewStatus: "verified",
	}
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policySHA := testSHA256(policyBytes)
	if err := os.WriteFile(filepath.Join(root, classifierlab.PreparedPolicyFileName), policyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	lock := []byte("laya==0.1.0\n")
	lockSHA := testSHA256(lock)
	if err := os.Mkdir(filepath.Join(root, "sdk"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sdk", "requirements.lock"), lock, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared := classifierlab.PreparedManifest{
		SchemaVersion: classifierlab.PreparedManifestSchemaVersion, PolicySHA256: policySHA,
		Model: classifierlab.PreparedModelManifest{
			Family: "laya-multilingual", SourceID: "convaiinnovations/laya-multilingual",
			Revision: strings.Repeat("b", 40), Device: "cpu", DType: "float32", FilesSHA256: strings.Repeat("c", 64),
		},
		SDK: classifierlab.PreparedSDKManifest{
			Distribution: "laya", Version: "0.1.0", SourceURL: "https://github.com/NandhaKishorM/laya",
			LockFile: "sdk/requirements.lock", LockSHA256: lockSHA,
		},
	}
	preparedBytes, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, classifierlab.PreparedManifestFileName), preparedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	version, sdkLockSHA := "0.1.0", lockSHA
	experiment := experimentManifest{
		PreparedManifestSHA256: testSHA256(preparedBytes), ModelFilesSHA256: prepared.Model.FilesSHA256,
		PolicySHA256: policySHA, PolicyStatus: "verified", ModelFamily: prepared.Model.Family,
		ModelRevision: prepared.Model.Revision, Device: prepared.Model.Device, DType: prepared.Model.DType,
		SDKVersion: &version, SDKLockSHA256: &sdkLockSHA,
	}
	if err := validatePreparedReplayEvidence(root, experiment); err != nil {
		t.Fatalf("valid prepared replay evidence rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sdk", "requirements.lock"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedReplayEvidence(root, experiment); err == nil {
		t.Fatal("changed SDK lock was accepted")
	}
	if _, err := preparedEvidencePath(root, "../outside.lock"); err == nil {
		t.Fatal("prepared lock path traversal was accepted")
	}
}

func testSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestPrepareSmokeReplayAndE2EFailClosedWithStatusArtifacts(t *testing.T) {
	root := findCoreRoot(t)
	config := writeOffConfig(t)
	parent := t.TempDir()

	prepareOut := filepath.Join(parent, "prepare")
	prepareBlockCode := sourceStatusBlockCode(t, root, "PREPARE_PINS_REQUIRED")
	if err := execute([]string{"prepare", "--config", config, "--out", prepareOut, "--repo", root}); !hasCommandCode(err, prepareBlockCode) {
		t.Fatalf("prepare error = %v", err)
	}
	var prepareStatus blockedStatus
	readJSONFile(t, filepath.Join(prepareOut, "prepare_status.json"), &prepareStatus)
	if prepareStatus.Status != "BLOCKED" || prepareStatus.OnlineAcquisitionAttempted || prepareStatus.ModelInvoked {
		t.Fatalf("prepare status = %+v", prepareStatus)
	}
	var contract adapterContract
	readJSONFile(t, filepath.Join(prepareOut, "adapter_contract.json"), &contract)
	if contract.Status != "UNVERIFIED" || contract.SDKVersion != nil || contract.LockSHA256 != nil {
		t.Fatalf("adapter contract claims unverified SDK details: %+v", contract)
	}
	for check, passed := range contract.Checks {
		if passed {
			t.Fatalf("adapter contract check %q was marked passed before SDK smoke", check)
		}
	}
	for _, check := range []string{"offline_configuration_verified", "network_isolation_verified"} {
		if _, exists := contract.Checks[check]; !exists {
			t.Fatalf("adapter contract omits distinct isolation check %q: %+v", check, contract.Checks)
		}
	}
	if _, exists := contract.Checks["offline_startup"]; exists {
		t.Fatalf("adapter contract retains ambiguous offline_startup check: %+v", contract.Checks)
	}

	smokeOut := filepath.Join(parent, "smoke")
	fixtures := filepath.Join(root, "testdata", "routing_classifier", "smoke.jsonl")
	smokeBlockCode := sourceStatusBlockCode(t, root, "PREPARED_CONFIG_REQUIRED")
	if err := execute([]string{"smoke", "--config", config, "--fixtures", fixtures, "--out", smokeOut, "--repo", root}); !hasCommandCode(err, smokeBlockCode) {
		t.Fatalf("smoke error = %v", err)
	}
	var blockedSmokeStatus blockedStatus
	readJSONFile(t, filepath.Join(smokeOut, "smoke_status.json"), &blockedSmokeStatus)
	if blockedSmokeStatus.Status != "NOT_RUN" || blockedSmokeStatus.ModelInvoked || blockedSmokeStatus.CaseCount != 5 {
		t.Fatalf("smoke status = %+v", blockedSmokeStatus)
	}
	var smokeStatusEvidence smokeStatus
	readJSONFile(t, filepath.Join(smokeOut, "smoke_status.json"), &smokeStatusEvidence)
	if smokeStatusEvidence.ReasonCode != smokeBlockCode || smokeStatusEvidence.ExternalNetworkAllowed != nil ||
		smokeStatusEvidence.AdapterContractStatus != "UNVERIFIED" || smokeStatusEvidence.ServerReady ||
		smokeStatusEvidence.ClassifyRequestsAttempted != 0 || smokeStatusEvidence.OnlineAcquisitionAttempted {
		t.Fatalf("blocked smoke contacted a server or claimed adapter evidence: %+v", smokeStatusEvidence)
	}
	if _, err := os.Stat(filepath.Join(smokeOut, "smoke_results.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked smoke wrote classification results: %v", err)
	}

	dataset := filepath.Join(root, "testdata", "routing_classifier", "synthetic_corpus.jsonl")
	replayOut := filepath.Join(parent, "replay")
	replayBlockCode := sourceStatusBlockCode(t, root, "PREPARED_CONFIG_REQUIRED")
	if err := execute([]string{"replay", "--config", config, "--dataset", dataset, "--split", "test", "--variants", "legacy,candidate,rules-only", "--out", replayOut, "--repo", root}); !hasCommandCode(err, replayBlockCode) {
		t.Fatalf("replay error = %v", err)
	}
	var run runManifest
	readJSONFile(t, filepath.Join(replayOut, "run_manifest.json"), &run)
	if run.Status != "PARTIAL" || run.ModelInvoked || run.PolicyStatus != inspectionPolicyStatus(t, root) || run.CaseCount != 22 ||
		run.VariantStatuses["rules-only"].Status != "COMPLETED" || run.VariantStatuses["candidate"].ReasonCode != replayBlockCode ||
		run.VariantStatuses["legacy"].Status != "UNAVAILABLE" {
		t.Fatalf("run manifest = %+v", run)
	}
	replayRows, err := classifierlab.LoadReplayPredictions(filepath.Join(replayOut, "predictions.jsonl"))
	if err != nil || len(replayRows) != 22 {
		t.Fatalf("rules-only partial replay rows = %d, err=%v", len(replayRows), err)
	}

	e2eOut := filepath.Join(parent, "e2e")
	if err := execute([]string{"e2e", "--config", config, "--dataset", dataset, "--isolated", "--out", e2eOut, "--repo", root}); !hasCommandCode(err, "ISOLATION_ADAPTER_UNAVAILABLE") {
		t.Fatalf("e2e error = %v", err)
	}
	var e2eStatus blockedStatus
	readJSONFile(t, filepath.Join(e2eOut, "e2e_status.json"), &e2eStatus)
	if e2eStatus.Status != "BLOCKED" || !e2eStatus.IsolatedRequested || e2eStatus.ModelInvoked {
		t.Fatalf("e2e status = %+v", e2eStatus)
	}

	reportOut := filepath.Join(parent, "report")
	if err := execute([]string{"report", "--run", replayOut, "--e2e", e2eOut, "--out", reportOut}); err != nil {
		t.Fatalf("report error = %v", err)
	}
	var report reportMetrics
	readJSONFile(t, filepath.Join(reportOut, "metrics.json"), &report)
	if report.Status != "INCONCLUSIVE" || report.ModelDecision != "INCONCLUSIVE" || report.E2EStatus != "INCONCLUSIVE" || report.AdapterContractStatus != "UNVERIFIED" {
		t.Fatalf("report status = %+v", report)
	}
	if report.PerformanceEvidence.TTFTStatus != "UNAVAILABLE" ||
		report.PerformanceEvidence.FinalResponseStatus != "UNAVAILABLE" ||
		report.PerformanceEvidence.SharedResourceStatus != "UNAVAILABLE" ||
		report.PerformanceEvidence.RepeatedMeasurementStatus != "UNAVAILABLE" {
		t.Fatalf("report did not label missing adoption measurements: %+v", report.PerformanceEvidence)
	}
	for _, issue := range missingPerformanceEvidenceIssues(report.PerformanceEvidence) {
		found := false
		for _, actual := range report.Issues {
			if actual == issue {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("report omitted missing measurement issue %q: %v", issue, report.Issues)
		}
	}
	reportMarkdown, err := os.ReadFile(filepath.Join(reportOut, "report.md"))
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	if !strings.Contains(string(reportMarkdown), "Paired TTFT: UNAVAILABLE") ||
		!strings.Contains(string(reportMarkdown), "paired remeasurement evidence is unavailable or unverified") {
		t.Fatalf("report.md omits missing adoption evidence: %s", reportMarkdown)
	}
}

func TestReplayRulesOnlyProducesMioPredictionsWithoutModelInvocation(t *testing.T) {
	root := findCoreRoot(t)
	config := writeOffConfig(t)
	dataset := filepath.Join(root, "testdata", "routing_classifier", "synthetic_corpus.jsonl")
	out := filepath.Join(t.TempDir(), "rules-only")
	if err := execute([]string{"replay", "--config", config, "--dataset", dataset, "--split", "test", "--variants", "rules-only", "--out", out, "--repo", root}); err != nil {
		t.Fatalf("rules-only replay: %v", err)
	}
	var manifest runManifest
	readJSONFile(t, filepath.Join(out, "run_manifest.json"), &manifest)
	if manifest.Status != "COMPLETED" || manifest.ModelInvoked || manifest.CaseCount != 22 {
		t.Fatalf("rules-only run manifest = %+v", manifest)
	}
	predictions, err := classifierlab.LoadReplayPredictions(filepath.Join(out, "predictions.jsonl"))
	if err != nil {
		t.Fatalf("load rules-only predictions: %v", err)
	}
	if len(predictions) != manifest.CaseCount {
		t.Fatalf("prediction count = %d, want %d", len(predictions), manifest.CaseCount)
	}
	content, err := os.ReadFile(filepath.Join(out, "predictions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "synthetic_message_") || strings.Contains(string(content), `"message"`) {
		t.Fatal("prediction output contains source message content")
	}
	classifierReachedCases := 0
	for _, prediction := range predictions {
		if prediction.Variant != "rules-only" || prediction.FinalRoute == nil || prediction.RuleMatched == nil || prediction.ClassifierReached == nil || prediction.LegacyClassifiedRoute != nil || prediction.CandidateSelectedRoute != nil {
			t.Fatalf("rules-only prediction is incomplete or misattributed: %+v", prediction)
		}
		if *prediction.ClassifierReached {
			classifierReachedCases++
		}
	}
	if classifierReachedCases == 0 {
		t.Fatal("real Mio replay did not exercise any classifier-reaching cases")
	}
	if got := manifest.VariantStatuses["rules-only"].ClassifierReachableCases; got != classifierReachedCases {
		t.Fatalf("rules-only classifier reachable count = %d, prediction flags contain %d", got, classifierReachedCases)
	}
}

func TestReplayReconstructsAllowlistedTurnFixtureMetadata(t *testing.T) {
	turn, err := replayTurnInput(classifierlab.ReplayCase{
		CaseID: "case-1", Message: "synthetic message",
		TurnFixture: map[string]json.RawMessage{
			"session_id": []byte(`"session-1"`), "viewer_recipient": []byte(`"viewer-1"`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.SessionID() != "session-1" || turn.ViewerRecipient() != "viewer-1" {
		t.Fatalf("reconstructed turn metadata = session %q, viewer %q", turn.SessionID(), turn.ViewerRecipient())
	}
}

func TestReportAdoptionGateRequiresMeasuredPerformanceEvidence(t *testing.T) {
	zero := 0
	lower, legacyP95, candidateP95 := 0.0, 10.0, 5.0
	macro := 0.9
	base := reportMetrics{
		Variants: map[string]classifierlab.VariantMetrics{
			"legacy":    {MacroF1: &macro, Latencies: map[string]classifierlab.LatencySummary{"legacy_classify_wall_ms": {P95MS: &legacyP95}}},
			"candidate": {MacroF1: &macro, Latencies: map[string]classifierlab.LatencySummary{"candidate_call_wall_ms": {P95MS: &candidateP95}}},
		},
		Bootstrap: &classifierlab.PairedBootstrapInterval{Status: "COMPUTED", Lower95: &lower},
		E2EStatus: "PASS", E2ESideEffectCount: &zero, AdapterContractStatus: "VERIFIED",
		LegacyModelUnknown: false, LoadedPromptObserved: true,
		VerifiedRealConversationCases: map[string]int{"legacy": 11, "candidate": 11},
		MissingVerifiedRouteSupport:   map[string][]string{"legacy": {}, "candidate": {}},
	}
	if reportCanPass(base, 0.01, 0) {
		t.Fatal("classification metrics and latency alone must not pass the adoption gate")
	}
	issues := missingPerformanceEvidenceIssues(base.PerformanceEvidence)
	containsIssue := func(want string) bool {
		for _, issue := range issues {
			if issue == want {
				return true
			}
		}
		return false
	}
	for _, expected := range []string{
		"paired TTFT evidence is unavailable or unverified",
		"paired final-response latency evidence is unavailable or unverified",
		"shared-resource contention measurements are unavailable or unverified",
		"paired remeasurement evidence is unavailable or unverified",
	} {
		if !containsIssue(expected) {
			t.Fatalf("missing performance issue %q not reported in %v", expected, issues)
		}
	}
	for _, evidence := range []reportPerformanceEvidence{
		{TTFTStatus: "VERIFIED"},
		{FinalResponseStatus: "VERIFIED"},
		{SharedResourceStatus: "VERIFIED"},
		{RepeatedMeasurementStatus: "VERIFIED"},
	} {
		if reportCanPass(reportMetrics{
			Variants: base.Variants, Bootstrap: base.Bootstrap, E2EStatus: base.E2EStatus,
			E2ESideEffectCount: base.E2ESideEffectCount, AdapterContractStatus: base.AdapterContractStatus,
			LoadedPromptObserved: base.LoadedPromptObserved, LegacyModelUnknown: base.LegacyModelUnknown,
			VerifiedRealConversationCases: base.VerifiedRealConversationCases,
			MissingVerifiedRouteSupport:   base.MissingVerifiedRouteSupport,
			PerformanceEvidence:           evidence,
		}, 0.01, 0) {
			t.Fatal("partial performance evidence must not pass the adoption gate")
		}
	}
}

func TestReadAdapterContractRejectsUnsubstantiatedVerifiedStatus(t *testing.T) {
	root := t.TempDir()
	required := map[string]bool{
		"pinned_sdk_version": true, "router_predict_signature": true,
		"multilingual_checkpoint_fixed": true, "choice_output_and_probabilities": true,
		"token_truncation_metadata": true, "offline_configuration_verified": true,
		"network_isolation_verified": true,
	}
	contract := adapterContract{Status: "VERIFIED", Checks: required}
	if err := writeJSONExclusive(filepath.Join(root, "adapter_contract.json"), contract); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdapterContract(root, experimentManifest{}); err == nil {
		t.Fatal("status string and true checks without prepared and smoke evidence were accepted")
	}

	delete(contract.Checks, "network_isolation_verified")
	if err := replaceJSON(filepath.Join(root, "adapter_contract.json"), contract); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdapterContract(root, experimentManifest{}); err == nil {
		t.Fatal("missing network isolation check was accepted for a VERIFIED adapter")
	}

	contract.Checks["network_isolation_verified"] = false
	if err := replaceJSON(filepath.Join(root, "adapter_contract.json"), contract); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdapterContract(root, experimentManifest{}); err == nil {
		t.Fatal("false network isolation check was accepted for a VERIFIED adapter")
	}

	contract.Status = "UNVERIFIED"
	if err := replaceJSON(filepath.Join(root, "adapter_contract.json"), contract); err != nil {
		t.Fatal(err)
	}
	if loaded, err := readAdapterContract(root, experimentManifest{}); err != nil || loaded.Status != "UNVERIFIED" {
		t.Fatalf("unverified adapter contract should remain inspectable: status=%q err=%v", loaded.Status, err)
	}
}

func TestStrictFixtureParserAndVariantValidation(t *testing.T) {
	if _, err := parseVariants("legacy,legacy"); err == nil {
		t.Fatal("duplicate variants were accepted")
	}
	if _, err := parseVariants("candidate,unknown"); err == nil {
		t.Fatal("unknown variant was accepted")
	}
	if _, err := loadSmokeFixtures(filepath.Join(findCoreRoot(t), "testdata", "routing_classifier", "smoke.jsonl")); err != nil {
		t.Fatalf("load smoke fixtures: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(bad, []byte(`{"id":"x","id":"y","language":"ja","message":"fixture","boundary":"normal"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSmokeFixtures(bad); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate fixture field error = %v", err)
	}
}

func TestExperimentManifestRejectsContentTampering(t *testing.T) {
	root := findCoreRoot(t)
	config := writeOffConfig(t)
	out := filepath.Join(t.TempDir(), "prepare")
	if err := execute([]string{"prepare", "--config", config, "--out", out, "--repo", root}); !hasCommandCode(err, sourceStatusBlockCode(t, root, "PREPARE_PINS_REQUIRED")) {
		t.Fatalf("prepare error = %v", err)
	}
	if _, err := readExperimentManifest(out); err != nil {
		t.Fatalf("read intact manifest: %v", err)
	}
	path := filepath.Join(out, "experiment_manifest.json")
	var manifest experimentManifest
	readJSONFile(t, path, &manifest)
	manifest.ModelRevision += "-tampered"
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExperimentManifest(out); err == nil || !strings.Contains(err.Error(), "manifest ID") {
		t.Fatalf("tampered manifest error = %v", err)
	}
}

func TestE2EStatusMustBindToSameExperimentAndDataset(t *testing.T) {
	sourceRoot := findCoreRoot(t)
	root := stableInspectionRepo(t, sourceRoot)
	config := writeOffConfig(t)
	dataset := filepath.Join(sourceRoot, "testdata", "routing_classifier", "synthetic_corpus.jsonl")
	parent := t.TempDir()
	replayOut := filepath.Join(parent, "replay")
	if err := execute([]string{"replay", "--config", config, "--dataset", dataset, "--split", "test", "--variants", "legacy,candidate", "--out", replayOut, "--repo", root}); !hasCommandCode(err, sourceStatusBlockCode(t, root, "PREPARED_CONFIG_REQUIRED")) {
		t.Fatalf("replay error = %v", err)
	}
	replayExperiment, err := readExperimentManifest(replayOut)
	if err != nil {
		t.Fatalf("read replay manifest: %v", err)
	}
	e2eOut := filepath.Join(parent, "e2e")
	if err := execute([]string{"e2e", "--config", config, "--dataset", dataset, "--isolated", "--out", e2eOut, "--repo", root}); !hasCommandCode(err, "ISOLATION_ADAPTER_UNAVAILABLE") {
		t.Fatalf("e2e error = %v", err)
	}
	e2eExperiment, err := readExperimentManifest(e2eOut)
	if err != nil {
		t.Fatalf("read e2e manifest: %v", err)
	}
	zero := 0
	e2eStatus := blockedStatus{
		SchemaVersion: classifierlab.SchemaVersion, Status: "PASS", ManifestID: e2eExperiment.ManifestID,
		ExperimentID: e2eExperiment.ExperimentID, DatasetSHA256: e2eExperiment.DatasetSHA256,
		IsolatedRequested: true, IsolationVerified: true, SideEffectCount: &zero,
	}
	writeStatus := func() {
		t.Helper()
		data, err := json.Marshal(e2eStatus)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e2eOut, "e2e_status.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeStatus()
	status, err := readE2EStatus(e2eOut, replayExperiment, nil)
	if err != nil || status.Status != "INCONCLUSIVE" {
		t.Fatalf("matching E2E status = %+v, err=%v", status, err)
	}
	e2eStatus.DatasetSHA256 = strings.Repeat("0", 64)
	writeStatus()
	if _, err := readE2EStatus(e2eOut, replayExperiment, nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched E2E dataset error = %v", err)
	}
}

func inspectionPolicyStatus(t *testing.T, root string) string {
	t.Helper()
	inspection, err := inspectRepository(root)
	if err != nil {
		t.Fatalf("inspect policy sources: %v", err)
	}
	return inspection.Review.Status
}

func sourceStatusBlockCode(t *testing.T, root, alignedBlockCode string) string {
	t.Helper()
	switch status := inspectionPolicyStatus(t, root); status {
	case "POLICY_UNDEFINED":
		return "POLICY_UNDEFINED"
	case "SOURCE_ALIGNED_REVIEW_REQUIRED":
		return alignedBlockCode
	default:
		t.Fatalf("unexpected inspection policy status %q", status)
		return ""
	}
}

func findCoreRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	return root
}

// stableInspectionRepo copies the source files read by inspectRepository into a
// committed temporary repository. Replay and E2E provenance includes the git
// status and untracked-source hashes, so two invocations against the live CORE
// worktree can disagree when unrelated tests create source files in parallel.
func stableInspectionRepo(t *testing.T, sourceRoot string) string {
	t.Helper()
	root := t.TempDir()
	paths := []string{
		"internal/infrastructure/routing/llm_classifier.go",
		"internal/domain/routing/route.go",
		"internal/domain/agent/mio_persona.go",
		"internal/infrastructure/routing/rule_dictionary.go",
		"rules/routing-policy.md",
		"prompts/classifier.md",
		"cmd/rencrow/runtime_dependencies.go",
		"internal/adapter/config/prompts.go",
	}
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read inspected source %s: %v", path, err)
		}
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/Nyukimin/RenCrow_CORE\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"init"},
		{"add", "."},
		{"-c", "user.name=classifier-test", "-c", "user.email=classifier-test@example.invalid", "commit", "-m", "stable inspection fixture"},
	}
	for _, arguments := range commands {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	return root
}

func writeOffConfig(t *testing.T) string {
	t.Helper()
	config := classifierlab.DefaultConfig()
	config.ExperimentID = "synthetic-routing-classifier-test"
	data, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "off.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func hasCommandCode(err error, code string) bool {
	var target commandError
	return errors.As(err, &target) && target.Code == code
}
