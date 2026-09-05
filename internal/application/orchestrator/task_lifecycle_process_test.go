package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainstore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestConfiguredMessageOrchestratorLifecycleUsesRootForMio(t *testing.T) {
	rootID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	manager := newRecordingTaskLifecycleManager()
	mio := &lifecycleMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
		response: "mio response",
	}
	orch := NewMessageOrchestrator(newLifecycleSessionRepository(), mio, &lifecycleShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)
	listener := &lifecycleEventListener{}
	orch.SetEventListener(listener)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID:  string(rootID),
		TraceID:     string(traceID),
		SessionID:   "lifecycle-local-mio",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		To:          "MIO",
		UserMessage: "answer normally",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if resp.TaskID != rootID.String() || resp.RootTaskID != rootID.String() || resp.TraceID != string(traceID) {
		t.Fatalf("response identity = %#v", resp)
	}
	if string(manager.tasks[rootID].Status) != "succeeded" {
		t.Fatalf("root task = %#v", manager.tasks[rootID])
	}
	if got, want := manager.calls, []string{"Create", "RecordRouting", "RecordAssignment", "Start", "Succeed"}; !equalStrings(got, want) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, want)
	}
	routingEvent := lifecycleProcessEvent(listener.events, "routing.decision")
	assignmentEvent := lifecycleProcessEvent(listener.events, "agent.assignment")
	if routingEvent.TaskID != rootID || assignmentEvent.TaskID != rootID || assignmentEvent.To != "mio" || assignmentEvent.CausationEventID != routingEvent.EventID {
		t.Fatalf("route/assignment events = %#v / %#v", routingEvent, assignmentEvent)
	}
	if routingEvent.TraceID != traceID || assignmentEvent.TraceID != traceID {
		t.Fatalf("event traces = %q / %q, want %q", routingEvent.TraceID, assignmentEvent.TraceID, traceID)
	}
}

func TestConfiguredMessageOrchestratorLifecycleUsesChildForShiroAndPreservesTurnRoot(t *testing.T) {
	rootID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	manager := newRecordingTaskLifecycleManager()
	var observedRoot modulecore.TaskID
	mio := &lifecycleMioAgent{decision: routing.NewDecision(routing.RouteOPS, 1, "ops")}
	shiro := &lifecycleShiroAgent{
		response: "shiro response",
		executeFunc: func(_ context.Context, input conversation.TurnInput) (string, error) {
			observedRoot = input.RootTaskID()
			return "shiro response", nil
		},
	}
	orch := NewMessageOrchestrator(newLifecycleSessionRepository(), mio, shiro, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)
	listener := &lifecycleEventListener{}
	orch.SetEventListener(listener)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID:  string(rootID),
		TraceID:     string(traceID),
		SessionID:   "lifecycle-local-shiro",
		Channel:     "line",
		ChatID:      "user-1",
		UserMessage: "run operation",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if resp.TaskID == rootID.String() || resp.RootTaskID != rootID.String() || observedRoot != rootID {
		t.Fatalf("response/turn identities = response=%#v observed_root=%s", resp, observedRoot)
	}
	childID := modulecore.TaskID(resp.TaskID)
	child := manager.tasks[childID]
	if child.ParentTaskID != rootID || child.Assignee != "shiro" || string(child.Status) != "succeeded" || string(manager.tasks[rootID].Status) != "succeeded" {
		t.Fatalf("root/child tasks = %#v / %#v", manager.tasks[rootID], child)
	}
	if len(manager.calls) < 8 || !equalStrings(manager.calls[len(manager.calls)-4:], []string{"Start", "Start", "Succeed", "Succeed"}) {
		t.Fatalf("lifecycle calls = %#v", manager.calls)
	}
	routingEvent := lifecycleProcessEvent(listener.events, "routing.decision")
	assignmentEvent := lifecycleProcessEvent(listener.events, "agent.assignment")
	if routingEvent.TaskID != rootID || assignmentEvent.TaskID != childID || assignmentEvent.To != "shiro" || assignmentEvent.CausationEventID != routingEvent.EventID {
		t.Fatalf("route/assignment events = %#v / %#v", routingEvent, assignmentEvent)
	}
	if child.AssignmentEventID != assignmentEvent.EventID || child.RoutingEventID != routingEvent.EventID {
		t.Fatalf("child event references = %#v", child)
	}
	responseEvent := lifecycleProcessEvent(listener.events, "agent.response")
	if responseEvent.TaskID != childID {
		t.Fatalf("execution response task = %s, want child %s", responseEvent.TaskID, childID)
	}
	for _, event := range listener.events {
		if strings.HasPrefix(event.Type, "agent.") {
			if event.TraceID != traceID {
				t.Fatalf("agent event trace = %#v, want %s", event, traceID)
			}
		}
	}
}

func TestConfiguredMessageOrchestratorLifecycleFailsChildThenRoot(t *testing.T) {
	rootID := modulecore.NewTaskID()
	manager := newRecordingTaskLifecycleManager()
	wantErr := errors.New("shiro unavailable")
	mio := &lifecycleMioAgent{decision: routing.NewDecision(routing.RouteOPS, 1, "ops")}
	shiro := &lifecycleShiroAgent{executeFunc: func(context.Context, conversation.TurnInput) (string, error) {
		return "", wantErr
	}}
	orch := NewMessageOrchestrator(newLifecycleSessionRepository(), mio, shiro, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID:  string(rootID),
		SessionID:   "lifecycle-local-failure",
		Channel:     "line",
		ChatID:      "user-1",
		UserMessage: "fail operation",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ProcessMessage error = %v, want %v", err, wantErr)
	}
	if len(manager.calls) < 8 || manager.calls[len(manager.calls)-2] != "Fail" || manager.calls[len(manager.calls)-1] != "Fail" {
		t.Fatalf("lifecycle failure calls = %#v", manager.calls)
	}
	childID := modulecore.TaskID("")
	for taskID, value := range manager.tasks {
		if value.ParentTaskID == rootID {
			childID = taskID
			break
		}
	}
	if childID == "" {
		t.Fatal("expected an execution child task")
	}
	wantFailureOrder := []string{"Fail:" + childID.String(), "Fail:" + rootID.String()}
	if got := manager.callIDs[len(manager.callIDs)-2:]; !equalStrings(got, wantFailureOrder) {
		t.Fatalf("failure terminal order = %#v, want %#v", got, wantFailureOrder)
	}
	if string(manager.tasks[rootID].Status) != "failed" {
		t.Fatalf("root task = %#v", manager.tasks[rootID])
	}
	for _, value := range manager.tasks {
		if value.ParentTaskID == rootID && string(value.Status) != "failed" {
			t.Fatalf("child task = %#v", value)
		}
	}
}

func TestConfiguredMessageOrchestratorLifecycleCoversPreRoutingCommand(t *testing.T) {
	rootID := modulecore.NewTaskID()
	manager := newRecordingTaskLifecycleManager()
	mio := &lifecycleMioAgent{cmdFunc: func(context.Context, string, string) (domainagent.ChatCommandResult, error) {
		return domainagent.ChatCommandResult{Handled: true, Response: "command response"}, nil
	}}
	orch := NewMessageOrchestrator(newLifecycleSessionRepository(), mio, &lifecycleShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)
	listener := &lifecycleEventListener{}
	orch.SetEventListener(listener)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID: string(rootID), SessionID: "lifecycle-command", Channel: "viewer", ChatID: "ren", UserMessage: "/status",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if resp.TaskID != rootID.String() || manager.tasks[rootID].Assignee != "mio" || manager.tasks[rootID].Status != "succeeded" {
		t.Fatalf("response/root = %#v / %#v", resp, manager.tasks[rootID])
	}
	routingIndex := lifecycleProcessEventIndex(listener.events, "routing.decision")
	assignmentIndex := lifecycleProcessEventIndex(listener.events, "agent.assignment")
	responseIndex := lifecycleProcessEventIndex(listener.events, "agent.response")
	if routingIndex < 0 || assignmentIndex <= routingIndex || responseIndex <= assignmentIndex {
		t.Fatalf("event order routing=%d assignment=%d response=%d events=%#v", routingIndex, assignmentIndex, responseIndex, listener.events)
	}
}

func TestConfiguredMessageOrchestratorLifecycleFailsHandledPreRoutingCommandAfterActivation(t *testing.T) {
	rootID := modulecore.NewTaskID()
	manager := newRecordingTaskLifecycleManager()
	wantErr := errors.New("command store unavailable")
	mio := &lifecycleMioAgent{cmdFunc: func(context.Context, string, string) (domainagent.ChatCommandResult, error) {
		return domainagent.ChatCommandResult{}, wantErr
	}}
	orch := NewMessageOrchestrator(newLifecycleSessionRepository(), mio, &lifecycleShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)
	listener := &lifecycleEventListener{}
	orch.SetEventListener(listener)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID: string(rootID), SessionID: "lifecycle-command-failure", Channel: "viewer", ChatID: "ren", UserMessage: "/status",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ProcessMessage error = %v, want %v", err, wantErr)
	}
	if manager.tasks[rootID].Assignee != "mio" || manager.tasks[rootID].Status != "failed" {
		t.Fatalf("failed root = %#v", manager.tasks[rootID])
	}
	if lifecycleProcessEventIndex(listener.events, "routing.decision") < 0 || lifecycleProcessEventIndex(listener.events, "agent.assignment") < 0 {
		t.Fatalf("missing route/assignment events: %#v", listener.events)
	}
	if lifecycleProcessEventIndex(listener.events, "agent.response") >= 0 {
		t.Fatalf("failed command emitted a response: %#v", listener.events)
	}
}

func TestConfiguredMessageOrchestratorLifecycleCoversDailyBriefCacheAndShiroCollection(t *testing.T) {
	t.Run("cache is Mio root", func(t *testing.T) {
		rootID := modulecore.NewTaskID()
		manager := newRecordingTaskLifecycleManager()
		mio := &lifecycleMioAgent{chatFunc: func(context.Context, conversation.TurnInput) (string, error) { return "morning brief", nil }}
		orch := NewMessageOrchestrator(newLifecycleSessionRepository(), mio, &lifecycleShiroAgent{}, nil, nil, nil, nil, nil)
		orch.SetTaskLifecycleManager(manager)
		orch.SetDailyNewsBriefReader(domainnews.ReaderFunc(func(_ context.Context, now time.Time) (domainnews.DailyNewsBrief, error) {
			return usableLifecycleDailyBrief(now), nil
		}))
		resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
			RootTaskID: string(rootID), SessionID: "lifecycle-daily-cache", Channel: "viewer", ChatID: "ren", UserMessage: "今朝のニュースを教えて",
		})
		if err != nil {
			t.Fatalf("ProcessMessage: %v", err)
		}
		if resp.TaskID != rootID.String() || manager.tasks[rootID].Assignee != "mio" || len(manager.tasks) != 1 || manager.tasks[rootID].Status != "succeeded" {
			t.Fatalf("response/tasks = %#v / %#v", resp, manager.tasks)
		}
	})

	t.Run("collector gets Shiro child before effect", func(t *testing.T) {
		rootID := modulecore.NewTaskID()
		manager := newRecordingTaskLifecycleManager()
		mio := &lifecycleMioAgent{chatFunc: func(context.Context, conversation.TurnInput) (string, error) { return "collected brief", nil }}
		orch := NewMessageOrchestrator(newLifecycleSessionRepository(), mio, &lifecycleShiroAgent{}, nil, nil, nil, nil, nil)
		orch.SetTaskLifecycleManager(manager)
		orch.SetDailyNewsBriefReader(domainnews.ReaderFunc(func(context.Context, time.Time) (domainnews.DailyNewsBrief, error) {
			return domainnews.DailyNewsBrief{Status: domainnews.StatusEmpty, Items: []domainnews.Item{}}, nil
		}))
		collector := &lifecycleDailyCollector{brief: usableLifecycleDailyBrief(time.Now())}
		orch.SetDailyNewsBriefCollector(collector)
		listener := &lifecycleEventListener{}
		orch.SetEventListener(listener)
		resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
			RootTaskID: string(rootID), SessionID: "lifecycle-daily-collector", Channel: "viewer", ChatID: "ren", UserMessage: "今朝のニュースを教えて",
		})
		if err != nil {
			t.Fatalf("ProcessMessage: %v", err)
		}
		if collector.calls != 1 || resp.TaskID != rootID.String() || manager.tasks[rootID].Assignee != "mio" {
			t.Fatalf("collector/response/root = %d / %#v / %#v", collector.calls, resp, manager.tasks[rootID])
		}
		childID := lifecycleChildTaskID(manager, rootID)
		if childID == "" || manager.tasks[childID].Assignee != "shiro" || manager.tasks[childID].Status != "succeeded" {
			t.Fatalf("Shiro child = %#v", manager.tasks[childID])
		}
		routingEvent := lifecycleProcessEvent(listener.events, "routing.decision")
		shiroAssignment := lifecycleAssignmentEvent(listener.events, "shiro")
		if shiroAssignment.TaskID != childID || shiroAssignment.CausationEventID != routingEvent.EventID {
			t.Fatalf("route/Shiro assignment = %#v / %#v", routingEvent, shiroAssignment)
		}
	})
}

func TestConfiguredMessageOrchestratorLifecycleCoversExplicitDCIAndDurableStore(t *testing.T) {
	t.Run("explicit DCI is Shiro child", func(t *testing.T) {
		rootID := modulecore.NewTaskID()
		manager := newRecordingTaskLifecycleManager()
		searcher := &lifecycleDCISearcher{trigger: true}
		orch := NewMessageOrchestrator(newLifecycleSessionRepository(), &lifecycleMioAgent{}, &lifecycleShiroAgent{}, nil, nil, nil, nil, nil)
		orch.SetTaskLifecycleManager(manager)
		orch.SetDCISearcher(searcher)
		resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
			RootTaskID: string(rootID), SessionID: "lifecycle-dci", Channel: "viewer", ChatID: "ren", UserMessage: "DCI を探して",
		})
		if err != nil {
			t.Fatalf("ProcessMessage: %v", err)
		}
		childID := modulecore.TaskID(resp.TaskID)
		if childID == rootID || manager.tasks[childID].Assignee != "shiro" || manager.tasks[childID].Status != "succeeded" || manager.tasks[rootID].Status != "succeeded" {
			t.Fatalf("response/root/child = %#v / %#v / %#v", resp, manager.tasks[rootID], manager.tasks[childID])
		}
	})

	t.Run("durable store is Mio root", func(t *testing.T) {
		rootID := modulecore.NewTaskID()
		manager := newRecordingTaskLifecycleManager()
		orch := NewMessageOrchestrator(newLifecycleSessionRepository(), &lifecycleMioAgent{}, &lifecycleShiroAgent{}, nil, nil, nil, nil, nil)
		orch.SetTaskLifecycleManager(manager)
		orch.SetDurableStoreWorkflow(lifecycleDurableWorkflow{handled: true, result: domainstore.WorkflowResult{
			Status: domainstore.StatusBlocked, Lifecycle: domainstore.LifecycleProposed,
			Requirement: domainstore.StorageRequirement{RequirementID: "sr-lifecycle", RequestedOutcome: domainstore.OutcomeImplement},
		}})
		resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
			RootTaskID: string(rootID), SessionID: "lifecycle-store", Channel: "viewer", ChatID: "ren", UserMessage: "ゲームDBを実装して",
		})
		if err != nil {
			t.Fatalf("ProcessMessage: %v", err)
		}
		if resp.TaskID != rootID.String() || resp.Capability != durableStoreCapability || manager.tasks[rootID].Assignee != "mio" || manager.tasks[rootID].Status != "succeeded" {
			t.Fatalf("response/root = %#v / %#v", resp, manager.tasks[rootID])
		}
	})
}

func TestConfiguredDistributedOrchestratorLifecycleUsesRootForMio(t *testing.T) {
	rootID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	manager := newRecordingTaskLifecycleManager()
	router := transport.NewMessageRouter()
	defer router.Stop()
	mio := &lifecycleMioAgent{decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"), response: "distributed mio response"}
	orch := NewDistributedOrchestrator(newLifecycleSessionRepository(), mio, router, session.NewCentralMemory(), nil)
	orch.SetTaskLifecycleManager(manager)
	listener := &lifecycleEventListener{}
	orch.SetEventListener(listener)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID:  string(rootID),
		TraceID:     string(traceID),
		SessionID:   "lifecycle-distributed-mio",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		To:          "mio",
		UserMessage: "distributed chat",
	})
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if resp.TaskID != rootID.String() || resp.RootTaskID != rootID.String() || string(manager.tasks[rootID].Status) != "succeeded" {
		t.Fatalf("response/root = %#v / %#v", resp, manager.tasks[rootID])
	}
	routingEvent := lifecycleProcessEvent(listener.events, "routing.decision")
	assignmentEvent := lifecycleProcessEvent(listener.events, "agent.assignment")
	if routingEvent.TaskID != rootID || assignmentEvent.TaskID != rootID || assignmentEvent.CausationEventID != routingEvent.EventID || assignmentEvent.TraceID != traceID {
		t.Fatalf("route/assignment events = %#v / %#v", routingEvent, assignmentEvent)
	}
}

func lifecycleProcessEvent(events []OrchestratorEvent, typ string) OrchestratorEvent {
	for _, event := range events {
		if event.Type == typ {
			return event
		}
	}
	return OrchestratorEvent{}
}

func lifecycleProcessEventIndex(events []OrchestratorEvent, typ string) int {
	for index, event := range events {
		if event.Type == typ {
			return index
		}
	}
	return -1
}

func lifecycleAssignmentEvent(events []OrchestratorEvent, assignee string) OrchestratorEvent {
	for _, event := range events {
		if event.Type == "agent.assignment" && event.To == assignee {
			return event
		}
	}
	return OrchestratorEvent{}
}

func lifecycleChildTaskID(manager *recordingTaskLifecycleManager, rootID modulecore.TaskID) modulecore.TaskID {
	for taskID, task := range manager.tasks {
		if task.ParentTaskID == rootID {
			return taskID
		}
	}
	return ""
}

func usableLifecycleDailyBrief(now time.Time) domainnews.DailyNewsBrief {
	return domainnews.DailyNewsBrief{
		Date:             domainnews.ExpectedMorningDate(now),
		Source:           domainnews.SourceScheduled,
		Status:           domainnews.StatusReady,
		EnrichmentStatus: domainnews.EnrichmentReady,
		Items:            []domainnews.Item{{ID: "news-lifecycle", Title: "morning news", Source: "official"}},
	}
}

type lifecycleEventListener struct {
	events []OrchestratorEvent
}

func (l *lifecycleEventListener) OnEvent(event OrchestratorEvent) error {
	l.events = append(l.events, event)
	return nil
}

type lifecycleMioAgent struct {
	decision   routing.Decision
	response   string
	decideFunc func(context.Context, conversation.TurnInput) (routing.Decision, error)
	chatFunc   func(context.Context, conversation.TurnInput) (string, error)
	cmdFunc    func(context.Context, string, string) (domainagent.ChatCommandResult, error)
}

func (a *lifecycleMioAgent) DecideAction(ctx context.Context, input conversation.TurnInput) (routing.Decision, error) {
	if a.decideFunc != nil {
		return a.decideFunc(ctx, input)
	}
	return a.decision, nil
}

func (a *lifecycleMioAgent) Chat(ctx context.Context, input conversation.TurnInput) (string, error) {
	if a.chatFunc != nil {
		return a.chatFunc(ctx, input)
	}
	return a.response, nil
}

func (a *lifecycleMioAgent) HandleChatCommand(ctx context.Context, sessionID, message string) (domainagent.ChatCommandResult, error) {
	if a.cmdFunc != nil {
		return a.cmdFunc(ctx, sessionID, message)
	}
	return domainagent.ChatCommandResult{}, nil
}

type lifecycleShiroAgent struct {
	response    string
	executeFunc func(context.Context, conversation.TurnInput) (string, error)
}

func (a *lifecycleShiroAgent) Execute(ctx context.Context, input conversation.TurnInput) (string, error) {
	if a.executeFunc != nil {
		return a.executeFunc(ctx, input)
	}
	return a.response, nil
}

type lifecycleSessionRepository struct {
	sessions map[string]*session.Session
}

func newLifecycleSessionRepository() *lifecycleSessionRepository {
	return &lifecycleSessionRepository{sessions: make(map[string]*session.Session)}
}

func (r *lifecycleSessionRepository) Save(_ context.Context, value *session.Session) error {
	r.sessions[value.ID()] = value
	return nil
}

func (r *lifecycleSessionRepository) Load(_ context.Context, id string) (*session.Session, error) {
	if value, ok := r.sessions[id]; ok {
		return value, nil
	}
	address, err := conversation.NewChannelAddress("test", "test")
	if err != nil {
		return nil, err
	}
	value, err := session.NewCanonicalSession(modulecore.NewSessionID(), "2026-09-05", address, time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return nil, err
	}
	r.sessions[id] = value
	return value, nil
}

func (r *lifecycleSessionRepository) Exists(_ context.Context, id string) (bool, error) {
	_, ok := r.sessions[id]
	return ok, nil
}

func (r *lifecycleSessionRepository) Delete(_ context.Context, id string) error {
	delete(r.sessions, id)
	return nil
}

type lifecycleDailyCollector struct {
	calls int
	brief domainnews.DailyNewsBrief
}

func (c *lifecycleDailyCollector) Collect(context.Context, string, time.Time) (domainnews.DailyNewsBrief, error) {
	c.calls++
	return c.brief, nil
}

type lifecycleDCISearcher struct {
	trigger bool
}

func (s *lifecycleDCISearcher) ShouldTrigger(string) bool {
	return s.trigger
}

func (s *lifecycleDCISearcher) Search(context.Context, string) (domaindci.SearchResult, error) {
	return domaindci.SearchResult{}, nil
}

type lifecycleDurableWorkflow struct {
	result  domainstore.WorkflowResult
	handled bool
}

func (w lifecycleDurableWorkflow) Handle(context.Context, appstore.Input) (domainstore.WorkflowResult, bool, error) {
	return w.result, w.handled, nil
}
