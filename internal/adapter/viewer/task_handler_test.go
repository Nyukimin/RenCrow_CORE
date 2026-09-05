package viewer

import (
	"context"
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
