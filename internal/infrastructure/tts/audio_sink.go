package tts

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AudioSink consumes ordered audio chunks.
type AudioSink interface {
	SubmitChunk(ctx context.Context, sessionID string, ch audioChunk) error
	CompleteSession(ctx context.Context, sessionID string) error
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
	queue chan audioChunk
	once  sync.Once
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
	session := s.getSession(sessionID)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case session.queue <- ch:
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
		queue: make(chan audioChunk, 16),
	}
	s.sessions[sessionID] = session
	go s.runSession(session)
	return session
}

func (s *asyncAudioSink) runSession(session *asyncAudioSinkSession) {
	for ch := range session.queue {
		if err := s.sink.SubmitChunk(context.Background(), session.id, ch); err != nil {
			log.Printf("WARN: async audio sink submit failed session=%s chunk=%d err=%v", session.id, ch.ChunkIndex, err)
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
