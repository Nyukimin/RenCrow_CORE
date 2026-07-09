package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
)

func TestBuildTTSClientBridge_Disabled(t *testing.T) {
	cfg := &config.Config{}
	if got := buildTTSClientBridge(cfg, nil, nil, nil); got != nil {
		t.Fatal("expected nil bridge when tts is disabled")
	}
}

func TestTTSPublicSessionRouteMarksOldIdleChatRoutesStaleOnReset(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	registerTTSPublicSession("idle-old-tts", "idle-old", "idle-old:0000")
	if isStaleTTSPublicSession("idle-old-tts") {
		t.Fatal("newly registered route must not be stale")
	}

	resetTTSPublicSessionRoutesForIdleChat()
	if !isStaleTTSPublicSession("idle-old-tts") {
		t.Fatal("old route should be stale after idlechat reset")
	}
	if snapshot := snapshotTTSPublicSessions(); snapshot.RouteCount != 0 || snapshot.StaleRouteCount != 0 {
		t.Fatalf("reset should remove consumable routes from status, got %+v", snapshot)
	}

	registerTTSPublicSession("idle-new-tts", "idle-new", "idle-new:0000")
	if isStaleTTSPublicSession("idle-new-tts") {
		t.Fatal("route registered after reset must be current")
	}
	session, chunk := resolveTTSPublicChunk("idle-new-tts", 0)
	if session != "idle-new" || chunk != 0 {
		t.Fatalf("new route chunk = %s/%d, want idle-new/0", session, chunk)
	}
	if got := nextTTSPublicResponseID("idle-new"); got != "idle-new:0000" {
		t.Fatalf("new response sequence after reset = %q, want idle-new:0000", got)
	}
}

func TestTTSPublicSessionRouteMarksTimedOutUtteranceStale(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	registerTTSPublicSessionWithMessage("idle-timeout-tts", "idle-timeout", "idle-timeout:0000", "idle-timeout:msg:0001", 1)
	registerTTSPublicSessionWithMessage("idle-timeout-tts-next", "idle-timeout", "idle-timeout:0001", "idle-timeout:msg:0002", 2)

	matched := markTTSPublicSessionTimedOut("idle-timeout", "idle-timeout:msg:0001", 1, false)
	if len(matched) != 1 || matched[0] != "idle-timeout-tts" {
		t.Fatalf("unexpected timed out routes: %#v", matched)
	}
	if !isStaleTTSPublicSession("idle-timeout-tts") {
		t.Fatal("timed out route should be stale")
	}
	if isStaleTTSPublicSession("idle-timeout-tts-next") {
		t.Fatal("next utterance in same public session must remain playable")
	}
}

func TestNextTTSPublicResponseIDForMessageAlignsIdleChatMessageNumber(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	if got := nextTTSPublicResponseIDForMessage("idle-align", "idle-align:topic"); got != "idle-align:0000" {
		t.Fatalf("topic response id = %q, want idle-align:0000", got)
	}
	if got := nextTTSPublicResponseIDForMessage("idle-align", "idle-align:msg:0007"); got != "idle-align:0007" {
		t.Fatalf("message response id = %q, want idle-align:0007", got)
	}
	if got := nextTTSPublicResponseID("idle-align"); got != "idle-align:0008" {
		t.Fatalf("next response id = %q, want idle-align:0008", got)
	}
}

func TestNextTTSPublicResponseIDForMessageKeepsForecastAnnouncementsOutOfMessageNumberSeries(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	if got := nextTTSPublicResponseIDForMessage("forecast-align", "forecast-align:domain:0000"); got != "forecast-align:domain:0000" {
		t.Fatalf("domain response id = %q, want forecast-align:domain:0000", got)
	}
	if got := nextTTSPublicResponseIDForMessage("forecast-align", "forecast-align:topic:0000"); got != "forecast-align:topic:0000" {
		t.Fatalf("topic response id = %q, want forecast-align:topic:0000", got)
	}
	if got := nextTTSPublicResponseIDForMessage("forecast-align", "forecast-align:msg:0001"); got != "forecast-align:0001" {
		t.Fatalf("message response id = %q, want forecast-align:0001", got)
	}
}

func TestTTSClientBridgeIdleChatChunkPayloadIncludesCanonicalSpeechFields(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()
	t.Cleanup(clearAllIdleChatTTSPending)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"audio_path":"/audio/idle.wav"}`)
	}))
	t.Cleanup(srv.Close)

	var chunks []orchestrator.OrchestratorEvent
	bridge := buildTTSClientBridge(&config.Config{
		TTS: config.TTSConfig{
			Enabled:     true,
			HTTPBaseURL: srv.URL,
			VoiceID:     "mio",
			TimeoutMS:   15000,
		},
	}, func(ev orchestrator.OrchestratorEvent) {
		chunks = append(chunks, ev)
	}, nil, nil)
	if bridge == nil {
		t.Fatal("expected bridge")
	}

	registerTTSPublicSessionWithMessage("idle-canon-tts", "idle-canon", "idle-canon:0003", "idle-canon:msg:0003", 3)
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID:   "idle-canon-tts",
		ResponseID:  "idle-canon:0003",
		CharacterID: "mio",
		VoiceID:     "mio",
	}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	displayBridge, ok := bridge.(orchestrator.TTSDisplayBridge)
	if !ok {
		t.Fatalf("expected display bridge, got %T", bridge)
	}
	if err := displayBridge.PushTextWithDisplay(context.Background(), "idle-canon-tts", "同じチャンクです。", "同じチャンクです。", nil); err != nil {
		t.Fatalf("push text: %v", err)
	}

	audioChunks := ttsAudioChunkEvents(chunks)
	if len(audioChunks) != 1 {
		t.Fatalf("expected one audio chunk event, got %d events=%#v", len(audioChunks), chunks)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(audioChunks[0].Content), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["session_id"] != "idle-canon" || payload["message_id"] != "idle-canon:msg:0003" {
		t.Fatalf("unexpected identity payload: %#v", payload)
	}
	if payload["speech_text"] != "😊同じチャンクです。" || payload["text"] != "😊同じチャンクです。" || payload["display_text"] != "同じチャンクです。" {
		t.Fatalf("speech/display fields must preserve emotion-prefixed speech and clean display text: %#v", payload)
	}
	if payload["track"] != "default" {
		t.Fatalf("track = %#v, want default", payload["track"])
	}
	if payload["audio_path"] == "" && payload["audio_url"] == "" {
		t.Fatalf("audio path/url missing: %#v", payload)
	}
}

func TestTTSClientBridgeTopicPayloadIncludesBrightTopicPrefix(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()
	t.Cleanup(clearAllIdleChatTTSPending)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"audio_path":"/audio/topic.wav"}`)
	}))
	t.Cleanup(srv.Close)

	var chunks []orchestrator.OrchestratorEvent
	bridge := buildTTSClientBridge(&config.Config{
		TTS: config.TTSConfig{
			Enabled:     true,
			HTTPBaseURL: srv.URL,
			VoiceID:     "mio",
			TimeoutMS:   15000,
		},
	}, func(ev orchestrator.OrchestratorEvent) {
		chunks = append(chunks, ev)
	}, nil, nil)
	if bridge == nil {
		t.Fatal("expected bridge")
	}

	registerTTSPublicSessionWithMessage("idle-topic-tts", "idle-topic", "idle-topic:0000", "idle-topic:topic", 0)
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID:   "idle-topic-tts",
		ResponseID:  "idle-topic:0000",
		CharacterID: "user",
		VoiceID:     "mio",
	}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	displayBridge, ok := bridge.(orchestrator.TTSDisplayBridge)
	if !ok {
		t.Fatalf("expected display bridge, got %T", bridge)
	}
	topicSpeech := "きょうのおだい、車輪の軌跡と乗り手の皮膚感覚。"
	if err := displayBridge.PushTextWithDisplay(context.Background(), "idle-topic-tts", topicSpeech, "今日のお題：車輪の軌跡と乗り手の皮膚感覚", nil); err != nil {
		t.Fatalf("push text: %v", err)
	}

	audioChunks := ttsAudioChunkEvents(chunks)
	if len(audioChunks) != 1 {
		t.Fatalf("expected one audio chunk event, got %d events=%#v", len(audioChunks), chunks)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(audioChunks[0].Content), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["speech_text"] != "😊"+topicSpeech || payload["text"] != "😊"+topicSpeech {
		t.Fatalf("topic speech must preserve bright prefix and full topic speech text: %#v", payload)
	}
}

func ttsAudioChunkEvents(events []orchestrator.OrchestratorEvent) []orchestrator.OrchestratorEvent {
	filtered := make([]orchestrator.OrchestratorEvent, 0, len(events))
	for _, ev := range events {
		if ev.Type == "tts.audio_chunk" {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

func TestTTSPublicSessionRouteSurvivesSessionCompletedUntilPlaybackAck(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()

	registerTTSPublicSessionWithMessage("idle-complete-tts", "idle-complete", "idle-complete:0000", "idle-complete:msg:0001", 1)
	ch := registerIdleChatTTSPending("idle-complete-tts", "idle-complete:0000")

	matched := markTTSPublicSessionTimedOut("idle-complete", "idle-complete:msg:0001", 1, false)
	if len(matched) != 1 || matched[0] != "idle-complete-tts" {
		t.Fatalf("route should remain matchable until playback ack, got %#v", matched)
	}
	clearIdleChatTTSPending("idle-complete-tts")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("cleanup should close pending channel")
	}
}

func TestTTSPlaybackAckClearsPublicSessionRoute(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()

	registerTTSPublicSessionWithMessage("idle-ack-tts", "idle-ack", "idle-ack:0000", "idle-ack:msg:0001", 1)
	registerIdleChatTTSPending("idle-ack-tts", "idle-ack:0000")

	if !notifyIdleChatTTSPlaybackCompleted("idle-ack:0000") {
		t.Fatal("playback ack should match pending response")
	}
	if got := resolveTTSPublicResponse("idle-ack-tts"); got != "" {
		t.Fatalf("public session route should be cleared after playback ack, got response %q", got)
	}
}

func TestClearTTSPublicSequenceStateIfNoRoutes(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	registerTTSPublicSession("idle-seq-tts", "idle-seq", "idle-seq:0000")
	if got := nextTTSPublicResponseID("idle-seq"); got != "idle-seq:0000" {
		t.Fatalf("first response = %q", got)
	}
	if session, chunk := resolveTTSPublicChunk("idle-seq-tts", 0); session != "idle-seq" || chunk != 0 {
		t.Fatalf("first chunk = %s/%d", session, chunk)
	}

	clearTTSPublicSequenceStateIfNoRoutes()
	if snapshot := snapshotTTSPublicSessions(); snapshot.NextChunkSessionCount != 1 || snapshot.NextResponseSessionCount != 1 {
		t.Fatalf("active route must keep sequence state, got %+v", snapshot)
	}

	clearTTSPublicSession("idle-seq-tts")
	clearTTSPublicSequenceStateIfNoRoutes()
	if snapshot := snapshotTTSPublicSessions(); snapshot.NextChunkSessionCount != 0 || snapshot.NextResponseSessionCount != 0 {
		t.Fatalf("sequence state should clear when no routes remain, got %+v", snapshot)
	}
}

func TestResetIdleChatTTSQueueClosesPendingPlaybackWaits(t *testing.T) {
	ch := registerIdleChatTTSPending("idle-reset-tts", "idle-reset:0000")
	resetIdleChatTTSQueue()

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("reset should close pending idlechat TTS wait channel")
	}
}

func TestBuildTTSClientBridge_Enabled(t *testing.T) {
	cfg := &config.Config{
		TTS: config.TTSConfig{
			Enabled:     true,
			HTTPBaseURL: "http://127.0.0.1:8765",
			VoiceID:     "female_01",
			TimeoutMS:   15000,
		},
	}
	if got := buildTTSClientBridge(cfg, nil, nil, nil); got == nil {
		t.Fatal("expected non-nil bridge when tts is enabled")
	}
}

func TestBuildTTSClientBridge_UsesRenCrowBridge(t *testing.T) {
	cfg := &config.Config{
		TTS: config.TTSConfig{
			Enabled:     true,
			HTTPBaseURL: "http://127.0.0.1:8765",
			VoiceID:     "female_01",
			TimeoutMS:   15000,
		},
	}
	got := buildTTSClientBridge(cfg, nil, nil, nil)
	if got == nil {
		t.Fatal("expected non-nil bridge")
	}
	if _, ok := got.(*ttsinfra.RenCrowTTSBridge); !ok {
		t.Fatalf("expected RenCrowTTSBridge, got %T", got)
	}
}

func TestBuildTTSClientBridge_UsesIrodoriDirectBridge(t *testing.T) {
	cfg := &config.Config{
		TTS: config.TTSConfig{
			Enabled:   true,
			OutputDir: t.TempDir(),
			Irodori: config.TTSIrodoriConfig{
				Enabled: true,
				BaseURL: "http://127.0.0.1:7870",
				VoiceID: "mio",
			},
		},
	}
	got := buildTTSClientBridge(cfg, nil, nil, nil)
	if got == nil {
		t.Fatal("expected non-nil bridge")
	}
	if _, ok := got.(*ttsinfra.ProviderTTSBridge); !ok {
		t.Fatalf("expected generic direct TTS bridge, got %T", got)
	}
}

func TestBuildTTSClientBridge_WithoutPlaybackCommands(t *testing.T) {
	cfg := &config.Config{
		TTS: config.TTSConfig{
			Enabled:     true,
			HTTPBaseURL: "http://127.0.0.1:8765",
			VoiceID:     "female_01",
			TimeoutMS:   15000,
		},
	}

	got := buildTTSClientBridge(cfg, nil, nil, nil)
	if got == nil {
		t.Fatal("expected non-nil bridge")
	}
	if _, ok := got.(*ttsinfra.RenCrowTTSBridge); !ok {
		t.Fatalf("expected RenCrowTTSBridge, got %T", got)
	}
}

func TestTTSPublicSessionRouteKeepsLogicalSessionAndGlobalChunkOrder(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	registerTTSPublicSession("idle-1-tts-a", "idle-1", "idle-1:0000")
	registerTTSPublicSession("idle-1-tts-b", "idle-1", "idle-1:0001")

	session, chunk := resolveTTSPublicChunk("idle-1-tts-a", 0)
	if session != "idle-1" || chunk != 0 {
		t.Fatalf("first chunk = %s/%d, want idle-1/0", session, chunk)
	}
	session, chunk = resolveTTSPublicChunk("idle-1-tts-a", 1)
	if session != "idle-1" || chunk != 1 {
		t.Fatalf("second chunk = %s/%d, want idle-1/1", session, chunk)
	}
	session, chunk = resolveTTSPublicChunk("idle-1-tts-b", 0)
	if session != "idle-1" || chunk != 2 {
		t.Fatalf("next utterance chunk = %s/%d, want idle-1/2", session, chunk)
	}
	session, chunk = resolveTTSPublicChunk("normal-session", 7)
	if session != "normal-session" || chunk != 7 {
		t.Fatalf("unmapped chunk = %s/%d, want passthrough", session, chunk)
	}
	if got := nextTTSPublicResponseID("idle-1"); got != "idle-1:0000" {
		t.Fatalf("first response id = %q", got)
	}
	if got := nextTTSPublicResponseID("idle-1"); got != "idle-1:0001" {
		t.Fatalf("second response id = %q", got)
	}
}
