package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestOnlineGameTaskCreateIsIdempotentWhileCanonicalWriterIsHeld(t *testing.T) {
	root := t.TempDir()
	taskRoot := filepath.Join(root, "tasks")
	taskStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := tasks.Close(); err != nil {
			t.Errorf("close TaskManager: %v", err)
		}
	})
	if _, err := taskpersistence.NewJSONLStore(taskRoot); err == nil {
		t.Fatal("second canonical Task writer opened while the fixture writer was held")
	}

	const token = taskEnqueueTestToken
	tokenPath := filepath.Join(root, "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	queue := superagentpersistence.NewJSONLStore(filepath.Join(root, "superagent"), 3000)
	waker := &taskEnqueueTestWaker{}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}, Heartbeat: config.HeartbeatConfig{AgentSchedulerEnabled: true}}
	createHandler, err := newConfiguredTaskCreateHandler(cfg, tasks)
	if err != nil || createHandler == nil {
		t.Fatalf("configure online Task create handler: handler=%v err=%v", createHandler, err)
	}
	enqueueHandler, err := newConfiguredTaskEnqueueHandler(cfg, tasks, queue, waker)
	if err != nil || enqueueHandler == nil {
		t.Fatalf("configure Task enqueue handler: handler=%v err=%v", enqueueHandler, err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST "+tasksCreatePath, createHandler)
	mux.Handle("POST "+tasksCreateLookupPath, createHandler)
	mux.Handle("POST "+tasksEnqueuePath, enqueueHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("read test server listener address %q: %v", server.Listener.Addr(), err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("read loopback test server port from %q: %v", server.URL, err)
	}
	cfg.Server.Port = port
	client := server.Client()

	request := taskCreateRequest{
		RequestID: modulecore.NewRequestID(), Title: "Herzog Zwei four-agent intake", Route: domaintask.RouteGAME,
		OwnerID: "ren", Assignee: "aka", Priority: domaintask.PriorityNormal,
		Plan:     "Play Herzog Zwei with the canonical four-agent roster.",
		GameSpec: &domaintask.GameSpec{GameID: "herzog_zwei", Personas: []string{"aka", "ao", "kin", "gin"}, Turns: 120, Mode: "realtime"},
	}

	lookupBefore := postTaskCreateJSON(t, client, server.URL+tasksCreateLookupPath, token, request)
	if lookupBefore.StatusCode != http.StatusNotFound {
		t.Fatalf("pre-create lookup status=%d body=%s; want receipt absent without creation", lookupBefore.StatusCode, lookupBefore.Body)
	}
	if got := listOnlineCreateTasks(t, tasks); len(got) != 0 {
		t.Fatalf("pre-create lookup created Tasks: %+v", got)
	}

	wrongOwner := request
	wrongOwner.OwnerID = "someone-else"
	if _, err := runTaskCreateRequest(context.Background(), cfg, []byte(token), client, wrongOwner); err == nil {
		t.Fatal("online create accepted an owner assertion different from authenticated owner")
	}
	if _, err := runTaskCreateRequest(context.Background(), cfg, []byte("incorrect-token"), client, request); err == nil {
		t.Fatal("online create accepted an incorrect owner token")
	}
	remoteResponse := serveTaskCreateRequest(t, createHandler, "192.0.2.10:12345", token, tasksCreatePath, request)
	if remoteResponse.Code < http.StatusBadRequest {
		t.Fatalf("non-loopback create status=%d body=%s; want rejection", remoteResponse.Code, remoteResponse.Body.String())
	}
	if got := listOnlineCreateTasks(t, tasks); len(got) != 0 {
		t.Fatalf("owner/auth/loopback rejection created Tasks: %+v", got)
	}

	created, err := runTaskCreateRequest(context.Background(), cfg, []byte(token), client, request)
	if err != nil {
		t.Fatalf("online GAME Task create: %v", err)
	}
	if created.RequestID != request.RequestID || created.TaskID.Validate() != nil || created.Task.TaskID != created.TaskID ||
		created.Task.Route != domaintask.RouteGAME || created.Task.OwnerID != "ren" || created.Task.Assignee != "aka" {
		t.Fatalf("create receipt does not identify the canonical GAME Task: %+v", created)
	}

	gotTask, err := tasks.Get(context.Background(), created.TaskID)
	if err != nil || gotTask.Status != domaintask.StatusQueued || gotTask.Route != domaintask.RouteGAME || gotTask.OwnerID != "ren" {
		t.Fatalf("stored Task=%+v err=%v; want queued owner-owned GAME Task awaiting enqueue", gotTask, err)
	}
	shared, err := taskStore.GetContext(context.Background(), created.TaskID)
	if err != nil || shared.GameSpec == nil || !reflect.DeepEqual(*shared.GameSpec, *request.GameSpec) || shared.UserIntent != request.Title || shared.CurrentPlan != request.Plan ||
		shared.GameProgress == nil || shared.GameProgress.Status != domaintask.GameExecutionPrepared || shared.GameProgress.SessionID != "" || shared.GameProgress.ControllerRunID != "" {
		t.Fatalf("stored context=%+v err=%v; want immutable game spec and request plan", shared, err)
	}
	if runs, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: created.TaskID}); err != nil || len(runs) != 0 {
		t.Fatalf("pre-enqueue Runs=%+v err=%v; create must not start a Run", runs, err)
	}
	reader, err := taskpersistence.NewJSONLReader(taskRoot)
	if err != nil {
		t.Fatalf("open show/list-style read-only Task reader while writer is held: %v", err)
	}
	readTask, readErr := reader.GetTask(context.Background(), created.TaskID)
	readContext, contextErr := reader.GetContext(context.Background(), created.TaskID)
	if err := reader.Close(); err != nil {
		t.Errorf("close read-only Task reader: %v", err)
	}
	if readErr != nil || contextErr != nil || readTask.TaskID != created.TaskID || readContext.GameSpec == nil || !reflect.DeepEqual(*readContext.GameSpec, *request.GameSpec) {
		t.Fatalf("read-only Task view task=%+v context=%+v errors=(%v,%v)", readTask, readContext, readErr, contextErr)
	}

	replayed, err := runTaskCreateRequest(context.Background(), cfg, []byte(token), client, request)
	if err != nil || replayed.RequestID != created.RequestID || replayed.TaskID != created.TaskID || !reflect.DeepEqual(replayed.Task, created.Task) {
		t.Fatalf("same-key create replay=%+v err=%v; want identical original receipt", replayed, err)
	}
	lookupAfter := postTaskCreateJSON(t, client, server.URL+tasksCreateLookupPath, token, request)
	if lookupAfter.StatusCode != http.StatusOK {
		t.Fatalf("post-create lookup status=%d body=%s", lookupAfter.StatusCode, lookupAfter.Body)
	}
	var lookedUp taskCreateReceipt
	if err := decodeTaskCreateResponse(bytes.NewReader(lookupAfter.Body), &lookedUp); err != nil || lookedUp.RequestID != created.RequestID || lookedUp.TaskID != created.TaskID || !reflect.DeepEqual(lookedUp.Task, created.Task) {
		t.Fatalf("lookup receipt=%+v err=%v; want original receipt", lookedUp, err)
	}
	changed := request
	changed.Title = "changed title under the original request key"
	conflict := postTaskCreateJSON(t, client, server.URL+tasksCreatePath, token, changed)
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("changed-body create status=%d body=%s; want conflict", conflict.StatusCode, conflict.Body)
	}
	if got := listOnlineCreateTasks(t, tasks); len(got) != 1 || got[0].TaskID != created.TaskID {
		t.Fatalf("create replay/conflict produced extra Tasks: %+v", got)
	}

	var enqueueOut, enqueueErr bytes.Buffer
	if code := runTasksEnqueueRequest(created.TaskID, false, cfg, []byte(token), client, &enqueueOut, &enqueueErr); code != 0 {
		t.Fatalf("first online enqueue exit=%d stdout=%q stderr=%q", code, enqueueOut.String(), enqueueErr.String())
	}
	firstEnqueue := enqueueOut.String()
	enqueueOut.Reset()
	enqueueErr.Reset()
	if code := runTasksEnqueueRequest(created.TaskID, false, cfg, []byte(token), client, &enqueueOut, &enqueueErr); code != 0 {
		t.Fatalf("duplicate online enqueue exit=%d stdout=%q stderr=%q", code, enqueueOut.String(), enqueueErr.String())
	}
	queueItems, err := queue.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(queueItems) != 1 || queueItems[0].TaskID != created.TaskID || queueItems[0].Status != "queued" ||
		!strings.Contains(firstEnqueue, string(created.TaskID)) || len(waker.actors) != 2 || waker.actors[0] != "aka" || waker.actors[1] != "aka" {
		t.Fatalf("enqueue queue=%+v waker=%v first=%q duplicate=%q err=%v", queueItems, waker.actors, firstEnqueue, enqueueOut.String(), err)
	}
	if runs, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: created.TaskID}); err != nil || len(runs) != 0 {
		t.Fatalf("Runs after queue-only enqueue=%+v err=%v; enqueue must defer execution to controller", runs, err)
	}
}

type taskCreateTestResponse struct {
	StatusCode int
	Body       []byte
}

func postTaskCreateJSON(t *testing.T, client *http.Client, endpoint, token string, request taskCreateRequest) taskCreateTestResponse {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := client.Do(httpRequest)
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read POST %s response: %v", endpoint, err)
	}
	return taskCreateTestResponse{StatusCode: response.StatusCode, Body: body}
}

func serveTaskCreateRequest(t *testing.T, handler http.Handler, remoteAddr, token, path string, request taskCreateRequest) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	req.RemoteAddr = remoteAddr
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func listOnlineCreateTasks(t *testing.T, tasks *taskmanager.Manager) []domaintask.Task {
	t.Helper()
	got, err := tasks.List(context.Background(), domaintask.Filter{})
	if err != nil {
		t.Fatalf("list Tasks after online create operation: %v", err)
	}
	return got
}
