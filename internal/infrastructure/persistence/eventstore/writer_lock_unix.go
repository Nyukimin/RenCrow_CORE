//go:build linux || darwin

package eventstore

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockEventStoreWriter(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
		return errEventStoreWriterBusy
	}
	return err
}

func unlockEventStoreWriter(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
