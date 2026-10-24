package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPortalOtherProfileMapsOnlyOtherSurface(t *testing.T) {
	if !surfacePresenceProfileAllowsClient("RenCrow_PORTAL", "portal-other") ||
		!surfacePresenceProfileAllows("RenCrow_PORTAL", "portal-other", "other") {
		t.Fatal("portal-other must allow the PORTAL client to claim the other surface")
	}
	for _, surface := range []string{"chat", "idlechat", "games", "cmd"} {
		if surfacePresenceProfileAllows("RenCrow_PORTAL", "portal-other", surface) {
			t.Errorf("portal-other unexpectedly allows %q", surface)
		}
	}
	if surfacePresenceProfileAllowsClient("RenCrow_CMD", "portal-other") {
		t.Fatal("portal-other must be restricted to RenCrow_PORTAL")
	}
}

func TestPortalOtherProfileRequiresV2RegistrationAndClaim(t *testing.T) {
	controller := newSurfacePresenceController(nil, time.Minute)
	t.Cleanup(controller.Close)
	deps := &Dependencies{surfacePresence: controller}
	newRequest := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/viewer/surface-presence", strings.NewReader(body))
		req.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
		req.Header.Set(interactionProfileHeader, "portal-other")
		rec := httptest.NewRecorder()
		deps.handleSurfacePresence().ServeHTTP(rec, req)
		return rec
	}

	register := newRequest(`{"protocol_version":2,"viewer_client_id":"other-tab","action":"register"}`)
	if register.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", register.Code, register.Body.String())
	}
	var registered surfacePresenceV2Response
	if err := json.NewDecoder(register.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}

	claimBody, err := json.Marshal(surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: "other-tab", RuntimeEpoch: registered.RuntimeEpoch,
		RegistrationReceipt: registered.RegistrationReceipt, Surface: "other", Action: "claim", UpdateSeq: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := newRequest(string(claimBody))
	if claim.Code != http.StatusOK {
		t.Fatalf("other claim status=%d body=%s", claim.Code, claim.Body.String())
	}
	var claimed surfacePresenceV2Response
	if err := json.NewDecoder(claim.Body).Decode(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.AcceptedUpdateSeq != 1 || claimed.ForegroundPresenceCount != 1 {
		t.Fatalf("claim response = %+v", claimed)
	}

	legacy := newRequest(`{"viewer_client_id":"legacy-other-tab","surface":"other","action":"claim"}`)
	if legacy.Code != http.StatusBadRequest {
		t.Fatalf("legacy other claim status=%d body=%s, want 400", legacy.Code, legacy.Body.String())
	}

	wrongSurfaceBody, err := json.Marshal(surfacePresenceV2Request{
		ProtocolVersion: 2, ViewerClientID: "other-tab", RuntimeEpoch: registered.RuntimeEpoch,
		RegistrationReceipt: registered.RegistrationReceipt, Surface: "games", Action: "claim", UpdateSeq: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongSurface := newRequest(string(wrongSurfaceBody))
	if wrongSurface.Code != http.StatusForbidden {
		t.Fatalf("games claim status=%d body=%s, want 403", wrongSurface.Code, wrongSurface.Body.String())
	}
}
