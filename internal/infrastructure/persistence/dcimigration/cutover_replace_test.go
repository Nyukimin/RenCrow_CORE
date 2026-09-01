package dcimigration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReplaceCutoverFileReplacesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("new generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplaceCutoverFile(source, target); err != nil {
		t.Fatalf("atomicReplaceCutoverFile() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new generation" {
		t.Fatalf("target content = %q, want new generation", got)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source after replacement = %v, want absent", err)
	}
}
