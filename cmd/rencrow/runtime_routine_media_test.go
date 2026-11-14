package main

import (
	"context"
	"encoding/json"
	"fmt"
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

	interrupted := makeResponse(5000)
	interrupted.Status = modulebrowser.StatusFailed
	interrupted.Error = &modulebrowser.Error{Code: modulebrowser.ErrCancelled}
	interrupted.Actions[0].Status = modulebrowser.StatusFailed
	receipt, cancelled, err := validateRoutineMediaReceipt(interrupted, item, "https://media.example.invalid", 0, routineMediaSliceMS)
	if err != nil || !cancelled || receipt.endMS != 5000 || receipt.elapsedMS != 5000 {
		t.Fatalf("verified interrupted playback receipt=%+v cancelled=%v err=%v", receipt, cancelled, err)
	}
	interrupted.Actions[0].Playback.ElapsedMilliseconds = 0
	if _, _, err := validateRoutineMediaReceipt(interrupted, item, "https://media.example.invalid", 0, routineMediaSliceMS); err == nil {
		t.Fatal("cancelled receipt without measured elapsed progress was accepted")
	}

	wrongSource := makeResponse(5000)
	wrongSource.Actions[0].Playback.SourceURL = "https://media.example.invalid/other.mp4"
	if _, _, err := validateRoutineMediaReceipt(wrongSource, item, "https://media.example.invalid", 0, routineMediaSliceMS); err == nil {
		t.Fatal("same-origin receipt from a different configured media URL was accepted")
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

func TestRoutineMediaResumePointUsesLatestConfirmedReceiptOnOrBeforeDate(t *testing.T) {
	ctx := context.Background()
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item := config.RoutineMediaItem{ID: "film-1", Kind: "film"}
	runtime := &agentRoutineRuntime{store: store}
	for _, test := range []struct {
		actorID   string
		date      string
		itemID    string
		endMS     int64
		confirmed bool
	}{
		{actorID: "shiro", date: "2026-11-12", itemID: item.ID, endMS: 15_000, confirmed: true},
		{actorID: "shiro", date: "2026-11-13", itemID: item.ID, endMS: 30_000, confirmed: true},
		{actorID: "shiro", date: "2026-11-13", itemID: item.ID, endMS: 40_000, confirmed: false},
		{actorID: "shiro", date: "2026-11-14", itemID: "other-film", endMS: 60_000, confirmed: true},
		{actorID: "shiro", date: "2026-11-15", itemID: item.ID, endMS: 50_000, confirmed: true},
		{actorID: "mio", date: "2026-11-13", itemID: item.ID, endMS: 70_000, confirmed: true},
	} {
		dayStart, err := time.ParseInLocation("2006-01-02", test.date, time.FixedZone("JST", 9*60*60))
		if err != nil {
			t.Fatal(err)
		}
		finishedAt := dayStart.Add(10*time.Hour + time.Duration(test.endMS)*time.Millisecond)
		startedAt := finishedAt.Add(-12 * time.Second)
		reference, err := json.Marshal(routineMediaFactReference{
			ItemID: test.itemID, Kind: "film", StartMS: test.endMS - 12_000, EndMS: test.endMS,
			ElapsedMS: 12_000, DurationMS: 900_000, StartedAt: startedAt, FinishedAt: finishedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveFact(ctx, agentroutine.Activity{
			ActorID: test.actorID, Date: test.date, Key: fmt.Sprintf("%s-%s-%d", test.actorID, test.date, test.endMS),
			Kind: agentroutine.ActivityHobby, At: finishedAt, Source: "routine_media_playback",
			SourceID: "old-task:" + test.itemID, Confirmed: test.confirmed, Reference: string(reference),
		}); err != nil {
			t.Fatalf("SaveFact(%s/%s/%d): %v", test.actorID, test.date, test.endMS, err)
		}
	}

	offset, duration, resumeID, err := runtime.routineMediaResumePoint(ctx, "shiro", "2026-11-14", []config.RoutineMediaItem{item}, "film")
	if err != nil || offset != 30_000 || duration != 900_000 || resumeID != item.ID {
		t.Fatalf("prior-date resume point=(%d,%d,%q), err=%v; want latest confirmed configured Shiro receipt not after requested date", offset, duration, resumeID, err)
	}
}

func TestExecuteRoutineMediaResumesPriorDateReceiptOffset(t *testing.T) {
	ctx := context.Background()
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const mediaURL = "https://media.example.invalid/film.mp4"
	item := config.RoutineMediaItem{ID: "film-1", Kind: "film", Title: "Film", Credit: "Library", URL: mediaURL}
	cfg := &config.Config{
		BrowserActor: config.BrowserActorConfig{Enabled: true},
		Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
			Media: map[string][]config.RoutineMediaItem{"shiro": {item}},
		}},
	}
	previousStart := time.Date(2026, time.November, 13, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	previousFinish := previousStart.Add(12 * time.Second)
	reference, err := json.Marshal(routineMediaFactReference{
		ItemID: item.ID, Kind: item.Kind, StartMS: 3_000, EndMS: 15_000, ElapsedMS: 12_000,
		DurationMS: 90_000, StartedAt: previousStart, FinishedAt: previousFinish,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveFact(ctx, agentroutine.Activity{
		ActorID: "shiro", Date: "2026-11-13", Key: "previous-intent:media:film-1", Kind: agentroutine.ActivityHobby,
		At: previousFinish, Source: "routine_media_playback", SourceID: "previous-intent:film-1", Confirmed: true, Reference: string(reference),
	}); err != nil {
		t.Fatalf("SaveFact: %v", err)
	}
	state, err := store.DraftIntent(ctx, agentroutine.Intent{
		ID: "today-intent", ActorID: "shiro", Date: "2026-11-14", Kind: agentroutine.ActivityHobby, Label: "film_research",
	})
	if err != nil {
		t.Fatal(err)
	}
	frameCount := int64(24)
	runner := &routineMediaTestRunner{response: modulebrowser.RunResponse{
		Status: modulebrowser.StatusCompleted,
		Actions: []modulebrowser.ActionResult{{
			Type: "play_media", Status: modulebrowser.StatusCompleted,
			StartedAt: "2026-11-14T01:00:00Z", CompletedAt: "2026-11-14T01:00:12Z",
			Playback: &modulebrowser.Playback{
				Kind: "video", ElapsedMilliseconds: 12_000, MediaTimeMilliseconds: 27_000,
				DurationMilliseconds: 90_000, SourceURL: mediaURL, VideoWidth: 640, VideoHeight: 360,
				PresentedFrameCount: &frameCount,
			},
		}},
	}}
	runtime := &agentRoutineRuntime{
		cfg: cfg, store: store, mediaRunner: runner,
		now: func() time.Time {
			return time.Date(2026, time.November, 14, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
		},
	}
	_, progress, err := runtime.executeRoutineMedia(ctx, nil, state, routineProgress{})
	if err != nil {
		t.Fatalf("executeRoutineMedia: %v", err)
	}
	if runner.calls != 1 || len(runner.request.Actions) != 1 || runner.request.Actions[0].StartMS != 15_000 ||
		progress.Media == nil || progress.Media.StartOffsetMS != 15_000 || progress.Media.OffsetMS != 27_000 {
		t.Fatalf("prior-day media resume request=%+v progress=%+v calls=%d", runner.request, progress.Media, runner.calls)
	}
}

func TestRoutineMediaResumePointDoesNotResumeAfterEOF(t *testing.T) {
	ctx := context.Background()
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item := config.RoutineMediaItem{ID: "film-1", Kind: "film"}
	runtime := &agentRoutineRuntime{store: store}
	for _, test := range []struct {
		date    string
		key     string
		startMS int64
		endMS   int64
		ended   bool
	}{
		{date: "2026-11-12", key: "partial", startMS: 0, endMS: 15_000},
		{date: "2026-11-13", key: "eof", startMS: 78_000, endMS: 90_000, ended: true},
	} {
		dayStart, err := time.ParseInLocation("2006-01-02", test.date, time.FixedZone("JST", 9*60*60))
		if err != nil {
			t.Fatal(err)
		}
		finishedAt := dayStart.Add(10 * time.Hour)
		reference, err := json.Marshal(routineMediaFactReference{
			ItemID: item.ID, Kind: item.Kind, StartMS: test.startMS, EndMS: test.endMS,
			ElapsedMS: test.endMS - test.startMS, DurationMS: 90_000, Ended: test.ended,
			StartedAt: finishedAt.Add(-12 * time.Second), FinishedAt: finishedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveFact(ctx, agentroutine.Activity{
			ActorID: "shiro", Date: test.date, Key: test.key, Kind: agentroutine.ActivityHobby,
			At: finishedAt, Source: "routine_media_playback", SourceID: test.key + ":" + item.ID,
			Confirmed: true, Reference: string(reference),
		}); err != nil {
			t.Fatalf("SaveFact(%s): %v", test.key, err)
		}
	}
	offset, duration, resumeID, err := runtime.routineMediaResumePoint(ctx, "shiro", "2026-11-14", []config.RoutineMediaItem{item}, "film")
	if err != nil || offset != 0 || duration != 0 || resumeID != "" {
		t.Fatalf("resume point after latest EOF=(%d,%d,%q), err=%v; want no continuation", offset, duration, resumeID, err)
	}
}
