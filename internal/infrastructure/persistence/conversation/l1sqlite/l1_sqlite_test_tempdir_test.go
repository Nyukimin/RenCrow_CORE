package l1sqlite

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func l1TestTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rencrow-l1-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	root, err := filepath.Abs(os.TempDir())
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	rel, err := filepath.Rel(root, absDir)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		t.Fatalf("unsafe temp directory: %q", absDir)
	}
	t.Cleanup(func() {
		var cleanupErr error
		for attempt := 0; attempt < 50; attempt++ {
			cleanupErr = os.RemoveAll(absDir)
			if cleanupErr == nil || errors.Is(cleanupErr, os.ErrNotExist) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("remove temp directory %q: %v", absDir, cleanupErr)
	})
	return absDir
}
