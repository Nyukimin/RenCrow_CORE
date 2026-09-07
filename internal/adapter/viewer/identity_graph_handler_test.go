package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type identityGraphEventReaderStub struct {
	events         []modulecore.EventEnvelope
	err            error
	calls          int
	requestedTrace modulecore.TraceID
	requestedMax   int
}

func (s *identityGraphEventReaderStub) ListByTraceID(_ context.Context, traceID modulecore.TraceID, max int) ([]modulecore.EventEnvelope, error) {
	s.calls++
	s.requestedTrace = traceID
	s.requestedMax = max
	if s.err != nil {
		return nil, s.err
	}
	return append([]modulecore.EventEnvelope(nil), s.events...), nil
}

type identityGraphTaskReaderStub struct {
	tasks  map[modulecore.TaskID]domaintask.Task
	errors map[modulecore.TaskID]error
	calls  []modulecore.TaskID
}

func (s *identityGraphTaskReaderStub) GetTask(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	s.calls = append(s.calls, taskID)
	if err := s.errors[taskID]; err != nil {
		return domaintask.Task{}, err
	}
	if task, ok := s.tasks[taskID]; ok {
		return task, nil
	}
	return domaintask.Task{}, domaintask.ErrNotFound
}

func TestIdentityGraphHandlerProjectsCanonicalTraceDeterministically(t *testing.T) {
	traceID := modulecore.NewTraceID()
	childTaskID := modulecore.NewTaskID()
	parentTaskID := modulecore.NewTaskID()
	dependencyTaskID := modulecore.NewTaskID()
	supersededTaskID := modulecore.NewTaskID()
	tasks := &identityGraphTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{
		childTaskID:      identityGraphTestTask(childTaskID, parentTaskID, []modulecore.TaskID{dependencyTaskID}, supersededTaskID, domaintask.StatusRunning),
		parentTaskID:     identityGraphTestTask(parentTaskID, "", nil, "", domaintask.StatusSucceeded),
		dependencyTaskID: identityGraphTestTask(dependencyTaskID, "", nil, "", domaintask.StatusQueued),
		supersededTaskID: identityGraphTestTask(supersededTaskID, "", nil, "", domaintask.StatusSuperseded),
	}}

	root := identityGraphTestEvent(traceID, 1, "task.started", childTaskID, modulecore.NewMessageID(), "secret payload", "agent", "mio")
	child := identityGraphTestEvent(traceID, 2, "task.completed", childTaskID, modulecore.NewMessageID(), "secret raw content", "", "")
	child.CausationEventID = root.EventID
	sourceEvents := []modulecore.EventEnvelope{child, root}
	before := append([]modulecore.EventEnvelope(nil), sourceEvents...)
	events := &identityGraphEventReaderStub{events: sourceEvents}
	handler := HandleIdentityGraph(events, tasks)

	first := identityGraphPerformRequest(t, handler, traceID)
	second := identityGraphPerformRequest(t, handler, traceID)
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("same canonical graph was not deterministic:\nfirst=%s\nsecond=%s", first.Body, second.Body)
	}
	if events.calls != 2 || events.requestedTrace != traceID || events.requestedMax != identityGraphEventLimit {
		t.Fatalf("event lookup = calls:%d trace:%s max:%d", events.calls, events.requestedTrace, events.requestedMax)
	}
	if !reflect.DeepEqual(sourceEvents, before) {
		t.Fatal("handler mutated the event reader input")
	}

	var response identityGraphResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Schema != IdentityGraphSchemaVersion || response.TraceID != traceID {
		t.Fatalf("identity graph header = %#v", response)
	}
	if len(response.Events) != 2 || response.Events[0].EventSeq != 1 || response.Events[1].EventSeq != 2 {
		t.Fatalf("events = %#v, want EventSeq order 1,2", response.Events)
	}
	if len(response.EventEdges) != 1 || response.EventEdges[0].SourceEventID != root.EventID || response.EventEdges[0].TargetEventID != child.EventID || response.EventEdges[0].Kind != "causation" {
		t.Fatalf("event edges = %#v", response.EventEdges)
	}
	if len(response.Tasks) != 4 || len(response.TaskEdges) != 3 {
		t.Fatalf("task graph = tasks:%#v edges:%#v", response.Tasks, response.TaskEdges)
	}
	if len(response.Messages) != 2 || len(response.MessageEdges) != 2 {
		t.Fatalf("message graph = messages:%#v edges:%#v", response.Messages, response.MessageEdges)
	}
	if response.Events[0].ActorKind != "agent" || response.Events[0].ActorID != "mio" {
		t.Fatalf("recorded actor was not preserved: %#v", response.Events[0])
	}
	if response.Events[1].ActorKind != "" || response.Events[1].ActorID != "" {
		t.Fatalf("unrecorded actor was synthesized: %#v", response.Events[1])
	}
	for _, forbidden := range []string{"secret payload", "secret raw content", "raw_content", "payload", "module_root", "\"title\"", "\"from\"", "\"to\""} {
		if strings.Contains(first.Body.String(), forbidden) {
			t.Fatalf("response contains forbidden field/value %q: %s", forbidden, first.Body)
		}
	}
	if response.Events == nil || response.EventEdges == nil || response.Tasks == nil || response.TaskEdges == nil || response.Messages == nil || response.MessageEdges == nil {
		t.Fatal("response contains a nil graph array")
	}
}

func TestIdentityGraphHandlerRejectsMalformedQueriesAndUnavailableOwners(t *testing.T) {
	traceID := modulecore.NewTraceID()
	events := &identityGraphEventReaderStub{events: []modulecore.EventEnvelope{identityGraphTestEvent(traceID, 1, "trace.started", "", "", "", "", "")}}
	tasks := &identityGraphTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{}}

	tests := []struct {
		name   string
		method string
		target string
		events IdentityGraphEventReader
		tasks  IdentityGraphTaskReader
		want   int
	}{
		{name: "missing trace", method: http.MethodGet, target: "/viewer/identity-graph", events: events, tasks: tasks, want: http.StatusBadRequest},
		{name: "invalid trace", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=not-a-trace", events: events, tasks: tasks, want: http.StatusBadRequest},
		{name: "unknown query", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID) + "&debug=1", events: events, tasks: tasks, want: http.StatusBadRequest},
		{name: "repeated trace", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID) + "&trace_id=" + string(traceID), events: events, tasks: tasks, want: http.StatusBadRequest},
		{name: "method", method: http.MethodPost, target: "/viewer/identity-graph?trace_id=" + string(traceID), events: events, tasks: tasks, want: http.StatusMethodNotAllowed},
		{name: "event owner unavailable", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID), events: nil, tasks: tasks, want: http.StatusServiceUnavailable},
		{name: "task owner unavailable", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID), events: events, tasks: nil, want: http.StatusServiceUnavailable},
		{name: "unknown trace", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID), events: &identityGraphEventReaderStub{}, tasks: tasks, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.target, nil)
			HandleIdentityGraph(test.events, test.tasks).ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body, test.want)
			}
		})
	}
}

func TestIdentityGraphHandlerRejectsClosedGraphAndTaskFailures(t *testing.T) {
	traceID := modulecore.NewTraceID()
	taskID := modulecore.NewTaskID()
	baseEvent := identityGraphTestEvent(traceID, 1, "trace.started", taskID, "", "", "", "")
	validTask := identityGraphTestTask(taskID, "", nil, "", domaintask.StatusQueued)

	tests := []struct {
		name       string
		events     []modulecore.EventEnvelope
		eventError error
		tasks      map[modulecore.TaskID]domaintask.Task
		taskErrors map[modulecore.TaskID]error
		want       int
	}{
		{name: "event sequence is not positive", events: []modulecore.EventEnvelope{func() modulecore.EventEnvelope { event := baseEvent; event.EventSeq = 0; return event }()}, tasks: map[modulecore.TaskID]domaintask.Task{taskID: validTask}, want: http.StatusConflict},
		{name: "event trace mismatch", events: []modulecore.EventEnvelope{baseEvent, identityGraphTestEvent(modulecore.NewTraceID(), 2, "other.trace", "", "", "", "", "")}, tasks: map[modulecore.TaskID]domaintask.Task{taskID: validTask}, want: http.StatusConflict},
		{name: "event cycle", events: func() []modulecore.EventEnvelope {
			first := identityGraphTestEvent(traceID, 1, "cycle.first", "", "", "", "", "")
			second := identityGraphTestEvent(traceID, 2, "cycle.second", "", "", "", "", "")
			first.CausationEventID = second.EventID
			second.CausationEventID = first.EventID
			return []modulecore.EventEnvelope{first, second}
		}(), tasks: map[modulecore.TaskID]domaintask.Task{}, want: http.StatusConflict},
		{name: "invalid actor kind", events: func() []modulecore.EventEnvelope {
			event := baseEvent
			event.ActorKind = "model"
			event.ActorID = "gpt"
			return []modulecore.EventEnvelope{event}
		}(), tasks: map[modulecore.TaskID]domaintask.Task{taskID: validTask}, want: http.StatusConflict},
		{name: "event limit read error", eventError: errors.New("trace lookup exceeded maximum"), tasks: map[modulecore.TaskID]domaintask.Task{}, want: http.StatusInternalServerError},
		{name: "event read error", eventError: errors.New("private sqlite path /secret/events.db"), tasks: map[modulecore.TaskID]domaintask.Task{}, want: http.StatusInternalServerError},
		{name: "missing task", events: []modulecore.EventEnvelope{baseEvent}, tasks: map[modulecore.TaskID]domaintask.Task{}, want: http.StatusConflict},
		{name: "task read error", events: []modulecore.EventEnvelope{baseEvent}, tasks: map[modulecore.TaskID]domaintask.Task{taskID: validTask}, taskErrors: map[modulecore.TaskID]error{taskID: errors.New("private task path /secret/tasks.jsonl")}, want: http.StatusInternalServerError},
		{name: "mismatched task", events: []modulecore.EventEnvelope{baseEvent}, tasks: map[modulecore.TaskID]domaintask.Task{taskID: identityGraphTestTask(modulecore.NewTaskID(), "", nil, "", domaintask.StatusQueued)}, want: http.StatusConflict},
		{name: "task cycle", events: []modulecore.EventEnvelope{baseEvent}, tasks: func() map[modulecore.TaskID]domaintask.Task {
			otherID := modulecore.NewTaskID()
			first := identityGraphTestTask(taskID, otherID, nil, "", domaintask.StatusQueued)
			second := identityGraphTestTask(otherID, taskID, nil, "", domaintask.StatusQueued)
			return map[modulecore.TaskID]domaintask.Task{taskID: first, otherID: second}
		}(), want: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &identityGraphEventReaderStub{events: test.events, err: test.eventError}
			taskReader := &identityGraphTaskReaderStub{tasks: test.tasks, errors: test.taskErrors}
			recorder := identityGraphPerformRequest(t, HandleIdentityGraph(reader, taskReader), traceID)
			if recorder.Code != test.want {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body, test.want)
			}
			if test.want == http.StatusInternalServerError && strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("raw owner error leaked: %s", recorder.Body)
			}
		})
	}
}

func TestIdentityGraphHandlerRejectsMoreThan256ReferencedTasks(t *testing.T) {
	traceID := modulecore.NewTraceID()
	rootEvents := make([]modulecore.EventEnvelope, 0, identityGraphTaskLimit+1)
	tasks := make(map[modulecore.TaskID]domaintask.Task, identityGraphTaskLimit+1)
	for index := 0; index < identityGraphTaskLimit+1; index++ {
		taskID := modulecore.NewTaskID()
		rootEvents = append(rootEvents, identityGraphTestEvent(traceID, modulecore.EventSeq(index+1), "task.observed", taskID, "", "", "", ""))
		tasks[taskID] = identityGraphTestTask(taskID, "", nil, "", domaintask.StatusQueued)
	}
	recorder := identityGraphPerformRequest(t, HandleIdentityGraph(&identityGraphEventReaderStub{events: rootEvents}, &identityGraphTaskReaderStub{tasks: tasks}), traceID)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body, http.StatusConflict)
	}
}

func TestIdentityGraphHandlerRejectsReturnedEventLimit(t *testing.T) {
	traceID := modulecore.NewTraceID()
	events := make([]modulecore.EventEnvelope, 0, identityGraphEventLimit+1)
	for index := 0; index < identityGraphEventLimit+1; index++ {
		events = append(events, identityGraphTestEvent(traceID, modulecore.EventSeq(index+1), "trace.observed", "", "", "", "", ""))
	}
	recorder := identityGraphPerformRequest(t, HandleIdentityGraph(&identityGraphEventReaderStub{events: events}, &identityGraphTaskReaderStub{}), traceID)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body, http.StatusConflict)
	}
}

func TestIdentityGraphHandlerReconstructsTheSameCanonicalStoresAfterRestart(t *testing.T) {
	root := t.TempDir()
	eventPath := filepath.Join(root, "events.sqlite")
	taskRoot := filepath.Join(root, "tasks")
	tasks, err := taskstore.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	t.Cleanup(func() {
		if err := tasks.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	traceID := modulecore.NewTraceID()
	childTaskID := modulecore.NewTaskID()
	parentTaskID := modulecore.NewTaskID()
	if err := tasks.SaveTask(context.Background(), identityGraphTestTask(parentTaskID, "", nil, "", domaintask.StatusSucceeded)); err != nil {
		t.Fatalf("SaveTask(parent): %v", err)
	}
	if err := tasks.SaveTask(context.Background(), identityGraphTestTask(childTaskID, parentTaskID, nil, "", domaintask.StatusRunning)); err != nil {
		t.Fatalf("SaveTask(child): %v", err)
	}

	store, err := eventstore.NewSQLiteStore(eventPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	first := identityGraphTestEvent(traceID, 1, "task.started", childTaskID, modulecore.NewMessageID(), "canonical private payload", "agent", "mio")
	first.EventSeq = 0
	storedFirst, err := store.AppendSequenced(context.Background(), first)
	if err != nil {
		_ = store.Close()
		t.Fatalf("AppendSequenced(first): %v", err)
	}
	second := identityGraphTestEvent(traceID, 2, "task.updated", childTaskID, modulecore.NewMessageID(), "canonical raw content", "", "")
	second.EventSeq = 0
	second.CausationEventID = storedFirst.EventID
	if _, err := store.AppendSequenced(context.Background(), second); err != nil {
		_ = store.Close()
		t.Fatalf("AppendSequenced(second): %v", err)
	}

	firstResponse := identityGraphPerformRequest(t, HandleIdentityGraph(store, tasks), traceID)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first graph status = %d body=%s", firstResponse.Code, firstResponse.Body)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	reopened, err := eventstore.NewSQLiteStore(eventPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopened): %v", err)
	}
	defer func() { _ = reopened.Close() }()
	secondResponse := identityGraphPerformRequest(t, HandleIdentityGraph(reopened, tasks), traceID)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("reopened graph status = %d body=%s", secondResponse.Code, secondResponse.Body)
	}
	if !bytes.Equal(firstResponse.Body.Bytes(), secondResponse.Body.Bytes()) {
		t.Fatalf("reopened graph differs:\nfirst=%s\nreopened=%s", firstResponse.Body, secondResponse.Body)
	}
	for _, forbidden := range []string{"canonical private payload", "canonical raw content", "payload", "raw_content", "module_root", "title"} {
		if strings.Contains(firstResponse.Body.String(), forbidden) {
			t.Fatalf("persisted graph leaked %q: %s", forbidden, firstResponse.Body)
		}
	}
}

func identityGraphPerformRequest(t *testing.T, handler http.Handler, traceID modulecore.TraceID) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/viewer/identity-graph?trace_id="+string(traceID), nil)
	handler.ServeHTTP(recorder, request)
	return recorder
}

func identityGraphTestEvent(traceID modulecore.TraceID, sequence modulecore.EventSeq, eventType string, taskID modulecore.TaskID, messageID modulecore.MessageID, secret string, actorKind, actorID string) modulecore.EventEnvelope {
	event := modulecore.NewEventEnvelope(traceID, "", nil, "orchestrator", eventType, time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), map[string]any{
		"content":     secret,
		"raw_content": secret,
		"module_root": "/private/module/root",
		"title":       "private task title",
	})
	event.EventSeq = sequence
	event.TaskID = taskID
	event.MessageID = messageID
	event.ActorKind = actorKind
	event.ActorID = actorID
	return event
}

func identityGraphTestTask(taskID, parentTaskID modulecore.TaskID, dependencyTaskIDs []modulecore.TaskID, supersedesTaskID modulecore.TaskID, status domaintask.Status) domaintask.Task {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	if dependencyTaskIDs == nil {
		dependencyTaskIDs = make([]modulecore.TaskID, 0)
	}
	return domaintask.Task{
		TaskID:            taskID,
		Title:             "private task title",
		Status:            status,
		Priority:          domaintask.PriorityNormal,
		Route:             domaintask.RouteGeneral,
		ParentTaskID:      parentTaskID,
		DependencyTaskIDs: dependencyTaskIDs,
		SupersedesTaskID:  supersedesTaskID,
		InterruptPolicy:   domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}
