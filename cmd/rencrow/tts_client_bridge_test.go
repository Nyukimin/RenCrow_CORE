package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func TestBuildTTSClientBridge_Disabled(t *testing.T) {
	cfg := &config.Config{}
	if got := buildTTSClientBridge(cfg, nil, nil, nil, nil); got != nil {
		t.Fatal("expected nil bridge when tts is disabled")
	}
}

func TestBuildTTSClientBridgeLoadsOwnerTokenAndAuthenticatesSynthesis(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "tts-owner.token")
	if err := os.WriteFile(tokenPath, []byte("owner-test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"gateway_service":"tts-gateway","audio_path":"/audio/test.wav"}`)
	}))
	t.Cleanup(server.Close)
	outputDir := t.TempDir()
	audioOwner, canonicalSessionID, binding := newTTSClientBridgeTestAudioOwner(t, outputDir, server.URL, "")
	bridge := buildTTSClientBridge(&config.Config{TTS: config.TTSConfig{
		Enabled: true, GatewayBaseURL: server.URL, AuthTokenFile: tokenPath, VoiceID: "mio", TimeoutMS: 15000, OutputDir: outputDir,
	}}, audioOwner, nil, nil, nil)
	if bridge == nil {
		t.Fatal("expected authenticated TTS bridge")
	}
	if err := bridge.StartSession(domainconversation.WithSessionBinding(context.Background(), binding), orchestrator.TTSSessionStart{SessionID: "auth-session", CanonicalSessionID: string(canonicalSessionID), VoiceID: "mio"}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.PushText(context.Background(), "auth-session", "認証確認です。", nil); err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer owner-test-token" {
		t.Fatalf("Authorization = %q, want owner bearer", authorization)
	}
}

func TestBuildTTSClientBridgeRegistersAudioBeforePublishingChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"gateway_service":"tts-gateway","audio_path":"/audio/registered.wav"}`)
	}))
	t.Cleanup(server.Close)

	outputDir := t.TempDir()
	audioOwner, sessionID, binding := newTTSClientBridgeTestAudioOwner(t, outputDir, server.URL, "")
	var readyRegistered, eventRegistered, eventBindingCorrect bool
	var audioEventCount int
	bridge := buildTTSClientBridge(&config.Config{TTS: config.TTSConfig{
		Enabled: true, GatewayBaseURL: server.URL, VoiceID: "mio", TimeoutMS: 10000, OutputDir: outputDir,
	}}, audioOwner, func(event orchestrator.OrchestratorEvent) {
		if event.Type != "tts.audio_chunk" {
			return
		}
		address := binding.ChannelAddress()
		eventBindingCorrect = event.SessionID == binding.SessionID() &&
			event.PublicationIncarnation == binding.PublicationIncarnation() &&
			event.Channel == address.ChannelType() && event.ChannelAddress == address.ExternalConversationID() &&
			event.Visibility == string(binding.Audience())
		audioEventCount++
		registeredSession, found := audioOwner.lookupURL(server.URL + "/audio/registered.wav")
		eventRegistered = found && registeredSession == string(sessionID)
	}, func(_, _, _ string) {
		registeredSession, found := audioOwner.lookupURL(server.URL + "/audio/registered.wav")
		readyRegistered = found && registeredSession == string(sessionID)
	}, nil)
	if bridge == nil {
		t.Fatal("expected TTS bridge")
	}
	if err := bridge.StartSession(domainconversation.WithSessionBinding(context.Background(), binding), orchestrator.TTSSessionStart{
		SessionID: "tts-register-audio", CanonicalSessionID: string(sessionID), CharacterID: "mio", VoiceID: "mio",
	}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.PushText(context.Background(), "tts-register-audio", "登録確認です。", nil); err != nil {
		t.Fatalf("PushText(): %v", err)
	}
	if !readyRegistered {
		t.Fatal("audio resource was not bound to the canonical Session before onChunkReady")
	}
	if !eventRegistered || !eventBindingCorrect || audioEventCount != 1 {
		t.Fatalf("audio event registration=%v binding=%v count=%d, want exact frozen binding before one event", eventRegistered, eventBindingCorrect, audioEventCount)
	}
}

func TestBuildTTSClientBridgePublishesOneSpeechPlanEventPerUtterance(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	t.Cleanup(resetTTSPublicSessionStateForTest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tts" {
			t.Errorf("Gateway request = %s %s, want POST /api/tts", r.Method, r.URL.Path)
		}
		var request struct {
			UtteranceIndex int `json:"utterance_index"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode SpeechPlan request: %v", err)
			return
		}
		requestID := r.Header.Get("X-RenCrow-TTS-Request-Id")
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", requestID)
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "2")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", fmt.Sprint(request.UtteranceIndex))
		_, _ = w.Write(ttsClientTestWAV())
	}))
	defer server.Close()

	turnID := modulecore.NewTurnID()
	taskID := modulecore.NewTaskID()
	messageID := modulecore.NewMessageID()
	outputDir := t.TempDir()
	audioOwner, sessionID, binding := newTTSClientBridgeTestAudioOwner(t, outputDir, server.URL, "")
	registerTTSPublicSessionWithMessage("tts-speech-plan-event", string(sessionID), taskID.String(), string(messageID), 3)
	var events []orchestrator.OrchestratorEvent
	playback := orchestrator.NewSpeechTurnCancellation()
	registeredSpeechPlanEvents := 0
	bridge := buildTTSClientBridge(&config.Config{TTS: config.TTSConfig{
		Enabled: true, GatewayBaseURL: server.URL, VoiceID: "mio", TimeoutMS: 10000, OutputDir: outputDir,
	}}, audioOwner, func(ev orchestrator.OrchestratorEvent) {
		events = append(events, ev)
		if ev.Type != "tts.audio_chunk" {
			return
		}
		var payload moduletts.AudioChunkEventPayload
		if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
			t.Errorf("decode audio chunk event for playback progress: %v", err)
			return
		}
		registeredSession, registered := audioOwner.lookupPathRel(payload.AudioPath)
		if !registered || registeredSession != string(sessionID) {
			t.Errorf("SpeechPlan resource %q registered=%v Session=%q, want canonical Session %q before event", payload.AudioPath, registered, registeredSession, sessionID)
		} else {
			registeredSpeechPlanEvents++
		}
		if err := playback.MarkStarted(payload.ChunkIndex); err != nil {
			t.Errorf("MarkStarted(%d): %v", payload.ChunkIndex, err)
			return
		}
		if err := playback.MarkEnded(payload.ChunkIndex); err != nil {
			t.Errorf("MarkEnded(%d): %v", payload.ChunkIndex, err)
		}
	}, nil, nil)
	if bridge == nil {
		t.Fatal("TTS bridge unavailable")
	}
	if err := bridge.StartSession(domainconversation.WithSessionBinding(context.Background(), binding), orchestrator.TTSSessionStart{
		SessionID: "tts-speech-plan-event", CanonicalSessionID: string(sessionID), PublicPlaybackRef: taskID.String(), TraceID: string(modulecore.NewTraceID()), CharacterID: "mio", VoiceID: "mio",
	}); err != nil {
		t.Fatal(err)
	}
	pusher, ok := bridge.(orchestrator.SpeechPlanTTSBridge)
	if !ok {
		t.Fatal("client bridge does not expose SpeechPlan transport")
	}
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{
		{Segments: []speech.Segment{{Text: "一つ目。"}}, PauseAfterMS: 150},
		{Segments: []speech.Segment{{Text: "二つ目。"}}, PauseAfterMS: 500},
	}}
	ctx := orchestrator.WithSpeechTurnCancellation(context.Background(), playback)
	if err := pusher.PushSpeechPlan(ctx, "tts-speech-plan-event", string(sessionID), string(turnID), "mio", plan); err != nil {
		t.Fatalf("PushSpeechPlan() error = %v", err)
	}
	var chunks []moduletts.AudioChunkEventPayload
	for _, event := range events {
		if event.Type != "tts.audio_chunk" {
			continue
		}
		var payload moduletts.AudioChunkEventPayload
		if err := json.Unmarshal([]byte(event.Content), &payload); err != nil {
			t.Fatalf("decode audio chunk event: %v", err)
		}
		chunks = append(chunks, payload)
	}
	if len(chunks) != 2 {
		t.Fatalf("SpeechPlan audio event count = %d, want 2: %#v", len(chunks), chunks)
	}
	if registeredSpeechPlanEvents != len(chunks) {
		t.Fatalf("registered SpeechPlan audio event count=%d, want %d", registeredSpeechPlanEvents, len(chunks))
	}
	for index, chunk := range chunks {
		if chunk.TurnID != string(turnID) || chunk.ChunkIndex != index {
			t.Errorf("chunk[%d] identity/index = turn=%q index=%d", index, chunk.TurnID, chunk.ChunkIndex)
		}
		if want := []int{150, 500}[index]; chunk.PauseAfterMS == nil || *chunk.PauseAfterMS != want {
			t.Errorf("chunk[%d] pause_after_ms = %v, want %d", index, chunk.PauseAfterMS, want)
		}
		if chunk.PlaybackOutput != "viewer" {
			t.Errorf("chunk[%d] playback_output = %q, want viewer for browser-only mode", index, chunk.PlaybackOutput)
		}
	}
}

func ttsClientTestWAV() []byte {
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
	for index := 0; index < samples; index++ {
		binary.LittleEndian.PutUint16(wav[44+index*2:46+index*2], uint16(1000+index%3))
	}
	return wav
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
	if got := nextTTSPublicPlaybackRef("idle-new"); got != "idle-new:0000" {
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

func TestNextTTSPublicPlaybackRefForMessageAlignsIdleChatMessageNumber(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	if got := nextTTSPublicPlaybackRefForMessage("idle-align", "idle-align:topic"); got != "idle-align:0000" {
		t.Fatalf("topic response id = %q, want idle-align:0000", got)
	}
	if got := nextTTSPublicPlaybackRefForMessage("idle-align", "idle-align:msg:0007"); got != "idle-align:0007" {
		t.Fatalf("message response id = %q, want idle-align:0007", got)
	}
	if got := nextTTSPublicPlaybackRef("idle-align"); got != "idle-align:0008" {
		t.Fatalf("next response id = %q, want idle-align:0008", got)
	}
}

func TestNextTTSPublicPlaybackRefForMessageKeepsForecastAnnouncementsOutOfMessageNumberSeries(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	if got := nextTTSPublicPlaybackRefForMessage("forecast-align", "forecast-align:domain:0000"); got != "forecast-align:domain:0000" {
		t.Fatalf("domain response id = %q, want forecast-align:domain:0000", got)
	}
	if got := nextTTSPublicPlaybackRefForMessage("forecast-align", "forecast-align:topic:0000"); got != "forecast-align:topic:0000" {
		t.Fatalf("topic response id = %q, want forecast-align:topic:0000", got)
	}
	if got := nextTTSPublicPlaybackRefForMessage("forecast-align", "forecast-align:msg:0001"); got != "forecast-align:0001" {
		t.Fatalf("message response id = %q, want forecast-align:0001", got)
	}
}

func TestTTSClientBridgePublishesIdleChatChunkWithCurrentPublicRoute(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()
	t.Cleanup(resetTTSPublicSessionStateForTest)
	t.Cleanup(clearAllIdleChatTTSPending)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"gateway_service":"tts-gateway","audio_path":"/audio/idle.wav"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/audio/idle.wav":
			w.Header().Set("Content-Type", "audio/wav")
			fmt.Fprint(w, "RIFF-idle-audio")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	var chunks []orchestrator.OrchestratorEvent
	outputDir := t.TempDir()
	audioOwner, _, _ := newTTSClientBridgeTestAudioOwner(t, outputDir, srv.URL, "")
	readyCount := 0
	bridge := buildTTSClientBridge(&config.Config{
		TTS: config.TTSConfig{
			Enabled:        true,
			GatewayBaseURL: srv.URL,
			VoiceID:        "mio",
			TimeoutMS:      15000,
			OutputDir:      outputDir,
		},
	}, audioOwner, func(ev orchestrator.OrchestratorEvent) {
		chunks = append(chunks, ev)
	}, func(_, _, _ string) { readyCount++ }, nil)
	if bridge == nil {
		t.Fatal("expected bridge")
	}

	registerTTSPublicSessionForPlayback("idle-canon-tts", "idle-canon", "idle-canon:0003", "idle-canon:msg:0003", 3, "test-runtime", 1)
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID:         "idle-canon-tts",
		PublicPlaybackRef: "idle-canon:0003",
		CharacterID:       "mio",
		VoiceID:           "mio",
	}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	displayBridge, ok := bridge.(orchestrator.TTSDisplayBridge)
	if !ok {
		t.Fatalf("expected display bridge, got %T", bridge)
	}
	if err := displayBridge.PushTextWithDisplay(context.Background(), "idle-canon-tts", "同じチャンクです。", "同じチャンクです。", nil); err != nil {
		t.Fatalf("IdleChat chunk with a current public route: %v", err)
	}

	audioChunks := ttsAudioChunkEvents(chunks)
	if len(audioChunks) != 1 {
		t.Fatalf("IdleChat with a current public route published %d chunks, want 1: %#v", len(audioChunks), chunks)
	}
	var payload moduletts.AudioChunkEventPayload
	if err := json.Unmarshal([]byte(audioChunks[0].Content), &payload); err != nil {
		t.Fatalf("decode IdleChat audio payload: %v", err)
	}
	if payload.SessionID != "idle-canon" || payload.MessageID != "idle-canon:msg:0003" {
		t.Fatalf("unexpected public identity payload: %#v", payload)
	}
	if payload.SpeechText != "😊同じチャンクです。" || payload.Text != "😊同じチャンクです。" || payload.DisplayText != "同じチャンクです。" {
		t.Fatalf("speech/display fields must preserve the emotion prefix and clean display text: %#v", payload)
	}
	registration, found := audioOwner.lookupURLRegistration(srv.URL + "/audio/idle.wav")
	if !found || registration.authorityKind != ttsAudioIdleChatPublicAuthority || registration.publicProof.PublicSessionID != "idle-canon" || registration.publicProof.RuntimeEpoch != "test-runtime" || registration.publicProof.PlaybackGeneration != 1 {
		t.Fatalf("IdleChat audio registration=%+v found=%v, want current public route proof", registration, found)
	}
	if _, found := audioOwner.lookupURL(srv.URL + "/audio/idle.wav"); found {
		t.Fatal("public IdleChat resource was exposed as a canonical Session registration")
	}
	if readyCount != 1 {
		t.Fatalf("IdleChat current route notified chunk-ready %d times, want 1", readyCount)
	}

	serve := handleTTSAudio(outputDir, srv.URL, audioOwner)
	request := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(srv.URL+"/audio/idle.wav"), nil)
	served := httptest.NewRecorder()
	serve.ServeHTTP(served, request)
	if served.Code != http.StatusOK || served.Body.String() != "RIFF-idle-audio" {
		t.Fatalf("current IdleChat route audio response=%d body=%q, want 200 RIFF-idle-audio", served.Code, served.Body.String())
	}

	resetTTSPublicSessionRoutesForIdleChat()
	request = httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(srv.URL+"/audio/idle.wav"), nil)
	stale := httptest.NewRecorder()
	serve.ServeHTTP(stale, request)
	if stale.Code != http.StatusNotFound {
		t.Fatalf("stale IdleChat route audio status=%d body=%q, want 404", stale.Code, stale.Body.String())
	}
}

func TestTTSClientBridgeReturnsPublicRegistrationErrorWithoutPublishingChunk(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	t.Cleanup(resetTTSPublicSessionStateForTest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"gateway_service":"tts-gateway","audio_path":"/audio/unregistered.wav"}`)
	}))
	t.Cleanup(srv.Close)

	outputDir := t.TempDir()
	audioOwner, _, _ := newTTSClientBridgeTestAudioOwner(t, outputDir, srv.URL, "")
	var events []orchestrator.OrchestratorEvent
	readyCount := 0
	bridge := buildTTSClientBridge(&config.Config{TTS: config.TTSConfig{
		Enabled: true, GatewayBaseURL: srv.URL, VoiceID: "mio", TimeoutMS: 15000, OutputDir: outputDir,
	}}, audioOwner, func(event orchestrator.OrchestratorEvent) {
		events = append(events, event)
	}, func(_, _, _ string) { readyCount++ }, nil)
	if bridge == nil {
		t.Fatal("expected bridge")
	}
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID: "idle-unregistered-tts", PublicPlaybackRef: "idle-unregistered:0000", CharacterID: "mio", VoiceID: "mio",
	}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	displayBridge, ok := bridge.(orchestrator.TTSDisplayBridge)
	if !ok {
		t.Fatalf("expected display bridge, got %T", bridge)
	}
	if err := displayBridge.PushTextWithDisplay(context.Background(), "idle-unregistered-tts", "route proof is missing", "route proof is missing", nil); err == nil {
		t.Fatal("missing public route proof must surface an audio registration error")
	}
	if got := ttsAudioChunkEvents(events); len(got) != 0 {
		t.Fatalf("chunk published without public route proof: %#v", got)
	}
	if readyCount != 0 || audioOwner.RegisteredResourceCount() != 0 {
		t.Fatalf("missing public route proof left ready=%d registered=%d, want both zero", readyCount, audioOwner.RegisteredResourceCount())
	}
}

func TestTTSClientBridgeRejectsDelayedCanonicalCallbackAfterSameIDRecreationWithoutIdleChatFallback(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	t.Cleanup(resetTTSPublicSessionStateForTest)
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseResponse
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"gateway_service":"tts-gateway","audio_path":"/audio/canonical-failure.wav"}`)
	}))
	t.Cleanup(srv.Close)

	outputDir := t.TempDir()
	canonicalSessionID, canonicalSession := ttsAudioTestSession(t, "")
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(canonicalSessionID): canonicalSession,
	}}
	binding := ttsAudioTestSessionBinding(t, canonicalSessionID, canonicalSession, 1)
	audioOwner := newTTSAudioAccessOwner(outputDir, srv.URL, "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository))
	var events []orchestrator.OrchestratorEvent
	readyCount := 0
	bridge := buildTTSClientBridge(&config.Config{TTS: config.TTSConfig{
		Enabled: true, GatewayBaseURL: srv.URL, VoiceID: "mio", TimeoutMS: 15000, OutputDir: outputDir,
	}}, audioOwner, func(event orchestrator.OrchestratorEvent) {
		events = append(events, event)
	}, func(_, _, _ string) { readyCount++ }, nil)
	if bridge == nil {
		t.Fatal("expected bridge")
	}
	registerTTSPublicSessionForPlayback("idle-invalid-canonical-tts", "idle-invalid-canonical", "idle-invalid-canonical:0000", "idle-invalid-canonical:msg:0001", 1, "test-runtime", 1)
	if err := bridge.StartSession(domainconversation.WithSessionBinding(context.Background(), binding), orchestrator.TTSSessionStart{
		SessionID: "idle-invalid-canonical-tts", CanonicalSessionID: string(canonicalSessionID),
		PublicPlaybackRef: "idle-invalid-canonical:0000", CharacterID: "mio", VoiceID: "mio",
	}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	displayBridge, ok := bridge.(orchestrator.TTSDisplayBridge)
	if !ok {
		t.Fatalf("expected display bridge, got %T", bridge)
	}
	pushResult := make(chan error, 1)
	go func() {
		pushResult <- displayBridge.PushTextWithDisplay(context.Background(), "idle-invalid-canonical-tts", "canonical authority must fail", "canonical authority must fail", nil)
	}()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("synthesis request did not reach the TTS gateway")
	}
	recreatedSession, err := domainsession.NewCanonicalSession(canonicalSessionID, "2026-10-24", binding.ChannelAddress(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if recreatedSession.PublicationIncarnation() == binding.PublicationIncarnation() {
		t.Fatal("same-ID recreation reused the old publication incarnation")
	}
	repository.sessions[string(canonicalSessionID)] = recreatedSession
	close(releaseResponse)
	select {
	case err := <-pushResult:
		if err == nil {
			t.Fatal("stale canonical callback must fail even when an IdleChat route proof exists")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delayed TTS synthesis callback did not finish")
	}
	if got := ttsAudioChunkEvents(events); len(got) != 0 {
		t.Fatalf("chunk published after failed canonical registration: %#v", got)
	}
	if readyCount != 0 || audioOwner.RegisteredResourceCount() != 0 {
		t.Fatalf("failed canonical registration left ready=%d registered=%d, want both zero", readyCount, audioOwner.RegisteredResourceCount())
	}
	if _, found := audioOwner.lookupURLRegistration(srv.URL + "/audio/canonical-failure.wav"); found {
		t.Fatal("stale canonical callback fell back to the IdleChat public authority")
	}
}

func TestTTSClientBridgeNormalSessionCompletionKeepsPublicPlaybackRef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"gateway_service":"tts-gateway","audio_path":"/audio/chat.wav"}`)
	}))
	t.Cleanup(srv.Close)

	var events []orchestrator.OrchestratorEvent
	traceID := modulecore.NewTraceID()
	taskID := modulecore.NewTaskID()
	outputDir := t.TempDir()
	audioOwner, canonicalSessionID, binding := newTTSClientBridgeTestAudioOwner(t, outputDir, srv.URL, "")
	bridge := buildTTSClientBridge(&config.Config{TTS: config.TTSConfig{
		Enabled: true, GatewayBaseURL: srv.URL, VoiceID: "mio", TimeoutMS: 15000, OutputDir: outputDir,
	}}, audioOwner, func(event orchestrator.OrchestratorEvent) {
		events = append(events, event)
	}, nil, nil)

	if err := bridge.StartSession(domainconversation.WithSessionBinding(context.Background(), binding), orchestrator.TTSSessionStart{
		SessionID: "viewer-chat-1", CanonicalSessionID: string(canonicalSessionID), PublicPlaybackRef: taskID.String(), TraceID: string(traceID), CharacterID: "mio", VoiceID: "mio",
	}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.PushText(context.Background(), "viewer-chat-1", "完了です。", nil); err != nil {
		t.Fatal(err)
	}
	if err := bridge.EndSession(context.Background(), "viewer-chat-1"); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, event := range events {
		if event.Type == "metrics.latency" || event.Type == "tts.audio_chunk" || event.Type == "tts.session_completed" {
			seen[event.Type] = true
			if event.TraceID != traceID {
				t.Fatalf("%s trace_id = %q, want parent %q", event.Type, event.TraceID, traceID)
			}
		}
		if event.Type != "tts.session_completed" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Content), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["public_playback_ref"] != taskID.String() {
			t.Fatalf("completion public_playback_ref = %#v, want %s; payload=%#v", payload["public_playback_ref"], taskID, payload)
		}
		if event.TraceID.Validate() != nil || string(event.TraceID) == event.TaskID.String() || event.TaskID != taskID {
			t.Fatalf("completion correlation = task:%q trace:%q, want canonical distinct identities", event.TaskID, event.TraceID)
		}
		if !seen["metrics.latency"] || !seen["tts.audio_chunk"] || !seen["tts.session_completed"] {
			t.Fatalf("missing TTS callback events: %#v", seen)
		}
		return
	}
	t.Fatal("tts.session_completed event was not emitted")
}

func TestTTSClientBridgePublishesIdleChatTopicWithCurrentPublicRoute(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()
	t.Cleanup(resetTTSPublicSessionStateForTest)
	t.Cleanup(clearAllIdleChatTTSPending)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"gateway_service":"tts-gateway","audio_path":"/audio/topic.wav"}`)
	}))
	t.Cleanup(srv.Close)

	var chunks []orchestrator.OrchestratorEvent
	outputDir := t.TempDir()
	audioOwner, _, _ := newTTSClientBridgeTestAudioOwner(t, outputDir, srv.URL, "")
	readyCount := 0
	bridge := buildTTSClientBridge(&config.Config{
		TTS: config.TTSConfig{
			Enabled:        true,
			GatewayBaseURL: srv.URL,
			VoiceID:        "mio",
			TimeoutMS:      15000,
			OutputDir:      outputDir,
		},
	}, audioOwner, func(ev orchestrator.OrchestratorEvent) {
		chunks = append(chunks, ev)
	}, func(_, _, _ string) { readyCount++ }, nil)
	if bridge == nil {
		t.Fatal("expected bridge")
	}

	registerTTSPublicSessionForPlayback("idle-topic-tts", "idle-topic", "idle-topic:0000", "idle-topic:topic", 0, "test-runtime", 1)
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{
		SessionID:         "idle-topic-tts",
		PublicPlaybackRef: "idle-topic:0000",
		CharacterID:       "user",
		VoiceID:           "mio",
	}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	displayBridge, ok := bridge.(orchestrator.TTSDisplayBridge)
	if !ok {
		t.Fatalf("expected display bridge, got %T", bridge)
	}
	topicSpeech := "きょうのおだい、車輪の軌跡と乗り手の皮膚感覚。"
	if err := displayBridge.PushTextWithDisplay(context.Background(), "idle-topic-tts", topicSpeech, "今日のお題：車輪の軌跡と乗り手の皮膚感覚", nil); err != nil {
		t.Fatalf("IdleChat topic with a current public route: %v", err)
	}

	audioChunks := ttsAudioChunkEvents(chunks)
	if len(audioChunks) != 1 {
		t.Fatalf("IdleChat topic with a current public route published %d chunks, want 1: %#v", len(audioChunks), chunks)
	}
	var payload moduletts.AudioChunkEventPayload
	if err := json.Unmarshal([]byte(audioChunks[0].Content), &payload); err != nil {
		t.Fatalf("decode topic payload: %v", err)
	}
	if payload.SpeechText != "😊"+topicSpeech || payload.Text != "😊"+topicSpeech {
		t.Fatalf("topic speech must preserve the bright prefix and full topic text: %#v", payload)
	}
	registration, found := audioOwner.lookupURLRegistration(srv.URL + "/audio/topic.wav")
	if !found || registration.authorityKind != ttsAudioIdleChatPublicAuthority || registration.publicProof.PublicSessionID != "idle-topic" || registration.publicProof.RuntimeEpoch != "test-runtime" || registration.publicProof.PlaybackGeneration != 1 {
		t.Fatalf("topic registration=%+v found=%v, want current public route proof", registration, found)
	}
	if readyCount != 1 {
		t.Fatalf("IdleChat topic notified chunk-ready %d times, want 1", readyCount)
	}
}

func TestBuildTTSClientBridgeStampsFrozenPublicAndPrivateSessionBindings(t *testing.T) {
	for _, ownerID := range []string{"", "ren"} {
		name := "public"
		wantVisibility := string(domainconversation.SessionAudiencePublic)
		if ownerID != "" {
			name = "private"
			wantVisibility = string(domainconversation.SessionAudiencePrivate)
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"gateway_service":"tts-gateway","audio_path":"/audio/frozen-binding.wav"}`)
			}))
			defer server.Close()
			outputDir := t.TempDir()
			audioOwner, sessionID, binding := newTTSClientBridgeTestAudioOwner(t, outputDir, server.URL, ownerID)
			var events []orchestrator.OrchestratorEvent
			bridge := buildTTSClientBridge(&config.Config{TTS: config.TTSConfig{
				Enabled: true, GatewayBaseURL: server.URL, VoiceID: "mio", TimeoutMS: 10000, OutputDir: outputDir,
			}}, audioOwner, func(event orchestrator.OrchestratorEvent) {
				events = append(events, event)
			}, nil, nil)
			if bridge == nil {
				t.Fatal("TTS bridge unavailable")
			}
			if err := bridge.StartSession(domainconversation.WithSessionBinding(context.Background(), binding), orchestrator.TTSSessionStart{
				SessionID: "tts-frozen-binding", CanonicalSessionID: string(sessionID), CharacterID: "mio", VoiceID: "mio",
			}); err != nil {
				t.Fatal(err)
			}
			if err := bridge.PushText(context.Background(), "tts-frozen-binding", "binding stamp", nil); err != nil {
				t.Fatalf("PushText(): %v", err)
			}
			address := binding.ChannelAddress()
			for _, event := range events {
				if event.Type != "metrics.latency" && event.Type != "tts.audio_chunk" {
					continue
				}
				if event.SessionID != sessionID || event.PublicationIncarnation != binding.PublicationIncarnation() ||
					event.Channel != address.ChannelType() || event.ChannelAddress != address.ExternalConversationID() ||
					event.Visibility != wantVisibility {
					t.Errorf("%s event binding = session:%q incarnation:%q route:%s/%s visibility:%q", event.Type, event.SessionID, event.PublicationIncarnation, event.Channel, event.ChannelAddress, event.Visibility)
				}
			}
			if got := ttsAudioChunkEvents(events); len(got) != 1 {
				t.Fatalf("audio chunk events=%d, want one", len(got))
			}
		})
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

func newTTSClientBridgeTestAudioOwner(t *testing.T, outputDir, gatewayURL, ownerID string) (*ttsAudioAccessOwner, modulecore.SessionID, domainconversation.SessionBinding) {
	t.Helper()
	sessionID, canonicalSession := ttsAudioTestSession(t, ownerID)
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(sessionID): canonicalSession,
	}}
	binding := ttsAudioTestSessionBinding(t, sessionID, canonicalSession, 1)
	return newTTSAudioAccessOwner(outputDir, gatewayURL, "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository)), sessionID, binding
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
	if got := resolveTTSPublicPlaybackRef("idle-ack-tts"); got != "" {
		t.Fatalf("public session route should be cleared after playback ack, got response %q", got)
	}
}

func TestClearTTSPublicSequenceStateIfNoRoutes(t *testing.T) {
	resetTTSPublicSessionStateForTest()

	registerTTSPublicSession("idle-seq-tts", "idle-seq", "idle-seq:0000")
	if got := nextTTSPublicPlaybackRef("idle-seq"); got != "idle-seq:0000" {
		t.Fatalf("first response = %q", got)
	}
	if session, chunk := resolveTTSPublicChunk("idle-seq-tts", 0); session != "idle-seq" || chunk != 0 {
		t.Fatalf("first chunk = %s/%d", session, chunk)
	}

	clearTTSPublicSequenceStateIfNoRoutes()
	if snapshot := snapshotTTSPublicSessions(); snapshot.NextChunkSessionCount != 1 || snapshot.NextPublicPlaybackSessionCount != 1 {
		t.Fatalf("active route must keep sequence state, got %+v", snapshot)
	}

	clearTTSPublicSession("idle-seq-tts")
	clearTTSPublicSequenceStateIfNoRoutes()
	if snapshot := snapshotTTSPublicSessions(); snapshot.NextChunkSessionCount != 0 || snapshot.NextPublicPlaybackSessionCount != 0 {
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
			Enabled:        true,
			GatewayBaseURL: "http://127.0.0.1:8765",
			VoiceID:        "female_01",
			TimeoutMS:      15000,
		},
	}
	if got := buildTTSClientBridge(cfg, nil, nil, nil, nil); got == nil {
		t.Fatal("expected non-nil bridge when tts is enabled")
	}
}

func TestBuildTTSClientBridge_UsesRenCrowBridge(t *testing.T) {
	cfg := &config.Config{
		TTS: config.TTSConfig{
			Enabled:        true,
			GatewayBaseURL: "http://127.0.0.1:8765",
			VoiceID:        "female_01",
			TimeoutMS:      15000,
		},
	}
	got := buildTTSClientBridge(cfg, nil, nil, nil, nil)
	if got == nil {
		t.Fatal("expected non-nil bridge")
	}
	if _, ok := got.(*ttsinfra.RenCrowTTSBridge); !ok {
		t.Fatalf("expected RenCrowTTSBridge, got %T", got)
	}
}

func TestBuildTTSClientBridge_UsesGatewayOnly(t *testing.T) {
	cfg := &config.Config{
		TTS: config.TTSConfig{
			Enabled:        true,
			OutputDir:      t.TempDir(),
			GatewayBaseURL: "http://127.0.0.1:7870",
			VoiceID:        "mio",
		},
	}
	got := buildTTSClientBridge(cfg, nil, nil, nil, nil)
	if got == nil {
		t.Fatal("expected non-nil bridge")
	}
	if _, ok := got.(*ttsinfra.RenCrowTTSBridge); !ok {
		t.Fatalf("expected RenCrow_TTS Gateway bridge, got %T", got)
	}
}

func TestBuildTTSClientBridge_WithoutPlaybackCommands(t *testing.T) {
	cfg := &config.Config{
		TTS: config.TTSConfig{
			Enabled:        true,
			GatewayBaseURL: "http://127.0.0.1:8765",
			VoiceID:        "female_01",
			TimeoutMS:      15000,
		},
	}

	got := buildTTSClientBridge(cfg, nil, nil, nil, nil)
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
	if got := nextTTSPublicPlaybackRef("idle-1"); got != "idle-1:0000" {
		t.Fatalf("first response id = %q", got)
	}
	if got := nextTTSPublicPlaybackRef("idle-1"); got != "idle-1:0001" {
		t.Fatalf("second response id = %q", got)
	}
}
