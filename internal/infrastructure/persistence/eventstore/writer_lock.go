package eventstore

import (
	"errors"
	"fmt"
	"os"
)

var errEventStoreWriterBusy = errors.New("event store writer is already open for this database path")

func acquireEventStoreFileLock(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("event store writer lock must not be a symlink")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open event store writer lock: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat event store writer lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("event store writer lock is not a regular file")
	}
	if err := lockEventStoreWriter(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errEventStoreWriterBusy) {
			return nil, errEventStoreWriterBusy
		}
		return nil, fmt.Errorf("acquire event store writer lock: %w", err)
	}
	return file, nil
}

func releaseEventStoreFileLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := unlockEventStoreWriter(file)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
