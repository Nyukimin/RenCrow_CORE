//go:build darwin

package main

import (
	"os"
	"syscall"
)

func openSystemJobsPlistFile(path string) (*os.File, error) {
	return os.OpenFile(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
