//go:build windows

package webgather

import "os/exec"

func configureWebwrightCommand(cmd *exec.Cmd) {}

func killWebwrightCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
