package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type queuedAudioFlushWriter struct {
	*httptest.ResponseRecorder
	hub     *viewer.EventHub
	event   orchestrator.OrchestratorEvent
	queued  chan struct{}
	release chan struct{}
	flushes int
}

func (w *queuedAudioFlushWriter) Flush() {
	w.ResponseRecorder.Flush()
	w.flushes++
	if w.flushes != 1 {
		return
	}
	w.hub.OnEvent(w.event)
	close(w.queued)
	<-w.release
}

func TestRegisterSTTAndAudioRoutesUsesSharedTTSAudioAccessOwner(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "registered.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "local-agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(ttsAudioTestOwnerToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionID, canonicalSession := ttsAudioTestSession(t, "ren")
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(sessionID): canonicalSession,
	}}
	visibility := newRuntimeSessionVisibility(repository)
	cfg := &config.Config{
		TTS:           config.TTSConfig{OutputDir: outputDir},
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"},
	}
	audioOwner := newConfiguredTTSAudioAccessOwner(cfg, visibility)
	if err := audioOwner.RegisterPath(ttsAudioTestSessionBinding(t, sessionID, canonicalSession, 1), audioPath); err != nil {
		t.Fatalf("register audio resource: %v", err)
	}
	dependencies := &Dependencies{
		eventHub:            viewer.NewEventHub(1),
		sessionRepository:   repository,
		sessionVisibility:   visibility,
		ttsAudioAccessOwner: audioOwner,
	}
	mux := http.NewServeMux()
	registerSTTAndAudioRoutes(mux, cfg, sttRuntime{}, voiceChatRuntime{}, dependencies)
	unauthenticated := httptest.NewRecorder()
	unauthenticatedRequest := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=registered.wav", nil)
	unauthenticatedRequest.RemoteAddr = "127.0.0.1:40123"
	unauthenticatedRequest.Host = "127.0.0.1:18790"
	mux.ServeHTTP(unauthenticated, unauthenticatedRequest)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("private TTS audio without bearer status=%d body=%q, want 401", unauthenticated.Code, unauthenticated.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=registered.wav", nil)
	request.RemoteAddr = "127.0.0.1:40123"
	request.Host = "127.0.0.1:18790"
	request.Header.Set("Authorization", "Bearer "+ttsAudioTestOwnerToken)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "RIFF" {
		t.Fatalf("registered private TTS audio response=%d body=%q, want 200 RIFF", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("private TTS audio Cache-Control=%q, want private, no-store", response.Header().Get("Cache-Control"))
	}
}

func TestAudioRouterSSERevalidatesQueuedCanonicalEventAfterSameIDRecreation(t *testing.T) {
	sessionID, originalSession := ttsAudioTestSession(t, "")
	binding := ttsAudioTestSessionBinding(t, sessionID, originalSession, 1)
	currentBindingRead := make(chan struct{}, 1)
	repository := ttsAudioTestSessionRepository{
		sessions: map[string]*domainsession.Session{string(sessionID): originalSession},
		onCurrentPublicationRead: func() {
			select {
			case currentBindingRead <- struct{}{}:
			default:
			}
		},
	}
	hub := viewer.NewEventHub(8)
	cfg := &config.Config{TTS: config.TTSConfig{OutputDir: t.TempDir()}}
	dependencies := &Dependencies{
		eventHub: hub, sessionRepository: repository, sessionVisibility: newRuntimeSessionVisibility(repository),
		ttsAudioAccessOwner: newTTSAudioAccessOwner(cfg.TTS.OutputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository)),
	}
	mux := http.NewServeMux()
	registerSTTAndAudioRoutes(mux, cfg, sttRuntime{}, voiceChatRuntime{}, dependencies)
	event := orchestrator.OrchestratorEvent{
		Type:                   "tts.audio_chunk",
		SessionID:              sessionID,
		PublicationIncarnation: binding.PublicationIncarnation(),
		Channel:                binding.ChannelAddress().ChannelType(),
		ChannelAddress:         binding.ChannelAddress().ExternalConversationID(),
		Content:                `{"character_id":"mio","audio_url":"https://old-incarnation.example/audio.wav"}`,
	}
	writer := &queuedAudioFlushWriter{
		ResponseRecorder: httptest.NewRecorder(), hub: hub, event: event,
		queued: make(chan struct{}), release: make(chan struct{}),
	}
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/audio-router/events", nil).WithContext(requestCtx)
	handlerDone := make(chan struct{})
	go func() {
		mux.ServeHTTP(writer, request)
		close(handlerDone)
	}()
	select {
	case <-writer.queued:
	case <-time.After(5 * time.Second):
		t.Fatal("audio event was not queued behind the initial SSE flush")
	}
	recreatedSession, err := domainsession.NewCanonicalSession(sessionID, "2026-10-24", binding.ChannelAddress(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if recreatedSession.PublicationIncarnation() == binding.PublicationIncarnation() {
		t.Fatal("same-ID recreation reused the old publication incarnation")
	}
	repository.sessions[string(sessionID)] = recreatedSession
	close(writer.release)
	select {
	case <-currentBindingRead:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE filter did not resolve the current binding for the queued event")
	}
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("audio-router SSE handler did not stop after request cancellation")
	}
	if strings.Contains(writer.Body.String(), "old-incarnation.example") {
		t.Fatalf("queued old-incarnation audio event leaked to SSE: %s", writer.Body.String())
	}
}
