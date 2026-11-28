package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeMovieGatewayToken = "runtime-movie-gateway-token-0123456789abcdef"

func TestConfigureRuntimeMovieToolsUsesObservedActorAndReadiness(t *testing.T) {
	for _, test := range []struct {
		name          string
		actor         string
		assetStorage  string
		rendering     string
		renderQueue   string
		musicTiming   string
		wantGateway   bool
		wantAsset     bool
		wantRender    bool
		wantMusicTime bool
	}{
		{name: "ready capabilities", actor: "ren", assetStorage: "available", rendering: "available", renderQueue: "available", musicTiming: "available", wantGateway: true, wantAsset: true, wantRender: true, wantMusicTime: true},
		{name: "render queue unavailable", actor: "ren", assetStorage: "available", rendering: "available", renderQueue: "disabled", musicTiming: "disabled", wantGateway: true, wantAsset: true},
		{name: "actor mismatch", actor: "other", assetStorage: "available", rendering: "available", renderQueue: "available", musicTiming: "available"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/v1/movie/capabilities", func(w http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer "+runtimeMovieGatewayToken || request.Header.Get("X-RenCrow-Movie-Expected-Actor") != test.actor {
					t.Errorf("Movie capability probe auth headers = %#v", request.Header)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"actor_id": "ren",
					"capabilities": map[string]any{
						"asset_storage":      test.assetStorage,
						"rendering":          test.rendering,
						"render_queue":       test.renderQueue,
						"music_timing":       test.musicTiming,
						"product_acceptance": false,
						"backend": map[string]any{
							"configured": test.rendering == "available",
							"probed":     test.rendering == "available",
							"available":  test.rendering == "available",
							"capabilities": []any{
								map[string]any{"name": "remotion-render", "status": map[bool]string{true: "available", false: "unavailable"}[test.rendering == "available"]},
							},
						},
					},
				})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			t.Setenv("REN_CROW_MOVIE_TEST_TOKEN", runtimeMovieGatewayToken)
			worker := &tools.ToolRunnerConfig{}
			cfg := config.MovieConfig{Enabled: true, Endpoint: server.URL, ActorID: test.actor, TokenEnv: "REN_CROW_MOVIE_TEST_TOKEN"}
			if err := configureRuntimeMovieTools(context.Background(), cfg, worker); err != nil {
				t.Fatalf("configureRuntimeMovieTools() error = %v", err)
			}
			if (worker.Movie != nil) != test.wantGateway || worker.MovieAssetStorageReady != test.wantAsset || worker.MovieRenderReady != test.wantRender || worker.MovieTimingReady != test.wantMusicTime {
				t.Fatalf("Movie config = gateway:%t asset:%t render:%t timing:%t", worker.Movie != nil, worker.MovieAssetStorageReady, worker.MovieRenderReady, worker.MovieTimingReady)
			}
		})
	}
}

func TestRegisterMovieRenderRouteRequiresCMDActorAndReadiness(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "cmd.token")
	if err := os.WriteFile(tokenPath, []byte(runtimeMovieGatewayToken), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		actor string
		ready bool
		want  int
	}{
		{name: "ready owner", actor: "ren", ready: true, want: http.StatusPartialContent},
		{name: "actor mismatch", actor: "other", ready: true, want: http.StatusNotFound},
		{name: "readiness gate", actor: "ren", ready: false, want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{
				Movie:         config.MovieConfig{Enabled: true, ActorID: "ren"},
				LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"},
			}
			gateway := &runtimeMovieGatewayStub{actorID: test.actor}
			mux := http.NewServeMux()
			registerMovieRenderRoutes(mux, cfg, &Dependencies{movieGateway: gateway, moviePlaybackLinkReady: test.ready})
			request := httptest.NewRequest(http.MethodGet, "/viewer/movie/jobs/job_opaque/download?to=mio", nil)
			request.Header.Set("Authorization", "Bearer "+runtimeMovieGatewayToken)
			request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
			request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
			request.Header.Set("Range", "bytes=0-3")
			request.RemoteAddr = "127.0.0.1:12345"
			request.Host = "127.0.0.1:18790"
			response := httptest.NewRecorder()
			withInteractionProfileGuard(mux).ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("registered Movie route status=%d want=%d", response.Code, test.want)
			}
			if test.want != http.StatusPartialContent && gateway.calls != 0 {
				t.Fatalf("unready or mismatched Movie route called gateway %d times", gateway.calls)
			}
		})
	}
}

func TestCMDChatMovieRenderDownloadPassesGuardOwnerAuthAndRange(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "cmd.token")
	if err := os.WriteFile(tokenPath, []byte(runtimeMovieGatewayToken), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Movie:         config.MovieConfig{Enabled: true, ActorID: "ren"},
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"},
	}
	gateway := &runtimeMovieGatewayStub{actorID: "ren"}
	mux := http.NewServeMux()
	registerMovieRenderRoutes(mux, cfg, &Dependencies{movieGateway: gateway, moviePlaybackLinkReady: true})
	handler := withInteractionProfileGuard(mux)
	for _, test := range []struct {
		name    string
		path    string
		profile string
		rangeV  string
		want    int
	}{
		{name: "full MP4 response", path: "/viewer/movie/jobs/job_opaque/download?to=mio", profile: "cmd-chat", want: http.StatusOK},
		{name: "Range MP4 response", path: "/viewer/movie/jobs/job_opaque/download?to=mio", profile: "cmd-chat", rangeV: "bytes=0-3", want: http.StatusPartialContent},
		{name: "neighbor path denied by profile guard", path: "/viewer/movie/jobs/job_opaque/download/extra?to=mio", profile: "cmd-chat", rangeV: "bytes=0-3", want: http.StatusForbidden},
		{name: "other CMD profile denied by profile guard", path: "/viewer/movie/jobs/job_opaque/download?to=mio", profile: "cmd-diagnostics", want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer "+runtimeMovieGatewayToken)
			request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
			request.Header.Set("X-RenCrow-Interaction-Profile", test.profile)
			if test.rangeV != "" {
				request.Header.Set("Range", test.rangeV)
			}
			request.RemoteAddr = "127.0.0.1:12345"
			request.Host = "127.0.0.1:18790"
			response := httptest.NewRecorder()
			before := gateway.calls
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("CMD Movie download status=%d want=%d body=%q", response.Code, test.want, response.Body.String())
			}
			if test.want == http.StatusOK || test.want == http.StatusPartialContent {
				if response.Body.String() != "mp4!" || response.Header().Get("Content-Type") != "video/mp4" || response.Header().Get("ETag") == "" || response.Header().Get("Accept-Ranges") != "bytes" {
					t.Fatalf("CMD Movie download body/headers = %q / %#v", response.Body.String(), response.Header())
				}
				if test.want == http.StatusPartialContent && response.Header().Get("Content-Range") != "bytes 0-3/4" {
					t.Fatalf("Range Content-Range=%q", response.Header().Get("Content-Range"))
				}
				if gateway.calls != before+1 {
					t.Fatalf("owner-authenticated request reached Movie %d times, want %d", gateway.calls-before, 1)
				}
			} else if gateway.calls != before {
				t.Fatalf("denied request reached Movie %d times", gateway.calls-before)
			}
		})
	}
}

type runtimeMovieGatewayStub struct {
	tools.MovieGateway
	actorID string
	calls   int
}

func (s *runtimeMovieGatewayStub) ActorID() string { return s.actorID }
func (s *runtimeMovieGatewayStub) OpenMovieRender(_ context.Context, _, rangeHeader string) (*http.Response, error) {
	s.calls++
	status := http.StatusOK
	header := http.Header{
		"Content-Type": []string{"video/mp4"},
		"Etag":         []string{`"sha256:` + strings.Repeat("a", 64) + `"`},
	}
	if rangeHeader != "" {
		status = http.StatusPartialContent
		header.Set("Content-Range", "bytes 0-3/4")
	}
	return &http.Response{
		StatusCode: status, Header: header, ContentLength: 4, Body: io.NopCloser(bytes.NewReader([]byte("mp4!"))),
	}, nil
}
