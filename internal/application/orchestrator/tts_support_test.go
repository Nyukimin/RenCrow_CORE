package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func TestSplitTTSChunksSplitsLongFinalText(t *testing.T) {
	text := "今日はいい天気ですね。少し歩いてから、温かいお茶を飲みましょう。最後に明日の予定も確認します。"

	chunks := SplitTTSChunks(text)

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %#v", len(chunks), chunks)
	}
	want := []string{
		"今日はいい天気ですね。",
		"少し歩いてから、温かいお茶を飲みましょう。",
		"最後に明日の予定も確認します。",
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk[%d] = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestSplitTTSChunksForceSplitsTextWithoutBoundaries(t *testing.T) {
	text := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"

	chunks := SplitTTSChunks(text)

	if len(chunks) != 2 {
		t.Fatalf("expected forced split into 2 chunks, got %d: %#v", len(chunks), chunks)
	}
	if len([]rune(chunks[0])) != ttsChunkMaxRunes {
		t.Fatalf("expected first chunk length %d, got %d", ttsChunkMaxRunes, len([]rune(chunks[0])))
	}
}

func TestSplitTTSChunksKeepsNaturalSentenceTogether(t *testing.T) {
	text := "雨に濡れた掲示板って、まるで過ぎた約束をそのまま残しているみたいだよね。そんな中で見つけた、誰かの忘れ物みたいな切ないメモってどんな感じかな？"

	chunks := SplitTTSChunks(text)

	want := []string{
		"雨に濡れた掲示板って、まるで過ぎた約束をそのまま残しているみたいだよね。",
		"そんな中で見つけた、誰かの忘れ物みたいな切ないメモってどんな感じかな？",
	}
	if len(chunks) != len(want) {
		t.Fatalf("expected %d chunks, got %d: %#v", len(want), len(chunks), chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk[%d] = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestSplitTTSChunksKeepsClosingQuoteWithQuestion(t *testing.T) {
	text := "例えば、AIが提示したデータを見て、生徒の「なぜ？」に寄り添うような対話が鍵になりそうだよ。"

	chunks := SplitTTSChunks(text)

	want := []string{
		"例えば、AIが提示したデータを見て、生徒の「なぜ？」",
		"に寄り添うような対話が鍵になりそうだよ。",
	}
	if len(chunks) != len(want) {
		t.Fatalf("expected %d chunks, got %d: %#v", len(want), len(chunks), chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk[%d] = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestTTSStreamForwarderFinalizeSplitsUnemittedFinalText(t *testing.T) {
	bridge := &recordingTTSBridge{}
	forwarder := newTTSStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "tts:", context.Background())

	forwarder.Finalize(context.Background(), "今日はいい天気ですね。少し歩いてから、温かいお茶を飲みましょう。")

	if len(bridge.texts) != 2 {
		t.Fatalf("expected 2 pushed chunks, got %d: %#v", len(bridge.texts), bridge.texts)
	}
}

func TestTTSFinalizeRevalidatesEachChunkAndDropsRevokedSuffix(t *testing.T) {
	revoked := errors.New("owner binding revoked")
	var mu sync.Mutex
	guardHeld := false
	validateCalls := 0
	bridge := &qualificationCheckingTTSBridge{isGuardHeld: func() bool {
		mu.Lock()
		defer mu.Unlock()
		return guardHeld
	}}
	forwarder := newTTSStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "tts:", context.Background())
	if forwarder == nil {
		t.Fatal("expected TTS forwarder")
	}
	validate := func(admit func() error) error {
		mu.Lock()
		guardHeld = true
		validateCalls++
		call := validateCalls
		var err error
		if call == 1 {
			err = admit()
		}
		guardHeld = false
		mu.Unlock()
		if call > 1 {
			return revoked
		}
		return err
	}
	forwarder.FinalizeWithAdmission(context.Background(), "First sentence. Second sentence.", validate)

	if got := bridge.snapshot(); len(got) != 1 || got[0] != "First sentence." {
		t.Fatalf("revoked suffix was handed to TTS: %#v", got)
	}
	if validateCalls != 2 {
		t.Fatalf("validator calls = %d, want one per queued chunk (2)", validateCalls)
	}
	if err := forwarder.Err(); !errors.Is(err, revoked) {
		t.Fatalf("forwarder Err() = %v, want revocation error", err)
	}
	if bridge.sawGuardHeld() {
		t.Fatal("TTS bridge was called while owner guard was held")
	}
}

func TestStreamBundleReportsRevokedVTuberFinalQualification(t *testing.T) {
	wantErr := errors.New("Session binding revoked before VTuber handoff")
	bridge := &recordingAbortVTuberBridge{}
	bundle := &streamBundle{vtuber: newVTuberStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "vtuber:")}
	bundle.FinalizeWithAdmission(context.Background(), "final answer", func(func() error) error {
		return wantErr
	})
	if err := bundle.Err(); !errors.Is(err, wantErr) {
		t.Fatalf("stream bundle Err()=%v, want qualification error %v", err, wantErr)
	}
	if got := bridge.snapshot(); len(got) != 0 {
		t.Fatalf("revoked VTuber final text was handed off: %#v", got)
	}
}

type qualificationCheckingTTSBridge struct {
	mu          sync.Mutex
	texts       []string
	isGuardHeld func() bool
	sawGuard    bool
}

func (*qualificationCheckingTTSBridge) StartSession(context.Context, TTSSessionStart) error {
	return nil
}

func (b *qualificationCheckingTTSBridge) PushText(_ context.Context, _ string, text string, _ *moduletts.EmotionState) error {
	if b.isGuardHeld != nil && b.isGuardHeld() {
		b.mu.Lock()
		b.sawGuard = true
		b.mu.Unlock()
	}
	b.mu.Lock()
	b.texts = append(b.texts, text)
	b.mu.Unlock()
	return nil
}

func (*qualificationCheckingTTSBridge) EndSession(context.Context, string) error { return nil }

func (b *qualificationCheckingTTSBridge) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.texts...)
}

func (b *qualificationCheckingTTSBridge) sawGuardHeld() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sawGuard
}

type recordingTTSBridge struct {
	texts []string
}

func (b *recordingTTSBridge) StartSession(context.Context, TTSSessionStart) error {
	return nil
}

func (b *recordingTTSBridge) PushText(_ context.Context, _ string, text string, _ *moduletts.EmotionState) error {
	b.texts = append(b.texts, text)
	return nil
}

func (b *recordingTTSBridge) EndSession(context.Context, string) error {
	return nil
}
