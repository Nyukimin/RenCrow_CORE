package viewer

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type monitorVisibilityArchiveStub struct {
	events []orchestrator.OrchestratorEvent
}

func (s monitorVisibilityArchiveStub) Query(_ context.Context, filter LogFilter) ([]orchestrator.OrchestratorEvent, error) {
	items := make([]orchestrator.OrchestratorEvent, 0, len(s.events))
	for _, event := range s.events {
		if matchesLogFilter(event, filter) {
			items = append(items, event)
		}
	}
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func TestMonitorStoreDoesNotPublishPrivateEventsWithoutVisibilityResolution(t *testing.T) {
	private := orchestrator.OrchestratorEvent{
		Type:       "agent.response",
		From:       "mio",
		To:         "user",
		Content:    "PRIVATE_MONITOR_SENTINEL",
		TaskID:     modulecore.NewTaskID(),
		SessionID:  modulecore.NewSessionID(),
		Visibility: "private",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	store := NewMonitorStore(nil, monitorVisibilityArchiveStub{events: []orchestrator.OrchestratorEvent{private}})

	store.OnEvent(private)
	if got := store.Logs(LogFilter{}); len(got) != 0 {
		t.Fatalf("RAM logs retained unresolved private event: %#v", got)
	}
	if got := store.TaskActivities(TaskActivityFilter{}); len(got) != 0 {
		t.Fatalf("task activity retained unresolved private event: %#v", got)
	}
	if got := store.Summary(); got.StoredLogs != 0 {
		t.Fatalf("summary counted unresolved private event: %+v", got)
	}
	archived, err := store.ArchivedLogs(context.Background(), LogFilter{})
	if err != nil {
		t.Fatalf("ArchivedLogs() error = %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("archived logs exposed unresolved private event: %#v", archived)
	}
}

func TestMonitorStoreVisibilityPredicateFiltersRAMAndArchiveProjections(t *testing.T) {
	publicTask := modulecore.NewTaskID()
	privateTask := modulecore.NewTaskID()
	unknownTask := modulecore.NewTaskID()
	errorTask := modulecore.NewTaskID()
	public := monitorVisibilityEvent(publicTask, "public", "PUBLIC_EVENT")
	private := monitorVisibilityEvent(privateTask, "private", "PRIVATE_SENTINEL")
	unknown := monitorVisibilityEvent(unknownTask, "owner_only", "UNKNOWN_SENTINEL")
	resolutionError := monitorVisibilityEvent(errorTask, "public", "RESOLUTION_ERROR_SENTINEL")
	resolutionError.EventSeq = 4
	private.EventSeq = 3
	unknown.EventSeq = 2
	public.EventSeq = 1
	archive := monitorVisibilityArchiveStub{events: []orchestrator.OrchestratorEvent{resolutionError, private, unknown, public}}
	store := NewMonitorStore(nil, archive)
	checkingVisibility := false
	store.SetPublicEventVisibilityPredicate(func(ctx context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
		// The predicate must run outside the Monitor mutex. Summary takes that mutex.
		if !checkingVisibility {
			checkingVisibility = true
			_ = store.Summary()
			checkingVisibility = false
		}
		if event.Content == "RESOLUTION_ERROR_SENTINEL" {
			return false, errors.New("session owner lookup failed")
		}
		return event.Visibility == "public", nil
	})

	store.OnEvent(public)
	store.OnEvent(private)
	store.OnEvent(unknown)
	store.OnEvent(resolutionError)

	if got := store.Logs(LogFilter{}); len(got) != 1 || got[0].Content != "PUBLIC_EVENT" {
		t.Fatalf("RAM logs = %#v, want only the explicitly public event", got)
	}
	if got := store.TaskActivities(TaskActivityFilter{}); len(got) != 1 || got[0].TaskID != publicTask.String() {
		t.Fatalf("RAM task activities = %#v, want only the public task", got)
	}
	if got := store.Status().TaskActivities; len(got) != 1 || got[0].TaskID != publicTask.String() {
		t.Fatalf("status task activities = %#v, want only the public task", got)
	}
	if got := store.Summary(); got.StoredLogs != 1 || got.ByType["agent.response"] != 1 {
		t.Fatalf("summary = %+v, want one public event", got)
	}
	activities, ok := store.AgentDetail(context.Background(), "mio", 10)
	if !ok || activities.Agent.Preview != "PUBLIC_EVENT" || len(activities.ActiveTaskActivities) != 1 || len(activities.Events) != 1 {
		t.Fatalf("AgentDetail() = %+v, found=%v, want only public event projections", activities, ok)
	}
	if detail, ok := store.TaskActivityDetail(context.Background(), publicTask); !ok || len(detail.Item.Events) != 1 || detail.Item.Events[0].Content != "PUBLIC_EVENT" {
		t.Fatalf("public TaskActivityDetail() = %+v, found=%v", detail, ok)
	}
	if _, ok := store.TaskActivityDetail(context.Background(), privateTask); ok {
		t.Fatal("private task activity detail was retained")
	}

	archived, err := store.ArchivedLogs(context.Background(), LogFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ArchivedLogs() error = %v", err)
	}
	if len(archived) != 1 || archived[0].Content != "PUBLIC_EVENT" {
		t.Fatalf("ArchivedLogs() = %#v, want public event after filtering private rows before limit", archived)
	}
	for _, event := range append(append([]orchestrator.OrchestratorEvent(nil), activities.Events...), archived...) {
		if strings.Contains(event.Content, "SENTINEL") {
			t.Fatalf("public Monitor output contains hidden event: %#v", event)
		}
	}
}

func TestMonitorTaskActivityDetailRevalidatesEventsAfterEvidenceReadBlocks(t *testing.T) {
	taskID := modulecore.NewTaskID()
	event := monitorVisibilityEvent(taskID, "public", "STALE_MONITOR_EVENT_SENTINEL")
	evidence := &blockingEvidenceStore{
		evidenceStoreStub: &evidenceStoreStub{items: []domainexecution.ExecutionReport{{TaskID: taskID, Goal: "evidence"}}},
		getEntered:        make(chan struct{}),
		getRelease:        make(chan struct{}),
	}
	store := NewMonitorStore(evidence, nil)
	var currentIncarnationVisible atomic.Bool
	currentIncarnationVisible.Store(true)
	store.SetPublicEventVisibilityPredicate(func(context.Context, orchestrator.OrchestratorEvent) (bool, error) {
		return currentIncarnationVisible.Load(), nil
	})
	store.OnEvent(event)

	type result struct {
		detail TaskActivityDetail
		found  bool
	}
	completed := make(chan result, 1)
	go func() {
		detail, found := store.TaskActivityDetail(context.Background(), taskID)
		completed <- result{detail: detail, found: found}
	}()
	waitForViewerSignal(t, evidence.getEntered, "Monitor evidence read")
	currentIncarnationVisible.Store(false)
	close(evidence.getRelease)
	var got result
	select {
	case got = <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Monitor TaskActivityDetail response")
	}
	if got.found {
		t.Fatalf("TaskActivityDetail returned a stale event after Session rebind: found=%v detail=%+v", got.found, got.detail)
	}
	for _, returned := range got.detail.Item.Events {
		if strings.Contains(returned.Content, "STALE_MONITOR_EVENT_SENTINEL") {
			t.Fatalf("TaskActivityDetail leaked stale event: %+v", got.detail)
		}
	}
}

func monitorVisibilityEvent(taskID modulecore.TaskID, visibility, content string) orchestrator.OrchestratorEvent {
	return orchestrator.OrchestratorEvent{
		Type:       "agent.response",
		From:       "mio",
		To:         "user",
		Content:    content,
		TaskID:     taskID,
		SessionID:  monitorTestSessionID,
		Visibility: visibility,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
}
