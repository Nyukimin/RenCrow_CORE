package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

func TestSpeechTurnCancellationLatchesBeforeSynthesisStarts(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	latch.Cancel()

	ctx := WithSpeechTurnCancellation(context.Background(), latch)
	speechCtx, done := SpeechSynthesisContext(ctx)
	defer done()
	if !errors.Is(speechCtx.Err(), context.Canceled) {
		t.Fatalf("speech context error=%v want=%v", speechCtx.Err(), context.Canceled)
	}
}

func TestSpeechTurnCancellationCancelsSpeechChildOnlyAndIsIdempotent(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	latch := NewSpeechTurnCancellation()
	speechCtx, done := SpeechSynthesisContext(WithSpeechTurnCancellation(parent, latch))
	defer done()

	latch.Cancel()
	latch.Cancel()
	if !errors.Is(speechCtx.Err(), context.Canceled) {
		t.Fatalf("speech context error=%v want=%v", speechCtx.Err(), context.Canceled)
	}
	if parent.Err() != nil {
		t.Fatalf("parent context was canceled: %v", parent.Err())
	}
}

func TestSpeechTurnCancellationCancelsAllBoundSpeechChildren(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	ctx := WithSpeechTurnCancellation(context.Background(), latch)
	first, doneFirst := SpeechSynthesisContext(ctx)
	defer doneFirst()
	second, doneSecond := SpeechSynthesisContext(ctx)
	defer doneSecond()

	latch.Cancel()
	for name, speechCtx := range map[string]context.Context{"first": first, "second": second} {
		if !errors.Is(speechCtx.Err(), context.Canceled) {
			t.Errorf("%s speech context error=%v want=%v", name, speechCtx.Err(), context.Canceled)
		}
	}
}

func TestSpeechTurnCancellationDoneUnbindsOnlyItsOwnChild(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	ctx := WithSpeechTurnCancellation(context.Background(), latch)
	oldChild, doneOld := SpeechSynthesisContext(ctx)
	doneOld()
	newChild, doneNew := SpeechSynthesisContext(ctx)
	defer doneNew()

	// A delayed or repeated cleanup for the old child must not unbind the new one.
	doneOld()
	latch.Cancel()
	if !errors.Is(oldChild.Err(), context.Canceled) {
		t.Fatalf("old speech context error=%v want=%v", oldChild.Err(), context.Canceled)
	}
	if !errors.Is(newChild.Err(), context.Canceled) {
		t.Fatalf("new speech context error=%v want=%v", newChild.Err(), context.Canceled)
	}
}

func TestSpeechTurnCancellationNilLatchLeavesSpeechContextUsable(t *testing.T) {
	speechCtx, done := SpeechSynthesisContext(context.Background())
	defer done()
	if speechCtx.Err() != nil {
		t.Fatalf("speech context unexpectedly canceled: %v", speechCtx.Err())
	}
}

func TestSpeechSynthesisContextHasBoundedFiveMinuteTurnDeadline(t *testing.T) {
	ctx, done := SpeechSynthesisContext(context.Background())
	defer done()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("SpeechSynthesisContext has no turn deadline")
	}
	remaining := time.Until(deadline)
	if remaining > 5*time.Minute || remaining < 4*time.Minute {
		t.Fatalf("SpeechSynthesisContext remaining deadline=%v, want at most five minutes", remaining)
	}
}

func TestSpeechTurnPlaybackProgressRequiresReadyAndMonotonicPlayback(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	ctx := WithSpeechTurnCancellation(context.Background(), latch)
	progress := speech.PlaybackProgressFromContext(ctx)
	if progress == nil {
		t.Fatal("SpeechTurnCancellation did not attach domain PlaybackProgress")
	}
	if err := progress.MarkStarted(0); err == nil {
		t.Fatal("started was accepted before ready")
	}
	if err := progress.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	if err := progress.MarkStarted(0); err != nil {
		t.Fatal(err)
	}
	if err := progress.MarkReady(1); err != nil {
		t.Fatalf("one-ahead utterance did not become ready while prior playback was active: %v", err)
	}
	if err := progress.MarkStarted(1); err == nil {
		t.Fatal("next utterance started before prior playback ended")
	}
	if err := progress.MarkEnded(0); err != nil {
		t.Fatal(err)
	}
	if err := progress.MarkStarted(1); err != nil {
		t.Fatal(err)
	}
	if err := progress.MarkEnded(1); err != nil {
		t.Fatal(err)
	}
	if err := progress.MarkStarted(1); err == nil {
		t.Fatal("stale started phase regressed an ended utterance")
	}
}

func TestSpeechTurnPlaybackProgressBoundsIndexesAtSix(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	progress := speech.PlaybackProgressFromContext(WithSpeechTurnCancellation(context.Background(), latch))
	for index := 0; index < speech.MaxUtterances; index++ {
		if err := progress.MarkReady(index); err != nil {
			t.Fatalf("MarkReady(%d): %v", index, err)
		}
		if err := progress.MarkStarted(index); err != nil {
			t.Fatalf("MarkStarted(%d): %v", index, err)
		}
		if err := progress.MarkEnded(index); err != nil {
			t.Fatalf("MarkEnded(%d): %v", index, err)
		}
	}
	if err := progress.MarkReady(speech.MaxUtterances); err == nil {
		t.Fatalf("MarkReady(%d) exceeded six-utterance limit", speech.MaxUtterances)
	}
	if err := progress.MarkStarted(-1); err == nil {
		t.Fatal("negative utterance index was accepted")
	}
}

func TestSpeechTurnCancellationCapturesCreationAndPlaybackPhaseTimes(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	createdAt := latch.TurnCreatedAt()
	if createdAt.IsZero() {
		t.Fatal("TurnCreatedAt() returned zero time")
	}
	if _, ok := latch.PlaybackStartedAt(0); ok {
		t.Fatal("PlaybackStartedAt(0) was available before playback started")
	}
	if _, ok := latch.PlaybackEndedAt(0); ok {
		t.Fatal("PlaybackEndedAt(0) was available before playback ended")
	}
	if err := latch.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	startedAfter := time.Now()
	if err := latch.MarkStarted(0); err != nil {
		t.Fatal(err)
	}
	startedAt, ok := latch.PlaybackStartedAt(0)
	if !ok || startedAt.Before(startedAfter) {
		t.Fatalf("PlaybackStartedAt(0) = %v, available=%t, want timestamp at/after %v", startedAt, ok, startedAfter)
	}
	time.Sleep(time.Millisecond)
	endedAfter := time.Now()
	if err := latch.MarkEnded(0); err != nil {
		t.Fatal(err)
	}
	endedAt, ok := latch.PlaybackEndedAt(0)
	if !ok || endedAt.Before(endedAfter) || !endedAt.After(startedAt) {
		t.Fatalf("PlaybackEndedAt(0) = %v, available=%t, want timestamp after playback start and at/after %v", endedAt, ok, endedAfter)
	}
}

func TestSpeechTurnPlaybackProgressIsReadyOnlyAfterPublishedUtterance(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	if latch.IsReady(-1) || latch.IsReady(0) || latch.IsReady(speech.MaxUtterances) {
		t.Fatal("invalid or unready utterance reported ready")
	}
	if err := latch.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	if !latch.IsReady(0) || latch.IsReady(1) {
		t.Fatalf("readiness after first publish: index 0=%t index 1=%t", latch.IsReady(0), latch.IsReady(1))
	}
}

func TestSpeechTurnCancellationPlaybackOutputDefaultsViewerAndFixesAtReady(t *testing.T) {
	type playbackOutputOwner interface {
		SetPlaybackOutput(string) error
		IsViewerPlayback() bool
	}

	latch := NewSpeechTurnCancellation()
	owner, ok := any(latch).(playbackOutputOwner)
	if !ok {
		t.Fatal("SpeechTurnCancellation does not expose playback output selection")
	}
	if !owner.IsViewerPlayback() {
		t.Fatal("unset playback output must default to Viewer")
	}
	if err := owner.SetPlaybackOutput("local"); err != nil {
		t.Fatal(err)
	}
	if owner.IsViewerPlayback() {
		t.Fatal("local playback output reported Viewer ownership")
	}
	if err := owner.SetPlaybackOutput("local"); err != nil {
		t.Fatalf("setting the same output twice should be idempotent: %v", err)
	}
	if err := owner.SetPlaybackOutput("unknown"); err == nil {
		t.Fatal("unknown playback output was accepted")
	}
	if err := latch.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetPlaybackOutput("viewer"); err == nil {
		t.Fatal("playback output switched after the first ready utterance")
	}
}

func TestSpeechTurnPlaybackProgressErrorReleasesWaitersAndRejectsFutureReady(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	progress := speech.PlaybackProgressFromContext(WithSpeechTurnCancellation(context.Background(), latch))
	if err := progress.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- progress.WaitStarted(context.Background(), 0) }()
	wantErr := errors.New("viewer playback failed")
	if err := progress.MarkError(0, wantErr); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waitDone:
		if !errors.Is(err, wantErr) {
			t.Fatalf("WaitStarted error=%v, want playback failure %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("playback failure did not release WaitStarted")
	}
	if err := progress.MarkReady(1); !errors.Is(err, wantErr) {
		t.Fatalf("MarkReady after playback error=%v, want %v", err, wantErr)
	}
}

func TestSpeechTurnPlaybackProgressRecordsInternalSynthesisFailureBeforeReady(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	progress := speech.PlaybackProgressFromContext(WithSpeechTurnCancellation(context.Background(), latch))
	wantErr := errors.New("gateway synthesis failed")
	if err := progress.MarkError(0, wantErr); err != nil {
		t.Fatalf("internal MarkError before ready: %v", err)
	}
	if latch.IsReady(0) {
		t.Fatal("failed synthesis was reported ready to the Viewer")
	}
	if got := progress.WaitStarted(context.Background(), 0); !errors.Is(got, wantErr) {
		t.Fatalf("WaitStarted error=%v, want original synthesis error %v", got, wantErr)
	}
}

func TestSpeechTurnPlaybackCancellationCancelsSynthesisChild(t *testing.T) {
	latch := NewSpeechTurnCancellation()
	turnCtx := WithSpeechTurnCancellation(context.Background(), latch)
	speechCtx, done := SpeechSynthesisContext(turnCtx)
	defer done()
	progress := speech.PlaybackProgressFromContext(turnCtx)
	if err := progress.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	if err := progress.MarkCancelled(0); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(speechCtx.Err(), context.Canceled) {
		t.Fatalf("speech child context error=%v, want context.Canceled", speechCtx.Err())
	}
	if turnCtx.Err() != nil {
		t.Fatalf("playback cancellation canceled ProcessMessage context: %v", turnCtx.Err())
	}
}
