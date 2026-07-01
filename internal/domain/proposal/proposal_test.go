package proposal

import "testing"

func TestNewProposal(t *testing.T) {
	plan := "Step 1: Create file\nStep 2: Test"
	patch := `[{"type":"file_edit","action":"create","target":"test.go","content":"package main"}]`
	risk := "Low risk"
	costHint := "5 minutes"

	proposal := NewProposal(plan, patch, risk, costHint)

	if proposal.Plan() != plan {
		t.Errorf("Expected plan '%s', got '%s'", plan, proposal.Plan())
	}

	if proposal.Patch() != patch {
		t.Errorf("Expected patch '%s', got '%s'", patch, proposal.Patch())
	}

	if proposal.Risk() != risk {
		t.Errorf("Expected risk '%s', got '%s'", risk, proposal.Risk())
	}

	if proposal.CostHint() != costHint {
		t.Errorf("Expected costHint '%s', got '%s'", costHint, proposal.CostHint())
	}
}

func TestProposalIsValid(t *testing.T) {
	tests := []struct {
		name     string
		plan     string
		patch    string
		expected bool
	}{
		{"Valid proposal", "Plan content", "Patch content", true},
		{"Missing plan", "", "Patch content", false},
		{"Missing patch", "Plan content", "", false},
		{"Missing both", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proposal := NewProposal(tt.plan, tt.patch, "risk", "cost")
			if proposal.IsValid() != tt.expected {
				t.Errorf("Expected IsValid()=%v, got %v", tt.expected, proposal.IsValid())
			}
		})
	}
}

func TestReconstruct(t *testing.T) {
	plan := "Reconstructed plan"
	patch := "Reconstructed patch"
	risk := "Low"
	costHint := "1 minute"

	proposal := Reconstruct(plan, patch, risk, costHint)

	if proposal.Plan() != plan {
		t.Errorf("Expected plan '%s', got '%s'", plan, proposal.Plan())
	}
	if proposal.Patch() != patch {
		t.Errorf("Expected patch '%s', got '%s'", patch, proposal.Patch())
	}
	if proposal.Risk() != risk {
		t.Errorf("Expected risk '%s', got '%s'", risk, proposal.Risk())
	}
	if proposal.CostHint() != costHint {
		t.Errorf("Expected costHint '%s', got '%s'", costHint, proposal.CostHint())
	}
}
