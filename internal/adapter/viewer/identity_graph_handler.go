package viewer

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	IdentityGraphSchemaVersion = "rencrow.identity-graph.v1"
	identityGraphEventLimit    = 1000
	identityGraphTaskLimit     = 256
)

// IdentityGraphEventReader is the bounded canonical Event Store query used by
// the Viewer identity graph. The reader owns the trace lookup and its limit.
type IdentityGraphEventReader interface {
	ListByTraceID(context.Context, modulecore.TraceID, int) ([]modulecore.EventEnvelope, error)
}

// IdentityGraphTaskReader is the narrow Task Store lookup used by the Viewer
// identity graph. Only the latest canonical Task aggregate is projected.
type IdentityGraphTaskReader interface {
	GetTask(context.Context, modulecore.TaskID) (domaintask.Task, error)
}

type identityGraphResponse struct {
	Schema       string                     `json:"schema"`
	TraceID      modulecore.TraceID         `json:"trace_id"`
	Events       []identityGraphEvent       `json:"events"`
	EventEdges   []identityGraphEventEdge   `json:"event_edges"`
	Tasks        []identityGraphTask        `json:"tasks"`
	TaskEdges    []identityGraphTaskEdge    `json:"task_edges"`
	Messages     []identityGraphMessage     `json:"messages"`
	MessageEdges []identityGraphMessageEdge `json:"message_edges"`
}

type identityGraphEvent struct {
	EventID   modulecore.EventID   `json:"event_id"`
	EventSeq  modulecore.EventSeq  `json:"event_seq"`
	EventType string               `json:"event_type"`
	TraceID   modulecore.TraceID   `json:"trace_id"`
	TaskID    modulecore.TaskID    `json:"task_id,omitempty"`
	RunID     modulecore.RunID     `json:"run_id,omitempty"`
	MessageID modulecore.MessageID `json:"message_id,omitempty"`
	SessionID modulecore.SessionID `json:"session_id,omitempty"`
	ThreadID  modulecore.ThreadID  `json:"thread_id,omitempty"`
	TurnID    modulecore.TurnID    `json:"turn_id,omitempty"`
	ActorKind string               `json:"actor_kind,omitempty"`
	ActorID   string               `json:"actor_id,omitempty"`
}

type identityGraphEventEdge struct {
	SourceEventID modulecore.EventID `json:"source_event_id"`
	TargetEventID modulecore.EventID `json:"target_event_id"`
	Kind          string             `json:"kind"`
}

type identityGraphTask struct {
	TaskID            modulecore.TaskID   `json:"task_id"`
	Status            domaintask.Status   `json:"status"`
	ParentTaskID      modulecore.TaskID   `json:"parent_task_id,omitempty"`
	DependencyTaskIDs []modulecore.TaskID `json:"dependency_task_ids"`
	SupersedesTaskID  modulecore.TaskID   `json:"supersedes_task_id,omitempty"`
}

type identityGraphTaskEdge struct {
	SourceTaskID modulecore.TaskID `json:"source_task_id"`
	TargetTaskID modulecore.TaskID `json:"target_task_id"`
	Kind         string            `json:"kind"`
}

type identityGraphMessage struct {
	MessageID modulecore.MessageID `json:"message_id"`
}

type identityGraphMessageEdge struct {
	MessageID modulecore.MessageID `json:"message_id"`
	EventID   modulecore.EventID   `json:"event_id"`
}

var (
	errIdentityGraphConflict    = errors.New("identity graph is inconsistent")
	errIdentityGraphRead        = errors.New("identity graph read failed")
	errIdentityGraphUnavailable = errors.New("identity graph is unavailable")
)

// HandleIdentityGraph returns a bounded, read-only projection of one
// canonical TraceID. It intentionally exposes no event payload, Task body,
// filesystem path, or owner error value.
func HandleIdentityGraph(events IdentityGraphEventReader, tasks IdentityGraphTaskReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r == nil || r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		traceID, ok := parseIdentityGraphTraceID(r.URL)
		if !ok {
			http.Error(w, "invalid identity graph query", http.StatusBadRequest)
			return
		}
		if events == nil || tasks == nil {
			http.Error(w, "identity graph is unavailable", http.StatusServiceUnavailable)
			return
		}

		traceEvents, err := events.ListByTraceID(r.Context(), traceID, identityGraphEventLimit)
		if err != nil {
			http.Error(w, "identity graph read failed", http.StatusInternalServerError)
			return
		}
		if len(traceEvents) == 0 {
			http.Error(w, "trace_id is unknown", http.StatusBadRequest)
			return
		}

		projection, err := buildIdentityGraph(r.Context(), traceID, traceEvents, tasks)
		if err != nil {
			status := http.StatusInternalServerError
			message := "identity graph read failed"
			switch {
			case errors.Is(err, errIdentityGraphConflict):
				status = http.StatusConflict
				message = "identity graph is inconsistent"
			case errors.Is(err, errIdentityGraphUnavailable):
				status = http.StatusServiceUnavailable
				message = "identity graph is unavailable"
			}
			http.Error(w, message, status)
			return
		}

		writeJSON(w, http.StatusOK, projection)
	}
}

func parseIdentityGraphTraceID(rawURL *url.URL) (modulecore.TraceID, bool) {
	if rawURL == nil {
		return "", false
	}
	values, err := url.ParseQuery(rawURL.RawQuery)
	if err != nil {
		return "", false
	}
	if len(values) != 1 {
		return "", false
	}
	rawValues, ok := values["trace_id"]
	if !ok || len(rawValues) != 1 {
		return "", false
	}
	traceID := modulecore.TraceID(strings.TrimSpace(rawValues[0]))
	if err := traceID.Validate(); err != nil {
		return "", false
	}
	return traceID, true
}

func buildIdentityGraph(ctx context.Context, traceID modulecore.TraceID, sourceEvents []modulecore.EventEnvelope, tasks IdentityGraphTaskReader) (identityGraphResponse, error) {
	if ctx == nil || tasks == nil {
		return identityGraphResponse{}, errIdentityGraphUnavailable
	}
	if len(sourceEvents) == 0 || len(sourceEvents) > identityGraphEventLimit {
		return identityGraphResponse{}, errIdentityGraphConflict
	}

	traceEvents := append([]modulecore.EventEnvelope(nil), sourceEvents...)
	if err := validateIdentityGraphEvents(traceID, traceEvents); err != nil {
		return identityGraphResponse{}, errors.Join(errIdentityGraphConflict, err)
	}

	taskValues, taskEdges, err := loadIdentityGraphTasks(ctx, traceEvents, tasks)
	if err != nil {
		return identityGraphResponse{}, err
	}

	response := identityGraphResponse{
		Schema:       IdentityGraphSchemaVersion,
		TraceID:      traceID,
		Events:       make([]identityGraphEvent, 0, len(traceEvents)),
		EventEdges:   make([]identityGraphEventEdge, 0),
		Tasks:        make([]identityGraphTask, 0, len(taskValues)),
		TaskEdges:    taskEdges,
		Messages:     make([]identityGraphMessage, 0),
		MessageEdges: make([]identityGraphMessageEdge, 0),
	}

	messageIDs := make(map[modulecore.MessageID]struct{})
	for _, event := range traceEvents {
		response.Events = append(response.Events, identityGraphEvent{
			EventID:   event.EventID,
			EventSeq:  event.EventSeq,
			EventType: event.EventType,
			TraceID:   event.TraceID,
			TaskID:    event.TaskID,
			RunID:     event.RunID,
			MessageID: event.MessageID,
			SessionID: event.SessionID,
			ThreadID:  event.ThreadID,
			TurnID:    event.TurnID,
			ActorKind: event.ActorKind,
			ActorID:   event.ActorID,
		})

		if event.CausationEventID != "" {
			response.EventEdges = append(response.EventEdges, identityGraphEventEdge{
				SourceEventID: event.CausationEventID,
				TargetEventID: event.EventID,
				Kind:          "causation",
			})
		}
		for _, dependencyID := range event.DependencyEventIDs {
			response.EventEdges = append(response.EventEdges, identityGraphEventEdge{
				SourceEventID: dependencyID,
				TargetEventID: event.EventID,
				Kind:          "dependency",
			})
		}

		if event.MessageID != "" {
			messageIDs[event.MessageID] = struct{}{}
			response.MessageEdges = append(response.MessageEdges, identityGraphMessageEdge{
				MessageID: event.MessageID,
				EventID:   event.EventID,
			})
		}
	}

	for taskID, task := range taskValues {
		response.Tasks = append(response.Tasks, identityGraphTask{
			TaskID:            taskID,
			Status:            task.Status,
			ParentTaskID:      task.ParentTaskID,
			DependencyTaskIDs: sortedTaskIDs(task.DependencyTaskIDs),
			SupersedesTaskID:  task.SupersedesTaskID,
		})
	}
	for messageID := range messageIDs {
		response.Messages = append(response.Messages, identityGraphMessage{MessageID: messageID})
	}

	sort.Slice(response.EventEdges, func(i, j int) bool {
		left, right := response.EventEdges[i], response.EventEdges[j]
		if left.SourceEventID != right.SourceEventID {
			return left.SourceEventID < right.SourceEventID
		}
		if left.TargetEventID != right.TargetEventID {
			return left.TargetEventID < right.TargetEventID
		}
		return left.Kind < right.Kind
	})
	sort.Slice(response.Tasks, func(i, j int) bool { return response.Tasks[i].TaskID < response.Tasks[j].TaskID })
	sort.Slice(response.Messages, func(i, j int) bool { return response.Messages[i].MessageID < response.Messages[j].MessageID })
	sort.Slice(response.MessageEdges, func(i, j int) bool {
		left, right := response.MessageEdges[i], response.MessageEdges[j]
		if left.MessageID != right.MessageID {
			return left.MessageID < right.MessageID
		}
		return left.EventID < right.EventID
	})

	return response, nil
}

func validateIdentityGraphEvents(traceID modulecore.TraceID, events []modulecore.EventEnvelope) error {
	if err := traceID.Validate(); err != nil {
		return err
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		return err
	}
	seenSequences := make(map[modulecore.EventSeq]struct{}, len(events))
	for _, event := range events {
		if event.TraceID != traceID {
			return errors.New("event trace_id does not match the requested trace")
		}
		if err := event.EventSeq.Validate(); err != nil {
			return err
		}
		if _, exists := seenSequences[event.EventSeq]; exists {
			return errors.New("duplicate event sequence")
		}
		seenSequences[event.EventSeq] = struct{}{}
		if err := validateIdentityGraphActor(event.ActorKind, event.ActorID); err != nil {
			return err
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].EventSeq < events[j].EventSeq })
	return nil
}

func validateIdentityGraphActor(kind, id string) error {
	kindSet := kind != ""
	idSet := id != ""
	if kindSet != idSet {
		return errors.New("actor kind and actor id must be set together")
	}
	if !kindSet {
		return nil
	}
	if strings.TrimSpace(kind) != kind || strings.TrimSpace(id) != id {
		return errors.New("actor identity must not have surrounding whitespace")
	}
	if kind != "agent" && kind != "user" {
		return errors.New("actor kind is not canonical")
	}
	return nil
}

func loadIdentityGraphTasks(ctx context.Context, events []modulecore.EventEnvelope, reader IdentityGraphTaskReader) (map[modulecore.TaskID]domaintask.Task, []identityGraphTaskEdge, error) {
	rootSet := make(map[modulecore.TaskID]struct{})
	for _, event := range events {
		if event.TaskID != "" {
			rootSet[event.TaskID] = struct{}{}
		}
	}
	rootIDs := make([]modulecore.TaskID, 0, len(rootSet))
	for taskID := range rootSet {
		rootIDs = append(rootIDs, taskID)
	}
	sort.Slice(rootIDs, func(i, j int) bool { return rootIDs[i] < rootIDs[j] })
	if len(rootIDs) > identityGraphTaskLimit {
		return nil, nil, errors.Join(errIdentityGraphConflict, errors.New("task graph exceeds the bound"))
	}

	values := make(map[modulecore.TaskID]domaintask.Task, len(rootIDs))
	edges := make([]identityGraphTaskEdge, 0)
	visiting := make(map[modulecore.TaskID]bool)
	var visit func(modulecore.TaskID) error
	visit = func(taskID modulecore.TaskID) error {
		if visiting[taskID] {
			return errors.Join(errIdentityGraphConflict, errors.New("task graph contains a cycle"))
		}
		if _, loaded := values[taskID]; loaded {
			return nil
		}
		if len(values) >= identityGraphTaskLimit {
			return errors.Join(errIdentityGraphConflict, errors.New("task graph exceeds the bound"))
		}

		visiting[taskID] = true
		value, err := reader.GetTask(ctx, taskID)
		if err != nil {
			delete(visiting, taskID)
			if errors.Is(err, domaintask.ErrNotFound) {
				return errors.Join(errIdentityGraphConflict, errors.New("referenced task is missing"))
			}
			return errors.Join(errIdentityGraphRead, errors.New("task lookup failed"))
		}
		if value.TaskID != taskID {
			delete(visiting, taskID)
			return errors.Join(errIdentityGraphConflict, errors.New("task lookup returned a mismatched task"))
		}
		if err := value.Validate(); err != nil {
			delete(visiting, taskID)
			return errors.Join(errIdentityGraphConflict, errors.New("task is invalid"))
		}
		values[taskID] = value

		if value.ParentTaskID != "" {
			edges = append(edges, identityGraphTaskEdge{SourceTaskID: taskID, TargetTaskID: value.ParentTaskID, Kind: "parent"})
			if err := visit(value.ParentTaskID); err != nil {
				delete(visiting, taskID)
				return err
			}
		}
		for _, dependencyID := range sortedTaskIDs(value.DependencyTaskIDs) {
			edges = append(edges, identityGraphTaskEdge{SourceTaskID: taskID, TargetTaskID: dependencyID, Kind: "dependency"})
			if err := visit(dependencyID); err != nil {
				delete(visiting, taskID)
				return err
			}
		}
		if value.SupersedesTaskID != "" {
			edges = append(edges, identityGraphTaskEdge{SourceTaskID: taskID, TargetTaskID: value.SupersedesTaskID, Kind: "supersedes"})
			if err := visit(value.SupersedesTaskID); err != nil {
				delete(visiting, taskID)
				return err
			}
		}
		delete(visiting, taskID)
		return nil
	}

	for _, taskID := range rootIDs {
		if err := visit(taskID); err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		left, right := edges[i], edges[j]
		if left.SourceTaskID != right.SourceTaskID {
			return left.SourceTaskID < right.SourceTaskID
		}
		if left.TargetTaskID != right.TargetTaskID {
			return left.TargetTaskID < right.TargetTaskID
		}
		return left.Kind < right.Kind
	})
	return values, edges, nil
}

func sortedTaskIDs(values []modulecore.TaskID) []modulecore.TaskID {
	result := append([]modulecore.TaskID(nil), values...)
	if result == nil {
		result = make([]modulecore.TaskID, 0)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
