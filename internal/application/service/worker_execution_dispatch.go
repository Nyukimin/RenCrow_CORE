package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

// executeCommand は単一コマンドを実行
func (w *workerExecutionService) executeCommand(
	ctx context.Context,
	jobID task.JobID,
	cmd patch.PatchCommand,
	index int,
) patch.CommandResult {
	start := time.Now()
	var output string
	var err error

	// Type別に処理を振り分け
	switch cmd.Type {
	case patch.TypeFileEdit:
		output, err = w.executeFileEdit(ctx, cmd)
	case patch.TypeShellCommand:
		output, err = w.executeShellCommand(ctx, cmd)
	case patch.TypeGitOperation:
		output, err = w.executeGitOperation(ctx, cmd)
	default:
		err = fmt.Errorf("unknown command type: %s", cmd.Type)
	}

	duration := time.Since(start)
	success := err == nil

	// ログ出力
	if success {
		fmt.Printf("[Worker] Command %d executed: %s %s (%.2fs)\n",
			index, cmd.Type, cmd.Action, duration.Seconds())
	} else {
		fmt.Printf("[Worker] Command %d failed: %s %s - %v\n",
			index, cmd.Type, cmd.Action, err)
	}

	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	return patch.CommandResult{
		Command: cmd,
		Success: success,
		Output:  output,
		Error:   errStr,
	}
}
