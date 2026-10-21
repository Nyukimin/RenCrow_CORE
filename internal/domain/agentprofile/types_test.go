package agentprofile

import "testing"

func TestAutonomyEnvelopeDecisionAndActionPolicy(t *testing.T) {
	envelope := AutonomyEnvelope{
		Decide:     []string{"ask_advisor"},
		ActAllowed: []string{"run_test", "git_push"},
		Forbidden:  []string{"expose_secret"},
	}
	if !envelope.CanDecide("ASK_ADVISOR") {
		t.Fatal("expected ask_advisor decision to be allowed")
	}
	if !envelope.CanAct("run_test") {
		t.Fatal("expected run_test action to be allowed")
	}
	if !envelope.CanAct("git_push") {
		t.Fatal("git_push should be directly allowed")
	}
	if envelope.CanAct("expose_secret") || !envelope.IsForbidden("expose_secret") {
		t.Fatal("expose_secret should be forbidden")
	}
}

func TestNonCognitiveEnvelopeNormalizesAndRequiresFallbackSubset(t *testing.T) {
	envelope := NonCognitiveEnvelope{
		Allowed:         []string{"walk", "rest", "idle"},
		ExpiredFallback: []string{"sleep", "rest"},
	}
	if _, err := envelope.Normalized(); err == nil {
		t.Fatal("Normalized() accepted a fallback outside allowed")
	}
	envelope.Allowed = append(envelope.Allowed, "sleep")
	got, err := envelope.Normalized()
	if err != nil {
		t.Fatalf("Normalized() error = %v", err)
	}
	if len(got.Allowed) != 4 || got.Allowed[0] != "idle" || got.Allowed[3] != "walk" {
		t.Fatalf("allowed = %#v", got.Allowed)
	}
	if len(got.ExpiredFallback) != 2 || got.ExpiredFallback[0] != "rest" {
		t.Fatalf("fallback = %#v", got.ExpiredFallback)
	}
}

func TestNonCognitiveEnvelopeRejectsEmptyAndDuplicateIDs(t *testing.T) {
	for name, envelope := range map[string]NonCognitiveEnvelope{
		"empty allowed":      {Allowed: nil, ExpiredFallback: []string{}},
		"empty id":           {Allowed: []string{"walk", ""}, ExpiredFallback: []string{}},
		"duplicate":          {Allowed: []string{"walk", "walk"}, ExpiredFallback: []string{}},
		"duplicate fallback": {Allowed: []string{"walk"}, ExpiredFallback: []string{"walk", "walk"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := envelope.Normalized(); err == nil {
				t.Fatal("Normalized() error = nil")
			}
		})
	}
}
