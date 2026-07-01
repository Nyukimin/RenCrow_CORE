package security

import (
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
	if !g.IsHostAllowed("api.openai.com", []string{"api.openai.com"}) {
		t.Fatal("expected exact host to be allowed")
	}
	if !g.IsHostAllowed("sub.example.com", []string{".example.com"}) {
		t.Fatal("expected suffix host to be allowed")
	}
	if g.IsHostAllowed("evil.com", []string{"api.openai.com"}) {
		t.Fatal("expected non-allowlisted host to be denied")
	}
}

func TestSandboxGuard_ExtractNetworkHost(t *testing.T) {
	g := NewSandboxGuard()
	host, ok := g.ExtractNetworkHost(map[string]any{"url": "https://api.openai.com/v1/models"})
	if !ok || host != "api.openai.com" {
		t.Fatalf("expected host api.openai.com, got ok=%v host=%q", ok, host)
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
