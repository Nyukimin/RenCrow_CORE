//go:build windows

package line

import "os"

// secretFilePermOK always accepts on Windows.
//
// NTFS does not expose POSIX permission bits. os.FileInfo.Mode().Perm()
// reports 0666 for a writable file and 0444 for a read-only one regardless of
// the actual ACL, so the Unix check would reject every file and disable LINE
// notification entirely. Access control is delegated to the NTFS ACL inherited
// from the workspace directory.
func secretFilePermOK(_ os.FileInfo) bool {
	return true
}
