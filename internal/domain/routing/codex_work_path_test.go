package routing

import "testing"

func TestDetectCodexWorkPath(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    CodexWorkDomain
	}{
		{
			name:    "drawing japanese",
			message: "この場面を描画して",
			want:    CodexWorkDomainDrawing,
		},
		{
			name:    "drawing english",
			message: "draw a quiet study room",
			want:    CodexWorkDomainDrawing,
		},
		{
			name:    "folktale japanese",
			message: "桃太郎の昔話生成をして",
			want:    CodexWorkDomainFolktale,
		},
		{
			name:    "folktale english",
			message: "generate a folktale about a mountain shrine",
			want:    CodexWorkDomainFolktale,
		},
		{
			name:    "no match",
			message: "普通に相談したい",
			want:    "",
		},
		{
			name:    "empty",
			message: "   ",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectCodexWorkPath(tt.message)
			if got.Domain != tt.want {
				t.Fatalf("Domain = %q, want %q", got.Domain, tt.want)
			}
			if tt.want != "" && !got.Found() {
				t.Fatal("Found should be true for a detected Codex work path")
			}
		})
	}
}
