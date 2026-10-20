package tts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

func TestAsyncAudioSinkPreservesTypedSpeechPlanContextAndMetadata(t *testing.T) {
	inner := &typedSpeechPlanSinkRecorder{items: make(chan typedSpeechPlanSinkItem, 1)}
	sink := NewAsyncAudioSink(inner)
	typed, ok := sink.(SpeechPlanAudioSink)
	if !ok {
		t.Fatal("async audio sink does not expose typed SpeechPlan enqueue")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := &audioSinkProgressStub{}
	chunk := SpeechPlanAudioChunk{TurnID: "turn_local", UtteranceIndex: 0, AudioPath: "utterance.wav", PauseAfterMS: 0}
	if err := typed.SubmitSpeechPlanChunk(ctx, "tts-local", chunk, progress); err != nil {
		t.Fatalf("SubmitSpeechPlanChunk() error = %v", err)
	}
	select {
	case got := <-inner.items:
		if got.ctx != ctx || got.progress != progress || got.sessionID != "tts-local" || got.chunk != chunk {
			t.Fatalf("typed SpeechPlan enqueue lost context/metadata: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("typed SpeechPlan enqueue did not reach the local sink")
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if inner.legacySubmissions != 0 {
		t.Fatalf("typed SpeechPlan enqueue used legacy SubmitChunk %d times", inner.legacySubmissions)
	}
}

type typedSpeechPlanSinkItem struct {
	ctx       context.Context
	sessionID string
	chunk     SpeechPlanAudioChunk
	progress  speech.PlaybackProgress
}

type typedSpeechPlanSinkRecorder struct {
	mu                sync.Mutex
	items             chan typedSpeechPlanSinkItem
	legacySubmissions int
}

func (s *typedSpeechPlanSinkRecorder) SubmitChunk(context.Context, string, audioChunk) error {
	s.mu.Lock()
	s.legacySubmissions++
	s.mu.Unlock()
	return nil
}

func (*typedSpeechPlanSinkRecorder) CompleteSession(context.Context, string) error { return nil }

func (s *typedSpeechPlanSinkRecorder) SubmitSpeechPlanChunk(ctx context.Context, sessionID string, chunk SpeechPlanAudioChunk, progress speech.PlaybackProgress) error {
	s.items <- typedSpeechPlanSinkItem{ctx: ctx, sessionID: sessionID, chunk: chunk, progress: progress}
	return nil
}

type audioSinkProgressStub struct{}

func (audioSinkProgressStub) MarkReady(int) error                    { return nil }
func (audioSinkProgressStub) WaitStarted(context.Context, int) error { return nil }
func (audioSinkProgressStub) WaitEnded(context.Context, int) error   { return nil }
func (audioSinkProgressStub) MarkStarted(int) error                  { return nil }
func (audioSinkProgressStub) MarkEnded(int) error                    { return nil }
func (audioSinkProgressStub) MarkError(int, error) error             { return nil }
func (audioSinkProgressStub) MarkCancelled(int) error                { return nil }

func TestPlaybackAudioSinkSpeechPlanUsesActualStartEndAndExactZeroPause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := &recordingSpeechProgress{cancelOnEnd: cancel}
	player := &startNotifyingPlayer{}
	sink := NewPlaybackAudioSink(player, "")
	if err := sink.SubmitSpeechPlanChunk(ctx, "tts", SpeechPlanAudioChunk{TurnID: "turn_1", UtteranceIndex: 0, AudioPath: "audio.wav", PauseAfterMS: 0}, progress); err != nil {
		t.Fatalf("SubmitSpeechPlanChunk() error = %v", err)
	}
	if got := strings.Join(progress.events, ","); got != "started,ended" {
		t.Fatalf("playback progress = %q, want started,ended", got)
	}
	if player.started != 1 || player.playCalls != 0 {
		t.Fatalf("player starts=%d legacy Play calls=%d, want 1/0", player.started, player.playCalls)
	}

	finalCtx, cancelFinal := context.WithCancel(context.Background())
	finalProgress := &recordingSpeechProgress{cancelOnEnd: cancelFinal}
	if err := NewPlaybackAudioSink(&startNotifyingPlayer{}, "").SubmitSpeechPlanChunk(finalCtx, "tts", SpeechPlanAudioChunk{
		TurnID: "turn_1", UtteranceIndex: 0, AudioPath: "audio.wav", PauseAfterMS: 200, FinalUtterance: true,
	}, finalProgress); err != nil {
		t.Fatalf("final utterance should not wait on an unused inter-utterance pause: %v", err)
	}
}

func TestPlaybackAudioSinkSpeechPlanCancellationAndFailureAreReported(t *testing.T) {
	t.Run("cancel command context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		progress := &recordingSpeechProgress{}
		player := &startNotifyingPlayer{blockUntilCancel: true, startedCh: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			done <- NewPlaybackAudioSink(player, "").SubmitSpeechPlanChunk(ctx, "tts", SpeechPlanAudioChunk{TurnID: "turn_1", UtteranceIndex: 0, AudioPath: "audio.wav"}, progress)
		}()
		select {
		case <-player.startedCh:
		case <-time.After(time.Second):
			t.Fatal("local playback did not report its actual start")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("SubmitSpeechPlanChunk() error = %v, want context.Canceled", err)
		}
		if got := strings.Join(progress.events, ","); got != "started,cancelled" {
			t.Fatalf("playback progress = %q, want started,cancelled", got)
		}
	})

	t.Run("player failure", func(t *testing.T) {
		progress := &recordingSpeechProgress{}
		playerErr := errors.New("player failed")
		player := &startNotifyingPlayer{playErr: playerErr}
		err := NewPlaybackAudioSink(player, "").SubmitSpeechPlanChunk(context.Background(), "tts", SpeechPlanAudioChunk{TurnID: "turn_1", UtteranceIndex: 0, AudioPath: "audio.wav"}, progress)
		if !errors.Is(err, playerErr) {
			t.Fatalf("SubmitSpeechPlanChunk() error = %v, want %v", err, playerErr)
		}
		if got := strings.Join(progress.events, ","); got != "started,error" {
			t.Fatalf("playback progress = %q, want started,error", got)
		}
	})
}

type recordingSpeechProgress struct {
	events      []string
	cause       error
	cancelOnEnd context.CancelFunc
}

func (*recordingSpeechProgress) MarkReady(int) error                    { return nil }
func (*recordingSpeechProgress) WaitStarted(context.Context, int) error { return nil }
func (*recordingSpeechProgress) WaitEnded(context.Context, int) error   { return nil }
func (p *recordingSpeechProgress) MarkStarted(int) error {
	p.events = append(p.events, "started")
	return nil
}
func (p *recordingSpeechProgress) MarkEnded(int) error {
	p.events = append(p.events, "ended")
	if p.cancelOnEnd != nil {
		p.cancelOnEnd()
	}
	return nil
}
func (p *recordingSpeechProgress) MarkError(_ int, cause error) error {
	p.events = append(p.events, "error")
	p.cause = cause
	return nil
}
func (p *recordingSpeechProgress) MarkCancelled(int) error {
	p.events = append(p.events, "cancelled")
	return nil
}

type startNotifyingPlayer struct {
	playCalls        int
	started          int
	startedCh        chan struct{}
	blockUntilCancel bool
	playErr          error
}

func (p *startNotifyingPlayer) Play(context.Context, string) (PlaybackResult, error) {
	p.playCalls++
	return PlaybackResult{}, nil
}

func (p *startNotifyingPlayer) PlayWithStart(ctx context.Context, _ string, onStarted func()) (PlaybackResult, error) {
	p.started++
	onStarted()
	if p.startedCh != nil {
		close(p.startedCh)
	}
	if p.blockUntilCancel {
		<-ctx.Done()
		return PlaybackResult{}, ctx.Err()
	}
	if p.playErr != nil {
		return PlaybackResult{}, p.playErr
	}
	return PlaybackResult{}, nil
}

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
