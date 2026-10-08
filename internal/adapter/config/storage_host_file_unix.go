//go:build !windows

package config

import (
	"errors"
	"os"
)

var errStorageHostTokenUnreadable = errors.New("storage.host.token_file is unreadable")

// validateConfidentialFileAccess rejects a confidential file (the storage host
// bearer token or the Human relay HMAC key) that group or others can read.
// label names the setting in the error. Windows uses the DACL rule in
// storage_host_file_windows.go because POSIX mode bits are not the Windows
// confidentiality contract.
func validateConfidentialFileAccess(label, path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New(label + " must not be accessible by group or others: " + path)
	}
	return nil
}
