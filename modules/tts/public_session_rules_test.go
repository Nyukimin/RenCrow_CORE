package tts

import "testing"

func TestFormatAndParseFixed4(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{-1, "0000"},
		{0, "0000"},
		{9, "0009"},
		{10, "0010"},
		{99, "0099"},
		{100, "0100"},
		{999, "0999"},
		{1000, "1000"},
	} {
		if got := FormatFixed4(tc.n); got != tc.want {
			t.Fatalf("FormatFixed4(%d) = %q, want %q", tc.n, got, tc.want)
		}
		parsed, ok := ParseFixed4(tc.want)
		if !ok || parsed < 0 {
			t.Fatalf("ParseFixed4(%q) = %d/%t", tc.want, parsed, ok)
		}
	}
}

func TestParseTrailingResponseNumber(t *testing.T) {
	got, ok := ParseTrailingResponseNumber("idle-session:msg:0007")
	if !ok || got != 7 {
		t.Fatalf("trailing response number = %d/%t, want 7/true", got, ok)
	}
	if _, ok := ParseTrailingResponseNumber("idle-session:msg:abc"); ok {
		t.Fatal("non-fixed4 suffix should not parse")
	}
}

func TestIsIdleChatPublicSession(t *testing.T) {
	for _, sessionID := range []string{"idle-1", "forecast-1", "story-1", "story-simple-1"} {
		if !IsIdleChatPublicSession(sessionID) {
			t.Fatalf("%q should be idlechat public session", sessionID)
		}
	}
	if IsIdleChatPublicSession("normal-session") {
		t.Fatal("normal-session should not be idlechat public session")
	}
}
