package orchestrator

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
)

type distRecordingEventListener struct {
	events []OrchestratorEvent
}

func (r *distRecordingEventListener) OnEvent(ev OrchestratorEvent) {
	r.events = append(r.events, ev)
}

func distIndexOfEvent(events []OrchestratorEvent, eventType, from, to, route string) int {
	for i, ev := range events {
		if ev.Type == eventType && ev.From == from && ev.To == to && ev.Route == route {
			return i
		}
	}
	return -1
}

// distMockMioAgent はDistributedOrchestrator テスト用のMioAgent
type distMockMioAgent struct {
	chatResponse        string
	routeResponse       string // "CHAT", "OPS", etc.
	lastChatInput       string
	lastViewerRecipient string
	decideCalls         int
	chatFunc            func(ctx context.Context, t task.Task) (string, error)
}

func (m *distMockMioAgent) DecideAction(ctx context.Context, t task.Task) (routing.Decision, error) {
	m.decideCalls++
	route := routing.RouteCHAT
	if m.routeResponse != "" {
		route = routeFromString(m.routeResponse)
	}
	return routing.Decision{
		Route:      route,
		Confidence: 0.9,
	}, nil
}

func (m *distMockMioAgent) Chat(ctx context.Context, t task.Task) (string, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, t)
	}
	m.lastChatInput = t.UserMessage()
	m.lastViewerRecipient = t.ViewerRecipient()
	return m.chatResponse, nil
}

func (m *distMockMioAgent) HandleChatCommand(ctx context.Context, sessionID string, message string) (agent.ChatCommandResult, error) {
	return agent.ChatCommandResult{Handled: false}, nil
}

// distMockSessionRepo はDistributedOrchestrator テスト用のSessionRepo
type distMockSessionRepo struct {
	sessions map[string]*session.Session
}

func (m *distMockSessionRepo) Save(ctx context.Context, sess *session.Session) error {
	if m.sessions == nil {
		m.sessions = make(map[string]*session.Session)
	}
	m.sessions[sess.ID()] = sess
	return nil
}

func (m *distMockSessionRepo) Load(ctx context.Context, id string) (*session.Session, error) {
	if m.sessions == nil {
		return nil, session.ErrSessionNotFound
	}
	sess, exists := m.sessions[id]
	if !exists {
		return nil, session.ErrSessionNotFound
	}
	return sess, nil
}

func (m *distMockSessionRepo) Exists(ctx context.Context, id string) (bool, error) {
	if m.sessions == nil {
		return false, nil
	}
	_, exists := m.sessions[id]
	return exists, nil
}

func (m *distMockSessionRepo) Delete(ctx context.Context, id string) error {
	if m.sessions != nil {
		delete(m.sessions, id)
	}
	return nil
}

// routeFromString はテスト用のルート文字列→Route変換
func routeFromString(s string) routing.Route {
	switch s {
	case "CHAT":
		return routing.RouteCHAT
	case "OPS":
		return routing.RouteOPS
	case "CODE":
		return routing.RouteCODE
	case "CODE1":
		return routing.RouteCODE1
	case "CODE2":
		return routing.RouteCODE2
	case "CODE3":
		return routing.RouteCODE3
	case "PLAN":
		return routing.RoutePLAN
	case "ANALYZE":
		return routing.RouteANALYZE
	case "RESEARCH":
		return routing.RouteRESEARCH
	case "WILD":
		return routing.RouteWILD
	default:
		return routing.RouteCHAT
	}
}

type distMockWildAgent struct {
	response string
	called   bool
}

func (m *distMockWildAgent) Generate(ctx context.Context, t task.Task) (string, error) {
	m.called = true
	return m.response, nil
}

func proposalForTest(plan, patch string) *domaintransport.ProposalPayload {
	return &domaintransport.ProposalPayload{
		Plan:  plan,
		Patch: patch,
	}
}

type distMockReportStore struct {
	reports []domainexecution.ExecutionReport
}

func (m *distMockReportStore) Save(_ context.Context, report domainexecution.ExecutionReport) error {
	m.reports = append(m.reports, report)
	return nil
}

func TestDistributedOrchestrator_ProcessMessage_LocalRoute(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "Hello from Mio!"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "hello",
	})

	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Response != "Hello from Mio!" {
		t.Errorf("Expected 'Hello from Mio!', got '%s'", resp.Response)
	}
}

func TestDistributedOrchestrator_ProcessMessage_ViewerRecipientBecomesChatSpeaker(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "RC_midori_contract、発想を広げたよ。"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	rec := &distRecordingEventListener{}
	orch.SetEventListener(rec)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "合言葉 RC_midori_contract で返答して",
		To:          "midori",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "RC_midori_contract、発想を広げたよ。" {
		t.Fatalf("response = %q", resp.Response)
	}
	if mockMio.lastViewerRecipient != "midori" {
		t.Fatalf("viewer recipient = %q, want midori", mockMio.lastViewerRecipient)
	}
	if mockMio.lastChatInput != "合言葉 RC_midori_contract で返答して" {
		t.Fatalf("chat input changed: %q", mockMio.lastChatInput)
	}
	if distIndexOfEvent(rec.events, "message.received", "user", "midori", "") < 0 {
		t.Fatalf("missing user->midori message.received: %#v", rec.events)
	}
	if distIndexOfEvent(rec.events, "agent.response", "midori", "user", "CHAT") < 0 {
		t.Fatalf("missing midori->user response: %#v", rec.events)
	}
	if distIndexOfEvent(rec.events, "agent.response", "mio", "user", "CHAT") >= 0 {
		t.Fatalf("recipient response must not be emitted as mio->user: %#v", rec.events)
	}
}

func TestDistributedOrchestrator_ProcessMessage_ExplicitDCIBypassesRouting(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat fallback"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	searcher := &mockDCISearcher{
		trigger: true,
		result: domaindci.SearchResult{
			Pack: domaindci.EvidencePack{
				EventID:     "evt_dist_dci_test",
				Query:       "docs から DCI を探して",
				CorpusScope: []string{"docs/10_新仕様"},
				Evidence: []domaindci.Evidence{{
					FilePath:  "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md",
					LineStart: 22,
					Snippet:   "DCIは通常RAGの代替ではない",
				}},
			},
			Trace: domaindci.SearchTrace{EventID: "evt_dist_dci_test", Status: "completed"},
		},
	}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetDCISearcher(searcher)
	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-dci",
		Channel:     "line",
		ChatID:      "chat-dci",
		UserMessage: "docs から DCI を探して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if mockMio.decideCalls != 0 {
		t.Fatal("explicit DCI trigger should bypass distributed route decision")
	}
	if searcher.calls != 1 {
		t.Fatalf("DCI search should be called once, got %d", searcher.calls)
	}
	if resp.Route != routing.RouteRESEARCH {
		t.Fatalf("route: want RESEARCH, got %s", resp.Route)
	}
	if !strings.Contains(resp.Response, "DCI探索結果") || !strings.Contains(resp.Response, "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md:22") {
		t.Fatalf("DCI response should include evidence location, got %q", resp.Response)
	}
}

func TestDistributedOrchestrator_ProcessMessage_ExplicitDCISavesRecallTrace(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat fallback"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	searcher := &mockDCISearcher{
		trigger: true,
		result: domaindci.SearchResult{
			Pack: domaindci.EvidencePack{
				EventID:     "evt_dist_dci_recall",
				Query:       "DCI を探して",
				CorpusScope: []string{"docs/10_新仕様"},
				Evidence: []domaindci.Evidence{{
					FilePath:   "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md",
					LineStart:  44,
					Snippet:    "Search Trace",
					Confidence: 0.75,
				}},
			},
			Trace: domaindci.SearchTrace{EventID: "evt_dist_dci_recall", Status: "completed", EndedAt: time.Date(2026, 5, 18, 1, 2, 3, 0, time.UTC)},
		},
	}
	recall := &mockRecallTraceStore{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetDCISearcher(searcher)
	orch.SetRecallTraceStore(recall)
	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-dci-recall",
		Channel:     "line",
		ChatID:      "chat-dci-recall",
		UserMessage: "DCI を探して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if len(recall.traces) != 1 {
		t.Fatalf("expected one recall trace, got %d", len(recall.traces))
	}
	trace := recall.traces[0]
	if trace.SessionID != "sess-dci-recall" || trace.ResponseID != resp.JobID || trace.Role != "dci" {
		t.Fatalf("unexpected recall trace identity: %+v", trace)
	}
	if len(trace.Items) != 1 || trace.Items[0].Layer != "DCI" || trace.Items[0].Kind != "evidence" {
		t.Fatalf("unexpected recall trace items: %+v", trace.Items)
	}
	if !strings.Contains(trace.Items[0].Summary, "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md:44") {
		t.Fatalf("recall trace item should include evidence location: %+v", trace.Items[0])
	}
}

func TestDistributedOrchestrator_ProcessMessage_ExplicitDCIErrorDoesNotFallback(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat fallback"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	searcher := &mockDCISearcher{trigger: true, err: errors.New("dci trace unavailable")}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetDCISearcher(searcher)
	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-dci-fail",
		Channel:     "line",
		ChatID:      "chat-dci-fail",
		UserMessage: "ログを探して",
	})
	if err == nil {
		t.Fatal("expected explicit DCI search failure")
	}
	if mockMio.decideCalls != 0 {
		t.Fatal("failed DCI trigger should not fall back to distributed route decision")
	}
	if !strings.Contains(err.Error(), "dci search failed") || !strings.Contains(err.Error(), "dci trace unavailable") {
		t.Fatalf("error should preserve DCI failure, got %v", err)
	}
}

func TestDistributedOrchestrator_ProcessMessage_WildRouteUsesWildAgentWithoutFallback(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat fallback", routeResponse: "WILD"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	wild := &distMockWildAgent{response: "wild response"}
	rec := &distRecordingEventListener{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetWildAgent(wild)
	orch.SetEventListener(rec)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "wild-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "/wild make something",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if !wild.called {
		t.Fatal("wild agent should be called")
	}
	if mockMio.lastChatInput != "" {
		t.Fatalf("wild route fell back to Mio chat: %q", mockMio.lastChatInput)
	}
	if resp.Route != routing.RouteWILD || resp.Response != "wild response" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	eventIndex := distIndexOfEvent(rec.events, "agent.response", "wild", "mio", "WILD")
	if eventIndex < 0 {
		t.Fatalf("expected wild response evidence, got %+v", rec.events)
	}
	responseEvent := rec.events[eventIndex]
	if responseEvent.SessionID != "wild-session" || responseEvent.JobID != resp.JobID {
		t.Fatalf("wild response evidence is not tied to the same flow: event=%+v response=%+v", responseEvent, resp)
	}
}

func TestDistributedOrchestrator_ProcessMessage_WildRouteRecordsSkillBootstrap(t *testing.T) {
	mockMio := &distMockMioAgent{routeResponse: "WILD"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	wild := &distMockWildAgent{response: "wild response"}
	recorder := &mockSkillBootstrapRecorder{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetWildAgent(wild)
	orch.SetSkillBootstrapRecorder(recorder)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "wild-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "/wild make something",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Route != routing.RouteWILD {
		t.Fatalf("route=%s", resp.Route)
	}
	if len(recorder.tasks) != 1 {
		t.Fatalf("skill bootstrap calls = %d", len(recorder.tasks))
	}
	if recorder.tasks[0].Intent != "wild" || recorder.tasks[0].Agent != "Worker" || recorder.tasks[0].WorkstreamID != "wild-session" {
		t.Fatalf("task context = %#v", recorder.tasks[0])
	}
	if !containsString(recorder.used[0], "core.worker") || !containsString(recorder.used[0], "core.wild") {
		t.Fatalf("used skills = %#v", recorder.used[0])
	}
}

func TestDistributedOrchestrator_ProcessMessage_SkillBootstrapFailureStopsRoute(t *testing.T) {
	mockMio := &distMockMioAgent{routeResponse: "WILD"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	wild := &distMockWildAgent{response: "wild response"}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetWildAgent(wild)
	orch.SetSkillBootstrapRecorder(&mockSkillBootstrapRecorder{err: errors.New("skill store failed")})

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "wild-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "/wild make something",
	})
	if err == nil {
		t.Fatal("expected skill bootstrap failure")
	}
	if !strings.Contains(err.Error(), "route WILD skill bootstrap failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if wild.called {
		t.Fatal("wild agent should not run after skill bootstrap failure")
	}
}

func TestDistributedOrchestrator_RegisteredSlashCommandExpandsRuntimePrompt(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("commands", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("commands/review-architecture.md", []byte("# /review-architecture\n\n## Steps\n1. 正本仕様を読む\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mockMio := &distMockMioAgent{chatResponse: "review result", routeResponse: "CHAT"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	events := &mockWorkflowEventRecorder{}
	skills := &mockSkillBootstrapRecorder{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetWorkflowEventRecorder(events)
	orch.SetSkillBootstrapRecorder(skills)
	orch.SetCommandRegistry(&mockCommandRegistryStore{commands: []domainai.CommandRegistry{{
		CommandName:   "/review-architecture",
		FilePath:      "commands/review-architecture.md",
		Description:   "architecture review",
		DefaultAgent:  "Coder",
		RequiredSkill: "core.architecture-review",
		UpdatedAt:     time.Now().UTC(),
	}}})

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "dist-command-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "/review-architecture docs を確認",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "review result" || resp.Route != routing.RouteCHAT {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !strings.Contains(mockMio.lastChatInput, "Slash command runtime expansion") ||
		!strings.Contains(mockMio.lastChatInput, "# /review-architecture") ||
		!strings.Contains(mockMio.lastChatInput, "User input:\ndocs を確認") {
		t.Fatalf("command prompt was not expanded:\n%s", mockMio.lastChatInput)
	}
	if len(events.events) != 1 || events.events[0].EventType != "command_invoked" {
		t.Fatalf("expected command_invoked event, got %+v", events.events)
	}
	if len(skills.used) != 1 || !containsString(skills.used[0], "core.architecture-review") {
		t.Fatalf("expected required skill bootstrap, got %+v", skills.used)
	}
}

func TestDistributedOrchestrator_RecordsLeadAgentRun(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat response", routeResponse: "CHAT"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	super := &mockSuperAgentRuntimeRecorder{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetSuperAgentRuntimeRecorder(super)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "dist-lead-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Route != routing.RouteCHAT {
		t.Fatalf("route=%s", resp.Route)
	}
	if len(super.runs) != 2 {
		t.Fatalf("expected running and completed agent_run records, got %+v", super.runs)
	}
	if super.runs[0].Status != "running" || super.runs[1].Status != "completed" {
		t.Fatalf("unexpected agent_run statuses: %+v", super.runs)
	}
	if len(super.traces) != 2 || super.traces[0].EventType != "lead_agent_started" || super.traces[1].EventType != "lead_agent_completed" {
		t.Fatalf("unexpected trace events: %+v", super.traces)
	}
}

func TestDistributedOrchestrator_ProcessMessage_WildRouteWithoutAgentFailsInsteadOfFallback(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat fallback", routeResponse: "WILD"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "wild-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "/wild make something",
	})
	if err == nil {
		t.Fatal("expected missing wild agent to fail")
	}
	if !strings.Contains(err.Error(), "no wild agent available") {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockMio.lastChatInput != "" {
		t.Fatalf("wild route fell back to Mio chat: %q", mockMio.lastChatInput)
	}
}

func TestDistributedOrchestrator_ProcessMessage_AnalyzeRouteUsesHeavyAgentWithoutFallback(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat fallback", routeResponse: "ANALYZE"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	heavy := &mockHeavyAgent{response: "heavy response"}
	rec := &distRecordingEventListener{}
	workflowEvents := &mockWorkflowEventRecorder{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetHeavyAgent(heavy)
	orch.SetEventListener(rec)
	orch.SetWorkflowEventRecorder(workflowEvents)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "analyze-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "分析して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if !heavy.called {
		t.Fatal("heavy agent should be called")
	}
	if mockMio.lastChatInput != "" {
		t.Fatalf("analyze route fell back to Mio chat: %q", mockMio.lastChatInput)
	}
	if resp.Route != routing.RouteANALYZE || resp.Response != "heavy response" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if distIndexOfEvent(rec.events, "agent.response", "heavy", "mio", "ANALYZE") < 0 {
		t.Fatalf("expected heavy response evidence, got %+v", rec.events)
	}
	if len(workflowEvents.events) != 2 {
		t.Fatalf("expected heavy lifecycle events, got %#v", workflowEvents.events)
	}
	if workflowEvents.events[0].EventType != "heavy_worker_started" || workflowEvents.events[1].EventType != "heavy_worker_completed" {
		t.Fatalf("unexpected heavy lifecycle events: %#v", workflowEvents.events)
	}
}

func TestDistributedOrchestrator_HeavyWorkerPolicyElevatesDeepDiveToAnalyze(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat fallback", routeResponse: "CHAT"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	heavy := &mockHeavyAgent{response: "heavy deep dive"}
	workflowEvents := &mockWorkflowEventRecorder{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetHeavyAgent(heavy)
	orch.SetWorkflowEventRecorder(workflowEvents)
	orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{
		Enabled:       true,
		RequireReason: true,
	})

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "deep-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "この件を深掘りして",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if !heavy.called {
		t.Fatal("heavy agent should be called after policy elevation")
	}
	if mockMio.lastChatInput != "" {
		t.Fatalf("policy-elevated analyze route fell back to Mio chat: %q", mockMio.lastChatInput)
	}
	if resp.Route != routing.RouteANALYZE || resp.Response != "heavy deep dive" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(workflowEvents.events) != 3 {
		t.Fatalf("expected requested + lifecycle events, got %#v", workflowEvents.events)
	}
	if workflowEvents.events[0].EventType != "heavy_worker_requested" ||
		workflowEvents.events[1].EventType != "heavy_worker_started" ||
		workflowEvents.events[2].EventType != "heavy_worker_completed" {
		t.Fatalf("unexpected heavy events: %#v", workflowEvents.events)
	}
}

func TestDistributedOrchestrator_ProcessMessage_AnalyzeRouteWithoutHeavyFailsInsteadOfFallback(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "chat fallback", routeResponse: "ANALYZE"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "analyze-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "分析して",
	})
	if err == nil {
		t.Fatal("expected missing heavy agent to fail")
	}
	if !strings.Contains(err.Error(), "no heavy agent available") {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockMio.lastChatInput != "" {
		t.Fatalf("analyze route fell back to Mio chat: %q", mockMio.lastChatInput)
	}
}

func TestDistributedOrchestrator_ProcessMessage_SavesEvidenceOnSuccess(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "Hello from Mio!"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	reporter := &distMockReportStore{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetReportStore(reporter)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("expected job id")
	}
	if len(reporter.reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reporter.reports))
	}
	report := reporter.reports[0]
	if report.JobID != resp.JobID {
		t.Fatalf("expected report job id %s, got %s", resp.JobID, report.JobID)
	}
	if report.Status != "passed" {
		t.Fatalf("expected passed report, got %s", report.Status)
	}
	if report.Route != "CHAT" {
		t.Fatalf("expected route CHAT, got %s", report.Route)
	}
	if report.AttemptCount != 1 {
		t.Fatalf("expected attempt count 1, got %d", report.AttemptCount)
	}
	if report.RepairCount != 0 {
		t.Fatalf("expected repair count 0, got %d", report.RepairCount)
	}
	if report.ErrorKind != "" {
		t.Fatalf("expected empty error kind, got %s", report.ErrorKind)
	}
	if len(report.Steps) == 0 || report.Steps[len(report.Steps)-1] != "done" {
		t.Fatalf("expected done steps, got %#v", report.Steps)
	}
}

func TestClassifyDistributedExecutionError_ProposalFailure(t *testing.T) {
	err := errors.New("agent coder1 returned error: proposal generation failed: " + agent.ProposalFailureInvalidPatch + ": proposal patch is not runnable")

	kind, reason, retryable := classifyDistributedExecutionError(err)

	if kind != agent.ProposalFailureInvalidPatch {
		t.Fatalf("expected kind %s, got %s", agent.ProposalFailureInvalidPatch, kind)
	}
	if !retryable {
		t.Fatal("expected proposal invalid patch to be retryable")
	}
	if !strings.Contains(reason, agent.ProposalFailureInvalidPatch) {
		t.Fatalf("expected reason to include failure kind, got %s", reason)
	}
}

func TestClassifyDistributedExecutionError_TimeoutAndRateLimit(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantKind  string
		retryable bool
	}{
		{
			name:      "deadline",
			err:       errors.New("llm queue failed: context deadline exceeded"),
			wantKind:  "timeout",
			retryable: true,
		},
		{
			name:      "rate limit",
			err:       errors.New("claude API error: status=429, body={\"error\":{\"type\":\"rate_limit_error\"}}"),
			wantKind:  "provider_rate_limited",
			retryable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, reason, retryable := classifyDistributedExecutionError(tt.err)
			if kind != tt.wantKind || retryable != tt.retryable {
				t.Fatalf("kind=%s retryable=%v, want %s %v; reason=%s", kind, retryable, tt.wantKind, tt.retryable, reason)
			}
			if reason == "" {
				t.Fatal("reason should preserve original error")
			}
		})
	}
}

func TestDistributedOrchestrator_TTSBridge_StreamAndEnd(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "Hello from Mio!"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	bridge := &mockTTSBridge{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetTTSBridge(bridge)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected one tts start, got %d", len(bridge.startReqs))
	}
	if len(bridge.ended) != 1 {
		t.Fatalf("expected one tts end, got %d", len(bridge.ended))
	}
}

func TestDistributedOrchestrator_TTSBridge_StreamsSentenceChunks(t *testing.T) {
	mockMio := &distMockMioAgent{
		chatFunc: func(ctx context.Context, t task.Task) (string, error) {
			if cb := llm.StreamCallbackFromContext(ctx); cb != nil {
				cb("最初の文です。")
				cb("二つ目の文です。")
			}
			return "最初の文です。二つ目の文です。", nil
		},
	}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	bridge := &mockTTSBridge{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetTTSBridge(bridge)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if len(bridge.pushes) != 2 {
		t.Fatalf("expected 2 chunk pushes, got %v", bridge.pushes)
	}
	if bridge.pushes[0] != "最初の文です。" {
		t.Fatalf("unexpected first chunk: %q", bridge.pushes[0])
	}
	if bridge.pushes[1] != "二つ目の文です。" {
		t.Fatalf("unexpected second chunk: %q", bridge.pushes[1])
	}
}

func TestDistributedWaitTimeout(t *testing.T) {
	t.Run("default chat path stays short", func(t *testing.T) {
		msg := domaintransport.NewMessage("mio", "shiro", "sess", "job", "hello")
		if got := distributedWaitTimeout("shiro", msg); got != distributedDefaultTimeout {
			t.Fatalf("expected default timeout %s, got %s", distributedDefaultTimeout, got)
		}
	})

	t.Run("coder path gets extended timeout", func(t *testing.T) {
		msg := domaintransport.NewMessage("shiro", "coder1", "sess", "job", "code please")
		if got := distributedWaitTimeout("coder1", msg); got != distributedCoderTimeout {
			t.Fatalf("expected coder timeout %s, got %s", distributedCoderTimeout, got)
		}
	})

	t.Run("worker proposal execution gets extended timeout", func(t *testing.T) {
		msg := domaintransport.NewMessage("mio", "shiro", "sess", "job", "Execute coder proposal")
		msg.Proposal = proposalForTest("plan", "patch")
		if got := distributedWaitTimeout("shiro", msg); got != distributedWorkerTimeout {
			t.Fatalf("expected worker timeout %s, got %s", distributedWorkerTimeout, got)
		}
	})
}

func TestDistributedOrchestrator_AttributionGuardOnUserChat(t *testing.T) {
	mockMio := &distMockMioAgent{chatResponse: "ok"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)

	// first turn
	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "guard-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "最初の質問",
	})
	if err != nil {
		t.Fatalf("first ProcessMessage failed: %v", err)
	}

	// second turn should include attribution guard context from memory
	_, err = orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "guard-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "続きの質問",
	})
	if err != nil {
		t.Fatalf("second ProcessMessage failed: %v", err)
	}

	if !strings.Contains(mockMio.lastChatInput, "【発言帰属ガード】") {
		t.Fatalf("expected guarded chat input, got: %s", mockMio.lastChatInput)
	}
	if !strings.Contains(mockMio.lastChatInput, "【ユーザー依頼】\n続きの質問") {
		t.Fatalf("expected original user request section, got: %s", mockMio.lastChatInput)
	}
}

func TestDistributedOrchestrator_RouteToAgent(t *testing.T) {
	mockMio := &distMockMioAgent{}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)

	tests := []struct {
		route    string
		expected string
	}{
		{"OPS", "shiro"},
		{"CODE", "shiro"},
		{"CODE1", "shiro"},
		{"CODE2", "shiro"},
		{"CODE3", "shiro"},
		{"CHAT", ""},
		{"PLAN", ""},
		{"ANALYZE", ""},
		{"RESEARCH", ""},
	}

	for _, tt := range tests {
		result := orch.routeToAgent(routeFromString(tt.route))
		if result != tt.expected {
			t.Errorf("routeToAgent(%s) = '%s', want '%s'", tt.route, result, tt.expected)
		}
	}
}

func TestDistributedOrchestrator_RouteToCoder_ConnectionAware(t *testing.T) {
	mockMio := &distMockMioAgent{}
	mockRepo := &distMockSessionRepo{}

	t.Run("CODE_requires_coder1_connection", func(t *testing.T) {
		router := transport.NewMessageRouter()
		defer router.Stop()
		router.RegisterAgent("coder2", transport.NewLocalTransport())
		memory := session.NewCentralMemory()

		orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
		if got := orch.routeToCoder(routing.RouteCODE); got != "" {
			t.Fatalf("routeToCoder(CODE) = %q, want empty", got)
		}
	})

	t.Run("CODE_does_not_use_non_default_ssh_connected_coder", func(t *testing.T) {
		router := transport.NewMessageRouter()
		defer router.Stop()
		memory := session.NewCentralMemory()

		sshTransports := map[string]domaintransport.Transport{
			"coder3": &distMockTransport{},
		}
		orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, sshTransports)
		if got := orch.routeToCoder(routing.RouteCODE); got != "" {
			t.Fatalf("routeToCoder(CODE) = %q, want empty", got)
		}
	})

	t.Run("explicit_route_requires_its_own_coder_connection", func(t *testing.T) {
		router := transport.NewMessageRouter()
		defer router.Stop()
		router.RegisterAgent("coder2", transport.NewLocalTransport())
		memory := session.NewCentralMemory()
		orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)

		if got := orch.routeToCoder(routing.RouteCODE1); got != "" {
			t.Fatalf("routeToCoder(CODE1) = %q, want empty", got)
		}
		if got := orch.routeToCoder(routing.RouteCODE2); got != "coder2" {
			t.Fatalf("routeToCoder(CODE2) = %q, want coder2", got)
		}
		if got := orch.routeToCoder(routing.RouteCODE3); got != "" {
			t.Fatalf("routeToCoder(CODE3) = %q, want empty", got)
		}
	})
}

func TestDistributedOrchestrator_RouteToCoderForMessage_UsesCapabilityWhenConfigured(t *testing.T) {
	mockMio := &distMockMioAgent{}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	router.RegisterAgent("coder1", transport.NewLocalTransport())
	router.RegisterAgent("coder2", transport.NewLocalTransport())
	router.RegisterAgent("coder3", transport.NewLocalTransport())
	memory := session.NewCentralMemory()

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetCoderCapabilities([]capdomain.CoderCapability{
		{Name: "coder1", Quality: 3, Available: true},
		{Name: "coder2", Quality: 4, Available: true},
		{Name: "coder3", Quality: 5, Available: true},
	})

	got := orch.routeToCoderForMessage(routing.RouteCODE, "TTSを実装して")
	if got != "coder3" {
		t.Fatalf("routeToCoderForMessage(CODE,TTS) = %q, want coder3", got)
	}
}

func TestDistributedOrchestrator_RouteToCoderForMessage_DegradesByCapability(t *testing.T) {
	mockMio := &distMockMioAgent{}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	router.RegisterAgent("coder1", transport.NewLocalTransport())
	router.RegisterAgent("coder2", transport.NewLocalTransport())
	memory := session.NewCentralMemory()

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetCoderCapabilities([]capdomain.CoderCapability{
		{Name: "coder1", Quality: 3, Available: true},
		{Name: "coder2", Quality: 4, Available: true},
		{Name: "coder3", Quality: 5, Available: true},
	})

	got := orch.routeToCoderForMessage(routing.RouteCODE3, "高品質レビューをして")
	if got != "coder2" {
		t.Fatalf("routeToCoderForMessage(CODE3) = %q, want coder2 degraded from unavailable coder3", got)
	}
}

// distMockTransport はSSH経路テスト用のmock Transport
type distMockTransport struct {
	sentMessages []domaintransport.Message
	response     domaintransport.Message
	responses    []domaintransport.Message
	closed       bool
}

func (m *distMockTransport) Send(ctx context.Context, msg domaintransport.Message) error {
	m.sentMessages = append(m.sentMessages, msg)
	return nil
}

func (m *distMockTransport) Receive(ctx context.Context) (domaintransport.Message, error) {
	if len(m.responses) > 0 {
		resp := m.responses[0]
		m.responses = m.responses[1:]
		return resp, nil
	}
	return m.response, nil
}

func (m *distMockTransport) Close() error {
	m.closed = true
	return nil
}

func (m *distMockTransport) IsHealthy() bool {
	return !m.closed
}

func TestDistributedOrchestrator_SSHExecution(t *testing.T) {
	mockMio := &distMockMioAgent{routeResponse: "CODE3"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()

	// Mio/Shiro のローカルTransportを登録（CODE3はShiro経由）
	mioTransport := transport.NewLocalTransport()
	defer mioTransport.Close()
	router.RegisterAgent("mio", mioTransport)
	shiroTransport := transport.NewLocalTransport()
	defer shiroTransport.Close()
	router.RegisterAgent("shiro", shiroTransport)

	// Coder3 のSSH Transport（mock）
	mockSSH := &distMockTransport{
		response: domaintransport.Message{
			From:    "coder3",
			To:      "shiro",
			Content: "code generated by Coder3 via SSH",
			Type:    domaintransport.MessageTypeResult,
		},
	}

	sshTransports := map[string]domaintransport.Transport{
		"coder3": mockSSH,
	}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, sshTransports)

	// Shiroが最終応答を返す
	// CODE3 ルート: verifyByContract を通過するためコードブロックを含める
	const shiroResponse = "shiro finalized code task\n```\napplied\n```"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		msg, err := shiroTransport.Receive(ctx)
		if err != nil {
			return
		}
		response := domaintransport.NewMessage("shiro", msg.From, msg.SessionID, msg.JobID, shiroResponse)
		response.Type = domaintransport.MessageTypeResult
		_ = mioTransport.PutInboundMessage(response)
	}()

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "generate code",
	})

	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Response != shiroResponse {
		t.Errorf("Expected 'shiro finalized code task', got '%s'", resp.Response)
	}

	// SSH Transport経由で送信されたことを確認
	if len(mockSSH.sentMessages) != 1 {
		t.Fatalf("Expected 1 sent message, got %d", len(mockSSH.sentMessages))
	}

	if mockSSH.sentMessages[0].To != "coder3" {
		t.Errorf("Expected message To='Coder3', got '%s'", mockSSH.sentMessages[0].To)
	}
}

func TestDistributedOrchestrator_DistributedExecution(t *testing.T) {
	mockMio := &distMockMioAgent{routeResponse: "OPS"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()

	// Shiro のTransportを登録
	shiroTransport := transport.NewLocalTransport()
	defer shiroTransport.Close()
	router.RegisterAgent("shiro", shiroTransport)

	// Mio のTransportを登録
	mioTransport := transport.NewLocalTransport()
	defer mioTransport.Close()
	router.RegisterAgent("mio", mioTransport)

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)

	// ShiroがメッセージをReceiveして応答を返すゴルーチン
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		msg, err := shiroTransport.Receive(ctx)
		if err != nil {
			return
		}

		response := domaintransport.NewMessage("shiro", msg.From, msg.SessionID, msg.JobID, "task executed by Shiro")
		response.Type = domaintransport.MessageTypeResult
		mioTransport.PutInboundMessage(response)
	}()

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "run script",
	})

	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Response != "task executed by Shiro" {
		t.Errorf("Expected 'task executed by Shiro', got '%s'", resp.Response)
	}

	if memory.AgentCount() < 2 {
		t.Errorf("Expected at least 2 agents in memory, got %d", memory.AgentCount())
	}
}

func TestDistributedOrchestrator_CodeRoute_FinalResponseComesFromMio(t *testing.T) {
	mockMio := &distMockMioAgent{routeResponse: "CODE3"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	rec := &distRecordingEventListener{}

	mioTransport := transport.NewLocalTransport()
	defer mioTransport.Close()
	router.RegisterAgent("mio", mioTransport)
	shiroTransport := transport.NewLocalTransport()
	defer shiroTransport.Close()
	router.RegisterAgent("shiro", shiroTransport)

	mockSSH := &distMockTransport{
		response: domaintransport.Message{
			From:    "coder3",
			To:      "shiro",
			Content: "code generated by Coder3 via SSH",
			Type:    domaintransport.MessageTypeResult,
		},
	}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, map[string]domaintransport.Transport{
		"coder3": mockSSH,
	})
	orch.SetEventListener(rec)

	// CODE3 ルート: verifyByContract を通過するためコードブロックを含める
	const shiroResponse2 = "shiro finalized code task\n```\napplied\n```"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		msg, err := shiroTransport.Receive(ctx)
		if err != nil {
			return
		}

		response := domaintransport.NewMessage("shiro", msg.From, msg.SessionID, msg.JobID, shiroResponse2)
		response.Type = domaintransport.MessageTypeResult
		_ = mioTransport.PutInboundMessage(response)
	}()

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "generate code",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != shiroResponse2 {
		t.Fatalf("unexpected response: %q", resp.Response)
	}

	iShiroToMio := distIndexOfEvent(rec.events, "agent.response", "shiro", "mio", "CODE3")
	iMioToUser := distIndexOfEvent(rec.events, "agent.response", "mio", "user", "CODE3")
	if iShiroToMio < 0 {
		t.Fatal("expected shiro -> mio response event")
	}
	if iMioToUser < 0 {
		t.Fatal("expected mio -> user response event")
	}
	if iMioToUser <= iShiroToMio {
		t.Fatalf("expected mio -> user response after shiro -> mio response, got %d <= %d", iMioToUser, iShiroToMio)
	}
}

func TestDistributedOrchestrator_CodeRoute_RetriesOnWorkerRetryableFailure(t *testing.T) {
	mockMio := &distMockMioAgent{routeResponse: "CODE3"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	rec := &distRecordingEventListener{}

	mioTransport := transport.NewLocalTransport()
	defer mioTransport.Close()
	router.RegisterAgent("mio", mioTransport)
	shiroTransport := transport.NewLocalTransport()
	defer shiroTransport.Close()
	router.RegisterAgent("shiro", shiroTransport)

	mockSSH := &distMockTransport{
		responses: []domaintransport.Message{
			{
				From:    "coder3",
				To:      "shiro",
				Content: "proposal attempt 1",
				Type:    domaintransport.MessageTypeResult,
				Proposal: &domaintransport.ProposalPayload{
					Plan:  "first plan",
					Patch: `[{"type":"shell_command","action":"run","target":"pip install broken"}]`,
				},
			},
			{
				From:    "coder3",
				To:      "shiro",
				Content: "proposal attempt 2",
				Type:    domaintransport.MessageTypeResult,
				Proposal: &domaintransport.ProposalPayload{
					Plan:  "second plan",
					Patch: `[{"type":"shell_command","action":"run","target":"python3 -m pip --version"}]`,
				},
			},
		},
	}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, map[string]domaintransport.Transport{
		"coder3": mockSSH,
	})
	orch.SetEventListener(rec)

	go func() {
		for attempt := 0; attempt < 2; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			msg, err := shiroTransport.Receive(ctx)
			cancel()
			if err != nil {
				return
			}

			response := domaintransport.NewMessage("shiro", msg.From, msg.SessionID, msg.JobID, "worker attempt failed")
			response.Type = domaintransport.MessageTypeResult
			if attempt == 0 {
				response.Content = "worker retry requested"
				response.Result = &domaintransport.ResultPayload{
					Success:       false,
					Summary:       "worker retry requested",
					ExecutedCmds:  1,
					FailedCmds:    1,
					FailureKind:   "missing_command",
					FailureReason: "bash: pip: command not found",
					Retryable:     true,
					FailedIndex:   0,
				}
			} else {
				// CODE3 ルート: verifyByContract を通過するためコードブロックを含める
				response.Content = "worker finalized code task\n```\napplied\n```"
				response.Result = &domaintransport.ResultPayload{
					Success:      true,
					Summary:      "worker finalized code task",
					ExecutedCmds: 1,
					FailedCmds:   0,
				}
			}
			_ = mioTransport.PutInboundMessage(response)
		}
	}()

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "retry-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "generate code with retry",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "worker finalized code task\n```\napplied\n```" {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
	if len(mockSSH.sentMessages) != 2 {
		t.Fatalf("expected 2 coder requests, got %d", len(mockSSH.sentMessages))
	}
	if !strings.Contains(mockSSH.sentMessages[1].Content, "failure_kind: missing_command") {
		t.Fatalf("expected retry context in second coder request, got %q", mockSSH.sentMessages[1].Content)
	}
	if distIndexOfEvent(rec.events, "worker.retry_request", "shiro", "coder3", "CODE3") < 0 {
		t.Fatal("expected worker.retry_request event")
	}
}

func TestDistributedOrchestrator_ProcessMessage_CodeRoute_UnconnectedExplicitCoder(t *testing.T) {
	mockMio := &distMockMioAgent{routeResponse: "CODE1"}
	mockRepo := &distMockSessionRepo{}
	router := transport.NewMessageRouter()
	defer router.Stop()
	memory := session.NewCentralMemory()
	reporter := &distMockReportStore{}

	orch := NewDistributedOrchestrator(mockRepo, mockMio, router, memory, nil)
	orch.SetReportStore(reporter)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "generate code",
	})
	if err == nil {
		t.Fatal("expected error for unconnected CODE1 coder")
	}
	if !strings.Contains(err.Error(), "no coder mapped for route CODE1") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reporter.reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reporter.reports))
	}
	if reporter.reports[0].Status != "failed" {
		t.Fatalf("expected failed report, got %s", reporter.reports[0].Status)
	}
	if reporter.reports[0].ErrorKind == "" {
		t.Fatal("expected error kind to be set")
	}
}
