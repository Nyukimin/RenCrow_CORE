package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskViewerRoutesExposeCanonicalTaskIdentity(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	value, err := manager.Create(context.Background(), domaintask.Task{Title: "viewer Task", Route: domaintask.RouteGeneral, OriginSessionID: modulecore.NewSessionID()}, domaintask.SharedRoleContext{CurrentPlan: "show it"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), value.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(context.Background(), value.TaskID, "done"); err != nil {
		t.Fatal(err)
	}

	publicOrigin := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		return task.OriginSessionID != "", nil
	})
	checks := []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{name: "list", handler: HandleTasksWithPublicVisibility(store, publicOrigin), path: "/viewer/tasks"},
		{name: "detail", handler: HandleTaskDetailWithPublicVisibility(store, publicOrigin), path: "/viewer/task/detail?task_id=" + string(value.TaskID)},
		{name: "notifications", handler: HandleTaskNotificationsWithPublicVisibility(store, publicOrigin), path: "/viewer/task-notifications?interrupt_only=false"},
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
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
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
	HandleTaskDetailWithPublicVisibility(detailStore, allowAllTasks)(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(task.TaskID), nil))
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
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	task, err := manager.Create(context.Background(), domaintask.Task{Title: "viewer run failure", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	detailStore := &taskDetailStore{TaskStore: store, listRunsErr: errors.New("run history unavailable")}
	recorder := httptest.NewRecorder()
	HandleTaskDetailWithPublicVisibility(detailStore, allowAllTasks)(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(task.TaskID), nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTaskDetailNormalizesNilRunHistory(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	task, err := manager.Create(context.Background(), domaintask.Task{Title: "viewer empty run history", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	detailStore := &taskDetailStore{TaskStore: store, returnNilRuns: true}
	recorder := httptest.NewRecorder()
	HandleTaskDetailWithPublicVisibility(detailStore, allowAllTasks)(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(task.TaskID), nil))
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
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	recorder := httptest.NewRecorder()
	HandleTaskDetail(store)(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id=job_old", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTaskViewerLegacyHandlersHideSessionOwnedTask(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	task, err := manager.Create(ctx, domaintask.Task{
		Title:           "private task title",
		Summary:         "private task summary",
		Route:           domaintask.RouteGeneral,
		OriginSessionID: modulecore.NewSessionID(),
	}, domaintask.SharedRoleContext{UserIntent: "private user intent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "private completion summary"); err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name       string
		handler    http.HandlerFunc
		path       string
		wantStatus int
	}{
		{name: "list", handler: HandleTasks(store), path: "/viewer/tasks", wantStatus: http.StatusOK},
		{name: "detail", handler: HandleTaskDetail(store), path: "/viewer/task/detail?task_id=" + string(task.TaskID) + "&session_id=" + string(task.OriginSessionID), wantStatus: http.StatusNotFound},
		{name: "notifications", handler: HandleTaskNotifications(store), path: "/viewer/task-notifications?interrupt_only=false", wantStatus: http.StatusOK},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			check.handler(recorder, httptest.NewRequest(http.MethodGet, check.path, nil))
			if recorder.Code != check.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, check.wantStatus, recorder.Body.String())
			}
			for _, secret := range []string{"private task title", "private task summary", "private user intent", "private completion summary"} {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Fatalf("private task data %q was exposed: %s", secret, recorder.Body.String())
				}
			}
		})
	}
}

func TestTaskViewerHandlersFilterSessionOwnedTasksAndNotifications(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	publicSessionID := modulecore.NewSessionID()
	privateSessionID := modulecore.NewSessionID()
	publicTask, err := manager.Create(ctx, domaintask.Task{
		Title:           "public unbound-session task",
		Summary:         "public task summary",
		Route:           domaintask.RouteGeneral,
		OriginSessionID: publicSessionID,
	}, domaintask.SharedRoleContext{UserIntent: "public intent"})
	if err != nil {
		t.Fatal(err)
	}
	privateTask, err := manager.Create(ctx, domaintask.Task{
		Title:           "private owner-bound task",
		Summary:         "private task summary",
		Route:           domaintask.RouteGeneral,
		OriginSessionID: privateSessionID,
	}, domaintask.SharedRoleContext{UserIntent: "private user intent"})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []domaintask.Task{publicTask, privateTask} {
		if _, err := manager.Start(ctx, task.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Succeed(ctx, task.TaskID, task.Title+" completion"); err != nil {
			t.Fatal(err)
		}
	}
	visibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		return task.OriginSessionID == publicSessionID, nil
	})

	listRecorder := httptest.NewRecorder()
	HandleTasksWithPublicVisibility(store, visibility).ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/viewer/tasks", nil))
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), string(publicTask.TaskID)) || strings.Contains(listRecorder.Body.String(), string(privateTask.TaskID)) {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	for _, secret := range []string{"private owner-bound task", "private task summary"} {
		if strings.Contains(listRecorder.Body.String(), secret) {
			t.Fatalf("private task data %q was exposed by list: %s", secret, listRecorder.Body.String())
		}
	}

	publicDetail := httptest.NewRecorder()
	HandleTaskDetailWithPublicVisibility(store, visibility).ServeHTTP(publicDetail, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(publicTask.TaskID), nil))
	if publicDetail.Code != http.StatusOK || !strings.Contains(publicDetail.Body.String(), "public intent") {
		t.Fatalf("public detail status=%d body=%s", publicDetail.Code, publicDetail.Body.String())
	}
	privateDetail := httptest.NewRecorder()
	HandleTaskDetailWithPublicVisibility(store, visibility).ServeHTTP(privateDetail, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(privateTask.TaskID)+"&session_id="+string(publicSessionID), nil))
	if privateDetail.Code != http.StatusNotFound {
		t.Fatalf("private detail status=%d body=%s", privateDetail.Code, privateDetail.Body.String())
	}
	if strings.Contains(privateDetail.Body.String(), "private") {
		t.Fatalf("private detail response exposed data: %s", privateDetail.Body.String())
	}

	notificationRecorder := httptest.NewRecorder()
	HandleTaskNotificationsWithPublicVisibility(store, visibility).ServeHTTP(notificationRecorder, httptest.NewRequest(http.MethodGet, "/viewer/task-notifications?interrupt_only=false", nil))
	if notificationRecorder.Code != http.StatusOK || !strings.Contains(notificationRecorder.Body.String(), string(publicTask.TaskID)) || strings.Contains(notificationRecorder.Body.String(), string(privateTask.TaskID)) {
		t.Fatalf("notifications status=%d body=%s", notificationRecorder.Code, notificationRecorder.Body.String())
	}
	for _, secret := range []string{"private owner-bound task", "private task summary", "private user intent"} {
		if strings.Contains(notificationRecorder.Body.String(), secret) {
			t.Fatalf("private task data %q was exposed by notifications: %s", secret, notificationRecorder.Body.String())
		}
	}
}

func TestTaskViewerVisibilityErrorsAndUnknownNotificationsFailClosed(t *testing.T) {
	taskID := modulecore.NewTaskID()
	task := domaintask.Task{TaskID: taskID, Title: "visibility error private title", Summary: "visibility error private summary"}
	missingTaskID := modulecore.NewTaskID()
	store := &taskVisibilityTestStore{
		tasks:         []domaintask.Task{task},
		notifications: []domaintask.Notification{{TaskID: taskID, Title: task.Title}, {TaskID: missingTaskID, Title: "missing task private title"}},
	}
	visibility := PublicTaskVisibility(func(context.Context, domaintask.Task) (bool, error) {
		return false, errors.New("session ownership lookup unavailable")
	})

	listRecorder := httptest.NewRecorder()
	HandleTasksWithPublicVisibility(store, visibility).ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/viewer/tasks", nil))
	if listRecorder.Code != http.StatusOK || strings.Contains(listRecorder.Body.String(), "visibility error private") {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}

	detailRecorder := httptest.NewRecorder()
	HandleTaskDetailWithPublicVisibility(store, visibility).ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(taskID), nil))
	if detailRecorder.Code != http.StatusNotFound || store.contextCalls != 0 || store.runCalls != 0 {
		t.Fatalf("detail status=%d context_calls=%d run_calls=%d body=%s", detailRecorder.Code, store.contextCalls, store.runCalls, detailRecorder.Body.String())
	}

	notificationRecorder := httptest.NewRecorder()
	HandleTaskNotificationsWithPublicVisibility(store, visibility).ServeHTTP(notificationRecorder, httptest.NewRequest(http.MethodGet, "/viewer/task-notifications?interrupt_only=false", nil))
	if notificationRecorder.Code != http.StatusOK || strings.Contains(notificationRecorder.Body.String(), "private title") {
		t.Fatalf("notifications status=%d body=%s", notificationRecorder.Code, notificationRecorder.Body.String())
	}
	if len(store.getTaskIDs) != 3 || store.getTaskIDs[1] != taskID || store.getTaskIDs[2] != missingTaskID {
		t.Fatalf("notification GetTask calls=%#v", store.getTaskIDs)
	}
}

func TestTaskViewerLookupErrorsFailClosed(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &taskVisibilityTestStore{
		tasks:        []domaintask.Task{{TaskID: taskID, Title: "lookup error private title"}},
		getTaskError: errors.New("task storage unavailable"),
		notifications: []domaintask.Notification{{
			TaskID:  taskID,
			Title:   "lookup error private notification",
			Summary: "lookup error private summary",
		}},
	}
	visibility := PublicTaskVisibility(allowAllTasks)

	detailRecorder := httptest.NewRecorder()
	HandleTaskDetailWithPublicVisibility(store, visibility).ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(taskID), nil))
	if detailRecorder.Code != http.StatusNotFound || store.contextCalls != 0 || store.runCalls != 0 {
		t.Fatalf("detail status=%d context_calls=%d run_calls=%d body=%s", detailRecorder.Code, store.contextCalls, store.runCalls, detailRecorder.Body.String())
	}

	notificationRecorder := httptest.NewRecorder()
	HandleTaskNotificationsWithPublicVisibility(store, visibility).ServeHTTP(notificationRecorder, httptest.NewRequest(http.MethodGet, "/viewer/task-notifications?interrupt_only=false", nil))
	if notificationRecorder.Code != http.StatusOK || strings.Contains(notificationRecorder.Body.String(), "lookup error private") {
		t.Fatalf("notifications status=%d body=%s", notificationRecorder.Code, notificationRecorder.Body.String())
	}
	if len(store.getTaskIDs) != 2 || store.getTaskIDs[0] != taskID || store.getTaskIDs[1] != taskID {
		t.Fatalf("GetTask calls=%#v", store.getTaskIDs)
	}
}

func TestTaskDetailChecksVisibilityBeforeReadingContextOrRuns(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &taskVisibilityTestStore{tasks: []domaintask.Task{{TaskID: taskID, Title: "owner-bound task"}}}
	visibility := PublicTaskVisibility(func(context.Context, domaintask.Task) (bool, error) {
		return false, nil
	})
	recorder := httptest.NewRecorder()
	HandleTaskDetailWithPublicVisibility(store, visibility).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(taskID), nil))
	if recorder.Code != http.StatusNotFound || store.contextCalls != 0 || store.runCalls != 0 {
		t.Fatalf("status=%d context_calls=%d run_calls=%d body=%s", recorder.Code, store.contextCalls, store.runCalls, recorder.Body.String())
	}
}

func TestTaskDetailRechecksPublicationBeforeWritingResponse(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &taskVisibilityTestStore{
		tasks: []domaintask.Task{{TaskID: taskID, Title: "must not escape after origin revocation"}},
	}
	visibilityCalls := 0
	visibility := PublicTaskVisibility(func(context.Context, domaintask.Task) (bool, error) {
		visibilityCalls++
		return visibilityCalls == 1, nil
	})
	recorder := httptest.NewRecorder()
	HandleTaskDetailWithPublicVisibility(store, visibility).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(taskID), nil),
	)
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "must not escape") {
		t.Fatalf("detail status=%d body=%s, want fail-closed response after second visibility check", recorder.Code, recorder.Body.String())
	}
	if visibilityCalls != 2 || store.contextCalls != 1 || store.runCalls != 1 {
		t.Fatalf("visibility calls=%d context_calls=%d run_calls=%d, want 2/1/1", visibilityCalls, store.contextCalls, store.runCalls)
	}
}

func TestTaskListRechecksEveryOriginAfterAllVisibilityReads(t *testing.T) {
	taskA := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "origin A task"}
	taskB := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "origin B task"}
	store := &taskVisibilityTestStore{tasks: []domaintask.Task{taskA, taskB}}
	var mu sync.Mutex
	visibleA := true
	enteredB := make(chan struct{})
	releaseB := make(chan struct{})
	var blockB sync.Once
	visibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		if task.TaskID == taskB.TaskID {
			blockB.Do(func() {
				close(enteredB)
				<-releaseB
			})
			return true, nil
		}
		mu.Lock()
		defer mu.Unlock()
		return visibleA, nil
	})

	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		HandleTasksWithPublicVisibility(store, visibility).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/viewer/tasks", nil))
		close(done)
	}()
	select {
	case <-enteredB:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for origin B visibility check")
	}
	mu.Lock()
	visibleA = false
	mu.Unlock()
	close(releaseB)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task list response")
	}

	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), string(taskA.TaskID)) || !strings.Contains(recorder.Body.String(), string(taskB.TaskID)) {
		t.Fatalf("list status=%d body=%s, revoked origin A task must be removed at response admission", recorder.Code, recorder.Body.String())
	}
}

func TestTaskNotificationsLoadAllTasksBeforeFinalOriginChecks(t *testing.T) {
	taskA := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "origin A task"}
	taskB := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "origin B task"}
	base := &taskVisibilityTestStore{
		tasks: []domaintask.Task{taskA, taskB},
		notifications: []domaintask.Notification{
			{TaskID: taskA.TaskID, Title: "origin A notification"},
			{TaskID: taskB.TaskID, Title: "origin B notification"},
		},
	}
	enteredB := make(chan struct{})
	releaseB := make(chan struct{})
	store := &blockingTaskGetStore{
		taskVisibilityTestStore: base,
		blockTaskID:             taskB.TaskID,
		entered:                 enteredB,
		release:                 releaseB,
	}
	var mu sync.Mutex
	visibleA := true
	visibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if task.TaskID == taskA.TaskID {
			return visibleA, nil
		}
		return true, nil
	})

	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		HandleTaskNotificationsWithPublicVisibility(store, visibility).ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, "/viewer/task-notifications?interrupt_only=false", nil),
		)
		close(done)
	}()
	select {
	case <-enteredB:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for origin B Task lookup")
	}
	mu.Lock()
	visibleA = false
	mu.Unlock()
	close(releaseB)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task notification response")
	}

	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), string(taskA.TaskID)) || strings.Contains(recorder.Body.String(), "origin A notification") || !strings.Contains(recorder.Body.String(), string(taskB.TaskID)) {
		t.Fatalf("notifications status=%d body=%s, revoked origin A notification must be removed at response admission", recorder.Code, recorder.Body.String())
	}
}

func TestTaskViewerLegacyHandlersFailClosedWithoutVisibilityPolicy(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &taskVisibilityTestStore{
		tasks:         []domaintask.Task{{TaskID: taskID, Title: "no-policy private title"}},
		notifications: []domaintask.Notification{{TaskID: taskID, Title: "no-policy private notification"}},
	}
	checks := []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{name: "list", handler: HandleTasks(store), path: "/viewer/tasks"},
		{name: "detail", handler: HandleTaskDetail(store), path: "/viewer/task/detail?task_id=" + string(taskID)},
		{name: "notifications", handler: HandleTaskNotifications(store), path: "/viewer/task-notifications?interrupt_only=false"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			check.handler(recorder, httptest.NewRequest(http.MethodGet, check.path, nil))
			if check.name == "detail" {
				if recorder.Code != http.StatusNotFound {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
			} else if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "private") {
				t.Fatalf("response exposed private data: %s", recorder.Body.String())
			}
		})
	}
	if store.listCalls != 0 || len(store.getTaskIDs) != 0 || store.contextCalls != 0 || store.runCalls != 0 || store.notificationCalls != 0 {
		t.Fatalf("no-policy handlers queried store: %+v", store)
	}
}

type taskVisibilityTestStore struct {
	TaskStore
	tasks             []domaintask.Task
	notifications     []domaintask.Notification
	getTaskError      error
	contextCalls      int
	runCalls          int
	notificationCalls int
	listCalls         int
	getTaskIDs        []modulecore.TaskID
}

type blockingTaskGetStore struct {
	*taskVisibilityTestStore
	blockTaskID modulecore.TaskID
	entered     chan struct{}
	release     chan struct{}
	blockOnce   sync.Once
}

func (s *blockingTaskGetStore) GetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if taskID == s.blockTaskID {
		s.blockOnce.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	return s.taskVisibilityTestStore.GetTask(ctx, taskID)
}

func (s *taskVisibilityTestStore) ListTasks(context.Context, domaintask.Filter) ([]domaintask.Task, error) {
	s.listCalls++
	return append([]domaintask.Task(nil), s.tasks...), nil
}

func (s *taskVisibilityTestStore) GetTask(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	s.getTaskIDs = append(s.getTaskIDs, taskID)
	if s.getTaskError != nil {
		return domaintask.Task{}, s.getTaskError
	}
	for _, task := range s.tasks {
		if task.TaskID == taskID {
			return task, nil
		}
	}
	return domaintask.Task{}, taskmanager.ErrNotFound
}

func (s *taskVisibilityTestStore) GetContext(context.Context, modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	s.contextCalls++
	return domaintask.SharedRoleContext{}, nil
}

func (s *taskVisibilityTestStore) ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error) {
	s.runCalls++
	return []domaintask.Run{}, nil
}

func (s *taskVisibilityTestStore) ListNotifications(context.Context, int, bool) ([]domaintask.Notification, error) {
	s.notificationCalls++
	return append([]domaintask.Notification(nil), s.notifications...), nil
}

func allowAllTasks(context.Context, domaintask.Task) (bool, error) {
	return true, nil
}
