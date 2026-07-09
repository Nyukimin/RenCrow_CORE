package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
)

type sttTimingTrace struct {
	sessionID          string
	mode               string
	startedAt          time.Time
	firstAudioAt       time.Time
	firstVoiceAt       time.Time
	firstProvisionalAt time.Time
	lastVoiceAt        time.Time
	finalAt            time.Time
}

type sttTimingSnapshot struct {
	SessionID          string
	Mode               string
	FirstAudioMS       string
	FirstVoiceMS       string
	FirstProvisionalMS string
	SilenceToFinalMS   string
	TotalMS            string
}

func newSTTTimingTrace(mode string) *sttTimingTrace {
	now := time.Now()
	return &sttTimingTrace{
		sessionID: sttinfra.NextEventID(now),
		mode:      strings.TrimSpace(mode),
		startedAt: now,
	}
}

func sttTimingTraceEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("STT_TIMING_TRACE"))
	return raw != "" && raw != "0" && !strings.EqualFold(raw, "false")
}

func (t *sttTimingTrace) markAudio(at time.Time) {
	if t.firstAudioAt.IsZero() {
		t.firstAudioAt = at
	}
}

func (t *sttTimingTrace) markVoice(at time.Time) {
	if t.firstVoiceAt.IsZero() {
		t.firstVoiceAt = at
	}
	t.lastVoiceAt = at
}

func (t *sttTimingTrace) markProvisional(at time.Time) {
	if t.firstProvisionalAt.IsZero() {
		t.firstProvisionalAt = at
	}
}

func (t *sttTimingTrace) snapshot(finalAt time.Time) sttTimingSnapshot {
	return sttTimingSnapshot{
		SessionID:          t.sessionID,
		Mode:               t.mode,
		FirstAudioMS:       sttTimingMillis(t.startedAt, t.firstAudioAt),
		FirstVoiceMS:       sttTimingMillis(t.startedAt, t.firstVoiceAt),
		FirstProvisionalMS: sttTimingMillis(t.startedAt, t.firstProvisionalAt),
		SilenceToFinalMS:   sttTimingMillis(t.lastVoiceAt, finalAt),
		TotalMS:            sttTimingMillis(t.startedAt, finalAt),
	}
}

func (t *sttTimingTrace) logFinal(source, reason, text string) {
	if !sttTimingTraceEnabled() {
		return
	}
	finalAt := time.Now()
	t.finalAt = finalAt
	snap := t.snapshot(finalAt)
	log.Printf(
		"[STT][timing] session=%s mode=%s source=%s reason=%s first_audio_ms=%s first_voice_ms=%s first_provisional_ms=%s silence_to_final_ms=%s total_ms=%s text_len=%d",
		snap.SessionID,
		snap.Mode,
		strings.TrimSpace(source),
		strings.TrimSpace(reason),
		snap.FirstAudioMS,
		snap.FirstVoiceMS,
		snap.FirstProvisionalMS,
		snap.SilenceToFinalMS,
		snap.TotalMS,
		len(strings.TrimSpace(text)),
	)
}

func sttTimingMillis(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", end.Sub(start).Seconds()*1000)
}
