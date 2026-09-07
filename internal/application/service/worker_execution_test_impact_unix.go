//go:build !windows

package service

import (
	"os"
	"os/exec"
	"syscall"
)

func configureWorkerProcess(command *exec.Cmd) bool {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return true
}

func signalWorkerProcess(process *os.Process, processGroup bool, signal os.Signal) error {
	if process == nil {
		return nil
	}
	if processGroup {
		if group, err := os.FindProcess(-process.Pid); err == nil {
			if err := group.Signal(signal); err == nil {
				return nil
			}
		}
	}
	return process.Signal(signal)
}

func forceKillWorkerProcessTree(process *os.Process, processGroup bool) error {
	if process == nil {
		return nil
	}
	if processGroup {
		return signalWorkerProcess(process, true, os.Kill)
	}
	return process.Kill()
}
