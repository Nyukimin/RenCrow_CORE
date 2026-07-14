package agentprofile

import "testing"

func TestAutonomyEnvelopeDecisionAndActionPolicy(t *testing.T) {
	envelope := AutonomyEnvelope{
		Decide:           []string{"ask_advisor"},
		ActAllowed:       []string{"run_test"},
		ApprovalRequired: []string{"git_push"},
		Forbidden:        []string{"expose_secret"},
	}
	if !envelope.CanDecide("ASK_ADVISOR") {
		t.Fatal("expected ask_advisor decision to be allowed")
	}
	if !envelope.CanAct("run_test") {
		t.Fatal("expected run_test action to be allowed")
	}
	if envelope.CanAct("git_push") || !envelope.RequiresApproval("git_push") {
		t.Fatal("git_push should require approval and not be directly allowed")
	}
	if envelope.CanAct("expose_secret") || !envelope.IsForbidden("expose_secret") {
		t.Fatal("expose_secret should be forbidden")
	}
}
