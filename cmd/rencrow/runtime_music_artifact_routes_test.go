package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeMusicArtifactRouteToken = "runtime-music-artifact-route-token-0123456789abcdef"

type runtimeMusicArtifactGatewayStub struct {
	actorID     string
	calls       int
	rangeID     string
	rangeHeader string
}

func (s *runtimeMusicArtifactGatewayStub) ActorID() string { return s.actorID }
func (s *runtimeMusicArtifactGatewayStub) OpenMusicArtifact(_ context.Context, artifactID string, rangeHeader string) (*http.Response, error) {
	s.calls++
	s.rangeID = artifactID
	s.rangeHeader = rangeHeader
	return &http.Response{
		StatusCode: http.StatusPartialContent,
		Header: http.Header{
			"Content-Type":  []string{"audio/flac"},
			"Content-Range": []string{"bytes 0-3/100"},
		},
		ContentLength: 4,
		Body:          io.NopCloser(bytes.NewReader([]byte("flac"))),
	}, nil
}

func TestRegisterMusicArtifactRouteRequiresReadyCapabilityAndCMDOwner(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "cmd.token")
	if err := os.WriteFile(tokenPath, []byte(runtimeMusicArtifactRouteToken), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Music:         config.MusicConfig{Enabled: true, ActorID: "ren"},
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"},
	}
	gateway := &runtimeMusicArtifactGatewayStub{actorID: "ren"}
	deps := &Dependencies{musicArtifactPlayback: gateway, musicPlaybackLinkReady: true}

	mux := http.NewServeMux()
	registerMusicArtifactRoutes(mux, cfg, deps)
	request := httptest.NewRequest(http.MethodGet, "/viewer/music/artifacts/art_opaque/download?to=mio", nil)
	request.Header.Set("Authorization", "Bearer "+runtimeMusicArtifactRouteToken)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	request.Header.Set("Range", "bytes=0-3")
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "flac" {
		t.Fatalf("registered Music artifact route = status %d body=%q", response.Code, response.Body.String())
	}
	if gateway.calls != 1 || gateway.rangeID != "art_opaque" || gateway.rangeHeader != "bytes=0-3" {
		t.Fatalf("registered route gateway input calls=%d id=%q Range=%q", gateway.calls, gateway.rangeID, gateway.rangeHeader)
	}

	publicRequest := httptest.NewRequest(http.MethodGet, "/viewer/music/artifacts/art_opaque/download?to=mio", nil)
	publicRequest.RemoteAddr = "127.0.0.1:12345"
	publicRequest.Host = "127.0.0.1:18790"
	publicResponse := httptest.NewRecorder()
	mux.ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusNotFound || gateway.calls != 1 {
		t.Fatalf("anonymous route read status=%d gatewayCalls=%d; want hidden resource and no upstream request", publicResponse.Code, gateway.calls)
	}
}

func TestRegisterMusicArtifactRouteOmitsRouteWithoutActorMatchOrGate(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "cmd.token")
	if err := os.WriteFile(tokenPath, []byte(runtimeMusicArtifactRouteToken), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		actor string
		ready bool
	}{
		{name: "actor mismatch", actor: "other", ready: true},
		{name: "capability unavailable", actor: "ren", ready: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Music:         config.MusicConfig{Enabled: true, ActorID: "ren"},
				LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"},
			}
			gateway := &runtimeMusicArtifactGatewayStub{actorID: tt.actor}
			mux := http.NewServeMux()
			registerMusicArtifactRoutes(mux, cfg, &Dependencies{musicArtifactPlayback: gateway, musicPlaybackLinkReady: tt.ready})
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/viewer/music/artifacts/art_opaque/download?to=mio", nil))
			if response.Code != http.StatusNotFound || gateway.calls != 0 {
				t.Fatalf("unready/mismatched route status=%d gateway calls=%d", response.Code, gateway.calls)
			}
		})
	}
}

var _ tools.MusicArtifactPlaybackGateway = (*runtimeMusicArtifactGatewayStub)(nil)
