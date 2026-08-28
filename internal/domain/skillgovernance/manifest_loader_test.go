package skillgovernance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseManifestYAML(t *testing.T) {
	manifest := ParseManifestYAML(`skill:
  id: "core.pr-readiness"
  name: "PR Readiness"
  scope: "core"
  version: "1.0.0"
  description: "PR gate"
triggers:
  keywords:
    - "PR"
    - "pull request"
  intents:
    - "prepare_pr"
`)
	if manifest.SkillID != "core.pr-readiness" {
		t.Fatalf("SkillID=%q", manifest.SkillID)
	}
	if manifest.Scope != ScopeCore {
		t.Fatalf("Scope=%q", manifest.Scope)
	}
	if len(manifest.KeywordTriggers) != 2 || manifest.KeywordTriggers[0] != "PR" {
		t.Fatalf("KeywordTriggers=%#v", manifest.KeywordTriggers)
	}
	if len(manifest.IntentTriggers) != 1 || manifest.IntentTriggers[0] != "prepare_pr" {
		t.Fatalf("IntentTriggers=%#v", manifest.IntentTriggers)
	}
}

func TestLoadManifestsFromDirs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "core", "pr-readiness")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skill_manifest.yaml"), []byte(`skill:
  id: "core.pr-readiness"
  name: "PR Readiness"
triggers:
  keywords:
    - "PR"
`), 0644); err != nil {
		t.Fatal(err)
	}
	manifests, err := LoadManifestsFromDirs(root)
	if err != nil {
		t.Fatalf("LoadManifestsFromDirs failed: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("len=%d manifests=%#v", len(manifests), manifests)
	}
	if manifests[0].Scope != ScopeCore {
		t.Fatalf("scope=%q", manifests[0].Scope)
	}
	if manifests[0].Path != dir {
		t.Fatalf("path=%q want %q", manifests[0].Path, dir)
	}
}

func TestParseManifestYAMLDefaultsAndTopLevelFields(t *testing.T) {
	manifest := ParseManifestYAML(`
# comment
skill:
  id: project.local
  name: Local Skill
  enabled: false
  unknown line without colon
triggers:
  keywords:
    - local
`)
	if manifest.SkillID != "project.local" || manifest.Name != "Local Skill" {
		t.Fatalf("manifest=%#v", manifest)
	}
	if manifest.Enabled {
		t.Fatalf("enabled flag wrong: %#v", manifest)
	}
	if len(manifest.KeywordTriggers) != 1 || manifest.KeywordTriggers[0] != "local" {
		t.Fatalf("keywords=%#v", manifest.KeywordTriggers)
	}
}

func TestParseManifestYAMLDevelopmentContract(t *testing.T) {
	manifest := ParseManifestYAML(`skill:
  id: "core.development-intake"
  name: "Development Intake"
  scope: "core"
  version: "1.0.0"
  description: "Turn a request into a bounded development intake."
  enabled: true
development_contract:
  required_capability: "repository_read"
  required_tools:
    - "rg"
    - "git"
  required_knowledge:
    - "AGENTS.md"
    - "docs/README.md"
  authority_requirement: "authenticated_request_scope"
  input_contract: "A stated development goal and target module."
  output_contract: "A bounded intake with owner, source of truth, and evidence gates."
  cost_hint: "low"
  risk_level: "low"
  evaluation_method: "manifest and evidence checklist"
  version: "1.0.0"
triggers:
  keywords:
    - "development intake"
`)
	want := DevelopmentContract{
		RequiredCapability:   "repository_read",
		RequiredTools:        []string{"rg", "git"},
		RequiredKnowledge:    []string{"AGENTS.md", "docs/README.md"},
		AuthorityRequirement: "authenticated_request_scope",
		InputContract:        "A stated development goal and target module.",
		OutputContract:       "A bounded intake with owner, source of truth, and evidence gates.",
		CostHint:             "low",
		RiskLevel:            "low",
		EvaluationMethod:     "manifest and evidence checklist",
		Version:              "1.0.0",
	}
	if !reflect.DeepEqual(manifest.DevelopmentContract, want) {
		t.Fatalf("DevelopmentContract=%#v, want %#v", manifest.DevelopmentContract, want)
	}
}

func TestParseManifestYAMLDevelopmentContractSupportsInlineLists(t *testing.T) {
	manifest := ParseManifestYAML(`skill:
  id: core.contract
  name: Contract
development_contract:
  required_capability: inspect
  required_tools: [rg, git]
  required_knowledge: [AGENTS.md]
  authority_requirement: none
  input_contract: request
  output_contract: receipt
  cost_hint: low
  risk_level: low
  evaluation_method: tests
  version: 1.0.0
`)
	if got := manifest.DevelopmentContract.RequiredTools; !reflect.DeepEqual(got, []string{"rg", "git"}) {
		t.Fatalf("RequiredTools=%#v", got)
	}
	if got := manifest.DevelopmentContract.RequiredKnowledge; !reflect.DeepEqual(got, []string{"AGENTS.md"}) {
		t.Fatalf("RequiredKnowledge=%#v", got)
	}
}

func TestSkillManifestJSONRoundTripPreservesDevelopmentContract(t *testing.T) {
	manifest := SkillManifest{
		SkillID: "core.contract",
		Name:    "Contract",
		Scope:   ScopeCore,
		Version: "1.0.0",
		Path:    "workspace/skills/contract",
		Enabled: true,
		DevelopmentContract: DevelopmentContract{
			RequiredCapability:   "inspect",
			RequiredTools:        []string{"rg"},
			RequiredKnowledge:    []string{"AGENTS.md"},
			AuthorityRequirement: "none",
			InputContract:        "request",
			OutputContract:       "receipt",
			CostHint:             "low",
			RiskLevel:            "low",
			EvaluationMethod:     "tests",
			Version:              "1.0.0",
		},
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if !strings.Contains(string(payload), `"development_contract"`) {
		t.Fatalf("JSON omitted development contract: %s", payload)
	}
	var got SkillManifest
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if !reflect.DeepEqual(got.DevelopmentContract, manifest.DevelopmentContract) {
		t.Fatalf("round-trip contract=%#v, want %#v", got.DevelopmentContract, manifest.DevelopmentContract)
	}
}

func TestDevelopmentMethodologySkillCardsHaveValidatedManifests(t *testing.T) {
	wantIDs := map[string]string{
		"development-intake":              "core.development-intake",
		"backlog-maturation-revalidation": "core.backlog-maturation-revalidation",
		"design-to-spec":                  "core.design-to-spec",
		"implementation-planning":         "core.implementation-planning",
		"worktree-baseline-setup":         "core.worktree-baseline-setup",
		"tdd-task-implementation":         "core.tdd-task-implementation",
		"systematic-debugging":            "core.systematic-debugging",
		"task-review":                     "core.task-review",
		"branch-review":                   "core.branch-review",
		"plan-conflict-ruling":            "core.plan-conflict-ruling",
		"delivery-verification":           "core.delivery-verification",
		"finish-implementation-unit":      "core.finish-implementation-unit",
		"rencrow-development-loop":        "core.rencrow-development-loop",
	}
	manifests, err := LoadManifestsFromDirs(filepath.Join("..", "..", "..", "workspace", "skills"))
	if err != nil {
		t.Fatalf("LoadManifestsFromDirs failed: %v", err)
	}
	byPath := make(map[string]SkillManifest, len(wantIDs))
	for _, manifest := range manifests {
		if err := ValidateSkillManifest(manifest); err != nil {
			t.Fatalf("manifest %s is invalid: %v", manifest.SkillID, err)
		}
		base := filepath.Base(manifest.Path)
		if _, wanted := wantIDs[base]; wanted {
			byPath[base] = manifest
		}
	}
	if len(byPath) != len(wantIDs) {
		t.Fatalf("loaded %d methodology manifests, want %d: %#v", len(byPath), len(wantIDs), byPath)
	}
	for dirName, wantID := range wantIDs {
		manifest, ok := byPath[dirName]
		if !ok {
			t.Fatalf("missing methodology manifest for %s", dirName)
		}
		if manifest.SkillID != wantID {
			t.Fatalf("%s skill id=%q, want %q", dirName, manifest.SkillID, wantID)
		}
		body, err := os.ReadFile(filepath.Join(manifest.Path, "SKILL.md"))
		if err != nil {
			t.Fatalf("%s SKILL.md missing: %v", dirName, err)
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			t.Fatalf("%s SKILL.md is empty", dirName)
		}
	}
}

func TestLoadManifestsFromDirsSkipsInvalidDuplicateAndInfersScopes(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		filepath.Join(root, "plugin", "alpha", "skill_manifest.yaml"): `skill:
  id: "plugin.alpha"
  name: "Alpha"
`,
		filepath.Join(root, "plugins", "beta", "skill_manifest.yaml"): `skill:
  id: "plugin.beta"
  name: "Beta"
`,
		filepath.Join(root, "projects", "gamma", "skill_manifest.yaml"): `skill:
  id: "project.gamma"
  name: "Gamma"
`,
		filepath.Join(root, "project", "duplicate", "skill_manifest.yaml"): `skill:
  id: "plugin.alpha"
  name: "Duplicate"
`,
		filepath.Join(root, "project", "missing-id", "skill_manifest.yaml"): `skill:
  name: "Missing ID"
`,
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	manifests, err := LoadManifestsFromDirs("", filepath.Join(root, "does-not-exist"), root)
	if err != nil {
		t.Fatalf("LoadManifestsFromDirs failed: %v", err)
	}
	byID := map[string]SkillManifest{}
	for _, manifest := range manifests {
		byID[manifest.SkillID] = manifest
		if manifest.Version != "0.0.0" || manifest.UpdatedAt.IsZero() {
			t.Fatalf("default fields missing: %#v", manifest)
		}
	}
	if len(byID) != 3 {
		t.Fatalf("manifests=%#v", manifests)
	}
	if byID["plugin.alpha"].Scope != ScopePlugin || byID["plugin.beta"].Scope != ScopePlugin || byID["project.gamma"].Scope != ScopeProject {
		t.Fatalf("scopes=%#v", byID)
	}
}
