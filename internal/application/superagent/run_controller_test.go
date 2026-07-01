package superagent

import (
	"context"
	"testing"
	"time"
)

func TestRunControllerPauseCancelsRegisteredRun(t *testing.T) {
	controller := NewRunController()
	ctx, unregister := controller.RegisterRun(context.Background(), "run_1")
	defer unregister()

	result := controller.PauseRun("run_1", "manual pause")
	if !result.Applied {
		t.Fatalf("expected pause to apply to active run")
	}
	if result.Action != "cancel_requested" {
		t.Fatalf("unexpected action: %s", result.Action)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("registered run context was not canceled")
	}
	if !controller.IsPauseRequested("run_1") {
		t.Fatal("pause request marker was not recorded")
	}
}

func TestRunControllerResumeClearsPauseMarkerWithoutRestarting(t *testing.T) {
	controller := NewRunController()
	controller.PauseRun("run_1", "manual pause")

	result := controller.ResumeRun("run_1", "manual resume")
	if !result.Applied {
		t.Fatalf("expected resume to clear pause marker")
	}
	if result.Action != "resume_marker_cleared" {
		t.Fatalf("unexpected action: %s", result.Action)
	}
	if controller.IsPauseRequested("run_1") {
		t.Fatal("pause marker should be cleared")
	}
}
