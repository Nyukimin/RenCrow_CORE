package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeMonitorVisibilityUsesCanonicalSessionOwnership(t *testing.T) {
	ctx := context.Background()
	repository := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	publicSessionID := saveRuntimeMonitorSession(t, ctx, repository, "")
	privateSessionID := saveRuntimeMonitorSession(t, ctx, repository, "ren")
	if err := repository.WarmCanonicalIndex(ctx); err != nil {
		t.Fatalf("warm canonical Session visibility projection: %v", err)
	}
	missingSessionID := modulecore.NewSessionID()

	store := viewer.NewMonitorStore(nil, nil)
	setRuntimeMonitorEventVisibility(store, newRuntimeSessionVisibility(repository))

	publicTaskID := modulecore.NewTaskID()
	publicEvent := runtimeMonitorTestEvent("agent.response", publicSessionID, publicTaskID, "public", "PUBLIC_MONITOR_EVENT")
	resolvedPublicSession, err := repository.Load(ctx, string(publicSessionID))
	if err != nil || resolvedPublicSession == nil {
		t.Fatalf("load public Monitor Session=%v err=%v", resolvedPublicSession, err)
	}
	publicEvent.PublicationIncarnation = resolvedPublicSession.PublicationIncarnation()
	publicEvent.Channel = "viewer"
	publicEvent.ChannelAddress = resolvedPublicSession.ChannelAddress().ExternalConversationID()
	privateEvent := runtimeMonitorTestEvent("agent.response", privateSessionID, modulecore.NewTaskID(), "", "PRIVATE_MONITOR_SENTINEL")
	unknownVisibility := runtimeMonitorTestEvent("agent.response", publicSessionID, modulecore.NewTaskID(), "owner_only", "UNKNOWN_VISIBILITY_SENTINEL")
	unresolvedSession := runtimeMonitorTestEvent("agent.response", missingSessionID, modulecore.NewTaskID(), "public", "UNRESOLVED_SESSION_SENTINEL")
	malformedSession := runtimeMonitorTestEvent("message.received", modulecore.SessionID("malformed"), modulecore.NewTaskID(), "public", "MALFORMED_SESSION_SENTINEL")
	malformedTask := runtimeMonitorTestEvent("task.started", publicSessionID, modulecore.TaskID("malformed"), "public", "MALFORMED_TASK_SENTINEL")
	conversationWithoutSession := runtimeMonitorTestEvent("message.received", "", modulecore.NewTaskID(), "public", "MISSING_SESSION_SENTINEL")
	unknownGlobal := runtimeMonitorTestEvent("new.global.event", "", "", "public", "UNKNOWN_GLOBAL_SENTINEL")
	safeGlobal := runtimeMonitorTestEvent("agent.unavailable", "", "", "public", "GLOBAL_UNAVAILABLE")

	for _, event := range []orchestrator.OrchestratorEvent{
		publicEvent,
		privateEvent,
		unknownVisibility,
		unresolvedSession,
		malformedSession,
		malformedTask,
		conversationWithoutSession,
		unknownGlobal,
		safeGlobal,
	} {
		store.OnEvent(event)
	}

	logs := store.Logs(viewer.LogFilter{})
	if len(logs) != 2 {
		t.Fatalf("public Monitor log count = %d, want the canonical public event and allowlisted global status event: %#v", len(logs), logs)
	}
	contents := map[string]bool{}
	for _, event := range logs {
		contents[event.Content] = true
	}
	if !contents["PUBLIC_MONITOR_EVENT"] || !contents["GLOBAL_UNAVAILABLE"] {
		t.Fatalf("public Monitor logs = %#v, want only the public session event and allowlisted global event", logs)
	}
	for content := range contents {
		if strings.Contains(content, "SENTINEL") {
			t.Fatalf("hidden Monitor event escaped: %q", content)
		}
	}
	if got := store.Summary(); got.StoredLogs != 2 || got.ByType["agent.response"] != 1 || got.ByType["agent.unavailable"] != 1 {
		t.Fatalf("public Monitor summary = %+v, want only two admitted events", got)
	}
	if got := store.TaskActivities(viewer.TaskActivityFilter{}); len(got) != 1 || got[0].TaskID != publicTaskID.String() {
		t.Fatalf("public Monitor task activities = %#v, want only the public session task", got)
	}
	if _, ok := store.TaskActivityDetail(ctx, modulecore.TaskID(privateEvent.TaskID)); ok {
		t.Fatal("private task activity detail was retained")
	}
}

func TestRuntimeMonitorLogsRecheckSessionIncarnationAfterSameIDRecreation(t *testing.T) {
	ctx := context.Background()
	repository := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	sessionID := modulecore.NewSessionID()
	externalID := "monitor-recreated-" + string(modulecore.NewTaskID())
	address, err := conversation.NewChannelAddress("viewer", externalID)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	oldSession, err := domainsession.NewCanonicalSession(sessionID, "2026-10-24", address, createdAt)
	if err != nil {
		t.Fatalf("create original public Session: %v", err)
	}
	if err := repository.Save(ctx, oldSession); err != nil {
		t.Fatalf("save original public Session: %v", err)
	}
	if err := repository.WarmCanonicalIndex(ctx); err != nil {
		t.Fatalf("warm original Session projection: %v", err)
	}
	eventStore, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "monitor-events.sqlite"))
	if err != nil {
		t.Fatalf("create Monitor EventStore: %v", err)
	}
	defer eventStore.Close()
	archive, err := viewer.NewCanonicalEventLog(eventStore)
	if err != nil {
		t.Fatalf("create canonical Monitor archive: %v", err)
	}
	store := viewer.NewMonitorStore(nil, archive)
	setRuntimeMonitorEventVisibility(store, newRuntimeSessionVisibility(repository))
	taskID := modulecore.NewTaskID()
	event := orchestrator.NewEvent("agent.response", "mio", "user", "OLD_PUBLIC_SESSION_SENTINEL", "CHAT", taskID.String(), string(sessionID), "viewer", externalID)
	event.Visibility = "public"
	event.PublicationIncarnation = oldSession.PublicationIncarnation()
	store.OnEvent(event)
	if _, err := archive.AppendSequenced(event); err != nil {
		t.Fatalf("archive original public event: %v", err)
	}
	if got := store.Logs(viewer.LogFilter{}); len(got) != 1 || got[0].Content != event.Content {
		t.Fatalf("initial public Monitor log = %#v, want the admitted event", got)
	}
	if status := store.Status(); len(status.TaskActivities) != 1 || status.TaskActivities[0].Summary != event.Content || len(status.TaskActivities[0].Events) != 1 {
		t.Fatalf("initial public Monitor status = %+v, want the admitted event projection", status)
	}
	if summary := store.Summary(); summary.StoredLogs != 1 {
		t.Fatalf("initial public Monitor summary = %+v, want one admitted event", summary)
	}
	if detail, found := store.AgentDetail(ctx, "mio", 10); !found || detail.Agent.Preview != event.Content || len(detail.ActiveTaskActivities) != 1 {
		t.Fatalf("initial public AgentDetail() = %+v, found=%v, want the admitted event projection", detail, found)
	}
	if detail, found := store.TaskActivityDetail(ctx, taskID); !found || detail.Item.Summary != event.Content || len(detail.Item.Events) != 1 {
		t.Fatalf("initial public TaskActivityDetail() = %+v, found=%v, want the admitted event projection", detail, found)
	}

	if err := repository.Delete(ctx, string(sessionID)); err != nil {
		t.Fatalf("delete original public Session: %v", err)
	}
	recreated, err := domainsession.NewCanonicalSessionWithOwner(sessionID, "ren", "2026-10-24", address, createdAt)
	if err != nil {
		t.Fatalf("create owner-bound same-ID Session: %v", err)
	}
	if err := repository.Save(ctx, recreated); err != nil {
		t.Fatalf("save owner-bound same-ID Session: %v", err)
	}
	if recreated.PublicationIncarnation() == oldSession.PublicationIncarnation() {
		t.Fatal("same-ID Session recreation reused the prior publication incarnation")
	}

	if got := store.Logs(viewer.LogFilter{}); len(got) != 0 {
		t.Fatalf("RAM Monitor logs retained old public event after private recreation: %#v", got)
	}
	archived, err := store.ArchivedLogs(ctx, viewer.LogFilter{})
	if err != nil {
		t.Fatalf("ArchivedLogs after same-ID recreation: %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("archived Monitor logs retained old public event after private recreation: %#v", archived)
	}
	if status := store.Status(); len(status.TaskActivities) != 0 || strings.Contains(status.Chat.Preview, event.Content) || strings.Contains(status.Worker.Preview, event.Content) {
		t.Fatalf("Monitor status retained derived data from revoked event: %+v", status)
	}
	if summary := store.Summary(); summary.StoredLogs != 0 || summary.ByType["agent.response"] != 0 {
		t.Fatalf("Monitor summary retained revoked event counts: %+v", summary)
	}
	if activities := store.TaskActivities(viewer.TaskActivityFilter{}); len(activities) != 0 {
		t.Fatalf("Monitor task activities retained revoked event: %#v", activities)
	}
	if detail, found := store.AgentDetail(ctx, "mio", 10); !found {
		t.Fatal("AgentDetail lost the registered Mio agent")
	} else if strings.Contains(detail.Agent.Preview, event.Content) || len(detail.ActiveTaskActivities) != 0 || len(detail.Events) != 0 {
		t.Fatalf("AgentDetail retained derived data from revoked event: %+v", detail)
	}
	if detail, found := store.TaskActivityDetail(ctx, taskID); found {
		t.Fatalf("TaskActivityDetail retained derived data after the archive filtered every revoked event: %+v", detail)
	}
	for _, query := range []string{"", "?scope=persisted"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/viewer/logs"+query, nil)
		viewer.HandleMonitorLogs(store).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), event.Content) {
			t.Fatalf("Monitor handler leaked stale event for scope %q: status=%d body=%s", query, recorder.Code, recorder.Body.String())
		}
	}
}

func saveRuntimeMonitorSession(t *testing.T, ctx context.Context, repository *sessionpersistence.JSONSessionRepository, ownerID string) modulecore.SessionID {
	t.Helper()
	address, err := conversation.NewChannelAddress("viewer", "monitor-visibility-"+string(modulecore.NewSessionID()))
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	id := modulecore.NewSessionID()
	var value *domainsession.Session
	if ownerID == "" {
		value, err = domainsession.NewCanonicalSession(id, "2026-10-24", address, time.Now().UTC())
	} else {
		value, err = domainsession.NewCanonicalSessionWithOwner(id, ownerID, "2026-10-24", address, time.Now().UTC())
	}
	if err != nil {
		t.Fatalf("create canonical session: %v", err)
	}
	if err := repository.Save(ctx, value); err != nil {
		t.Fatalf("save canonical session: %v", err)
	}
	return id
}

func runtimeMonitorTestEvent(eventType string, sessionID modulecore.SessionID, taskID modulecore.TaskID, visibility, content string) orchestrator.OrchestratorEvent {
	return orchestrator.OrchestratorEvent{
		Type:       eventType,
		From:       "mio",
		To:         "user",
		Content:    content,
		TaskID:     taskID,
		SessionID:  sessionID,
		Visibility: visibility,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
}
