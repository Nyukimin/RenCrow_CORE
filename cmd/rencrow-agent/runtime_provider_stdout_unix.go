//go:build !windows

package main

import (
	"io"
	"os"
	"syscall"
)

// protectStdout は stdout fd を通信専用 fd として早期確保し、fd1 を stderr にリダイレクトする。
// これにより CGO ライブラリ等の想定外の stdout 書き込みから JSON 通信チャネルを保護する。
func protectStdout() io.Writer {
	fd, err := syscall.Dup(syscall.Stdout)
	if err != nil {
		return os.Stdout
	}
	if err := syscall.Dup2(syscall.Stderr, syscall.Stdout); err != nil {
		syscall.Close(fd)
		return os.Stdout
	}
	return os.NewFile(uintptr(fd), "json-out")
}
