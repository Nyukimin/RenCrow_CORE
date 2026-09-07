//go:build linux || darwin

package task

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockTaskWriter(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrTaskWriterBusy
		}
		return err
	}
	return nil
}

func unlockTaskWriter(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
