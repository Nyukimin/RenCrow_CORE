//go:build linux || darwin

package eventstore

import (
	"fmt"
	"os"
	"syscall"
)

func eventStoreFileHasMultipleLinks(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("event store file identity is unavailable")
	}
	return stat.Nlink > 1, nil
}
