package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type musicWorkflowGatewayStub struct {
	actorID      string
	submits      int
	statuses     int
	edits        int
	projectID    string
	jobID        string
	artifactID   string
	submitInput  MusicGenerationRequest
	submitKey    string
	editInput    MusicEditInput
	editKey      string
	jobStatus    MusicJobStatus
	editArtifact MusicArtifact
}

func (s *musicWorkflowGatewayStub) ActorID() string { return s.actorID }
func (s *musicWorkflowGatewayStub) SubmitMusicGeneration(_ context.Context, projectID string, input MusicGenerationRequest, key string) (MusicGenerationAccepted, error) {
	s.submits++
	s.projectID = projectID
	s.submitInput = input
	s.submitKey = key
	return MusicGenerationAccepted{JobID: "job_opaque", ProjectID: projectID, PlanID: input.PlanID, PlanHash: input.ExpectedPlanHash, State: "queued"}, nil
}
func (s *musicWorkflowGatewayStub) GetMusicJob(_ context.Context, jobID string) (MusicJobStatus, error) {
	s.statuses++
	s.jobID = jobID
	return s.jobStatus, nil
}
func (s *musicWorkflowGatewayStub) CreateMusicEdit(_ context.Context, projectID string, input MusicEditInput, key string) (MusicArtifact, error) {
	s.edits++
	s.projectID = projectID
	s.editInput = input
	s.editKey = key
	return s.editArtifact, nil
}
func (s *musicWorkflowGatewayStub) OpenMusicArtifact(context.Context, string, string) (*http.Response, error) {
	return nil, nil
}

func TestMusicWorkflowToolsRegisterForTheirOwnReadyCapabilities(t *testing.T) {
	without := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	metadata, err := without.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, toolID := range []string{"music.generation.submit", "music.generation.status", "music.remix.create"} {
		if hasRunnerTool(metadata, toolID) {
			t.Errorf("Music Tool %q registered without its capability gateway", toolID)
		}
	}

	gateway := &musicWorkflowGatewayStub{actorID: "ren"}
	worker := NewToolRunner(ToolRunnerConfig{
		MusicGeneration: gateway, MusicRemix: gateway, DisableToolHarness: true,
	})
	metadata, err = worker.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, toolID := range []string{"music.generation.submit", "music.generation.status", "music.remix.create"} {
		if !hasRunnerTool(metadata, toolID) {
			t.Errorf("ready Music capability is missing Worker Tool %q", toolID)
		}
	}
	for _, forbidden := range []string{"music.generation.cancel", "music.studio.invoke", "music.remix.backend"} {
		if hasRunnerTool(metadata, forbidden) {
			t.Errorf("Music integration must not register unsupported Tool %q", forbidden)
		}
	}
	for _, item := range metadata {
		if item.ToolID != "music.generation.submit" && item.ToolID != "music.generation.status" && item.ToolID != "music.remix.create" {
			continue
		}
		properties, ok := item.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("Tool %s has no typed parameters", item.ToolID)
		}
		for _, forbidden := range []string{"actor_id", "token", "backend_url", "studio_id", "engine", "path", "url"} {
			if _, exists := properties[forbidden]; exists {
				t.Errorf("Tool %s exposes forbidden routing/security field %q", item.ToolID, forbidden)
			}
		}
	}
}

func TestMusicGenerationSubmissionDerivesIdempotencyFromDelegatedProof(t *testing.T) {
	gateway := &musicWorkflowGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicGeneration: gateway, DisableToolHarness: true})
	args := map[string]any{
		"project_id": "prj_opaque", "plan_id": "plan_opaque", "expected_plan_hash": "sha256:" + strings.Repeat("a", 64),
	}
	noScope, err := runner.ExecuteV2(context.Background(), "music.generation.submit", args)
	if err != nil || noScope == nil || noScope.Error == nil || noScope.Error.Code != tool.ErrToolExecutionScopeMissing {
		t.Fatalf("submit without proof = (%#v, %v), want missing-scope denial", noScope, err)
	}
	if gateway.submits != 0 {
		t.Fatal("untrusted generation submit reached Music")
	}

	ctx := delegatedMusicUserContext(t, "music-generation-request-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	response, err := runner.ExecuteV2(ctx, "music.generation.submit", args)
	if err != nil || response == nil || response.Error != nil || gateway.submits != 1 {
		t.Fatalf("authorized submit = (%#v, %v), calls=%d", response, err, gateway.submits)
	}
	if gateway.projectID != "prj_opaque" || gateway.submitInput.PlanID != "plan_opaque" || gateway.submitInput.ExpectedPlanHash != args["expected_plan_hash"] {
		t.Fatalf("typed generation request = %+v project=%q", gateway.submitInput, gateway.projectID)
	}
	if !strings.HasPrefix(gateway.submitKey, "rencrow-core-generation-") || len(gateway.submitKey) > 200 {
		t.Fatalf("generation idempotency key = %q", gateway.submitKey)
	}
	accepted, ok := response.Result.(MusicGenerationAccepted)
	if !ok || accepted.JobID != "job_opaque" || accepted.State != "queued" {
		t.Fatalf("generation result = %#v, want safe accepted job receipt", response.Result)
	}

	for _, key := range []string{"actor_id", "idempotency_key", "backend_url", "studio_id", "engine"} {
		forged := map[string]any{"project_id": "prj_opaque", "plan_id": "plan_opaque", "expected_plan_hash": args["expected_plan_hash"], key: "override"}
		bad, err := runner.ExecuteV2(ctx, "music.generation.submit", forged)
		if err != nil || bad == nil || bad.Error == nil || bad.Error.Code != tool.ErrValidationFailed {
			t.Errorf("forged generation field %q = (%#v, %v), want validation failure", key, bad, err)
		}
	}
	if gateway.submits != 1 {
		t.Fatalf("forged generation fields reached Music, submit count=%d", gateway.submits)
	}
}

func TestMusicGenerationStatusRequiresDelegationAndReturnsOnlyScopedProxyPath(t *testing.T) {
	gateway := &musicWorkflowGatewayStub{actorID: "ren", jobStatus: MusicJobStatus{
		JobID: "job_opaque", ProjectID: "prj_opaque", PlanID: "plan_opaque", State: "succeeded",
		ArtifactID: "art_opaque", DurationActualS: 20,
	}}
	runner := NewToolRunner(ToolRunnerConfig{MusicGeneration: gateway, MusicArtifactPlayback: gateway, MusicPlaybackLinkReady: true, DisableToolHarness: true})
	ctx := delegatedMusicUserContext(t, "music-generation-read-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	response, err := runner.ExecuteV2(ctx, "music.generation.status", map[string]any{"job_id": "job_opaque"})
	if err != nil || response == nil || response.Error != nil || gateway.statuses != 1 || gateway.jobID != "job_opaque" {
		t.Fatalf("authorized status = (%#v, %v), gateway=%+v", response, err, gateway)
	}
	status, ok := response.Result.(MusicGenerationJobResult)
	if !ok || status.ArtifactID != "art_opaque" || status.PlaybackURL != "/viewer/music/artifacts/art_opaque/download?to=mio" {
		t.Fatalf("status result = %#v, want only a scoped relative CORE proxy path", response.Result)
	}
	if strings.Contains(status.PlaybackURL, "http") || strings.Contains(status.PlaybackURL, "token") || strings.Contains(status.PlaybackURL, "127.0.0.1") {
		t.Fatalf("playback result exposed an origin or credential: %q", status.PlaybackURL)
	}

	public, err := runner.ExecuteV2(context.Background(), "music.generation.status", map[string]any{"job_id": "job_opaque"})
	if err != nil || public == nil || public.Error == nil || public.Error.Code != tool.ErrToolExecutionScopeMissing || gateway.statuses != 1 {
		t.Fatalf("status without proof = (%#v, %v), backend reads=%d", public, err, gateway.statuses)
	}
}

func TestMusicRemixCreatesTypedBoundedImmutableArtifact(t *testing.T) {
	gateway := &musicWorkflowGatewayStub{actorID: "ren", editArtifact: MusicArtifact{
		ID: "art_derived", ProjectID: "prj_opaque", Kind: "audition_remix", SHA256: strings.Repeat("b", 64),
		SizeBytes: 4096, MediaType: "audio/wav", Format: "wav", DurationFrames: 240000, SampleRate: 48000, Channels: 2,
		CreatedAt: "2026-11-08T10:00:00Z", SourceArtifactID: "art_source", StartFrame: 120000, EndFrame: 360000,
		GainDB: -6, FadeFrames: 960,
	}}
	runner := NewToolRunner(ToolRunnerConfig{MusicRemix: gateway, MusicArtifactPlayback: gateway, MusicPlaybackLinkReady: true, DisableToolHarness: true})
	ctx := delegatedMusicUserContext(t, "music-remix-request-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	args := map[string]any{
		"project_id": "prj_opaque", "source_artifact_id": "art_source", "start_frame": 120000,
		"end_frame": 360000, "gain_db": -6.0, "fade_frames": 960,
	}
	response, err := runner.ExecuteV2(ctx, "music.remix.create", args)
	if err != nil || response == nil || response.Error != nil || gateway.edits != 1 {
		t.Fatalf("authorized remix = (%#v, %v), gateway=%+v", response, err, gateway)
	}
	if gateway.projectID != "prj_opaque" || gateway.editInput.SourceArtifactID != "art_source" || gateway.editInput.StartFrame != 120000 || gateway.editInput.EndFrame != 360000 || gateway.editInput.GainDB != -6 || gateway.editInput.FadeFrames != 960 {
		t.Fatalf("Music edit input was not preserved: %+v", gateway.editInput)
	}
	if !strings.HasPrefix(gateway.editKey, "rencrow-core-remix-") {
		t.Fatalf("remix idempotency key = %q", gateway.editKey)
	}
	result, ok := response.Result.(MusicArtifactResult)
	if !ok || result.ID != "art_derived" || result.PlaybackURL != "/viewer/music/artifacts/art_derived/download?to=mio" {
		t.Fatalf("remix result = %#v, want typed derived artifact and scoped proxy URL", response.Result)
	}

	for _, badArgs := range []map[string]any{
		{"project_id": "prj_opaque", "source_artifact_id": "art_source", "start_frame": 0, "end_frame": 1000, "gain_db": 0.0, "fade_frames": 501},
		{"project_id": "prj_opaque", "source_artifact_id": "art_source", "start_frame": 0, "end_frame": 960001, "gain_db": 0.0, "fade_frames": 0},
		{"project_id": "prj_opaque", "source_artifact_id": "../escape", "start_frame": 0, "end_frame": 1000, "gain_db": 0.0, "fade_frames": 0},
	} {
		bad, err := runner.ExecuteV2(ctx, "music.remix.create", badArgs)
		if err != nil || bad == nil || bad.Error == nil || bad.Error.Code != tool.ErrValidationFailed {
			t.Errorf("invalid remix args = (%#v, %v), want validation failure", bad, err)
		}
	}
	if gateway.edits != 1 {
		t.Fatalf("invalid remix arguments reached Music, edit count=%d", gateway.edits)
	}
}

func hasRunnerTool(metadata []tool.ToolMetadata, toolID string) bool {
	for _, item := range metadata {
		if item.ToolID == toolID {
			return true
		}
	}
	return false
}
