package promptbundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBundleUsesManifestOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "manifest.txt", "# prompt bundle\n10_policy.md\n00_system.md\n")
	writeFile(t, dir, "00_system.md", "system")
	writeFile(t, dir, "10_policy.md", "policy")

	got, ok := ReadBundle(dir)
	if !ok {
		t.Fatal("ReadBundle returned false")
	}
	want := "policy" + Separator + "system"
	if got != want {
		t.Fatalf("bundle content mismatch:\nwant=%q\ngot =%q", want, got)
	}
}

func TestReadBundleIgnoresInvalidManifestEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "manifest.txt", strings.Join([]string{
		"00_system.md",
		"../secret.md",
		"nested/policy.md",
		"note.txt",
		"missing.md",
	}, "\n"))
	writeFile(t, dir, "00_system.md", "system")

	got, ok := ReadBundle(dir)
	if !ok {
		t.Fatal("ReadBundle returned false")
	}
	if got != "system" {
		t.Fatalf("invalid entries should be ignored, got %q", got)
	}
}

func TestLoadCharacterBundlesFromDirSupportsRepoAndWorkspaceRoots(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, filepath.Join(dir, "characters", "mio"), "repo mio")
	writeBundle(t, filepath.Join(dir, "prompts", "characters", "shiro"), "workspace shiro")

	bundles := LoadCharacterBundlesFromDir(dir)

	got := map[string]string{}
	for _, bundle := range bundles {
		got[bundle.Name] = bundle.Content
	}
	if got["mio"] != "repo mio" {
		t.Fatalf("repo-style characters root was not loaded: %#v", got)
	}
	if got["shiro"] != "workspace shiro" {
		t.Fatalf("workspace-style prompts/characters root was not loaded: %#v", got)
	}
}

func writeBundle(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	writeFile(t, dir, "manifest.txt", "00_system.md\n")
	writeFile(t, dir, "00_system.md", content)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir test dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
