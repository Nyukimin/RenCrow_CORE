package jsonlbatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

var errLockBusy = errors.New("jsonl batch lock is busy")

func (s *Store) withLock(ctx context.Context, callback func() error) error {
	if callback == nil {
		return fmt.Errorf("jsonl batch lock callback is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := rejectSymlinkPath(s.lockPath); err != nil {
		return err
	}
	file, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open jsonl batch lock: %w", err)
	}
	if info, statErr := file.Stat(); statErr != nil {
		_ = file.Close()
		return fmt.Errorf("stat jsonl batch lock: %w", statErr)
	} else if !info.Mode().IsRegular() {
		_ = file.Close()
		return fmt.Errorf("jsonl batch lock is not a regular file")
	}
	locked := false
	released := false
	defer func() {
		if released {
			return
		}
		if locked {
			_ = unlockExclusive(file)
		}
		_ = file.Close()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = tryLockExclusive(file)
		if err == nil {
			locked = true
			break
		}
		if !errors.Is(err, errLockBusy) {
			return fmt.Errorf("acquire jsonl batch lock: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}

	callbackErr := callback()
	unlockErr := unlockExclusive(file)
	locked = false
	closeErr := file.Close()
	released = true
	return errors.Join(callbackErr, unlockErr, closeErr)
}
