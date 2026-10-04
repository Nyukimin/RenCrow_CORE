//go:build linux

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("proc fd unavailable: %v", err)
	}
	return len(entries)
}

// TestStorageHostTokenFileDoesNotLeakHandles は remote検証がtoken fileの
// handleをcloseしていることの確認（long-running COREでのFDリーク防止）。
func TestStorageHostTokenFileDoesNotLeakHandles(t *testing.T) {
	token := filepath.Join(t.TempDir(), "storage-host.token")
	if err := os.WriteFile(token, []byte("token-value\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	body := `storage:
  host:
    mode: remote
    endpoint: "http://127.0.0.1:18820"
    token_file: "` + token + `"
`
	path := writeStorageHostConfig(t, body)
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	before := openFDCount(t)
	for i := 0; i < 50; i++ {
		if _, err := LoadConfig(path); err != nil {
			t.Fatalf("LoadConfig %d: %v", i, err)
		}
	}
	if after := openFDCount(t); after-before >= 10 {
		t.Fatalf("token file handles leaked: before=%d after=%d", before, after)
	}
}
