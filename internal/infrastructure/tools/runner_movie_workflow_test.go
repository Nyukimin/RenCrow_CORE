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

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type movieWorkflowAnalysisStub struct {
	MusicAnalysisGateway
	actorID  string
	analyses map[string]MusicAnalysis
	getCalls int
}

func (s *movieWorkflowAnalysisStub) ActorID() string { return s.actorID }
func (s *movieWorkflowAnalysisStub) GetMusicAnalysis(_ context.Context, id string) (MusicAnalysis, error) {
	s.getCalls++
	analysis, ok := s.analyses[id]
	if !ok {
		return MusicAnalysis{}, context.Canceled
	}
	return analysis, nil
}

type movieWorkflowArtifactMetadataStub struct {
	MusicArtifactMetadataGateway
	actorID  string
	artifact MusicArtifact
}

func (s movieWorkflowArtifactMetadataStub) ActorID() string { return s.actorID }
func (s movieWorkflowArtifactMetadataStub) GetMusicArtifact(context.Context, string) (MusicArtifact, error) {
	return s.artifact, nil
}

type movieWorkflowGatewayStub struct {
	MovieGateway
	actorID       string
	asset         MovieAsset
	receipts      map[string]MovieTimingReceipt
	requestedOps  []string
	submittedPlan MovieRenderPlan
	submitKey     string
	submitCalls   int
	createdAt     string
	importData    []byte
	importType    string
	importSource  string
	importHash    string
	importKey     string
	importCalls   int
	renderStates  []MovieRenderJob
	renderReads   int
	submitJob     MovieRenderJob
}

func (s *movieWorkflowGatewayStub) ActorID() string { return s.actorID }
func (s *movieWorkflowGatewayStub) GetMovieAssetMetadata(context.Context, string) (MovieAsset, error) {
	return s.asset, nil
}
func (s *movieWorkflowGatewayStub) GetMovieRenderJob(context.Context, string) (MovieRenderJob, error) {
	if len(s.renderStates) == 0 {
		return MovieRenderJob{}, context.Canceled
	}
	index := s.renderReads
	if index >= len(s.renderStates) {
		index = len(s.renderStates) - 1
	}
	s.renderReads++
	return s.renderStates[index], nil
}
func (s *movieWorkflowGatewayStub) CreateMovieTimings(_ context.Context, analysis MusicAnalysis, _ int) (MovieTimingReceipt, error) {
	s.requestedOps = append(s.requestedOps, analysis.Operation)
	receipt, ok := s.receipts[analysis.Operation]
	if !ok {
		return MovieTimingReceipt{}, context.Canceled
	}
	return receipt, nil
}
func (s *movieWorkflowGatewayStub) SubmitMovieRender(_ context.Context, plan MovieRenderPlan, key string) (MovieRenderJob, error) {
	s.submitCalls++
	s.submittedPlan = plan
	s.submitKey = key
	if s.submitJob.ID != "" {
		return s.submitJob, nil
	}
	return MovieRenderJob{ID: "job_render", State: "queued", CreatedAt: s.createdAt, UpdatedAt: s.createdAt}, nil
}
func (s *movieWorkflowGatewayStub) CreateMovieAsset(_ context.Context, mediaType, sourceModule, sourceID, expectedSHA256 string, data []byte, key string) (MovieAsset, error) {
	s.importCalls++
	s.importType, s.importSource, s.importHash, s.importKey = mediaType, sourceModule+":"+sourceID, expectedSHA256, key
	s.importData = append([]byte(nil), data...)
	return s.asset, nil
}

func TestMovieAssetImportFetchesOwnedMusicMetadataAndVerifiedBytes(t *testing.T) {
	audio := []byte("fLaC" + strings.Repeat("x", 32))
	hash := sha256.Sum256(audio)
	shaText := hex.EncodeToString(hash[:])
	artifact := MusicArtifact{
		ID: "art_imported", ProjectID: "project_opaque", Kind: "imported_audio", SHA256: shaText,
		SizeBytes: int64(len(audio)), MediaType: "audio/flac", Format: "flac", DurationFrames: 44100 * 2,
		SampleRate: 44100, Channels: 2, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	metadataBody, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	var musicPaths []string
	client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicAnalysisClientToken,
		HTTPClient: &http.Client{Transport: musicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			musicPaths = append(musicPaths, request.URL.Path)
			if request.Header.Get("Authorization") != "Bearer "+musicAnalysisClientToken || request.Header.Get("X-RenCrow-Music-Expected-Actor") != "ren" {
				t.Errorf("Music artifact request auth headers = %#v", request.Header)
			}
			if request.URL.Path == "/v1/music/artifacts/art_imported" {
				return musicTestJSONResponse(http.StatusOK, metadataBody, request), nil
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"audio/flac"}, "Etag": []string{`"sha256:` + shaText + `"`}},
				ContentLength: int64(len(audio)), Body: io.NopCloser(bytes.NewReader(audio)), Request: request,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	movie := &movieWorkflowGatewayStub{
		actorID: "ren", asset: MovieAsset{
			ID: "asset_imported", SHA256: shaText, SizeBytes: int64(len(audio)), MediaType: "audio/flac",
			CreatedAt: now, SourceModule: "music", SourceID: artifact.ID,
		},
	}
	runner := NewToolRunner(ToolRunnerConfig{
		Movie: movie, MovieAssetStorageReady: true, MusicArtifactMetadata: client, MusicArtifactPlayback: client,
		DisableToolHarness: true,
	})
	ctx := musicAudioImportExecutionContext(t, "movie-asset-request", "movie-asset-task", "ren")
	response, err := runner.ExecuteV2(ctx, "movie.asset.import", map[string]any{"music_artifact_id": artifact.ID})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("movie.asset.import = (%#v, %v)", response, err)
	}
	if len(musicPaths) != 2 || musicPaths[0] != "/v1/music/artifacts/art_imported" || musicPaths[1] != "/v1/music/artifacts/art_imported/download" ||
		movie.importCalls != 1 || movie.importType != "audio/flac" || movie.importSource != "music:art_imported" || movie.importHash != shaText ||
		!bytes.Equal(movie.importData, audio) || movie.importKey == "" || movie.importKey == "movie-asset-request" {
		t.Fatalf("Movie import musicPaths=%v calls=%d type=%q source=%q hash=%q dataMatches=%v key=%q", musicPaths, movie.importCalls, movie.importType, movie.importSource, movie.importHash, bytes.Equal(movie.importData, audio), movie.importKey)
	}
}

func TestMusicAnalysisGetRemainsAvailableWithoutCreateCapabilities(t *testing.T) {
	analysis := successfulMusicAnalysis(48000)
	analysis.AnalysisID = "analysis_recovered"
	reader := &movieWorkflowAnalysisStub{actorID: "ren", analyses: map[string]MusicAnalysis{analysis.AnalysisID: analysis}}
	runner := NewToolRunner(ToolRunnerConfig{MusicAnalysisReader: reader, DisableToolHarness: true})
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if !hasMusicAnalysisToolMetadata(metadata, "music.analysis.get") || hasMusicAnalysisToolMetadata(metadata, "music.analysis.create") {
		t.Fatalf("analysis read/create registration = get:%v create:%v", hasMusicAnalysisToolMetadata(metadata, "music.analysis.get"), hasMusicAnalysisToolMetadata(metadata, "music.analysis.create"))
	}
	ctx := musicAudioImportExecutionContext(t, "analysis-get-request", "analysis-get-task", "ren")
	response, err := runner.ExecuteV2(ctx, "music.analysis.get", map[string]any{"analysis_id": analysis.AnalysisID})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("music.analysis.get = (%#v, %v)", response, err)
	}
	result, ok := response.Result.(MusicAnalysis)
	if !ok || result.AnalysisID != analysis.AnalysisID || reader.getCalls != 1 {
		t.Fatalf("music.analysis.get result=%#v getCalls=%d", response.Result, reader.getCalls)
	}
}

func TestMovieRenderSubmitComposesOnlyMatchingMovieReceipts(t *testing.T) {
	for _, test := range []struct {
		name        string
		plan        map[string]any
		assetSource string
		captionSize int64
		wantError   bool
	}{
		{name: "fills only Movie timing fields", assetSource: "art_source"},
		{name: "rejects conflicting beats", plan: map[string]any{"beat_frames": []any{99}}, assetSource: "art_source", wantError: true},
		{name: "rejects mismatched asset provenance", assetSource: "art_other", wantError: true},
		{name: "rejects disagreeing receipt timelines", assetSource: "art_source", captionSize: 1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			musicBeat, musicCaption, source := movieWorkflowInputs()
			analysis := &movieWorkflowAnalysisStub{actorID: "ren", analyses: map[string]MusicAnalysis{
				musicBeat.AnalysisID: musicBeat, musicCaption.AnalysisID: musicCaption,
			}}
			receipts := map[string]MovieTimingReceipt{
				"beats": {
					Status: "succeeded", FPS: 24, SourceSampleRate: 48000, SourceDurationFrames: 48000 * 8,
					VideoDurationFrames: 192, BeatFrames: []int64{24}, Captions: []MovieCaption{},
				},
				"captions": {
					Status: "succeeded", FPS: 24, SourceSampleRate: 48000, SourceDurationFrames: 48000 * 8,
					VideoDurationFrames: 192 + test.captionSize, BeatFrames: []int64{},
					Captions: []MovieCaption{{StartFrame: 0, EndFrame: 48, Text: "hello"}},
				},
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			movie := &movieWorkflowGatewayStub{
				actorID: "ren", createdAt: now,
				asset:    MovieAsset{ID: "asset_audio", SHA256: source.SHA256, SizeBytes: source.SizeBytes, MediaType: "audio/flac", CreatedAt: now, SourceModule: "music", SourceID: test.assetSource},
				receipts: receipts,
			}
			artifactGateway := movieWorkflowArtifactMetadataStub{actorID: "ren", artifact: source}
			runner := NewToolRunner(ToolRunnerConfig{
				Movie: movie, MovieTimingReady: true, MovieRenderReady: true,
				MusicArtifactMetadata: artifactGateway, MusicAnalysisReader: analysis,
				DisableToolHarness: true,
			})
			plan := map[string]any{
				"schema_version": 1, "width": 320, "height": 240, "fps": 24, "title": "A short film",
				"audio_asset_id": "asset_audio", "scenes": []any{map[string]any{"kind": "three", "start_frame": 0}},
			}
			for key, value := range test.plan {
				plan[key] = value
			}
			ctx := musicAudioImportExecutionContext(t, "movie-request-"+test.name, "movie-task-"+test.name, "ren")
			response, err := runner.ExecuteV2(ctx, "movie.render.submit", map[string]any{
				"plan": plan, "music_analysis_ids": []any{musicBeat.AnalysisID, musicCaption.AnalysisID},
			})
			if err != nil || response == nil {
				t.Fatalf("movie.render.submit = (%#v, %v)", response, err)
			}
			if test.wantError {
				if response.Error == nil || response.Error.Code != tool.ErrValidationFailed || movie.submitCalls != 0 {
					t.Fatalf("invalid render composition = response:%#v submitCalls:%d", response, movie.submitCalls)
				}
				return
			}
			if response.Error != nil || movie.submitCalls != 1 || movie.submittedPlan.DurationFrames == nil || *movie.submittedPlan.DurationFrames != 192 ||
				movie.submittedPlan.Scenes[0].DurationFrames == nil || *movie.submittedPlan.Scenes[0].DurationFrames != 192 ||
				len(movie.submittedPlan.BeatFrames) != 1 || movie.submittedPlan.BeatFrames[0] != 24 ||
				len(movie.submittedPlan.Captions) != 1 || movie.submittedPlan.Captions[0].Text != "hello" ||
				len(movie.requestedOps) != 2 || movie.submitKey == "" || movie.submitKey == "movie-request-"+test.name {
				t.Fatalf("Movie plan composition = error:%#v submitCalls:%d plan:%+v ops:%v key:%q", response.Error, movie.submitCalls, movie.submittedPlan, movie.requestedOps, movie.submitKey)
			}
		})
	}
}

func TestMovieRenderSubmitPreservesTerminalReplayAndSuccessfulDownloadLink(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, test := range []struct {
		name         string
		job          MovieRenderJob
		wantDownload string
	}{
		{name: "succeeded replay", job: MovieRenderJob{ID: "job_replay", State: "succeeded", CreatedAt: now, UpdatedAt: now, ArtifactID: "asset_render", SHA256: strings.Repeat("a", 64), SizeBytes: 1024}, wantDownload: "/viewer/movie/jobs/job_replay/download?to=mio"},
		{name: "failed replay", job: MovieRenderJob{ID: "job_replay", State: "failed", CreatedAt: now, UpdatedAt: now, FailureCode: "RENDER_FAILED"}},
		{name: "reconciliation replay", job: MovieRenderJob{ID: "job_replay", State: "needs_reconciliation", CreatedAt: now, UpdatedAt: now, FailureCode: "RECONCILIATION_REQUIRED"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			movie := &movieWorkflowGatewayStub{actorID: "ren", createdAt: now, submitJob: test.job}
			runner := NewToolRunner(ToolRunnerConfig{Movie: movie, MovieRenderReady: true, MoviePlaybackLinkReady: true, DisableToolHarness: true})
			ctx := musicAudioImportExecutionContext(t, "movie-replay-request-"+test.name, "movie-replay-task-"+test.name, "ren")
			response, err := runner.ExecuteV2(ctx, "movie.render.submit", map[string]any{"plan": map[string]any{
				"schema_version": 1, "width": 320, "height": 240, "fps": 24, "duration_frames": 24,
				"title": "Replay", "scenes": []any{map[string]any{"kind": "three", "start_frame": 0, "duration_frames": 24}},
			}})
			if err != nil || response == nil || response.Error != nil {
				t.Fatalf("movie.render.submit = (%#v, %v), want persisted replay", response, err)
			}
			result, ok := response.Result.(MovieRenderJobResult)
			if !ok || result.Job != test.job || result.DownloadURL != test.wantDownload || movie.submitCalls != 1 {
				t.Fatalf("movie.render.submit lost replay state/result: got=%#v wantJob=%+v wantDownload=%q calls=%d", response.Result, test.job, test.wantDownload, movie.submitCalls)
			}
		})
	}
}

func TestMovieRenderStatusWaitsWithinCallerBudgetAndReturnsAuthenticatedLink(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	movie := &movieWorkflowGatewayStub{
		actorID: "ren",
		renderStates: []MovieRenderJob{
			{ID: "job_render", State: "queued", CreatedAt: now, UpdatedAt: now},
			{ID: "job_render", State: "succeeded", CreatedAt: now, UpdatedAt: now, ArtifactID: "artifact_render", SHA256: strings.Repeat("a", 64), SizeBytes: 1024},
		},
	}
	runner := NewToolRunner(ToolRunnerConfig{Movie: movie, MoviePlaybackLinkReady: true, DisableToolHarness: true})
	ctx := musicAudioImportExecutionContext(t, "movie-status-request", "movie-status-task", "ren")
	response, err := runner.ExecuteV2(ctx, "movie.render.status", map[string]any{"job_id": "job_render", "wait_seconds": 1})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("movie.render.status = (%#v, %v)", response, err)
	}
	result, ok := response.Result.(MovieRenderJobResult)
	if !ok || result.Job.State != "succeeded" || result.DownloadURL != "/viewer/movie/jobs/job_render/download?to=mio" || movie.renderReads != 2 {
		t.Fatalf("bounded Movie status result=%#v reads=%d", response.Result, movie.renderReads)
	}
}

func movieWorkflowInputs() (MusicAnalysis, MusicAnalysis, MusicArtifact) {
	beat := successfulMusicAnalysis(48000)
	beat.AnalysisID = "analysis_beats"
	beat.SourceArtifactID = "art_source"
	caption := successfulMusicAnalysis(48000)
	caption.AnalysisID = "analysis_captions"
	caption.Operation = "captions"
	caption.SourceArtifactID = "art_source"
	var captionDoc MusicAnalysisDocument
	_ = json.Unmarshal(caption.Result, &captionDoc)
	captionDoc.Operation = "captions"
	captionDoc.BPM = nil
	captionDoc.Beats = []MusicAnalysisBeat{}
	captionDoc.Words = []MusicAnalysisWord{{Text: "hello", StartFrame: 0, EndFrame: 48000}}
	captionDoc.Versions.WhisperX = "3.8.6"
	caption.Result, _ = json.Marshal(captionDoc)
	hash := sha256.Sum256(caption.Result)
	caption.ResultSHA256 = hex.EncodeToString(hash[:])
	return beat, caption, MusicArtifact{
		ID: "art_source", ProjectID: "project_opaque", Kind: "imported_audio", SHA256: beat.SourceSHA256,
		SizeBytes: 36, MediaType: "audio/flac", Format: "flac", DurationFrames: 48000 * 8,
		SampleRate: 48000, Channels: 2, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func hasMusicAnalysisToolMetadata(metadata []tool.ToolMetadata, name string) bool {
	for _, item := range metadata {
		if item.ToolID == name {
			return true
		}
	}
	return false
}
