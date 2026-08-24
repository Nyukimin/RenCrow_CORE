package memory

import "testing"

func TestProfilePromotionLogicalRequestBudgets(t *testing.T) {
	if ProfilePromotionEvidenceBlockMax != 3000 {
		t.Fatalf("evidence block max=%d want 3000", ProfilePromotionEvidenceBlockMax)
	}
	if ProfilePromotionExistingContextMax != 800 {
		t.Fatalf("existing context max=%d want 800", ProfilePromotionExistingContextMax)
	}
	if ProfilePromotionMaterialDigestMax != 800 {
		t.Fatalf("material digest max=%d want 800", ProfilePromotionMaterialDigestMax)
	}
	if ProfilePromotionMaxTokens != 1024 {
		t.Fatalf("max tokens=%d want 1024", ProfilePromotionMaxTokens)
	}
	if ProfilePromotionPerGroupCandidateLimit != 4 {
		t.Fatalf("per-group candidate max=%d want 4", ProfilePromotionPerGroupCandidateLimit)
	}
	wantInitial := ProfilePromotionEvidenceBlockMax + ProfilePromotionExistingContextMax + ProfilePromotionMaterialDigestMax + ProfilePromotionPromptInstructionMax
	if ProfilePromotionInitialPromptMax != wantInitial {
		t.Fatalf("initial prompt max=%d want derived %d", ProfilePromotionInitialPromptMax, wantInitial)
	}
	wantPrompt := wantInitial + ProfilePromotionRepairInstructionMax
	if ProfilePromotionPromptMax != wantPrompt {
		t.Fatalf("prompt max=%d want derived %d", ProfilePromotionPromptMax, wantPrompt)
	}
}

func TestValidateProfilePromotionProjectionRequiresCanonicalNonEmptyEnums(t *testing.T) {
	base := UserMemory{
		Namespace: "user:ren", UserID: "ren", Type: UserMemoryTypeProfile,
		Statement: "bounded", State: MemoryStateCandidate, Active: true,
		Confidence: 0.7, Sensitivity: "normal", Scope: "all_personas",
	}
	for _, sensitivity := range []string{"", "private"} {
		item := base
		item.Sensitivity = sensitivity
		if err := ValidateProfilePromotionProjection([]UserMemory{item}, "ren"); err == nil {
			t.Fatalf("sensitivity %q was accepted", sensitivity)
		}
	}
	for _, scope := range []string{"", "untrusted"} {
		item := base
		item.Scope = scope
		if err := ValidateProfilePromotionProjection([]UserMemory{item}, "ren"); err == nil {
			t.Fatalf("scope %q was accepted", scope)
		}
	}
}
