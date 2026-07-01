package tools

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

func TestWithTimeout_Normal(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "ok", nil
	}
	wrapped := withTimeout(fn, 5*time.Second)
	result, err := wrapped(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestWithTimeout_Exceeded(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "should not reach", nil
		}
	}
	wrapped := withTimeout(fn, 50*time.Millisecond)
	_, err := wrapped(context.Background(), nil)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestWithPathValidation_Valid(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "ok", nil
	}
	wrapped := withPathValidation(fn, "path")
	result, err := wrapped(context.Background(), map[string]interface{}{"path": "/safe/path"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestWithPathValidation_Traversal(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "should not reach", nil
	}
	wrapped := withPathValidation(fn, "path")
	_, err := wrapped(context.Background(), map[string]interface{}{"path": "../etc/passwd"})
	if err == nil {
		t.Error("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "VALIDATION_FAILED") {
		t.Errorf("error should contain VALIDATION_FAILED, got: %v", err)
	}
}

func TestWithPathValidation_ControlChars(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "should not reach", nil
	}
	wrapped := withPathValidation(fn, "path")
	_, err := wrapped(context.Background(), map[string]interface{}{"path": "/path/\x00evil"})
	if err == nil {
		t.Error("expected error for control chars")
	}
}

func TestWithPathValidation_Empty(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "should not reach", nil
	}
	wrapped := withPathValidation(fn, "path")
	_, err := wrapped(context.Background(), map[string]interface{}{"path": ""})
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestWithPathValidation_MissingArg(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "should not reach", nil
	}
	wrapped := withPathValidation(fn, "path")
	_, err := wrapped(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Error("expected error for missing arg")
	}
}

func TestWithStringValidation_Valid(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "ok", nil
	}
	wrapped := withStringValidation(fn, "query", 100)
	result, err := wrapped(context.Background(), map[string]interface{}{"query": "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestWithStringValidation_TooLong(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "should not reach", nil
	}
	wrapped := withStringValidation(fn, "query", 5)
	_, err := wrapped(context.Background(), map[string]interface{}{"query": "toolong"})
	if err == nil {
		t.Error("expected error for too-long input")
	}
}

func TestWithStringValidation_Empty(t *testing.T) {
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		return "should not reach", nil
	}
	wrapped := withStringValidation(fn, "query", 100)
	_, err := wrapped(context.Background(), map[string]interface{}{"query": ""})
	if err == nil {
		t.Error("expected error for empty string")
	}
}

func TestWithRetry_SuccessOnFirst(t *testing.T) {
	var calls int32
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "ok", nil
	}
	wrapped := withRetry(fn, RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond})
	result, err := wrapped(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestWithRetry_SuccessOnSecond(t *testing.T) {
	var calls int32
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return "", fmt.Errorf("temporary failure")
		}
		return "recovered", nil
	}
	wrapped := withRetry(fn, RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond})
	result, err := wrapped(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "recovered" {
		t.Errorf("result = %q, want %q", result, "recovered")
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestWithRetry_ExhaustedAttempts(t *testing.T) {
	var calls int32
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", fmt.Errorf("persistent failure")
	}
	wrapped := withRetry(fn, RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond})
	_, err := wrapped(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after exhausted attempts")
	}
	if !strings.Contains(err.Error(), "persistent failure") {
		t.Errorf("error = %q, want contains 'persistent failure'", err.Error())
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetry_NoRetryOnValidationError(t *testing.T) {
	var calls int32
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", &tool.ToolError{Code: tool.ErrValidationFailed, Message: "bad input"}
	}
	wrapped := withRetry(fn, RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond})
	_, err := wrapped(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	// Should NOT retry validation errors
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1 (no retry for validation errors)", calls)
	}
}

func TestWithRetry_NoRetryOnContextCancel(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	fn := func(ctx context.Context, args map[string]interface{}) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			cancel() // cancel context on first call
			return "", fmt.Errorf("first failure")
		}
		return "should not reach", nil
	}
	wrapped := withRetry(fn, RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond})
	_, err := wrapped(ctx, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1 (no retry on cancelled context)", calls)
	}
}
