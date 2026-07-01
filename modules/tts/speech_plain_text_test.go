package tts

import "testing"

func TestFormatTTSSpeechPlainTextRemovesMarkdownAndNormalizesBrackets(t *testing.T) {
	got := FormatTTSSpeechPlainText("## **重要** 【Mio】は [資料](https://example.com) を `確認` したよ。")
	want := "重要 「Mio」は 資料 を 確認 したよ。"
	if got != want {
		t.Fatalf("FormatTTSSpeechPlainText() = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextKeepsJapaneseQuoteBrackets(t *testing.T) {
	got := FormatTTSSpeechPlainText("『引用』と「会話」と（補足）と《強調》")
	want := "「引用」と「会話」と「補足」と「強調」"
	if got != want {
		t.Fatalf("brackets = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextRemovesMarkdownLineSyntax(t *testing.T) {
	got := FormatTTSSpeechPlainText("> **要点**\n- まず確認\n1. 次に実行\n---\n| A | B |\n|---|---|")
	want := "要点 まず確認 次に実行 A B"
	if got != want {
		t.Fatalf("line markdown = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextDropsUnsupportedLeadingIcon(t *testing.T) {
	got := FormatTTSSpeechPlainText("🥰 **本文**です。")
	want := "本文です。"
	if got != want {
		t.Fatalf("unsupported leading icon = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextKeepsAllowedLeadingIcon(t *testing.T) {
	got := FormatTTSSpeechPlainText("😊 **本文**です。")
	want := "😊 本文です。"
	if got != want {
		t.Fatalf("allowed leading icon = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextDropsUnsupportedIconUntilAllowedPrefix(t *testing.T) {
	got := FormatTTSSpeechPlainText("🥰😊 **本文**です。")
	want := "😊 本文です。"
	if got != want {
		t.Fatalf("mixed leading icons = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextDropsUnsupportedZWJLeadingIcon(t *testing.T) {
	got := FormatTTSSpeechPlainText("😮‍💨 ため息まじりです。")
	want := "ため息まじりです。"
	if got != want {
		t.Fatalf("unsupported zwj leading icon = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextKeepsTwoAllowedLeadingIcons(t *testing.T) {
	got := FormatTTSSpeechPlainText("😊😆 **本文**です。")
	want := "😊😆 本文です。"
	if got != want {
		t.Fatalf("two allowed leading icons = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextDropsExtraLeadingIconsAfterTwo(t *testing.T) {
	got := FormatTTSSpeechPlainText("😊😆💥 **本文**です。")
	want := "😊😆 本文です。"
	if got != want {
		t.Fatalf("extra leading icons = %q, want %q", got, want)
	}
}

func TestFormatTTSSpeechPlainTextDropsIconsOutsideLeadingPrefix(t *testing.T) {
	got := FormatTTSSpeechPlainText("😊 本文の途中😆にも、未許可🥰にもある。")
	want := "😊 本文の途中にも、未許可にもある。"
	if got != want {
		t.Fatalf("inline icons = %q, want %q", got, want)
	}
}
