// Package tts defines text-to-speech module contracts.
package tts

import (
	"context"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type SynthesisRequest struct {
	SessionID   core.SessionID   `json:"session_id,omitempty"`
	ResponseID  core.ResponseID  `json:"response_id,omitempty"`
	UtteranceID core.UtteranceID `json:"utterance_id,omitempty"`
	CharacterID string           `json:"character_id,omitempty"`
	VoiceID     string           `json:"voice_id,omitempty"`
	SpeechText  string           `json:"speech_text"`
	DisplayText string           `json:"display_text,omitempty"`
	Emotion     *EmotionState    `json:"emotion,omitempty"`
}

type AudioChunk struct {
	Ref         core.ChunkRef `json:"ref"`
	CharacterID string        `json:"character_id,omitempty"`
	SpeechText  string        `json:"speech_text,omitempty"`
	DisplayText string        `json:"display_text,omitempty"`
	AudioPath   string        `json:"audio_path,omitempty"`
	AudioURL    string        `json:"audio_url,omitempty"`
	Duration    time.Duration `json:"duration,omitempty"`
}

type SynthesisResult struct {
	Chunks []AudioChunk `json:"chunks"`
}

type SynthesisOutput struct {
	AudioPath  string
	AudioURL   string
	DurationMS int64
}

type PlaybackStateSnapshot struct {
	PendingSessionCount      int      `json:"pending_session_count"`
	PendingResponseCount     int      `json:"pending_response_count"`
	PendingSessionIDs        []string `json:"pending_session_ids,omitempty"`
	PendingResponseIDs       []string `json:"pending_response_ids,omitempty"`
	TopicGateCount           int      `json:"topic_gate_count"`
	TopicRouteCount          int      `json:"topic_route_count"`
	PublicRouteCount         int      `json:"public_route_count"`
	PublicStaleRouteCount    int      `json:"public_stale_route_count"`
	NextChunkSessionCount    int      `json:"next_chunk_session_count"`
	NextResponseSessionCount int      `json:"next_response_session_count"`
}

type PendingPlaybackSnapshot struct {
	PendingSessionCount  int      `json:"pending_session_count"`
	PendingResponseCount int      `json:"pending_response_count"`
	PendingSessionIDs    []string `json:"pending_session_ids,omitempty"`
	PendingResponseIDs   []string `json:"pending_response_ids,omitempty"`
	TopicGateCount       int      `json:"topic_gate_count"`
	TopicRouteCount      int      `json:"topic_route_count"`
}

type PublicPlaybackSnapshot struct {
	RouteCount               int `json:"route_count"`
	StaleRouteCount          int `json:"stale_route_count"`
	NextChunkSessionCount    int `json:"next_chunk_session_count"`
	NextResponseSessionCount int `json:"next_response_session_count"`
}

type PlaybackStateObserver interface {
	Health(ctx context.Context) core.HealthReport
	Snapshot(ctx context.Context) (PlaybackStateSnapshot, error)
}

type Provider interface {
	Name() string
	Health(ctx context.Context) core.HealthReport
	Synthesize(ctx context.Context, req SynthesisRequest) (SynthesisResult, error)
}
