package entity

import (
	"strings"
	"testing"
	"time"
)

func validGlossaryCandidate() GlossaryCandidate {
	return GlossaryCandidate{
		ID:          "glossary-candidate/sha256:abc",
		Term:        "RenCrow",
		Explanation: "A candidate definition.",
		SourceURL:   "https://example.com/glossary/rencrow",
		Category:    "new_word",
		ProposedBy:  "shiro",
		State:       GlossaryCandidateState,
		CreatedAt:   time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
	}
}

func TestValidateGlossaryCandidate(t *testing.T) {
	if err := ValidateGlossaryCandidate(validGlossaryCandidate()); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
}

func TestValidateGlossaryCandidateRejectsUnsafeOrMalformedFields(t *testing.T) {
	tests := []struct {
		name string
		edit func(*GlossaryCandidate)
	}{
		{"missing id", func(v *GlossaryCandidate) { v.ID = "" }},
		{"http source", func(v *GlossaryCandidate) { v.SourceURL = "http://example.com" }},
		{"relative source", func(v *GlossaryCandidate) { v.SourceURL = "/tmp/source" }},
		{"userinfo source", func(v *GlossaryCandidate) { v.SourceURL = "https://user:pass@example.com/source" }},
		{"unsafe category", func(v *GlossaryCandidate) { v.Category = "../private" }},
		{"wrong state", func(v *GlossaryCandidate) { v.State = "promoted" }},
		{"long explanation", func(v *GlossaryCandidate) {
			v.Explanation = strings.Repeat("x", glossaryCandidateMaxExplanationLength+1)
		}},
		{"missing created at", func(v *GlossaryCandidate) { v.CreatedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validGlossaryCandidate()
			tt.edit(&candidate)
			if err := ValidateGlossaryCandidate(candidate); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
