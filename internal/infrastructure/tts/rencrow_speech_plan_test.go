package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type speechPlanPushBridge interface {
	PushSpeechPlan(ctx context.Context, ttsSessionID, conversationSessionID, turnID, speakerID string, plan speech.SpeechPlan) error
}

type immediatePlaybackProgress struct{}

func (immediatePlaybackProgress) SetPlaybackOutput(string) error         { return nil }
func (immediatePlaybackProgress) MarkReady(int) error                    { return nil }
func (immediatePlaybackProgress) WaitStarted(context.Context, int) error { return nil }
func (immediatePlaybackProgress) WaitEnded(context.Context, int) error   { return nil }
func (immediatePlaybackProgress) MarkStarted(int) error                  { return nil }
func (immediatePlaybackProgress) MarkEnded(int) error                    { return nil }
func (immediatePlaybackProgress) MarkError(int, error) error             { return nil }
func (immediatePlaybackProgress) MarkCancelled(int) error                { return nil }

type controlledPlaybackProgress struct {
	ready       chan int
	mu          sync.Mutex
	started     map[int]bool
	ended       map[int]bool
	terminalErr error
	changed     chan struct{}
}

func newControlledPlaybackProgress() *controlledPlaybackProgress {
	return &controlledPlaybackProgress{ready: make(chan int, 8), started: make(map[int]bool), ended: make(map[int]bool), changed: make(chan struct{})}
}

func (*controlledPlaybackProgress) SetPlaybackOutput(string) error { return nil }

func (p *controlledPlaybackProgress) MarkReady(index int) error {
	p.ready <- index
	return nil
}

func (p *controlledPlaybackProgress) WaitStarted(ctx context.Context, index int) error {
	for {
		p.mu.Lock()
		if p.terminalErr != nil {
			err := p.terminalErr
			p.mu.Unlock()
			return err
		}
		if p.started[index] || p.ended[index] {
			p.mu.Unlock()
			return nil
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *controlledPlaybackProgress) WaitEnded(ctx context.Context, index int) error {
	for {
		p.mu.Lock()
		if p.terminalErr != nil {
			err := p.terminalErr
			p.mu.Unlock()
			return err
		}
		if p.ended[index] {
			p.mu.Unlock()
			return nil
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *controlledPlaybackProgress) MarkStarted(index int) error {
	p.mu.Lock()
	p.started[index] = true
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
	return nil
}
func (p *controlledPlaybackProgress) MarkEnded(index int) error {
	p.mu.Lock()
	p.ended[index] = true
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
	return nil
}
func (p *controlledPlaybackProgress) MarkError(_ int, cause error) error {
	p.mu.Lock()
	p.terminalErr = cause
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
	return nil
}
func (p *controlledPlaybackProgress) MarkCancelled(int) error {
	return p.MarkError(0, context.Canceled)
}

type speechPlanSinkRecorder struct {
	mu          sync.Mutex
	submissions int
}

func (s *speechPlanSinkRecorder) SubmitChunk(context.Context, string, audioChunk) error {
	s.mu.Lock()
	s.submissions++
	s.mu.Unlock()
	return nil
}

func (*speechPlanSinkRecorder) CompleteSession(context.Context, string) error { return nil }

func TestRenCrowTTSBridgePushSpeechPlanPostsAndPublishesUtterancesInOrder(t *testing.T) {
	type requestEnvelope struct {
		SpeakerID      string            `json:"speaker_id"`
		SessionID      string            `json:"session_id"`
		TurnID         string            `json:"turn_id"`
		UtteranceIndex int               `json:"utterance_index"`
		SpeechPlan     speech.SpeechPlan `json:"speech_plan"`
		LegacyText     string            `json:"text"`
		LegacyVoiceID  string            `json:"voice_id"`
	}
	var mu sync.Mutex
	var requests []requestEnvelope
	var requestIDs []string
	var active, maxActive int
	var speechReady []SpeechPlanChunkReady
	legacyCallbackCount := 0
	latch := orchestrator.NewSpeechTurnCancellation()
	wav := speechPlanTestWAV()
	conversationSessionID := string(modulecore.NewSessionID())
	turnID := string(modulecore.NewTurnID())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tts" {
			t.Errorf("request = %s %s, want POST /api/tts", r.Method, r.URL.Path)
		}
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()

		var envelope requestEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode envelope: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if envelope.SessionID != conversationSessionID || envelope.TurnID != turnID {
			t.Errorf("SpeechPlan identity = session=%q turn=%q", envelope.SessionID, envelope.TurnID)
		}
		requestID := r.Header.Get("X-RenCrow-TTS-Request-Id")
		if requestID == "" {
			t.Error("missing canonical request ID header")
		}
		mu.Lock()
		requests = append(requests, envelope)
		requestIDs = append(requestIDs, requestID)
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", requestID)
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "7")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", strconv.Itoa(envelope.UtteranceIndex))
		_, _ = w.Write(wav)
	}))
	defer server.Close()

	outputDir := t.TempDir()
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: server.URL,
		OutputDir:   outputDir,
		VoiceID:     "mio",
		OnChunkReady: func(string, string, string, int, string, string, string, string, string) {
			legacyCallbackCount++
		},
		OnSpeechChunkReady: func(chunk SpeechPlanChunkReady) {
			if err := latch.MarkStarted(chunk.UtteranceIndex); err != nil {
				t.Errorf("MarkStarted(%d): %v", chunk.UtteranceIndex, err)
			}
			if err := latch.MarkEnded(chunk.UtteranceIndex); err != nil {
				t.Errorf("MarkEnded(%d): %v", chunk.UtteranceIndex, err)
			}
			mu.Lock()
			defer mu.Unlock()
			speechReady = append(speechReady, chunk)
		},
	})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID:   "tts-speech-plan",
		CharacterID: "mio",
		VoiceID:     "mio",
	}); err != nil {
		t.Fatal(err)
	}
	pusher, ok := any(bridge).(speechPlanPushBridge)
	if !ok {
		t.Fatal("RenCrowTTSBridge does not implement optional PushSpeechPlan")
	}
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{
		{Segments: []speech.Segment{{Text: "え、"}, {Text: "本当ですか？"}}, Delivery: speech.Delivery{Tone: "surprised", Attitude: "questioning", Energy: "medium", Pace: "normal", Mode: "dialogue"}, PauseAfterMS: 150},
		{Segments: []speech.Segment{{Text: "確認します。"}}, Delivery: speech.Delivery{Tone: "calm", Attitude: "gentle", Energy: "low", Pace: "slow", Mode: "dialogue"}, PauseAfterMS: 300},
	}}
	ctx := orchestrator.WithSpeechTurnCancellation(context.Background(), latch)
	if err := pusher.PushSpeechPlan(ctx, "tts-speech-plan", conversationSessionID, turnID, "mio", plan); err != nil {
		t.Fatalf("PushSpeechPlan() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("Gateway request count = %d, want 2", len(requests))
	}
	if maxActive != 1 {
		t.Fatalf("maximum in-flight synthesis = %d, want 1", maxActive)
	}
	if len(requestIDs) != 2 || requestIDs[0] == requestIDs[1] {
		t.Fatalf("request IDs = %#v, want one unique ID per utterance", requestIDs)
	}
	for i, request := range requests {
		if request.SpeakerID != "mio" || request.SessionID != conversationSessionID || request.TurnID != turnID || request.UtteranceIndex != i {
			t.Errorf("envelope[%d] identity = %#v", i, request)
		}
		if request.SpeechPlan.SchemaVersion != speech.SchemaVersion || len(request.SpeechPlan.Utterances) != 2 {
			t.Errorf("envelope[%d] did not carry normalized source SpeechPlan: %#v", i, request.SpeechPlan)
		}
		if request.LegacyText != "" || request.LegacyVoiceID != "" {
			t.Errorf("envelope[%d] contains legacy text/voice fields: %#v", i, request)
		}
	}
	if legacyCallbackCount != 0 {
		t.Errorf("SpeechPlan also invoked legacy event callback %d times", legacyCallbackCount)
	}
	if latch.IsViewerPlayback() != true {
		t.Errorf("default SpeechPlan playback output is local, want Viewer")
	}
	if len(speechReady) != 2 || speechReady[0].TurnID != turnID || speechReady[0].PlaybackOutput != SpeechPlaybackOutputViewer || speechReady[1].PlaybackOutput != SpeechPlaybackOutputViewer || speechReady[0].PauseAfterMS != 150 || speechReady[1].PauseAfterMS != 300 || speechReady[0].ProfileRevision != 7 {
		t.Errorf("SpeechPlan callback metadata = %#v", speechReady)
	}
	if len(speechReady) != 2 || speechReady[0].DisplayText != "え、本当ですか？" || speechReady[1].SpeechText != "確認します。" {
		t.Errorf("SpeechPlan callback text = %#v", speechReady)
	}
	for i, chunk := range speechReady {
		path := chunk.AudioPath
		if filepath.IsAbs(path) {
			t.Errorf("callback path[%d] should be relative to output dir, got %q", i, path)
		}
		if _, err := os.Stat(filepath.Join(outputDir, path)); err != nil {
			t.Errorf("saved WAV %q unavailable: %v", path, err)
		}
		if !strings.HasPrefix(chunk.AudioURL, "/viewer/tts/audio?path=") {
			t.Errorf("callback URL[%d] = %q, want local Viewer audio route", i, chunk.AudioURL)
		}
	}
}

func TestRenCrowTTSBridgeUsesLocalPlaybackSinkForSpeechPlan(t *testing.T) {
	var requestMu sync.Mutex
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		requests++
		requestMu.Unlock()
		var request gatewaySpeechPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", r.Header.Get("X-RenCrow-TTS-Request-Id"))
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "1")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", strconv.Itoa(request.UtteranceIndex))
		_, _ = w.Write(speechPlanTestWAV())
	}))
	defer server.Close()
	inner := &cancelableSpeechPlanSink{entered: make(chan typedSpeechPlanSinkItem, 2), completed: make(chan struct{})}
	sink := NewAsyncAudioSink(inner)
	ready := make(chan SpeechPlanChunkReady, 3)
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL:        server.URL,
		OutputDir:          t.TempDir(),
		DownloadAudio:      true,
		Sink:               sink,
		OnSpeechChunkReady: func(chunk SpeechPlanChunkReady) { ready <- chunk },
	})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-local-speech", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	latch := orchestrator.NewSpeechTurnCancellation()
	turnCtx := orchestrator.WithSpeechTurnCancellation(context.Background(), latch)
	speechCtx, doneSpeech := orchestrator.SpeechSynthesisContext(turnCtx)
	defer doneSpeech()
	turnID := string(modulecore.NewTurnID())
	done := make(chan error, 1)
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{
		{Segments: []speech.Segment{{Text: "再生中の一発話目。"}}, PauseAfterMS: 0},
		{Segments: []speech.Segment{{Text: "先読み二発話目。"}}, PauseAfterMS: 300},
		{Segments: []speech.Segment{{Text: "取消後の三発話目。"}}, PauseAfterMS: 25},
	}}
	go func() {
		done <- bridge.PushSpeechPlan(speechCtx, "tts-local-speech", string(modulecore.NewSessionID()), turnID, "mio", plan)
	}()
	first := receiveSpeechPlanChunk(t, ready)
	if first.PlaybackOutput != SpeechPlaybackOutputLocal || first.UtteranceIndex != 0 {
		t.Fatalf("first local playback metadata = %#v", first)
	}
	if latch.IsViewerPlayback() {
		t.Fatal("local SpeechPlan remained Viewer-owned")
	}
	firstPlayback := receiveTypedSpeechPlanSinkItem(t, inner.entered)
	if firstPlayback.chunk.TurnID != turnID || firstPlayback.chunk.UtteranceIndex != 0 || firstPlayback.chunk.PauseAfterMS != 0 || !filepath.IsAbs(firstPlayback.chunk.AudioPath) {
		t.Fatalf("first local playback chunk lost typed metadata/path: %+v", firstPlayback.chunk)
	}
	second := receiveSpeechPlanChunk(t, ready)
	if second.PlaybackOutput != SpeechPlaybackOutputLocal || second.UtteranceIndex != 1 || second.PauseAfterMS != 300 {
		t.Fatalf("second local playback metadata = %#v", second)
	}
	latch.Cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("PushSpeechPlan cancellation error = %v, want context.Canceled", err)
	}
	if err := sink.CompleteSession(context.Background(), "tts-local-speech"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inner.completed:
	case <-time.After(time.Second):
		t.Fatal("local async sink did not drain its cancelled queue")
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if inner.calls != 1 || inner.legacySubmissions != 0 {
		t.Fatalf("local SpeechPlan sink calls=%d legacy submissions=%d, want 1/0", inner.calls, inner.legacySubmissions)
	}
	requestMu.Lock()
	requestCount := requests
	requestMu.Unlock()
	if requestCount != 2 {
		t.Fatalf("local SpeechPlan Gateway requests=%d, want 2 (one lookahead)", requestCount)
	}
	select {
	case chunk := <-ready:
		t.Fatalf("third utterance became ready after cancellation: %#v", chunk)
	default:
	}
}

type cancelableSpeechPlanSink struct {
	mu                sync.Mutex
	entered           chan typedSpeechPlanSinkItem
	completed         chan struct{}
	calls             int
	legacySubmissions int
}

func (s *cancelableSpeechPlanSink) SubmitChunk(context.Context, string, audioChunk) error {
	s.mu.Lock()
	s.legacySubmissions++
	s.mu.Unlock()
	return nil
}

func (s *cancelableSpeechPlanSink) CompleteSession(context.Context, string) error {
	close(s.completed)
	return nil
}

func (s *cancelableSpeechPlanSink) SubmitSpeechPlanChunk(ctx context.Context, sessionID string, chunk SpeechPlanAudioChunk, progress speech.PlaybackProgress) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.entered <- typedSpeechPlanSinkItem{ctx: ctx, sessionID: sessionID, chunk: chunk, progress: progress}
	if err := progress.MarkStarted(chunk.UtteranceIndex); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func receiveTypedSpeechPlanSinkItem(t *testing.T, items <-chan typedSpeechPlanSinkItem) typedSpeechPlanSinkItem {
	t.Helper()
	select {
	case item := <-items:
		return item
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for typed local SpeechPlan sink item")
		return typedSpeechPlanSinkItem{}
	}
}

func TestRenCrowTTSBridgeLimitsLookaheadAndReleasesSynthesisGateDuringPlaybackWait(t *testing.T) {
	var mu sync.Mutex
	requestCounts := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request gatewaySpeechPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requestCounts[request.TurnID]++
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", r.Header.Get("X-RenCrow-TTS-Request-Id"))
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "1")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", strconv.Itoa(request.UtteranceIndex))
		_, _ = w.Write(speechPlanTestWAV())
	}))
	defer server.Close()
	sink := &speechPlanSinkRecorder{}
	readyA := make(chan SpeechPlanChunkReady, 4)
	readyB := make(chan SpeechPlanChunkReady, 2)
	turnA := string(modulecore.NewTurnID())
	turnB := string(modulecore.NewTurnID())
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: server.URL,
		OutputDir:   t.TempDir(),
		Sink:        sink,
		OnSpeechChunkReady: func(chunk SpeechPlanChunkReady) {
			if chunk.TurnID == turnA {
				readyA <- chunk
				return
			}
			readyB <- chunk
		},
	})
	for _, sessionID := range []string{"tts-plan-a", "tts-plan-b"} {
		if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: sessionID, CharacterID: "mio", VoiceID: "mio"}); err != nil {
			t.Fatal(err)
		}
	}
	progressA := newControlledPlaybackProgress()
	planA := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{
		{Segments: []speech.Segment{{Text: "長い一発話目。"}}},
		{Segments: []speech.Segment{{Text: "二発話目。"}}},
		{Segments: []speech.Segment{{Text: "三発話目。"}}},
	}}
	doneA := make(chan error, 1)
	go func() {
		ctx := speech.WithPlaybackProgress(context.Background(), progressA)
		doneA <- bridge.PushSpeechPlan(ctx, "tts-plan-a", string(modulecore.NewSessionID()), turnA, "mio", planA)
	}()
	var first SpeechPlanChunkReady
	select {
	case first = <-readyA:
	case err := <-doneA:
		t.Fatalf("first-turn synthesis completed before ready callback: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first-turn ready callback")
	}
	if first.UtteranceIndex != 0 {
		t.Fatalf("first ready utterance index=%d, want 0", first.UtteranceIndex)
	}
	if err := progressA.MarkStarted(0); err != nil {
		t.Fatal(err)
	}
	second := receiveSpeechPlanChunk(t, readyA)
	if second.UtteranceIndex != 1 {
		t.Fatalf("second ready utterance index=%d, want 1", second.UtteranceIndex)
	}
	mu.Lock()
	gotA := requestCounts[turnA]
	mu.Unlock()
	if gotA != 2 {
		t.Fatalf("Gateway requests for first turn while first utterance is still playing=%d, want 2", gotA)
	}

	// Turn A is waiting for the actual start of its second utterance. A separate
	// turn must still be able to use the shared HTTP synthesis slot.
	planB := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "別turn。"}}}}}
	doneB := make(chan error, 1)
	go func() {
		ctx := speech.WithPlaybackProgress(context.Background(), immediatePlaybackProgress{})
		doneB <- bridge.PushSpeechPlan(ctx, "tts-plan-b", string(modulecore.NewSessionID()), turnB, "mio", planB)
	}()
	if got := receiveSpeechPlanChunk(t, readyB); got.TurnID != turnB {
		t.Fatalf("second turn ready metadata=%#v", got)
	}
	if err := <-doneB; err != nil {
		t.Fatalf("second turn was blocked by first turn playback wait: %v", err)
	}
	select {
	case chunk := <-readyA:
		t.Fatalf("third utterance became ready during first utterance playback: %#v", chunk)
	case <-time.After(150 * time.Millisecond):
	}
	mu.Lock()
	gotA = requestCounts[turnA]
	mu.Unlock()
	if gotA != 2 {
		t.Fatalf("third Gateway request began during first utterance playback: request count=%d", gotA)
	}

	if err := progressA.MarkEnded(0); err != nil {
		t.Fatal(err)
	}
	if err := progressA.MarkStarted(1); err != nil {
		t.Fatal(err)
	}
	third := receiveSpeechPlanChunk(t, readyA)
	if third.UtteranceIndex != 2 {
		t.Fatalf("third ready utterance index=%d, want 2", third.UtteranceIndex)
	}
	if err := progressA.MarkEnded(1); err != nil {
		t.Fatal(err)
	}
	if err := progressA.MarkStarted(2); err != nil {
		t.Fatal(err)
	}
	if err := progressA.MarkEnded(2); err != nil {
		t.Fatal(err)
	}
	if err := <-doneA; err != nil {
		t.Fatalf("first turn PushSpeechPlan() error = %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.submissions != 0 {
		t.Fatalf("SpeechPlan submitted %d chunks to local sink, want none", sink.submissions)
	}
}

func TestRenCrowTTSBridgeCancellationDuringPlaybackWaitDropsRemainingUtterances(t *testing.T) {
	var requestCount int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request gatewaySpeechPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requestCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", r.Header.Get("X-RenCrow-TTS-Request-Id"))
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "1")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", strconv.Itoa(request.UtteranceIndex))
		_, _ = w.Write(speechPlanTestWAV())
	}))
	defer server.Close()
	ready := make(chan SpeechPlanChunkReady, 2)
	sink := &speechPlanSinkRecorder{}
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL:        server.URL,
		OutputDir:          t.TempDir(),
		Sink:               sink,
		OnSpeechChunkReady: func(chunk SpeechPlanChunkReady) { ready <- chunk },
	})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-cancel-wait", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	progress := newControlledPlaybackProgress()
	ctx, cancel := context.WithCancel(speech.WithPlaybackProgress(context.Background(), progress))
	done := make(chan error, 1)
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{
		{Segments: []speech.Segment{{Text: "現在再生中。"}}},
		{Segments: []speech.Segment{{Text: "未送信二件目。"}}},
		{Segments: []speech.Segment{{Text: "未送信三件目。"}}},
	}}
	go func() {
		done <- bridge.PushSpeechPlan(ctx, "tts-cancel-wait", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", plan)
	}()
	if got := receiveSpeechPlanChunk(t, ready); got.UtteranceIndex != 0 {
		t.Fatalf("first ready index=%d, want 0", got.UtteranceIndex)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("PushSpeechPlan cancellation error=%v, want context.Canceled", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requestCount != 1 {
		t.Fatalf("Gateway requests after playback wait cancellation=%d, want 1", requestCount)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.submissions != 0 {
		t.Fatalf("cancelled SpeechPlan submitted %d chunks to local sink, want 0", sink.submissions)
	}
}

func TestRenCrowTTSBridgePlaybackErrorDropsRemainingUtterances(t *testing.T) {
	var requestCount int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request gatewaySpeechPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requestCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", r.Header.Get("X-RenCrow-TTS-Request-Id"))
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "1")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", strconv.Itoa(request.UtteranceIndex))
		_, _ = w.Write(speechPlanTestWAV())
	}))
	defer server.Close()
	ready := make(chan SpeechPlanChunkReady, 1)
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL:        server.URL,
		OutputDir:          t.TempDir(),
		OnSpeechChunkReady: func(chunk SpeechPlanChunkReady) { ready <- chunk },
	})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-error-wait", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	progress := newControlledPlaybackProgress()
	done := make(chan error, 1)
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{
		{Segments: []speech.Segment{{Text: "再生エラー。"}}},
		{Segments: []speech.Segment{{Text: "破棄する二件目。"}}},
	}}
	go func() {
		ctx := speech.WithPlaybackProgress(context.Background(), progress)
		done <- bridge.PushSpeechPlan(ctx, "tts-error-wait", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", plan)
	}()
	if got := receiveSpeechPlanChunk(t, ready); got.UtteranceIndex != 0 {
		t.Fatalf("first ready index=%d, want 0", got.UtteranceIndex)
	}
	wantErr := errors.New("viewer playback error")
	if err := progress.MarkError(0, wantErr); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, wantErr) {
		t.Fatalf("PushSpeechPlan playback error=%v, want %v", err, wantErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if requestCount != 1 {
		t.Fatalf("Gateway requests after playback error=%d, want 1", requestCount)
	}
}

func TestRenCrowTTSBridgePreCanceledTurnDoesNotStartGatewayRequest(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{HTTPBaseURL: server.URL, OutputDir: t.TempDir()})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-pre-cancel", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	latch := orchestrator.NewSpeechTurnCancellation()
	latch.Cancel()
	turnCtx := orchestrator.WithSpeechTurnCancellation(context.Background(), latch)
	speechCtx, done := orchestrator.SpeechSynthesisContext(turnCtx)
	defer done()
	err := bridge.PushSpeechPlan(speechCtx, "tts-pre-cancel", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", speech.SpeechPlan{
		SchemaVersion: speech.SchemaVersion,
		Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: "送信禁止。"}}}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled PushSpeechPlan error=%v, want context.Canceled", err)
	}
	if requestCount != 0 {
		t.Fatalf("pre-canceled turn sent %d Gateway requests, want 0", requestCount)
	}
}

func receiveSpeechPlanChunk(t *testing.T, ready <-chan SpeechPlanChunkReady) SpeechPlanChunkReady {
	t.Helper()
	select {
	case chunk := <-ready:
		return chunk
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SpeechPlan ready chunk")
		return SpeechPlanChunkReady{}
	}
}

func TestRenCrowTTSBridgePushSpeechPlanRejectsMismatchedGatewayIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(http.Header, string)
		wav    []byte
	}{
		{name: "service", mutate: func(h http.Header, _ string) { h.Set("X-RenCrow-TTS-Gateway-Service", "other") }},
		{name: "request echo", mutate: func(h http.Header, _ string) { h.Set("X-RenCrow-TTS-Request-Id", "req_wrong") }},
		{name: "profile revision", mutate: func(h http.Header, _ string) { h.Set("X-RenCrow-TTS-Profile-Revision", "0") }},
		{name: "utterance index", mutate: func(h http.Header, _ string) { h.Set("X-RenCrow-TTS-Utterance-Index", "1") }},
		{name: "duplicate identity", mutate: func(h http.Header, requestID string) { h.Add("X-RenCrow-TTS-Request-Id", requestID) }},
		{name: "invalid WAV", wav: []byte("RIFF")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var envelope struct {
					UtteranceIndex int `json:"utterance_index"`
				}
				if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "audio/wav")
				w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
				requestID := r.Header.Get("X-RenCrow-TTS-Request-Id")
				w.Header().Set("X-RenCrow-TTS-Request-Id", requestID)
				w.Header().Set("X-RenCrow-TTS-Profile-Revision", "1")
				w.Header().Set("X-RenCrow-TTS-Utterance-Index", strconv.Itoa(envelope.UtteranceIndex))
				if tc.mutate != nil {
					tc.mutate(w.Header(), requestID)
				}
				wav := tc.wav
				if wav == nil {
					wav = speechPlanTestWAV()
				}
				_, _ = w.Write(wav)
			}))
			defer server.Close()
			bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{HTTPBaseURL: server.URL, OutputDir: t.TempDir()})
			if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-invalid", CharacterID: "mio", VoiceID: "mio"}); err != nil {
				t.Fatal(err)
			}
			pusher, ok := any(bridge).(speechPlanPushBridge)
			if !ok {
				t.Fatal("RenCrowTTSBridge does not implement optional PushSpeechPlan")
			}
			plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "音声。"}}}}}
			ctx := speech.WithPlaybackProgress(context.Background(), immediatePlaybackProgress{})
			err := pusher.PushSpeechPlan(ctx, "tts-invalid", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", plan)
			if err == nil {
				t.Fatal("PushSpeechPlan accepted a mismatched Gateway identity header")
			}
			entries, readErr := os.ReadDir(bridge.cfg.OutputDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("invalid response created local audio files: %#v", entries)
			}
		})
	}
}

func TestValidateSpeechPlanResponseRejectsCrossOriginAndMalformedWAV(t *testing.T) {
	requestURL, err := url.Parse("https://gateway.example/api/tts")
	if err != nil {
		t.Fatal(err)
	}
	responseURL, err := url.Parse("https://other.example/api/tts")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSpeechPlanResponseOrigin(&http.Request{URL: requestURL}, &http.Response{Request: &http.Request{URL: responseURL}}); err == nil {
		t.Fatal("cross-origin Gateway response was accepted")
	}
	if err := validateWAVBytes([]byte("RIFF")); err == nil {
		t.Fatal("malformed WAV response was accepted")
	}
}

func TestRenCrowTTSBridgeCompletesReceiptForEachSpeechUtterance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			UtteranceIndex int `json:"utterance_index"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestID := r.Header.Get("X-RenCrow-TTS-Request-Id")
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", requestID)
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "5")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", strconv.Itoa(request.UtteranceIndex))
		_, _ = w.Write(speechPlanTestWAV())
	}))
	defer server.Close()
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{HTTPBaseURL: server.URL, OutputDir: t.TempDir()})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-receipts", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	owner := &speechPlanReceiptOwner{}
	ctx, err := domaintransport.WithReceiptOwner(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx = speech.WithPlaybackProgress(ctx, immediatePlaybackProgress{})
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{
		{Segments: []speech.Segment{{Text: "一件目。"}}},
		{Segments: []speech.Segment{{Text: "二件目。"}}},
	}}
	if err := bridge.PushSpeechPlan(ctx, "tts-receipts", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", plan); err != nil {
		t.Fatalf("PushSpeechPlan() error = %v", err)
	}
	if owner.begun != 2 || owner.completed != 2 || owner.failed != 0 {
		t.Fatalf("transport receipts begun=%d completed=%d failed=%d, want 2/2/0", owner.begun, owner.completed, owner.failed)
	}
}

type speechPlanReceiptOwner struct {
	begun, completed, failed int
	beginErr                 error
}

func (o *speechPlanReceiptOwner) BeginRequest(_ context.Context, operation string) (domaintransport.Request, error) {
	o.begun++
	if o.beginErr != nil {
		return domaintransport.Request{}, o.beginErr
	}
	return domaintransport.Request{RequestID: modulecore.NewRequestID(), Operation: operation, Sequence: o.begun}, nil
}

func (o *speechPlanReceiptOwner) CompleteResponse(_ context.Context, requestID modulecore.RequestID, externalRef string) (domaintransport.Response, error) {
	o.completed++
	return domaintransport.Response{RequestID: requestID, ExternalRef: externalRef}, nil
}

func (o *speechPlanReceiptOwner) FailWithoutResponse(_ context.Context, _ modulecore.RequestID, _ string) error {
	o.failed++
	return nil
}

func TestRenCrowTTSBridgePushSpeechPlanCancellationSuppressesLateAudio(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", r.Header.Get("X-RenCrow-TTS-Request-Id"))
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "1")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", "0")
		_, _ = w.Write(speechPlanTestWAV())
		close(handlerDone)
	}))
	defer server.Close()
	outputDir := t.TempDir()
	var callbackCount int
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{
		HTTPBaseURL: server.URL,
		OutputDir:   outputDir,
		OnSpeechChunkReady: func(SpeechPlanChunkReady) {
			callbackCount++
		},
	})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-cancel", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	pusher := any(bridge).(speechPlanPushBridge)
	latch := orchestrator.NewSpeechTurnCancellation()
	turnCtx := orchestrator.WithSpeechTurnCancellation(context.Background(), latch)
	ctx, doneSpeech := orchestrator.SpeechSynthesisContext(turnCtx)
	defer doneSpeech()
	done := make(chan error, 1)
	go func() {
		done <- pusher.PushSpeechPlan(ctx, "tts-cancel", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", speech.SpeechPlan{
			SchemaVersion: speech.SchemaVersion,
			Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: "取消される発話。"}}}},
		})
	}()
	<-started
	latch.Cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("PushSpeechPlan cancellation error = %v, want context.Canceled", err)
	}
	close(release)
	<-handlerDone
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || callbackCount != 0 {
		t.Fatalf("late result escaped cancellation: files=%#v callbacks=%d", entries, callbackCount)
	}
}

func speechPlanTestWAV() []byte {
	const samples = 160
	dataSize := samples * 2
	wav := make([]byte, 44+dataSize)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 16000)
	binary.LittleEndian.PutUint32(wav[28:32], 32000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(dataSize))
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(wav[44+i*2:46+i*2], uint16(1000+(i%3)))
	}
	return wav
}

func TestRenCrowTTSBridgeRejectsMalformedOptionalMetricHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
	}{
		{name: "seed is not numeric", values: []string{"seed"}},
		{name: "seed is negative", values: []string{"-1"}},
		{name: "seed is empty", values: []string{""}},
		{name: "seed is ambiguous", values: []string{"1", "2"}},
		{name: "seed is comma joined", values: []string{"1,2"}},
		{name: "synthesis duration is fractional", values: []string{"1.5"}},
		{name: "synthesis duration is empty", values: []string{""}},
		{name: "synthesis duration exceeds turn timeout", values: []string{"300001"}},
		{name: "synthesis duration overflows", values: []string{"18446744073709551616"}},
		{name: "synthesis duration is ambiguous", values: []string{"1", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			oldWriter := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(oldWriter)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "audio/wav")
				w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
				w.Header().Set("X-RenCrow-TTS-Request-Id", r.Header.Get("X-RenCrow-TTS-Request-Id"))
				w.Header().Set("X-RenCrow-TTS-Profile-Revision", "1")
				w.Header().Set("X-RenCrow-TTS-Utterance-Index", "0")
				header := "X-RenCrow-TTS-Irodori-Seed"
				if strings.Contains(tc.name, "synthesis duration") {
					header = "X-RenCrow-TTS-Synthesis-Ms"
				}
				for _, value := range tc.values {
					w.Header().Add(header, value)
				}
				_, _ = w.Write(speechPlanTestWAV())
			}))
			defer server.Close()
			outputDir := t.TempDir()
			bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{HTTPBaseURL: server.URL, OutputDir: outputDir})
			if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-metric-header", CharacterID: "mio", VoiceID: "mio"}); err != nil {
				t.Fatal(err)
			}
			ctx := speech.WithPlaybackProgress(context.Background(), immediatePlaybackProgress{})
			err := bridge.PushSpeechPlan(ctx, "tts-metric-header", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", speech.SpeechPlan{
				SchemaVersion: speech.SchemaVersion,
				Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: "検証用発話。"}}}},
			})
			if err == nil {
				t.Fatal("PushSpeechPlan accepted a malformed or ambiguous optional metric header")
			}
			var observation map[string]any
			if decodeErr := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &observation); decodeErr != nil {
				t.Fatalf("decode fallback observation JSON %q: %v", logs.String(), decodeErr)
			}
			if observation["fallback_reason"] != speechPlanFallbackTransport {
				t.Errorf("fallback_reason = %#v, want %q", observation["fallback_reason"], speechPlanFallbackTransport)
			}
			entries, readErr := os.ReadDir(outputDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("invalid optional metric header created local audio files: %#v", entries)
			}
		})
	}
}

func TestRenCrowTTSBridgeWritesNullForUnavailableOptionalMetrics(t *testing.T) {
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", r.Header.Get("X-RenCrow-TTS-Request-Id"))
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "3")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", "0")
		_, _ = w.Write(speechPlanTestWAV())
	}))
	defer server.Close()
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{HTTPBaseURL: server.URL, OutputDir: t.TempDir()})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-metric-null", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	ctx := speech.WithPlaybackProgress(context.Background(), immediatePlaybackProgress{})
	if err := bridge.PushSpeechPlan(ctx, "tts-metric-null", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", speech.SpeechPlan{
		SchemaVersion: speech.SchemaVersion,
		Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: "null検証。"}}}},
	}); err != nil {
		t.Fatalf("PushSpeechPlan() error = %v", err)
	}
	var observation map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &observation); err != nil {
		t.Fatalf("decode observation JSON %q: %v", logs.String(), err)
	}
	for _, field := range []string{"irodori_seed", "tts_synthesis_ms", "first_audio_ms", "playback_duration_ms", "fallback_reason"} {
		if value, ok := observation[field]; !ok || value != nil {
			t.Errorf("%s = %#v (present=%t), want JSON null", field, value, ok)
		}
	}
	if _, ok := observation["tts_queue_wait_ms"].(float64); !ok {
		t.Errorf("tts_queue_wait_ms = %#v, want a measured number", observation["tts_queue_wait_ms"])
	}
}

func TestRenCrowTTSBridgeLogsReceiptBeginFailureStageWithoutSendingRequest(t *testing.T) {
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount++
	}))
	defer server.Close()
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{HTTPBaseURL: server.URL, OutputDir: t.TempDir()})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-receipt-begin-failure", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	owner := &speechPlanReceiptOwner{beginErr: errors.New("private receipt failure detail")}
	ctx, err := domaintransport.WithReceiptOwner(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx = speech.WithPlaybackProgress(ctx, immediatePlaybackProgress{})
	turnID := string(modulecore.NewTurnID())
	err = bridge.PushSpeechPlan(ctx, "tts-receipt-begin-failure", string(modulecore.NewSessionID()), turnID, "mio", speech.SpeechPlan{
		SchemaVersion: speech.SchemaVersion,
		Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: "private speech text"}}}},
	})
	if err == nil {
		t.Fatal("PushSpeechPlan() error = nil, want receipt begin failure")
	}
	if requestCount != 0 {
		t.Fatalf("Gateway request count = %d, want 0 when receipt begin fails", requestCount)
	}
	var observation map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &observation); err != nil {
		t.Fatalf("decode receipt failure observation JSON %q: %v", logs.String(), err)
	}
	if observation["turn_id"] != turnID || observation["utterance_index"] != float64(0) {
		t.Errorf("failure correlation = turn_id=%#v utterance_index=%#v", observation["turn_id"], observation["utterance_index"])
	}
	if observation["failure_stage"] != "receipt_begin" {
		t.Errorf("failure_stage = %#v, want receipt_begin", observation["failure_stage"])
	}
	if observation["fallback_reason"] != speechPlanFallbackTransport {
		t.Errorf("fallback_reason = %#v, want %q", observation["fallback_reason"], speechPlanFallbackTransport)
	}
	for _, forbidden := range []string{"private receipt failure detail", "private speech text", server.URL} {
		if strings.Contains(logs.String(), forbidden) {
			t.Errorf("receipt failure observation leaked %q: %s", forbidden, logs.String())
		}
	}
}

func TestRenCrowTTSBridgeLogsGatewayStatusFailureStageAndStatus(t *testing.T) {
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)

	const privateBody = "private gateway response detail"
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		http.Error(w, privateBody, http.StatusBadRequest)
	}))
	defer server.Close()
	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{HTTPBaseURL: server.URL, OutputDir: t.TempDir()})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-gateway-status-failure", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	turnID := string(modulecore.NewTurnID())
	err := bridge.PushSpeechPlan(speech.WithPlaybackProgress(context.Background(), immediatePlaybackProgress{}), "tts-gateway-status-failure", string(modulecore.NewSessionID()), turnID, "mio", speech.SpeechPlan{
		SchemaVersion: speech.SchemaVersion,
		Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: "private speech text"}}}},
	})
	if err == nil {
		t.Fatal("PushSpeechPlan() error = nil, want Gateway status failure")
	}
	if requestCount != 1 {
		t.Fatalf("Gateway request count = %d, want 1", requestCount)
	}
	var observation map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &observation); err != nil {
		t.Fatalf("decode Gateway failure observation JSON %q: %v", logs.String(), err)
	}
	if observation["turn_id"] != turnID || observation["utterance_index"] != float64(0) {
		t.Errorf("failure correlation = turn_id=%#v utterance_index=%#v", observation["turn_id"], observation["utterance_index"])
	}
	if observation["failure_stage"] != "gateway_status" {
		t.Errorf("failure_stage = %#v, want gateway_status", observation["failure_stage"])
	}
	if observation["gateway_status"] != float64(http.StatusBadRequest) {
		t.Errorf("gateway_status = %#v, want %d", observation["gateway_status"], http.StatusBadRequest)
	}
	if observation["fallback_reason"] != speechPlanFallbackTransport {
		t.Errorf("fallback_reason = %#v, want %q", observation["fallback_reason"], speechPlanFallbackTransport)
	}
	for _, forbidden := range []string{privateBody, "private speech text", server.URL} {
		if strings.Contains(logs.String(), forbidden) {
			t.Errorf("Gateway failure observation leaked %q: %s", forbidden, logs.String())
		}
	}
}

func TestRenCrowTTSBridgeLogsBoundedSpeechPlanTransportAndPlaybackMetrics(t *testing.T) {
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)

	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", r.Header.Get("X-RenCrow-TTS-Request-Id"))
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "7")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", "0")
		w.Header().Set("X-RenCrow-TTS-Irodori-Seed", "123")
		w.Header().Set("X-RenCrow-TTS-Synthesis-Ms", "42")
		_, _ = w.Write(speechPlanTestWAV())
	}))
	defer server.Close()

	bridge := NewRenCrowTTSBridge(RenCrowTTSBridgeConfig{HTTPBaseURL: server.URL, OutputDir: t.TempDir()})
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-metric-log", CharacterID: "mio", VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	// Occupying the existing gate makes its queue wait visible in the metric.
	bridge.speechPlanGate <- struct{}{}
	progress := orchestrator.NewSpeechTurnCancellation()
	ctx := orchestrator.WithSpeechTurnCancellation(context.Background(), progress)
	ready := make(chan struct{})
	bridge.cfg.OnSpeechChunkReady = func(SpeechPlanChunkReady) {
		if err := progress.MarkStarted(0); err != nil {
			t.Errorf("MarkStarted(0): %v", err)
		}
		close(ready)
	}
	done := make(chan error, 1)
	go func() {
		done <- bridge.PushSpeechPlan(ctx, "tts-metric-log", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", speech.SpeechPlan{
			SchemaVersion: speech.SchemaVersion,
			Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: "この本文とURLは記録されない。"}}}},
		})
	}()
	select {
	case <-time.After(40 * time.Millisecond):
	case <-requestStarted:
		t.Fatal("Gateway request started while the synthesis gate was held")
	}
	<-bridge.speechPlanGate
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ready audio")
	}
	time.Sleep(20 * time.Millisecond)
	if err := progress.MarkEnded(0); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("PushSpeechPlan() error = %v", err)
	}

	var observation map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &observation); err != nil {
		t.Fatalf("decode observation JSON %q: %v", logs.String(), err)
	}
	if observation["schema_version"] != float64(1) || observation["tag"] != "external" || observation["event"] != "speech.transport.observed" || observation["turn_id"] == "" || observation["speaker_id"] != "mio" || observation["utterance_index"] != float64(0) {
		t.Fatalf("observation identity = %#v", observation)
	}
	for field, want := range map[string]float64{
		"voice_profile_revision": 7,
		"irodori_seed":           123,
		"tts_synthesis_ms":       42,
	} {
		if observation[field] != want {
			t.Errorf("%s = %#v, want %v", field, observation[field], want)
		}
	}
	for _, field := range []string{"tts_queue_wait_ms", "first_audio_ms", "playback_duration_ms"} {
		value, ok := observation[field].(float64)
		if !ok || value <= 0 {
			t.Errorf("%s = %#v, want positive measured milliseconds", field, observation[field])
		}
	}
	if fallback, ok := observation["fallback_reason"]; !ok || fallback != nil {
		t.Errorf("fallback_reason = %#v, want JSON null on success", fallback)
	}
	if _, ok := observation["failure_stage"]; ok {
		t.Errorf("failure_stage = %#v, want omitted on success", observation["failure_stage"])
	}
	for _, forbidden := range []string{"この本文", server.URL, bridge.cfg.OutputDir, "Authorization"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Errorf("transport observation leaked %q: %s", forbidden, logs.String())
		}
	}
}
