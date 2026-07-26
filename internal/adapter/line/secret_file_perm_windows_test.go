//go:build windows

package line

import "testing"

// assertTargetFilePermissions is a no-op on Windows. NTFS does not expose
// POSIX permission bits, so the 0600 assertion used on Unix cannot hold here.
// See secret_file_perm_windows.go for the production counterpart.
func assertTargetFilePermissions(t *testing.T, _ string) {
	t.Helper()
}
