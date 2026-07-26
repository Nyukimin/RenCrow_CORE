package agentprofile

import "testing"

func TestStaticCatalogListsEightAgents(t *testing.T) {
	catalog := NewStaticCatalog()
	profiles := catalog.List()
	if len(profiles) != 8 {
		t.Fatalf("len(profiles) = %d, want 8", len(profiles))
	}
}

func TestStaticCatalogCoderIdentityMapping(t *testing.T) {
	catalog := NewStaticCatalog()
	tests := []struct {
		id   string
		role string
	}{
		{id: "aka", role: "Coder1 / architecture"},
		{id: "ao", role: "Coder2 / implementation"},
		{id: "kin", role: "Coder3 / risk and hard implementation"},
		{id: "gin", role: "Coder4 / comparison and finish"},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			profile, err := catalog.MustGet(tt.id)
			if err != nil {
				t.Fatalf("MustGet(%q) failed: %v", tt.id, err)
			}
			if profile.Role != tt.role {
				t.Fatalf("%s role = %q, want %q", tt.id, profile.Role, tt.role)
			}
		})
	}
}

func TestStaticCatalogShiroCanAskAdvisorAndPolicyAllowsGitPush(t *testing.T) {
	catalog := NewStaticCatalog()
	shiro, err := catalog.MustGet("shiro")
	if err != nil {
		t.Fatalf("MustGet failed: %v", err)
	}
	if !shiro.AutonomyEnvelope.CanDecide("ask_advisor") {
		t.Fatal("Shiro should be able to decide ask_advisor")
	}
	if !shiro.AutonomyEnvelope.CanAct("git_push") {
		t.Fatal("Shiro git_push should be allowed")
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
