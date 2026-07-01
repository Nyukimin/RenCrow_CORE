package aiworkflow

import "testing"

func TestEvaluateHeavyWorkerDisabled(t *testing.T) {
	decision := EvaluateHeavyWorker(HeavyWorkerRequest{TargetFileCount: 100}, HeavyWorkerPolicy{})
	if decision.Status != HeavyWorkerStatusNotRequired {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestEvaluateHeavyWorkerRequestsOnThresholds(t *testing.T) {
	decision := EvaluateHeavyWorker(HeavyWorkerRequest{
		EventID:         "heavy_1",
		Agent:           "Coder",
		TargetFileCount: 21,
		Reason:          "large refactor",
	}, HeavyWorkerPolicy{
		Enabled:       true,
		RequireReason: true,
	})
	if decision.Status != HeavyWorkerStatusRequested {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestEvaluateHeavyWorkerBlocksWhenReasonRequired(t *testing.T) {
	decision := EvaluateHeavyWorker(HeavyWorkerRequest{
		EventID:               "heavy_1",
		Agent:                 "Coder",
		UserRequestedDeepDive: true,
	}, HeavyWorkerPolicy{
		Enabled:       true,
		RequireReason: true,
	})
	if decision.Status != HeavyWorkerStatusBlocked {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestEvaluateHeavyWorkerNotRequired(t *testing.T) {
	decision := EvaluateHeavyWorker(HeavyWorkerRequest{
		EventID:         "heavy_1",
		Agent:           "Coder",
		TargetFileCount: 3,
		Reason:          "small change",
	}, HeavyWorkerPolicy{
		Enabled:       true,
		RequireReason: true,
	})
	if decision.Status != HeavyWorkerStatusNotRequired {
		t.Fatalf("decision=%#v", decision)
	}
}
