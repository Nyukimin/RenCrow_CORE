package developmentmethodology

import (
	"testing"
	"time"

	skillgovernance "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

func TestTeamComposerMatchesCapabilityButNeverExpandsAuthority(t *testing.T) {
	skill := skillgovernance.SkillManifest{SkillID: "core.tdd", Name: "TDD", Scope: skillgovernance.ScopeCore, Path: "workspace/skills/tdd", UpdatedAt: time.Now(), DevelopmentContract: skillgovernance.DevelopmentContract{RequiredCapability: "code_change", RequiredTools: []string{"git", "go"}, RequiredKnowledge: []string{"AGENTS.md"}, AuthorityRequirement: "worker", InputContract: "task", OutputContract: "receipt", CostHint: "medium", RiskLevel: "medium", EvaluationMethod: "tests", Version: "1.0.0"}}
	candidates := []TeamCandidate{{ExecutionID: "coder-a", Role: RoleCoder, Capabilities: []string{"code_change"}, Tools: []string{"git", "go"}}, {ExecutionID: "worker-b", Role: RoleWorker, Capabilities: []string{"code_change"}, Tools: []string{"git", "go"}}}
	assignment, err := ComposeTeam(skill, OperationRepositoryWrite, candidates)
	if err != nil || assignment.ExecutionID != "worker-b" || assignment.Role != RoleWorker {
		t.Fatalf("assignment=%+v err=%v", assignment, err)
	}
	if _, err := ComposeTeam(skill, OperationRepositoryWrite, candidates[:1]); err == nil {
		t.Fatal("skill capability expanded coder authority")
	}
}

func TestTeamComposerFailsClosedOnMissingToolsOrCapability(t *testing.T) {
	skill := skillgovernance.SkillManifest{SkillID: "core.review", Name: "Review", Scope: skillgovernance.ScopeCore, Path: "workspace/skills/review", UpdatedAt: time.Now(), DevelopmentContract: skillgovernance.DevelopmentContract{RequiredCapability: "review", RequiredTools: []string{"git"}, RequiredKnowledge: []string{"spec"}, AuthorityRequirement: "reviewer", InputContract: "diff", OutputContract: "review", CostHint: "low", RiskLevel: "low", EvaluationMethod: "review test", Version: "1.0.0"}}
	if _, err := ComposeTeam(skill, OperationReview, []TeamCandidate{{ExecutionID: "reviewer", Role: RoleReviewer, Capabilities: []string{"review"}, Tools: []string{"rg"}}}); err == nil {
		t.Fatal("missing required tool accepted")
	}
}
