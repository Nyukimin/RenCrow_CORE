package tts

import (
	"testing"
	"time"
)

func TestReorderBuffer_DrainInOrder(t *testing.T) {
	b := newReorderBuffer(3 * time.Second)
	now := time.Now().UTC()
	b.add(audioChunk{ChunkIndex: 1, AudioPath: "b.wav"}, now)
	b.add(audioChunk{ChunkIndex: 0, AudioPath: "a.wav"}, now)

	out := b.drain(now, false)
	if len(out) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(out))
	}
	if out[0].ChunkIndex != 0 || out[1].ChunkIndex != 1 {
		t.Fatalf("unexpected order: %+v", out)
	}
}

func TestReorderBuffer_GapTimeoutSkip(t *testing.T) {
	b := newReorderBuffer(1 * time.Second)
	now := time.Now().UTC()
	b.add(audioChunk{ChunkIndex: 2, AudioPath: "c.wav"}, now)

	out := b.drain(now.Add(2*time.Second), false)
	if len(out) != 1 || out[0].ChunkIndex != 2 {
		t.Fatalf("expected skip to chunk 2, got %+v", out)
	}
}

func TestReorderBuffer_AllowSkipOnComplete(t *testing.T) {
	b := newReorderBuffer(10 * time.Second)
	now := time.Now().UTC()
	b.add(audioChunk{ChunkIndex: 3, AudioPath: "d.wav"}, now)

	out := b.drain(now, true)
	if len(out) != 1 || out[0].ChunkIndex != 3 {
		t.Fatalf("expected force drain chunk 3, got %+v", out)
	}
}
