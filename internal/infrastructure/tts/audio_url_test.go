package tts

import "testing"

func TestResolveAudioURL_ExplicitRelative(t *testing.T) {
	got := resolveAudioURL("http://192.168.1.33:8765", "cache\\x.wav", "/cache/x.wav")
	want := "http://192.168.1.33:8765/cache/x.wav"
	if got != want {
		t.Fatalf("unexpected url: got=%q want=%q", got, want)
	}
}

func TestResolveAudioURL_FromAudioPath(t *testing.T) {
	got := resolveAudioURL("http://192.168.1.33:8765", `cache\oneshot-1_000.wav`, "")
	want := "http://192.168.1.33:8765/cache/oneshot-1_000.wav"
	if got != want {
		t.Fatalf("unexpected url: got=%q want=%q", got, want)
	}
}

func TestResolveAudioURL_AbsolutePreferred(t *testing.T) {
	got := resolveAudioURL("http://192.168.1.33:8765", "cache\\x.wav", "https://cdn.example/audio.wav")
	want := "https://cdn.example/audio.wav"
	if got != want {
		t.Fatalf("unexpected url: got=%q want=%q", got, want)
	}
}

func TestResolveAudioURL_RewritesLocalAbsoluteToBaseURL(t *testing.T) {
	got := resolveAudioURL("http://192.168.1.33:8765", "cache\\x.wav", "http://127.0.0.1:7870/audio/sample.wav")
	want := "http://192.168.1.33:8765/audio/sample.wav"
	if got != want {
		t.Fatalf("unexpected url: got=%q want=%q", got, want)
	}
}

func TestResolveAudioURL_RewritesPrivateAbsoluteToBaseURL(t *testing.T) {
	got := resolveAudioURL("http://192.168.1.33:8765", "cache\\x.wav", "http://10.0.0.8:7870/audio/sample.wav")
	want := "http://192.168.1.33:8765/audio/sample.wav"
	if got != want {
		t.Fatalf("unexpected url: got=%q want=%q", got, want)
	}
}

func TestResolveAudioURL_FromInternalCachePath(t *testing.T) {
	got := resolveAudioURL("http://192.168.1.33:8765", `cache-a\oneshot-1_000.wav`, "")
	want := "http://192.168.1.33:8765/audio/oneshot-1_000.wav"
	if got != want {
		t.Fatalf("unexpected url: got=%q want=%q", got, want)
	}
}

func TestResolveAudioURL_RewriteAbsoluteInternalCachePath(t *testing.T) {
	got := resolveAudioURL("http://192.168.1.33:8765", "", "http://192.168.1.33:8765/cache-b/oneshot-2_000.wav")
	want := "http://192.168.1.33:8765/audio/oneshot-2_000.wav"
	if got != want {
		t.Fatalf("unexpected url: got=%q want=%q", got, want)
	}
}
