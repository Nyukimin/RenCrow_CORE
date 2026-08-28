package developmentmethodology

import (
	"errors"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

func TestWorkClassificationAndTaskTransitionTable(t *testing.T) {
	for _, want := range []WorkClass{WorkClassSpike, WorkClassBounded, WorkClassArchitectural} {
		got, err := ClassifyWork(string(want))
		if err != nil || got != want {
			t.Fatalf("classify %q: got %q, err=%v", want, got, err)
		}
	}
	if _, err := ClassifyWork("unknown"); err == nil {
		t.Fatal("unknown work class must be rejected")
	}

	task := NewTask("plan-1", "task-1", "implement domain")
	if _, err := TransitionTask(task, TaskGreenVerified); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("task state skip must be rejected: %v", err)
	}
	for _, state := range []TaskState{TaskReady, TaskAssigned, TaskRedVerified, TaskGreenVerified, TaskRefactored, TaskReviewed, TaskDone} {
		next, err := TransitionTask(task, state)
		if err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
		task = next
	}
	if !task.State.IsTerminal() || task.TerminalOutcome != OutcomeOK {
		t.Fatalf("done task not terminal: %+v", task)
	}
	if _, err := TransitionTask(task, TaskReady); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal task must not reopen: %v", err)
	}
}

func TestTaskAndLedgerShareCanonicalLifecycleAndTerminalRules(t *testing.T) {
	table := TaskTransitionTable()
	for _, state := range []TaskState{
		TaskPending, TaskReady, TaskAssigned, TaskRedVerified, TaskGreenVerified,
		TaskRefactored, TaskReviewed, TaskDone, TaskBlocked, TaskFailed, TaskCancelled,
	} {
		if _, ok := table[state]; !ok {
			t.Fatalf("canonical transition table omits %s", state)
		}
	}
	for _, target := range []TaskState{TaskBlocked, TaskFailed, TaskCancelled} {
		if !CanTransitionTask(TaskReviewed, target) {
			t.Fatalf("terminal transition from late lifecycle state %s -> %s must be allowed", TaskReviewed, target)
		}
	}
	if CanTransitionTask(TaskDone, TaskReady) || CanTransitionTask(TaskBlocked, TaskReady) {
		t.Fatal("terminal lifecycle states must not reopen")
	}
	if err := ValidateLedgerState(string(TaskReviewed)); err != nil {
		t.Fatalf("canonical ledger state rejected: %v", err)
	}
	if err := ValidateLedgerState("waiting"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("human-wait vocabulary must be rejected: %v", err)
	}

	task := NewTask("plan-1", "task-1", "late terminal failure")
	task.State = TaskReviewed
	for _, target := range []TaskState{TaskBlocked, TaskFailed, TaskCancelled} {
		candidate, err := TransitionTask(task, target)
		if err != nil || candidate.TerminalOutcome != outcomeForTaskState(target) {
			t.Fatalf("terminal transition %s -> %s: task=%+v err=%v", task.State, target, candidate, err)
		}
	}
}

func TestDeliveryVocabularyDelegatesToBacklog(t *testing.T) {
	if !CanTransitionDelivery(domainbacklog.DeliveryQueued, domainbacklog.DeliverySpec) {
		t.Fatal("backlog delivery transition should remain available")
	}
	if CanTransitionDelivery(domainbacklog.DeliveryQueued, domainbacklog.DeliveryLiveVerified) {
		t.Fatal("delivery transition must not skip stages")
	}
}

func TestImplementationAuthorityTokenRequiresVerifiedAdoptionAndInjectableClock(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := FixedClock{Current: base}
	adoption := AdoptionEvidence{
		EvidenceID: "adopt-1", UnitID: "unit-1", SpecRef: "spec-1",
		SpecHash: HashContent("spec"), Decision: "ADOPT", Verified: true, CreatedAt: base,
	}
	req := ImplementationAuthorityRequest{
		ImplementationAuthorityTokenID: "implementation_authority-1", UnitID: "unit-1", SpecRef: "spec-1",
		SpecHash: adoption.SpecHash, Issuer: "system-owner", Scope: []string{"implementation"},
		Reason: "explicit adoption", ExpiresAt: base.Add(24 * time.Hour),
	}
	token, err := IssueImplementationAuthorityToken(req, adoption, clock)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := token.ValidateFor(adoption, clock); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	clock.Current = base.Add(24*time.Hour + time.Nanosecond)
	if !errors.Is(token.Validate(clock), ErrStaleImplementationAuthorityToken) {
		t.Fatal("expired token must be rejected")
	}
	adoption.Verified = false
	if _, err := IssueImplementationAuthorityToken(req, adoption, FixedClock{Current: base}); !errors.Is(err, ErrImplementationAuthorityRequired) {
		t.Fatalf("unverified adoption must not issue token: %v", err)
	}
}

func TestAuthorityWorktreeBaselineAndTDDGates(t *testing.T) {
	if AuthorityAllows(RoleCoder, OperationRepositoryWrite) {
		t.Fatal("coder must not write repositories")
	}
	if AuthorityAllows(RoleCoder, OperationShell) || AuthorityAllows(RoleCoder, OperationTestExecution) {
		t.Fatal("coder must not execute shell/tests")
	}
	if !AuthorityAllows(RoleCoder, OperationPlan) || !AuthorityAllows(RoleWorker, OperationTestExecution) {
		t.Fatal("expected role authority missing")
	}
	if AuthorityAllowsWithSkill(RoleCoder, "tdd_task_implementation", OperationRepositoryWrite) {
		t.Fatal("skill must not expand authority")
	}

	when := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	specHash := HashContent("spec")
	wt := WorktreeEvidence{WorktreePath: "/tmp/unit", Branch: "main", BaseRevision: "abc", Isolated: false, Verified: true, CreatedAt: when}
	if !errors.Is(ValidateWorktreeGate(wt), ErrWorktreeRequired) {
		t.Fatal("main direct write must be rejected")
	}
	wt.Branch, wt.Isolated = "feature/unit", true
	if err := ValidateWorktreeGate(wt); err != nil {
		t.Fatalf("isolated worktree rejected: %v", err)
	}
	baseline := BaselineEvidence{UnitID: "unit-1", PlanID: "plan-1", SpecRef: "spec-1", SpecHash: specHash, ValidForRevision: "rev-1", WorktreePath: wt.WorktreePath, Branch: wt.Branch, BaseRevision: wt.BaseRevision, Command: "go test ./...", GitRevision: "abc", Verified: true, CreatedAt: when}
	if err := ValidateBaselineGate(baseline); err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	}

	red := EvidenceReceipt{EvidenceID: "red", UnitID: "unit-1", PlanID: "plan-1", SpecHash: specHash, TaskID: "task-1", Stage: string(DeliveryTDDRed), EvidenceType: "tdd_red", Command: "go test ./...", ExitCode: 1, ExpectedFailure: "expected failure", ActualFailure: "expected failure: missing", ValidForRevision: "rev-1", Verified: true, CreatedAt: when}
	green := EvidenceReceipt{EvidenceID: "green", UnitID: "unit-1", PlanID: "plan-1", SpecHash: specHash, TaskID: "task-1", Stage: string(DeliveryTDDGreen), EvidenceType: "tdd_green", Command: "go test ./...", ExitCode: 0, ValidForRevision: "rev-1", Verified: true, CreatedAt: when}
	if err := ValidateTDDRedEvidence(red); err != nil {
		t.Fatalf("valid RED rejected: %v", err)
	}
	if err := ValidateTDDGreenEvidence(green, red); err != nil {
		t.Fatalf("valid GREEN rejected: %v", err)
	}
	if err := ValidateTDDGreenEvidence(green, EvidenceReceipt{}); !errors.Is(err, ErrMissingEvidence) {
		t.Fatalf("GREEN without RED must be blocked: %v", err)
	}
	red.ActualFailure = "unrelated failure"
	if err := ValidateTDDRedEvidence(red); err == nil {
		t.Fatal("RED with wrong failure must be rejected")
	}
}

func TestReviewRootCauseRulingAndTerminalOutcome(t *testing.T) {
	specHash := HashContent("spec")
	review := ReviewRecord{ReviewID: "review-1", UnitID: "unit-1", PlanID: "plan-1", TaskID: "task-1", ReviewType: ReviewTypeTask, ImplementerAgentID: "coder-1", ReviewerAgentID: "reviewer-1", SpecRef: "spec-1", SpecHash: specHash, ValidForRevision: "rev-1", DiffRef: "diff-1", Verdict: ReviewAccepted, EvidenceRefs: []string{"green"}, CreatedAt: time.Now().UTC()}
	if err := ValidateReviewRecord(review); err != nil {
		t.Fatalf("valid independent review rejected: %v", err)
	}
	review.ReviewerAgentID = review.ImplementerAgentID
	if !errors.Is(ValidateReviewRecord(review), ErrReviewRequired) {
		t.Fatal("self-review must be rejected")
	}

	root := RootCauseEvidence{EvidenceID: "rc-1", UnitID: "unit-1", PlanID: "plan-1", TaskID: "task-1", SpecRef: "spec-1", SpecHash: specHash, ValidForRevision: "rev-1", Reproduced: true, ReproductionRef: "repro", ErrorLogRef: "log", TraceRef: "trace", CallPath: []string{"entry", "owner", "store"}, Hypothesis: "bad state", VerificationRef: "verify", FailureCount: RootCauseEscalation, Verified: true, CreatedAt: time.Now().UTC()}
	if !ArchitectureReviewRequired(root.FailureCount) {
		t.Fatal("three failed attempts must escalate")
	}
	if err := ValidateRootCauseGate(root); err == nil {
		t.Fatal("three failures without escalation must be blocked")
	}
	root.ArchitectureReviewRequired = true
	if err := ValidateRootCauseGate(root); err != nil {
		t.Fatalf("escalated root cause rejected: %v", err)
	}

	for _, test := range []struct {
		conflict ConflictType
		decision RulingDecision
		blocked  bool
	}{
		{ConflictReversibleLocalAmbiguity, RulingContinue, false},
		{ConflictNonDestructiveDesignGap, RulingContinue, false},
		{ConflictDestructiveIrreversible, RulingContinue, true},
		{ConflictProductSemantics, RulingContinue, true},
	} {
		ruling := Ruling{RulingID: "ruling-" + string(test.conflict), UnitID: "unit-1", PlanID: "plan-1", ConflictType: test.conflict, SpecRef: "spec-1", Decision: test.decision, Rationale: "reason", Impact: "impact", Actor: "controller", CreatedAt: time.Now().UTC()}
		if test.blocked {
			if err := ValidateRuling(ruling); !errors.Is(err, ErrConflictBlocked) {
				t.Fatalf("destructive ruling not blocked: %v", err)
			}
		} else if err := ValidateRuling(ruling); err != nil {
			t.Fatalf("reversible ruling rejected: %v", err)
		}
	}
	if err := ValidateTerminalOutcome(OutcomeOK); err != nil {
		t.Fatalf("valid outcome rejected: %v", err)
	}
	if err := ValidateTerminalOutcome(TerminalOutcome("partial")); err == nil {
		t.Fatal("partial must not be a terminal outcome")
	}
}
