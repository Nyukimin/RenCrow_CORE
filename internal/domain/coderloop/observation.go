package coderloop

import (
	"encoding/json"
	"fmt"
)

const maxObservationOutputBytes = 2048

// ObservationAction は Worker に依頼する観測アクション（実行前）
type ObservationAction struct {
	Action string         `json:"action"` // "shell_command" | "mcp_tool"
	Target string         `json:"target"` // shell_command: コマンド / mcp_tool: ツール名
	Args   map[string]any `json:"args,omitempty"` // mcp_tool のみ使用
}

// ObservationActionResult は単一アクションの実行結果
type ObservationActionResult struct {
	Action    string `json:"action"`
	Target    string `json:"target"`
	Status    string `json:"status"` // "ok" | "error"
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ObservationResult は Worker → Coder への観測返却
type ObservationResult struct {
	Type    string                    `json:"type"` // 常に "observation"
	Turn    int                       `json:"turn"`
	Results []ObservationActionResult `json:"results"`
}

// NewObservationResult は ObservationResult を生成する
func NewObservationResult(turn int, results []ObservationActionResult) *ObservationResult {
	return &ObservationResult{
		Type:    "observation",
		Turn:    turn,
		Results: results,
	}
}

// ToJSON は ObservationResult を JSON 文字列に変換する
func (o *ObservationResult) ToJSON() string {
	b, err := json.Marshal(o)
	if err != nil {
		return fmt.Sprintf(`{"type":"observation","turn":%d,"results":[],"error":"%s"}`, o.Turn, err.Error())
	}
	return string(b)
}

// TruncateOutput は output を maxObservationOutputBytes に丸める
func TruncateOutput(output string) (string, bool) {
	if len(output) <= maxObservationOutputBytes {
		return output, false
	}
	return output[:maxObservationOutputBytes] + "\n...[truncated]", true
}

// NewObservationActionResult は単一アクションの結果を生成する（自動トリム）
func NewObservationActionResult(action, target, output string, err error) ObservationActionResult {
	if err != nil {
		return ObservationActionResult{
			Action: action,
			Target: target,
			Status: "error",
			Output: err.Error(),
		}
	}
	trimmed, truncated := TruncateOutput(output)
	return ObservationActionResult{
		Action:    action,
		Target:    target,
		Status:    "ok",
		Output:    trimmed,
		Truncated: truncated,
	}
}

// ActionsFromWorkerActions は CoderMessage の WorkerAction スライスを ObservationAction に変換する
func ActionsFromWorkerActions(actions []WorkerAction) []ObservationAction {
	out := make([]ObservationAction, len(actions))
	for i, a := range actions {
		out[i] = ObservationAction{Action: a.Action, Target: a.Target, Args: a.Args}
	}
	return out
}
