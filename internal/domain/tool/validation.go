package tool

import (
	"strings"
	"unicode"
)

// ValidatePath はパストラバーサルを検出する（TOOL_CONTRACT 2.2）
func ValidatePath(path string) *ToolError {
	if strings.Contains(path, "..") {
		return &ToolError{
			Code:    ErrValidationFailed,
			Message: "path contains traversal sequence",
			Details: map[string]any{"field": "path", "value": path},
		}
	}
	return nil
}

// ValidateNoControlChars は制御文字を検出する（\n, \t, \r は許可）
func ValidateNoControlChars(s string) *ToolError {
	for i, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return &ToolError{
				Code:    ErrValidationFailed,
				Message: "input contains control character",
				Details: map[string]any{"position": i, "char": r},
			}
		}
	}
	return nil
}

// ValidateID は ID の汚染を検出する（?, #, /, \ を拒否）
func ValidateID(id string) *ToolError {
	for _, c := range "?#/\\" {
		if strings.ContainsRune(id, c) {
			return &ToolError{
				Code:    ErrValidationFailed,
				Message: "ID contains forbidden character",
				Details: map[string]any{"field": "id", "char": string(c)},
			}
		}
	}
	return nil
}

// ValidateLength は文字列の長さ制限をチェックする
func ValidateLength(s string, maxLen int) *ToolError {
	if len(s) > maxLen {
		return &ToolError{
			Code:    ErrValidationFailed,
			Message: "input exceeds maximum length",
			Details: map[string]any{"max": maxLen, "actual": len(s)},
		}
	}
	return nil
}

// ValidateNoDoubleEncoding は二重エンコードを検出する（%25 等）
func ValidateNoDoubleEncoding(s string) *ToolError {
	if strings.Contains(s, "%25") {
		return &ToolError{
			Code:    ErrValidationFailed,
			Message: "input contains double-encoded characters",
			Details: map[string]any{"pattern": "%25"},
		}
	}
	return nil
}

// ValidateNotEmpty は空文字列をチェックする
func ValidateNotEmpty(s string, fieldName string) *ToolError {
	if strings.TrimSpace(s) == "" {
		return &ToolError{
			Code:    ErrValidationFailed,
			Message: fieldName + " is required",
			Details: map[string]any{"field": fieldName},
		}
	}
	return nil
}
