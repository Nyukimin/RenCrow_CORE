package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func TestViewerActiveControl_LastClaimWinsPerKind(t *testing.T) {
	resetActiveViewerControlForTest()

	first := activeViewerControl.Claim("audio", "pc-viewer")
	if first.ActiveAudioViewerID != "pc-viewer" {
		t.Fatalf("expected first audio viewer, got %q", first.ActiveAudioViewerID)
	}
	second := activeViewerControl.Claim("audio", "phone-viewer")
	if second.ActiveAudioViewerID != "phone-viewer" {
		t.Fatalf("expected later audio viewer to win, got %q", second.ActiveAudioViewerID)
	}
	input := activeViewerControl.Claim("input", "pc-viewer")
	if input.ActiveAudioViewerID != "phone-viewer" || input.ActiveInputViewerID != "pc-viewer" {
		t.Fatalf("audio and input active IDs should be independent, got %#v", input)
	}
}

func TestViewerActiveControl_ReleaseOnlyClearsMatchingOwner(t *testing.T) {
	resetActiveViewerControlForTest()

	activeViewerControl.Claim("audio", "pc-viewer")
	activeViewerControl.Release("audio", "phone-viewer")
	if got := activeViewerControl.Snapshot().ActiveAudioViewerID; got != "pc-viewer" {
		t.Fatalf("non-owner release should not clear audio owner, got %q", got)
	}
	activeViewerControl.Release("audio", "pc-viewer")
	if got := activeViewerControl.Snapshot().ActiveAudioViewerID; got != "" {
		t.Fatalf("owner release should clear audio owner, got %q", got)
	}
}

func TestViewerActiveControl_StaleOwnerExpires(t *testing.T) {
	oldTTL := viewerActiveOwnerTTL
	viewerActiveOwnerTTL = time.Millisecond
	resetActiveViewerControlForTest()
	defer func() {
		viewerActiveOwnerTTL = oldTTL
		resetActiveViewerControlForTest()
	}()

	activeViewerControl.Claim("audio", "stale-viewer")
	time.Sleep(2 * time.Millisecond)
	if got := activeViewerControl.Snapshot().ActiveAudioViewerID; got != "" {
		t.Fatalf("stale audio owner should expire, got %q", got)
	}
}

func TestViewerActiveControl_HeartbeatKeepsOwnerFresh(t *testing.T) {
	oldTTL := viewerActiveOwnerTTL
	viewerActiveOwnerTTL = 200 * time.Millisecond
	resetActiveViewerControlForTest()
	defer func() {
		viewerActiveOwnerTTL = oldTTL
		resetActiveViewerControlForTest()
	}()

	activeViewerControl.Claim("audio", "live-viewer")
	time.Sleep(20 * time.Millisecond)
	activeViewerControl.Heartbeat("audio", "live-viewer")
	time.Sleep(20 * time.Millisecond)
	if got := activeViewerControl.Snapshot().ActiveAudioViewerID; got != "live-viewer" {
		t.Fatalf("heartbeat should keep audio owner, got %q", got)
	}
}

func TestTTSPlaybackAckOnlyReleasesActiveAudioViewer(t *testing.T) {
	resetActiveViewerControlForTest()
	ch := registerIdleChatTTSPending("idle-active-tts", "response-active-1")
	activeViewerControl.Claim("audio", "pc-viewer")

	reqBody, _ := json.Marshal(ttsPlaybackAckRequest{
		PublicPlaybackRef: "response-active-1",
		SessionID:         "idle-active-tts",
		ViewerClientID:    "phone-viewer",
		Status:            "ended",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/tts/playback-ack", bytes.NewReader(reqBody))
	handleTTSPlaybackAck()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inactive ack should be accepted as an observation, got HTTP %d", rec.Code)
	}
	select {
	case <-ch:
		t.Fatal("inactive viewer ack must not release idlechat TTS pending")
	default:
	}

	// Exercise the Viewer wire contract independently of the Go request tags.
	reqBody = []byte(`{"public_playback_ref":"response-active-1","session_id":"idle-active-tts","viewer_client_id":"pc-viewer","status":"ended"}`)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/viewer/tts/playback-ack", bytes.NewReader(reqBody))
	handleTTSPlaybackAck()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("active ack got HTTP %d", rec.Code)
	}
	var receipt map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["public_playback_ref"] != "response-active-1" || receipt["matched"] != true {
		t.Fatalf("ack must echo the matched public ref: %s", rec.Body.String())
	}
	if _, exists := receipt["response_id"]; exists {
		t.Fatalf("ack must not emit legacy response_id: %s", rec.Body.String())
	}
	select {
	case <-ch:
	default:
		t.Fatal("active viewer ack should release idlechat TTS pending")
	}
}

func TestTTSPlaybackErrorAckDoesNotReleaseWhenNoActiveAudioViewer(t *testing.T) {
	resetActiveViewerControlForTest()
	ch := registerIdleChatTTSPending("idle-error-tts", "response-error-1")

	reqBody, _ := json.Marshal(ttsPlaybackAckRequest{
		PublicPlaybackRef: "response-error-1",
		SessionID:         "idle-error-tts",
		ViewerClientID:    "pc-viewer",
		Status:            "error",
		ErrorCode:         "TTS_AUDIO_DISABLED",
		Error:             "IdleChat audio playback was disabled",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/tts/playback-ack", bytes.NewReader(reqBody))
	handleTTSPlaybackAck()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("error ack got HTTP %d", rec.Code)
	}
	select {
	case <-ch:
		t.Fatal("explicit error ack from non-active viewer must not release idlechat TTS pending")
	default:
	}
	clearAllIdleChatTTSPending()
}

func TestTTSPlaybackAckRejectsLegacyResponseIDWithoutReleasingPending(t *testing.T) {
	resetActiveViewerControlForTest()
	clearAllIdleChatTTSPending()
	t.Cleanup(func() {
		clearAllIdleChatTTSPending()
		resetActiveViewerControlForTest()
	})
	ch := registerIdleChatTTSPending("idle-legacy-tts", "legacy-response-1")
	activeViewerControl.Claim("audio", "pc-viewer")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/tts/playback-ack", bytes.NewBufferString(`{"response_id":"legacy-response-1","session_id":"idle-legacy-tts","viewer_client_id":"pc-viewer","status":"ended"}`))
	handleTTSPlaybackAck()(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("legacy response_id should be rejected, got HTTP %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-ch:
		t.Fatal("legacy response_id must not release pending playback")
	default:
	}
}

func TestViewerActiveClaimHandlerBroadcastsControlEvent(t *testing.T) {
	resetActiveViewerControlForTest()
	var emitted []orchestrator.OrchestratorEvent
	body := bytes.NewBufferString(`{"viewer_client_id":"phone-viewer","kind":"input","reason":"stt_start","action":"claim"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/active-control", body)

	handleViewerActiveClaim(func(ev orchestrator.OrchestratorEvent) {
		emitted = append(emitted, ev)
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("claim got HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if got := activeViewerControl.Snapshot().ActiveInputViewerID; got != "phone-viewer" {
		t.Fatalf("expected active input viewer, got %q", got)
	}
	if len(emitted) != 1 || emitted[0].Type != "viewer.active_control" {
		t.Fatalf("expected viewer.active_control event, got %#v", emitted)
	}
}

func TestViewerActiveClaimHandlerReleasesOwner(t *testing.T) {
	resetActiveViewerControlForTest()
	activeViewerControl.Claim("audio", "pc-viewer")

	body := bytes.NewBufferString(`{"viewer_client_id":"pc-viewer","kind":"audio","reason":"pagehide","action":"release"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/active-control", body)
	handleViewerActiveClaim(nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("release got HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if got := activeViewerControl.Snapshot().ActiveAudioViewerID; got != "" {
		t.Fatalf("release should clear active audio owner, got %q", got)
	}
}
