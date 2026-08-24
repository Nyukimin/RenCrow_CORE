package agentcontrol

import (
	"fmt"
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
		"Shared Engineering Rules",
		"project-level-ai-rules",
		"上から下へ追える",
		"目的整理",
		"CHAT -> mio",
		"ANALYZE -> kuro",
		"destination_owner: orchestrator",
		"required_capability",
		"core_toolrunner",
		"selection_principles",
		"Tool、Skill、MCP",
	} {
		if !strings.Contains(mioPrompt, want) {
			t.Fatalf("Mio control prompt missing %q:\n%s", want, mioPrompt)
		}
	}
	mioContracts := control.PromptForMio()
	for _, want := range []string{
		"Mio Agent Contract Index",
		"shiro",
		"aka",
		"ao",
		"gin",
		"kuro",
		"midori",
		"delegatable_work",
		"expected_output",
		"return_to_mio",
		"selection_principles",
		"Tool、Skill、MCP",
	} {
		if !strings.Contains(mioContracts, want) {
			t.Fatalf("Mio contract index missing %q:\n%s", want, mioContracts)
		}
	}

	midoriPrompt := control.PromptFor("midori")
	for _, want := range []string{"RenCrow_Image", "Forge Neo", "codex.run", "ImageGen", "automatic_fallback: false"} {
		if !strings.Contains(midoriPrompt, want) {
			t.Fatalf("Midori control prompt missing %q:\n%s", want, midoriPrompt)
		}
	}
}

func TestPromptForMioSummarizesToolRulesInsteadOfExpandingCatalog(t *testing.T) {
	rules := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		rules = append(rules, fmt.Sprintf("data.capability.%03d", i))
	}
	control := &Control{
		Agents: map[string]Agent{"mio": {Role: "conversation_orchestrator"}},
		Routing: Routing{Fallback: "CHAT", Routes: map[string]Route{
			"CHAT": {Primary: "mio"},
		}},
		Handoff: Handoff{DestinationOwner: "orchestrator"},
		Tools: Tools{
			MetadataSource:       "core_toolrunner",
			AvailabilityRequired: true,
			SelectionPrinciples:  []string{"CORE metadataで利用可能性を確認する"},
			Agents: map[string]AgentTools{
				"mio": {Access: "chat_read_only", Rules: rules},
			},
		},
	}

	prompt := control.PromptForMio()
	for _, want := range []string{"tool_rules_count: 300", "個別ruleは実行時CORE policyが判定"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Mio tool boundary missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "data.capability.000") || strings.Contains(prompt, "data.capability.299") {
		t.Fatalf("Mio prompt expanded the full tool catalog: %d runes", len([]rune(prompt)))
	}
	if got := len([]rune(prompt)); got > 6000 {
		t.Fatalf("Mio stable prompt is unbounded: %d runes", got)
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

func TestLoadRejectsMissingEmptyOrBlankSelectionPrinciples(t *testing.T) {
	const principles = `selection_principles:
  - CORE metadataと注入済み能力一覧から利用可能なTool、Skill、MCPを把握する
  - 目的、必要な証拠、副作用、再現性に合う最小の組合せを選び、単一手段への思い込みを避ける
  - 読み取りや検証に適切な能力がある場合は推測だけで済ませず、その能力で事実を確認する
  - 未提供・利用不能な能力を使ったふりをせず、必要能力と根拠をOrchestratorへ返す
`
	cases := []struct {
		name    string
		replace string
		with    string
	}{
		{name: "missing", replace: principles, with: ""},
		{name: "empty", replace: principles, with: "selection_principles: []\n"},
		{name: "blank", replace: "  - CORE metadataと注入済み能力一覧から利用可能なTool、Skill、MCPを把握する\n", with: "  - \"   \"\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspaceDir := t.TempDir()
			writeControlFixture(t, workspaceDir)
			path := filepath.Join(workspaceDir, "control", "tools.yaml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.Replace(string(data), tc.replace, tc.with, 1))
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}

			if _, err := Load(workspaceDir); err == nil || !strings.Contains(err.Error(), "tools selection_principles") {
				t.Fatalf("Load() error = %v, want tools selection_principles rejection", err)
			}
		})
	}
}

func TestLoadRejectsMissingSharedEngineeringRules(t *testing.T) {
	workspaceDir := t.TempDir()
	writeControlFixture(t, workspaceDir)
	path := filepath.Join(workspaceDir, "control", "agents.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block := `shared_engineering_rules:
  source: project-level-ai-rules
  principles:
    - 要件が同等なら簡潔な設計を選ぶ
    - 主経路を上から下へ追える形にする
`
	data = []byte(strings.Replace(string(data), block, "", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(workspaceDir); err == nil || !strings.Contains(err.Error(), "shared_engineering_rules") {
		t.Fatalf("Load() error = %v, want missing shared engineering rules rejection", err)
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
shared_engineering_rules:
  source: project-level-ai-rules
  principles:
    - 要件が同等なら簡潔な設計を選ぶ
    - 主経路を上から下へ追える形にする
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
  aka:
    role: coder1
    capabilities: [primary_implementation]
    non_goals: [patch_application]
  ao:
    role: coder2
    capabilities: [supporting_implementation]
    non_goals: [patch_application]
  kin:
    role: coder3
    capabilities: [security_review]
    non_goals: [patch_application]
  gin:
    role: coder4
    capabilities: [integration_review]
    non_goals: [patch_application]
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
selection_principles:
  - CORE metadataと注入済み能力一覧から利用可能なTool、Skill、MCPを把握する
  - 目的、必要な証拠、副作用、再現性に合う最小の組合せを選び、単一手段への思い込みを避ける
  - 読み取りや検証に適切な能力がある場合は推測だけで済ませず、その能力で事実を確認する
  - 未提供・利用不能な能力を使ったふりをせず、必要能力と根拠をOrchestratorへ返す
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
  aka:
    access: proposal_only
  ao:
    access: proposal_only
  kin:
    access: proposal_only
  gin:
    access: proposal_only
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(controlDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
