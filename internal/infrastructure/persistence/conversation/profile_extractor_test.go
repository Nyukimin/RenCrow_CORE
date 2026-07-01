package conversation

import (
	"testing"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean json", `{"preferences": {}, "facts": []}`, `{"preferences": {}, "facts": []}`},
		{"with prefix", `Here is the result: {"preferences": {"好み": "SF"}, "facts": ["猫が好き"]}`, `{"preferences": {"好み": "SF"}, "facts": ["猫が好き"]}`},
		{"with suffix", `{"preferences": {}, "facts": []} end`, `{"preferences": {}, "facts": []}`},
		{"no json", "no json here", "{}"},
		{"empty", "", "{}"},
		{"nested", `{"a": {"b": "c"}}`, `{"a": {"b": "c"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
