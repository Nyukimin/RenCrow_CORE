package coderloop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
)

// MessageType は Coder が出力するメッセージの種別
type MessageType string

const (
	TypeReadRequest     MessageType = "read_request"
	TypePlan            MessageType = "plan"
	TypePatchProposal   MessageType = "patch_proposal"
	TypeTestRequest     MessageType = "test_request"
	TypeRevisionRequest MessageType = "revision_request"
	TypeFinalReport     MessageType = "final_report"
)

// WorkerAction は Coder が Worker に依頼する単一アクション
type WorkerAction struct {
	Action string         `json:"action"`         // "shell_command" | "mcp_tool"
	Target string         `json:"target"`         // shell_command: コマンド文字列 / mcp_tool: ツール名
	Args   map[string]any `json:"args,omitempty"` // mcp_tool のみ使用
}

// ReadRequestMessage はリポジトリ読み取り依頼
type ReadRequestMessage struct {
	Type    MessageType    `json:"type"`
	Actions []WorkerAction `json:"actions"`
}

// PlanMessage は作業計画
type PlanMessage struct {
	Type        MessageType `json:"type"`
	TaskSummary string      `json:"task_summary"`
	Steps       []string    `json:"steps"`
	Risk        []string    `json:"risk"`
}

// PatchProposalMessage はパッチ案
type PatchProposalMessage struct {
	Type     MessageType `json:"type"`
	Intent   string      `json:"intent"`
	Patch    string      `json:"patch"` // ParsePatch() が解析できる形式
	Tests    []string    `json:"tests"`
	TestHint *TestHint   `json:"test_hint,omitempty"` // owner test-impact への advisory hint。選択権は持たない
}

// TestHint は Coder が提示できる test-impact の advisory 情報です。
// Owner CLI はこの情報で選択範囲を狭めません。
type TestHint struct {
	ChangedSurface       []string `json:"changed_surface,omitempty"`
	ExpectedImpact       string   `json:"expected_impact,omitempty"`
	RecommendedTestTier  string   `json:"recommended_tier,omitempty"`
	RecommendedTestSteps []string `json:"recommended_steps,omitempty"`
	Risk                 string   `json:"risk,omitempty"`
}

// TestRequestMessage はテスト実行依頼
type TestRequestMessage struct {
	Type    MessageType    `json:"type"`
	Actions []WorkerAction `json:"actions"`
}

// RevisionRequestMessage は修正依頼（テスト失敗時）
type RevisionRequestMessage struct {
	Type    MessageType    `json:"type"`
	Reason  string         `json:"reason"`
	Actions []WorkerAction `json:"actions"`
}

// FinalReportMessage は完了報告
type FinalReportMessage struct {
	Type           MessageType `json:"type"`
	Summary        string      `json:"summary"`
	ChangedFiles   []string    `json:"changed_files"`
	TestsRun       []string    `json:"tests_run"`
	RemainingRisks []string    `json:"remaining_risks"`
}

// CoderMessage は Coder の出力を保持する共用体
type CoderMessage struct {
	Type MessageType `json:"type"`
	Raw  string      // 元の JSON 文字列

	ReadRequest     *ReadRequestMessage
	Plan            *PlanMessage
	PatchProposal   *PatchProposalMessage
	TestRequest     *TestRequestMessage
	RevisionRequest *RevisionRequestMessage
	FinalReport     *FinalReportMessage
}

// ParseCoderMessage は単一の JSON オブジェクトを検証して CoderMessage に解析する
func ParseCoderMessage(content string) (*CoderMessage, error) {
	jsonStr := strings.TrimSpace(content)
	fields, err := parseJSONFields(jsonStr)
	if err != nil {
		return nil, fmt.Errorf("parse coder response object: %w", err)
	}

	// type フィールドだけ先読み
	var peek struct {
		Type MessageType `json:"type"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &peek); err != nil {
		return nil, fmt.Errorf("failed to parse message type: %w", err)
	}

	msg := &CoderMessage{Type: peek.Type, Raw: jsonStr}

	switch peek.Type {
	case TypeReadRequest:
		actions, err := parseActions(fields["actions"], true)
		if err != nil {
			return nil, fmt.Errorf("validate read_request actions: %w", err)
		}
		msg.ReadRequest = &ReadRequestMessage{Type: peek.Type, Actions: actions}

	case TypePlan:
		var m PlanMessage
		if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
			return nil, fmt.Errorf("parse plan: %w", err)
		}
		msg.Plan = &m

	case TypePatchProposal:
		var m PatchProposalMessage
		if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
			return nil, fmt.Errorf("parse patch_proposal: %w", err)
		}
		if strings.TrimSpace(m.Intent) == "" {
			return nil, fmt.Errorf("patch_proposal intent must not be blank")
		}
		if strings.TrimSpace(m.Patch) == "" {
			return nil, fmt.Errorf("patch_proposal patch must not be blank")
		}
		commands, err := patch.ParsePatch(m.Patch)
		if err != nil {
			return nil, fmt.Errorf("parse patch_proposal patch: %w", err)
		}
		if len(commands) == 0 {
			return nil, fmt.Errorf("patch_proposal patch contains no commands")
		}
		msg.PatchProposal = &m

	case TypeTestRequest:
		actions, err := parseActions(fields["actions"], true)
		if err != nil {
			return nil, fmt.Errorf("validate test_request actions: %w", err)
		}
		msg.TestRequest = &TestRequestMessage{Type: peek.Type, Actions: actions}

	case TypeRevisionRequest:
		m := &RevisionRequestMessage{Type: peek.Type}
		if reason, ok := fields["reason"]; ok {
			if err := json.Unmarshal(reason, &m.Reason); err != nil {
				return nil, fmt.Errorf("parse revision_request reason: %w", err)
			}
		}
		if actions, ok := fields["actions"]; ok {
			parsedActions, err := parseActions(actions, false)
			if err != nil {
				return nil, fmt.Errorf("validate revision_request actions: %w", err)
			}
			m.Actions = parsedActions
		}
		msg.RevisionRequest = m

	case TypeFinalReport:
		var m FinalReportMessage
		if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
			return nil, fmt.Errorf("parse final_report: %w", err)
		}
		msg.FinalReport = &m

	default:
		return nil, fmt.Errorf("unknown message type: %q", peek.Type)
	}

	return msg, nil
}

func parseJSONFields(jsonStr string) (map[string]json.RawMessage, error) {
	if jsonStr == "" || jsonStr[0] != '{' {
		return nil, fmt.Errorf("response must be one JSON object")
	}
	if err := rejectAmbiguousObjectFields(json.RawMessage(jsonStr), []string{
		"type", "actions", "task_summary", "steps", "risk", "intent", "patch", "tests",
		"test_hint", "reason", "summary", "changed_files", "tests_run", "remaining_risks",
	}); err != nil {
		return nil, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &fields); err != nil {
		return nil, fmt.Errorf("invalid single JSON object: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("response must be one JSON object")
	}
	return fields, nil
}

func parseActions(rawActions json.RawMessage, requireNonempty bool) ([]WorkerAction, error) {
	trimmedActions := strings.TrimSpace(string(rawActions))
	if len(trimmedActions) == 0 || trimmedActions[0] != '[' {
		return nil, fmt.Errorf("actions must be an array")
	}

	var rawActionsList []json.RawMessage
	if err := json.Unmarshal(rawActions, &rawActionsList); err != nil {
		return nil, fmt.Errorf("decode actions: %w", err)
	}
	if requireNonempty && len(rawActionsList) == 0 {
		return nil, fmt.Errorf("at least one action is required")
	}

	actions := make([]WorkerAction, len(rawActionsList))
	for i, rawAction := range rawActionsList {
		action, err := parseWorkerAction(rawAction)
		if err != nil {
			return nil, fmt.Errorf("actions[%d]: %w", i, err)
		}
		actions[i] = action
	}
	return actions, nil
}

func parseWorkerAction(rawAction json.RawMessage) (WorkerAction, error) {
	if err := rejectAmbiguousObjectFields(rawAction, []string{"action", "target", "args"}); err != nil {
		return WorkerAction{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawAction, &fields); err != nil {
		return WorkerAction{}, fmt.Errorf("decode action object: %w", err)
	}
	if fields == nil {
		return WorkerAction{}, fmt.Errorf("action must be an object")
	}

	var action WorkerAction
	if rawAction, ok := fields["action"]; ok {
		if err := json.Unmarshal(rawAction, &action.Action); err != nil {
			return WorkerAction{}, fmt.Errorf("action must be a string: %w", err)
		}
	}
	if rawTarget, ok := fields["target"]; ok {
		if err := json.Unmarshal(rawTarget, &action.Target); err != nil {
			return WorkerAction{}, fmt.Errorf("target must be a string: %w", err)
		}
	}

	switch action.Action {
	case "shell_command", "mcp_tool":
	default:
		return WorkerAction{}, fmt.Errorf("unsupported action %q", action.Action)
	}
	if strings.TrimSpace(action.Target) == "" {
		return WorkerAction{}, fmt.Errorf("target must not be blank")
	}
	rawArgs, hasArgs := fields["args"]
	if !hasArgs {
		return action, nil
	}
	switch action.Action {
	case "shell_command":
		var args map[string]any
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return WorkerAction{}, fmt.Errorf("shell_command args must be an object: %w", err)
		}
		if len(args) != 0 {
			return WorkerAction{}, fmt.Errorf("shell_command does not use args")
		}
		action.Args = args
	case "mcp_tool":
		var args map[string]any
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return WorkerAction{}, fmt.Errorf("mcp_tool args must be an object: %w", err)
		}
		if args == nil {
			return WorkerAction{}, fmt.Errorf("mcp_tool args must be an object")
		}
		action.Args = args
	}
	return action, nil
}

func rejectAmbiguousObjectFields(rawObject json.RawMessage, knownFields []string) error {
	decoder := json.NewDecoder(bytes.NewReader(rawObject))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read JSON object: %w", err)
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return fmt.Errorf("value must be a JSON object")
	}

	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("read JSON object field: %w", err)
		}
		field, ok := token.(string)
		if !ok {
			return fmt.Errorf("JSON object field name is not a string")
		}
		if _, exists := seen[field]; exists {
			return fmt.Errorf("duplicate JSON object field %q", field)
		}
		seen[field] = struct{}{}
		for _, known := range knownFields {
			if field != known && strings.EqualFold(field, known) {
				return fmt.Errorf("JSON object field %q conflicts with %q", field, known)
			}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode JSON object field %q: %w", field, err)
		}
	}

	token, err = decoder.Token()
	if err != nil {
		return fmt.Errorf("close JSON object: %w", err)
	}
	closing, ok := token.(json.Delim)
	if !ok || closing != '}' {
		return fmt.Errorf("JSON object is not closed")
	}
	return nil
}
