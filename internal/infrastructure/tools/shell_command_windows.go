//go:build windows

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func shellCommandContext(ctx context.Context, command string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command), nil
}

func shellScriptCommandContext(ctx context.Context, scriptPath string, args ...string) (*exec.Cmd, error) {
	if sh, err := exec.LookPath("sh"); err == nil {
		return exec.CommandContext(ctx, sh, append([]string{scriptPath}, args...)...), nil
	}
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Git", "usr", "bin", "bash.exe"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return exec.CommandContext(ctx, candidate, append([]string{scriptPath}, args...)...), nil
		}
	}
	return nil, fmt.Errorf("POSIX shell required for registered .sh tool")
}
