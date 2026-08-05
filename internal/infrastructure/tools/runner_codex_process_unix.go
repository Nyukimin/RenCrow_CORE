//go:build !windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCodexCommandCancellation puts the CLI wrapper and every subprocess
// it starts in one process group. Context cancellation must stop the actual
// Codex process too, not only its Node wrapper.
func configureCodexCommandCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
