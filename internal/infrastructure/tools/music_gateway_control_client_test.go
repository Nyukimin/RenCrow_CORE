package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMusicGatewayControlClientUsesTypedActorBoundRoutes(t *testing.T) {
	var updateBody map[string]any
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+musicWorkflowClientTestToken || r.Header.Get("X-RenCrow-Music-Expected-Actor") != "ren" {
			t.Errorf("request omitted configured bearer or fixed actor assertion: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/music/projects/prj_track_01/control":
			_ = json.NewEncoder(w).Encode(MusicControlState{
				ProjectID: "prj_track_01", Revision: 4, Mode: "DIRECT", ModeSource: "project",
				Locks:           []MusicLock{{ID: "lock_01", Type: "audio_exact", Stage: "final_mix", ReferenceVersionID: "art_master_02", Properties: []string{"audio_sha256"}}},
				HardConstraints: []MusicConstraint{{Property: "write_scope", Value: "frames:0-480000"}},
				UpdatedAt:       time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/music/projects/prj_track_01/control":
			if got := r.Header.Get("Idempotency-Key"); got != "rencrow-core-music-control-key" {
				t.Errorf("control Idempotency-Key = %q", got)
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("control Content-Type = %q", r.Header.Get("Content-Type"))
			}
			if err := json.NewDecoder(r.Body).Decode(&updateBody); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(MusicControlState{
				ProjectID: "prj_track_01", Revision: 5, Mode: "DIRECT", ModeSource: "explicit",
				Locks: []MusicLock{}, HardConstraints: []MusicConstraint{},
				AdoptedVersionID: musicControlTestString("art_master_02"), UpdatedAt: time.Date(2026, 11, 8, 10, 1, 0, 0, time.UTC),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/music/projects/prj_track_01/requests":
			if got := r.Header.Get("Idempotency-Key"); got != "rencrow-core-music-request-key" {
				t.Errorf("request Idempotency-Key = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(MusicControlRequest{
				MusicControlRequestInput: MusicControlRequestInput{
					RawText: "  make a song  ", ExplicitMode: musicControlTestString("DIRECT"), Layers: []string{"L1"}, Operation: "generation",
					SoftPreferences: []MusicConstraint{{Property: "tempo", Value: "about 110 BPM"}},
				},
				RequestID: "req_music_01", ProjectID: "prj_track_01", RequestRevision: 1, ControlRevision: 5,
				Mode: "DIRECT", ModeSource: "explicit", EffectiveLocks: []MusicLock{}, EffectiveHardConstraints: []MusicConstraint{},
				CreatedAt: time.Date(2026, 11, 8, 10, 2, 0, 0, time.UTC),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/music/projects/prj_track_01/requests/req_music_01":
			_ = json.NewEncoder(w).Encode(MusicControlRequest{
				MusicControlRequestInput: MusicControlRequestInput{RawText: "  make a song  ", Layers: []string{"L1"}, Operation: "generation"},
				RequestID:                "req_music_01", ProjectID: "prj_track_01", RequestRevision: 1, ControlRevision: 5,
				Mode: "DIRECT", ModeSource: "explicit", EffectiveLocks: []MusicLock{}, EffectiveHardConstraints: []MusicConstraint{},
				CreatedAt: time.Date(2026, 11, 8, 10, 2, 0, 0, time.UTC),
			})
		default:
			t.Errorf("unexpected Music control route: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
		Endpoint: server.URL, ActorID: "ren", BearerToken: musicWorkflowClientTestToken,
	})
	if err != nil {
		t.Fatal(err)
	}

	state, err := client.GetMusicControl(context.Background(), "prj_track_01")
	if err != nil || state.Revision != 4 || state.Mode != "DIRECT" || len(state.Locks) != 1 || state.Locks[0].Properties[0] != "audio_sha256" || state.HardConstraints[0].Property != "write_scope" {
		t.Fatalf("GetMusicControl() = %+v, %v", state, err)
	}
	updated, err := client.UpdateMusicControl(context.Background(), "prj_track_01", MusicControlUpdate{
		ExpectedRevision: 4, Mode: musicControlTestString("DIRECT"), SelectVersionID: musicControlTestString("art_master_02"),
	}, "rencrow-core-music-control-key")
	if err != nil || updated.Revision != 5 || updated.AdoptedVersionID == nil || *updated.AdoptedVersionID != "art_master_02" {
		t.Fatalf("UpdateMusicControl() = %+v, %v", updated, err)
	}
	if updateBody["expected_revision"] != float64(4) || updateBody["mode"] != "DIRECT" || updateBody["select_version_id"] != "art_master_02" || len(updateBody) != 3 {
		t.Fatalf("control update JSON = %#v", updateBody)
	}
	created, err := client.CreateMusicControlRequest(context.Background(), "prj_track_01", MusicControlRequestInput{
		RawText: "  make a song  ", ExplicitMode: musicControlTestString("DIRECT"), Layers: []string{"L1"}, Operation: "generation",
		SoftPreferences: []MusicConstraint{{Property: "tempo", Value: "about 110 BPM"}},
	}, "rencrow-core-music-request-key")
	if err != nil || created.RequestID != "req_music_01" || created.ControlRevision != 5 || created.ModeSource != "explicit" {
		t.Fatalf("CreateMusicControlRequest() = %+v, %v", created, err)
	}
	if requestBody["raw_text"] != "  make a song  " || requestBody["operation"] != "generation" || requestBody["explicit_mode"] != "DIRECT" || len(requestBody) != 5 {
		t.Fatalf("request JSON = %#v", requestBody)
	}
	read, err := client.GetMusicControlRequest(context.Background(), "prj_track_01", "req_music_01")
	if err != nil || read.RequestID != "req_music_01" || read.ProjectID != "prj_track_01" {
		t.Fatalf("GetMusicControlRequest() = %+v, %v", read, err)
	}
}

func TestMusicGatewayControlClientPreservesOnlyAllowlistedErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		code string
		want string
	}{
		{code: "MUSIC_NEEDS_CONFIRMATION", want: "MUSIC_NEEDS_CONFIRMATION"},
		{code: "MUSIC_PRESERVATION_CONFLICT", want: "MUSIC_PRESERVATION_CONFLICT"},
		{code: "MUSIC_CONTROL_CONFLICT", want: "MUSIC_CONTROL_CONFLICT"},
		{code: "MUSIC_CONTROL_STALE", want: "MUSIC_CONTROL_STALE"},
		{code: "MUSIC_UNSUPPORTED_OPERATION", want: "MUSIC_UNSUPPORTED_OPERATION"},
		{code: "MUSIC_IDEMPOTENCY_CONFLICT", want: ""},
	} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"` + tc.code + `","message":"private detail must not escape","retryable":false,"request_id":"opaque"}}`))
			}))
			defer server.Close()
			client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
				Endpoint: server.URL, ActorID: "ren", BearerToken: musicWorkflowClientTestToken,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.UpdateMusicControl(context.Background(), "prj_track_01", MusicControlUpdate{
				ExpectedRevision: 1, Mode: musicControlTestString("DIRECT"),
			}, "rencrow-core-music-control-key")
			statusErr, ok := err.(*MusicGatewayStatusError)
			if !ok || statusErr.Code != tc.want || strings.Contains(err.Error(), "private detail") || strings.Contains(err.Error(), "opaque") {
				t.Fatalf("status error = %#v (%v), want allowlisted code %q without body text", err, err, tc.want)
			}
		})
	}
}

func TestValidateMusicControlStateAllowsMissingTimestampOnlyForInitialDefault(t *testing.T) {
	initial := MusicControlState{ProjectID: "prj_track_01", Revision: 0, Mode: "AUTO", ModeSource: "default"}
	if err := validateMusicControlState(initial, initial.ProjectID); err != nil {
		t.Fatalf("initial default control state with zero updated_at rejected: %v", err)
	}

	for _, state := range []MusicControlState{
		{ProjectID: "prj_track_01", Revision: 0, Mode: "AUTO", ModeSource: "project"},
		{ProjectID: "prj_track_01", Revision: 1, Mode: "AUTO", ModeSource: "default"},
	} {
		if err := validateMusicControlState(state, state.ProjectID); err == nil {
			t.Errorf("control state without updated_at was accepted outside initial default: %+v", state)
		}
	}
}

func musicControlTestString(value string) *string { return &value }
