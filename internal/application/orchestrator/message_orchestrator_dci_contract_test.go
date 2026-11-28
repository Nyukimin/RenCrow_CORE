package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainstore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const structuredRFCResearchRequest = `{"schema_version":"rencrow.research.request.v1","mode":"collect","objective":"RFC1149原文を取得し、knowledge_stagingへ保存する。今日のニュース依頼でもコード実行でもなく、deep diveは要求しない","source_kinds":["web"],"seed_urls":["https://www.rfc-editor.org/rfc/rfc1149"],"outputs":{"report":{"required":true,"format":"markdown"},"save":[{"target":"knowledge_staging","required":true}],"evidence":{"min_documents":1,"min_independent_sources":1,"require_primary_for_material_claims":true}},"limits":{"search_calls":1,"candidates":3,"fetch_attempts":2,"max_documents":1,"deadline_seconds":30},"cost_profile":"zero_incremental_cost"}`

type storageLikeDurableStoreWorkflow struct {
	matcherCalls int
	handleCalls  int
}

func (w *storageLikeDurableStoreWorkflow) CanHandle(appstore.Input) bool {
	w.matcherCalls++
	return true
}

func (w *storageLikeDurableStoreWorkflow) Handle(context.Context, appstore.Input) (domainstore.WorkflowResult, bool, error) {
	w.handleCalls++
	return domainstore.WorkflowResult{}, true, nil
}

func TestVersionedResearchRequestKeepsExplicitViewerRecipientOwner(t *testing.T) {
	searcher := &mockDCISearcher{trigger: true}
	for _, actor := range []string{taskLifecycleMio, taskLifecycleShiro, taskLifecycleKuro, taskLifecycleMidori} {
		t.Run(actor, func(t *testing.T) {
			req := ProcessMessageRequest{Channel: "viewer", To: actor, UserMessage: structuredRFCResearchRequest}
			workflow := &storageLikeDurableStoreWorkflow{}
			if shouldHandleExplicitDCI(searcher, req.UserMessage) {
				t.Fatal("versioned Research request was classified as DCI")
			}
			rootActor, direct := directViewerRootAssignee(req, workflow, searcher)
			if actor == taskLifecycleMio {
				if direct {
					t.Fatalf("Mio recipient unexpectedly selected a direct root %q", rootActor)
				}
			} else if !direct || rootActor != actor {
				t.Fatalf("direct root = %q, %v; want recipient %q", rootActor, direct, actor)
			}
			if actor != taskLifecycleMio && !directViewerExecutionActor(req, routing.RouteCHAT, actor) {
				t.Fatalf("explicit recipient %q was not retained as the CHAT executor", actor)
			}
			if workflow.matcherCalls != 0 || workflow.handleCalls != 0 {
				t.Fatalf("typed Research reached durable store matcher/handler: matcher=%d handler=%d", workflow.matcherCalls, workflow.handleCalls)
			}
			mio := &mockMioAgent{}
			input := newOrchestratorTestTurnInput(t, req.UserMessage, req.Channel, "viewer:"+actor)
			decision, err := newRouteDecisionCoordinator(mio).DecideWithChatDraft(context.Background(), input, req, modulecore.NewTaskID(), true)
			wantRoute := routing.RouteCHAT
			if actor == taskLifecycleMio {
				wantRoute = routing.RouteRESEARCH
			}
			if err != nil || decision.Decision.Route != wantRoute || mio.decideCalls != 0 {
				t.Fatalf("recipient %s route=%s err=%v Mio decisions=%d; want route %s", actor, decision.Decision.Route, err, mio.decideCalls, wantRoute)
			}
		})
	}
}

func TestVersionedResearchRequestRemainsOnNormalResearchDispatch(t *testing.T) {
	searcher := &mockDCISearcher{trigger: true, result: validDCISearchResult("RFC1149", "rfc1149.txt", "RFC source", 1)}
	var gatheredText string
	mio := &mockMioAgent{
		decideFunc: func(_ context.Context, input conversation.TurnInput) (routing.Decision, error) {
			return routing.NewDecision(routing.RouteCODE, 1, "generic classifier must not reinterpret typed Research"), nil
		},
		chatFunc: func(_ context.Context, input conversation.TurnInput) (string, error) {
			gatheredText = input.MessageText()
			return "research response", nil
		},
	}
	manager := newRecordingTaskLifecycleManager()
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	workflow := &storageLikeDurableStoreWorkflow{}
	orch.SetDurableStoreWorkflow(workflow)
	dailyReaderCalls := 0
	orch.SetDailyNewsBriefReader(domainnews.ReaderFunc(func(context.Context, time.Time) (domainnews.DailyNewsBrief, error) {
		dailyReaderCalls++
		return domainnews.DailyNewsBrief{}, nil
	}))
	orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{Enabled: true})
	orch.SetMusicWorkflowRouting("ren", true, true, true, true)
	orch.SetTaskLifecycleManager(manager)
	orch.SetDCISearcher(searcher)
	req := defaultReq()
	req.UserMessage = structuredRFCResearchRequest

	resp, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if searcher.calls != 0 {
		t.Fatalf("DCI calls = %d, want 0 for a versioned Research request", searcher.calls)
	}
	if mio.decideCalls != 0 || dailyReaderCalls != 0 || gatheredText != structuredRFCResearchRequest {
		t.Fatalf("normal Research dispatch lost request or used generic routing: decide=%d daily=%d gathered=%q", mio.decideCalls, dailyReaderCalls, gatheredText)
	}
	if resp.Route != routing.RouteRESEARCH || resp.Response != "research response" {
		t.Fatalf("Research response = %#v", resp)
	}
	if workflow.matcherCalls != 0 || workflow.handleCalls != 0 {
		t.Fatalf("typed Research reached durable store matcher/handler: matcher=%d handler=%d", workflow.matcherCalls, workflow.handleCalls)
	}

	t.Run("distributed orchestrator", func(t *testing.T) {
		searcher := &mockDCISearcher{trigger: true, result: validDCISearchResult("RFC1149", "rfc1149.txt", "RFC source", 1)}
		var gatheredText string
		mio := &distMockMioAgent{
			routeResponse: string(routing.RouteCODE),
			chatFunc: func(_ context.Context, input conversation.TurnInput) (string, error) {
				gatheredText = input.MessageText()
				return "research response", nil
			},
		}
		router := transport.NewMessageRouter()
		defer router.Stop()
		orch := NewDistributedOrchestrator(&distMockSessionRepo{}, mio, router, session.NewCentralMemory(), nil)
		attachCanonicalTestTaskOwner(t, orch)
		workflow := &storageLikeDurableStoreWorkflow{}
		orch.SetDurableStoreWorkflow(workflow)
		dailyReaderCalls := 0
		orch.SetDailyNewsBriefReader(domainnews.ReaderFunc(func(context.Context, time.Time) (domainnews.DailyNewsBrief, error) {
			dailyReaderCalls++
			return domainnews.DailyNewsBrief{}, nil
		}))
		orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{Enabled: true})
		orch.SetMusicWorkflowRouting("ren", true, true, true, true)
		orch.SetDCISearcher(searcher)
		req := defaultReq()
		req.UserMessage = structuredRFCResearchRequest

		resp, err := orch.ProcessMessage(context.Background(), req)
		if err != nil {
			t.Fatalf("distributed ProcessMessage: %v", err)
		}
		if searcher.calls != 0 || mio.decideCalls != 0 || dailyReaderCalls != 0 || resp.Route != routing.RouteRESEARCH || gatheredText != structuredRFCResearchRequest || resp.Response != "research response" {
			t.Fatalf("distributed Research dispatch: DCI=%d decide=%d daily=%d route=%s gathered=%q response=%q", searcher.calls, mio.decideCalls, dailyReaderCalls, resp.Route, gatheredText, resp.Response)
		}
		if workflow.matcherCalls != 0 || workflow.handleCalls != 0 {
			t.Fatalf("typed Research reached durable store matcher/handler: matcher=%d handler=%d", workflow.matcherCalls, workflow.handleCalls)
		}
	})
}

func TestOrdinaryDurableStoreIntentStillUsesMatchedWorkflow(t *testing.T) {
	decisions := 0
	mio := &mockMioAgent{decideFunc: func(context.Context, conversation.TurnInput) (routing.Decision, error) {
		decisions++
		return routing.NewDecision(routing.RouteCHAT, 1, "should not run"), nil
	}}
	workflow := &storageLikeDurableStoreWorkflow{}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDurableStoreWorkflow(workflow)
	req := defaultReq()
	req.UserMessage = "ゲームDBを実装して"

	resp, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if decisions != 0 || workflow.matcherCalls != 1 || workflow.handleCalls != 1 || resp.Capability != durableStoreCapability {
		t.Fatalf("ordinary durable workflow changed: decisions=%d matcher=%d handler=%d capability=%q", decisions, workflow.matcherCalls, workflow.handleCalls, resp.Capability)
	}
}

func TestMalformedVersionedResearchRequestFailsBeforeDCIOrAgentEffects(t *testing.T) {
	searcher := &mockDCISearcher{trigger: true, result: validDCISearchResult("invalid", "rfc1149.txt", "should not run", 1)}
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteRESEARCH, 1, "research")}
	listener := &recordingEventListener{}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDCISearcher(searcher)
	orch.SetEventListener(listener)
	req := defaultReq()
	req.UserMessage = `{"schema_version":"rencrow.research.request.v1","mode":"unsupported","objective":"RFC1149原文"}`
	workflow := &storageLikeDurableStoreWorkflow{}
	if candidate := newDurableStoreCandidate(req, workflow); candidate != nil {
		t.Fatal("malformed versioned Research request became a durable store candidate")
	}
	orch.SetDurableStoreWorkflow(workflow)

	_, err := orch.ProcessMessage(context.Background(), req)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "research") {
		t.Fatalf("ProcessMessage error = %v, want Research contract validation error", err)
	}
	if searcher.calls != 0 || mio.decideCalls != 0 || len(listener.events) != 0 || workflow.matcherCalls != 0 || workflow.handleCalls != 0 {
		t.Fatalf("invalid Research request caused effects: DCI=%d MioDecide=%d matcher=%d handler=%d events=%#v", searcher.calls, mio.decideCalls, workflow.matcherCalls, workflow.handleCalls, listener.events)
	}

	t.Run("distributed orchestrator", func(t *testing.T) {
		searcher := &mockDCISearcher{trigger: true, result: validDCISearchResult("invalid", "rfc1149.txt", "should not run", 1)}
		mio := &distMockMioAgent{}
		router := transport.NewMessageRouter()
		defer router.Stop()
		orch := NewDistributedOrchestrator(&distMockSessionRepo{}, mio, router, session.NewCentralMemory(), nil)
		workflow := &storageLikeDurableStoreWorkflow{}
		orch.SetDurableStoreWorkflow(workflow)
		orch.SetDCISearcher(searcher)
		listener := &distRecordingEventListener{}
		orch.SetEventListener(listener)
		req := defaultReq()
		req.UserMessage = `{"schema_version":"rencrow.research.request.v1","mode":"unsupported","objective":"RFC1149原文"}`

		_, err := orch.ProcessMessage(context.Background(), req)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "research") {
			t.Fatalf("ProcessMessage error = %v, want Research contract validation error", err)
		}
		if searcher.calls != 0 || mio.decideCalls != 0 || len(listener.events) != 0 || workflow.matcherCalls != 0 || workflow.handleCalls != 0 {
			t.Fatalf("invalid Research request caused distributed effects: DCI=%d MioDecide=%d matcher=%d handler=%d events=%#v", searcher.calls, mio.decideCalls, workflow.matcherCalls, workflow.handleCalls, listener.events)
		}
	})
}

func TestDCIHandlersBindCanonicalShiroTaskRunAndPreserveTrustedScope(t *testing.T) {
	t.Run("message orchestrator", func(t *testing.T) {
		manager := newRecordingTaskLifecycleManager()
		searcher := &contextRecordingDCISearcher{result: validDCISearchResult("RFC", "spec.md", "evidence", 1)}
		orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
		orch.SetTaskLifecycleManager(manager)
		orch.SetDCISearcher(searcher)
		req := defaultReq()
		assertMessageDCIExecutionIdentity(t, context.Background(), manager, searcher, func(ctx context.Context) (ProcessMessageResponse, error) {
			return orch.ProcessMessage(ctx, req)
		})
	})

	t.Run("distributed orchestrator", func(t *testing.T) {
		manager := newRecordingTaskLifecycleManager()
		searcher := &contextRecordingDCISearcher{result: validDCISearchResult("RFC", "spec.md", "evidence", 1)}
		router := transport.NewMessageRouter()
		defer router.Stop()
		orch := NewDistributedOrchestrator(&distMockSessionRepo{}, &distMockMioAgent{}, router, session.NewCentralMemory(), nil)
		orch.SetTaskLifecycleManager(manager)
		orch.SetDCISearcher(searcher)
		req := defaultReq()
		assertMessageDCIExecutionIdentity(t, context.Background(), manager, searcher, func(ctx context.Context) (ProcessMessageResponse, error) {
			return orch.ProcessMessage(ctx, req)
		})
	})
}

func TestDCIHandlersRejectUnresolvableActiveRunBeforeSearch(t *testing.T) {
	cases := []struct {
		name    string
		project func([]domaintask.Run) []domaintask.Run
	}{
		{name: "missing run", project: func([]domaintask.Run) []domaintask.Run { return nil }},
		{name: "multiple active runs", project: func(runs []domaintask.Run) []domaintask.Run { return append(runs, runs[0]) }},
		{name: "mismatched run actor", project: func(runs []domaintask.Run) []domaintask.Run { runs[0].Assignee = taskLifecycleMio; return runs }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			manager := newRecordingTaskLifecycleManager()
			projected := &dciRunProjectionManager{TaskLifecycleManager: manager, project: test.project, projectAfter: 2}
			searcher := &contextRecordingDCISearcher{result: validDCISearchResult("RFC", "spec.md", "evidence", 1)}
			orch := NewMessageOrchestrator(newMockSessionRepository(), &mockMioAgent{}, &mockShiroAgent{}, nil, nil, nil, nil, nil)
			orch.SetTaskLifecycleManager(projected)
			orch.SetDCISearcher(searcher)

			_, err := orch.ProcessMessage(context.Background(), defaultReq())
			if err == nil {
				t.Fatal("ProcessMessage unexpectedly accepted an invalid active Run projection")
			}
			if searcher.calls != 0 {
				t.Fatalf("DCI Search calls = %d, want 0 before active Run identity is validated", searcher.calls)
			}
		})
	}
}

func TestExplicitDCIKeywordAndSlashPriorityRemainUnchanged(t *testing.T) {
	searcher := &mockDCISearcher{trigger: true}
	if !shouldHandleExplicitDCI(searcher, "仕様書の原文を探して") {
		t.Fatal("ordinary DCI-triggered text no longer selects DCI")
	}
	if shouldHandleExplicitDCI(searcher, "/research 仕様書の原文を探して") {
		t.Fatal("slash command no longer has priority over DCI")
	}
}

type contextRecordingDCISearcher struct {
	result      domaindci.SearchResult
	calls       int
	identity    domainexecution.Identity
	identityErr error
	scope       domaintool.ToolExecutionScope
	scopeFound  bool
}

func (s *contextRecordingDCISearcher) ShouldTrigger(string) bool { return true }

func (s *contextRecordingDCISearcher) Search(ctx context.Context, _ string) (domaindci.SearchResult, error) {
	s.calls++
	s.identity, s.identityErr = domainexecution.IdentityFromContext(ctx)
	s.scope, s.scopeFound = domaintool.ToolExecutionScopeFromContext(ctx)
	return s.result, nil
}

type dciRunProjectionManager struct {
	TaskLifecycleManager
	project      func([]domaintask.Run) []domaintask.Run
	projectAfter int
	readCalls    int
}

func (m *dciRunProjectionManager) ReadTaskRuns(ctx context.Context, taskIDs []modulecore.TaskID) (map[modulecore.TaskID]domaintask.Task, map[modulecore.TaskID][]domaintask.Run, error) {
	m.readCalls++
	tasks, runs, err := m.TaskLifecycleManager.ReadTaskRuns(ctx, taskIDs)
	if err != nil {
		return nil, nil, err
	}
	if m.project != nil && m.readCalls >= m.projectAfter {
		for taskID, taskRuns := range runs {
			runs[taskID] = m.project(taskRuns)
		}
	}
	return tasks, runs, nil
}

func assertMessageDCIExecutionIdentity(
	t *testing.T,
	ctx context.Context,
	manager *recordingTaskLifecycleManager,
	searcher *contextRecordingDCISearcher,
	process func(context.Context) (ProcessMessageResponse, error),
) {
	t.Helper()
	trustedScope, err := domaintool.NewToolExecutionScope(
		"dci-parent-request", domaintool.ActorKindAgent, "mio", "",
		[]string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx = domaintool.WithToolExecutionScope(ctx, trustedScope)
	resp, err := process(ctx)
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if searcher.calls != 1 || searcher.identityErr != nil {
		t.Fatalf("DCI identity = %#v err=%v calls=%d", searcher.identity, searcher.identityErr, searcher.calls)
	}
	if !searcher.scopeFound || searcher.scope.RequestID != trustedScope.RequestID || searcher.scope.ActorKind != trustedScope.ActorKind ||
		searcher.scope.ActorID != trustedScope.ActorID || searcher.scope.AuthenticatedUserID != trustedScope.AuthenticatedUserID ||
		searcher.scope.AuthenticationSource != trustedScope.AuthenticationSource || len(searcher.scope.AllowedDataScopes) != len(trustedScope.AllowedDataScopes) {
		t.Fatalf("DCI handler did not preserve trusted parent scope: got=%#v found=%v want=%#v", searcher.scope, searcher.scopeFound, trustedScope)
	}
	if len(searcher.scope.AllowedDataScopes) == len(trustedScope.AllowedDataScopes) && searcher.scope.AllowedDataScopes[0] != trustedScope.AllowedDataScopes[0] {
		t.Fatalf("DCI handler changed trusted parent scopes: got=%#v want=%#v", searcher.scope.AllowedDataScopes, trustedScope.AllowedDataScopes)
	}
	taskID := modulecore.TaskID(resp.TaskID)
	task, found := manager.tasks[taskID]
	if !found || task.TaskID != taskID || task.ParentTaskID != modulecore.TaskID(resp.RootTaskID) || task.OwnerID != taskLifecycleMio ||
		task.Assignee != taskLifecycleShiro || task.Status != domaintask.StatusSucceeded {
		t.Fatalf("DCI execution Task = %#v found=%v response=%#v", task, found, resp)
	}
	if searcher.identity.TaskID != taskID || searcher.identity.TraceID != modulecore.TraceID(resp.TraceID) {
		t.Fatalf("DCI execution identity = %#v, want Task %s and original trace %s", searcher.identity, taskID, resp.TraceID)
	}
	run, ok := manager.runs[searcher.identity.RunID]
	if !ok || run.TaskID != taskID || run.Assignee != taskLifecycleShiro || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("DCI active Run projection = %#v found=%v, want succeeded Shiro Run for Task %s", run, ok, taskID)
	}
}
