package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type fakeAutoplayProvider struct {
	content string
	err     error
	asked   int
}

func (p *fakeAutoplayProvider) Name() string { return "fake" }

func (p *fakeAutoplayProvider) Generate(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.asked++
	if p.err != nil {
		return llm.GenerateResponse{}, p.err
	}
	return llm.GenerateResponse{Content: p.content}, nil
}

// fakeAutoplayObserver は launch / session status / leaderboard を持つ
// 最小の observer もどき。
func fakeAutoplayObserver(t *testing.T, sessionStatus string) (*httptest.Server, *int) {
	t.Helper()
	launches := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/games/launch", func(w http.ResponseWriter, r *http.Request) {
		launches++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "game_id": "nethack", "session_id": "auto_1", "status": "launching",
		})
	})
	mux.HandleFunc("/games/sessions/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "session": map[string]any{"status": sessionStatus},
		})
	})
	mux.HandleFunc("/games/leaderboard", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "entries": []map[string]any{}})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &launches
}

func autoplayServiceForTest(t *testing.T, provider llm.LLMProvider, observerURL string) *GameAutoplayService {
	t.Helper()
	service := NewGameAutoplayService(GameAutoplayOptions{
		Provider: provider,
		Launch:   GameLaunchOptions{ObserverBaseURL: observerURL},
		Personas: []string{"mio", "shiro", "midori"},
	})
	if service == nil {
		t.Fatal("service must not be nil")
	}
	return service
}

func TestGameAutoplayPlayFalseSchedulesLLMChosenDelay(t *testing.T) {
	observer, launches := fakeAutoplayObserver(t, "completed")
	provider := &fakeAutoplayProvider{content: `{"play":false,"game_id":"","personas":[],"reason":"","next_check_minutes":120}`}
	service := autoplayServiceForTest(t, provider, observer.URL)

	delay := service.RunOnce(context.Background())
	if delay != 120*time.Minute {
		t.Fatalf("delay=%s want 120m", delay)
	}
	if *launches != 0 {
		t.Fatalf("play=false must not launch: %d", *launches)
	}
}

func TestGameAutoplayPlayTrueLaunchesAndCounts(t *testing.T) {
	observer, launches := fakeAutoplayObserver(t, "completed")
	provider := &fakeAutoplayProvider{content: `{"play":true,"game_id":"nethack","personas":["mio"],"reason":"スコアを取り返したい","next_check_minutes":30}`}
	service := autoplayServiceForTest(t, provider, observer.URL)

	delay := service.RunOnce(context.Background())
	if delay != 30*time.Minute {
		t.Fatalf("delay=%s want 30m", delay)
	}
	if *launches != 1 {
		t.Fatalf("launch count=%d want 1", *launches)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.dayCount != 1 || service.lastSessionID != "auto_1" {
		t.Fatalf("launch must be recorded: count=%d session=%s", service.dayCount, service.lastSessionID)
	}
}

func TestGameAutoplayClampsNextCheck(t *testing.T) {
	observer, _ := fakeAutoplayObserver(t, "completed")
	provider := &fakeAutoplayProvider{content: `{"play":false,"game_id":"","personas":[],"reason":"","next_check_minutes":1}`}
	service := autoplayServiceForTest(t, provider, observer.URL)
	if delay := service.RunOnce(context.Background()); delay != gameAutoplayMinCheck {
		t.Fatalf("small value must clamp to %s: %s", gameAutoplayMinCheck, delay)
	}
	provider.content = `{"play":false,"game_id":"","personas":[],"reason":"","next_check_minutes":99999}`
	if delay := service.RunOnce(context.Background()); delay != gameAutoplayMaxCheck {
		t.Fatalf("huge value must clamp to %s: %s", gameAutoplayMaxCheck, delay)
	}
}

func TestGameAutoplayBrokenJSONFallsBackToDefault(t *testing.T) {
	observer, launches := fakeAutoplayObserver(t, "completed")
	provider := &fakeAutoplayProvider{content: `遊びたい！`}
	service := autoplayServiceForTest(t, provider, observer.URL)
	if delay := service.RunOnce(context.Background()); delay != gameAutoplayDefaultCheck {
		t.Fatalf("broken json must fall back to default: %s", delay)
	}
	if *launches != 0 {
		t.Fatalf("broken json must not launch: %d", *launches)
	}
}

func TestGameAutoplayFiltersPersonasToRoster(t *testing.T) {
	observer, launches := fakeAutoplayObserver(t, "completed")
	// kuro はロースター外 (docs/10: 常時使えるのは mio/shiro/midori)。
	provider := &fakeAutoplayProvider{content: `{"play":true,"game_id":"nethack","personas":["kuro"],"reason":"x","next_check_minutes":30}`}
	service := autoplayServiceForTest(t, provider, observer.URL)
	service.RunOnce(context.Background())
	if *launches != 0 {
		t.Fatalf("roster-only filter must skip launch: %d", *launches)
	}
}

func TestGameAutoplayRespectsDailyCap(t *testing.T) {
	observer, launches := fakeAutoplayObserver(t, "completed")
	provider := &fakeAutoplayProvider{content: `{"play":true,"game_id":"nethack","personas":["mio"],"reason":"x","next_check_minutes":30}`}
	service := NewGameAutoplayService(GameAutoplayOptions{
		Provider:          provider,
		Launch:            GameLaunchOptions{ObserverBaseURL: observer.URL},
		Personas:          []string{"mio"},
		MaxSessionsPerDay: 1,
	})
	service.RunOnce(context.Background())
	// 2 回目: 上限到達。ただし直前セッションは completed なので busy ではない。
	service.mu.Lock()
	service.lastSessionID = "" // busy 判定を外し、cap だけを検証する
	service.mu.Unlock()
	if delay := service.RunOnce(context.Background()); delay != gameAutoplayDefaultCheck {
		t.Fatalf("cap must return default delay: %s", delay)
	}
	if *launches != 1 {
		t.Fatalf("daily cap must block second launch: %d", *launches)
	}
	if provider.asked != 1 {
		t.Fatalf("cap must skip asking the LLM: asked=%d", provider.asked)
	}
}

func TestGameAutoplaySkipsWhileSessionRunning(t *testing.T) {
	observer, launches := fakeAutoplayObserver(t, "running")
	provider := &fakeAutoplayProvider{content: `{"play":true,"game_id":"nethack","personas":["mio"],"reason":"x","next_check_minutes":30}`}
	service := autoplayServiceForTest(t, provider, observer.URL)
	service.mu.Lock()
	service.lastSessionID = "auto_running"
	service.mu.Unlock()

	if delay := service.RunOnce(context.Background()); delay != gameAutoplayBusyRetry {
		t.Fatalf("running session must return busy retry: %s", delay)
	}
	if *launches != 0 || provider.asked != 0 {
		t.Fatalf("running session must skip ask/launch: launches=%d asked=%d", *launches, provider.asked)
	}
}

func TestGameAutoplayStartStopLifecycle(t *testing.T) {
	observer, _ := fakeAutoplayObserver(t, "completed")
	provider := &fakeAutoplayProvider{content: `{"play":false,"game_id":"","personas":[],"reason":"","next_check_minutes":60}`}
	service := autoplayServiceForTest(t, provider, observer.URL)
	service.Start()
	service.Start() // 二重 Start は無視
	service.Stop()
	service.Stop() // 冪等
}
