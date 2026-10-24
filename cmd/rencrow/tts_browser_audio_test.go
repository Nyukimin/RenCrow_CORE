package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

const ttsAudioTestOwnerToken = "tts-audio-owner-token-0123456789abcdef"

func TestBuildLocalTTSAudioURL(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF"), 0o644); err != nil {
		t.Fatalf("write audio file: %v", err)
	}

	got := buildLocalTTSAudioURL(outputDir, audioPath)
	want := "/viewer/tts/audio?path=sample.wav"
	if got != want {
		t.Fatalf("unexpected local audio url: got %q want %q", got, want)
	}
}

func TestBuildLocalTTSAudioURL_RejectsOutsideOutputDir(t *testing.T) {
	outputDir := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "sample.wav")
	if err := os.WriteFile(outsidePath, []byte("RIFF"), 0o644); err != nil {
		t.Fatalf("write outside audio file: %v", err)
	}

	if got := buildLocalTTSAudioURL(outputDir, outsidePath); got != "" {
		t.Fatalf("expected empty url for path outside output dir, got %q", got)
	}
}

func TestHandleLocalTTSAudio(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "chunk.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF"), 0o644); err != nil {
		t.Fatalf("write audio file: %v", err)
	}
	sessionID, session := ttsAudioTestSession(t, "")
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(sessionID): session,
	}}))
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, sessionID, session, 1), audioPath); err != nil {
		t.Fatalf("register public audio: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=chunk.wav", nil)
	rec := httptest.NewRecorder()

	handleLocalTTSAudio(outputDir, owner)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "RIFF" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestHandleTTSAudio_ProxiesConfiguredRemoteAudioURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/chunk.wav" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF"))
	}))
	defer upstream.Close()
	sessionID, session := ttsAudioTestSession(t, "")
	owner := newTTSAudioAccessOwner(t.TempDir(), upstream.URL, "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(sessionID): session,
	}}))
	remoteURL := upstream.URL + "/audio/chunk.wav"
	if err := owner.RegisterURL(ttsAudioTestSessionBinding(t, sessionID, session, 1), remoteURL); err != nil {
		t.Fatalf("register public remote audio: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(remoteURL), nil)
	rec := httptest.NewRecorder()

	handleTTSAudio(t.TempDir(), upstream.URL, owner)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "RIFF" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestHandleTTSAudio_RejectsPublicRemoteAudioURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape("https://example.com/audio.wav"), nil)
	rec := httptest.NewRecorder()

	handleTTSAudio(t.TempDir(), "http://127.0.0.1:7870")(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleTTSAudio_RejectsUnconfiguredPrivateRemoteAudioURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unconfigured private upstream must not be called")
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(upstream.URL+"/audio.wav"), nil)
	rec := httptest.NewRecorder()

	handleTTSAudio(t.TempDir(), "http://127.0.0.1:7870")(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleTTSAudioAdmitsSpeechEvalArtifactsAnonymously(t *testing.T) {
	outputDir := t.TempDir()
	setID := strings.Repeat("a", 32)
	filename := strings.Repeat("b", 32) + ".wav"
	setDir := filepath.Join(outputDir, "speech-eval", setID)
	if err := os.MkdirAll(setDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schema_version":"rencrow.speech-eval/1","set_id":"` + setID + `","items":[]}`)
	if err := os.WriteFile(filepath.Join(outputDir, "speech-eval", "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setDir, filename), []byte("RIFF-WAV"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The static speech-eval authority does not depend on a Session repository
	// or a bearer token. Canonical Session and IdleChat checks remain separate.
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), nil)
	handler := handleTTSAudio(outputDir, "", owner)
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "manifest", path: "speech-eval/manifest.json", want: string(manifest)},
		{name: "generated wav", path: "speech-eval/" + setID + "/" + filename, want: "RIFF-WAV"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path="+url.QueryEscape(tc.path), nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusOK || response.Body.String() != tc.want {
				t.Fatalf("anonymous speech-eval request status=%d body=%q; want 200 and %q", response.Code, response.Body.String(), tc.want)
			}
		})
	}
}

func TestHandleTTSAudioRejectsInvalidSpeechEvalPathsAndPrivateAnswer(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "audio")
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setID := strings.Repeat("a", 32)
	setDir := filepath.Join(outputDir, "speech-eval", setID)
	if err := os.MkdirAll(setDir, 0o755); err != nil {
		t.Fatal(err)
	}
	validFilename := strings.Repeat("b", 32) + ".wav"
	if err := os.WriteFile(filepath.Join(setDir, validFilename), []byte("RIFF-WAV"), 0o644); err != nil {
		t.Fatal(err)
	}
	privateDir := filepath.Join(root, "private")
	if err := os.Mkdir(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	answerPath := filepath.Join(privateDir, "speech-eval-answer.json")
	if err := os.WriteFile(answerPath, []byte("PRIVATE-ANSWER"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), nil)
	handler := handleTTSAudio(outputDir, "", owner)
	for _, path := range []string{
		"speech-eval/manifest.json/extra",
		"speech-eval/not-a-set/" + validFilename,
		"speech-eval/" + setID + "/not-a-wav.wav",
		"speech-eval/" + setID + "/" + validFilename + "/extra",
		"speech-eval/" + setID + "/../manifest.json",
		"../private/speech-eval-answer.json",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path="+url.QueryEscape(path), nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "PRIVATE-ANSWER") || strings.Contains(response.Body.String(), "RIFF-WAV") {
				t.Fatalf("invalid speech-eval path %q status=%d body=%q; want hidden 404", path, response.Code, response.Body.String())
			}
		})
	}
	withSessionClaim := httptest.NewRecorder()
	handler.ServeHTTP(withSessionClaim, httptest.NewRequest(http.MethodGet,
		"/viewer/tts/audio?path=speech-eval/"+setID+"/"+validFilename+"&session_id=claimed", nil))
	if withSessionClaim.Code != http.StatusNotFound {
		t.Fatalf("speech-eval query session_id status=%d body=%q; want 404", withSessionClaim.Code, withSessionClaim.Body.String())
	}
}

func TestTTSAudioOwnerRejectsSessionRegistrationUnderSpeechEvalRoot(t *testing.T) {
	outputDir := t.TempDir()
	sessionID, session := ttsAudioTestSession(t, "ren")
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(ttsAudioTestSessionRepository{
		sessions: map[string]*domainsession.Session{string(sessionID): session},
	}))
	privatePath := filepath.Join(outputDir, "speech-eval", strings.Repeat("a", 32), strings.Repeat("b", 32)+".wav")
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, sessionID, session, 1), privatePath); err == nil {
		t.Fatal("canonical private Session resource was registered in the public speech-eval namespace")
	}
}

func TestHandleTTSAudioRejectsSpeechEvalSymlinkEscape(t *testing.T) {
	outputDir := t.TempDir()
	setID := strings.Repeat("a", 32)
	setDir := filepath.Join(outputDir, "speech-eval", setID)
	if err := os.MkdirAll(setDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), strings.Repeat("b", 32)+".wav")
	if err := os.WriteFile(outsidePath, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Base(outsidePath)
	if err := os.Symlink(outsidePath, filepath.Join(setDir, filename)); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), nil)
	response := httptest.NewRecorder()
	handleTTSAudio(outputDir, "", owner).ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=speech-eval/"+setID+"/"+filename, nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "OUTSIDE") {
		t.Fatalf("speech-eval symlink escape status=%d body=%q; want hidden 404", response.Code, response.Body.String())
	}
}

func TestHandleLocalTTSAudio_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "audio")
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(root, "secret.wav")
	if err := os.WriteFile(outsidePath, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionID, session := ttsAudioTestSession(t, "")
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(sessionID): session,
	}}))
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, sessionID, session, 1), outsidePath); err == nil {
		t.Fatal("outside audio path was accepted into the resource registry")
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=../secret.wav", nil)
	rec := httptest.NewRecorder()

	handleLocalTTSAudio(outputDir, owner)(rec, req)

	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("expected hidden traversal 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleTTSAudioRequiresRegisteredCanonicalSession(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "private.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	publicID, publicSession := ttsAudioTestSession(t, "")
	privateID, privateSession := ttsAudioTestSession(t, "ren")
	var remoteReads atomic.Int32
	remoteUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/registered.wav" {
			t.Errorf("remote audio path=%q, want /audio/registered.wav", r.URL.Path)
		}
		remoteReads.Add(1)
		_, _ = w.Write([]byte("REMOTE-OLD"))
	}))
	t.Cleanup(remoteUpstream.Close)
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(publicID):  publicSession,
		string(privateID): privateSession,
	}}
	owner := newTTSAudioAccessOwner(outputDir, remoteUpstream.URL, "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository))
	handler := handleTTSAudio(outputDir, remoteUpstream.URL, owner)

	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "unregistered resource", url: "/viewer/tts/audio?path=private.wav"},
		{name: "ambiguous path and url", url: "/viewer/tts/audio?path=private.wav&url=" + url.QueryEscape("http://127.0.0.1/audio.wav")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%q, want 404", rec.Code, rec.Body.String())
			}
		})
	}

	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, publicID, publicSession, 1), audioPath); err != nil {
		t.Fatalf("register public audio: %v", err)
	}
	registeredRemoteURL := remoteUpstream.URL + "/audio/registered.wav"
	if err := owner.RegisterURL(ttsAudioTestSessionBinding(t, publicID, publicSession, 1), registeredRemoteURL); err != nil {
		t.Fatalf("register public remote audio: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=private.wav", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK || rec.Body.String() != "RIFF" {
		t.Fatalf("public canonical session audio status=%d body=%q", rec.Code, rec.Body.String())
	}
	remoteRequest := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(registeredRemoteURL), nil)
	remoteResponse := httptest.NewRecorder()
	handler.ServeHTTP(remoteResponse, remoteRequest)
	if remoteResponse.Code != http.StatusOK || remoteResponse.Body.String() != "REMOTE-OLD" {
		t.Fatalf("public canonical remote audio status=%d body=%q", remoteResponse.Code, remoteResponse.Body.String())
	}
	recreatedPublicSession, err := domainsession.NewCanonicalSession(publicID, "2026-10-24", publicSession.ChannelAddress(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if recreatedPublicSession.PublicationIncarnation() == publicSession.PublicationIncarnation() {
		t.Fatal("same-ID recreation reused the old publication incarnation")
	}
	repository.sessions[string(publicID)] = recreatedPublicSession
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=private.wav", nil))
	if staleResponse.Code != http.StatusNotFound {
		t.Fatalf("stale canonical Session status=%d body=%q, want 404", staleResponse.Code, staleResponse.Body.String())
	}
	staleRemoteRequest := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(registeredRemoteURL), nil)
	staleRemoteResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleRemoteResponse, staleRemoteRequest)
	if staleRemoteResponse.Code != http.StatusNotFound || remoteReads.Load() != 1 {
		t.Fatalf("stale canonical remote audio status=%d upstream_reads=%d body=%q, want hidden 404 without upstream read", staleRemoteResponse.Code, remoteReads.Load(), staleRemoteResponse.Body.String())
	}

	privatePath := filepath.Join(outputDir, "private-owner.wav")
	if err := os.WriteFile(privatePath, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, privateID, privateSession, 1), privatePath); err != nil {
		t.Fatalf("register private audio: %v", err)
	}
	claimedPublicSession := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=private-owner.wav&session_id="+url.QueryEscape(string(publicID)), nil)
	claimedPublicResponse := httptest.NewRecorder()
	handler.ServeHTTP(claimedPublicResponse, claimedPublicSession)
	if claimedPublicResponse.Code != http.StatusNotFound {
		t.Fatalf("query-supplied public Session status=%d body=%q, want 404", claimedPublicResponse.Code, claimedPublicResponse.Body.String())
	}
	for _, tc := range []struct {
		name       string
		authority  []string
		wantStatus int
	}{
		{name: "missing bearer", wantStatus: http.StatusUnauthorized},
		{name: "wrong bearer", authority: []string{"Bearer wrong"}, wantStatus: http.StatusUnauthorized},
		{name: "duplicate bearer", authority: []string{"Bearer " + ttsAudioTestOwnerToken, "Bearer " + ttsAudioTestOwnerToken}, wantStatus: http.StatusUnauthorized},
		{name: "matching bearer", authority: []string{"Bearer " + ttsAudioTestOwnerToken}, wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=private-owner.wav", nil)
			setTTSAudioTestDirectLoopback(req)
			for _, value := range tc.authority {
				req.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%q, want %d", response.Code, response.Body.String(), tc.wantStatus)
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("private audio Cache-Control=%q, want private, no-store", response.Header().Get("Cache-Control"))
			}
		})
	}

	otherID, otherSession := ttsAudioTestSession(t, "other-user")
	repository.sessions[string(otherID)] = otherSession
	otherPath := filepath.Join(outputDir, "other.wav")
	if err := os.WriteFile(otherPath, []byte("OTHER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, otherID, otherSession, 1), otherPath); err != nil {
		t.Fatal(err)
	}
	wrongOwner := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=other.wav", nil)
	setTTSAudioTestDirectLoopback(wrongOwner)
	wrongOwner.Header.Set("Authorization", "Bearer "+ttsAudioTestOwnerToken)
	wrongOwnerResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongOwnerResponse, wrongOwner)
	if wrongOwnerResponse.Code != http.StatusNotFound {
		t.Fatalf("foreign owner status=%d body=%q, want hidden 404", wrongOwnerResponse.Code, wrongOwnerResponse.Body.String())
	}
}

func TestHandleTTSAudioFailsClosedForUnknownSessionAndNonGET(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "stale.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	unknownID, unknownSession := ttsAudioTestSession(t, "")
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{}}))
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, unknownID, unknownSession, 1), audioPath); err == nil {
		t.Fatal("resource registration without a current canonical Session binding must fail closed")
	}
	handler := handleTTSAudio(outputDir, "", owner)
	request := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=stale.wav", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown canonical Session status=%d body=%q, want 404", response.Code, response.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, "/viewer/tts/audio?path=stale.wav", nil)
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d body=%q, want 405", postResponse.Code, postResponse.Body.String())
	}
}

func TestHandleTTSAudioRejectsRegisteredArtifactAfterCanonicalAddressChanges(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "address-bound.wav")
	if err := os.WriteFile(audioPath, []byte("ADDRESS-BOUND"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionID, session := ttsAudioTestSession(t, "")
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{string(sessionID): session}}
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository))
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, sessionID, session, 1), audioPath); err != nil {
		t.Fatalf("register canonical audio: %v", err)
	}
	handler := handleTTSAudio(outputDir, "", owner)
	current := httptest.NewRecorder()
	handler.ServeHTTP(current, httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=address-bound.wav", nil))
	if current.Code != http.StatusOK || current.Body.String() != "ADDRESS-BOUND" {
		t.Fatalf("current canonical audio status=%d body=%q, want 200 ADDRESS-BOUND", current.Code, current.Body.String())
	}
	changedAddress, err := domainconversation.NewChannelAddress("viewer", "different-conversation")
	if err != nil {
		t.Fatal(err)
	}
	changedSession, err := domainsession.ReconstructCanonicalSessionWithPublicationIncarnation(
		sessionID, "2026-10-24", changedAddress, nil, nil,
		session.CreatedAt(), session.UpdatedAt(), session.PublicationIncarnation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	repository.sessions[string(sessionID)] = changedSession
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=address-bound.wav", nil))
	if stale.Code != http.StatusNotFound || strings.Contains(stale.Body.String(), "ADDRESS-BOUND") {
		t.Fatalf("changed-address canonical audio status=%d body=%q, want hidden 404", stale.Code, stale.Body.String())
	}
}

func TestHandleTTSAudioRechecksCanonicalBindingAfterBlockedUpstreamRead(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseResponse) }) }
	defer release()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseResponse
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("OLD-CANONICAL-AUDIO"))
	}))
	t.Cleanup(upstream.Close)
	outputDir := t.TempDir()
	sessionID, originalSession := ttsAudioTestSession(t, "")
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{string(sessionID): originalSession}}
	binding := ttsAudioTestSessionBinding(t, sessionID, originalSession, 1)
	owner := newTTSAudioAccessOwner(outputDir, upstream.URL, "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository))
	remoteURL := upstream.URL + "/audio.wav"
	if err := owner.RegisterURL(binding, remoteURL); err != nil {
		t.Fatalf("register remote audio: %v", err)
	}
	handler := handleTTSAudio(outputDir, upstream.URL, owner)
	request := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(remoteURL), nil)
	response := httptest.NewRecorder()
	handlerDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(handlerDone)
	}()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request did not start")
	}
	recreatedSession, err := domainsession.NewCanonicalSession(sessionID, "2026-10-24", binding.ChannelAddress(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if recreatedSession.PublicationIncarnation() == binding.PublicationIncarnation() {
		t.Fatal("same-ID recreation reused the old publication incarnation")
	}
	repository.sessions[string(sessionID)] = recreatedSession
	release()
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("audio handler did not finish after upstream release")
	}
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "OLD-CANONICAL-AUDIO") {
		t.Fatalf("revoked canonical audio response status=%d body=%q, want hidden 404 without old bytes", response.Code, response.Body.String())
	}
}

type gatedTTSAudioReader struct {
	started chan struct{}
	release <-chan struct{}
	data    []byte
	done    bool
}

func (r *gatedTTSAudioReader) Read(buffer []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	close(r.started)
	<-r.release
	r.done = true
	return copy(buffer, r.data), nil
}

func TestTTSAudioResponseGateRechecksCanonicalBindingAfterBlockedFileRead(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "blocked-file.wav")
	if err := os.WriteFile(audioPath, []byte("OLD-FILE-AUDIO"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionID, originalSession := ttsAudioTestSession(t, "")
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{string(sessionID): originalSession}}
	binding := ttsAudioTestSessionBinding(t, sessionID, originalSession, 1)
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository))
	if err := owner.RegisterPath(binding, audioPath); err != nil {
		t.Fatalf("register local audio: %v", err)
	}
	registration, found := owner.lookupPathRegistration("blocked-file.wav")
	if !found {
		t.Fatal("registered local audio was not found")
	}
	request := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=blocked-file.wav", nil)
	response := httptest.NewRecorder()
	writer := &ttsAudioGuardedResponseWriter{ResponseWriter: response, request: request, owner: owner, registration: registration}
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRead) }) }
	defer release()
	reader := &gatedTTSAudioReader{started: readStarted, release: releaseRead, data: []byte("OLD-FILE-AUDIO")}
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(writer, reader)
		copyDone <- err
	}()
	select {
	case <-readStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("local audio read did not block")
	}
	recreatedSession, err := domainsession.NewCanonicalSession(sessionID, "2026-10-24", binding.ChannelAddress(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	repository.sessions[string(sessionID)] = recreatedSession
	release()
	select {
	case err := <-copyDone:
		if err == nil {
			t.Fatal("revoked file response was copied without a binding error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local audio copy did not finish after release")
	}
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "OLD-FILE-AUDIO") {
		t.Fatalf("revoked local audio response status=%d body=%q, want hidden 404 without old bytes", response.Code, response.Body.String())
	}
}

func TestHandleTTSAudioRemoteDoesNotForwardBearerOrFollowRedirect(t *testing.T) {
	var redirected atomic.Bool
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("secret"))
	}))
	defer redirectTarget.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("upstream received browser Authorization header %q", got)
		}
		if r.URL.Path == "/redirect.wav" {
			http.Redirect(w, r, redirectTarget.URL+"/target.wav", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF"))
	}))
	defer upstream.Close()

	outputDir := t.TempDir()
	privateID, privateSession := ttsAudioTestSession(t, "ren")
	visibility := newRuntimeSessionVisibility(ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(privateID): privateSession,
	}})
	owner := newTTSAudioAccessOwner(outputDir, upstream.URL, "ren", []byte(ttsAudioTestOwnerToken), visibility)
	audioURL := upstream.URL + "/audio.wav"
	privateBinding := ttsAudioTestSessionBinding(t, privateID, privateSession, 1)
	if err := owner.RegisterURL(privateBinding, audioURL); err != nil {
		t.Fatalf("register private remote audio: %v", err)
	}
	handler := handleTTSAudio(outputDir, upstream.URL, owner)
	request := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(audioURL), nil)
	setTTSAudioTestDirectLoopback(request)
	request.Header.Set("Authorization", "Bearer "+ttsAudioTestOwnerToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "RIFF" {
		t.Fatalf("registered remote audio status=%d body=%q", response.Code, response.Body.String())
	}

	redirectURL := upstream.URL + "/redirect.wav"
	if err := owner.RegisterURL(privateBinding, redirectURL); err != nil {
		t.Fatalf("register redirect URL: %v", err)
	}
	redirectRequest := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(redirectURL), nil)
	setTTSAudioTestDirectLoopback(redirectRequest)
	redirectRequest.Header.Set("Authorization", "Bearer "+ttsAudioTestOwnerToken)
	redirectResponse := httptest.NewRecorder()
	handler.ServeHTTP(redirectResponse, redirectRequest)
	if redirectResponse.Code != http.StatusNotFound || redirected.Load() {
		t.Fatalf("redirect response status=%d followed=%t body=%q; want rejected without follow", redirectResponse.Code, redirected.Load(), redirectResponse.Body.String())
	}
}

func TestTTSAudioAccessRegistryIsBoundedAndCanRetireSessionResources(t *testing.T) {
	outputDir := t.TempDir()
	id, session := ttsAudioTestSession(t, "")
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{string(id): session}}))
	for index := 0; index <= ttsAudioAccessRegistryCapacity; index++ {
		path := fmt.Sprintf("audio-%04d.wav", index)
		if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, id, session, 1), path); err != nil {
			t.Fatalf("register %s: %v", path, err)
		}
	}
	if got := owner.RegisteredResourceCount(); got != ttsAudioAccessRegistryCapacity {
		t.Fatalf("registered resources=%d, want capacity %d", got, ttsAudioAccessRegistryCapacity)
	}
	if owner.UnregisterSession(string(id)) != ttsAudioAccessRegistryCapacity {
		t.Fatal("UnregisterSession did not retire all resources for the canonical Session")
	}
	if got := owner.RegisteredResourceCount(); got != 0 {
		t.Fatalf("registered resources after retirement=%d, want zero", got)
	}
}

func TestTTSAudioAccessRegistryRejectsResourceRebind(t *testing.T) {
	outputDir := t.TempDir()
	firstID, firstSession := ttsAudioTestSession(t, "")
	secondID, secondSession := ttsAudioTestSession(t, "")
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(firstID):  firstSession,
		string(secondID): secondSession,
	}}
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository))
	audioPath := filepath.Join(outputDir, "one-resource.wav")
	firstBinding := ttsAudioTestSessionBinding(t, firstID, firstSession, 1)
	secondBinding := ttsAudioTestSessionBinding(t, secondID, secondSession, 1)
	if err := owner.RegisterPath(firstBinding, audioPath); err != nil {
		t.Fatalf("register first session resource: %v", err)
	}
	if err := owner.RegisterPath(firstBinding, audioPath); err != nil {
		t.Fatalf("identical registration should be idempotent: %v", err)
	}
	if err := owner.RegisterPath(secondBinding, audioPath); err == nil {
		t.Fatal("resource rebind to a different canonical Session was accepted")
	}
	if got := owner.RegisteredResourceCount(); got != 1 {
		t.Fatalf("registered resource count=%d, want 1", got)
	}
}

func TestPrivateTTSAudioRequiresDirectLoopbackEvenWithBearer(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "private-loopback.wav")
	if err := os.WriteFile(audioPath, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionID, session := ttsAudioTestSession(t, "ren")
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{
		string(sessionID): session,
	}}))
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, sessionID, session, 1), audioPath); err != nil {
		t.Fatal(err)
	}
	handler := handleTTSAudio(outputDir, "", owner)
	for _, tc := range []struct {
		name      string
		remote    string
		host      string
		forwarded bool
		want      int
	}{
		{name: "remote with valid bearer", remote: "192.0.2.10:40123", host: "127.0.0.1:18790", want: http.StatusNotFound},
		{name: "proxy-origin header", remote: "127.0.0.1:40123", host: "127.0.0.1:18790", forwarded: true, want: http.StatusNotFound},
		{name: "direct loopback", remote: "127.0.0.1:40123", host: "127.0.0.1:18790", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=private-loopback.wav", nil)
			req.RemoteAddr = tc.remote
			req.Host = tc.host
			req.Header.Set("Authorization", "Bearer "+ttsAudioTestOwnerToken)
			if tc.forwarded {
				req.Header.Set("X-Forwarded-For", "198.51.100.8")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%q, want %d", response.Code, response.Body.String(), tc.want)
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("private response Cache-Control=%q, want private, no-store", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestHandleTTSAudioIdleChatRequiresAndRevalidatesCurrentRouteProof(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invalidate func(*moduletts.PublicSessionStore, string)
	}{
		{name: "timeout", invalidate: func(store *moduletts.PublicSessionStore, publicID string) {
			store.MarkTimedOut(publicID, "", -1, true)
		}},
		{name: "reset", invalidate: func(store *moduletts.PublicSessionStore, _ string) { store.ResetForIdleChat() }},
		{name: "retire", invalidate: func(store *moduletts.PublicSessionStore, _ string) { store.Retire("tts-internal") }},
		{name: "unregistered", invalidate: func(store *moduletts.PublicSessionStore, _ string) { store.Clear("tts-internal") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputDir := t.TempDir()
			audioPath := filepath.Join(outputDir, "idle-public.wav")
			if err := os.WriteFile(audioPath, []byte("IDLECHAT"), 0o600); err != nil {
				t.Fatal(err)
			}
			store := moduletts.NewPublicSessionStore()
			if !store.Register(moduletts.PublicSessionRouteRegistration{
				InternalSessionID: "tts-internal", PublicSessionID: "idle-public-session",
				RuntimeEpoch: "runtime-epoch", PlaybackGeneration: 5,
			}) {
				t.Fatal("IdleChat route registration failed")
			}
			proof, ok := store.CurrentRouteProof("tts-internal")
			if !ok {
				t.Fatal("current IdleChat route proof unavailable")
			}
			owner := newTTSAudioAccessOwnerWithPublicSessions(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), nil, store)
			if err := owner.RegisterPublicPath(proof, audioPath); err != nil {
				t.Fatalf("register public IdleChat resource: %v", err)
			}
			if err := owner.RegisterPublicPath(proof, audioPath); err != nil {
				t.Fatalf("identical public resource registration should be idempotent: %v", err)
			}
			handler := handleTTSAudio(outputDir, "", owner)
			validResponse := httptest.NewRecorder()
			handler.ServeHTTP(validResponse, httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=idle-public.wav", nil))
			if validResponse.Code != http.StatusOK || validResponse.Body.String() != "IDLECHAT" {
				t.Fatalf("current public route status=%d body=%q", validResponse.Code, validResponse.Body.String())
			}

			tc.invalidate(store, "idle-public-session")
			staleResponse := httptest.NewRecorder()
			handler.ServeHTTP(staleResponse, httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=idle-public.wav", nil))
			if staleResponse.Code != http.StatusNotFound || strings.Contains(staleResponse.Body.String(), "IDLECHAT") {
				t.Fatalf("invalidated public route status=%d body=%q, want hidden 404", staleResponse.Code, staleResponse.Body.String())
			}
		})
	}
}

func TestRegisterTTSPublicAudioResourceUsesExplicitCurrentRouteProof(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	t.Cleanup(resetTTSPublicSessionStateForTest)
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "producer-public.wav")
	if err := os.WriteFile(audioPath, []byte("PUBLIC"), 0o600); err != nil {
		t.Fatal(err)
	}
	internalSessionID := string(modulecore.NewSessionID())
	registerTTSPublicSessionForPlayback(internalSessionID, "idle-public-producer", "idle-public-producer:0000", "message-1", 0, "runtime-epoch", 1)
	owner := newTTSAudioAccessOwner(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), nil)
	if err := registerTTSPublicAudioResource(owner, internalSessionID, audioPath, ""); err != nil {
		t.Fatalf("producer resource registration: %v", err)
	}
	handler := handleTTSAudio(outputDir, "", owner)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=producer-public.wav", nil))
	if response.Code != http.StatusOK || response.Body.String() != "PUBLIC" {
		t.Fatalf("current producer route status=%d body=%q", response.Code, response.Body.String())
	}

	retireTTSPublicSession(internalSessionID)
	retired := httptest.NewRecorder()
	handler.ServeHTTP(retired, httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=producer-public.wav", nil))
	if retired.Code != http.StatusNotFound {
		t.Fatalf("retired producer route status=%d body=%q, want 404", retired.Code, retired.Body.String())
	}
	if err := registerTTSPublicAudioResource(owner, "unregistered-session", audioPath, ""); err == nil {
		t.Fatal("producer registration without a current public route was accepted")
	}
}

func TestTTSAudioRegistrationRequiresExactIdleChatProofAndRejectsPrivateToPublicRebind(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "same-resource.wav")
	if err := os.WriteFile(audioPath, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateID, privateSession := ttsAudioTestSession(t, "ren")
	store := moduletts.NewPublicSessionStore()
	if !store.Register(moduletts.PublicSessionRouteRegistration{
		InternalSessionID: "tts-internal", PublicSessionID: "idle-public-session",
		RuntimeEpoch: "runtime-epoch", PlaybackGeneration: 5,
	}) {
		t.Fatal("IdleChat route registration failed")
	}
	proof, ok := store.CurrentRouteProof("tts-internal")
	if !ok {
		t.Fatal("current IdleChat route proof unavailable")
	}
	repository := ttsAudioTestSessionRepository{sessions: map[string]*domainsession.Session{string(privateID): privateSession}}
	owner := newTTSAudioAccessOwnerWithPublicSessions(outputDir, "", "ren", []byte(ttsAudioTestOwnerToken), newRuntimeSessionVisibility(repository), store)
	if err := owner.RegisterPath(ttsAudioTestSessionBinding(t, privateID, privateSession, 1), audioPath); err != nil {
		t.Fatal(err)
	}

	wrongProof := proof
	wrongProof.PublicSessionID = "idle-another-session"
	if err := owner.RegisterPublicPath(wrongProof, audioPath); err == nil {
		t.Fatal("mismatched route proof was accepted")
	}
	if err := owner.RegisterPublicPath(proof, audioPath); err == nil {
		t.Fatal("private resource was rebound to public IdleChat authority")
	}

	handler := handleTTSAudio(outputDir, "", owner)
	request := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=same-resource.wav", nil)
	setTTSAudioTestDirectLoopback(request)
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, request)
	if denied.Code != http.StatusUnauthorized || denied.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("uncredentialed resource status=%d Cache-Control=%q; want private 401", denied.Code, denied.Header().Get("Cache-Control"))
	}

	request = httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=same-resource.wav", nil)
	setTTSAudioTestDirectLoopback(request)
	request.Header.Set("Authorization", "Bearer "+ttsAudioTestOwnerToken)
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, request)
	if allowed.Code != http.StatusOK || allowed.Body.String() != "PRIVATE" {
		t.Fatalf("canonical private resource after rejected rebind status=%d body=%q", allowed.Code, allowed.Body.String())
	}
}

type ttsAudioTestSessionRepository struct {
	sessions                 map[string]*domainsession.Session
	bindingGenerations       map[string]domainconversation.BindingGeneration
	onCurrentPublicationRead func()
}

func (r ttsAudioTestSessionRepository) Save(context.Context, *domainsession.Session) error {
	return nil
}

func (r ttsAudioTestSessionRepository) Load(_ context.Context, id string) (*domainsession.Session, error) {
	session, ok := r.sessions[id]
	if !ok {
		return nil, domainsession.ErrSessionNotFound
	}
	return session, nil
}

func (r ttsAudioTestSessionRepository) CurrentPublicationBinding(ctx context.Context, id string) (domainsession.CanonicalSessionPublicationBinding, error) {
	if r.onCurrentPublicationRead != nil {
		r.onCurrentPublicationRead()
	}
	if ctx == nil {
		return domainsession.CanonicalSessionPublicationBinding{}, fmt.Errorf("publication lookup context is required")
	}
	if err := ctx.Err(); err != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, err
	}
	session, err := r.Load(ctx, id)
	if err != nil {
		return domainsession.CanonicalSessionPublicationBinding{}, err
	}
	return domainsession.NewCanonicalSessionPublicationBinding(session)
}

func (r ttsAudioTestSessionRepository) UseBinding(ctx context.Context, binding domainconversation.SessionBinding, claim func() error) error {
	if ctx == nil {
		return fmt.Errorf("SessionBinding claim context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if claim == nil {
		return fmt.Errorf("SessionBinding claim callback is required")
	}
	if err := binding.ValidateForPublication(); err != nil || binding.BindingGeneration() == domainconversation.BindingGenerationUnavailable {
		return fmt.Errorf("invalid SessionBinding")
	}
	sess, err := r.Load(ctx, string(binding.SessionID()))
	if err != nil {
		return err
	}
	audience := domainconversation.SessionAudiencePublic
	if sess.OwnerID() != "" {
		audience = domainconversation.SessionAudiencePrivate
	}
	generation := r.bindingGenerations[string(binding.SessionID())]
	if generation == domainconversation.BindingGenerationUnavailable {
		generation = 1
	}
	current, err := domainconversation.NewSessionBindingWithPublicationIncarnation(
		binding.SessionID(), sess.ChannelAddress(), audience, sess.OwnerID(), generation, sess.PublicationIncarnation(),
	)
	if err != nil || current != binding {
		return fmt.Errorf("SessionBinding is stale")
	}
	return claim()
}

func (r ttsAudioTestSessionRepository) UsePublicationBinding(ctx context.Context, expected domainsession.CanonicalSessionPublicationBinding, claim func() error) error {
	if ctx == nil {
		return fmt.Errorf("publication claim context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if claim == nil {
		return fmt.Errorf("publication claim callback is required")
	}
	current, err := r.CurrentPublicationBinding(ctx, string(expected.SessionID()))
	if err != nil {
		return err
	}
	if !current.SameIdentity(expected) {
		return fmt.Errorf("publication binding is stale")
	}
	return claim()
}

func (r ttsAudioTestSessionRepository) Exists(_ context.Context, id string) (bool, error) {
	_, ok := r.sessions[id]
	return ok, nil
}

func (ttsAudioTestSessionRepository) Delete(context.Context, string) error { return nil }

var _ orchestrator.SessionRepository = ttsAudioTestSessionRepository{}

func ttsAudioTestSession(t *testing.T, ownerID string) (modulecore.SessionID, *domainsession.Session) {
	t.Helper()
	id := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	var session *domainsession.Session
	if ownerID == "" {
		session, err = domainsession.NewCanonicalSession(id, "2026-10-24", address, now)
	} else {
		session, err = domainsession.NewCanonicalSessionWithOwner(id, ownerID, "2026-10-24", address, now)
	}
	if err != nil {
		t.Fatal(err)
	}
	return id, session
}

func ttsAudioTestSessionBinding(t *testing.T, id modulecore.SessionID, sess *domainsession.Session, generation domainconversation.BindingGeneration) domainconversation.SessionBinding {
	t.Helper()
	audience := domainconversation.SessionAudiencePublic
	if sess != nil && sess.OwnerID() != "" {
		audience = domainconversation.SessionAudiencePrivate
	}
	if sess == nil {
		t.Fatal("canonical Session is required for test binding")
	}
	binding, err := domainconversation.NewSessionBindingWithPublicationIncarnation(
		id, sess.ChannelAddress(), audience, sess.OwnerID(), generation, sess.PublicationIncarnation(),
	)
	if err != nil {
		t.Fatalf("create canonical Session test binding: %v", err)
	}
	return binding
}

func setTTSAudioTestDirectLoopback(request *http.Request) {
	request.RemoteAddr = "127.0.0.1:40123"
	request.Host = "127.0.0.1:18790"
}

func TestHandleTTSAudioAbsentAccessOwnerFailsClosed(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "legacy.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleTTSAudio(outputDir, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=legacy.wav", nil))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "RIFF") {
		t.Fatalf("handler without registered owner status=%d body=%q, want fail-closed 404", rec.Code, rec.Body.String())
	}
}
