package speech

import (
	"context"
	"testing"
)

type playbackProgressStub struct{}

func (playbackProgressStub) MarkReady(int) error                    { return nil }
func (playbackProgressStub) WaitStarted(context.Context, int) error { return nil }
func (playbackProgressStub) WaitEnded(context.Context, int) error   { return nil }
func (playbackProgressStub) MarkStarted(int) error                  { return nil }
func (playbackProgressStub) MarkEnded(int) error                    { return nil }
func (playbackProgressStub) MarkError(int, error) error             { return nil }
func (playbackProgressStub) MarkCancelled(int) error                { return nil }

func TestPlaybackProgressContextCarriesTurnOwner(t *testing.T) {
	progress := playbackProgressStub{}
	ctx := WithPlaybackProgress(context.Background(), progress)
	if got := PlaybackProgressFromContext(ctx); got == nil {
		t.Fatal("PlaybackProgressFromContext() returned nil")
	}
}

func TestPlaybackProgressContextKeepsNilInputsSafe(t *testing.T) {
	if got := WithPlaybackProgress(nil, nil); got == nil {
		t.Fatal("WithPlaybackProgress(nil, nil) returned nil context")
	}
	if got := PlaybackProgressFromContext(nil); got != nil {
		t.Fatalf("PlaybackProgressFromContext(nil) = %v, want nil", got)
	}
}
