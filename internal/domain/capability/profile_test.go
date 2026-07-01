package capability

import "testing"

func TestDetermineProfile(t *testing.T) {
	tests := []struct {
		name string
		caps NodeCapabilities
		want Profile
	}{
		{
			name: "claude available → coder-high",
			caps: NodeCapabilities{
				Memory: MemoryInfo{AvailableMB: 16384},
				LLMs: []LLMCapability{
					{ProviderName: "claude", Available: true, Quality: 5},
				},
			},
			want: ProfileCoderHigh,
		},
		{
			name: "openai available → coder-mid",
			caps: NodeCapabilities{
				Memory: MemoryInfo{AvailableMB: 16384},
				LLMs: []LLMCapability{
					{ProviderName: "openai", Available: true, Quality: 4},
				},
			},
			want: ProfileCoderMid,
		},
		{
			name: "deepseek available → coder-mid",
			caps: NodeCapabilities{
				Memory: MemoryInfo{AvailableMB: 16384},
				LLMs: []LLMCapability{
					{ProviderName: "deepseek", Available: true, Quality: 3},
				},
			},
			want: ProfileCoderMid,
		},
		{
			name: "ollama only 8GB+ → coder-local",
			caps: NodeCapabilities{
				Memory: MemoryInfo{AvailableMB: 8 * 1024},
				LLMs: []LLMCapability{
					{ProviderName: "ollama", Available: true, Quality: 2},
				},
			},
			want: ProfileCoderLocal,
		},
		{
			name: "ollama only under 8GB → worker-only",
			caps: NodeCapabilities{
				Memory: MemoryInfo{AvailableMB: 4 * 1024},
				LLMs: []LLMCapability{
					{ProviderName: "ollama", Available: true, Quality: 2},
				},
			},
			want: ProfileWorkerOnly,
		},
		{
			name: "no available LLMs → unavailable",
			caps: NodeCapabilities{
				Memory: MemoryInfo{AvailableMB: 16384},
				LLMs: []LLMCapability{
					{ProviderName: "claude", Available: false},
				},
			},
			want: ProfileUnavailable,
		},
		{
			name: "empty LLMs → unavailable",
			caps: NodeCapabilities{},
			want: ProfileUnavailable,
		},
		{
			name: "claude takes priority over openai",
			caps: NodeCapabilities{
				Memory: MemoryInfo{AvailableMB: 16384},
				LLMs: []LLMCapability{
					{ProviderName: "openai", Available: true, Quality: 4},
					{ProviderName: "claude", Available: true, Quality: 5},
				},
			},
			want: ProfileCoderHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetermineProfile(tt.caps)
			if got != tt.want {
				t.Errorf("DetermineProfile() = %q, want %q", got, tt.want)
			}
		})
	}
}
