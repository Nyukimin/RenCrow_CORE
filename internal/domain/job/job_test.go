package job

import "testing"

func TestCanTransition(t *testing.T) {
	if !CanTransition(StatusQueued, StatusRunning) {
		t.Fatal("queued should transition to running")
	}
	if CanTransition(StatusSucceeded, StatusRunning) {
		t.Fatal("terminal status should not transition")
	}
	if CanTransition(StatusQueued, StatusSucceeded) {
		t.Fatal("queued should not skip directly to succeeded")
	}
}

func TestShouldNotify(t *testing.T) {
	j := Job{Status: StatusSucceeded, InterruptPolicy: InterruptNotifyDoneOrBlocked}
	if !ShouldNotify(j) {
		t.Fatal("succeeded job should notify")
	}
	j.InterruptPolicy = InterruptSilent
	if ShouldNotify(j) {
		t.Fatal("silent job should not notify")
	}
}
