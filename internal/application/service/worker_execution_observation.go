package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/coderloop"
)

// ObservationAction は ExecuteObservation に渡す単一アクション（coderloop 型のエイリアス）
type ObservationAction = coderloop.ObservationAction

// ObservationActionResult は ExecuteObservation の単一結果（coderloop 型のエイリアス）
type ObservationActionResult = coderloop.ObservationActionResult

// observationAllowList は観測フェーズで許可するコマンドプレフィックス
var observationAllowList = []string{
	"git grep", "git show", "git log", "git diff", "git ls-files", "git status",
	"cat ", "find ", "head ", "tail ", "wc ",
	"go test", "go build", "go vet",
	"grep ",
}

// observationDenyList は観測フェーズで禁止するコマンドプレフィックス
var observationDenyList = []string{
	"rm ", "rmdir",
	"mv ", "cp ",
	"git commit", "git reset", "git checkout", "git push", "git merge",
	"chmod", "chown",
}

// ExecuteObservation は読み取り専用アクションを実行して観測結果を返す
func (w *workerExecutionService) ExecuteObservation(
	ctx context.Context,
	actions []ObservationAction,
) ([]ObservationActionResult, error) {
	results := make([]ObservationActionResult, 0, len(actions))
	for _, a := range actions {
		results = append(results, w.executeSingleObservation(ctx, a))
	}
	return results, nil
}

func (w *workerExecutionService) executeSingleObservation(
	ctx context.Context,
	a ObservationAction,
) ObservationActionResult {
	switch a.Action {
	case "shell_command":
		if err := checkObservationCommand(a.Target); err != nil {
			return coderloop.NewObservationActionResult(a.Action, a.Target, "", err)
		}
		cmd := workerShellCommand(ctx, a.Target)
		cmd.Dir = w.config.Workspace
		output, err := cmd.CombinedOutput()
		return coderloop.NewObservationActionResult(a.Action, a.Target, string(output), err)

	case "mcp_tool":
		if w.mcpCaller == nil {
			return coderloop.NewObservationActionResult(a.Action, a.Target, "",
				fmt.Errorf("mcp_tool requested but MCP caller not configured"))
		}
		args := a.Args
		if args == nil {
			args = map[string]any{}
		}
		output, err := w.mcpCaller.CallTool(ctx, a.Target, args)
		return coderloop.NewObservationActionResult(a.Action, a.Target, output, err)

	default:
		return coderloop.NewObservationActionResult(a.Action, a.Target, "",
			fmt.Errorf("unsupported observation action: %q", a.Action))
	}
}

func checkObservationCommand(target string) error {
	trimmed := strings.TrimSpace(strings.ToLower(target))
	for _, deny := range observationDenyList {
		if strings.HasPrefix(trimmed, strings.TrimSpace(deny)) {
			return fmt.Errorf("command not allowed in observation phase: %q", target)
		}
	}
	for _, allow := range observationAllowList {
		if strings.HasPrefix(trimmed, strings.TrimSpace(allow)) {
			return nil
		}
	}
	return fmt.Errorf("command not in observation allowlist: %q", target)
}
