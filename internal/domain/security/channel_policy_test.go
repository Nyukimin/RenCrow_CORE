package security

import "testing"

func TestChannelPolicyEvaluateAllowsDMAndRejectsUnknownSender(t *testing.T) {
	policy := ChannelPolicy{
		AllowDM:        true,
		AllowedSenders: []string{"U123"},
	}

	allow := policy.Evaluate(ChannelRequest{Channel: "line", SourceType: ChannelSourceDM, SenderID: "U123", ChatID: "U123"})
	if !allow.Allowed {
		t.Fatalf("expected allowed DM, got %#v", allow)
	}

	deny := policy.Evaluate(ChannelRequest{Channel: "line", SourceType: ChannelSourceDM, SenderID: "U999", ChatID: "U999"})
	if deny.Allowed {
		t.Fatalf("expected denied unknown sender")
	}
	if deny.Reason != ChannelDenyUnknownSender {
		t.Fatalf("reason=%q, want unknown_sender", deny.Reason)
	}
}

func TestChannelPolicyEvaluateRejectsUnpairedGroup(t *testing.T) {
	policy := ChannelPolicy{
		AllowGroups:  true,
		PairedGroups: []string{"G-paired"},
	}

	deny := policy.Evaluate(ChannelRequest{Channel: "line", SourceType: ChannelSourceGroup, SenderID: "U123", ChatID: "G-new"})
	if deny.Allowed {
		t.Fatal("expected denied unpaired group")
	}
	if deny.Reason != ChannelDenyUnpairedGroup {
		t.Fatalf("reason=%q, want unpaired_group", deny.Reason)
	}

	allow := policy.Evaluate(ChannelRequest{Channel: "line", SourceType: ChannelSourceGroup, SenderID: "U123", ChatID: "G-paired"})
	if !allow.Allowed {
		t.Fatalf("expected paired group allowed, got %#v", allow)
	}
}

func TestChannelPolicyEvaluateRejectsGroupWhenDisabled(t *testing.T) {
	policy := ChannelPolicy{AllowDM: true, AllowGroups: false}

	deny := policy.Evaluate(ChannelRequest{Channel: "line", SourceType: ChannelSourceGroup, SenderID: "U123", ChatID: "G1"})
	if deny.Allowed {
		t.Fatal("expected group denied")
	}
	if deny.Reason != ChannelDenyGroupDisabled {
		t.Fatalf("reason=%q, want group_disabled", deny.Reason)
	}
}
