package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type musicAnalysisRunnerArtifactMetadataStub struct {
	MusicArtifactMetadataGateway
	actorID  string
	artifact MusicArtifact
}

func (s musicAnalysisRunnerArtifactMetadataStub) ActorID() string { return s.actorID }
func (s musicAnalysisRunnerArtifactMetadataStub) GetMusicArtifact(context.Context, string) (MusicArtifact, error) {
	return s.artifact, nil
}

func TestMusicAnalysisCreateReturnsDurableOwnerStateWithoutLosingID(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		state      string
		failure    string
	}{
		{name: "first failed response", statusCode: http.StatusBadGateway, state: "failed", failure: "WORKER_FAILED"},
		{name: "failed replay", statusCode: http.StatusOK, state: "failed", failure: "WORKER_FAILED"},
		{name: "reconciliation replay", statusCode: http.StatusOK, state: "needs_reconciliation", failure: "WORKER_INTERRUPTED"},
		{name: "running replay", statusCode: http.StatusOK, state: "running"},
		{name: "succeeded replay", statusCode: http.StatusOK, state: "succeeded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := successfulMusicAnalysis(48000)
			analysis.AnalysisID = "analysis_durable"
			analysis.State = test.state
			if test.state != "succeeded" {
				analysis.Result = nil
				analysis.ResultSHA256 = ""
				analysis.FailureCode = test.failure
			}
			body, err := json.Marshal(analysis)
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
				Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicAnalysisClientToken,
				HTTPClient: &http.Client{Transport: musicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: test.statusCode, Header: http.Header{"Content-Type": []string{"application/json"}},
						ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: request,
					}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			projects := &musicProjectGatewayStub{actorID: "ren"}
			artifact := MusicArtifact{
				ID: "art_opaque", ProjectID: "project_opaque", Kind: "imported_audio", SHA256: analysis.SourceSHA256,
				SizeBytes: 36, MediaType: "audio/flac", Format: "flac", DurationFrames: 48000 * 8,
				SampleRate: 48000, Channels: 2, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
			metadata := musicAnalysisRunnerArtifactMetadataStub{actorID: "ren", artifact: artifact}
			runner := NewToolRunner(ToolRunnerConfig{
				MusicProjects: projects, MusicArtifactMetadata: metadata, MusicAnalysisBeats: client,
				DisableToolHarness: true,
			})
			ctx := musicAudioImportExecutionContext(t, "analysis-create-request-"+test.name, "analysis-create-task-"+test.name, "ren")
			response, err := runner.ExecuteV2(ctx, "music.analysis.create", map[string]any{
				"project_id": "project_opaque", "source_artifact_id": "art_opaque", "operation": "beats",
			})
			if err != nil || response == nil || response.Error != nil {
				t.Fatalf("music.analysis.create = (%#v, %v); durable owner response must be preserved", response, err)
			}
			result, ok := response.Result.(MusicAnalysis)
			if !ok || result.AnalysisID != "analysis_durable" || result.State != test.state || result.FailureCode != test.failure {
				t.Fatalf("music.analysis.create lost durable projection: %#v", response.Result)
			}
			if test.state != "succeeded" && (len(result.Result) != 0 || result.ResultSHA256 != "") {
				t.Fatalf("non-success analysis unexpectedly returned result metadata: %+v", result)
			}
			if test.state == "succeeded" {
				var document MusicAnalysisDocument
				if err := json.Unmarshal(result.Result, &document); err != nil || document.SampleRate != artifact.SampleRate || document.DurationFrames != artifact.DurationFrames {
					t.Fatalf("successful source timeline not validated against owned artifact: document=%+v err=%v", document, err)
				}
			}
			if projects.projectID != "project_opaque" || projects.got != 1 || strings.TrimSpace(result.AnalysisID) == "" {
				t.Fatalf("owner scope checks projectID=%q projectCalls=%d analysisID=%q", projects.projectID, projects.got, result.AnalysisID)
			}
		})
	}
}

func TestMusicAnalysisCreateStillRejectsSuccessTimelineMismatch(t *testing.T) {
	analysis := successfulMusicAnalysis(44100)
	body, err := json.Marshal(analysis)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicAnalysisClientToken,
		HTTPClient: &http.Client{Transport: musicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": []string{"application/json"}},
				ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: request,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := musicAnalysisRunnerArtifactMetadataStub{
		actorID: "ren",
		artifact: MusicArtifact{
			ID: "art_opaque", ProjectID: "project_opaque", Kind: "imported_audio", SHA256: analysis.SourceSHA256,
			SizeBytes: 36, MediaType: "audio/flac", Format: "flac", DurationFrames: 48000 * 8,
			SampleRate: 48000, Channels: 2, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	runner := NewToolRunner(ToolRunnerConfig{
		MusicProjects: &musicProjectGatewayStub{actorID: "ren"}, MusicArtifactMetadata: metadata, MusicAnalysisBeats: client,
		DisableToolHarness: true,
	})
	ctx := musicAudioImportExecutionContext(t, "analysis-mismatch-request", "analysis-mismatch-task", "ren")
	response, err := runner.ExecuteV2(ctx, "music.analysis.create", map[string]any{
		"project_id": "project_opaque", "source_artifact_id": "art_opaque", "operation": "beats",
	})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("mismatched successful timeline = (%#v, %v), want validation error", response, err)
	}
}
