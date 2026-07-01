package coderloop

import (
	"encoding/json"
	"fmt"
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
	Action string         `json:"action"` // "shell_command" | "mcp_tool"
	Target string         `json:"target"` // shell_command: コマンド文字列 / mcp_tool: ツール名
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
	Type   MessageType `json:"type"`
	Intent string      `json:"intent"`
	Patch  string      `json:"patch"` // ParsePatch() が解析できる形式
	Tests  []string    `json:"tests"`
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
	Type            MessageType `json:"type"`
	Summary         string      `json:"summary"`
	ChangedFiles    []string    `json:"changed_files"`
	TestsRun        []string    `json:"tests_run"`
	RemainingRisks  []string    `json:"remaining_risks"`
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

// ParseCoderMessage は LLM 応答から CoderMessage を抽出・解析する
func ParseCoderMessage(content string) (*CoderMessage, error) {
	jsonStr, err := extractJSON(content)
	if err != nil {
		return nil, fmt.Errorf("no JSON found in coder response: %w", err)
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
		var m ReadRequestMessage
		if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
			return nil, fmt.Errorf("parse read_request: %w", err)
		}
		msg.ReadRequest = &m

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
		msg.PatchProposal = &m

	case TypeTestRequest:
		var m TestRequestMessage
		if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
			return nil, fmt.Errorf("parse test_request: %w", err)
		}
		msg.TestRequest = &m

	case TypeRevisionRequest:
		var m RevisionRequestMessage
		if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
			return nil, fmt.Errorf("parse revision_request: %w", err)
		}
		msg.RevisionRequest = &m

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

// extractJSON は文字列から最初の JSON オブジェクト（{ ... }）を取り出す
func extractJSON(s string) (string, error) {
	start := -1
	depth := 0
	inStr := false
	escape := false

	for i, ch := range s {
		if escape {
			escape = false
			continue
		}
		if inStr {
			if ch == '\\' {
				escape = true
			} else if ch == '"' {
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("no complete JSON object found")
}
