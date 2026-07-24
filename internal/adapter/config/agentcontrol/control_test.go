package agentcontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsAndRendersSharedAgentControl(t *testing.T) {
	workspaceDir := t.TempDir()
	writeControlFixture(t, workspaceDir)

	control, err := Load(workspaceDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if control == nil {
		t.Fatal("Load() returned nil control")
	}

	mioPrompt := control.PromptFor("mio")
	for _, want := range []string{
		"Shared Agent Control",
		"目的整理",
		"CHAT -> mio",
		"ANALYZE -> kuro",
		"destination_owner: orchestrator",
		"required_capability",
		"core_toolrunner",
	} {
		if !strings.Contains(mioPrompt, want) {
			t.Fatalf("Mio control prompt missing %q:\n%s", want, mioPrompt)
		}
	}

	midoriPrompt := control.PromptFor("midori")
	for _, want := range []string{"RenCrow_Image", "Forge Neo", "codex.run", "ImageGen", "automatic_fallback: false"} {
		if !strings.Contains(midoriPrompt, want) {
			t.Fatalf("Midori control prompt missing %q:\n%s", want, midoriPrompt)
		}
	}
}

func TestLoadReturnsNilWhenControlDirectoryDoesNotExist(t *testing.T) {
	control, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if control != nil {
		t.Fatalf("Load() = %#v, want nil", control)
	}
}

func TestLoadRejectsPartialControlSet(t *testing.T) {
	workspaceDir := t.TempDir()
	controlDir := filepath.Join(workspaceDir, "control")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "agents.yaml"), []byte("version: 1\nagents: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(workspaceDir); err == nil || !strings.Contains(err.Error(), "routing.yaml") {
		t.Fatalf("Load() error = %v, want missing routing.yaml", err)
	}
}

func TestLoadRejectsRouteThatDoesNotMatchCoreExecutionOwner(t *testing.T) {
	workspaceDir := t.TempDir()
	writeControlFixture(t, workspaceDir)
	path := filepath.Join(workspaceDir, "control", "routing.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "ANALYZE:\n    primary: kuro", "ANALYZE:\n    primary: shiro", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(workspaceDir); err == nil || !strings.Contains(err.Error(), "ANALYZE") {
		t.Fatalf("Load() error = %v, want ANALYZE owner mismatch", err)
	}
}

func TestLoadRejectsAutomaticToolFallback(t *testing.T) {
	workspaceDir := t.TempDir()
	writeControlFixture(t, workspaceDir)
	path := filepath.Join(workspaceDir, "control", "tools.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "automatic_fallback: false", "automatic_fallback: true", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(workspaceDir); err == nil || !strings.Contains(err.Error(), "automatic_fallback") {
		t.Fatalf("Load() error = %v, want automatic_fallback rejection", err)
	}
}

func writeControlFixture(t *testing.T, workspaceDir string) {
	t.Helper()
	controlDir := filepath.Join(workspaceDir, "control")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"agents.yaml": `version: 1
agents:
  mio:
    role: chat_orchestrator
    capabilities: [目的整理]
    non_goals: [side_effect_execution]
  shiro:
    role: worker
    capabilities: [execution]
    non_goals: [route_destination_selection]
  kuro:
    role: heavy
    capabilities: [deep_analysis]
    non_goals: [side_effect_execution]
  midori:
    role: wild
    capabilities: [visual_creation]
    non_goals: [route_destination_selection]
`,
		"routing.yaml": `version: 1
fallback: CHAT
routes:
  CHAT:
    primary: mio
  PLAN:
    primary: mio
  ANALYZE:
    primary: kuro
  OPS:
    primary: shiro
  RESEARCH:
    primary: mio
  CODE:
    primary: shiro
  CODE1:
    primary: shiro
  CODE2:
    primary: shiro
  CODE3:
    primary: shiro
  CODE4:
    primary: shiro
  WILD:
    primary: midori
`,
		"handoff.yaml": `version: 1
destination_owner: orchestrator
agent_selects_destination: false
required_fields:
  - reason
  - required_capability
  - context
  - constraints
  - expected_output
`,
		"tools.yaml": `version: 1
metadata_source: core_toolrunner
availability_required: true
agents:
  mio:
    access: chat_read_only
    rules: [照会系Toolだけを使う]
  shiro:
    access: worker_policy
    rules: [file_read file_list file_write shell web_search web_gather browser.run codex.run subagent register_tool]
  kuro:
    access: evidence_only
    rules: [実行はShiroへ移譲する]
  midori:
    access: creative_policy
    rules: [画像生成方法を目的から選ぶ]
    selections:
      image_generation:
        preferred: RenCrow_Image / Forge Neo
        alternatives: [codex.run / ImageGen]
        automatic_fallback: false
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(controlDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
