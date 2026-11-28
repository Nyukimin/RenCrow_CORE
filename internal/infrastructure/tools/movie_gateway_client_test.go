package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const movieGatewayClientToken = "movie-gateway-client-token-0123456789abcdef"

func TestMovieGatewayAssetImportUsesActorBoundMusicProvenance(t *testing.T) {
	audio := []byte("fLaC" + strings.Repeat("x", 32))
	hash := sha256.Sum256(audio)
	shaText := hex.EncodeToString(hash[:])
	asset := MovieAsset{
		ID: "asset_opaque", SHA256: shaText, SizeBytes: int64(len(audio)), MediaType: "audio/flac",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceModule: "music", SourceID: "art_opaque",
	}
	responseBody, err := json.Marshal(asset)
	if err != nil {
		t.Fatal(err)
	}
	var observedPath string
	var observedHeader http.Header
	var observedBody []byte
	client, err := NewMovieGatewayClient(MovieGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18870", ActorID: "ren", BearerToken: movieGatewayClientToken,
		HTTPClient: &http.Client{Transport: movieRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			observedPath = request.URL.Path
			observedHeader = request.Header.Clone()
			observedBody, _ = io.ReadAll(request.Body)
			return movieJSONResponse(http.StatusCreated, responseBody, request), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.CreateMovieAsset(context.Background(), "audio/flac", "music", "art_opaque", shaText, audio, "movie-asset-import-key")
	if err != nil {
		t.Fatalf("CreateMovieAsset() error = %v", err)
	}
	if got.ID != asset.ID || observedPath != "/v1/movie/assets" || !bytes.Equal(observedBody, audio) {
		t.Fatalf("Movie asset receipt=%+v path=%q bodyMatches=%t", got, observedPath, bytes.Equal(observedBody, audio))
	}
	for header, want := range map[string]string{
		"Authorization":                  "Bearer " + movieGatewayClientToken,
		"X-RenCrow-Movie-Expected-Actor": "ren",
		"Content-Type":                   "audio/flac",
		"X-Source-Module":                "music",
		"X-Source-ID":                    "art_opaque",
		"X-Expected-SHA256":              shaText,
		"Idempotency-Key":                "movie-asset-import-key",
	} {
		if got := observedHeader.Get(header); got != want {
			t.Errorf("Movie header %s=%q want %q", header, got, want)
		}
	}
}

func TestMovieGatewayTimingSendsFullMusicAnalysisAndValidatesReceipt(t *testing.T) {
	analysis := successfulMusicAnalysis(48000)
	receipt := MovieTimingReceipt{
		Status: "succeeded", FPS: 24, SourceSampleRate: 48000, SourceDurationFrames: 48000 * 8,
		VideoDurationFrames: 192, BeatFrames: []int64{24}, Captions: []MovieCaption{},
	}
	responseBody, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var observedPath string
	var observedHeader http.Header
	var observedBody []byte
	client, err := NewMovieGatewayClient(MovieGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18870", ActorID: "ren", BearerToken: movieGatewayClientToken,
		HTTPClient: &http.Client{Transport: movieRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			observedPath = request.URL.Path
			observedHeader = request.Header.Clone()
			observedBody, _ = io.ReadAll(request.Body)
			return movieJSONResponse(http.StatusOK, responseBody, request), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.CreateMovieTimings(context.Background(), analysis, 24)
	if err != nil {
		t.Fatalf("CreateMovieTimings() error = %v", err)
	}
	var wire struct {
		Analysis MusicAnalysis `json:"analysis"`
		FPS      int           `json:"fps"`
	}
	if err := json.Unmarshal(observedBody, &wire); err != nil {
		t.Fatal(err)
	}
	if got.VideoDurationFrames != 192 || observedPath != "/v1/movie/timings" || wire.FPS != 24 || wire.Analysis.AnalysisID != analysis.AnalysisID || !bytes.Equal(wire.Analysis.Result, analysis.Result) {
		t.Fatalf("Movie timing request/receipt = path:%q fps:%d analysis:%q resultSame:%v receipt:%+v", observedPath, wire.FPS, wire.Analysis.AnalysisID, bytes.Equal(wire.Analysis.Result, analysis.Result), got)
	}
	if observedHeader.Get("Authorization") != "Bearer "+movieGatewayClientToken || observedHeader.Get("X-RenCrow-Movie-Expected-Actor") != "ren" {
		t.Fatalf("Movie timing auth headers = %#v", observedHeader)
	}
}

func TestMovieGatewayRejectsActorMismatchAndInvalidTimingReceipt(t *testing.T) {
	analysis := successfulMusicAnalysis(48000)
	client, err := NewMovieGatewayClient(MovieGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18870", ActorID: "ren", BearerToken: movieGatewayClientToken,
		HTTPClient: &http.Client{Transport: movieRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := []byte(`{"actor_id":"other","capabilities":{"asset_storage":"available","rendering":"disabled","backend":{"configured":false,"probed":false,"available":false,"capabilities":[]},"render_queue":"disabled","music_timing":"disabled","product_acceptance":false}}`)
			return movieJSONResponse(http.StatusOK, body, request), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ProbeCapabilities(context.Background()); err == nil {
		t.Fatal("Movie capability probe accepted a different actor")
	}

	invalid := MovieTimingReceipt{
		Status: "succeeded", FPS: 24, SourceSampleRate: 22050, SourceDurationFrames: 22050 * 8,
		VideoDurationFrames: 192, BeatFrames: []int64{24}, Captions: []MovieCaption{},
	}
	invalidBody, _ := json.Marshal(invalid)
	client, err = NewMovieGatewayClient(MovieGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18870", ActorID: "ren", BearerToken: movieGatewayClientToken,
		HTTPClient: &http.Client{Transport: movieRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return movieJSONResponse(http.StatusOK, invalidBody, request), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateMovieTimings(context.Background(), analysis, 24); err == nil {
		t.Fatal("Movie timing client accepted a receipt that changed the Music native timeline")
	}
}

type movieRoundTripFunc func(*http.Request) (*http.Response, error)

func (f movieRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func movieJSONResponse(status int, body []byte, request *http.Request) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
		ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: request,
	}
}

func TestMovieSubmitPreservesIdempotentReplayTerminalState(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tests := []struct {
		name string
		job  MovieRenderJob
	}{
		{name: "queued", job: MovieRenderJob{ID: "job_render", State: "queued", CreatedAt: now, UpdatedAt: now}},
		{name: "running", job: MovieRenderJob{ID: "job_render", State: "running", CreatedAt: now, UpdatedAt: now}},
		{name: "succeeded replay", job: MovieRenderJob{ID: "job_render", State: "succeeded", CreatedAt: now, UpdatedAt: now, ArtifactID: "asset_render", SHA256: strings.Repeat("a", 64), SizeBytes: 128}},
		{name: "failed replay", job: MovieRenderJob{ID: "job_render", State: "failed", CreatedAt: now, UpdatedAt: now, FailureCode: "RENDER_FAILED"}},
		{name: "reconciliation replay", job: MovieRenderJob{ID: "job_render", State: "needs_reconciliation", CreatedAt: now, UpdatedAt: now, FailureCode: "RECONCILIATION_REQUIRED"}},
	}
	duration := 24
	plan := MovieRenderPlan{
		SchemaVersion: 1, Width: 320, Height: 240, FPS: 24, DurationFrames: &duration, Title: "Replay",
		Scenes: []MovieScene{{Kind: "three", StartFrame: 0, DurationFrames: &duration}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(test.job)
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewMovieGatewayClient(MovieGatewayClientConfig{
				Endpoint: "http://127.0.0.1:18870", ActorID: "ren", BearerToken: movieGatewayClientToken,
				HTTPClient: &http.Client{Transport: movieRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.Header.Get("Idempotency-Key") != "movie-render-stable-key" {
						t.Errorf("idempotency key=%q", request.Header.Get("Idempotency-Key"))
					}
					return movieJSONResponse(http.StatusAccepted, body, request), nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.SubmitMovieRender(context.Background(), plan, "movie-render-stable-key")
			if err != nil {
				t.Fatalf("SubmitMovieRender() rejected persisted %s replay: %v", test.job.State, err)
			}
			if got != test.job {
				t.Fatalf("SubmitMovieRender() = %+v, want exact persisted job %+v", got, test.job)
			}
		})
	}
}
