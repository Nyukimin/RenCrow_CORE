//go:build !windows

package dcimigration

import (
	"errors"
	"os"
)

// syncDirectory flushes a directory entry after an atomic file operation.
// Directory fsync is available on Unix-like systems.
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
