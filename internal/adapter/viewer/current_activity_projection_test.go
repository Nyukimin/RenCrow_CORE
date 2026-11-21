package viewer

import "testing"

func TestFormatPublicCurrentActivityUsesCuratedLabelsAndPhases(t *testing.T) {
	tests := []struct {
		kind, label, phase, actor, wantLabel, wantPhase string
	}{
		{"work", "user supplied private task title", "arbitrary", "mio", "作業中", "working"},
		{"work", "CORE agent routine: daily diary", "active", "mio", "日記を書く", "working"},
		{"chat", "chat-with-shiro", "unknown", "mio", "シロと会話中", "talking"},
		{"chat", "private chat title", "unknown", "mio", "会話中", "talking"},
		{"hobby", "fashion", "unknown", "mio", "ファッション中", "active"},
		{"hobby", "private hobby title", "unknown", "mio", "趣味中", "active"},
		{"rest", "anything", "unknown", "mio", "休憩中", "resting"},
		{"sleep", "anything", "sleep_window", "mio", "就寝予定", "sleep_window"},
		{"sleep", "anything", "sleeping", "mio", "就寝中", "sleeping"},
		{"moving", "anything", "anything", "mio", "移動中", "in_transit"},
		{"game", "nethack", "playing", "mio", "NetHackをプレイ中", "playing"},
		{"game", "private title", "playing", "mio", "ゲーム中", "playing"},
		{"game", "nethack", "launching", "mio", "NetHackを準備中", "preparing"},
		{"evaluating", "anything", "anything", "mio", "次の活動を選択中", "waiting"},
	}
	for _, test := range tests {
		got, ok := FormatPublicCurrentActivity(test.kind, test.label, test.phase, test.actor, 7)
		if !ok || got.Kind != test.kind || got.Label != test.wantLabel || got.Phase != test.wantPhase || got.Revision != 7 {
			t.Errorf("FormatPublicCurrentActivity(%q, %q) = %+v, %t; want label=%q phase=%q", test.kind, test.label, got, ok, test.wantLabel, test.wantPhase)
		}
	}
	if got, ok := FormatPublicCurrentActivity("unknown", "private", "private", "mio", 7); ok || got != (PublicCurrentActivity{}) {
		t.Fatalf("unknown activity = %+v, %t; want rejected", got, ok)
	}
	if got, ok := FormatPublicCurrentActivity("game", "nethack", "unknown", "mio", 7); ok || got != (PublicCurrentActivity{}) {
		t.Fatalf("unknown GAME phase = %+v, %t; want rejected", got, ok)
	}
	if got, ok := FormatPublicCurrentActivity("work", "Diary", "active", "mio", 0); ok || got != (PublicCurrentActivity{}) {
		t.Fatalf("zero revision activity = %+v, %t; want rejected", got, ok)
	}
}
