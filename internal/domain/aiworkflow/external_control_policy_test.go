package aiworkflow

import "testing"

func TestEvaluateExternalControlBlocksUnknownActor(t *testing.T) {
	decision := EvaluateExternalControl(ExternalControlPolicy{
		AllowedActors:   []string{"Worker"},
		AllowedChannels: []string{"viewer"},
		AllowedActions:  []string{"promotion_apply"},
	}, ExternalControlRequest{
		Actor:     "external",
		ChannelID: "viewer",
		Action:    "promotion_apply",
	})
	if decision.Status != ExternalControlStatusBlocked {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestEvaluateExternalControlRequiresApprovalForSensitiveAction(t *testing.T) {
	policy := ExternalControlPolicy{
		AllowedActors:    []string{"Worker"},
		AllowedChannels:  []string{"viewer"},
		AllowedActions:   []string{"promotion_apply"},
		ApprovalRequired: []string{"promotion_apply"},
	}
	decision := EvaluateExternalControl(policy, ExternalControlRequest{
		Actor:     "Worker",
		ChannelID: "viewer",
		Action:    "promotion_apply",
	})
	if decision.Status != ExternalControlStatusNeedsApproval || !decision.RequiresApproval {
		t.Fatalf("decision=%#v", decision)
	}
	decision = EvaluateExternalControl(policy, ExternalControlRequest{
		Actor:         "Worker",
		ChannelID:     "viewer",
		Action:        "promotion_apply",
		HumanApproved: true,
	})
	if decision.Status != ExternalControlStatusAllowed || !decision.RequiresApproval {
		t.Fatalf("approved decision=%#v", decision)
	}
}
