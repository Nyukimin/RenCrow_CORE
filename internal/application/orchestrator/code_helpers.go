package orchestrator

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
)

// truncate はビュワー表示用に長いテキストを切り詰める
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// 行単位で切り詰め
	lines := strings.SplitN(s, "\n", -1)
	var b strings.Builder
	for _, line := range lines {
		if b.Len()+len(line)+1 > maxLen {
			b.WriteString("\n... (truncated)")
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

// formatExecutionResult はProposalとPatchExecutionResultを整形
func formatExecutionResult(
	p *proposal.Proposal,
	result *patch.PatchExecutionResult,
) string {
	return fmt.Sprintf(`## Plan
%s

## Execution Result
- **Status**: %s
- **Executed**: %d commands
- **Failed**: %d commands
- **Success Rate**: %.1f%%%s

### Command Results%s

## Risk
%s
`,
		p.Plan(),
		executionStatusEmoji(result),
		result.ExecutedCmds,
		result.FailedCmds,
		executionSuccessRatePercent(result),
		formatGitCommitLine(result),
		formatCommandDetails(result),
		p.Risk(),
	)
}

func executionSuccessRatePercent(result *patch.PatchExecutionResult) float64 {
	if result == nil {
		return 0
	}
	if result.ExecutedCmds == 0 && result.Success {
		return 100
	}
	return result.SuccessRate() * 100
}

func executionStatusEmoji(result *patch.PatchExecutionResult) string {
	if result.Success {
		return "✅"
	}
	return "⚠️"
}

func formatGitCommitLine(result *patch.PatchExecutionResult) string {
	if result.GitCommit == "" || result.GitCommit == "no-changes" {
		return ""
	}
	shortHash := result.GitCommit
	if len(shortHash) > 8 {
		shortHash = shortHash[:8]
	}
	return fmt.Sprintf("\n- **Git Commit**: `%s`", shortHash)
}

func formatCommandDetails(result *patch.PatchExecutionResult) string {
	commandDetails := ""
	for i, cmdResult := range result.Results {
		status := "✅"
		if !cmdResult.Success {
			status = "❌"
		}
		commandDetails += fmt.Sprintf("\n%d. %s `%s` %s",
			i+1, status, cmdResult.Command.Action, cmdResult.Command.Target)
		if cmdResult.Error != "" {
			commandDetails += fmt.Sprintf("\n   Error: %s", cmdResult.Error)
		}
	}
	return commandDetails
}
