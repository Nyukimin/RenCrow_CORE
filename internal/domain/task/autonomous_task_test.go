package task

import (
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func autonomousDomainPlan(t *testing.T) Task {
	t.Helper()
	taskID, err := AutonomousPlanTaskID("ren", "mio", "2026-11-15", "")
	if err != nil {
		t.Fatalf("AutonomousPlanTaskID: %v", err)
	}
	moduleID, err := AutonomousModuleID("mio")
	if err != nil {
		t.Fatalf("AutonomousModuleID: %v", err)
	}
	now := time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)
	return Task{
		TaskID: taskID, Title: "Plan", ModuleID: moduleID, OwnerID: "ren", Assignee: "mio",
		Route: RouteCHAT, Status: StatusQueued, Priority: PriorityLow, InterruptPolicy: InterruptSilent,
		ReadOnly: true, CreatedAt: now, UpdatedAt: now,
		Autonomous: &AutonomousTask{
			PolicyVersion: AutonomousPolicyVersion, Kind: AutonomousTaskKindPlan,
			Date: "2026-11-15", RootTaskID: taskID,
		},
	}
}

func TestAutonomousPlanIDIsDeterministicAndOwnerScoped(t *testing.T) {
	first, err := AutonomousPlanTaskID("ren", "mio", "2026-11-15", "")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := AutonomousPlanTaskID("ren", "mio", "2026-11-15", "")
	if err != nil || replay != first {
		t.Fatalf("replayed plan ID = %q err=%v, want %q", replay, err, first)
	}
	if err := first.Validate(); err != nil || !strings.Contains(string(first), "-") {
		t.Fatalf("deterministic plan ID is not canonical UUID TaskID: %q err=%v", first, err)
	}
	want, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "autonomous_task", "plan/"+AutonomousPolicyVersion,
		strings.Join([]string{AutonomousPolicyVersion, "ren", "mio", "2026-11-15", ""}, "\x00"))
	if err != nil || string(first) != want {
		t.Fatalf("plan ID %q err=%v, want policy-version-namespaced ID %q", first, err, want)
	}
	oldPolicyID, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "autonomous_task", "plan/autonomous-task.v0",
		strings.Join([]string{"autonomous-task.v0", "ren", "mio", "2026-11-15", ""}, "\x00"))
	if err != nil || string(first) == oldPolicyID {
		t.Fatalf("current policy ID %q err=%v reused old policy identity %q", first, err, oldPolicyID)
	}
	otherOwner, err := AutonomousPlanTaskID("another-user", "mio", "2026-11-15", "")
	if err != nil || otherOwner == first {
		t.Fatalf("different owner plan ID = %q err=%v, want distinct ID", otherOwner, err)
	}
	otherActor, err := AutonomousPlanTaskID("ren", "shiro", "2026-11-15", "")
	if err != nil || otherActor == first {
		t.Fatalf("different actor plan ID = %q err=%v, want distinct ID", otherActor, err)
	}
	for _, date := range []string{"2026-2-01", "2026-02-30", " 2026-02-01"} {
		if _, err := AutonomousPlanTaskID("ren", "mio", date, ""); err == nil {
			t.Errorf("AutonomousPlanTaskID accepted invalid date %q", date)
		}
	}
	if _, err := AutonomousPlanTaskID("ren", "worker", "2026-11-15", ""); err == nil {
		t.Error("AutonomousPlanTaskID accepted a non-canonical actor")
	}
}

func TestAutonomousTaskValidationPinsPrivateReadOnlyShapeAndLineage(t *testing.T) {
	task := autonomousDomainPlan(t)
	if err := task.Validate(); err != nil {
		t.Fatalf("valid autonomous root plan rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Task)
	}{
		{"route", func(value *Task) { value.Route = RouteCode }},
		{"priority", func(value *Task) { value.Priority = PriorityNormal }},
		{"writable", func(value *Task) { value.ReadOnly = false }},
		{"origin", func(value *Task) { value.OriginSessionID = modulecore.NewSessionID() }},
		{"parent", func(value *Task) { value.ParentTaskID = modulecore.NewTaskID() }},
		{"workstream", func(value *Task) { value.WorkstreamID = modulecore.NewWorkstreamID() }},
		{"depth", func(value *Task) { value.Autonomous.Depth = AutonomousMaxDepth + 1 }},
		{"kind", func(value *Task) { value.Autonomous.Kind = "unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := task
			autonomous := *task.Autonomous
			candidate.Autonomous = &autonomous
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("mutated autonomous Task was accepted")
			}
		})
	}
}

func TestAutonomousDecisionRequiresDeterministicBoundedCandidate(t *testing.T) {
	plan := autonomousDomainPlan(t)
	workID, err := AutonomousWorkTaskID(plan.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	wantWorkID, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "autonomous_task", "work/"+AutonomousPolicyVersion,
		AutonomousPolicyVersion+"\x00"+string(plan.TaskID))
	if err != nil || string(workID) != wantWorkID {
		t.Fatalf("work ID %q err=%v, want policy-version-namespaced ID %q", workID, err, wantWorkID)
	}
	valid := AutonomousDecision{
		Decision: AutonomousDecisionContinue, Reason: "A bounded next step is useful.",
		Title: "Review result", Objective: "Review the result", Completion: "Record findings",
		CandidateTaskID: workID,
	}
	if err := valid.Validate(plan.TaskID); err != nil {
		t.Fatalf("valid decision rejected: %v", err)
	}
	invalid := valid
	invalid.CandidateTaskID = modulecore.NewTaskID()
	if err := invalid.Validate(plan.TaskID); err == nil {
		t.Fatal("decision with non-deterministic candidate ID was accepted")
	}
	stop := AutonomousDecision{Decision: AutonomousDecisionStop, Reason: "No useful continuation."}
	if err := stop.Validate(plan.TaskID); err != nil {
		t.Fatalf("valid stop decision rejected: %v", err)
	}
}
