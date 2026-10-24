package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
)

func attachViewerBridgeTestPresence(t *testing.T, deps *Dependencies, request *http.Request) {
	t.Helper()
	if deps == nil || request == nil {
		t.Fatal("Viewer send test requires runtime dependencies and a request")
	}
	if deps.surfacePresence == nil {
		deps.surfacePresence = newSurfacePresenceController(nil, time.Minute)
		t.Cleanup(deps.surfacePresence.Close)
	}
	client := strings.TrimSpace(request.Header.Get("X-RenCrow-Client"))
	if client == "" {
		client = "RenCrow_CORE"
		request.Header.Set("X-RenCrow-Client", client)
	}
	profile := strings.ToLower(strings.TrimSpace(request.Header.Get(interactionProfileHeader)))
	if profile == "" {
		switch client {
		case "RenCrow_CMD":
			profile = "cmd-chat"
		case "RenCrow_PORTAL":
			profile = "portal-chat"
		default:
			profile = "core-viewer"
		}
		request.Header.Set(interactionProfileHeader, profile)
	}
	surface, ok := viewerForegroundPresenceSurface(profile)
	if !ok || !surfacePresenceProfileAllows(client, profile, surface) {
		t.Fatalf("test request client/profile %q/%q does not allow a foreground presence surface", client, profile)
	}
	principal, ok := surfacePresenceRequestPrincipal(request)
	if !ok {
		t.Fatal("test request has invalid authenticated actor provenance")
	}

	var body map[string]json.RawMessage
	if request.Body == nil {
		body = make(map[string]json.RawMessage)
	} else if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("decode test Viewer send body: %v", err)
	}
	if body == nil {
		body = make(map[string]json.RawMessage)
	}
	viewerClientID := "viewer-bridge-test"
	if raw := body["viewer_client_id"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &viewerClientID); err != nil {
			t.Fatalf("decode test viewer_client_id: %v", err)
		}
	}
	identity := surfacePresenceIdentity{principal: principal, clientType: client, viewerClientID: viewerClientID}
	registration, err := deps.surfacePresence.RegisterV2(identity)
	if err != nil {
		t.Fatalf("register test Viewer presence: %v", err)
	}
	claimed, err := deps.surfacePresence.UpdateV2(identity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: surface, Action: "claim", UpdateSeq: 1,
	})
	if err != nil {
		t.Fatalf("claim test Viewer presence: %v", err)
	}
	body["viewer_client_id"], err = json.Marshal(viewerClientID)
	if err != nil {
		t.Fatal(err)
	}
	body["presence"], err = json.Marshal(map[string]any{
		"protocol_version": 2, "runtime_epoch": registration.RuntimeEpoch,
		"registration_receipt": registration.RegistrationReceipt, "claim_seq": claimed.CurrentClaimSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request.Body = io.NopCloser(bytes.NewReader(encoded))
	request.ContentLength = int64(len(encoded))
}

func TestPinViewerForegroundPresenceUsesTrustedRequestIdentity(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_PORTAL", viewerClientID: "portal-tab-presence"}
	registration, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := controller.UpdateV2(identity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: identity.viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: "chat", Action: "claim", UpdateSeq: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", nil)
	request.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	request.Header.Set(interactionProfileHeader, "portal-chat")
	lease, err := pinViewerForegroundPresence(&Dependencies{surfacePresence: controller}, request, viewer.ForegroundPinRequest{
		ViewerClientID: identity.viewerClientID,
		Presence: &viewer.ForegroundPresenceReference{
			ProtocolVersion: 2, RuntimeEpoch: registration.RuntimeEpoch,
			RegistrationReceipt: registration.RegistrationReceipt, ClaimSeq: claimed.CurrentClaimSeq,
		},
	})
	if err != nil {
		t.Fatalf("pin valid trusted identity: %v", err)
	}
	controller.mu.Lock()
	pinCount := len(controller.pins)
	controller.mu.Unlock()
	if pinCount != 1 {
		t.Fatalf("foreground pin count=%d, want 1", pinCount)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release pin: %v", err)
	}
	controller.mu.Lock()
	pinCount = len(controller.pins)
	controller.mu.Unlock()
	if pinCount != 0 {
		t.Fatalf("foreground pin count after release=%d, want 0", pinCount)
	}

	request.Header.Set("X-RenCrow-Authenticated-Actor", "tailscale-sha256:"+strings.Repeat("a", 64))
	_, err = pinViewerForegroundPresence(&Dependencies{surfacePresence: controller}, request, viewer.ForegroundPinRequest{
		ViewerClientID: identity.viewerClientID,
		Presence: &viewer.ForegroundPresenceReference{
			ProtocolVersion: 2, RuntimeEpoch: registration.RuntimeEpoch,
			RegistrationReceipt: registration.RegistrationReceipt, ClaimSeq: claimed.CurrentClaimSeq,
		},
	})
	var pinErr *viewer.ForegroundPinError
	if !errors.As(err, &pinErr) || pinErr.StatusCode != http.StatusConflict || pinErr.ErrorCode != "registration_required" {
		t.Fatalf("body reference with different trusted principal error=%#v, want typed registration conflict", err)
	}
}

func TestPinViewerForegroundPresenceFailsClosedForMissingControllerAndProfile(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", nil)
	pinRequest := viewer.ForegroundPinRequest{
		ViewerClientID: "portal-tab-presence",
		Presence: &viewer.ForegroundPresenceReference{
			ProtocolVersion: 2, RuntimeEpoch: "epoch", RegistrationReceipt: "receipt", ClaimSeq: 1,
		},
	}
	_, err := pinViewerForegroundPresence(&Dependencies{}, request, pinRequest)
	var pinErr *viewer.ForegroundPinError
	if !errors.As(err, &pinErr) || pinErr.StatusCode != http.StatusServiceUnavailable || pinErr.ErrorCode != "foreground_not_ready" {
		t.Fatalf("missing controller error=%#v, want typed 503", err)
	}

	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	request.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	request.Header.Set(interactionProfileHeader, "cmd-chat")
	_, err = pinViewerForegroundPresence(&Dependencies{surfacePresence: controller}, request, pinRequest)
	if !errors.As(err, &pinErr) || pinErr.StatusCode != http.StatusForbidden || pinErr.ErrorCode != "profile_surface_mismatch" {
		t.Fatalf("mismatched profile error=%#v, want typed 403", err)
	}
}

func TestPinViewerForegroundPresenceAllowsCoreViewerOtherLease(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	identity := surfacePresenceIdentity{principal: "local", clientType: "RenCrow_CORE", viewerClientID: "core-debug-tab"}
	registration, err := controller.RegisterV2(identity)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := controller.UpdateV2(identity, surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: identity.viewerClientID,
		RuntimeEpoch: registration.RuntimeEpoch, RegistrationReceipt: registration.RegistrationReceipt,
		Surface: "other", Action: "claim", UpdateSeq: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", nil)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CORE")
	request.Header.Set(interactionProfileHeader, "core-viewer")
	lease, err := pinViewerForegroundPresence(&Dependencies{surfacePresence: controller}, request, viewer.ForegroundPinRequest{
		ViewerClientID: identity.viewerClientID,
		Presence: &viewer.ForegroundPresenceReference{
			ProtocolVersion: 2, RuntimeEpoch: registration.RuntimeEpoch,
			RegistrationReceipt: registration.RegistrationReceipt, ClaimSeq: claimed.CurrentClaimSeq,
		},
	})
	if err != nil {
		t.Fatalf("CORE core-viewer/other lease was rejected: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release CORE viewer pin: %v", err)
	}
}

func TestPinViewerForegroundPresenceUsesServerPinForAssistantCore(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{manual: true, active: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", nil)
	request.Header.Set("X-RenCrow-Client", "RenCrow_ASSISTANT")
	request.Header.Set(interactionProfileHeader, "assistant-core")

	lease, err := pinViewerForegroundPresence(&Dependencies{surfacePresence: controller}, request, viewer.ForegroundPinRequest{})
	if err != nil {
		t.Fatalf("trusted assistant-core send without V2 registration was rejected: %v", err)
	}
	if !runtime.IsDisabled() || runtime.IsChatActive() || runtime.IsManualMode() {
		t.Fatal("assistant-core pin returned before foreground quick stop was applied")
	}
	if starts, stops := runtime.counts(); starts != 0 || stops != 1 {
		t.Fatalf("assistant-core quick-stop counts starts=%d stops=%d, want 0/1", starts, stops)
	}
	controller.mu.Lock()
	pinCount := len(controller.pins)
	ready := controller.playbackStopConfirmed && controller.appliedRevision >= controller.stateRevision
	controller.mu.Unlock()
	if pinCount != 1 || !ready {
		t.Fatalf("server pin state pins=%d ready=%t, want one ready transient pin", pinCount, ready)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release assistant-core server pin: %v", err)
	}
	controller.mu.Lock()
	pinCount = len(controller.pins)
	controller.mu.Unlock()
	if pinCount != 0 {
		t.Fatalf("server pin count after release=%d, want 0", pinCount)
	}
}

func TestPinViewerForegroundPresenceRejectsAssistantProfileSpoof(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	for _, tc := range []struct {
		name    string
		client  string
		profile string
	}{
		{name: "CMD client cannot claim assistant profile", client: "RenCrow_CMD", profile: "assistant-core"},
		{name: "assistant client cannot claim portal profile", client: "RenCrow_ASSISTANT", profile: "portal-chat"},
		{name: "assistant client requires profile", client: "RenCrow_ASSISTANT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/viewer/send", nil)
			request.Header.Set("X-RenCrow-Client", tc.client)
			if tc.profile != "" {
				request.Header.Set(interactionProfileHeader, tc.profile)
			}
			lease, err := pinViewerForegroundPresence(&Dependencies{surfacePresence: controller}, request, viewer.ForegroundPinRequest{})
			if lease != nil {
				_ = lease.Release()
			}
			var pinErr *viewer.ForegroundPinError
			if !errors.As(err, &pinErr) || pinErr.StatusCode != http.StatusForbidden {
				t.Fatalf("spoofed client/profile was not denied with 403: lease=%v err=%v", lease, err)
			}
			controller.mu.Lock()
			pinCount := len(controller.pins)
			controller.mu.Unlock()
			if pinCount != 0 {
				t.Fatalf("spoofed profile created %d server pins", pinCount)
			}
		})
	}
}

func TestPinViewerForegroundPresenceRequiresV2ForOtherProfiles(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", nil)
	request.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	request.Header.Set(interactionProfileHeader, "portal-chat")
	_, err := pinViewerForegroundPresence(&Dependencies{surfacePresence: controller}, request, viewer.ForegroundPinRequest{
		ViewerClientID: "portal-tab-no-reference",
	})
	var pinErr *viewer.ForegroundPinError
	if !errors.As(err, &pinErr) || pinErr.StatusCode != http.StatusConflict || pinErr.ErrorCode != "presence_required" {
		t.Fatalf("portal send without V2 reference was not rejected: err=%v", err)
	}
	controller.mu.Lock()
	pinCount := len(controller.pins)
	controller.mu.Unlock()
	if pinCount != 0 {
		t.Fatalf("portal send without V2 reference created %d pins", pinCount)
	}
}

type assistantCorePinProcessingObservation struct {
	request         orchestrator.ProcessMessageRequest
	pinCount        int
	runtimeDisabled bool
	runtimeActive   bool
	runtimeManual   bool
}

type assistantCorePinBlockingProcessor struct {
	controller *surfacePresenceController
	runtime    *fakeSurfacePresenceRuntime
	started    chan assistantCorePinProcessingObservation
	finish     <-chan struct{}
}

func (p *assistantCorePinBlockingProcessor) ProcessMessage(_ context.Context, request orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	p.runtime.mu.Lock()
	observation := assistantCorePinProcessingObservation{
		request:         request,
		runtimeDisabled: p.runtime.disabled,
		runtimeActive:   p.runtime.active,
		runtimeManual:   p.runtime.manual,
	}
	p.runtime.mu.Unlock()
	p.controller.mu.Lock()
	observation.pinCount = len(p.controller.pins)
	p.controller.mu.Unlock()
	p.started <- observation
	<-p.finish
	return orchestrator.ProcessMessageResponse{SessionID: request.SessionID, Response: "assistant complete"}, nil
}

func TestAssistantCoreViewerSendQuickStopsAndHoldsPinUntilProcessReturns(t *testing.T) {
	runtime := &fakeSurfacePresenceRuntime{manual: true, active: true}
	controller := newSurfacePresenceController(runtime, time.Minute)
	t.Cleanup(controller.Close)
	finish := make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	processor := &assistantCorePinBlockingProcessor{
		controller: controller, runtime: runtime,
		started: make(chan assistantCorePinProcessingObservation, 1), finish: finish,
	}
	deps := &Dependencies{surfacePresence: controller, canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	handler := withInteractionProfileGuard(buildViewerBridgeHandlers(
		&config.Config{}, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	).ViewerSendFromOrch(processor))
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(
		`{"message":"assistant request","to":"mio","user_id":"body-spoof","viewer_client_id":"body-client-without-registration"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_ASSISTANT")
	request.Header.Set(interactionProfileHeader, "assistant-core")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("assistant-core send status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case observed := <-processor.started:
		if observed.pinCount != 1 || !observed.runtimeDisabled || observed.runtimeActive || observed.runtimeManual {
			t.Fatalf("processor started before assistant foreground stop was ready: %+v", observed)
		}
		if observed.request.OperationSource != "RenCrow_ASSISTANT" || observed.request.Channel != "viewer" || observed.request.ChatID != "viewer-user" {
			t.Fatalf("assistant send lost trusted source/actor identity: %+v", observed.request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("assistant-core send did not reach ProcessMessage")
	}
	controller.mu.Lock()
	pinCount := len(controller.pins)
	controller.mu.Unlock()
	if pinCount != 1 {
		t.Fatalf("pin count after HTTP acceptance=%d, want 1 until processing completes", pinCount)
	}
	close(finish)
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		controller.mu.Lock()
		pinCount = len(controller.pins)
		controller.mu.Unlock()
		if pinCount == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("assistant foreground pin was not released after ProcessMessage returned")
		case <-ticker.C:
		}
	}
}

func TestAssistantCoreViewerSendHandlerRejectsSpoofedAndMissingProfiles(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	processor := &viewerBridgeCountProcessor{}
	deps := &Dependencies{surfacePresence: controller, canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	handler := withInteractionProfileGuard(buildViewerBridgeHandlers(
		&config.Config{}, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	).ViewerSendFromOrch(processor))
	for _, tc := range []struct {
		name    string
		client  string
		profile string
	}{
		{name: "CMD cannot spoof assistant-core", client: "RenCrow_CMD", profile: "assistant-core"},
		{name: "assistant client requires profile", client: "RenCrow_ASSISTANT"},
		{name: "assistant cannot claim portal profile", client: "RenCrow_ASSISTANT", profile: "portal-chat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"should be rejected","to":"mio"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-RenCrow-Client", tc.client)
			if tc.profile != "" {
				request.Header.Set(interactionProfileHeader, tc.profile)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("spoofed/missing profile status=%d body=%s, want 403", response.Code, response.Body.String())
			}
			controller.mu.Lock()
			pinCount := len(controller.pins)
			controller.mu.Unlock()
			if pinCount != 0 || processor.calls != 0 {
				t.Fatalf("denied request caused work: pins=%d processor calls=%d", pinCount, processor.calls)
			}
		})
	}
}

func TestAssistantCoreCannotRegisterSurfacePresence(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	handler := withInteractionProfileGuard((&Dependencies{surfacePresence: controller}).handleSurfacePresence())
	request := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", strings.NewReader(
		`{"protocol_version":2,"viewer_client_id":"assistant-tab","surface":"other","action":"register"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_ASSISTANT")
	request.Header.Set(interactionProfileHeader, "assistant-core")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("assistant-core surface registration status=%d body=%s, want 403", response.Code, response.Body.String())
	}
	controller.mu.Lock()
	registrations, pins := len(controller.registrations), len(controller.pins)
	controller.mu.Unlock()
	if registrations != 0 || pins != 0 {
		t.Fatalf("assistant-core surface registration changed state: registrations=%d pins=%d", registrations, pins)
	}
}

func TestViewerSendRuntimeReturnsTyped503WhenPresenceControllerIsMissing(t *testing.T) {
	deps := &Dependencies{}
	handler := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir())).ViewerSendFromOrch(&viewerBridgeCountProcessor{})
	body, err := json.Marshal(map[string]any{
		"message": "hello", "viewer_client_id": "core-debug-tab",
		"presence": map[string]any{
			"protocol_version": 2, "runtime_epoch": "epoch-1", "registration_receipt": "receipt-1", "claim_seq": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_CORE")
	request.Header.Set(interactionProfileHeader, "core-viewer")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result struct {
		ErrorCode string `json:"error_code"`
		Protocol  int    `json:"protocol_version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || result.ErrorCode != "foreground_not_ready" || result.Protocol != 2 {
		t.Fatalf("missing controller response status=%d result=%+v body=%s", response.Code, result, response.Body.String())
	}
}
