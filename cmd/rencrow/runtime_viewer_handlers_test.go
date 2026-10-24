package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildViewerRuntimeHandlersUsesConfiguredGameObserverURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/games/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/games/launch":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"game_id":"nethack","session_id":"session-1","status":"launching"}`))
		default:
			t.Fatalf("upstream path=%q", r.URL.Path)
		}
	}))
	t.Cleanup(upstream.Close)
	legacyURL := "http://127.0.0.1:1"
	if err := os.Setenv("RENCROW_GAMES_OBSERVER_URL", legacyURL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("RENCROW_GAMES_OBSERVER_URL") })

	deps := &Dependencies{}
	cfg := &config.Config{WorkspaceDir: t.TempDir()}
	cfg.Games.ObserverURL = upstream.URL
	if err := initializeRuntimeTaskOwner(deps, cfg.WorkspaceDir); err != nil {
		t.Fatalf("initialize task owner: %v", err)
	}
	sessionDir := filepath.Join(cfg.WorkspaceDir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	publicSessionID := modulecore.NewSessionID()
	saveRuntimeVisibilitySession(t, sessionRepo, publicSessionID, "", "runtime-viewer-public")
	deps.sessionRepository = sessionRepo
	deps.sessionVisibility = newRuntimeSessionVisibility(sessionRepo)
	ownerStore := deps.taskStore
	ownerManager := deps.taskManager
	t.Cleanup(func() {
		if err := ownerManager.Close(); err != nil {
			t.Errorf("close task owner: %v", err)
		}
	})
	created := createRuntimeVisibilityTaskWithCanonicalOrigin(
		t, context.Background(), ownerManager, sessionRepo,
		domaintask.Task{Title: "shared viewer task", OriginSessionID: publicSessionID}, domaintask.SharedRoleContext{},
	)
	buildViewerRuntimeHandlers(cfg, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.taskStore != ownerStore || deps.taskManager != ownerManager {
		t.Fatal("Viewer construction replaced the canonical Task owner")
	}
	if deps.taskManager == nil || deps.tasks == nil || deps.taskDetail == nil || deps.taskNotifications == nil {
		t.Fatal("canonical Task store was not shared with the Orchestrator lifecycle and Viewer handlers")
	}
	tasksRec := httptest.NewRecorder()
	deps.tasks.ServeHTTP(tasksRec, httptest.NewRequest(http.MethodGet, "/viewer/tasks", nil))
	if tasksRec.Code != http.StatusOK || !strings.Contains(tasksRec.Body.String(), string(created.TaskID)) {
		t.Fatalf("Viewer did not read the initialized owner store: status=%d body=%s", tasksRec.Code, tasksRec.Body.String())
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/observer-api/games/status", nil)
	deps.viewerGamesObserverProxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("observer proxy status=%d body=%q", rec.Code, rec.Body.String())
	}
	launchRec := httptest.NewRecorder()
	launchReq := httptest.NewRequest(http.MethodPost, "/viewer/games/launch", strings.NewReader(`{"game_id":"nethack"}`))
	deps.viewerGamesLaunch.ServeHTTP(launchRec, launchReq)
	if launchRec.Code != http.StatusOK || !strings.Contains(launchRec.Body.String(), `"session_id":"session-1"`) {
		t.Fatalf("game launch status=%d body=%q", launchRec.Code, launchRec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersFiltersTaskRoutesByCanonicalOriginSession(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, workspace); err != nil {
		t.Fatalf("initialize task owner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close task owner: %v", err)
		}
	})
	sessionDir := filepath.Join(workspace, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	publicSessionID := modulecore.NewSessionID()
	privateSessionID := modulecore.NewSessionID()
	unknownSessionID := modulecore.NewSessionID()
	saveRuntimeVisibilitySession(t, sessionRepo, publicSessionID, "", "runtime-viewer-public")
	saveRuntimeVisibilitySession(t, sessionRepo, privateSessionID, "ren", "runtime-viewer-private")
	deps.sessionRepository = sessionRepo
	deps.sessionVisibility = newRuntimeSessionVisibility(sessionRepo)
	canonicalEvents, err := eventstore.NewSQLiteStore(filepath.Join(workspace, "events.sqlite"))
	if err != nil {
		t.Fatalf("create canonical event store: %v", err)
	}
	deps.canonicalEventStore = canonicalEvents
	t.Cleanup(func() {
		if err := canonicalEvents.Close(); err != nil {
			t.Errorf("close canonical event store: %v", err)
		}
	})

	createBoundTask := func(draft domaintask.Task, shared domaintask.SharedRoleContext) domaintask.Task {
		sess, err := sessionRepo.Load(ctx, string(draft.OriginSessionID))
		if err != nil || sess == nil {
			t.Fatalf("load canonical Task origin Session: session=%v err=%v", sess, err)
		}
		audience := domainconversation.SessionAudiencePublic
		ownerID := ""
		if sess.OwnerID() != "" {
			audience = domainconversation.SessionAudiencePrivate
			ownerID = sess.OwnerID()
		}
		binding, err := domainconversation.NewSessionBindingWithPublicationIncarnation(
			draft.OriginSessionID, sess.ChannelAddress(), audience, ownerID, 0, sess.PublicationIncarnation(),
		)
		if err != nil {
			t.Fatalf("construct canonical Task origin SessionBinding: %v", err)
		}
		created, err := deps.taskManager.Create(domainconversation.WithSessionBinding(ctx, binding), draft, shared)
		if err != nil {
			t.Fatalf("create Task with validated SessionBinding: %v", err)
		}
		return created
	}
	publicTask := createBoundTask(domaintask.Task{
		Title:           "public issuance-bound session task",
		Summary:         "public summary",
		OriginSessionID: publicSessionID,
	}, domaintask.SharedRoleContext{UserIntent: "public user intent"})
	privateTask := createBoundTask(domaintask.Task{
		Title:           "private owner-bound task",
		Summary:         "private summary",
		OriginSessionID: privateSessionID,
	}, domaintask.SharedRoleContext{UserIntent: "private user intent"})
	unknownTask, err := deps.taskManager.Create(ctx, domaintask.Task{
		Title:           "unknown session task",
		OriginSessionID: unknownSessionID,
	}, domaintask.SharedRoleContext{UserIntent: "unknown user intent"})
	if err != nil {
		t.Fatal(err)
	}
	noOriginTask, err := deps.taskManager.Create(ctx, domaintask.Task{
		Title: "task without origin session",
	}, domaintask.SharedRoleContext{UserIntent: "no-origin user intent"})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []domaintask.Task{publicTask, privateTask, unknownTask, noOriginTask} {
		if _, err := deps.taskManager.Start(ctx, task.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err := deps.taskManager.Succeed(ctx, task.TaskID, task.Title+" completion"); err != nil {
			t.Fatal(err)
		}
	}
	publicTraceID := modulecore.NewTraceID()
	privateTraceID := modulecore.NewTraceID()
	appendRuntimeVisibilityTrace(t, canonicalEvents, sessionRepo, publicTraceID, publicTask.TaskID, publicSessionID)
	appendRuntimeVisibilityTrace(t, canonicalEvents, sessionRepo, privateTraceID, privateTask.TaskID, privateSessionID)
	buildViewerRuntimeHandlers(&config.Config{WorkspaceDir: workspace}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)

	listRecorder := httptest.NewRecorder()
	deps.tasks.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/viewer/tasks", nil))
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), string(publicTask.TaskID)) {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	for _, task := range []domaintask.Task{privateTask, unknownTask, noOriginTask} {
		if strings.Contains(listRecorder.Body.String(), string(task.TaskID)) || strings.Contains(listRecorder.Body.String(), task.Title) {
			t.Fatalf("list exposed hidden task %s: %s", task.TaskID, listRecorder.Body.String())
		}
	}

	publicDetail := httptest.NewRecorder()
	deps.taskDetail.ServeHTTP(publicDetail, httptest.NewRequest(http.MethodGet, "/viewer/task/detail?task_id="+string(publicTask.TaskID), nil))
	if publicDetail.Code != http.StatusOK || !strings.Contains(publicDetail.Body.String(), "public user intent") {
		t.Fatalf("public detail status=%d body=%s", publicDetail.Code, publicDetail.Body.String())
	}
	for _, task := range []domaintask.Task{privateTask, unknownTask, noOriginTask} {
		recorder := httptest.NewRecorder()
		path := "/viewer/task/detail?task_id=" + string(task.TaskID) + "&session_id=" + string(publicSessionID)
		deps.taskDetail.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), task.Title) {
			t.Fatalf("detail exposed hidden task %s: status=%d body=%s", task.TaskID, recorder.Code, recorder.Body.String())
		}
	}

	notificationRecorder := httptest.NewRecorder()
	deps.taskNotifications.ServeHTTP(notificationRecorder, httptest.NewRequest(http.MethodGet, "/viewer/task-notifications?interrupt_only=false", nil))
	if notificationRecorder.Code != http.StatusOK || !strings.Contains(notificationRecorder.Body.String(), string(publicTask.TaskID)) {
		t.Fatalf("notifications status=%d body=%s", notificationRecorder.Code, notificationRecorder.Body.String())
	}
	for _, task := range []domaintask.Task{privateTask, unknownTask, noOriginTask} {
		if strings.Contains(notificationRecorder.Body.String(), string(task.TaskID)) || strings.Contains(notificationRecorder.Body.String(), task.Title) {
			t.Fatalf("notifications exposed hidden task %s: %s", task.TaskID, notificationRecorder.Body.String())
		}
	}

	publicGraph := httptest.NewRecorder()
	deps.identityGraph.ServeHTTP(publicGraph, httptest.NewRequest(http.MethodGet, "/viewer/identity-graph?trace_id="+string(publicTraceID), nil))
	if publicGraph.Code != http.StatusOK || !strings.Contains(publicGraph.Body.String(), string(publicTask.TaskID)) {
		t.Fatalf("public identity graph status=%d body=%s", publicGraph.Code, publicGraph.Body.String())
	}
	privateGraph := httptest.NewRecorder()
	deps.identityGraph.ServeHTTP(privateGraph, httptest.NewRequest(http.MethodGet, "/viewer/identity-graph?trace_id="+string(privateTraceID), nil))
	if privateGraph.Code != http.StatusNotFound {
		t.Fatalf("private identity graph status=%d body=%s, want 404", privateGraph.Code, privateGraph.Body.String())
	}
	for _, secret := range []string{string(privateTraceID), string(privateTask.TaskID), privateTask.Title, "private user intent"} {
		if strings.Contains(privateGraph.Body.String(), secret) {
			t.Fatalf("private identity graph exposed %q: %s", secret, privateGraph.Body.String())
		}
	}
	unknownGraph := httptest.NewRecorder()
	deps.identityGraph.ServeHTTP(unknownGraph, httptest.NewRequest(http.MethodGet, "/viewer/identity-graph?trace_id="+string(modulecore.NewTraceID()), nil))
	if unknownGraph.Code != http.StatusNotFound {
		t.Fatalf("unknown identity graph status=%d body=%s, want 404", unknownGraph.Code, unknownGraph.Body.String())
	}

	// A trace event without a Task still carries its issuance binding. Reusing
	// the same public SessionID must not make its old graph visible again.
	recreationTraceID := modulecore.NewTraceID()
	appendRuntimeVisibilityTrace(t, canonicalEvents, sessionRepo, recreationTraceID, "", publicSessionID)
	firstIncarnation, err := sessionRepo.CurrentPublicationBinding(ctx, string(publicSessionID))
	if err != nil {
		t.Fatalf("read original publication binding: %v", err)
	}
	firstGraph := httptest.NewRecorder()
	deps.identityGraph.ServeHTTP(firstGraph, httptest.NewRequest(http.MethodGet, "/viewer/identity-graph?trace_id="+string(recreationTraceID), nil))
	if firstGraph.Code != http.StatusOK {
		t.Fatalf("original public identity graph status=%d body=%s", firstGraph.Code, firstGraph.Body.String())
	}
	if err := sessionRepo.Delete(ctx, string(publicSessionID)); err != nil {
		t.Fatalf("delete original public Session: %v", err)
	}
	saveRuntimeVisibilitySession(t, sessionRepo, publicSessionID, "", "runtime-viewer-public")
	secondIncarnation, err := sessionRepo.CurrentPublicationBinding(ctx, string(publicSessionID))
	if err != nil {
		t.Fatalf("read recreated publication binding: %v", err)
	}
	if secondIncarnation.PublicationIncarnation() == firstIncarnation.PublicationIncarnation() {
		t.Fatal("same-ID public Session recreation retained the old publication incarnation")
	}
	staleGraph := httptest.NewRecorder()
	deps.identityGraph.ServeHTTP(staleGraph, httptest.NewRequest(http.MethodGet, "/viewer/identity-graph?trace_id="+string(recreationTraceID), nil))
	if staleGraph.Code != http.StatusNotFound || strings.Contains(staleGraph.Body.String(), string(recreationTraceID)) {
		t.Fatalf("same-ID recreated public Session exposed old graph: status=%d body=%s", staleGraph.Code, staleGraph.Body.String())
	}
}

func TestIdentityGraphRuntimeRejectsOldEventAfterSameIDPublicSessionRecreation(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, workspace); err != nil {
		t.Fatalf("initialize task owner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close task owner: %v", err)
		}
	})
	sessionDir := filepath.Join(workspace, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	sessionID := modulecore.NewSessionID()
	saveRuntimeVisibilitySession(t, sessionRepo, sessionID, "", "identity-graph-recreation")
	deps.sessionRepository = sessionRepo
	deps.sessionVisibility = newRuntimeSessionVisibility(sessionRepo)
	events, err := eventstore.NewSQLiteStore(filepath.Join(workspace, "events.sqlite"))
	if err != nil {
		t.Fatalf("create canonical event store: %v", err)
	}
	deps.canonicalEventStore = events
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close canonical event store: %v", err)
		}
	})

	traceID := modulecore.NewTraceID()
	appendRuntimeVisibilityTrace(t, events, sessionRepo, traceID, "", sessionID)
	buildViewerRuntimeHandlers(&config.Config{WorkspaceDir: workspace}, deps, nil, nil, filepath.Join(workspace, "reports.jsonl"), nil, nil)
	first := httptest.NewRecorder()
	deps.identityGraph.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/viewer/identity-graph?trace_id="+string(traceID), nil))
	if first.Code != http.StatusOK {
		t.Fatalf("original public graph status=%d body=%s", first.Code, first.Body.String())
	}

	original, err := sessionRepo.CurrentPublicationBinding(ctx, string(sessionID))
	if err != nil {
		t.Fatalf("read original Session binding: %v", err)
	}
	if err := sessionRepo.Delete(ctx, string(sessionID)); err != nil {
		t.Fatalf("delete original Session: %v", err)
	}
	saveRuntimeVisibilitySession(t, sessionRepo, sessionID, "", "identity-graph-recreation")
	recreated, err := sessionRepo.CurrentPublicationBinding(ctx, string(sessionID))
	if err != nil {
		t.Fatalf("read recreated Session binding: %v", err)
	}
	if recreated.PublicationIncarnation() == original.PublicationIncarnation() {
		t.Fatal("same-ID public Session recreation retained its old publication incarnation")
	}

	stale := httptest.NewRecorder()
	deps.identityGraph.ServeHTTP(stale, httptest.NewRequest(http.MethodGet, "/viewer/identity-graph?trace_id="+string(traceID), nil))
	if stale.Code != http.StatusNotFound || strings.Contains(stale.Body.String(), string(traceID)) || strings.Contains(stale.Body.String(), string(sessionID)) {
		t.Fatalf("old public event graph survived Session recreation: status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func saveRuntimeVisibilitySession(t *testing.T, repository *sessionpersistence.JSONSessionRepository, id modulecore.SessionID, ownerID, externalID string) {
	t.Helper()
	address, err := domainconversation.NewChannelAddress("viewer", externalID)
	if err != nil {
		t.Fatalf("create Session channel address: %v", err)
	}
	createdAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var value *domainsession.Session
	if ownerID == "" {
		value, err = domainsession.NewCanonicalSession(id, "2026-10-24", address, createdAt)
	} else {
		value, err = domainsession.NewCanonicalSessionWithOwner(id, ownerID, "2026-10-24", address, createdAt)
	}
	if err != nil {
		t.Fatalf("create canonical Session: %v", err)
	}
	if err := repository.Save(context.Background(), value); err != nil {
		t.Fatalf("save canonical Session: %v", err)
	}
	if err := repository.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("warm canonical Session visibility projection: %v", err)
	}
}

func createRuntimeVisibilityTaskWithCanonicalOrigin(
	t *testing.T,
	ctx context.Context,
	manager *taskmanager.Manager,
	repository canonicalPublicationBindingReader,
	draft domaintask.Task,
	shared domaintask.SharedRoleContext,
) domaintask.Task {
	t.Helper()
	if ctx == nil || manager == nil || repository == nil {
		t.Fatal("canonical Task origin fixture requires context, Task owner, and Session publication owner")
	}
	publication, err := repository.CurrentPublicationBinding(ctx, string(draft.OriginSessionID))
	if err != nil {
		t.Fatalf("read canonical Task origin publication binding: %v", err)
	}
	if err := publication.Validate(); err != nil {
		t.Fatalf("validate canonical Task origin publication binding: %v", err)
	}
	binding, err := domainconversation.NewSessionBindingWithPublicationIncarnation(
		publication.SessionID(), publication.ChannelAddress(), publication.Audience(), publication.OwnerID(),
		domainconversation.BindingGenerationUnavailable, publication.PublicationIncarnation(),
	)
	if err != nil {
		t.Fatalf("construct Task origin from canonical Session issuance: %v", err)
	}
	created, err := manager.Create(domainconversation.WithSessionBinding(ctx, binding), draft, shared)
	if err != nil {
		t.Fatalf("create Task with canonical Session issuance binding: %v", err)
	}
	return created
}

func appendRuntimeVisibilityTrace(t *testing.T, store *eventstore.SQLiteStore, sessions canonicalPublicationBindingReader, traceID modulecore.TraceID, taskID modulecore.TaskID, sessionID modulecore.SessionID) {
	t.Helper()
	binding, err := sessions.CurrentPublicationBinding(context.Background(), string(sessionID))
	if err != nil {
		t.Fatalf("read publication binding for trace event: %v", err)
	}
	address := binding.ChannelAddress()
	eventType := "conversation.completed"
	if taskID != "" {
		eventType = "task.started"
	}
	event := modulecore.NewEventEnvelope(traceID, "", nil, "orchestrator", eventType, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), map[string]any{
		"title":                   "private event title",
		"session_id":              string(sessionID),
		"publication_incarnation": binding.PublicationIncarnation(),
		"channel":                 address.ChannelType(),
		"channel_address":         address.ExternalConversationID(),
		"visibility":              "public",
	})
	event.TaskID = taskID
	event.SessionID = sessionID
	if _, err := store.AppendSequenced(context.Background(), event); err != nil {
		t.Fatalf("append canonical trace event: %v", err)
	}
}

func TestBuildViewerRuntimeHandlersFiltersEvidenceByCanonicalOriginSession(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, workspace); err != nil {
		t.Fatalf("initialize task owner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close task owner: %v", err)
		}
	})
	sessionDir := filepath.Join(workspace, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	publicSessionID := modulecore.NewSessionID()
	privateSessionID := modulecore.NewSessionID()
	unknownSessionID := modulecore.NewSessionID()
	saveRuntimeVisibilitySession(t, sessionRepo, publicSessionID, "", "runtime-evidence-public")
	saveRuntimeVisibilitySession(t, sessionRepo, privateSessionID, "ren", "runtime-evidence-private")
	deps.sessionRepository = sessionRepo
	deps.sessionVisibility = newRuntimeSessionVisibility(sessionRepo)

	publicTask := createRuntimeVisibilityTaskWithCanonicalOrigin(
		t, ctx, deps.taskManager, sessionRepo,
		domaintask.Task{Title: "public evidence task", OriginSessionID: publicSessionID}, domaintask.SharedRoleContext{},
	)
	privateTask := createRuntimeVisibilityTaskWithCanonicalOrigin(
		t, ctx, deps.taskManager, sessionRepo,
		domaintask.Task{Title: "private evidence task", OriginSessionID: privateSessionID}, domaintask.SharedRoleContext{},
	)
	unknownTask, err := deps.taskManager.Create(ctx, domaintask.Task{
		Title:           "unknown evidence task",
		OriginSessionID: unknownSessionID,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	blankTask, err := deps.taskManager.Create(ctx, domaintask.Task{Title: "blank evidence task"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}

	reportPath := filepath.Join(workspace, "reports.jsonl")
	cfg := &config.Config{WorkspaceDir: workspace}
	buildViewerRuntimeHandlers(cfg, deps, nil, nil, reportPath, nil, nil)
	if deps.reportStore == nil || deps.evidenceHandler == nil || deps.evidenceDetail == nil || deps.evidenceSummary == nil {
		t.Fatal("evidence handlers were not wired to the runtime report store")
	}
	createdAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, value := range []struct {
		taskID modulecore.TaskID
		goal   string
	}{
		{taskID: publicTask.TaskID, goal: "public evidence goal"},
		{taskID: privateTask.TaskID, goal: "private owner-bound evidence goal"},
		{taskID: unknownTask.TaskID, goal: "unknown session evidence goal"},
		{taskID: blankTask.TaskID, goal: "blank session evidence goal"},
	} {
		if err := deps.reportStore.Save(ctx, domainexecution.ExecutionReport{
			TaskID:     value.taskID,
			Goal:       value.goal,
			Status:     "passed",
			CreatedAt:  createdAt,
			FinishedAt: createdAt,
		}); err != nil {
			t.Fatalf("save execution report for %s: %v", value.taskID, err)
		}
	}

	recent := httptest.NewRecorder()
	deps.evidenceHandler.ServeHTTP(recent, httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent?limit=20", nil))
	if recent.Code != http.StatusOK || !strings.Contains(recent.Body.String(), string(publicTask.TaskID)) || !strings.Contains(recent.Body.String(), "public evidence goal") {
		t.Fatalf("public evidence status=%d body=%s", recent.Code, recent.Body.String())
	}
	for _, secret := range []string{
		string(privateTask.TaskID), "private owner-bound evidence goal",
		string(unknownTask.TaskID), "unknown session evidence goal",
		string(blankTask.TaskID), "blank session evidence goal",
	} {
		if strings.Contains(recent.Body.String(), secret) {
			t.Fatalf("recent evidence exposed %q: %s", secret, recent.Body.String())
		}
	}

	publicDetail := httptest.NewRecorder()
	deps.evidenceDetail.ServeHTTP(publicDetail, httptest.NewRequest(http.MethodGet, "/viewer/evidence/detail?task_id="+string(publicTask.TaskID), nil))
	if publicDetail.Code != http.StatusOK || !strings.Contains(publicDetail.Body.String(), "public evidence goal") {
		t.Fatalf("public evidence detail status=%d body=%s", publicDetail.Code, publicDetail.Body.String())
	}
	for _, task := range []domaintask.Task{privateTask, unknownTask, blankTask} {
		detail := httptest.NewRecorder()
		path := "/viewer/evidence/detail?task_id=" + string(task.TaskID) + "&session_id=" + string(publicSessionID)
		deps.evidenceDetail.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, path, nil))
		if detail.Code != http.StatusNotFound || strings.Contains(detail.Body.String(), "evidence goal") {
			t.Fatalf("evidence detail exposed task %s: status=%d body=%s", task.TaskID, detail.Code, detail.Body.String())
		}
	}

	summary := httptest.NewRecorder()
	deps.evidenceSummary.ServeHTTP(summary, httptest.NewRequest(http.MethodGet, "/viewer/evidence/summary", nil))
	if summary.Code != http.StatusServiceUnavailable || strings.Contains(summary.Body.String(), `"passed":4`) {
		t.Fatalf("unscoped evidence summary status=%d body=%s", summary.Code, summary.Body.String())
	}
}

func TestInitializeRuntimeTaskOwnerRejectsDuplicateWithoutReplacingUsableOwner(t *testing.T) {
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err != nil {
		t.Fatalf("initialize task owner: %v", err)
	}
	ownerStore := deps.taskStore
	ownerManager := deps.taskManager
	t.Cleanup(func() {
		if err := ownerManager.Close(); err != nil {
			t.Errorf("close task owner: %v", err)
		}
	})
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err == nil {
		t.Fatal("duplicate Task owner initialization was accepted")
	}
	if deps.taskStore != ownerStore || deps.taskManager != ownerManager {
		t.Fatal("duplicate initialization replaced the canonical owner")
	}
	if _, err := ownerManager.Create(context.Background(), domaintask.Task{Title: "owner remains usable"}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("original Task owner became unusable: %v", err)
	}
}

func TestInitializeRuntimeTaskOwnerRejectsBusyWriterWithoutPublishingOwner(t *testing.T) {
	root := t.TempDir()
	busy, err := taskpersistence.NewJSONLStore(defaultTaskStorePath(root))
	if err != nil {
		t.Fatalf("open busy task writer: %v", err)
	}
	t.Cleanup(func() {
		if err := busy.Close(); err != nil {
			t.Errorf("close busy task writer: %v", err)
		}
	})
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, root); err == nil {
		t.Fatal("busy Task writer was accepted")
	}
	if deps.taskStore != nil || deps.taskManager != nil {
		t.Fatal("failed Task owner initialization published dependencies")
	}
}

func TestInitializeRuntimeTaskOwnerRejectsMissingDependenciesAndWorkspace(t *testing.T) {
	if err := initializeRuntimeTaskOwner(nil, t.TempDir()); err == nil {
		t.Fatal("nil dependencies were accepted")
	}
	if err := initializeRuntimeTaskOwner(&Dependencies{}, " "); err == nil {
		t.Fatal("empty workspace was accepted")
	}
}

func TestBuildViewerRuntimeHandlersRegistersSourceRegistryUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerSourceRegistry == nil {
		t.Fatal("viewerSourceRegistry handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/source-registry", nil)
	rec := httptest.NewRecorder()
	deps.viewerSourceRegistry.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "source registry unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersMemoryLayersUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerMemoryLayers == nil {
		t.Fatal("viewerMemoryLayers handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/layers", nil)
	rec := httptest.NewRecorder()
	deps.viewerMemoryLayers.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "memory layers unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersRecallTraceUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerRecallTraces == nil {
		t.Fatal("viewerRecallTraces handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/recall/traces?limit=5", nil)
	rec := httptest.NewRecorder()
	deps.viewerRecallTraces.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersDomainGraphUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerDomainGraphAssertions == nil {
		t.Fatal("viewerDomainGraphAssertions handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/domain-graph/assertions", nil)
	rec := httptest.NewRecorder()
	deps.viewerDomainGraphAssertions.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "domain graph unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersMovieDomainGraphSyncUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerMovieDomainGraphSync == nil {
		t.Fatal("viewerMovieDomainGraphSync handler is nil")
	}

	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	deps.viewerMovieDomainGraphSync.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "movie domain graph sync unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersHobbyDomainGraphSyncUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerHobbyDomainGraphSync == nil {
		t.Fatal("viewerHobbyDomainGraphSync handler is nil")
	}

	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	deps.viewerHobbyDomainGraphSync.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "hobby domain graph sync unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestRuntimeToolAdmissionRemainsMandatoryWithoutSecurity(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, cfg.WorkspaceDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deps.taskManager.Close() })
	task, err := deps.taskManager.Create(context.Background(), domaintask.Task{Title: "runtime admission", Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := deps.taskManager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope("runtime-admission-test", domaintool.ActorKindAgent, "shiro", "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	ctx = domaintool.WithToolExecutionScope(ctx, scope)
	runtime := buildToolRuntimeWithCapabilities(deps.taskManager, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
	path := filepath.Join(cfg.WorkspaceDir, "admission.txt")
	if err := os.WriteFile(path, []byte("owner-visible-content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, runner := range []domaintool.RunnerV2{runtime.ChatRuntimeRunnerV2, runtime.WorkerRuntimeRunnerV2} {
		if _, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{"path": path}); err == nil {
			t.Fatal("missing identity accepted with security disabled")
		}
		response, err := runner.ExecuteV2(ctx, "file_read", map[string]any{"path": path})
		if err != nil || response == nil || response.IsError() || !strings.Contains(response.String(), "owner-visible-content") {
			t.Fatalf("valid runtime execution: response=%v error=%v", response, err)
		}
	}
	if _, err := deps.taskManager.Succeed(ctx, task.TaskID, "done"); err != nil {
		t.Fatal(err)
	}
	for _, runner := range []domaintool.RunnerV2{runtime.ChatRuntimeRunnerV2, runtime.WorkerRuntimeRunnerV2} {
		if _, err := runner.ExecuteV2(ctx, "file_read", map[string]any{"path": path}); err == nil {
			t.Fatal("terminal Run admitted")
		}
	}
}

// runtimeToolOwnerFixture supplies real persisted owner state for runtime runner tests.
func runtimeToolOwnerFixture(t *testing.T, workspace, actor string) (*taskmanager.Manager, context.Context) {
	t.Helper()
	if workspace == "" {
		workspace = t.TempDir()
	}
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Error(err)
		}
	})
	task, err := deps.taskManager.Create(context.Background(), domaintask.Task{Title: "runtime tool fixture", Assignee: actor}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := deps.taskManager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope("runtime-tool-fixture", domaintool.ActorKindAgent, actor, "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return deps.taskManager, domaintool.WithToolExecutionScope(ctx, scope)
}
