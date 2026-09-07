//go:build windows

package service

import (
	"os"
	"os/exec"
	"strconv"
)

func configureWorkerProcess(*exec.Cmd) bool {
	return false
}

func signalWorkerProcess(process *os.Process, _ bool, signal os.Signal) error {
	if process == nil {
		return nil
	}
	return process.Signal(signal)
}

func forceKillWorkerProcessTree(process *os.Process, _ bool) error {
	if process == nil {
		return nil
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(process.Pid)).Run()
}
