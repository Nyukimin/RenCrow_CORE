package stt

import "testing"

func TestNormalizeBusyPolicy(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{" direct ", BusyPolicyDirect},
		{" reject ", BusyPolicyReject},
		{" queue_latest ", BusyPolicyQueueLatest},
		{" ", BusyPolicyQueueLatest},
		{"unknown", BusyPolicyQueueLatest},
	}
	for _, tt := range tests {
		if got := NormalizeBusyPolicy(tt.raw); got != tt.want {
			t.Fatalf("NormalizeBusyPolicy(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestBuildBusyPolicyPlan(t *testing.T) {
	queue := BuildBusyPolicyPlan("queue_latest")
	if queue.Policy != BusyPolicyQueueLatest || !queue.UsesQueue || queue.UsesReject || queue.UsesDirect {
		t.Fatalf("queue plan = %+v", queue)
	}
	reject := BuildBusyPolicyPlan("reject")
	if reject.Policy != BusyPolicyReject || reject.UsesQueue || !reject.UsesReject || reject.UsesDirect {
		t.Fatalf("reject plan = %+v", reject)
	}
	direct := BuildBusyPolicyPlan("direct")
	if direct.Policy != BusyPolicyDirect || direct.UsesQueue || direct.UsesReject || !direct.UsesDirect {
		t.Fatalf("direct plan = %+v", direct)
	}
}
