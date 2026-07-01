package execution

import "testing"

func TestStatusIsTerminal(t *testing.T) {
	if !StatusSucceeded.IsTerminal() || !StatusFailed.IsTerminal() || !StatusDenied.IsTerminal() {
		t.Fatal("expected succeeded/failed/denied to be terminal")
	}
	if StatusRunning.IsTerminal() || StatusPending.IsTerminal() {
		t.Fatal("expected running/pending to be non-terminal")
	}
}

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from Status
		to   Status
		ok   bool
	}{
		{StatusPending, StatusRunning, true},
		{StatusPending, StatusDenied, true},
		{StatusPending, StatusCanceled, true},
		{StatusRunning, StatusSucceeded, true},
		{StatusRunning, StatusFailed, true},
		{StatusRunning, StatusRunning, true},
		{StatusDenied, StatusRunning, false},
		{StatusSucceeded, StatusRunning, false},
		{StatusCanceled, StatusRunning, false},
		{StatusRunning, StatusDenied, false},
		{StatusPending, StatusSucceeded, false},
		{Status("unknown"), StatusRunning, false},
	}

	for _, tt := range tests {
		if got := CanTransition(tt.from, tt.to); got != tt.ok {
			t.Errorf("CanTransition(%s->%s)=%v, want %v", tt.from, tt.to, got, tt.ok)
		}
	}
}
