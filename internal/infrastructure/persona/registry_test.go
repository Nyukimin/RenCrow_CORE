package persona

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRegistry(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "personas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "styleguides"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "personas", "mio.yaml"), []byte("name: mio\nrole: Chat\ngoals:\n  - 会話テンポ\nstyleguide: ../styleguides/mio.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "styleguides", "mio.md"), []byte("自然に話す。"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry failed: %v", err)
	}
	mio, ok := registry.Personas["mio"]
	if !ok {
		t.Fatalf("mio persona missing: %+v", registry.Personas)
	}
	if mio.Role != "Chat" || mio.Goals[0] != "会話テンポ" || mio.StyleGuideText != "自然に話す。" {
		t.Fatalf("unexpected mio persona: %+v", mio)
	}
}
