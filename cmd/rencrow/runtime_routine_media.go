package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	modulebrowser "github.com/Nyukimin/RenCrow_CORE/modules/browseractor"
)

const routineMediaSliceMS = 12000
const routineMediaMaxDurationMS int64 = 24 * 60 * 60 * 1000
const routineMediaResumeLookbackDays = 30

var errRoutineMediaNotConfigured = errors.New("no operator-configured media matches this routine hobby")

type routineMediaRunner interface {
	Run(context.Context, modulebrowser.RunRequest) (modulebrowser.RunResponse, error)
}

// routineMediaProgress is bounded playback evidence. It deliberately excludes URLs,
// raw Browser Actor responses, page text, and local decisions.
type routineMediaProgress struct {
	ItemID              string    `json:"item_id"`
	Kind                string    `json:"kind"`
	Title               string    `json:"title"`
	Credit              string    `json:"credit"`
	StartOffsetMS       int64     `json:"start_offset_ms,omitempty"`
	OffsetMS            int64     `json:"offset_ms"`
	DurationMS          int64     `json:"duration_ms,omitempty"`
	CumulativeElapsedMS int64     `json:"cumulative_elapsed_ms,omitempty"`
	StartedAt           time.Time `json:"started_at,omitempty"`
	FinishedAt          time.Time `json:"finished_at,omitempty"`
	Ended               bool      `json:"ended,omitempty"`
	Verified            bool      `json:"verified,omitempty"`
	Completed           bool      `json:"completed,omitempty"`
}

type routineMediaFactReference struct {
	ItemID     string    `json:"item_id"`
	Kind       string    `json:"kind"`
	StartMS    int64     `json:"start_ms"`
	EndMS      int64     `json:"end_ms"`
	ElapsedMS  int64     `json:"elapsed_ms"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Ended      bool      `json:"ended"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type routineMediaReceipt struct {
	startMS    int64
	endMS      int64
	elapsedMS  int64
	durationMS int64
	ended      bool
	startedAt  time.Time
	finishedAt time.Time
}

func (r *agentRoutineRuntime) executeRoutineMedia(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if ctx == nil || r == nil || r.cfg == nil || !r.cfg.BrowserActor.Enabled || r.store == nil || r.mediaRunner == nil || state.Pending == nil {
		return state, progress, errors.New("routine media execution is not configured")
	}
	intent := state.Pending
	if intent.Kind != agentroutine.ActivityHobby || intent.ActorID == "" || intent.ActorID != state.ActorID || !routineFactActorID(intent.ActorID) {
		return state, progress, errors.New("routine media intent identity is invalid")
	}
	if _, _, err := routineFactDay(intent.Date); err != nil {
		return state, progress, errors.New("routine media intent date is invalid")
	}
	expectedKind, ok := routineMediaKindForLabel(intent.Label)
	if !ok {
		return state, progress, errRoutineMediaNotConfigured
	}
	items := r.cfg.Heartbeat.Routine.Media[intent.ActorID]
	if len(items) == 0 {
		return state, progress, errRoutineMediaNotConfigured
	}

	var selected config.RoutineMediaItem
	if progress.Media == nil {
		selected, ok = selectRoutineMediaItem(items, expectedKind, intent.ActorID, intent.Date, intent.ID, intent.Label)
		if !ok {
			return state, progress, errRoutineMediaNotConfigured
		}
		resumeOffset, resumeDuration, resumeItemID, resumeErr := r.routineMediaResumePoint(ctx, intent.ActorID, intent.Date, items, expectedKind)
		if resumeErr != nil {
			return state, progress, resumeErr
		}
		if resumeItemID != "" {
			selected, ok = findRoutineMediaItem(items, resumeItemID)
			if !ok || selected.Kind != expectedKind {
				return state, progress, errors.New("saved routine media resume item is no longer configured")
			}
		}
		progress.Media = &routineMediaProgress{
			ItemID: selected.ID, Kind: selected.Kind, Title: selected.Title, Credit: selected.Credit,
			OffsetMS: resumeOffset, DurationMS: resumeDuration,
		}
		var err error
		state, progress, err = r.saveProgress(ctx, state, "routine_media_selection_saved", progress)
		if err != nil {
			return state, progress, err
		}
	} else {
		selected, ok = findRoutineMediaItem(items, progress.Media.ItemID)
		if !ok || selected.Kind != expectedKind || selected.Kind != progress.Media.Kind {
			return state, progress, errors.New("persisted routine media selection is no longer configured")
		}
		progress.Media.Title, progress.Media.Credit = selected.Title, selected.Credit
	}
	if progress.Media.Verified {
		if !progress.EffectCompleted {
			progress.EffectCompleted = true
			if progress.CompletedAt.IsZero() {
				progress.CompletedAt = progress.Media.FinishedAt.UTC()
			}
			state, progress, err := r.saveProgress(ctx, state, "routine_media_completion_saved", progress)
			if err != nil {
				return state, progress, err
			}
		}
		if err := r.saveRoutineMediaFact(ctx, state, *progress.Media); err != nil {
			return state, progress, err
		}
		return state, progress, nil
	}
	if progress.Media.OffsetMS < 0 || progress.Media.OffsetMS > routineMediaMaxDurationMS || progress.Media.DurationMS < 0 ||
		progress.Media.DurationMS > routineMediaMaxDurationMS || progress.Media.CumulativeElapsedMS < 0 || progress.Media.CumulativeElapsedMS > routineMediaMaxDurationMS {
		return state, progress, errors.New("persisted routine media offset is invalid")
	}
	if routineStopRequested(ctx, stop) {
		return state, progress, errAgentRoutineStopped
	}

	origin, err := routineMediaOrigin(selected.URL)
	if err != nil {
		return state, progress, err
	}
	windowCtx, cancelWindow := r.withRoutineWindow(ctx, intent, state)
	defer cancelWindow()
	if r.worldLife != nil {
		roomID := r.cfg.Heartbeat.Routine.PrivateRooms[intent.ActorID]
		if roomID == "" {
			return state, progress, errors.New("routine media requires the actor's configured private room")
		}
		state, progress, _, err = r.ensureRoutineWorldArrival(windowCtx, stop, state, progress, intent.ActorID, roomID, "", "media-private-room", routineWorldMoveWait)
		if err != nil {
			return state, progress, err
		}
	}
	callCtx, cancelCall := context.WithTimeout(windowCtx, time.Duration(routineMediaSliceMS+8000)*time.Millisecond)
	defer cancelCall()
	callCtx, cancelStop := routineMediaStopContext(callCtx, stop)
	defer cancelStop()

	request := modulebrowser.RunRequest{
		SchemaVersion:  modulebrowser.SchemaVersion,
		Goal:           "bounded routine media playback",
		StartURL:       selected.URL,
		Headless:       true,
		AllowedOrigins: []string{origin},
		TimeoutMS:      routineMediaSliceMS + 8000,
		MaxActions:     1,
		SaveTrace:      false,
		SaveScreenshot: false,
		MaskSecrets:    true,
		Actions: []modulebrowser.Action{{
			Type: "play_media", Value: routineMediaPlayerKind(selected.Kind),
			StartMS: progress.Media.OffsetMS, MaxPlayMS: routineMediaSliceMS,
		}},
	}
	request = modulebrowser.NormalizeRunRequest(request)
	if err := modulebrowser.ValidateRunRequest(request); err != nil {
		return state, progress, errors.New("routine media request failed local validation")
	}
	response, runErr := r.mediaRunner.Run(callCtx, request)
	if callCtx.Err() != nil && response.Status == "" {
		return state, progress, errAgentRoutineStopped
	}
	if runErr != nil {
		if callCtx.Err() != nil {
			return state, progress, errAgentRoutineStopped
		}
		return state, progress, errors.New("routine media runner failed")
	}
	receipt, cancelled, err := validateRoutineMediaReceipt(response, selected, origin, progress.Media.OffsetMS, routineMediaSliceMS)
	if err != nil {
		if cancelled && callCtx.Err() != nil {
			return state, progress, errAgentRoutineStopped
		}
		return state, progress, err
	}

	progress.Media.StartOffsetMS = receipt.startMS
	progress.Media.OffsetMS = receipt.endMS
	progress.Media.DurationMS = receipt.durationMS
	if progress.Media.CumulativeElapsedMS > routineMediaMaxDurationMS-receipt.elapsedMS {
		return state, progress, errors.New("routine media elapsed bound exceeded")
	}
	progress.Media.CumulativeElapsedMS += receipt.elapsedMS
	progress.Media.StartedAt = receipt.startedAt.UTC()
	progress.Media.FinishedAt = receipt.finishedAt.UTC()
	progress.Media.Ended = receipt.ended
	progress.Media.Verified = true
	progress.Media.Completed = true // This bounded playback Task is complete; Ended indicates media EOF.
	progress.EffectCompleted = true
	progress.CompletedAt = receipt.finishedAt.UTC()
	state, progress, err = r.saveProgress(ctx, state, "routine_media_receipt_saved", progress)
	if err != nil {
		return state, progress, err
	}
	if err := r.saveRoutineMediaFact(ctx, state, *progress.Media); err != nil {
		return state, progress, err
	}
	if cancelled || routineStopRequested(ctx, stop) {
		return state, progress, errAgentRoutineStopped
	}
	return state, progress, nil
}

func (r *agentRoutineRuntime) routineMediaResumePoint(ctx context.Context, actorID, date string, items []config.RoutineMediaItem, kind string) (int64, int64, string, error) {
	if r == nil || r.store == nil {
		return 0, 0, "", nil
	}
	dayStart, _, err := routineFactDay(date)
	if err != nil {
		return 0, 0, "", errors.New("routine media resume date is invalid")
	}
	// A 30-day actor/date range covers long media across daily 30-minute hobby
	// windows while keeping the canonical activity read bounded.
	lookbackDate := dayStart.AddDate(0, 0, -(routineMediaResumeLookbackDays - 1)).Format("2006-01-02")
	facts, err := r.store.ListFactsInDateRange(ctx, actorID, lookbackDate, date)
	if err != nil {
		return 0, 0, "", err
	}
	configured := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.Kind == kind {
			configured[item.ID] = struct{}{}
		}
	}
	var latest agentroutine.Activity
	var latestReceipt routineMediaFactReference
	for _, fact := range facts {
		if fact.ActorID != actorID || !fact.Confirmed || fact.Source != "routine_media_playback" ||
			fact.Kind != agentroutine.ActivityHobby || fact.Date < lookbackDate || fact.Date > date || fact.At.IsZero() {
			continue
		}
		for itemID := range configured {
			if !strings.HasSuffix(fact.SourceID, ":"+itemID) {
				continue
			}
			receipt, valid := validatedRoutineMediaFactReference(fact)
			if !valid || receipt.ItemID != itemID || receipt.Kind != kind {
				return 0, 0, "", errors.New("saved routine media resume receipt is invalid")
			}
			if latest.Key == "" || fact.At.After(latest.At) || (fact.At.Equal(latest.At) && fact.SourceID > latest.SourceID) {
				latest, latestReceipt = fact, receipt
			}
		}
	}
	if latest.Key == "" || latestReceipt.Ended || latestReceipt.EndMS <= 0 ||
		(latestReceipt.DurationMS > 0 && latestReceipt.EndMS >= latestReceipt.DurationMS) {
		return 0, 0, "", nil
	}
	return latestReceipt.EndMS, latestReceipt.DurationMS, latestReceipt.ItemID, nil
}

func validatedRoutineMediaFactReference(fact agentroutine.Activity) (routineMediaFactReference, bool) {
	if !fact.Confirmed || fact.Source != "routine_media_playback" || fact.Kind != agentroutine.ActivityHobby ||
		fact.At.IsZero() || len(fact.Reference) > 4096 {
		return routineMediaFactReference{}, false
	}
	var receipt routineMediaFactReference
	if json.Unmarshal([]byte(fact.Reference), &receipt) != nil || receipt.ItemID == "" ||
		!strings.HasSuffix(fact.SourceID, ":"+receipt.ItemID) ||
		(receipt.Kind != "video" && receipt.Kind != "film" && receipt.Kind != "music") ||
		receipt.StartMS < 0 || receipt.EndMS <= receipt.StartMS || receipt.EndMS > routineMediaMaxDurationMS ||
		receipt.ElapsedMS <= 0 || receipt.ElapsedMS > routineMediaMaxDurationMS ||
		receipt.DurationMS < 0 || receipt.DurationMS > routineMediaMaxDurationMS ||
		(receipt.DurationMS > 0 && receipt.EndMS > receipt.DurationMS+1000) ||
		receipt.StartedAt.IsZero() || receipt.FinishedAt.IsZero() || receipt.FinishedAt.Before(receipt.StartedAt) ||
		!fact.At.Equal(receipt.FinishedAt) {
		return routineMediaFactReference{}, false
	}
	return receipt, true
}

func routineMediaKindForLabel(label string) (string, bool) {
	switch label {
	case "video_creativity":
		return "video", true
	case "film_research":
		return "film", true
	case "music_culture":
		return "music", true
	default:
		return "", false
	}
}

func selectRoutineMediaItem(items []config.RoutineMediaItem, kind, actorID, date, intentID, label string) (config.RoutineMediaItem, bool) {
	matches := make([]config.RoutineMediaItem, 0, len(items))
	for _, item := range items {
		if item.Kind == kind {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return config.RoutineMediaItem{}, false
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	seed := sha256.Sum256([]byte(actorID + "\x00" + date + "\x00" + intentID + "\x00" + label))
	index := int(uint64(seed[0])<<8|uint64(seed[1])) % len(matches)
	return matches[index], true
}

func findRoutineMediaItem(items []config.RoutineMediaItem, id string) (config.RoutineMediaItem, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return config.RoutineMediaItem{}, false
}

func routineMediaPlayerKind(kind string) string {
	if kind == "music" {
		return "audio"
	}
	return "video"
}

func routineMediaOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", errors.New("routine media URL is outside the configured HTTPS boundary")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" || port == "443" {
		if strings.Contains(host, ":") {
			return "https://[" + host + "]", nil
		}
		return "https://" + host, nil
	}
	return "https://" + net.JoinHostPort(host, port), nil
}

func routineMediaStopContext(ctx context.Context, stop <-chan struct{}) (context.Context, context.CancelFunc) {
	callCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() {
		select {
		case <-stop:
			cancel()
		case <-callCtx.Done():
		case <-finished:
		}
	}()
	return callCtx, func() {
		close(finished)
		cancel()
	}
}

func validateRoutineMediaReceipt(response modulebrowser.RunResponse, item config.RoutineMediaItem, expectedOrigin string, startMS int64, maxPlayMS int) (routineMediaReceipt, bool, error) {
	cancelled := response.Error != nil && response.Error.Code == modulebrowser.ErrCancelled
	if len(response.Actions) != 1 || response.Actions[0].Type != "play_media" || response.Actions[0].Playback == nil {
		return routineMediaReceipt{}, cancelled, errors.New("routine media runner returned no typed playback receipt")
	}
	result := response.Actions[0]
	if result.Error != "" || (response.Error != nil && !cancelled) {
		return routineMediaReceipt{}, cancelled, errors.New("routine media runner returned an action error")
	}
	if result.Status != modulebrowser.StatusCompleted && !cancelled {
		return routineMediaReceipt{}, false, errors.New("routine media playback did not complete successfully")
	}
	if response.Status != modulebrowser.StatusCompleted && !cancelled {
		return routineMediaReceipt{}, false, errors.New("routine media run did not complete successfully")
	}
	playback := result.Playback
	expectedPlayer := routineMediaPlayerKind(item.Kind)
	const playbackFrameToleranceMS = 100
	if playback.Kind != expectedPlayer || playback.ElapsedMilliseconds <= 0 || playback.ElapsedMilliseconds > int64(maxPlayMS+playbackFrameToleranceMS) ||
		playback.MediaTimeMilliseconds <= startMS || playback.MediaTimeMilliseconds < 0 || playback.DurationMilliseconds < 0 || playback.DurationMilliseconds > routineMediaMaxDurationMS {
		return routineMediaReceipt{}, cancelled, errors.New("routine media playback receipt contains invalid progress")
	}
	if playback.DurationMilliseconds > 0 && playback.MediaTimeMilliseconds > playback.DurationMilliseconds+1000 {
		return routineMediaReceipt{}, cancelled, errors.New("routine media playback position exceeds reported duration")
	}
	if playback.Ended && playback.DurationMilliseconds > 0 && playback.MediaTimeMilliseconds+1000 < playback.DurationMilliseconds {
		return routineMediaReceipt{}, cancelled, errors.New("routine media EOF does not match reported duration")
	}
	if expectedPlayer == "video" {
		if playback.VideoWidth <= 0 || playback.VideoHeight <= 0 ||
			(playback.PresentedFrameCount != nil && *playback.PresentedFrameCount <= 0) {
			return routineMediaReceipt{}, cancelled, errors.New("routine video playback receipt has no observed frame evidence")
		}
	}
	sourceOrigin, err := routineMediaOrigin(playback.SourceURL)
	if err != nil || sourceOrigin != expectedOrigin || playback.SourceURL != item.URL {
		return routineMediaReceipt{}, cancelled, errors.New("routine media playback source left its configured origin")
	}
	startedAt, err := parseRoutineMediaTimestamp(result.StartedAt)
	if err != nil {
		return routineMediaReceipt{}, cancelled, errors.New("routine media receipt has no valid action start time")
	}
	finishedAt, err := parseRoutineMediaTimestamp(result.CompletedAt)
	if err != nil || finishedAt.Before(startedAt) {
		return routineMediaReceipt{}, cancelled, errors.New("routine media receipt has no valid action end time")
	}
	return routineMediaReceipt{
		startMS: startMS, endMS: playback.MediaTimeMilliseconds, elapsedMS: playback.ElapsedMilliseconds,
		durationMS: playback.DurationMilliseconds, ended: playback.Ended, startedAt: startedAt, finishedAt: finishedAt,
	}, cancelled, nil
}

func parseRoutineMediaTimestamp(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return time.Time{}, errors.New("missing timestamp")
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}, errors.New("invalid timestamp")
	}
	return parsed.UTC(), nil
}

func (r *agentRoutineRuntime) saveRoutineMediaFact(ctx context.Context, state agentroutine.State, media routineMediaProgress) error {
	if r == nil || r.store == nil || state.Pending == nil || !media.Verified || media.CumulativeElapsedMS <= 0 || media.ItemID == "" || media.StartedAt.IsZero() || media.FinishedAt.IsZero() {
		return errors.New("routine media fact is incomplete")
	}
	reference, err := json.Marshal(routineMediaFactReference{
		ItemID: media.ItemID, Kind: media.Kind, StartMS: media.StartOffsetMS,
		EndMS: media.OffsetMS, ElapsedMS: media.CumulativeElapsedMS, DurationMS: media.DurationMS,
		Ended: media.Ended, StartedAt: media.StartedAt.UTC(), FinishedAt: media.FinishedAt.UTC(),
	})
	if err != nil {
		return errors.New("encode routine media receipt metadata")
	}
	factDate := media.FinishedAt.In(r.location()).Format("2006-01-02")
	fact := agentroutine.Activity{
		ActorID: state.Pending.ActorID, Date: factDate,
		Key: state.Pending.ID + ":media:" + media.ItemID, Kind: agentroutine.ActivityHobby,
		At: media.FinishedAt.UTC(), Source: "routine_media_playback", SourceID: state.Pending.ID + ":" + media.ItemID,
		Confirmed: true, Summary: routineMediaDiarySummary(media), Reference: string(reference),
	}
	_, err = routinePersist(ctx, func(persistCtx context.Context) (agentroutine.Activity, error) {
		return r.store.SaveFact(persistCtx, fact)
	})
	return err
}

func routineMediaDiarySummary(media routineMediaProgress) string {
	if !media.Verified || media.CumulativeElapsedMS <= 0 || media.Title == "" || media.Credit == "" {
		return ""
	}
	label := "動画"
	if media.Kind == "film" {
		label = "映画映像"
	} else if media.Kind == "music" {
		label = "音楽"
	}
	return fmt.Sprintf("%s「%s」を合計%.1f秒再生（出典: %s）", label, media.Title, float64(media.CumulativeElapsedMS)/1000, media.Credit)
}
