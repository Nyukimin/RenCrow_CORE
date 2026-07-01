package tool

import (
	"encoding/json"
	"testing"
)

func TestNewSuccess(t *testing.T) {
	resp := NewSuccess("hello")
	if resp.IsError() {
		t.Error("expected success, got error")
	}
	if resp.String() != "hello" {
		t.Errorf("String() = %q, want %q", resp.String(), "hello")
	}
	if resp.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should be set")
	}
}

func TestNewSuccess_StructResult(t *testing.T) {
	data := map[string]string{"key": "value"}
	resp := NewSuccess(data)
	s := resp.String()
	if s == "" {
		t.Error("String() should not be empty for struct result")
	}
	// Should be valid JSON
	var parsed map[string]string
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		t.Errorf("String() should be valid JSON: %v", err)
	}
	if parsed["key"] != "value" {
		t.Errorf("parsed key = %q, want %q", parsed["key"], "value")
	}
}

func TestNewError(t *testing.T) {
	resp := NewError(ErrValidationFailed, "bad input", map[string]any{"field": "path"})
	if !resp.IsError() {
		t.Error("expected error")
	}
	if resp.Error.Code != ErrValidationFailed {
		t.Errorf("Code = %s, want VALIDATION_FAILED", resp.Error.Code)
	}
	if resp.Error.Message != "bad input" {
		t.Errorf("Message = %q, want %q", resp.Error.Message, "bad input")
	}
	if resp.String() != "VALIDATION_FAILED: bad input" {
		t.Errorf("String() = %q", resp.String())
	}
}

func TestToolError_Error(t *testing.T) {
	e := &ToolError{Code: ErrTimeout, Message: "timed out"}
	if e.Error() != "TIMEOUT: timed out" {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestToolResponse_JSON(t *testing.T) {
	resp := NewSuccess("data")
	b, err := resp.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if parsed["result"] != "data" {
		t.Errorf("result = %v, want %q", parsed["result"], "data")
	}
	if _, ok := parsed["generated_at"]; !ok {
		t.Error("generated_at should be present")
	}
}

func TestToolResponse_JSON_Error(t *testing.T) {
	resp := NewError(ErrNotFound, "not found", nil)
	b, err := resp.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatal("error field should be an object")
	}
	if errObj["code"] != "NOT_FOUND" {
		t.Errorf("error.code = %v, want NOT_FOUND", errObj["code"])
	}
}
