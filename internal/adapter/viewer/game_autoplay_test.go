package viewer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

type controlledAutoplayLaunchTransport struct {
	started       chan struct{}
	allowResponse <-chan struct{}
	canceled      chan struct{}
}

func (rt controlledAutoplayLaunchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	close(rt.started)
	select {
	case <-request.Context().Done():
		close(rt.canceled)
		return nil, request.Context().Err()
	case <-rt.allowResponse:
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"ok":true,"game_id":"nethack","session_id":"auto_1","status":"launching"}`,
			)),
			Request: request,
		}, nil
	}
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

func TestGameAutoplayDefaultRosterIncludesAllFourPublicPersonas(t *testing.T) {
	service := NewGameAutoplayService(GameAutoplayOptions{
		Provider: &fakeAutoplayProvider{content: `{"play":false,"game_id":"","personas":[],"reason":"","next_check_minutes":60}`},
	})
	if service == nil {
		t.Fatal("service must not be nil")
	}
	want := []string{"mio", "shiro", "kuro", "midori"}
	if len(service.personas) != len(want) {
		t.Fatalf("default personas=%v want=%v", service.personas, want)
	}
	for i := range want {
		if service.personas[i] != want[i] {
			t.Fatalf("default personas[%d]=%q want=%q", i, service.personas[i], want[i])
		}
	}
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

func TestGameAutoplayIdleCompletesSubmittedLaunchAfterStopCancellation(t *testing.T) {
	observer, _ := fakeAutoplayObserver(t, "completed")
	started := make(chan struct{})
	allowResponse := make(chan struct{})
	requestCanceled := make(chan struct{})
	transport := controlledAutoplayLaunchTransport{started: started, allowResponse: allowResponse, canceled: requestCanceled}
	var releaseOnce sync.Once
	releaseObserver := func() { releaseOnce.Do(func() { close(allowResponse) }) }
	defer releaseObserver()
	service := NewGameAutoplayService(GameAutoplayOptions{
		Provider: &fakeAutoplayProvider{content: `{"play":true,"game_id":"nethack","personas":["mio"],"reason":"x","next_check_minutes":30}`},
		Launch: GameLaunchOptions{
			ObserverBaseURL: observer.URL,
			HTTPClient:      &http.Client{Timeout: 2 * time.Second, Transport: transport},
		},
		Personas: []string{"mio"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan time.Duration, 1)
	go func() { done <- service.RunOnceIdle(ctx) }()
	<-started

	// The observer has received the launch request. A stop at this point must
	// not discard its response or the session ID CORE needs to retain.
	cancel()
	select {
	case <-requestCanceled:
		t.Fatal("submitted idle launch request was canceled")
	case <-time.After(50 * time.Millisecond):
	}
	releaseObserver()
	select {
	case delay := <-done:
		if delay != 30*time.Minute {
			t.Fatalf("RunOnceIdle() delay = %s, want 30m", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("RunOnceIdle() did not finish after observer response")
	}
	select {
	case <-requestCanceled:
		t.Fatal("submitted idle launch request was canceled")
	default:
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.dayCount != 1 || service.lastSessionID != "auto_1" {
		t.Fatalf("submitted launch receipt lost: count=%d session=%q", service.dayCount, service.lastSessionID)
	}
}

func TestGameAutoplayLegacyRunOnceStillCancelsLaunchRequest(t *testing.T) {
	observer, _ := fakeAutoplayObserver(t, "completed")
	started := make(chan struct{})
	allowResponse := make(chan struct{})
	requestCanceled := make(chan struct{})
	transport := controlledAutoplayLaunchTransport{started: started, allowResponse: allowResponse, canceled: requestCanceled}
	var releaseOnce sync.Once
	releaseObserver := func() { releaseOnce.Do(func() { close(allowResponse) }) }
	defer releaseObserver()
	service := NewGameAutoplayService(GameAutoplayOptions{
		Provider: &fakeAutoplayProvider{content: `{"play":true,"game_id":"nethack","personas":["mio"],"reason":"x","next_check_minutes":30}`},
		Launch: GameLaunchOptions{
			ObserverBaseURL: observer.URL,
			HTTPClient:      &http.Client{Timeout: 2 * time.Second, Transport: transport},
		},
		Personas: []string{"mio"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan time.Duration, 1)
	go func() { done <- service.RunOnce(ctx) }()
	<-started
	cancel()
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("legacy RunOnce() did not cancel its in-flight launch request")
	}
	releaseObserver()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunOnce() did not finish after cancellation")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.dayCount != 0 || service.lastSessionID != "" {
		t.Fatalf("legacy canceled launch unexpectedly recorded: count=%d session=%q", service.dayCount, service.lastSessionID)
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
	// 明示ロースターに含まれない kuro は起動対象にしない。
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
