package conversation

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// mockRetryableError はリトライ可能なエラー
type mockRetryableError struct {
	msg       string
	retryable bool
}

func (e *mockRetryableError) Error() string {
	return e.msg
}

func (e *mockRetryableError) IsRetryable() bool {
	return e.retryable
}

type mockNetError struct {
	timeout bool
}

func (e mockNetError) Error() string {
	return "mock network error"
}

func (e mockNetError) Timeout() bool {
	return e.timeout
}

func (e mockNetError) Temporary() bool {
	return e.timeout
}

func TestWithRetry_Success(t *testing.T) {
	attempts := 0
	operation := func() error {
		attempts++
		if attempts < 2 {
			return errors.New("temporary error")
		}
		return nil
	}

	config := RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		Multiplier:   2.0,
	}

	err := withRetry(context.Background(), config, operation)
	if err != nil {
		t.Errorf("Expected success after retry, got error: %v", err)
	}

	if attempts != 2 {
		t.Errorf("Expected 2 attempts, got %d", attempts)
	}
}

func TestWithRetry_MaxAttemptsExceeded(t *testing.T) {
	attempts := 0
	operation := func() error {
		attempts++
		return errors.New("persistent error")
	}

	config := RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		Multiplier:   2.0,
	}

	err := withRetry(context.Background(), config, operation)
	if err == nil {
		t.Error("Expected error after max attempts, got nil")
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}

	if !errors.Is(err, errors.New("persistent error")) {
		// Check error message contains attempt information
		errMsg := err.Error()
		if errMsg == "" {
			t.Error("Expected non-empty error message")
		}
	}
}

func TestWithRetry_NonRetryableError(t *testing.T) {
	attempts := 0
	operation := func() error {
		attempts++
		return &mockRetryableError{
			msg:       "non-retryable error",
			retryable: false,
		}
	}

	config := RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		Multiplier:   2.0,
	}

	err := withRetry(context.Background(), config, operation)
	if err == nil {
		t.Error("Expected error for non-retryable error")
	}

	if attempts != 1 {
		t.Errorf("Expected 1 attempt for non-retryable error, got %d", attempts)
	}
}

func TestWithRetry_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	operation := func() error {
		attempts++
		if attempts == 1 {
			// Cancel context after first failure
			cancel()
		}
		return errors.New("error")
	}

	config := RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		Multiplier:   2.0,
	}

	err := withRetry(ctx, config, operation)
	if err == nil {
		t.Error("Expected error when context is cancelled")
	}

	// Should not retry after context cancellation
	if attempts > 2 {
		t.Errorf("Expected at most 2 attempts when context cancelled, got %d", attempts)
	}
}

func TestWithRetry_ImmediateSuccess(t *testing.T) {
	attempts := 0
	operation := func() error {
		attempts++
		return nil
	}

	config := RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		Multiplier:   2.0,
	}

	err := withRetry(context.Background(), config, operation)
	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	if attempts != 1 {
		t.Errorf("Expected 1 attempt for immediate success, got %d", attempts)
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "nil error",
			err:       nil,
			retryable: false,
		},
		{
			name:      "retryable error",
			err:       &mockRetryableError{msg: "temp", retryable: true},
			retryable: true,
		},
		{
			name:      "non-retryable error",
			err:       &mockRetryableError{msg: "perm", retryable: false},
			retryable: false,
		},
		{
			name:      "standard error (default retryable)",
			err:       errors.New("standard error"),
			retryable: true,
		},
		{
			name:      "context canceled",
			err:       context.Canceled,
			retryable: false,
		},
		{
			name:      "context deadline exceeded",
			err:       context.DeadlineExceeded,
			retryable: true,
		},
		{
			name:      "wrapped missing file",
			err:       errors.Join(errors.New("load failed"), os.ErrNotExist),
			retryable: false,
		},
		{
			name:      "wrapped permission error",
			err:       errors.Join(errors.New("open failed"), os.ErrPermission),
			retryable: false,
		},
		{
			name:      "network timeout",
			err:       &net.OpError{Op: "dial", Net: "tcp", Err: mockNetError{timeout: true}},
			retryable: true,
		},
		{
			name:      "network permanent error",
			err:       &net.OpError{Op: "dial", Net: "tcp", Err: mockNetError{timeout: false}},
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err)
			if got != tt.retryable {
				t.Errorf("isRetryableError(%v): want %v, got %v", tt.err, tt.retryable, got)
			}
		})
	}
}
