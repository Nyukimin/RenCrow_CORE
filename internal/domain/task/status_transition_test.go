package task

import (
	"errors"
	"fmt"
	"testing"
)

// TestAlreadyTerminalRecognizesOnlyTerminalSourceTransitions は、終端済みTaskへの
// 別状態の書き込み拒否だけを「既に決着済み」とみなし、非終端からの無効遷移や
// 無関係なエラーは決着済みとして扱わないことを検証する。
func TestAlreadyTerminalRecognizesOnlyTerminalSourceTransitions(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"terminal source failed", &InvalidStatusTransitionError{From: StatusFailed, To: StatusSucceeded}, true},
		{"terminal source succeeded", &InvalidStatusTransitionError{From: StatusSucceeded, To: StatusFailed}, true},
		{"terminal source cancelled", &InvalidStatusTransitionError{From: StatusCancelled, To: StatusSucceeded}, true},
		{"terminal source superseded", &InvalidStatusTransitionError{From: StatusSuperseded, To: StatusFailed}, true},
		{"wrapped terminal source", fmt.Errorf("finalize: %w", &InvalidStatusTransitionError{From: StatusFailed, To: StatusSucceeded}), true},
		{"non terminal source waiting", &InvalidStatusTransitionError{From: StatusWaiting, To: StatusSucceeded}, false},
		{"non terminal source queued", &InvalidStatusTransitionError{From: StatusQueued, To: StatusSucceeded}, false},
		{"unrelated error", errors.New("task store rejected terminal write"), false},
		{"context deadline", fmt.Errorf("write: %w", errors.New("context deadline exceeded")), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AlreadyTerminal(tc.err); got != tc.want {
				t.Fatalf("AlreadyTerminal(%v)=%t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

// TestInvalidStatusTransitionErrorKeepsHistoricalMessage は、型付けしても
// 既存のログ・監視が頼るエラー文言を変えないことを検証する。
func TestInvalidStatusTransitionErrorKeepsHistoricalMessage(t *testing.T) {
	err := error(&InvalidStatusTransitionError{From: StatusFailed, To: StatusSucceeded})
	if got, want := err.Error(), "invalid status transition: failed -> succeeded"; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}
