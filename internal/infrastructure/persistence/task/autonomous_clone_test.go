package task

import (
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestTaskAndContextSnapshotsDeepCloneAutonomousMetadata(t *testing.T) {
	task := domaintask.Task{Autonomous: &domaintask.AutonomousTask{PolicyVersion: domaintask.AutonomousPolicyVersion}}
	clonedTask := cloneTransactionTask(task)
	clonedTask.Autonomous.PolicyVersion = "changed"
	if task.Autonomous.PolicyVersion != domaintask.AutonomousPolicyVersion {
		t.Fatal("cloned Task mutated the original autonomous metadata")
	}

	contextValue := domaintask.SharedRoleContext{
		RelevantFiles: []string{"source.md"},
		AutonomousDecision: &domaintask.AutonomousDecision{
			Decision: domaintask.AutonomousDecisionContinue, Reason: "reason", CandidateTaskID: "candidate",
		},
	}
	clonedContext := cloneTransactionContext(contextValue)
	clonedContext.AutonomousDecision.Reason = "changed"
	clonedContext.RelevantFiles[0] = "changed.md"
	if contextValue.AutonomousDecision.Reason != "reason" || contextValue.RelevantFiles[0] != "source.md" {
		t.Fatal("cloned context mutated the original autonomous metadata or slice")
	}
}
