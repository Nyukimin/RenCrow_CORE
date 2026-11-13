package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	eventpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskEnqueueTestToken = "0123456789abcdef0123456789abcdef"

func TestTaskEnqueueHandlerPersistsFixedPrivateGoalAndReturnsOriginalReceipt(t *testing.T) {
	owner, queue, task := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "mio", Priority: domaintask.PriorityHigh,
		Title: "authored title must stay in the Task owner",
	}, domaintask.SharedRoleContext{UserIntent: "authored context must stay in the Task owner"})
	handler := &taskEnqueueHandler{ownerID: "ren", token: []byte(taskEnqueueTestToken), tasks: owner, queue: queue, agentSchedulerEnabled: true}
	before, err := owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID}); err != nil || len(runs) != 0 {
		t.Fatalf("pre-enqueue Runs=%#v err=%v, want none", runs, err)
	}

	first := serveTaskEnqueueRequest(handler, http.MethodPost, "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(task.TaskID)+`"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first enqueue status=%d body=%s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("first enqueue Cache-Control=%q, want no-store", got)
	}
	var firstReceipt taskEnqueueReceipt
	if err := json.Unmarshal(first.Body.Bytes(), &firstReceipt); err != nil {
		t.Fatal(err)
	}
	if firstReceipt.Status != "enqueued" || firstReceipt.TaskID != task.TaskID || firstReceipt.QueueStatus != "queued" || firstReceipt.QueueItemID.Validate() != nil {
		t.Fatalf("first receipt=%+v", firstReceipt)
	}
	items, err := queue.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("queue items=%#v err=%v, want exactly one", items, err)
	}
	item := items[0]
	if item.QueueItemID != firstReceipt.QueueItemID || item.TaskID != task.TaskID || item.RunStartReason != domaintask.RunStartReasonFirst ||
		item.Action != "chat" || item.Status != "queued" || item.Goal != domainsuperagent.RunQueueGoalPrivateFirstChat ||
		item.IdempotencyKey != domainsuperagent.RunQueueIntentPrivateFirstChat || item.Priority != 3 || !item.CreatedAt.Equal(task.CreatedAt) {
		t.Fatalf("queued item=%+v", item)
	}
	if strings.Contains(item.Goal, task.Title) || strings.Contains(item.Goal, "authored context") {
		t.Fatalf("public queue Goal contains authored Task content: %q", item.Goal)
	}
	after, err := owner.Get(context.Background(), task.TaskID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("Task changed during enqueue: after=%+v before=%+v err=%v", after, before, err)
	}
	if runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID}); err != nil || len(runs) != 0 {
		t.Fatalf("post-enqueue Runs=%#v err=%v, want none", runs, err)
	}

	completed := item
	completed.Status = "completed"
	completed.RunID = modulecore.NewRunID()
	completed.CompletedAt = time.Now().UTC()
	if err := queue.SaveRunQueueItem(context.Background(), completed); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Cancel(context.Background(), task.TaskID, "fixture terminal state"); err != nil {
		t.Fatalf("mark Task terminal for duplicate retry: %v", err)
	}
	terminal, err := owner.Get(context.Background(), task.TaskID)
	if err != nil || terminal.Status != domaintask.StatusCancelled {
		t.Fatalf("terminal Task=%+v err=%v", terminal, err)
	}
	duplicate := serveTaskEnqueueRequest(handler, http.MethodPost, "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(task.TaskID)+`"}`)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate enqueue status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	if got := duplicate.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("duplicate enqueue Cache-Control=%q, want no-store", got)
	}
	var duplicateReceipt taskEnqueueReceipt
	if err := json.Unmarshal(duplicate.Body.Bytes(), &duplicateReceipt); err != nil {
		t.Fatal(err)
	}
	if duplicateReceipt.Status != "existing" || duplicateReceipt.QueueItemID != firstReceipt.QueueItemID || duplicateReceipt.QueueStatus != "completed" {
		t.Fatalf("duplicate receipt=%+v, want existing completed receipt %s", duplicateReceipt, firstReceipt.QueueItemID)
	}
	items, err = queue.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].QueueItemID != firstReceipt.QueueItemID || items[0].Status != "completed" {
		t.Fatalf("queue after duplicate=%#v err=%v, want one unchanged completed receipt", items, err)
	}
	afterDuplicate, err := owner.Get(context.Background(), task.TaskID)
	if err != nil || !reflect.DeepEqual(afterDuplicate, terminal) {
		t.Fatalf("duplicate enqueue changed Task: after=%+v before=%+v err=%v", afterDuplicate, terminal, err)
	}
}

func TestTaskEnqueueHandlerAcceptsAdditionalPrivateChatActor(t *testing.T) {
	owner, queue, task := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "aka", Priority: domaintask.PriorityHigh,
		Title: "private CHAT task for a BOARD character",
	}, domaintask.SharedRoleContext{})
	handler := &taskEnqueueHandler{ownerID: "ren", token: []byte(taskEnqueueTestToken), tasks: owner, queue: queue, agentSchedulerEnabled: true}

	response := serveTaskEnqueueRequest(handler, http.MethodPost, "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(task.TaskID)+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("Aka enqueue status=%d body=%s, want private CHAT intent persisted", response.Code, response.Body.String())
	}
	items, err := queue.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("Aka queue=%#v err=%v, want exactly one persisted item", items, err)
	}
	item := items[0]
	if item.TaskID != task.TaskID || item.RunStartReason != domaintask.RunStartReasonFirst || item.Goal != domainsuperagent.RunQueueGoalPrivateFirstChat ||
		item.Action != "chat" || item.Status != "queued" || item.IdempotencyKey != domainsuperagent.RunQueueIntentPrivateFirstChat {
		t.Fatalf("Aka private queue item=%+v, want the shared private first-CHAT contract", item)
	}
}

func TestTaskEnqueueHandlerRequiresHeartbeatAndOwnerEligibility(t *testing.T) {
	owner, queue, task := newTaskEnqueueFixture(t, domaintask.Task{OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "shiro", Title: "eligible"}, domaintask.SharedRoleContext{})
	disabled := &taskEnqueueHandler{ownerID: "ren", token: []byte(taskEnqueueTestToken), tasks: owner, queue: queue}
	response := serveTaskEnqueueRequest(disabled, http.MethodPost, "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(task.TaskID)+`"}`)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "heartbeat_scheduler_disabled") {
		t.Fatalf("scheduler-disabled enqueue status=%d body=%s", response.Code, response.Body.String())
	}
	items, err := queue.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("scheduler-disabled queue=%#v err=%v, want no insert", items, err)
	}

	base := task
	for _, tc := range []struct {
		name   string
		owner  string
		mutate func(*domaintask.Task)
	}{
		{name: "wrong owner", owner: "other"},
		{name: "wrong route", owner: "ren", mutate: func(value *domaintask.Task) { value.Route = domaintask.RouteGeneral }},
		{name: "noncanonical assignee", owner: "ren", mutate: func(value *domaintask.Task) { value.Assignee = "Mio" }},
		{name: "external session", owner: "ren", mutate: func(value *domaintask.Task) { value.OriginSessionID = modulecore.NewSessionID() }},
		{name: "Kuro outside private CHAT scope", owner: "ren", mutate: func(value *domaintask.Task) { value.Assignee = "kuro"; value.Route = domaintask.RouteGeneral }},
		{name: "parent task", owner: "ren", mutate: func(value *domaintask.Task) { value.ParentTaskID = modulecore.NewTaskID() }},
		{name: "accepted OPS claim", owner: "ren", mutate: func(value *domaintask.Task) { value.AcceptedOPSClaim = &domaintask.AcceptedOPSClaim{} }},
		{name: "native resume claim", owner: "ren", mutate: func(value *domaintask.Task) { value.NativeResumeClaims = []domaintask.NativeOPSResumeClaim{{}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			if err := validateTaskEnqueueEligibility(candidate, tc.owner); err == nil {
				t.Fatal("ineligible Task passed private enqueue scope")
			}
		})
	}
}

func TestTaskEnqueueHandlerAcceptsKuroOnlyWithinPrivateChatScope(t *testing.T) {
	owner, queue, task := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "kuro", Title: "private Kuro task",
	}, domaintask.SharedRoleContext{})
	handler := &taskEnqueueHandler{ownerID: "ren", token: []byte(taskEnqueueTestToken), tasks: owner, queue: queue, agentSchedulerEnabled: true}

	response := serveTaskEnqueueRequest(handler, http.MethodPost, "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(task.TaskID)+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("Kuro private CHAT enqueue status=%d body=%s, want private queue receipt", response.Code, response.Body.String())
	}
	items, err := queue.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].TaskID != task.TaskID || items[0].Goal != domainsuperagent.RunQueueGoalPrivateFirstChat || items[0].Action != "chat" {
		t.Fatalf("Kuro queue=%#v err=%v, want only the bounded private CHAT item", items, err)
	}
}

func TestTaskEnqueueRouteIsRegisteredAndLoopbackOnly(t *testing.T) {
	owner, queue, task := newTaskEnqueueFixture(t, domaintask.Task{OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "midori", Title: "private route task"}, domaintask.SharedRoleContext{})
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(taskEnqueueTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{WorkspaceDir: t.TempDir(), LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}, Heartbeat: config.HeartbeatConfig{AgentSchedulerEnabled: true}}
	events, err := eventpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	runtimeStore := composeRuntimeSuperAgentStore(queue, events)
	handler, err := newConfiguredTaskEnqueueHandler(cfg, owner, runtimeStore)
	if err != nil || handler == nil {
		t.Fatalf("newConfiguredTaskEnqueueHandler()=(%v,%v)", handler, err)
	}
	mux := http.NewServeMux()
	registerOpsRoutes(mux, cfg, &Dependencies{taskEnqueue: handler})

	remote := makeTaskEnqueueRequest(http.MethodPost, "203.0.113.9:1234", taskEnqueueTestToken, task.TaskID)
	remoteResponse := httptest.NewRecorder()
	mux.ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusNotFound {
		t.Fatalf("remote enqueue status=%d body=%s, want 404", remoteResponse.Code, remoteResponse.Body.String())
	}
	unauthorized := makeTaskEnqueueRequest(http.MethodPost, "127.0.0.1:1234", "", task.TaskID)
	unauthorizedResponse := httptest.NewRecorder()
	mux.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized enqueue status=%d body=%s, want 401", unauthorizedResponse.Code, unauthorizedResponse.Body.String())
	}
	local := makeTaskEnqueueRequest(http.MethodPost, "127.0.0.1:1234", taskEnqueueTestToken, task.TaskID)
	localResponse := httptest.NewRecorder()
	mux.ServeHTTP(localResponse, local)
	if localResponse.Code != http.StatusOK {
		t.Fatalf("loopback enqueue status=%d body=%s", localResponse.Code, localResponse.Body.String())
	}
	items, err := queue.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("queue after route requests=%#v err=%v, want one item", items, err)
	}
}

func TestParseTasksEnqueueArgsRequiresOneCanonicalTaskID(t *testing.T) {
	taskID := modulecore.NewTaskID()
	parsed, compact, err := parseTasksEnqueueArgs([]string{"enqueue", string(taskID)})
	if err != nil || parsed != taskID || compact {
		t.Fatalf("parseTasksEnqueueArgs()=(%q,%v,%v)", parsed, compact, err)
	}
	for _, args := range [][]string{
		{"enqueue"}, {"enqueue", "tsk_legacy"}, {"enqueue", string(taskID), "extra"}, {"enqueue", "--json", string(taskID)},
	} {
		if _, _, err := parseTasksEnqueueArgs(args); err == nil {
			t.Fatalf("parseTasksEnqueueArgs(%v) accepted invalid input", args)
		}
	}
}

func TestRunTasksEnqueueRequestUsesConfiguredLoopbackPortAndSendsOnlyTaskID(t *testing.T) {
	taskID := modulecore.NewTaskID()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != tasksEnqueuePath || r.Header.Get("Authorization") != "Bearer "+taskEnqueueTestToken {
			t.Errorf("request method/path/auth=%s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.RemoteAddr == "" || !strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") {
			t.Errorf("request remote addr=%q, want direct loopback", r.RemoteAddr)
		}
		var fields map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(fields) != 1 || string(fields["task_id"]) != `"`+string(taskID)+`"` {
			t.Errorf("request fields=%s, want only canonical task_id", mustMarshalTest(t, fields))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(taskEnqueueReceipt{
			Status: "enqueued", TaskID: taskID, QueueItemID: modulecore.NewQueueItemID(), QueueStatus: "queued",
			Priority: 2, CreatedAt: time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC),
		})
	}))
	defer server.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscan(portText, &port); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true}, Server: config.ServerConfig{Host: "0.0.0.0", Port: port}}
	var stdout, stderr bytes.Buffer
	code := runTasksEnqueueRequest(taskID, false, cfg, []byte(taskEnqueueTestToken), server.Client(), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var receipt taskEnqueueReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.TaskID != taskID || receipt.Status != "enqueued" {
		t.Fatalf("CLI receipt=%+v parse_err=%v output=%s", receipt, err, stdout.String())
	}
}

func TestRunTasksEnqueueRequestReportsCOREFailureCode(t *testing.T) {
	taskID := modulecore.NewTaskID()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"heartbeat_scheduler_disabled"}`))
	}))
	defer server.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscan(portText, &port); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true}, Server: config.ServerConfig{Port: port}}
	var stdout, stderr bytes.Buffer
	code := runTasksEnqueueRequest(taskID, false, cfg, []byte(taskEnqueueTestToken), server.Client(), &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "heartbeat_scheduler_disabled") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunTasksEnqueueRequestDoesNotFollowRedirectOrForwardBearer(t *testing.T) {
	var targetHits int
	var forwardedAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		forwardedAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"enqueued"}`))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+taskEnqueueTestToken {
			t.Errorf("CORE request Authorization=%q, want configured local bearer", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscan(portText, &port); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true}, Server: config.ServerConfig{Port: port}}
	var stdout, stderr bytes.Buffer
	client := &http.Client{Timeout: 2 * time.Second}
	code := runTasksEnqueueRequest(modulecore.NewTaskID(), false, cfg, []byte(taskEnqueueTestToken), client, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "HTTP 302") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if targetHits != 0 || forwardedAuthorization != "" {
		t.Fatalf("redirect target hits=%d authorization=%q, want no follow and no token forwarding", targetHits, forwardedAuthorization)
	}
}

func newTaskEnqueueFixture(t *testing.T, draft domaintask.Task, shared domaintask.SharedRoleContext) (*taskmanager.Manager, *superagentpersistence.JSONLStore, domaintask.Task) {
	t.Helper()
	root := t.TempDir()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	owner := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close TaskManager: %v", err)
		}
	})
	queue := superagentpersistence.NewJSONLStore(filepath.Join(root, "superagent"), 3000)
	draft.Title = strings.TrimSpace(draft.Title)
	if draft.Title == "" {
		draft.Title = "test Task"
	}
	task, err := owner.Create(context.Background(), draft, shared)
	if err != nil {
		t.Fatalf("create fixture Task: %v", err)
	}
	return owner, queue, task
}

func serveTaskEnqueueRequest(handler http.Handler, method, remoteAddr, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, tasksEnqueuePath, strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	req.Host = "127.0.0.1"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func makeTaskEnqueueRequest(method, remoteAddr, token string, taskID modulecore.TaskID) *http.Request {
	req := httptest.NewRequest(method, tasksEnqueuePath, strings.NewReader(`{"task_id":"`+string(taskID)+`"}`))
	req.RemoteAddr = remoteAddr
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func mustMarshalTest(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
