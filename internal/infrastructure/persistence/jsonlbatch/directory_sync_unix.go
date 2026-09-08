//go:build linux || darwin

package jsonlbatch

import (
	"errors"
	"fmt"
	"os"
)

// syncDirectory flushes directory metadata after New creates the lock, WAL,
// and data entries. Linux and macOS expose a directory file descriptor whose
// Sync has the required durability semantics.
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open jsonl batch directory: %w", err)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync jsonl batch directory: %w", err)
	}
	return nil
}
