package tts

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

// AudioSink consumes ordered audio chunks.
type AudioSink interface {
	SubmitChunk(ctx context.Context, sessionID string, ch audioChunk) error
	CompleteSession(ctx context.Context, sessionID string) error
}

// SpeechPlanAudioChunk keeps the selected utterance metadata needed by local
// playback without changing the legacy AudioSink chunk contract.
type SpeechPlanAudioChunk struct {
	TurnID         string
	UtteranceIndex int
	AudioPath      string
	PauseAfterMS   int
	FinalUtterance bool
}

// SpeechPlanAudioSink is an optional typed enqueue seam for turn-paced speech.
type SpeechPlanAudioSink interface {
	SubmitSpeechPlanChunk(ctx context.Context, sessionID string, chunk SpeechPlanAudioChunk, progress speech.PlaybackProgress) error
}

// NoopAudioSink keeps browser-only playback paths working without local audio output.
type NoopAudioSink struct{}

func NewNoopAudioSink() *NoopAudioSink {
	return &NoopAudioSink{}
}

func (s *NoopAudioSink) SubmitChunk(_ context.Context, _ string, _ audioChunk) error {
	return nil
}

func (s *NoopAudioSink) CompleteSession(_ context.Context, _ string) error {
	return nil
}

type asyncAudioSink struct {
	sink     AudioSink
	mu       sync.Mutex
	sessions map[string]*asyncAudioSinkSession
}

type asyncAudioSinkSession struct {
	id    string
	queue chan asyncAudioSinkItem
	once  sync.Once
}

type asyncAudioSinkItem struct {
	ctx      context.Context
	chunk    audioChunk
	speech   *SpeechPlanAudioChunk
	progress speech.PlaybackProgress
}

// NewAsyncAudioSink preserves ordered local playback without making TTS synthesis
// wait for each chunk to finish playing.
func NewAsyncAudioSink(sink AudioSink) AudioSink {
	if sink == nil {
		return NewNoopAudioSink()
	}
	return &asyncAudioSink{
		sink:     sink,
		sessions: make(map[string]*asyncAudioSinkSession),
	}
}

func (s *asyncAudioSink) SubmitChunk(ctx context.Context, sessionID string, ch audioChunk) error {
	if s == nil || s.sink == nil {
		return fmt.Errorf("audio sink is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session := s.getSession(sessionID)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case session.queue <- asyncAudioSinkItem{chunk: ch}:
		return nil
	}
}

func (s *asyncAudioSink) SubmitSpeechPlanChunk(ctx context.Context, sessionID string, chunk SpeechPlanAudioChunk, progress speech.PlaybackProgress) error {
	if s == nil || s.sink == nil {
		return fmt.Errorf("audio sink is not configured")
	}
	if _, ok := s.sink.(SpeechPlanAudioSink); !ok {
		return fmt.Errorf("audio sink does not support typed SpeechPlan playback")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if progress == nil {
		return fmt.Errorf("SpeechPlan playback progress is unavailable")
	}
	session := s.getSession(sessionID)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case session.queue <- asyncAudioSinkItem{ctx: ctx, speech: &chunk, progress: progress}:
		return nil
	}
}

func (s *asyncAudioSink) CompleteSession(_ context.Context, sessionID string) error {
	if s == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	session := s.sessions[sessionID]
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	if session != nil {
		session.close()
	}
	return nil
}

func (s *asyncAudioSink) getSession(sessionID string) *asyncAudioSinkSession {
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[sessionID]; session != nil {
		return session
	}
	session := &asyncAudioSinkSession{
		id:    sessionID,
		queue: make(chan asyncAudioSinkItem, 16),
	}
	s.sessions[sessionID] = session
	go s.runSession(session)
	return session
}

func (s *asyncAudioSink) runSession(session *asyncAudioSinkSession) {
	for item := range session.queue {
		if item.speech == nil {
			if err := s.sink.SubmitChunk(context.Background(), session.id, item.chunk); err != nil {
				log.Printf("WARN: async audio sink submit failed session=%s chunk=%d err=%v", session.id, item.chunk.ChunkIndex, err)
			}
			continue
		}
		if err := item.ctx.Err(); err != nil {
			_ = item.progress.MarkCancelled(item.speech.UtteranceIndex)
			continue
		}
		speechSink, ok := s.sink.(SpeechPlanAudioSink)
		if !ok {
			err := fmt.Errorf("audio sink does not support typed SpeechPlan playback")
			_ = item.progress.MarkError(item.speech.UtteranceIndex, err)
			continue
		}
		if err := speechSink.SubmitSpeechPlanChunk(item.ctx, session.id, *item.speech, item.progress); err != nil {
			if item.ctx.Err() != nil {
				_ = item.progress.MarkCancelled(item.speech.UtteranceIndex)
			} else {
				_ = item.progress.MarkError(item.speech.UtteranceIndex, err)
			}
			log.Printf("WARN: async SpeechPlan audio sink submit failed session=%s turn=%s utterance=%d err=%v", session.id, item.speech.TurnID, item.speech.UtteranceIndex, err)
		}
	}
	if err := s.sink.CompleteSession(context.Background(), session.id); err != nil {
		log.Printf("WARN: async audio sink complete failed session=%s err=%v", session.id, err)
	}
}

func (s *asyncAudioSinkSession) close() {
	s.once.Do(func() {
		close(s.queue)
	})
}

const defaultChunkPause = 200 * time.Millisecond // 同一話者内の句間ブレイク

// PlaybackAudioSink reuses CommandPlayer to play generated audio paths.
type PlaybackAudioSink struct {
	player        Player
	audioPathRoot string
}

func NewPlaybackAudioSink(player Player, audioPathRoot string) *PlaybackAudioSink {
	return &PlaybackAudioSink{player: player, audioPathRoot: audioPathRoot}
}

func (s *PlaybackAudioSink) SubmitChunk(ctx context.Context, sessionID string, ch audioChunk) error {
	if s == nil || s.player == nil {
		return fmt.Errorf("audio sink is not configured")
	}
	if strings.TrimSpace(ch.AudioPath) == "" {
		return fmt.Errorf("audio_path is empty")
	}
	resolvedPath := resolveAudioPath(ch.AudioPath, s.audioPathRoot)
	r, err := s.player.Play(ctx, resolvedPath)
	if err != nil {
		return err
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("audio playback exit code=%d", r.ExitCode)
	}
	pause := parsePauseAfter(ch.PauseAfter)
	if pause <= 0 {
		pause = defaultChunkPause
	}
	select {
	case <-ctx.Done():
	case <-time.After(pause):
	}
	return nil
}

func (s *PlaybackAudioSink) SubmitSpeechPlanChunk(ctx context.Context, _ string, chunk SpeechPlanAudioChunk, progress speech.PlaybackProgress) error {
	if s == nil || s.player == nil {
		return fmt.Errorf("audio sink is not configured")
	}
	if progress == nil {
		return fmt.Errorf("SpeechPlan playback progress is unavailable")
	}
	if strings.TrimSpace(chunk.AudioPath) == "" {
		return fmt.Errorf("audio_path is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	playCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	started := false
	var startErr error
	markStarted := func() {
		if started {
			return
		}
		started = true
		startErr = progress.MarkStarted(chunk.UtteranceIndex)
		if startErr != nil {
			cancel()
		}
	}
	resolvedPath := resolveAudioPath(chunk.AudioPath, s.audioPathRoot)
	var result PlaybackResult
	var err error
	if player, ok := s.player.(PlaybackStartNotifier); ok {
		result, err = player.PlayWithStart(playCtx, resolvedPath, markStarted)
	} else {
		markStarted()
		if startErr != nil {
			return startErr
		}
		result, err = s.player.Play(playCtx, resolvedPath)
	}
	if startErr != nil {
		return startErr
	}
	if err != nil {
		return markSpeechPlanPlaybackFailure(ctx, progress, chunk.UtteranceIndex, err)
	}
	if !started {
		return markSpeechPlanPlaybackFailure(ctx, progress, chunk.UtteranceIndex, fmt.Errorf("audio player completed without a start notification"))
	}
	if result.ExitCode != 0 {
		return markSpeechPlanPlaybackFailure(ctx, progress, chunk.UtteranceIndex, fmt.Errorf("audio playback exit code=%d", result.ExitCode))
	}
	if err := ctx.Err(); err != nil {
		_ = progress.MarkCancelled(chunk.UtteranceIndex)
		return err
	}
	if err := progress.MarkEnded(chunk.UtteranceIndex); err != nil {
		return err
	}
	if pause := time.Duration(chunk.PauseAfterMS) * time.Millisecond; pause > 0 && !chunk.FinalUtterance {
		timer := time.NewTimer(pause)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			_ = progress.MarkCancelled(chunk.UtteranceIndex)
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func markSpeechPlanPlaybackFailure(ctx context.Context, progress speech.PlaybackProgress, index int, cause error) error {
	if err := ctx.Err(); err != nil {
		_ = progress.MarkCancelled(index)
		return err
	}
	_ = progress.MarkError(index, cause)
	return cause
}

func (s *PlaybackAudioSink) CompleteSession(_ context.Context, sessionID string) error {
	_ = sessionID
	return nil
}

// parsePauseAfter はTTS Serverから返される pause_after 値をパースする。
// "0.5s", "500ms", "1.2" (秒として解釈) などを受け付ける。
func parsePauseAfter(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
		return time.Duration(f * float64(time.Second))
	}
	return 0
}
