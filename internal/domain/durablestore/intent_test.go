package durablestore

import "testing"

func TestNormalizeStorageIntent(t *testing.T) {
	tests := []struct {
		name    string
		message string
		handled bool
		outcome RequestedOutcome
	}{
		{"implicit implement", "今後この情報を蓄積して検索できるようにして", true, OutcomeImplement},
		{"explicit implement", "XのBookmarkをDBに保存する仕組みを実装して", true, OutcomeImplement},
		{"assessment", "案件DBの設計案を比較して", true, OutcomeAssess},
		{"general question", "データベースについて教えて", false, ""},
		{"ordinary chat", "今日の天気は？", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, handled := NormalizeStorageIntent(tt.message)
			if handled != tt.handled {
				t.Fatalf("handled=%v, want %v (intent=%+v)", handled, tt.handled, got)
			}
			if handled && got.RequestedOutcome != tt.outcome {
				t.Fatalf("outcome=%q, want %q", got.RequestedOutcome, tt.outcome)
			}
		})
	}
}
