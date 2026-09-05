package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func TestTaskViewerRoutesExposeCanonicalTaskIdentity(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	value, err := manager.Create(context.Background(), domaintask.Task{Title: "viewer Task", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{CurrentPlan: "show it"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), value.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(context.Background(), value.TaskID, "done"); err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{name: "list", handler: HandleTasks(store), path: "/viewer/tasks"},
		{name: "detail", handler: HandleTaskDetail(store), path: "/viewer/task/detail?task_id=" + string(value.TaskID)},
		{name: "notifications", handler: HandleTaskNotifications(store), path: "/viewer/task-notifications?interrupt_only=false"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			check.handler(recorder, httptest.NewRequest(http.MethodGet, check.path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			if !strings.Contains(body, string(value.TaskID)) || strings.Contains(body, `"job_id"`) {
				t.Fatalf("non-canonical Task response: %s", body)
			}
		})
	}
}

type taskDetailResponse struct {
	Task    domaintask.Task              `json:"task"`
	Runs    []domaintask.Run             `json:"runs"`
	Context domaintask.SharedRoleContext `json:"context"`
}

type taskDetailStore struct {
	TaskStore
	listRuns      func(context.Context, domaintask.RunFilter) ([]domaintask.Run, error)
	listRunsErr   error
	returnNilRuns bool
	lastRunFilter domaintask.RunFilter
}

func (s *taskDetailStore) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	s.lastRunFilter = filter
	if s.listRunsErr != nil {
		return nil, s.listRunsErr
	}
	if s.returnNilRuns {
		return nil, nil
	}
	return s.listRuns(ctx, filter)
}

func TestTaskDetailIncludesCanonicalRunHistory(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	task, err := manager.Create(ctx, domaintask.Task{Title: "viewer run history", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "first attempt"); err != nil {
		t.Fatal(err)
	}
	firstRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(firstRuns) != 1 {
		t.Fatalf("first runs=%#v err=%v", firstRuns, err)
	}
	firstRunID := firstRuns[0].RunID
	if _, err := manager.Start(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "rerun attempt"); err != nil {
		t.Fatal(err)
	}

	detailStore := &taskDetailStore{TaskStore: store, listRuns: store.ListRuns}
	recorder := httptest.NewRecorder()
	HandleTaskDetail(detailStore)(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(task.TaskID), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if detailStore.lastRunFilter.TaskID != task.TaskID {
		t.Fatalf("ListRuns filter=%#v want task_id=%s", detailStore.lastRunFilter, task.TaskID)
	}
	if strings.Contains(recorder.Body.String(), `"job_id"`) || strings.Contains(recorder.Body.String(), `"parent_run_id"`) {
		t.Fatalf("legacy run identity in detail response: %s", recorder.Body.String())
	}
	var response taskDetailResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Runs == nil || len(response.Runs) != 2 {
		t.Fatalf("runs=%#v", response.Runs)
	}
	if response.Task.TaskID != task.TaskID {
		t.Fatalf("task_id=%s want %s", response.Task.TaskID, task.TaskID)
	}
	if response.Runs[0].StartedAt.After(response.Runs[1].StartedAt) {
		t.Fatalf("runs are not chronological: %#v", response.Runs)
	}
	var first, rerun *domaintask.Run
	for index := range response.Runs {
		item := response.Runs[index]
		if item.TaskID != task.TaskID {
			t.Fatalf("run[%d] task_id=%s want %s", index, item.TaskID, task.TaskID)
		}
		if err := item.RunID.Validate(); err != nil {
			t.Fatalf("run[%d] run_id=%s is not canonical: %v", index, item.RunID, err)
		}
		switch item.RunID {
		case firstRunID:
			first = &item
		default:
			rerun = &item
		}
	}
	if first == nil || rerun == nil || first.RunID == rerun.RunID {
		t.Fatalf("run identities=%#v", response.Runs)
	}
	if first.StartReason != domaintask.RunStartReasonFirst || first.Status != domaintask.RunStatusSucceeded || first.CompletedAt == nil {
		t.Fatalf("first run=%#v", first)
	}
	if rerun.StartReason != domaintask.RunStartReasonExplicitRerun || rerun.Status != domaintask.RunStatusSucceeded || rerun.CompletedAt == nil {
		t.Fatalf("rerun=%#v", rerun)
	}
}

func TestTaskDetailReturnsInternalServerErrorWhenRunHistoryFails(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	task, err := manager.Create(context.Background(), domaintask.Task{Title: "viewer run failure", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	detailStore := &taskDetailStore{TaskStore: store, listRunsErr: errors.New("run history unavailable")}
	recorder := httptest.NewRecorder()
	HandleTaskDetail(detailStore)(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(task.TaskID), nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTaskDetailNormalizesNilRunHistory(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	task, err := manager.Create(context.Background(), domaintask.Task{Title: "viewer empty run history", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	detailStore := &taskDetailStore{TaskStore: store, returnNilRuns: true}
	recorder := httptest.NewRecorder()
	HandleTaskDetail(detailStore)(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(task.TaskID), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response taskDetailResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Runs == nil || len(response.Runs) != 0 {
		t.Fatalf("runs=%#v", response.Runs)
	}
}

func TestTaskDetailRejectsMalformedTaskIdentity(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	HandleTaskDetail(store)(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id=job_old", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
