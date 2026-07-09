package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func (w *workerExecutionService) executeCommands(ctx context.Context, jobID task.JobID, commands []patch.PatchCommand) *patch.PatchExecutionResult {
	if w.config.ParallelExecution {
		return w.executeParallel(ctx, jobID, commands)
	}
	return w.executeSequential(ctx, jobID, commands)
}

// executeSequential はコマンドを順次実行
func (w *workerExecutionService) executeSequential(ctx context.Context, jobID task.JobID, commands []patch.PatchCommand) *patch.PatchExecutionResult {
	result := patch.NewPatchExecutionResult()
	for i, cmd := range commands {
		cmdResult := w.executeCommand(ctx, jobID, cmd, i)
		result.AddResult(cmdResult)

		if !cmdResult.Success && w.config.StopOnError {
			fmt.Printf("[Worker] Execution stopped on error at command %d\n", i)
			break
		}
	}
	return result
}

// executeParallel はType-Based Phased Executionで並列実行
// file_edit → shell_command → git_operation のフェーズ順
// 同フェーズ内は goroutine + semaphore で並列化
func (w *workerExecutionService) executeParallel(ctx context.Context, jobID task.JobID, commands []patch.PatchCommand) *patch.PatchExecutionResult {
	// フェーズ分類
	phases := []patch.Type{patch.TypeFileEdit, patch.TypeShellCommand, patch.TypeGitOperation}
	grouped := make(map[patch.Type][]indexedCommand)

	for i, cmd := range commands {
		grouped[cmd.Type] = append(grouped[cmd.Type], indexedCommand{index: i, cmd: cmd})
	}

	maxParallel := w.config.MaxParallelism
	if maxParallel <= 0 {
		maxParallel = 4
	}

	result := patch.NewPatchExecutionResult()

	for _, phase := range phases {
		cmds := grouped[phase]
		if len(cmds) == 0 {
			continue
		}

		fmt.Printf("[Worker] Phase %s: %d commands (parallel=%d)\n", phase, len(cmds), maxParallel)

		// セマフォ付き並列実行
		sem := make(chan struct{}, maxParallel)
		var mu sync.Mutex
		var wg sync.WaitGroup

		phaseResults := make([]patch.CommandResult, len(cmds))

		for j, ic := range cmds {
			wg.Add(1)
			go func(idx int, ic indexedCommand) {
				defer wg.Done()

				sem <- struct{}{}        // acquire
				defer func() { <-sem }() // release

				cmdResult := w.executeCommand(ctx, jobID, ic.cmd, ic.index)
				mu.Lock()
				phaseResults[idx] = cmdResult
				mu.Unlock()
			}(j, ic)
		}

		wg.Wait()

		// 結果を元のインデックス順に追加
		for _, cr := range phaseResults {
			result.AddResult(cr)
		}

		// フェーズ内で失敗があり StopOnError の場合は次フェーズへ進まない
		if w.config.StopOnError && result.FailedCmds > 0 {
			fmt.Printf("[Worker] Phase %s had failures, stopping\n", phase)
			break
		}
	}

	return result
}

// indexedCommand はインデックス付きコマンド
type indexedCommand struct {
	index int
	cmd   patch.PatchCommand
}
