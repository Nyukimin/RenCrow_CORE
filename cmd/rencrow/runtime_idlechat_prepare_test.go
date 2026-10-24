package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

func newStoryPrepareHandler(t *testing.T, target int, scheduler *idlechat.NightPreparationScheduler) http.Handler {
	t.Helper()
	orch := idlechat.NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	orch.SetStoryEpisodeService(idlechat.NewPersistentStoryEpisodeService(filepath.Join(t.TempDir(), "stories.jsonl"), target, nil, nil))
	if scheduler != nil {
		orch.SetNightPreparationScheduler(scheduler)
	}
	return (&Dependencies{idleChatOrch: orch}).handleIdleChatEpisodesPrepare()
}

func TestHandleIdleChatEpisodesPrepareRequiresSchedulerAdmission(t *testing.T) {
	handler := newStoryPrepareHandler(t, 3, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/episodes/prepare", strings.NewReader(`{"count":1}`))

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503 when night scheduler is unavailable", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"job_id"`) {
		t.Fatalf("response invented a job id: %s", recorder.Body.String())
	}
	var response struct {
		OK        bool   `json:"ok"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.OK || response.ErrorCode != "idlechat_episode_prepare_unavailable" {
		t.Fatalf("typed unavailable response = %+v, err=%v", response, err)
	}
}

func TestHandleIdleChatEpisodesPrepareRejectsExplicitZeroCount(t *testing.T) {
	handler := newStoryPrepareHandler(t, 3, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/episodes/prepare", strings.NewReader(`{"count":0}`))

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400 for explicit zero count", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"error_code":"idlechat_episode_prepare_count_invalid"`) {
		t.Fatalf("count rejection is not typed: %s", recorder.Body.String())
	}
}

func TestHandleIdleChatEpisodesPrepareRejectsTopLevelNull(t *testing.T) {
	handler := newStoryPrepareHandler(t, 3, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/episodes/prepare", strings.NewReader(`null`))

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400 for top-level null", recorder.Code, recorder.Body.String())
	}
	var response struct {
		OK        bool   `json:"ok"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.OK || response.ErrorCode != "idlechat_episode_prepare_request_invalid" {
		t.Fatalf("top-level null rejection = %+v err=%v body=%s", response, err, recorder.Body.String())
	}
}

func TestHandleIdleChatEpisodesPrepareRejectsUnsupportedCategories(t *testing.T) {
	handler := newStoryPrepareHandler(t, 3, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/episodes/prepare", strings.NewReader(`{"count":1,"categories":["folktale"]}`))

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400 for unsupported categories", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"error_code":"idlechat_episode_prepare_categories_unsupported"`) {
		t.Fatalf("category rejection is not typed: %s", recorder.Body.String())
	}
}

func TestHandleIdleChatEpisodesPrepareAcceptsDeferredMissingCountWithoutJobID(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 24, 1, 59, 0, 0, loc)
	dispatched := make(chan []string, 1)
	scheduler, err := idlechat.NewNightPreparationScheduler(
		idlechat.NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		func() time.Time { return now },
		func() bool { return false },
		func(_ context.Context, reasons []string) { dispatched <- reasons },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()
	handler := newStoryPrepareHandler(t, 3, scheduler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/episodes/prepare", strings.NewReader(`{}`))

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202 after scheduler admission", recorder.Code, recorder.Body.String())
	}
	var response struct {
		OK               bool   `json:"ok"`
		State            string `json:"state"`
		TargetReadyCount int    `json:"target_ready_count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.State != "deferred" || response.TargetReadyCount != 3 {
		t.Fatalf("response = %+v, want deferred target 3", response)
	}
	if strings.Contains(recorder.Body.String(), `"job_id"`) {
		t.Fatalf("response invented a job id: %s", recorder.Body.String())
	}
	select {
	case reasons := <-dispatched:
		t.Fatalf("deferred request dispatched outside the configured window: %v", reasons)
	default:
	}

	now = time.Date(2026, time.October, 24, 2, 0, 0, 0, loc)
	scheduler.Tick()
	select {
	case reasons := <-dispatched:
		if len(reasons) != 1 || reasons[0] != "viewer_story_prepare" {
			t.Fatalf("night owner reasons = %v, want fixed Story prepare reason", reasons)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred accepted request was not dispatched when the night window opened")
	}
}

func TestHandleIdleChatEpisodesPrepareReturnsReadyWhenOmittedCountHasNoMissingStock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stories.jsonl")
	artifact, err := json.Marshal(idlechat.StoryEpisodeArtifact{
		EpisodeID: "ready-episode", StoryTitle: "保存済みの題名",
		ProductionStatus: idlechat.StoryProductionReady,
		Validation:       idlechat.StoryValidationResult{Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(artifact, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	orch := idlechat.NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	orch.SetStoryEpisodeService(idlechat.NewPersistentStoryEpisodeService(path, 1, nil, nil))
	handler := (&Dependencies{idleChatOrch: orch}).handleIdleChatEpisodesPrepare()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/episodes/prepare", strings.NewReader(`{}`))

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 for ready stock", recorder.Code, recorder.Body.String())
	}
	var response struct {
		OK               bool   `json:"ok"`
		State            string `json:"state"`
		TargetReadyCount int    `json:"target_ready_count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.State != "ready" || response.TargetReadyCount != 1 {
		t.Fatalf("response = %+v, want ready target 1", response)
	}
}

func TestHandleIdleChatEpisodesPrepareRejectsClosedScheduler(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := idlechat.NewNightPreparationScheduler(
		idlechat.NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		func() time.Time { return time.Date(2026, time.October, 24, 3, 0, 0, 0, loc) },
		func() bool { return false },
		func(context.Context, []string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.Close()
	handler := newStoryPrepareHandler(t, 3, scheduler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/episodes/prepare", strings.NewReader(`{"count":1}`))

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503 for closed scheduler", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"error_code":"idlechat_episode_prepare_unavailable"`) {
		t.Fatalf("closed scheduler response is not typed: %s", recorder.Body.String())
	}
}
