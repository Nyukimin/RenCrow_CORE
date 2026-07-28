package capability

import "testing"

func TestDetermineProfile(t *testing.T) {
	tests := []struct {
		name string
		caps NodeCapabilities
		want Profile
	}{
		{
			name: "quality five",
			caps: NodeCapabilities{LLMs: []LLMCapability{
				{ProviderName: "rencrow_llm", ModelName: "coder1", Available: true, Quality: 5},
			}},
			want: ProfileCoderHigh,
		},
		{
			name: "quality three",
			caps: NodeCapabilities{LLMs: []LLMCapability{
				{ProviderName: "rencrow_llm", ModelName: "coder2", Available: true, Quality: 3},
			}},
			want: ProfileCoderStandard,
		},
		{
			name: "quality one",
			caps: NodeCapabilities{LLMs: []LLMCapability{
				{ProviderName: "rencrow_llm", ModelName: "worker", Available: true, Quality: 1},
			}},
			want: ProfileWorker,
		},
		{
			name: "unavailable alias",
			caps: NodeCapabilities{LLMs: []LLMCapability{
				{ProviderName: "rencrow_llm", ModelName: "coder1", Available: false, Quality: 5},
			}},
			want: ProfileUnavailable,
		},
		{name: "empty", caps: NodeCapabilities{}, want: ProfileUnavailable},
		{
			name: "highest available quality wins",
			caps: NodeCapabilities{LLMs: []LLMCapability{
				{ProviderName: "rencrow_llm", ModelName: "worker", Available: true, Quality: 1},
				{ProviderName: "rencrow_llm", ModelName: "coder1", Available: true, Quality: 5},
			}},
			want: ProfileCoderHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetermineProfile(tt.caps); got != tt.want {
				t.Fatalf("DetermineProfile() = %q, want %q", got, tt.want)
			}
		})
	}
}
