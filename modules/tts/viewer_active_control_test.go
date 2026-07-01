package tts

import (
	"testing"
	"time"
)

func TestViewerActiveControlStoreLastClaimWinsPerKind(t *testing.T) {
	store := NewViewerActiveControlStore(time.Minute)

	first := store.Claim(ViewerActiveKindAudio, "pc-viewer")
	if first.ActiveAudioViewerID != "pc-viewer" {
		t.Fatalf("expected first audio viewer, got %q", first.ActiveAudioViewerID)
	}
	second := store.Claim(ViewerActiveKindAudio, "phone-viewer")
	if second.ActiveAudioViewerID != "phone-viewer" {
		t.Fatalf("expected later audio viewer to win, got %q", second.ActiveAudioViewerID)
	}
	input := store.Claim(ViewerActiveKindInput, "pc-viewer")
	if input.ActiveAudioViewerID != "phone-viewer" || input.ActiveInputViewerID != "pc-viewer" {
		t.Fatalf("audio and input active IDs should be independent, got %#v", input)
	}
}

func TestViewerActiveControlStoreReleaseOnlyClearsMatchingOwner(t *testing.T) {
	store := NewViewerActiveControlStore(time.Minute)

	store.Claim(ViewerActiveKindAudio, "pc-viewer")
	store.Release(ViewerActiveKindAudio, "phone-viewer")
	if got := store.Snapshot().ActiveAudioViewerID; got != "pc-viewer" {
		t.Fatalf("non-owner release should not clear audio owner, got %q", got)
	}
	store.Release(ViewerActiveKindAudio, "pc-viewer")
	if got := store.Snapshot().ActiveAudioViewerID; got != "" {
		t.Fatalf("owner release should clear audio owner, got %q", got)
	}
}

func TestViewerActiveControlStoreHeartbeatKeepsOwnerFresh(t *testing.T) {
	store := NewViewerActiveControlStore(200 * time.Millisecond)

	store.Claim(ViewerActiveKindAudio, "live-viewer")
	time.Sleep(20 * time.Millisecond)
	store.Heartbeat(ViewerActiveKindAudio, "live-viewer")
	time.Sleep(20 * time.Millisecond)
	if got := store.Snapshot().ActiveAudioViewerID; got != "live-viewer" {
		t.Fatalf("heartbeat should keep audio owner, got %q", got)
	}
}

func TestViewerActiveControlStoreStaleOwnerExpires(t *testing.T) {
	store := NewViewerActiveControlStore(time.Millisecond)

	store.Claim(ViewerActiveKindAudio, "stale-viewer")
	time.Sleep(2 * time.Millisecond)
	if got := store.Snapshot().ActiveAudioViewerID; got != "" {
		t.Fatalf("stale audio owner should expire, got %q", got)
	}
}

func TestViewerActiveControlStoreIsActiveAudio(t *testing.T) {
	store := NewViewerActiveControlStore(time.Minute)

	store.Claim(ViewerActiveKindAudio, "pc-viewer")
	if !store.IsActiveAudio("pc-viewer") {
		t.Fatal("owner should be active audio")
	}
	if store.IsActiveAudio("phone-viewer") {
		t.Fatal("non-owner should not be active audio")
	}
}
