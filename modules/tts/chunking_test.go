package tts

import "testing"

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
	if len([]rune(chunks[0])) != TTSChunkMaxRunes {
		t.Fatalf("expected first chunk length %d, got %d", TTSChunkMaxRunes, len([]rune(chunks[0])))
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

func TestSplitTTSChunksUsesSoftBoundaryBeforeLongSentenceGetsSlow(t *testing.T) {
	text := "この会話では、少女の「冷たさ＝痛み」という具体的な気づきが、幻想の館の謎を解く鍵となり、物語を深めていきました。"

	chunks := SplitTTSChunks(text)

	want := []string{
		"この会話では、少女の「冷たさ＝痛み」という具体的な気づきが、",
		"幻想の館の謎を解く鍵となり、物語を深めていきました。",
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
