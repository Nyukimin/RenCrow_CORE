package service

import (
	"fmt"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

// showExecutionSummary は実行前サマリを表示
func (w *workerExecutionService) showExecutionSummary(jobID task.JobID, commands []patch.PatchCommand) {
	fileEdits := 0
	shellCmds := 0
	gitOps := 0
	for _, cmd := range commands {
		switch cmd.Type {
		case patch.TypeFileEdit:
			fileEdits++
		case patch.TypeShellCommand:
			shellCmds++
		case patch.TypeGitOperation:
			gitOps++
		}
	}

	fmt.Printf("[Worker] Execution Summary (JobID: %s)\n", jobID.String())
	fmt.Printf("  Total Commands: %d\n", len(commands))
	fmt.Printf("  File Edits: %d\n", fileEdits)
	fmt.Printf("  Shell Commands: %d\n", shellCmds)
	fmt.Printf("  Git Operations: %d\n", gitOps)
}
