package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLivingHeartbeatHandlerProjectsOnlyPublicActivityFields(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	workStarted := make(chan struct{})
	scheduler := agentheartbeat.New([]string{"mio"}, agentheartbeat.Sources{
		Board: func(context.Context, string) (agentheartbeat.Work, bool, error) {
			return agentheartbeat.Work{
				ID: "queue_id=secret-queue prompt=secret-prompt board_text=secret-board-body",
				Run: func(runCtx context.Context, _ <-chan struct{}) error {
					close(workStarted)
					<-runCtx.Done()
					return nil
				},
			}, true, nil
		},
	}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		waitLivingScheduler(t, scheduler)
	})
	select {
	case <-workStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled work did not start")
	}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	HandleLivingHeartbeat(scheduler, nil, nil).ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", rec.Header().Get("Cache-Control"))
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("top-level fields=%v, want only observed_at and agents", payload)
	}
	observedAt, ok := payload["observed_at"].(string)
	if !ok {
		t.Fatalf("observed_at=%v, want RFC3339 timestamp", payload["observed_at"])
	}
	if _, err := time.Parse(time.RFC3339Nano, observedAt); err != nil {
		t.Fatalf("observed_at=%q is not RFC3339: %v", observedAt, err)
	}
	agents, ok := payload["agents"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("agents=%v, want one actor", payload["agents"])
	}
	agent, ok := agents[0].(map[string]any)
	if !ok || len(agent) != 3 || agent["actor_id"] != "mio" || agent["state"] != "running" || agent["activity_kind"] != "board" {
		t.Fatalf("agent=%v, want only mio/running/board", agents[0])
	}
	for _, forbidden := range []string{"secret-queue", "secret-prompt", "secret-board-body", "title", "work_id", "error"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(forbidden)) {
			t.Fatalf("response leaked %q: %s", forbidden, rec.Body.String())
		}
	}

	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
	waitLivingScheduler(t, scheduler)
}

func TestLivingHeartbeatHandlerRejectsNonCanonicalOrNonLocalRequests(t *testing.T) {
	tests := []struct {
		name   string
		method string
		url    string
		remote string
		host   string
		header string
		want   int
	}{
		{name: "valid but unavailable", method: http.MethodGet, url: "http://127.0.0.1:18790/viewer/living/heartbeat", remote: "127.0.0.1:41000", want: http.StatusServiceUnavailable},
		{name: "external remote", method: http.MethodGet, url: "http://127.0.0.1:18790/viewer/living/heartbeat", remote: "192.0.2.10:41000", want: http.StatusNotFound},
		{name: "hostname remote is not numeric", method: http.MethodGet, url: "http://127.0.0.1:18790/viewer/living/heartbeat", remote: "localhost:41000", want: http.StatusNotFound},
		{name: "forwarded loopback", method: http.MethodGet, url: "http://127.0.0.1:18790/viewer/living/heartbeat", remote: "127.0.0.1:41000", header: "X-Forwarded-For", want: http.StatusNotFound},
		{name: "non-loopback host authority", method: http.MethodGet, url: "http://example.com/viewer/living/heartbeat", remote: "127.0.0.1:41000", want: http.StatusNotFound},
		{name: "query is rejected", method: http.MethodGet, url: "http://127.0.0.1:18790/viewer/living/heartbeat?actor=mio", remote: "127.0.0.1:41000", want: http.StatusNotFound},
		{name: "empty query is rejected", method: http.MethodGet, url: "http://127.0.0.1:18790/viewer/living/heartbeat?", remote: "127.0.0.1:41000", want: http.StatusNotFound},
		{name: "alternate path is rejected", method: http.MethodGet, url: "http://127.0.0.1:18790/viewer/living/heartbeat/", remote: "127.0.0.1:41000", want: http.StatusNotFound},
		{name: "method is GET only", method: http.MethodPost, url: "http://127.0.0.1:18790/viewer/living/heartbeat", remote: "127.0.0.1:41000", want: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.url, nil)
			request.RemoteAddr = tt.remote
			if tt.host != "" {
				request.Host = tt.host
			}
			if tt.header != "" {
				request.Header.Set(tt.header, "192.0.2.20")
			}
			HandleLivingHeartbeat(nil, nil, nil).ServeHTTP(rec, request)
			if rec.Code != tt.want {
				t.Fatalf("status=%d body=%q want=%d", rec.Code, rec.Body.String(), tt.want)
			}
		})
	}
}

func TestLivingHeartbeatHandlerNeverReturnsIdleWhenSchedulerIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closedScheduler := agentheartbeat.New([]string{"mio"}, agentheartbeat.Sources{}, nil)
	started := make(chan error, 1)
	go func() { started <- closedScheduler.Start(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ready := closedScheduler.LivingSnapshot(context.Background(), nil); ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, ready := closedScheduler.LivingSnapshot(context.Background(), nil); !ready {
		cancel()
		t.Fatal("scheduler did not become ready before shutdown")
	}
	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not close")
	}
	waitLivingScheduler(t, closedScheduler)

	for _, tc := range []struct {
		name      string
		scheduler *agentheartbeat.Scheduler
	}{
		{name: "disabled nil owner"},
		{name: "unstarted owner", scheduler: agentheartbeat.New([]string{"mio"}, agentheartbeat.Sources{}, nil)},
		{name: "closed owner", scheduler: closedScheduler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
			request.RemoteAddr = "127.0.0.1:49152"
			HandleLivingHeartbeat(tc.scheduler, nil, nil).ServeHTTP(rec, request)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%q, want 503", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `"agents"`) {
				t.Fatalf("unavailable scheduler returned an idle-looking projection: %s", rec.Body.String())
			}
		})
	}
}

func TestLivingHeartbeatTitleRequiresPublicTaskVisibility(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "Public task title"}
	reader := &livingHeartbeatTaskReaderStub{task: task}
	workStarted := make(chan struct{})
	scheduler := agentheartbeat.New([]string{"mio"}, agentheartbeat.Sources{
		Private: func(context.Context, string) (agentheartbeat.Work, bool, error) {
			return agentheartbeat.Work{
				ID: "private-queue-id", VisibilityTaskID: task.TaskID,
				Run: func(runCtx context.Context, _ <-chan struct{}) error {
					close(workStarted)
					<-runCtx.Done()
					return nil
				},
			}, true, nil
		},
	}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		waitLivingScheduler(t, scheduler)
	})
	select {
	case <-workStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("private task did not start")
	}

	visibility := PublicTaskVisibility(func(_ context.Context, got domaintask.Task) (bool, error) {
		if got.TaskID != task.TaskID || got.Title != task.Title {
			t.Errorf("visibility received task=%+v, want canonical task %s", got, task.TaskID)
		}
		return false, nil
	})
	assertLivingHeartbeatHasTitle(t, HandleLivingHeartbeat(scheduler, reader, visibility), false)
	visibility = func(_ context.Context, got domaintask.Task) (bool, error) {
		if got.TaskID != task.TaskID || got.Title != task.Title {
			t.Errorf("visibility received task=%+v, want canonical task %s", got, task.TaskID)
		}
		return true, nil
	}
	assertLivingHeartbeatHasTitle(t, HandleLivingHeartbeat(scheduler, reader, visibility), true)
	if reader.lastTaskID != task.TaskID {
		t.Fatalf("read canonical TaskID=%q, want %q", reader.lastTaskID, task.TaskID)
	}

	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
	waitLivingScheduler(t, scheduler)
}

type livingHeartbeatTaskReaderStub struct {
	task       domaintask.Task
	lastTaskID modulecore.TaskID
}

func (s *livingHeartbeatTaskReaderStub) GetTask(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	s.lastTaskID = taskID
	if taskID != s.task.TaskID {
		return domaintask.Task{}, errors.New("unexpected task id")
	}
	return s.task, nil
}

func assertLivingHeartbeatHasTitle(t *testing.T, handler http.HandlerFunc, wantTitle bool) {
	t.Helper()
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	agents, ok := payload["agents"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("agents=%v, want one actor", payload["agents"])
	}
	agent, ok := agents[0].(map[string]any)
	if !ok {
		t.Fatalf("agent=%v, want object", agents[0])
	}
	title, gotTitle := agent["title"].(string)
	if gotTitle != wantTitle || (wantTitle && title != "Public task title") {
		t.Fatalf("title=%v present=%t, want title present=%t", agent["title"], gotTitle, wantTitle)
	}
}

func waitLivingScheduler(t *testing.T, scheduler *agentheartbeat.Scheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Wait(ctx); err != nil {
		t.Errorf("scheduler did not drain: %v", err)
	}
}
