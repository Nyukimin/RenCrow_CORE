package storagehost

import (
	"errors"
	"fmt"
	"os"
)

// errLockBusy is returned when another process already holds the journal
// directory writer lock.
var errLockBusy = errors.New("storagehost: journal directory writer lock is busy")

// writerLockFile is the exclusive process-level lock guarding the storage
// host durable directory. It is acquired before the writer generation is
// advanced and before the journal is recovered, and held for the whole
// handler lifetime: the storage host is the single writer for its files.
type writerLock struct {
	path *os.File
}

func acquireWriterLock(lockPath string) (*writerLock, error) {
	if linkInfo, err := os.Lstat(lockPath); err == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("storagehost: writer lock must not be a symlink: %s", lockPath)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("storagehost: open writer lock: %w", err)
	}
	if info, err := f.Stat(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("storagehost: stat writer lock: %w", err)
	} else if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("storagehost: writer lock is not a regular file")
	}
	if err := tryLockExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockBusy) {
			return nil, errLockBusy
		}
		return nil, fmt.Errorf("storagehost: acquire writer lock: %w", err)
	}
	return &writerLock{path: f}, nil
}

func (w *writerLock) release() error {
	if w == nil || w.path == nil {
		return nil
	}
	unlockErr := unlockExclusive(w.path)
	closeErr := w.path.Close()
	w.path = nil
	return errors.Join(unlockErr, closeErr)
}
