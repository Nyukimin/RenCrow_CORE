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
	onGet  func(modulecore.TaskID)
}

func (s *identityGraphTaskReaderStub) GetTask(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	s.calls = append(s.calls, taskID)
	if s.onGet != nil {
		s.onGet(taskID)
	}
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
	handler := identityGraphTestHandler(events, tasks)

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

func TestIdentityGraphLegacyHandlerDoesNotExposeUnfilteredTraceMetadata(t *testing.T) {
	traceID := modulecore.NewTraceID()
	taskID := modulecore.NewTaskID()
	sessionID := modulecore.NewSessionID()
	event := identityGraphTestEvent(traceID, 1, "task.started", taskID, modulecore.NewMessageID(), "private payload", "agent", "mio")
	identityGraphBindTestEvent(&event, sessionID, "0123456789abcdef0123456789abcdef")
	tasks := &identityGraphTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{
		taskID: func() domaintask.Task {
			task := identityGraphTestTask(taskID, "", nil, "", domaintask.StatusRunning)
			task.OriginSessionID = sessionID
			return task
		}(),
	}}
	events := &identityGraphEventReaderStub{events: []modulecore.EventEnvelope{event}}

	recorder := identityGraphPerformRequest(t, HandleIdentityGraph(events, tasks), traceID)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unfiltered identity graph status=%d body=%s, want %d", recorder.Code, recorder.Body, http.StatusNotFound)
	}
	if events.calls != 0 || len(tasks.calls) != 0 {
		t.Fatalf("legacy handler queried stores without trusted visibility: event_calls=%d task_calls=%#v", events.calls, tasks.calls)
	}
}

func TestIdentityGraphVisibilityHidesPrivateReferencedTasksAndSessions(t *testing.T) {
	traceID := modulecore.NewTraceID()
	publicSessionID := modulecore.NewSessionID()
	privateSessionID := modulecore.NewSessionID()
	childTaskID := modulecore.NewTaskID()
	parentTaskID := modulecore.NewTaskID()
	event := identityGraphTestEvent(traceID, 1, "task.started", childTaskID, modulecore.NewMessageID(), "private payload", "agent", "mio")
	identityGraphBindTestEvent(&event, publicSessionID, "0123456789abcdef0123456789abcdef")
	child := identityGraphTestTask(childTaskID, parentTaskID, nil, "", domaintask.StatusRunning)
	child.OriginSessionID = publicSessionID
	parent := identityGraphTestTask(parentTaskID, "", nil, "", domaintask.StatusSucceeded)
	parent.OriginSessionID = privateSessionID
	taskReader := &identityGraphTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{
		childTaskID:  child,
		parentTaskID: parent,
	}}
	eventReader := &identityGraphEventReaderStub{events: []modulecore.EventEnvelope{event}}
	var checkedTasks []modulecore.TaskID
	taskVisibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		checkedTasks = append(checkedTasks, task.TaskID)
		return task.OriginSessionID == publicSessionID, nil
	})
	sessionVisibility := PublicSessionVisibility(func(_ context.Context, event modulecore.EventEnvelope) (bool, error) {
		return event.SessionID == publicSessionID, nil
	})

	recorder := identityGraphPerformRequest(t, HandleIdentityGraphWithPublicVisibility(eventReader, taskReader, taskVisibility, sessionVisibility), traceID)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("private related Task graph status=%d body=%s, want %d", recorder.Code, recorder.Body, http.StatusNotFound)
	}
	if strings.Contains(recorder.Body.String(), string(traceID)) || strings.Contains(recorder.Body.String(), string(childTaskID)) || strings.Contains(recorder.Body.String(), string(parentTaskID)) {
		t.Fatalf("hidden graph response exposed identifiers: %s", recorder.Body)
	}
	if !reflect.DeepEqual(checkedTasks, []modulecore.TaskID{childTaskID, parentTaskID}) {
		t.Fatalf("visibility checked Tasks=%#v, want root and related Task", checkedTasks)
	}
	if !reflect.DeepEqual(taskReader.calls, []modulecore.TaskID{childTaskID, parentTaskID}) {
		t.Fatalf("Task lookups=%#v, want root and related Task", taskReader.calls)
	}
}

func TestIdentityGraphLegacySessionEventWithoutPublicationProofFailsClosed(t *testing.T) {
	traceID := modulecore.NewTraceID()
	sessionID := modulecore.NewSessionID()
	event := identityGraphTestEvent(traceID, 1, "conversation.completed", "", modulecore.NewMessageID(), "private payload", "agent", "mio")
	event.SessionID = sessionID
	// Canonical legacy envelopes have a SessionID but no immutable
	// publication_incarnation in their orchestrator payload.
	recorder := identityGraphPerformRequest(t, HandleIdentityGraphWithPublicVisibility(
		&identityGraphEventReaderStub{events: []modulecore.EventEnvelope{event}},
		&identityGraphTaskReaderStub{},
		allowAllIdentityGraphTasks,
		allowAllIdentityGraphSessions,
	), traceID)
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), string(traceID)) || strings.Contains(recorder.Body.String(), string(sessionID)) {
		t.Fatalf("legacy IdentityGraph status=%d body=%s, want hidden without publication proof", recorder.Code, recorder.Body)
	}
}

func TestIdentityGraphRejectsSameIDPublicSessionRecreation(t *testing.T) {
	traceID := modulecore.NewTraceID()
	sessionID := modulecore.NewSessionID()
	taskID := modulecore.NewTaskID()
	oldIncarnation := "0123456789abcdef0123456789abcdef"
	newIncarnation := "abcdef0123456789abcdef0123456789"
	event := identityGraphTestEvent(traceID, 1, "task.started", taskID, modulecore.NewMessageID(), "private payload", "agent", "mio")
	identityGraphBindTestEvent(&event, sessionID, oldIncarnation)
	task := identityGraphTestTask(taskID, "", nil, "", domaintask.StatusRunning)
	taskReader := &identityGraphTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{taskID: task}}
	sessionVisibility := PublicSessionVisibility(func(_ context.Context, candidate modulecore.EventEnvelope) (bool, error) {
		incarnation, _ := candidate.Payload["publication_incarnation"].(string)
		// The SessionID is public before and after recreation, but its immutable
		// issuance value no longer matches the event's old publication.
		return candidate.SessionID == sessionID && incarnation == newIncarnation, nil
	})
	recorder := identityGraphPerformRequest(t, HandleIdentityGraphWithPublicVisibility(
		&identityGraphEventReaderStub{events: []modulecore.EventEnvelope{event}},
		taskReader,
		allowAllIdentityGraphTasks,
		sessionVisibility,
	), traceID)
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), string(traceID)) {
		t.Fatalf("same-ID public recreation status=%d body=%s, want hidden old graph", recorder.Code, recorder.Body)
	}
	if len(taskReader.calls) != 0 {
		t.Fatalf("stale graph read Task owner before rejecting Session incarnation: %#v", taskReader.calls)
	}
}

func TestIdentityGraphRechecksSessionAfterTaskReadBeforeResponse(t *testing.T) {
	traceID := modulecore.NewTraceID()
	sessionID := modulecore.NewSessionID()
	taskID := modulecore.NewTaskID()
	oldIncarnation := "0123456789abcdef0123456789abcdef"
	newIncarnation := "abcdef0123456789abcdef0123456789"
	event := identityGraphTestEvent(traceID, 1, "task.started", taskID, modulecore.NewMessageID(), "private payload", "agent", "mio")
	identityGraphBindTestEvent(&event, sessionID, oldIncarnation)
	currentIncarnation := oldIncarnation
	visibilityChecks := 0
	sessionVisibility := PublicSessionVisibility(func(_ context.Context, candidate modulecore.EventEnvelope) (bool, error) {
		visibilityChecks++
		incarnation, _ := candidate.Payload["publication_incarnation"].(string)
		return candidate.SessionID == sessionID && incarnation == currentIncarnation, nil
	})
	taskIDValue := taskID
	taskReader := &identityGraphTaskReaderStub{
		tasks: map[modulecore.TaskID]domaintask.Task{taskID: identityGraphTestTask(taskID, "", nil, "", domaintask.StatusRunning)},
		onGet: func(got modulecore.TaskID) {
			if got == taskIDValue {
				currentIncarnation = newIncarnation
			}
		},
	}
	recorder := identityGraphPerformRequest(t, HandleIdentityGraphWithPublicVisibility(
		&identityGraphEventReaderStub{events: []modulecore.EventEnvelope{event}},
		taskReader,
		allowAllIdentityGraphTasks,
		sessionVisibility,
	), traceID)
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), string(traceID)) || strings.Contains(recorder.Body.String(), string(taskID)) {
		t.Fatalf("Session changed during Task read status=%d body=%s, want hidden response", recorder.Code, recorder.Body)
	}
	if visibilityChecks < 2 {
		t.Fatalf("Session visibility checks=%d, want pre-read and response-admission checks", visibilityChecks)
	}
}

func TestIdentityGraphVisibilityErrorsUnknownSessionsAndUngroundedTracesFailClosed(t *testing.T) {
	traceID := modulecore.NewTraceID()
	sessionID := modulecore.NewSessionID()
	taskID := modulecore.NewTaskID()
	publicTask := identityGraphTestTask(taskID, "", nil, "", domaintask.StatusRunning)
	publicTask.OriginSessionID = sessionID
	event := identityGraphTestEvent(traceID, 1, "task.started", taskID, modulecore.NewMessageID(), "private payload", "agent", "mio")
	identityGraphBindTestEvent(&event, sessionID, "0123456789abcdef0123456789abcdef")
	newHandler := func(eventReader *identityGraphEventReaderStub, taskReader *identityGraphTaskReaderStub, sessionCheck PublicSessionVisibility) http.HandlerFunc {
		return HandleIdentityGraphWithPublicVisibility(
			eventReader,
			taskReader,
			PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
				return task.OriginSessionID == sessionID, nil
			}),
			sessionCheck,
		)
	}
	taskReader := &identityGraphTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{taskID: publicTask}}
	privateSessionReader := &identityGraphEventReaderStub{events: []modulecore.EventEnvelope{event}}
	privateSessionEvent := event
	identityGraphBindTestEvent(&privateSessionEvent, modulecore.NewSessionID(), "0123456789abcdef0123456789abcdef")
	privateSessionReader.events[0] = privateSessionEvent
	privateSessionRecorder := identityGraphPerformRequest(t, newHandler(privateSessionReader, taskReader, func(context.Context, modulecore.EventEnvelope) (bool, error) {
		return false, nil
	}), traceID)
	if privateSessionRecorder.Code != http.StatusNotFound || strings.Contains(privateSessionRecorder.Body.String(), string(traceID)) {
		t.Fatalf("session policy error status=%d body=%s", privateSessionRecorder.Code, privateSessionRecorder.Body)
	}
	if len(taskReader.calls) != 0 {
		t.Fatalf("Task store queried before private Session was rejected: %#v", taskReader.calls)
	}
	visibilityErrorRecorder := identityGraphPerformRequest(t, newHandler(privateSessionReader, taskReader, func(context.Context, modulecore.EventEnvelope) (bool, error) {
		return false, errors.New("Session owner lookup failed")
	}), traceID)
	if visibilityErrorRecorder.Code != http.StatusNotFound || strings.Contains(visibilityErrorRecorder.Body.String(), string(traceID)) {
		t.Fatalf("session policy error status=%d body=%s", visibilityErrorRecorder.Code, visibilityErrorRecorder.Body)
	}
	if len(taskReader.calls) != 0 {
		t.Fatalf("Task store queried despite Session policy error: %#v", taskReader.calls)
	}

	ungroundedTraceID := modulecore.NewTraceID()
	ungroundedEvent := identityGraphTestEvent(ungroundedTraceID, 1, "trace.observed", "", modulecore.NewMessageID(), "private payload", "agent", "mio")
	ungroundedRecorder := identityGraphPerformRequest(t, newHandler(&identityGraphEventReaderStub{events: []modulecore.EventEnvelope{ungroundedEvent}}, &identityGraphTaskReaderStub{}, allowAllIdentityGraphSessions), ungroundedTraceID)
	if ungroundedRecorder.Code != http.StatusNotFound || strings.Contains(ungroundedRecorder.Body.String(), string(ungroundedTraceID)) {
		t.Fatalf("ungrounded trace status=%d body=%s", ungroundedRecorder.Code, ungroundedRecorder.Body)
	}

	unknownTraceID := modulecore.NewTraceID()
	unknownRecorder := identityGraphPerformRequest(t, newHandler(&identityGraphEventReaderStub{}, &identityGraphTaskReaderStub{}, allowAllIdentityGraphSessions), unknownTraceID)
	if unknownRecorder.Code != http.StatusNotFound || strings.Contains(unknownRecorder.Body.String(), string(unknownTraceID)) {
		t.Fatalf("unknown trace status=%d body=%s", unknownRecorder.Code, unknownRecorder.Body)
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
		{name: "caller supplied identity", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID) + "&session_id=" + string(modulecore.NewSessionID()), events: events, tasks: tasks, want: http.StatusBadRequest},
		{name: "repeated trace", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID) + "&trace_id=" + string(traceID), events: events, tasks: tasks, want: http.StatusBadRequest},
		{name: "method", method: http.MethodPost, target: "/viewer/identity-graph?trace_id=" + string(traceID), events: events, tasks: tasks, want: http.StatusMethodNotAllowed},
		{name: "event owner unavailable", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID), events: nil, tasks: tasks, want: http.StatusServiceUnavailable},
		{name: "task owner unavailable", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID), events: events, tasks: nil, want: http.StatusServiceUnavailable},
		{name: "unknown trace", method: http.MethodGet, target: "/viewer/identity-graph?trace_id=" + string(traceID), events: &identityGraphEventReaderStub{}, tasks: tasks, want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.target, nil)
			identityGraphTestHandler(test.events, test.tasks).ServeHTTP(recorder, request)
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
			recorder := identityGraphPerformRequest(t, identityGraphTestHandler(reader, taskReader), traceID)
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
	recorder := identityGraphPerformRequest(t, identityGraphTestHandler(&identityGraphEventReaderStub{events: rootEvents}, &identityGraphTaskReaderStub{tasks: tasks}), traceID)
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
	recorder := identityGraphPerformRequest(t, identityGraphTestHandler(&identityGraphEventReaderStub{events: events}, &identityGraphTaskReaderStub{}), traceID)
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

	firstResponse := identityGraphPerformRequest(t, identityGraphTestHandler(store, tasks), traceID)
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
	secondResponse := identityGraphPerformRequest(t, identityGraphTestHandler(reopened, tasks), traceID)
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

func identityGraphTestHandler(events IdentityGraphEventReader, tasks IdentityGraphTaskReader) http.HandlerFunc {
	return HandleIdentityGraphWithPublicVisibility(events, tasks, allowAllIdentityGraphTasks, allowAllIdentityGraphSessions)
}

func allowAllIdentityGraphTasks(context.Context, domaintask.Task) (bool, error) {
	return true, nil
}

func allowAllIdentityGraphSessions(context.Context, modulecore.EventEnvelope) (bool, error) {
	return true, nil
}

func identityGraphBindTestEvent(event *modulecore.EventEnvelope, sessionID modulecore.SessionID, incarnation string) {
	event.SessionID = sessionID
	if event.Payload == nil {
		event.Payload = make(map[string]any)
	}
	event.Payload["session_id"] = string(sessionID)
	event.Payload["publication_incarnation"] = incarnation
	event.Payload["channel"] = "viewer"
	event.Payload["channel_address"] = "identity-graph-test"
	event.Payload["visibility"] = "public"
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
