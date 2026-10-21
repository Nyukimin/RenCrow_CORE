package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	"gopkg.in/yaml.v3"
)

func TestInspectLegacyBaselineIsOptInAndBodyFree(t *testing.T) {
	const apiKeyEnv = "RENCROW_INSPECT_LEGACY_TEST_API_KEY"
	const apiKeyValue = "inspect-secret-value-must-not-be-recorded"
	const workspacePrompt = "workspace classifier body must not be recorded\n"
	t.Setenv(apiKeyEnv, apiKeyValue)

	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolve CORE root: %v", err)
	}
	experimentConfig := writeInspectExperimentConfig(t)

	t.Run("without explicit CORE config remains unobserved", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "inspect-unobserved")
		if err := execute([]string{"inspect", "--config", experimentConfig, "--out", out, "--repo", root}); err != nil {
			t.Fatalf("inspect without CORE config: %v", err)
		}
		var evidence inspectBaselineEvidence
		readJSONFile(t, filepath.Join(out, "baseline_manifest.json"), &evidence)
		if evidence.LegacyBaseline != nil || evidence.LegacyBaselineStatus != "UNOBSERVED_CORE_CONFIG_NOT_SUPPLIED" ||
			evidence.LoadedPromptSHA256 != nil || evidence.LoadedPromptStatus != "UNOBSERVED_WORKSPACE_OVERRIDE_POSSIBLE" {
			t.Fatalf("baseline without CORE config = %+v", evidence)
		}
		if !evidence.LegacyModelUnknown || evidence.LegacyPhysicalModelStatus != "UNKNOWN_NOT_OBSERVED" || evidence.LegacyProviderInvoked {
			t.Fatalf("unknown model/provider state = %+v", evidence)
		}
	})

	t.Run("explicit CORE config resolves effective prompt identity only", func(t *testing.T) {
		coreConfig := writeInspectCoreConfig(t, apiKeyEnv, workspacePrompt)
		out := filepath.Join(t.TempDir(), "inspect-resolved")
		if err := execute([]string{
			"inspect", "--config", experimentConfig, "--core-config", coreConfig,
			"--out", out, "--repo", root,
		}); err != nil {
			t.Fatalf("inspect with explicit CORE config: %v", err)
		}
		baselinePath := filepath.Join(out, "baseline_manifest.json")
		var evidence inspectBaselineEvidence
		readJSONFile(t, baselinePath, &evidence)
		if evidence.LegacyBaseline == nil {
			t.Fatal("resolved legacy baseline is missing from evidence")
		}
		resolved := evidence.LegacyBaseline
		if evidence.LegacyBaselineStatus != "CONFIG_RESOLVED_PROVIDER_NOT_CALLED" ||
			evidence.LoadedPromptSHA256 != nil || evidence.LoadedPromptStatus != "CONFIG_RESOLVED_NOT_RUNTIME_OBSERVED" {
			t.Fatalf("config/runtime observation state = %+v", evidence)
		}
		if resolved.ClassifierPromptSHA256 != inspectSHA256([]byte(strings.TrimSpace(workspacePrompt))) ||
			resolved.ManifestID == "" || resolved.ManifestID != resolved.CalculatedManifestID() ||
			resolved.ConfigSHA256 == "" || !resolved.LegacyModelUnknown {
			t.Fatalf("resolved baseline identity = %+v", resolved)
		}
		if resolved.AuthMode != "env" || resolved.APIKeyEnv != apiKeyEnv || resolved.GatewayURL != "http://127.0.0.1:8090" {
			t.Fatalf("resolved baseline fields = %+v", resolved)
		}
		if !evidence.LegacyModelUnknown || evidence.LegacyPhysicalModelStatus != "UNKNOWN_NOT_OBSERVED" || evidence.LegacyProviderInvoked {
			t.Fatalf("config resolution incorrectly observed runtime model/provider = %+v", evidence)
		}
		encoded, err := os.ReadFile(baselinePath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), apiKeyValue) || strings.Contains(string(encoded), strings.TrimSpace(workspacePrompt)) {
			t.Fatal("baseline evidence contains secret value or prompt body")
		}
	})
}

type inspectBaselineEvidence struct {
	LoadedPromptSHA256        *string                       `json:"loaded_prompt_sha256"`
	LoadedPromptStatus        string                        `json:"loaded_prompt_status"`
	ClassifierParserLabels    []string                      `json:"classifier_parser_accepted_labels"`
	RepositoryPromptLabels    []string                      `json:"repository_classifier_prompt_labels"`
	Code4Reachability         code4ReachabilityEvidence     `json:"code4_reachability"`
	LegacyModelUnknown        bool                          `json:"legacy_model_unknown"`
	LegacyBaselineStatus      string                        `json:"legacy_baseline_status"`
	LegacyBaseline            *classifierlab.LegacyBaseline `json:"legacy_baseline"`
	LegacyPhysicalModelStatus string                        `json:"legacy_physical_model_status"`
	LegacyProviderInvoked     bool                          `json:"legacy_provider_invoked"`
}

func TestInspectSeparatesParserPromptAndCODE4Reachability(t *testing.T) {
	root := findCoreRoot(t)
	inspection, err := inspectRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Review.Status != "SOURCE_ALIGNED_REVIEW_REQUIRED" {
		t.Fatalf("source status = %q (%s)", inspection.Review.Status, inspection.Review.Conflict)
	}
	wantParserLabels := []string{"CODE4", "CODE3", "CODE2", "CODE1", "CODE", "ANALYZE", "RESEARCH", "WILD", "PLAN", "CHAT", "OPS"}
	if !equalStringSlices(inspection.Baseline.ClassifierParserAcceptedLabels, wantParserLabels) {
		t.Fatalf("parser accepted-label set = %#v", inspection.Baseline.ClassifierParserAcceptedLabels)
	}
	wantPromptLabels := []string{"CHAT", "PLAN", "ANALYZE", "OPS", "RESEARCH", "CODE", "CODE1", "CODE2", "CODE3"}
	if !equalStringSlices(inspection.Baseline.RepositoryClassifierPromptLabels, wantPromptLabels) {
		t.Fatalf("legacy prompt labels = %#v, want %#v", inspection.Baseline.RepositoryClassifierPromptLabels, wantPromptLabels)
	}
	reachability := inspection.Baseline.Code4Reachability
	if reachability.ExplicitCommandStatus != "REACHABLE" || reachability.ExplicitCommand != "/code4" ||
		reachability.RuleDictionaryStatus != "NO_DEDICATED_RULE" ||
		reachability.NaturalLanguageAutomaticSelectionStatus != "UNDEFINED" {
		t.Fatalf("CODE4 reachability evidence = %+v", reachability)
	}
	if strings.Contains(strings.ToLower(reachability.NaturalLanguageAutomaticSelectionReason), "unreachable") {
		t.Fatalf("prompt omission was incorrectly described as automatic unreachability: %+v", reachability)
	}
}

func TestInspectCode4AgreementComparesRouteAndPolicyMeaning(t *testing.T) {
	route := []byte(`const RouteCODE4 Route = "CODE4" // 高速コーディング/実験（スロット4）`)
	policy := []byte("### CODE4\n高速な試作・実験的coding。比較レビューや仕上げのrouteではない。\n")
	status, conflict := inspectCode4SourceAgreement(route, string(policy))
	if status != "SOURCE_ALIGNED_REVIEW_REQUIRED" || conflict != "" {
		t.Fatalf("aligned route/rules were affected by legacy prompt omission: %q, %q", status, conflict)
	}
	conflictingPolicy := []byte("### CODE4\n複数案の比較・実装レビューを担当する。\n")
	status, conflict = inspectCode4SourceAgreement(route, string(conflictingPolicy))
	if status != "POLICY_UNDEFINED" || conflict == "" {
		t.Fatalf("conflicting route/rules status = %q, %q", status, conflict)
	}
}

func TestCode4PolicyMeaningUsesDeclaredLeadAndExcludesLaterNegation(t *testing.T) {
	criteria := "高速な試作・実験的coding。後段の比較レビューや仕上げのrouteではない。\n"
	meaning := code4PolicyMeaning(criteria)
	domains := code4Domains(meaning)
	if len(domains) != 1 {
		t.Fatalf("unexpected CODE4 lead meaning: %q / %#v", meaning, domains)
	}
	if _, fast := domains["fast_or_experiment"]; !fast {
		t.Fatalf("CODE4 lead meaning lost fast/experiment category: %q / %#v", meaning, domains)
	}
	criteria = "背景説明。\n意味: 高速な試作・実験的coding。比較レビューではない。\n"
	meaning = code4PolicyMeaning(criteria)
	domains = code4Domains(meaning)
	if _, fast := domains["fast_or_experiment"]; !fast {
		t.Fatalf("explicit meaning was not selected: %q / %#v", meaning, domains)
	}
	if _, review := domains["comparison_or_review"]; review {
		t.Fatalf("later negated comparison phrase was counted as positive meaning: %q / %#v", meaning, domains)
	}
	domains = code4Domains("CODE4 is fast prototyping, not a comparison/review route")
	if _, fast := domains["fast_or_experiment"]; !fast {
		t.Fatalf("fast category missing from explicit contrast: %#v", domains)
	}
	if _, review := domains["comparison_or_review"]; review {
		t.Fatalf("negative comparison phrase was counted as positive meaning: %#v", domains)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func writeInspectExperimentConfig(t *testing.T) string {
	t.Helper()
	config := classifierlab.DefaultConfig()
	config.ExperimentID = "inspect-legacy-baseline-test"
	data, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "experiment.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeInspectCoreConfig(t *testing.T, apiKeyEnv, workspacePrompt string) string {
	t.Helper()
	root := t.TempDir()
	promptsDir := filepath.Join(root, "prompts")
	workspaceDir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(promptsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptsDir, "classifier.md"), []byte("repository classifier prompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "classifier.md"), []byte(workspacePrompt), 0o600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`server:
  port: 8080
prompts_dir: %q
workspace_dir: %q
llm_gateway:
  base_url: "http://127.0.0.1:8090"
  api_key_env: %q
  timeout_sec: 60
ai_workflow:
  context_budget_tokens: 4096
`, promptsDir, workspaceDir, apiKeyEnv)
	path := filepath.Join(root, "core.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func inspectSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func TestInspectRejectsInvalidExplicitCOREConfigBeforeCreatingOutput(t *testing.T) {
	root := filepath.Join("..", "..")
	config := writeInspectExperimentConfig(t)
	coreConfig := filepath.Join(t.TempDir(), "missing-core.yaml")
	out := filepath.Join(t.TempDir(), "inspect-invalid")
	if err := execute([]string{
		"inspect", "--config", config, "--core-config", coreConfig,
		"--out", out, "--repo", root,
	}); err == nil {
		t.Fatal("inspect accepted a missing explicit CORE config")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("invalid config created output directory: stat err=%v", err)
	}
}
