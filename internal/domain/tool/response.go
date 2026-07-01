package tool

import (
	"encoding/json"
	"time"
)

// ToolError は構造化エラー値オブジェクト（TOOL_CONTRACT 1.3）
type ToolError struct {
	Code    ErrorCode      `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error は error インターフェースを満たす
func (e *ToolError) Error() string {
	return string(e.Code) + ": " + e.Message
}

// ToolResponse は構造化出力値オブジェクト（TOOL_CONTRACT 1.2）
type ToolResponse struct {
	Result      any            `json:"result,omitempty"`
	Error       *ToolError     `json:"error,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"` // 構造化データ（KB保存等に使用）
	GeneratedAt time.Time      `json:"generated_at"`
}

// NewSuccess は成功レスポンスを生成する
func NewSuccess(result any) *ToolResponse {
	return &ToolResponse{
		Result:      result,
		GeneratedAt: time.Now(),
	}
}

// NewError はエラーレスポンスを生成する
func NewError(code ErrorCode, message string, details map[string]any) *ToolResponse {
	return &ToolResponse{
		Error: &ToolError{
			Code:    code,
			Message: message,
			Details: details,
		},
		GeneratedAt: time.Now(),
	}
}

// IsError はエラーレスポンスかどうかを返す
func (r *ToolResponse) IsError() bool {
	return r.Error != nil
}

// String は結果を文字列として返す
// 成功時: Result が string ならそのまま、それ以外は JSON
// エラー時: "CODE: message"
func (r *ToolResponse) String() string {
	if r.IsError() {
		return r.Error.Error()
	}
	if s, ok := r.Result.(string); ok {
		return s
	}
	b, err := json.Marshal(r.Result)
	if err != nil {
		return ""
	}
	return string(b)
}

// JSON はレスポンス全体を JSON バイト列として返す
func (r *ToolResponse) JSON() ([]byte, error) {
	return json.Marshal(r)
}
