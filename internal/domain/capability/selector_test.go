package capability_test

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

func TestSelectCoder_CODE3_DirectMatch(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder1", Quality: 3, Available: true},
		{Name: "coder2", Quality: 4, Available: true},
		{Name: "coder3", Quality: 5, Available: true},
	}
	selected, degraded, err := capability.SelectCoder(coders, routing.RouteCODE3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "coder3" {
		t.Errorf("expected coder3, got %s", selected)
	}
	if degraded != "" {
		t.Errorf("expected no degradation, got %s", degraded)
	}
}

func TestSelectCoder_CODE3_DegradeToQuality4(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder1", Quality: 3, Available: true},
		{Name: "coder2", Quality: 4, Available: true},
		{Name: "coder3", Quality: 5, Available: false},
	}
	selected, degraded, err := capability.SelectCoder(coders, routing.RouteCODE3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "coder2" {
		t.Errorf("expected coder2, got %s", selected)
	}
	if degraded != routing.RouteCODE2 {
		t.Errorf("expected degradedRoute CODE2, got %q", degraded)
	}
}

func TestSelectCoder_CODE3_DegradeToQuality3(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder1", Quality: 3, Available: true},
		{Name: "coder2", Quality: 4, Available: false},
		{Name: "coder3", Quality: 5, Available: false},
	}
	selected, degraded, err := capability.SelectCoder(coders, routing.RouteCODE3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "coder1" {
		t.Errorf("expected coder1, got %s", selected)
	}
	if degraded != routing.RouteCODE1 {
		t.Errorf("expected degradedRoute CODE1, got %q", degraded)
	}
}

func TestSelectCoder_AllUnavailable_Error(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder1", Quality: 3, Available: false},
		{Name: "coder2", Quality: 4, Available: false},
		{Name: "coder3", Quality: 5, Available: false},
	}
	_, _, err := capability.SelectCoder(coders, routing.RouteCODE3)
	if err == nil {
		t.Error("expected error when all coders unavailable")
	}
}

func TestSelectCoder_CODE_PicksHighestQuality(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder1", Quality: 3, Available: true},
		{Name: "coder2", Quality: 4, Available: true},
		{Name: "coder3", Quality: 5, Available: false},
	}
	selected, _, err := capability.SelectCoder(coders, routing.RouteCODE)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "coder2" {
		t.Errorf("expected coder2 (highest available quality), got %s", selected)
	}
}

func TestSelectCoder_CODE2_DirectMatch(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder1", Quality: 3, Available: true},
		{Name: "coder2", Quality: 4, Available: true},
	}
	selected, degraded, err := capability.SelectCoder(coders, routing.RouteCODE2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "coder2" {
		t.Errorf("expected coder2, got %s", selected)
	}
	if degraded != "" {
		t.Errorf("expected no degradation, got %q", degraded)
	}
}

func TestSelectCoder_SameQuality_AlphabeticalOrder(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder4", Quality: 4, Available: true},
		{Name: "coder2", Quality: 4, Available: true},
	}
	selected, _, err := capability.SelectCoder(coders, routing.RouteCODE)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "coder2" {
		t.Errorf("expected coder2 (alphabetically first among equal quality), got %s", selected)
	}
}

func TestSelectCoderWithEvidence_RecordsCapabilityDecision(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder1", Quality: 3, Available: true},
		{Name: "coder2", Quality: 4, Available: true},
		{Name: "coder3", Quality: 5, Available: false},
	}

	selected, degraded, evidence, err := capability.SelectCoderWithEvidence(coders, routing.RouteCODE3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "coder2" {
		t.Fatalf("selected=%q, want coder2", selected)
	}
	if degraded != routing.RouteCODE2 {
		t.Fatalf("degraded=%q, want CODE2", degraded)
	}
	if evidence.RequestedRoute != routing.RouteCODE3 {
		t.Fatalf("requested route=%q, want CODE3", evidence.RequestedRoute)
	}
	if evidence.RequiredQuality != 5 {
		t.Fatalf("required quality=%d, want 5", evidence.RequiredQuality)
	}
	if evidence.Selected != "coder2" || evidence.SelectedQuality != 4 {
		t.Fatalf("selected evidence=%s/%d, want coder2/4", evidence.Selected, evidence.SelectedQuality)
	}
	if evidence.DegradedRoute != routing.RouteCODE2 {
		t.Fatalf("evidence degraded route=%q, want CODE2", evidence.DegradedRoute)
	}
	if len(evidence.Candidates) != 3 {
		t.Fatalf("candidate count=%d, want 3", len(evidence.Candidates))
	}
	assertCandidate := func(name, reason string) {
		t.Helper()
		for _, candidate := range evidence.Candidates {
			if candidate.Name == name {
				if candidate.Reason != reason {
					t.Fatalf("%s reason=%q, want %q", name, candidate.Reason, reason)
				}
				return
			}
		}
		t.Fatalf("candidate %s not found in evidence", name)
	}
	assertCandidate("coder1", capability.SelectionReasonBelowRequiredQuality)
	assertCandidate("coder2", capability.SelectionReasonSelectedWithDegradation)
	assertCandidate("coder3", capability.SelectionReasonUnavailable)
}

func TestSelectCoderWithEvidence_AllUnavailableStillReturnsEvidence(t *testing.T) {
	coders := []capability.CoderCapability{
		{Name: "coder1", Quality: 3, Available: false},
		{Name: "coder2", Quality: 4, Available: false},
	}

	_, _, evidence, err := capability.SelectCoderWithEvidence(coders, routing.RouteCODE2)
	if err == nil {
		t.Fatal("expected error")
	}
	if evidence.RequestedRoute != routing.RouteCODE2 {
		t.Fatalf("requested route=%q, want CODE2", evidence.RequestedRoute)
	}
	if evidence.Selected != "" {
		t.Fatalf("selected=%q, want empty", evidence.Selected)
	}
	if len(evidence.Candidates) != 2 {
		t.Fatalf("candidate count=%d, want 2", len(evidence.Candidates))
	}
	for _, candidate := range evidence.Candidates {
		if candidate.Reason != capability.SelectionReasonUnavailable {
			t.Fatalf("%s reason=%q, want unavailable", candidate.Name, candidate.Reason)
		}
	}
}
