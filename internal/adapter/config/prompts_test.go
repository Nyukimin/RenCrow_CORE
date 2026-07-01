package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPromptsLoadsCharacterBundlesFromWorkspace(t *testing.T) {
	baseDir := t.TempDir()
	workspaceDir := t.TempDir()

	writeCharacterBundle(t, workspaceDir, "mio", map[string]string{
		"00_system.md":    "mio system",
		"10_policy.md":    "mio policy",
		"20_routing.md":   "mio routing",
		"30_knowledge.md": "mio knowledge",
	})
	writeCharacterBundle(t, workspaceDir, "shiro", map[string]string{
		"00_system.md": "shiro system",
		"10_policy.md": "shiro policy",
	})
	writeCharacterBundle(t, workspaceDir, "kuro", map[string]string{
		"00_system.md": "kuro system",
	})
	writeCharacterBundle(t, workspaceDir, "midori", map[string]string{
		"00_system.md": "midori system",
	})
	for _, name := range []string{"aka", "ao", "gin", "kin"} {
		writeCharacterBundle(t, workspaceDir, name, map[string]string{
			"00_system.md": name + " system",
		})
	}

	p := LoadPrompts(baseDir, workspaceDir)

	if !strings.Contains(p.MioPersona, "mio system") || !strings.Contains(p.MioPersona, "mio knowledge") {
		t.Fatalf("MioPersona did not load mio bundle:\n%s", p.MioPersona)
	}
	if !strings.Contains(p.Worker, "shiro system") || !strings.Contains(p.Worker, "shiro policy") {
		t.Fatalf("Worker did not load shiro character bundle:\n%s", p.Worker)
	}
	if !strings.Contains(p.Heavy, "kuro system") {
		t.Fatalf("Heavy did not load kuro character bundle:\n%s", p.Heavy)
	}
	if !strings.Contains(p.Wild, "midori system") {
		t.Fatalf("Wild did not load midori character bundle:\n%s", p.Wild)
	}
	if got := p.CharacterPrompts["mio"]; !strings.Contains(got, "mio routing") {
		t.Fatalf("CharacterPrompts[mio] missing bundle content:\n%s", got)
	}
	for _, name := range []string{"aka", "ao", "gin", "kin"} {
		if got := p.CharacterPrompts[name]; !strings.Contains(got, name+" system") {
			t.Fatalf("CharacterPrompts[%s] missing bundle content:\n%s", name, got)
		}
	}
}

func TestLoadPromptsIgnoresCharacterBundlesFromPromptsDir(t *testing.T) {
	baseDir := t.TempDir()
	workspaceDir := t.TempDir()

	writeCharacterBundleAtRoot(t, filepath.Join(baseDir, "characters"), "mio", map[string]string{
		"00_system.md":    "repo mio system",
		"30_knowledge.md": "repo mio knowledge",
	})
	writeCharacterBundleAtRoot(t, filepath.Join(baseDir, "characters"), "midori", map[string]string{
		"00_system.md": "repo midori system",
	})

	p := LoadPrompts(baseDir, workspaceDir)

	if strings.Contains(p.MioPersona, "repo mio system") || strings.Contains(p.MioPersona, "repo mio knowledge") {
		t.Fatalf("MioPersona should not load repo character bundle:\n%s", p.MioPersona)
	}
	if strings.Contains(p.Wild, "repo midori system") {
		t.Fatalf("Wild should not load repo character bundle:\n%s", p.Wild)
	}
}

func TestLoadPromptsWorkspaceCharacterBundleOverridesLegacyPrompt(t *testing.T) {
	baseDir := t.TempDir()
	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "worker.md"), []byte("legacy worker"), 0o644); err != nil {
		t.Fatalf("write legacy worker: %v", err)
	}
	writeCharacterBundle(t, workspaceDir, "shiro", map[string]string{
		"00_system.md": "character shiro",
	})

	p := LoadPrompts(baseDir, workspaceDir)

	if strings.Contains(p.Worker, "legacy worker") || !strings.Contains(p.Worker, "character shiro") {
		t.Fatalf("workspace shiro bundle should override legacy worker prompt:\n%s", p.Worker)
	}
}

func TestBuildIdleChatAgentPromptsLayersCharacterBundleThenIdleCorrection(t *testing.T) {
	p := &LoadedPrompts{
		CharacterPrompts: map[string]string{
			"mio":   "mio character system\nmio character policy",
			"shiro": "shiro character system\nshiro character policy",
		},
		IdleChatAgents: map[string]string{
			"mio":   "mio idle correction",
			"Shiro": "shiro idle correction",
		},
	}

	got := BuildIdleChatAgentPrompts(p)

	for _, key := range []string{"mio", "Mio"} {
		if !strings.Contains(got[key], "mio character system") || !strings.Contains(got[key], "mio idle correction") {
			t.Fatalf("mio idle prompt %q did not layer character and idle prompts:\n%s", key, got[key])
		}
		if strings.Index(got[key], "mio character system") > strings.Index(got[key], "mio idle correction") {
			t.Fatalf("mio idle correction should be appended after character prompt:\n%s", got[key])
		}
	}
	for _, key := range []string{"shiro", "Shiro"} {
		if !strings.Contains(got[key], "shiro character system") || !strings.Contains(got[key], "shiro idle correction") {
			t.Fatalf("shiro idle prompt %q did not layer character and idle prompts:\n%s", key, got[key])
		}
		if strings.Index(got[key], "shiro character system") > strings.Index(got[key], "shiro idle correction") {
			t.Fatalf("shiro idle correction should be appended after character prompt:\n%s", got[key])
		}
	}
}

func writeCharacterBundle(t *testing.T, workspaceDir, name string, files map[string]string) {
	t.Helper()
	writeCharacterBundleAtRoot(t, filepath.Join(workspaceDir, "prompts", "characters"), name, files)
}

func writeCharacterBundleAtRoot(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir character bundle: %v", err)
	}
	manifest := make([]string, 0, len(files))
	for filename, content := range files {
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", filename, err)
		}
		manifest = append(manifest, filename)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.txt"), []byte(strings.Join(manifest, "\n")), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
