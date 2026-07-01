package tts

import "testing"

func TestStreamChunkerAcceptTokenEmitsCompleteChunks(t *testing.T) {
	var chunker StreamChunker

	if chunks := chunker.AcceptToken("今日はいい天気"); len(chunks) != 0 {
		t.Fatalf("unexpected early chunks: %#v", chunks)
	}
	chunks := chunker.AcceptToken("ですね。続き")
	if len(chunks) != 1 || chunks[0] != "今日はいい天気ですね。" {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
	if !chunker.Emitted() {
		t.Fatal("chunker should remember emitted state")
	}
}

func TestStreamChunkerFinalizeAllSplitsUnemittedFinalText(t *testing.T) {
	var chunker StreamChunker

	chunks := chunker.FinalizeAll("今日はいい天気ですね。少し歩いてから、温かいお茶を飲みましょう。")
	want := []string{"今日はいい天気ですね。", "少し歩いてから、温かいお茶を飲みましょう。"}
	if len(chunks) != len(want) {
		t.Fatalf("expected %d chunks, got %d: %#v", len(want), len(chunks), chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk[%d] = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestStreamChunkerFinalizeOnePreservesFinalTextWhenNothingEmitted(t *testing.T) {
	var chunker StreamChunker

	chunks := chunker.FinalizeOne("今日はいい天気ですね。少し歩きましょう。")
	if len(chunks) != 1 || chunks[0] != "今日はいい天気ですね。少し歩きましょう。" {
		t.Fatalf("unexpected final one chunks: %#v", chunks)
	}
}

func TestStreamChunkerFinalizeOneFlushesPendingAfterEmission(t *testing.T) {
	var chunker StreamChunker
	_ = chunker.AcceptToken("今日はいい天気ですね。残り")

	chunks := chunker.FinalizeOne("")
	if len(chunks) != 1 || chunks[0] != "残り" {
		t.Fatalf("unexpected pending final chunk: %#v", chunks)
	}
}
