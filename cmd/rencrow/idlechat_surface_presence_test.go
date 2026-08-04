package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSurfaceIdleChatRuntime struct {
	mu       sync.Mutex
	manual   bool
	active   bool
	disabled bool
	starts   int
	stops    int
	startErr error
}

func (f *fakeSurfaceIdleChatRuntime) StartManualMode() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.starts++
	f.manual = true
	f.disabled = false
	return nil
}

func (f *fakeSurfaceIdleChatRuntime) StopManualMode() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.manual = false
	f.active = false
	f.disabled = true
}

func (f *fakeSurfaceIdleChatRuntime) IsManualMode() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.manual
}

func (f *fakeSurfaceIdleChatRuntime) IsChatActive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fakeSurfaceIdleChatRuntime) IsDisabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disabled
}

func (f *fakeSurfaceIdleChatRuntime) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func TestSurfacePresenceArbitratesChatOverIdleChatAcrossClients(t *testing.T) {
	runtime := &fakeSurfaceIdleChatRuntime{disabled: true}
	var resets atomic.Int32
	controller := newIdleChatSurfacePresenceController(runtime, time.Minute, func() { resets.Add(1) })
	t.Cleanup(controller.Close)

	idleSnapshot, err := controller.Update("idle-tab", "idlechat", "claim")
	if err != nil {
		t.Fatal(err)
	}
	if idleSnapshot.EffectiveMode != "idlechat" || !idleSnapshot.IdleChatActive || idleSnapshot.IdleChatPresenceCount != 1 {
		t.Fatalf("idle snapshot = %+v", idleSnapshot)
	}

	chatSnapshot, err := controller.Update("chat-tab", "chat", "claim")
	if err != nil {
		t.Fatal(err)
	}
	if chatSnapshot.EffectiveMode != "chat" || chatSnapshot.IdleChatActive || chatSnapshot.ChatPresenceCount != 1 || chatSnapshot.IdleChatPresenceCount != 1 {
		t.Fatalf("chat snapshot = %+v", chatSnapshot)
	}
	if got := resets.Load(); got != 1 {
		t.Fatalf("TTS resets = %d, want 1", got)
	}

	resumed, err := controller.Update("chat-tab", "chat", "release")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.EffectiveMode != "idlechat" || !resumed.IdleChatActive {
		t.Fatalf("resumed snapshot = %+v", resumed)
	}

	stopped, err := controller.Update("idle-tab", "idlechat", "release")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.EffectiveMode != "none" || stopped.IdleChatActive {
		t.Fatalf("stopped snapshot = %+v", stopped)
	}
	starts, stops := runtime.counts()
	if starts != 2 || stops != 2 {
		t.Fatalf("runtime starts=%d stops=%d, want 2/2", starts, stops)
	}
}

func TestSurfacePresenceHeartbeatIsIdempotent(t *testing.T) {
	runtime := &fakeSurfaceIdleChatRuntime{disabled: true}
	controller := newIdleChatSurfacePresenceController(runtime, time.Minute, nil)
	t.Cleanup(controller.Close)

	if _, err := controller.Update("idle-tab", "idlechat", "claim"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Update("idle-tab", "idlechat", "heartbeat"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Update("idle-tab", "idlechat", "heartbeat"); err != nil {
		t.Fatal(err)
	}
	starts, _ := runtime.counts()
	if starts != 1 {
		t.Fatalf("runtime starts=%d, want 1", starts)
	}
}

func TestSurfacePresenceLeaseExpiryStopsPortalOwnedIdleChat(t *testing.T) {
	runtime := &fakeSurfaceIdleChatRuntime{disabled: true}
	controller := newIdleChatSurfacePresenceController(runtime, 20*time.Millisecond, nil)
	t.Cleanup(controller.Close)
	if _, err := controller.Update("idle-tab", "idlechat", "claim"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, stops := runtime.counts()
		if stops > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expired IdleChat surface lease did not stop the PORTAL-owned runtime")
}

func TestSurfacePresenceBlocksExplicitStartWhileChatIsVisible(t *testing.T) {
	runtime := &fakeSurfaceIdleChatRuntime{disabled: true}
	controller := newIdleChatSurfacePresenceController(runtime, time.Minute, nil)
	t.Cleanup(controller.Close)
	if _, err := controller.Update("chat-tab", "chat", "claim"); err != nil {
		t.Fatal(err)
	}
	if err := controller.StartExplicit(); !errors.Is(err, errChatSurfacePresent) {
		t.Fatalf("StartExplicit error = %v", err)
	}
}

func TestHandleSurfacePresenceValidatesProfileAndReturnsAggregateState(t *testing.T) {
	runtime := &fakeSurfaceIdleChatRuntime{disabled: true}
	controller := newIdleChatSurfacePresenceController(runtime, time.Minute, nil)
	t.Cleanup(controller.Close)
	deps := &Dependencies{idleChatSurfacePresence: controller}

	requestBody := []byte(`{"viewer_client_id":"idle-tab","surface":"idlechat","action":"claim"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewReader(requestBody))
	req.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	req.Header.Set(interactionProfileHeader, "portal-idlechat")
	rec := httptest.NewRecorder()
	deps.handleSurfacePresence().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		EffectiveMode  string `json:"effective_mode"`
		IdleChatActive bool   `json:"idlechat_active"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.EffectiveMode != "idlechat" || !response.IdleChatActive {
		t.Fatalf("response = %+v", response)
	}

	mismatch := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", bytes.NewReader(requestBody))
	mismatch.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	mismatch.Header.Set(interactionProfileHeader, "portal-chat")
	mismatchRec := httptest.NewRecorder()
	deps.handleSurfacePresence().ServeHTTP(mismatchRec, mismatch)
	if mismatchRec.Code != http.StatusForbidden {
		t.Fatalf("profile mismatch status=%d, want 403", mismatchRec.Code)
	}
}
