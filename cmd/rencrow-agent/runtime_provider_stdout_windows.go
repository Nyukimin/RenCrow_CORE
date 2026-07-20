//go:build windows

package main

import (
	"io"
	"os"
)

// Windows では syscall.Dup/Dup2 が利用できないため、標準出力をそのまま通信先にする。
func protectStdout() io.Writer {
	return os.Stdout
}
