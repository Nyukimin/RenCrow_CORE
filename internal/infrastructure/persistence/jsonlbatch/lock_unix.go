//go:build linux || darwin

package jsonlbatch

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLockExclusive(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
		return errLockBusy
	}
	return err
}

func unlockExclusive(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
