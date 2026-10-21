package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	"gopkg.in/yaml.v3"
)

type fakePrepareBackend struct {
	createCalls   int
	installCalls  int
	downloadCalls int
	sdkVersion    string
	modelRevision string
	createErr     error
	installErr    error
	downloadErr   error
}

func (backend *fakePrepareBackend) CreateVenv(_ context.Context, _ string, directory string) (string, string, error) {
	backend.createCalls++
	if backend.createErr != nil {
		return "", "", backend.createErr
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	return filepath.Join(directory, "bin", "python"), "3.11.9", nil
}

func (backend *fakePrepareBackend) InstallSDK(_ context.Context, _ string, outputDirectory, version string) (map[string]string, []byte, error) {
	backend.installCalls++
	backend.sdkVersion = version
	if backend.installErr != nil {
		return nil, nil, backend.installErr
	}
	return map[string]string{
		"laya":            version,
		"huggingface-hub": "0.30.0",
		"torch":           "2.4.0",
	}, []byte("huggingface-hub==0.30.0 --hash=sha256:" + strings.Repeat("a", 64) + "\nlaya==" + version + " --hash=sha256:" + strings.Repeat("b", 64) + "\ntorch==2.4.0 --hash=sha256:" + strings.Repeat("c", 64) + "\n"), nil
}

func (backend *fakePrepareBackend) DownloadModel(_ context.Context, _ string, modelDirectory, revision string) error {
	backend.downloadCalls++
	backend.modelRevision = revision
	if backend.downloadErr != nil {
		return backend.downloadErr
	}
	if err := os.WriteFile(filepath.Join(modelDirectory, "config.json"), []byte(`{"model_type":"laya"}`), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(modelDirectory, "weights.safetensors"), []byte("synthetic test weights"), 0o600)
}

func TestPrepareCODE4SourceConflictNeverCallsAcquisitionBackend(t *testing.T) {
	root := createConflictingPolicyRepository(t)
	configPath := writeOffConfig(t)
	inspection, err := inspectRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	policy := inspection.Policy
	policy.SemanticReviewStatus = "verified"
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &fakePrepareBackend{}
	out := filepath.Join(t.TempDir(), "blocked")
	err = runPrepareWithBackend(configPath, out, root, prepareSourceArgs{
		PolicyPath: policyPath, LegacyPrompt: filepath.Join(root, "prompts", "classifier.md"), SDKVersion: "1.2.3",
	}, backend)
	if !hasCommandCode(err, "POLICY_UNDEFINED") {
		t.Fatalf("prepare error = %v", err)
	}
	if backend.createCalls != 0 || backend.installCalls != 0 || backend.downloadCalls != 0 {
		t.Fatalf("backend was called despite unresolved policy: %+v", backend)
	}
	var status prepareStatus
	readJSONFile(t, filepath.Join(out, "prepare_status.json"), &status)
	if status.Status != "BLOCKED" || status.OnlineAcquisitionAttempted || status.ModelInvoked {
		t.Fatalf("prepare status = %+v", status)
	}
}

func TestPrepareMissingPinsOnAlignedSourcesDoesNotAcquire(t *testing.T) {
	repo := createAlignedPolicyRepository(t)
	config := classifierlab.DefaultConfig()
	config.ExperimentID = "missing-pins-contract"
	config.Candidate.ExpectedModelRevision = strings.Repeat("a", 40)
	configData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &fakePrepareBackend{}
	out := filepath.Join(t.TempDir(), "blocked")
	err = runPrepareWithBackend(configPath, out, repo, prepareSourceArgs{}, backend)
	if !hasCommandCode(err, "PREPARE_PINS_REQUIRED") {
		t.Fatalf("prepare error = %v", err)
	}
	if backend.createCalls != 0 || backend.installCalls != 0 || backend.downloadCalls != 0 {
		t.Fatalf("backend was called without explicit pins: %+v", backend)
	}
	var status prepareStatus
	readJSONFile(t, filepath.Join(out, "prepare_status.json"), &status)
	if status.Status != "BLOCKED" || status.OnlineAcquisitionAttempted || status.ModelInvoked {
		t.Fatalf("prepare status = %+v", status)
	}
}

func TestPrepareFakeAcquisitionWritesLoadablePinnedArtifacts(t *testing.T) {
	repo := createAlignedPolicyRepository(t)
	inspection, err := inspectRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Review.Status != "SOURCE_ALIGNED_REVIEW_REQUIRED" {
		t.Fatalf("aligned source status = %q (%s)", inspection.Review.Status, inspection.Review.Conflict)
	}

	policy := inspection.Policy
	policy.SemanticReviewStatus = "verified"
	promptPath := filepath.Join(t.TempDir(), "workspace-prompt-override.md")
	promptBytes := []byte("CODE4: Fast prototype and experimental coding from the workspace override.\n")
	if err := os.WriteFile(promptPath, promptBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	policy.LegacyPromptSHA256 = sha256Hex(promptBytes)
	policyPath := filepath.Join(t.TempDir(), "verified-policy.json")
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	config := classifierlab.DefaultConfig()
	config.ExperimentID = "fake-prepare-contract"
	config.Candidate.ExpectedFamily = "REPLACE_WITH_PREPARED_FAMILY"
	config.Candidate.ExpectedModelRevision = strings.Repeat("a", 40)
	config.Candidate.ExpectedPolicySHA256 = "REPLACE_WITH_PREPARED_POLICY_SHA256"
	configBytes, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "input-config.yaml")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakePrepareBackend{}
	out := filepath.Join(t.TempDir(), "prepared")
	if err := runPrepareWithBackend(configPath, out, repo, prepareSourceArgs{
		PolicyPath: policyPath, LegacyPrompt: promptPath, SDKVersion: "1.2.3",
	}, backend); err != nil {
		t.Fatalf("prepare with fake acquisition backend: %v", err)
	}
	if backend.createCalls != 1 || backend.installCalls != 1 || backend.downloadCalls != 1 || backend.sdkVersion != "1.2.3" || backend.modelRevision != strings.Repeat("a", 40) {
		t.Fatalf("fake backend call record = %+v", backend)
	}
	afterConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeConfig) != string(afterConfig) {
		t.Fatal("prepare modified the caller's input config")
	}

	resolved, err := classifierlab.LoadConfig(filepath.Join(out, "resolved-config.yaml"))
	if err != nil {
		t.Fatalf("load resolved config: %v", err)
	}
	if resolved.Mode != classifierlab.ModeOff || resolved.Candidate.ExpectedFamily != "laya-multilingual" ||
		resolved.Candidate.ExpectedModelRevision != strings.Repeat("a", 40) || resolved.Candidate.ExpectedPolicySHA256 != sha256Hex(mustCanonicalJSON(t, policy)) ||
		!resolved.ReferenceWorker.OfflineRequired {
		t.Fatalf("resolved config lost fixed preparation values: %+v", resolved)
	}
	artifacts, err := classifierlab.LoadPreparedArtifacts(out, resolved.ReferenceWorker.ModelDirectory,
		resolved.Candidate.ExpectedFamily, resolved.Candidate.ExpectedModelRevision,
		resolved.ReferenceWorker.Device, resolved.ReferenceWorker.DType, resolved.Candidate.ExpectedPolicySHA256)
	if err != nil {
		t.Fatalf("load prepared artifacts: %v", err)
	}
	if artifacts.Manifest.SDK.Version != "1.2.3" || artifacts.Manifest.Model.SourceID != "convaiinnovations/laya-multilingual" {
		t.Fatalf("prepared pins = %+v", artifacts.Manifest)
	}
	if _, err := os.Stat(filepath.Join(out, "requirements.lock")); err != nil {
		t.Fatalf("dependency lock missing: %v", err)
	}
	var contract adapterContract
	readJSONFile(t, filepath.Join(out, "adapter_contract.json"), &contract)
	if contract.Status != "UNVERIFIED" || contract.Checks["router_predict_signature"] {
		t.Fatalf("prepare incorrectly verified the SDK adapter: %+v", contract)
	}
	for _, check := range []string{"offline_configuration_verified", "network_isolation_verified"} {
		if _, exists := contract.Checks[check]; !exists || contract.Checks[check] {
			t.Fatalf("prepared contract does not keep %q independently unverified: %+v", check, contract.Checks)
		}
	}
	if _, exists := contract.Checks["offline_startup"]; exists {
		t.Fatalf("prepared contract retains ambiguous offline_startup check: %+v", contract.Checks)
	}
	var experiment experimentManifest
	readJSONFile(t, filepath.Join(out, "experiment_manifest.json"), &experiment)
	if experiment.SuppliedLegacyPromptSHA256 != policy.LegacyPromptSHA256 || experiment.SuppliedLegacyPromptSHA256 == experiment.RepositoryPromptSHA256 ||
		experiment.SuppliedLegacyPromptPath != filepath.Clean(promptPath) || experiment.LegacyPromptRuntimeObservation != "NOT_OBSERVED" || experiment.LoadedPromptSHA256 != nil {
		t.Fatalf("prompt provenance falsely claims runtime observation: %+v", experiment)
	}
	if experiment.ModelFamily != "laya-multilingual" {
		t.Fatalf("experiment manifest model family = %q", experiment.ModelFamily)
	}
	var status prepareStatus
	readJSONFile(t, filepath.Join(out, "prepare_status.json"), &status)
	if status.Status != "PREPARED" || !status.OnlineAcquisitionAttempted || status.ModelInvoked || status.AdapterContractStatus != "UNVERIFIED" {
		t.Fatalf("prepare status = %+v", status)
	}
}

func TestPipInstallReportRequiresOfficialHashedArtifacts(t *testing.T) {
	sha := strings.Repeat("a", 64)
	valid := `{"install":[{"metadata":{"name":"laya","version":"1.2.3"},"download_info":{"url":"https://files.pythonhosted.org/packages/laya.whl","archive_info":{"hashes":{"sha256":"` + sha + `"}}}}]}`
	packages, hashes, err := parsePipInstallReport([]byte(valid))
	if err != nil {
		t.Fatalf("parse valid pip report: %v", err)
	}
	if packages["laya"] != "1.2.3" || hashes["laya"] != sha {
		t.Fatalf("parsed pip package pins = %v / %v", packages, hashes)
	}
	thirdParty := strings.Replace(valid, "files.pythonhosted.org", "example.invalid", 1)
	if _, _, err := parsePipInstallReport([]byte(thirdParty)); err == nil {
		t.Fatal("third-party package URL was accepted")
	}
	missingHash := strings.Replace(valid, `"hashes":{"sha256":"`+sha+`"}`, `"hashes":{}`, 1)
	if _, _, err := parsePipInstallReport([]byte(missingHash)); err == nil {
		t.Fatal("pip artifact without SHA-256 was accepted")
	}
}

func TestPrepareSubprocessEnvironmentDropsGlobalPythonAndHubOverrides(t *testing.T) {
	t.Setenv("PYTHONPATH", "/caller/python")
	t.Setenv("PIP_INDEX_URL", "https://user:secret@example.invalid/simple")
	t.Setenv("HF_TOKEN", "never-forward-this")
	environment := isolatedPythonEnvironment(t.TempDir())
	values := make(map[string]string, len(environment))
	for _, pair := range environment {
		key, value, found := strings.Cut(pair, "=")
		if found {
			values[key] = value
		}
	}
	for _, key := range []string{"PYTHONPATH", "PIP_INDEX_URL", "HF_TOKEN"} {
		if _, exists := values[key]; exists {
			t.Fatalf("private subprocess inherited %s", key)
		}
	}
	if values["PIP_CONFIG_FILE"] != os.DevNull || values["PIP_REQUIRE_VIRTUALENV"] != "1" ||
		values["PYTHONNOUSERSITE"] != "1" || values["HF_HUB_DISABLE_IMPLICIT_TOKEN"] != "1" {
		t.Fatalf("private Python environment lacks isolation settings: %#v", values)
	}
}

func createAlignedPolicyRepository(t *testing.T) string {
	t.Helper()
	return createCode4PolicyRepository(t, false)
}

func createConflictingPolicyRepository(t *testing.T) string {
	t.Helper()
	return createCode4PolicyRepository(t, true)
}

func createCode4PolicyRepository(t *testing.T, conflict bool) string {
	t.Helper()
	root := t.TempDir()
	code4Policy := "高速な試作・実験的coding。自然文classifier選択は未定義。比較レビューや仕上げのrouteではない。\n"
	if conflict {
		code4Policy = "複数案の比較と実装レビューを担当する。\n"
	}
	files := map[string]string{
		"internal/infrastructure/routing/llm_classifier.go":  syntheticParserSource(),
		"internal/domain/routing/route.go":                   "package routing\nconst RouteCODE4 = \"CODE4\" // 高速コーディング/実験（スロット4）\n",
		"internal/domain/agent/mio_persona.go":               "var commands = []command{{\"/code4\", routing.RouteCODE4}}\n",
		"internal/infrastructure/routing/rule_dictionary.go": "package routing\n// No dedicated CODE4 natural-language rule.\n",
		"rules/routing-policy.md":                            alignedRoutePolicySource(code4Policy),
		"prompts/classifier.md":                              "- CHAT: conversation\n- CODE3: review\n",
		"cmd/rencrow/runtime_dependencies.go":                "classifier, err = buildRuntimeClassifier(llmRuntime.Chat, cfg.Prompts.Classifier, classifierTestConfig)\n",
		"internal/adapter/config/prompts.go":                 "package config\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	runGit(t, root, "config", "user.name", "Test")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "synthetic policy source")
	return root
}

func alignedRoutePolicySource(code4Policy string) string {
	routes := []string{"CHAT", "PLAN", "ANALYZE", "OPS", "RESEARCH", "CODE", "CODE1", "CODE2", "CODE3", "CODE4", "WILD"}
	var builder strings.Builder
	for _, route := range routes {
		builder.WriteString("### " + route + "\n")
		if route == "CODE4" {
			builder.WriteString(code4Policy)
		} else {
			builder.WriteString(route + " synthetic criteria.\n")
		}
	}
	return builder.String()
}

func syntheticParserSource() string {
	return `package routing
var validRoutes = []struct{ key string; route routing.Route }{
 {"CODE4", routing.RouteCODE4}, {"CODE3", routing.RouteCODE3}, {"CODE2", routing.RouteCODE2},
 {"CODE1", routing.RouteCODE1}, {"CODE", routing.RouteCODE}, {"ANALYZE", routing.RouteANALYZE},
 {"RESEARCH", routing.RouteRESEARCH}, {"WILD", routing.RouteWILD}, {"PLAN", routing.RoutePLAN},
 {"CHAT", routing.RouteCHAT}, {"OPS", routing.RouteOPS},
}
`
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", arguments, err, output)
	}
}

func mustCanonicalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
