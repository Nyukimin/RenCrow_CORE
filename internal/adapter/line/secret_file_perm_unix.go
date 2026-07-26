//go:build !windows

package line

import "os"

// secretFilePermOK reports whether a file holding LINE secrets is closed to
// group and others. Callers keep their own error message.
func secretFilePermOK(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}
