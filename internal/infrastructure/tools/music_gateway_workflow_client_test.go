package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const musicWorkflowClientTestToken = "music-workflow-client-test-token-0123456789abcdef"

func TestMusicGatewayWorkflowClientUsesTypedActorBoundRoutes(t *testing.T) {
	var generationBody, remixBody map[string]any
	expectedPlanHash := "sha256:" + strings.Repeat("a", 64)
	generationReplyPlanHash := expectedPlanHash
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+musicWorkflowClientTestToken || r.Header.Get("X-RenCrow-Music-Expected-Actor") != "ren" {
			t.Errorf("request omitted configured bearer or fixed actor assertion: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/music/projects/prj_opaque/generations":
			if got := r.Header.Get("Idempotency-Key"); got != "rencrow-core-generation-key" && got != "rencrow-core-generation-mismatch-key" {
				t.Errorf("generation Idempotency-Key = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&generationBody); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(w, `{"job_id":"job_opaque","project_id":"prj_opaque","plan_id":"plan_opaque","plan_hash":%q,"state":"queued","created_at":"2026-11-08T10:00:00Z","updated_at":"2026-11-08T10:00:01Z"}`, generationReplyPlanHash)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/music/jobs/job_opaque":
			_, _ = fmt.Fprintf(w, `{"job_id":"job_opaque","project_id":"prj_opaque","plan_id":"plan_opaque","plan_hash":%q,"state":"succeeded","artifact_id":"art_opaque","duration_frames":960000,"duration_actual_s":20,"created_at":"2026-11-08T10:00:00Z","updated_at":"2026-11-08T10:05:00Z"}`, expectedPlanHash)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/music/projects/prj_opaque/edits":
			if r.Header.Get("Idempotency-Key") != "rencrow-core-remix-key" {
				t.Errorf("remix Idempotency-Key = %q", r.Header.Get("Idempotency-Key"))
			}
			if err := json.NewDecoder(r.Body).Decode(&remixBody); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"art_derived","project_id":"prj_opaque","kind":"audition_remix","sha256":"`+strings.Repeat("b", 64)+`","size_bytes":4096,"media_type":"audio/wav","format":"wav","duration_frames":240000,"sample_rate":48000,"channels":2,"created_at":"2026-11-08T10:00:00Z","source_artifact_id":"art_source","start_frame":120000,"end_frame":360000,"gain_db":-6,"fade_frames":960}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/music/artifacts/art_opaque/download":
			if r.Header.Get("Range") != "bytes=10-19" {
				t.Errorf("artifact Range = %q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Type", "audio/flac")
			w.Header().Set("Content-Range", "bytes 10-19/100")
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0123456789"))
		default:
			t.Errorf("unexpected Music route: %s %s", r.Method, r.URL.Path)
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

	accepted, err := client.SubmitMusicGeneration(context.Background(), "prj_opaque", MusicGenerationRequest{
		PlanID: "plan_opaque", ExpectedPlanHash: expectedPlanHash,
	}, "rencrow-core-generation-key")
	if err != nil || accepted.JobID != "job_opaque" || accepted.State != "queued" || accepted.PlanHash != expectedPlanHash || accepted.CreatedAt == "" || accepted.UpdatedAt == "" {
		t.Fatalf("SubmitMusicGeneration() = %+v, %v", accepted, err)
	}
	if generationBody["plan_id"] != "plan_opaque" || generationBody["expected_plan_hash"] != expectedPlanHash || len(generationBody) != 2 {
		t.Fatalf("generation body = %#v, want only immutable plan ID/hash", generationBody)
	}
	generationReplyPlanHash = "sha256:" + strings.Repeat("b", 64)
	if _, err := client.SubmitMusicGeneration(context.Background(), "prj_opaque", MusicGenerationRequest{
		PlanID: "plan_opaque", ExpectedPlanHash: expectedPlanHash,
	}, "rencrow-core-generation-mismatch-key"); err == nil {
		t.Fatal("generation acceptance with a different plan hash was accepted")
	}
	status, err := client.GetMusicJob(context.Background(), "job_opaque")
	if err != nil || status.State != "succeeded" || status.ArtifactID != "art_opaque" || status.DurationActualS != 20 || status.DurationFrames != 960000 || status.PlanHash != expectedPlanHash || status.CreatedAt == "" || status.UpdatedAt == "" {
		t.Fatalf("GetMusicJob() = %+v, %v", status, err)
	}
	artifact, err := client.CreateMusicEdit(context.Background(), "prj_opaque", MusicEditInput{
		SourceArtifactID: "art_source", StartFrame: 120000, EndFrame: 360000, GainDB: -6, FadeFrames: 960,
	}, "rencrow-core-remix-key")
	if err != nil || artifact.ID != "art_derived" || artifact.SourceArtifactID != "art_source" || artifact.Kind != "audition_remix" {
		t.Fatalf("CreateMusicEdit() = %+v, %v", artifact, err)
	}
	if remixBody["start_frame"] != float64(120000) || remixBody["end_frame"] != float64(360000) || remixBody["gain_db"] != float64(-6) || remixBody["fade_frames"] != float64(960) || len(remixBody) != 5 {
		t.Fatalf("remix body = %#v, want only five typed edit properties", remixBody)
	}
	response, err := client.OpenMusicArtifact(context.Background(), "art_opaque", "bytes=10-19")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != "bytes 10-19/100" || response.ContentLength != 10 {
		t.Fatalf("artifact Range response = status %d headers=%v length=%d", response.StatusCode, response.Header, response.ContentLength)
	}
}

func TestMusicGatewayWorkflowClientRejectsUnsafeIDsAndUnsupportedGenerationStates(t *testing.T) {
	client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicWorkflowClientTestToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"../outside", "job%2fhidden", "job/child", ""} {
		if _, err := client.GetMusicJob(context.Background(), invalid); err == nil {
			t.Errorf("GetMusicJob(%q) unexpectedly accepted unsafe ID", invalid)
		}
		if _, err := client.OpenMusicArtifact(context.Background(), invalid, ""); err == nil {
			t.Errorf("OpenMusicArtifact(%q) unexpectedly accepted unsafe ID", invalid)
		}
	}
	if _, err := client.SubmitMusicGeneration(context.Background(), "prj_opaque", MusicGenerationRequest{
		PlanID: "plan_opaque", ExpectedPlanHash: "invalid-hash",
	}, "key"); err == nil {
		t.Fatal("generation request with non-schema plan hash was accepted")
	}
	if _, err := client.CreateMusicEdit(context.Background(), "prj_opaque", MusicEditInput{
		SourceArtifactID: "art_source", StartFrame: 0, EndFrame: 1000, GainDB: 0, FadeFrames: 501,
	}, "key"); err == nil {
		t.Fatal("generation request with fades crossing the edit boundary was accepted")
	}
}
