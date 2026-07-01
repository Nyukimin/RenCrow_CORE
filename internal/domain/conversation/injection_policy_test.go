package conversation

import "testing"

func TestInjectionPolicyRejectsUnsafeCandidates(t *testing.T) {
	policy := NewInjectionPolicy("chat")
	cases := []struct {
		name string
		in   RecallCandidate
		want string
	}{
		{
			name: "candidate user memory",
			in:   RecallCandidate{Kind: "user_memory", State: "candidate", Sensitivity: "normal", Scope: "all_personas"},
			want: TraceStatusFilteredStatus,
		},
		{
			name: "sensitive user memory",
			in:   RecallCandidate{Kind: "user_memory", State: "confirmed", Sensitivity: "sensitive", Scope: "all_personas"},
			want: TraceStatusFilteredSensitivity,
		},
		{
			name: "runtime log",
			in:   RecallCandidate{Kind: "context_usage", SourceType: "runtime_log", State: "confirmed"},
			want: TraceStatusFilteredStatus,
		},
		{
			name: "worker scoped",
			in:   RecallCandidate{Kind: "thread_summary", State: "confirmed", Roles: []string{"worker"}},
			want: TraceStatusFilteredScope,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.Decide(tc.in)
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s: %+v", got.Status, tc.want, got)
			}
		})
	}
}

func TestInjectionPolicyAllowsConfirmedUserMemory(t *testing.T) {
	got := NewInjectionPolicy("chat").Decide(RecallCandidate{
		Kind:        "user_memory",
		State:       "confirmed",
		Sensitivity: "normal",
		Scope:       "all_personas",
		Score:       0.9,
	})
	if got.Status != TraceStatusInjected || got.PromptSection != PromptSectionUserMemory {
		t.Fatalf("unexpected decision: %+v", got)
	}
}
