package tts

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAsyncAudioSinkDoesNotBlockNextChunkOnPlayback(t *testing.T) {
	inner := &blockingAudioSink{
		entered: make(chan int, 2),
		release: make(chan struct{}),
	}
	sink := NewAsyncAudioSink(inner)

	if err := sink.SubmitChunk(context.Background(), "s1", audioChunk{ChunkIndex: 0}); err != nil {
		t.Fatalf("submit first chunk: %v", err)
	}
	select {
	case idx := <-inner.entered:
		if idx != 0 {
			t.Fatalf("first played chunk = %d, want 0", idx)
		}
	case <-time.After(time.Second):
		t.Fatal("first chunk was not submitted to inner sink")
	}

	done := make(chan error, 1)
	go func() {
		done <- sink.SubmitChunk(context.Background(), "s1", audioChunk{ChunkIndex: 1})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("submit second chunk: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second chunk submit blocked behind playback")
	}

	close(inner.release)
	select {
	case idx := <-inner.entered:
		if idx != 1 {
			t.Fatalf("second played chunk = %d, want 1", idx)
		}
	case <-time.After(time.Second):
		t.Fatal("second chunk was not played after release")
	}
}

type blockingAudioSink struct {
	mu       sync.Mutex
	entered  chan int
	release  chan struct{}
	complete int
}

func (s *blockingAudioSink) SubmitChunk(_ context.Context, _ string, ch audioChunk) error {
	s.entered <- ch.ChunkIndex
	<-s.release
	return nil
}

func (s *blockingAudioSink) CompleteSession(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.complete++
	return nil
}
