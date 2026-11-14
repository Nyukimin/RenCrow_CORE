package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	modulebrowser "github.com/Nyukimin/RenCrow_CORE/modules/browseractor"
)

type routineMediaTestRunner struct {
	request  modulebrowser.RunRequest
	response modulebrowser.RunResponse
	err      error
	calls    int
}

func (r *routineMediaTestRunner) Run(_ context.Context, request modulebrowser.RunRequest) (modulebrowser.RunResponse, error) {
	r.request = request
	r.calls++
	return r.response, r.err
}

func TestExecuteRoutineMediaPersistsVerifiedBoundedPlaybackFact(t *testing.T) {
	ctx := context.Background()
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const mediaURL = "https://media.example.invalid/flower.mp4"
	mediaItem := config.RoutineMediaItem{ID: "mdn-flower", Kind: "video", Title: "Flower animation", Credit: "MDN", URL: mediaURL}
	cfg := &config.Config{
		BrowserActor: config.BrowserActorConfig{Enabled: true, AllowedOrigins: []string{"https://broad.example.invalid", "file://"}},
		Heartbeat:    config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{Media: map[string][]config.RoutineMediaItem{"mio": {mediaItem}}}},
	}
	startedAt := "2026-11-13T14:59:58Z"
	finishedAt := "2026-11-13T15:00:10Z" // JST date rolls over while the browser action is running.
	frameCount := int64(24)
	runner := &routineMediaTestRunner{response: modulebrowser.RunResponse{
		Status: modulebrowser.StatusCompleted,
		Actions: []modulebrowser.ActionResult{{
			Type: "play_media", Status: modulebrowser.StatusCompleted, StartedAt: startedAt, CompletedAt: finishedAt,
			Playback: &modulebrowser.Playback{
				Kind: "video", ElapsedMilliseconds: 12000, MediaTimeMilliseconds: 12000,
				DurationMilliseconds: 30000, SourceURL: mediaURL, VideoWidth: 640, VideoHeight: 360,
				PresentedFrameCount: &frameCount,
			},
		}},
	}}
	state, err := store.DraftIntent(ctx, agentroutine.Intent{
		ID: "intent-media-1", ActorID: "mio", Date: "2026-11-13", Kind: agentroutine.ActivityHobby, Label: "video_creativity",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &agentRoutineRuntime{
		cfg: cfg, store: store, mediaRunner: runner,
		now: func() time.Time {
			return time.Date(2026, time.November, 13, 23, 59, 0, 0, time.FixedZone("JST", 9*60*60))
		},
	}
	state, progress, err := runtime.executeRoutineMedia(ctx, nil, state, routineProgress{})
	if err != nil {
		t.Fatalf("executeRoutineMedia: %v", err)
	}
	if progress.Media == nil || !progress.Media.Verified || !progress.Media.Completed || progress.Media.OffsetMS != 12000 || progress.Media.CumulativeElapsedMS != 12000 {
		t.Fatalf("media progress = %+v", progress.Media)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if runner.request.RunID != "" || runner.request.ArtifactDir != "" || runner.request.ProfileID != "" || runner.request.StorageStatePath != "" ||
		!runner.request.Headless || runner.request.SaveTrace || runner.request.SaveScreenshot || !runner.request.MaskSecrets {
		t.Fatalf("routine browser request is not isolated: %+v", runner.request)
	}
	if len(runner.request.AllowedOrigins) != 1 || runner.request.AllowedOrigins[0] != "https://media.example.invalid" {
		t.Fatalf("request allowed origins = %#v, want only exact configured origin", runner.request.AllowedOrigins)
	}
	if len(runner.request.Actions) != 1 || runner.request.Actions[0].Type != "play_media" || runner.request.Actions[0].Value != "video" ||
		runner.request.Actions[0].MaxPlayMS != routineMediaSliceMS || runner.request.Actions[0].StartMS != 0 {
		t.Fatalf("routine playback action = %+v", runner.request.Actions)
	}

	facts, err := store.ListFacts(ctx, "mio", "2026-11-14")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("media facts = %d, want 1", len(facts))
	}
	fact := facts[0]
	if fact.Source != "routine_media_playback" || fact.Kind != agentroutine.ActivityHobby || !fact.Confirmed ||
		fact.Summary != "動画「Flower animation」を合計12.0秒再生（出典: MDN）" || fact.At.Format(time.RFC3339) != finishedAt {
		t.Fatalf("media fact = %+v", fact)
	}
	if strings.Contains(fact.Reference, mediaURL) || strings.Contains(fact.Reference, "private") || strings.Contains(fact.Reference, "decisions") {
		t.Fatalf("media fact included URL/body/decision data: %q", fact.Reference)
	}
	var receipt routineMediaFactReference
	if err := json.Unmarshal([]byte(fact.Reference), &receipt); err != nil {
		t.Fatalf("decode receipt metadata: %v", err)
	}
	if receipt.ItemID != mediaItem.ID || receipt.StartMS != 0 || receipt.EndMS != 12000 || receipt.ElapsedMS != 12000 ||
		receipt.StartedAt.Format(time.RFC3339) != startedAt || receipt.FinishedAt.Format(time.RFC3339) != finishedAt {
		t.Fatalf("receipt metadata = %+v", receipt)
	}

	_, _, err = runtime.executeRoutineMedia(ctx, nil, state, progress)
	if err != nil {
		t.Fatalf("idempotent receipt retry: %v", err)
	}
	facts, err = store.ListFacts(ctx, "mio", "2026-11-14")
	if err != nil || len(facts) != 1 || runner.calls != 1 {
		t.Fatalf("idempotent retry facts=%d runner calls=%d err=%v", len(facts), runner.calls, err)
	}
}

func TestExecuteRoutineMediaRejectsUnprovenPlayback(t *testing.T) {
	tests := []struct {
		name string
		edit func(*modulebrowser.RunResponse)
	}{
		{name: "no typed result", edit: func(response *modulebrowser.RunResponse) { response.Actions[0].Playback = nil }},
		{name: "wrong source origin", edit: func(response *modulebrowser.RunResponse) {
			response.Actions[0].Playback.SourceURL = "https://other.example.invalid/flower.mp4"
		}},
		{name: "no video frames", edit: func(response *modulebrowser.RunResponse) {
			response.Actions[0].Playback.PresentedFrameCount = nil
			response.Actions[0].Playback.VideoWidth = 0
		}},
		{name: "zero elapsed", edit: func(response *modulebrowser.RunResponse) { response.Actions[0].Playback.ElapsedMilliseconds = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			const mediaURL = "https://media.example.invalid/flower.mp4"
			frameCount := int64(24)
			response := modulebrowser.RunResponse{Status: modulebrowser.StatusCompleted, Actions: []modulebrowser.ActionResult{{
				Type: "play_media", Status: modulebrowser.StatusCompleted, StartedAt: "2026-11-13T10:00:00Z", CompletedAt: "2026-11-13T10:00:12Z",
				Playback: &modulebrowser.Playback{Kind: "video", ElapsedMilliseconds: 12000, MediaTimeMilliseconds: 12000,
					DurationMilliseconds: 30000, SourceURL: mediaURL, VideoWidth: 640, VideoHeight: 360, PresentedFrameCount: &frameCount},
			}}}
			tt.edit(&response)
			runner := &routineMediaTestRunner{response: response}
			cfg := &config.Config{BrowserActor: config.BrowserActorConfig{Enabled: true}, Heartbeat: config.HeartbeatConfig{
				Routine: config.RoutineHeartbeatConfig{Media: map[string][]config.RoutineMediaItem{"mio": {{ID: "mdn-flower", Kind: "video", Title: "Flower", Credit: "MDN", URL: mediaURL}}}},
			}}
			state, err := store.DraftIntent(ctx, agentroutine.Intent{ID: "intent-media-test", ActorID: "mio", Date: "2026-11-13", Kind: agentroutine.ActivityHobby, Label: "video_creativity"})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &agentRoutineRuntime{cfg: cfg, store: store, mediaRunner: runner,
				now: func() time.Time { return time.Date(2026, time.November, 13, 10, 0, 0, 0, time.UTC) }}
			_, _, err = runtime.executeRoutineMedia(ctx, nil, state, routineProgress{})
			if err == nil {
				t.Fatal("executeRoutineMedia unexpectedly accepted unproven playback")
			}
			facts, factErr := store.ListFacts(ctx, "mio", "2026-11-13")
			if factErr != nil || len(facts) != 0 {
				t.Fatalf("unverified media facts=%d err=%v", len(facts), factErr)
			}
		})
	}
}

func TestSelectRoutineMediaItemIsStableAcrossConfigOrder(t *testing.T) {
	items := []config.RoutineMediaItem{
		{ID: "b", Kind: "film"}, {ID: "a", Kind: "film"}, {ID: "music", Kind: "music"},
	}
	first, ok := selectRoutineMediaItem(items, "film", "shiro", "2026-11-13", "intent-1", "film_research")
	if !ok {
		t.Fatal("no matching media item selected")
	}
	items[0], items[1] = items[1], items[0]
	second, ok := selectRoutineMediaItem(items, "film", "shiro", "2026-11-13", "intent-1", "film_research")
	if !ok || first.ID != second.ID {
		t.Fatalf("selection changed with config order: %q -> %q", first.ID, second.ID)
	}
}

func TestRoutineMediaReceiptAllowsOnlySmallBrowserPollingOverrun(t *testing.T) {
	frameCount := int64(12)
	item := config.RoutineMediaItem{ID: "clip", Kind: "video", URL: "https://media.example.invalid/clip.mp4"}
	makeResponse := func(elapsed int64) modulebrowser.RunResponse {
		return modulebrowser.RunResponse{Status: modulebrowser.StatusCompleted, Actions: []modulebrowser.ActionResult{{
			Type: "play_media", Status: modulebrowser.StatusCompleted,
			StartedAt: "2026-11-14T01:00:00Z", CompletedAt: "2026-11-14T01:00:12Z",
			Playback: &modulebrowser.Playback{Kind: "video", ElapsedMilliseconds: elapsed, MediaTimeMilliseconds: elapsed,
				DurationMilliseconds: 30000, SourceURL: item.URL, VideoWidth: 640, VideoHeight: 360, PresentedFrameCount: &frameCount},
		}}}
	}
	if _, _, err := validateRoutineMediaReceipt(makeResponse(routineMediaSliceMS+60), item, "https://media.example.invalid", 0, routineMediaSliceMS); err != nil {
		t.Fatalf("one-frame polling overrun was rejected: %v", err)
	}
	if _, _, err := validateRoutineMediaReceipt(makeResponse(routineMediaSliceMS+101), item, "https://media.example.invalid", 0, routineMediaSliceMS); err == nil {
		t.Fatal("media receipt beyond the one-frame tolerance was accepted")
	}
}

func TestRoutineMediaResumePointUsesLatestConfirmedPerDayReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const date = "2026-11-14"
	item := config.RoutineMediaItem{ID: "film-1", Kind: "film"}
	runtime := &agentRoutineRuntime{store: store}
	started := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC)
	for _, receipt := range []routineMediaFactReference{
		{ItemID: item.ID, Kind: item.Kind, StartMS: 0, EndMS: 15000, ElapsedMS: 15000, DurationMS: 90000, StartedAt: started, FinishedAt: started.Add(15 * time.Second)},
	} {
		reference, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveFact(ctx, agentroutine.Activity{
			ActorID: "shiro", Date: date, Key: "old-task:media", Kind: agentroutine.ActivityHobby,
			At: receipt.FinishedAt, Source: "routine_media_playback", SourceID: "old-task:" + item.ID, Confirmed: true, Reference: string(reference),
		}); err != nil {
			t.Fatal(err)
		}
	}
	offset, duration, resumeID, err := runtime.routineMediaResumePoint(ctx, "shiro", date, []config.RoutineMediaItem{item}, "film")
	if err != nil || offset != 15000 || duration != 90000 || resumeID != item.ID {
		t.Fatalf("resume point=(%d,%d,%q), err=%v", offset, duration, resumeID, err)
	}
}
