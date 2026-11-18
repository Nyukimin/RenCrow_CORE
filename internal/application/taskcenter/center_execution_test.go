package taskcenter

import (
	"errors"
	"testing"
)

func TestCenterForwardsManagedExecutionInterfaceAndFailsClosedWithoutOwner(t *testing.T) {
	var _ TaskCenterExecutionCoordinator = (*Center)(nil)
	center, err := NewCenter(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := dailyTestAgentContext(t, "center-no-managed", "mio", nil)
	if _, err := center.ExecuteTaskCenterExecution(ctx, ExecutionInput{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ExecuteTaskCenterExecution() error = %v, want unavailable", err)
	}
	if _, err := center.ReadResult(ctx, "goal-test"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ReadResult() error = %v, want unavailable", err)
	}
	if _, err := center.AcceptResult(ctx, AcceptResultInput{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("AcceptResult() error = %v, want unavailable", err)
	}
}
