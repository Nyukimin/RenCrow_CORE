package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBuildIdleChatRuntimeWiresStoryPrepareDemandToNightScheduler(t *testing.T) {
	storageDir := t.TempDir()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	start := now.Add(-time.Minute).Format("15:04")
	end := now.Add(3 * time.Minute).Format("15:04")
	if start == end {
		t.Fatalf("test schedule unexpectedly has equal boundaries: %s", start)
	}
	enabled := true
	cfg := &config.Config{
		Session: config.SessionConfig{StorageDir: storageDir},
		IdleChat: config.IdleChatConfig{
			Enabled:      true,
			Participants: []string{"mio", "shiro"},
			IntervalMin:  60,
			IntervalSec:  3600,
			MaxTurns:     2,
			Temperature:  0.7,
			EpisodePreparation: config.IdleChatEpisodePreparationConfig{
				Enabled:     &enabled,
				ReadyTarget: 3,
				Schedule: config.IdleChatNightPreparationScheduleConfig{
					Timezone: "Asia/Tokyo",
					Start:    start,
					End:      end,
				},
			},
		},
	}
	deps := &Dependencies{}
	buildIdleChatRuntime(cfg, deps, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if deps.idleChatOrch == nil {
		t.Fatal("IdleChat runtime was not constructed")
	}
	t.Cleanup(deps.idleChatOrch.Stop)
	if got := deps.idleChatOrch.StoryEpisodeStockSnapshot(); !got.Enabled || got.Missing != 3 {
		t.Fatalf("runtime Story stock = %+v, want enabled with three missing episodes", got)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/idlechat/episodes/prepare", strings.NewReader(`{"count":1}`))
	deps.handleIdleChatEpisodesPrepare().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("prepare status=%d body=%s, want accepted by runtime-wired scheduler", recorder.Code, recorder.Body.String())
	}
	var response struct {
		State            string `json:"state"`
		TargetReadyCount int    `json:"target_ready_count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.State != "deferred" || response.TargetReadyCount != 1 {
		t.Fatalf("runtime response = %+v, want deferred absolute target 1", response)
	}
	state, accepted := deps.idleChatOrch.RequestNightStoryPreparation(2)
	if !accepted || state != "deferred" {
		t.Fatalf("runtime scheduler coalescing request = (%q, %t), want accepted/deferred", state, accepted)
	}
}
