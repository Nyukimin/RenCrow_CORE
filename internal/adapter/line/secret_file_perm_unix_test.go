//go:build !windows

package line

import (
	"os"
	"path/filepath"
	"testing"
)

func assertTargetFilePermissions(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("target file mode = %o, want 600", perm)
	}
}

func TestDirectUserTargetStoreRejectsPermissiveExistingFile(t *testing.T) {
	store := NewDirectUserTargetStore(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(store.Path(), []byte("U0123456789abcdef0123456789abcdef\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Chmod(store.Path(), 0o644); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	if _, err := store.Load(); err == nil {
		t.Fatal("Load should reject a target file readable by group/others")
	}
}
