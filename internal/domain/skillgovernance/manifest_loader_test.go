package skillgovernance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseManifestYAML(t *testing.T) {
	manifest := ParseManifestYAML(`skill:
  id: "core.pr-readiness"
  name: "PR Readiness"
  scope: "core"
  version: "1.0.0"
  description: "PR gate"
  human_approval_required: true
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
	if !manifest.HumanApprovalRequired {
		t.Fatal("expected human approval required")
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
human_approval_required: true
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
	if manifest.Enabled || !manifest.HumanApprovalRequired {
		t.Fatalf("enabled/human approval flags wrong: %#v", manifest)
	}
	if len(manifest.KeywordTriggers) != 1 || manifest.KeywordTriggers[0] != "local" {
		t.Fatalf("keywords=%#v", manifest.KeywordTriggers)
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
