package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// executeShell はシェルコマンドを実行
func (r *ToolRunner) executeShell(ctx context.Context, args map[string]interface{}) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", fmt.Errorf("'command' argument is required and must be a string")
	}

	// dry-run: コマンド表示のみ（実行しない）
	mode, _ := args["mode"].(string)
	if mode == "plan" {
		return fmt.Sprintf("[DRY-RUN] shell\ncommand: %s\naction: would execute via sh -c", command), nil
	}

	if blocked, reason := registeredToolBlockedLine(command); blocked {
		return "", &tool.ToolError{
			Code:    tool.ErrPermissionDenied,
			Message: "command blocked by shell command gate: " + reason,
			Details: map[string]any{"command": command},
		}
	}

	// 許可コマンドリストチェック
	if len(r.config.AllowedShellCommands) > 0 {
		if !r.isShellCommandAllowed(command) {
			return "", &tool.ToolError{
				Code:    tool.ErrPermissionDenied,
				Message: "command not in allowed list",
				Details: map[string]any{"command": command},
			}
		}
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command failed: %w, output: %s", err, string(output))
	}

	return string(output), nil
}

// isShellCommandAllowed は許可コマンドリストに含まれるか判定する。
// シェルメタ文字によるコマンドチェーニングは拒否する。
func (r *ToolRunner) isShellCommandAllowed(command string) bool {
	trimmed := strings.TrimSpace(command)
	for _, meta := range shellMetachars {
		if strings.Contains(trimmed, meta) {
			return false
		}
	}
	for _, prefix := range r.config.AllowedShellCommands {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}
