package conversation

import (
	"testing"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

func makeDetectorTestThread(domain string, turnsAgo time.Duration, messages ...string) *domconv.Thread {
	t := domconv.NewThread("test-session", domain)
	for _, msg := range messages {
		m := domconv.NewMessage(domconv.SpeakerUser, msg, nil)
		m.Timestamp = time.Now().Add(-turnsAgo)
		t.AddMessage(m)
	}
	return t
}

func TestDetect_Keyword(t *testing.T) {
	detector := NewThreadBoundaryDetector(nil)
	thread := makeDetectorTestThread("general", 1*time.Minute, "こんにちは", "元気？")

	keywords := []string{"ところで", "別件", "質問変えて", "話変わるけど", "別の話", "それとは別に"}
	for _, kw := range keywords {
		result := detector.Detect(thread, kw+"テストです", "")
		if !result.ShouldCreateNew {
			t.Errorf("keyword %q should trigger new thread", kw)
		}
		if result.Reason != domconv.BoundaryKeyword {
			t.Errorf("expected BoundaryKeyword, got %s", result.Reason)
		}
	}
}

func TestDetect_NoKeyword(t *testing.T) {
	detector := NewThreadBoundaryDetector(nil)
	thread := makeDetectorTestThread("general", 1*time.Minute, "こんにちは")

	result := detector.Detect(thread, "今日の天気はどう？", "")
	if result.ShouldCreateNew {
		t.Error("normal message should not trigger new thread")
	}
}

func TestDetect_Inactivity(t *testing.T) {
	detector := NewThreadBoundaryDetector(nil)
	thread := makeDetectorTestThread("general", 15*time.Minute, "古いメッセージ")

	result := detector.Detect(thread, "久しぶり", "")
	if !result.ShouldCreateNew {
		t.Error("inactivity should trigger new thread")
	}
	if result.Reason != domconv.BoundaryInactivity {
		t.Errorf("expected BoundaryInactivity, got %s", result.Reason)
	}
}

func TestDetect_NoInactivity(t *testing.T) {
	detector := NewThreadBoundaryDetector(nil)
	thread := makeDetectorTestThread("general", 5*time.Minute, "最近のメッセージ")

	result := detector.Detect(thread, "続き", "")
	if result.ShouldCreateNew {
		t.Error("recent message should not trigger inactivity")
	}
}

func TestDetect_DomainChange(t *testing.T) {
	detector := NewThreadBoundaryDetector(nil)
	thread := makeDetectorTestThread("CHAT", 1*time.Minute, "会話中")

	result := detector.Detect(thread, "調べて", "RESEARCH")
	if !result.ShouldCreateNew {
		t.Error("domain change should trigger new thread")
	}
	if result.Reason != domconv.BoundaryDomain {
		t.Errorf("expected BoundaryDomain, got %s", result.Reason)
	}
}

func TestDetect_SameDomain(t *testing.T) {
	detector := NewThreadBoundaryDetector(nil)
	thread := makeDetectorTestThread("CHAT", 1*time.Minute, "会話中")

	result := detector.Detect(thread, "続き", "CHAT")
	if result.ShouldCreateNew {
		t.Error("same domain should not trigger new thread")
	}
}

func TestDetect_EmptyNewDomain(t *testing.T) {
	detector := NewThreadBoundaryDetector(nil)
	thread := makeDetectorTestThread("CHAT", 1*time.Minute, "会話中")

	result := detector.Detect(thread, "続き", "")
	if result.ShouldCreateNew {
		t.Error("empty domain should not trigger new thread")
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float32
		tol  float32
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0, 0.01},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0.0, 0.01},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1.0, 0.01},
		{"similar", []float32{1, 1, 0}, []float32{1, 0.9, 0.1}, 0.98, 0.05},
		{"empty", []float32{}, []float32{}, 0.0, 0.01},
		{"zero", []float32{0, 0, 0}, []float32{1, 0, 0}, 0.0, 0.01},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			if got < tt.want-tt.tol || got > tt.want+tt.tol {
				t.Errorf("cosineSimilarity(%v, %v) = %f, want ~%f", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
