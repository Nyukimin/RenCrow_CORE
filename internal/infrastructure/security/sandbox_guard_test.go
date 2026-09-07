package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxGuard_IsCommandDenied(t *testing.T) {
	g := NewSandboxGuard()
	if !g.IsCommandDenied("rm -rf /tmp/x", []string{"rm -rf"}) {
		t.Fatal("expected rm -rf to be denied")
	}
	if g.IsCommandDenied("echo hello", []string{"rm -rf"}) {
		t.Fatal("expected echo to be allowed")
	}
}

func TestSandboxGuard_IsPathWithinWorkspace(t *testing.T) {
	g := NewSandboxGuard()
	ws := t.TempDir()
	inside := filepath.Join(ws, "a", "b.txt")
	outside := filepath.Join(filepath.Dir(ws), "outside.txt")

	if !g.IsPathWithinWorkspace(inside, ws) {
		t.Fatal("expected inside path to be allowed")
	}
	if g.IsPathWithinWorkspace(outside, ws) {
		t.Fatal("expected outside path to be denied")
	}
}

func TestSandboxGuard_IsSafeSandboxWritePath(t *testing.T) {
	g := NewSandboxGuard()
	root := t.TempDir()
	inside := filepath.Join(root, "workspace", "draft.md")
	outside := filepath.Join(filepath.Dir(root), "outside.md")

	if !g.IsSafeSandboxWritePath(inside, root) {
		t.Fatal("expected sandbox write path to be allowed")
	}
	if g.IsSafeSandboxWritePath(outside, root) {
		t.Fatal("expected outside sandbox path to be denied")
	}
	if g.IsSafeSandboxWritePath(filepath.Join(root, ".env"), root) {
		t.Fatal("expected secret-like sandbox path to be denied")
	}
	if g.IsSafeSandboxWritePath(filepath.Join(root, "..", "escape.md"), root) {
		t.Fatal("expected traversal escape to be denied")
	}
}

func TestSandboxGuard_IsHostAllowed(t *testing.T) {
	g := NewSandboxGuard()
	if !g.IsHostAllowed("api.example.com", []string{"api.example.com"}) {
		t.Fatal("expected exact host to be allowed")
	}
	if !g.IsHostAllowed("sub.example.com", []string{".example.com"}) {
		t.Fatal("expected suffix host to be allowed")
	}
	if g.IsHostAllowed("evil.com", []string{"api.example.com"}) {
		t.Fatal("expected non-allowlisted host to be denied")
	}
}

func TestSandboxGuard_ExtractNetworkHost(t *testing.T) {
	g := NewSandboxGuard()
	host, ok := g.ExtractNetworkHost(map[string]any{"url": "https://api.example.com/v1/models"})
	if !ok || host != "api.example.com" {
		t.Fatalf("expected host api.example.com, got ok=%v host=%q", ok, host)
	}
	host, ok = g.ExtractNetworkHost(map[string]any{"host": "localhost:8080"})
	if !ok || host != "localhost" {
		t.Fatalf("expected host localhost, got ok=%v host=%q", ok, host)
	}
	host, ok = g.ExtractNetworkHost(map[string]any{"start_url": "https://example.com/path"})
	if !ok || host != "example.com" {
		t.Fatalf("expected host example.com, got ok=%v host=%q", ok, host)
	}
}

func TestSandboxGuardPhysicalWorkspaceBoundary(t *testing.T) {
	g := NewSandboxGuard()
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{filepath.Join(workspace, "escape"), filepath.Join(workspace, "escape", "new.txt")} {
		if g.IsPathWithinWorkspace(target, workspace) {
			t.Errorf("external symlink allowed: %s", target)
		}
	}
	inside := filepath.Join(workspace, "real")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(workspace, "alias")); err != nil {
		t.Fatal(err)
	}
	if !g.IsPathWithinWorkspace(filepath.Join(workspace, "alias", "new.txt"), workspace) {
		t.Fatal("internal alias rejected")
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(workspace, "dangling")); err != nil {
		t.Fatal(err)
	}
	if g.IsPathWithinWorkspace(filepath.Join(workspace, "dangling", "new.txt"), workspace) {
		t.Fatal("dangling link allowed")
	}
	if !g.IsPathWithinWorkspace(filepath.Join(workspace, "..notes"), workspace) {
		t.Fatal("ordinary dotdot-prefixed filename rejected")
	}
}

func TestSandboxGuardProtectedAlias(t *testing.T) {
	g := NewSandboxGuard()
	workspace := t.TempDir()
	secret := filepath.Join(workspace, ".env")
	if err := os.WriteFile(secret, []byte("test fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(workspace, "ordinary.txt")
	if err := os.Symlink(secret, alias); err != nil {
		t.Fatal(err)
	}
	if g.IsSafeSandboxWritePath(alias, workspace) {
		t.Fatal("protected target accepted through alias")
	}
}

func TestSandboxGuardInvalidPhysicalPaths(t *testing.T) {
	g := NewSandboxGuard()
	root := t.TempDir()
	regular := filepath.Join(root, "file")
	if err := os.WriteFile(regular, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", regular + string(filepath.Separator) + "child", root + string(filepath.Separator) + "missing" + string(filepath.Separator) + ".." + string(filepath.Separator) + "leaf"} {
		if g.IsPathWithinWorkspace(path, root) {
			t.Errorf("invalid path accepted: %q", path)
		}
	}
	if g.IsPathWithinWorkspace(filepath.Join(root, "missing", "leaf"), filepath.Join(root, "missing")) {
		t.Fatal("missing workspace accepted")
	}
}
