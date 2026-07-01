package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
)

// executeShellCommand はシェルコマンドを実行
func (w *workerExecutionService) executeShellCommand(
	ctx context.Context,
	cmd patch.PatchCommand,
) (string, error) {
	// タイムアウト設定
	timeout := time.Duration(w.config.CommandTimeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// コマンド実行
	command := cmd.Target
	shellCmd := workerShellCommand(ctx, command)

	// ワークスペース内で実行
	shellCmd.Dir = w.config.Workspace

	// 基本環境 + Metadataからの上書き
	shellCmd.Env = append([]string(nil), os.Environ()...)
	if env := cmd.Metadata["env"]; env != "" {
		shellCmd.Env = append(shellCmd.Env, strings.Split(env, ",")...)
	}

	output, err := shellCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("shell command failed: %w, output: %s", err, string(output))
	}

	return string(output), nil
}

func workerShellCommand(ctx context.Context, command string) *exec.Cmd {
	if _, err := exec.LookPath("bash"); err == nil {
		return exec.CommandContext(ctx, "bash", "-lc", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
