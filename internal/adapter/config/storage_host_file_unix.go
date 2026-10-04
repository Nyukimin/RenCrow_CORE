//go:build !windows

package config

import (
	"errors"
	"os"
)

var errStorageHostTokenUnreadable = errors.New("storage.host.token_file is unreadable")

// validateStorageHostTokenFileAccess rejects a token file that group or others
// can read. Windows uses the DACL rule in storage_host_file_windows.go because
// POSIX mode bits are not the Windows confidentiality contract.
func validateStorageHostTokenFileAccess(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("storage.host.token_file must not be accessible by group or others: " + path)
	}
	return nil
}
