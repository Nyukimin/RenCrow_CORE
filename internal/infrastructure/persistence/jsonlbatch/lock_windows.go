//go:build windows

package jsonlbatch

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryLockExclusive(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return errLockBusy
	}
	return err
}

func unlockExclusive(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
