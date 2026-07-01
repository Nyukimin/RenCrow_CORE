package vocabulary_test

import (
	"testing"
	"time"

	vocabDomain "github.com/Nyukimin/picoclaw_multiLLM/domain/vocabulary"
)

func TestEntry_IsRecent(t *testing.T) {
	now := time.Now()
	
	tests := []struct {
		name     string
		entry    *vocabDomain.Entry
		expected bool
	}{
		{
			name: "entry from 1 day ago is recent",
			entry: &vocabDomain.Entry{
				Timestamp: now.Add(-24 * time.Hour),
			},
			expected: true,
		},
		{
			name: "entry from 8 days ago is not recent",
			entry: &vocabDomain.Entry{
				Timestamp: now.Add(-8 * 24 * time.Hour),
			},
			expected: false,
		},
		{
			name: "entry from now is recent",
			entry: &vocabDomain.Entry{
				Timestamp: now,
			},
			expected: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.IsRecent(); got != tt.expected {
				t.Errorf("IsRecent() = %v, want %v", got, tt.expected)
			}
		})
	}
}
