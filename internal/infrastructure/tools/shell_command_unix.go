//go:build !windows

package tools

import (
	"context"
	"os/exec"
)

func shellCommandContext(ctx context.Context, command string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "sh", "-c", command), nil
}

func shellScriptCommandContext(ctx context.Context, scriptPath string, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "sh", append([]string{scriptPath}, args...)...), nil
}
