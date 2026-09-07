package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
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
	if runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if strings.TrimSpace(root) == "" {
				continue
			}
			bashPath := filepath.Join(root, "Git", "bin", "bash.exe")
			if _, err := os.Stat(bashPath); err == nil {
				return exec.CommandContext(ctx, bashPath, "-lc", command)
			}
		}
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return exec.CommandContext(ctx, "bash", "-lc", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

const workerCommandGracePeriod = 2 * time.Second
const maxWorkerCommandOutputBytes = 64 * 1024

type workerCommandBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *workerCommandBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxWorkerCommandOutputBytes - b.buffer.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, err := b.buffer.Write(p)
	return n, err
}
func (b *workerCommandBuffer) String() string {
	if b.truncated {
		return b.buffer.String() + "\n[output truncated]"
	}
	return b.buffer.String()
}

func runWorkerCommand(ctx context.Context, workspace, binary string, args ...string) (string, string, error) {
	if ctx == nil {
		return "", "", fmt.Errorf("Worker command context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = workspace
	cmd.Env = append([]string(nil), os.Environ()...)
	if binary == "git" {
		// Git observations must not inherit another repository/index/config.
		// Preserve all unrelated process environment (including repo-local temp).
		clean := cmd.Env[:0]
		for _, entry := range cmd.Env {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
				clean = append(clean, entry)
			}
		}
		cmd.Env = append(clean, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	}

	var stdout, stderr workerCommandBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	processGroup := configureWorkerProcess(cmd)
	if err := cmd.Start(); err != nil {
		return stdout.String(), stderr.String(), err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case err := <-wait:
		if err == nil {
			err = ctx.Err()
		}
		return stdout.String(), stderr.String(), err
	case <-ctx.Done():
		_ = signalWorkerProcess(cmd.Process, processGroup, os.Interrupt)
		timer := time.NewTimer(workerCommandGracePeriod)
		defer timer.Stop()
		select {
		case err := <-wait:
			return stdout.String(), stderr.String(), fmt.Errorf("%w: %v", ctx.Err(), err)
		case <-timer.C:
			if err := forceKillWorkerProcessTree(cmd.Process, processGroup); err != nil {
				_ = cmd.Process.Kill()
			}
			err := <-wait
			if err == nil {
				return stdout.String(), stderr.String(), ctx.Err()
			}
			return stdout.String(), stderr.String(), fmt.Errorf("%w: %v", ctx.Err(), err)
		}
	}
}
