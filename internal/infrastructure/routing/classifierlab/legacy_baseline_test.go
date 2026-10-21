package classifierlab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestResolveLegacyBaselineUsesEffectiveWorkspacePromptAndDoesNotExposeSecret(t *testing.T) {
	const apiKeyEnv = "RENCROW_LEGACY_BASELINE_TEST_API_KEY"
	const apiKeyValue = "test-secret-that-must-not-be-emitted"
	t.Setenv(apiKeyEnv, apiKeyValue)
	configPath, basePrompt, workspacePrompt, fixtureRoot := writeLegacyBaselineFixture(t, "http://127.0.0.1:8090")
	before := snapshotLegacyBaselineTree(t, fixtureRoot)

	baseline, err := ResolveLegacyBaseline(configPath)
	if err != nil {
		t.Fatalf("ResolveLegacyBaseline() error = %v", err)
	}
	if baseline.ConfigSHA256 != testLegacyBaselineSHA256(readLegacyBaselineFile(t, configPath)) {
		t.Fatalf("config hash = %q, want hash of explicit config bytes", baseline.ConfigSHA256)
	}
	if baseline.ClassifierPromptSHA256 != testLegacyBaselineSHA256([]byte(strings.TrimSpace(workspacePrompt))) {
		t.Fatalf("effective prompt hash = %q, want workspace override hash", baseline.ClassifierPromptSHA256)
	}
	if baseline.ClassifierPromptSHA256 == testLegacyBaselineSHA256([]byte(strings.TrimSpace(basePrompt))) {
		t.Fatal("workspace override hash unexpectedly equals repository prompt hash")
	}
	if baseline.ClassifierPromptSHA256 == testLegacyBaselineSHA256([]byte(basePrompt)) {
		t.Fatal("workspace override hash unexpectedly equals the repository file hash")
	}
	if baseline.GatewayURL != "http://127.0.0.1:8090" || baseline.GatewayTimeoutMS != 60_000 ||
		legacyBaselineAuthMode(t, baseline) != "env" || baseline.APIKeyEnv != apiKeyEnv || baseline.ContextBudgetTokens != 4096 ||
		baseline.MioAgentID != "mio" || baseline.ChatExecutionRole != "chat" ||
		baseline.ChatExecutionAlias != "mio" || !baseline.LegacyModelUnknown {
		t.Fatalf("resolved legacy baseline = %+v", baseline)
	}
	if !isLowerSHA256(baseline.ManifestID) || baseline.ManifestID != baseline.CalculatedManifestID() {
		t.Fatalf("manifest ID = %q, calculated = %q", baseline.ManifestID, baseline.CalculatedManifestID())
	}
	if err := baseline.ValidateExpectedManifestID(baseline.ManifestID); err != nil {
		t.Fatalf("ValidateExpectedManifestID(valid) error = %v", err)
	}
	if err := baseline.ValidateExpectedManifestID(strings.Repeat("0", 64)); err == nil {
		t.Fatal("ValidateExpectedManifestID accepted a different hash")
	}
	changed := baseline
	changed.GatewayTimeoutMS++
	if changed.CalculatedManifestID() == baseline.ManifestID || changed.ValidateExpectedManifestID(baseline.ManifestID) == nil {
		t.Fatal("manifest ID did not bind the resolved baseline fields")
	}
	encoded, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), apiKeyValue) {
		t.Fatal("baseline DTO serialized the API key value")
	}
	if strings.Contains(string(encoded), strings.TrimSpace(workspacePrompt)) {
		t.Fatal("baseline DTO serialized the resolved prompt body")
	}
	if after := snapshotLegacyBaselineTree(t, fixtureRoot); !reflect.DeepEqual(after, before) {
		t.Fatalf("baseline resolution modified fixture files: before=%v after=%v", before, after)
	}
}

func TestResolveLegacyBaselineAllowsExplicitNoAuthGateway(t *testing.T) {
	const ignoredEnv = "RENCROW_LEGACY_BASELINE_UNUSED_KEY"
	const configuredButIgnoredEnv = "RENCROW_LEGACY_BASELINE_TEST_API_KEY"
	const ignoredValue = "must-not-be-used-or-emitted"
	t.Setenv(ignoredEnv, ignoredValue)
	t.Setenv(configuredButIgnoredEnv, ignoredValue)
	configPath, _, _, _ := writeLegacyBaselineFixtureWithAuthEnv(t, "http://127.0.0.1:8090", "")
	baseline, err := ResolveLegacyBaseline(configPath)
	if err != nil {
		t.Fatalf("explicit empty llm_gateway.api_key_env should resolve as no auth: %v", err)
	}
	if legacyBaselineAuthMode(t, baseline) != "none" || baseline.APIKeyEnv != "" {
		t.Fatalf("no-auth baseline = %+v, want auth_mode=none and empty api_key_env", baseline)
	}
	encoded, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), ignoredValue) {
		t.Fatal("no-auth baseline emitted an unrelated environment value")
	}
}

func TestResolveLegacyBaselineRejectsNonLoopbackGateway(t *testing.T) {
	const apiKeyEnv = "RENCROW_LEGACY_BASELINE_TEST_API_KEY"
	t.Setenv(apiKeyEnv, "present-secret")
	for _, gatewayURL := range []string{
		"http://localhost:8090",
		"http://127.0.0.2:8090",
		"https://example.invalid:8090",
		"http://user:pass@127.0.0.1:8090",
		"http://127.0.0.1:8090/v1",
		"http://127.0.0.1:8090/?x=1",
	} {
		t.Run(gatewayURL, func(t *testing.T) {
			configPath, _, workspacePrompt, _ := writeLegacyBaselineFixture(t, gatewayURL)
			baseline, err := ResolveLegacyBaseline(configPath)
			if err == nil {
				t.Fatalf("ResolveLegacyBaseline(%q) = %+v, want rejection", gatewayURL, baseline)
			}
			if strings.Contains(err.Error(), "present-secret") {
				t.Fatalf("error exposed API key value: %v", err)
			}
			if strings.Contains(err.Error(), strings.TrimSpace(workspacePrompt)) {
				t.Fatalf("error exposed prompt body: %v", err)
			}
		})
	}
}

func TestResolveLegacyBaselineFailsClosedForMissingConfigPromptOrAuth(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		if _, err := ResolveLegacyBaseline(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
			t.Fatal("missing config was accepted")
		}
	})

	t.Run("prompt", func(t *testing.T) {
		const apiKeyEnv = "RENCROW_LEGACY_BASELINE_TEST_API_KEY"
		t.Setenv(apiKeyEnv, "present-secret")
		configPath, _, _, _ := writeLegacyBaselineFixture(t, "http://127.0.0.1:8090")
		if err := os.Remove(filepath.Join(filepath.Dir(configPath), "workspace", "classifier.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(filepath.Dir(configPath), "prompts", "classifier.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveLegacyBaseline(configPath); err == nil {
			t.Fatal("missing classifier prompt files were accepted via the compiled fallback")
		}
	})

	t.Run("auth env", func(t *testing.T) {
		const apiKeyEnv = "RENCROW_LEGACY_BASELINE_TEST_API_KEY"
		t.Setenv(apiKeyEnv, "  ")
		configPath, _, _, _ := writeLegacyBaselineFixture(t, "http://127.0.0.1:8090")
		if _, err := ResolveLegacyBaseline(configPath); err == nil {
			t.Fatal("empty API key environment variable was accepted")
		} else if strings.Contains(err.Error(), apiKeyEnv) {
			// The environment variable name is intentionally part of the baseline
			// DTO, but error strings remain generic and contain no credential data.
			t.Fatalf("auth error unexpectedly disclosed environment details: %v", err)
		}
	})
}

func writeLegacyBaselineFixture(t *testing.T, gatewayURL string) (configPath, basePrompt, workspacePrompt, root string) {
	t.Helper()
	return writeLegacyBaselineFixtureWithAuthEnv(t, gatewayURL, "RENCROW_LEGACY_BASELINE_TEST_API_KEY")
}

func writeLegacyBaselineFixtureWithAuthEnv(t *testing.T, gatewayURL, apiKeyEnv string) (configPath, basePrompt, workspacePrompt, root string) {
	t.Helper()
	root = t.TempDir()
	promptsDir := filepath.Join(root, "prompts")
	workspaceDir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(promptsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryPromptPath := filepath.Join(filepath.Dir(sourcePath), "..", "..", "..", "..", "prompts", "classifier.md")
	repositoryPrompt, err := os.ReadFile(repositoryPromptPath)
	if err != nil {
		t.Fatalf("read repository classifier prompt: %v", err)
	}
	basePrompt = string(repositoryPrompt)
	workspacePrompt = "Workspace classifier override.\n"
	if err := os.WriteFile(filepath.Join(promptsDir, "classifier.md"), []byte(basePrompt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "classifier.md"), []byte(workspacePrompt), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(root, "config.yaml")
	content := fmt.Sprintf(`server:
  port: 8080
prompts_dir: %q
workspace_dir: %q
llm_gateway:
  base_url: %q
  api_key_env: %q
  timeout_sec: 60
ai_workflow:
  context_budget_tokens: 4096
`, promptsDir, workspaceDir, gatewayURL, apiKeyEnv)
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, basePrompt, workspacePrompt, root
}

func readLegacyBaselineFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func snapshotLegacyBaselineTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			files[relative] = fmt.Sprintf("directory:%04o", info.Mode().Perm())
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = fmt.Sprintf("file:%s:%04o", testLegacyBaselineSHA256(data), info.Mode().Perm())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func testLegacyBaselineSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func legacyBaselineAuthMode(t *testing.T, baseline LegacyBaseline) string {
	t.Helper()
	encoded, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	mode, _ := fields["auth_mode"].(string)
	return mode
}

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
