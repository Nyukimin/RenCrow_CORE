package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeTaskVisibilityRequiresCurrentOriginPublicationBinding(t *testing.T) {
	ctx := context.Background()
	sessions := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	publicA := saveRuntimeTaskSession(t, ctx, sessions, modulecore.NewSessionID(), "viewer", "task-a-chat", "")
	publicB := saveRuntimeTaskSession(t, ctx, sessions, modulecore.NewSessionID(), "viewer", "task-b-chat", "")
	if err := sessions.WarmCanonicalIndex(ctx); err != nil {
		t.Fatalf("warm canonical Session projection: %v", err)
	}

	taskStore, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	manager := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	taskA := createRuntimeTaskWithSessionBinding(t, ctx, manager, sessions, publicA, "public task A")
	taskB := createRuntimeTaskWithSessionBinding(t, ctx, manager, sessions, publicB, "public task B")
	legacy, err := manager.Create(ctx, domaintask.Task{
		Title: "legacy proofless task", Route: domaintask.RouteGeneral, OwnerID: "mio", OriginSessionID: publicA,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create proofless legacy Task: %v", err)
	}
	for _, task := range []domaintask.Task{taskA, taskB} {
		if _, err := manager.Start(ctx, task.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Succeed(ctx, task.TaskID, "completed"); err != nil {
			t.Fatal(err)
		}
	}

	visibility := runtimePublicTaskVisibility(newRuntimeSessionVisibility(sessions))
	assertRuntimeTaskRoutesVisible(t, taskStore, visibility, []domaintask.Task{taskA, taskB}, legacy, true)

	// Re-key one public Session at the same ID and recreate another as private.
	// Both changes must revoke the exact publication proof saved with the Tasks.
	if err := sessions.Delete(ctx, string(publicA)); err != nil {
		t.Fatalf("delete original public Session A: %v", err)
	}
	saveRuntimeTaskSession(t, ctx, sessions, publicA, "viewer", "task-a-rekeyed-chat", "")
	if err := sessions.Delete(ctx, string(publicB)); err != nil {
		t.Fatalf("delete original public Session B: %v", err)
	}
	saveRuntimeTaskSession(t, ctx, sessions, publicB, "viewer", "task-b-chat", "ren")
	if err := sessions.WarmCanonicalIndex(ctx); err != nil {
		t.Fatalf("warm recreated Session projection: %v", err)
	}
	assertRuntimeTaskRoutesVisible(t, taskStore, visibility, []domaintask.Task{taskA, taskB}, legacy, false)
}

func saveRuntimeTaskSession(t *testing.T, ctx context.Context, repository *sessionpersistence.JSONSessionRepository, id modulecore.SessionID, channel, addressID, ownerID string) modulecore.SessionID {
	t.Helper()
	address, err := conversation.NewChannelAddress(channel, addressID)
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	createdAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var sess *domainsession.Session
	if ownerID == "" {
		sess, err = domainsession.NewCanonicalSession(id, "2026-10-24", address, createdAt)
	} else {
		sess, err = domainsession.NewCanonicalSessionWithOwner(id, ownerID, "2026-10-24", address, createdAt)
	}
	if err != nil {
		t.Fatalf("create canonical Session: %v", err)
	}
	if err := repository.Save(ctx, sess); err != nil {
		t.Fatalf("save canonical Session: %v", err)
	}
	return id
}

func createRuntimeTaskWithSessionBinding(t *testing.T, ctx context.Context, manager *taskmanager.Manager, sessions *sessionpersistence.JSONSessionRepository, sessionID modulecore.SessionID, title string) domaintask.Task {
	t.Helper()
	sess, err := sessions.Load(ctx, string(sessionID))
	if err != nil || sess == nil {
		t.Fatalf("load canonical Session %s: session=%v err=%v", sessionID, sess, err)
	}
	binding, err := conversation.NewSessionBindingWithPublicationIncarnation(
		sessionID, sess.ChannelAddress(), conversation.SessionAudiencePublic, "", 0, sess.PublicationIncarnation(),
	)
	if err != nil {
		t.Fatalf("construct validated SessionBinding: %v", err)
	}
	created, err := manager.Create(conversation.WithSessionBinding(ctx, binding), domaintask.Task{
		Title: title, Route: domaintask.RouteGeneral, OwnerID: "mio", OriginSessionID: sessionID,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create Task with SessionBinding: %v", err)
	}
	if created.OriginPublication == nil || created.OriginPublication.PublicationIncarnation != sess.PublicationIncarnation() {
		t.Fatalf("Task origin publication = %#v, want Session incarnation %q", created.OriginPublication, sess.PublicationIncarnation())
	}
	return created
}

func assertRuntimeTaskRoutesVisible(t *testing.T, store viewer.TaskStore, visibility viewer.PublicTaskVisibility, tasks []domaintask.Task, legacy domaintask.Task, wantVisible bool) {
	t.Helper()
	listRecorder := httptest.NewRecorder()
	viewer.HandleTasksWithPublicVisibility(store, visibility).ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/viewer/tasks", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("Task list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	for _, task := range tasks {
		hasTask := strings.Contains(listRecorder.Body.String(), string(task.TaskID))
		if hasTask != wantVisible {
			t.Fatalf("Task list visibility for %s = %v want %v: %s", task.TaskID, hasTask, wantVisible, listRecorder.Body.String())
		}
	}
	if strings.Contains(listRecorder.Body.String(), string(legacy.TaskID)) {
		t.Fatalf("legacy Task without publication proof was exposed: %s", listRecorder.Body.String())
	}
	for _, task := range tasks {
		recorder := httptest.NewRecorder()
		viewer.HandleTaskDetailWithPublicVisibility(store, visibility).ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(task.TaskID), nil),
		)
		wantStatus := http.StatusNotFound
		if wantVisible {
			wantStatus = http.StatusOK
		}
		if recorder.Code != wantStatus {
			t.Fatalf("Task detail %s status=%d want %d body=%s", task.TaskID, recorder.Code, wantStatus, recorder.Body.String())
		}
	}
	notifications := httptest.NewRecorder()
	viewer.HandleTaskNotificationsWithPublicVisibility(store, visibility).ServeHTTP(
		notifications,
		httptest.NewRequest(http.MethodGet, "/viewer/task-notifications?interrupt_only=false", nil),
	)
	if notifications.Code != http.StatusOK {
		t.Fatalf("Task notifications status=%d body=%s", notifications.Code, notifications.Body.String())
	}
	for _, task := range tasks {
		hasTask := strings.Contains(notifications.Body.String(), string(task.TaskID))
		if hasTask != wantVisible {
			t.Fatalf("Task notification visibility for %s = %v want %v: %s", task.TaskID, hasTask, wantVisible, notifications.Body.String())
		}
	}
	if strings.Contains(notifications.Body.String(), string(legacy.TaskID)) {
		t.Fatalf("legacy Task notification without publication proof was exposed: %s", notifications.Body.String())
	}
}
