package agentprofile

import "testing"

func TestStaticCatalogListsEightAgents(t *testing.T) {
	catalog := NewStaticCatalog()
	profiles := catalog.List()
	if len(profiles) != 8 {
		t.Fatalf("len(profiles) = %d, want 8", len(profiles))
	}
}

func TestStaticCatalogShiroCanAskAdvisorButRequiresApprovalForGitPush(t *testing.T) {
	catalog := NewStaticCatalog()
	shiro, err := catalog.MustGet("shiro")
	if err != nil {
		t.Fatalf("MustGet failed: %v", err)
	}
	if !shiro.AutonomyEnvelope.CanDecide("ask_advisor") {
		t.Fatal("Shiro should be able to decide ask_advisor")
	}
	if shiro.AutonomyEnvelope.CanAct("git_push") || !shiro.AutonomyEnvelope.RequiresApproval("git_push") {
		t.Fatal("Shiro git_push should require approval")
	}
}

func TestStaticCatalogKuroCanRecommendStop(t *testing.T) {
	catalog := NewStaticCatalog()
	kuro, err := catalog.MustGet("kuro")
	if err != nil {
		t.Fatalf("MustGet failed: %v", err)
	}
	if !kuro.AutonomyEnvelope.CanDecide("recommend_stop") {
		t.Fatal("Kuro should be able to recommend_stop")
	}
}
