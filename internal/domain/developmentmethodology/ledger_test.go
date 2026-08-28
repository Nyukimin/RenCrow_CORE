package developmentmethodology

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLedgerEvidenceAndUpdatesAreIdempotentAndPlanScoped(t *testing.T) {
	specHash := HashContent("spec")
	ledger := NewLedger("unit-1", "plan-1", "spec-1", specHash)
	ledger.Tasks = []Task{{TaskID: "task-1", PlanID: "plan-1", Purpose: "test", State: TaskPending}}
	receipt := EvidenceReceipt{
		EvidenceID: "evidence-1", IdempotencyKey: "same-request", UnitID: "unit-1", PlanID: "plan-1", SpecHash: specHash,
		TaskID: "task-1", Stage: string(DeliveryTDDGreen), EvidenceType: "tdd_green",
		Command: "go test ./internal/domain/developmentmethodology", ExitCode: 0,
		GitRevision: "rev-1", ValidForRevision: "rev-1", Verified: true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	changed, err := ledger.RecordEvidence(receipt)
	if err != nil || !changed {
		t.Fatalf("first evidence record: changed=%v err=%v", changed, err)
	}
	changed, err = ledger.RecordEvidence(receipt)
	if err != nil || changed || len(ledger.EvidenceRefs) != 1 {
		t.Fatalf("duplicate evidence must be idempotent: changed=%v len=%d err=%v", changed, len(ledger.EvidenceRefs), err)
	}
	conflicting := receipt
	conflicting.ResultSummary = "different result"
	if _, err := ledger.RecordEvidence(conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting evidence must be rejected: %v", err)
	}
	wrongPlan := receipt
	wrongPlan.UnitID = "other-unit"
	if _, err := ledger.RecordEvidence(wrongPlan); err == nil {
		t.Fatal("evidence from another unit must be rejected")
	}

	review := ReviewRecord{ReviewID: "review-1", UnitID: "unit-1", PlanID: "plan-1", TaskID: "task-1", ReviewType: ReviewTypeTask, ImplementerAgentID: "coder", ReviewerAgentID: "reviewer", SpecRef: "spec-1", SpecHash: specHash, ValidForRevision: "rev-1", DiffRef: "diff-1", Verdict: ReviewAccepted, EvidenceRefs: []string{"evidence-1"}, CreatedAt: time.Now().UTC()}
	if changed, err := ledger.RecordReview(review); err != nil || !changed {
		t.Fatalf("first review: changed=%v err=%v", changed, err)
	}
	if changed, err := ledger.RecordReview(review); err != nil || changed {
		t.Fatalf("duplicate review must be idempotent: changed=%v err=%v", changed, err)
	}
	if err := ledger.Validate(); err != nil {
		t.Fatalf("ledger validation: %v", err)
	}
	resumed, err := ledger.Resume()
	if err != nil || resumed.PlanID != ledger.PlanID || len(resumed.EvidenceRefs) != 1 {
		t.Fatalf("resume lost plan-scoped state: %+v err=%v", resumed, err)
	}
}

func TestLiveGateRequiresEveryMachineBoundaryAndIgnoresCheckOK(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sha := HashContent("artifact")
	specHash := HashContent("spec")
	receipt := func(stage, kind string) EvidenceReceipt {
		return EvidenceReceipt{EvidenceID: stage + "-receipt", UnitID: "unit-1", PlanID: "plan-1", SpecHash: specHash, Stage: stage, EvidenceType: kind, Command: "machine command", ExitCode: 0, ArtifactSHA256: sha, GitRevision: "rev-1", TraceID: "trace-" + stage, ValidForRevision: "rev-1", Verified: true, CreatedAt: base}
	}
	reviews := []ReviewRecord{
		{ReviewID: "task-review", UnitID: "unit-1", PlanID: "plan-1", TaskID: "task-1", ReviewType: ReviewTypeTask, ImplementerAgentID: "worker", ReviewerAgentID: "reviewer", SpecRef: "spec-1", SpecHash: specHash, ValidForRevision: "rev-1", DiffRef: "diff", Verdict: ReviewAccepted, EvidenceRefs: []string{"TDD_GREEN-receipt"}, CreatedAt: base},
		{ReviewID: "branch-review", UnitID: "unit-1", PlanID: "plan-1", ReviewType: ReviewTypeBranch, ImplementerAgentID: "worker", ReviewerAgentID: "reviewer", SpecRef: "spec-1", SpecHash: specHash, ValidForRevision: "rev-1", DiffRef: "branch", Verdict: ReviewAccepted, EvidenceRefs: []string{"REGRESSION-receipt"}, CreatedAt: base},
	}
	input := LiveGateInput{
		UnitID: "unit-1", PlanID: "plan-1", SpecHash: specHash, Revision: "rev-1", CheckOK: true, AcceptedImplementation: true, FullRelevantTests: true,
		BuildEvidence:              receipt(string(DeliveryBuild), "build"),
		EcosystemEvidence:          receipt("ECOSYSTEM_VERIFIED", "ecosystem_verified"),
		DeployEvidence:             receipt(string(DeliveryDeploy), "deploy_receipt"),
		RestartEvidence:            receipt(string(DeliveryRestart), "restart_receipt"),
		ProcessIdentityEvidence:    receipt("PROCESS_IDENTITY_VERIFIED", "process_identity"),
		ReadinessEvidence:          receipt(string(DeliveryPostDeployVerify), "readiness"),
		ProductionSmokeEvidence:    receipt(string(DeliveryLiveVerified), "production_smoke"),
		ViewerVerificationEvidence: receipt("VIEWER_VERIFIED", "viewer_verification"),
		Evidence: []EvidenceReceipt{
			receipt("ACCEPTED", "accepted"), receipt(string(DeliveryTDDGreen), "tdd_green"), receipt("REGRESSION", "regression"),
		},
		Reviews: reviews,
	}
	if err := ValidateLIVEGate(input); err != nil {
		t.Fatalf("complete LIVE evidence rejected: %v", err)
	}
	input.ViewerVerificationEvidence = EvidenceReceipt{}
	if err := ValidateLIVEGate(input); !errors.Is(err, ErrLiveGate) {
		t.Fatalf("missing Viewer evidence must block LIVE: %v", err)
	}
	input.ViewerVerificationEvidence = receipt("VIEWER_VERIFIED", "viewer_verification")
	input.DeployEvidence = EvidenceReceipt{}
	if err := ValidateLIVEGate(input); !errors.Is(err, ErrLiveGate) {
		t.Fatalf("missing deploy evidence must block LIVE: %v", err)
	}
	input.DeployEvidence = receipt(string(DeliveryDeploy), "deploy_receipt")
	input.ReadinessEvidence = EvidenceReceipt{}
	if err := ValidateLIVEGate(input); !errors.Is(err, ErrLiveGate) {
		t.Fatalf("missing readiness evidence must block LIVE: %v", err)
	}
	input.ReadinessEvidence = receipt(string(DeliveryPostDeployVerify), "readiness")
	input.ProductionSmokeEvidence = EvidenceReceipt{}
	if err := ValidateLIVEGate(input); !errors.Is(err, ErrLiveGate) {
		t.Fatalf("missing production smoke must block LIVE: %v", err)
	}
	input.ProductionSmokeEvidence = receipt(string(DeliveryLiveVerified), "production_smoke")
	input.CheckOK = false
	if err := ValidateLIVEGate(input); err != nil {
		t.Fatalf("LIVE gate must not depend on check_ok: %v", err)
	}
}

func TestSecretRedactionDoesNotLeakIntoEvidenceOrLedgerJSON(t *testing.T) {
	secretText := "Authorization: Bearer super-secret-token password=hunter2 api_key=sk-live-123"
	redacted := RedactSecrets(secretText)
	if redacted == secretText || containsAny(redacted, "super-secret-token", "hunter2", "sk-live-123") {
		t.Fatalf("secret was not redacted: %q", redacted)
	}
	specHash := HashContent("spec")
	receipt := EvidenceReceipt{EvidenceID: "secret-evidence", UnitID: "unit-1", PlanID: "plan-1", SpecHash: specHash, Stage: string(DeliveryBuild), EvidenceType: "build", Command: secretText, ExitCode: 0, ValidForRevision: "rev-1", CreatedAt: time.Now().UTC()}
	ledger := NewLedger("unit-1", "plan-1", "spec-1", specHash)
	if err := ledger.AddEvidence(receipt); err != nil {
		t.Fatalf("redacted evidence should be accepted: %v", err)
	}
	if containsAny(ledger.EvidenceRefs[0].Command, "super-secret-token", "hunter2", "sk-live-123") {
		t.Fatalf("ledger retained secret: %q", ledger.EvidenceRefs[0].Command)
	}
}

func TestLedgerProgressRejectsSkippedStateAndRemovedEvidence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	specHash := HashContent("spec")
	previous := NewLedger("unit-1", "plan-1", "spec-1", specHash)
	previous.Revision = "rev-1"
	previous.LastCheckpointAt = now
	next := previous
	next.LastCheckpointAt = now.Add(time.Second)
	next.CurrentState = string(TaskGreenVerified)
	if err := ValidateLedgerProgress(previous, next); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skipped state accepted: %v", err)
	}

	receipt := EvidenceReceipt{EvidenceID: "accepted", UnitID: previous.UnitID, PlanID: previous.PlanID, SpecHash: specHash, Stage: "ACCEPTED", EvidenceType: "accepted", Command: "verify", ExitCode: 0, ValidForRevision: previous.Revision, Verified: true, MachineGenerated: true, CreatedAt: now}
	previous.EvidenceRefs = []EvidenceReceipt{receipt}
	next = previous
	next.LastCheckpointAt = now.Add(time.Second)
	next.CurrentState = string(TaskReady)
	next.EvidenceRefs = nil
	if err := ValidateLedgerProgress(previous, next); err == nil || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("removed evidence accepted: %v", err)
	}
}

func TestLedgerProgressAllowsExplicitTerminalFailureFromAnyNonTerminalState(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	specHash := HashContent("spec")
	for _, outcome := range []TerminalOutcome{OutcomeBlocked, OutcomeFailed, OutcomeCancelled} {
		previous := NewLedger("unit-1", "plan-1", "spec-1", specHash)
		previous.Revision = "rev-1"
		previous.CurrentState = string(TaskReviewed)
		previous.LastCheckpointAt = now
		next := previous
		next.LastCheckpointAt = now.Add(time.Second)
		switch outcome {
		case OutcomeBlocked:
			next.CurrentState, next.TerminalOutcome, next.BlockedReason = string(TaskBlocked), outcome, "blocked by verified deployment failure"
		case OutcomeFailed:
			next.CurrentState, next.TerminalOutcome = string(TaskFailed), outcome
		case OutcomeCancelled:
			next.CurrentState, next.TerminalOutcome = string(TaskCancelled), outcome
		}
		if err := ValidateLedgerProgress(previous, next); err != nil {
			t.Fatalf("terminal outcome %s rejected from late lifecycle state: %v", outcome, err)
		}
	}
}

func TestLedgerRevisionRolloverRequiresFreshExplicitBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	previous := NewLedger("unit-1", "plan-1", "spec-1", HashContent("spec-v1"))
	previous.Revision = "rev-1"
	previous.CurrentState = string(TaskFailed)
	previous.TerminalOutcome = OutcomeFailed
	previous.LastCheckpointAt = now

	next := NewLedger("unit-1", "plan-2", "spec-1", HashContent("spec-v2"))
	next.Revision = "rev-2"
	next.CurrentState = string(TaskPending)
	next.LastCheckpointAt = now.Add(time.Second)
	next.SupersedesPlanID = previous.PlanID
	next.SupersedesRevision = previous.Revision
	next.Worktrees = []WorktreeEvidence{{WorktreePath: "/tmp/rev-2", Branch: "feat/rev-2", BaseRevision: "rev-2", GitRevision: "rev-2", Isolated: true, Verified: true, CreatedAt: now.Add(time.Second)}}
	next.BaselineEvidence = []BaselineEvidence{{UnitID: next.UnitID, PlanID: next.PlanID, SpecRef: next.SpecRef, SpecHash: next.SpecHash, ValidForRevision: next.Revision, WorktreePath: "/tmp/rev-2", Branch: "feat/rev-2", BaseRevision: "rev-2", Command: "git status --porcelain", GitRevision: "rev-2", Verified: true, CreatedAt: now.Add(time.Second)}}
	if err := ValidateLedgerRollover(previous, next); err != nil {
		t.Fatalf("fresh explicit rollover rejected: %v", err)
	}

	stale := next
	stale.EvidenceRefs = []EvidenceReceipt{{
		EvidenceID: "stale", UnitID: stale.UnitID, PlanID: stale.PlanID, SpecHash: stale.SpecHash,
		Stage: "ACCEPTED", EvidenceType: "accepted", Command: "old receipt", ExitCode: 0,
		ValidForRevision: stale.Revision, Verified: true, CreatedAt: now.Add(time.Second),
	}}
	if err := ValidateLedgerRollover(previous, stale); err == nil {
		t.Fatal("rollover accepted evidence carried into a fresh revision")
	}

	unmarked := next
	unmarked.SupersedesPlanID, unmarked.SupersedesRevision = "", ""
	if err := ValidateLedgerProgress(previous, unmarked); !errors.Is(err, ErrRevisionRolloverRequired) {
		t.Fatalf("unmarked revision change was not rejected explicitly: %v", err)
	}
}

func TestLedgerProgressRejectsMutationOfPriorAppendOnlyContent(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	specHash := HashContent("spec")
	previous := NewLedger("unit-1", "plan-1", "spec-1", specHash)
	previous.Revision = "rev-1"
	previous.Tasks = []Task{{TaskID: "task-1", PlanID: previous.PlanID, Purpose: "task", State: TaskPending}}
	previous.EvidenceRefs = []EvidenceReceipt{{
		EvidenceID: "evidence-1", UnitID: previous.UnitID, PlanID: previous.PlanID, SpecHash: specHash,
		TaskID: "task-1", Stage: "ACCEPTED", EvidenceType: "accepted", Command: "verify", ExitCode: 0,
		ValidForRevision: previous.Revision, Verified: true, CreatedAt: now,
	}}
	previous.LastCheckpointAt = now
	next := previous
	next.CurrentState = string(TaskReady)
	next.LastCheckpointAt = now.Add(time.Second)
	next.EvidenceRefs = append([]EvidenceReceipt(nil), previous.EvidenceRefs...)
	next.EvidenceRefs[0].ResultSummary = "mutated after append"
	if err := ValidateLedgerProgress(previous, next); err == nil {
		t.Fatal("mutation of prior evidence was accepted")
	}
}

func TestLIVEGateResolvesEveryReviewEvidenceReferenceAndRejectsInvalidMatchingReceipt(t *testing.T) {
	input := completeLiveGateInput(t)
	input.Reviews[0].EvidenceRefs = []string{"does-not-exist"}
	if err := ValidateLIVEGate(input); !errors.Is(err, ErrLiveGate) {
		t.Fatalf("unresolved task review evidence was accepted: %v", err)
	}

	input = completeLiveGateInput(t)
	invalid := input.BuildEvidence
	invalid.EvidenceID = "invalid-build"
	invalid.ArtifactSHA256 = "not-a-sha256"
	input.Evidence = append(input.Evidence, invalid)
	if err := ValidateLIVEGate(input); !errors.Is(err, ErrLiveGate) {
		t.Fatalf("invalid matching build receipt was accepted: %v", err)
	}
}

func completeLiveGateInput(t *testing.T) LiveGateInput {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sha := HashContent("artifact")
	specHash := HashContent("spec")
	receipt := func(stage, kind string) EvidenceReceipt {
		return EvidenceReceipt{EvidenceID: stage + "-receipt", UnitID: "unit-1", PlanID: "plan-1", SpecHash: specHash, Stage: stage, EvidenceType: kind, Command: "machine command", ExitCode: 0, ArtifactSHA256: sha, GitRevision: "rev-1", TraceID: "trace-" + stage, ValidForRevision: "rev-1", Verified: true, CreatedAt: base}
	}
	return LiveGateInput{
		UnitID: "unit-1", PlanID: "plan-1", SpecHash: specHash, Revision: "rev-1",
		BuildEvidence:              receipt(string(DeliveryBuild), "build"),
		EcosystemEvidence:          receipt("ECOSYSTEM_VERIFIED", "ecosystem_verified"),
		DeployEvidence:             receipt(string(DeliveryDeploy), "deploy_receipt"),
		RestartEvidence:            receipt(string(DeliveryRestart), "restart_receipt"),
		ProcessIdentityEvidence:    receipt("PROCESS_IDENTITY_VERIFIED", "process_identity"),
		ReadinessEvidence:          receipt(string(DeliveryPostDeployVerify), "readiness"),
		ProductionSmokeEvidence:    receipt(string(DeliveryLiveVerified), "production_smoke"),
		ViewerVerificationEvidence: receipt("VIEWER_VERIFIED", "viewer_verification"),
		Evidence: []EvidenceReceipt{
			receipt("ACCEPTED", "accepted"), receipt(string(DeliveryTDDGreen), "tdd_green"), receipt("REGRESSION", "regression"),
		},
		Reviews: []ReviewRecord{
			{ReviewID: "task-review", UnitID: "unit-1", PlanID: "plan-1", TaskID: "task-1", ReviewType: ReviewTypeTask, ImplementerAgentID: "worker", ReviewerAgentID: "reviewer", SpecRef: "spec-1", SpecHash: specHash, ValidForRevision: "rev-1", DiffRef: "diff", Verdict: ReviewAccepted, EvidenceRefs: []string{"TDD_GREEN-receipt"}, CreatedAt: base},
			{ReviewID: "branch-review", UnitID: "unit-1", PlanID: "plan-1", ReviewType: ReviewTypeBranch, ImplementerAgentID: "worker", ReviewerAgentID: "reviewer", SpecRef: "spec-1", SpecHash: specHash, ValidForRevision: "rev-1", DiffRef: "branch", Verdict: ReviewAccepted, EvidenceRefs: []string{"REGRESSION-receipt"}, CreatedAt: base},
		},
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		for index := 0; index+len(needle) <= len(value); index++ {
			if value[index:index+len(needle)] == needle {
				return true
			}
		}
	}
	return false
}
