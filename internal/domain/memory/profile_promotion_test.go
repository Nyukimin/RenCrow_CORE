package memory

import "testing"

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
